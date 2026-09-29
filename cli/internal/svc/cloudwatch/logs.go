package cloudwatch

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	KMSKeyID      string    `json:"kms_key_id,omitempty"`
	metricFilters int
	Class         string    `json:"log_group_class,omitempty"`
	Tags          core.Tags `json:"tags,omitempty"`
}

type LogStream struct {
	Name          string    `json:"name"`
	LastEventTime time.Time `json:"last_event_time"`
	StoredBytes   int64     `json:"stored_bytes"`
	CreatedAt     time.Time `json:"created_at"`
	// FirstEventTime and LastIngestionTime are zero for streams without events.
	FirstEventTime    time.Time `json:"first_event_time"`
	LastIngestionTime time.Time `json:"last_ingestion_time"`
	seq               int64
}

type LogEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	Stream    string    `json:"stream"`
	Ingestion time.Time `json:"-"`
	ID        string    `json:"-"`
}

// streamMeta is kept next to each stream file, so listing streams needs no scan.
type streamMeta struct {
	Created int64 `json:"created"`
	First   int64 `json:"first,omitempty"`
	Last    int64 `json:"last,omitempty"`
	Ingest  int64 `json:"ingest,omitempty"`
	Seq     int64 `json:"seq,omitempty"`
}

type logStore struct {
	env  *svc.Env
	mu   sync.Mutex
	dir  string
	meta map[string]*streamMeta // by stream file path
}

func newLogStore(env *svc.Env) (*logStore, error) {
	dir := env.Cfg.Path("logs")
	return &logStore{env: env, dir: dir, meta: map[string]*streamMeta{}}, os.MkdirAll(dir, 0o700)
}

// File names are hex-encoded so no group or stream name ("..", "/", ...) can
// escape the logs directory.
func (l *logStore) groupDir(group string) string {
	return filepath.Join(l.dir, "g-"+hex.EncodeToString([]byte(group)))
}

func (l *logStore) streamFile(group, stream string) string {
	return filepath.Join(l.groupDir(group), hex.EncodeToString([]byte(stream))+".jsonl")
}

func metaFile(streamFile string) string { return strings.TrimSuffix(streamFile, ".jsonl") + ".meta" }

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
	I int64  `json:"i,omitempty"` // ingestion time (ms)
	E string `json:"e,omitempty"` // event ID
}

var eventSeq atomic.Uint64

// newEventID returns a 56-digit event ID (like AWS's) that sorts by timestamp, then ingestion.
func newEventID(ts, ingest int64) string {
	return fmt.Sprintf("%020d%020d%016d", max(ts, 0), max(ingest, 0), eventSeq.Add(1)%1e16)
}

// metaLocked returns (and caches) a stream's metadata; l.mu must be held. For
// streams written before metadata existed it is rebuilt from the events.
func (l *logStore) metaLocked(group, stream string) (*streamMeta, bool) {
	path := l.streamFile(group, stream)
	if m := l.meta[path]; m != nil {
		return m, true
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	m := &streamMeta{}
	if b, err := os.ReadFile(metaFile(path)); err == nil && json.Unmarshal(b, m) == nil && m.Created > 0 {
		l.meta[path] = m
		return m, true
	}
	m.Created = info.ModTime().UnixMilli()
	for _, e := range l.readFile(path, stream) {
		t := e.Timestamp.UnixMilli()
		if m.First == 0 || t < m.First {
			m.First = t
		}
		m.Last = max(m.Last, t)
		m.Ingest = max(m.Ingest, e.Ingestion.UnixMilli())
		m.Created = min(m.Created, e.Ingestion.UnixMilli())
		m.Seq++
	}
	l.meta[path] = m
	l.saveMetaLocked(path, m)
	return m, true
}

func (l *logStore) saveMetaLocked(path string, m *streamMeta) {
	b, _ := json.Marshal(m)
	tmp := metaFile(path) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, metaFile(path))
	}
}

// createStreamLocked makes an empty stream; l.mu must be held.
func (l *logStore) createStreamLocked(group, stream string) error {
	if err := os.MkdirAll(l.groupDir(group), 0o700); err != nil {
		return err
	}
	path := l.streamFile(group, stream)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	m := &streamMeta{Created: time.Now().UnixMilli()}
	l.meta[path] = m
	l.saveMetaLocked(path, m)
	return nil
}

func (l *logStore) streamExists(group, stream string) bool {
	_, err := os.Stat(l.streamFile(group, stream))
	return err == nil
}

// appendLocked writes events to a stream (creating it) and returns the
// stream's new sequence number and the events as stored.
func (l *logStore) appendLocked(group, stream string, events []LogEvent) (int64, []LogEvent, error) {
	if err := os.MkdirAll(l.groupDir(group), 0o700); err != nil {
		return 0, nil, err
	}
	path := l.streamFile(group, stream)
	m, ok := l.metaLocked(group, stream)
	if !ok {
		m = &streamMeta{Created: time.Now().UnixMilli()}
		l.meta[path] = m
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	now := time.Now().UnixMilli()
	stored := make([]LogEvent, 0, len(events))
	for _, e := range events {
		if e.Timestamp.IsZero() {
			e.Timestamp = time.Now()
		}
		if len(e.Message) > maxEventBytes {
			e.Message = e.Message[:maxEventBytes] + " [truncated]"
		}
		t := e.Timestamp.UnixMilli()
		id := newEventID(t, now)
		stored = append(stored, LogEvent{Timestamp: time.UnixMilli(t).UTC(), Message: e.Message, Stream: stream, Ingestion: time.UnixMilli(now).UTC(), ID: id})
		b, _ := json.Marshal(rawEvent{T: t, M: e.Message, I: now, E: id})
		w.Write(b)
		w.WriteByte('\n')
		if m.First == 0 || t < m.First {
			m.First = t
		}
		m.Last = max(m.Last, t)
	}
	if err := w.Flush(); err != nil {
		return 0, nil, err
	}
	if len(events) > 0 {
		m.Ingest = now
		m.Seq++
	}
	l.saveMetaLocked(path, m)
	return m.Seq, stored, nil
}

// Append writes events to group/stream, creating the group and stream if needed.
func (s *Service) Append(group, stream string, events ...LogEvent) error {
	if err := validGroup(group); err != nil {
		return err
	}
	if err := validStream(stream); err != nil {
		return err
	}
	l := s.logs
	l.mu.Lock()
	if !store.Has(s.env.Store, cLogGroups, group) { // checked under l.mu, so creation cannot race
		g := LogGroup{Name: group, ARN: s.env.ARN("logs", "log-group:"+group), CreatedAt: core.Now(), Source: "stored"}
		if err := store.Put(s.env.Store, cLogGroups, group, g); err != nil {
			l.mu.Unlock()
			return err
		}
	}
	_, stored, err := l.appendLocked(group, stream, events)
	l.mu.Unlock()
	if err == nil {
		s.applyFilters(group, stream, stored)
	}
	return err
}

func (l *logStore) streams(group string) []LogStream {
	out := []LogStream{}
	entries, err := os.ReadDir(l.groupDir(group))
	if err != nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
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
		st := LogStream{Name: n, StoredBytes: info.Size(), LastEventTime: info.ModTime().UTC(), CreatedAt: info.ModTime().UTC()}
		if m, ok := l.metaLocked(group, n); ok {
			st.CreatedAt = time.UnixMilli(m.Created).UTC()
			st.LastEventTime = msTime(m.Last)
			st.FirstEventTime = msTime(m.First)
			st.LastIngestionTime = msTime(m.Ingest)
			st.seq = m.Seq
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastEventTime.After(out[j].LastEventTime) })
	return out
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// readFile returns every event of a stream file in file order.
func (l *logStore) readFile(path, stream string) []LogEvent {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []LogEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 2*maxEventBytes)
	line := 0
	for sc.Scan() {
		line++
		var r rawEvent
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if r.I == 0 {
			r.I = r.T
		}
		if r.E == "" { // events written before IDs existed
			r.E = fmt.Sprintf("%020d%020d%016d", max(r.T, 0), 0, line)
		}
		out = append(out, LogEvent{Timestamp: time.UnixMilli(r.T).UTC(), Message: r.M, Stream: stream, Ingestion: time.UnixMilli(r.I).UTC(), ID: r.E})
	}
	if err := sc.Err(); err != nil {
		log.Printf("logs: read %s: %v", path, err)
	}
	return out
}

// readStreams returns the events of the named streams (every stream when nil)
// between start and end (inclusive, zero = unbounded) that match, sorted by
// timestamp, then event ID. Events older than cut (retention) are hidden.
func (l *logStore) readStreams(group string, names []string, start, end, cut time.Time, match func(LogEvent) bool) []LogEvent {
	if names == nil {
		for _, st := range l.streams(group) {
			names = append(names, st.Name)
		}
	}
	var out []LogEvent
	for _, n := range names {
		for _, e := range l.readFile(l.streamFile(group, n), n) {
			if (!start.IsZero() && e.Timestamp.Before(start)) || (!end.IsZero() && e.Timestamp.After(end)) || (!cut.IsZero() && e.Timestamp.Before(cut)) {
				continue
			}
			if match != nil && !match(e) {
				continue
			}
			out = append(out, e)
		}
	}
	sortEvents(out)
	return out
}

func sortEvents(ev []LogEvent) {
	sort.SliceStable(ev, func(i, j int) bool {
		if !ev[i].Timestamp.Equal(ev[j].Timestamp) {
			return ev[i].Timestamp.Before(ev[j].Timestamp)
		}
		return ev[i].ID < ev[j].ID
	})
}

func containsFold(filter string) func(LogEvent) bool {
	if filter == "" {
		return nil
	}
	f := strings.ToLower(filter)
	return func(e LogEvent) bool { return strings.Contains(strings.ToLower(e.Message), f) }
}

func (l *logStore) read(group, stream string, start, end time.Time, filter string, limit int, cut time.Time) []LogEvent {
	var names []string
	if stream != "" {
		names = []string{stream}
	}
	out := l.readStreams(group, names, start, end, cut, containsFold(filter))
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func retentionCut(g LogGroup) time.Time {
	if g.RetentionDays <= 0 {
		return time.Time{}
	}
	return time.Now().Add(-time.Duration(g.RetentionDays) * 24 * time.Hour)
}

func (l *logStore) enforceRetention() {
	for _, g := range store.List[LogGroup](l.env.Store, cLogGroups) {
		cut := retentionCut(g)
		if cut.IsZero() {
			continue
		}
		for _, st := range l.streams(g.Name) {
			l.trim(g.Name, st.Name, cut)
		}
	}
}

// trim rewrites a stream without the events older than cut. The stream itself
// stays (as in CloudWatch Logs), possibly empty.
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
	var first int64
	for i, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r rawEvent
		if json.Unmarshal(line, &r) == nil && time.UnixMilli(r.T).Before(cut) {
			dropped = true
			continue
		}
		if r.E == "" { // keep legacy event IDs stable once lines move
			r.E = fmt.Sprintf("%020d%020d%016d", max(r.T, 0), 0, i+1)
			if r.I == 0 {
				r.I = r.T
			}
			line, _ = json.Marshal(r)
		}
		if first == 0 || r.T < first {
			first = r.T
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
	if m, ok := l.metaLocked(group, stream); ok {
		m.First = first
		if first == 0 {
			m.Last = 0
		}
		l.saveMetaLocked(path, m)
	}
}

func (l *logStore) deleteStream(group, stream string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	path := l.streamFile(group, stream)
	delete(l.meta, path)
	_ = os.Remove(path)
	_ = os.Remove(metaFile(path))
}

// DeleteGroup removes a stored log group and its events.
func (s *Service) DeleteGroup(name string) {
	l := s.logs
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = store.Delete(s.env.Store, cLogGroups, name)
	_ = store.Delete(s.env.Store, cLogFilters, name)
	dir := l.groupDir(name)
	for p := range l.meta {
		if strings.HasPrefix(p, dir+string(os.PathSeparator)) {
			delete(l.meta, p)
		}
	}
	_ = os.RemoveAll(dir)
}

// containerGroups lists virtual log groups for running HomeCloud containers.
func (s *Service) containerGroups() []LogGroup {
	out := []LogGroup{}
	if s.env.Docker == nil {
		return out
	}
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
	if !ok || s.env.Docker == nil {
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
	for i, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		ts, msg, _ := strings.Cut(line, " ")
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			t, msg = time.Time{}, line
		}
		t = t.UTC()
		out = append(out, LogEvent{Timestamp: t, Message: msg, Stream: stream, Ingestion: t,
			ID: fmt.Sprintf("%020d%020d%016d", max(t.UnixMilli(), 0), max(t.UnixNano()%1e18, 0), i)})
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

// groupsWithSize returns the stored log groups with their sizes, then the container groups.
func (s *Service) groupsWithSize() []LogGroup {
	out := []LogGroup{}
	for _, g := range store.List[LogGroup](s.env.Store, cLogGroups) {
		for _, st := range s.logs.streams(g.Name) {
			g.StoredBytes += st.StoredBytes
		}
		out = append(out, g)
	}
	return append(out, s.containerGroups()...)
}

func (s *Service) listGroups(c *httpx.Ctx) (any, error) {
	out := s.groupsWithSize()
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

// CreateGroup creates a stored log group.
func (s *Service) CreateGroup(g LogGroup) (LogGroup, error) {
	if err := validGroup(g.Name); err != nil {
		return g, err
	}
	if strings.HasPrefix(g.Name, containerGroupPrefix) {
		return g, core.BadRequest("log group names may not start with the reserved prefix %s", containerGroupPrefix)
	}
	if g.RetentionDays < 0 {
		return g, core.BadRequest("retention must be 0 (never expire) or more days")
	}
	s.logs.mu.Lock()
	defer s.logs.mu.Unlock()
	if store.Has(s.env.Store, cLogGroups, g.Name) {
		return g, core.Errf(409, "AlreadyExists", "log group %q already exists", g.Name)
	}
	g.ARN, g.CreatedAt, g.Source = s.env.ARN("logs", "log-group:"+g.Name), core.Now(), "stored"
	return g, store.Put(s.env.Store, cLogGroups, g.Name, g)
}

func (s *Service) createGroup(c *httpx.Ctx) (any, error) {
	var in struct {
		Name          string `json:"name"`
		RetentionDays int    `json:"retention_days"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	g, err := s.CreateGroup(LogGroup{Name: in.Name, RetentionDays: in.RetentionDays})
	if e, ok := err.(*core.Error); ok && e.Code == "AlreadyExists" {
		return nil, core.Conflict("%s", e.Message)
	}
	return g, err
}

func (s *Service) deleteGroup(c *httpx.Ctx) (any, error) {
	name := c.Param("group")
	if !store.Has(s.env.Store, cLogGroups, name) {
		return nil, core.NotFound("log group", name)
	}
	s.DeleteGroup(name)
	return nil, nil
}

// SetRetention sets a stored group's retention (0 = never expire).
func (s *Service) SetRetention(group string, days int) (LogGroup, error) {
	if days < 0 {
		return LogGroup{}, core.BadRequest("retention_days must be 0 (never expire) or more")
	}
	g, err := store.Update(s.env.Store, cLogGroups, group, func(g *LogGroup) error { g.RetentionDays = days; return nil })
	if err == store.ErrNotFound {
		return g, core.NotFound("log group", group)
	}
	if err != nil {
		return g, err
	}
	if days > 0 {
		go s.logs.enforceRetention()
	}
	for _, st := range s.logs.streams(g.Name) {
		g.StoredBytes += st.StoredBytes
	}
	return g, nil
}

func (s *Service) putRetention(c *httpx.Ctx) (any, error) {
	var in struct {
		RetentionDays int `json:"retention_days"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.SetRetention(c.Param("group"), in.RetentionDays)
}

func (s *Service) listStreams(c *httpx.Ctx) (any, error) {
	g := c.Param("group")
	if strings.HasPrefix(g, containerGroupPrefix) {
		return []LogStream{{Name: "stdout", LastEventTime: time.Now().UTC()}}, nil
	}
	if !store.Has(s.env.Store, cLogGroups, g) {
		return nil, core.NotFound("log group", g)
	}
	out := s.logs.streams(g)
	for i := range out {
		if out[i].LastEventTime.IsZero() { // an empty stream: show when it was created
			out[i].LastEventTime = out[i].CreatedAt
		}
	}
	return out, nil
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

// containerEvents reads a container-backed group's output.
func (s *Service) containerEvents(group string, start, end time.Time, match func(LogEvent) bool, limit int) ([]LogEvent, error) {
	id := s.containerFor(group)
	if id == "" {
		return nil, core.NotFound("log group", group)
	}
	// Filtering happens after reading, so read a larger window when filtering.
	tail := limit
	if match != nil || tail <= 0 {
		tail = max(limit*20, 10000)
	}
	raw, err := s.env.Docker.Logs(id, tail, start)
	if err != nil {
		return nil, err
	}
	var out []LogEvent
	for _, e := range ContainerEvents(raw, "stdout") {
		if match != nil && !match(e) {
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

// Events returns events for a group; also used by other services (e.g. Lambda logs).
func (s *Service) Events(group, stream string, start, end time.Time, filter string, limit int) ([]LogEvent, error) {
	if strings.HasPrefix(group, containerGroupPrefix) {
		return s.containerEvents(group, start, end, containsFold(filter), limit)
	}
	g, err := store.Get[LogGroup](s.env.Store, cLogGroups, group)
	if err != nil {
		return nil, core.NotFound("log group", group)
	}
	return s.logs.read(group, stream, start, end, filter, limit, retentionCut(g)), nil
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
