package lambda

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const cMappings = "lambda_event_source_mappings"

// QueueMessage is what an event source hands to a function.
type QueueMessage struct {
	ID                string            `json:"id"`
	ReceiptHandle     string            `json:"receipt_handle"`
	Body              string            `json:"body"`
	Attributes        map[string]string `json:"attributes"`
	MessageAttributes map[string]any    `json:"message_attributes"`
	MD5OfBody         string            `json:"md5_of_body"`
}

// QueueSource is implemented by the SQS service.
type QueueSource interface {
	Receive(queue string, max int, visibility time.Duration) ([]QueueMessage, error)
	Delete(queue, receipt string) error
	QueueARN(queue string) (string, bool)
}

// Mapping connects a queue to a function: messages are delivered in batches
// and deleted when the function succeeds (or per item with batchItemFailures).
type Mapping struct {
	ID                    string     `json:"id"`
	FunctionName          string     `json:"function_name"` // name, or name:qualifier
	QueueName             string     `json:"queue_name"`
	EventSourceARN        string     `json:"event_source_arn"`
	BatchSize             int        `json:"batch_size"`
	BatchingWindowSeconds int        `json:"batching_window_seconds,omitempty"`
	FunctionResponseTypes []string   `json:"function_response_types,omitempty"`
	Enabled               bool       `json:"enabled"`
	LastProcessingResult  string     `json:"last_processing_result"`
	LastInvokedAt         *time.Time `json:"last_invoked_at,omitempty"`
	LastModified          *time.Time `json:"last_modified,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	// DynamoDB stream mappings (EventSourceARN is a stream ARN; QueueName is empty).
	StartingPosition     string `json:"starting_position,omitempty"` // TRIM_HORIZON | LATEST
	Checkpoint           string `json:"checkpoint,omitempty"`        // stream position after the last processed batch
	MaximumRetryAttempts *int   `json:"maximum_retry_attempts,omitempty"`
	BisectOnError        bool   `json:"bisect_batch_on_function_error,omitempty"`
	OnFailure            string `json:"on_failure,omitempty"` // destination for discarded batches
}

func (m Mapping) isStream() bool { return isStreamARN(m.EventSourceARN) }

func (s *Service) esmRoutes(r *httpx.Router) {
	r.Handle("GET /api/v1/lambda/event-source-mappings", "lambda:ListEventSourceMappings", s.listMappings)
	r.Handle("POST /api/v1/lambda/event-source-mappings", "lambda:CreateEventSourceMapping", s.createMappingRoute)
	res := httpx.Res("arn:aws:lambda:{region}:{account}:event-source-mapping:{id}")
	r.Handle("PATCH /api/v1/lambda/event-source-mappings/{id}", "lambda:UpdateEventSourceMapping", s.updateMappingRoute, res)
	r.Handle("DELETE /api/v1/lambda/event-source-mappings/{id}", "lambda:DeleteEventSourceMapping", s.deleteMappingRoute, res)
}

func (s *Service) listMappings(c *httpx.Ctx) (any, error) {
	out := []Mapping{}
	for _, m := range store.List[Mapping](s.env.Store, cMappings) {
		if fn := c.Query("function"); fn == "" || m.FunctionName == fn {
			out = append(out, m)
		}
	}
	return out, nil
}

type mappingInput struct {
	FunctionName          string   `json:"function_name"`
	QueueName             string   `json:"queue_name"`
	BatchSize             int      `json:"batch_size"`
	BatchingWindowSeconds int      `json:"batching_window_seconds"`
	FunctionResponseTypes []string `json:"function_response_types"`
	Enabled               *bool    `json:"enabled"`
	// Streams.
	EventSourceARN       string `json:"event_source_arn"`
	StartingPosition     string `json:"starting_position"`
	MaximumRetryAttempts *int   `json:"maximum_retry_attempts"`
	BisectOnError        bool   `json:"bisect_batch_on_function_error"`
	OnFailure            string `json:"on_failure"`
}

func (s *Service) createMapping(authz authorizer, in mappingInput) (Mapping, error) {
	name, _ := parseRef(in.FunctionName)
	if !s.Exists(in.FunctionName) {
		return Mapping{}, fnNotFound(s.fnARN(name))
	}
	if err := authz("lambda:InvokeFunction", s.fnARN(name)); err != nil {
		return Mapping{}, err
	}
	if isStreamARN(in.EventSourceARN) {
		return s.createStreamMapping(authz, in)
	}
	if in.QueueName == "" && in.EventSourceARN != "" {
		qn, ok := queueName(in.EventSourceARN)
		if !ok {
			return Mapping{}, core.BadRequest("Unsupported event source %q: HomeCloud supports SQS queues and DynamoDB streams", in.EventSourceARN)
		}
		in.QueueName = qn
	}
	if s.Queues == nil {
		return Mapping{}, core.Errf(http.StatusServiceUnavailable, "ServiceUnavailable", "queues are not available")
	}
	arn, ok := s.Queues.QueueARN(in.QueueName)
	if !ok {
		return Mapping{}, core.NotFound("queue", in.QueueName)
	}
	// The poller reads and deletes the queue's messages on the caller's behalf.
	for _, action := range []string{"sqs:ReceiveMessage", "sqs:DeleteMessage"} {
		if err := authz(action, arn); err != nil {
			return Mapping{}, err
		}
	}
	if in.BatchSize <= 0 {
		in.BatchSize = 10
	}
	if in.BatchSize > 10 {
		return Mapping{}, core.BadRequest("BatchSize may not exceed 10 for SQS event sources in HomeCloud")
	}
	if in.BatchingWindowSeconds < 0 || in.BatchingWindowSeconds > 300 {
		return Mapping{}, core.BadRequest("MaximumBatchingWindowInSeconds must be between 0 and 300")
	}
	for _, t := range in.FunctionResponseTypes {
		if t != "ReportBatchItemFailures" {
			return Mapping{}, core.BadRequest("FunctionResponseTypes may only contain ReportBatchItemFailures")
		}
	}
	for _, m := range store.List[Mapping](s.env.Store, cMappings) {
		if m.EventSourceARN == arn && m.FunctionName == in.FunctionName {
			return Mapping{}, core.Conflict("The event source arn (%s) and function (%s) provided mapping already exists. Please update or delete the existing mapping with UUID %s", arn, in.FunctionName, m.ID)
		}
	}
	now := core.Now()
	m := Mapping{ID: uuid(), FunctionName: in.FunctionName, QueueName: in.QueueName, EventSourceARN: arn, BatchSize: in.BatchSize,
		BatchingWindowSeconds: in.BatchingWindowSeconds, FunctionResponseTypes: in.FunctionResponseTypes,
		Enabled: in.Enabled == nil || *in.Enabled, LastProcessingResult: "No records processed", CreatedAt: now, LastModified: &now}
	return m, store.Put(s.env.Store, cMappings, m.ID, m)
}

func (s *Service) createMappingRoute(c *httpx.Ctx) (any, error) {
	var in mappingInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.createMapping(c.Authorize, in)
}

type mappingUpdate struct {
	FunctionName          string    `json:"function_name"`
	Enabled               *bool     `json:"enabled"`
	BatchSize             int       `json:"batch_size"`
	BatchingWindowSeconds *int      `json:"batching_window_seconds"`
	FunctionResponseTypes *[]string `json:"function_response_types"`
}

func (s *Service) updateMapping(authz authorizer, id string, in mappingUpdate) (Mapping, error) {
	if in.FunctionName != "" {
		name, _ := parseRef(in.FunctionName)
		if !s.Exists(in.FunctionName) {
			return Mapping{}, fnNotFound(s.fnARN(name))
		}
		if err := authz("lambda:InvokeFunction", s.fnARN(name)); err != nil {
			return Mapping{}, err
		}
	}
	if in.BatchSize < 0 || in.BatchSize > 10 {
		return Mapping{}, core.BadRequest("BatchSize must be between 1 and 10")
	}
	m, err := store.Update(s.env.Store, cMappings, id, func(m *Mapping) error {
		if in.FunctionName != "" {
			m.FunctionName = in.FunctionName
		}
		if in.Enabled != nil {
			m.Enabled = *in.Enabled
		}
		if in.BatchSize > 0 {
			m.BatchSize = in.BatchSize
		}
		if in.BatchingWindowSeconds != nil {
			m.BatchingWindowSeconds = *in.BatchingWindowSeconds
		}
		if in.FunctionResponseTypes != nil {
			m.FunctionResponseTypes = *in.FunctionResponseTypes
		}
		n := core.Now()
		m.LastModified = &n
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return m, mappingNotFound(id)
	}
	return m, err
}

func mappingNotFound(id string) error {
	return core.Errf(http.StatusNotFound, "ResourceNotFound", "The resource you requested does not exist. (Event source mapping %s)", id)
}

func (s *Service) updateMappingRoute(c *httpx.Ctx) (any, error) {
	var in mappingUpdate
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.updateMapping(c.Authorize, c.Param("id"), in)
}

func (s *Service) deleteMapping(id string) (Mapping, error) {
	m, err := store.Get[Mapping](s.env.Store, cMappings, id)
	if err != nil {
		return m, mappingNotFound(id)
	}
	streamStates.Delete(id)
	return m, store.Delete(s.env.Store, cMappings, id)
}

func (s *Service) deleteMappingRoute(c *httpx.Ctx) (any, error) {
	_, err := s.deleteMapping(c.Param("id"))
	return nil, err
}

// ---- AWS API ----

func (s *Service) mappingARN(id string) string {
	return s.env.ARN("lambda", "event-source-mapping:"+id)
}

func (s *Service) mappingOut(m Mapping, state string) map[string]any {
	if state == "" {
		state = "Disabled"
		if m.Enabled {
			state = "Enabled"
		}
	}
	name, qual := parseRef(m.FunctionName)
	mod := m.CreatedAt
	if m.LastModified != nil {
		mod = *m.LastModified
	}
	out := map[string]any{"UUID": m.ID, "BatchSize": m.BatchSize, "MaximumBatchingWindowInSeconds": m.BatchingWindowSeconds,
		"EventSourceArn": m.EventSourceARN, "FunctionArn": Function{ARN: s.fnARN(name)}.qualifiedARN(qual),
		"LastModified": awsapi.Epoch(mod), "LastProcessingResult": m.LastProcessingResult, "State": state,
		"StateTransitionReason": "USER_INITIATED", "EventSourceMappingArn": s.mappingARN(m.ID)}
	if len(m.FunctionResponseTypes) > 0 {
		out["FunctionResponseTypes"] = m.FunctionResponseTypes
	}
	if m.isStream() {
		retries := -1
		if m.MaximumRetryAttempts != nil {
			retries = *m.MaximumRetryAttempts
		}
		out["StartingPosition"], out["MaximumRetryAttempts"], out["BisectBatchOnFunctionError"] = m.StartingPosition, retries, m.BisectOnError
		out["ParallelizationFactor"], out["MaximumRecordAgeInSeconds"] = 1, -1
		dc := map[string]any{}
		if m.OnFailure != "" {
			dc["OnFailure"] = map[string]string{"Destination": m.OnFailure}
		}
		out["DestinationConfig"] = dc
	}
	return out
}

type awsMappingIn struct {
	EventSourceArn                 string    `json:"EventSourceArn"`
	FunctionName                   string    `json:"FunctionName"`
	Enabled                        *bool     `json:"Enabled"`
	BatchSize                      int       `json:"BatchSize"`
	MaximumBatchingWindowInSeconds *int      `json:"MaximumBatchingWindowInSeconds"`
	FunctionResponseTypes          *[]string `json:"FunctionResponseTypes"`
	StartingPosition               string    `json:"StartingPosition"`
	MaximumRetryAttempts           *int      `json:"MaximumRetryAttempts"`
	BisectBatchOnFunctionError     bool      `json:"BisectBatchOnFunctionError"`
	DestinationConfig              *struct {
		OnFailure *struct{ Destination string } `json:"OnFailure"`
	} `json:"DestinationConfig"`
}

// queueName extracts the queue name from an SQS ARN.
func queueName(arn string) (string, bool) {
	arn = core.CanonicalARN(arn)
	if arnService(arn) != "sqs" {
		return "", false
	}
	return arn[strings.LastIndexByte(arn, ':')+1:], true
}

// fnRef normalises a function reference to "name" or "name:qualifier".
func fnRef(ref string) string {
	fn, qual := parseRef(ref)
	if qual != "" {
		return fn + ":" + qual
	}
	return fn
}

func (s *Service) awsCreateMapping(q *awsapi.Req, _ map[string]string) error {
	var in awsMappingIn
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:CreateEventSourceMapping", "*"); err != nil {
		return err
	}
	mi := mappingInput{FunctionName: fnRef(in.FunctionName), EventSourceARN: in.EventSourceArn, BatchSize: in.BatchSize, Enabled: in.Enabled,
		StartingPosition: in.StartingPosition, MaximumRetryAttempts: in.MaximumRetryAttempts, BisectOnError: in.BisectBatchOnFunctionError}
	if in.DestinationConfig != nil && in.DestinationConfig.OnFailure != nil {
		mi.OnFailure = in.DestinationConfig.OnFailure.Destination
	}
	if !isStreamARN(in.EventSourceArn) {
		qn, ok := queueName(in.EventSourceArn)
		if !ok {
			return core.BadRequest("Unsupported event source %q: HomeCloud supports SQS queues and DynamoDB streams", in.EventSourceArn)
		}
		mi.QueueName = qn
	}
	if in.MaximumBatchingWindowInSeconds != nil {
		mi.BatchingWindowSeconds = *in.MaximumBatchingWindowInSeconds
	}
	if in.FunctionResponseTypes != nil {
		mi.FunctionResponseTypes = *in.FunctionResponseTypes
	}
	m, err := s.createMapping(q.Authorize, mi)
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusAccepted, s.mappingOut(m, "Creating"))
	return nil
}

func (s *Service) awsListMappings(q *awsapi.Req, _ map[string]string) error {
	if err := q.Authorize("lambda:ListEventSourceMappings", "*"); err != nil {
		return err
	}
	qs := q.R.URL.Query()
	fn, _ := parseRef(qs.Get("FunctionName"))
	var ms []Mapping
	for _, m := range store.List[Mapping](s.env.Store, cMappings) {
		mf, _ := parseRef(m.FunctionName)
		if (fn == "" || mf == fn) && (qs.Get("EventSourceArn") == "" || m.EventSourceARN == qs.Get("EventSourceArn")) {
			ms = append(ms, m)
		}
	}
	start, end, next := paginate(q, len(ms))
	out := []map[string]any{}
	for _, m := range ms[start:end] {
		out = append(out, s.mappingOut(m, ""))
	}
	q.WriteJSON(http.StatusOK, withMarker(map[string]any{"EventSourceMappings": out}, next))
	return nil
}

func (s *Service) awsGetMapping(q *awsapi.Req, p map[string]string) error {
	if err := q.Authorize("lambda:GetEventSourceMapping", s.mappingARN(p["id"])); err != nil {
		return err
	}
	m, err := store.Get[Mapping](s.env.Store, cMappings, p["id"])
	if err != nil {
		return mappingNotFound(p["id"])
	}
	q.WriteJSON(http.StatusOK, s.mappingOut(m, ""))
	return nil
}

func (s *Service) awsUpdateMapping(q *awsapi.Req, p map[string]string) error {
	var in awsMappingIn
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:UpdateEventSourceMapping", s.mappingARN(p["id"])); err != nil {
		return err
	}
	up := mappingUpdate{Enabled: in.Enabled, BatchSize: in.BatchSize, BatchingWindowSeconds: in.MaximumBatchingWindowInSeconds,
		FunctionResponseTypes: in.FunctionResponseTypes}
	if in.FunctionName != "" {
		up.FunctionName = fnRef(in.FunctionName)
	}
	m, err := s.updateMapping(q.Authorize, p["id"], up)
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusAccepted, s.mappingOut(m, ""))
	return nil
}

func (s *Service) awsDeleteMapping(q *awsapi.Req, p map[string]string) error {
	if err := q.Authorize("lambda:DeleteEventSourceMapping", s.mappingARN(p["id"])); err != nil {
		return err
	}
	m, err := s.deleteMapping(p["id"])
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusAccepted, s.mappingOut(m, "Deleting"))
	return nil
}

// PollQueues delivers queued messages to mapped functions until ctx is done.
func (s *Service) PollQueues(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	inflight := map[string]bool{}
	done := make(chan string, 64)
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-done:
			delete(inflight, id)
			continue
		case <-t.C:
		}
		for _, m := range store.List[Mapping](s.env.Store, cMappings) {
			if !m.Enabled || inflight[m.ID] {
				continue
			}
			if m.isStream() {
				if s.Streams == nil {
					continue
				}
				inflight[m.ID] = true
				go func(m Mapping) {
					defer func() { done <- m.ID }()
					defer core.Recover("lambda stream delivery " + m.FunctionName)
					s.pollStream(ctx, m)
				}(m)
				continue
			}
			if s.Queues == nil {
				continue
			}
			fn, _ := parseRef(m.FunctionName)
			f, err := s.getLatest(fn)
			if err != nil {
				continue
			}
			msgs, err := s.Queues.Receive(m.QueueName, m.BatchSize, time.Duration(f.TimeoutSec*6)*time.Second)
			if err != nil || len(msgs) == 0 {
				continue
			}
			inflight[m.ID] = true
			go func(m Mapping, msgs []QueueMessage) {
				defer func() { done <- m.ID }()
				defer core.Recover("lambda queue delivery " + m.FunctionName)
				s.deliverBatch(ctx, m, msgs)
			}(m, msgs)
		}
	}
}

func (s *Service) deliverBatch(ctx context.Context, m Mapping, msgs []QueueMessage) {
	records := make([]map[string]any, 0, len(msgs))
	for _, q := range msgs {
		records = append(records, map[string]any{
			"messageId": q.ID, "receiptHandle": q.ReceiptHandle, "body": q.Body, "attributes": q.Attributes,
			"messageAttributes": q.MessageAttributes, "md5OfBody": q.MD5OfBody, "eventSource": "aws:sqs",
			"eventSourceARN": m.EventSourceARN, "awsRegion": s.env.Cfg.Region,
		})
	}
	payload, _ := json.Marshal(map[string]any{"Records": records})
	res, err := s.InvokeWith(ctx, m.FunctionName, payload, InvokeOptions{Wait: 30 * time.Second})
	result := "OK"
	failed := map[string]bool{}
	switch {
	case err != nil:
		result = "PROBLEM: " + err.Error()
		for _, q := range msgs {
			failed[q.ID] = true
		}
	case res.FunctionError != "":
		result = "PROBLEM: function error"
		for _, q := range msgs {
			failed[q.ID] = true
		}
	default:
		var partial struct {
			BatchItemFailures []struct {
				ItemIdentifier string `json:"itemIdentifier"`
			} `json:"batchItemFailures"`
		}
		known := map[string]bool{}
		for _, q := range msgs {
			known[q.ID] = true
		}
		if json.Unmarshal(res.Payload, &partial) == nil {
			for _, f := range partial.BatchItemFailures {
				if !known[f.ItemIdentifier] {
					// An unknown or empty identifier fails the whole batch, as in AWS.
					result = "PROBLEM: batchItemFailures names an unknown message"
					for _, q := range msgs {
						failed[q.ID] = true
					}
					break
				}
				failed[f.ItemIdentifier] = true
			}
		}
	}
	for _, q := range msgs {
		if !failed[q.ID] {
			if err := s.Queues.Delete(m.QueueName, q.ReceiptHandle); err != nil {
				log.Printf("lambda: delete message from %s: %v", m.QueueName, err)
			}
		}
	}
	_, _ = store.Update(s.env.Store, cMappings, m.ID, func(x *Mapping) error {
		n := core.Now()
		x.LastProcessingResult, x.LastInvokedAt = result, &n
		return nil
	})
}
