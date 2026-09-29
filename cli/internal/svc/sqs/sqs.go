// Package sqs implements message queues: standard and FIFO queues with
// visibility timeouts, delays, long polling, retention and dead-letter queues.
// Messages live in memory and are snapshotted to disk.
package sqs

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
)

const (
	cQueues      = "sqs_queues"
	maxBodyBytes = 256 << 10
	dedupWindow  = 5 * time.Minute
)

type Redrive struct {
	DeadLetterQueue string `json:"dead_letter_queue"`
	MaxReceiveCount int    `json:"max_receive_count"`
}

type Queue struct {
	Name                      string    `json:"name"`
	ARN                       string    `json:"arn"`
	URL                       string    `json:"url"`
	FIFO                      bool      `json:"fifo"`
	ContentBasedDeduplication bool      `json:"content_based_deduplication"`
	VisibilityTimeout         int       `json:"visibility_timeout"`
	MessageRetention          int       `json:"message_retention_seconds"`
	DelaySeconds              int       `json:"delay_seconds"`
	ReceiveWaitTime           int       `json:"receive_wait_time_seconds"`
	MaxMessageSize            int       `json:"max_message_size"`
	Redrive                   *Redrive  `json:"redrive_policy,omitempty"`
	CreatedAt                 time.Time `json:"created_at"`
	LastModified              time.Time `json:"last_modified"`
	Tags                      core.Tags `json:"tags,omitempty"`
}

type MessageAttribute struct {
	DataType    string `json:"data_type"`
	StringValue string `json:"string_value"`
}

type message struct {
	ID           string                      `json:"id"`
	Body         string                      `json:"body"`
	MD5          string                      `json:"md5"`
	Attrs        map[string]MessageAttribute `json:"attrs,omitempty"`
	SentAt       time.Time                   `json:"sent_at"`
	VisibleAt    time.Time                   `json:"visible_at"`
	FirstReceive time.Time                   `json:"first_receive,omitempty"`
	ReceiveCount int                         `json:"receive_count"`
	Receipt      string                      `json:"receipt,omitempty"`
	GroupID      string                      `json:"group_id,omitempty"`
	DedupID      string                      `json:"dedup_id,omitempty"`
	SequenceNo   int64                       `json:"sequence_number,omitempty"`
	SourceQueue  string                      `json:"source_queue,omitempty"` // set when moved to a DLQ
}

type queueState struct {
	msgs   []*message
	dedup  map[string]time.Time
	seq    int64
	notify chan struct{}
	stats  struct{ sent, received, deleted int64 }
}

type Service struct {
	env    *svc.Env
	mu     sync.Mutex
	queues map[string]*queueState
	dirty  bool
}

func New(env *svc.Env) *Service {
	s := &Service{env: env, queues: map[string]*queueState{}}
	for _, q := range store.List[Queue](env.Store, cQueues) {
		s.queues[q.Name] = s.load(q.Name)
	}
	return s
}

func (s *Service) file(name string) string { return s.env.Cfg.Path("sqs", name+".json") }

func (s *Service) load(name string) *queueState {
	st := &queueState{dedup: map[string]time.Time{}, notify: make(chan struct{})}
	b, err := os.ReadFile(s.file(name))
	if err == nil {
		var saved struct {
			Msgs []*message `json:"messages"`
			Seq  int64      `json:"seq"`
		}
		if json.Unmarshal(b, &saved) == nil {
			st.msgs, st.seq = saved.Msgs, saved.Seq
		}
	}
	return st
}

// persist snapshots every queue's messages; must be called without mu held.
func (s *Service) persist() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	s.dirty = false
	snaps := map[string][]byte{}
	for n, q := range s.queues {
		b, _ := json.Marshal(map[string]any{"messages": q.msgs, "seq": q.seq})
		snaps[n] = b
	}
	s.mu.Unlock()
	_ = os.MkdirAll(s.env.Cfg.Path("sqs"), 0o700)
	for n, b := range snaps {
		tmp := s.file(n) + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, s.file(n))
		}
	}
}

// Run persists queues periodically and expires old messages.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.persist()
			return
		case <-t.C:
		}
		s.expire()
		s.persist()
	}
}

func (s *Service) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, st := range s.queues {
		q, err := store.Get[Queue](s.env.Store, cQueues, name)
		if err != nil {
			continue
		}
		cut := time.Now().Add(-time.Duration(q.MessageRetention) * time.Second)
		kept := st.msgs[:0]
		for _, m := range st.msgs {
			if m.SentAt.After(cut) {
				kept = append(kept, m)
			} else {
				s.dirty = true
			}
		}
		st.msgs = kept
		for k, t := range st.dedup {
			if time.Since(t) > dedupWindow {
				delete(st.dedup, k)
			}
		}
	}
}

func (s *Service) queueURL(name string) string {
	_, port, _ := strings.Cut(s.env.Cfg.APIAddr, ":")
	return fmt.Sprintf("http://%s:%s/api/v1/sqs/queues/%s", s.env.Cfg.PublicHost, port, name)
}

func (s *Service) getQueue(name string) (Queue, error) {
	q, err := store.Get[Queue](s.env.Store, cQueues, name)
	if err != nil {
		return q, core.Errf(http.StatusNotFound, "QueueDoesNotExist", "queue %q does not exist", name)
	}
	return q, nil
}

// ---- core operations (also used by Lambda, SNS and EventBridge) ----

type SendInput struct {
	Body              string                      `json:"body"`
	DelaySeconds      *int                        `json:"delay_seconds"`
	MessageAttributes map[string]MessageAttribute `json:"message_attributes"`
	GroupID           string                      `json:"group_id"`
	DedupID           string                      `json:"dedup_id"`
}

type SendResult struct {
	MessageID      string `json:"message_id"`
	MD5OfBody      string `json:"md5_of_body"`
	SequenceNumber string `json:"sequence_number,omitempty"`
	Duplicate      bool   `json:"duplicate,omitempty"`
}

func md5hex(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

func (s *Service) Send(name string, in SendInput) (SendResult, error) {
	q, err := s.getQueue(name)
	if err != nil {
		return SendResult{}, err
	}
	if in.Body == "" {
		return SendResult{}, core.BadRequest("message body must not be empty")
	}
	if len(in.Body) > q.MaxMessageSize {
		return SendResult{}, core.BadRequest("message body exceeds %d bytes", q.MaxMessageSize)
	}
	delay := q.DelaySeconds
	if in.DelaySeconds != nil {
		delay = *in.DelaySeconds
	}
	if delay < 0 || delay > 900 {
		return SendResult{}, core.BadRequest("delay_seconds must be 0-900")
	}
	if q.FIFO {
		if in.GroupID == "" {
			return SendResult{}, core.BadRequest("group_id is required for FIFO queues")
		}
		if in.DedupID == "" {
			if !q.ContentBasedDeduplication {
				return SendResult{}, core.BadRequest("dedup_id is required unless content-based deduplication is enabled")
			}
			in.DedupID = md5hex(in.Body)
		}
		delay = 0 // per-message delays are not supported on FIFO queues
	}
	now := time.Now()
	m := &message{ID: uuid(), Body: in.Body, MD5: md5hex(in.Body), Attrs: in.MessageAttributes, SentAt: now,
		VisibleAt: now.Add(time.Duration(delay) * time.Second), GroupID: in.GroupID, DedupID: in.DedupID}
	s.mu.Lock()
	st := s.queues[name]
	if st == nil {
		st = &queueState{dedup: map[string]time.Time{}, notify: make(chan struct{})}
		s.queues[name] = st
	}
	if q.FIFO {
		if t, ok := st.dedup[in.DedupID]; ok && time.Since(t) < dedupWindow {
			s.mu.Unlock()
			return SendResult{MessageID: m.ID, MD5OfBody: m.MD5, Duplicate: true}, nil
		}
		st.dedup[in.DedupID] = now
		st.seq++
		m.SequenceNo = st.seq
	}
	st.msgs = append(st.msgs, m)
	st.stats.sent++
	s.dirty = true
	close(st.notify)
	st.notify = make(chan struct{})
	s.mu.Unlock()
	res := SendResult{MessageID: m.ID, MD5OfBody: m.MD5}
	if m.SequenceNo > 0 {
		res.SequenceNumber = fmt.Sprintf("%020d", m.SequenceNo)
	}
	return res, nil
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

type Received struct {
	MessageID         string                      `json:"message_id"`
	ReceiptHandle     string                      `json:"receipt_handle"`
	Body              string                      `json:"body"`
	MD5OfBody         string                      `json:"md5_of_body"`
	Attributes        map[string]string           `json:"attributes"`
	MessageAttributes map[string]MessageAttribute `json:"message_attributes,omitempty"`
}

// receiveNow takes up to max visible messages; mu must be held.
func (s *Service) receiveNow(q Queue, st *queueState, max int, vis time.Duration) []Received {
	now := time.Now()
	var out []Received
	var moved []*message
	busyGroups := map[string]bool{}
	if q.FIFO {
		for _, m := range st.msgs {
			if m.GroupID != "" && m.VisibleAt.After(now) && m.ReceiveCount > 0 {
				busyGroups[m.GroupID] = true
			}
		}
	}
	kept := st.msgs[:0]
	for _, m := range st.msgs {
		if len(out) >= max || m.VisibleAt.After(now) || (q.FIFO && busyGroups[m.GroupID]) {
			kept = append(kept, m)
			continue
		}
		if q.Redrive != nil && q.Redrive.MaxReceiveCount > 0 && m.ReceiveCount >= q.Redrive.MaxReceiveCount {
			moved = append(moved, m)
			continue
		}
		m.ReceiveCount++
		if m.FirstReceive.IsZero() {
			m.FirstReceive = now
		}
		m.Receipt = core.NewSecret(48)
		m.VisibleAt = now.Add(vis)
		attrs := map[string]string{
			"SentTimestamp": fmt.Sprint(m.SentAt.UnixMilli()), "ApproximateReceiveCount": fmt.Sprint(m.ReceiveCount),
			"ApproximateFirstReceiveTimestamp": fmt.Sprint(m.FirstReceive.UnixMilli()),
		}
		if m.GroupID != "" {
			attrs["MessageGroupId"] = m.GroupID
			attrs["MessageDeduplicationId"] = m.DedupID
			attrs["SequenceNumber"] = fmt.Sprintf("%020d", m.SequenceNo)
		}
		if m.SourceQueue != "" {
			attrs["DeadLetterQueueSourceArn"] = m.SourceQueue
		}
		out = append(out, Received{MessageID: m.ID, ReceiptHandle: m.Receipt, Body: m.Body, MD5OfBody: m.MD5, Attributes: attrs, MessageAttributes: m.Attrs})
		kept = append(kept, m)
	}
	st.msgs = kept
	if len(out) > 0 || len(moved) > 0 {
		st.stats.received += int64(len(out))
		s.dirty = true
	}
	if len(moved) > 0 {
		if dlq := s.queues[q.Redrive.DeadLetterQueue]; dlq != nil {
			for _, m := range moved {
				m.VisibleAt, m.Receipt, m.SourceQueue = now, "", q.ARN
				m.ReceiveCount = 0
				dlq.msgs = append(dlq.msgs, m)
			}
			close(dlq.notify)
			dlq.notify = make(chan struct{})
		}
	}
	return out
}

// ReceiveWait receives messages, long-polling up to wait for at least one.
func (s *Service) ReceiveWait(ctx context.Context, name string, max int, vis *int, wait *int) ([]Received, error) {
	q, err := s.getQueue(name)
	if err != nil {
		return nil, err
	}
	if max <= 0 {
		max = 1
	}
	if max > 10 {
		return nil, core.BadRequest("max_messages must be 1-10")
	}
	v := q.VisibilityTimeout
	if vis != nil {
		v = *vis
	}
	w := q.ReceiveWaitTime
	if wait != nil {
		w = *wait
	}
	if w < 0 || w > 20 {
		return nil, core.BadRequest("wait_seconds must be 0-20")
	}
	deadline := time.Now().Add(time.Duration(w) * time.Second)
	for {
		s.mu.Lock()
		st := s.queues[name]
		if st == nil {
			s.mu.Unlock()
			return nil, core.Errf(http.StatusNotFound, "QueueDoesNotExist", "queue %q does not exist", name)
		}
		out := s.receiveNow(q, st, max, time.Duration(v)*time.Second)
		ch := st.notify
		s.mu.Unlock()
		if len(out) > 0 || time.Now().After(deadline) {
			if out == nil {
				out = []Received{}
			}
			return out, nil
		}
		// Wake on new messages, or re-check periodically for delayed/visible-again ones.
		select {
		case <-ctx.Done():
			return []Received{}, nil
		case <-ch:
		case <-time.After(min(time.Until(deadline), time.Second)):
		}
	}
}

// Receive implements lambda.QueueSource.
func (s *Service) Receive(name string, max int, vis time.Duration) ([]lambda.QueueMessage, error) {
	q, err := s.getQueue(name)
	if err != nil {
		return nil, err
	}
	max = min(max, 10)
	s.mu.Lock()
	st := s.queues[name]
	if st == nil {
		s.mu.Unlock()
		return nil, core.Errf(http.StatusNotFound, "QueueDoesNotExist", "queue %q does not exist", name)
	}
	rs := s.receiveNow(q, st, max, vis)
	s.mu.Unlock()
	out := make([]lambda.QueueMessage, len(rs))
	for i, r := range rs {
		ma := map[string]any{}
		for k, a := range r.MessageAttributes {
			ma[k] = map[string]string{"dataType": a.DataType, "stringValue": a.StringValue}
		}
		out[i] = lambda.QueueMessage{ID: r.MessageID, ReceiptHandle: r.ReceiptHandle, Body: r.Body, Attributes: r.Attributes, MessageAttributes: ma, MD5OfBody: r.MD5OfBody}
	}
	return out, nil
}

func (s *Service) Delete(name, receipt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.queues[name]
	if st == nil {
		return core.Errf(http.StatusNotFound, "QueueDoesNotExist", "queue %q does not exist", name)
	}
	for i, m := range st.msgs {
		if m.Receipt != "" && m.Receipt == receipt {
			st.msgs = append(st.msgs[:i], st.msgs[i+1:]...)
			st.stats.deleted++
			s.dirty = true
			return nil
		}
	}
	return core.Errf(http.StatusBadRequest, "ReceiptHandleIsInvalid", "the receipt handle is invalid or expired")
}

func (s *Service) QueueARN(name string) (string, bool) {
	q, err := s.getQueue(name)
	return q.ARN, err == nil
}

// NameFromARN resolves "arn:aws:sqs:<region>:<account>:<name>".
func NameFromARN(arn string) string {
	i := strings.LastIndex(arn, ":")
	return arn[i+1:]
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:sqs:{region}:{account}:{name}")
	r.Handle("GET /api/v1/sqs/queues", "sqs:ListQueues", s.list)
	r.Handle("POST /api/v1/sqs/queues", "sqs:CreateQueue", s.create)
	r.Handle("GET /api/v1/sqs/queues/{name}", "sqs:GetQueueAttributes", s.get, res)
	r.Handle("PATCH /api/v1/sqs/queues/{name}", "sqs:SetQueueAttributes", s.update, res)
	r.Handle("DELETE /api/v1/sqs/queues/{name}", "sqs:DeleteQueue", s.deleteQueue, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/purge", "sqs:PurgeQueue", s.purge, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/messages", "sqs:SendMessage", s.send, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/messages/receive", "sqs:ReceiveMessage", s.receive, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/messages/delete", "sqs:DeleteMessage", s.deleteMessages, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/messages/visibility", "sqs:ChangeMessageVisibility", s.changeVisibility, res)
	r.Handle("GET /api/v1/sqs/queues/{name}/messages/peek", "sqs:ReceiveMessage", s.peek, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/redrive", "sqs:StartMessageMoveTask", s.redrive, res)
}

func (s *Service) view(q Queue) map[string]any {
	s.mu.Lock()
	st := s.queues[q.Name]
	visible, inflight, delayed := 0, 0, 0
	now := time.Now()
	var stats [3]int64
	if st != nil {
		for _, m := range st.msgs {
			switch {
			case m.VisibleAt.After(now) && m.ReceiveCount > 0:
				inflight++
			case m.VisibleAt.After(now):
				delayed++
			default:
				visible++
			}
		}
		stats = [3]int64{st.stats.sent, st.stats.received, st.stats.deleted}
	}
	s.mu.Unlock()
	var sources []string
	for _, o := range store.List[Queue](s.env.Store, cQueues) {
		if o.Redrive != nil && o.Redrive.DeadLetterQueue == q.Name {
			sources = append(sources, o.Name)
		}
	}
	b, _ := json.Marshal(q)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["approximate_number_of_messages"] = visible
	m["approximate_number_of_messages_not_visible"] = inflight
	m["approximate_number_of_messages_delayed"] = delayed
	m["messages_sent"], m["messages_received"], m["messages_deleted"] = stats[0], stats[1], stats[2]
	m["dead_letter_source_queues"] = sources
	return m
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, q := range store.List[Queue](s.env.Store, cQueues) {
		if p := c.Query("prefix"); p == "" || strings.HasPrefix(q.Name, p) {
			out = append(out, s.view(q))
		}
	}
	return out, nil
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type attrsInput struct {
	VisibilityTimeout         *int      `json:"visibility_timeout"`
	MessageRetention          *int      `json:"message_retention_seconds"`
	DelaySeconds              *int      `json:"delay_seconds"`
	ReceiveWaitTime           *int      `json:"receive_wait_time_seconds"`
	MaxMessageSize            *int      `json:"max_message_size"`
	ContentBasedDeduplication *bool     `json:"content_based_deduplication"`
	Redrive                   *Redrive  `json:"redrive_policy"`
	Tags                      core.Tags `json:"tags"`
}

func (s *Service) applyAttrs(c *httpx.Ctx, q *Queue, in attrsInput) error {
	check := func(v *int, lo, hi int, name string, dst *int) error {
		if v == nil {
			return nil
		}
		if *v < lo || *v > hi {
			return core.BadRequest("%s must be %d-%d", name, lo, hi)
		}
		*dst = *v
		return nil
	}
	for _, e := range []error{
		check(in.VisibilityTimeout, 0, 43200, "visibility_timeout", &q.VisibilityTimeout),
		check(in.MessageRetention, 60, 1209600, "message_retention_seconds", &q.MessageRetention),
		check(in.DelaySeconds, 0, 900, "delay_seconds", &q.DelaySeconds),
		check(in.ReceiveWaitTime, 0, 20, "receive_wait_time_seconds", &q.ReceiveWaitTime),
		check(in.MaxMessageSize, 1024, maxBodyBytes, "max_message_size", &q.MaxMessageSize),
	} {
		if e != nil {
			return e
		}
	}
	if in.ContentBasedDeduplication != nil {
		q.ContentBasedDeduplication = *in.ContentBasedDeduplication
	}
	if in.Redrive != nil {
		if in.Redrive.DeadLetterQueue == "" {
			q.Redrive = nil
		} else {
			dlq, err := s.getQueue(in.Redrive.DeadLetterQueue)
			if err != nil {
				return err
			}
			if dlq.FIFO != q.FIFO || dlq.Name == q.Name {
				return core.BadRequest("the dead-letter queue must be a different queue of the same type (standard/FIFO)")
			}
			if in.Redrive.MaxReceiveCount < 1 || in.Redrive.MaxReceiveCount > 1000 {
				return core.BadRequest("max_receive_count must be 1-1000")
			}
			if err := c.Authorize("sqs:SendMessage", dlq.ARN); err != nil {
				return err
			}
			q.Redrive = in.Redrive
		}
	}
	if in.Tags != nil {
		q.Tags = in.Tags
	}
	return nil
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in struct {
		Name string `json:"name"`
		FIFO bool   `json:"fifo"`
		attrsInput
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(in.Name, ".fifo")
	if !nameRe.MatchString(base) {
		return nil, core.BadRequest("queue names are 1-80 letters, digits, hyphens or underscores (FIFO queues end in .fifo)")
	}
	if in.FIFO != strings.HasSuffix(in.Name, ".fifo") {
		return nil, core.BadRequest("FIFO queue names must end in .fifo, and only FIFO queue names may")
	}
	if store.Has(s.env.Store, cQueues, in.Name) {
		return nil, core.Errf(http.StatusConflict, "QueueAlreadyExists", "queue %q already exists", in.Name)
	}
	q := Queue{Name: in.Name, ARN: s.env.ARN("sqs", in.Name), URL: s.queueURL(in.Name), FIFO: in.FIFO,
		VisibilityTimeout: 30, MessageRetention: 345600, MaxMessageSize: maxBodyBytes, CreatedAt: core.Now(), LastModified: core.Now()}
	if err := s.applyAttrs(c, &q, in.attrsInput); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.queues[q.Name] = &queueState{dedup: map[string]time.Time{}, notify: make(chan struct{})}
	s.mu.Unlock()
	return s.view(q), store.Put(s.env.Store, cQueues, q.Name, q)
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	q, err := s.getQueue(c.Param("name"))
	if err != nil {
		return nil, err
	}
	return s.view(q), nil
}

func (s *Service) update(c *httpx.Ctx) (any, error) {
	var in attrsInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	q, err := s.getQueue(c.Param("name"))
	if err != nil {
		return nil, err
	}
	if err := s.applyAttrs(c, &q, in); err != nil {
		return nil, err
	}
	q.LastModified = core.Now()
	return s.view(q), store.Put(s.env.Store, cQueues, q.Name, q)
}

func (s *Service) deleteQueue(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if _, err := s.getQueue(name); err != nil {
		return nil, err
	}
	for _, o := range store.List[Queue](s.env.Store, cQueues) {
		if o.Redrive != nil && o.Redrive.DeadLetterQueue == name {
			return nil, core.Conflict("queue %q is the dead-letter queue of %q; remove that redrive policy first", name, o.Name)
		}
	}
	s.mu.Lock()
	delete(s.queues, name)
	s.mu.Unlock()
	_ = os.Remove(s.file(name))
	return nil, store.Delete(s.env.Store, cQueues, name)
}

func (s *Service) purge(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if _, err := s.getQueue(name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if st := s.queues[name]; st != nil {
		st.msgs = nil
		s.dirty = true
	}
	s.mu.Unlock()
	return nil, nil
}

func (s *Service) send(c *httpx.Ctx) (any, error) {
	var in struct {
		SendInput
		Entries []SendInput `json:"entries"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Entries) == 0 {
		return s.Send(c.Param("name"), in.SendInput)
	}
	if len(in.Entries) > 10 {
		return nil, core.BadRequest("a batch holds at most 10 messages")
	}
	type result struct {
		SendResult
		Error string `json:"error,omitempty"`
	}
	out := make([]result, 0, len(in.Entries))
	for _, e := range in.Entries {
		r, err := s.Send(c.Param("name"), e)
		res := result{SendResult: r}
		if err != nil {
			var ce *core.Error
			if errors.As(err, &ce) && ce.Code == "QueueDoesNotExist" {
				return nil, err
			}
			res.Error = err.Error()
		}
		out = append(out, res)
	}
	return out, nil
}

func (s *Service) receive(c *httpx.Ctx) (any, error) {
	var in struct {
		MaxMessages       int  `json:"max_messages"`
		VisibilityTimeout *int `json:"visibility_timeout"`
		WaitSeconds       *int `json:"wait_seconds"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.ReceiveWait(c.R.Context(), c.Param("name"), in.MaxMessages, in.VisibilityTimeout, in.WaitSeconds)
}

func (s *Service) deleteMessages(c *httpx.Ctx) (any, error) {
	var in struct {
		ReceiptHandle  string   `json:"receipt_handle"`
		ReceiptHandles []string `json:"receipt_handles"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.ReceiptHandle != "" {
		in.ReceiptHandles = append(in.ReceiptHandles, in.ReceiptHandle)
	}
	failed := []string{}
	for _, r := range in.ReceiptHandles {
		if err := s.Delete(c.Param("name"), r); err != nil {
			var ce *core.Error
			if errors.As(err, &ce) && ce.Code == "QueueDoesNotExist" {
				return nil, err
			}
			failed = append(failed, r)
		}
	}
	return map[string]any{"deleted": len(in.ReceiptHandles) - len(failed), "failed": failed}, nil
}

func (s *Service) changeVisibility(c *httpx.Ctx) (any, error) {
	var in struct {
		ReceiptHandle     string `json:"receipt_handle"`
		VisibilityTimeout int    `json:"visibility_timeout"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.VisibilityTimeout < 0 || in.VisibilityTimeout > 43200 {
		return nil, core.BadRequest("visibility_timeout must be 0-43200")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.queues[c.Param("name")]
	if st == nil {
		return nil, core.Errf(http.StatusNotFound, "QueueDoesNotExist", "queue %q does not exist", c.Param("name"))
	}
	for _, m := range st.msgs {
		if in.ReceiptHandle != "" && m.Receipt == in.ReceiptHandle {
			m.VisibleAt = time.Now().Add(time.Duration(in.VisibilityTimeout) * time.Second)
			s.dirty = true
			if in.VisibilityTimeout == 0 {
				close(st.notify)
				st.notify = make(chan struct{})
			}
			return nil, nil
		}
	}
	return nil, core.Errf(http.StatusBadRequest, "ReceiptHandleIsInvalid", "the receipt handle is invalid or expired")
}

// peek lists messages without receiving them (a console convenience).
func (s *Service) peek(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if _, err := s.getQueue(name); err != nil {
		return nil, err
	}
	limit := c.QueryInt("limit", 50)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	now := time.Now()
	st := s.queues[name]
	if st == nil {
		return out, nil
	}
	for _, m := range st.msgs {
		if len(out) >= limit {
			break
		}
		state := "available"
		if m.VisibleAt.After(now) {
			state = map[bool]string{true: "in-flight", false: "delayed"}[m.ReceiveCount > 0]
		}
		out = append(out, map[string]any{"message_id": m.ID, "body": m.Body, "sent_at": m.SentAt, "receive_count": m.ReceiveCount,
			"state": state, "group_id": m.GroupID, "message_attributes": m.Attrs, "size": len(m.Body), "source_queue": m.SourceQueue})
	}
	return out, nil
}

// redrive moves every message in a dead-letter queue back to its source queue (or a given destination).
func (s *Service) redrive(c *httpx.Ctx) (any, error) {
	var in struct {
		Destination string `json:"destination"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	name := c.Param("name")
	if _, err := s.getQueue(name); err != nil {
		return nil, err
	}
	if in.Destination != "" {
		dq, err := s.getQueue(in.Destination)
		if err != nil {
			return nil, err
		}
		if err := c.Authorize("sqs:SendMessage", dq.ARN); err != nil {
			return nil, err
		}
	} else {
		// Messages return to their source queues; the caller must be able to send there.
		for _, q := range store.List[Queue](s.env.Store, cQueues) {
			if q.Redrive != nil && q.Redrive.DeadLetterQueue == name {
				if err := c.Authorize("sqs:SendMessage", q.ARN); err != nil {
					return nil, err
				}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.queues[name]
	if src == nil {
		return nil, core.Errf(http.StatusNotFound, "QueueDoesNotExist", "queue %q does not exist", name)
	}
	moved := 0
	kept := src.msgs[:0]
	for _, m := range src.msgs {
		dest := in.Destination
		if dest == "" && m.SourceQueue != "" {
			dest = NameFromARN(m.SourceQueue)
		}
		dst := s.queues[dest]
		if dst == nil || dest == name {
			kept = append(kept, m)
			continue
		}
		m.SourceQueue, m.ReceiveCount, m.Receipt, m.VisibleAt = "", 0, "", time.Now()
		dst.msgs = append(dst.msgs, m)
		close(dst.notify)
		dst.notify = make(chan struct{})
		moved++
	}
	src.msgs = kept
	s.dirty = true
	return map[string]int{"moved": moved}, nil
}
