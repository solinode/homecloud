package sfn

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIntrinsics(t *testing.T) {
	input := map[string]any{"name": "Ann", "n": 3.0, "list": []any{1.0, 2.0, 2.0, 3.0}, "obj": map[string]any{"a": 1.0}, "csv": "a,b;c"}
	ctx := map[string]any{"Execution": map[string]any{"Name": "run-1"}}
	cases := map[string]string{
		`States.Format('Hello {}, you are {}', $.name, $.n)`:             `"Hello Ann, you are 3"`,
		`States.Format('it\'s {} \{literal\}', $$.Execution.Name)`:       `"it's run-1 {literal}"`,
		`States.StringToJson('{"x": [1, 2]}')`:                           `{"x":[1,2]}`,
		`States.JsonToString($.obj)`:                                     `"{\"a\":1}"`,
		`States.Array($.name, 1, true, null)`:                            `["Ann",1,true,null]`,
		`States.ArrayPartition($.list, 3)`:                               `[[1,2,2],[3]]`,
		`States.ArrayContains($.list, 3)`:                                `true`,
		`States.ArrayContains($.list, 9)`:                                `false`,
		`States.ArrayRange(1, 9, 3)`:                                     `[1,4,7]`,
		`States.ArrayGetItem($.list, 3)`:                                 `3`,
		`States.ArrayLength($.list)`:                                     `4`,
		`States.ArrayUnique($.list)`:                                     `[1,2,3]`,
		`States.Base64Encode('hi there')`:                                `"aGkgdGhlcmU="`,
		`States.Base64Decode('aGkgdGhlcmU=')`:                            `"hi there"`,
		`States.Hash('abc', 'SHA-256')`:                                  `"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`,
		`States.Hash('abc', 'MD5')`:                                      `"900150983cd24fb0d6963f7d28e17f72"`,
		`States.JsonMerge($.obj, States.StringToJson('{"b":2}'), false)`: `{"a":1,"b":2}`,
		`States.MathAdd($.n, -1)`:                                        `2`,
		`States.StringSplit($.csv, ',;')`:                                `["a","b","c"]`,
		`States.ArrayLength(States.StringSplit('a b c', ' '))`:           `3`,
		`States.Format('{}', States.JsonToString(States.Array(1, 2)))`:   `"[1,2]"`,
	}
	for expr, want := range cases {
		v, err := evalIntrinsic(expr, input, ctx)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if got := mustJSON(v); got != want {
			t.Errorf("%s = %s, want %s", expr, got, want)
		}
	}
	v, err := evalIntrinsic("States.MathRandom(10, 20)", input, ctx)
	if f, _ := v.(float64); err != nil || f < 10 || f >= 20 {
		t.Errorf("MathRandom = %v, %v", v, err)
	}
	a, _ := evalIntrinsic("States.MathRandom(0, 1000, 7)", input, ctx)
	b, _ := evalIntrinsic("States.MathRandom(0, 1000, 7)", input, ctx)
	if a != b {
		t.Errorf("seeded MathRandom differs: %v %v", a, b)
	}
	u, _ := evalIntrinsic("States.UUID()", input, ctx)
	if s, _ := u.(string); len(s) != 36 || s[14] != '4' {
		t.Errorf("UUID %v", u)
	}
	for _, bad := range []string{"States.Nope(1)", "States.Format('{} {}', 1)", "States.ArrayGetItem($.list, 9)", "States.StringToJson($.n)",
		"States.Hash('a', 'SHA-3')", "States.JsonMerge($.obj, $.obj, true)", "States.ArrayRange(1, 5, 0)", "States.Format('x'", "States.Base64Decode('!!')"} {
		_, err := evalIntrinsic(bad, input, ctx)
		se, ok := err.(*StateError)
		if !ok || (se.Name != "States.IntrinsicFailure" && se.Name != "States.Runtime") {
			t.Errorf("%s: expected an intrinsic failure, got %v", bad, err)
		}
	}
}

func TestJSONPath(t *testing.T) {
	doc := map[string]any{"items": []any{
		map[string]any{"id": "a", "price": 5.0, "tag": "x"},
		map[string]any{"id": "b", "price": 15.0},
		map[string]any{"id": "c", "price": 25.0, "tag": "y"},
	}, "odd key": 1.0}
	cases := map[string]string{
		"$.items[0].id":                  `"a"`,
		"$.items[-1].id":                 `"c"`,
		"$.items[*].id":                  `["a","b","c"]`,
		"$.items[1:].id":                 `["b","c"]`,
		"$.items[?(@.price > 10)].id":    `["b","c"]`,
		"$.items[?(@.tag)].id":           `["a","c"]`,
		"$.items[?(@.tag == 'y')].price": `[25]`,
		"$['odd key']":                   `1`,
		"$.items[?(@.price > 100)]":      `[]`,
	}
	for p, want := range cases {
		v, err := get(doc, p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if got := mustJSON(v); got != want {
			t.Errorf("%s = %s, want %s", p, got, want)
		}
	}
	if _, err := get(doc, "$.missing"); err == nil {
		t.Error("missing field should fail")
	}
	if _, err := set(doc, "$.items[0]", 1); err == nil {
		t.Error("ResultPath with an index should fail")
	}
}

func TestChoiceRules(t *testing.T) {
	in := map[string]any{"s": "img/cat.png", "n": 5.0, "b": true, "t": "2026-01-01T00:00:00Z", "t2": "2026-06-01T00:00:00Z", "nil": nil}
	cases := []struct {
		rule string
		want bool
	}{
		{`{"Variable":"$.s","StringMatches":"img/*.png"}`, true},
		{`{"Variable":"$.s","StringMatches":"*.jpg"}`, false},
		{`{"Variable":"$.s","StringMatches":"img\\*"}`, false},
		{`{"Variable":"$.n","NumericGreaterThanEquals":5}`, true},
		{`{"Variable":"$.t","TimestampLessThanPath":"$.t2"}`, true},
		{`{"Variable":"$.t","IsTimestamp":true}`, true},
		{`{"Variable":"$.s","IsTimestamp":true}`, false},
		{`{"Variable":"$.nil","IsNull":true}`, true},
		{`{"Variable":"$.nope","IsPresent":false}`, true},
		{`{"And":[{"Variable":"$.b","BooleanEquals":true},{"Not":{"Variable":"$.n","NumericEquals":4}}]}`, true},
		{`{"Or":[{"Variable":"$.n","NumericLessThan":1},{"Variable":"$.s","StringEquals":"x"}]}`, false},
	}
	for _, c := range cases {
		var rule map[string]any
		_ = json.Unmarshal([]byte(c.rule), &rule)
		if err := validateRule(rule, false); err != nil {
			t.Errorf("%s: invalid: %v", c.rule, err)
		}
		got, err := evalRule(rule, in)
		if err != nil || got != c.want {
			t.Errorf("%s = %v (%v), want %v", c.rule, got, err, c.want)
		}
	}
	var bad map[string]any
	_ = json.Unmarshal([]byte(`{"Variable":"$.n","NumericEqualz":1}`), &bad)
	if validateRule(bad, false) == nil {
		t.Error("unknown comparison accepted")
	}
}

func TestValidation(t *testing.T) {
	for _, def := range []string{
		`{"StartAt":"A","States":{"A":{"Type":"Pass","End":true,"Next":"A"}}}`,
		`{"StartAt":"A","States":{"A":{"Type":"Succeed","Retry":[{"ErrorEquals":["States.ALL"]}]}}}`,
		`{"StartAt":"A","States":{"A":{"Type":"Task","Resource":"arn:aws:states:::s3:getObject","End":true}}}`,
		`{"StartAt":"A","States":{"A":{"Type":"Task","Resource":"arn:aws:states:::dynamodb:putItem.sync","End":true}}}`,
		`{"StartAt":"A","States":{"A":{"Type":"Task","Resource":"arn:aws:states:us-east-1:1:activity:x","End":true}}}`,
		`{"StartAt":"A","States":{"A":{"Type":"Wait","End":true}}}`,
		`{"QueryLanguage":"JSONata","StartAt":"A","States":{"A":{"Type":"Pass","End":true}}}`,
		`{"StartAt":"A","States":{"A":{"Type":"Choice","Choices":[{"Variable":"$.x","Bogus":1,"Next":"A"}]}}}`,
	} {
		if m, _ := parse(json.RawMessage(def)); m != nil {
			t.Errorf("accepted invalid definition %s", def)
		}
	}
	for _, def := range []string{
		`{"StartAt":"A","States":{"A":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:dynamodb:getItem","End":true}}}`,
		`{"StartAt":"A","States":{"A":{"Type":"Task","Resource":"arn:aws:states:::states:startExecution.sync:2","End":true}}}`,
		`{"StartAt":"A","States":{"A":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke.waitForTaskToken","HeartbeatSeconds":5,"TimeoutSeconds":60,"End":true}}}`,
	} {
		if m, errs := parse(json.RawMessage(def)); m == nil {
			t.Errorf("rejected %s: %v", def, errs)
		}
	}
}

func TestMapParallelAndContext(t *testing.T) {
	def := `{"StartAt":"Fan","States":{
	  "Fan":{"Type":"Map","ItemsPath":"$.orders","MaxConcurrency":2,
	    "ItemSelector":{"order.$":"$$.Map.Item.Value","index.$":"$$.Map.Item.Index","customer.$":"$.customer"},
	    "ItemProcessor":{"StartAt":"Label","States":{"Label":{"Type":"Pass","Parameters":{"label.$":"States.Format('{}#{} for {}', $.order.id, $.index, $.customer)"},"End":true}}},
	    "ResultPath":"$.labels","Next":"Both"},
	  "Both":{"Type":"Parallel","Branches":[
	    {"StartAt":"Count","States":{"Count":{"Type":"Pass","Parameters":{"count.$":"States.ArrayLength($.labels)"},"End":true}}},
	    {"StartAt":"Name","States":{"Name":{"Type":"Pass","Parameters":{"exec.$":"$$.Execution.Name"},"End":true}}}
	  ],"ResultSelector":{"count.$":"$[0].count","exec.$":"$[1].exec"},"ResultPath":"$.summary","Next":"Done"},
	  "Done":{"Type":"Pass","OutputPath":"$.summary","End":true}}}`
	out, err, _ := run(t, def, map[string]any{"customer": "Ann", "orders": []any{map[string]any{"id": "o1"}, map[string]any{"id": "o2"}, map[string]any{"id": "o3"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(out); got != `{"count":3,"exec":"test"}` {
		t.Fatalf("output %s", got)
	}
}

func TestRetryAndFailPaths(t *testing.T) {
	def := `{"StartAt":"Call","States":{
	  "Call":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:1:function:flaky",
	    "Retry":[{"ErrorEquals":["TransientError"],"IntervalSeconds":0.01,"MaxAttempts":3,"BackoffRate":10,"MaxDelaySeconds":0.02,"JitterStrategy":"FULL"}],
	    "ResultPath":"$.r","Next":"Boom"},
	  "Boom":{"Type":"Fail","ErrorPath":"$.err","CausePath":"States.Format('failed after {}', $.r)"}}}`
	start := time.Now()
	_, err, ft := run(t, def, map[string]any{"err": "Custom.Error"})
	se, ok := err.(*StateError)
	if !ok || se.Name != "Custom.Error" || se.Cause != "failed after ok" {
		t.Fatalf("error %v", err)
	}
	if ft.calls["flaky"] != 3 || time.Since(start) > 2*time.Second {
		t.Fatalf("retries: %d calls in %v", ft.calls["flaky"], time.Since(start))
	}
	// States.TaskFailed catches task errors but not timeouts.
	if !matchesError([]string{"States.TaskFailed"}, "Lambda.Unknown") || matchesError([]string{"States.TaskFailed"}, "States.Timeout") ||
		!matchesError([]string{"States.Timeout"}, "States.HeartbeatTimeout") || matchesError([]string{"States.ALL"}, "States.Runtime") {
		t.Fatal("error matching")
	}
}

func TestTaskTokens(t *testing.T) {
	def := `{"StartAt":"Ask","States":{
	  "Ask":{"Type":"Task","Resource":"arn:aws:states:::sqs:sendMessage.waitForTaskToken",
	    "Parameters":{"QueueUrl":"https://sqs/1/approvals","MessageBody":{"token.$":"$$.Task.Token","order.$":"$.id"}},
	    "HeartbeatSeconds":1,"TimeoutSeconds":30,"ResultPath":"$.approval","End":true}}}`
	m, errs := parse(json.RawMessage(def))
	if m == nil {
		t.Fatal(errs)
	}
	sent := make(chan string, 2)
	ft := &tokenTasks{fakeTasks: fakeTasks{calls: map[string]int{}}, sent: sent}
	reg := newTokenRegistry()
	r := &runner{tasks: ft, record: func(string, string, any) {}, tokens: reg, context: map[string]any{}}
	done := make(chan any, 1)
	go func() {
		out, err := r.run(context.Background(), m, map[string]any{"id": "o-9"})
		if err != nil {
			done <- err
			return
		}
		done <- out
	}()
	body := <-sent
	var msg map[string]string
	_ = json.Unmarshal([]byte(body), &msg)
	if msg["order"] != "o-9" || !strings.HasPrefix(msg["token"], "AQ") {
		t.Fatalf("message %s", body)
	}
	time.Sleep(600 * time.Millisecond)
	if err := reg.heartbeat(msg["token"]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond) // past the first heartbeat window, but a heartbeat arrived
	if err := reg.complete(msg["token"], taskResult{output: map[string]any{"approved": true}}); err != nil {
		t.Fatal(err)
	}
	if out := <-done; mustJSON(out) != `{"approval":{"approved":true},"id":"o-9"}` {
		t.Fatalf("output %v", out)
	}
	if reg.complete(msg["token"], taskResult{}) == nil {
		t.Fatal("a used token should be gone")
	}

	// Without heartbeats the task times out.
	go func() {
		_, err := r.run(context.Background(), m, map[string]any{"id": "o-10"})
		done <- err
	}()
	<-sent
	if err, _ := (<-done).(*StateError); err == nil || err.Name != "States.HeartbeatTimeout" {
		t.Fatalf("heartbeat timeout: %v", err)
	}
}

type tokenTasks struct {
	fakeTasks
	sent chan string
}

func (f *tokenTasks) SendMessage(q, body string) (map[string]any, error) {
	f.sent <- body
	return map[string]any{"MessageId": "m1"}, nil
}
