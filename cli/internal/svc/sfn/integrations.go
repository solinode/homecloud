package sfn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// Task resources:
//
//	arn:aws:lambda:<region>:<account>:function:<name>        invoke a function (legacy form)
//	arn:aws:states:::lambda:invoke[.waitForTaskToken]
//	arn:aws:states:::sqs:sendMessage[.waitForTaskToken]
//	arn:aws:states:::sns:publish[.waitForTaskToken]
//	arn:aws:states:::dynamodb:getItem|putItem|updateItem|deleteItem
//	arn:aws:states:::states:startExecution[.sync|.sync:2|.waitForTaskToken]
//	arn:aws:states:::events:putEvents[.waitForTaskToken]
//	arn:aws:states:::aws-sdk:<service>:<action>[.waitForTaskToken]   any awsJson operation HomeCloud serves

type resource struct {
	raw      string
	service  string // lambda, sqs, sns, dynamodb, states, events, or the aws-sdk service
	action   string
	pattern  string // "", "sync", "sync:2", "waitForTaskToken"
	sdk      bool
	legacyFn string
}

var optimized = map[string]map[string][]string{
	"lambda":   {"invoke": {"", "waitForTaskToken"}},
	"sqs":      {"sendMessage": {"", "waitForTaskToken"}},
	"sns":      {"publish": {"", "waitForTaskToken"}},
	"dynamodb": {"getItem": {""}, "putItem": {""}, "updateItem": {""}, "deleteItem": {""}},
	"states":   {"startExecution": {"", "sync", "sync:2", "waitForTaskToken"}},
	"events":   {"putEvents": {"", "waitForTaskToken"}},
}

func parseResource(r string) (resource, error) {
	res := resource{raw: r}
	c := core.CanonicalARN(r)
	parts := strings.SplitN(c, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" || parts[1] != core.Partition {
		return res, fmt.Errorf("Resource %q is not an ARN", r)
	}
	switch {
	case parts[2] == "lambda" && strings.HasPrefix(parts[5], "function:"):
		res.service, res.legacyFn = "lambda", c
		return res, nil
	case parts[2] == "states" && strings.HasPrefix(parts[5], "activity:"):
		return res, fmt.Errorf("activities are not supported (Resource %s)", r)
	case parts[2] != "states" || parts[4] != "":
		return res, fmt.Errorf("unsupported Resource %q", r)
	}
	rest := parts[5]
	if i := strings.Index(rest, "."); i >= 0 {
		rest, res.pattern = rest[:i], rest[i+1:]
	}
	if sdk, ok := strings.CutPrefix(rest, "aws-sdk:"); ok {
		svc, action, ok := strings.Cut(sdk, ":")
		if !ok || svc == "" || action == "" {
			return res, fmt.Errorf("aws-sdk resources look like arn:aws:states:::aws-sdk:<service>:<action>")
		}
		res.sdk, res.service, res.action = true, svc, action
		if res.pattern != "" && res.pattern != "waitForTaskToken" {
			return res, fmt.Errorf("aws-sdk integrations support only the .waitForTaskToken pattern")
		}
		return res, nil
	}
	svc, action, _ := strings.Cut(rest, ":")
	res.service, res.action = svc, action
	patterns, ok := optimized[svc][action]
	if !ok {
		return res, fmt.Errorf("unsupported service integration %s:%s (supported: lambda:invoke, sqs:sendMessage, sns:publish, dynamodb:getItem/putItem/updateItem/deleteItem, states:startExecution, events:putEvents, aws-sdk:*)", svc, action)
	}
	for _, p := range patterns {
		if p == res.pattern {
			return res, nil
		}
	}
	return res, fmt.Errorf("%s:%s does not support the .%s pattern", svc, action, res.pattern)
}

// resourceType / resourceName are how history events name the resource.
func (r resource) resourceType() string {
	if r.sdk {
		return "aws-sdk:" + r.service
	}
	return r.service
}

func (r resource) resourceName() string {
	if r.legacyFn != "" {
		return r.legacyFn
	}
	if r.pattern != "" {
		return r.action + "." + r.pattern
	}
	return r.action
}

// ---- task tokens ----

type taskResult struct {
	output any
	err    *StateError
}

type taskWait struct {
	owner  string // state machine name
	result chan taskResult
	beat   chan struct{}
}

type tokenRegistry struct {
	mu    sync.Mutex
	waits map[string]*taskWait
}

func newTokenRegistry() *tokenRegistry { return &tokenRegistry{waits: map[string]*taskWait{}} }

// newToken registers a task token for a state machine's execution.
func (t *tokenRegistry) newToken(owner string) string {
	tok := "AQ" + core.NewSecret(160)
	t.mu.Lock()
	t.waits[tok] = &taskWait{owner: owner, result: make(chan taskResult, 1), beat: make(chan struct{}, 1)}
	t.mu.Unlock()
	return tok
}

func (t *tokenRegistry) drop(tok string) {
	t.mu.Lock()
	delete(t.waits, tok)
	t.mu.Unlock()
}

func (t *tokenRegistry) get(tok string) *taskWait {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.waits[tok]
}

// wait blocks until the token is completed, the heartbeat lapses or ctx ends.
func (t *tokenRegistry) wait(ctx context.Context, tok string, heartbeat time.Duration) (any, error) {
	w := t.get(tok)
	if w == nil {
		return nil, fail("States.Runtime", "task token is not registered")
	}
	var hb <-chan time.Time
	var timer *time.Timer
	if heartbeat > 0 {
		timer = time.NewTimer(heartbeat)
		defer timer.Stop()
		hb = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctxErr(ctx)
		case r := <-w.result:
			if r.err != nil {
				return nil, r.err
			}
			return r.output, nil
		case <-w.beat:
			if timer != nil {
				if !timer.Stop() {
					<-timer.C
				}
				timer.Reset(heartbeat)
			}
		case <-hb:
			return nil, fail("States.HeartbeatTimeout", "no heartbeat was received within %v", heartbeat)
		}
	}
}

var errNoTask = errors.New("task does not exist")

func (t *tokenRegistry) complete(tok string, r taskResult) error {
	w := t.get(tok)
	if w == nil {
		return errNoTask
	}
	select {
	case w.result <- r:
		return nil
	default:
		return errNoTask // already completed
	}
}

func (t *tokenRegistry) heartbeat(tok string) error {
	w := t.get(tok)
	if w == nil {
		return errNoTask
	}
	select {
	case w.beat <- struct{}{}:
	default:
	}
	return nil
}

// ---- calls ----

func (r *runner) can(action, resource string) error {
	if r.p == nil || r.p.Can(action, resource) {
		return nil
	}
	return fmt.Errorf("AccessDeniedException: %s is not authorized to perform: %s on resource: %s", r.p.ARN, action, resource)
}

func (r *runner) arn(service, res string) string {
	return fmt.Sprintf("arn:%s:%s:%s:%s:%s", core.Partition, service, r.region, r.account, res)
}

func textOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return string(unicode.ToUpper(rune(s[0]))) + s[1:]
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return string(unicode.ToLower(rune(s[0]))) + s[1:]
}

// rekey changes the first letter of every object key, recursively.
func rekey(v any, f func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[f(k)] = rekey(val, f)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = rekey(val, f)
		}
		return out
	}
	return v
}

// sdkServices maps aws-sdk service names to signing names; camelCase lists
// services whose API members are camelCase (SDK integrations use PascalCase).
var (
	sdkServices = map[string]string{"sfn": "states", "eventbridge": "events", "cloudwatchlogs": "logs", "cloudwatch": "monitoring"}
	camelCase   = map[string]bool{"logs": true, "states": true}
	errPrefix   = map[string]string{"dynamodb": "DynamoDB", "events": "EventBridge", "sqs": "SQS", "sns": "SNS", "lambda": "Lambda", "states": "StepFunctions"}
)

// callError turns a service error into the States error a Catch can match.
func callError(prefix string, err error) error {
	var se *StateError
	if errors.As(err, &se) {
		return se
	}
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		return &StateError{Name: prefix + "." + ae.Code, Cause: ae.Message}
	}
	var ce *core.Error
	if errors.As(err, &ce) {
		return &StateError{Name: prefix + "." + ce.Code, Cause: ce.Message}
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "AccessDeniedException: ") {
		return &StateError{Name: prefix + ".AccessDeniedException", Cause: strings.TrimPrefix(msg, "AccessDeniedException: ")}
	}
	return &StateError{Name: prefix + ".SdkClientException", Cause: msg}
}

func (r *runner) callResource(ctx context.Context, res resource, input any, token string, heartbeat time.Duration) (any, error) {
	out, err := r.invoke(ctx, res, input)
	if err != nil || res.pattern != "waitForTaskToken" {
		return out, err
	}
	return r.tokens.wait(ctx, token, heartbeat)
}

func (r *runner) invoke(ctx context.Context, res resource, input any) (any, error) {
	m, _ := input.(map[string]any)
	if res.sdk {
		svc := res.service
		if s, ok := sdkServices[svc]; ok {
			svc = s
		}
		prefix := errPrefix[svc]
		if prefix == "" {
			prefix = upperFirst(res.service)
		}
		if r.call == nil {
			return nil, fail(prefix+".SdkClientException", "service integrations are not available")
		}
		in := input
		if camelCase[svc] {
			in = rekey(input, lowerFirst)
		}
		raw, err := r.call(ctx, r.p, svc, upperFirst(res.action), in)
		if err != nil {
			return nil, callError(prefix, err)
		}
		var out any
		_ = json.Unmarshal(raw, &out)
		if camelCase[svc] {
			out = rekey(out, upperFirst)
		}
		return out, nil
	}
	switch res.service {
	case "lambda":
		fn, payload := res.legacyFn, input
		if fn == "" {
			fn = textOf(m["FunctionName"])
			if m["FunctionName"] == nil {
				return nil, fail("States.Runtime", "lambda:invoke needs FunctionName")
			}
			payload = m["Payload"]
		}
		name := fnName(fn)
		if err := r.can("lambda:InvokeFunction", r.arn("lambda", "function:"+name)); err != nil {
			return nil, callError("Lambda", err)
		}
		if r.tasks == nil {
			return nil, fail("Lambda.ServiceException", "Lambda is not available")
		}
		var p []byte
		if s, ok := payload.(string); ok && res.legacyFn == "" {
			p = []byte(s)
		} else {
			p, _ = json.Marshal(payload)
		}
		out, errType, errMsg, err := r.tasks.Invoke(ctx, name, p)
		if err != nil {
			return nil, callError("Lambda", err)
		}
		if errType != "" {
			return nil, &StateError{Name: errType, Cause: errMsg}
		}
		var v any
		if len(out) > 0 {
			_ = json.Unmarshal(out, &v)
		}
		if res.legacyFn != "" {
			return v, nil
		}
		return map[string]any{"ExecutedVersion": "$LATEST", "Payload": v, "StatusCode": 200.0,
			"SdkHttpMetadata": map[string]any{"HttpStatusCode": 200.0}, "SdkResponseMetadata": map[string]any{"RequestId": awsapi.RequestID()}}, nil
	case "sqs":
		q := textOf(firstOf(m, "QueueUrl", "QueueName"))
		if q == "" {
			return nil, fail("States.Runtime", "sqs:sendMessage needs QueueUrl")
		}
		name := path.Base(q)
		if i := strings.LastIndex(q, ":"); strings.HasPrefix(q, "arn:") && i >= 0 {
			name = q[i+1:]
		}
		if err := r.can("sqs:SendMessage", r.arn("sqs", name)); err != nil {
			return nil, callError("SQS", err)
		}
		if r.tasks == nil {
			return nil, fail("SQS.SdkClientException", "SQS is not available")
		}
		out, err := r.tasks.SendMessage(name, textOf(m["MessageBody"]))
		if err != nil {
			return nil, callError("SQS", err)
		}
		return out, nil
	case "sns":
		topic := textOf(m["TopicArn"])
		if m["TopicArn"] == nil {
			return nil, fail("States.Runtime", "sns:publish needs TopicArn")
		}
		if err := r.can("sns:Publish", topic); err != nil {
			return nil, callError("SNS", err)
		}
		if r.tasks == nil {
			return nil, fail("SNS.SdkClientException", "SNS is not available")
		}
		subject, _ := m["Subject"].(string)
		out, err := r.tasks.Publish(topic, subject, textOf(m["Message"]))
		if err != nil {
			return nil, callError("SNS", err)
		}
		return out, nil
	case "dynamodb":
		if r.call == nil {
			return nil, fail("DynamoDB.SdkClientException", "DynamoDB is not available")
		}
		raw, err := r.call(ctx, r.p, "dynamodb", upperFirst(res.action), input)
		if err != nil {
			return nil, callError("DynamoDB", err)
		}
		var out any
		_ = json.Unmarshal(raw, &out)
		return out, nil
	case "events":
		if r.call == nil {
			return nil, fail("EventBridge.SdkClientException", "EventBridge is not available")
		}
		entries, _ := m["Entries"].([]any)
		for i, e := range entries {
			em, _ := e.(map[string]any)
			if em == nil {
				continue
			}
			c := map[string]any{}
			for k, v := range em {
				c[k] = v
			}
			if d, ok := c["Detail"]; ok {
				c["Detail"] = textOf(d)
			}
			entries[i] = c
		}
		raw, err := r.call(ctx, r.p, "events", "PutEvents", map[string]any{"Entries": entries})
		if err != nil {
			return nil, callError("EventBridge", err)
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if n, _ := out["FailedEntryCount"].(float64); n > 0 {
			return nil, &StateError{Name: "EventBridge.FailedEntry", Cause: string(raw)}
		}
		return out, nil
	case "states":
		sm := textOf(m["StateMachineArn"])
		if m["StateMachineArn"] == nil {
			return nil, fail("States.Runtime", "states:startExecution needs StateMachineArn")
		}
		if err := r.can("states:StartExecution", sm); err != nil {
			return nil, callError("StepFunctions", err)
		}
		if r.children == nil {
			return nil, fail("StepFunctions.SdkClientException", "nested executions are not available")
		}
		var childIn any = map[string]any{}
		if v, ok := m["Input"]; ok {
			childIn = v
			if s, isStr := v.(string); isStr {
				if err := json.Unmarshal([]byte(s), &childIn); err != nil {
					return nil, fail("StepFunctions.InvalidExecutionInputException", "Input must be JSON")
				}
			}
		}
		name, _ := m["Name"].(string)
		x, err := r.children.startChild(ctx, sm, name, childIn)
		if err != nil {
			return nil, callError("StepFunctions", err)
		}
		if res.pattern != "sync" && res.pattern != "sync:2" {
			return map[string]any{"ExecutionArn": x.ARN, "StartDate": x.StartDate.Format(time.RFC3339Nano)}, nil
		}
		done := r.children.waitChild(ctx, x)
		if done == nil { // the parent stopped: stop the child too
			r.children.stopChild(x, "States.Aborted", "the parent execution stopped")
			return nil, ctxErr(ctx)
		}
		desc := map[string]any{"ExecutionArn": done.ARN, "StateMachineArn": sm, "Name": done.Name, "Status": done.Status,
			"StartDate": done.StartDate.Format(time.RFC3339Nano), "Input": textOf(done.Input)}
		if done.StopDate != nil {
			desc["StopDate"] = done.StopDate.Format(time.RFC3339Nano)
		}
		if done.Status != "SUCCEEDED" {
			desc["Error"], desc["Cause"] = done.Error, done.Cause
			b, _ := json.Marshal(desc)
			name := "States.TaskFailed"
			if done.Status == "ABORTED" {
				name = "StepFunctions.ExecutionAbortedException"
			} else if done.Status == "TIMED_OUT" {
				name = "StepFunctions.ExecutionTimedOutException"
			}
			return nil, &StateError{Name: name, Cause: string(b)}
		}
		if res.pattern == "sync:2" {
			desc["Output"] = done.Output
		} else {
			desc["Output"] = textOf(done.Output)
		}
		return desc, nil
	}
	return nil, fail("States.Runtime", "unsupported Resource %q", res.raw)
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return ""
}
