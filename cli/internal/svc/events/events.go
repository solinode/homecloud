// Package events implements EventBridge: rules that fire on a schedule or
// when a published event matches a pattern, delivering to Lambda functions,
// SQS queues or SNS topics.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const cRules = "events_rules"

type Target struct {
	ID    string `json:"id"`
	ARN   string `json:"arn"`
	Input string `json:"input,omitempty"` // constant JSON sent instead of the event
}

type Rule struct {
	Name               string          `json:"name"`
	ARN                string          `json:"arn"`
	Description        string          `json:"description"`
	EventBus           string          `json:"event_bus"`
	ScheduleExpression string          `json:"schedule_expression,omitempty"`
	EventPattern       json.RawMessage `json:"event_pattern,omitempty"`
	State              string          `json:"state"` // ENABLED | DISABLED
	Targets            []Target        `json:"targets"`
	LastTriggered      *time.Time      `json:"last_triggered,omitempty"`
	NextRun            *time.Time      `json:"next_run,omitempty"`
	Invocations        int64           `json:"invocations"`
	FailedInvocations  int64           `json:"failed_invocations"`
	LastError          string          `json:"last_error,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
}

// Deliverer sends a payload to a target ARN (Lambda function, SQS queue or SNS topic).
type Deliverer func(ctx context.Context, arn string, payload []byte) error

type Service struct {
	env     *svc.Env
	Deliver Deliverer
	// Exists validates target ARNs at rule creation.
	Exists func(arn string) bool
}

func New(env *svc.Env) *Service { return &Service{env: env} }

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:events:local-1:{account}:rule/{name}")
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

func (s *Service) list(c *httpx.Ctx) (any, error) {
	return store.List[Rule](s.env.Store, cRules), nil
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	r, err := store.Get[Rule](s.env.Store, cRules, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("rule", c.Param("name"))
	}
	return r, nil
}

var nameRe = regexp.MustCompile(`^[\w.-]{1,64}$`)

func (s *Service) put(c *httpx.Ctx) (any, error) {
	var in Rule
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	name := c.Param("name")
	if !nameRe.MatchString(name) {
		return nil, core.BadRequest("rule names are 1-64 letters, digits, dots, hyphens or underscores")
	}
	hasSched, hasPattern := in.ScheduleExpression != "", len(in.EventPattern) > 0 && string(in.EventPattern) != "null"
	if hasSched == hasPattern {
		return nil, core.BadRequest("a rule needs exactly one of schedule_expression or event_pattern")
	}
	r := Rule{Name: name, ARN: s.env.ARN("events", "rule/"+name), Description: in.Description, EventBus: "default",
		ScheduleExpression: in.ScheduleExpression, State: "ENABLED", Targets: []Target{}, CreatedAt: core.Now()}
	if in.State == "DISABLED" {
		r.State = "DISABLED"
	}
	if hasSched {
		sch, err := ParseSchedule(in.ScheduleExpression)
		if err != nil {
			return nil, core.BadRequest("%v", err)
		}
		n := sch.Next(time.Now())
		r.NextRun = &n
	} else {
		var p map[string]any
		if err := json.Unmarshal(in.EventPattern, &p); err != nil {
			return nil, core.BadRequest("event_pattern must be a JSON object")
		}
		r.EventPattern = in.EventPattern
	}
	if len(in.Targets) > 5 {
		return nil, core.BadRequest("a rule has at most 5 targets")
	}
	for i, t := range in.Targets {
		action := core.TargetAction(t.ARN)
		if action == "" {
			return nil, core.BadRequest("target %s is not a Lambda function, SQS queue, SNS topic or state machine ARN", t.ARN)
		}
		if err := c.Authorize(action, t.ARN); err != nil {
			return nil, err
		}
		if s.Exists != nil && !s.Exists(t.ARN) {
			return nil, core.BadRequest("target %s does not exist (use a Lambda function, SQS queue, SNS topic or state machine ARN)", t.ARN)
		}
		if t.Input != "" && !json.Valid([]byte(t.Input)) {
			return nil, core.BadRequest("target input must be JSON")
		}
		if t.ID == "" {
			t.ID = fmt.Sprintf("target-%d", i+1)
		}
		r.Targets = append(r.Targets, t)
	}
	if old, err := store.Get[Rule](s.env.Store, cRules, name); err == nil {
		r.CreatedAt, r.Invocations, r.FailedInvocations, r.LastTriggered = old.CreatedAt, old.Invocations, old.FailedInvocations, old.LastTriggered
	}
	return r, store.Put(s.env.Store, cRules, name, r)
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	if err := store.Delete(s.env.Store, cRules, c.Param("name")); err != nil {
		return nil, core.NotFound("rule", c.Param("name"))
	}
	return nil, nil
}

func (s *Service) setState(c *httpx.Ctx, state string) (any, error) {
	r, err := store.Update(s.env.Store, cRules, c.Param("name"), func(r *Rule) error { r.State = state; return nil })
	if err == store.ErrNotFound {
		return nil, core.NotFound("rule", c.Param("name"))
	}
	return r, err
}

func (s *Service) enable(c *httpx.Ctx) (any, error)  { return s.setState(c, "ENABLED") }
func (s *Service) disable(c *httpx.Ctx) (any, error) { return s.setState(c, "DISABLED") }

// event builds an EventBridge-shaped event envelope.
func (s *Service) event(source, detailType string, resources []string, detail json.RawMessage) map[string]any {
	if len(detail) == 0 {
		detail = json.RawMessage("{}")
	}
	if resources == nil {
		resources = []string{}
	}
	return map[string]any{"version": "0", "id": uuid(), "detail-type": detailType, "source": source, "account": s.env.AccountID,
		"time": time.Now().UTC().Format(time.RFC3339), "region": s.env.Cfg.Region, "resources": resources, "detail": detail}
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Service) fire(ctx context.Context, r Rule, ev map[string]any) {
	defer core.Recover("events rule " + r.Name)
	payload, _ := json.Marshal(ev)
	var lastErr error
	for _, t := range r.Targets {
		body := payload
		if t.Input != "" {
			body = []byte(t.Input)
		}
		if s.Deliver == nil {
			continue
		}
		if err := s.Deliver(ctx, t.ARN, body); err != nil {
			lastErr = err
			log.Printf("events: rule %s -> %s: %v", r.Name, t.ARN, err)
		}
	}
	_, _ = store.Update(s.env.Store, cRules, r.Name, func(x *Rule) error {
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
	s.fire(c.R.Context(), r, s.event("aws.events", "Scheduled Event", []string{r.ARN}, nil))
	return store.Get[Rule](s.env.Store, cRules, r.Name)
}

type entry struct {
	Source     string          `json:"source"`
	DetailType string          `json:"detail_type"`
	Detail     json.RawMessage `json:"detail"`
	Resources  []string        `json:"resources"`
}

// Publish routes one event to every enabled rule whose pattern matches.
func (s *Service) Publish(ctx context.Context, e entry) (string, int) {
	ev := s.event(e.Source, e.DetailType, e.Resources, e.Detail)
	var generic map[string]any
	b, _ := json.Marshal(ev)
	_ = json.Unmarshal(b, &generic)
	n := 0
	for _, r := range store.List[Rule](s.env.Store, cRules) {
		if r.State != "ENABLED" || len(r.EventPattern) == 0 {
			continue
		}
		var p map[string]any
		if json.Unmarshal(r.EventPattern, &p) != nil || !matchPattern(p, generic) {
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
		id, n := s.Publish(c.R.Context(), e)
		out = append(out, map[string]any{"event_id": id, "matched_rules": n})
	}
	return map[string]any{"entries": out, "failed_entry_count": 0}, nil
}

func (s *Service) testPattern(c *httpx.Ctx) (any, error) {
	var in struct {
		Pattern map[string]any `json:"pattern"`
		Event   map[string]any `json:"event"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return map[string]bool{"result": matchPattern(in.Pattern, in.Event)}, nil
}

// matchPattern implements EventBridge matching: every pattern key must match;
// arrays list allowed values; nested objects recurse; {"prefix":"x"},
// {"exists":bool} and {"anything-but":[...]} are supported.
func matchPattern(pattern, ev map[string]any) bool {
	for k, pv := range pattern {
		v, present := ev[k]
		switch p := pv.(type) {
		case map[string]any:
			sub, ok := v.(map[string]any)
			if !ok || !matchPattern(p, sub) {
				return false
			}
		case []any:
			if !matchValues(p, v, present) {
				return false
			}
		default:
			if !matchValues([]any{p}, v, present) {
				return false
			}
		}
	}
	return true
}

func matchValues(allowed []any, v any, present bool) bool {
	vals := []any{v}
	if arr, ok := v.([]any); ok {
		vals = arr
	}
	for _, a := range allowed {
		if m, ok := a.(map[string]any); ok {
			if ex, ok := m["exists"].(bool); ok {
				if ex == present {
					return true
				}
				continue
			}
			if !present {
				continue
			}
			if pre, ok := m["prefix"].(string); ok {
				for _, x := range vals {
					if s, ok := x.(string); ok && strings.HasPrefix(s, pre) {
						return true
					}
				}
			}
			if ab, ok := m["anything-but"]; ok {
				list, isList := ab.([]any)
				if !isList {
					list = []any{ab}
				}
				for _, x := range vals {
					if !slices.ContainsFunc(list, func(y any) bool { return fmt.Sprint(y) == fmt.Sprint(x) }) {
						return true
					}
				}
			}
			if num, ok := m["numeric"].([]any); ok {
				for _, x := range vals {
					if f, ok := x.(float64); ok && numeric(num, f) {
						return true
					}
				}
			}
			continue
		}
		if !present {
			continue
		}
		for _, x := range vals {
			if fmt.Sprint(x) == fmt.Sprint(a) {
				return true
			}
		}
	}
	return false
}

func numeric(cond []any, v float64) bool {
	for i := 0; i+1 < len(cond); i += 2 {
		op, _ := cond[i].(string)
		n, _ := cond[i+1].(float64)
		ok := map[string]bool{"<": v < n, "<=": v <= n, "=": v == n, ">": v > n, ">=": v >= n}[op]
		if !ok {
			return false
		}
	}
	return true
}

// Run fires scheduled rules until ctx is done.
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
		now = time.Now()
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
			_, _ = store.Update(s.env.Store, cRules, r.Name, func(x *Rule) error { x.NextRun, x.LastTriggered = &next, &fired; return nil })
			go s.fire(ctx, r, s.event("aws.events", "Scheduled Event", []string{r.ARN}, nil))
		}
	}
}
