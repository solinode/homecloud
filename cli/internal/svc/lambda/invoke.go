package lambda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
)

// maxSyncPayload is the largest synchronous request or response (6 MB).
const maxSyncPayload = 6 << 20

// InvokeResult is the outcome of one synchronous invocation.
type InvokeResult struct {
	RequestID       string          `json:"request_id"`
	StatusCode      int             `json:"status_code"`
	Payload         json.RawMessage `json:"payload"`
	FunctionError   string          `json:"function_error,omitempty"`
	Logs            string          `json:"logs"` // the invocation's log output (last 4 KB)
	DurationMS      float64         `json:"duration_ms"`
	BilledMS        int64           `json:"billed_duration_ms"`
	ColdStart       bool            `json:"cold_start"`
	ExecutedVersion string          `json:"executed_version"`
	// body is the function's response exactly as returned.
	body []byte
}

// Body is the function's response as returned by the runtime.
func (r *InvokeResult) Body() []byte {
	if r.body != nil {
		return r.body
	}
	return r.Payload
}

// InvokeOptions tune an invocation.
type InvokeOptions struct {
	Qualifier     string        // version or alias (overrides one in the reference)
	ClientContext string        // base64 JSON passed to the function's context
	Wait          time.Duration // how long to queue for a free environment (default: brief)
}

// Invoke runs a function synchronously with payload as the event. ref is a
// function name, "name:qualifier" or an ARN.
func (s *Service) Invoke(ctx context.Context, ref string, payload []byte) (*InvokeResult, error) {
	return s.InvokeWith(ctx, ref, payload, InvokeOptions{})
}

// InvokeWith is Invoke with options.
func (s *Service) InvokeWith(ctx context.Context, ref string, payload []byte, o InvokeOptions) (*InvokeResult, error) {
	name, qual := parseRef(ref)
	if o.Qualifier != "" {
		if qual != "" && qual != o.Qualifier {
			return nil, core.BadRequest("The derived qualifier from the function name does not match the specified qualifier.")
		}
		qual = o.Qualifier
	}
	head, err := s.getLatest(name)
	if err != nil {
		return nil, err
	}
	cfg, _, err := s.resolve(name, qual)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = []byte("{}")
	}
	if len(payload) > maxSyncPayload {
		return nil, core.Errf(http.StatusRequestEntityTooLarge, "RequestTooLargeException", "Request must be smaller than %d bytes for the InvokeFunction operation", maxSyncPayload)
	}
	if !json.Valid(payload) {
		return nil, core.Errf(http.StatusBadRequest, "InvalidRequestContentException", "Could not parse request body into json: payload is not valid JSON")
	}
	if cfg.State == "Failed" {
		return nil, core.Conflict("The function could not be invoked because it is in the Failed state: %s", cfg.StateReason)
	}
	wait := o.Wait
	if wait == 0 {
		wait = syncQueueWait
	}
	dims := map[string]string{"FunctionName": name}
	e, err := s.acquire(ctx, cfg, limitFor(head), wait)
	if err != nil {
		if ce, ok := err.(*core.Error); ok && ce.Status == http.StatusTooManyRequests {
			s.metric("Throttles", dims, "Count", 1)
		}
		return nil, err
	}
	s.metric("ConcurrentExecutions", dims, "Count", float64(s.concurrent(name)))
	cold := !e.used
	reqID := uuid()
	mark := e.logs.mark()
	start := time.Now()
	tctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second+30*time.Second)
	body, perr := s.post(tctx, e, reqID, o.ClientContext, payload)
	cancel()
	wall := time.Since(start)
	lines := e.logs.until(mark, "REPORT RequestId: "+reqID, 2*time.Second)
	if perr != nil {
		// The emulator is gone or mid-invocation: never reuse this environment.
		s.mu.Lock()
		e.dead = true
		s.mu.Unlock()
	}
	s.release(e)
	res := &InvokeResult{RequestID: reqID, StatusCode: 200, ColdStart: cold, ExecutedVersion: cfg.version()}
	timedOut := false
	switch {
	case perr != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case perr != nil:
		log.Printf("lambda: invoke %s: %v", name, perr)
		res.FunctionError = "Unhandled"
		res.body, _ = json.Marshal(map[string]string{"errorType": "Runtime.ExitError",
			"errorMessage": fmt.Sprintf("RequestId: %s Error: Runtime exited without providing a reason", reqID)})
	default:
		res.body, res.FunctionError, timedOut = classify(body, reqID)
	}
	if len(res.body) > maxSyncPayload {
		res.FunctionError = "Unhandled"
		res.body, _ = json.Marshal(map[string]string{"errorType": "Function.ResponseSizeTooLarge",
			"errorMessage": fmt.Sprintf("Response payload size exceeded maximum allowed payload size (%d bytes).", maxSyncPayload)})
	}
	if json.Valid(res.body) {
		res.Payload = res.body
	} else {
		res.Payload, _ = json.Marshal(string(res.body))
	}

	// Logs: the invocation's lines without the emulator's diagnostics, with
	// AWS's timeout line.
	events := make([]cloudwatch.LogEvent, 0, len(lines)+1)
	var text strings.Builder
	for _, l := range lines {
		if isEmulatorLine(l.text) {
			continue
		}
		if timedOut && strings.HasPrefix(l.text, "END RequestId: "+reqID) {
			msg := fmt.Sprintf("%s %s Task timed out after %d.00 seconds", l.t.UTC().Format("2006-01-02T15:04:05.000Z"), reqID, cfg.TimeoutSec)
			events = append(events, cloudwatch.LogEvent{Timestamp: l.t, Message: msg})
			text.WriteString(msg + "\n")
		}
		if d, b, ok := parseReport(l.text); ok && strings.Contains(l.text, reqID) {
			res.DurationMS, res.BilledMS = d, b
		}
		events = append(events, cloudwatch.LogEvent{Timestamp: l.t, Message: l.text})
		text.WriteString(l.text + "\n")
	}
	if res.DurationMS == 0 {
		res.DurationMS = float64(wall.Microseconds()) / 1000
		res.BilledMS = int64(math.Ceil(res.DurationMS))
	}
	if len(events) > 0 && s.cw != nil {
		if err := s.cw.Append(cfg.LogGroup, e.stream, events...); err != nil {
			log.Printf("lambda: write logs: %v", err)
		}
	}
	res.Logs = tail(text.String(), 4096)
	s.metric("Invocations", dims, "Count", 1)
	s.metric("Duration", dims, "Milliseconds", res.DurationMS)
	errs := 0.0
	if res.FunctionError != "" {
		errs = 1
	}
	s.metric("Errors", dims, "Count", errs)
	return res, nil
}

func (s *Service) metric(name string, dims map[string]string, unit string, v float64) {
	if s.cw != nil {
		s.cw.Put(metricsNS, name, dims, unit, v, time.Time{})
	}
}

var reportRe = regexp.MustCompile(`\tDuration: ([0-9.]+) ms\tBilled Duration: ([0-9]+) ms`)

// parseReport reads the durations from a REPORT log line.
func parseReport(line string) (float64, int64, bool) {
	if !strings.HasPrefix(line, "REPORT RequestId: ") {
		return 0, 0, false
	}
	m := reportRe.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, false
	}
	d, _ := strconv.ParseFloat(m[1], 64)
	b, _ := strconv.ParseInt(m[2], 10, 64)
	return d, b, true
}

// classify interprets the emulator's response: function errors come back as
// 200 with an {"errorMessage","errorType",...} body, timeouts as plain text.
func classify(body []byte, reqID string) (payload []byte, functionError string, timedOut bool) {
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(trimmed, []byte("Task timed out after ")) {
		msg := fmt.Sprintf("%s %s %s", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), reqID, string(trimmed))
		b, _ := json.Marshal(map[string]string{"errorMessage": msg})
		return b, "Unhandled", true
	}
	if len(trimmed) == 0 {
		return []byte("null"), "", false
	}
	if isErrorShape(trimmed) {
		return body, "Unhandled", false
	}
	return body, "", false
}

// isErrorShape reports whether a response is a runtime error object.
func isErrorShape(b []byte) bool {
	if len(b) == 0 || b[0] != '{' {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	if _, ok := m["errorMessage"]; !ok {
		return false
	}
	if _, ok := m["errorType"]; !ok {
		return false
	}
	for k := range m {
		switch k {
		case "errorMessage", "errorType", "requestId", "stackTrace", "trace", "cause":
		default:
			return false
		}
	}
	return true
}
