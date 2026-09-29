package lambda

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// StreamSource reads DynamoDB streams (set from server.go). checkpoint "" is
// the oldest record (TRIM_HORIZON) and "LATEST" skips existing records; the
// returned checkpoint is the position after the returned records.
type StreamSource interface {
	ReadStream(arn, checkpoint string, limit int) ([]map[string]any, string, error)
}

func isStreamARN(arn string) bool {
	return arnService(arn) == "dynamodb" && strings.Contains(arn, "/stream/")
}

// streamState tracks retries of a stream mapping's current batch (in memory).
type streamState struct {
	retries  int
	batch    int // reduced batch size after BisectBatchOnFunctionError
	notUntil time.Time
}

var streamStates sync.Map // mapping ID -> *streamState

func (s *Service) createStreamMapping(authz authorizer, in mappingInput) (Mapping, error) {
	if s.Streams == nil {
		return Mapping{}, core.Errf(http.StatusServiceUnavailable, "ServiceUnavailable", "DynamoDB streams are not available")
	}
	arn := core.CanonicalARN(in.EventSourceARN)
	for _, action := range []string{"dynamodb:DescribeStream", "dynamodb:GetShardIterator", "dynamodb:GetRecords"} {
		if err := authz(action, arn); err != nil {
			return Mapping{}, err
		}
	}
	checkpoint := ""
	switch in.StartingPosition {
	case "TRIM_HORIZON":
	case "LATEST":
		// Start after the records that exist now.
		_, cp, err := s.Streams.ReadStream(arn, "LATEST", 1)
		if err != nil {
			return Mapping{}, core.BadRequest("Stream %s cannot be read: %v", arn, err)
		}
		checkpoint = cp
	case "":
		return Mapping{}, core.BadRequest("StartingPosition is required for DynamoDB stream event sources (TRIM_HORIZON or LATEST)")
	default:
		return Mapping{}, core.BadRequest("StartingPosition must be TRIM_HORIZON or LATEST")
	}
	if checkpoint == "" {
		if _, _, err := s.Streams.ReadStream(arn, "", 1); err != nil {
			return Mapping{}, core.BadRequest("Stream %s cannot be read: %v", arn, err)
		}
	}
	if in.BatchSize <= 0 {
		in.BatchSize = 100
	}
	if in.BatchSize > 1000 {
		return Mapping{}, core.BadRequest("BatchSize must be between 1 and 1000 for DynamoDB streams in HomeCloud")
	}
	if r := in.MaximumRetryAttempts; r != nil {
		if *r < -1 || *r > 10000 {
			return Mapping{}, core.BadRequest("MaximumRetryAttempts must be between -1 and 10000")
		}
		if *r == -1 {
			in.MaximumRetryAttempts = nil
		}
	}
	if in.OnFailure != "" {
		if svc := arnService(in.OnFailure); svc != "sqs" && svc != "sns" {
			return Mapping{}, core.BadRequest("OnFailure destinations of stream event sources must be SQS queues or SNS topics")
		}
		if err := s.checkTarget(authz, in.OnFailure, false); err != nil {
			return Mapping{}, err
		}
	}
	for _, m := range store.List[Mapping](s.env.Store, cMappings) {
		if m.EventSourceARN == arn && m.FunctionName == in.FunctionName {
			return Mapping{}, core.Conflict("The event source arn (%s) and function (%s) provided mapping already exists. Please update or delete the existing mapping with UUID %s", arn, in.FunctionName, m.ID)
		}
	}
	now := core.Now()
	m := Mapping{ID: uuid(), FunctionName: in.FunctionName, EventSourceARN: arn, BatchSize: in.BatchSize,
		Enabled: in.Enabled == nil || *in.Enabled, LastProcessingResult: "No records processed", CreatedAt: now, LastModified: &now,
		StartingPosition: in.StartingPosition, Checkpoint: checkpoint, MaximumRetryAttempts: in.MaximumRetryAttempts,
		BisectOnError: in.BisectOnError, OnFailure: in.OnFailure}
	return m, store.Put(s.env.Store, cMappings, m.ID, m)
}

// lambdaRecord converts a DynamoDB Streams record to the shape Lambda passes to functions.
func lambdaRecord(r map[string]any, streamARN string) map[string]any {
	out := make(map[string]any, len(r)+1)
	for k, v := range r {
		out[k] = v
	}
	out["eventSourceARN"] = streamARN
	if _, ok := out["eventSource"]; !ok {
		out["eventSource"] = "aws:dynamodb"
	}
	if ui, ok := r["userIdentity"].(map[string]any); ok {
		nu := make(map[string]any, len(ui))
		for k, v := range ui {
			switch k {
			case "Type":
				nu["type"] = v
			case "PrincipalId":
				nu["principalId"] = v
			default:
				nu[k] = v
			}
		}
		out["userIdentity"] = nu
	}
	return out
}

func (s *Service) invokeWith(ctx context.Context, ref string, payload []byte, o InvokeOptions) (*InvokeResult, error) {
	if s.invokeHook != nil {
		return s.invokeHook(ctx, ref, payload, o)
	}
	return s.InvokeWith(ctx, ref, payload, o)
}

func (s *Service) setMappingResult(id, result, checkpoint string, advance bool) {
	_, _ = store.Update(s.env.Store, cMappings, id, func(x *Mapping) error {
		n := core.Now()
		x.LastProcessingResult, x.LastInvokedAt = result, &n
		if advance {
			x.Checkpoint = checkpoint
		}
		return nil
	})
}

// pollStream delivers the next batch of a stream mapping. The checkpoint only
// advances when the function succeeds (or the batch is discarded after
// MaximumRetryAttempts), so records are processed in order, at least once.
func (s *Service) pollStream(ctx context.Context, m Mapping) {
	v, _ := streamStates.LoadOrStore(m.ID, &streamState{})
	st := v.(*streamState)
	if time.Now().Before(st.notUntil) {
		return
	}
	batch := m.BatchSize
	if st.batch > 0 && st.batch < batch {
		batch = st.batch
	}
	records, next, err := s.Streams.ReadStream(m.EventSourceARN, m.Checkpoint, batch)
	if err != nil {
		s.setMappingResult(m.ID, "PROBLEM: "+err.Error(), "", false)
		st.notUntil = time.Now().Add(10 * time.Second)
		return
	}
	if len(records) == 0 {
		if next != "" && next != m.Checkpoint {
			_, _ = store.Update(s.env.Store, cMappings, m.ID, func(x *Mapping) error { x.Checkpoint = next; return nil })
		}
		return
	}
	recs := make([]map[string]any, len(records))
	for i, r := range records {
		recs[i] = lambdaRecord(r, m.EventSourceARN)
	}
	payload, _ := json.Marshal(map[string]any{"Records": recs})
	res, err := s.invokeWith(ctx, m.FunctionName, payload, InvokeOptions{Wait: 30 * time.Second})
	if err == nil && res.FunctionError == "" {
		s.setMappingResult(m.ID, "OK", next, true)
		*st = streamState{}
		return
	}
	st.retries++
	reason := "function error"
	if err != nil {
		reason = err.Error()
	}
	if m.BisectOnError && len(records) > 1 {
		st.batch = len(records) / 2
	}
	if m.MaximumRetryAttempts != nil && st.retries > *m.MaximumRetryAttempts {
		// Discard the batch (after sending its description to the failure destination).
		log.Printf("lambda: discarding %d stream records for %s after %d attempts", len(records), m.FunctionName, st.retries)
		if m.OnFailure != "" {
			s.streamFailure(ctx, m, records, res, st.retries)
		}
		s.setMappingResult(m.ID, fmt.Sprintf("PROBLEM: %s; batch discarded after %d attempts", reason, st.retries), next, true)
		*st = streamState{}
		return
	}
	s.setMappingResult(m.ID, "PROBLEM: "+reason, "", false)
	st.notUntil = time.Now().Add(min(time.Duration(1<<min(st.retries, 5))*time.Second, 30*time.Second))
}

// streamFailure sends an invocation record for a discarded stream batch.
func (s *Service) streamFailure(ctx context.Context, m Mapping, records []map[string]any, res *InvokeResult, attempts int) {
	seq := func(r map[string]any) any {
		if d, ok := r["dynamodb"].(map[string]any); ok {
			return d["SequenceNumber"]
		}
		return nil
	}
	name, qual := parseRef(m.FunctionName)
	rec := map[string]any{
		"version":   "1.0",
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"requestContext": map[string]any{"requestId": uuid(), "functionArn": Function{ARN: s.fnARN(name)}.qualifiedARN(qual),
			"condition": "RetryAttemptsExhausted", "approximateInvokeCount": attempts},
		"DDBStreamBatchInfo": map[string]any{"shardId": "", "startSequenceNumber": seq(records[0]), "endSequenceNumber": seq(records[len(records)-1]),
			"batchSize": len(records), "streamArn": m.EventSourceARN},
	}
	if res != nil {
		rec["responseContext"] = map[string]any{"statusCode": 200, "executedVersion": res.ExecutedVersion, "functionError": res.FunctionError}
	}
	b, _ := json.Marshal(rec)
	if err := s.deliver(ctx, m.OnFailure, b, "", nil); err != nil {
		log.Printf("lambda: stream failure destination %s: %v", m.OnFailure, err)
	}
}
