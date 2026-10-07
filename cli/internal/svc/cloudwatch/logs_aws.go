package cloudwatch

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// CloudWatch Logs over the AWS JSON 1.1 protocol (X-Amz-Target Logs_20140328.*).

func (s *Service) registerLogsAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "logs", JSONPrefix: "Logs_20140328", JSONVersion: "1.1",
		ErrorCode: map[string]string{
			"ValidationError":  "InvalidParameterException",
			"BadRequest":       "InvalidParameterException",
			"ResourceConflict": "ResourceAlreadyExistsException",
			"AlreadyExists":    "ResourceAlreadyExistsException",
		},
		Ops: map[string]awsapi.Op{
			"CreateLogGroup":               s.awsCreateLogGroup,
			"DeleteLogGroup":               s.awsDeleteLogGroup,
			"DescribeLogGroups":            s.awsDescribeLogGroups,
			"PutRetentionPolicy":           s.awsPutRetentionPolicy,
			"DeleteRetentionPolicy":        s.awsDeleteRetentionPolicy,
			"CreateLogStream":              s.awsCreateLogStream,
			"DeleteLogStream":              s.awsDeleteLogStream,
			"DescribeLogStreams":           s.awsDescribeLogStreams,
			"PutLogEvents":                 s.awsPutLogEvents,
			"GetLogEvents":                 s.awsGetLogEvents,
			"FilterLogEvents":              s.awsFilterLogEvents,
			"TagLogGroup":                  s.awsTagLogGroup,
			"UntagLogGroup":                s.awsUntagLogGroup,
			"ListTagsLogGroup":             s.awsListTagsLogGroup,
			"TagResource":                  s.awsTagResource,
			"UntagResource":                s.awsUntagResource,
			"ListTagsForResource":          s.awsListTagsForResource,
			"StartQuery":                   s.awsStartQuery,
			"GetQueryResults":              s.awsGetQueryResults,
			"StopQuery":                    s.awsStopQuery,
			"DescribeQueries":              s.awsDescribeQueries,
			"PutQueryDefinition":           s.awsPutQueryDefinition,
			"DescribeQueryDefinitions":     s.awsDescribeQueryDefinitions,
			"DeleteQueryDefinition":        s.awsDeleteQueryDefinition,
			"PutDeliverySource":            s.awsPutDeliverySource,
			"GetDeliverySource":            s.awsGetDeliverySource,
			"DescribeDeliverySources":      s.awsDescribeDeliverySources,
			"DeleteDeliverySource":         s.awsDeleteDeliverySource,
			"PutDeliveryDestination":       s.awsPutDeliveryDestination,
			"GetDeliveryDestination":       s.awsGetDeliveryDestination,
			"DescribeDeliveryDestinations": s.awsDescribeDeliveryDestinations,
			"DeleteDeliveryDestination":    s.awsDeleteDeliveryDestination,
			"CreateDelivery":               s.awsCreateDelivery,
			"GetDelivery":                  s.awsGetDelivery,
			"DescribeDeliveries":           s.awsDescribeDeliveries,
			"DeleteDelivery":               s.awsDeleteDelivery,
			"PutMetricFilter":              s.awsPutMetricFilter,
			"DescribeMetricFilters":        s.awsDescribeMetricFilters,
			"DeleteMetricFilter":           s.awsDeleteMetricFilter,
			"TestMetricFilter":             s.awsTestMetricFilter,
			"PutSubscriptionFilter":        s.awsPutSubscriptionFilter,
			"DescribeSubscriptionFilters":  s.awsDescribeSubscriptionFilters,
			"DeleteSubscriptionFilter":     s.awsDeleteSubscriptionFilter,
		},
	})
}

func logsErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

func groupNotFound() error {
	return logsErr("ResourceNotFoundException", "The specified log group does not exist.")
}

func (s *Service) groupARN(name string) string { return s.env.ARN("logs", "log-group:"+name) }

// authGroup authorizes an action on a log group. Policies usually name groups
// as "log-group:NAME:*", so that form is tried first.
// authorizer is satisfied by *awsapi.Req and *httpx.Ctx, so the AWS and native
// handlers share their logic.
type authorizer interface {
	Authorize(action, resource string) error
	Check(action, resource string) error
}

func (s *Service) authGroup(q authorizer, action, group string) error {
	arn := s.groupARN(group)
	if q.Check(action, arn+":*") == nil {
		return q.Authorize(action, arn+":*")
	}
	return q.Authorize(action, arn)
}

func (s *Service) authStream(q *awsapi.Req, action, group, stream string) error {
	return q.Authorize(action, s.groupARN(group)+":log-stream:"+stream)
}

// groupRef resolves logGroupName / logGroupIdentifier (a name or ARN).
func groupRef(name, ident string) (string, error) {
	if name == "" {
		name = ident
	}
	if i := strings.Index(name, ":log-group:"); strings.HasPrefix(name, "arn:") && i >= 0 {
		name = strings.TrimSuffix(name[i+len(":log-group:"):], ":*")
	}
	if name == "" {
		return "", logsErr("InvalidParameterException", "logGroupName or logGroupIdentifier is required")
	}
	return name, nil
}

func (s *Service) storedGroup(name string) (LogGroup, error) {
	g, err := store.Get[LogGroup](s.env.Store, cLogGroups, name)
	if err != nil {
		return g, groupNotFound()
	}
	return g, nil
}

// knownGroup reports whether a group exists (stored or container-backed).
func (s *Service) knownGroup(name string) (LogGroup, bool) {
	if g, err := store.Get[LogGroup](s.env.Store, cLogGroups, name); err == nil {
		return g, true
	}
	if strings.HasPrefix(name, containerGroupPrefix) && s.containerFor(name) != "" {
		return LogGroup{Name: name, ARN: s.groupARN(name), Source: "container"}, true
	}
	return LogGroup{}, false
}

var retentionValues = map[int]bool{1: true, 3: true, 5: true, 7: true, 14: true, 30: true, 60: true, 90: true, 120: true, 150: true, 180: true,
	365: true, 400: true, 545: true, 731: true, 1096: true, 1827: true, 2192: true, 2557: true, 2922: true, 3288: true, 3653: true}

func checkTags(tags map[string]string) error {
	if len(tags) > 50 {
		return logsErr("InvalidParameterException", "a resource can have at most 50 tags")
	}
	for k, v := range tags {
		if k == "" || len(k) > 128 || len(v) > 256 || strings.HasPrefix(strings.ToLower(k), "aws:") {
			return logsErr("InvalidParameterException", "invalid tag %q", k)
		}
	}
	return nil
}

// pageToken encodes a pagination position.
func pageToken(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }

func fromToken(t string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(t)
	if err != nil {
		return "", logsErr("InvalidParameterException", "invalid nextToken")
	}
	return string(b), nil
}

// ---- groups ----

func (s *Service) awsCreateLogGroup(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName  string            `json:"logGroupName"`
		KmsKeyID      string            `json:"kmsKeyId"`
		Tags          map[string]string `json:"tags"`
		LogGroupClass string            `json:"logGroupClass"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:CreateLogGroup", in.LogGroupName); err != nil {
		return nil, err
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	if in.LogGroupClass == "" {
		in.LogGroupClass = "STANDARD"
	}
	_, err := s.CreateGroup(LogGroup{Name: in.LogGroupName, KMSKeyID: in.KmsKeyID, Class: in.LogGroupClass, Tags: in.Tags})
	return nil, err
}

func (s *Service) awsDeleteLogGroup(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName string `json:"logGroupName"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:DeleteLogGroup", in.LogGroupName); err != nil {
		return nil, err
	}
	if _, err := s.storedGroup(in.LogGroupName); err != nil {
		return nil, err
	}
	s.DeleteGroup(in.LogGroupName)
	return nil, nil
}

type awsLogGroup struct {
	LogGroupName      string `json:"logGroupName"`
	CreationTime      int64  `json:"creationTime"`
	RetentionInDays   int    `json:"retentionInDays,omitempty"`
	MetricFilterCount int    `json:"metricFilterCount"`
	Arn               string `json:"arn"`
	LogGroupArn       string `json:"logGroupArn"`
	StoredBytes       int64  `json:"storedBytes"`
	KmsKeyID          string `json:"kmsKeyId,omitempty"`
	LogGroupClass     string `json:"logGroupClass"`
}

func toAWSGroup(g LogGroup) awsLogGroup {
	class := g.Class
	if class == "" {
		class = "STANDARD"
	}
	return awsLogGroup{LogGroupName: g.Name, CreationTime: g.CreatedAt.UnixMilli(), RetentionInDays: g.RetentionDays, MetricFilterCount: g.metricFilters,
		Arn: g.ARN + ":*", LogGroupArn: g.ARN, StoredBytes: g.StoredBytes, KmsKeyID: g.KMSKeyID, LogGroupClass: class}
}

func (s *Service) awsDescribeLogGroups(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupNamePrefix  string   `json:"logGroupNamePrefix"`
		LogGroupNamePattern string   `json:"logGroupNamePattern"`
		LogGroupIdentifiers []string `json:"logGroupIdentifiers"`
		LogGroupClass       string   `json:"logGroupClass"`
		NextToken           string   `json:"nextToken"`
		Limit               int      `json:"limit"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:DescribeLogGroups", s.groupARN("*")); err != nil {
		return nil, err
	}
	if in.LogGroupNamePrefix != "" && in.LogGroupNamePattern != "" {
		return nil, logsErr("InvalidParameterException", "logGroupNamePrefix and logGroupNamePattern are mutually exclusive")
	}
	if in.Limit <= 0 || in.Limit > 50 {
		in.Limit = 50
	}
	after := ""
	if in.NextToken != "" {
		var err error
		if after, err = fromToken(in.NextToken); err != nil {
			return nil, err
		}
	}
	idents := map[string]bool{}
	for _, id := range in.LogGroupIdentifiers {
		n, _ := groupRef(id, "")
		idents[n] = true
	}
	groups := s.groupsWithSize()
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	out := struct {
		LogGroups []awsLogGroup `json:"logGroups"`
		NextToken string        `json:"nextToken,omitempty"`
	}{LogGroups: []awsLogGroup{}}
	for _, g := range groups {
		switch {
		case g.Name <= after && after != "":
			continue
		case in.LogGroupNamePrefix != "" && !strings.HasPrefix(g.Name, in.LogGroupNamePrefix):
			continue
		case in.LogGroupNamePattern != "" && !strings.Contains(strings.ToLower(g.Name), strings.ToLower(in.LogGroupNamePattern)):
			continue
		case len(idents) > 0 && !idents[g.Name]:
			continue
		case in.LogGroupClass != "" && in.LogGroupClass != "STANDARD" && in.LogGroupClass != g.Class:
			continue
		}
		if len(out.LogGroups) == in.Limit {
			out.NextToken = pageToken(out.LogGroups[len(out.LogGroups)-1].LogGroupName)
			break
		}
		g.metricFilters = len(s.filters(g.Name).Metric)
		out.LogGroups = append(out.LogGroups, toAWSGroup(g))
	}
	return out, nil
}

func (s *Service) awsPutRetentionPolicy(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName    string `json:"logGroupName"`
		RetentionInDays int    `json:"retentionInDays"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:PutRetentionPolicy", in.LogGroupName); err != nil {
		return nil, err
	}
	if !retentionValues[in.RetentionInDays] {
		return nil, logsErr("InvalidParameterException", "retentionInDays must be one of 1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653")
	}
	if _, err := s.storedGroup(in.LogGroupName); err != nil {
		return nil, err
	}
	_, err := s.SetRetention(in.LogGroupName, in.RetentionInDays)
	return nil, err
}

func (s *Service) awsDeleteRetentionPolicy(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName string `json:"logGroupName"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:DeleteRetentionPolicy", in.LogGroupName); err != nil {
		return nil, err
	}
	if _, err := s.storedGroup(in.LogGroupName); err != nil {
		return nil, err
	}
	_, err := s.SetRetention(in.LogGroupName, 0)
	return nil, err
}

// ---- streams ----

func (s *Service) awsCreateLogStream(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:CreateLogStream", in.LogGroupName); err != nil {
		return nil, err
	}
	if err := validStream(in.LogStreamName); err != nil {
		return nil, err
	}
	if _, err := s.storedGroup(in.LogGroupName); err != nil {
		return nil, err
	}
	l := s.logs
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.streamExists(in.LogGroupName, in.LogStreamName) {
		return nil, logsErr("ResourceAlreadyExistsException", "The specified log stream already exists")
	}
	return nil, l.createStreamLocked(in.LogGroupName, in.LogStreamName)
}

func (s *Service) awsDeleteLogStream(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authStream(q, "logs:DeleteLogStream", in.LogGroupName, in.LogStreamName); err != nil {
		return nil, err
	}
	if _, err := s.storedGroup(in.LogGroupName); err != nil {
		return nil, err
	}
	if !s.logs.streamExists(in.LogGroupName, in.LogStreamName) {
		return nil, logsErr("ResourceNotFoundException", "The specified log stream does not exist.")
	}
	s.logs.deleteStream(in.LogGroupName, in.LogStreamName)
	return nil, nil
}

type awsLogStream struct {
	LogStreamName       string `json:"logStreamName"`
	CreationTime        int64  `json:"creationTime"`
	FirstEventTimestamp int64  `json:"firstEventTimestamp,omitempty"`
	LastEventTimestamp  int64  `json:"lastEventTimestamp,omitempty"`
	LastIngestionTime   int64  `json:"lastIngestionTime,omitempty"`
	UploadSequenceToken string `json:"uploadSequenceToken,omitempty"`
	Arn                 string `json:"arn"`
	StoredBytes         int64  `json:"storedBytes"`
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func seqToken(n int64) string { return fmt.Sprintf("%056d", n) }

func (s *Service) awsDescribeLogStreams(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName        string `json:"logGroupName"`
		LogGroupIdentifier  string `json:"logGroupIdentifier"`
		LogStreamNamePrefix string `json:"logStreamNamePrefix"`
		OrderBy             string `json:"orderBy"`
		Descending          bool   `json:"descending"`
		NextToken           string `json:"nextToken"`
		Limit               int    `json:"limit"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name, err := groupRef(in.LogGroupName, in.LogGroupIdentifier)
	if err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:DescribeLogStreams", name); err != nil {
		return nil, err
	}
	switch in.OrderBy {
	case "", "LogStreamName":
		in.OrderBy = "LogStreamName"
	case "LastEventTime":
		if in.LogStreamNamePrefix != "" {
			return nil, logsErr("InvalidParameterException", "Cannot order by LastEventTime with a logStreamNamePrefix.")
		}
	default:
		return nil, logsErr("InvalidParameterException", "orderBy must be LogStreamName or LastEventTime")
	}
	if in.Limit <= 0 || in.Limit > 50 {
		in.Limit = 50
	}
	g, ok := s.knownGroup(name)
	if !ok {
		return nil, groupNotFound()
	}
	var streams []LogStream
	if g.Source == "container" {
		streams = []LogStream{{Name: "stdout", LastEventTime: time.Now().UTC(), CreatedAt: g.CreatedAt}}
	} else {
		streams = s.logs.streams(name)
	}
	sort.SliceStable(streams, func(i, j int) bool {
		a, b := streams[i], streams[j]
		if in.OrderBy == "LastEventTime" {
			ta, tb := a.LastEventTime, b.LastEventTime
			if ta.IsZero() {
				ta = a.CreatedAt
			}
			if tb.IsZero() {
				tb = b.CreatedAt
			}
			if !ta.Equal(tb) {
				return ta.Before(tb)
			}
		}
		return a.Name < b.Name
	})
	if in.Descending {
		slices.Reverse(streams)
	}
	start := 0
	if in.NextToken != "" {
		t, err := fromToken(in.NextToken)
		if err != nil {
			return nil, err
		}
		if start, err = strconv.Atoi(t); err != nil || start < 0 {
			return nil, logsErr("InvalidParameterException", "invalid nextToken")
		}
	}
	out := struct {
		LogStreams []awsLogStream `json:"logStreams"`
		NextToken  string         `json:"nextToken,omitempty"`
	}{LogStreams: []awsLogStream{}}
	i := 0
	for _, st := range streams {
		if in.LogStreamNamePrefix != "" && !strings.HasPrefix(st.Name, in.LogStreamNamePrefix) {
			continue
		}
		i++
		if i <= start {
			continue
		}
		if len(out.LogStreams) == in.Limit {
			out.NextToken = pageToken(strconv.Itoa(start + in.Limit))
			break
		}
		out.LogStreams = append(out.LogStreams, awsLogStream{LogStreamName: st.Name, CreationTime: ms(st.CreatedAt),
			FirstEventTimestamp: ms(st.FirstEventTime), LastEventTimestamp: ms(st.LastEventTime), LastIngestionTime: ms(st.LastIngestionTime),
			UploadSequenceToken: seqToken(st.seq + 1), Arn: g.ARN + ":log-stream:" + st.Name, StoredBytes: st.StoredBytes})
	}
	return out, nil
}

// ---- events ----

const maxBatchBytes = 1048576

func (s *Service) awsPutLogEvents(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
		LogEvents     []struct {
			Timestamp *int64 `json:"timestamp"`
			Message   string `json:"message"`
		} `json:"logEvents"`
		SequenceToken string `json:"sequenceToken"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authStream(q, "logs:PutLogEvents", in.LogGroupName, in.LogStreamName); err != nil {
		return nil, err
	}
	n := len(in.LogEvents)
	if n == 0 || n > 10000 {
		return nil, logsErr("InvalidParameterException", "logEvents must contain 1-10000 events")
	}
	size := 0
	for i, e := range in.LogEvents {
		if e.Timestamp == nil {
			return nil, logsErr("InvalidParameterException", "logEvents.%d.timestamp is required", i+1)
		}
		if e.Message == "" {
			return nil, logsErr("InvalidParameterException", "logEvents.%d.message must be at least 1 character", i+1)
		}
		if !utf8.ValidString(e.Message) {
			return nil, logsErr("InvalidParameterException", "log event messages must be valid UTF-8")
		}
		size += len(e.Message) + 26
		if i > 0 && *e.Timestamp < *in.LogEvents[i-1].Timestamp {
			return nil, logsErr("InvalidParameterException", "Log events in a single PutLogEvents request must be in chronological order.")
		}
	}
	if size > maxBatchBytes {
		return nil, logsErr("InvalidParameterException", "the batch exceeds %d bytes", maxBatchBytes)
	}
	if *in.LogEvents[n-1].Timestamp-*in.LogEvents[0].Timestamp > 24*3600*1000 {
		return nil, logsErr("InvalidParameterException", "A batch of log events in a single request cannot span more than 24 hours.")
	}
	if strings.HasPrefix(in.LogGroupName, containerGroupPrefix) {
		return nil, logsErr("InvalidParameterException", "container log groups are read-only")
	}
	g, err := s.storedGroup(in.LogGroupName)
	if err != nil {
		return nil, err
	}
	// Classify events that are too new, too old or past the group's retention.
	now := time.Now()
	tooNew, tooOld, expired := -1, -1, -1
	oldCut := now.Add(-14 * 24 * time.Hour).UnixMilli()
	newCut := now.Add(2 * time.Hour).UnixMilli()
	retCut := int64(0)
	if c := retentionCut(g); !c.IsZero() {
		retCut = c.UnixMilli()
	}
	var accepted []LogEvent
	for i, e := range in.LogEvents {
		t := *e.Timestamp
		switch {
		case t > newCut:
			if tooNew < 0 {
				tooNew = i
			}
			continue
		case retCut > 0 && t < retCut:
			expired = i
			continue
		case t < oldCut:
			tooOld = i
			continue
		}
		accepted = append(accepted, LogEvent{Timestamp: time.UnixMilli(t).UTC(), Message: e.Message})
	}
	l := s.logs
	l.mu.Lock()
	if !l.streamExists(in.LogGroupName, in.LogStreamName) {
		l.mu.Unlock()
		return nil, logsErr("ResourceNotFoundException", "The specified log stream does not exist.")
	}
	seq, stored, err := l.appendLocked(in.LogGroupName, in.LogStreamName, accepted)
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.applyFilters(in.LogGroupName, in.LogStreamName, stored)
	out := map[string]any{"nextSequenceToken": seqToken(seq + 1)}
	if tooNew >= 0 || tooOld >= 0 || expired >= 0 {
		info := map[string]int{}
		if tooNew >= 0 {
			info["tooNewLogEventStartIndex"] = tooNew
		}
		if tooOld >= 0 {
			info["tooOldLogEventEndIndex"] = tooOld
		}
		if expired >= 0 {
			info["expiredLogEventEndIndex"] = expired
		}
		out["rejectedLogEventsInfo"] = info
	}
	return out, nil
}

type outEvent struct {
	LogStreamName string `json:"logStreamName,omitempty"`
	Timestamp     int64  `json:"timestamp"`
	Message       string `json:"message"`
	IngestionTime int64  `json:"ingestionTime"`
	EventID       string `json:"eventId,omitempty"`
}

// groupEvents reads events from a stored or container-backed group.
func (s *Service) groupEvents(g LogGroup, streams []string, start, end time.Time, match func(LogEvent) bool) ([]LogEvent, error) {
	if g.Source == "container" {
		if streams != nil && !contains(streams, "stdout") {
			return nil, nil
		}
		return s.containerEvents(g.Name, start, end, match, 0)
	}
	return s.logs.readStreams(g.Name, streams, start, end, retentionCut(g), match), nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func msArg(v *int64) time.Time {
	if v == nil || *v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(*v).UTC()
}

func (s *Service) awsGetLogEvents(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName       string `json:"logGroupName"`
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		LogStreamName      string `json:"logStreamName"`
		StartTime          *int64 `json:"startTime"`
		EndTime            *int64 `json:"endTime"`
		NextToken          string `json:"nextToken"`
		Limit              int    `json:"limit"`
		StartFromHead      bool   `json:"startFromHead"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name, err := groupRef(in.LogGroupName, in.LogGroupIdentifier)
	if err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:GetLogEvents", name); err != nil {
		return nil, err
	}
	g, ok := s.knownGroup(name)
	if !ok {
		return nil, groupNotFound()
	}
	if g.Source != "container" && !s.logs.streamExists(name, in.LogStreamName) {
		return nil, logsErr("ResourceNotFoundException", "The specified log stream does not exist.")
	}
	if in.Limit <= 0 || in.Limit > 10000 {
		in.Limit = 10000
	}
	// The end time is exclusive in GetLogEvents.
	end := msArg(in.EndTime)
	if !end.IsZero() {
		end = end.Add(-time.Millisecond)
	}
	all, err := s.groupEvents(g, []string{in.LogStreamName}, msArg(in.StartTime), end, nil)
	if err != nil {
		return nil, err
	}
	// Tokens are positions in the time-ordered event list: "f/N" reads forward
	// from N, "b/N" backward from N.
	from, to := 0, len(all)
	switch tok := in.NextToken; {
	case tok == "":
		if in.StartFromHead {
			to = min(len(all), in.Limit)
		} else {
			from = max(0, len(all)-in.Limit)
		}
	case strings.HasPrefix(tok, "f/") || strings.HasPrefix(tok, "b/"):
		n, err := strconv.Atoi(tok[2:])
		if err != nil {
			return nil, logsErr("InvalidParameterException", "The specified nextToken is invalid.")
		}
		n = min(max(n, 0), len(all))
		if tok[0] == 'f' {
			from, to = n, min(len(all), n+in.Limit)
		} else {
			from, to = max(0, n-in.Limit), n
		}
	default:
		return nil, logsErr("InvalidParameterException", "The specified nextToken is invalid.")
	}
	events := make([]outEvent, 0, to-from)
	for _, e := range all[from:to] {
		events = append(events, outEvent{Timestamp: e.Timestamp.UnixMilli(), Message: e.Message, IngestionTime: e.Ingestion.UnixMilli()})
	}
	return map[string]any{"events": events, "nextForwardToken": fmt.Sprintf("f/%056d", to), "nextBackwardToken": fmt.Sprintf("b/%056d", from)}, nil
}

func (s *Service) awsFilterLogEvents(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName        string   `json:"logGroupName"`
		LogGroupIdentifier  string   `json:"logGroupIdentifier"`
		LogStreamNames      []string `json:"logStreamNames"`
		LogStreamNamePrefix string   `json:"logStreamNamePrefix"`
		StartTime           *int64   `json:"startTime"`
		EndTime             *int64   `json:"endTime"`
		FilterPattern       string   `json:"filterPattern"`
		NextToken           string   `json:"nextToken"`
		Limit               int      `json:"limit"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name, err := groupRef(in.LogGroupName, in.LogGroupIdentifier)
	if err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:FilterLogEvents", name); err != nil {
		return nil, err
	}
	if len(in.LogStreamNames) > 0 && in.LogStreamNamePrefix != "" {
		return nil, logsErr("InvalidParameterException", "logStreamNames and logStreamNamePrefix are mutually exclusive")
	}
	m, err := ParseFilterPattern(in.FilterPattern)
	if err != nil {
		return nil, logsErr("InvalidParameterException", "Invalid filter pattern: %v", err)
	}
	g, ok := s.knownGroup(name)
	if !ok {
		return nil, groupNotFound()
	}
	if in.Limit <= 0 || in.Limit > 10000 {
		in.Limit = 10000
	}
	var streams []string
	switch {
	case len(in.LogStreamNames) > 0:
		streams = in.LogStreamNames
	case in.LogStreamNamePrefix != "":
		streams = []string{}
		if g.Source == "container" {
			if strings.HasPrefix("stdout", in.LogStreamNamePrefix) {
				streams = append(streams, "stdout")
			}
		} else {
			for _, st := range s.logs.streams(name) {
				if strings.HasPrefix(st.Name, in.LogStreamNamePrefix) {
					streams = append(streams, st.Name)
				}
			}
		}
	}
	all, err := s.groupEvents(g, streams, msArg(in.StartTime), msArg(in.EndTime), func(e LogEvent) bool { return m(e.Message) })
	if err != nil {
		return nil, err
	}
	// The token is the (timestamp, event ID) of the last event returned, so
	// events that arrive between pages are neither skipped nor repeated.
	if in.NextToken != "" {
		t, err := fromToken(in.NextToken)
		if err != nil {
			return nil, err
		}
		tsPart, id, _ := strings.Cut(t, "/")
		ts, err := strconv.ParseInt(tsPart, 10, 64)
		if err != nil {
			return nil, logsErr("InvalidParameterException", "invalid nextToken")
		}
		i := sort.Search(len(all), func(i int) bool {
			e := all[i]
			return e.Timestamp.UnixMilli() > ts || (e.Timestamp.UnixMilli() == ts && e.ID > id)
		})
		all = all[i:]
	}
	out := struct {
		Events             []outEvent       `json:"events"`
		SearchedLogStreams []map[string]any `json:"searchedLogStreams"`
		NextToken          string           `json:"nextToken,omitempty"`
	}{Events: []outEvent{}, SearchedLogStreams: []map[string]any{}}
	if len(all) > in.Limit {
		last := all[in.Limit-1]
		out.NextToken = pageToken(fmt.Sprintf("%d/%s", last.Timestamp.UnixMilli(), last.ID))
		all = all[:in.Limit]
	}
	for _, e := range all {
		out.Events = append(out.Events, outEvent{LogStreamName: e.Stream, Timestamp: e.Timestamp.UnixMilli(), Message: e.Message,
			IngestionTime: e.Ingestion.UnixMilli(), EventID: e.ID})
	}
	return out, nil
}

// ---- tags ----

func (s *Service) tagGroup(name string, add map[string]string, remove []string) error {
	_, err := store.Update(s.env.Store, cLogGroups, name, func(g *LogGroup) error {
		if g.Tags == nil {
			g.Tags = core.Tags{}
		}
		for k, v := range add {
			g.Tags[k] = v
		}
		for _, k := range remove {
			delete(g.Tags, k)
		}
		if len(g.Tags) > 50 {
			return logsErr("InvalidParameterException", "a log group can have at most 50 tags")
		}
		return nil
	})
	if err == store.ErrNotFound {
		return groupNotFound()
	}
	return err
}

func (s *Service) groupTags(name string) (map[string]string, error) {
	g, err := s.storedGroup(name)
	if err != nil {
		return nil, err
	}
	if g.Tags == nil {
		return map[string]string{}, nil
	}
	return g.Tags, nil
}

func (s *Service) awsTagLogGroup(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName string            `json:"logGroupName"`
		Tags         map[string]string `json:"tags"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:TagLogGroup", in.LogGroupName); err != nil {
		return nil, err
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	return nil, s.tagGroup(in.LogGroupName, in.Tags, nil)
}

func (s *Service) awsUntagLogGroup(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName string   `json:"logGroupName"`
		Tags         []string `json:"tags"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:UntagLogGroup", in.LogGroupName); err != nil {
		return nil, err
	}
	return nil, s.tagGroup(in.LogGroupName, nil, in.Tags)
}

func (s *Service) awsListTagsLogGroup(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName string `json:"logGroupName"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:ListTagsLogGroup", in.LogGroupName); err != nil {
		return nil, err
	}
	tags, err := s.groupTags(in.LogGroupName)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tags": tags}, nil
}

func (s *Service) resourceGroup(arn string) (string, error) {
	if !strings.HasPrefix(arn, "arn:") || !strings.Contains(arn, ":log-group:") {
		return "", logsErr("InvalidParameterException", "resourceArn must be a log group ARN")
	}
	return groupRef(arn, "")
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string            `json:"resourceArn"`
		Tags        map[string]string `json:"tags"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if _, ok, err := s.deliveryTags(q, "logs:TagResource", in.ResourceArn, in.Tags, nil); ok {
		return nil, err
	}
	name, err := s.resourceGroup(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:TagResource", name); err != nil {
		return nil, err
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	return nil, s.tagGroup(name, in.Tags, nil)
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if _, ok, err := s.deliveryTags(q, "logs:UntagResource", in.ResourceArn, nil, in.TagKeys); ok {
		return nil, err
	}
	name, err := s.resourceGroup(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:UntagResource", name); err != nil {
		return nil, err
	}
	return nil, s.tagGroup(name, nil, in.TagKeys)
}

func (s *Service) awsListTagsForResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if tags, ok, err := s.deliveryTags(q, "logs:ListTagsForResource", in.ResourceArn, nil, nil); ok {
		if err != nil {
			return nil, err
		}
		return map[string]any{"tags": tags}, nil
	}
	name, err := s.resourceGroup(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:ListTagsForResource", name); err != nil {
		return nil, err
	}
	tags, err := s.groupTags(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tags": tags}, nil
}

// ---- Logs Insights ----

type insightsRun struct {
	ID      string
	Query   string
	Group   string
	Status  string
	Created time.Time
	Results [][]map[string]string
	Matched int
	Scanned int
	Bytes   int
}

type queryRegistry struct {
	mu   sync.Mutex
	runs map[string]*insightsRun
	ids  []string
}

var queries = &queryRegistry{runs: map[string]*insightsRun{}}

func (r *queryRegistry) add(x *insightsRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[x.ID] = x
	r.ids = append(r.ids, x.ID)
	for len(r.ids) > 200 {
		delete(r.runs, r.ids[0])
		r.ids = r.ids[1:]
	}
}

func (r *queryRegistry) get(id string) *insightsRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}

type startQueryIn struct {
	QueryLanguage       string   `json:"queryLanguage"`
	LogGroupName        string   `json:"logGroupName"`
	LogGroupNames       []string `json:"logGroupNames"`
	LogGroupIdentifiers []string `json:"logGroupIdentifiers"`
	StartTime           int64    `json:"startTime"`
	EndTime             int64    `json:"endTime"`
	QueryString         string   `json:"queryString"`
	Limit               int      `json:"limit"`
}

func (s *Service) awsStartQuery(q *awsapi.Req) (any, error) {
	var in startQueryIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.startQuery(q, in)
}

func (s *Service) startQuery(q authorizer, in startQueryIn) (any, error) {
	var names []string
	if in.LogGroupName != "" {
		names = append(names, in.LogGroupName)
	}
	names = append(names, in.LogGroupNames...)
	for _, id := range in.LogGroupIdentifiers {
		n, _ := groupRef(id, "")
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil, logsErr("InvalidParameterException", "specify logGroupName, logGroupNames or logGroupIdentifiers")
	}
	for _, n := range names {
		if err := s.authGroup(q, "logs:StartQuery", n); err != nil {
			return nil, err
		}
	}
	if in.QueryLanguage != "" && in.QueryLanguage != "CWLI" {
		return nil, logsErr("InvalidParameterException", "HomeCloud supports the CloudWatch Logs Insights query language (CWLI) only")
	}
	if in.EndTime < in.StartTime {
		return nil, logsErr("InvalidParameterException", "endTime must not be before startTime")
	}
	iq, err := ParseInsights(in.QueryString)
	if err != nil {
		return nil, logsErr("MalformedQueryException", "%v", err)
	}
	if in.Limit < 0 || in.Limit > 10000 {
		return nil, logsErr("InvalidParameterException", "limit must be 1-10000")
	}
	start, end := time.Unix(in.StartTime, 0).UTC(), time.Unix(in.EndTime, 0).UTC().Add(999*time.Millisecond)
	var events []LogEvent
	var groups []string
	for _, n := range names {
		g, ok := s.knownGroup(n)
		if !ok {
			return nil, logsErr("ResourceNotFoundException", "Log group %q does not exist", n)
		}
		ev, err := s.groupEvents(g, nil, start, end, nil)
		if err != nil {
			return nil, err
		}
		events = append(events, ev...)
		for range ev {
			groups = append(groups, n)
		}
	}
	rows, matched, err := iq.Run(events, groups, s.env.AccountID, in.Limit)
	if err != nil {
		return nil, logsErr("MalformedQueryException", "%v", err)
	}
	run := &insightsRun{ID: awsapi.RequestID(), Query: in.QueryString, Group: names[0], Status: "Complete", Created: time.Now(),
		Matched: matched, Scanned: len(events)}
	for _, e := range events {
		run.Bytes += len(e.Message)
	}
	for _, row := range rows {
		fields := make([]map[string]string, 0, len(row))
		for _, f := range row {
			fields = append(fields, map[string]string{"field": f[0], "value": f[1]})
		}
		run.Results = append(run.Results, fields)
	}
	queries.add(run)
	return map[string]string{"queryId": run.ID}, nil
}

func (s *Service) awsGetQueryResults(q *awsapi.Req) (any, error) {
	var in struct {
		QueryID string `json:"queryId"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.queryResults(q, in.QueryID)
}

func (s *Service) queryResults(q authorizer, id string) (any, error) {
	run := queries.get(id)
	if run == nil {
		return nil, logsErr("ResourceNotFoundException", "query %s does not exist", id)
	}
	if err := s.authGroup(q, "logs:GetQueryResults", run.Group); err != nil {
		return nil, err
	}
	results := run.Results
	if results == nil {
		results = [][]map[string]string{}
	}
	return map[string]any{"queryLanguage": "CWLI", "status": run.Status, "results": results,
		"statistics": map[string]float64{"recordsMatched": float64(run.Matched), "recordsScanned": float64(run.Scanned), "bytesScanned": float64(run.Bytes)}}, nil
}

func (s *Service) awsStopQuery(q *awsapi.Req) (any, error) {
	var in struct {
		QueryID string `json:"queryId"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	run := queries.get(in.QueryID)
	if run == nil {
		return nil, logsErr("ResourceNotFoundException", "query %s does not exist", in.QueryID)
	}
	if err := s.authGroup(q, "logs:StopQuery", run.Group); err != nil {
		return nil, err
	}
	// Queries run to completion when they start, so there is never one to stop.
	return map[string]bool{"success": false}, nil
}

func (s *Service) awsDescribeQueries(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName string `json:"logGroupName"`
		Status       string `json:"status"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:DescribeQueries", s.groupARN("*")); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	queries.mu.Lock()
	for i := len(queries.ids) - 1; i >= 0; i-- {
		r := queries.runs[queries.ids[i]]
		if (in.LogGroupName != "" && r.Group != in.LogGroupName) || (in.Status != "" && in.Status != r.Status) {
			continue
		}
		out = append(out, map[string]any{"queryId": r.ID, "queryString": r.Query, "status": r.Status, "createTime": r.Created.UnixMilli(),
			"logGroupName": r.Group, "queryLanguage": "CWLI"})
	}
	queries.mu.Unlock()
	return map[string]any{"queries": out}, nil
}
