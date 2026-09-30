package sqs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The AWS SQS API: awsJson 1.0 (current SDKs and the AWS CLI) and awsQuery
// (older SDKs), over the same operations. Queue URLs are
// <endpoint the client used>/<account>/<queue>.

const queryNS = "http://queue.amazonaws.com/doc/2012-11-05/"

// RegisterAWS serves SQS over the AWS protocols.
func (s *Service) RegisterAWS() {
	ops := map[string]func(q *awsapi.Req) (any, error){
		"CreateQueue":                  s.awsCreateQueue,
		"GetQueueUrl":                  s.awsGetQueueURL,
		"ListQueues":                   s.awsListQueues,
		"DeleteQueue":                  s.awsDeleteQueue,
		"GetQueueAttributes":           s.awsGetQueueAttributes,
		"SetQueueAttributes":           s.awsSetQueueAttributes,
		"SendMessage":                  s.awsSendMessage,
		"SendMessageBatch":             s.awsSendMessageBatch,
		"ReceiveMessage":               s.awsReceiveMessage,
		"DeleteMessage":                s.awsDeleteMessage,
		"DeleteMessageBatch":           s.awsDeleteMessageBatch,
		"ChangeMessageVisibility":      s.awsChangeMessageVisibility,
		"ChangeMessageVisibilityBatch": s.awsChangeMessageVisibilityBatch,
		"PurgeQueue":                   s.awsPurgeQueue,
		"TagQueue":                     s.awsTagQueue,
		"UntagQueue":                   s.awsUntagQueue,
		"ListQueueTags":                s.awsListQueueTags,
		"ListDeadLetterSourceQueues":   s.awsListDeadLetterSourceQueues,
		"AddPermission":                s.awsAddPermission,
		"RemovePermission":             s.awsRemovePermission,
		"StartMessageMoveTask":         s.awsStartMessageMoveTask,
		"CancelMessageMoveTask":        s.awsCancelMessageMoveTask,
		"ListMessageMoveTasks":         s.awsListMessageMoveTasks,
	}
	svc := &awsapi.Service{Name: "sqs", JSONPrefix: "AmazonSQS", JSONVersion: "1.0", XMLNS: queryNS, Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = wrap(fn)
	}
	awsapi.Register(svc)
}

// wrap converts errors to SQS error codes and renders awsQuery results.
func wrap(fn func(q *awsapi.Req) (any, error)) awsapi.Op {
	return func(q *awsapi.Req) (any, error) {
		out, err := fn(q)
		if err != nil {
			return nil, sqsError(err)
		}
		if q.Protocol == awsapi.Query {
			if out == nil {
				return awsapi.NoResult{}, nil
			}
			return toXML(reflect.ValueOf(out)), nil
		}
		return out, nil
	}
}

// queryCodes are the legacy awsQuery codes (x-amzn-query-error) of SQS errors
// whose awsJson name differs.
var queryCodes = map[string]string{
	"QueueDoesNotExist":            "AWS.SimpleQueueService.NonExistentQueue",
	"QueueNameExists":              "QueueAlreadyExists",
	"MessageNotInflight":           "AWS.SimpleQueueService.MessageNotInflight",
	"PurgeQueueInProgress":         "AWS.SimpleQueueService.PurgeQueueInProgress",
	"EmptyBatchRequest":            "AWS.SimpleQueueService.EmptyBatchRequest",
	"TooManyEntriesInBatchRequest": "AWS.SimpleQueueService.TooManyEntriesInBatchRequest",
	"BatchEntryIdsNotDistinct":     "AWS.SimpleQueueService.BatchEntryIdsNotDistinct",
	"InvalidBatchEntryId":          "AWS.SimpleQueueService.InvalidBatchEntryId",
	"BatchRequestTooLong":          "AWS.SimpleQueueService.BatchRequestTooLong",
	"UnsupportedOperation":         "AWS.SimpleQueueService.UnsupportedOperation",
	"QueueDeletedRecently":         "AWS.SimpleQueueService.QueueDeletedRecently",
	"AccessDeniedException":        "AccessDenied",
}

func sqsCode(code string) string {
	switch code {
	case "ValidationError", "BadRequest", "ResourceConflict", "Conflict":
		return "InvalidParameterValue"
	case "ResourceNotFound":
		return "QueueDoesNotExist"
	case "QueueAlreadyExists":
		return "QueueNameExists"
	}
	return code
}

func sqsError(err error) error {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		if ae.QueryCode != "" {
			return ae
		}
		e := *ae
		e.QueryCode = e.Code
		if qc := queryCodes[e.Code]; qc != "" {
			e.QueryCode = qc
		}
		return &e
	}
	var ce *core.Error
	if errors.As(err, &ce) {
		code := sqsCode(ce.Code)
		status := http.StatusBadRequest
		if ce.Status == http.StatusForbidden || ce.Status >= 500 {
			status = ce.Status
		}
		qc := code
		if c := queryCodes[code]; c != "" {
			qc = c
		}
		return &awsapi.Error{Status: status, Code: code, QueryCode: qc, Message: ce.Message}
	}
	return err
}

func awsErr(code, format string, a ...any) error {
	return &awsapi.Error{Status: http.StatusBadRequest, Code: code, Message: fmt.Sprintf(format, a...)}
}

// bind decodes the request (awsJson body or awsQuery parameters) into v.
func bind(q *awsapi.Req, v any) error {
	if q.Protocol == awsapi.JSON {
		return q.Bind(v)
	}
	return decodeForm(q.Form, "", reflect.ValueOf(v).Elem())
}

// queueURL is the AWS queue URL for name, on the endpoint the client used.
func queueURL(q *awsapi.Req, name string) string {
	scheme := "http"
	if q.R.TLS != nil {
		scheme = "https"
	}
	if p := q.R.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	return scheme + "://" + q.R.Host + "/" + q.Account + "/" + name
}

var accountRe = regexp.MustCompile(`^\d{12}$`)

// queueName resolves a QueueUrl (or, for awsQuery requests sent to the queue
// URL itself, the request path) to a queue name.
func queueName(q *awsapi.Req, raw string) (string, error) {
	if raw == "" && q.Protocol == awsapi.Query {
		raw = q.R.URL.Path
	}
	path := raw
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		path = u.Path
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	name := segs[len(segs)-1]
	if name == "" {
		return "", awsErr("MissingParameter", "The request must contain the parameter QueueUrl.")
	}
	if len(segs) >= 2 && accountRe.MatchString(segs[len(segs)-2]) && segs[len(segs)-2] != q.Account {
		return "", errNoQueue(name)
	}
	return name, nil
}

// queue resolves QueueUrl, authorizes action on the queue and loads it.
func (s *Service) queue(q *awsapi.Req, rawURL, action string) (Queue, error) {
	name, err := queueName(q, rawURL)
	if err != nil {
		return Queue{}, err
	}
	if err := q.Authorize(action, q.ARN("sqs", name)); err != nil {
		return Queue{}, err
	}
	return s.getQueue(name)
}

// ---- attributes ----

func itoa(n int) string { return strconv.Itoa(n) }

type redrivePolicy struct {
	DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	MaxReceiveCount     int    `json:"maxReceiveCount"`
}

// awsAttributes returns every queue attribute in AWS form.
func (s *Service) awsAttributes(q Queue) map[string]string {
	vis, inf, del := s.counts(q.Name)
	m := map[string]string{
		"QueueArn":                              q.ARN,
		"ApproximateNumberOfMessages":           itoa(vis),
		"ApproximateNumberOfMessagesNotVisible": itoa(inf),
		"ApproximateNumberOfMessagesDelayed":    itoa(del),
		"CreatedTimestamp":                      strconv.FormatInt(q.CreatedAt.Unix(), 10),
		"LastModifiedTimestamp":                 strconv.FormatInt(q.LastModified.Unix(), 10),
		"VisibilityTimeout":                     itoa(q.VisibilityTimeout),
		"MaximumMessageSize":                    itoa(q.MaxMessageSize),
		"MessageRetentionPeriod":                itoa(q.MessageRetention),
		"DelaySeconds":                          itoa(q.DelaySeconds),
		"ReceiveMessageWaitTimeSeconds":         itoa(q.ReceiveWaitTime),
	}
	sse := q.KmsMasterKeyID == ""
	if q.SqsManagedSSE != nil {
		sse = *q.SqsManagedSSE
	}
	m["SqsManagedSseEnabled"] = strconv.FormatBool(sse)
	if q.KmsMasterKeyID != "" {
		m["KmsMasterKeyId"] = q.KmsMasterKeyID
		m["KmsDataKeyReusePeriodSeconds"] = itoa(q.KmsDataKeyReusePeriod)
	}
	if q.Policy != "" {
		m["Policy"] = q.Policy
	}
	if q.Redrive != nil {
		b, _ := json.Marshal(redrivePolicy{DeadLetterTargetArn: s.env.ARN("sqs", q.Redrive.DeadLetterQueue), MaxReceiveCount: q.Redrive.MaxReceiveCount})
		m["RedrivePolicy"] = string(b)
	}
	if q.RedriveAllowPolicy != "" {
		m["RedriveAllowPolicy"] = q.RedriveAllowPolicy
	}
	if q.FIFO {
		m["FifoQueue"] = "true"
		m["ContentBasedDeduplication"] = strconv.FormatBool(q.ContentBasedDeduplication)
		m["DeduplicationScope"] = orDefault(q.DeduplicationScope, "queue")
		m["FifoThroughputLimit"] = orDefault(q.FifoThroughputLimit, "perQueue")
	}
	return m
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

var queueAttributeNames = map[string]bool{
	"All": true, "Policy": true, "VisibilityTimeout": true, "MaximumMessageSize": true, "MessageRetentionPeriod": true,
	"ApproximateNumberOfMessages": true, "ApproximateNumberOfMessagesNotVisible": true, "CreatedTimestamp": true,
	"LastModifiedTimestamp": true, "QueueArn": true, "ApproximateNumberOfMessagesDelayed": true, "DelaySeconds": true,
	"ReceiveMessageWaitTimeSeconds": true, "RedrivePolicy": true, "FifoQueue": true, "ContentBasedDeduplication": true,
	"KmsMasterKeyId": true, "KmsDataKeyReusePeriodSeconds": true, "DeduplicationScope": true, "FifoThroughputLimit": true,
	"RedriveAllowPolicy": true, "SqsManagedSseEnabled": true,
}

func unknownAttr(name string) error {
	return awsErr("InvalidAttributeName", "Unknown Attribute %s.", name)
}

func badAttr(name, value string) error {
	return awsErr("InvalidAttributeValue", "Invalid value for the parameter %s.", name)
}

// parseAttrs converts AWS queue attributes to an attrsInput. fifo reports FifoQueue.
func (s *Service) parseAttrs(q *awsapi.Req, attrs map[string]string, creating bool) (in attrsInput, fifo bool, err error) {
	num := func(name, v string) (*int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, badAttr(name, v)
		}
		return &n, nil
	}
	boolean := func(name, v string) (*bool, error) {
		switch strings.ToLower(v) {
		case "true":
			t := true
			return &t, nil
		case "false":
			f := false
			return &f, nil
		}
		return nil, badAttr(name, v)
	}
	str := func(v string) *string { return &v }
	for name, v := range attrs {
		switch name {
		case "VisibilityTimeout":
			in.VisibilityTimeout, err = num(name, v)
		case "MessageRetentionPeriod":
			in.MessageRetention, err = num(name, v)
		case "DelaySeconds":
			in.DelaySeconds, err = num(name, v)
		case "ReceiveMessageWaitTimeSeconds":
			in.ReceiveWaitTime, err = num(name, v)
		case "MaximumMessageSize":
			in.MaxMessageSize, err = num(name, v)
		case "KmsDataKeyReusePeriodSeconds":
			in.KmsDataKeyReusePeriod, err = num(name, v)
		case "ContentBasedDeduplication":
			in.ContentBasedDeduplication, err = boolean(name, v)
		case "SqsManagedSseEnabled":
			in.SqsManagedSSE, err = boolean(name, v)
		case "FifoQueue":
			if !creating {
				return in, false, unknownAttr(name)
			}
			var b *bool
			if b, err = boolean(name, v); err == nil {
				fifo = *b
			}
		case "DeduplicationScope":
			in.DeduplicationScope = str(v)
		case "FifoThroughputLimit":
			in.FifoThroughputLimit = str(v)
		case "KmsMasterKeyId":
			in.KmsMasterKeyID = str(v)
		case "Policy":
			in.Policy = str(v)
		case "RedriveAllowPolicy":
			in.RedriveAllowPolicy = str(v)
		case "RedrivePolicy":
			if strings.TrimSpace(v) == "" {
				in.Redrive = &Redrive{}
				continue
			}
			var rp struct {
				DeadLetterTargetArn string          `json:"deadLetterTargetArn"`
				MaxReceiveCount     json.RawMessage `json:"maxReceiveCount"`
			}
			if json.Unmarshal([]byte(v), &rp) != nil || rp.DeadLetterTargetArn == "" {
				return in, false, awsErr("InvalidAttributeValue", "Value %s for parameter RedrivePolicy is invalid. Reason: Redrive policy is not a valid JSON map.", v)
			}
			n, cerr := strconv.Atoi(strings.Trim(string(rp.MaxReceiveCount), `"`))
			if cerr != nil {
				return in, false, awsErr("InvalidAttributeValue", "Value %s for parameter RedrivePolicy is invalid. Reason: Invalid value for maxReceiveCount.", v)
			}
			arn := core.CanonicalARN(rp.DeadLetterTargetArn)
			if !strings.HasPrefix(arn, "arn:"+core.Partition+":sqs:") || !strings.Contains(arn, ":"+q.Account+":") {
				return in, false, awsErr("InvalidAttributeValue", "Value %s for parameter RedrivePolicy is invalid. Reason: Dead letter target does not exist.", v)
			}
			in.Redrive = &Redrive{DeadLetterQueue: NameFromARN(arn), MaxReceiveCount: n}
		default:
			return in, false, unknownAttr(name)
		}
		if err != nil {
			return in, false, err
		}
	}
	return in, fifo, nil
}

// ---- queue operations ----

type attrValue struct {
	StringValue      string   `json:"StringValue,omitempty"`
	BinaryValue      []byte   `json:"BinaryValue,omitempty"`
	StringListValues []string `json:"StringListValues,omitempty" query:"StringListValue"`
	BinaryListValues [][]byte `json:"BinaryListValues,omitempty" query:"BinaryListValue"`
	DataType         string   `json:"DataType"`
}

func fromAttrValues(in map[string]attrValue) map[string]MessageAttribute {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]MessageAttribute, len(in))
	for k, v := range in {
		out[k] = MessageAttribute{DataType: v.DataType, StringValue: v.StringValue, BinaryValue: v.BinaryValue,
			StringListValues: v.StringListValues, BinaryListValues: v.BinaryListValues}
	}
	return out
}

func toAttrValues(in map[string]MessageAttribute) map[string]attrValue {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]attrValue, len(in))
	for k, v := range in {
		av := attrValue{DataType: v.DataType, StringListValues: v.StringListValues, BinaryListValues: v.BinaryListValues}
		if v.binary() {
			av.BinaryValue = v.BinaryValue
		} else {
			av.StringValue = v.StringValue
		}
		out[k] = av
	}
	return out
}

type queueURLOut struct {
	QueueUrl string
}

func (s *Service) awsCreateQueue(q *awsapi.Req) (any, error) {
	var in struct {
		QueueName  string
		Attributes map[string]string `query:"Attribute"`
		Tags       map[string]string `json:"tags" query:"Tag,Key,Value"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	if in.QueueName == "" {
		return nil, awsErr("MissingParameter", "The request must contain the parameter QueueName.")
	}
	if err := q.Authorize("sqs:CreateQueue", q.ARN("sqs", in.QueueName)); err != nil {
		return nil, err
	}
	attrs, fifo, err := s.parseAttrs(q, in.Attributes, true)
	if err != nil {
		return nil, err
	}
	if len(in.Tags) > 0 {
		if err := q.Authorize("sqs:TagQueue", q.ARN("sqs", in.QueueName)); err != nil {
			return nil, err
		}
		attrs.Tags = in.Tags
	}
	if _, _, err := s.createQueue(q.Authorize, in.QueueName, fifo, attrs); err != nil {
		return nil, err
	}
	return queueURLOut{QueueUrl: queueURL(q, in.QueueName)}, nil
}

func (s *Service) awsGetQueueURL(q *awsapi.Req) (any, error) {
	var in struct {
		QueueName              string
		QueueOwnerAWSAccountId string
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	if in.QueueName == "" {
		return nil, awsErr("MissingParameter", "The request must contain the parameter QueueName.")
	}
	if err := q.Authorize("sqs:GetQueueUrl", q.ARN("sqs", in.QueueName)); err != nil {
		return nil, err
	}
	if in.QueueOwnerAWSAccountId != "" && in.QueueOwnerAWSAccountId != q.Account {
		return nil, errNoQueue(in.QueueName)
	}
	if _, err := s.getQueue(in.QueueName); err != nil {
		return nil, err
	}
	return queueURLOut{QueueUrl: queueURL(q, in.QueueName)}, nil
}

// page returns names after the NextToken, up to max (0 = 1000), and the next token.
func page(names []string, token string, max *int) ([]string, string, error) {
	limit := 1000
	if max != nil {
		if *max < 1 || *max > 1000 {
			return nil, "", awsErr("InvalidParameterValue", "Value %d for parameter MaxResults is invalid. Reason: must be between 1 and 1000.", *max)
		}
		limit = *max
	}
	start := 0
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return nil, "", awsErr("InvalidParameterValue", "Invalid NextToken value.")
		}
		start = sort.SearchStrings(names, string(b)+"\x00")
	}
	end := min(start+limit, len(names))
	out := names[start:end]
	next := ""
	if max != nil && end < len(names) {
		next = base64.RawURLEncoding.EncodeToString([]byte(names[end-1]))
	}
	return out, next, nil
}

func (s *Service) awsListQueues(q *awsapi.Req) (any, error) {
	var in struct {
		QueueNamePrefix string
		NextToken       string
		MaxResults      *int
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	if err := q.Authorize("sqs:ListQueues", "*"); err != nil {
		return nil, err
	}
	var names []string
	for _, qu := range store.List[Queue](s.env.Store, cQueues) {
		if strings.HasPrefix(qu.Name, in.QueueNamePrefix) {
			names = append(names, qu.Name)
		}
	}
	sort.Strings(names)
	sel, next, err := page(names, in.NextToken, in.MaxResults)
	if err != nil {
		return nil, err
	}
	var out struct {
		QueueUrls []string `json:",omitempty" query:"QueueUrl"`
		NextToken string   `json:",omitempty"`
	}
	for _, n := range sel {
		out.QueueUrls = append(out.QueueUrls, queueURL(q, n))
	}
	out.NextToken = next
	return out, nil
}

type queueIn struct {
	QueueUrl string
}

func (s *Service) awsDeleteQueue(q *awsapi.Req) (any, error) {
	var in queueIn
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:DeleteQueue")
	if err != nil {
		return nil, err
	}
	return nil, s.deleteQueue(qu.Name)
}

func (s *Service) awsGetQueueAttributes(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl       string
		AttributeNames []string `query:"AttributeName"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:GetQueueAttributes")
	if err != nil {
		return nil, err
	}
	all := s.awsAttributes(qu)
	var out struct {
		Attributes map[string]string `json:",omitempty" query:"Attribute"`
	}
	for _, n := range in.AttributeNames {
		if !queueAttributeNames[n] {
			return nil, unknownAttr(n)
		}
		if n == "All" {
			out.Attributes = all
			break
		}
		if v, ok := all[n]; ok {
			if out.Attributes == nil {
				out.Attributes = map[string]string{}
			}
			out.Attributes[n] = v
		}
	}
	return out, nil
}

func (s *Service) awsSetQueueAttributes(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl   string
		Attributes map[string]string `query:"Attribute"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:SetQueueAttributes")
	if err != nil {
		return nil, err
	}
	attrs, _, err := s.parseAttrs(q, in.Attributes, false)
	if err != nil {
		return nil, err
	}
	_, err = s.setQueueAttributes(q.Authorize, qu.Name, attrs)
	return nil, err
}

func (s *Service) awsPurgeQueue(q *awsapi.Req) (any, error) {
	var in queueIn
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:PurgeQueue")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	recent := false
	if st := s.queues[qu.Name]; st != nil && !st.lastPurge.IsZero() && time.Since(st.lastPurge) < time.Minute {
		recent = true
	}
	s.mu.Unlock()
	if recent {
		return nil, awsErr("PurgeQueueInProgress", "Only one PurgeQueue operation on %s is allowed every 60 seconds.", qu.Name)
	}
	return nil, s.Purge(qu.Name)
}

// ---- tags, permissions, dead-letter queues ----

func (s *Service) awsTagQueue(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl string
		Tags     map[string]string `query:"Tag,Key,Value"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:TagQueue")
	if err != nil {
		return nil, err
	}
	_, err = store.Update(s.env.Store, cQueues, qu.Name, func(x *Queue) error {
		if x.Tags == nil {
			x.Tags = core.Tags{}
		}
		for k, v := range in.Tags {
			x.Tags[k] = v
		}
		if len(x.Tags) > 50 {
			return awsErr("InvalidParameterValue", "Too many tags: a queue can have at most 50.")
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsUntagQueue(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl string
		TagKeys  []string `query:"TagKey"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:UntagQueue")
	if err != nil {
		return nil, err
	}
	_, err = store.Update(s.env.Store, cQueues, qu.Name, func(x *Queue) error {
		for _, k := range in.TagKeys {
			delete(x.Tags, k)
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsListQueueTags(q *awsapi.Req) (any, error) {
	var in queueIn
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:ListQueueTags")
	if err != nil {
		return nil, err
	}
	var out struct {
		Tags map[string]string `json:",omitempty" query:"Tag,Key,Value"`
	}
	if len(qu.Tags) > 0 {
		out.Tags = qu.Tags
	}
	return out, nil
}

func (s *Service) awsListDeadLetterSourceQueues(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl   string
		NextToken  string
		MaxResults *int
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:ListDeadLetterSourceQueues")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, o := range s.deadLetterSources(qu.Name) {
		names = append(names, o.Name)
	}
	sort.Strings(names)
	sel, next, err := page(names, in.NextToken, in.MaxResults)
	if err != nil {
		return nil, err
	}
	out := struct {
		QueueUrls []string `json:"queueUrls" query:"QueueUrl"`
		NextToken string   `json:",omitempty"`
	}{QueueUrls: []string{}, NextToken: next}
	for _, n := range sel {
		out.QueueUrls = append(out.QueueUrls, queueURL(q, n))
	}
	return out, nil
}

type policyDoc struct {
	Version   string           `json:"Version"`
	ID        string           `json:"Id,omitempty"`
	Statement []map[string]any `json:"Statement"`
}

func (s *Service) awsAddPermission(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl      string
		Label         string
		AWSAccountIds []string `query:"AWSAccountId"`
		Actions       []string `query:"ActionName"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:AddPermission")
	if err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`).MatchString(in.Label) || len(in.AWSAccountIds) == 0 || len(in.Actions) == 0 {
		return nil, awsErr("InvalidParameterValue", "Label, AWSAccountIds and Actions are required.")
	}
	doc := policyDoc{Version: "2012-10-17", ID: qu.ARN + "/SQSDefaultPolicy"}
	if qu.Policy != "" {
		_ = json.Unmarshal([]byte(qu.Policy), &doc)
	}
	for _, st := range doc.Statement {
		if st["Sid"] == in.Label {
			return nil, awsErr("InvalidParameterValue", "Value %s for parameter Label is invalid. Reason: Already exists.", in.Label)
		}
	}
	principals := make([]string, len(in.AWSAccountIds))
	for i, a := range in.AWSAccountIds {
		principals[i] = "arn:" + core.Partition + ":iam::" + a + ":root"
	}
	actions := make([]string, len(in.Actions))
	for i, a := range in.Actions {
		actions[i] = "SQS:" + a
	}
	doc.Statement = append(doc.Statement, map[string]any{"Sid": in.Label, "Effect": "Allow",
		"Principal": map[string]any{"AWS": principals}, "Action": actions, "Resource": qu.ARN})
	b, _ := json.Marshal(doc)
	p := string(b)
	_, err = s.setQueueAttributes(q.Authorize, qu.Name, attrsInput{Policy: &p})
	return nil, err
}

func (s *Service) awsRemovePermission(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl string
		Label    string
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:RemovePermission")
	if err != nil {
		return nil, err
	}
	var doc policyDoc
	_ = json.Unmarshal([]byte(qu.Policy), &doc)
	kept := doc.Statement[:0]
	for _, st := range doc.Statement {
		if st["Sid"] != in.Label {
			kept = append(kept, st)
		}
	}
	if len(kept) == len(doc.Statement) {
		return nil, awsErr("InvalidParameterValue", "Value %s for parameter Label is invalid. Reason: can't find label.", in.Label)
	}
	p := ""
	if len(kept) > 0 {
		doc.Statement = kept
		b, _ := json.Marshal(doc)
		p = string(b)
	}
	_, err = s.setQueueAttributes(q.Authorize, qu.Name, attrsInput{Policy: &p})
	return nil, err
}

// ---- messages ----

type sendEntry struct {
	Id                      string
	MessageBody             string
	DelaySeconds            *int
	MessageAttributes       map[string]attrValue `query:"MessageAttribute"`
	MessageSystemAttributes map[string]attrValue `query:"MessageSystemAttribute"`
	MessageDeduplicationId  string
	MessageGroupId          string
}

func (e sendEntry) input(sender string) SendInput {
	return SendInput{Body: e.MessageBody, DelaySeconds: e.DelaySeconds, MessageAttributes: fromAttrValues(e.MessageAttributes),
		SystemAttributes: fromAttrValues(e.MessageSystemAttributes), GroupID: e.MessageGroupId, DedupID: e.MessageDeduplicationId, SenderID: sender}
}

type sendOut struct {
	MD5OfMessageBody             string
	MD5OfMessageAttributes       string `json:",omitempty"`
	MD5OfMessageSystemAttributes string `json:",omitempty"`
	MessageId                    string
	SequenceNumber               string `json:",omitempty"`
}

func (s *Service) awsSendMessage(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl string
		sendEntry
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:SendMessage")
	if err != nil {
		return nil, err
	}
	r, err := s.Send(qu.Name, in.input(principalID(q.P)))
	if err != nil {
		return nil, err
	}
	return sendOut{MD5OfMessageBody: r.MD5OfBody, MD5OfMessageAttributes: r.MD5OfAttrs, MD5OfMessageSystemAttributes: r.MD5OfSysAttrs,
		MessageId: r.MessageID, SequenceNumber: r.SequenceNumber}, nil
}

var batchIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func checkBatch(ids []string) error {
	if len(ids) == 0 {
		return awsErr("EmptyBatchRequest", "There should be at least one entry in the request.")
	}
	if len(ids) > 10 {
		return awsErr("TooManyEntriesInBatchRequest", "Maximum number of entries per request are 10. You have sent %d.", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !batchIDRe.MatchString(id) {
			return awsErr("InvalidBatchEntryId", "A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long.")
		}
		if seen[id] {
			return awsErr("BatchEntryIdsNotDistinct", "Id %s repeated.", id)
		}
		seen[id] = true
	}
	return nil
}

type batchError struct {
	Id          string
	SenderFault bool
	Code        string
	Message     string `json:",omitempty"`
}

func entryError(id string, err error) batchError {
	e := sqsError(err)
	var ae *awsapi.Error
	if errors.As(e, &ae) {
		return batchError{Id: id, SenderFault: ae.Status < 500, Code: ae.Code, Message: ae.Message}
	}
	log.Printf("sqs: batch entry %s: %v", id, err)
	return batchError{Id: id, Code: "InternalError", Message: core.InternalErrorMessage}
}

func isNoQueue(err error) bool {
	var ce *core.Error
	return errors.As(err, &ce) && ce.Code == "QueueDoesNotExist"
}

func (s *Service) awsSendMessageBatch(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl string
		Entries  []sendEntry `query:"SendMessageBatchRequestEntry"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:SendMessage")
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(in.Entries))
	total := 0
	for i, e := range in.Entries {
		ids[i] = e.Id
		total += len(e.MessageBody)
		for k, a := range e.MessageAttributes {
			total += len(k) + len(a.DataType) + len(a.StringValue) + len(a.BinaryValue)
		}
	}
	if err := checkBatch(ids); err != nil {
		return nil, err
	}
	if total > qu.MaxMessageSize {
		return nil, awsErr("BatchRequestTooLong", "Batch requests cannot be longer than %d bytes. You have sent %d bytes.", qu.MaxMessageSize, total)
	}
	type okEntry struct {
		Id string
		sendOut
	}
	out := struct {
		Successful []okEntry    `query:"SendMessageBatchResultEntry"`
		Failed     []batchError `query:"BatchResultErrorEntry"`
	}{Successful: []okEntry{}, Failed: []batchError{}}
	sender := principalID(q.P)
	for _, e := range in.Entries {
		r, err := s.Send(qu.Name, e.input(sender))
		if err != nil {
			if isNoQueue(err) {
				return nil, err
			}
			out.Failed = append(out.Failed, entryError(e.Id, err))
			continue
		}
		out.Successful = append(out.Successful, okEntry{Id: e.Id, sendOut: sendOut{MD5OfMessageBody: r.MD5OfBody, MD5OfMessageAttributes: r.MD5OfAttrs,
			MD5OfMessageSystemAttributes: r.MD5OfSysAttrs, MessageId: r.MessageID, SequenceNumber: r.SequenceNumber}})
	}
	return out, nil
}

type awsMessage struct {
	MessageId              string
	ReceiptHandle          string
	MD5OfBody              string
	Body                   string
	Attributes             map[string]string    `json:",omitempty" query:"Attribute"`
	MD5OfMessageAttributes string               `json:",omitempty"`
	MessageAttributes      map[string]attrValue `json:",omitempty" query:"MessageAttribute"`
}

// selectMessageAttributes applies MessageAttributeNames ("All", ".*", "prefix.*", exact names).
func selectMessageAttributes(all map[string]MessageAttribute, names []string) map[string]MessageAttribute {
	if len(all) == 0 || len(names) == 0 {
		return nil
	}
	out := map[string]MessageAttribute{}
	for _, n := range names {
		if n == "All" || n == ".*" {
			return all
		}
		if p, ok := strings.CutSuffix(n, ".*"); ok {
			for k, v := range all {
				if strings.HasPrefix(k, p+".") {
					out[k] = v
				}
			}
		} else if v, ok := all[n]; ok {
			out[n] = v
		}
	}
	return out
}

func (s *Service) awsReceiveMessage(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl                    string
		AttributeNames              []string `query:"AttributeName"`
		MessageSystemAttributeNames []string `query:"MessageSystemAttributeName"`
		MessageAttributeNames       []string `query:"MessageAttributeName"`
		MaxNumberOfMessages         int
		VisibilityTimeout           *int
		WaitTimeSeconds             *int
		ReceiveRequestAttemptId     string
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:ReceiveMessage")
	if err != nil {
		return nil, err
	}
	got, err := s.ReceiveWith(q.R.Context(), qu.Name, ReceiveOptions{Max: in.MaxNumberOfMessages,
		VisibilityTimeout: in.VisibilityTimeout, WaitSeconds: in.WaitTimeSeconds, AttemptID: in.ReceiveRequestAttemptId})
	if err != nil {
		return nil, err
	}
	sysNames := map[string]bool{}
	for _, n := range append(in.AttributeNames, in.MessageSystemAttributeNames...) {
		sysNames[n] = true
	}
	var out struct {
		Messages []awsMessage `json:",omitempty" query:"Message"`
	}
	for _, r := range got {
		m := awsMessage{MessageId: r.MessageID, ReceiptHandle: r.ReceiptHandle, MD5OfBody: r.MD5OfBody, Body: r.Body}
		for k, v := range r.Attributes {
			if sysNames["All"] || sysNames[k] {
				if m.Attributes == nil {
					m.Attributes = map[string]string{}
				}
				m.Attributes[k] = v
			}
		}
		if ma := selectMessageAttributes(r.MessageAttributes, in.MessageAttributeNames); len(ma) > 0 {
			m.MessageAttributes = toAttrValues(ma)
			m.MD5OfMessageAttributes = md5OfAttributes(ma)
		}
		out.Messages = append(out.Messages, m)
	}
	return out, nil
}

// validReceipt reports whether h looks like a receipt handle this server issued.
func validReceipt(h string) bool {
	if len(h) < 32 || len(h) > 1024 {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// deleteMessage deletes by receipt handle with AWS semantics: a well-formed but
// stale handle succeeds on standard queues (the message is not deleted).
func (s *Service) deleteMessage(qu Queue, receipt string) error {
	err := s.Delete(qu.Name, receipt)
	if errors.Is(err, errStaleReceipt) {
		if !validReceipt(receipt) {
			return awsErr("ReceiptHandleIsInvalid", "The input receipt handle \"%s\" is not a valid receipt handle.", receipt)
		}
		if !qu.FIFO {
			return nil
		}
	}
	return err
}

func (s *Service) awsDeleteMessage(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl      string
		ReceiptHandle string
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:DeleteMessage")
	if err != nil {
		return nil, err
	}
	return nil, s.deleteMessage(qu, in.ReceiptHandle)
}

type idEntry struct {
	Id string
}

func (s *Service) awsDeleteMessageBatch(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl string
		Entries  []struct {
			Id            string
			ReceiptHandle string
		} `query:"DeleteMessageBatchRequestEntry"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:DeleteMessage")
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(in.Entries))
	for i, e := range in.Entries {
		ids[i] = e.Id
	}
	if err := checkBatch(ids); err != nil {
		return nil, err
	}
	out := struct {
		Successful []idEntry    `query:"DeleteMessageBatchResultEntry"`
		Failed     []batchError `query:"BatchResultErrorEntry"`
	}{Successful: []idEntry{}, Failed: []batchError{}}
	for _, e := range in.Entries {
		if err := s.deleteMessage(qu, e.ReceiptHandle); err != nil {
			if isNoQueue(err) {
				return nil, err
			}
			out.Failed = append(out.Failed, entryError(e.Id, err))
		} else {
			out.Successful = append(out.Successful, idEntry{Id: e.Id})
		}
	}
	return out, nil
}

func (s *Service) changeVisibilityAWS(qu Queue, receipt string, seconds int) error {
	err := s.ChangeVisibility(qu.Name, receipt, seconds)
	if errors.Is(err, errStaleReceipt) && !validReceipt(receipt) {
		return awsErr("ReceiptHandleIsInvalid", "The input receipt handle \"%s\" is not a valid receipt handle.", receipt)
	}
	return err
}

func (s *Service) awsChangeMessageVisibility(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl          string
		ReceiptHandle     string
		VisibilityTimeout int
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:ChangeMessageVisibility")
	if err != nil {
		return nil, err
	}
	return nil, s.changeVisibilityAWS(qu, in.ReceiptHandle, in.VisibilityTimeout)
}

func (s *Service) awsChangeMessageVisibilityBatch(q *awsapi.Req) (any, error) {
	var in struct {
		QueueUrl string
		Entries  []struct {
			Id                string
			ReceiptHandle     string
			VisibilityTimeout int
		} `query:"ChangeMessageVisibilityBatchRequestEntry"`
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	qu, err := s.queue(q, in.QueueUrl, "sqs:ChangeMessageVisibility")
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(in.Entries))
	for i, e := range in.Entries {
		ids[i] = e.Id
	}
	if err := checkBatch(ids); err != nil {
		return nil, err
	}
	out := struct {
		Successful []idEntry    `query:"ChangeMessageVisibilityBatchResultEntry"`
		Failed     []batchError `query:"BatchResultErrorEntry"`
	}{Successful: []idEntry{}, Failed: []batchError{}}
	for _, e := range in.Entries {
		if err := s.changeVisibilityAWS(qu, e.ReceiptHandle, e.VisibilityTimeout); err != nil {
			if isNoQueue(err) {
				return nil, err
			}
			out.Failed = append(out.Failed, entryError(e.Id, err))
		} else {
			out.Successful = append(out.Successful, idEntry{Id: e.Id})
		}
	}
	return out, nil
}

// ---- message move tasks ----

// queueByARN authorizes action on a queue ARN in this account and loads it.
func (s *Service) queueByARN(q *awsapi.Req, arn, action string) (Queue, error) {
	arn = core.CanonicalARN(arn)
	parts := strings.Split(arn, ":")
	if len(parts) != 6 || parts[2] != "sqs" || parts[4] != q.Account {
		return Queue{}, awsErr("ResourceNotFoundException", "The resource that you specified for the ARN %s doesn't exist.", arn)
	}
	if err := q.Authorize(action, arn); err != nil {
		return Queue{}, err
	}
	qu, err := s.getQueue(parts[5])
	if err != nil {
		return Queue{}, awsErr("ResourceNotFoundException", "The resource that you specified for the ARN %s doesn't exist.", arn)
	}
	return qu, nil
}

func (s *Service) awsStartMessageMoveTask(q *awsapi.Req) (any, error) {
	var in struct {
		SourceArn                    string
		DestinationArn               string
		MaxNumberOfMessagesPerSecond *int
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	src, err := s.queueByARN(q, in.SourceArn, "sqs:StartMessageMoveTask")
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("sqs:ReceiveMessage", src.ARN); err != nil {
		return nil, err
	}
	if err := q.Authorize("sqs:DeleteMessage", src.ARN); err != nil {
		return nil, err
	}
	sources := s.deadLetterSources(src.Name)
	if len(sources) == 0 {
		return nil, awsErr("InvalidParameterValue", "Source queue must be configured as a Dead Letter Queue.")
	}
	dest := ""
	if in.DestinationArn != "" {
		d, err := s.queueByARN(q, in.DestinationArn, "sqs:SendMessage")
		if err != nil {
			return nil, err
		}
		dest = d.ARN
	} else {
		for _, o := range sources {
			if err := q.Authorize("sqs:SendMessage", o.ARN); err != nil {
				return nil, err
			}
		}
	}
	rate := 0
	if in.MaxNumberOfMessagesPerSecond != nil {
		rate = *in.MaxNumberOfMessagesPerSecond
		if rate < 1 || rate > 500 {
			return nil, awsErr("InvalidParameterValue", "MaxNumberOfMessagesPerSecond must be between 1 and 500.")
		}
	}
	t, err := s.startMove(src.ARN, dest, rate)
	if err != nil {
		return nil, err
	}
	return struct{ TaskHandle string }{t.Handle}, nil
}

func (s *Service) awsCancelMessageMoveTask(q *awsapi.Req) (any, error) {
	var in struct{ TaskHandle string }
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	var source string
	if b, err := base64.StdEncoding.DecodeString(in.TaskHandle); err == nil {
		var h struct {
			SourceArn string `json:"sourceArn"`
		}
		_ = json.Unmarshal(b, &h)
		source = h.SourceArn
	}
	if source == "" {
		return nil, awsErr("ResourceNotFoundException", "The task handle is not valid.")
	}
	if err := q.Authorize("sqs:CancelMessageMoveTask", source); err != nil {
		return nil, err
	}
	t, err := s.cancelMove(in.TaskHandle)
	if err != nil {
		return nil, err
	}
	return struct{ ApproximateNumberOfMessagesMoved int64 }{t.Moved}, nil
}

func (s *Service) awsListMessageMoveTasks(q *awsapi.Req) (any, error) {
	var in struct {
		SourceArn  string
		MaxResults *int
	}
	if err := bind(q, &in); err != nil {
		return nil, err
	}
	src, err := s.queueByARN(q, in.SourceArn, "sqs:ListMessageMoveTasks")
	if err != nil {
		return nil, err
	}
	max := 1
	if in.MaxResults != nil {
		if *in.MaxResults < 1 || *in.MaxResults > 10 {
			return nil, awsErr("InvalidParameterValue", "MaxResults must be between 1 and 10.")
		}
		max = *in.MaxResults
	}
	type entry struct {
		TaskHandle                        string `json:",omitempty"`
		Status                            string
		SourceArn                         string
		DestinationArn                    string `json:",omitempty"`
		MaxNumberOfMessagesPerSecond      *int   `json:",omitempty"`
		ApproximateNumberOfMessagesMoved  int64
		ApproximateNumberOfMessagesToMove *int64 `json:",omitempty"`
		FailureReason                     string `json:",omitempty"`
		StartedTimestamp                  int64
	}
	out := struct {
		Results []entry `query:"ListMessageMoveTasksResultEntry"`
	}{Results: []entry{}}
	for _, t := range s.listMoves(src.ARN, max) {
		e := entry{Status: t.Status, SourceArn: t.Source, DestinationArn: t.Dest, ApproximateNumberOfMessagesMoved: t.Moved,
			FailureReason: t.Failure, StartedTimestamp: t.Started.UnixMilli()}
		if t.Status == taskRunning {
			e.TaskHandle = t.Handle
			n := t.ToMove
			e.ApproximateNumberOfMessagesToMove = &n
		}
		if t.Rate > 0 {
			r := t.Rate
			e.MaxNumberOfMessagesPerSecond = &r
		}
		out.Results = append(out.Results, e)
	}
	return out, nil
}
