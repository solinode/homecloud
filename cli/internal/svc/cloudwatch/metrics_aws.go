package cloudwatch

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// CloudWatch metrics, alarms and dashboards over the AWS protocols. Current
// AWS CLIs and SDKs send awsJson 1.0 (X-Amz-Target
// GraniteServiceVersion20100801.*); older SDKs and Terraform providers send
// awsQuery. Both decode into the same structs (awsapi.Req.Decode).

const monitoringNS = "http://monitoring.amazonaws.com/doc/2010-08-01/"

func (s *Service) registerMetricsAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "monitoring", JSONPrefix: "GraniteServiceVersion20100801", JSONVersion: "1.0", XMLNS: monitoringNS,
		ErrorCode: map[string]string{
			"ValidationError":  "InvalidParameterValue",
			"BadRequest":       "InvalidParameterValue",
			"ResourceNotFound": "ResourceNotFound",
			"AlreadyExists":    "InvalidParameterValue",
		},
		Ops: map[string]awsapi.Op{
			"PutMetricData":            s.awsPutMetricData,
			"GetMetricStatistics":      s.awsGetMetricStatistics,
			"GetMetricData":            s.awsGetMetricData,
			"ListMetrics":              s.awsListMetrics,
			"PutMetricAlarm":           s.awsPutMetricAlarm,
			"DescribeAlarms":           s.awsDescribeAlarms,
			"DescribeAlarmsForMetric":  s.awsDescribeAlarmsForMetric,
			"DeleteAlarms":             s.awsDeleteAlarms,
			"SetAlarmState":            s.awsSetAlarmState,
			"EnableAlarmActions":       s.awsEnableAlarmActions,
			"DisableAlarmActions":      s.awsDisableAlarmActions,
			"PutAnomalyDetector":       s.awsPutAnomalyDetector,
			"DescribeAnomalyDetectors": s.awsDescribeAnomalyDetectors,
			"DeleteAnomalyDetector":    s.awsDeleteAnomalyDetector,
			"DescribeAlarmHistory":     s.awsDescribeAlarmHistory,
			"TagResource":              s.awsCWTagResource,
			"UntagResource":            s.awsCWUntagResource,
			"ListTagsForResource":      s.awsCWListTags,
			"PutDashboard":             s.awsPutDashboard,
			"GetDashboard":             s.awsGetDashboard,
			"ListDashboards":           s.awsListDashboards,
			"DeleteDashboards":         s.awsDeleteDashboards,
		},
	})
}

// cwErr is a CloudWatch error; the code doubles as the awsQuery code that
// query-compatible JSON clients read from x-amzn-query-error.
func cwErr(status int, code, format string, a ...any) error {
	return &awsapi.Error{Status: status, Code: code, QueryCode: code, Message: fmt.Sprintf(format, a...)}
}

func invalid(format string, a ...any) error {
	return cwErr(http.StatusBadRequest, "InvalidParameterValue", format, a...)
}

func missingParam(name string) error {
	return cwErr(http.StatusBadRequest, "MissingParameter", "The parameter %s is required.", name)
}

func alarmNotFound(name string) error {
	return cwErr(http.StatusNotFound, "ResourceNotFound", "Alarm %s does not exist", name)
}

var units = map[string]bool{"Seconds": true, "Microseconds": true, "Milliseconds": true, "Bytes": true, "Kilobytes": true, "Megabytes": true,
	"Gigabytes": true, "Terabytes": true, "Bits": true, "Kilobits": true, "Megabits": true, "Gigabits": true, "Terabits": true, "Percent": true,
	"Count": true, "Bytes/Second": true, "Kilobytes/Second": true, "Megabytes/Second": true, "Gigabytes/Second": true, "Terabytes/Second": true,
	"Bits/Second": true, "Kilobits/Second": true, "Megabits/Second": true, "Gigabits/Second": true, "Terabits/Second": true, "Count/Second": true, "None": true}

func checkDims(ds []Dimension) error {
	if len(ds) > 30 {
		return invalid("a metric has at most 30 dimensions")
	}
	seen := map[string]bool{}
	for _, d := range ds {
		if d.Name == "" || d.Value == "" || len(d.Name) > 255 || len(d.Value) > 1024 {
			return invalid("dimension names and values must be 1-255 / 1-1024 characters")
		}
		if seen[d.Name] {
			return invalid("dimension %s appears more than once", d.Name)
		}
		seen[d.Name] = true
	}
	return nil
}

func (s *Service) alarmARN(name string) string { return s.env.ARN("cloudwatch", "alarm:"+name) }

func (s *Service) dashboardARN(name string) string {
	return fmt.Sprintf("arn:%s:cloudwatch::%s:dashboard/%s", core.Partition, s.env.AccountID, name)
}

// ---- metrics ----

type statisticSet struct {
	SampleCount, Sum, Minimum, Maximum float64
}

func (s *Service) awsPutMetricData(q *awsapi.Req) (any, error) {
	var in struct {
		Namespace  string
		MetricData []struct {
			MetricName        string
			Dimensions        []Dimension
			Timestamp         *awsapi.Time
			Value             *float64
			StatisticValues   *statisticSet
			Values            []float64
			Counts            []float64
			Unit              string
			StorageResolution int
		}
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:PutMetricData", "*"); err != nil {
		return nil, err
	}
	if in.Namespace == "" {
		return nil, missingParam("Namespace")
	}
	if strings.HasPrefix(in.Namespace, "AWS/") || strings.HasPrefix(in.Namespace, "HC/") || strings.HasPrefix(in.Namespace, ":") || len(in.Namespace) > 255 {
		return nil, invalid("The value %s for parameter Namespace is invalid (the AWS/ and HC/ prefixes are reserved).", in.Namespace)
	}
	if len(in.MetricData) == 0 || len(in.MetricData) > 1000 {
		return nil, invalid("MetricData must contain 1-1000 items")
	}
	now := time.Now()
	type put struct {
		dims map[string]string
		p    Point
	}
	var puts []func()
	for i, d := range in.MetricData {
		n := i + 1
		if d.MetricName == "" || len(d.MetricName) > 255 {
			return nil, missingParam(fmt.Sprintf("MetricData.member.%d.MetricName", n))
		}
		if err := checkDims(d.Dimensions); err != nil {
			return nil, err
		}
		if d.Unit != "" && !units[d.Unit] {
			return nil, invalid("The value %s for parameter MetricData.member.%d.Unit is invalid.", d.Unit, n)
		}
		switch d.StorageResolution {
		case 0, 60, 1:
		default:
			return nil, invalid("StorageResolution must be 1 or 60")
		}
		t := now.UTC()
		if d.Timestamp != nil {
			t = d.Timestamp.Time
			if t.Before(now.Add(-14*24*time.Hour)) || t.After(now.Add(2*time.Hour)) {
				return nil, invalid("The parameter MetricData.member.%d.Timestamp must specify a time no more than two weeks in the past and no more than two hours in the future.", n)
			}
		}
		given := 0
		for _, b := range []bool{d.Value != nil, d.StatisticValues != nil, len(d.Values) > 0} {
			if b {
				given++
			}
		}
		if given != 1 {
			return nil, cwErr(http.StatusBadRequest, "InvalidParameterCombination", "The parameters MetricData.member.%d.Value, MetricData.member.%d.StatisticValues and MetricData.member.%d.Values are mutually exclusive and you must specify one of them.", n, n, n)
		}
		var pts []Point
		switch {
		case d.Value != nil:
			if !finite(*d.Value) {
				return nil, invalid("MetricData.member.%d.Value must be a finite number", n)
			}
			pts = []Point{{T: t, V: *d.Value}}
		case d.StatisticValues != nil:
			sv := d.StatisticValues
			if sv.SampleCount <= 0 || sv.Minimum > sv.Maximum || !finite(sv.Sum, sv.Minimum, sv.Maximum, sv.SampleCount) {
				return nil, invalid("MetricData.member.%d.StatisticValues is invalid (SampleCount > 0 and Minimum <= Maximum)", n)
			}
			pts = []Point{{T: t, V: sv.Sum / sv.SampleCount, N: sv.SampleCount, S: sv.Sum, Lo: sv.Minimum, Hi: sv.Maximum}}
		default:
			if len(d.Values) > 150 {
				return nil, invalid("MetricData.member.%d.Values has at most 150 values", n)
			}
			if len(d.Counts) > 0 && len(d.Counts) != len(d.Values) {
				return nil, invalid("MetricData.member.%d.Counts must have as many items as Values", n)
			}
			for j, v := range d.Values {
				c := 1.0
				if len(d.Counts) > 0 {
					c = d.Counts[j]
				}
				if !finite(v, c) || c <= 0 {
					return nil, invalid("MetricData.member.%d.Values and Counts must be finite (counts positive)", n)
				}
				pts = append(pts, Point{T: t, V: v, N: c, S: v * c, Lo: v, Hi: v})
			}
		}
		name, dims, unit, hr := d.MetricName, dimMap(d.Dimensions), d.Unit, d.StorageResolution == 1
		for _, p := range pts {
			p := p
			puts = append(puts, func() { s.PutPoint(in.Namespace, name, dims, unit, p, hr) })
		}
	}
	for _, f := range puts { // only after the whole request validated
		f()
	}
	return nil, nil
}

func checkPeriod(period int) error {
	if period <= 0 || (period%60 != 0 && period != 1 && period != 5 && period != 10 && period != 30) {
		return invalid("The parameter Period must be 1, 5, 10, 30 or a multiple of 60.")
	}
	return nil
}

type awsDatapoint struct {
	Timestamp          awsapi.Time
	SampleCount        *float64           `json:",omitempty"`
	Average            *float64           `json:",omitempty"`
	Sum                *float64           `json:",omitempty"`
	Minimum            *float64           `json:",omitempty"`
	Maximum            *float64           `json:",omitempty"`
	Unit               string             `json:",omitempty"`
	ExtendedStatistics map[string]float64 `json:",omitempty"`
}

func (s *Service) awsGetMetricStatistics(q *awsapi.Req) (any, error) {
	var in struct {
		Namespace          string
		MetricName         string
		Dimensions         []Dimension
		StartTime          *awsapi.Time
		EndTime            *awsapi.Time
		Period             int
		Statistics         []string
		ExtendedStatistics []string
		Unit               string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:GetMetricStatistics", "*"); err != nil {
		return nil, err
	}
	for name, v := range map[string]string{"Namespace": in.Namespace, "MetricName": in.MetricName} {
		if v == "" {
			return nil, missingParam(name)
		}
	}
	if in.StartTime == nil {
		return nil, missingParam("StartTime")
	}
	if in.EndTime == nil {
		return nil, missingParam("EndTime")
	}
	if err := checkPeriod(in.Period); err != nil {
		return nil, err
	}
	if !in.StartTime.Before(in.EndTime.Time) {
		return nil, invalid("The parameter StartTime must be less than the parameter EndTime.")
	}
	if len(in.Statistics)+len(in.ExtendedStatistics) == 0 {
		return nil, cwErr(http.StatusBadRequest, "InvalidParameterCombination", "Must specify either Statistics or ExtendedStatistics.")
	}
	for _, st := range in.Statistics {
		if !slices.Contains([]string{"Average", "Sum", "Minimum", "Maximum", "SampleCount"}, st) {
			return nil, invalid("The value %s for parameter Statistics is invalid.", st)
		}
	}
	for _, st := range in.ExtendedStatistics {
		if _, ok := percentileOf(st); !ok {
			return nil, invalid("The value %s for parameter ExtendedStatistics is invalid.", st)
		}
	}
	period := time.Duration(in.Period) * time.Second
	if in.EndTime.Sub(in.StartTime.Time)/period > 1440 {
		return nil, cwErr(http.StatusBadRequest, "InvalidParameterCombination", "You have requested up to %d datapoints, which exceeds the limit of 1,440.", int(in.EndTime.Sub(in.StartTime.Time)/period))
	}
	bs, unit := s.buckets(in.Namespace, in.MetricName, dimMap(in.Dimensions), in.StartTime.Time, in.EndTime.Time, period, in.Unit)
	if unit == "" {
		unit = "None"
	}
	dps := []awsDatapoint{}
	for _, b := range bs {
		d := awsDatapoint{Timestamp: awsapi.Time{Time: b.T}, Unit: unit}
		for _, st := range in.Statistics {
			v, _ := b.Stat(st)
			v2 := v
			switch st {
			case "Average":
				d.Average = &v2
			case "Sum":
				d.Sum = &v2
			case "Minimum":
				d.Minimum = &v2
			case "Maximum":
				d.Maximum = &v2
			case "SampleCount":
				d.SampleCount = &v2
			}
		}
		for _, st := range in.ExtendedStatistics {
			if d.ExtendedStatistics == nil {
				d.ExtendedStatistics = map[string]float64{}
			}
			d.ExtendedStatistics[st], _ = b.Stat(st)
		}
		dps = append(dps, d)
	}
	return struct {
		Label      string
		Datapoints []awsDatapoint
	}{in.MetricName, dps}, nil
}

type metricDataResult struct {
	Id         string
	Label      string
	Timestamps []awsapi.Time
	Values     []float64
	StatusCode string
	Messages   []struct{ Code, Value string }
}

func (s *Service) awsGetMetricData(q *awsapi.Req) (any, error) {
	var in struct {
		MetricDataQueries []MetricDataQuery
		StartTime         *awsapi.Time
		EndTime           *awsapi.Time
		NextToken         string
		ScanBy            string
		MaxDatapoints     int
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:GetMetricData", "*"); err != nil {
		return nil, err
	}
	if len(in.MetricDataQueries) == 0 {
		return nil, missingParam("MetricDataQueries")
	}
	if len(in.MetricDataQueries) > 500 {
		return nil, invalid("at most 500 MetricDataQueries")
	}
	if in.StartTime == nil || in.EndTime == nil {
		return nil, missingParam("StartTime and EndTime")
	}
	if !in.StartTime.Before(in.EndTime.Time) {
		return nil, invalid("The parameter StartTime must be less than the parameter EndTime.")
	}
	if in.NextToken != "" {
		return nil, cwErr(http.StatusBadRequest, "InvalidNextToken", "invalid NextToken")
	}
	for _, mq := range in.MetricDataQueries {
		if mq.MetricStat != nil {
			if err := checkPeriod(mq.MetricStat.Period); err != nil {
				return nil, err
			}
		}
	}
	res, order, err := s.evalMetricQueries(in.MetricDataQueries, in.StartTime.Time, in.EndTime.Time)
	if err != nil {
		return nil, cwErr(http.StatusBadRequest, "ValidationError", "%v", err)
	}
	desc := in.ScanBy != "TimestampAscending"
	budget := in.MaxDatapoints
	if budget <= 0 {
		budget = 100800
	}
	out := []metricDataResult{}
	byID := map[string]MetricDataQuery{}
	for _, mq := range in.MetricDataQueries {
		byID[mq.Id] = mq
	}
	for _, id := range order {
		mq := byID[id]
		if mq.ReturnData != nil && !*mq.ReturnData {
			continue
		}
		v := res[id]
		var series []*tseries
		switch {
		case v.series != nil:
			series = []*tseries{v.series}
		case v.array != nil:
			series = v.array
		case v.scalar != nil:
			// A scalar result is reported at every timestamp of the window's first series.
			series = []*tseries{{label: id}}
		}
		for _, ts := range series {
			r := metricDataResult{Id: id, Label: ts.label, Timestamps: []awsapi.Time{}, Values: []float64{}, StatusCode: "Complete"}
			if mq.Label != "" && ts.band != "" {
				r.Label = mq.Label + map[string]string{"upper": " (Upper)", "lower": " (Lower)"}[ts.band]
			} else if mq.Label != "" {
				r.Label = mq.Label
			}
			idx := make([]int, len(ts.t))
			for i := range idx {
				idx[i] = i
			}
			if desc {
				slices.Reverse(idx)
			}
			for _, i := range idx {
				if budget == 0 {
					r.StatusCode = "PartialData"
					break
				}
				r.Timestamps = append(r.Timestamps, awsapi.Time{Time: ts.t[i]})
				r.Values = append(r.Values, ts.v[i])
				budget--
			}
			out = append(out, r)
		}
	}
	return struct {
		MetricDataResults []metricDataResult
		Messages          []struct{ Code, Value string }
	}{out, []struct{ Code, Value string }{}}, nil
}

func (s *Service) awsListMetrics(q *awsapi.Req) (any, error) {
	var in struct {
		Namespace      string
		MetricName     string
		Dimensions     []struct{ Name, Value string }
		NextToken      string
		RecentlyActive string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:ListMetrics", "*"); err != nil {
		return nil, err
	}
	if in.RecentlyActive != "" && in.RecentlyActive != "PT3H" {
		return nil, invalid("RecentlyActive must be PT3H")
	}
	start := 0
	if in.NextToken != "" {
		t, err := fromToken(in.NextToken)
		if err == nil {
			start, err = strconv.Atoi(t)
		}
		if err != nil || start < 0 {
			return nil, cwErr(http.StatusBadRequest, "InvalidNextToken", "invalid NextToken")
		}
	}
	type metric struct {
		Namespace  string
		MetricName string
		Dimensions []Dimension
	}
	var all []metric
	var keys []string
	s.mu.RLock()
	for k, x := range s.series {
		if len(x.Points) == 0 || (in.Namespace != "" && x.Namespace != in.Namespace) || (in.MetricName != "" && x.Name != in.MetricName) {
			continue
		}
		if in.RecentlyActive != "" && time.Since(x.Points[len(x.Points)-1].T) > 3*time.Hour && time.Since(x.Updated) > 3*time.Hour {
			continue
		}
		ok := true
		for _, f := range in.Dimensions {
			v, has := x.Dimensions[f.Name]
			if !has || (f.Value != "" && v != f.Value) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		all = append(all, metric{x.Namespace, x.Name, dimList(x.Dimensions)})
		keys = append(keys, k)
	}
	s.mu.RUnlock()
	idx := make([]int, len(all))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
	out := struct {
		Metrics   []metric
		NextToken string `json:",omitempty"`
	}{Metrics: []metric{}}
	for n, i := range idx {
		if n < start {
			continue
		}
		if len(out.Metrics) == 500 {
			out.NextToken = pageToken(strconv.Itoa(n))
			break
		}
		out.Metrics = append(out.Metrics, all[i])
	}
	return out, nil
}

// ---- alarms ----

type awsMetricAlarm struct {
	AlarmName                          string
	AlarmArn                           string
	AlarmDescription                   string `json:",omitempty"`
	AlarmConfigurationUpdatedTimestamp *awsapi.Time
	ActionsEnabled                     bool
	OKActions                          []string
	AlarmActions                       []string
	InsufficientDataActions            []string
	StateValue                         string
	StateReason                        string
	StateReasonData                    string `json:",omitempty"`
	StateUpdatedTimestamp              *awsapi.Time
	StateTransitionedTimestamp         *awsapi.Time
	MetricName                         string            `json:",omitempty"`
	Namespace                          string            `json:",omitempty"`
	Statistic                          string            `json:",omitempty"`
	ExtendedStatistic                  string            `json:",omitempty"`
	Dimensions                         []Dimension       `json:",omitempty"`
	Period                             int               `json:",omitempty"`
	Unit                               string            `json:",omitempty"`
	EvaluationPeriods                  int               `json:",omitempty"`
	DatapointsToAlarm                  int               `json:",omitempty"`
	Threshold                          *float64          `json:",omitempty"`
	ThresholdMetricId                  string            `json:",omitempty"`
	ComparisonOperator                 string            `json:",omitempty"`
	TreatMissingData                   string            `json:",omitempty"`
	Metrics                            []MetricDataQuery `json:",omitempty"`
}

func toAWSAlarm(a Alarm) awsMetricAlarm {
	cfg := a.ConfigUpdatedAt
	if cfg.IsZero() {
		cfg = a.CreatedAt
	}
	out := awsMetricAlarm{AlarmName: a.Name, AlarmArn: a.ARN, AlarmDescription: a.Description, AlarmConfigurationUpdatedTimestamp: awsapi.T(cfg),
		ActionsEnabled: a.actionsEnabled(), OKActions: nonNil(a.OKActions), AlarmActions: nonNil(a.AlarmActions),
		InsufficientDataActions: nonNil(a.InsufficientDataActions), StateValue: a.State, StateReason: a.StateReason, StateReasonData: a.StateReasonData,
		StateUpdatedTimestamp: awsapi.T(a.StateUpdatedAt), StateTransitionedTimestamp: awsapi.T(a.StateUpdatedAt),
		MetricName: a.Metric, Namespace: a.Namespace, Statistic: a.Statistic, ExtendedStatistic: a.ExtendedStatistic,
		Period: a.Period, Unit: a.Unit, EvaluationPeriods: a.EvaluationPeriods, DatapointsToAlarm: a.DatapointsToAlarm, Threshold: &a.Threshold,
		ComparisonOperator: a.ComparisonOperator, TreatMissingData: a.TreatMissingData, Metrics: a.Metrics, ThresholdMetricId: a.ThresholdMetricID}
	if a.ThresholdMetricID != "" {
		out.Threshold = nil
	}
	if len(a.Metrics) == 0 {
		out.Dimensions = dimList(a.Dimensions)
	} else {
		out.Period = 0
	}
	return out
}

type awsTag struct{ Key, Value string }

func (s *Service) awsPutMetricAlarm(q *awsapi.Req) (any, error) {
	var in struct {
		AlarmName                        string
		AlarmDescription                 string
		ActionsEnabled                   *bool
		OKActions                        []string
		AlarmActions                     []string
		InsufficientDataActions          []string
		MetricName                       string
		Namespace                        string
		Statistic                        string
		ExtendedStatistic                string
		Dimensions                       []Dimension
		Period                           int
		Unit                             string
		EvaluationPeriods                int
		DatapointsToAlarm                int
		Threshold                        *float64
		ComparisonOperator               string
		TreatMissingData                 string
		EvaluateLowSampleCountPercentile string
		Metrics                          []MetricDataQuery
		Tags                             []awsTag
		ThresholdMetricId                string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if in.AlarmName == "" {
		return nil, missingParam("AlarmName")
	}
	for name, v := range map[string]bool{"EvaluationPeriods": in.EvaluationPeriods > 0, "ComparisonOperator": in.ComparisonOperator != ""} {
		if !v {
			return nil, missingParam(name)
		}
	}
	if in.ThresholdMetricId != "" {
		if in.Threshold != nil {
			return nil, invalid("an anomaly detection alarm (ThresholdMetricId) takes no Threshold")
		}
		zero := 0.0
		in.Threshold = &zero
	}
	if in.Threshold == nil {
		return nil, missingParam("Threshold")
	}
	if len(in.Metrics) == 0 {
		if err := checkPeriod(in.Period); err != nil {
			return nil, err
		}
		if err := checkDims(in.Dimensions); err != nil {
			return nil, err
		}
	}
	if in.Unit != "" && !units[in.Unit] {
		return nil, invalid("The value %s for parameter Unit is invalid.", in.Unit)
	}
	if in.Period > 0 && in.Period*in.EvaluationPeriods > 7*24*3600 {
		return nil, invalid("The evaluation range (Period x EvaluationPeriods) cannot exceed one week.")
	}
	a := Alarm{Name: in.AlarmName, Description: in.AlarmDescription, ActionsEnabled: in.ActionsEnabled, AlarmActions: in.AlarmActions,
		OKActions: in.OKActions, InsufficientDataActions: in.InsufficientDataActions, Metric: in.MetricName, Namespace: in.Namespace,
		Statistic: in.Statistic, ExtendedStatistic: in.ExtendedStatistic, Dimensions: dimMap(in.Dimensions), Period: in.Period, Unit: in.Unit,
		EvaluationPeriods: in.EvaluationPeriods, DatapointsToAlarm: in.DatapointsToAlarm, Threshold: *in.Threshold,
		ComparisonOperator: in.ComparisonOperator, TreatMissingData: in.TreatMissingData, Metrics: in.Metrics, ThresholdMetricID: in.ThresholdMetricId}
	if len(in.Tags) > 0 && !store.Has(s.env.Store, cAlarms, a.Name) {
		a.Tags = core.Tags{}
		for _, t := range in.Tags {
			a.Tags[t.Key] = t.Value
		}
	}
	if err := q.Authorize("cloudwatch:PutMetricAlarm", s.alarmARN(in.AlarmName)); err != nil {
		return nil, err
	}
	// Notifying an action target needs the caller's permission for it (sns:Publish).
	if _, err := s.PutAlarm(a, q.Check); err != nil {
		if e, ok := err.(*core.Error); ok && e.Code == "ValidationError" {
			return nil, invalid("%s", e.Message)
		}
		return nil, err
	}
	return nil, nil
}
