package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

type delivery struct {
	arn, payload string
}

type sink struct {
	mu   sync.Mutex
	got  []delivery
	fail map[string]int // ARN -> failures before success (-1 = always)
}

func (k *sink) deliver(ctx context.Context, arn string, payload []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if n := k.fail[arn]; n != 0 {
		if n > 0 {
			k.fail[arn] = n - 1
		}
		return errors.New("target unavailable")
	}
	k.got = append(k.got, delivery{arn, string(payload)})
	return nil
}

func (k *sink) wait(t *testing.T, n int) []delivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		k.mu.Lock()
		if len(k.got) >= n {
			out := append([]delivery(nil), k.got...)
			k.mu.Unlock()
			return out
		}
		k.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	t.Fatalf("expected %d deliveries, got %v", n, k.got)
	return nil
}

func (k *sink) reset() {
	k.mu.Lock()
	k.got = nil
	k.mu.Unlock()
}

func newAWS(t *testing.T) (*awstest.Harness, *Service, *sink) {
	t.Helper()
	h := awstest.New(t)
	s := New(h.Env)
	s.retryBase = 5 * time.Millisecond
	k := &sink{fail: map[string]int{}}
	s.Deliver = k.deliver
	s.RegisterAWS()
	s.Routes(h.Router)
	return h, s, k
}

func TestEventBridgeCLI(t *testing.T) {
	h, s, k := newAWS(t)
	acct := h.Env.AccountID
	queue := "arn:aws:sqs:us-east-1:" + acct + ":orders"
	dlq := "arn:aws:sqs:us-east-1:" + acct + ":dlq"
	fn := "arn:aws:lambda:us-east-1:" + acct + ":function:audit"

	bus := h.AWSJSON(t, "events", "create-event-bus", "--name", "shop", "--tags", "Key=team,Value=core")
	if bus["EventBusArn"] != "arn:aws:events:us-east-1:"+acct+":event-bus/shop" {
		t.Fatalf("bus %v", bus)
	}
	if out, err := h.AWSErr(t, "events", "create-event-bus", "--name", "shop"); err == nil || !strings.Contains(out, "ResourceAlreadyExistsException") {
		t.Fatalf("duplicate bus: %v %s", err, out)
	}
	if l := h.AWSJSON(t, "events", "list-event-buses"); len(l["EventBuses"].([]any)) != 2 {
		t.Fatalf("buses %v", l)
	}
	if out, err := h.AWSErr(t, "events", "put-rule", "--name", "bad", "--event-pattern", `{"source": "x"}`); err == nil || !strings.Contains(out, "InvalidEventPatternException") {
		t.Fatalf("invalid pattern: %v %s", err, out)
	}
	rule := h.AWSJSON(t, "events", "put-rule", "--name", "big-orders", "--event-bus-name", "shop",
		"--event-pattern", `{"source": ["shop.orders"], "detail": {"total": [{"numeric": [">=", 100]}], "country": [{"anything-but": ["XX"]}]}}`)
	if rule["RuleArn"] != "arn:aws:events:us-east-1:"+acct+":rule/shop/big-orders" {
		t.Fatalf("rule %v", rule)
	}
	pt := h.AWSJSON(t, "events", "put-targets", "--rule", "big-orders", "--event-bus-name", "shop", "--targets",
		`[{"Id":"q","Arn":"`+queue+`","InputTransformer":{"InputPathsMap":{"id":"$.detail.id","total":"$.detail.total"},"InputTemplate":"{\"order\": <id>, \"amount\": <total>, \"msg\": \"order <id> placed\"}"}},
		  {"Id":"f","Arn":"`+fn+`","InputPath":"$.detail","RetryPolicy":{"MaximumRetryAttempts":0},"DeadLetterConfig":{"Arn":"`+dlq+`"}},
		  {"Id":"ecs","Arn":"arn:aws:ecs:us-east-1:`+acct+`:cluster/x","EcsParameters":{"TaskDefinitionArn":"x"}}]`)
	if pt["FailedEntryCount"].(float64) != 1 || pt["FailedEntries"].([]any)[0].(map[string]any)["TargetId"] != "ecs" {
		t.Fatalf("put-targets %v", pt)
	}
	k.mu.Lock()
	k.fail[fn] = -1 // the function always fails: after 0 retries the event goes to the DLQ
	k.mu.Unlock()
	res := h.AWSJSON(t, "events", "put-events", "--entries",
		`[{"Source":"shop.orders","DetailType":"OrderPlaced","Detail":"{\"id\":\"o-1\",\"total\":250,\"country\":\"DE\"}","EventBusName":"shop"},
		  {"Source":"shop.orders","DetailType":"OrderPlaced","Detail":"{\"id\":\"o-2\",\"total\":5}","EventBusName":"shop"},
		  {"Source":"shop.orders","DetailType":"OrderPlaced","Detail":"not json","EventBusName":"shop"},
		  {"Source":"shop.orders","DetailType":"OrderPlaced","Detail":"{\"id\":\"o-3\",\"total\":500}"}]`)
	if res["FailedEntryCount"].(float64) != 1 || res["Entries"].([]any)[2].(map[string]any)["ErrorCode"] != "MalformedDetail" {
		t.Fatalf("put-events %v", res)
	}
	got := k.wait(t, 2)
	byARN := map[string]string{}
	for _, d := range got {
		byARN[d.arn] = d.payload
	}
	var q map[string]any
	if err := json.Unmarshal([]byte(byARN[queue]), &q); err != nil || q["order"] != "o-1" || q["amount"].(float64) != 250 || q["msg"] != "order o-1 placed" {
		t.Fatalf("queue payload %q", byARN[queue])
	}
	if !strings.Contains(byARN[dlq], `"id":"o-1"`) {
		t.Fatalf("dead-letter payload %q (all: %v)", byARN[dlq], got)
	}
	r, _ := store.Get[Rule](s.env.Store, cRules, "shop/big-orders")
	if r.Invocations != 1 || r.FailedInvocations != 1 {
		t.Fatalf("rule counters %+v", r)
	}

	d := h.AWSJSON(t, "events", "describe-rule", "--name", "big-orders", "--event-bus-name", "shop")
	if d["State"] != "ENABLED" || d["EventBusName"] != "shop" || !strings.Contains(d["EventPattern"].(string), "numeric") {
		t.Fatalf("describe-rule %v", d)
	}
	lt := h.AWSJSON(t, "events", "list-targets-by-rule", "--rule", "big-orders", "--event-bus-name", "shop")
	if ts := lt["Targets"].([]any); len(ts) != 2 || ts[1].(map[string]any)["DeadLetterConfig"].(map[string]any)["Arn"] != dlq {
		t.Fatalf("targets %v", lt)
	}
	names := h.AWSJSON(t, "events", "list-rule-names-by-target", "--target-arn", queue, "--event-bus-name", "shop")
	if n := names["RuleNames"].([]any); len(n) != 1 || n[0] != "big-orders" {
		t.Fatalf("rule names %v", names)
	}
	tp := h.AWSJSON(t, "events", "test-event-pattern", "--event-pattern", `{"detail":{"size":[{"numeric":[">",10]}]}}`,
		"--event", `{"id":"1","account":"`+acct+`","source":"x","time":"2026-01-01T00:00:00Z","region":"us-east-1","resources":[],"detail-type":"t","detail":{"size":11}}`)
	if tp["Result"] != true {
		t.Fatalf("test-event-pattern %v", tp)
	}

	// Disabled rules do not match.
	k.reset()
	h.AWS(t, "events", "disable-rule", "--name", "big-orders", "--event-bus-name", "shop")
	h.AWS(t, "events", "put-events", "--entries", `[{"Source":"shop.orders","DetailType":"x","Detail":"{\"total\":900}","EventBusName":"shop"}]`)
	time.Sleep(100 * time.Millisecond)
	k.mu.Lock()
	n := len(k.got)
	k.mu.Unlock()
	if n != 0 {
		t.Fatalf("disabled rule delivered %d events", n)
	}

	h.AWS(t, "events", "tag-resource", "--resource-arn", rule["RuleArn"].(string), "--tags", "Key=env,Value=dev")
	if tg := h.AWSJSON(t, "events", "list-tags-for-resource", "--resource-arn", rule["RuleArn"].(string)); len(tg["Tags"].([]any)) != 1 {
		t.Fatalf("rule tags %v", tg)
	}
	if tg := h.AWSJSON(t, "events", "list-tags-for-resource", "--resource-arn", bus["EventBusArn"].(string)); tg["Tags"].([]any)[0].(map[string]any)["Key"] != "team" {
		t.Fatalf("bus tags %v", tg)
	}
	if out, err := h.AWSErr(t, "events", "delete-rule", "--name", "big-orders", "--event-bus-name", "shop"); err == nil || !strings.Contains(out, "has targets") {
		t.Fatalf("delete rule with targets: %v %s", err, out)
	}
	h.AWS(t, "events", "remove-targets", "--rule", "big-orders", "--event-bus-name", "shop", "--ids", "q", "f")
	h.AWS(t, "events", "delete-rule", "--name", "big-orders", "--event-bus-name", "shop")
	h.AWS(t, "events", "delete-event-bus", "--name", "shop")
	if out, err := h.AWSErr(t, "events", "describe-event-bus", "--name", "shop"); err == nil || !strings.Contains(out, "ResourceNotFoundException") {
		t.Fatalf("deleted bus: %v %s", err, out)
	}

	// Scheduled rules on the default bus.
	k.reset()
	h.AWS(t, "events", "put-rule", "--name", "every-minute", "--schedule-expression", "rate(1 minute)")
	h.AWS(t, "events", "put-targets", "--rule", "every-minute", "--targets", `[{"Id":"1","Arn":"`+queue+`","Input":"{\"ping\":true}"}]`)
	s.tick(context.Background(), time.Now().Add(2*time.Minute))
	if got := k.wait(t, 1); got[0].payload != `{"ping":true}` {
		t.Fatalf("scheduled delivery %v", got)
	}
	if out, err := h.AWSErr(t, "events", "put-rule", "--name", "bad-sched", "--schedule-expression", "rate(5 fortnights)"); err == nil || !strings.Contains(out, "ValidationException") {
		t.Fatalf("bad schedule: %v %s", err, out)
	}
}

func TestEventBridgePermissions(t *testing.T) {
	h, _, _ := newAWS(t)
	acct := h.Env.AccountID
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "events-only", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "events:*", "Resource": "*"}}}})
	akid, secret := h.User(t, "ev", "events-only")
	if out, err := h.AWSAs(t, akid, secret, "", "events", "put-rule", "--name", "r", "--event-pattern", `{"source":["a"]}`); err != nil {
		t.Fatalf("put-rule: %v %s", err, out)
	}
	// Delivering to a queue needs sqs:SendMessage on it.
	out, err := h.AWSAs(t, akid, secret, "", "events", "put-targets", "--rule", "r", "--targets", `[{"Id":"1","Arn":"arn:aws:sqs:us-east-1:`+acct+`:q"}]`)
	if err == nil || !strings.Contains(out, "AccessDenied") || !strings.Contains(out, "sqs:SendMessage") {
		t.Fatalf("put-targets without sqs permission: %v %s", err, out)
	}
}

func TestSchedulerCLI(t *testing.T) {
	h, s, k := newAWS(t)
	acct := h.Env.AccountID
	fn := "arn:aws:lambda:us-east-1:" + acct + ":function:report"
	role := "arn:aws:iam::" + acct + ":role/scheduler"
	when := time.Now().Add(3 * time.Minute).In(time.FixedZone("x", 0))
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	local := when.In(loc).Format("2006-01-02T15:04:05")
	out := h.AWSJSON(t, "scheduler", "create-schedule", "--name", "once", "--schedule-expression", "at("+local+")",
		"--schedule-expression-timezone", "Asia/Kolkata", "--flexible-time-window", "Mode=OFF",
		"--target", `{"Arn":"`+fn+`","RoleArn":"`+role+`","Input":"{\"report\":\"daily\"}"}`, "--action-after-completion", "DELETE")
	if out["ScheduleArn"] != "arn:aws:scheduler:us-east-1:"+acct+":schedule/default/once" {
		t.Fatalf("create-schedule %v", out)
	}
	if o, err := h.AWSErr(t, "scheduler", "create-schedule", "--name", "once", "--schedule-expression", "rate(5 minutes)", "--flexible-time-window", "Mode=OFF",
		"--target", `{"Arn":"`+fn+`","RoleArn":"`+role+`"}`); err == nil || !strings.Contains(o, "ConflictException") {
		t.Fatalf("duplicate schedule: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "scheduler", "create-schedule", "--name", "bad", "--schedule-expression", "cron(0 9 * * * *)", "--flexible-time-window", "Mode=OFF",
		"--target", `{"Arn":"`+fn+`","RoleArn":"`+role+`"}`); err == nil || !strings.Contains(o, "ValidationException") {
		t.Fatalf("bad cron: %v %s", err, o)
	}
	g := h.AWSJSON(t, "scheduler", "get-schedule", "--name", "once")
	if g["ScheduleExpressionTimezone"] != "Asia/Kolkata" || g["State"] != "ENABLED" || g["Target"].(map[string]any)["Arn"] != fn {
		t.Fatalf("get-schedule %v", g)
	}
	h.AWS(t, "scheduler", "create-schedule-group", "--name", "reports", "--tags", "Key=team,Value=data")
	h.AWS(t, "scheduler", "create-schedule", "--name", "hourly", "--group-name", "reports", "--schedule-expression", "cron(0 * * * ? *)",
		"--flexible-time-window", "Mode=FLEXIBLE,MaximumWindowInMinutes=5", "--target", `{"Arn":"`+fn+`","RoleArn":"`+role+`"}`)
	l := h.AWSJSON(t, "scheduler", "list-schedules")
	if n := len(l["Schedules"].([]any)); n != 2 {
		t.Fatalf("list-schedules %v", l)
	}
	if l := h.AWSJSON(t, "scheduler", "list-schedules", "--group-name", "reports"); len(l["Schedules"].([]any)) != 1 {
		t.Fatalf("list by group %v", l)
	}
	h.AWS(t, "scheduler", "update-schedule", "--name", "hourly", "--group-name", "reports", "--schedule-expression", "cron(30 * * * ? *)",
		"--flexible-time-window", "Mode=OFF", "--state", "DISABLED", "--target", `{"Arn":"`+fn+`","RoleArn":"`+role+`"}`)
	if g := h.AWSJSON(t, "scheduler", "get-schedule", "--name", "hourly", "--group-name", "reports"); g["State"] != "DISABLED" {
		t.Fatalf("updated schedule %v", g)
	}
	tags := h.AWSJSON(t, "scheduler", "list-tags-for-resource", "--resource-arn", "arn:aws:scheduler:us-east-1:"+acct+":schedule-group/reports")
	if tg := tags["Tags"].([]any); len(tg) != 1 || tg[0].(map[string]any)["Value"] != "data" {
		t.Fatalf("group tags %v", tags)
	}

	// Not yet due; then due: fires once and deletes itself.
	s.tickSchedules(context.Background(), when.Add(-time.Minute))
	s.tickSchedules(context.Background(), when.Add(time.Second))
	got := k.wait(t, 1)
	if got[0].arn != fn || got[0].payload != `{"report":"daily"}` {
		t.Fatalf("schedule delivery %v", got)
	}
	if o, err := h.AWSErr(t, "scheduler", "get-schedule", "--name", "once"); err == nil || !strings.Contains(o, "ResourceNotFoundException") {
		t.Fatalf("one-time schedule should delete itself: %v %s", err, o)
	}
	h.AWS(t, "scheduler", "delete-schedule-group", "--name", "reports")
	if l := h.AWSJSON(t, "scheduler", "list-schedules"); len(l["Schedules"].([]any)) != 0 {
		t.Fatalf("group deletion keeps schedules: %v", l)
	}
}

func TestEventBridgeBoto3(t *testing.T) {
	h, _, k := newAWS(t)
	out := h.Python(t, `
ev = boto3.client("events")
for i in range(3):
    ev.put_rule(Name="r%d" % i, EventPattern=json.dumps({"source": ["b"]}))
ev.put_targets(Rule="r0", Targets=[{"Id": "t", "Arn": "arn:aws:sns:us-east-1:`+h.Env.AccountID+`:topic"}])
pages = list(ev.get_paginator("list_rules").paginate(PaginationConfig={"PageSize": 2}))
print("pages", len(pages), sum(len(p["Rules"]) for p in pages))
r = ev.put_events(Entries=[{"Source": "b", "DetailType": "d", "Detail": json.dumps({"k": 1})}, {"Source": "b", "DetailType": "d"}])
print("failed", r["FailedEntryCount"], r["Entries"][1]["ErrorCode"])
try:
    ev.describe_rule(Name="nope")
except ev.exceptions.ResourceNotFoundException as e:
    print("missing", e.response["Error"]["Code"])
s = boto3.client("scheduler")
print("groups", [g["Name"] for g in s.list_schedule_groups()["ScheduleGroups"]])
`)
	for _, want := range []string{"pages 2 3", "failed 1 InvalidArgument", "missing ResourceNotFoundException", "groups ['default']"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if got := k.wait(t, 1); !strings.Contains(got[0].payload, `"detail":{"k":1}`) {
		t.Fatalf("delivery %v", got)
	}
}
