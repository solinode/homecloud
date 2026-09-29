// Package sfn implements Step Functions: state machines written in Amazon
// States Language, executed with full input/output processing, retries,
// catchers, parallel branches, maps, intrinsic functions, task tokens and
// service integrations, with a recorded event history.
//
// Executions act as the state machine's IAM role when it has one (the role
// must trust states.amazonaws.com; its policies decide what tasks may do).
// Without a role, the creator's permissions for every task were checked when
// the machine was created or updated, and executions run with them.
package sfn

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	cMachines   = "sfn_state_machines"
	maxHistory  = 25000
	keepPerMach = 200
	// expressTimeout is the longest an EXPRESS execution may run.
	expressTimeout = 5 * time.Minute
)

type StateMachine struct {
	Name        string          `json:"name"`
	ARN         string          `json:"arn"`
	Definition  json.RawMessage `json:"definition"`
	Status      string          `json:"status"`
	Type        string          `json:"type,omitempty"` // STANDARD (default) | EXPRESS
	RoleARN     string          `json:"role_arn,omitempty"`
	Description string          `json:"description,omitempty"`
	Logging     json.RawMessage `json:"logging_configuration,omitempty"`
	Tracing     json.RawMessage `json:"tracing_configuration,omitempty"`
	RevisionID  string          `json:"revision_id,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Tags        core.Tags       `json:"tags,omitempty"`
}

func (m StateMachine) express() bool { return m.Type == "EXPRESS" }

type HistoryEvent struct {
	ID        int       `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	State     string    `json:"state,omitempty"`
	Details   any       `json:"details,omitempty"`
}

type Execution struct {
	deleted              bool
	done                 chan struct{}
	stopError, stopCause string
	ID                   string         `json:"id"`
	ARN                  string         `json:"arn"`
	Name                 string         `json:"name"`
	StateMachine         string         `json:"state_machine"`
	Status               string         `json:"status"` // RUNNING | SUCCEEDED | FAILED | TIMED_OUT | ABORTED
	Input                any            `json:"input"`
	Output               any            `json:"output,omitempty"`
	Error                string         `json:"error,omitempty"`
	Cause                string         `json:"cause,omitempty"`
	StartDate            time.Time      `json:"start_date"`
	StopDate             *time.Time     `json:"stop_date,omitempty"`
	RoleARN              string         `json:"role_arn,omitempty"`
	History              []HistoryEvent `json:"history,omitempty"`
}

type Service struct {
	env   *svc.Env
	Tasks Tasks
	// Call invokes awsJson operations of other services in-process
	// (DynamoDB, EventBridge and aws-sdk integrations).
	Call Caller
	// Role returns the principal executions of a machine with this role act
	// as; it fails when the role does not exist or does not trust states.amazonaws.com.
	Role   func(roleARN string) (*httpx.Principal, error)
	tokens *tokenRegistry
	mu     sync.Mutex
	execs  map[string]*Execution
	cancel map[string]context.CancelFunc
}

func New(env *svc.Env) *Service {
	s := &Service{env: env, execs: map[string]*Execution{}, cancel: map[string]context.CancelFunc{}, tokens: newTokenRegistry()}
	s.load()
	return s
}

func (s *Service) dir() string { return s.env.Cfg.Path("sfn") }

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func (s *Service) load() {
	entries, _ := os.ReadDir(s.dir())
	for _, e := range entries {
		b, err := os.ReadFile(s.dir() + "/" + e.Name())
		if err != nil {
			continue
		}
		var x Execution
		if json.Unmarshal(b, &x) != nil {
			continue
		}
		if x.Status == "RUNNING" {
			n := core.Now()
			x.Status, x.StopDate, x.Error, x.Cause = "ABORTED", &n, "States.Aborted", "HomeCloud restarted while the execution was running"
			s.saveLocked(&x)
		}
		x.done = closedChan()
		s.execs[x.ID] = &x
	}
}

// saveLocked persists one execution; callers hold s.mu (or own x exclusively).
func (s *Service) saveLocked(x *Execution) {
	if x.deleted {
		return
	}
	_ = os.MkdirAll(s.dir(), 0o700)
	b, _ := json.Marshal(x)
	tmp := s.dir() + "/" + x.ID + ".json.tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.dir()+"/"+x.ID+".json")
	}
}

func parse(def json.RawMessage) (*Machine, []string) {
	var m Machine
	if err := json.Unmarshal(def, &m); err != nil {
		return nil, []string{"definition is not valid JSON: " + err.Error()}
	}
	if errs := m.Validate(""); len(errs) > 0 {
		slices.Sort(errs)
		return nil, errs
	}
	return &m, nil
}

// trusted is the principal of executions without a role: the creator's
// permissions for each task were checked when the definition was saved.
var trusted = &httpx.Principal{ARN: "states.amazonaws.com", UserName: "states.amazonaws.com", Can: func(string, string) bool { return true }}

func notFoundMachine(name string) error {
	return core.Errf(http.StatusNotFound, "StateMachineDoesNotExist", "State Machine Does Not Exist: '%s'", name)
}

// Start begins an execution and returns immediately.
func (s *Service) Start(machine, name string, input any) (*Execution, error) {
	return s.start(machine, name, input, "")
}

// machineName accepts a state machine name or ARN.
func machineName(ref string) string {
	if i := strings.Index(ref, ":stateMachine:"); strings.HasPrefix(ref, "arn:") && i >= 0 {
		ref = ref[i+len(":stateMachine:"):]
		if j := strings.IndexByte(ref, ':'); j >= 0 { // version or alias qualifier
			ref = ref[:j]
		}
	}
	return ref
}

// start begins an execution. rawInput, when set, is the caller's input text
// (for StartExecution's idempotency check).
func (s *Service) start(machine, name string, input any, rawInput string) (*Execution, error) {
	sm, err := store.Get[StateMachine](s.env.Store, cMachines, machine)
	if err != nil {
		return nil, notFoundMachine(machine)
	}
	m, errs := parse(sm.Definition)
	if m == nil {
		return nil, core.BadRequest("stored definition is invalid: %v", errs)
	}
	if input == nil {
		input = map[string]any{}
	}
	id := core.RandHex(32)
	if name == "" {
		name = uuid()
	}
	arn := s.env.ARN("states", "execution:"+machine+":"+name)
	if sm.express() {
		arn = s.env.ARN("states", "express:"+machine+":"+name+":"+uuid())
	}
	s.mu.Lock()
	for _, x := range s.execs {
		if x.StateMachine == machine && x.Name == name && !sm.express() {
			same := x.Status == "RUNNING" && rawInput != "" && jsonEqualText(x.Input, input)
			s.mu.Unlock()
			if same {
				return x, nil // StartExecution is idempotent for a running execution with the same input
			}
			return nil, core.Errf(http.StatusConflict, "ExecutionAlreadyExists", "Execution Already Exists: '%s'", x.ARN)
		}
	}
	x := &Execution{ID: id, ARN: arn, Name: name, StateMachine: machine, Status: "RUNNING", Input: input, StartDate: core.Now(),
		RoleARN: sm.RoleARN, done: make(chan struct{})}
	s.execs[id] = x
	ctx, cancel := context.WithCancel(context.Background())
	if sm.express() {
		ctx, cancel = context.WithTimeout(context.Background(), expressTimeout)
	}
	s.cancel[id] = cancel
	s.mu.Unlock()

	seq := 0
	var lastSave time.Time
	record := func(typ, state string, details any) {
		s.mu.Lock()
		defer s.mu.Unlock()
		seq++
		if len(x.History) < maxHistory {
			x.History = append(x.History, HistoryEvent{ID: seq, Timestamp: time.Now().UTC(), Type: typ, State: state, Details: clone(details)})
		}
		if time.Since(lastSave) > 500*time.Millisecond {
			s.saveLocked(x)
			lastSave = time.Now()
		}
	}
	principal := trusted
	var roleErr error
	if sm.RoleARN != "" && s.Role != nil {
		principal, roleErr = s.Role(sm.RoleARN)
	}
	r := &runner{tasks: s.Tasks, record: record, p: principal, call: s.Call, tokens: s.tokens, children: s,
		region: s.env.Cfg.Region, account: s.env.AccountID, machine: machine,
		context: map[string]any{
			"Execution":    map[string]any{"Id": x.ARN, "Name": name, "StartTime": x.StartDate.Format(time.RFC3339), "Input": input, "RoleArn": sm.RoleARN, "RedriveCount": 0.0},
			"StateMachine": map[string]any{"Id": sm.ARN, "Name": sm.Name},
		}}
	record("ExecutionStarted", "", map[string]any{"input": input, "roleArn": sm.RoleARN})
	go func() {
		defer cancel()
		var out any
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fail("States.Runtime", "internal error: %v", p)
				}
			}()
			if roleErr != nil {
				err = fail("States.Runtime", "could not assume role %s: %v", sm.RoleARN, roleErr)
				return
			}
			out, err = r.run(ctx, m, clone(input))
		}()
		s.mu.Lock()
		defer s.mu.Unlock()
		n := core.Now()
		x.StopDate = &n
		delete(s.cancel, id)
		if err != nil {
			se, ok := err.(*StateError)
			if !ok {
				se = fail("States.Runtime", "%v", err)
			}
			x.Error, x.Cause = se.Name, se.Cause
			switch se.Name {
			case "States.Timeout":
				x.Status = "TIMED_OUT"
			case "States.Aborted":
				x.Status = "ABORTED"
				if x.stopError != "" || x.stopCause != "" {
					x.Error, x.Cause = x.stopError, x.stopCause
				}
			default:
				x.Status = "FAILED"
			}
		} else {
			x.Status, x.Output = "SUCCEEDED", out
		}
		seq++
		x.History = append(x.History, HistoryEvent{ID: seq, Timestamp: time.Now().UTC(), Type: "Execution" + map[string]string{
			"SUCCEEDED": "Succeeded", "FAILED": "Failed", "TIMED_OUT": "TimedOut", "ABORTED": "Aborted"}[x.Status],
			Details: map[string]any{"output": x.Output, "error": x.Error, "cause": x.Cause}})
		s.saveLocked(x)
		close(x.done)
		s.prune(machine)
	}()
	return x, nil
}

func jsonEqualText(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// snapshot copies an execution under s.mu.
func (s *Service) snapshot(x *Execution) *Execution {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *x
	cp.History = append([]HistoryEvent(nil), x.History...)
	return &cp
}

// wait blocks until an execution finishes (nil if ctx ends first).
func (s *Service) wait(ctx context.Context, x *Execution) *Execution {
	select {
	case <-x.done:
		return s.snapshot(x)
	case <-ctx.Done():
		return nil
	}
}

// stopExecution aborts a running execution with an optional error and cause.
func (s *Service) stopExecution(id, errName, cause string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.execs[id]
	cancel := s.cancel[id]
	if !ok || cancel == nil {
		return false
	}
	x.stopError, x.stopCause = errName, cause
	cancel()
	return true
}

// children, for states:startExecution tasks.
func (s *Service) startChild(ctx context.Context, machineARN, name string, input any) (*Execution, error) {
	return s.start(machineName(machineARN), name, input, "")
}
func (s *Service) waitChild(ctx context.Context, x *Execution) *Execution { return s.wait(ctx, x) }
func (s *Service) stopChild(x *Execution, errName, cause string) {
	s.stopExecution(x.ID, errName, cause)
}

// prune keeps the newest executions per state machine; s.mu must be held.
func (s *Service) prune(machine string) {
	var done []*Execution
	for _, x := range s.execs {
		if x.StateMachine == machine && x.Status != "RUNNING" {
			done = append(done, x)
		}
	}
	if len(done) <= keepPerMach {
		return
	}
	slices.SortFunc(done, func(a, b *Execution) int { return b.StartDate.Compare(a.StartDate) })
	for _, x := range done[keepPerMach:] {
		delete(s.execs, x.ID)
		_ = os.Remove(s.dir() + "/" + x.ID + ".json")
	}
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:states:{region}:{account}:stateMachine:{name}")
	r.Handle("GET /api/v1/sfn/state-machines", "states:ListStateMachines", s.list)
	r.Handle("POST /api/v1/sfn/state-machines", "states:CreateStateMachine", s.create)
	r.Handle("GET /api/v1/sfn/state-machines/{name}", "states:DescribeStateMachine", s.get, res)
	r.Handle("PUT /api/v1/sfn/state-machines/{name}", "states:UpdateStateMachine", s.update, res)
	r.Handle("DELETE /api/v1/sfn/state-machines/{name}", "states:DeleteStateMachine", s.delete, res)
	r.Handle("POST /api/v1/sfn/state-machines/{name}/executions", "states:StartExecution", s.startRoute, res)
	r.Handle("GET /api/v1/sfn/state-machines/{name}/executions", "states:ListExecutions", s.listExecutions, res)
	r.Handle("GET /api/v1/sfn/executions/{id}", "states:DescribeExecution", s.getExecution, httpx.Deferred())
	r.Handle("POST /api/v1/sfn/executions/{id}/stop", "states:StopExecution", s.stop, httpx.Deferred())
	r.Handle("POST /api/v1/sfn/validate", "states:ValidateStateMachineDefinition", s.validate)
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func (s *Service) counts(machine string) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := map[string]int{}
	for _, x := range s.execs {
		if x.StateMachine == machine {
			c[x.Status]++
		}
	}
	return c
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, m := range store.List[StateMachine](s.env.Store, cMachines) {
		out = append(out, map[string]any{"name": m.Name, "arn": m.ARN, "status": m.Status, "created_at": m.CreatedAt,
			"updated_at": m.UpdatedAt, "executions": s.counts(m.Name), "type": orDefault(m.Type, "STANDARD")})
	}
	return out, nil
}

// definitionArg accepts the definition as a JSON object or a JSON string holding it.
func definitionArg(raw json.RawMessage) (json.RawMessage, error) {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		raw = json.RawMessage(str)
	}
	return checkDefinition(raw)
}

func checkDefinition(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, core.BadRequest("definition is required")
	}
	if _, errs := parse(raw); len(errs) > 0 {
		return nil, &core.Error{Status: http.StatusBadRequest, Code: "InvalidDefinition", Message: "Invalid State Machine Definition: " + strings.Join(errs, "; ")}
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	b, _ := json.MarshalIndent(v, "", "  ")
	return b, nil
}

// MachineInput is what CreateStateMachine / UpdateStateMachine set.
type MachineInput struct {
	Name, Type, RoleARN, Description string
	Definition                       json.RawMessage // validated and normalized
	Logging, Tracing                 json.RawMessage
	Tags                             core.Tags
}

// checkRole validates a role for executions: it must exist and trust Step Functions.
func (s *Service) checkRole(roleARN string) error {
	if roleARN == "" || s.Role == nil {
		return nil
	}
	if _, err := s.Role(roleARN); err != nil {
		return core.Errf(http.StatusBadRequest, "InvalidArn", "Neither the global service principal states.amazonaws.com, nor the regional one is authorized to assume the provided role (%s): %v", roleARN, err)
	}
	return nil
}

// authorizeMachine checks the caller may create a machine with this
// definition and role: iam:PassRole for the role, or (without one) every
// task's own permission, since executions then act with the creator's rights.
func (s *Service) authorizeMachine(authorize func(action, resource string) error, def json.RawMessage, roleARN string) error {
	if roleARN != "" {
		if err := authorize("iam:PassRole", roleARN); err != nil {
			return err
		}
		return s.checkRole(roleARN)
	}
	m, errs := parse(def)
	if m == nil {
		return core.BadRequest("%v", errs)
	}
	for _, p := range taskPermissions(m, s.env.AccountID) {
		if err := authorize(p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

// CreateMachine stores a new state machine. A repeated create with the same
// definition, role and type returns the existing machine, as in AWS.
func (s *Service) CreateMachine(in MachineInput) (StateMachine, error) {
	if !nameRe.MatchString(in.Name) {
		return StateMachine{}, core.Errf(http.StatusBadRequest, "InvalidName", "Invalid Name: '%s' (1-80 letters, digits, - or _)", in.Name)
	}
	switch in.Type {
	case "":
		in.Type = "STANDARD"
	case "STANDARD", "EXPRESS":
	default:
		return StateMachine{}, core.BadRequest("type must be STANDARD or EXPRESS")
	}
	if old, err := store.Get[StateMachine](s.env.Store, cMachines, in.Name); err == nil {
		if jsonEqualText(json.RawMessage(old.Definition), json.RawMessage(in.Definition)) && old.RoleARN == in.RoleARN && orDefault(old.Type, "STANDARD") == in.Type {
			return old, nil
		}
		return StateMachine{}, core.Errf(http.StatusConflict, "StateMachineAlreadyExists", "State Machine Already Exists: '%s'", old.ARN)
	}
	m := StateMachine{Name: in.Name, ARN: s.env.ARN("states", "stateMachine:"+in.Name), Definition: in.Definition, Status: "ACTIVE",
		Type: in.Type, RoleARN: in.RoleARN, Description: in.Description, Logging: in.Logging, Tracing: in.Tracing, RevisionID: uuid(),
		CreatedAt: core.Now(), UpdatedAt: core.Now(), Tags: in.Tags}
	return m, store.Put(s.env.Store, cMachines, m.Name, m)
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in struct {
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition"`
		Type       string          `json:"type"`
		RoleARN    string          `json:"role_arn"`
		Tags       core.Tags       `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !nameRe.MatchString(in.Name) {
		return nil, core.BadRequest("names are 1-80 letters, digits, hyphens or underscores")
	}
	if store.Has(s.env.Store, cMachines, in.Name) {
		return nil, core.Errf(http.StatusConflict, "StateMachineAlreadyExists", "state machine %q already exists", in.Name)
	}
	def, err := definitionArg(in.Definition)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeMachine(c.Authorize, def, in.RoleARN); err != nil {
		return nil, err
	}
	return s.CreateMachine(MachineInput{Name: in.Name, Type: in.Type, RoleARN: in.RoleARN, Definition: def, Tags: in.Tags})
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	m, err := store.Get[StateMachine](s.env.Store, cMachines, c.Param("name"))
	if err != nil {
		return nil, notFoundMachine(c.Param("name"))
	}
	return map[string]any{"state_machine": m, "executions": s.counts(m.Name)}, nil
}

// UpdateMachine changes a machine's definition and/or role.
func (s *Service) UpdateMachine(name string, fn func(m *StateMachine) error) (StateMachine, error) {
	m, err := store.Update(s.env.Store, cMachines, name, func(m *StateMachine) error {
		if err := fn(m); err != nil {
			return err
		}
		m.UpdatedAt, m.RevisionID = core.Now(), uuid()
		return nil
	})
	if err == store.ErrNotFound {
		return m, notFoundMachine(name)
	}
	return m, err
}

func (s *Service) update(c *httpx.Ctx) (any, error) {
	var in struct {
		Definition json.RawMessage `json:"definition"`
		RoleARN    *string         `json:"role_arn"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	def, err := definitionArg(in.Definition)
	if err != nil {
		return nil, err
	}
	old, err := store.Get[StateMachine](s.env.Store, cMachines, c.Param("name"))
	if err != nil {
		return nil, notFoundMachine(c.Param("name"))
	}
	role := old.RoleARN
	if in.RoleARN != nil {
		role = *in.RoleARN
	}
	if err := s.authorizeMachine(c.Authorize, def, role); err != nil {
		return nil, err
	}
	return s.UpdateMachine(c.Param("name"), func(m *StateMachine) error {
		m.Definition, m.RoleARN = def, role
		return nil
	})
}

// DeleteMachine stops a machine's executions and removes it.
func (s *Service) DeleteMachine(name string) error {
	if !store.Has(s.env.Store, cMachines, name) {
		return notFoundMachine(name)
	}
	s.mu.Lock()
	for id, x := range s.execs {
		if x.StateMachine == name {
			if cancel := s.cancel[id]; cancel != nil {
				cancel()
			}
			x.deleted = true
			delete(s.execs, id)
			_ = os.Remove(s.dir() + "/" + id + ".json")
		}
	}
	s.mu.Unlock()
	return store.Delete(s.env.Store, cMachines, name)
}

func (s *Service) delete(c *httpx.Ctx) (any, error) { return nil, s.DeleteMachine(c.Param("name")) }

func (s *Service) startRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Name  string `json:"name"`
		Input any    `json:"input"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if str, ok := in.Input.(string); ok {
		if err := json.Unmarshal([]byte(str), &in.Input); err != nil {
			return nil, core.BadRequest("input must be JSON")
		}
	}
	if in.Name != "" && !nameRe.MatchString(in.Name) {
		return nil, core.BadRequest("execution names are 1-80 letters, digits, hyphens or underscores")
	}
	x, err := s.Start(c.Param("name"), in.Name, in.Input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": x.ID, "arn": x.ARN, "name": x.Name, "start_date": x.StartDate}, nil
}

func (s *Service) listExecutions(c *httpx.Ctx) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for _, x := range s.execs {
		if x.StateMachine != c.Param("name") {
			continue
		}
		if st := c.Query("status"); st != "" && x.Status != st {
			continue
		}
		out = append(out, map[string]any{"id": x.ID, "arn": x.ARN, "name": x.Name, "status": x.Status, "start_date": x.StartDate, "stop_date": x.StopDate})
	}
	slices.SortFunc(out, func(a, b map[string]any) int { return b["start_date"].(time.Time).Compare(a["start_date"].(time.Time)) })
	return out, nil
}

func (s *Service) getExecution(c *httpx.Ctx) (any, error) {
	s.mu.Lock()
	x, ok := s.execs[c.Param("id")]
	s.mu.Unlock()
	if !ok {
		return nil, core.Errf(http.StatusNotFound, "ExecutionDoesNotExist", "execution %q does not exist", c.Param("id"))
	}
	cp := s.snapshot(x)
	if err := c.Authorize("states:DescribeExecution", s.env.ARN("states", "stateMachine:"+cp.StateMachine)); err != nil {
		return nil, err
	}
	return cp, nil
}

func (s *Service) stop(c *httpx.Ctx) (any, error) {
	s.mu.Lock()
	x, ok := s.execs[c.Param("id")]
	var machine, status string
	if ok {
		machine, status = x.StateMachine, x.Status
	}
	s.mu.Unlock()
	if !ok {
		return nil, core.Errf(http.StatusNotFound, "ExecutionDoesNotExist", "execution %q does not exist", c.Param("id"))
	}
	if err := c.Authorize("states:StopExecution", s.env.ARN("states", "stateMachine:"+machine)); err != nil {
		return nil, err
	}
	if !s.stopExecution(c.Param("id"), "", "") {
		return nil, core.Errf(http.StatusConflict, "ExecutionNotRunning", "execution is %s", status)
	}
	return map[string]string{"status": "stopping"}, nil
}

func (s *Service) validate(c *httpx.Ctx) (any, error) {
	var in struct {
		Definition json.RawMessage `json:"definition"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	var str string
	if json.Unmarshal(in.Definition, &str) == nil {
		in.Definition = json.RawMessage(str)
	}
	_, errs := parse(in.Definition)
	if errs == nil {
		errs = []string{}
	}
	return map[string]any{"valid": len(errs) == 0, "errors": errs}, nil
}

// taskPermissions lists the permissions a machine's Task states need: without
// a role the creator must hold them, since executions act on their behalf.
// Targets chosen at run time (".$" parameters) need the permission on every resource.
func taskPermissions(m *Machine, account string) [][2]string {
	var out [][2]string
	var walk func(m *Machine)
	walk = func(m *Machine) {
		for _, st := range m.States {
			for _, b := range st.Branches {
				walk(b)
			}
			if st.ItemProcessor != nil {
				walk(st.ItemProcessor)
			}
			if st.Iterator != nil {
				walk(st.Iterator)
			}
			if st.Type != "Task" {
				continue
			}
			res, err := parseResource(st.Resource)
			if err != nil {
				continue
			}
			params, _ := st.Parameters.(map[string]any)
			static := func(keys ...string) string {
				for _, k := range keys {
					if v, ok := params[k].(string); ok {
						return v
					}
				}
				return "*"
			}
			arnFor := func(service, v string) string {
				if v == "*" || strings.HasPrefix(v, "arn:") {
					return v
				}
				return core.ARN(account, service, v)
			}
			switch {
			case res.sdk:
				svc := res.service
				switch svc {
				case "sfn":
					svc = "states"
				case "eventbridge":
					svc = "events"
				case "cloudwatchlogs":
					svc = "logs"
				}
				out = append(out, [2]string{svc + ":" + upperFirst(res.action), "*"})
			case res.legacyFn != "":
				out = append(out, [2]string{"lambda:InvokeFunction", arnFor("lambda", "function:"+fnName(res.legacyFn))})
			case res.service == "lambda":
				fn := static("FunctionName")
				if fn != "*" {
					fn = arnFor("lambda", "function:"+fnName(fn))
				}
				out = append(out, [2]string{"lambda:InvokeFunction", fn})
			case res.service == "sqs":
				q := static("QueueName", "QueueUrl")
				if q != "*" {
					q = arnFor("sqs", q[strings.LastIndexAny(q, "/:")+1:])
				}
				out = append(out, [2]string{"sqs:SendMessage", q})
			case res.service == "sns":
				out = append(out, [2]string{"sns:Publish", static("TopicArn")})
			case res.service == "dynamodb":
				t := static("TableName")
				if t != "*" {
					t = arnFor("dynamodb", "table/"+t)
				}
				out = append(out, [2]string{"dynamodb:" + upperFirst(res.action), t})
			case res.service == "states":
				out = append(out, [2]string{"states:StartExecution", static("StateMachineArn")})
			case res.service == "events":
				bus := "*"
				if entries, ok := params["Entries"].([]any); ok && len(entries) == 1 {
					if e, ok := entries[0].(map[string]any); ok {
						if _, dyn := e["EventBusName.$"]; !dyn {
							b, _ := e["EventBusName"].(string)
							if b == "" {
								b = "default"
							}
							bus = b
							if !strings.HasPrefix(b, "arn:") {
								bus = core.ARN(account, "events", "event-bus/"+b)
							}
						}
					}
				}
				out = append(out, [2]string{"events:PutEvents", bus})
			}
		}
	}
	walk(m)
	return out
}

// Exists reports whether a state machine exists.
func (s *Service) Exists(name string) bool { return store.Has(s.env.Store, cMachines, name) }
