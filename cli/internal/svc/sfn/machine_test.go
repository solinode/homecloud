package sfn

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

type fakeTasks struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *fakeTasks) Invoke(ctx context.Context, fn string, payload []byte) (json.RawMessage, string, string, error) {
	f.mu.Lock()
	f.calls[fn]++
	n := f.calls[fn]
	f.mu.Unlock()
	var in map[string]any
	_ = json.Unmarshal(payload, &in)
	switch fn {
	case "double":
		v, _ := in["n"].(float64)
		return json.RawMessage(mustJSON(map[string]any{"n": v * 2})), "", "", nil
	case "flaky": // fails twice, then succeeds
		if n <= 2 {
			return nil, "TransientError", "try again", nil
		}
		return json.RawMessage(`"ok"`), "", "", nil
	case "broken":
		return nil, "ValueError", "boom", nil
	}
	return json.RawMessage(`null`), "", "", nil
}
func (f *fakeTasks) SendMessage(q, body string) (map[string]any, error) {
	return map[string]any{"MessageId": "m1"}, nil
}
func (f *fakeTasks) Publish(t, s, m string) (map[string]any, error) {
	return map[string]any{"MessageId": "p1"}, nil
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func run(t *testing.T, def string, input any) (any, error, *fakeTasks) {
	t.Helper()
	m, errs := parse(json.RawMessage(def))
	if m == nil {
		t.Fatalf("invalid definition: %v", errs)
	}
	ft := &fakeTasks{calls: map[string]int{}}
	r := &runner{tasks: ft, record: func(string, string, any) {}, context: map[string]any{"Execution": map[string]any{"Name": "test"}}}
	out, err := r.run(context.Background(), m, input)
	return out, err, ft
}

func TestPipeline(t *testing.T) {
	def := `{"StartAt":"Init","States":{
	  "Init":{"Type":"Pass","Result":{"n":3},"ResultPath":"$.data","Next":"Double"},
	  "Double":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:1:function:double","InputPath":"$.data","ResultPath":"$.doubled","Next":"Check"},
	  "Check":{"Type":"Choice","Choices":[{"Variable":"$.doubled.n","NumericGreaterThan":5,"Next":"Big"}],"Default":"Small"},
	  "Big":{"Type":"Pass","Parameters":{"size":"big","value.$":"$.doubled.n","msg.$":"States.Format('n={} for {}', $.doubled.n, $$.Execution.Name)"},"End":true},
	  "Small":{"Type":"Fail","Error":"TooSmall"}}}`
	out, err, _ := run(t, def, map[string]any{"user": "a"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["size"] != "big" || m["value"] != 6.0 || m["msg"] != "n=6 for test" {
		t.Fatalf("unexpected output %v", m)
	}
}

func TestRetryAndCatch(t *testing.T) {
	def := `{"StartAt":"Flaky","States":{
	  "Flaky":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:1:function:flaky","Retry":[{"ErrorEquals":["TransientError"],"IntervalSeconds":0.01,"MaxAttempts":3}],"Next":"Broken"},
	  "Broken":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:1:function:broken","Catch":[{"ErrorEquals":["States.ALL"],"ResultPath":"$.err","Next":"Handled"}],"End":true},
	  "Handled":{"Type":"Pass","End":true}}}`
	out, err, ft := run(t, def, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if ft.calls["flaky"] != 3 {
		t.Errorf("flaky called %d times, want 3", ft.calls["flaky"])
	}
	e := out.(map[string]any)["err"].(map[string]any)
	if e["Error"] != "ValueError" || e["Cause"] != "boom" {
		t.Errorf("caught %v", e)
	}
}

func TestUncaughtFailure(t *testing.T) {
	_, err, _ := run(t, `{"StartAt":"B","States":{"B":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:1:function:broken","End":true}}}`, map[string]any{})
	se, ok := err.(*StateError)
	if !ok || se.Name != "ValueError" {
		t.Fatalf("got %v", err)
	}
}

func TestMapAndParallel(t *testing.T) {
	def := `{"StartAt":"Each","States":{
	  "Each":{"Type":"Map","ItemsPath":"$.items","MaxConcurrency":2,
	    "ItemProcessor":{"StartAt":"D","States":{"D":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:1:function:double","End":true}}},
	    "ResultPath":"$.results","Next":"Both"},
	  "Both":{"Type":"Parallel","Branches":[
	    {"StartAt":"A","States":{"A":{"Type":"Pass","Result":"a","End":true}}},
	    {"StartAt":"Q","States":{"Q":{"Type":"Task","Resource":"arn:aws:states:::sqs:sendMessage","Parameters":{"QueueName":"q","MessageBody.$":"$.results"},"End":true}}}],
	    "ResultPath":"$.parallel","End":true}}}`
	out, err, ft := run(t, def, map[string]any{"items": []any{map[string]any{"n": 1.0}, map[string]any{"n": 2.0}, map[string]any{"n": 3.0}}})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	res := m["results"].([]any)
	if len(res) != 3 || res[2].(map[string]any)["n"] != 6.0 || ft.calls["double"] != 3 {
		t.Fatalf("map results %v", res)
	}
	par := m["parallel"].([]any)
	if par[0] != "a" || par[1].(map[string]any)["MessageId"] != "m1" {
		t.Fatalf("parallel %v", par)
	}
}

func TestValidate(t *testing.T) {
	_, errs := parse(json.RawMessage(`{"StartAt":"X","States":{"X":{"Type":"Pass","Next":"Nope"},"Y":{"Type":"Bogus"}}}`))
	if len(errs) < 2 {
		t.Fatalf("expected errors, got %v", errs)
	}
}

func TestChoiceOperators(t *testing.T) {
	in := map[string]any{"s": "hello.txt", "n": 5.0, "b": true, "t": "2026-01-02T00:00:00Z"}
	cases := []struct {
		rule string
		want bool
	}{
		{`{"Variable":"$.s","StringMatches":"*.txt"}`, true},
		{`{"And":[{"Variable":"$.n","NumericGreaterThanEquals":5},{"Variable":"$.b","BooleanEquals":true}]}`, true},
		{`{"Not":{"Variable":"$.missing","IsPresent":true}}`, true},
		{`{"Variable":"$.t","TimestampLessThan":"2026-06-01T00:00:00Z"}`, true},
		{`{"Or":[{"Variable":"$.n","NumericLessThan":1},{"Variable":"$.s","StringEquals":"x"}]}`, false},
	}
	for _, c := range cases {
		var rule map[string]any
		_ = json.Unmarshal([]byte(c.rule), &rule)
		got, err := evalRule(rule, in)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v", c.rule, got, err)
		}
	}
}

func TestReviewRegressions(t *testing.T) {
	// Intrinsics without arguments fail the execution instead of panicking.
	_, err, _ := run(t, `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"x.$":"States.JsonToString()"},"End":true}}}`, map[string]any{})
	if err == nil {
		t.Fatal("expected an error")
	}
	// Catch with ResultPath null passes the failing state's input through.
	out, err, _ := run(t, `{"StartAt":"A","States":{
	  "A":{"Type":"Pass","Result":{"x":"state-input"},"Next":"B"},
	  "B":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:1:function:broken","Catch":[{"ErrorEquals":["States.ALL"],"ResultPath":null,"Next":"C"}],"End":true},
	  "C":{"Type":"Pass","End":true}}}`, map[string]any{"orig": true})
	if err != nil || out.(map[string]any)["x"] != "state-input" {
		t.Fatalf("ResultPath null: %v %v", out, err)
	}
	// A machine timeout during a Wait is a timeout, not an abort.
	_, err, _ = run(t, `{"StartAt":"W","TimeoutSeconds":1,"States":{"W":{"Type":"Wait","Seconds":3,"End":true}}}`, map[string]any{})
	if se, ok := err.(*StateError); !ok || se.Name != "States.Timeout" {
		t.Fatalf("got %v", err)
	}
}

func TestTaskPermissions(t *testing.T) {
	m, _ := parse(json.RawMessage(`{"StartAt":"A","States":{
	  "A":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:1:function:f1","Next":"B"},
	  "B":{"Type":"Task","Resource":"arn:aws:states:::sqs:sendMessage","Parameters":{"QueueName":"q1","MessageBody":"x"},"Next":"C"},
	  "C":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName.$":"$.fn"},"End":true}}}`))
	got := map[string]bool{}
	for _, p := range taskPermissions(m, "1") {
		got[p[0]+" "+p[1]] = true
	}
	for _, want := range []string{"lambda:InvokeFunction arn:aws:lambda:us-east-1:1:function:f1", "sqs:SendMessage arn:aws:sqs:us-east-1:1:q1", "lambda:InvokeFunction *"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}
