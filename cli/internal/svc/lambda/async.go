package lambda

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const (
	maxAsyncPayload    = 1 << 20
	defaultEventAge    = 6 * 3600
	defaultAsyncRetry  = 2
	asyncInFlight      = 256
	asyncThrottleDelay = time.Second
)

// asyncRetryDelay is the wait before the first retry of a failed asynchronous
// invocation; the second retry waits twice as long (1 and 2 minutes, as in AWS).
var asyncRetryDelay = time.Minute

// EventInvokeConfig configures asynchronous invocation of a function version or alias.
type EventInvokeConfig struct {
	Function                 string    `json:"function"`
	Qualifier                string    `json:"qualifier"`
	MaximumRetryAttempts     *int      `json:"maximum_retry_attempts,omitempty"`
	MaximumEventAgeInSeconds *int      `json:"maximum_event_age_seconds,omitempty"`
	OnSuccess                string    `json:"on_success,omitempty"`
	OnFailure                string    `json:"on_failure,omitempty"`
	LastModified             time.Time `json:"last_modified"`
}

func (c EventInvokeConfig) retries() int {
	if c.MaximumRetryAttempts != nil {
		return *c.MaximumRetryAttempts
	}
	return defaultAsyncRetry
}

func (c EventInvokeConfig) maxAge() time.Duration {
	if c.MaximumEventAgeInSeconds != nil {
		return time.Duration(*c.MaximumEventAgeInSeconds) * time.Second
	}
	return defaultEventAge * time.Second
}

func (s *Service) invokeConfig(name, qual string) (EventInvokeConfig, bool) {
	c, err := store.Get[EventInvokeConfig](s.env.Store, cInvokeCfgs, name+":"+qualifierKey(qual))
	return c, err == nil
}

// invokeConfigInput sets asynchronous invocation options. Nil fields are
// left unchanged by an update (and unset by a put).
type invokeConfigInput struct {
	MaximumRetryAttempts     *int    `json:"maximum_retry_attempts"`
	MaximumEventAgeInSeconds *int    `json:"maximum_event_age_seconds"`
	OnSuccess                *string `json:"on_success"`
	OnFailure                *string `json:"on_failure"`
}

func (s *Service) putInvokeConfig(authz authorizer, name, qual string, in invokeConfigInput, merge bool) (EventInvokeConfig, error) {
	if _, _, err := s.resolve(name, qual); err != nil {
		return EventInvokeConfig{}, err
	}
	c := EventInvokeConfig{Function: name, Qualifier: qualifierKey(qual)}
	if merge {
		old, ok := s.invokeConfig(name, qual)
		if !ok {
			return c, core.Errf(http.StatusNotFound, "ResourceNotFound", "The function %s doesn't have an EventInvokeConfig", s.fnARN(name)+":"+qualifierKey(qual))
		}
		c = old
	}
	if in.MaximumRetryAttempts != nil {
		if *in.MaximumRetryAttempts < 0 || *in.MaximumRetryAttempts > 2 {
			return c, core.BadRequest("MaximumRetryAttempts must be between 0 and 2")
		}
		c.MaximumRetryAttempts = in.MaximumRetryAttempts
	}
	if in.MaximumEventAgeInSeconds != nil {
		if *in.MaximumEventAgeInSeconds < 60 || *in.MaximumEventAgeInSeconds > 21600 {
			return c, core.BadRequest("MaximumEventAgeInSeconds must be between 60 and 21600")
		}
		c.MaximumEventAgeInSeconds = in.MaximumEventAgeInSeconds
	}
	for _, d := range []struct {
		in  *string
		out *string
	}{{in.OnSuccess, &c.OnSuccess}, {in.OnFailure, &c.OnFailure}} {
		if d.in == nil {
			continue
		}
		if *d.in != "" {
			if err := s.checkTarget(authz, *d.in, false); err != nil {
				return c, err
			}
		}
		*d.out = *d.in
	}
	c.LastModified = time.Now().UTC()
	return c, store.Put(s.env.Store, cInvokeCfgs, name+":"+c.Qualifier, c)
}

func (s *Service) deleteInvokeConfig(name, qual string) error {
	if _, ok := s.invokeConfig(name, qual); !ok {
		return core.Errf(http.StatusNotFound, "ResourceNotFound", "The function %s doesn't have an EventInvokeConfig", s.fnARN(name)+":"+qualifierKey(qual))
	}
	return store.Delete(s.env.Store, cInvokeCfgs, name+":"+qualifierKey(qual))
}

func (s *Service) listInvokeConfigs(name string) []EventInvokeConfig {
	out := []EventInvokeConfig{}
	for _, c := range store.List[EventInvokeConfig](s.env.Store, cInvokeCfgs) {
		if c.Function == name {
			out = append(out, c)
		}
	}
	return out
}

// arnService returns the service of an ARN ("sqs", "sns", "lambda", "events").
func arnService(arn string) string {
	parts := strings.SplitN(core.CanonicalARN(arn), ":", 6)
	if len(parts) != 6 || parts[0] != "arn" {
		return ""
	}
	return parts[2]
}

// checkTarget validates a destination (or, with dlq, a dead-letter target)
// and that the caller could deliver to it directly.
func (s *Service) checkTarget(authz authorizer, arn string, dlq bool) error {
	arn = core.CanonicalARN(arn)
	action := map[string]string{"sqs": "sqs:SendMessage", "sns": "sns:Publish", "lambda": "lambda:InvokeFunction", "events": "events:PutEvents"}[arnService(arn)]
	if action == "" || (dlq && (action == "lambda:InvokeFunction" || action == "events:PutEvents")) {
		if dlq {
			return core.BadRequest("The provided target ARN %s is invalid: dead-letter targets must be SQS queues or SNS topics", arn)
		}
		return core.BadRequest("The provided destination config DestinationConfig is invalid: %s must be an SQS queue, SNS topic, Lambda function or EventBridge event bus", arn)
	}
	exists := false
	switch {
	case action == "lambda:InvokeFunction":
		exists = s.Exists(arn)
	case action == "events:PutEvents":
		exists = strings.Contains(arn, ":event-bus/")
	case s.TargetExists != nil:
		exists = s.TargetExists(arn)
	}
	if !exists {
		return core.BadRequest("The destination ARN %s is invalid or does not exist.", arn)
	}
	return authz(action, arn)
}

// asyncEvent is a queued asynchronous invocation.
type asyncEvent struct {
	id        string
	fn, qual  string
	payload   []byte
	clientCtx string
	received  time.Time
	attempts  int
}

// InvokeAsync queues an event for asynchronous invocation and returns its request ID.
func (s *Service) InvokeAsync(ref, qualifier string, payload []byte, clientCtx string) (string, error) {
	name, qual := parseRef(ref)
	if qualifier != "" {
		qual = qualifier
	}
	if _, _, err := s.resolve(name, qual); err != nil {
		return "", err
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if len(payload) > maxAsyncPayload {
		return "", core.Errf(http.StatusRequestEntityTooLarge, "RequestTooLargeException", "Request must be smaller than %d bytes for asynchronous invocations", maxAsyncPayload)
	}
	if !json.Valid(payload) {
		return "", core.Errf(http.StatusBadRequest, "InvalidRequestContentException", "Could not parse request body into json: payload is not valid JSON")
	}
	ev := &asyncEvent{id: uuid(), fn: name, qual: qual, payload: payload, clientCtx: clientCtx, received: time.Now()}
	select {
	case s.async <- ev:
		return ev.id, nil
	default:
		return "", core.Errf(http.StatusTooManyRequests, "TooManyRequests", "Rate Exceeded. (the asynchronous event queue is full)")
	}
}

// runAsync processes queued asynchronous invocations until ctx is done.
func (s *Service) runAsync(ctx context.Context) {
	sem := make(chan struct{}, asyncInFlight)
	for {
		var ev *asyncEvent
		select {
		case <-ctx.Done():
			return
		case ev = <-s.async:
		}
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		go func() {
			defer func() { <-sem }()
			defer core.Recover("lambda async " + ev.fn)
			s.processAsync(ctx, ev)
		}()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// processAsync invokes an event, retrying function errors (MaximumRetryAttempts)
// and throttles or system errors (until MaximumEventAgeInSeconds), then sends
// the outcome to the configured destination or dead-letter queue.
func (s *Service) processAsync(ctx context.Context, ev *asyncEvent) {
	backoff := asyncThrottleDelay
	for {
		cfg, _ := s.invokeConfig(ev.fn, ev.qual)
		if time.Since(ev.received) > cfg.maxAge() {
			s.asyncFinished(ctx, ev, cfg, "EventAgeExceeded", nil)
			return
		}
		res, err := s.InvokeWith(ctx, ev.fn, ev.payload, InvokeOptions{Qualifier: ev.qual, ClientContext: ev.clientCtx, Wait: 30 * time.Second})
		if err != nil {
			var ce *core.Error
			if errors.As(err, &ce) && ce.Status == http.StatusNotFound {
				log.Printf("lambda: dropping asynchronous event for %s: %v", ev.fn, err)
				return
			}
			if ctx.Err() != nil || !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		ev.attempts++
		if res.FunctionError == "" {
			s.asyncFinished(ctx, ev, cfg, "Success", res)
			return
		}
		if ev.attempts > cfg.retries() {
			s.asyncFinished(ctx, ev, cfg, "RetriesExhausted", res)
			return
		}
		wait := asyncRetryDelay * time.Duration(ev.attempts)
		if left := cfg.maxAge() - time.Since(ev.received); wait > left {
			wait = max(left, 0) + time.Millisecond
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// asyncFinished delivers an asynchronous invocation record to a destination
// and, on failure, the event to the dead-letter queue.
func (s *Service) asyncFinished(ctx context.Context, ev *asyncEvent, cfg EventInvokeConfig, condition string, res *InvokeResult) {
	f, _, err := s.resolve(ev.fn, ev.qual)
	if err != nil {
		return
	}
	success := condition == "Success"
	if !success {
		log.Printf("lambda: asynchronous invocation of %s failed (%s) after %d attempt(s)", ev.fn, condition, ev.attempts)
		if f.DeadLetterTarget != "" {
			if err := s.deliver(ctx, f.DeadLetterTarget, ev.payload, "", nil); err != nil {
				log.Printf("lambda: dead-letter delivery for %s: %v", ev.fn, err)
			}
		}
	}
	dest := cfg.OnFailure
	if success {
		dest = cfg.OnSuccess
	}
	if dest == "" {
		return
	}
	rec := map[string]any{
		"version":   "1.0",
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"requestContext": map[string]any{"requestId": ev.id, "functionArn": f.qualifiedARN(qualifierKey(ev.qual)),
			"condition": condition, "approximateInvokeCount": ev.attempts},
		"requestPayload": json.RawMessage(ev.payload),
	}
	if res != nil {
		rc := map[string]any{"statusCode": 200, "executedVersion": res.ExecutedVersion}
		if res.FunctionError != "" {
			rc["functionError"] = res.FunctionError
		}
		rec["responseContext"] = rc
		rec["responsePayload"] = res.Payload
	}
	b, _ := json.Marshal(rec)
	detailType := "Lambda Function Invocation Result - Success"
	if !success {
		detailType = "Lambda Function Invocation Result - Failure"
	}
	if err := s.deliver(ctx, dest, b, detailType, []string{f.qualifiedARN(qualifierKey(ev.qual))}); err != nil {
		log.Printf("lambda: destination %s for %s: %v", dest, ev.fn, err)
	}
}

// deliver sends a payload to a function (asynchronously), queue, topic or event bus.
func (s *Service) deliver(ctx context.Context, arn string, payload []byte, detailType string, resources []string) error {
	switch arnService(arn) {
	case "lambda":
		_, err := s.InvokeAsync(arn, "", payload, "")
		return err
	case "events":
		if s.PutEvent == nil {
			return errors.New("EventBridge is not available")
		}
		return s.PutEvent(ctx, arn, "lambda", detailType, resources, payload)
	}
	if s.Deliver == nil {
		return errors.New("delivery is not available")
	}
	return s.Deliver(ctx, arn, payload)
}
