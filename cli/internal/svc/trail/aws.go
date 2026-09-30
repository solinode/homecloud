package trail

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// The AWS CloudTrail API (awsJson 1.1): LookupEvents over the audit log, and
// trails that deliver the log to S3.

// RegisterAWS serves CloudTrail over the AWS protocol.
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{Name: "cloudtrail", JSONPrefix: "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101", JSONVersion: "1.1",
		ErrorCode: map[string]string{"ResourceNotFound": "TrailNotFoundException", "TrailAlreadyExists": "TrailAlreadyExistsException",
			"S3BucketDoesNotExist": "S3BucketDoesNotExistException", "BadRequest": "InvalidParameterCombinationException", "ValidationError": "InvalidParameterCombinationException"},
		Ops: map[string]awsapi.Op{
			"LookupEvents":        s.awsLookupEvents,
			"CreateTrail":         s.awsCreateTrail,
			"DescribeTrails":      s.awsDescribeTrails,
			"GetTrail":            s.awsGetTrail,
			"ListTrails":          s.awsListTrails,
			"UpdateTrail":         s.awsUpdateTrail,
			"DeleteTrail":         s.awsDeleteTrail,
			"GetTrailStatus":      s.awsGetTrailStatus,
			"StartLogging":        s.awsStartStop,
			"StopLogging":         s.awsStartStop,
			"AddTags":             s.awsAddTags,
			"RemoveTags":          s.awsRemoveTags,
			"ListTags":            s.awsListTags,
			"GetEventSelectors":   s.awsGetEventSelectors,
			"PutEventSelectors":   s.awsPutEventSelectors,
			"GetInsightSelectors": s.awsGetInsightSelectors,
		}})
}

func trailErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

type tag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

func tagsOf(l []tag) core.Tags {
	if len(l) == 0 {
		return nil
	}
	t := core.Tags{}
	for _, x := range l {
		t[x.Key] = x.Value
	}
	return t
}

func tagList(t core.Tags) []tag {
	out := make([]tag, 0, len(t))
	for k, v := range t {
		out = append(out, tag{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ---- LookupEvents ----

func (s *Service) awsLookupEvents(q *awsapi.Req) (any, error) {
	var in struct {
		LookupAttributes []struct{ AttributeKey, AttributeValue string }
		StartTime        *float64
		EndTime          *float64
		EventCategory    string
		MaxResults       *int
		NextToken        string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:LookupEvents", "*"); err != nil {
		return nil, err
	}
	limit := 50
	if in.MaxResults != nil {
		if *in.MaxResults < 1 || *in.MaxResults > 50 {
			return nil, trailErr("InvalidMaxResultsException", "MaxResults must be between 1 and 50")
		}
		limit = *in.MaxResults
	}
	var f Filter
	for _, a := range in.LookupAttributes {
		if a.AttributeValue == "" {
			return nil, trailErr("InvalidLookupAttributesException", "AttributeValue for %s must not be empty", a.AttributeKey)
		}
		switch a.AttributeKey {
		case "EventId":
			f.EventID = a.AttributeValue
		case "EventName":
			f.EventName = a.AttributeValue
		case "Username":
			f.User = a.AttributeValue
		case "ResourceName":
			f.ResourceName = a.AttributeValue
		case "ResourceType":
			f.ResourceType = a.AttributeValue
		case "EventSource":
			f.EventSource = a.AttributeValue
		case "AccessKeyId":
			f.AccessKey = a.AttributeValue
		case "ReadOnly":
			v := strings.EqualFold(a.AttributeValue, "true")
			if !v && !strings.EqualFold(a.AttributeValue, "false") {
				return nil, trailErr("InvalidLookupAttributesException", "ReadOnly must be true or false")
			}
			f.ReadOnly = &v
		default:
			return nil, trailErr("InvalidLookupAttributesException", "unsupported lookup attribute %q", a.AttributeKey)
		}
	}
	if in.StartTime != nil {
		f.Start = fromEpoch(*in.StartTime)
	}
	if in.EndTime != nil {
		f.End = fromEpoch(*in.EndTime)
	}
	if !f.Start.IsZero() && !f.End.IsZero() && f.Start.After(f.End) {
		return nil, trailErr("InvalidTimeRangeException", "StartTime must not be after EndTime")
	}
	if in.EventCategory != "" && in.EventCategory != "insight" {
		return nil, trailErr("InvalidEventCategoryException", "EventCategory must be insight")
	}
	out := []map[string]any{}
	var next string
	if in.EventCategory != "insight" { // insights are not generated
		evs, n, err := s.Search(f, in.NextToken, limit)
		if err != nil {
			return nil, trailErr("InvalidNextTokenException", "invalid NextToken")
		}
		next = n
		for _, e := range evs {
			res := []map[string]string{}
			for _, r := range e.Resources() {
				res = append(res, map[string]string{"ResourceType": r.Type, "ResourceName": r.Name})
			}
			m := map[string]any{"EventId": e.ID, "EventName": e.Name(), "ReadOnly": boolStr(e.ReadOnly()), "EventTime": awsapi.Epoch(e.Time),
				"EventSource": e.Source(), "Username": e.User, "Resources": res, "CloudTrailEvent": e.RecordJSON(q.Account)}
			if e.AccessKey != "" {
				m["AccessKeyId"] = e.AccessKey
			}
			out = append(out, m)
		}
	}
	res := map[string]any{"Events": out}
	if next != "" {
		res["NextToken"] = next
	}
	return res, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func fromEpoch(f float64) time.Time { return time.UnixMilli(int64(f * 1000)).UTC() }

// ---- trails ----

func (s *Service) view(t Trail) map[string]any {
	m := map[string]any{"Name": t.Name, "S3BucketName": t.S3Bucket, "IncludeGlobalServiceEvents": t.IncludeGlobal,
		"IsMultiRegionTrail": t.MultiRegion, "HomeRegion": core.Region, "TrailARN": s.ARN(t.Name), "LogFileValidationEnabled": t.LogValidation,
		"HasCustomEventSelectors": t.EventSelectors != nil, "HasInsightSelectors": false, "IsOrganizationTrail": t.Organization}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("S3KeyPrefix", t.S3Prefix)
	set("SnsTopicName", t.SNSTopic)
	set("SnsTopicARN", t.SNSTopic)
	set("CloudWatchLogsLogGroupArn", t.CWLogsGroup)
	set("CloudWatchLogsRoleArn", t.CWLogsRole)
	set("KmsKeyId", t.KMSKey)
	return m
}

type trailInput struct {
	Name                       string
	S3BucketName               string
	S3KeyPrefix                string
	SnsTopicName               string
	IncludeGlobalServiceEvents *bool
	IsMultiRegionTrail         *bool
	EnableLogFileValidation    *bool
	CloudWatchLogsLogGroupArn  string
	CloudWatchLogsRoleArn      string
	KmsKeyId                   string
	IsOrganizationTrail        *bool
	TagsList                   []tag
}

func (s *Service) awsCreateTrail(q *awsapi.Req) (any, error) {
	var in trailInput
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:CreateTrail", s.ARN(in.Name)); err != nil {
		return nil, err
	}
	if !ValidName(in.Name) {
		return nil, trailErr("InvalidTrailNameException", "trail name must be 3-128 characters: letters, numbers, periods, underscores and hyphens")
	}
	if in.S3BucketName == "" {
		return nil, trailErr("InvalidParameterCombinationException", "S3BucketName is required")
	}
	t := Trail{Name: in.Name, S3Bucket: in.S3BucketName, S3Prefix: in.S3KeyPrefix, SNSTopic: in.SnsTopicName, IncludeGlobal: in.IncludeGlobalServiceEvents == nil || *in.IncludeGlobalServiceEvents,
		MultiRegion: flag(in.IsMultiRegionTrail), LogValidation: flag(in.EnableLogFileValidation), CWLogsGroup: in.CloudWatchLogsLogGroupArn,
		CWLogsRole: in.CloudWatchLogsRoleArn, KMSKey: in.KmsKeyId, Organization: flag(in.IsOrganizationTrail), Tags: tagsOf(in.TagsList)}
	t, err := s.Create(t)
	if err != nil {
		return nil, err
	}
	return s.view(t), nil
}

func flag(b *bool) bool { return b != nil && *b }

func (s *Service) awsUpdateTrail(q *awsapi.Req) (any, error) {
	var in trailInput
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name := lastOf(in.Name)
	if err := q.Authorize("cloudtrail:UpdateTrail", s.ARN(name)); err != nil {
		return nil, err
	}
	old, err := s.Trail(in.Name)
	if err != nil {
		return nil, err
	}
	if in.S3BucketName != "" && in.S3BucketName != old.S3Bucket {
		if err := s.checkBucket(in.S3BucketName); err != nil {
			return nil, err
		}
	}
	t, err := s.Modify(old.Name, func(t *Trail) error {
		if in.S3BucketName != "" {
			t.S3Bucket = in.S3BucketName
		}
		if in.S3KeyPrefix != "" {
			t.S3Prefix = in.S3KeyPrefix
		}
		if in.SnsTopicName != "" {
			t.SNSTopic = in.SnsTopicName
		}
		if in.IncludeGlobalServiceEvents != nil {
			t.IncludeGlobal = *in.IncludeGlobalServiceEvents
		}
		if in.IsMultiRegionTrail != nil {
			t.MultiRegion = *in.IsMultiRegionTrail
		}
		if in.EnableLogFileValidation != nil {
			t.LogValidation = *in.EnableLogFileValidation
		}
		if in.IsOrganizationTrail != nil {
			t.Organization = *in.IsOrganizationTrail
		}
		if in.CloudWatchLogsLogGroupArn != "" {
			t.CWLogsGroup = in.CloudWatchLogsLogGroupArn
		}
		if in.CloudWatchLogsRoleArn != "" {
			t.CWLogsRole = in.CloudWatchLogsRoleArn
		}
		if in.KmsKeyId != "" {
			t.KMSKey = in.KmsKeyId
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.view(t), nil
}

// lastOf turns a trail ARN into its name for authorization.
func lastOf(ref string) string {
	if i := strings.Index(ref, ":trail/"); i >= 0 {
		return ref[i+len(":trail/"):]
	}
	return ref
}

func (s *Service) awsDescribeTrails(q *awsapi.Req) (any, error) {
	var in struct{ TrailNameList []string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:DescribeTrails", "*"); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	if len(in.TrailNameList) == 0 {
		for _, t := range s.Trails() {
			out = append(out, s.view(t))
		}
	}
	for _, n := range in.TrailNameList {
		if t, err := s.Trail(n); err == nil {
			out = append(out, s.view(t))
		}
	}
	return map[string]any{"trailList": out}, nil
}

func (s *Service) awsGetTrail(q *awsapi.Req) (any, error) {
	var in struct{ Name string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:GetTrail", s.ARN(lastOf(in.Name))); err != nil {
		return nil, err
	}
	t, err := s.Trail(in.Name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Trail": s.view(t)}, nil
}

func (s *Service) awsListTrails(q *awsapi.Req) (any, error) {
	if err := q.Authorize("cloudtrail:ListTrails", "*"); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, t := range s.Trails() {
		out = append(out, map[string]any{"Name": t.Name, "TrailARN": s.ARN(t.Name), "HomeRegion": core.Region})
	}
	return map[string]any{"Trails": out}, nil
}

func (s *Service) awsDeleteTrail(q *awsapi.Req) (any, error) {
	var in struct{ Name string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:DeleteTrail", s.ARN(lastOf(in.Name))); err != nil {
		return nil, err
	}
	t, err := s.Trail(in.Name)
	if err != nil {
		return nil, err
	}
	return nil, s.Delete(t.Name)
}

func (s *Service) awsGetTrailStatus(q *awsapi.Req) (any, error) {
	var in struct{ Name string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:GetTrailStatus", s.ARN(lastOf(in.Name))); err != nil {
		return nil, err
	}
	t, err := s.Trail(in.Name)
	if err != nil {
		return nil, err
	}
	m := map[string]any{"IsLogging": t.Logging, "LatestDeliveryAttemptSucceeded": "", "LatestNotificationAttemptSucceeded": ""}
	setTime := func(k string, v time.Time) {
		if !v.IsZero() {
			m[k] = awsapi.Epoch(v)
		}
	}
	setTime("StartLoggingTime", t.StartedAt)
	setTime("StopLoggingTime", t.StoppedAt)
	setTime("LatestDeliveryTime", t.LastDelivery)
	setTime("LatestDeliveryAttemptTime", t.LastAttempt)
	if !t.LastAttempt.IsZero() {
		m["LatestDeliveryAttemptTime"] = t.LastAttempt.Format("2006-01-02T15:04:05Z")
		if t.LastDeliveryError == "" {
			m["LatestDeliveryAttemptSucceeded"] = m["LatestDeliveryAttemptTime"]
		}
	}
	if t.LastDeliveryError != "" {
		m["LatestDeliveryError"] = t.LastDeliveryError
	}
	if !t.StartedAt.IsZero() {
		m["TimeLoggingStarted"] = t.StartedAt.Format("2006-01-02T15:04:05Z")
	}
	if !t.StoppedAt.IsZero() {
		m["TimeLoggingStopped"] = t.StoppedAt.Format("2006-01-02T15:04:05Z")
	}
	return m, nil
}

func (s *Service) awsStartStop(q *awsapi.Req) (any, error) {
	var in struct{ Name string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:"+q.Op, s.ARN(lastOf(in.Name))); err != nil {
		return nil, err
	}
	t, err := s.Trail(in.Name)
	if err != nil {
		return nil, err
	}
	if q.Op == "StartLogging" {
		return nil, s.Start(t.Name)
	}
	return nil, s.Stop(t.Name)
}

// ---- event selectors ----

func (s *Service) awsGetEventSelectors(q *awsapi.Req) (any, error) {
	var in struct{ TrailName string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:GetEventSelectors", s.ARN(lastOf(in.TrailName))); err != nil {
		return nil, err
	}
	t, err := s.Trail(in.TrailName)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"TrailARN": s.ARN(t.Name)}
	if t.EventSelectors != nil {
		out["EventSelectors"] = t.EventSelectors
	} else {
		out["EventSelectors"] = []any{map[string]any{"ReadWriteType": "All", "IncludeManagementEvents": true, "DataResources": []any{}}}
	}
	return out, nil
}

func (s *Service) awsPutEventSelectors(q *awsapi.Req) (any, error) {
	var in struct {
		TrailName      string
		EventSelectors []any
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:PutEventSelectors", s.ARN(lastOf(in.TrailName))); err != nil {
		return nil, err
	}
	t, err := s.Trail(in.TrailName)
	if err != nil {
		return nil, err
	}
	// Selectors are stored and returned; every management event is still recorded.
	if _, err := s.Modify(t.Name, func(t *Trail) error { t.EventSelectors = in.EventSelectors; return nil }); err != nil {
		return nil, err
	}
	return map[string]any{"TrailARN": s.ARN(t.Name), "EventSelectors": in.EventSelectors}, nil
}

func (s *Service) awsGetInsightSelectors(q *awsapi.Req) (any, error) {
	var in struct{ TrailName string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:GetInsightSelectors", s.ARN(lastOf(in.TrailName))); err != nil {
		return nil, err
	}
	if _, err := s.Trail(in.TrailName); err != nil {
		return nil, err
	}
	return nil, trailErr("InsightNotEnabledException", "Insights are not enabled on this trail")
}

// ---- tags ----

func (s *Service) awsAddTags(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceId string
		TagsList   []tag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:AddTags", s.ARN(lastOf(in.ResourceId))); err != nil {
		return nil, err
	}
	t, err := s.Trail(in.ResourceId)
	if err != nil {
		return nil, err
	}
	_, err = s.Modify(t.Name, func(t *Trail) error {
		if t.Tags == nil {
			t.Tags = core.Tags{}
		}
		if len(t.Tags)+len(in.TagsList) > 50 {
			return trailErr("TagsLimitExceededException", "a trail can have at most 50 tags")
		}
		for _, x := range in.TagsList {
			t.Tags[x.Key] = x.Value
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsRemoveTags(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceId string
		TagsList   []tag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudtrail:RemoveTags", s.ARN(lastOf(in.ResourceId))); err != nil {
		return nil, err
	}
	t, err := s.Trail(in.ResourceId)
	if err != nil {
		return nil, err
	}
	_, err = s.Modify(t.Name, func(t *Trail) error {
		for _, x := range in.TagsList {
			delete(t.Tags, x.Key)
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsListTags(q *awsapi.Req) (any, error) {
	var in struct{ ResourceIdList []string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, id := range in.ResourceIdList {
		if err := q.Authorize("cloudtrail:ListTags", s.ARN(lastOf(id))); err != nil {
			return nil, err
		}
		t, err := s.Trail(id)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"ResourceId": s.ARN(t.Name), "TagsList": tagList(t.Tags)})
	}
	return map[string]any{"ResourceTagList": out}, nil
}
