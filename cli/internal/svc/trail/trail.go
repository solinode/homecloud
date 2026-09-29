// Package trail implements CloudTrail: an append-only audit log of every
// mutating API call, plus any call that was denied.
package trail

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
}

type Service struct {
	env *svc.Env
	dir string
	mu  sync.Mutex
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
		if strings.TrimSuffix(e.Name(), ".jsonl") < cut {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

func (s *Service) Routes(r *httpx.Router) {
	r.Handle("GET /api/v1/cloudtrail/events", "cloudtrail:LookupEvents", s.lookup)
}

func (s *Service) lookup(c *httpx.Ctx) (any, error) {
	limit := c.QueryInt("limit", 200)
	user, action, q := c.Query("user"), strings.ToLower(c.Query("action")), strings.ToLower(c.Query("q"))
	onlyErrors := c.Query("errors") == "true"
	entries, _ := os.ReadDir(s.dir)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := []Event{}
	for _, n := range names {
		f, err := os.Open(filepath.Join(s.dir, n))
		if err != nil {
			continue
		}
		var day []Event
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var e Event
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			if (user != "" && e.User != user) || (action != "" && !strings.Contains(strings.ToLower(e.Action), action)) ||
				(onlyErrors && e.Status < 400) ||
				(q != "" && !strings.Contains(strings.ToLower(e.Action+" "+e.Resource+" "+e.User), q)) {
				continue
			}
			day = append(day, e)
		}
		f.Close()
		for i := len(day) - 1; i >= 0 && len(out) < limit; i-- {
			out = append(out, day[i])
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
