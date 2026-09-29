package sfn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"strings"
	"sync"
	"time"
)

// Machine is an Amazon States Language state machine (or a Parallel branch / Map iterator).
type Machine struct {
	Comment        string            `json:"Comment,omitempty"`
	StartAt        string            `json:"StartAt"`
	States         map[string]*State `json:"States"`
	TimeoutSeconds int               `json:"TimeoutSeconds,omitempty"`
}

type Retrier struct {
	ErrorEquals     []string `json:"ErrorEquals"`
	IntervalSeconds float64  `json:"IntervalSeconds"`
	MaxAttempts     *int     `json:"MaxAttempts"`
	BackoffRate     float64  `json:"BackoffRate"`
}

type Catcher struct {
	ErrorEquals []string        `json:"ErrorEquals"`
	Next        string          `json:"Next"`
	ResultPath  json.RawMessage `json:"ResultPath"`
}

type State struct {
	Type           string           `json:"Type"`
	Comment        string           `json:"Comment,omitempty"`
	Next           string           `json:"Next,omitempty"`
	End            bool             `json:"End,omitempty"`
	InputPath      json.RawMessage  `json:"InputPath,omitempty"`
	OutputPath     json.RawMessage  `json:"OutputPath,omitempty"`
	ResultPath     json.RawMessage  `json:"ResultPath,omitempty"`
	Parameters     any              `json:"Parameters,omitempty"`
	ResultSelector any              `json:"ResultSelector,omitempty"`
	Result         any              `json:"Result,omitempty"`
	Resource       string           `json:"Resource,omitempty"`
	TimeoutSeconds int              `json:"TimeoutSeconds,omitempty"`
	Retry          []Retrier        `json:"Retry,omitempty"`
	Catch          []Catcher        `json:"Catch,omitempty"`
	Choices        []map[string]any `json:"Choices,omitempty"`
	Default        string           `json:"Default,omitempty"`
	Seconds        float64          `json:"Seconds,omitempty"`
	SecondsPath    string           `json:"SecondsPath,omitempty"`
	Timestamp      string           `json:"Timestamp,omitempty"`
	TimestampPath  string           `json:"TimestampPath,omitempty"`
	Error          string           `json:"Error,omitempty"`
	Cause          string           `json:"Cause,omitempty"`
	Branches       []*Machine       `json:"Branches,omitempty"`
	ItemsPath      string           `json:"ItemsPath,omitempty"`
	ItemSelector   any              `json:"ItemSelector,omitempty"`
	Iterator       *Machine         `json:"Iterator,omitempty"`
	ItemProcessor  *Machine         `json:"ItemProcessor,omitempty"`
	MaxConcurrency int              `json:"MaxConcurrency,omitempty"`
}

// Validate checks a machine's structure and returns every problem found.
func (m *Machine) Validate(prefix string) []string {
	var errs []string
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
		if len(name) > 80 {
			errs = append(errs, fmt.Sprintf("%sstate name %q is longer than 80 characters", prefix, name))
		}
		switch s.Type {
		case "Pass", "Task", "Wait", "Parallel", "Map":
			if s.Next == "" && !s.End {
				errs = append(errs, fmt.Sprintf("%sstate %q needs Next or End", prefix, name))
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
			}
			ref(name, s.Default)
		case "Succeed", "Fail":
		default:
			errs = append(errs, fmt.Sprintf("%sstate %q has unknown Type %q", prefix, name, s.Type))
		}
		if s.Type == "Task" && s.Resource == "" {
			errs = append(errs, fmt.Sprintf("%sTask state %q needs a Resource", prefix, name))
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

func matchesError(list []string, name string) bool {
	if name == "States.Aborted" {
		return false // a stopped execution never runs catchers or retries
	}
	for _, e := range list {
		if e == name || (e == "States.ALL" && name != "States.Runtime") || (e == "States.TaskFailed" && !strings.HasPrefix(name, "States.")) {
			return true
		}
	}
	return false
}

// ---- execution ----

// Tasks performs Task-state work against other services.
type Tasks interface {
	Invoke(ctx context.Context, function string, payload []byte) (result json.RawMessage, errType, errMsg string, err error)
	SendMessage(queue, body string) (map[string]any, error)
	Publish(topic, subject, message string) (map[string]any, error)
}

type runner struct {
	tasks   Tasks
	record  func(eventType, state string, details any)
	context map[string]any // the $$ context object
}

// child returns a runner with its own copy of the context object, for concurrent branches.
func (r *runner) child() *runner {
	c := &runner{tasks: r.tasks, record: r.record, context: map[string]any{}}
	for k, v := range r.context {
		c.context[k] = v
	}
	return c
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
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fail("States.Timeout", "the execution timed out")
			}
			return nil, fail("States.Aborted", "the execution was stopped")
		}
		s := m.States[name]
		if s == nil {
			return nil, fail("States.Runtime", "state %q does not exist", name)
		}
		r.context["State"] = map[string]any{"Name": name, "EnteredTime": time.Now().UTC().Format(time.RFC3339Nano)}
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
					r.record(s.Type+"StateExited", name, map[string]any{"caught": se, "next": c.Next})
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

// step runs one state and returns its output and the next state name ("" = end).
func (r *runner) step(ctx context.Context, name string, s *State, raw any) (any, string, error) {
	ctxObj := any(r.context)
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
	effective := in
	if s.Parameters != nil && s.Type != "Map" {
		v, err := resolveParams(s.Parameters, in, ctxObj)
		if err != nil {
			return nil, "", fail("States.Runtime", "Parameters: %v", err)
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
		return applyOutput(r, s, raw, effective)
	case "Fail":
		return nil, "", &StateError{Name: orDefault(s.Error, "States.Fail"), Cause: s.Cause}
	case "Choice":
		for _, c := range s.Choices {
			ok, err := evalRule(c, in)
			if err != nil {
				return nil, "", fail("States.Runtime", "Choice %s: %v", name, err)
			}
			if ok {
				n, _ := c["Next"].(string)
				out, _, err := applyOutputNoResult(r, s, in)
				return out, n, err
			}
		}
		if s.Default == "" {
			return nil, "", fail("States.NoChoiceMatched", "no choice rule matched and there is no Default")
		}
		out, _, err := applyOutputNoResult(r, s, in)
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
		result = in
		out, _, err := applyOutputNoResult(r, s, result)
		if s.End {
			return out, "", err
		}
		return out, next, err
	case "Task":
		v, err := r.task(ctx, name, s, effective)
		if err != nil {
			return nil, "", err
		}
		result = v
	case "Parallel":
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
			return nil, "", err
		}
		result = outs
	case "Map":
		v, err := r.mapState(ctx, s, in)
		if err != nil {
			return nil, "", err
		}
		result = v
	}
	if s.ResultSelector != nil {
		v, err := resolveParams(s.ResultSelector, result, ctxObj)
		if err != nil {
			return nil, "", fail("States.Runtime", "ResultSelector: %v", err)
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
	out, _, err := applyOutputNoResult(r, s, out)
	if s.End {
		return out, "", err
	}
	return out, next, err
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func applyOutput(r *runner, s *State, _ any, v any) (any, string, error) {
	out, _, err := applyOutputNoResult(r, s, v)
	return out, "", err
}

func applyOutputNoResult(r *runner, s *State, v any) (any, string, error) {
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
	case s.Seconds > 0:
		return time.Duration(s.Seconds * float64(time.Second)), nil
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

func (r *runner) task(ctx context.Context, name string, s *State, input any) (any, error) {
	maxAttempts := func(rt Retrier) int {
		if rt.MaxAttempts == nil {
			return 3
		}
		return *rt.MaxAttempts
	}
	attempts := map[int]int{}
	for {
		tctx := ctx
		var cancel context.CancelFunc = func() {}
		if s.TimeoutSeconds > 0 {
			tctx, cancel = context.WithTimeout(ctx, time.Duration(s.TimeoutSeconds)*time.Second)
		}
		r.record("TaskScheduled", name, map[string]any{"resource": s.Resource})
		out, err := r.callResource(tctx, s.Resource, input)
		timedOut := errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancel()
		if timedOut {
			err = fail("States.Timeout", "task timed out after %d seconds", s.TimeoutSeconds)
		} else if err != nil && ctx.Err() != nil {
			return nil, ctxErr(ctx) // the execution itself timed out or was stopped
		}
		if err == nil {
			r.record("TaskSucceeded", name, map[string]any{"output": out})
			return out, nil
		}
		var se *StateError
		if !errors.As(err, &se) {
			se = fail("States.TaskFailed", "%v", err)
		}
		r.record("TaskFailed", name, se)
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
			wait := time.Duration(interval * math.Pow(rate, float64(attempts[i])) * float64(time.Second))
			attempts[i]++
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

func (r *runner) callResource(ctx context.Context, resource string, input any) (any, error) {
	payload, _ := json.Marshal(input)
	switch {
	case strings.HasSuffix(resource, ":lambda:invoke") || strings.HasSuffix(resource, ":lambda:invoke.waitForTaskToken"):
		m, _ := input.(map[string]any)
		fn := fmt.Sprint(m["FunctionName"])
		p, _ := json.Marshal(m["Payload"])
		res, errType, errMsg, err := r.tasks.Invoke(ctx, fnName(fn), p)
		if err != nil {
			return nil, fail("Lambda.ServiceException", "%v", err)
		}
		if errType != "" {
			return nil, &StateError{Name: errType, Cause: errMsg}
		}
		var out any
		_ = json.Unmarshal(res, &out)
		return map[string]any{"StatusCode": 200, "Payload": out}, nil
	case strings.Contains(resource, ":lambda:") && strings.Contains(resource, ":function:"):
		res, errType, errMsg, err := r.tasks.Invoke(ctx, fnName(resource), payload)
		if err != nil {
			return nil, fail("Lambda.ServiceException", "%v", err)
		}
		if errType != "" {
			return nil, &StateError{Name: errType, Cause: errMsg}
		}
		var out any
		_ = json.Unmarshal(res, &out)
		return out, nil
	case strings.HasSuffix(resource, ":sqs:sendMessage"):
		m, _ := input.(map[string]any)
		q := fmt.Sprint(firstOf(m, "QueueName", "QueueUrl"))
		body := m["MessageBody"]
		s, ok := body.(string)
		if !ok {
			b, _ := json.Marshal(body)
			s = string(b)
		}
		out, err := r.tasks.SendMessage(path.Base(q), s)
		if err != nil {
			return nil, fail("SQS.SdkClientException", "%v", err)
		}
		return out, nil
	case strings.HasSuffix(resource, ":sns:publish"):
		m, _ := input.(map[string]any)
		msg := m["Message"]
		s, ok := msg.(string)
		if !ok {
			b, _ := json.Marshal(msg)
			s = string(b)
		}
		subject, _ := m["Subject"].(string)
		out, err := r.tasks.Publish(fmt.Sprint(m["TopicArn"]), subject, s)
		if err != nil {
			return nil, fail("SNS.SdkClientException", "%v", err)
		}
		return out, nil
	}
	return nil, fail("States.Runtime", "unsupported Resource %q", resource)
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

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return ""
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

func (r *runner) mapState(ctx context.Context, s *State, in any) (any, error) {
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
	if limit <= 0 || limit > 40 {
		limit = 40
	}
	if len(items) > 10000 {
		return nil, fail("States.Runtime", "Map input has %d items; the limit is 10000", len(items))
	}
	inputs := make([]any, len(items))
	for i, item := range items {
		inputs[i] = item
		if selector != nil {
			ctxObj := map[string]any{"Map": map[string]any{"Item": map[string]any{"Index": i, "Value": item}}}
			for k, v := range r.context {
				ctxObj[k] = v
			}
			sv, err := resolveParams(selector, in, ctxObj)
			if err != nil {
				return nil, fail("States.Runtime", "ItemSelector: %v", err)
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
			out[i], errs[i] = r.child().run(mctx, proc, inputs[i])
			if errs[i] != nil {
				cancel()
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
		if op == "Variable" || op == "Next" {
			continue
		}
		if b, ok := want.(bool); ok {
			switch op {
			case "IsPresent":
				return (verr == nil) == b, nil
			case "IsNull":
				return (verr == nil && val == nil) == b, nil
			case "IsNumeric":
				_, isNum := val.(float64)
				return (verr == nil && isNum) == b, nil
			case "IsString":
				_, isStr := val.(string)
				return (verr == nil && isStr) == b, nil
			case "IsBoolean":
				_, isBool := val.(bool)
				return (verr == nil && isBool) == b, nil
			}
		}
		if verr != nil {
			return false, fmt.Errorf("variable %s: %v", varPath, verr)
		}
		if p, ok := strings.CutSuffix(op, "Path"); ok && op != "SecondsPath" {
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
			ok, _ := path.Match(strings.ReplaceAll(b, "/", "\x00"), strings.ReplaceAll(a, "/", "\x00"))
			return ok, nil
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
