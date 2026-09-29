package sfn

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc/events"
)

// awsTasks stands in for Lambda/SQS/SNS; queue messages are captured.
type awsTasks struct {
	fakeTasks
	mu   sync.Mutex
	sent []string
}

func (a *awsTasks) SendMessage(q, body string) (map[string]any, error) {
	a.mu.Lock()
	a.sent = append(a.sent, body)
	a.mu.Unlock()
	return map[string]any{"MessageId": "m-1", "MD5OfMessageBody": "x"}, nil
}

func (a *awsTasks) lastSent(t *testing.T) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		a.mu.Lock()
		if n := len(a.sent); n > 0 {
			s := a.sent[n-1]
			a.mu.Unlock()
			return s
		}
		a.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no message was sent")
	return ""
}

// fakeDynamo is a minimal awsJson "dynamodb" service, so integrations are
// exercised through awsapi.Call with the execution's principal.
type fakeDynamo struct {
	mu    sync.Mutex
	items map[string]any
}

func (d *fakeDynamo) register(account string) {
	arn := func(t any) string { return "arn:aws:dynamodb:us-east-1:" + account + ":table/" + t.(string) }
	awsapi.Register(&awsapi.Service{Name: "dynamodb", JSONPrefix: "DynamoDB_20120810", JSONVersion: "1.0", Ops: map[string]awsapi.Op{
		"PutItem": func(q *awsapi.Req) (any, error) {
			var in map[string]any
			_ = q.Bind(&in)
			if err := q.Authorize("dynamodb:PutItem", arn(in["TableName"])); err != nil {
				return nil, err
			}
			d.mu.Lock()
			d.items[in["TableName"].(string)] = in["Item"]
			d.mu.Unlock()
			return map[string]any{}, nil
		},
		"GetItem": func(q *awsapi.Req) (any, error) {
			var in map[string]any
			_ = q.Bind(&in)
			if err := q.Authorize("dynamodb:GetItem", arn(in["TableName"])); err != nil {
				return nil, err
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			it, ok := d.items[in["TableName"].(string)]
			if !ok {
				return map[string]any{}, nil
			}
			return map[string]any{"Item": it}, nil
		},
	}})
}

type sfnHarness struct {
	*awstest.Harness
	s     *Service
	tasks *awsTasks
	dyn   *fakeDynamo
	sent  chan string // events delivered by EventBridge rules
}

func newSFN(t *testing.T) *sfnHarness {
	t.Helper()
	h := awstest.New(t)
	s := New(h.Env)
	tasks := &awsTasks{fakeTasks: fakeTasks{calls: map[string]int{}}}
	s.Tasks = tasks
	s.Call = func(ctx context.Context, p *httpx.Principal, service, op string, in any) (json.RawMessage, error) {
		return awsapi.Call(ctx, p, h.Env.AccountID, service, op, in)
	}
	s.Role = func(arn string) (*httpx.Principal, error) {
		return h.IAM.ServiceRolePrincipal(arn, "states.amazonaws.com", "sfn")
	}
	s.RegisterAWS()
	s.Routes(h.Router)
	dyn := &fakeDynamo{items: map[string]any{}}
	dyn.register(h.Env.AccountID)
	ev := events.New(h.Env)
	sent := make(chan string, 10)
	ev.Deliver = func(ctx context.Context, arn string, payload []byte) error { sent <- string(payload); return nil }
	ev.RegisterAWS()
	return &sfnHarness{Harness: h, s: s, tasks: tasks, dyn: dyn, sent: sent}
}

func (h *sfnHarness) role(t *testing.T, name string, trust string, actions ...[2]string) string {
	t.Helper()
	var stmts []any
	for _, a := range actions {
		stmts = append(stmts, map[string]any{"Effect": "Allow", "Action": a[0], "Resource": a[1]})
	}
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": name,
		"assume_role_policy": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"Service": trust}, "Action": "sts:AssumeRole"}}}})
	if len(stmts) > 0 {
		h.Native(t, "PUT", "/api/v1/iam/roles/"+name+"/inline-policies/p", map[string]any{"Version": "2012-10-17", "Statement": stmts})
	}
	return "arn:aws:iam::" + h.Env.AccountID + ":role/" + name
}

func (h *sfnHarness) waitDone(t *testing.T, arn string) map[string]any {
	t.Helper()
	for i := 0; i < 200; i++ {
		d := h.AWSJSON(t, "stepfunctions", "describe-execution", "--execution-arn", arn)
		if d["status"] != "RUNNING" {
			return d
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("execution %s did not finish", arn)
	return nil
}

func TestStepFunctionsCLI(t *testing.T) {
	h := newSFN(t)
	acct := h.Env.AccountID
	role := h.role(t, "sfn-exec", "states.amazonaws.com",
		[2]string{"lambda:InvokeFunction", "arn:aws:lambda:*:*:function:double"},
		[2]string{"dynamodb:PutItem", "arn:aws:dynamodb:us-east-1:" + acct + ":table/orders"},
		[2]string{"dynamodb:GetItem", "arn:aws:dynamodb:us-east-1:" + acct + ":table/orders"},
		[2]string{"events:PutEvents", "*"})
	h.AWS(t, "events", "put-rule", "--name", "done", "--event-pattern", `{"source":["orders"]}`)
	h.AWS(t, "events", "put-targets", "--rule", "done", "--targets", `[{"Id":"q","Arn":"arn:aws:sqs:us-east-1:`+acct+`:audit"}]`)

	def := `{"Comment":"orders","StartAt":"Prepare","States":{
	  "Prepare":{"Type":"Pass","Parameters":{"id.$":"States.Format('order-{}', $.n)","n.$":"$.n","items.$":"States.ArrayRange(1, 3, 1)"},"Next":"Double"},
	  "Double":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"double","Payload":{"n.$":"$.n"}},
	    "ResultSelector":{"n.$":"$.Payload.n"},"ResultPath":"$.doubled","Retry":[{"ErrorEquals":["States.TaskFailed"],"MaxAttempts":1}],"Next":"Save"},
	  "Save":{"Type":"Task","Resource":"arn:aws:states:::dynamodb:putItem","Parameters":{"TableName":"orders","Item":{"id":{"S.$":"$.id"},"n":{"N.$":"States.JsonToString($.doubled.n)"}}},"ResultPath":null,"Next":"Load"},
	  "Load":{"Type":"Task","Resource":"arn:aws:states:::aws-sdk:dynamodb:getItem","Parameters":{"TableName":"orders","Key":{"id":{"S.$":"$.id"}}},"ResultSelector":{"n.$":"$.Item.n.N"},"ResultPath":"$.loaded","Next":"Big?"},
	  "Big?":{"Type":"Choice","Choices":[{"Variable":"$.doubled.n","NumericGreaterThan":10,"Next":"Fan"}],"Default":"Small"},
	  "Small":{"Type":"Fail","Error":"TooSmall","Cause":"n must double past 10"},
	  "Fan":{"Type":"Map","ItemsPath":"$.items","ItemSelector":{"i.$":"$$.Map.Item.Value","id.$":"$.id"},
	    "ItemProcessor":{"StartAt":"Line","States":{"Line":{"Type":"Pass","Parameters":{"line.$":"States.Format('{}-{}', $.id, $.i)"},"OutputPath":"$.line","End":true}}},
	    "ResultPath":"$.lines","Next":"Announce"},
	  "Announce":{"Type":"Task","Resource":"arn:aws:states:::events:putEvents","Parameters":{"Entries":[{"Source":"orders","DetailType":"OrderDone","Detail":{"id.$":"$.id"}}]},"ResultPath":null,"Next":"Pause"},
	  "Pause":{"Type":"Wait","Seconds":0,"Next":"Done"},
	  "Done":{"Type":"Succeed","OutputPath":"$.lines"}}}`
	sm := h.AWSJSON(t, "stepfunctions", "create-state-machine", "--name", "orders", "--definition", def, "--role-arn", role,
		"--tags", "key=team,value=core")
	arn := sm["stateMachineArn"].(string)
	if arn != "arn:aws:states:us-east-1:"+acct+":stateMachine:orders" {
		t.Fatalf("create %v", sm)
	}
	// Creating the same machine again is idempotent; a different definition conflicts.
	h.AWS(t, "stepfunctions", "create-state-machine", "--name", "orders", "--definition", def, "--role-arn", role)
	if out, err := h.AWSErr(t, "stepfunctions", "create-state-machine", "--name", "orders", "--definition", `{"StartAt":"A","States":{"A":{"Type":"Succeed"}}}`, "--role-arn", role); err == nil || !strings.Contains(out, "StateMachineAlreadyExists") {
		t.Fatalf("conflicting create: %v %s", err, out)
	}
	if out, err := h.AWSErr(t, "stepfunctions", "create-state-machine", "--name", "bad", "--definition", `{"StartAt":"X","States":{}}`, "--role-arn", role); err == nil || !strings.Contains(out, "InvalidDefinition") {
		t.Fatalf("invalid definition: %v %s", err, out)
	}
	d := h.AWSJSON(t, "stepfunctions", "describe-state-machine", "--state-machine-arn", arn)
	if d["roleArn"] != role || d["type"] != "STANDARD" || !strings.Contains(d["definition"].(string), "Prepare") {
		t.Fatalf("describe %v", d)
	}

	x := h.AWSJSON(t, "stepfunctions", "start-execution", "--state-machine-arn", arn, "--name", "run-1", "--input", `{"n": 6}`)
	done := h.waitDone(t, x["executionArn"].(string))
	if done["status"] != "SUCCEEDED" || done["output"] != `["order-6-1","order-6-2","order-6-3"]` {
		t.Fatalf("execution %v", done)
	}
	if h.dyn.items["orders"] == nil {
		t.Fatal("dynamodb:putItem did not store the item")
	}
	select {
	case ev := <-h.sent:
		if !strings.Contains(ev, `"detail":{"id":"order-6"}`) {
			t.Fatalf("event %s", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("events:putEvents did not reach the rule's target")
	}

	hist := h.AWSJSON(t, "stepfunctions", "get-execution-history", "--execution-arn", x["executionArn"].(string))
	var types []string
	for _, e := range hist["events"].([]any) {
		types = append(types, e.(map[string]any)["type"].(string))
	}
	joined := strings.Join(types, ",")
	for _, want := range []string{"ExecutionStarted,PassStateEntered,PassStateExited,TaskStateEntered,TaskScheduled,TaskStarted,TaskSucceeded,TaskStateExited",
		"MapStateStarted", "MapIterationStarted", "MapIterationSucceeded", "WaitStateEntered", "SucceedStateEntered", "ExecutionSucceeded"} {
		if !strings.Contains(joined, want) {
			t.Errorf("history lacks %s: %s", want, joined)
		}
	}
	first := hist["events"].([]any)[4].(map[string]any)["taskScheduledEventDetails"].(map[string]any)
	if first["resourceType"] != "lambda" || first["resource"] != "invoke" || !strings.Contains(first["parameters"].(string), "double") {
		t.Fatalf("taskScheduled %v", first)
	}
	rev := h.AWSJSON(t, "stepfunctions", "get-execution-history", "--execution-arn", x["executionArn"].(string), "--reverse-order", "--max-items", "1")
	if rev["events"].([]any)[0].(map[string]any)["type"] != "ExecutionSucceeded" {
		t.Fatalf("reverse history %v", rev)
	}

	// Failing path, idempotent start and list filters.
	x2 := h.AWSJSON(t, "stepfunctions", "start-execution", "--state-machine-arn", arn, "--name", "small", "--input", `{"n": 1}`)
	f := h.waitDone(t, x2["executionArn"].(string))
	if f["status"] != "FAILED" || f["error"] != "TooSmall" {
		t.Fatalf("failed execution %v", f)
	}
	if out, err := h.AWSErr(t, "stepfunctions", "start-execution", "--state-machine-arn", arn, "--name", "small", "--input", `{"n": 1}`); err == nil || !strings.Contains(out, "ExecutionAlreadyExists") {
		t.Fatalf("reused name: %v %s", err, out)
	}
	l := h.AWSJSON(t, "stepfunctions", "list-executions", "--state-machine-arn", arn, "--status-filter", "FAILED")
	if es := l["executions"].([]any); len(es) != 1 || es[0].(map[string]any)["name"] != "small" {
		t.Fatalf("list-executions %v", l)
	}
	if ms := h.AWSJSON(t, "stepfunctions", "list-state-machines"); len(ms["stateMachines"].([]any)) != 1 {
		t.Fatalf("list-state-machines %v", ms)
	}
	tags := h.AWSJSON(t, "stepfunctions", "list-tags-for-resource", "--resource-arn", arn)
	if tg := tags["tags"].([]any); len(tg) != 1 || tg[0].(map[string]any)["value"] != "core" {
		t.Fatalf("tags %v", tags)
	}
	v := h.AWSJSON(t, "stepfunctions", "validate-state-machine-definition", "--definition", `{"StartAt":"A","States":{"A":{"Type":"Pass"}}}`)
	if v["result"] != "FAIL" || len(v["diagnostics"].([]any)) != 1 {
		t.Fatalf("validate %v", v)
	}
	h.AWS(t, "stepfunctions", "update-state-machine", "--state-machine-arn", arn, "--definition", `{"StartAt":"A","States":{"A":{"Type":"Wait","Seconds":30,"End":true}}}`)
	x3 := h.AWSJSON(t, "stepfunctions", "start-execution", "--state-machine-arn", arn, "--name", "idem", "--input", `{"a":1}`)
	// The same name and input while running returns the same execution; other input conflicts.
	if again := h.AWSJSON(t, "stepfunctions", "start-execution", "--state-machine-arn", arn, "--name", "idem", "--input", `{"a": 1}`); again["executionArn"] != x3["executionArn"] {
		t.Fatalf("idempotent start: %v vs %v", again, x3)
	}
	if out, err := h.AWSErr(t, "stepfunctions", "start-execution", "--state-machine-arn", arn, "--name", "idem", "--input", `{"a":2}`); err == nil || !strings.Contains(out, "ExecutionAlreadyExists") {
		t.Fatalf("conflicting start: %v %s", err, out)
	}
	h.AWS(t, "stepfunctions", "stop-execution", "--execution-arn", x3["executionArn"].(string), "--error", "Operator.Stop", "--cause", "maintenance")
	s := h.waitDone(t, x3["executionArn"].(string))
	if s["status"] != "ABORTED" || s["error"] != "Operator.Stop" || s["cause"] != "maintenance" {
		t.Fatalf("stopped execution %v", s)
	}
	h.AWS(t, "stepfunctions", "delete-state-machine", "--state-machine-arn", arn)
	if out, err := h.AWSErr(t, "stepfunctions", "describe-state-machine", "--state-machine-arn", arn); err == nil || !strings.Contains(out, "StateMachineDoesNotExist") {
		t.Fatalf("deleted machine: %v %s", err, out)
	}
}

func TestStepFunctionsRolesAndTokens(t *testing.T) {
	h := newSFN(t)
	acct := h.Env.AccountID
	role := h.role(t, "narrow", "states.amazonaws.com", [2]string{"sqs:SendMessage", "*"})
	untrusted := h.role(t, "lambda-only", "lambda.amazonaws.com")
	callback := `{"StartAt":"Ask","States":{
	  "Ask":{"Type":"Task","Resource":"arn:aws:states:::sqs:sendMessage.waitForTaskToken",
	    "Parameters":{"QueueUrl":"https://sqs.us-east-1.amazonaws.com/` + acct + `/approvals","MessageBody":{"token.$":"$$.Task.Token"}},
	    "ResultPath":"$.answer","Next":"Invoke"},
	  "Invoke":{"Type":"Task","Resource":"arn:aws:lambda:us-east-1:` + acct + `:function:double",
	    "Catch":[{"ErrorEquals":["Lambda.AccessDeniedException"],"ResultPath":"$.denied","Next":"Done"}],"End":true},
	  "Done":{"Type":"Pass","End":true}}}`
	if out, err := h.AWSErr(t, "stepfunctions", "create-state-machine", "--name", "x", "--definition", callback, "--role-arn", untrusted); err == nil || !strings.Contains(out, "states.amazonaws.com") {
		t.Fatalf("untrusted role accepted: %v %s", err, out)
	}
	// A caller needs iam:PassRole for the role.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "sfn-no-pass", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "states:*", "Resource": "*"}}}})
	akid, secret := h.User(t, "dev", "sfn-no-pass")
	if out, err := h.AWSAs(t, akid, secret, "", "stepfunctions", "create-state-machine", "--name", "x", "--definition", callback, "--role-arn", role); err == nil || !strings.Contains(out, "iam:PassRole") {
		t.Fatalf("create without PassRole: %v %s", err, out)
	}
	arn := h.AWSJSON(t, "stepfunctions", "create-state-machine", "--name", "approve", "--definition", callback, "--role-arn", role)["stateMachineArn"].(string)
	x := h.AWSJSON(t, "stepfunctions", "start-execution", "--state-machine-arn", arn)["executionArn"].(string)
	var msg map[string]string
	_ = json.Unmarshal([]byte(h.tasks.lastSent(t)), &msg)
	if out, err := h.AWSErr(t, "stepfunctions", "send-task-success", "--task-token", "AQbogus", "--task-output", `{}`); err == nil || !strings.Contains(out, "TaskDoesNotExist") {
		t.Fatalf("bogus token: %v %s", err, out)
	}
	h.AWS(t, "stepfunctions", "send-task-heartbeat", "--task-token", msg["token"])
	h.AWS(t, "stepfunctions", "send-task-success", "--task-token", msg["token"], "--task-output", `{"approved":true}`)
	d := h.waitDone(t, x)
	// The role may send to SQS but not invoke Lambda: the Catch handles the denial.
	if d["status"] != "SUCCEEDED" || !strings.Contains(d["output"].(string), `"approved":true`) || !strings.Contains(d["output"].(string), "Lambda.AccessDeniedException") {
		t.Fatalf("execution %v", d)
	}
	if h.tasks.calls["double"] != 0 {
		t.Fatal("the function was invoked despite the role's policy")
	}

	// SendTaskFailure fails the task.
	x = h.AWSJSON(t, "stepfunctions", "start-execution", "--state-machine-arn", arn)["executionArn"].(string)
	time.Sleep(100 * time.Millisecond)
	_ = json.Unmarshal([]byte(h.tasks.lastSent(t)), &msg)
	h.AWS(t, "stepfunctions", "send-task-failure", "--task-token", msg["token"], "--error", "Rejected", "--cause", "no budget")
	if d := h.waitDone(t, x); d["status"] != "FAILED" || d["error"] != "Rejected" {
		t.Fatalf("failed task %v", d)
	}
}

func TestStepFunctionsBoto3(t *testing.T) {
	h := newSFN(t)
	role := h.role(t, "exec", "states.amazonaws.com", [2]string{"states:StartExecution", "*"}, [2]string{"lambda:InvokeFunction", "*"})
	out := h.Python(t, `
from botocore.config import Config
sfn = boto3.client("stepfunctions")
child = sfn.create_state_machine(name="child", roleArn="`+role+`", definition=json.dumps({"StartAt": "D", "States": {
    "D": {"Type": "Task", "Resource": "arn:aws:states:::lambda:invoke", "Parameters": {"FunctionName": "double", "Payload.$": "$"}, "OutputPath": "$.Payload", "End": True}}}))["stateMachineArn"]
parent = sfn.create_state_machine(name="parent", roleArn="`+role+`", definition=json.dumps({"StartAt": "Run", "States": {
    "Run": {"Type": "Task", "Resource": "arn:aws:states:::states:startExecution.sync:2",
            "Parameters": {"StateMachineArn": child, "Input": {"n.$": "$.n"}}, "OutputPath": "$.Output", "End": True}}}))["stateMachineArn"]
fast = sfn.create_state_machine(name="fast", type="EXPRESS", roleArn="`+role+`", definition=json.dumps({"StartAt": "P", "States": {
    "P": {"Type": "Pass", "Parameters": {"sum.$": "States.MathAdd($.a, $.b)"}, "End": True}}}))["stateMachineArn"]
import time
x = sfn.start_execution(stateMachineArn=parent, input=json.dumps({"n": 21}))["executionArn"]
for _ in range(100):
    d = sfn.describe_execution(executionArn=x)
    if d["status"] != "RUNNING":
        break
    time.sleep(0.05)
print("parent", d["status"], d.get("output"))
sync = boto3.client("stepfunctions", config=Config(inject_host_prefix=False))
r = sync.start_sync_execution(stateMachineArn=fast, input=json.dumps({"a": 2, "b": 3}))
print("sync", r["status"], r["output"], r["executionArn"].split(":")[5])
try:
    sync.start_sync_execution(stateMachineArn=child, input="{}")
except botocore.exceptions.ClientError as e:
    print("standard sync", e.response["Error"]["Code"])
try:
    sfn.start_execution(stateMachineArn=parent, input="not json")
except sfn.exceptions.InvalidExecutionInput as e:
    print("input", e.response["Error"]["Code"])
try:
    sfn.describe_execution(executionArn=x + "nope")
except sfn.exceptions.ExecutionDoesNotExist as e:
    print("missing", e.response["Error"]["Code"])
pages = list(sfn.get_paginator("list_executions").paginate(stateMachineArn=child, PaginationConfig={"PageSize": 1}))
print("child runs", sum(len(p["executions"]) for p in pages))
`)
	for _, want := range []string{`parent SUCCEEDED {"n":42}`, `sync SUCCEEDED {"sum":5} express`, "standard sync StateMachineTypeNotSupported",
		"input InvalidExecutionInput", "missing ExecutionDoesNotExist", "child runs 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
