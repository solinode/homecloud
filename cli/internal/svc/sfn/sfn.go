// Package sfn implements Step Functions: state machines written in Amazon
// States Language, executed with full input/output processing, retries,
// catchers, parallel branches and maps, and a recorded event history.
package sfn

import (
	"context"
	"encoding/json"
	"fmt"
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
	maxHistory  = 5000
	keepPerMach = 200
)

type StateMachine struct {
	Name       string          `json:"name"`
	ARN        string          `json:"arn"`
	Definition json.RawMessage `json:"definition"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Tags       core.Tags       `json:"tags,omitempty"`
}

type HistoryEvent struct {
	ID        int       `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	State     string    `json:"state,omitempty"`
	Details   any       `json:"details,omitempty"`
}

type Execution struct {
	deleted      bool
	ID           string         `json:"id"`
	ARN          string         `json:"arn"`
	Name         string         `json:"name"`
	StateMachine string         `json:"state_machine"`
	Status       string         `json:"status"` // RUNNING | SUCCEEDED | FAILED | TIMED_OUT | ABORTED
	Input        any            `json:"input"`
	Output       any            `json:"output,omitempty"`
	Error        string         `json:"error,omitempty"`
	Cause        string         `json:"cause,omitempty"`
	StartDate    time.Time      `json:"start_date"`
	StopDate     *time.Time     `json:"stop_date,omitempty"`
	History      []HistoryEvent `json:"history,omitempty"`
}

type Service struct {
	env    *svc.Env
	Tasks  Tasks
	mu     sync.Mutex
	execs  map[string]*Execution
	cancel map[string]context.CancelFunc
}

func New(env *svc.Env) *Service {
	s := &Service{env: env, execs: map[string]*Execution{}, cancel: map[string]context.CancelFunc{}}
	s.load()
	return s
}

func (s *Service) dir() string { return s.env.Cfg.Path("sfn") }

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
		return nil, errs
	}
	return &m, nil
}

// Start begins an execution and returns immediately.
func (s *Service) Start(machine, name string, input any) (*Execution, error) {
	sm, err := store.Get[StateMachine](s.env.Store, cMachines, machine)
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "StateMachineDoesNotExist", "state machine %q does not exist", machine)
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
		name = id[:12]
	}
	s.mu.Lock()
	for _, x := range s.execs {
		if x.StateMachine == machine && x.Name == name {
			s.mu.Unlock()
			return nil, core.Errf(http.StatusConflict, "ExecutionAlreadyExists", "execution %q already exists", name)
		}
	}
	x := &Execution{ID: id, ARN: s.env.ARN("states", "execution:"+machine+":"+name), Name: name, StateMachine: machine,
		Status: "RUNNING", Input: input, StartDate: core.Now()}
	s.execs[id] = x
	ctx, cancel := context.WithCancel(context.Background())
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
	r := &runner{tasks: s.Tasks, record: record, context: map[string]any{
		"Execution":    map[string]any{"Id": x.ARN, "Name": name, "StartTime": x.StartDate.Format(time.RFC3339), "Input": input},
		"StateMachine": map[string]any{"Id": sm.ARN, "Name": sm.Name},
	}}
	record("ExecutionStarted", "", map[string]any{"input": input})
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
		s.prune(machine)
	}()
	return x, nil
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
	res := httpx.Res("arn:hc:states:local-1:{account}:stateMachine:{name}")
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
			"updated_at": m.UpdatedAt, "executions": s.counts(m.Name)})
	}
	return out, nil
}

func definitionArg(raw json.RawMessage) (json.RawMessage, error) {
	// Accept the definition as a JSON object or as a JSON string holding it.
	var str string
	if json.Unmarshal(raw, &str) == nil {
		raw = json.RawMessage(str)
	}
	if len(raw) == 0 {
		return nil, core.BadRequest("definition is required")
	}
	if _, errs := parse(raw); len(errs) > 0 {
		return nil, &core.Error{Status: http.StatusBadRequest, Code: "InvalidDefinition", Message: fmt.Sprint(errs)}
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	b, _ := json.MarshalIndent(v, "", "  ")
	return b, nil
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in struct {
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition"`
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
	if err := s.authorizeTasks(c, def); err != nil {
		return nil, err
	}
	m := StateMachine{Name: in.Name, ARN: s.env.ARN("states", "stateMachine:"+in.Name), Definition: def, Status: "ACTIVE",
		CreatedAt: core.Now(), UpdatedAt: core.Now(), Tags: in.Tags}
	return m, store.Put(s.env.Store, cMachines, m.Name, m)
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	m, err := store.Get[StateMachine](s.env.Store, cMachines, c.Param("name"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "StateMachineDoesNotExist", "state machine %q does not exist", c.Param("name"))
	}
	return map[string]any{"state_machine": m, "executions": s.counts(m.Name)}, nil
}

func (s *Service) update(c *httpx.Ctx) (any, error) {
	var in struct {
		Definition json.RawMessage `json:"definition"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	def, err := definitionArg(in.Definition)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTasks(c, def); err != nil {
		return nil, err
	}
	m, err := store.Update(s.env.Store, cMachines, c.Param("name"), func(m *StateMachine) error {
		m.Definition, m.UpdatedAt = def, core.Now()
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.Errf(http.StatusNotFound, "StateMachineDoesNotExist", "state machine %q does not exist", c.Param("name"))
	}
	return m, err
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if !store.Has(s.env.Store, cMachines, name) {
		return nil, core.Errf(http.StatusNotFound, "StateMachineDoesNotExist", "state machine %q does not exist", name)
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
	return nil, store.Delete(s.env.Store, cMachines, name)
}

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
	var cp Execution
	if ok {
		cp = *x
		cp.History = append([]HistoryEvent(nil), x.History...)
	}
	s.mu.Unlock()
	if !ok {
		return nil, core.Errf(http.StatusNotFound, "ExecutionDoesNotExist", "execution %q does not exist", c.Param("id"))
	}
	if err := c.Authorize("states:DescribeExecution", s.env.ARN("states", "stateMachine:"+cp.StateMachine)); err != nil {
		return nil, err
	}
	return cp, nil
}

func (s *Service) stop(c *httpx.Ctx) (any, error) {
	s.mu.Lock()
	x, ok := s.execs[c.Param("id")]
	cancel := s.cancel[c.Param("id")]
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
	if cancel == nil {
		return nil, core.Errf(http.StatusConflict, "ExecutionNotRunning", "execution is %s", status)
	}
	cancel()
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

// taskPermissions lists the permissions a machine's Task states need: the
// creator must hold them, since executions act on their behalf. Targets chosen
// at run time (".$" parameters) need the permission on every resource.
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
				return fmt.Sprintf("arn:hc:%s:local-1:%s:%s", service, account, v)
			}
			switch r := st.Resource; {
			case strings.HasSuffix(r, ":lambda:invoke") || strings.HasSuffix(r, ":lambda:invoke.waitForTaskToken"):
				fn := static("FunctionName")
				if fn != "*" {
					fn = arnFor("lambda", "function:"+fnName(fn))
				}
				out = append(out, [2]string{"lambda:InvokeFunction", fn})
			case strings.Contains(r, ":lambda:") && strings.Contains(r, ":function:"):
				out = append(out, [2]string{"lambda:InvokeFunction", arnFor("lambda", "function:"+fnName(r))})
			case strings.HasSuffix(r, ":sqs:sendMessage"):
				q := static("QueueName", "QueueUrl")
				if q != "*" {
					q = arnFor("sqs", q[strings.LastIndexAny(q, "/:")+1:])
				}
				out = append(out, [2]string{"sqs:SendMessage", q})
			case strings.HasSuffix(r, ":sns:publish"):
				out = append(out, [2]string{"sns:Publish", static("TopicArn")})
			}
		}
	}
	walk(m)
	return out
}

func (s *Service) authorizeTasks(c *httpx.Ctx, def json.RawMessage) error {
	m, errs := parse(def)
	if m == nil {
		return core.BadRequest("%v", errs)
	}
	for _, p := range taskPermissions(m, s.env.AccountID) {
		if err := c.Authorize(p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

// Exists reports whether a state machine exists.
func (s *Service) Exists(name string) bool { return store.Has(s.env.Store, cMachines, name) }
