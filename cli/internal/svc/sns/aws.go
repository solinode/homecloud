package sns

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The AWS SNS API (awsQuery, 2010-03-31).

const xmlns = "http://sns.amazonaws.com/doc/2010-03-31/"

// RegisterAWS serves SNS over the AWS protocol.
func (s *Service) RegisterAWS() {
	ops := map[string]func(q *awsapi.Req) (any, error){
		"CreateTopic":               s.awsCreateTopic,
		"ListTopics":                s.awsListTopics,
		"DeleteTopic":               s.awsDeleteTopic,
		"GetTopicAttributes":        s.awsGetTopicAttributes,
		"SetTopicAttributes":        s.awsSetTopicAttributes,
		"AddPermission":             s.awsAddPermission,
		"RemovePermission":          s.awsRemovePermission,
		"Subscribe":                 s.awsSubscribe,
		"ConfirmSubscription":       s.awsConfirmSubscription,
		"Unsubscribe":               s.awsUnsubscribe,
		"ListSubscriptions":         s.awsListSubscriptions,
		"ListSubscriptionsByTopic":  s.awsListSubscriptionsByTopic,
		"GetSubscriptionAttributes": s.awsGetSubscriptionAttributes,
		"SetSubscriptionAttributes": s.awsSetSubscriptionAttributes,
		"Publish":                   s.awsPublish,
		"PublishBatch":              s.awsPublishBatch,
		"TagResource":               s.awsTagResource,
		"UntagResource":             s.awsUntagResource,
		"ListTagsForResource":       s.awsListTagsForResource,
	}
	svc := &awsapi.Service{Name: "sns", XMLNS: xmlns, Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = wrap(fn)
	}
	awsapi.Register(svc)
}

func wrap(fn func(q *awsapi.Req) (any, error)) awsapi.Op {
	return func(q *awsapi.Req) (any, error) {
		out, err := fn(q)
		if err != nil {
			return nil, snsError(err)
		}
		if out == nil {
			return awsapi.NoResult{}, nil
		}
		return out, nil
	}
}

// snsStatus is the HTTP status of each SNS error code.
var snsStatus = map[string]int{
	"NotFound": 404, "ResourceNotFound": 404, "AuthorizationError": 403, "InternalError": 500,
	"Throttled": 429, "TopicLimitExceeded": 403, "SubscriptionLimitExceeded": 403, "FilterPolicyLimitExceeded": 403,
}

func snsError(err error) error {
	code, msg := "", ""
	var ae *awsapi.Error
	var ce *core.Error
	switch {
	case errors.As(err, &ae):
		code, msg = ae.Code, ae.Message
	case errors.As(err, &ce):
		code, msg = ce.Code, ce.Message
	default:
		return err
	}
	switch code {
	case "AccessDenied", "AccessDeniedException":
		code = "AuthorizationError"
	case "ValidationError", "BadRequest", "ResourceConflict", "Conflict", "ValidationException":
		code, msg = "InvalidParameter", "Invalid parameter: "+msg
	}
	status := snsStatus[code]
	if status == 0 {
		status = http.StatusBadRequest
		if ae != nil && ae.Status >= 500 {
			status = ae.Status
		}
	}
	return &awsapi.Error{Status: status, Code: code, Message: msg}
}

func notFound(format string, a ...any) error {
	return core.Errf(http.StatusNotFound, "NotFound", format, a...)
}

// topicName validates a topic ARN in this account and returns the topic name.
func topicName(q *awsapi.Req, arn, param string) (string, error) {
	parts := strings.Split(core.CanonicalARN(arn), ":")
	if arn == "" || len(parts) != 6 || parts[0] != "arn" || parts[2] != "sns" || parts[5] == "" {
		return "", errInvalid("%s", param)
	}
	if parts[4] != q.Account {
		return "", notFound("Topic does not exist")
	}
	return parts[5], nil
}

// topic resolves and authorizes a topic ARN, and loads the topic.
func (s *Service) topic(q *awsapi.Req, arn, action string) (Topic, error) {
	name, err := topicName(q, arn, "TopicArn")
	if err != nil {
		return Topic{}, err
	}
	if err := q.Authorize(action, q.ARN("sns", name)); err != nil {
		return Topic{}, err
	}
	return s.getTopic(name)
}

// subscription loads a subscription and authorizes action on its topic.
func (s *Service) subscription(q *awsapi.Req, arn, action string) (Subscription, error) {
	if arn == "" || strings.Count(arn, ":") != 6 {
		return Subscription{}, errInvalid("SubscriptionArn")
	}
	topicARN := arn[:strings.LastIndex(arn, ":")]
	name, err := topicName(q, topicARN, "SubscriptionArn")
	if err != nil {
		return Subscription{}, err
	}
	if err := q.Authorize(action, q.ARN("sns", name)); err != nil {
		return Subscription{}, err
	}
	sub, err := store.Get[Subscription](s.env.Store, cSubs, core.CanonicalARN(arn))
	if err != nil {
		return Subscription{}, errNoSub()
	}
	return sub, nil
}

// entries renders a map as an awsQuery map (<entry><key/><value/></entry>).
func entries(m map[string]string) awsapi.Named {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := awsapi.Named{Name: "entry"}
	for _, k := range keys {
		out.Values = append(out.Values, awsapi.Ordered{{K: "key", V: k}, {K: "value", V: m[k]}})
	}
	return out
}

// messageAttributes parses MessageAttributes.entry.N.{Name,Value.DataType,Value.StringValue,Value.BinaryValue}.
func messageAttributes(m map[string]string, prefix string) (map[string]Attribute, error) {
	var out map[string]Attribute
	for n := 1; ; n++ {
		p := prefix + ".entry." + strconv.Itoa(n) + "."
		name, ok := m[p+"Name"]
		if !ok {
			return out, nil
		}
		a := Attribute{DataType: m[p+"Value.DataType"], StringValue: m[p+"Value.StringValue"]}
		if a.DataType == "" {
			return nil, errValue("The message attribute '%s' must contain non-empty message attribute type.", name)
		}
		if b, ok := m[p+"Value.BinaryValue"]; ok {
			v, err := base64.StdEncoding.DecodeString(b)
			if err != nil {
				return nil, errValue("The message attribute '%s' has an invalid binary value.", name)
			}
			a.BinaryValue = v
		}
		if out == nil {
			out = map[string]Attribute{}
		}
		out[name] = a
	}
}

func formValues(q *awsapi.Req) map[string]string {
	m := make(map[string]string, len(q.Form))
	for k, v := range q.Form {
		m[k] = v[0]
	}
	return m
}

// pageOf returns the ids after token (up to 100) and the next token.
func pageOf(ids []string, token string) ([]string, string, error) {
	start := 0
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return nil, "", errInvalid("NextToken")
		}
		start = sort.SearchStrings(ids, string(b)+"\x00")
	}
	end := min(start+100, len(ids))
	next := ""
	if end < len(ids) {
		next = base64.RawURLEncoding.EncodeToString([]byte(ids[end-1]))
	}
	return ids[start:end], next, nil
}

// ---- topics ----

func (s *Service) awsCreateTopic(q *awsapi.Req) (any, error) {
	name := q.Param("Name")
	if name == "" {
		return nil, errInvalid("Topic Name")
	}
	if err := q.Authorize("sns:CreateTopic", q.ARN("sns", name)); err != nil {
		return nil, err
	}
	attrs := q.Map("Attributes", "key", "value")
	if p := q.Param("DataProtectionPolicy"); p != "" {
		attrs["DataProtectionPolicy"] = p
	}
	var tags core.Tags
	if ts := q.Structs("Tags"); len(ts) > 0 {
		if err := q.Authorize("sns:TagResource", q.ARN("sns", name)); err != nil {
			return nil, err
		}
		tags = core.Tags{}
		for _, t := range ts {
			tags[t["Key"]] = t["Value"]
		}
	}
	t, err := s.createTopic(name, attrs, tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"TopicArn": t.ARN}, nil
}

func (s *Service) awsListTopics(q *awsapi.Req) (any, error) {
	if err := q.Authorize("sns:ListTopics", "*"); err != nil {
		return nil, err
	}
	var arns []string
	for _, t := range store.List[Topic](s.env.Store, cTopics) {
		arns = append(arns, t.ARN)
	}
	sort.Strings(arns)
	sel, next, err := pageOf(arns, q.Param("NextToken"))
	if err != nil {
		return nil, err
	}
	topics := awsapi.Members{}
	for _, a := range sel {
		topics = append(topics, map[string]any{"TopicArn": a})
	}
	out := map[string]any{"Topics": topics}
	if next != "" {
		out["NextToken"] = next
	}
	return out, nil
}

func (s *Service) awsDeleteTopic(q *awsapi.Req) (any, error) {
	name, err := topicName(q, q.Param("TopicArn"), "TopicArn")
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("sns:DeleteTopic", q.ARN("sns", name)); err != nil {
		return nil, err
	}
	return nil, s.deleteTopic(name)
}

func defaultPolicy(t Topic, account string) string {
	b, _ := json.Marshal(map[string]any{"Version": "2008-10-17", "Id": "__default_policy_ID", "Statement": []any{map[string]any{
		"Sid": "__default_statement_ID", "Effect": "Allow", "Principal": map[string]any{"AWS": "*"},
		"Action": []string{"SNS:GetTopicAttributes", "SNS:SetTopicAttributes", "SNS:AddPermission", "SNS:RemovePermission",
			"SNS:DeleteTopic", "SNS:Subscribe", "SNS:ListSubscriptionsByTopic", "SNS:Publish"},
		"Resource": t.ARN, "Condition": map[string]any{"StringEquals": map[string]any{"AWS:SourceOwner": account}}}}})
	return string(b)
}

const defaultDeliveryPolicy = `{"http":{"defaultHealthyRetryPolicy":{"minDelayTarget":20,"maxDelayTarget":20,"numRetries":3,"numMaxDelayRetries":0,"numNoDelayRetries":0,"numMinDelayRetries":0,"backoffFunction":"linear"},"disableSubscriptionOverrides":false,"defaultRequestPolicy":{"headerContentType":"text/plain; charset=UTF-8"}}}`

func (s *Service) topicAttributes(t Topic) map[string]string {
	confirmed, pending := 0, 0
	for _, sub := range s.subsOf(t.ARN) {
		if sub.Status == statusPending {
			pending++
		} else {
			confirmed++
		}
	}
	m := map[string]string{
		"TopicArn": t.ARN, "Owner": s.env.AccountID, "DisplayName": t.DisplayName, "Policy": defaultPolicy(t, s.env.AccountID),
		"SubscriptionsConfirmed": strconv.Itoa(confirmed), "SubscriptionsPending": strconv.Itoa(pending),
		"SubscriptionsDeleted": strconv.FormatInt(t.SubsDeleted, 10), "EffectiveDeliveryPolicy": defaultDeliveryPolicy,
	}
	for k, v := range t.Attributes {
		m[k] = v
	}
	if d := t.attr("DeliveryPolicy"); d != "" {
		m["EffectiveDeliveryPolicy"] = d
	}
	if t.FIFO {
		m["FifoTopic"] = "true"
		if m["ContentBasedDeduplication"] == "" {
			m["ContentBasedDeduplication"] = "false"
		}
	}
	return m
}

func (s *Service) awsGetTopicAttributes(q *awsapi.Req) (any, error) {
	t, err := s.topic(q, q.Param("TopicArn"), "sns:GetTopicAttributes")
	if err != nil {
		return nil, err
	}
	return map[string]any{"Attributes": entries(s.topicAttributes(t))}, nil
}

func (s *Service) awsSetTopicAttributes(q *awsapi.Req) (any, error) {
	t, err := s.topic(q, q.Param("TopicArn"), "sns:SetTopicAttributes")
	if err != nil {
		return nil, err
	}
	name, value := q.Param("AttributeName"), q.Param("AttributeValue")
	_, err = store.Update(s.env.Store, cTopics, t.Name, func(x *Topic) error { return setTopicAttribute(x, name, value, false) })
	return nil, err
}

type policyDoc struct {
	Version   string           `json:"Version"`
	ID        string           `json:"Id,omitempty"`
	Statement []map[string]any `json:"Statement"`
}

func (s *Service) editPolicy(t Topic, fn func(doc *policyDoc) error) error {
	var doc policyDoc
	_ = json.Unmarshal([]byte(s.topicAttributes(t)["Policy"]), &doc)
	if err := fn(&doc); err != nil {
		return err
	}
	b, _ := json.Marshal(doc)
	_, err := store.Update(s.env.Store, cTopics, t.Name, func(x *Topic) error { return setTopicAttribute(x, "Policy", string(b), false) })
	return err
}

var labelRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (s *Service) awsAddPermission(q *awsapi.Req) (any, error) {
	t, err := s.topic(q, q.Param("TopicArn"), "sns:AddPermission")
	if err != nil {
		return nil, err
	}
	label, accounts, actions := q.Param("Label"), q.List("AWSAccountId"), q.List("ActionName")
	if !labelRe.MatchString(label) || len(accounts) == 0 || len(actions) == 0 {
		return nil, errInvalid("Label, AWSAccountId and ActionName are required")
	}
	return nil, s.editPolicy(t, func(doc *policyDoc) error {
		for _, st := range doc.Statement {
			if st["Sid"] == label {
				return errInvalid("Statement already exists")
			}
		}
		principals := make([]string, len(accounts))
		for i, a := range accounts {
			principals[i] = "arn:" + core.Partition + ":iam::" + a + ":root"
		}
		acts := make([]string, len(actions))
		for i, a := range actions {
			acts[i] = "SNS:" + a
		}
		doc.Statement = append(doc.Statement, map[string]any{"Sid": label, "Effect": "Allow",
			"Principal": map[string]any{"AWS": principals}, "Action": acts, "Resource": t.ARN})
		return nil
	})
}

func (s *Service) awsRemovePermission(q *awsapi.Req) (any, error) {
	t, err := s.topic(q, q.Param("TopicArn"), "sns:RemovePermission")
	if err != nil {
		return nil, err
	}
	label := q.Param("Label")
	return nil, s.editPolicy(t, func(doc *policyDoc) error {
		kept := doc.Statement[:0]
		for _, st := range doc.Statement {
			if st["Sid"] != label {
				kept = append(kept, st)
			}
		}
		if len(kept) == len(doc.Statement) {
			return errInvalid("Label: statement %s does not exist", label)
		}
		doc.Statement = kept
		return nil
	})
}

// ---- subscriptions ----

func (s *Service) awsSubscribe(q *awsapi.Req) (any, error) {
	t, err := s.topic(q, q.Param("TopicArn"), "sns:Subscribe")
	if err != nil {
		return nil, err
	}
	if q.Param("Protocol") == "" {
		return nil, errInvalid("Protocol")
	}
	if q.Param("Endpoint") == "" {
		return nil, errInvalid("Endpoint")
	}
	sub, err := s.subscribe(t, subscribeInput{Protocol: q.Param("Protocol"), Endpoint: q.Param("Endpoint"),
		Attributes: q.Map("Attributes", "key", "value"), BaseURL: BaseURL(q.R), Principal: q.P.ARN}, q.Authorize)
	if err != nil {
		return nil, err
	}
	arn := sub.ARN
	if sub.Status == statusPending && !q.ParamBool("ReturnSubscriptionArn", false) {
		arn = "pending confirmation"
	}
	return map[string]any{"SubscriptionArn": arn}, nil
}

func (s *Service) awsConfirmSubscription(q *awsapi.Req) (any, error) {
	t, err := s.topic(q, q.Param("TopicArn"), "sns:ConfirmSubscription")
	if err != nil {
		return nil, err
	}
	sub, err := s.confirm(t.ARN, q.Param("Token"), true, q.ParamBool("AuthenticateOnUnsubscribe", false))
	if err != nil {
		return nil, err
	}
	return map[string]any{"SubscriptionArn": sub.ARN}, nil
}

func (s *Service) awsUnsubscribe(q *awsapi.Req) (any, error) {
	sub, err := s.subscription(q, q.Param("SubscriptionArn"), "sns:Unsubscribe")
	if err != nil {
		return nil, err
	}
	return nil, s.unsubscribe(sub)
}

func subscriptionMember(sub Subscription) map[string]any {
	arn := sub.ARN
	if sub.Status == statusPending {
		arn = "PendingConfirmation"
	}
	return map[string]any{"SubscriptionArn": arn, "Owner": sub.Owner, "Protocol": sub.Protocol, "Endpoint": sub.Endpoint, "TopicArn": sub.TopicARN}
}

func (s *Service) listSubscriptions(q *awsapi.Req, topicARN string) (any, error) {
	all := store.List[Subscription](s.env.Store, cSubs)
	byARN := map[string]Subscription{}
	var arns []string
	for _, sub := range all {
		if topicARN == "" || sub.TopicARN == topicARN {
			if sub.Owner == "" {
				sub.Owner = s.env.AccountID
			}
			byARN[sub.ARN] = sub
			arns = append(arns, sub.ARN)
		}
	}
	sort.Strings(arns)
	sel, next, err := pageOf(arns, q.Param("NextToken"))
	if err != nil {
		return nil, err
	}
	subs := awsapi.Members{}
	for _, a := range sel {
		subs = append(subs, subscriptionMember(byARN[a]))
	}
	out := map[string]any{"Subscriptions": subs}
	if next != "" {
		out["NextToken"] = next
	}
	return out, nil
}

func (s *Service) awsListSubscriptions(q *awsapi.Req) (any, error) {
	if err := q.Authorize("sns:ListSubscriptions", "*"); err != nil {
		return nil, err
	}
	return s.listSubscriptions(q, "")
}

func (s *Service) awsListSubscriptionsByTopic(q *awsapi.Req) (any, error) {
	t, err := s.topic(q, q.Param("TopicArn"), "sns:ListSubscriptionsByTopic")
	if err != nil {
		return nil, err
	}
	return s.listSubscriptions(q, t.ARN)
}

func (s *Service) subscriptionAttributes(sub Subscription) map[string]string {
	owner := sub.Owner
	if owner == "" {
		owner = s.env.AccountID
	}
	m := map[string]string{
		"SubscriptionArn": sub.ARN, "TopicArn": sub.TopicARN, "Owner": owner, "Protocol": sub.Protocol, "Endpoint": sub.Endpoint,
		"ConfirmationWasAuthenticated": strconv.FormatBool(sub.ConfirmedByAuth || sub.Protocol == "sqs" || sub.Protocol == "lambda"),
		"PendingConfirmation":          strconv.FormatBool(sub.Status == statusPending),
		"RawMessageDelivery":           strconv.FormatBool(sub.RawMessageDelivery),
	}
	if len(sub.FilterPolicy) > 0 {
		m["FilterPolicy"] = string(sub.FilterPolicy)
		m["FilterPolicyScope"] = sub.scope()
	}
	if sub.RedrivePolicy != "" {
		m["RedrivePolicy"] = sub.RedrivePolicy
	}
	if sub.DeliveryPolicy != "" {
		m["DeliveryPolicy"] = sub.DeliveryPolicy
	}
	if sub.Protocol == "http" || sub.Protocol == "https" {
		m["EffectiveDeliveryPolicy"] = orDefault(sub.DeliveryPolicy, defaultDeliveryPolicy)
	}
	if sub.SubscriptionRoleARN != "" {
		m["SubscriptionRoleArn"] = sub.SubscriptionRoleARN
	}
	if sub.Principal != "" {
		m["SubscriptionPrincipal"] = sub.Principal
	}
	return m
}

func (s *Service) awsGetSubscriptionAttributes(q *awsapi.Req) (any, error) {
	sub, err := s.subscription(q, q.Param("SubscriptionArn"), "sns:GetSubscriptionAttributes")
	if err != nil {
		return nil, err
	}
	return map[string]any{"Attributes": entries(s.subscriptionAttributes(sub))}, nil
}

func (s *Service) awsSetSubscriptionAttributes(q *awsapi.Req) (any, error) {
	sub, err := s.subscription(q, q.Param("SubscriptionArn"), "sns:SetSubscriptionAttributes")
	if err != nil {
		return nil, err
	}
	name, value := q.Param("AttributeName"), q.Param("AttributeValue")
	_, err = store.Update(s.env.Store, cSubs, sub.ARN, func(x *Subscription) error { return s.setSubAttribute(x, name, value) })
	return nil, err
}

// ---- publishing ----

func (s *Service) awsPublish(q *awsapi.Req) (any, error) {
	f := formValues(q)
	attrs, err := messageAttributes(f, "MessageAttributes")
	if err != nil {
		return nil, err
	}
	arn := f["TopicArn"]
	if arn == "" {
		arn = f["TargetArn"]
	}
	if arn == "" {
		phone := f["PhoneNumber"]
		if phone == "" {
			return nil, errInvalid("TopicArn or TargetArn Reason: no value for required parameter")
		}
		if err := q.Authorize("sns:Publish", "*"); err != nil {
			return nil, err
		}
		if !phoneRe.MatchString(phone) {
			return nil, errInvalid("PhoneNumber Reason: %s is not valid to publish to", phone)
		}
		if f["Message"] == "" {
			return nil, errInvalid("Empty message")
		}
		// HomeCloud has no SMS gateway: the message is logged.
		log.Printf("sns: sms to %s: %s", phone, f["Message"])
		return map[string]any{"MessageId": uuid()}, nil
	}
	t, err := s.topic(q, arn, "sns:Publish")
	if err != nil {
		return nil, err
	}
	r, err := s.PublishMessage(PublishInput{Topic: t.Name, Subject: f["Subject"], Message: f["Message"], Structure: f["MessageStructure"],
		Attributes: attrs, GroupID: f["MessageGroupId"], DedupID: f["MessageDeduplicationId"], Strict: true})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"MessageId": r.MessageID}
	if r.SequenceNumber != "" {
		out["SequenceNumber"] = r.SequenceNumber
	}
	return out, nil
}

var batchIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func batchErr(code, format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, code, format, a...)
}

func (s *Service) awsPublishBatch(q *awsapi.Req) (any, error) {
	t, err := s.topic(q, q.Param("TopicArn"), "sns:Publish")
	if err != nil {
		return nil, err
	}
	entries := q.Structs("PublishBatchRequestEntries")
	if len(entries) == 0 {
		return nil, batchErr("EmptyBatchRequest", "The batch request doesn't contain any entries.")
	}
	if len(entries) > 10 {
		return nil, batchErr("TooManyEntriesInBatchRequest", "The batch request contains more entries than permissible.")
	}
	seen, total := map[string]bool{}, 0
	for _, e := range entries {
		if !batchIDRe.MatchString(e["Id"]) {
			return nil, batchErr("InvalidBatchEntryId", "The Id of a batch entry in a batch request doesn't abide by the specification.")
		}
		if seen[e["Id"]] {
			return nil, batchErr("BatchEntryIdsNotDistinct", "Two or more batch entries in the request have the same Id.")
		}
		seen[e["Id"]] = true
		for k, v := range e {
			if k == "Message" || strings.HasPrefix(k, "MessageAttributes.") {
				total += len(v)
			}
		}
	}
	if total > maxMessageBytes {
		return nil, batchErr("BatchRequestTooLong", "The length of all the messages put together is more than the limit.")
	}
	ok, failed := awsapi.Members{}, awsapi.Members{}
	for _, e := range entries {
		attrs, err := messageAttributes(e, "MessageAttributes")
		var r PublishResult
		if err == nil {
			r, err = s.PublishMessage(PublishInput{Topic: t.Name, Subject: e["Subject"], Message: e["Message"], Structure: e["MessageStructure"],
				Attributes: attrs, GroupID: e["MessageGroupId"], DedupID: e["MessageDeduplicationId"], Strict: true})
		}
		if err != nil {
			var ae *awsapi.Error
			if errors.As(snsError(err), &ae) {
				failed = append(failed, map[string]any{"Id": e["Id"], "Code": ae.Code, "Message": ae.Message, "SenderFault": ae.Status < 500})
			}
			continue
		}
		m := map[string]any{"Id": e["Id"], "MessageId": r.MessageID}
		if r.SequenceNumber != "" {
			m["SequenceNumber"] = r.SequenceNumber
		}
		ok = append(ok, m)
	}
	return map[string]any{"Successful": ok, "Failed": failed}, nil
}

// ---- tags ----

func (s *Service) tagTarget(q *awsapi.Req, action string) (Topic, error) {
	arn := q.Param("ResourceArn")
	parts := strings.Split(core.CanonicalARN(arn), ":")
	if len(parts) != 6 || parts[2] != "sns" || parts[4] != q.Account {
		return Topic{}, core.Errf(http.StatusNotFound, "ResourceNotFound", "Resource does not exist")
	}
	if err := q.Authorize(action, q.ARN("sns", parts[5])); err != nil {
		return Topic{}, err
	}
	t, err := s.getTopic(parts[5])
	if err != nil {
		return Topic{}, core.Errf(http.StatusNotFound, "ResourceNotFound", "Resource does not exist")
	}
	return t, nil
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	t, err := s.tagTarget(q, "sns:TagResource")
	if err != nil {
		return nil, err
	}
	tags := q.Structs("Tags")
	_, err = store.Update(s.env.Store, cTopics, t.Name, func(x *Topic) error {
		if x.Tags == nil {
			x.Tags = core.Tags{}
		}
		for _, tg := range tags {
			if tg["Key"] == "" || len(tg["Key"]) > 128 || len(tg["Value"]) > 256 {
				return errInvalid("Tags")
			}
			x.Tags[tg["Key"]] = tg["Value"]
		}
		if len(x.Tags) > 50 {
			return core.Errf(http.StatusBadRequest, "TagLimitExceeded", "Could not complete request: tag quota of per resource exceeded")
		}
		return nil
	})
	return emptyResult(err)
}

// emptyResult is the (empty) result element of operations that have one.
func emptyResult(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	t, err := s.tagTarget(q, "sns:UntagResource")
	if err != nil {
		return nil, err
	}
	keys := q.List("TagKeys")
	_, err = store.Update(s.env.Store, cTopics, t.Name, func(x *Topic) error {
		for _, k := range keys {
			delete(x.Tags, k)
		}
		return nil
	})
	return emptyResult(err)
}

func (s *Service) awsListTagsForResource(q *awsapi.Req) (any, error) {
	t, err := s.tagTarget(q, "sns:ListTagsForResource")
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(t.Tags))
	for k := range t.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tags := awsapi.Members{}
	for _, k := range keys {
		tags = append(tags, awsapi.Ordered{{K: "Key", V: k}, {K: "Value", V: t.Tags[k]}})
	}
	return map[string]any{"Tags": tags}, nil
}
