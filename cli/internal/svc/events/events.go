// Package events implements EventBridge: event buses, rules that fire on a
// schedule or when a published event matches a pattern, delivering to Lambda
// functions, SQS queues, SNS topics or state machines; and EventBridge
// Scheduler schedules (scheduler.go).
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	cRules = "events_rules"
	cBuses = "events_buses"
)

type InputTransformer struct {
	InputPathsMap map[string]string `json:"input_paths_map,omitempty"`
	InputTemplate string            `json:"input_template"`
}

type RetryPolicy struct {
	MaximumRetryAttempts     *int `json:"maximum_retry_attempts,omitempty"`
	MaximumEventAgeInSeconds *int `json:"maximum_event_age_in_seconds,omitempty"`
}

type Target struct {
	ID               string            `json:"id"`
	ARN              string            `json:"arn"`
	Input            string            `json:"input,omitempty"` // constant JSON sent instead of the event
	InputPath        string            `json:"input_path,omitempty"`
	InputTransformer *InputTransformer `json:"input_transformer,omitempty"`
	RoleARN          string            `json:"role_arn,omitempty"`
	RetryPolicy      *RetryPolicy      `json:"retry_policy,omitempty"`
	DeadLetterARN    string            `json:"dead_letter_arn,omitempty"`
	MessageGroupID   string            `json:"message_group_id,omitempty"` // SQS FIFO targets
}

type Rule struct {
	Name               string          `json:"name"`
	ARN                string          `json:"arn"`
	Description        string          `json:"description"`
	EventBus           string          `json:"event_bus"`
	ScheduleExpression string          `json:"schedule_expression,omitempty"`
	EventPattern       json.RawMessage `json:"event_pattern,omitempty"`
	State              string          `json:"state"` // ENABLED | DISABLED
	RoleARN            string          `json:"role_arn,omitempty"`
	Targets            []Target        `json:"targets"`
	LastTriggered      *time.Time      `json:"last_triggered,omitempty"`
	NextRun            *time.Time      `json:"next_run,omitempty"`
	Invocations        int64           `json:"invocations"`
	FailedInvocations  int64           `json:"failed_invocations"`
	LastError          string          `json:"last_error,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	Tags               core.Tags       `json:"tags,omitempty"`
}

// Bus is a custom event bus ("default" always exists and is not stored).
type Bus struct {
	Name          string    `json:"name"`
	ARN           string    `json:"arn"`
	Description   string    `json:"description,omitempty"`
	DeadLetterARN string    `json:"dead_letter_arn,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Tags          core.Tags `json:"tags,omitempty"`
}

// Deliverer sends a payload to a target ARN (Lambda function, SQS queue, SNS topic or state machine).
type Deliverer func(ctx context.Context, arn string, payload []byte) error

type Service struct {
	env     *svc.Env
	Deliver Deliverer
	// Exists validates target ARNs at rule creation (native API).
	Exists func(arn string) bool
	// retryBase is the first retry delay (shortened in tests).
	retryBase time.Duration
}

func New(env *svc.Env) *Service { return &Service{env: env, retryBase: time.Second} }

// ruleKey stores default-bus rules by name (as before buses existed) and
// others as "bus/name" (rule names cannot contain "/").
func ruleKey(bus, name string) string {
	if bus == "" || bus == "default" {
		return name
	}
	return bus + "/" + name
}

func (s *Service) ruleARN(bus, name string) string {
	if bus == "" || bus == "default" {
		return s.env.ARN("events", "rule/"+name)
	}
	return s.env.ARN("events", "rule/"+bus+"/"+name)
}

func (s *Service) busARN(name string) string { return s.env.ARN("events", "event-bus/"+name) }

// busExists reports whether an event bus exists.
func (s *Service) busExists(name string) bool {
	return name == "default" || store.Has(s.env.Store, cBuses, name)
}

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:events:{region}:{account}:rule/{name}")
	r.Handle("GET /api/v1/events/rules", "events:ListRules", s.list)
	r.Handle("PUT /api/v1/events/rules/{name}", "events:PutRule", s.put, res)
	r.Handle("GET /api/v1/events/rules/{name}", "events:DescribeRule", s.get, res)
	r.Handle("DELETE /api/v1/events/rules/{name}", "events:DeleteRule", s.delete, res)
	r.Handle("POST /api/v1/events/rules/{name}/enable", "events:EnableRule", s.enable, res)
	r.Handle("POST /api/v1/events/rules/{name}/disable", "events:DisableRule", s.disable, res)
	r.Handle("POST /api/v1/events/rules/{name}/run", "events:PutEvents", s.runNow, res)
	r.Handle("POST /api/v1/events/events", "events:PutEvents", s.putEvents)
	r.Handle("POST /api/v1/events/test-pattern", "events:TestEventPattern", s.testPattern)
}

// defaultRules lists the default bus's rules (the native API's view).
func (s *Service) defaultRules() []Rule {
	out := []Rule{}
	for _, r := range store.List[Rule](s.env.Store, cRules) {
		if r.EventBus == "" || r.EventBus == "default" {
			out = append(out, r)
		}
	}
	return out
}

func (s *Service) list(c *httpx.Ctx) (any, error) { return s.defaultRules(), nil }

func (s *Service) get(c *httpx.Ctx) (any, error) {
	r, err := store.Get[Rule](s.env.Store, cRules, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("rule", c.Param("name"))
	}
	return r, nil
}

var nameRe = regexp.MustCompile(`^[\w.-]{1,64}$`)

// RuleInput is what PutRule sets.
type RuleInput struct {
	Name, Bus, Description, ScheduleExpression, State, RoleARN string
	EventPattern                                               json.RawMessage
	Tags                                                       core.Tags
}

// PutRule creates or updates a rule and keeps its targets.
func (s *Service) PutRule(in RuleInput) (Rule, error) {
	if in.Bus == "" {
		in.Bus = "default"
	}
	if !nameRe.MatchString(in.Name) {
		return Rule{}, core.BadRequest("rule names are 1-64 letters, digits, dots, hyphens or underscores")
	}
	if !s.busExists(in.Bus) {
		return Rule{}, core.Errf(http.StatusNotFound, "ResourceNotFound", "Event bus %s does not exist.", in.Bus)
	}
	hasSched, hasPattern := in.ScheduleExpression != "", len(in.EventPattern) > 0 && string(in.EventPattern) != "null"
	if !hasSched && !hasPattern {
		return Rule{}, core.BadRequest("a rule needs a schedule expression or an event pattern")
	}
	if hasSched && hasPattern {
		return Rule{}, core.BadRequest("HomeCloud rules have either a schedule expression or an event pattern, not both")
	}
	r := Rule{Name: in.Name, ARN: s.ruleARN(in.Bus, in.Name), Description: in.Description, EventBus: in.Bus,
		ScheduleExpression: in.ScheduleExpression, State: "ENABLED", RoleARN: in.RoleARN, Targets: []Target{}, CreatedAt: core.Now(), Tags: in.Tags}
	switch in.State {
	case "", "ENABLED":
	case "DISABLED":
		r.State = "DISABLED"
	default:
		return Rule{}, core.BadRequest("State must be ENABLED or DISABLED")
	}
	if hasSched {
		if in.Bus != "default" {
			return Rule{}, core.BadRequest("ScheduleExpression is supported only on the default event bus.")
		}
		sch, err := ParseSchedule(in.ScheduleExpression)
		if err != nil {
			return Rule{}, core.BadRequest("Parameter ScheduleExpression is not valid: %v", err)
		}
		n := sch.Next(time.Now())
		r.NextRun = &n
	} else {
		if _, err := CompilePattern(in.EventPattern); err != nil {
			return Rule{}, &core.Error{Status: http.StatusBadRequest, Code: "InvalidEventPattern", Message: err.Error()}
		}
		r.EventPattern = in.EventPattern
	}
	key := ruleKey(in.Bus, in.Name)
	if old, err := store.Get[Rule](s.env.Store, cRules, key); err == nil {
		r.CreatedAt, r.Invocations, r.FailedInvocations, r.LastTriggered, r.Targets = old.CreatedAt, old.Invocations, old.FailedInvocations, old.LastTriggered, old.Targets
		r.Tags = old.Tags // tags change through TagResource
		if r.Targets == nil {
			r.Targets = []Target{}
		}
	}
	return r, store.Put(s.env.Store, cRules, key, r)
}

// ValidateTarget checks a target's static configuration.
func ValidateTarget(t Target) error {
	if t.ID == "" || !regexp.MustCompile(`^[\.\-_A-Za-z0-9]{1,64}$`).MatchString(t.ID) {
		return core.BadRequest("target IDs are 1-64 letters, digits, . - or _")
	}
	if core.TargetAction(t.ARN) == "" {
		return core.BadRequest("target %s is not a Lambda function, SQS queue, SNS topic or state machine ARN", t.ARN)
	}
	n := 0
	for _, set := range []bool{t.Input != "", t.InputPath != "", t.InputTransformer != nil} {
		if set {
			n++
		}
	}
	if n > 1 {
		return core.BadRequest("Input, InputPath and InputTransformer are mutually exclusive")
	}
	if t.Input != "" && !json.Valid([]byte(t.Input)) {
		return core.BadRequest("target input must be JSON")
	}
	if t.InputPath != "" {
		if _, err := parseEventPath(t.InputPath); err != nil {
			return core.BadRequest("InputPath: %v", err)
		}
	}
	if it := t.InputTransformer; it != nil {
		if it.InputTemplate == "" {
			return core.BadRequest("InputTransformer.InputTemplate is required")
		}
		if len(it.InputPathsMap) > 100 {
			return core.BadRequest("InputPathsMap has at most 100 entries")
		}
		for k, p := range it.InputPathsMap {
			if strings.HasPrefix(k, "aws.") || !regexp.MustCompile(`^[A-Za-z0-9_\-]+$`).MatchString(k) {
				return core.BadRequest("InputPathsMap key %q is not valid", k)
			}
			if _, err := parseEventPath(p); err != nil {
				return core.BadRequest("InputPathsMap %s: %v", k, err)
			}
		}
	}
	if rp := t.RetryPolicy; rp != nil {
		if rp.MaximumRetryAttempts != nil && (*rp.MaximumRetryAttempts < 0 || *rp.MaximumRetryAttempts > 185) {
			return core.BadRequest("MaximumRetryAttempts must be 0-185")
		}
		if rp.MaximumEventAgeInSeconds != nil && (*rp.MaximumEventAgeInSeconds < 60 || *rp.MaximumEventAgeInSeconds > 86400) {
			return core.BadRequest("MaximumEventAgeInSeconds must be 60-86400")
		}
	}
	if t.DeadLetterARN != "" && !strings.HasPrefix(core.CanonicalARN(t.DeadLetterARN), "arn:"+core.Partition+":sqs:") {
		return core.BadRequest("a dead-letter queue must be an SQS queue ARN")
	}
	return nil
}

// PutTargets adds or replaces targets (by ID) on a rule.
func (s *Service) PutTargets(bus, rule string, ts []Target) (Rule, error) {
	if bus == "" {
		bus = "default"
	}
	for _, t := range ts {
		if err := ValidateTarget(t); err != nil {
			return Rule{}, err
		}
	}
	r, err := store.Update(s.env.Store, cRules, ruleKey(bus, rule), func(r *Rule) error {
		for _, t := range ts {
			replaced := false
			for i := range r.Targets {
				if r.Targets[i].ID == t.ID {
					r.Targets[i], replaced = t, true
				}
			}
			if !replaced {
				r.Targets = append(r.Targets, t)
			}
		}
		if len(r.Targets) > 5 {
			return core.Errf(http.StatusBadRequest, "LimitExceeded", "a rule has at most 5 targets")
		}
		return nil
	})
	if err == store.ErrNotFound {
		return r, ruleNotFound(bus, rule)
	}
	return r, err
}

func ruleNotFound(bus, name string) error {
	return core.Errf(http.StatusNotFound, "ResourceNotFound", "Rule %s does not exist on EventBus %s.", name, bus)
}

// put is the native API: the body carries the rule and its complete target list.
func (s *Service) put(c *httpx.Ctx) (any, error) {
	var in Rule
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	name := c.Param("name")
	if len(in.Targets) > 5 {
		return nil, core.BadRequest("a rule has at most 5 targets")
	}
	for i := range in.Targets {
		t := &in.Targets[i]
		if t.ID == "" {
			t.ID = fmt.Sprintf("target-%d", i+1)
		}
		if err := ValidateTarget(*t); err != nil {
			return nil, err
		}
		if err := c.Authorize(core.TargetAction(t.ARN), t.ARN); err != nil {
			return nil, err
		}
		if t.DeadLetterARN != "" {
			if err := c.Authorize("sqs:SendMessage", t.DeadLetterARN); err != nil {
				return nil, err
			}
		}
		if s.Exists != nil && !s.Exists(t.ARN) {
			return nil, core.BadRequest("target %s does not exist (use a Lambda function, SQS queue, SNS topic or state machine ARN)", t.ARN)
		}
	}
	if err := c.Authorize("events:PutRule", s.ruleARN("default", name)); err != nil {
		return nil, err
	}
	if _, err := s.PutRule(RuleInput{Name: name, Bus: "default", Description: in.Description, ScheduleExpression: in.ScheduleExpression,
		State: in.State, EventPattern: in.EventPattern, RoleARN: in.RoleARN}); err != nil {
		return nil, err
	}
	if in.Targets == nil {
		in.Targets = []Target{}
	}
	return store.Update(s.env.Store, cRules, name, func(r *Rule) error { r.Targets = in.Targets; return nil })
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	if err := store.Delete(s.env.Store, cRules, c.Param("name")); err != nil {
		return nil, core.NotFound("rule", c.Param("name"))
	}
	return nil, nil
}

// SetState enables or disables a rule.
func (s *Service) SetState(bus, name, state string) (Rule, error) {
	r, err := store.Update(s.env.Store, cRules, ruleKey(bus, name), func(r *Rule) error {
		r.State = state
		if r.ScheduleExpression != "" && state == "ENABLED" {
			if sch, err := ParseSchedule(r.ScheduleExpression); err == nil {
				n := sch.Next(time.Now())
				r.NextRun = &n
			}
		}
		return nil
	})
	if err == store.ErrNotFound {
		return r, ruleNotFound(bus, name)
	}
	return r, err
}

func (s *Service) enable(c *httpx.Ctx) (any, error) {
	return s.SetState("default", c.Param("name"), "ENABLED")
}
func (s *Service) disable(c *httpx.Ctx) (any, error) {
	return s.SetState("default", c.Param("name"), "DISABLED")
}

// event builds an EventBridge-shaped event envelope.
func (s *Service) event(source, detailType string, resources []string, detail json.RawMessage, at time.Time) map[string]any {
	if len(detail) == 0 {
		detail = json.RawMessage("{}")
	}
	if resources == nil {
		resources = []string{}
	}
	if at.IsZero() {
		at = time.Now()
	}
	return map[string]any{"version": "0", "id": uuid(), "detail-type": detailType, "source": source, "account": s.env.AccountID,
		"time": at.UTC().Format(time.RFC3339), "region": s.env.Cfg.Region, "resources": resources, "detail": detail}
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// payloadFor applies a target's Input, InputPath or InputTransformer to an event.
func payloadFor(t Target, r Rule, ev map[string]any, raw []byte) ([]byte, error) {
	switch {
	case t.Input != "":
		return []byte(t.Input), nil
	case t.InputPath != "":
		p, err := parseEventPath(t.InputPath)
		if err != nil {
			return nil, err
		}
		v, _ := p.get(ev)
		return json.Marshal(v)
	case t.InputTransformer != nil:
		return transform(t.InputTransformer, r, ev, raw)
	}
	return raw, nil
}

// ErrNoRetry marks delivery errors EventBridge does not retry, such as a
// Lambda function's own error (EventBridge invokes functions asynchronously,
// so in AWS the function's failure is not a delivery failure).
var ErrNoRetry = errors.New("not retried")

// permanent reports errors that retrying cannot fix (missing or forbidden targets).
func permanent(err error) bool {
	if errors.Is(err, ErrNoRetry) {
		return true
	}
	var ce *core.Error
	if errors.As(err, &ce) {
		return ce.Status == http.StatusNotFound || ce.Status == http.StatusForbidden || ce.Status == http.StatusBadRequest
	}
	return false
}

// deliver sends one payload, retrying with backoff per the target's retry
// policy, then to its dead-letter queue.
func (s *Service) deliver(ctx context.Context, r Rule, t Target, payload []byte, eventTime time.Time) error {
	if s.Deliver == nil {
		return nil
	}
	attempts, maxAge := 185, 24*time.Hour
	if rp := t.RetryPolicy; rp != nil {
		if rp.MaximumRetryAttempts != nil {
			attempts = *rp.MaximumRetryAttempts
		}
		if rp.MaximumEventAgeInSeconds != nil {
			maxAge = time.Duration(*rp.MaximumEventAgeInSeconds) * time.Second
		}
	}
	var err error
	for i := 0; ; i++ {
		if err = s.Deliver(ctx, t.ARN, payload); err == nil {
			return nil
		}
		if permanent(err) || i >= attempts || ctx.Err() != nil {
			break
		}
		wait := time.Duration(float64(s.retryBase) * math.Pow(2, float64(min(i, 8))))
		if time.Since(eventTime)+wait > maxAge {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	log.Printf("events: rule %s -> %s: %v", r.Name, t.ARN, err)
	if t.DeadLetterARN != "" {
		if derr := s.Deliver(context.Background(), t.DeadLetterARN, payload); derr != nil {
			log.Printf("events: dead-letter queue %s: %v", t.DeadLetterARN, derr)
		}
	}
	return err
}

func (s *Service) fire(ctx context.Context, r Rule, ev map[string]any) {
	defer core.Recover("events rule " + r.Name)
	raw, _ := json.Marshal(ev)
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	eventTime := time.Now()
	errs := make(chan error, len(r.Targets))
	for _, t := range r.Targets {
		payload, err := payloadFor(t, r, generic, raw)
		if err != nil {
			errs <- err
			continue
		}
		go func(t Target, payload []byte) { errs <- s.deliver(ctx, r, t, payload, eventTime) }(t, payload)
	}
	var lastErr error
	for range r.Targets {
		if err := <-errs; err != nil {
			lastErr = err
		}
	}
	_, _ = store.Update(s.env.Store, cRules, ruleKey(r.EventBus, r.Name), func(x *Rule) error {
		if x.ScheduleExpression == "" { // scheduled rules record the trigger time before firing
			n := core.Now()
			x.LastTriggered = &n
		}
		x.Invocations++
		if lastErr != nil {
			x.FailedInvocations++
			x.LastError = lastErr.Error()
		}
		return nil
	})
}

func (s *Service) runNow(c *httpx.Ctx) (any, error) {
	r, err := store.Get[Rule](s.env.Store, cRules, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("rule", c.Param("name"))
	}
	s.fire(c.R.Context(), r, s.event("aws.events", "Scheduled Event", []string{r.ARN}, nil, time.Time{}))
	return store.Get[Rule](s.env.Store, cRules, r.Name)
}

type entry struct {
	Source     string          `json:"source"`
	DetailType string          `json:"detail_type"`
	Detail     json.RawMessage `json:"detail"`
	Resources  []string        `json:"resources"`
	Bus        string          `json:"event_bus,omitempty"`
	Time       time.Time       `json:"time,omitempty"`
}

// Publish routes one event to every enabled rule on its bus whose pattern matches.
func (s *Service) Publish(ctx context.Context, e entry) (string, int) {
	bus := e.Bus
	if bus == "" {
		bus = "default"
	}
	ev := s.event(e.Source, e.DetailType, e.Resources, e.Detail, e.Time)
	var generic map[string]any
	b, _ := json.Marshal(ev)
	_ = json.Unmarshal(b, &generic)
	n := 0
	for _, r := range store.List[Rule](s.env.Store, cRules) {
		rb := r.EventBus
		if rb == "" {
			rb = "default"
		}
		if rb != bus || r.State != "ENABLED" || len(r.EventPattern) == 0 {
			continue
		}
		p, err := CompilePattern(r.EventPattern)
		if err != nil || !p.Match(generic) {
			continue
		}
		n++
		go s.fire(context.Background(), r, ev)
	}
	return ev["id"].(string), n
}

func (s *Service) putEvents(c *httpx.Ctx) (any, error) {
	var in struct {
		Entries []entry `json:"entries"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Entries) == 0 || len(in.Entries) > 10 {
		return nil, core.BadRequest("send 1-10 entries")
	}
	out := []map[string]any{}
	for _, e := range in.Entries {
		if e.Source == "" || e.DetailType == "" {
			return nil, core.BadRequest("source and detail_type are required")
		}
		if strings.HasPrefix(e.Source, "aws.") {
			return nil, core.BadRequest("the aws. source prefix is reserved")
		}
		if len(e.Detail) > 0 && !json.Valid(e.Detail) {
			return nil, core.BadRequest("detail must be JSON")
		}
		e.Bus = "default"
		id, n := s.Publish(c.R.Context(), e)
		out = append(out, map[string]any{"event_id": id, "matched_rules": n})
	}
	return map[string]any{"entries": out, "failed_entry_count": 0}, nil
}

func (s *Service) testPattern(c *httpx.Ctx) (any, error) {
	var in struct {
		Pattern json.RawMessage `json:"pattern"`
		Event   json.RawMessage `json:"event"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	ok, err := MatchJSON(in.Pattern, in.Event)
	if err != nil {
		return nil, core.BadRequest("%v", err)
	}
	return map[string]bool{"result": ok}, nil
}

// Run fires scheduled rules and schedules until ctx is done.
func (s *Service) Run(ctx context.Context) {
	for {
		now := time.Now()
		// Tick at the top of each minute (plus a little), like EventBridge.
		wait := now.Truncate(time.Minute).Add(time.Minute + 2*time.Second).Sub(now)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		s.tick(ctx, time.Now())
	}
}

func (s *Service) tick(ctx context.Context, now time.Time) {
	for _, r := range store.List[Rule](s.env.Store, cRules) {
		if r.State != "ENABLED" || r.ScheduleExpression == "" {
			continue
		}
		sch, err := ParseSchedule(r.ScheduleExpression)
		if err != nil {
			continue
		}
		last := time.Time{}
		if r.LastTriggered != nil {
			last = *r.LastTriggered
		} else if _, isRate := sch.(rate); isRate {
			last = r.CreatedAt
		}
		if !sch.Due(now, last) {
			continue
		}
		// Record the trigger before firing so a slow target cannot make the rule fire again.
		next := sch.Next(now)
		fired := now.UTC().Truncate(time.Second)
		_, _ = store.Update(s.env.Store, cRules, ruleKey(r.EventBus, r.Name), func(x *Rule) error { x.NextRun, x.LastTriggered = &next, &fired; return nil })
		go s.fire(ctx, r, s.event("aws.events", "Scheduled Event", []string{r.ARN}, nil, time.Time{}))
	}
	s.tickSchedules(ctx, now)
}
