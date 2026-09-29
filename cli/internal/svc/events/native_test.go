package events

import (
	"encoding/json"
	"testing"
)

func TestNativeRules(t *testing.T) {
	h, _, k := newAWS(t)
	q := "arn:aws:sqs:us-east-1:" + h.Env.AccountID + ":native"
	h.Native(t, "PUT", "/api/v1/events/rules/n1", map[string]any{"event_pattern": map[string]any{"source": []any{"app"}},
		"targets": []map[string]any{{"arn": q, "input_path": "$.detail"}}})
	var rules []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/events/rules", nil), &rules)
	if len(rules) != 1 || rules[0]["targets"].([]any)[0].(map[string]any)["id"] != "target-1" {
		t.Fatalf("native rules %v", rules)
	}
	h.Native(t, "POST", "/api/v1/events/events", map[string]any{"entries": []map[string]any{{"source": "app", "detail_type": "t", "detail": map[string]any{"x": 1}}}})
	if got := k.wait(t, 1); got[0].payload != `{"x":1}` {
		t.Fatalf("native delivery %v", got)
	}
	// The AWS API sees the native rule and its targets.
	if lt := h.AWSJSON(t, "events", "list-targets-by-rule", "--rule", "n1"); lt["Targets"].([]any)[0].(map[string]any)["InputPath"] != "$.detail" {
		t.Fatalf("aws view %v", lt)
	}
	var res map[string]bool
	_ = json.Unmarshal(h.Native(t, "POST", "/api/v1/events/test-pattern", map[string]any{"pattern": map[string]any{"a": []any{map[string]any{"prefix": "x"}}},
		"event": map[string]any{"a": "xy"}}), &res)
	if !res["result"] {
		t.Fatal("native test-pattern")
	}
}
