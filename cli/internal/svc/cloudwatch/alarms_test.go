package cloudwatch

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/store"
)

func (s *Service) alarmByName(n string) (Alarm, error) {
	return store.Get[Alarm](s.env.Store, cAlarms, n)
}

func TestAlarmEvaluation(t *testing.T) {
	s := newTestService(t)
	var mu sync.Mutex
	var sent []string
	s.Notify = func(target, subject, message string) {
		mu.Lock()
		sent = append(sent, target+"|"+subject+"|"+message)
		mu.Unlock()
	}
	allow := func(string, string) error { return nil }
	enabled := true
	_, err := s.PutAlarm(Alarm{Name: "cpu", Namespace: "App", Metric: "CPU", Statistic: "Maximum", Period: 60, EvaluationPeriods: 3, DatapointsToAlarm: 2,
		Threshold: 80, ComparisonOperator: "GreaterThanThreshold", ActionsEnabled: &enabled,
		AlarmActions: []string{"arn:aws:sns:us-east-1:123456789012:ops"}, OKActions: []string{"arn:aws:sns:us-east-1:123456789012:ops"}}, allow)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Minute)
	put := func(ago int, v float64) {
		s.Put("App", "CPU", nil, "Percent", v, now.Add(-time.Duration(ago)*time.Minute).Add(time.Second))
	}
	get := func() Alarm {
		a, _ := s.alarmByName("cpu")
		return a
	}
	s.evaluateAlarms()
	if a := get(); a.State != "INSUFFICIENT_DATA" {
		t.Fatalf("no data: %s", a.State)
	}
	// 2 of the last 3 periods breach -> ALARM (M of N).
	put(2, 90)
	put(1, 50)
	put(0, 95)
	s.evaluateAlarms()
	if a := get(); a.State != "ALARM" || !strings.Contains(a.StateReason, "2 out of the last 3 datapoints") {
		t.Fatalf("M of N: %s %s", a.State, a.StateReason)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(sent) != 1 || !strings.Contains(sent[0], `ALARM: "cpu"`) {
		t.Fatalf("notification %v", sent)
	}
	var msg map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN(sent[0], "|", 3)[2]), &msg)
	mu.Unlock()
	if msg["NewStateValue"] != "ALARM" || msg["OldStateValue"] != "INSUFFICIENT_DATA" || msg["Trigger"].(map[string]any)["Statistic"] != "MAXIMUM" {
		t.Fatalf("message %v", msg)
	}
	// Disabled actions change state without notifying.
	off := false
	a := get()
	a.ActionsEnabled = &off
	if _, err := s.PutAlarm(a, allow); err != nil {
		t.Fatal(err)
	}
	if get().State != "ALARM" {
		t.Fatal("updating an alarm must keep its state")
	}
	s.setAlarmState(get(), "OK", "manual", "")
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(sent) != 1 {
		t.Fatalf("disabled actions notified: %v", sent)
	}
	mu.Unlock()
	if h := s.history("cpu"); len(h) < 4 {
		t.Fatalf("history %v", h)
	}

	// TreatMissingData.
	vals := []*float64{nil, nil, nil}
	base := Alarm{EvaluationPeriods: 3, Threshold: 1, ComparisonOperator: "GreaterThanThreshold", State: "OK"}
	for tmd, want := range map[string]string{"": "INSUFFICIENT_DATA", "missing": "INSUFFICIENT_DATA", "ignore": "OK", "breaching": "ALARM", "notBreaching": "OK"} {
		b := base
		b.TreatMissingData = tmd
		if got, _, _ := evaluate(b, vals); got != want {
			t.Errorf("TreatMissingData %q: %s, want %s", tmd, got, want)
		}
	}
	// Percentile alarm.
	if _, err := s.PutAlarm(Alarm{Name: "p90", Namespace: "App", Metric: "CPU", ExtendedStatistic: "p90", Period: 60, EvaluationPeriods: 1,
		Threshold: 10, ComparisonOperator: "GreaterThanThreshold"}, allow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAlarm(Alarm{Name: "both", Namespace: "App", Metric: "CPU", Statistic: "Sum", ExtendedStatistic: "p90", Period: 60,
		EvaluationPeriods: 1, ComparisonOperator: "GreaterThanThreshold"}, allow); err == nil {
		t.Fatal("Statistic and ExtendedStatistic together accepted")
	}
	// Metric-math alarm.
	ret := true
	no := false
	_, err = s.PutAlarm(Alarm{Name: "math", Period: 60, EvaluationPeriods: 1, Threshold: 100, ComparisonOperator: "GreaterThanThreshold",
		Metrics: []MetricDataQuery{
			{Id: "c", MetricStat: &MetricStat{Metric: Metric{Namespace: "App", MetricName: "CPU"}, Period: 60, Stat: "Maximum"}, ReturnData: &no},
			{Id: "e", Expression: "c * 2", ReturnData: &ret}}}, allow)
	if err != nil {
		t.Fatal(err)
	}
	s.evaluateAlarms()
	if a, _ := s.alarmByName("math"); a.State != "ALARM" {
		t.Fatalf("math alarm %s %s", a.State, a.StateReason)
	}
}

func TestNativeLogsAndAlarms(t *testing.T) {
	h, _ := newAWS(t)
	h.Native(t, "POST", "/api/v1/logs/groups", map[string]any{"name": "native", "retention_days": 3})
	h.Native(t, "POST", "/api/v1/logs/groups/native/streams/s1/events", map[string]any{"events": []map[string]any{{"message": "hello native"}}})
	var evs []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/logs/groups/native/events?filter=HELLO", nil), &evs)
	if len(evs) != 1 || evs[0]["stream"] != "s1" {
		t.Fatalf("native events %v", evs)
	}
	var streams []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/logs/groups/native/streams", nil), &streams)
	if len(streams) != 1 || streams[0]["name"] != "s1" {
		t.Fatalf("native streams %v", streams)
	}
	// The same events through the AWS API.
	f := h.AWSJSON(t, "logs", "filter-log-events", "--log-group-name", "native")
	if len(f["events"].([]any)) != 1 {
		t.Fatalf("aws view %v", f)
	}
	h.Native(t, "PUT", "/api/v1/cloudwatch/alarms/a1", map[string]any{"namespace": "App", "metric": "M", "threshold": 1, "comparison_operator": "GreaterThanThreshold", "period": 60})
	al := h.AWSJSON(t, "cloudwatch", "describe-alarms")
	if a := al["MetricAlarms"].([]any); len(a) != 1 || a[0].(map[string]any)["AlarmName"] != "a1" {
		t.Fatalf("alarm %v", al)
	}
}
