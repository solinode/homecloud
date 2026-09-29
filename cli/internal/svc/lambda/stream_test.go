package lambda

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

// fakeStream serves records by position; checkpoints are record offsets.
type fakeStream struct {
	mu      sync.Mutex
	records []map[string]any
}

func (f *fakeStream) ReadStream(arn, cp string, limit int) ([]map[string]any, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := 0
	if cp == "LATEST" {
		start = len(f.records)
	} else if cp != "" {
		start, _ = strconv.Atoi(cp)
	}
	end := min(start+limit, len(f.records))
	return f.records[start:end], strconv.Itoa(end), nil
}

func (f *fakeStream) add(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < n; i++ {
		seq := strconv.Itoa(len(f.records) + 1)
		f.records = append(f.records, map[string]any{"eventName": "INSERT", "dynamodb": map[string]any{"SequenceNumber": seq},
			"userIdentity": map[string]any{"Type": "Service", "PrincipalId": "dynamodb.amazonaws.com"}})
	}
}

func TestStreamMapping(t *testing.T) {
	env := svctest.Env(t)
	s := New(env, nil, nil)
	fs := &fakeStream{}
	fs.add(3)
	s.Streams = fs
	if err := store.Put(env.Store, cFunctions, "fn", Function{Name: "fn", ARN: s.fnARN("fn"), Version: latest, State: "Active"}); err != nil {
		t.Fatal(err)
	}
	var calls [][]map[string]any
	fail := 1
	s.invokeHook = func(ctx context.Context, ref string, payload []byte, o InvokeOptions) (*InvokeResult, error) {
		var ev struct{ Records []map[string]any }
		_ = json.Unmarshal(payload, &ev)
		calls = append(calls, ev.Records)
		if fail > 0 {
			fail--
			return &InvokeResult{FunctionError: "Unhandled", ExecutedVersion: latest}, nil
		}
		return &InvokeResult{ExecutedVersion: latest}, nil
	}
	arn := "arn:aws:dynamodb:us-east-1:123456789012:table/t/stream/2026-01-01T00:00:00.000"
	if _, err := s.createMapping(allowAll, mappingInput{FunctionName: "fn", EventSourceARN: arn}); err == nil {
		t.Fatal("StartingPosition is required for streams")
	}
	m, err := s.createMapping(allowAll, mappingInput{FunctionName: "fn", EventSourceARN: arn, StartingPosition: "TRIM_HORIZON", BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	poll := func() Mapping {
		if v, ok := streamStates.Load(m.ID); ok {
			v.(*streamState).notUntil = time.Time{}
		}
		cur, _ := store.Get[Mapping](env.Store, cMappings, m.ID)
		s.pollStream(context.Background(), cur)
		cur, _ = store.Get[Mapping](env.Store, cMappings, m.ID)
		return cur
	}
	// The first delivery fails: the checkpoint stays, and the same records are retried.
	if cur := poll(); cur.Checkpoint != "" {
		t.Fatalf("checkpoint advanced after a failure: %q", cur.Checkpoint)
	}
	if cur := poll(); cur.Checkpoint != "2" || cur.LastProcessingResult != "OK" {
		t.Fatalf("after success: %+v", cur)
	}
	if calls[0][0]["dynamodb"].(map[string]any)["SequenceNumber"] != "1" || calls[1][0]["dynamodb"].(map[string]any)["SequenceNumber"] != "1" {
		t.Fatalf("retry must redeliver the same batch: %v", calls)
	}
	r := calls[1][0]
	if r["eventSourceARN"] != arn || r["userIdentity"].(map[string]any)["principalId"] != "dynamodb.amazonaws.com" {
		t.Fatalf("record shape: %v", r)
	}
	if cur := poll(); cur.Checkpoint != "3" || len(calls[2]) != 1 {
		t.Fatalf("last record: %+v %v", cur, calls)
	}
	// LATEST starts after existing records; retries are bounded by MaximumRetryAttempts.
	zero := 0
	m2, err := s.createMapping(allowAll, mappingInput{FunctionName: "fn", EventSourceARN: arn + "x", StartingPosition: "LATEST", MaximumRetryAttempts: &zero})
	if err != nil || m2.Checkpoint != "3" {
		t.Fatalf("LATEST: %+v %v", m2, err)
	}
	fs.add(1)
	fail = 5
	m = m2
	if cur := poll(); cur.Checkpoint != "4" {
		t.Fatalf("batch must be discarded after MaximumRetryAttempts: %+v", cur)
	}
}
