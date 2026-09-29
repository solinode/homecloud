package sfn

import (
	"encoding/json"
	"errors"
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

// Step Functions over the AWS JSON 1.0 protocol (X-Amz-Target AWSStepFunctions.*).

// RegisterAWS serves Step Functions ("states") over the AWS protocol.
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "states", JSONPrefix: "AWSStepFunctions", JSONVersion: "1.0",
		ErrorCode: map[string]string{"ValidationError": "ValidationException", "BadRequest": "ValidationException", "ResourceNotFound": "ResourceNotFound"},
		Ops: map[string]awsapi.Op{
			"CreateStateMachine":               s.awsCreateStateMachine,
			"DescribeStateMachine":             s.awsDescribeStateMachine,
			"DescribeStateMachineForExecution": s.awsDescribeStateMachineForExecution,
			"ListStateMachines":                s.awsListStateMachines,
			"UpdateStateMachine":               s.awsUpdateStateMachine,
			"DeleteStateMachine":               s.awsDeleteStateMachine,
			"StartExecution":                   s.awsStartExecution,
			"StartSyncExecution":               s.awsStartSyncExecution,
			"DescribeExecution":                s.awsDescribeExecution,
			"ListExecutions":                   s.awsListExecutions,
			"StopExecution":                    s.awsStopExecution,
			"GetExecutionHistory":              s.awsGetExecutionHistory,
			"SendTaskSuccess":                  s.awsSendTaskSuccess,
			"SendTaskFailure":                  s.awsSendTaskFailure,
			"SendTaskHeartbeat":                s.awsSendTaskHeartbeat,
			"TagResource":                      s.awsTagResource,
			"UntagResource":                    s.awsUntagResource,
			"ListTagsForResource":              s.awsListTagsForResource,
			"ValidateStateMachineDefinition":   s.awsValidate,
		},
	})
}

func sfnErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

func (s *Service) machineARN(name string) string { return s.env.ARN("states", "stateMachine:"+name) }

// machineRef resolves a state machine ARN in this account.
func (s *Service) machineRef(arn string) (string, error) {
	prefix := s.env.ARN("states", "stateMachine:")
	if !strings.HasPrefix(core.CanonicalARN(arn), prefix) {
		return "", sfnErr("InvalidArn", "Invalid Arn: '%s'", arn)
	}
	return machineName(core.CanonicalARN(arn)), nil
}

func (s *Service) machine(arn string) (StateMachine, error) {
	name, err := s.machineRef(arn)
	if err != nil {
		return StateMachine{}, err
	}
	m, err := store.Get[StateMachine](s.env.Store, cMachines, name)
	if err != nil {
		return m, sfnErr("StateMachineDoesNotExist", "State Machine Does Not Exist: '%s'", arn)
	}
	return m, nil
}

// execution finds an execution by ARN.
func (s *Service) execution(arn string) (*Execution, error) {
	if !strings.HasPrefix(arn, "arn:") || (!strings.Contains(arn, ":execution:") && !strings.Contains(arn, ":express:")) {
		return nil, sfnErr("InvalidArn", "Invalid Arn: '%s'", arn)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.execs {
		if x.ARN == arn {
			return x, nil
		}
	}
	return nil, sfnErr("ExecutionDoesNotExist", "Execution Does Not Exist: '%s'", arn)
}

type awsTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func tagsIn(ts []awsTag) (core.Tags, error) {
	if len(ts) > 50 {
		return nil, sfnErr("ValidationException", "a resource can have at most 50 tags")
	}
	out := core.Tags{}
	for _, t := range ts {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			return nil, sfnErr("ValidationException", "invalid tag %q", t.Key)
		}
		out[t.Key] = t.Value
	}
	return out, nil
}

func loggingOrDefault(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{"level":"OFF","includeExecutionData":false}`)
	}
	return raw
}

func tracingOrDefault(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{"enabled":false}`)
	}
	return raw
}

// definition parses the definition string AWS clients send.
func definitionString(def string) (json.RawMessage, error) {
	raw, err := checkDefinition(json.RawMessage(def))
	if err != nil {
		var ce *core.Error
		if errors.As(err, &ce) && ce.Code == "InvalidDefinition" {
			return nil, sfnErr("InvalidDefinition", "%s", ce.Message)
		}
		return nil, err
	}
	return raw, nil
}

// ---- state machines ----

func (s *Service) awsCreateStateMachine(q *awsapi.Req) (any, error) {
	var in struct {
		Name                 string          `json:"name"`
		Definition           string          `json:"definition"`
		RoleArn              string          `json:"roleArn"`
		Type                 string          `json:"type"`
		LoggingConfiguration json.RawMessage `json:"loggingConfiguration"`
		TracingConfiguration json.RawMessage `json:"tracingConfiguration"`
		Tags                 []awsTag        `json:"tags"`
		Publish              bool            `json:"publish"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:CreateStateMachine", s.machineARN(in.Name)); err != nil {
		return nil, err
	}
	if !nameRe.MatchString(in.Name) {
		return nil, sfnErr("InvalidName", "Invalid Name: '%s'", in.Name)
	}
	def, err := definitionString(in.Definition)
	if err != nil {
		return nil, err
	}
	tags, err := tagsIn(in.Tags)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeMachine(q.Check, def, in.RoleArn); err != nil {
		return nil, err
	}
	m, err := s.CreateMachine(MachineInput{Name: in.Name, Type: in.Type, RoleARN: in.RoleArn, Definition: def,
		Logging: in.LoggingConfiguration, Tracing: in.TracingConfiguration, Tags: tags})
	if err != nil {
		return nil, err
	}
	return map[string]any{"stateMachineArn": m.ARN, "creationDate": awsapi.Time{Time: m.CreatedAt}}, nil
}

func (s *Service) describeMachine(m StateMachine) map[string]any {
	var def any
	_ = json.Unmarshal(m.Definition, &def)
	b, _ := json.Marshal(def)
	out := map[string]any{"stateMachineArn": m.ARN, "name": m.Name, "status": m.Status, "definition": string(b), "roleArn": m.RoleARN,
		"type": orDefault(m.Type, "STANDARD"), "creationDate": awsapi.Time{Time: m.CreatedAt}, "loggingConfiguration": loggingOrDefault(m.Logging),
		"tracingConfiguration": tracingOrDefault(m.Tracing)}
	if m.Description != "" {
		out["description"] = m.Description
	}
	if m.RevisionID != "" {
		out["revisionId"] = m.RevisionID
	}
	return out
}

func (s *Service) awsDescribeStateMachine(q *awsapi.Req) (any, error) {
	var in struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:DescribeStateMachine", in.StateMachineArn); err != nil {
		return nil, err
	}
	m, err := s.machine(in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	return s.describeMachine(m), nil
}

func (s *Service) awsDescribeStateMachineForExecution(q *awsapi.Req) (any, error) {
	var in struct {
		ExecutionArn string `json:"executionArn"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:DescribeStateMachineForExecution", in.ExecutionArn); err != nil {
		return nil, err
	}
	x, err := s.execution(in.ExecutionArn)
	if err != nil {
		return nil, err
	}
	m, err := store.Get[StateMachine](s.env.Store, cMachines, x.StateMachine)
	if err != nil {
		return nil, sfnErr("StateMachineDoesNotExist", "State Machine Does Not Exist")
	}
	out := s.describeMachine(m)
	out["updateDate"] = awsapi.Time{Time: m.UpdatedAt}
	delete(out, "status")
	delete(out, "type")
	delete(out, "creationDate")
	return out, nil
}

func pageArgs(token string, limit, max int) (int, int, error) {
	start := 0
	if token != "" {
		var err error
		if start, err = strconv.Atoi(token); err != nil || start < 0 {
			return 0, 0, sfnErr("InvalidToken", "Invalid Token: '%s'", token)
		}
	}
	if limit <= 0 || limit > max {
		limit = max
	}
	return start, limit, nil
}

func (s *Service) awsListStateMachines(q *awsapi.Req) (any, error) {
	var in struct {
		MaxResults int    `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:ListStateMachines", "*"); err != nil {
		return nil, err
	}
	start, limit, err := pageArgs(in.NextToken, in.MaxResults, 1000)
	if err != nil {
		return nil, err
	}
	ms := store.List[StateMachine](s.env.Store, cMachines)
	sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
	out := map[string]any{}
	list := []map[string]any{}
	for i, m := range ms {
		if i < start {
			continue
		}
		if len(list) == limit {
			out["nextToken"] = strconv.Itoa(start + limit)
			break
		}
		list = append(list, map[string]any{"stateMachineArn": m.ARN, "name": m.Name, "type": orDefault(m.Type, "STANDARD"), "creationDate": awsapi.Time{Time: m.CreatedAt}})
	}
	out["stateMachines"] = list
	return out, nil
}

func (s *Service) awsUpdateStateMachine(q *awsapi.Req) (any, error) {
	var in struct {
		StateMachineArn      string          `json:"stateMachineArn"`
		Definition           *string         `json:"definition"`
		RoleArn              *string         `json:"roleArn"`
		LoggingConfiguration json.RawMessage `json:"loggingConfiguration"`
		TracingConfiguration json.RawMessage `json:"tracingConfiguration"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:UpdateStateMachine", in.StateMachineArn); err != nil {
		return nil, err
	}
	old, err := s.machine(in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	if in.Definition == nil && in.RoleArn == nil && in.LoggingConfiguration == nil && in.TracingConfiguration == nil {
		return nil, sfnErr("MissingRequiredParameter", "Either the definition, the role ARN, the LoggingConfiguration, or the TracingConfiguration must be specified")
	}
	def, role := old.Definition, old.RoleARN
	if in.Definition != nil {
		if def, err = definitionString(*in.Definition); err != nil {
			return nil, err
		}
	}
	if in.RoleArn != nil {
		role = *in.RoleArn
	}
	if in.Definition != nil || in.RoleArn != nil {
		if err := s.authorizeMachine(q.Check, def, role); err != nil {
			return nil, err
		}
	}
	m, err := s.UpdateMachine(old.Name, func(m *StateMachine) error {
		m.Definition, m.RoleARN = def, role
		if in.LoggingConfiguration != nil {
			m.Logging = in.LoggingConfiguration
		}
		if in.TracingConfiguration != nil {
			m.Tracing = in.TracingConfiguration
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"updateDate": awsapi.Time{Time: m.UpdatedAt}, "revisionId": m.RevisionID}, nil
}

func (s *Service) awsDeleteStateMachine(q *awsapi.Req) (any, error) {
	var in struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:DeleteStateMachine", in.StateMachineArn); err != nil {
		return nil, err
	}
	name, err := s.machineRef(in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cMachines, name) {
		return nil, nil // deleting a missing state machine succeeds, as in AWS
	}
	return nil, s.DeleteMachine(name)
}

// ---- executions ----

func executionInput(raw string) (any, error) {
	if raw == "" {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, sfnErr("InvalidExecutionInput", "Invalid execution input: %v", err)
	}
	return v, nil
}

func (s *Service) awsStartExecution(q *awsapi.Req) (any, error) {
	var in struct {
		StateMachineArn string `json:"stateMachineArn"`
		Name            string `json:"name"`
		Input           string `json:"input"`
		TraceHeader     string `json:"traceHeader"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:StartExecution", in.StateMachineArn); err != nil {
		return nil, err
	}
	m, err := s.machine(in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	if in.Name != "" && !nameRe.MatchString(in.Name) {
		return nil, sfnErr("InvalidName", "Invalid Name: '%s'", in.Name)
	}
	input, err := executionInput(in.Input)
	if err != nil {
		return nil, err
	}
	x, err := s.start(m.Name, in.Name, input, orDefault(in.Input, "{}"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"executionArn": x.ARN, "startDate": awsapi.Time{Time: x.StartDate}}, nil
}

func (s *Service) describeExecution(x *Execution, sync bool) map[string]any {
	out := map[string]any{"executionArn": x.ARN, "stateMachineArn": s.machineARN(x.StateMachine), "name": x.Name, "status": x.Status,
		"startDate": awsapi.Time{Time: x.StartDate}, "input": textOf(x.Input), "inputDetails": map[string]any{"included": true}}
	if x.StopDate != nil {
		out["stopDate"] = awsapi.Time{Time: *x.StopDate}
	}
	if x.Status == "SUCCEEDED" {
		out["output"], out["outputDetails"] = textOf(x.Output), map[string]any{"included": true}
	}
	if x.Error != "" {
		out["error"] = x.Error
	}
	if x.Cause != "" {
		out["cause"] = x.Cause
	}
	if !sync {
		out["redriveCount"], out["redriveStatus"] = 0, "NOT_REDRIVABLE"
	}
	return out
}

func (s *Service) awsStartSyncExecution(q *awsapi.Req) (any, error) {
	var in struct {
		StateMachineArn string `json:"stateMachineArn"`
		Name            string `json:"name"`
		Input           string `json:"input"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:StartSyncExecution", in.StateMachineArn); err != nil {
		return nil, err
	}
	m, err := s.machine(in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	if !m.express() {
		return nil, sfnErr("StateMachineTypeNotSupported", "This operation is not supported by this type of state machine")
	}
	input, err := executionInput(in.Input)
	if err != nil {
		return nil, err
	}
	x, err := s.start(m.Name, in.Name, input, "")
	if err != nil {
		return nil, err
	}
	done := s.wait(q.R.Context(), x)
	if done == nil {
		s.stopExecution(x.ID, "States.Aborted", "the caller disconnected")
		return nil, q.R.Context().Err()
	}
	out := s.describeExecution(done, true)
	ms := int64(0)
	if done.StopDate != nil {
		ms = done.StopDate.Sub(done.StartDate).Milliseconds()
	}
	out["billingDetails"] = map[string]any{"billedMemoryUsedInMB": 64, "billedDurationInMilliseconds": max(100, ms)}
	return out, nil
}

func (s *Service) awsDescribeExecution(q *awsapi.Req) (any, error) {
	var in struct {
		ExecutionArn string `json:"executionArn"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:DescribeExecution", in.ExecutionArn); err != nil {
		return nil, err
	}
	x, err := s.execution(in.ExecutionArn)
	if err != nil {
		return nil, err
	}
	return s.describeExecution(s.snapshot(x), false), nil
}

func (s *Service) awsListExecutions(q *awsapi.Req) (any, error) {
	var in struct {
		StateMachineArn string `json:"stateMachineArn"`
		StatusFilter    string `json:"statusFilter"`
		MaxResults      int    `json:"maxResults"`
		NextToken       string `json:"nextToken"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:ListExecutions", in.StateMachineArn); err != nil {
		return nil, err
	}
	m, err := s.machine(in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	switch in.StatusFilter {
	case "", "RUNNING", "SUCCEEDED", "FAILED", "TIMED_OUT", "ABORTED", "PENDING_REDRIVE":
	default:
		return nil, sfnErr("ValidationException", "statusFilter must be RUNNING, SUCCEEDED, FAILED, TIMED_OUT or ABORTED")
	}
	start, limit, err := pageArgs(in.NextToken, in.MaxResults, 1000)
	if err != nil {
		return nil, err
	}
	var xs []Execution
	s.mu.Lock()
	for _, x := range s.execs {
		if x.StateMachine == m.Name && (in.StatusFilter == "" || x.Status == in.StatusFilter) {
			xs = append(xs, *x)
		}
	}
	s.mu.Unlock()
	slices.SortFunc(xs, func(a, b Execution) int {
		if c := b.StartDate.Compare(a.StartDate); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	out := map[string]any{}
	list := []map[string]any{}
	for i, x := range xs {
		if i < start {
			continue
		}
		if len(list) == limit {
			out["nextToken"] = strconv.Itoa(start + limit)
			break
		}
		e := map[string]any{"executionArn": x.ARN, "stateMachineArn": m.ARN, "name": x.Name, "status": x.Status, "startDate": awsapi.Time{Time: x.StartDate}}
		if x.StopDate != nil {
			e["stopDate"] = awsapi.Time{Time: *x.StopDate}
		}
		list = append(list, e)
	}
	out["executions"] = list
	return out, nil
}

func (s *Service) awsStopExecution(q *awsapi.Req) (any, error) {
	var in struct {
		ExecutionArn string `json:"executionArn"`
		Error        string `json:"error"`
		Cause        string `json:"cause"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:StopExecution", in.ExecutionArn); err != nil {
		return nil, err
	}
	x, err := s.execution(in.ExecutionArn)
	if err != nil {
		return nil, err
	}
	if s.stopExecution(x.ID, in.Error, in.Cause) {
		select { // the execution stops at its next cancellation point
		case <-x.done:
		case <-time.After(5 * time.Second):
		}
	}
	cp := s.snapshot(x)
	stop := core.Now()
	if cp.StopDate != nil {
		stop = *cp.StopDate
	}
	return map[string]any{"stopDate": awsapi.Time{Time: stop}}, nil
}

func (s *Service) awsGetExecutionHistory(q *awsapi.Req) (any, error) {
	var in struct {
		ExecutionArn         string `json:"executionArn"`
		MaxResults           int    `json:"maxResults"`
		ReverseOrder         bool   `json:"reverseOrder"`
		NextToken            string `json:"nextToken"`
		IncludeExecutionData *bool  `json:"includeExecutionData"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:GetExecutionHistory", in.ExecutionArn); err != nil {
		return nil, err
	}
	x, err := s.execution(in.ExecutionArn)
	if err != nil {
		return nil, err
	}
	events := awsHistory(s.snapshot(x), in.IncludeExecutionData == nil || *in.IncludeExecutionData)
	if in.ReverseOrder {
		slices.Reverse(events)
	}
	start, limit, err := pageArgs(in.NextToken, in.MaxResults, 1000)
	if err != nil {
		return nil, err
	}
	if in.MaxResults == 0 {
		limit = 100
	}
	out := map[string]any{}
	end := min(len(events), start+limit)
	if start > len(events) {
		start = len(events)
	}
	if end < len(events) {
		out["nextToken"] = strconv.Itoa(end)
	}
	out["events"] = events[start:end]
	return out, nil
}

// ---- task tokens ----

func (s *Service) tokenOwner(q *awsapi.Req, action, token string) error {
	if len(token) < 1 || len(token) > 2048 {
		return sfnErr("InvalidToken", "Invalid Token: the task token is malformed")
	}
	w := s.tokens.get(token)
	if w == nil {
		if err := q.Authorize(action, "*"); err != nil {
			return err
		}
		return sfnErr("TaskDoesNotExist", "Task Does Not Exist")
	}
	return q.Authorize(action, s.machineARN(w.owner))
}

func (s *Service) awsSendTaskSuccess(q *awsapi.Req) (any, error) {
	var in struct {
		TaskToken string `json:"taskToken"`
		Output    string `json:"output"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.tokenOwner(q, "states:SendTaskSuccess", in.TaskToken); err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal([]byte(in.Output), &out); err != nil {
		return nil, sfnErr("InvalidOutput", "Invalid Output: %v", err)
	}
	if err := s.tokens.complete(in.TaskToken, taskResult{output: out}); err != nil {
		return nil, sfnErr("TaskDoesNotExist", "Task Does Not Exist")
	}
	return nil, nil
}

func (s *Service) awsSendTaskFailure(q *awsapi.Req) (any, error) {
	var in struct {
		TaskToken string `json:"taskToken"`
		Error     string `json:"error"`
		Cause     string `json:"cause"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.tokenOwner(q, "states:SendTaskFailure", in.TaskToken); err != nil {
		return nil, err
	}
	name := in.Error
	if name == "" {
		name = "States.TaskFailed"
	}
	if err := s.tokens.complete(in.TaskToken, taskResult{err: &StateError{Name: name, Cause: in.Cause}}); err != nil {
		return nil, sfnErr("TaskDoesNotExist", "Task Does Not Exist")
	}
	return nil, nil
}

func (s *Service) awsSendTaskHeartbeat(q *awsapi.Req) (any, error) {
	var in struct {
		TaskToken string `json:"taskToken"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.tokenOwner(q, "states:SendTaskHeartbeat", in.TaskToken); err != nil {
		return nil, err
	}
	if err := s.tokens.heartbeat(in.TaskToken); err != nil {
		return nil, sfnErr("TaskDoesNotExist", "Task Does Not Exist")
	}
	return nil, nil
}

// SendTaskSuccess completes a task token (for in-process callers).
func (s *Service) SendTaskSuccess(token string, output any) error {
	return s.tokens.complete(token, taskResult{output: output})
}

// ---- tags ----

func (s *Service) taggedMachine(arn string) (string, error) {
	name, err := s.machineRef(arn)
	if err != nil {
		return "", err
	}
	if !store.Has(s.env.Store, cMachines, name) {
		return "", sfnErr("ResourceNotFound", "Resource not found: '%s'", arn)
	}
	return name, nil
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string   `json:"resourceArn"`
		Tags        []awsTag `json:"tags"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:TagResource", in.ResourceArn); err != nil {
		return nil, err
	}
	name, err := s.taggedMachine(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	tags, err := tagsIn(in.Tags)
	if err != nil {
		return nil, err
	}
	_, err = store.Update(s.env.Store, cMachines, name, func(m *StateMachine) error {
		if m.Tags == nil {
			m.Tags = core.Tags{}
		}
		for k, v := range tags {
			m.Tags[k] = v
		}
		if len(m.Tags) > 50 {
			return sfnErr("TooManyTags", "a resource can have at most 50 tags")
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string   `json:"resourceArn"`
		TagKeys     []string `json:"tagKeys"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:UntagResource", in.ResourceArn); err != nil {
		return nil, err
	}
	name, err := s.taggedMachine(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	_, err = store.Update(s.env.Store, cMachines, name, func(m *StateMachine) error {
		for _, k := range in.TagKeys {
			delete(m.Tags, k)
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsListTagsForResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string `json:"resourceArn"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:ListTagsForResource", in.ResourceArn); err != nil {
		return nil, err
	}
	name, err := s.taggedMachine(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	m, _ := store.Get[StateMachine](s.env.Store, cMachines, name)
	out := []awsTag{}
	for k, v := range m.Tags {
		out = append(out, awsTag{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return map[string]any{"tags": out}, nil
}

// ---- validation ----

func (s *Service) awsValidate(q *awsapi.Req) (any, error) {
	var in struct {
		Definition string `json:"definition"`
		Type       string `json:"type"`
		MaxResults int    `json:"maxResults"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("states:ValidateStateMachineDefinition", "*"); err != nil {
		return nil, err
	}
	_, errs := parse(json.RawMessage(in.Definition))
	diags := []map[string]string{}
	for _, e := range errs {
		code := "SCHEMA_VALIDATION_FAILED"
		if strings.Contains(e, "not valid JSON") {
			code = "INVALID_JSON_DESCRIPTION"
		}
		diags = append(diags, map[string]string{"severity": "ERROR", "code": code, "message": e})
	}
	truncated := false
	if in.MaxResults > 0 && len(diags) > in.MaxResults {
		diags, truncated = diags[:in.MaxResults], true
	}
	result := "OK"
	if len(errs) > 0 {
		result = "FAIL"
	}
	return map[string]any{"result": result, "diagnostics": diags, "truncated": truncated}, nil
}
