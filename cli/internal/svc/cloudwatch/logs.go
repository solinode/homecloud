package cloudwatch

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const cLogGroups = "logs_groups"

// Container-backed log groups are virtual: "/hc/<service>/<resource>" reads the
// resource's container output straight from Docker.
const containerGroupPrefix = "/hc/"

type LogGroup struct {
	Name          string    `json:"name"`
	ARN           string    `json:"arn"`
	RetentionDays int       `json:"retention_days"` // 0 = never expire
	CreatedAt     time.Time `json:"created_at"`
	Source        string    `json:"source"` // "stored" | "container"
	StoredBytes   int64     `json:"stored_bytes"`
}

type LogStream struct {
	Name          string    `json:"name"`
	LastEventTime time.Time `json:"last_event_time"`
	StoredBytes   int64     `json:"stored_bytes"`
}

type LogEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	Stream    string    `json:"stream"`
}

type logStore struct {
	env *svc.Env
	mu  sync.Mutex
	dir string
}

func newLogStore(env *svc.Env) (*logStore, error) {
	dir := env.Cfg.Path("logs")
	return &logStore{env: env, dir: dir}, os.MkdirAll(dir, 0o700)
}

// File names are hex-encoded so no group or stream name ("..", "/", ...) can
// escape the logs directory.
func (l *logStore) groupDir(group string) string {
	return filepath.Join(l.dir, "g-"+hex.EncodeToString([]byte(group)))
}

func (l *logStore) streamFile(group, stream string) string {
	return filepath.Join(l.groupDir(group), hex.EncodeToString([]byte(stream))+".jsonl")
}

const maxEventBytes = 256 << 10

var (
	groupNameRe  = regexp.MustCompile(`^[\.\-_/#A-Za-z0-9]{1,512}$`)
	streamNameRe = regexp.MustCompile(`^[^:*]{1,512}$`)
)

func validGroup(name string) error {
	if !groupNameRe.MatchString(name) || name == "." || name == ".." {
		return core.BadRequest("log group names are 1-512 of letters, digits and . - _ / #, and may not be . or ..")
	}
	return nil
}

func validStream(name string) error {
	if !streamNameRe.MatchString(name) || strings.ContainsAny(name, "\x00\n\r") {
		return core.BadRequest("log stream names are 1-512 characters without : or *")
	}
	return nil
}

type rawEvent struct {
	T int64  `json:"t"`
	M string `json:"m"`
}

// Append writes events to group/stream, creating the group if needed.
func (s *Service) Append(group, stream string, events ...LogEvent) error {
	if err := validGroup(group); err != nil {
		return err
	}
	if err := validStream(stream); err != nil {
		return err
	}
	l := s.logs
	l.mu.Lock()
	defer l.mu.Unlock()
	if !store.Has(s.env.Store, cLogGroups, group) { // checked under l.mu, so creation cannot race
		g := LogGroup{Name: group, ARN: s.env.ARN("logs", "log-group:"+group), CreatedAt: core.Now(), Source: "stored"}
		if err := store.Put(s.env.Store, cLogGroups, group, g); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(l.groupDir(group), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.streamFile(group, stream), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, e := range events {
		if e.Timestamp.IsZero() {
			e.Timestamp = time.Now()
		}
		if len(e.Message) > maxEventBytes {
			e.Message = e.Message[:maxEventBytes] + " [truncated]"
		}
		b, _ := json.Marshal(rawEvent{T: e.Timestamp.UnixMilli(), M: e.Message})
		w.Write(b)
		w.WriteByte('\n')
	}
	return w.Flush()
}

func (l *logStore) streams(group string) []LogStream {
	out := []LogStream{}
	entries, err := os.ReadDir(l.groupDir(group))
	if err != nil {
		return out
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		nb, err := hex.DecodeString(name)
		if err != nil {
			continue
		}
		n := string(nb)
		out = append(out, LogStream{Name: n, LastEventTime: info.ModTime().UTC(), StoredBytes: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastEventTime.After(out[j].LastEventTime) })
	return out
}

func (l *logStore) read(group, stream string, start, end time.Time, filter string, limit int) []LogEvent {
	var names []string
	if stream != "" {
		names = []string{stream}
	} else {
		for _, st := range l.streams(group) {
			names = append(names, st.Name)
		}
	}
	var out []LogEvent
	for _, n := range names {
		f, err := os.Open(l.streamFile(group, n))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 2*maxEventBytes)
		for sc.Scan() {
			var r rawEvent
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			t := time.UnixMilli(r.T).UTC()
			if (!start.IsZero() && t.Before(start)) || (!end.IsZero() && t.After(end)) {
				continue
			}
			if filter != "" && !strings.Contains(strings.ToLower(r.M), strings.ToLower(filter)) {
				continue
			}
			out = append(out, LogEvent{Timestamp: t, Message: r.M, Stream: n})
		}
		if err := sc.Err(); err != nil {
			log.Printf("logs: read %s/%s: %v", group, n, err)
		}
		f.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (l *logStore) enforceRetention() {
	for _, g := range store.List[LogGroup](l.env.Store, cLogGroups) {
		if g.RetentionDays <= 0 {
			continue
		}
		cut := time.Now().Add(-time.Duration(g.RetentionDays) * 24 * time.Hour)
		for _, st := range l.streams(g.Name) {
			if st.LastEventTime.Before(cut) {
				_ = os.Remove(l.streamFile(g.Name, st.Name))
				continue
			}
			l.trim(g.Name, st.Name, cut)
		}
	}
}

// trim rewrites a stream without the events older than cut.
func (l *logStore) trim(group, stream string, cut time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	path := l.streamFile(group, stream)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var keep bytes.Buffer
	dropped := false
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r rawEvent
		if json.Unmarshal(line, &r) == nil && time.UnixMilli(r.T).Before(cut) {
			dropped = true
			continue
		}
		keep.Write(line)
		keep.WriteByte('\n')
	}
	if !dropped {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, keep.Bytes(), 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// DeleteGroup removes a stored log group and its events.
func (s *Service) DeleteGroup(name string) {
	_ = store.Delete(s.env.Store, cLogGroups, name)
	_ = os.RemoveAll(s.logs.groupDir(name))
}

// containerGroups lists virtual log groups for running HomeCloud containers.
func (s *Service) containerGroups() []LogGroup {
	out := []LogGroup{}
	cs, err := s.env.Docker.ManagedContainers()
	if err != nil {
		return out
	}
	for _, c := range cs {
		svcName, res := c.Labels[core.LabelService], c.Labels[core.LabelResource]
		if svcName == "" || res == "" || svcName == "lambda" {
			continue
		}
		name := containerGroupPrefix + svcName + "/" + res
		out = append(out, LogGroup{Name: name, ARN: s.env.ARN("logs", "log-group:"+name), CreatedAt: time.Unix(c.Created, 0).UTC(), Source: "container"})
	}
	return out
}

func (s *Service) containerFor(group string) string {
	rest, ok := strings.CutPrefix(group, containerGroupPrefix)
	if !ok {
		return ""
	}
	svcName, res, _ := strings.Cut(rest, "/")
	cs, err := s.env.Docker.ManagedContainers()
	if err != nil {
		return ""
	}
	for _, c := range cs {
		if c.Labels[core.LabelService] == svcName && c.Labels[core.LabelResource] == res {
			return c.ID
		}
	}
	return ""
}

// ContainerEvents parses `docker logs --timestamps` output into events.
func ContainerEvents(raw, stream string) []LogEvent {
	var out []LogEvent
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		ts, msg, _ := strings.Cut(line, " ")
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			t, msg = time.Time{}, line
		}
		out = append(out, LogEvent{Timestamp: t.UTC(), Message: msg, Stream: stream})
	}
	return out
}

func (s *Service) logRoutes(r *httpx.Router) {
	res := httpx.Res("arn:aws:logs:{region}:{account}:log-group:{group}")
	r.Handle("GET /api/v1/logs/groups", "logs:DescribeLogGroups", s.listGroups)
	r.Handle("POST /api/v1/logs/groups", "logs:CreateLogGroup", s.createGroup)
	r.Handle("DELETE /api/v1/logs/groups/{group}", "logs:DeleteLogGroup", s.deleteGroup, res)
	r.Handle("PUT /api/v1/logs/groups/{group}/retention", "logs:PutRetentionPolicy", s.putRetention, res)
	r.Handle("GET /api/v1/logs/groups/{group}/streams", "logs:DescribeLogStreams", s.listStreams, res)
	r.Handle("GET /api/v1/logs/groups/{group}/events", "logs:FilterLogEvents", s.events, res)
	r.Handle("POST /api/v1/logs/groups/{group}/streams/{stream}/events", "logs:PutLogEvents", s.putEvents, res)
}

func (s *Service) listGroups(c *httpx.Ctx) (any, error) {
	out := []LogGroup{}
	for _, g := range store.List[LogGroup](s.env.Store, cLogGroups) {
		for _, st := range s.logs.streams(g.Name) {
			g.StoredBytes += st.StoredBytes
		}
		out = append(out, g)
	}
	out = append(out, s.containerGroups()...)
	if p := c.Query("prefix"); p != "" {
		filtered := out[:0]
		for _, g := range out {
			if strings.HasPrefix(g.Name, p) {
				filtered = append(filtered, g)
			}
		}
		out = filtered
	}
	return out, nil
}

func (s *Service) createGroup(c *httpx.Ctx) (any, error) {
	var in struct {
		Name          string `json:"name"`
		RetentionDays int    `json:"retention_days"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := validGroup(in.Name); err != nil {
		return nil, err
	}
	if strings.HasPrefix(in.Name, containerGroupPrefix) {
		return nil, core.BadRequest("log group names may not start with the reserved prefix %s", containerGroupPrefix)
	}
	s.logs.mu.Lock()
	defer s.logs.mu.Unlock()
	if store.Has(s.env.Store, cLogGroups, in.Name) {
		return nil, core.Conflict("log group %q already exists", in.Name)
	}
	g := LogGroup{Name: in.Name, ARN: s.env.ARN("logs", "log-group:"+in.Name), RetentionDays: in.RetentionDays, CreatedAt: core.Now(), Source: "stored"}
	return g, store.Put(s.env.Store, cLogGroups, g.Name, g)
}

func (s *Service) deleteGroup(c *httpx.Ctx) (any, error) {
	name := c.Param("group")
	if !store.Has(s.env.Store, cLogGroups, name) {
		return nil, core.NotFound("log group", name)
	}
	s.DeleteGroup(name)
	return nil, nil
}

func (s *Service) putRetention(c *httpx.Ctx) (any, error) {
	var in struct {
		RetentionDays int `json:"retention_days"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.RetentionDays < 0 {
		return nil, core.BadRequest("retention_days must be 0 (never expire) or more")
	}
	g, err := store.Update(s.env.Store, cLogGroups, c.Param("group"), func(g *LogGroup) error { g.RetentionDays = in.RetentionDays; return nil })
	if err == store.ErrNotFound {
		return nil, core.NotFound("log group", c.Param("group"))
	}
	for _, st := range s.logs.streams(g.Name) {
		g.StoredBytes += st.StoredBytes
	}
	return g, err
}

func (s *Service) listStreams(c *httpx.Ctx) (any, error) {
	g := c.Param("group")
	if strings.HasPrefix(g, containerGroupPrefix) {
		return []LogStream{{Name: "stdout", LastEventTime: time.Now().UTC()}}, nil
	}
	if !store.Has(s.env.Store, cLogGroups, g) {
		return nil, core.NotFound("log group", g)
	}
	return s.logs.streams(g), nil
}

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC()
	}
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

// Events returns events for a group; also used by other services (e.g. Lambda logs).
func (s *Service) Events(group, stream string, start, end time.Time, filter string, limit int) ([]LogEvent, error) {
	if strings.HasPrefix(group, containerGroupPrefix) {
		id := s.containerFor(group)
		if id == "" {
			return nil, core.NotFound("log group", group)
		}
		// Filtering happens after reading, so read a larger window when filtering.
		tail := limit
		if filter != "" {
			tail = max(limit*20, 10000)
		}
		raw, err := s.env.Docker.Logs(id, tail, start)
		if err != nil {
			return nil, err
		}
		var out []LogEvent
		for _, e := range ContainerEvents(raw, "stdout") {
			if filter != "" && !strings.Contains(strings.ToLower(e.Message), strings.ToLower(filter)) {
				continue
			}
			if !end.IsZero() && e.Timestamp.After(end) {
				continue
			}
			out = append(out, e)
		}
		if limit > 0 && len(out) > limit {
			out = out[len(out)-limit:]
		}
		return out, nil
	}
	if !store.Has(s.env.Store, cLogGroups, group) {
		return nil, core.NotFound("log group", group)
	}
	return s.logs.read(group, stream, start, end, filter, limit), nil
}

func (s *Service) events(c *httpx.Ctx) (any, error) {
	ev, err := s.Events(c.Param("group"), c.Query("stream"), parseTime(c.Query("start")), parseTime(c.Query("end")), c.Query("filter"), c.QueryInt("limit", 1000))
	if err != nil {
		return nil, err
	}
	if ev == nil {
		ev = []LogEvent{}
	}
	return ev, nil
}

func (s *Service) putEvents(c *httpx.Ctx) (any, error) {
	var in struct {
		Events []LogEvent `json:"events"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	g := c.Param("group")
	if strings.HasPrefix(g, containerGroupPrefix) {
		return nil, core.BadRequest("container log groups are read-only")
	}
	if !store.Has(s.env.Store, cLogGroups, g) {
		return nil, core.NotFound("log group", g)
	}
	if len(in.Events) > 10000 {
		return nil, core.BadRequest("at most 10000 events per call")
	}
	return map[string]int{"accepted": len(in.Events)}, s.Append(g, c.Param("stream"), in.Events...)
}
