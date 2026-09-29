package cloudwatch

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

func (s *Service) sortedAlarms() []Alarm {
	as := store.List[Alarm](s.env.Store, cAlarms)
	sort.Slice(as, func(i, j int) bool { return as[i].Name < as[j].Name })
	return as
}

func (s *Service) awsDescribeAlarms(q *awsapi.Req) (any, error) {
	var in struct {
		AlarmNames      []string
		AlarmNamePrefix string
		AlarmTypes      []string
		StateValue      string
		ActionPrefix    string
		MaxRecords      int
		NextToken       string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:DescribeAlarms", s.alarmARN("*")); err != nil {
		return nil, err
	}
	if len(in.AlarmNames) > 0 && in.AlarmNamePrefix != "" {
		return nil, cwErr(http.StatusBadRequest, "InvalidParameterCombination", "AlarmNames and AlarmNamePrefix are mutually exclusive")
	}
	if in.MaxRecords <= 0 || in.MaxRecords > 100 {
		in.MaxRecords = 100
	}
	after := ""
	if in.NextToken != "" {
		var err error
		if after, err = fromToken(in.NextToken); err != nil {
			return nil, cwErr(http.StatusBadRequest, "InvalidNextToken", "invalid NextToken")
		}
	}
	wantMetric := len(in.AlarmTypes) == 0 || slices.Contains(in.AlarmTypes, "MetricAlarm")
	out := struct {
		MetricAlarms    []awsMetricAlarm
		CompositeAlarms []struct{}
		NextToken       string `json:",omitempty"`
	}{MetricAlarms: []awsMetricAlarm{}, CompositeAlarms: []struct{}{}}
	if !wantMetric {
		return out, nil
	}
	for _, a := range s.sortedAlarms() {
		switch {
		case after != "" && a.Name <= after:
			continue
		case len(in.AlarmNames) > 0 && !slices.Contains(in.AlarmNames, a.Name):
			continue
		case in.AlarmNamePrefix != "" && !strings.HasPrefix(a.Name, in.AlarmNamePrefix):
			continue
		case in.StateValue != "" && a.State != in.StateValue:
			continue
		case in.ActionPrefix != "" && !slices.ContainsFunc(slices.Concat(a.AlarmActions, a.OKActions, a.InsufficientDataActions),
			func(x string) bool { return strings.HasPrefix(x, in.ActionPrefix) }):
			continue
		}
		if len(out.MetricAlarms) == in.MaxRecords {
			out.NextToken = pageToken(out.MetricAlarms[len(out.MetricAlarms)-1].AlarmName)
			break
		}
		out.MetricAlarms = append(out.MetricAlarms, toAWSAlarm(a))
	}
	return out, nil
}

func (s *Service) awsDescribeAlarmsForMetric(q *awsapi.Req) (any, error) {
	var in struct {
		MetricName        string
		Namespace         string
		Statistic         string
		ExtendedStatistic string
		Dimensions        []Dimension
		Period            int
		Unit              string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:DescribeAlarmsForMetric", "*"); err != nil {
		return nil, err
	}
	if in.MetricName == "" || in.Namespace == "" {
		return nil, missingParam("MetricName and Namespace")
	}
	dims := dimMap(in.Dimensions)
	out := []awsMetricAlarm{}
	for _, a := range s.sortedAlarms() {
		if a.Metric != in.MetricName || a.Namespace != in.Namespace ||
			(in.Statistic != "" && a.Statistic != in.Statistic) || (in.ExtendedStatistic != "" && a.ExtendedStatistic != in.ExtendedStatistic) ||
			(in.Period != 0 && a.Period != in.Period) || (in.Unit != "" && a.Unit != in.Unit) ||
			(len(in.Dimensions) > 0 && seriesKey("", "", dims) != seriesKey("", "", a.Dimensions)) {
			continue
		}
		out = append(out, toAWSAlarm(a))
	}
	return struct{ MetricAlarms []awsMetricAlarm }{out}, nil
}

func (s *Service) awsDeleteAlarms(q *awsapi.Req) (any, error) {
	var in struct{ AlarmNames []string }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if len(in.AlarmNames) == 0 || len(in.AlarmNames) > 100 {
		return nil, missingParam("AlarmNames")
	}
	for _, n := range in.AlarmNames {
		if err := q.Authorize("cloudwatch:DeleteAlarms", s.alarmARN(n)); err != nil {
			return nil, err
		}
	}
	for _, n := range in.AlarmNames {
		if !store.Has(s.env.Store, cAlarms, n) {
			return nil, alarmNotFound(n)
		}
	}
	for _, n := range in.AlarmNames {
		_ = s.DeleteAlarm(n)
	}
	return nil, nil
}

func (s *Service) awsSetAlarmState(q *awsapi.Req) (any, error) {
	var in struct{ AlarmName, StateValue, StateReason, StateReasonData string }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:SetAlarmState", s.alarmARN(in.AlarmName)); err != nil {
		return nil, err
	}
	if in.StateReason == "" {
		return nil, missingParam("StateReason")
	}
	if !slices.Contains([]string{"OK", "ALARM", "INSUFFICIENT_DATA"}, in.StateValue) {
		return nil, invalid("StateValue must be OK, ALARM or INSUFFICIENT_DATA")
	}
	if in.StateReasonData != "" && !json.Valid([]byte(in.StateReasonData)) {
		return nil, cwErr(http.StatusBadRequest, "InvalidFormat", "StateReasonData must be JSON")
	}
	a, err := store.Get[Alarm](s.env.Store, cAlarms, in.AlarmName)
	if err != nil {
		return nil, alarmNotFound(in.AlarmName)
	}
	if a.State != in.StateValue {
		s.setAlarmState(a, in.StateValue, in.StateReason, in.StateReasonData)
	} else {
		_, _ = store.Update(s.env.Store, cAlarms, a.Name, func(x *Alarm) error {
			x.StateReason, x.StateReasonData = in.StateReason, in.StateReasonData
			return nil
		})
	}
	return nil, nil
}

func (s *Service) setActions(q *awsapi.Req, action string, enabled bool) (any, error) {
	var in struct{ AlarmNames []string }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if len(in.AlarmNames) == 0 || len(in.AlarmNames) > 100 {
		return nil, missingParam("AlarmNames")
	}
	for _, n := range in.AlarmNames {
		if err := q.Authorize(action, s.alarmARN(n)); err != nil {
			return nil, err
		}
	}
	for _, n := range in.AlarmNames {
		_, _ = store.Update(s.env.Store, cAlarms, n, func(a *Alarm) error { a.ActionsEnabled = &enabled; return nil })
	}
	return nil, nil
}

func (s *Service) awsEnableAlarmActions(q *awsapi.Req) (any, error) {
	return s.setActions(q, "cloudwatch:EnableAlarmActions", true)
}

func (s *Service) awsDisableAlarmActions(q *awsapi.Req) (any, error) {
	return s.setActions(q, "cloudwatch:DisableAlarmActions", false)
}

type awsHistoryItem struct {
	AlarmName       string
	AlarmType       string
	Timestamp       awsapi.Time
	HistoryItemType string
	HistorySummary  string
	HistoryData     string
}

func (s *Service) awsDescribeAlarmHistory(q *awsapi.Req) (any, error) {
	var in struct {
		AlarmName       string
		AlarmTypes      []string
		HistoryItemType string
		StartDate       *awsapi.Time
		EndDate         *awsapi.Time
		MaxRecords      int
		NextToken       string
		ScanBy          string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	res := s.alarmARN("*")
	if in.AlarmName != "" {
		res = s.alarmARN(in.AlarmName)
	}
	if err := q.Authorize("cloudwatch:DescribeAlarmHistory", res); err != nil {
		return nil, err
	}
	if in.MaxRecords <= 0 || in.MaxRecords > 100 {
		in.MaxRecords = 100
	}
	var items []AlarmHistoryItem
	if in.AlarmName != "" {
		items = s.history(in.AlarmName)
	} else {
		for _, h := range store.List[[]AlarmHistoryItem](s.env.Store, cAlarmHistory) {
			items = append(items, h...)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Timestamp.Before(items[j].Timestamp) })
	if in.ScanBy != "TimestampAscending" {
		slices.Reverse(items)
	}
	start := 0
	if in.NextToken != "" {
		t, err := fromToken(in.NextToken)
		if err == nil {
			start, err = strconv.Atoi(t)
		}
		if err != nil {
			return nil, cwErr(http.StatusBadRequest, "InvalidNextToken", "invalid NextToken")
		}
	}
	out := struct {
		AlarmHistoryItems []awsHistoryItem
		NextToken         string `json:",omitempty"`
	}{AlarmHistoryItems: []awsHistoryItem{}}
	n := 0
	for _, it := range items {
		if (in.HistoryItemType != "" && it.HistoryItemType != in.HistoryItemType) ||
			(in.StartDate != nil && it.Timestamp.Before(in.StartDate.Time)) || (in.EndDate != nil && it.Timestamp.After(in.EndDate.Time)) {
			continue
		}
		n++
		if n <= start {
			continue
		}
		if len(out.AlarmHistoryItems) == in.MaxRecords {
			out.NextToken = pageToken(strconv.Itoa(start + in.MaxRecords))
			break
		}
		out.AlarmHistoryItems = append(out.AlarmHistoryItems, awsHistoryItem{AlarmName: it.AlarmName, AlarmType: "MetricAlarm",
			Timestamp: awsapi.Time{Time: it.Timestamp}, HistoryItemType: it.HistoryItemType, HistorySummary: it.HistorySummary, HistoryData: it.HistoryData})
	}
	return out, nil
}

// ---- tags ----

// taggable resolves an alarm or dashboard ARN to its store collection and key.
func (s *Service) taggable(arn string) (coll, key string, err error) {
	switch {
	case strings.HasPrefix(arn, s.alarmARN("")):
		coll, key = cAlarms, strings.TrimPrefix(arn, s.alarmARN(""))
	case strings.HasPrefix(arn, s.dashboardARN("")):
		coll, key = cDashboards, strings.TrimPrefix(arn, s.dashboardARN(""))
	default:
		return "", "", cwErr(http.StatusBadRequest, "InvalidParameterValue", "ResourceARN must be an alarm or dashboard ARN in this account")
	}
	if !store.Has(s.env.Store, coll, key) {
		return "", "", cwErr(http.StatusNotFound, "ResourceNotFoundException", "%s does not exist", arn)
	}
	return coll, key, nil
}

func (s *Service) updateTags(coll, key string, fn func(core.Tags)) error {
	var err error
	switch coll {
	case cAlarms:
		_, err = store.Update(s.env.Store, cAlarms, key, func(a *Alarm) error {
			if a.Tags == nil {
				a.Tags = core.Tags{}
			}
			fn(a.Tags)
			return nil
		})
	default:
		_, err = store.Update(s.env.Store, cDashboards, key, func(d *Dashboard) error {
			if d.Tags == nil {
				d.Tags = core.Tags{}
			}
			fn(d.Tags)
			return nil
		})
	}
	return err
}

func (s *Service) awsCWTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceARN string
		Tags        []awsTag
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:TagResource", in.ResourceARN); err != nil {
		return nil, err
	}
	coll, key, err := s.taggable(in.ResourceARN)
	if err != nil {
		return nil, err
	}
	for _, t := range in.Tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 || strings.HasPrefix(strings.ToLower(t.Key), "aws:") {
			return nil, invalid("invalid tag key %q", t.Key)
		}
	}
	return nil, s.updateTags(coll, key, func(tags core.Tags) {
		for _, t := range in.Tags {
			tags[t.Key] = t.Value
		}
	})
}

func (s *Service) awsCWUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceARN string
		TagKeys     []string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:UntagResource", in.ResourceARN); err != nil {
		return nil, err
	}
	coll, key, err := s.taggable(in.ResourceARN)
	if err != nil {
		return nil, err
	}
	return nil, s.updateTags(coll, key, func(tags core.Tags) {
		for _, k := range in.TagKeys {
			delete(tags, k)
		}
	})
}

func (s *Service) awsCWListTags(q *awsapi.Req) (any, error) {
	var in struct{ ResourceARN string }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:ListTagsForResource", in.ResourceARN); err != nil {
		return nil, err
	}
	coll, key, err := s.taggable(in.ResourceARN)
	if err != nil {
		return nil, err
	}
	var tags core.Tags
	if coll == cAlarms {
		a, _ := store.Get[Alarm](s.env.Store, cAlarms, key)
		tags = a.Tags
	} else {
		d, _ := store.Get[Dashboard](s.env.Store, cDashboards, key)
		tags = d.Tags
	}
	out := []awsTag{}
	for k, v := range tags {
		out = append(out, awsTag{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return struct{ Tags []awsTag }{out}, nil
}

// ---- dashboards (stored for tools; the console does not render them) ----

const cDashboards = "cloudwatch_dashboards"

type Dashboard struct {
	Name         string    `json:"name"`
	Body         string    `json:"body"`
	LastModified time.Time `json:"last_modified"`
	Tags         core.Tags `json:"tags,omitempty"`
}

var dashboardNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)

func (s *Service) awsPutDashboard(q *awsapi.Req) (any, error) {
	var in struct{ DashboardName, DashboardBody string }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:PutDashboard", s.dashboardARN(in.DashboardName)); err != nil {
		return nil, err
	}
	if !dashboardNameRe.MatchString(in.DashboardName) {
		return nil, cwErr(http.StatusBadRequest, "InvalidParameterInput", "dashboard names are 1-255 letters, digits, - or _")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(in.DashboardBody), &body); err != nil {
		return nil, cwErr(http.StatusBadRequest, "InvalidParameterInput", "The field DashboardBody must be a JSON object: %v", err)
	}
	if _, ok := body["widgets"].([]any); !ok {
		return nil, cwErr(http.StatusBadRequest, "InvalidParameterInput", "The dashboard body must have a widgets array")
	}
	if len(in.DashboardBody) > 1<<20 {
		return nil, cwErr(http.StatusBadRequest, "InvalidParameterInput", "The dashboard body exceeds 1 MB")
	}
	d := Dashboard{Name: in.DashboardName, Body: in.DashboardBody, LastModified: core.Now()}
	if old, err := store.Get[Dashboard](s.env.Store, cDashboards, d.Name); err == nil {
		d.Tags = old.Tags
	}
	if err := store.Put(s.env.Store, cDashboards, d.Name, d); err != nil {
		return nil, err
	}
	return struct {
		DashboardValidationMessages []struct{ DataPath, Message string }
	}{[]struct{ DataPath, Message string }{}}, nil
}

func (s *Service) awsGetDashboard(q *awsapi.Req) (any, error) {
	var in struct{ DashboardName string }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:GetDashboard", s.dashboardARN(in.DashboardName)); err != nil {
		return nil, err
	}
	d, err := store.Get[Dashboard](s.env.Store, cDashboards, in.DashboardName)
	if err != nil {
		return nil, cwErr(http.StatusNotFound, "ResourceNotFound", "Dashboard %s does not exist", in.DashboardName)
	}
	return struct{ DashboardArn, DashboardBody, DashboardName string }{s.dashboardARN(d.Name), d.Body, d.Name}, nil
}

func (s *Service) awsListDashboards(q *awsapi.Req) (any, error) {
	var in struct{ DashboardNamePrefix, NextToken string }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:ListDashboards", "*"); err != nil {
		return nil, err
	}
	type entry struct {
		DashboardName string
		DashboardArn  string
		LastModified  awsapi.Time
		Size          int64
	}
	ds := store.List[Dashboard](s.env.Store, cDashboards)
	sort.Slice(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
	out := []entry{}
	for _, d := range ds {
		if strings.HasPrefix(d.Name, in.DashboardNamePrefix) {
			out = append(out, entry{d.Name, s.dashboardARN(d.Name), awsapi.Time{Time: d.LastModified}, int64(len(d.Body))})
		}
	}
	return struct{ DashboardEntries []entry }{out}, nil
}

func (s *Service) awsDeleteDashboards(q *awsapi.Req) (any, error) {
	var in struct{ DashboardNames []string }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if len(in.DashboardNames) == 0 {
		return nil, missingParam("DashboardNames")
	}
	for _, n := range in.DashboardNames {
		if err := q.Authorize("cloudwatch:DeleteDashboards", s.dashboardARN(n)); err != nil {
			return nil, err
		}
	}
	for _, n := range in.DashboardNames {
		if !store.Has(s.env.Store, cDashboards, n) {
			return nil, cwErr(http.StatusNotFound, "ResourceNotFound", "Dashboard %s does not exist", n)
		}
	}
	for _, n := range in.DashboardNames {
		_ = store.Delete(s.env.Store, cDashboards, n)
	}
	return nil, nil
}
