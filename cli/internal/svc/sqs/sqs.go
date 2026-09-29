// Package sqs implements message queues: standard and FIFO queues with
// visibility timeouts, delays, long polling, retention and dead-letter queues.
// Messages live in memory and are snapshotted to disk.
//
// The native API is in this file; aws.go serves the same queues over the AWS
// SQS protocols (awsJson 1.0 and awsQuery).
package sqs

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
)

const (
	cQueues        = "sqs_queues"
	maxBodyBytes   = 256 << 10 // default MaximumMessageSize
	maxMessageSize = 1 << 20   // largest MaximumMessageSize a queue may set
	dedupWindow    = 5 * time.Minute
	maxAttributes  = 10
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
	DeduplicationScope        string    `json:"deduplication_scope,omitempty"`   // FIFO: queue | messageGroup
	FifoThroughputLimit       string    `json:"fifo_throughput_limit,omitempty"` // FIFO: perQueue | perMessageGroupId
	VisibilityTimeout         int       `json:"visibility_timeout"`
	MessageRetention          int       `json:"message_retention_seconds"`
	DelaySeconds              int       `json:"delay_seconds"`
	ReceiveWaitTime           int       `json:"receive_wait_time_seconds"`
	MaxMessageSize            int       `json:"max_message_size"`
	Redrive                   *Redrive  `json:"redrive_policy,omitempty"`
	RedriveAllowPolicy        string    `json:"redrive_allow_policy,omitempty"` // JSON, as in AWS
	Policy                    string    `json:"policy,omitempty"`               // JSON access policy (stored, not enforced)
	KmsMasterKeyID            string    `json:"kms_master_key_id,omitempty"`
	KmsDataKeyReusePeriod     int       `json:"kms_data_key_reuse_period_seconds,omitempty"`
	SqsManagedSSE             *bool     `json:"sqs_managed_sse_enabled,omitempty"`
	CreatedAt                 time.Time `json:"created_at"`
	LastModified              time.Time `json:"last_modified"`
	Tags                      core.Tags `json:"tags,omitempty"`
}

// MessageAttribute is a message attribute (or message system attribute).
// DataType is String, Number or Binary, optionally with a custom suffix
// ("Number.float", "Binary.png").
type MessageAttribute struct {
	DataType         string   `json:"data_type"`
	StringValue      string   `json:"string_value"`
	BinaryValue      []byte   `json:"binary_value,omitempty"`
	StringListValues []string `json:"string_list_values,omitempty"`
	BinaryListValues [][]byte `json:"binary_list_values,omitempty"`
}

func (a MessageAttribute) binary() bool { return strings.HasPrefix(a.DataType, "Binary") }

type message struct {
	ID           string                      `json:"id"`
	Body         string                      `json:"body"`
	MD5          string                      `json:"md5"`
	Attrs        map[string]MessageAttribute `json:"attrs,omitempty"`
	SysAttrs     map[string]MessageAttribute `json:"sys_attrs,omitempty"`
	SenderID     string                      `json:"sender_id,omitempty"`
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

func (m *message) inFlight(now time.Time) bool { return m.ReceiveCount > 0 && m.VisibleAt.After(now) }

type dedupEntry struct {
	at  time.Time
	id  string
	seq int64
}

// attempt remembers a FIFO ReceiveRequestAttemptId so a retried receive returns the same messages.
type attempt struct {
	at       time.Time
	receipts map[string]string // message ID -> receipt handle
}

type queueState struct {
	msgs      []*message
	dedup     map[string]dedupEntry
	attempts  map[string]attempt
	seq       int64
	notify    chan struct{}
	lastPurge time.Time
	stats     struct{ sent, received, deleted int64 }
}

func newState() *queueState {
	return &queueState{dedup: map[string]dedupEntry{}, attempts: map[string]attempt{}, notify: make(chan struct{})}
}

// wake notifies long-polling receivers; mu must be held.
func (st *queueState) wake() {
	close(st.notify)
	st.notify = make(chan struct{})
}

type Service struct {
	env    *svc.Env
	mu     sync.Mutex
	queues map[string]*queueState
	dirty  bool
	tasks  *moveTasks
}

func New(env *svc.Env) *Service {
	s := &Service{env: env, queues: map[string]*queueState{}, tasks: &moveTasks{byHandle: map[string]*moveTask{}}}
	for _, q := range store.List[Queue](env.Store, cQueues) {
		s.queues[q.Name] = s.load(q.Name)
	}
	return s
}

func (s *Service) file(name string) string { return s.env.Cfg.Path("sqs", name+".json") }

func (s *Service) load(name string) *queueState {
	st := newState()
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
	retention := map[string]int{}
	for _, q := range store.List[Queue](s.env.Store, cQueues) {
		retention[q.Name] = q.MessageRetention
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, st := range s.queues {
		r, ok := retention[name]
		if !ok {
			continue
		}
		cut := time.Now().Add(-time.Duration(r) * time.Second)
		kept := st.msgs[:0]
		for _, m := range st.msgs {
			if m.SentAt.After(cut) {
				kept = append(kept, m)
			} else {
				s.dirty = true
			}
		}
		clear(st.msgs[len(kept):])
		st.msgs = kept
		for k, d := range st.dedup {
			if time.Since(d.at) > dedupWindow {
				delete(st.dedup, k)
			}
		}
		for k, a := range st.attempts {
			if time.Since(a.at) > dedupWindow {
				delete(st.attempts, k)
			}
		}
	}
}

func (s *Service) queueURL(name string) string {
	_, port, _ := strings.Cut(s.env.Cfg.APIAddr, ":")
	return fmt.Sprintf("http://%s:%s/api/v1/sqs/queues/%s", s.env.Cfg.PublicHost, port, name)
}

func errNoQueue(name string) error {
	return core.Errf(http.StatusNotFound, "QueueDoesNotExist", "The specified queue %s does not exist.", name)
}

func (s *Service) getQueue(name string) (Queue, error) {
	q, err := store.Get[Queue](s.env.Store, cQueues, name)
	if err != nil {
		return q, errNoQueue(name)
	}
	return q, nil
}

// state returns a queue's in-memory state, creating it if needed; mu must be held.
func (s *Service) state(name string) *queueState {
	st := s.queues[name]
	if st == nil {
		st = newState()
		s.queues[name] = st
	}
	return st
}

// ---- core operations (also used by Lambda, SNS and EventBridge) ----

type SendInput struct {
	Body              string                      `json:"body"`
	DelaySeconds      *int                        `json:"delay_seconds"`
	MessageAttributes map[string]MessageAttribute `json:"message_attributes"`
	// SystemAttributes holds message system attributes (AWSTraceHeader).
	SystemAttributes map[string]MessageAttribute `json:"message_system_attributes"`
	GroupID          string                      `json:"group_id"`
	DedupID          string                      `json:"dedup_id"`
	// SenderID is reported as the SenderId attribute (set by the server).
	SenderID string `json:"-"`
}

type SendResult struct {
	MessageID      string `json:"message_id"`
	MD5OfBody      string `json:"md5_of_body"`
	MD5OfAttrs     string `json:"md5_of_message_attributes,omitempty"`
	MD5OfSysAttrs  string `json:"md5_of_message_system_attributes,omitempty"`
	SequenceNumber string `json:"sequence_number,omitempty"`
	Duplicate      bool   `json:"duplicate,omitempty"`
}

func md5hex(s string) string { h := md5.Sum([]byte(s)); return hex.EncodeToString(h[:]) }

func invalid(format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, "InvalidParameterValue", format, a...)
}

func missing(param string) error {
	return core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter %s.", param)
}

// validBody reports whether s holds only the characters SQS accepts:
// #x9 | #xA | #xD | #x20-#xD7FF | #xE000-#xFFFD | #x10000-#x10FFFF.
func validBody(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == 0x9 || r == 0xA || r == 0xD:
		case r >= 0x20 && r <= 0xD7FF:
		case r >= 0xE000 && r <= 0xFFFD:
		case r >= 0x10000 && r <= 0x10FFFF:
		default:
			return false
		}
	}
	return true
}

var attrNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`)

// checkAttributes validates message attributes and returns their size in bytes.
func checkAttributes(attrs map[string]MessageAttribute) (int, error) {
	if len(attrs) > maxAttributes {
		return 0, invalid("Number of message attributes [%d] exceeds the allowed maximum [%d].", len(attrs), maxAttributes)
	}
	size := 0
	for name, a := range attrs {
		lower := strings.ToLower(name)
		if !attrNameRe.MatchString(name) || strings.HasPrefix(lower, "aws.") || strings.HasPrefix(lower, "amazon.") ||
			strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
			return 0, invalid("Message (user) attribute name '%s' is invalid.", name)
		}
		base, _, _ := strings.Cut(a.DataType, ".")
		switch base {
		case "String":
			if a.StringValue == "" && len(a.StringListValues) == 0 {
				return 0, invalid("Message (user) attribute '%s' must contain a non-empty value of type 'String'.", name)
			}
			if !validBody(a.StringValue) {
				return 0, invalid("Message (user) attribute '%s' contains invalid characters.", name)
			}
		case "Number":
			if _, err := strconv.ParseFloat(strings.TrimSpace(a.StringValue), 64); err != nil {
				return 0, invalid("Can't cast the value of message (user) attribute '%s' to a number.", name)
			}
		case "Binary":
			if len(a.BinaryValue) == 0 && len(a.BinaryListValues) == 0 {
				return 0, invalid("Message (user) attribute '%s' must contain a non-empty value of type 'Binary'.", name)
			}
		default:
			if a.DataType == "" {
				return 0, invalid("The message attribute '%s' must contain non-empty message attribute type.", name)
			}
			return 0, invalid("The type of message (user) attribute '%s' is invalid. You must use only the following supported type prefixes: Binary, Number, String.", name)
		}
		size += len(name) + len(a.DataType) + len(a.StringValue) + len(a.BinaryValue)
	}
	return size, nil
}

// md5OfAttributes computes MD5OfMessageAttributes as AWS does: attributes
// sorted by name, each encoded as length-prefixed name, length-prefixed data
// type, a transport byte (1 string, 2 binary) and the length-prefixed value.
func md5OfAttributes(attrs map[string]MessageAttribute) string {
	if len(attrs) == 0 {
		return ""
	}
	names := make([]string, 0, len(attrs))
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	put := func(p []byte) {
		var n [4]byte
		l := len(p)
		n[0], n[1], n[2], n[3] = byte(l>>24), byte(l>>16), byte(l>>8), byte(l)
		b.Write(n[:])
		b.Write(p)
	}
	for _, n := range names {
		a := attrs[n]
		put([]byte(n))
		put([]byte(a.DataType))
		switch {
		case a.binary() && len(a.BinaryListValues) > 0:
			b.WriteByte(4)
			for _, v := range a.BinaryListValues {
				put(v)
			}
		case a.binary():
			b.WriteByte(2)
			put(a.BinaryValue)
		case len(a.StringListValues) > 0:
			b.WriteByte(3)
			for _, v := range a.StringListValues {
				put([]byte(v))
			}
		default:
			b.WriteByte(1)
			put([]byte(a.StringValue))
		}
	}
	h := md5.Sum(b.Bytes())
	return hex.EncodeToString(h[:])
}

func (s *Service) Send(name string, in SendInput) (SendResult, error) {
	q, err := s.getQueue(name)
	if err != nil {
		return SendResult{}, err
	}
	if in.Body == "" {
		return SendResult{}, missing("MessageBody")
	}
	if !validBody(in.Body) {
		return SendResult{}, core.Errf(http.StatusBadRequest, "InvalidMessageContents", "Invalid binary character in the message body. Only #x9 | #xA | #xD | #x20 to #xD7FF | #xE000 to #xFFFD | #x10000 to #x10FFFF are allowed.")
	}
	attrSize, err := checkAttributes(in.MessageAttributes)
	if err != nil {
		return SendResult{}, err
	}
	for n, a := range in.SystemAttributes {
		if n != "AWSTraceHeader" || !strings.HasPrefix(a.DataType, "String") || a.StringValue == "" {
			return SendResult{}, invalid("Message system attribute '%s' is invalid; only AWSTraceHeader of type String is supported.", n)
		}
	}
	if size := len(in.Body) + attrSize; size > q.MaxMessageSize {
		return SendResult{}, invalid("One or more parameters are invalid. Reason: Message must be shorter than %d bytes.", q.MaxMessageSize)
	}
	delay := q.DelaySeconds
	if in.DelaySeconds != nil {
		if q.FIFO && *in.DelaySeconds != 0 {
			return SendResult{}, invalid("Value %d for parameter DelaySeconds is invalid. Reason: The request include parameter that is not valid for this queue type.", *in.DelaySeconds)
		}
		if !q.FIFO {
			delay = *in.DelaySeconds
		}
	}
	if delay < 0 || delay > 900 {
		return SendResult{}, invalid("Value %d for parameter DelaySeconds is invalid. Reason: DelaySeconds must be >= 0 and <= 900.", delay)
	}
	if len(in.GroupID) > 128 || len(in.DedupID) > 128 {
		return SendResult{}, invalid("MessageGroupId and MessageDeduplicationId can be at most 128 characters.")
	}
	if q.FIFO {
		if in.GroupID == "" {
			return SendResult{}, missing("MessageGroupId")
		}
		if in.DedupID == "" {
			if !q.ContentBasedDeduplication {
				return SendResult{}, invalid("The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
			}
			h := sha256.Sum256([]byte(in.Body))
			in.DedupID = hex.EncodeToString(h[:])
		}
	} else if in.DedupID != "" {
		return SendResult{}, invalid("The request include parameter MessageDeduplicationId that is not valid for this queue type")
	}
	now := time.Now()
	m := &message{ID: uuid(), Body: in.Body, MD5: md5hex(in.Body), SentAt: now, SenderID: in.SenderID,
		VisibleAt: now.Add(time.Duration(delay) * time.Second), GroupID: in.GroupID, DedupID: in.DedupID}
	if len(in.MessageAttributes) > 0 {
		m.Attrs = in.MessageAttributes
	}
	if len(in.SystemAttributes) > 0 {
		m.SysAttrs = in.SystemAttributes
	}
	res := SendResult{MessageID: m.ID, MD5OfBody: m.MD5, MD5OfAttrs: md5OfAttributes(m.Attrs), MD5OfSysAttrs: md5OfAttributes(m.SysAttrs)}
	s.mu.Lock()
	st := s.state(name)
	if q.FIFO {
		key := in.DedupID
		if q.DeduplicationScope == "messageGroup" {
			key = in.GroupID + "\x00" + in.DedupID
		}
		if d, ok := st.dedup[key]; ok && time.Since(d.at) < dedupWindow {
			s.mu.Unlock()
			res.MessageID, res.SequenceNumber, res.Duplicate = d.id, seqString(d.seq), true
			return res, nil
		}
		st.seq++
		m.SequenceNo = st.seq
		st.dedup[key] = dedupEntry{at: now, id: m.ID, seq: m.SequenceNo}
	}
	st.msgs = append(st.msgs, m)
	st.stats.sent++
	s.dirty = true
	st.wake()
	s.mu.Unlock()
	if m.SequenceNo > 0 {
		res.SequenceNumber = seqString(m.SequenceNo)
	}
	return res, nil
}

func seqString(n int64) string { return fmt.Sprintf("%020d", n) }

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
	// SystemAttributes are message system attributes set by the sender (AWSTraceHeader).
	SystemAttributes map[string]MessageAttribute `json:"-"`
}

func received(m *message) Received {
	attrs := map[string]string{
		"SentTimestamp": fmt.Sprint(m.SentAt.UnixMilli()), "ApproximateReceiveCount": fmt.Sprint(m.ReceiveCount),
		"ApproximateFirstReceiveTimestamp": fmt.Sprint(m.FirstReceive.UnixMilli()),
	}
	if m.SenderID != "" {
		attrs["SenderId"] = m.SenderID
	}
	if m.GroupID != "" {
		attrs["MessageGroupId"] = m.GroupID
	}
	if m.DedupID != "" {
		attrs["MessageDeduplicationId"] = m.DedupID
	}
	if m.SequenceNo > 0 {
		attrs["SequenceNumber"] = seqString(m.SequenceNo)
	}
	if m.SourceQueue != "" {
		attrs["DeadLetterQueueSourceArn"] = m.SourceQueue
	}
	if a, ok := m.SysAttrs["AWSTraceHeader"]; ok {
		attrs["AWSTraceHeader"] = a.StringValue
	}
	return Received{MessageID: m.ID, ReceiptHandle: m.Receipt, Body: m.Body, MD5OfBody: m.MD5, Attributes: attrs,
		MessageAttributes: m.Attrs, SystemAttributes: m.SysAttrs}
}

// receiveNow takes up to max visible messages; mu must be held.
func (s *Service) receiveNow(q Queue, st *queueState, max int, vis time.Duration, attemptID string) []Received {
	now := time.Now()
	// A retried FIFO receive with the same attempt ID returns the same messages
	// while they are still in flight under the receipts it handed out.
	if attemptID != "" {
		if a, ok := st.attempts[attemptID]; ok && now.Sub(a.at) < dedupWindow {
			var out []Received
			for _, m := range st.msgs {
				if r, ok := a.receipts[m.ID]; ok && m.Receipt == r && m.inFlight(now) {
					out = append(out, received(m))
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	var out []Received
	var moved []*message
	var dlq *queueState
	if q.Redrive != nil && q.Redrive.MaxReceiveCount > 0 {
		if store.Has(s.env.Store, cQueues, q.Redrive.DeadLetterQueue) {
			dlq = s.state(q.Redrive.DeadLetterQueue)
		}
	}
	busyGroups := map[string]bool{}
	if q.FIFO {
		for _, m := range st.msgs {
			if m.inFlight(now) {
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
		if dlq != nil && m.ReceiveCount >= q.Redrive.MaxReceiveCount {
			moved = append(moved, m)
			continue
		}
		m.ReceiveCount++
		if m.FirstReceive.IsZero() {
			m.FirstReceive = now
		}
		m.Receipt = core.NewSecret(64)
		m.VisibleAt = now.Add(vis)
		out = append(out, received(m))
		kept = append(kept, m)
	}
	clear(st.msgs[len(kept):])
	st.msgs = kept
	if len(out) > 0 || len(moved) > 0 {
		st.stats.received += int64(len(out))
		s.dirty = true
	}
	if len(moved) > 0 {
		for _, m := range moved {
			m.VisibleAt, m.Receipt, m.SourceQueue = now, "", q.ARN
			dlq.msgs = append(dlq.msgs, m)
		}
		dlq.wake()
	}
	if attemptID != "" && len(out) > 0 {
		a := attempt{at: now, receipts: map[string]string{}}
		for _, r := range out {
			a.receipts[r.MessageID] = r.ReceiptHandle
		}
		st.attempts[attemptID] = a
	}
	return out
}

// ReceiveOptions control a receive.
type ReceiveOptions struct {
	Max               int
	VisibilityTimeout *int
	WaitSeconds       *int
	// AttemptID is the FIFO ReceiveRequestAttemptId.
	AttemptID string
}

// ReceiveWait receives messages, long-polling up to wait for at least one.
func (s *Service) ReceiveWait(ctx context.Context, name string, max int, vis *int, wait *int) ([]Received, error) {
	return s.ReceiveWith(ctx, name, ReceiveOptions{Max: max, VisibilityTimeout: vis, WaitSeconds: wait})
}

// ReceiveWith receives messages; long polls park on a channel, not a thread.
func (s *Service) ReceiveWith(ctx context.Context, name string, o ReceiveOptions) ([]Received, error) {
	q, err := s.getQueue(name)
	if err != nil {
		return nil, err
	}
	max := o.Max
	if max == 0 {
		max = 1
	}
	if max < 1 || max > 10 {
		return nil, invalid("Value %d for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.", max)
	}
	v := q.VisibilityTimeout
	if o.VisibilityTimeout != nil {
		v = *o.VisibilityTimeout
	}
	if v < 0 || v > 43200 {
		return nil, invalid("Value %d for parameter VisibilityTimeout is invalid. Reason: Must be >= 0 and <= 43200.", v)
	}
	w := q.ReceiveWaitTime
	if o.WaitSeconds != nil {
		w = *o.WaitSeconds
	}
	if w < 0 || w > 20 {
		return nil, invalid("Value %d for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.", w)
	}
	attemptID := o.AttemptID
	if !q.FIFO {
		attemptID = ""
	}
	deadline := time.Now().Add(time.Duration(w) * time.Second)
	for {
		s.mu.Lock()
		st := s.queues[name]
		if st == nil || !store.Has(s.env.Store, cQueues, name) {
			s.mu.Unlock()
			return nil, errNoQueue(name)
		}
		out := s.receiveNow(q, st, max, time.Duration(v)*time.Second, attemptID)
		ch := st.notify
		s.mu.Unlock()
		if len(out) > 0 || !time.Now().Before(deadline) {
			if out == nil {
				out = []Received{}
			}
			return out, nil
		}
		// Wake on new messages, or re-check periodically for delayed/visible-again ones.
		t := time.NewTimer(min(time.Until(deadline), time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			return []Received{}, nil
		case <-ch:
		case <-t.C:
		}
		t.Stop()
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
		return nil, errNoQueue(name)
	}
	rs := s.receiveNow(q, st, max, vis, "")
	s.mu.Unlock()
	out := make([]lambda.QueueMessage, len(rs))
	for i, r := range rs {
		ma := map[string]any{}
		for k, a := range r.MessageAttributes {
			v := map[string]any{"dataType": a.DataType, "stringListValues": nonNil(a.StringListValues), "binaryListValues": nonNil(a.BinaryListValues)}
			if a.binary() {
				v["binaryValue"] = a.BinaryValue
			} else {
				v["stringValue"] = a.StringValue
			}
			ma[k] = v
		}
		out[i] = lambda.QueueMessage{ID: r.MessageID, ReceiptHandle: r.ReceiptHandle, Body: r.Body, Attributes: r.Attributes, MessageAttributes: ma, MD5OfBody: r.MD5OfBody}
	}
	return out, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

var errStaleReceipt = core.Errf(http.StatusBadRequest, "ReceiptHandleIsInvalid", "The receipt handle has expired.")

// Delete removes the message last received with receipt.
func (s *Service) Delete(name, receipt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.queues[name]
	if st == nil {
		return errNoQueue(name)
	}
	for i, m := range st.msgs {
		if m.Receipt != "" && m.Receipt == receipt {
			st.msgs = append(st.msgs[:i], st.msgs[i+1:]...)
			st.stats.deleted++
			s.dirty = true
			return nil
		}
	}
	return errStaleReceipt
}

// ChangeVisibility sets the visibility timeout of an in-flight message.
func (s *Service) ChangeVisibility(name, receipt string, seconds int) error {
	if seconds < 0 || seconds > 43200 {
		return invalid("Value %d for parameter VisibilityTimeout is invalid. Reason: Must be >= 0 and <= 43200.", seconds)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.queues[name]
	if st == nil {
		return errNoQueue(name)
	}
	now := time.Now()
	for _, m := range st.msgs {
		if receipt != "" && m.Receipt == receipt {
			if !m.inFlight(now) {
				return core.Errf(http.StatusBadRequest, "MessageNotInflight", "The message referred to is not in flight.")
			}
			m.VisibleAt = now.Add(time.Duration(seconds) * time.Second)
			s.dirty = true
			if seconds == 0 {
				st.wake()
			}
			return nil
		}
	}
	return errStaleReceipt
}

// Purge deletes every message in a queue.
func (s *Service) Purge(name string) error {
	if _, err := s.getQueue(name); err != nil {
		return err
	}
	s.mu.Lock()
	if st := s.queues[name]; st != nil {
		st.msgs = nil
		st.lastPurge = time.Now()
		s.dirty = true
	}
	s.mu.Unlock()
	return nil
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

// counts returns the visible, in-flight and delayed message counts of a queue.
func (s *Service) counts(name string) (visible, inflight, delayed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.queues[name]
	if st == nil {
		return
	}
	now := time.Now()
	for _, m := range st.msgs {
		switch {
		case m.inFlight(now):
			inflight++
		case m.VisibleAt.After(now):
			delayed++
		default:
			visible++
		}
	}
	return
}

// deadLetterSources lists the queues whose redrive policy targets name.
func (s *Service) deadLetterSources(name string) []Queue {
	var out []Queue
	for _, o := range store.List[Queue](s.env.Store, cQueues) {
		if o.Redrive != nil && o.Redrive.DeadLetterQueue == name {
			out = append(out, o)
		}
	}
	return out
}

// ---- queue lifecycle and attributes ----

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type attrsInput struct {
	VisibilityTimeout         *int      `json:"visibility_timeout"`
	MessageRetention          *int      `json:"message_retention_seconds"`
	DelaySeconds              *int      `json:"delay_seconds"`
	ReceiveWaitTime           *int      `json:"receive_wait_time_seconds"`
	MaxMessageSize            *int      `json:"max_message_size"`
	ContentBasedDeduplication *bool     `json:"content_based_deduplication"`
	DeduplicationScope        *string   `json:"deduplication_scope"`
	FifoThroughputLimit       *string   `json:"fifo_throughput_limit"`
	Redrive                   *Redrive  `json:"redrive_policy"`
	RedriveAllowPolicy        *string   `json:"redrive_allow_policy"`
	Policy                    *string   `json:"policy"`
	KmsMasterKeyID            *string   `json:"kms_master_key_id"`
	KmsDataKeyReusePeriod     *int      `json:"kms_data_key_reuse_period_seconds"`
	SqsManagedSSE             *bool     `json:"sqs_managed_sse_enabled"`
	Tags                      core.Tags `json:"tags"`
}

type authorizer func(action, resource string) error

func attrErr(format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, "InvalidAttributeValue", format, a...)
}

type redriveAllow struct {
	RedrivePermission string   `json:"redrivePermission"`
	SourceQueueArns   []string `json:"sourceQueueArns,omitempty"`
}

func parseRedriveAllow(s string) (redriveAllow, error) {
	var p redriveAllow
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return p, attrErr("Invalid value for the parameter RedriveAllowPolicy. Reason: Redrive allow policy is not a valid JSON map.")
	}
	switch p.RedrivePermission {
	case "allowAll", "denyAll":
		if len(p.SourceQueueArns) > 0 {
			return p, attrErr("Invalid value for the parameter RedriveAllowPolicy. Reason: sourceQueueArns is only valid with redrivePermission byQueue.")
		}
	case "byQueue":
		if len(p.SourceQueueArns) == 0 || len(p.SourceQueueArns) > 10 {
			return p, attrErr("Invalid value for the parameter RedriveAllowPolicy. Reason: byQueue requires 1-10 sourceQueueArns.")
		}
	default:
		return p, attrErr("Invalid value for the parameter RedriveAllowPolicy. Reason: redrivePermission must be allowAll, denyAll or byQueue.")
	}
	return p, nil
}

func compactJSON(name, s string) (string, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil || !strings.HasPrefix(strings.TrimSpace(s), "{") {
		return "", attrErr("Invalid value for the parameter %s. Reason: not a valid JSON object.", name)
	}
	return b.String(), nil
}

func (s *Service) applyAttrs(authorize authorizer, q *Queue, in attrsInput) error {
	check := func(v *int, lo, hi int, name string, dst *int) error {
		if v == nil {
			return nil
		}
		if *v < lo || *v > hi {
			return attrErr("Invalid value for the parameter %s. Reason: must be between %d and %d.", name, lo, hi)
		}
		*dst = *v
		return nil
	}
	for _, e := range []error{
		check(in.VisibilityTimeout, 0, 43200, "VisibilityTimeout", &q.VisibilityTimeout),
		check(in.MessageRetention, 60, 1209600, "MessageRetentionPeriod", &q.MessageRetention),
		check(in.DelaySeconds, 0, 900, "DelaySeconds", &q.DelaySeconds),
		check(in.ReceiveWaitTime, 0, 20, "ReceiveMessageWaitTimeSeconds", &q.ReceiveWaitTime),
		check(in.MaxMessageSize, 1024, maxMessageSize, "MaximumMessageSize", &q.MaxMessageSize),
		check(in.KmsDataKeyReusePeriod, 60, 86400, "KmsDataKeyReusePeriodSeconds", &q.KmsDataKeyReusePeriod),
	} {
		if e != nil {
			return e
		}
	}
	fifoOnly := func(set bool, name string) error {
		if set && !q.FIFO {
			return core.Errf(http.StatusBadRequest, "InvalidAttributeName", "Unknown Attribute %s.", name)
		}
		return nil
	}
	for _, e := range []error{fifoOnly(in.ContentBasedDeduplication != nil && *in.ContentBasedDeduplication, "ContentBasedDeduplication"),
		fifoOnly(in.DeduplicationScope != nil, "DeduplicationScope"), fifoOnly(in.FifoThroughputLimit != nil, "FifoThroughputLimit")} {
		if e != nil {
			return e
		}
	}
	if in.ContentBasedDeduplication != nil {
		q.ContentBasedDeduplication = *in.ContentBasedDeduplication
	}
	if in.DeduplicationScope != nil {
		if *in.DeduplicationScope != "queue" && *in.DeduplicationScope != "messageGroup" {
			return attrErr("Invalid value for the parameter DeduplicationScope.")
		}
		q.DeduplicationScope = *in.DeduplicationScope
	}
	if in.FifoThroughputLimit != nil {
		if *in.FifoThroughputLimit != "perQueue" && *in.FifoThroughputLimit != "perMessageGroupId" {
			return attrErr("Invalid value for the parameter FifoThroughputLimit.")
		}
		q.FifoThroughputLimit = *in.FifoThroughputLimit
	}
	if in.Policy != nil {
		q.Policy = ""
		if *in.Policy != "" {
			p, err := compactJSON("Policy", *in.Policy)
			if err != nil {
				return err
			}
			q.Policy = p
		}
	}
	if in.RedriveAllowPolicy != nil {
		q.RedriveAllowPolicy = ""
		if *in.RedriveAllowPolicy != "" {
			if _, err := parseRedriveAllow(*in.RedriveAllowPolicy); err != nil {
				return err
			}
			p, _ := compactJSON("RedriveAllowPolicy", *in.RedriveAllowPolicy)
			q.RedriveAllowPolicy = p
		}
	}
	if in.KmsMasterKeyID != nil {
		q.KmsMasterKeyID = *in.KmsMasterKeyID
		if q.KmsMasterKeyID != "" {
			f := false
			q.SqsManagedSSE = &f
			if q.KmsDataKeyReusePeriod == 0 {
				q.KmsDataKeyReusePeriod = 300
			}
		}
	}
	if in.SqsManagedSSE != nil {
		v := *in.SqsManagedSSE
		q.SqsManagedSSE = &v
		if v {
			q.KmsMasterKeyID, q.KmsDataKeyReusePeriod = "", 0
		}
	}
	if in.Redrive != nil {
		if in.Redrive.DeadLetterQueue == "" {
			q.Redrive = nil
		} else {
			dlq, err := s.getQueue(in.Redrive.DeadLetterQueue)
			if err != nil {
				return attrErr("Value %s for parameter RedrivePolicy is invalid. Reason: Dead letter target does not exist.", in.Redrive.DeadLetterQueue)
			}
			if dlq.FIFO != q.FIFO || dlq.Name == q.Name {
				return attrErr("Value for parameter RedrivePolicy is invalid. Reason: Dead-letter queue must be a different queue of the same type (standard or FIFO).")
			}
			if in.Redrive.MaxReceiveCount < 1 || in.Redrive.MaxReceiveCount > 1000 {
				return attrErr("Value for parameter RedrivePolicy is invalid. Reason: Invalid value for maxReceiveCount: %d, valid values are from 1 to 1000 both inclusive.", in.Redrive.MaxReceiveCount)
			}
			if dlq.RedriveAllowPolicy != "" {
				p, _ := parseRedriveAllow(dlq.RedriveAllowPolicy)
				if p.RedrivePermission == "denyAll" || (p.RedrivePermission == "byQueue" && !containsARN(p.SourceQueueArns, q.ARN)) {
					return attrErr("Value for parameter RedrivePolicy is invalid. Reason: Queue %s does not allow %s as a source queue (RedriveAllowPolicy).", dlq.ARN, q.ARN)
				}
			}
			if err := authorize("sqs:SendMessage", dlq.ARN); err != nil {
				return err
			}
			r := *in.Redrive
			q.Redrive = &r
		}
	}
	if in.Tags != nil {
		if len(in.Tags) > 50 {
			return invalid("Too many tags: a queue can have at most 50.")
		}
		q.Tags = in.Tags
	}
	return nil
}

func containsARN(list []string, arn string) bool {
	for _, a := range list {
		if core.CanonicalARN(a) == arn {
			return true
		}
	}
	return false
}

// createQueue creates a queue, or returns the existing one if its attributes
// match (created is false). A queue with different attributes is an error.
func (s *Service) createQueue(authorize authorizer, name string, fifo bool, in attrsInput) (q Queue, created bool, err error) {
	base := strings.TrimSuffix(name, ".fifo")
	if !nameRe.MatchString(base) {
		return q, false, invalid("Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length")
	}
	if fifo != strings.HasSuffix(name, ".fifo") {
		if fifo {
			return q, false, invalid("The name of a FIFO queue can only include alphanumeric characters, hyphens, or underscores, must end with .fifo suffix and be 1 to 80 in length.")
		}
		return q, false, invalid("Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length")
	}
	if existing, err := store.Get[Queue](s.env.Store, cQueues, name); err == nil {
		cp := existing
		in.Tags = nil // AWS ignores tags when the queue already exists
		if err := s.applyAttrs(func(string, string) error { return nil }, &cp, in); err != nil {
			return q, false, err
		}
		if !sameQueue(cp, existing) {
			return q, false, core.Errf(http.StatusBadRequest, "QueueNameExists", "A queue already exists with the same name and a different value for attribute(s)")
		}
		return existing, false, nil
	}
	now := core.Now()
	q = Queue{Name: name, ARN: s.env.ARN("sqs", name), URL: s.queueURL(name), FIFO: fifo,
		VisibilityTimeout: 30, MessageRetention: 345600, MaxMessageSize: maxBodyBytes, CreatedAt: now, LastModified: now}
	if fifo {
		q.DeduplicationScope, q.FifoThroughputLimit = "queue", "perQueue"
	}
	if err := s.applyAttrs(authorize, &q, in); err != nil {
		return q, false, err
	}
	if q.SqsManagedSSE == nil {
		t := q.KmsMasterKeyID == ""
		q.SqsManagedSSE = &t
	}
	s.mu.Lock()
	s.queues[q.Name] = newState()
	s.mu.Unlock()
	return q, true, store.Put(s.env.Store, cQueues, q.Name, q)
}

func sameQueue(a, b Queue) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// setQueueAttributes updates a queue's attributes.
func (s *Service) setQueueAttributes(authorize authorizer, name string, in attrsInput) (Queue, error) {
	q, err := s.getQueue(name)
	if err != nil {
		return q, err
	}
	if err := s.applyAttrs(authorize, &q, in); err != nil {
		return q, err
	}
	q.LastModified = core.Now()
	return q, store.Put(s.env.Store, cQueues, q.Name, q)
}

// deleteQueue removes a queue and its messages.
func (s *Service) deleteQueue(name string) error {
	if _, err := s.getQueue(name); err != nil {
		return err
	}
	s.mu.Lock()
	if st := s.queues[name]; st != nil {
		st.wake()
	}
	delete(s.queues, name)
	s.mu.Unlock()
	_ = os.Remove(s.file(name))
	return store.Delete(s.env.Store, cQueues, name)
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:sqs:{region}:{account}:{name}")
	r.Handle("GET /api/v1/sqs/queues", "sqs:ListQueues", s.list)
	r.Handle("POST /api/v1/sqs/queues", "sqs:CreateQueue", s.create)
	r.Handle("GET /api/v1/sqs/queues/{name}", "sqs:GetQueueAttributes", s.get, res)
	r.Handle("PATCH /api/v1/sqs/queues/{name}", "sqs:SetQueueAttributes", s.update, res)
	r.Handle("DELETE /api/v1/sqs/queues/{name}", "sqs:DeleteQueue", s.nativeDeleteQueue, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/purge", "sqs:PurgeQueue", s.purge, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/messages", "sqs:SendMessage", s.send, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/messages/receive", "sqs:ReceiveMessage", s.receive, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/messages/delete", "sqs:DeleteMessage", s.deleteMessages, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/messages/visibility", "sqs:ChangeMessageVisibility", s.changeVisibility, res)
	r.Handle("GET /api/v1/sqs/queues/{name}/messages/peek", "sqs:ReceiveMessage", s.peek, res)
	r.Handle("POST /api/v1/sqs/queues/{name}/redrive", "sqs:StartMessageMoveTask", s.redrive, res)
}

func (s *Service) view(q Queue) map[string]any {
	visible, inflight, delayed := s.counts(q.Name)
	var stats [3]int64
	s.mu.Lock()
	if st := s.queues[q.Name]; st != nil {
		stats = [3]int64{st.stats.sent, st.stats.received, st.stats.deleted}
	}
	s.mu.Unlock()
	sources := []string{}
	for _, o := range s.deadLetterSources(q.Name) {
		sources = append(sources, o.Name)
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

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in struct {
		Name string `json:"name"`
		FIFO bool   `json:"fifo"`
		attrsInput
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if store.Has(s.env.Store, cQueues, in.Name) {
		return nil, core.Errf(http.StatusConflict, "QueueAlreadyExists", "queue %q already exists", in.Name)
	}
	q, _, err := s.createQueue(c.Authorize, in.Name, in.FIFO, in.attrsInput)
	if err != nil {
		return nil, err
	}
	return s.view(q), nil
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
	q, err := s.setQueueAttributes(c.Authorize, c.Param("name"), in)
	if err != nil {
		return nil, err
	}
	return s.view(q), nil
}

func (s *Service) nativeDeleteQueue(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if _, err := s.getQueue(name); err != nil {
		return nil, err
	}
	for _, o := range s.deadLetterSources(name) {
		return nil, core.Conflict("queue %q is the dead-letter queue of %q; remove that redrive policy first", name, o.Name)
	}
	return nil, s.deleteQueue(name)
}

func (s *Service) purge(c *httpx.Ctx) (any, error) {
	return nil, s.Purge(c.Param("name"))
}

func principalID(p *httpx.Principal) string {
	if p == nil {
		return ""
	}
	if p.RoleName != "" {
		return p.AccessKey + ":" + p.SessionName
	}
	if p.AccessKey != "" {
		return p.AccessKey
	}
	return p.UserName
}

func (s *Service) send(c *httpx.Ctx) (any, error) {
	var in struct {
		SendInput
		Entries []SendInput `json:"entries"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	sender := principalID(c.P)
	if len(in.Entries) == 0 {
		in.SenderID = sender
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
		e.SenderID = sender
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
	return nil, s.ChangeVisibility(c.Param("name"), in.ReceiptHandle, in.VisibilityTimeout)
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
		for _, q := range s.deadLetterSources(name) {
			if err := c.Authorize("sqs:SendMessage", q.ARN); err != nil {
				return nil, err
			}
		}
	}
	moved := s.moveMessages(name, in.Destination, -1, false)
	return map[string]int{"moved": moved}, nil
}

// moveMessages moves up to limit (-1: all) messages from src to dest, or to
// each message's source queue when dest is "". onlyVisible skips in-flight and
// delayed messages (message move tasks); the native redrive moves everything.
func (s *Service) moveMessages(src, dest string, limit int, onlyVisible bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	from := s.queues[src]
	if from == nil {
		return 0
	}
	now := time.Now()
	moved := 0
	kept := from.msgs[:0]
	woken := map[*queueState]bool{}
	for _, m := range from.msgs {
		d := dest
		if d == "" && m.SourceQueue != "" {
			d = NameFromARN(m.SourceQueue)
		}
		var to *queueState
		if d != "" && d != src && store.Has(s.env.Store, cQueues, d) {
			to = s.state(d)
		}
		if to == nil || (limit >= 0 && moved >= limit) || (onlyVisible && m.VisibleAt.After(now)) {
			kept = append(kept, m)
			continue
		}
		m.SourceQueue, m.ReceiveCount, m.Receipt, m.VisibleAt, m.FirstReceive = "", 0, "", now, time.Time{}
		to.msgs = append(to.msgs, m)
		woken[to] = true
		moved++
	}
	clear(from.msgs[len(kept):])
	from.msgs = kept
	for st := range woken {
		st.wake()
	}
	if moved > 0 {
		s.dirty = true
	}
	return moved
}
