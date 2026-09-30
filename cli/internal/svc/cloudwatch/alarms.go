package cloudwatch

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const (
	cAlarms       = "cloudwatch_alarms"
	cAlarmHistory = "cloudwatch_alarm_history"
	maxHistory    = 100
)

var alarmName = regexp.MustCompile(`^[^/\x00-\x1f]{1,255}$`)

type Alarm struct {
	Name               string            `json:"name"`
	ARN                string            `json:"arn"`
	Description        string            `json:"description"`
	Namespace          string            `json:"namespace"`
	Metric             string            `json:"metric"`
	Dimensions         map[string]string `json:"dimensions"`
	Statistic          string            `json:"statistic"`
	ExtendedStatistic  string            `json:"extended_statistic,omitempty"` // a percentile such as p99, instead of Statistic
	Unit               string            `json:"unit,omitempty"`
	Period             int               `json:"period"` // seconds
	EvaluationPeriods  int               `json:"evaluation_periods"`
	DatapointsToAlarm  int               `json:"datapoints_to_alarm,omitempty"` // default: EvaluationPeriods
	Threshold          float64           `json:"threshold"`
	ComparisonOperator string            `json:"comparison_operator"`
	TreatMissingData   string            `json:"treat_missing_data,omitempty"` // missing | ignore | breaching | notBreaching
	// Metrics makes a metric-math alarm (instead of Namespace/Metric): the
	// query with ReturnData true is compared with the threshold.
	Metrics []MetricDataQuery `json:"metrics,omitempty"`
	// ThresholdMetricID names the ANOMALY_DETECTION_BAND expression among Metrics
	// that an anomaly detection alarm compares the metric with.
	ThresholdMetricID string `json:"threshold_metric_id,omitempty"`
	// ActionsEnabled nil means enabled.
	ActionsEnabled *bool `json:"actions_enabled,omitempty"`
	// Actions are SNS topic ARNs or http(s) webhook URLs notified on state change.
	AlarmActions            []string  `json:"alarm_actions"`
	OKActions               []string  `json:"ok_actions"`
	InsufficientDataActions []string  `json:"insufficient_data_actions,omitempty"`
	State                   string    `json:"state"`
	StateReason             string    `json:"state_reason"`
	StateReasonData         string    `json:"state_reason_data,omitempty"`
	StateUpdatedAt          time.Time `json:"state_updated_at"`
	ConfigUpdatedAt         time.Time `json:"config_updated_at,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
	Tags                    core.Tags `json:"tags,omitempty"`
}

func (a Alarm) actionsEnabled() bool { return a.ActionsEnabled == nil || *a.ActionsEnabled }

func (a Alarm) stat() string {
	if a.ExtendedStatistic != "" {
		return a.ExtendedStatistic
	}
	return a.Statistic
}

// AlarmHistoryItem is one entry of DescribeAlarmHistory.
type AlarmHistoryItem struct {
	AlarmName       string    `json:"alarm_name"`
	Timestamp       time.Time `json:"timestamp"`
	HistoryItemType string    `json:"type"` // ConfigurationUpdate | StateUpdate | Action
	HistorySummary  string    `json:"summary"`
	HistoryData     string    `json:"data"`
}

var operators = map[string]func(a, b float64) bool{
	"GreaterThanThreshold":          func(a, b float64) bool { return a > b },
	"GreaterThanOrEqualToThreshold": func(a, b float64) bool { return a >= b },
	"LessThanThreshold":             func(a, b float64) bool { return a < b },
	"LessThanOrEqualToThreshold":    func(a, b float64) bool { return a <= b },
}

// anomalyOps are the comparison operators of anomaly detection alarms.
var anomalyOps = map[string]func(v, lo, hi float64) bool{
	"LessThanLowerOrGreaterThanUpperThreshold": func(v, lo, hi float64) bool { return v < lo || v > hi },
	"LessThanLowerThreshold":                   func(v, lo, hi float64) bool { return v < lo },
	"GreaterThanUpperThreshold":                func(v, lo, hi float64) bool { return v > hi },
}

var anomalyWords = map[string]string{"LessThanLowerOrGreaterThanUpperThreshold": "less than the lower or greater than the upper",
	"LessThanLowerThreshold": "less than the lower", "GreaterThanUpperThreshold": "greater than the upper"}

// alarmBand is the anomaly band at one evaluated datapoint.
type alarmBand struct{ lo, hi float64 }

func (s *Service) addHistory(name, typ, summary string, data any) {
	b, _ := json.Marshal(data)
	item := AlarmHistoryItem{AlarmName: name, Timestamp: time.Now().UTC(), HistoryItemType: typ, HistorySummary: summary, HistoryData: string(b)}
	err := store.Put(s.env.Store, cAlarmHistory, name, append(s.history(name), item))
	if err != nil {
		return
	}
	if h := s.history(name); len(h) > maxHistory {
		_ = store.Put(s.env.Store, cAlarmHistory, name, h[len(h)-maxHistory:])
	}
}

func (s *Service) history(name string) []AlarmHistoryItem {
	h, _ := store.Get[[]AlarmHistoryItem](s.env.Store, cAlarmHistory, name)
	return h
}

// alarmValues returns the alarm's metric value for each of the last
// EvaluationPeriods periods, oldest first (nil = no data). The current period
// is used when it already has data, otherwise the last complete one ends the window.
func (s *Service) alarmValues(a Alarm, now time.Time) ([]*float64, error) {
	v, _, err := s.alarmData(a, now)
	return v, err
}

// alarmData is alarmValues plus, for anomaly detection alarms, the band at each
// datapoint. A datapoint without a band counts as missing.
func (s *Service) alarmData(a Alarm, now time.Time) ([]*float64, []*alarmBand, error) {
	n := max(a.EvaluationPeriods, 1)
	period := time.Duration(a.Period) * time.Second
	if len(a.Metrics) > 0 {
		for _, q := range a.Metrics {
			if q.MetricStat != nil && q.MetricStat.Period > 0 {
				period = time.Duration(q.MetricStat.Period) * time.Second
				break
			}
		}
	}
	if period <= 0 {
		period = time.Minute
	}
	end := now.Truncate(period).Add(period)
	start := end.Add(-period * time.Duration(n+1))
	vals := map[int64]float64{}
	bands := map[int64]*alarmBand{}
	if len(a.Metrics) > 0 {
		res, order, err := s.evalMetricQueries(a.Metrics, start, end)
		if err != nil {
			return nil, nil, err
		}
		var out *tseries
		for _, id := range order {
			q := a.Metrics[slices.IndexFunc(a.Metrics, func(m MetricDataQuery) bool { return m.Id == id })]
			if id == a.ThresholdMetricID {
				for _, bs := range res[id].array {
					for i, t := range bs.t {
						b := bands[t.Unix()]
						if b == nil {
							b = &alarmBand{}
							bands[t.Unix()] = b
						}
						if bs.band == "upper" {
							b.hi = bs.v[i]
						} else {
							b.lo = bs.v[i]
						}
					}
				}
				continue
			}
			if out == nil && (q.ReturnData == nil || *q.ReturnData) {
				out = res[id].series
			}
		}
		if out != nil {
			for i, t := range out.t {
				vals[t.Unix()] = out.v[i]
			}
		}
	} else {
		bs, _ := s.buckets(a.Namespace, a.Metric, a.Dimensions, start, end, period, a.Unit)
		for _, b := range bs {
			if v, ok := b.Stat(a.stat()); ok {
				vals[b.T.Unix()] = v
			}
		}
	}
	cur := end.Add(-period)
	if _, ok := vals[cur.Unix()]; !ok {
		cur = cur.Add(-period)
	}
	out := make([]*float64, n)
	var outBands []*alarmBand
	if a.ThresholdMetricID != "" {
		outBands = make([]*alarmBand, n)
	}
	for i := 0; i < n; i++ {
		t := cur.Add(-period * time.Duration(n-1-i))
		if v, ok := vals[t.Unix()]; ok {
			if a.ThresholdMetricID != "" {
				b, ok := bands[t.Unix()]
				if !ok {
					continue
				}
				outBands[i] = b
			}
			out[i] = &v
		}
	}
	return out, outBands, nil
}

// evaluate decides an alarm's state from its datapoints.
func evaluate(a Alarm, vals []*float64) (state, reason string, recent []float64) {
	return evaluateBands(a, vals, nil)
}

// evaluateBands is evaluate for anomaly detection alarms when bands is set.
func evaluateBands(a Alarm, vals []*float64, bands []*alarmBand) (state, reason string, recent []float64) {
	n := len(vals)
	m := a.DatapointsToAlarm
	if m <= 0 || m > n {
		m = n
	}
	cmp := operators[a.ComparisonOperator]
	breach, good, missing := 0, 0, 0
	for i, v := range vals {
		switch {
		case v == nil:
			missing++
		case bands != nil && anomalyOps[a.ComparisonOperator](*v, bands[i].lo, bands[i].hi):
			breach++
			recent = append(recent, *v)
		case bands != nil:
			good++
			recent = append(recent, *v)
		case cmp(*v, a.Threshold):
			breach++
			recent = append(recent, *v)
		default:
			good++
			recent = append(recent, *v)
		}
	}
	switch a.TreatMissingData {
	case "breaching":
		breach += missing
	case "notBreaching":
		good += missing
	case "ignore":
		if missing == n {
			return a.State, a.StateReason, nil
		}
	default:
		if missing == n {
			return "INSUFFICIENT_DATA", fmt.Sprintf("Insufficient Data: %d datapoints were unknown.", n), nil
		}
	}
	vs := make([]string, len(recent))
	for i, v := range recent {
		vs[i] = fmt.Sprint(v)
	}
	if bands != nil {
		w := anomalyWords[a.ComparisonOperator]
		if breach >= m {
			return "ALARM", fmt.Sprintf("Thresholds Crossed: %d out of the last %d datapoints [%s] were %s band (minimum %d datapoints for OK -> ALARM transition).",
				breach, n, strings.Join(vs, ", "), w, m), recent
		}
		return "OK", fmt.Sprintf("Thresholds Crossed: %d out of the last %d datapoints [%s] were not %s band (minimum %d datapoints for ALARM -> OK transition).",
			n-breach, n, strings.Join(vs, ", "), w, m), recent
	}
	if breach >= m {
		return "ALARM", fmt.Sprintf("Threshold Crossed: %d out of the last %d datapoints [%s] were %s the threshold (%v) (minimum %d datapoints for OK -> ALARM transition).",
			breach, n, strings.Join(vs, ", "), opWords[a.ComparisonOperator], a.Threshold, m), recent
	}
	return "OK", fmt.Sprintf("Threshold Crossed: %d out of the last %d datapoints [%s] were not %s the threshold (%v) (minimum %d datapoints for ALARM -> OK transition).",
		n-breach, n, strings.Join(vs, ", "), opWords[a.ComparisonOperator], a.Threshold, m), recent
}

var opWords = map[string]string{"GreaterThanThreshold": "greater than", "GreaterThanOrEqualToThreshold": "greater than or equal to",
	"LessThanThreshold": "less than", "LessThanOrEqualToThreshold": "less than or equal to"}

func (s *Service) evaluateAlarms() {
	now := time.Now()
	for _, a := range store.List[Alarm](s.env.Store, cAlarms) {
		vals, bands, err := s.alarmData(a, now)
		if err != nil {
			continue
		}
		state, reason, recent := evaluateBands(a, vals, bands)
		if state == a.State {
			continue
		}
		data := map[string]any{"version": "1.0", "queryDate": now.UTC().Format("2006-01-02T15:04:05.000+0000"), "recentDatapoints": recent, "threshold": a.Threshold}
		b, _ := json.Marshal(data)
		s.setAlarmState(a, state, reason, string(b))
	}
}

// setAlarmState records a state transition and runs the alarm's actions.
func (s *Service) setAlarmState(a Alarm, state, reason, reasonData string) {
	prev := a.State
	now := core.Now()
	updated, err := store.Update(s.env.Store, cAlarms, a.Name, func(x *Alarm) error {
		x.State, x.StateReason, x.StateReasonData, x.StateUpdatedAt = state, reason, reasonData, now
		return nil
	})
	if err != nil {
		return
	}
	s.addHistory(a.Name, "StateUpdate", fmt.Sprintf("Alarm updated from %s to %s", prev, state), map[string]any{
		"version": "1.0", "oldState": map[string]string{"stateValue": prev, "stateReason": a.StateReason},
		"newState": map[string]string{"stateValue": state, "stateReason": reason}})
	if s.Notify == nil || !updated.actionsEnabled() {
		return
	}
	targets := updated.OKActions
	switch state {
	case "ALARM":
		targets = updated.AlarmActions
	case "INSUFFICIENT_DATA":
		targets = updated.InsufficientDataActions
	}
	if len(targets) == 0 {
		return
	}
	subject := fmt.Sprintf("%s: %q in %s", state, updated.Name, s.env.Cfg.Region)
	msg := s.alarmMessage(updated, prev)
	for _, t := range targets {
		go s.Notify(t, subject, msg)
		s.addHistory(a.Name, "Action", "Successfully executed action "+t, map[string]string{"actionState": "Succeeded", "notificationResource": t})
	}
}

// alarmMessage is the JSON body CloudWatch sends to SNS on a state change.
func (s *Service) alarmMessage(a Alarm, prev string) string {
	dims := []map[string]string{}
	for _, d := range dimList(a.Dimensions) {
		dims = append(dims, map[string]string{"name": d.Name, "value": d.Value})
	}
	trigger := map[string]any{"MetricName": a.Metric, "Namespace": a.Namespace, "StatisticType": "Statistic",
		"Statistic": strings.ToUpper(a.Statistic), "Unit": nil, "Dimensions": dims, "Period": a.Period, "EvaluationPeriods": a.EvaluationPeriods,
		"DatapointsToAlarm": max(a.DatapointsToAlarm, a.EvaluationPeriods), "ComparisonOperator": a.ComparisonOperator, "Threshold": a.Threshold,
		"TreatMissingData": a.TreatMissingData, "EvaluateLowSampleCountPercentile": ""}
	if a.ExtendedStatistic != "" {
		trigger["StatisticType"], trigger["ExtendedStatistic"] = "ExtendedStatistic", a.ExtendedStatistic
		delete(trigger, "Statistic")
	}
	if a.Unit != "" {
		trigger["Unit"] = a.Unit
	}
	if len(a.Metrics) > 0 {
		trigger = map[string]any{"Metrics": a.Metrics, "Period": a.Period, "EvaluationPeriods": a.EvaluationPeriods,
			"ComparisonOperator": a.ComparisonOperator, "Threshold": a.Threshold, "TreatMissingData": a.TreatMissingData}
	}
	b, _ := json.Marshal(map[string]any{
		"AlarmName": a.Name, "AlarmDescription": a.Description, "AWSAccountId": s.env.AccountID,
		"AlarmConfigurationUpdatedTimestamp": a.ConfigUpdatedAt.Format("2006-01-02T15:04:05.000+0000"),
		"NewStateValue":                      a.State, "NewStateReason": a.StateReason, "StateChangeTime": a.StateUpdatedAt.Format("2006-01-02T15:04:05.000+0000"),
		"Region": s.env.Cfg.Region, "AlarmArn": a.ARN, "OldStateValue": prev,
		"OKActions": a.OKActions, "AlarmActions": a.AlarmActions, "InsufficientDataActions": nonNil(a.InsufficientDataActions), "Trigger": trigger,
	})
	return string(b)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Service) alarmRoutes(r *httpx.Router) {
	r.Handle("GET /api/v1/cloudwatch/alarms", "cloudwatch:DescribeAlarms", s.listAlarms)
	r.Handle("GET /api/v1/cloudwatch/alarms/{name}", "cloudwatch:DescribeAlarms", s.getAlarm, httpx.Res("arn:aws:cloudwatch:{region}:{account}:alarm:{name}"))
	r.Handle("PUT /api/v1/cloudwatch/alarms/{name}", "cloudwatch:PutMetricAlarm", s.putAlarm, httpx.Res("arn:aws:cloudwatch:{region}:{account}:alarm:{name}"))
	r.Handle("DELETE /api/v1/cloudwatch/alarms/{name}", "cloudwatch:DeleteAlarms", s.deleteAlarm, httpx.Res("arn:aws:cloudwatch:{region}:{account}:alarm:{name}"))
}

func (s *Service) listAlarms(c *httpx.Ctx) (any, error) {
	return store.List[Alarm](s.env.Store, cAlarms), nil
}

func (s *Service) getAlarm(c *httpx.Ctx) (any, error) {
	a, err := store.Get[Alarm](s.env.Store, cAlarms, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("alarm", c.Param("name"))
	}
	return a, nil
}

// PutAlarm creates or replaces an alarm. authorize checks the caller may
// notify each action target. An existing alarm keeps its state and tags.
func (s *Service) PutAlarm(a Alarm, authorize func(action, resource string) error) (Alarm, error) {
	if !alarmName.MatchString(a.Name) {
		return a, core.BadRequest("alarm names are 1-255 characters without control characters or slashes")
	}
	for _, t := range slices.Concat(a.AlarmActions, a.OKActions, a.InsufficientDataActions) {
		switch {
		case strings.HasPrefix(t, "arn:aws:sns:"):
			if err := authorize("sns:Publish", t); err != nil {
				return a, err
			}
		case strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://"):
			if err := core.CheckWebhookURL(t); err != nil {
				return a, err
			}
		default:
			return a, core.BadRequest("action %q must be an SNS topic ARN or an http(s) URL", t)
		}
	}
	if math.IsNaN(a.Threshold) || math.IsInf(a.Threshold, 0) {
		return a, core.BadRequest("threshold must be a number")
	}
	if len(a.Metrics) > 0 {
		if a.Namespace != "" || a.Metric != "" || a.Statistic != "" || a.ExtendedStatistic != "" || len(a.Dimensions) > 0 {
			return a, core.BadRequest("an alarm uses either Metrics or MetricName/Namespace/Statistic, not both")
		}
		returns := 0
		for _, q := range a.Metrics {
			if q.Id != a.ThresholdMetricID && (q.ReturnData == nil || *q.ReturnData) {
				returns++
			}
		}
		if returns != 1 {
			return a, core.BadRequest("exactly one of the alarm's Metrics must have ReturnData true")
		}
		if _, _, err := s.evalMetricQueries(a.Metrics, time.Now().Add(-time.Minute), time.Now()); err != nil {
			return a, core.BadRequest("%v", err)
		}
	} else {
		if a.Namespace == "" || a.Metric == "" {
			return a, core.BadRequest("namespace and metric are required")
		}
		if a.ExtendedStatistic != "" {
			if a.Statistic != "" {
				return a, core.BadRequest("specify Statistic or ExtendedStatistic, not both")
			}
			if _, ok := percentileOf(a.ExtendedStatistic); !ok {
				return a, core.BadRequest("ExtendedStatistic must be a percentile such as p99")
			}
		} else if a.Statistic == "" {
			a.Statistic = "Average"
		} else if !slices.Contains([]string{"Average", "Sum", "Minimum", "Maximum", "SampleCount"}, a.Statistic) {
			return a, core.BadRequest("statistic must be Average, Sum, Minimum, Maximum or SampleCount")
		}
	}
	if a.ThresholdMetricID != "" {
		if _, ok := anomalyOps[a.ComparisonOperator]; !ok {
			return a, core.BadRequest("an anomaly detection alarm's ComparisonOperator must be LessThanLowerOrGreaterThanUpperThreshold, LessThanLowerThreshold or GreaterThanUpperThreshold")
		}
		var band *MetricDataQuery
		for i, q := range a.Metrics {
			if q.Id == a.ThresholdMetricID {
				band = &a.Metrics[i]
			}
		}
		if band == nil {
			return a, core.BadRequest("ThresholdMetricId %q is not one of the alarm's Metrics", a.ThresholdMetricID)
		}
		src, ok := bandSource(band.Expression)
		if !ok {
			return a, core.BadRequest("ThresholdMetricId must name an ANOMALY_DETECTION_BAND expression")
		}
		byID := map[string]MetricDataQuery{}
		for _, q := range a.Metrics {
			byID[q.Id] = q
		}
		if q := byID[src]; q.MetricStat != nil {
			s.ensureDetector(AnomalyDetector{Single: &SingleMetricDetector{Namespace: q.MetricStat.Metric.Namespace, MetricName: q.MetricStat.Metric.MetricName,
				Dimensions: q.MetricStat.Metric.Dimensions, Stat: q.MetricStat.Stat}})
		} else if q.Expression != "" {
			s.ensureDetector(AnomalyDetector{Math: s.depQueries(byID, src)})
		}
	} else if _, ok := operators[a.ComparisonOperator]; !ok {
		return a, core.BadRequest("comparison_operator must be one of GreaterThanThreshold, GreaterThanOrEqualToThreshold, LessThanThreshold, LessThanOrEqualToThreshold")
	}
	switch a.TreatMissingData {
	case "", "missing", "ignore", "breaching", "notBreaching":
	default:
		return a, core.BadRequest("TreatMissingData must be missing, ignore, breaching or notBreaching")
	}
	if a.Period < 10 && len(a.Metrics) == 0 {
		a.Period = 60
	}
	if a.EvaluationPeriods < 1 {
		a.EvaluationPeriods = 1
	}
	if a.DatapointsToAlarm > a.EvaluationPeriods {
		return a, core.BadRequest("DatapointsToAlarm must not exceed EvaluationPeriods")
	}
	a.ARN = s.env.ARN("cloudwatch", "alarm:"+a.Name)
	now := core.Now()
	a.State, a.StateReason, a.StateUpdatedAt, a.CreatedAt, a.ConfigUpdatedAt = "INSUFFICIENT_DATA", "Unchecked: Initial alarm creation", now, now, now
	summary := fmt.Sprintf("Alarm %q created", a.Name)
	if old, err := store.Get[Alarm](s.env.Store, cAlarms, a.Name); err == nil {
		a.CreatedAt, a.State, a.StateReason, a.StateReasonData, a.StateUpdatedAt = old.CreatedAt, old.State, old.StateReason, old.StateReasonData, old.StateUpdatedAt
		a.Tags = old.Tags // tags change only through TagResource
		summary = fmt.Sprintf("Alarm %q updated", a.Name)
	}
	if a.AlarmActions == nil {
		a.AlarmActions = []string{}
	}
	if a.OKActions == nil {
		a.OKActions = []string{}
	}
	if err := store.Put(s.env.Store, cAlarms, a.Name, a); err != nil {
		return a, err
	}
	s.addHistory(a.Name, "ConfigurationUpdate", summary, map[string]any{"version": "1.0", "type": "Update", "updatedAlarm": a.Name})
	return a, nil
}

func (s *Service) putAlarm(c *httpx.Ctx) (any, error) {
	var a Alarm
	if err := c.Bind(&a); err != nil {
		return nil, err
	}
	a.Name = c.Param("name")
	a.Tags = nil
	if a.Period < 30 {
		a.Period = 60
	}
	return s.PutAlarm(a, c.Authorize)
}

// DeleteAlarm removes an alarm and its history.
func (s *Service) DeleteAlarm(name string) error {
	if err := store.Delete(s.env.Store, cAlarms, name); err != nil {
		return core.NotFound("alarm", name)
	}
	_ = store.Delete(s.env.Store, cAlarmHistory, name)
	return nil
}

func (s *Service) deleteAlarm(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteAlarm(c.Param("name"))
}
