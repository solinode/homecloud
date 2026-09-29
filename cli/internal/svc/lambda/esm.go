package lambda

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

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
	ID                   string     `json:"id"`
	FunctionName         string     `json:"function_name"`
	QueueName            string     `json:"queue_name"`
	EventSourceARN       string     `json:"event_source_arn"`
	BatchSize            int        `json:"batch_size"`
	Enabled              bool       `json:"enabled"`
	LastProcessingResult string     `json:"last_processing_result"`
	LastInvokedAt        *time.Time `json:"last_invoked_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}

func (s *Service) esmRoutes(r *httpx.Router) {
	r.Handle("GET /api/v1/lambda/event-source-mappings", "lambda:ListEventSourceMappings", s.listMappings)
	r.Handle("POST /api/v1/lambda/event-source-mappings", "lambda:CreateEventSourceMapping", s.createMapping)
	r.Handle("PATCH /api/v1/lambda/event-source-mappings/{id}", "lambda:UpdateEventSourceMapping", s.updateMapping)
	r.Handle("DELETE /api/v1/lambda/event-source-mappings/{id}", "lambda:DeleteEventSourceMapping", s.deleteMapping)
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

func (s *Service) createMapping(c *httpx.Ctx) (any, error) {
	var in struct {
		FunctionName string `json:"function_name"`
		QueueName    string `json:"queue_name"`
		BatchSize    int    `json:"batch_size"`
		Enabled      *bool  `json:"enabled"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !s.Exists(in.FunctionName) {
		return nil, core.NotFound("function", in.FunctionName)
	}
	if s.Queues == nil {
		return nil, core.Errf(http.StatusServiceUnavailable, "ServiceUnavailable", "queues are not available")
	}
	arn, ok := s.Queues.QueueARN(in.QueueName)
	if !ok {
		return nil, core.NotFound("queue", in.QueueName)
	}
	if in.BatchSize <= 0 {
		in.BatchSize = 10
	}
	if in.BatchSize > 100 {
		return nil, core.BadRequest("batch_size may not exceed 100")
	}
	m := Mapping{ID: uuid(), FunctionName: in.FunctionName, QueueName: in.QueueName, EventSourceARN: arn, BatchSize: in.BatchSize,
		Enabled: in.Enabled == nil || *in.Enabled, LastProcessingResult: "No records processed", CreatedAt: core.Now()}
	return m, store.Put(s.env.Store, cMappings, m.ID, m)
}

func (s *Service) updateMapping(c *httpx.Ctx) (any, error) {
	var in struct {
		Enabled   *bool `json:"enabled"`
		BatchSize int   `json:"batch_size"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	m, err := store.Update(s.env.Store, cMappings, c.Param("id"), func(m *Mapping) error {
		if in.Enabled != nil {
			m.Enabled = *in.Enabled
		}
		if in.BatchSize > 0 && in.BatchSize <= 100 {
			m.BatchSize = in.BatchSize
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("event source mapping", c.Param("id"))
	}
	return m, err
}

func (s *Service) deleteMapping(c *httpx.Ctx) (any, error) {
	if err := store.Delete(s.env.Store, cMappings, c.Param("id")); err != nil {
		return nil, core.NotFound("event source mapping", c.Param("id"))
	}
	return nil, nil
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
		if s.Queues == nil {
			continue
		}
		for _, m := range store.List[Mapping](s.env.Store, cMappings) {
			if !m.Enabled || inflight[m.ID] {
				continue
			}
			f, err := store.Get[Function](s.env.Store, cFunctions, m.FunctionName)
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
				s.deliver(ctx, m, msgs)
			}(m, msgs)
		}
	}
}

func (s *Service) deliver(ctx context.Context, m Mapping, msgs []QueueMessage) {
	records := make([]map[string]any, 0, len(msgs))
	for _, q := range msgs {
		records = append(records, map[string]any{
			"messageId": q.ID, "receiptHandle": q.ReceiptHandle, "body": q.Body, "attributes": q.Attributes,
			"messageAttributes": q.MessageAttributes, "md5OfBody": q.MD5OfBody, "eventSource": "aws:sqs",
			"eventSourceARN": m.EventSourceARN, "awsRegion": s.env.Cfg.Region,
		})
	}
	payload, _ := json.Marshal(map[string]any{"Records": records})
	res, err := s.Invoke(ctx, m.FunctionName, payload)
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
		if json.Unmarshal(res.Payload, &partial) == nil {
			for _, f := range partial.BatchItemFailures {
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
