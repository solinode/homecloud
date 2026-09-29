package sfn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Machine is an Amazon States Language state machine (or a Parallel branch / Map iterator).
type Machine struct {
	Comment         string            `json:"Comment,omitempty"`
	StartAt         string            `json:"StartAt"`
	States          map[string]*State `json:"States"`
	TimeoutSeconds  int               `json:"TimeoutSeconds,omitempty"`
	Version         string            `json:"Version,omitempty"`
	QueryLanguage   string            `json:"QueryLanguage,omitempty"`
	ProcessorConfig *struct {
		Mode          string `json:"Mode,omitempty"`
		ExecutionType string `json:"ExecutionType,omitempty"`
	} `json:"ProcessorConfig,omitempty"`
}

type Retrier struct {
	ErrorEquals     []string `json:"ErrorEquals"`
	IntervalSeconds float64  `json:"IntervalSeconds"`
	MaxAttempts     *int     `json:"MaxAttempts"`
	BackoffRate     float64  `json:"BackoffRate"`
	MaxDelaySeconds float64  `json:"MaxDelaySeconds,omitempty"`
	JitterStrategy  string   `json:"JitterStrategy,omitempty"` // FULL | NONE
}

type Catcher struct {
	ErrorEquals []string        `json:"ErrorEquals"`
	Next        string          `json:"Next"`
	ResultPath  json.RawMessage `json:"ResultPath"`
}

type State struct {
	Type                       string           `json:"Type"`
	Comment                    string           `json:"Comment,omitempty"`
	Next                       string           `json:"Next,omitempty"`
	End                        bool             `json:"End,omitempty"`
	InputPath                  json.RawMessage  `json:"InputPath,omitempty"`
	OutputPath                 json.RawMessage  `json:"OutputPath,omitempty"`
	ResultPath                 json.RawMessage  `json:"ResultPath,omitempty"`
	Parameters                 any              `json:"Parameters,omitempty"`
	ResultSelector             any              `json:"ResultSelector,omitempty"`
	Result                     any              `json:"Result,omitempty"`
	Resource                   string           `json:"Resource,omitempty"`
	TimeoutSeconds             int              `json:"TimeoutSeconds,omitempty"`
	TimeoutSecondsPath         string           `json:"TimeoutSecondsPath,omitempty"`
	HeartbeatSeconds           int              `json:"HeartbeatSeconds,omitempty"`
	HeartbeatSecondsPath       string           `json:"HeartbeatSecondsPath,omitempty"`
	Retry                      []Retrier        `json:"Retry,omitempty"`
	Catch                      []Catcher        `json:"Catch,omitempty"`
	Choices                    []map[string]any `json:"Choices,omitempty"`
	Default                    string           `json:"Default,omitempty"`
	Seconds                    *float64         `json:"Seconds,omitempty"`
	SecondsPath                string           `json:"SecondsPath,omitempty"`
	Timestamp                  string           `json:"Timestamp,omitempty"`
	TimestampPath              string           `json:"TimestampPath,omitempty"`
	Error                      string           `json:"Error,omitempty"`
	ErrorPath                  string           `json:"ErrorPath,omitempty"`
	Cause                      string           `json:"Cause,omitempty"`
	CausePath                  string           `json:"CausePath,omitempty"`
	Branches                   []*Machine       `json:"Branches,omitempty"`
	ItemsPath                  string           `json:"ItemsPath,omitempty"`
	ItemSelector               any              `json:"ItemSelector,omitempty"`
	Iterator                   *Machine         `json:"Iterator,omitempty"`
	ItemProcessor              *Machine         `json:"ItemProcessor,omitempty"`
	MaxConcurrency             int              `json:"MaxConcurrency,omitempty"`
	MaxConcurrencyPath         string           `json:"MaxConcurrencyPath,omitempty"`
	ItemReader                 any              `json:"ItemReader,omitempty"`
	ItemBatcher                any              `json:"ItemBatcher,omitempty"`
	ResultWriter               any              `json:"ResultWriter,omitempty"`
	Label                      string           `json:"Label,omitempty"`
	ToleratedFailurePercentage *float64         `json:"ToleratedFailurePercentage,omitempty"`
	ToleratedFailureCount      *int             `json:"ToleratedFailureCount,omitempty"`
	QueryLanguage              string           `json:"QueryLanguage,omitempty"`
	Credentials                any              `json:"Credentials,omitempty"`
}

// Validate checks a machine's structure and returns every problem found.
func (m *Machine) Validate(prefix string) []string {
	var errs []string
	if m.QueryLanguage != "" && m.QueryLanguage != "JSONPath" {
		errs = append(errs, prefix+"QueryLanguage "+m.QueryLanguage+" is not supported (HomeCloud runs JSONPath state machines)")
	}
	if m.StartAt == "" {
		errs = append(errs, prefix+"StartAt is required")
	} else if m.States[m.StartAt] == nil {
		errs = append(errs, fmt.Sprintf("%sStartAt %q is not a state", prefix, m.StartAt))
	}
	if len(m.States) == 0 {
		errs = append(errs, prefix+"States must not be empty")
	}
	ref := func(from, to string) {
		if to != "" && m.States[to] == nil {
			errs = append(errs, fmt.Sprintf("%sstate %q points to missing state %q", prefix, from, to))
		}
	}
	for name, s := range m.States {
		if s == nil {
			errs = append(errs, fmt.Sprintf("%sstate %q is empty", prefix, name))
			continue
		}
		if len(name) > 80 {
			errs = append(errs, fmt.Sprintf("%sstate name %q is longer than 80 characters", prefix, name))
		}
		if s.QueryLanguage != "" && s.QueryLanguage != "JSONPath" {
			errs = append(errs, fmt.Sprintf("%sstate %q: QueryLanguage %s is not supported", prefix, name, s.QueryLanguage))
		}
		switch s.Type {
		case "Pass", "Task", "Wait", "Parallel", "Map":
			if s.Next == "" && !s.End {
				errs = append(errs, fmt.Sprintf("%sstate %q needs Next or End", prefix, name))
			}
			if s.Next != "" && s.End {
				errs = append(errs, fmt.Sprintf("%sstate %q has both Next and End", prefix, name))
			}
			ref(name, s.Next)
		case "Choice":
			if len(s.Choices) == 0 {
				errs = append(errs, fmt.Sprintf("%sChoice state %q needs Choices", prefix, name))
			}
			for _, c := range s.Choices {
				n, _ := c["Next"].(string)
				if n == "" {
					errs = append(errs, fmt.Sprintf("%sa choice rule in %q has no Next", prefix, name))
				}
				ref(name, n)
				if err := validateRule(c, true); err != nil {
					errs = append(errs, fmt.Sprintf("%sChoice state %q: %v", prefix, name, err))
				}
			}
			ref(name, s.Default)
		case "Succeed", "Fail":
			if s.Next != "" || s.End {
				errs = append(errs, fmt.Sprintf("%s%s state %q may not have Next or End", prefix, s.Type, name))
			}
		default:
			errs = append(errs, fmt.Sprintf("%sstate %q has unknown Type %q", prefix, name, s.Type))
		}
		if (len(s.Retry) > 0 || len(s.Catch) > 0) && s.Type != "Task" && s.Type != "Parallel" && s.Type != "Map" {
			errs = append(errs, fmt.Sprintf("%s%s state %q may not have Retry or Catch", prefix, s.Type, name))
		}
		if s.Type == "Task" {
			if s.Resource == "" {
				errs = append(errs, fmt.Sprintf("%sTask state %q needs a Resource", prefix, name))
			} else if _, err := parseResource(s.Resource); err != nil {
				errs = append(errs, fmt.Sprintf("%sTask state %q: %v", prefix, name, err))
			}
			if s.TimeoutSeconds > 0 && s.HeartbeatSeconds >= s.TimeoutSeconds {
				errs = append(errs, fmt.Sprintf("%sTask state %q: HeartbeatSeconds must be less than TimeoutSeconds", prefix, name))
			}
		}
		if s.Type == "Wait" {
			n := 0
			for _, set := range []bool{s.Seconds != nil, s.SecondsPath != "", s.Timestamp != "", s.TimestampPath != ""} {
				if set {
					n++
				}
			}
			if n != 1 {
				errs = append(errs, fmt.Sprintf("%sWait state %q needs exactly one of Seconds, SecondsPath, Timestamp or TimestampPath", prefix, name))
			}
		}
		for _, c := range s.Catch {
			ref(name, c.Next)
		}
		for i, b := range s.Branches {
			errs = append(errs, b.Validate(fmt.Sprintf("%s%s.Branches[%d]: ", prefix, name, i))...)
		}
		if s.Type == "Parallel" && len(s.Branches) == 0 {
			errs = append(errs, fmt.Sprintf("%sParallel state %q needs Branches", prefix, name))
		}
		if s.Type == "Map" {
			it := s.ItemProcessor
			if it == nil {
				it = s.Iterator
			}
			if it == nil {
				errs = append(errs, fmt.Sprintf("%sMap state %q needs an ItemProcessor", prefix, name))
			} else {
				errs = append(errs, it.Validate(prefix+name+".ItemProcessor: ")...)
			}
			if s.ItemReader != nil || s.ResultWriter != nil || s.ItemBatcher != nil {
				errs = append(errs, fmt.Sprintf("%sMap state %q: ItemReader, ItemBatcher and ResultWriter (distributed maps over S3) are not supported", prefix, name))
			}
		}
	}
	return errs
}

// ---- errors ----

type StateError struct {
	Name  string `json:"error"`
	Cause string `json:"cause"`
}

func (e *StateError) Error() string { return e.Name + ": " + e.Cause }

func fail(name, format string, a ...any) *StateError {
	return &StateError{Name: name, Cause: fmt.Sprintf(format, a...)}
}

// matchesError implements ErrorEquals: States.ALL matches everything but
// States.Runtime and States.DataLimitExceeded; States.TaskFailed matches any
// task error except timeouts; States.Timeout also matches heartbeat timeouts.
func matchesError(list []string, name string) bool {
	if name == "States.Aborted" {
		return false // a stopped execution never runs catchers or retries
	}
	for _, e := range list {
		switch {
		case e == name:
			return true
		case e == "States.ALL":
			if name != "States.Runtime" && name != "States.DataLimitExceeded" {
				return true
			}
		case e == "States.TaskFailed":
			if name != "States.Timeout" && name != "States.HeartbeatTimeout" && name != "States.Runtime" && name != "States.DataLimitExceeded" {
				return true
			}
		case e == "States.Timeout":
			if name == "States.HeartbeatTimeout" {
				return true
			}
		}
	}
	return false
}

// ---- execution ----

// Tasks performs Lambda, SQS and SNS work for Task states.
type Tasks interface {
	Invoke(ctx context.Context, function string, payload []byte) (result json.RawMessage, errType, errMsg string, err error)
	SendMessage(queue, body string) (map[string]any, error)
	Publish(topic, subject, message string) (map[string]any, error)
}

// Caller invokes an awsJson operation of another service in-process as a
// principal (DynamoDB, EventBridge and aws-sdk integrations).
type Caller func(ctx context.Context, p *httpx.Principal, service, op string, input any) (json.RawMessage, error)

// children runs nested executions (states:startExecution).
type children interface {
	startChild(ctx context.Context, machineARN, name string, input any) (*Execution, error)
	waitChild(ctx context.Context, x *Execution) *Execution
	stopChild(x *Execution, errName, cause string)
}

type runner struct {
	tasks   Tasks
	record  func(eventType, state string, details any)
	context map[string]any // the $$ context object
	// p is whom the execution acts as: the state machine's role, or (without
	// one) a principal trusted because the creator's permissions were checked.
	p        *httpx.Principal
	call     Caller
	tokens   *tokenRegistry
	children children
	region   string
	account  string
	machine  string // state machine name (owns task tokens)
}

// child returns a runner with its own copy of the context object, for concurrent branches.
func (r *runner) child() *runner {
	c := *r
	c.context = map[string]any{}
	for k, v := range r.context {
		c.context[k] = v
	}
	return &c
}

func (r *runner) path(raw json.RawMessage, def string) (string, bool) {
	if len(raw) == 0 {
		return def, true
	}
	if string(raw) == "null" {
		return "", false
	}
	var p string
	_ = json.Unmarshal(raw, &p)
	return p, true
}

// run executes a machine from StartAt and returns its output.
func (r *runner) run(ctx context.Context, m *Machine, input any) (any, error) {
	if m.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(m.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	name := m.StartAt
	data := input
	for steps := 0; ; steps++ {
		if steps > 25000 {
			return nil, fail("States.Runtime", "execution exceeded 25000 state transitions")
		}
		if err := ctx.Err(); err != nil {
			return nil, ctxErr(ctx)
		}
		s := m.States[name]
		if s == nil {
			return nil, fail("States.Runtime", "state %q does not exist", name)
		}
		r.context["State"] = map[string]any{"Name": name, "EnteredTime": time.Now().UTC().Format(time.RFC3339Nano), "RetryCount": 0.0}
		delete(r.context, "Task")
		r.record(s.Type+"StateEntered", name, map[string]any{"input": data})
		out, next, err := r.step(ctx, name, s, data)
		if err != nil {
			var se *StateError
			if !errors.As(err, &se) {
				se = fail("States.Runtime", "%v", err)
			}
			// Catchers route errors to another state.
			handled := false
			for _, c := range s.Catch {
				if matchesError(c.ErrorEquals, se.Name) {
					rp, keep := r.path(c.ResultPath, "$")
					errOut := map[string]any{"Error": se.Name, "Cause": se.Cause}
					if keep { // with ResultPath null the state's input passes through unchanged
						if data, err = set(data, rp, errOut); err != nil {
							return nil, fail("States.Runtime", "%v", err)
						}
					}
					r.record(s.Type+"StateExited", name, map[string]any{"caught": se, "next": c.Next, "output": data})
					name, handled = c.Next, true
					break
				}
			}
			if handled {
				continue
			}
			r.record(s.Type+"StateFailed", name, se)
			return nil, se
		}
		r.record(s.Type+"StateExited", name, map[string]any{"output": out})
		data = out
		if next == "" {
			return data, nil
		}
		name = next
	}
}

func (r *runner) ctxObj() any { return r.context }

// step runs one state and returns its output and the next state name ("" = end).
func (r *runner) step(ctx context.Context, name string, s *State, raw any) (any, string, error) {
	// InputPath / Parameters
	in := raw
	if p, keep := r.path(s.InputPath, "$"); !keep {
		in = map[string]any{}
	} else if p != "$" {
		v, err := get(raw, p)
		if err != nil {
			return nil, "", fail("States.Runtime", "InputPath: %v", err)
		}
		in = v
	}
	var token string
	if s.Type == "Task" && strings.HasSuffix(s.Resource, ".waitForTaskToken") {
		if r.tokens == nil {
			return nil, "", fail("States.Runtime", "task tokens are not available here")
		}
		token = r.tokens.newToken(r.machine)
		r.context["Task"] = map[string]any{"Token": token}
		defer r.tokens.drop(token)
	}
	effective := in
	if s.Parameters != nil && s.Type != "Map" {
		v, err := resolveParams(s.Parameters, in, r.ctxObj())
		if err != nil {
			return nil, "", paramErr("Parameters", err)
		}
		effective = v
	}
	var result any
	next := s.Next
	switch s.Type {
	case "Pass":
		result = effective
		if s.Result != nil {
			result = s.Result
		}
	case "Succeed":
		out, _, err := applyOutput(r, s, effective)
		return out, "", err
	case "Fail":
		se := &StateError{Name: orDefault(s.Error, "States.Fail"), Cause: s.Cause}
		if s.ErrorPath != "" {
			v, err := evalRef(s.ErrorPath, raw, r.ctxObj())
			if err != nil {
				return nil, "", err
			}
			se.Name = fmt.Sprint(v)
		}
		if s.CausePath != "" {
			v, err := evalRef(s.CausePath, raw, r.ctxObj())
			if err != nil {
				return nil, "", err
			}
			se.Cause = fmt.Sprint(v)
		}
		return nil, "", se
	case "Choice":
		for _, c := range s.Choices {
			ok, err := evalRule(c, in)
			if err != nil {
				return nil, "", fail("States.Runtime", "Choice %s: %v", name, err)
			}
			if ok {
				n, _ := c["Next"].(string)
				out, _, err := applyOutput(r, s, in)
				return out, n, err
			}
		}
		if s.Default == "" {
			return nil, "", fail("States.NoChoiceMatched", "no choice rule matched and there is no Default")
		}
		out, _, err := applyOutput(r, s, in)
		return out, s.Default, err
	case "Wait":
		d, err := waitDuration(s, in)
		if err != nil {
			return nil, "", err
		}
		r.record("WaitStateWaiting", name, map[string]any{"seconds": d.Seconds()})
		select {
		case <-ctx.Done():
			return nil, "", ctxErr(ctx)
		case <-time.After(d):
		}
		out, _, err := applyOutput(r, s, in)
		if s.End {
			return out, "", err
		}
		return out, next, err
	case "Task":
		v, err := r.task(ctx, name, s, effective, in, token)
		if err != nil {
			return nil, "", err
		}
		result = v
	case "Parallel":
		r.record("ParallelStateStarted", name, nil)
		outs := make([]any, len(s.Branches))
		errs := make([]error, len(s.Branches))
		bctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for i, b := range s.Branches {
			wg.Add(1)
			go func(i int, b *Machine) {
				defer wg.Done()
				outs[i], errs[i] = r.child().run(bctx, b, effective)
				if errs[i] != nil {
					cancel() // one failed branch stops its siblings
				}
			}(i, b)
		}
		wg.Wait()
		cancel()
		if err := firstError(errs); err != nil {
			return nil, "", err // run records ParallelStateFailed
		}
		r.record("ParallelStateSucceeded", name, nil)
		result = outs
	case "Map":
		v, err := r.mapState(ctx, name, s, in)
		if err != nil {
			return nil, "", err // run records MapStateFailed
		}
		r.record("MapStateSucceeded", name, nil)
		result = v
	}
	if s.ResultSelector != nil {
		v, err := resolveParams(s.ResultSelector, result, r.ctxObj())
		if err != nil {
			return nil, "", paramErr("ResultSelector", err)
		}
		result = v
	}
	out := raw
	if p, keep := r.path(s.ResultPath, "$"); keep {
		v, err := set(raw, p, result)
		if err != nil {
			return nil, "", fail("States.Runtime", "ResultPath: %v", err)
		}
		out = v
	}
	out, _, err := applyOutput(r, s, out)
	if s.End {
		return out, "", err
	}
	return out, next, err
}

func paramErr(where string, err error) error {
	var se *StateError
	if errors.As(err, &se) && se.Name != "States.Runtime" {
		return se
	}
	return fail("States.Runtime", "%s: %v", where, err)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func applyOutput(r *runner, s *State, v any) (any, string, error) {
	p, keep := r.path(s.OutputPath, "$")
	if !keep {
		return map[string]any{}, "", nil
	}
	if p == "$" {
		return v, "", nil
	}
	out, err := get(v, p)
	if err != nil {
		return nil, "", fail("States.Runtime", "OutputPath: %v", err)
	}
	return out, "", nil
}

func waitDuration(s *State, in any) (time.Duration, error) {
	switch {
	case s.Seconds != nil:
		return time.Duration(*s.Seconds * float64(time.Second)), nil
	case s.SecondsPath != "":
		v, err := get(in, s.SecondsPath)
		if err != nil {
			return 0, fail("States.Runtime", "SecondsPath: %v", err)
		}
		f, ok := v.(float64)
		if !ok || f < 0 {
			return 0, fail("States.Runtime", "SecondsPath must point at a non-negative number")
		}
		return time.Duration(f * float64(time.Second)), nil
	case s.Timestamp != "" || s.TimestampPath != "":
		ts := s.Timestamp
		if s.TimestampPath != "" {
			v, err := get(in, s.TimestampPath)
			if err != nil {
				return 0, fail("States.Runtime", "TimestampPath: %v", err)
			}
			ts = fmt.Sprint(v)
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return 0, fail("States.Runtime", "invalid timestamp %q", ts)
		}
		return max(0, time.Until(t)), nil
	}
	return 0, nil
}

// secondsFrom resolves TimeoutSeconds / HeartbeatSeconds (or their Path forms).
func secondsFrom(fixed int, p string, in any) (time.Duration, error) {
	if p == "" {
		return time.Duration(fixed) * time.Second, nil
	}
	v, err := get(in, p)
	if err != nil {
		return 0, fail("States.Runtime", "%s: %v", p, err)
	}
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return 0, fail("States.Runtime", "%s must point at a positive number", p)
	}
	return time.Duration(f) * time.Second, nil
}

func (r *runner) task(ctx context.Context, name string, s *State, input, stateInput any, token string) (any, error) {
	maxAttempts := func(rt Retrier) int {
		if rt.MaxAttempts == nil {
			return 3
		}
		return *rt.MaxAttempts
	}
	timeout, err := secondsFrom(s.TimeoutSeconds, s.TimeoutSecondsPath, stateInput)
	if err != nil {
		return nil, err
	}
	heartbeat, err := secondsFrom(s.HeartbeatSeconds, s.HeartbeatSecondsPath, stateInput)
	if err != nil {
		return nil, err
	}
	res, _ := parseResource(s.Resource)
	attempts := map[int]int{}
	for {
		tctx := ctx
		cancel := context.CancelFunc(func() {})
		if timeout > 0 {
			tctx, cancel = context.WithTimeout(ctx, timeout)
		}
		params, _ := json.Marshal(input)
		r.record("TaskScheduled", name, map[string]any{"resource": s.Resource, "resourceType": res.resourceType(), "resourceName": res.resourceName(),
			"parameters": string(params), "timeout": int(timeout.Seconds()), "heartbeat": int(heartbeat.Seconds())})
		r.record("TaskStarted", name, map[string]any{"resourceType": res.resourceType(), "resourceName": res.resourceName()})
		out, err := r.callResource(tctx, res, input, token, heartbeat)
		timedOut := errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancel()
		if timedOut {
			err = fail("States.Timeout", "task timed out after %v", timeout)
		} else if err != nil && ctx.Err() != nil {
			return nil, ctxErr(ctx) // the execution itself timed out or was stopped
		}
		if err == nil {
			r.record("TaskSucceeded", name, map[string]any{"output": out, "resourceType": res.resourceType(), "resourceName": res.resourceName()})
			return out, nil
		}
		var se *StateError
		if !errors.As(err, &se) {
			se = fail("States.TaskFailed", "%v", err)
		}
		typ := "TaskFailed"
		if se.Name == "States.Timeout" || se.Name == "States.HeartbeatTimeout" {
			typ = "TaskTimedOut"
		}
		r.record(typ, name, map[string]any{"error": se.Name, "cause": se.Cause, "resourceType": res.resourceType(), "resourceName": res.resourceName()})
		retried := false
		for i, rt := range s.Retry {
			if !matchesError(rt.ErrorEquals, se.Name) {
				continue
			}
			if attempts[i] >= maxAttempts(rt) {
				break
			}
			interval := rt.IntervalSeconds
			if interval == 0 {
				interval = 1
			}
			rate := rt.BackoffRate
			if rate == 0 {
				rate = 2
			}
			secs := interval * math.Pow(rate, float64(attempts[i]))
			if rt.MaxDelaySeconds > 0 {
				secs = math.Min(secs, rt.MaxDelaySeconds)
			}
			if rt.JitterStrategy == "FULL" {
				secs = rand.Float64() * secs
			}
			wait := time.Duration(secs * float64(time.Second))
			attempts[i]++
			if st, ok := r.context["State"].(map[string]any); ok {
				st["RetryCount"] = float64(attempts[i])
			}
			r.record("TaskRetrying", name, map[string]any{"attempt": attempts[i], "wait_seconds": wait.Seconds(), "error": se.Name})
			select {
			case <-ctx.Done():
				return nil, ctxErr(ctx)
			case <-time.After(wait):
			}
			retried = true
			break
		}
		if !retried {
			return nil, se
		}
	}
}

// ctxErr maps a finished context to the States error it represents.
func ctxErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail("States.Timeout", "the execution timed out")
	}
	return fail("States.Aborted", "the execution was stopped")
}

// firstError prefers a real failure over the aborts it caused in siblings.
func firstError(errs []error) error {
	var aborted error
	for _, e := range errs {
		if e == nil {
			continue
		}
		if se, ok := e.(*StateError); ok && se.Name == "States.Aborted" {
			aborted = e
			continue
		}
		return e
	}
	return aborted
}

func fnName(ref string) string {
	if i := strings.Index(ref, ":function:"); i >= 0 {
		ref = ref[i+len(":function:"):]
	}
	if i := strings.Index(ref, ":"); i >= 0 { // strip qualifiers like :$LATEST
		ref = ref[:i]
	}
	return ref
}

func (r *runner) mapState(ctx context.Context, name string, s *State, in any) (any, error) {
	itemsPath := s.ItemsPath
	if itemsPath == "" {
		itemsPath = "$"
	}
	v, err := get(in, itemsPath)
	if err != nil {
		return nil, fail("States.Runtime", "ItemsPath: %v", err)
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fail("States.Runtime", "ItemsPath must point at an array")
	}
	proc := s.ItemProcessor
	if proc == nil {
		proc = s.Iterator
	}
	selector := s.ItemSelector
	if selector == nil {
		selector = s.Parameters
	}
	limit := s.MaxConcurrency
	if s.MaxConcurrencyPath != "" {
		mv, err := get(in, s.MaxConcurrencyPath)
		f, ok := mv.(float64)
		if err != nil || !ok || f < 0 {
			return nil, fail("States.Runtime", "MaxConcurrencyPath must point at a non-negative number")
		}
		limit = int(f)
	}
	if limit <= 0 || limit > 40 {
		limit = 40
	}
	if len(items) > 10000 {
		return nil, fail("States.Runtime", "Map input has %d items; the limit is 10000", len(items))
	}
	r.record("MapStateStarted", name, map[string]any{"length": len(items)})
	inputs := make([]any, len(items))
	for i, item := range items {
		inputs[i] = item
		if selector != nil {
			ctxObj := map[string]any{"Map": map[string]any{"Item": map[string]any{"Index": float64(i), "Value": item}}}
			for k, v := range r.context {
				ctxObj[k] = v
			}
			sv, err := resolveParams(selector, in, ctxObj)
			if err != nil {
				return nil, paramErr("ItemSelector", err)
			}
			inputs[i] = sv
		}
	}
	out := make([]any, len(items))
	errs := make([]error, len(items))
	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range inputs {
		if mctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			iter := map[string]any{"name": name, "index": i}
			r.record("MapIterationStarted", name, iter)
			c := r.child()
			c.context["Map"] = map[string]any{"Item": map[string]any{"Index": float64(i), "Value": items[i]}}
			out[i], errs[i] = c.run(mctx, proc, inputs[i])
			if errs[i] != nil {
				r.record("MapIterationFailed", name, iter)
				cancel()
			} else {
				r.record("MapIterationSucceeded", name, iter)
			}
		}(i)
	}
	wg.Wait()
	if err := firstError(errs); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Choice rules ----

var comparisonOps = map[string]bool{}

func init() {
	for _, t := range []string{"String", "Numeric", "Timestamp"} {
		for _, op := range []string{"Equals", "LessThan", "GreaterThan", "LessThanEquals", "GreaterThanEquals"} {
			comparisonOps[t+op] = true
			comparisonOps[t+op+"Path"] = true
		}
	}
	for _, op := range []string{"BooleanEquals", "BooleanEqualsPath", "StringMatches", "IsPresent", "IsNull", "IsNumeric", "IsString", "IsBoolean", "IsTimestamp"} {
		comparisonOps[op] = true
	}
}

// validateRule checks a choice rule's shape (top-level rules carry Next).
func validateRule(rule map[string]any, top bool) error {
	for _, k := range []string{"And", "Or"} {
		if list, ok := rule[k]; ok {
			arr, isArr := list.([]any)
			if !isArr || len(arr) == 0 {
				return fmt.Errorf("%s must be a non-empty array of rules", k)
			}
			for _, x := range arr {
				m, ok := x.(map[string]any)
				if !ok {
					return fmt.Errorf("%s must contain rules", k)
				}
				if err := validateRule(m, false); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if not, ok := rule["Not"]; ok {
		m, isMap := not.(map[string]any)
		if !isMap {
			return fmt.Errorf("Not must be a rule")
		}
		return validateRule(m, false)
	}
	if _, ok := rule["Variable"].(string); !ok {
		return fmt.Errorf("a choice rule needs Variable (or And/Or/Not)")
	}
	n := 0
	for k := range rule {
		switch k {
		case "Variable", "Next", "Comment":
		default:
			if !comparisonOps[k] {
				return fmt.Errorf("unknown comparison %s", k)
			}
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("a choice rule needs exactly one comparison")
	}
	return nil
}

func evalRule(rule map[string]any, in any) (bool, error) {
	if and, ok := rule["And"].([]any); ok {
		for _, x := range and {
			m, _ := x.(map[string]any)
			ok, err := evalRule(m, in)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}
	if or, ok := rule["Or"].([]any); ok {
		for _, x := range or {
			m, _ := x.(map[string]any)
			ok, err := evalRule(m, in)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	if not, ok := rule["Not"].(map[string]any); ok {
		ok, err := evalRule(not, in)
		return !ok, err
	}
	varPath, _ := rule["Variable"].(string)
	val, verr := get(in, varPath)
	for op, want := range rule {
		if op == "Variable" || op == "Next" || op == "Comment" {
			continue
		}
		if b, ok := want.(bool); ok && strings.HasPrefix(op, "Is") {
			var res bool
			switch op {
			case "IsPresent":
				res = verr == nil
			case "IsNull":
				res = verr == nil && val == nil
			case "IsNumeric":
				_, isNum := val.(float64)
				res = verr == nil && isNum
			case "IsString":
				_, isStr := val.(string)
				res = verr == nil && isStr
			case "IsBoolean":
				_, isBool := val.(bool)
				res = verr == nil && isBool
			case "IsTimestamp":
				s, isStr := val.(string)
				_, perr := time.Parse(time.RFC3339, s)
				res = verr == nil && isStr && perr == nil
			}
			return res == b, nil
		}
		if verr != nil {
			return false, fmt.Errorf("Invalid path '%s': the choice state's condition path references an invalid value", varPath)
		}
		if p, ok := strings.CutSuffix(op, "Path"); ok {
			ref, _ := want.(string)
			v, err := get(in, ref)
			if err != nil {
				return false, err
			}
			op, want = p, v
		}
		return compare(op, val, want)
	}
	return false, fmt.Errorf("choice rule has no comparison")
}

// stringMatches implements StringMatches: * matches any run of characters; \* is a literal *.
func stringMatches(s, pattern string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case pattern[i] == '\\' && i+1 < len(pattern):
			b.WriteString(regexp.QuoteMeta(string(pattern[i+1])))
			i++
		case pattern[i] == '*':
			b.WriteString(".*")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile("(?s)" + b.String())
	return err == nil && re.MatchString(s)
}

func compare(op string, val, want any) (bool, error) {
	switch {
	case strings.HasPrefix(op, "String"):
		a, ok1 := val.(string)
		b, ok2 := want.(string)
		if !ok1 || !ok2 {
			return false, nil
		}
		switch op {
		case "StringEquals":
			return a == b, nil
		case "StringLessThan":
			return a < b, nil
		case "StringGreaterThan":
			return a > b, nil
		case "StringLessThanEquals":
			return a <= b, nil
		case "StringGreaterThanEquals":
			return a >= b, nil
		case "StringMatches":
			return stringMatches(a, b), nil
		}
	case strings.HasPrefix(op, "Numeric"):
		a, ok1 := val.(float64)
		b, ok2 := want.(float64)
		if !ok1 || !ok2 {
			return false, nil
		}
		switch op {
		case "NumericEquals":
			return a == b, nil
		case "NumericLessThan":
			return a < b, nil
		case "NumericGreaterThan":
			return a > b, nil
		case "NumericLessThanEquals":
			return a <= b, nil
		case "NumericGreaterThanEquals":
			return a >= b, nil
		}
	case op == "BooleanEquals":
		a, ok1 := val.(bool)
		b, ok2 := want.(bool)
		return ok1 && ok2 && a == b, nil
	case strings.HasPrefix(op, "Timestamp"):
		as, _ := val.(string)
		bs, _ := want.(string)
		a, err1 := time.Parse(time.RFC3339, as)
		b, err2 := time.Parse(time.RFC3339, bs)
		if err1 != nil || err2 != nil {
			return false, nil
		}
		switch op {
		case "TimestampEquals":
			return a.Equal(b), nil
		case "TimestampLessThan":
			return a.Before(b), nil
		case "TimestampGreaterThan":
			return a.After(b), nil
		case "TimestampLessThanEquals":
			return !a.After(b), nil
		case "TimestampGreaterThanEquals":
			return !a.Before(b), nil
		}
	}
	return false, fmt.Errorf("unsupported comparison %s", op)
}
