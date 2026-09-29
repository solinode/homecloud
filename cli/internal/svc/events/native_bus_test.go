package events

import (
	"encoding/json"
	"testing"
)

func TestNativeBusesAndScheduler(t *testing.T) {
	h, _, _ := newAWS(t)
	q := "arn:aws:sqs:us-east-1:" + h.Env.AccountID + ":native"
	h.Native(t, "POST", "/api/v1/events/buses", map[string]any{"name": "orders"})
	var buses []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/events/buses", nil), &buses)
	if len(buses) != 2 {
		t.Fatalf("buses %v", buses)
	}
	h.Native(t, "PUT", "/api/v1/events/rules/r1?event_bus=orders", map[string]any{"event_pattern": map[string]any{"source": []any{"app"}},
		"targets": []map[string]any{{"arn": q}}})
	var on, def []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/events/rules?event_bus=orders", nil), &on)
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/events/rules", nil), &def)
	if len(on) != 1 || len(def) != 0 {
		t.Fatalf("rules by bus: %v / %v", on, def)
	}
	h.Native(t, "PUT", "/api/v1/events/rules/r1?event_bus=orders", map[string]any{"event_pattern": map[string]any{"source": []any{"app"}}})
	h.Native(t, "DELETE", "/api/v1/events/buses/orders", nil)

	h.Native(t, "POST", "/api/v1/scheduler/schedule-groups", map[string]any{"name": "qa"})
	h.Native(t, "PUT", "/api/v1/scheduler/schedule-groups/qa/schedules/s1", map[string]any{
		"ScheduleExpression": "rate(5 minutes)", "FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{"Arn": q, "RoleArn": "arn:aws:iam::" + h.Env.AccountID + ":role/x"}})
	var scs []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/scheduler/schedules?group=qa", nil), &scs)
	if len(scs) != 1 || scs[0]["State"] != "ENABLED" {
		t.Fatalf("schedules %v", scs)
	}
	h.Native(t, "DELETE", "/api/v1/scheduler/schedule-groups/qa", nil)
}
