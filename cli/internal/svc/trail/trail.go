// Package trail implements CloudTrail: an append-only audit log of every
// mutating API call, plus any call that was denied. The native API and the AWS
// protocol layer (aws.go) read the same log through Search, and trails
// (trails.go) deliver it to S3 in the CloudTrail file layout.
package trail

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const retentionDays = 90

type Event struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	User      string    `json:"user"`
	UserARN   string    `json:"user_arn"`
	AccessKey string    `json:"access_key,omitempty"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	SourceIP  string    `json:"source_ip"`
	UserAgent string    `json:"user_agent"`
	LatencyMS int64     `json:"latency_ms"`
	// Set for calls made over the AWS protocol.
	RequestID    string `json:"request_id,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type Service struct {
	env *svc.Env
	dir string
	mu  sync.Mutex

	// Deliver stores a log file in an S3 bucket; BucketExists checks the bucket
	// (both are wired to S3 by the server; nil disables delivery).
	Deliver      func(bucket, key string, body []byte) error
	BucketExists func(bucket string) (bool, error)
	dmu          sync.Mutex
}

func New(env *svc.Env) (*Service, error) {
	dir := env.Cfg.Path("trail")
	return &Service{env: env, dir: dir}, os.MkdirAll(dir, 0o700)
}

func (s *Service) file(t time.Time) string {
	return filepath.Join(s.dir, t.UTC().Format("2006-01-02")+".jsonl")
}

// Record is the httpx.AuditFunc.
func (s *Service) Record(p *httpx.Principal, action, resource string, r *http.Request, status int, took time.Duration) {
	if r.Method == http.MethodGet && status != http.StatusForbidden {
		return
	}
	e := Event{ID: core.RandHex(16), Time: time.Now().UTC(), User: p.UserName, UserARN: p.ARN, AccessKey: p.AccessKey,
		Action: action, Resource: resource, Method: r.Method, Path: r.URL.Path, Status: status,
		SourceIP: httpx.ClientIP(r), UserAgent: r.UserAgent(), LatencyMS: took.Milliseconds()}
	if in := awsapi.AuditInfo(r); in != nil {
		e.RequestID, e.ErrorCode, e.ErrorMessage = in.RequestID, in.ErrorCode, in.ErrorMessage
	}
	b, _ := json.Marshal(e)
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.file(e.Time), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// Prune deletes trail files older than the retention period.
func (s *Service) Prune() {
	entries, _ := os.ReadDir(s.dir)
	cut := time.Now().AddDate(0, 0, -retentionDays).Format("2006-01-02")
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") && strings.TrimSuffix(e.Name(), ".jsonl") < cut {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

func (s *Service) Routes(r *httpx.Router) {
	r.Handle("GET /api/v1/cloudtrail/events", "cloudtrail:LookupEvents", s.lookup)
}

// Filter selects events; zero fields match everything.
type Filter struct {
	User         string // exact user name
	Action       string // substring of "service:Action", case-insensitive
	Q            string // substring of action, resource or user
	OnlyErrors   bool
	EventID      string
	EventName    string
	EventSource  string
	ResourceName string
	ResourceType string
	AccessKey    string
	ReadOnly     *bool
	Start, End   time.Time // inclusive bounds
}

func (f Filter) match(e Event) bool {
	if f.User != "" && e.User != f.User {
		return false
	}
	if f.Action != "" && !strings.Contains(strings.ToLower(e.Action), strings.ToLower(f.Action)) {
		return false
	}
	if f.OnlyErrors && e.Status < 400 {
		return false
	}
	if f.Q != "" && !strings.Contains(strings.ToLower(e.Action+" "+e.Resource+" "+e.User), strings.ToLower(f.Q)) {
		return false
	}
	if f.EventID != "" && e.ID != f.EventID {
		return false
	}
	if f.EventName != "" && e.Name() != f.EventName {
		return false
	}
	if f.EventSource != "" && e.Source() != f.EventSource {
		return false
	}
	if f.AccessKey != "" && e.AccessKey != f.AccessKey {
		return false
	}
	if f.ReadOnly != nil && e.ReadOnly() != *f.ReadOnly {
		return false
	}
	if f.ResourceName != "" || f.ResourceType != "" {
		ok := false
		for _, r := range e.Resources() {
			if (f.ResourceName == "" || r.Name == f.ResourceName || r.ARN == f.ResourceName || lastSegment(r.ARN) == f.ResourceName) &&
				(f.ResourceType == "" || r.Type == f.ResourceType) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if !f.Start.IsZero() && e.Time.Before(f.Start) {
		return false
	}
	if !f.End.IsZero() && e.Time.After(f.End) {
		return false
	}
	return true
}

// ErrBadToken reports a NextToken that does not belong to the log.
var ErrBadToken = core.BadRequest("invalid next token")

// Search returns up to limit matching events, newest first, starting after the
// event named by token (from a previous call). next is empty on the last page.
func (s *Service) Search(f Filter, token string, limit int) (out []Event, next string, err error) {
	if limit < 1 {
		limit = 200
	}
	after := ""
	if token != "" {
		b, derr := base64.RawURLEncoding.DecodeString(token)
		if derr != nil || len(b) == 0 {
			return nil, "", ErrBadToken
		}
		after = string(b)
	}
	entries, _ := os.ReadDir(s.dir)
	names := []string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(e.Name(), ".jsonl")
		if (!f.Start.IsZero() && day < f.Start.UTC().Format("2006-01-02")) || (!f.End.IsZero() && day > f.End.UTC().Format("2006-01-02")) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	seen := after == ""
	for _, n := range names {
		fh, ferr := os.Open(filepath.Join(s.dir, n))
		if ferr != nil {
			continue
		}
		var day []Event
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				day = append(day, e)
			}
		}
		fh.Close()
		for i := len(day) - 1; i >= 0; i-- {
			e := day[i]
			if !seen {
				seen = e.ID == after
				continue
			}
			if !f.match(e) {
				continue
			}
			if len(out) == limit {
				return out, base64.RawURLEncoding.EncodeToString([]byte(out[limit-1].ID)), nil
			}
			out = append(out, e)
		}
	}
	if !seen {
		return nil, "", ErrBadToken
	}
	return out, "", nil
}

func (s *Service) lookup(c *httpx.Ctx) (any, error) {
	f := Filter{User: c.Query("user"), Action: c.Query("action"), Q: c.Query("q"), OnlyErrors: c.Query("errors") == "true"}
	out, _, err := s.Search(f, "", c.QueryInt("limit", 200))
	if out == nil {
		out = []Event{}
	}
	return out, err
}
