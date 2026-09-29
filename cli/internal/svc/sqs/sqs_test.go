package sqs

import (
	"context"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

func newQueue(t *testing.T, s *Service, q Queue) {
	t.Helper()
	q.ARN, q.MaxMessageSize, q.MessageRetention = s.env.ARN("sqs", q.Name), maxBodyBytes, 3600
	if err := store.Put(s.env.Store, cQueues, q.Name, q); err != nil {
		t.Fatal(err)
	}
	s.queues[q.Name] = s.load(q.Name)
}

func ptr(i int) *int { return &i }

func TestVisibilityAndDelete(t *testing.T) {
	s := New(svctest.Env(t))
	newQueue(t, s, Queue{Name: "q", VisibilityTimeout: 30})
	if _, err := s.Send("q", SendInput{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ReceiveWait(context.Background(), "q", 10, ptr(1), ptr(0))
	if len(got) != 1 || got[0].Body != "hello" {
		t.Fatalf("receive %v", got)
	}
	if again, _ := s.ReceiveWait(context.Background(), "q", 10, nil, ptr(0)); len(again) != 0 {
		t.Fatal("in-flight message was delivered twice")
	}
	time.Sleep(1100 * time.Millisecond)
	back, _ := s.ReceiveWait(context.Background(), "q", 10, nil, ptr(0))
	if len(back) != 1 || back[0].Attributes["ApproximateReceiveCount"] != "2" {
		t.Fatalf("message did not become visible again: %v", back)
	}
	if err := s.Delete("q", got[0].ReceiptHandle); err == nil {
		t.Fatal("a stale receipt handle deleted the message")
	}
	if err := s.Delete("q", back[0].ReceiptHandle); err != nil {
		t.Fatal(err)
	}
}

func TestDeadLetterQueue(t *testing.T) {
	s := New(svctest.Env(t))
	newQueue(t, s, Queue{Name: "dlq"})
	newQueue(t, s, Queue{Name: "q", Redrive: &Redrive{DeadLetterQueue: "dlq", MaxReceiveCount: 2}})
	_, _ = s.Send("q", SendInput{Body: "poison"})
	for i := 0; i < 2; i++ {
		if got, _ := s.ReceiveWait(context.Background(), "q", 1, ptr(0), ptr(0)); len(got) != 1 {
			t.Fatalf("receive %d got %d messages", i, len(got))
		}
	}
	if got, _ := s.ReceiveWait(context.Background(), "q", 1, ptr(0), ptr(0)); len(got) != 0 {
		t.Fatal("message exceeded maxReceiveCount but was delivered again")
	}
	moved, _ := s.ReceiveWait(context.Background(), "dlq", 1, ptr(0), ptr(0))
	if len(moved) != 1 || moved[0].Body != "poison" {
		t.Fatalf("message not in DLQ: %v", moved)
	}
}

func TestFIFO(t *testing.T) {
	s := New(svctest.Env(t))
	newQueue(t, s, Queue{Name: "o.fifo", FIFO: true, ContentBasedDeduplication: true, VisibilityTimeout: 30})
	for _, b := range []string{"a", "b", "a"} {
		_, _ = s.Send("o.fifo", SendInput{Body: b, GroupID: "g"})
	}
	_, _ = s.Send("o.fifo", SendInput{Body: "x", GroupID: "h"})
	got, _ := s.ReceiveWait(context.Background(), "o.fifo", 10, nil, ptr(0))
	if len(got) != 3 || got[0].Body != "a" || got[1].Body != "b" || got[2].Body != "x" {
		t.Fatalf("FIFO order/dedup wrong: %v", got)
	}
	_, _ = s.Send("o.fifo", SendInput{Body: "c", GroupID: "g", DedupID: "c1"})
	if more, _ := s.ReceiveWait(context.Background(), "o.fifo", 10, nil, ptr(0)); len(more) != 0 {
		t.Fatal("group g was delivered while its earlier messages are in flight")
	}
	if _, err := s.Send("o.fifo", SendInput{Body: "no group"}); err == nil {
		t.Fatal("FIFO send without group accepted")
	}
}

func TestLongPollWakesOnSend(t *testing.T) {
	s := New(svctest.Env(t))
	newQueue(t, s, Queue{Name: "q", VisibilityTimeout: 30})
	go func() { time.Sleep(200 * time.Millisecond); _, _ = s.Send("q", SendInput{Body: "late"}) }()
	start := time.Now()
	got, _ := s.ReceiveWait(context.Background(), "q", 1, nil, ptr(5))
	if len(got) != 1 || time.Since(start) > 2*time.Second {
		t.Fatalf("long poll returned %v after %v", got, time.Since(start))
	}
}
