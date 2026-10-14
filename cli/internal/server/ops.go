package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/metrics"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/s3"
)

// Commit is the Git commit the binary was built from; the CLI sets it.
var Commit = "dev"

// Operator observability: Prometheus metrics on GET /metrics and a detailed
// status document on GET /api/v1/system/status.
//
// Access: both are ordinary IAM-protected routes of the API
// (homecloud:GetMetrics, homecloud:GetSystemStatus), so they are never
// readable without credentials, whatever address the API listens on.
// Prometheus authenticates with an access key, as a bearer token
// ("AKID:SECRET") or HTTP basic auth (username AKID, password SECRET). The
// optional --metrics-addr listener serves the metrics without authentication
// for a co-located Prometheus; it must be a loopback address unless
// --metrics-addr-insecure says otherwise.

const (
	// ActionGetMetrics reads /metrics; ActionGetStatus reads the status document.
	ActionGetMetrics = "homecloud:GetMetrics"
	ActionGetStatus  = "homecloud:GetSystemStatus"
	metricsPath      = "/metrics"

	dockerTTL    = 10 * time.Second
	dockerWait   = 4 * time.Second
	diskInterval = 5 * time.Minute
)

// latencyBuckets are the upper bounds (seconds) of the request duration histogram.
var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}

// helpers maps the long-running helper containers to their names in metrics.
var helpers = []struct{ name, service string }{
	{"minio", "s3"}, {"ecr", "ecr"}, {"dns", "route53"}, {"imds", "ec2"},
}

type envStatser interface{ EnvironmentStats() lambda.EnvStats }

type ops struct {
	cfg     core.Config
	docker  *runtime.Docker // nil in tests without Docker
	lambda  envStatser
	account string
	started time.Time

	reg       *metrics.Registry
	requests  *metrics.Vec
	reqErrors *metrics.Vec
	authFails *metrics.Vec
	duration  *metrics.HistogramVec
	inflight  atomic.Int64

	refreshMu sync.Mutex // one Docker refresh at a time
	snapMu    sync.Mutex
	snap      dockerSnapshot

	diskMu sync.Mutex
	disk   diskSnapshot
}

type dockerSnapshot struct {
	at         time.Time
	reachable  bool
	err        string
	info       *docker.DockerInfo
	containers []docker.APIContainers // this installation's
}

type diskSnapshot struct {
	at           time.Time
	dataDirBytes int64
	volumes      runtime.VolumeUsage
	volumesErr   string
}

func newOps(cfg core.Config, dk *runtime.Docker, lam envStatser, account string, started time.Time) *ops {
	o := &ops{cfg: cfg, docker: dk, lambda: lam, account: account, started: started, reg: metrics.NewRegistry()}
	r := o.reg
	r.NewCollector("homecloud_build_info", "HomeCloud build information.", metrics.Gauge, func() []metrics.Sample {
		return []metrics.Sample{{Labels: []string{"version", Version, "commit", Commit, "go_version", goruntime.Version()}, Value: 1}}
	})
	r.NewGaugeFunc("homecloud_start_time_seconds", "When the server started, in seconds since the epoch.", func() float64 {
		return float64(o.started.UnixNano()) / 1e9
	})
	o.requests = r.NewCounter("homecloud_http_requests_total", "HTTP requests served, by API kind, service, operation (route pattern or AWS operation) and status class.", "api", "service", "operation", "status_class")
	o.reqErrors = r.NewCounter("homecloud_http_request_errors_total", "Failed requests by API kind, service and error code.", "api", "service", "code")
	o.duration = r.NewHistogram("homecloud_http_request_duration_seconds", "Time to serve a request.", latencyBuckets, "api", "service")
	r.NewGaugeFunc("homecloud_http_requests_in_flight", "Requests being served.", func() float64 { return float64(o.inflight.Load()) })
	o.authFails = r.NewCounter("homecloud_auth_failures_total", "Requests whose credentials were missing or invalid, by API kind and error code.", "api", "code")

	r.NewGaugeFunc("homecloud_docker_up", "Whether the Docker engine answers (1) or not (0).", func() float64 {
		return b2f(o.dockerState().reachable)
	})
	r.NewCollector("homecloud_containers", "Containers HomeCloud manages, by service and state.", metrics.Gauge, func() []metrics.Sample {
		s := o.dockerState()
		if !s.reachable {
			return nil
		}
		var out []metrics.Sample
		for _, c := range countContainers(s.containers) {
			out = append(out, metrics.Sample{Labels: []string{"service", c.Service, "state", c.State}, Value: float64(c.Count)})
		}
		return out
	})
	r.NewCollector("homecloud_helper_up", "Whether a helper container (MinIO, the ECR registry, the DNS server, the instance metadata service) is running.", metrics.Gauge, func() []metrics.Sample {
		s := o.dockerState()
		if !s.reachable {
			return nil
		}
		var out []metrics.Sample
		for _, h := range helperStates(s.containers) {
			out = append(out, metrics.Sample{Labels: []string{"helper", h.name}, Value: b2f(h.state == "running")})
		}
		return out
	})

	r.NewCollector("homecloud_data_dir_bytes", "Size of the data directory (measured every 5 minutes).", metrics.Gauge, func() []metrics.Sample {
		d := o.diskState()
		if d.at.IsZero() {
			return nil
		}
		return []metrics.Sample{{Value: float64(d.dataDirBytes)}}
	})
	r.NewCollector("homecloud_data_dir_filesystem_size_bytes", "Size of the filesystem holding the data directory.", metrics.Gauge, func() []metrics.Sample {
		if t, _, ok := fsUsage(o.cfg.DataDir); ok {
			return []metrics.Sample{{Value: float64(t)}}
		}
		return nil
	})
	r.NewCollector("homecloud_data_dir_filesystem_free_bytes", "Free space (for unprivileged users) on the filesystem holding the data directory.", metrics.Gauge, func() []metrics.Sample {
		if _, f, ok := fsUsage(o.cfg.DataDir); ok {
			return []metrics.Sample{{Value: float64(f)}}
		}
		return nil
	})
	r.NewCollector("homecloud_docker_volumes", "Docker volumes of this installation (measured every 5 minutes).", metrics.Gauge, func() []metrics.Sample {
		if d := o.diskState(); !d.at.IsZero() && d.volumesErr == "" {
			return []metrics.Sample{{Value: float64(d.volumes.Count)}}
		}
		return nil
	})
	r.NewCollector("homecloud_docker_volumes_bytes", "Disk space used by the Docker volumes of this installation (measured every 5 minutes).", metrics.Gauge, func() []metrics.Sample {
		if d := o.diskState(); !d.at.IsZero() && d.volumesErr == "" {
			return []metrics.Sample{{Value: float64(d.volumes.Bytes)}}
		}
		return nil
	})

	loopSamples := func(f func(core.Loop) float64) func() []metrics.Sample {
		return func() []metrics.Sample {
			var out []metrics.Sample
			for _, l := range core.Loops() {
				out = append(out, metrics.Sample{Labels: []string{"loop", l.Name}, Value: f(l)})
			}
			return out
		}
	}
	r.NewCollector("homecloud_loop_last_run_timestamp_seconds", "When a background loop last completed a pass.", metrics.Gauge,
		loopSamples(func(l core.Loop) float64 { return float64(l.LastRun.UnixNano()) / 1e9 }))
	r.NewCollector("homecloud_loop_last_run_age_seconds", "Seconds since a background loop last completed a pass.", metrics.Gauge,
		loopSamples(func(l core.Loop) float64 { return time.Since(l.LastRun).Seconds() }))
	r.NewCollector("homecloud_loop_interval_seconds", "How often a background loop runs.", metrics.Gauge,
		loopSamples(func(l core.Loop) float64 { return l.Interval.Seconds() }))

	r.NewCollector("homecloud_lambda_environments", "Lambda execution environments by state.", metrics.Gauge, func() []metrics.Sample {
		if o.lambda == nil {
			return nil
		}
		s := o.lambda.EnvironmentStats()
		return []metrics.Sample{
			{Labels: []string{"state", "busy"}, Value: float64(s.Busy)},
			{Labels: []string{"state", "idle"}, Value: float64(s.Idle)},
			{Labels: []string{"state", "starting"}, Value: float64(s.Starting)},
		}
	})
	r.NewCollector("homecloud_lambda_starts_total", "Lambda invocations served by a new environment (cold) or a running one (warm).", metrics.Counter, func() []metrics.Sample {
		if o.lambda == nil {
			return nil
		}
		s := o.lambda.EnvironmentStats()
		return []metrics.Sample{
			{Labels: []string{"kind", "cold"}, Value: float64(s.ColdStarts)},
			{Labels: []string{"kind", "warm"}, Value: float64(s.WarmStarts)},
		}
	})

	registerProcessMetrics(r, started)
	return o
}

func registerProcessMetrics(r *metrics.Registry, started time.Time) {
	var (
		msMu sync.Mutex
		ms   goruntime.MemStats
		msAt time.Time
	)
	mem := func() goruntime.MemStats {
		msMu.Lock()
		defer msMu.Unlock()
		if time.Since(msAt) > time.Second { // one stop-the-world read per scrape
			goruntime.ReadMemStats(&ms)
			msAt = time.Now()
		}
		return ms
	}
	r.NewGaugeFunc("go_goroutines", "Number of goroutines that currently exist.", func() float64 { return float64(goruntime.NumGoroutine()) })
	r.NewGaugeFunc("go_memstats_alloc_bytes", "Bytes of allocated heap objects.", func() float64 { return float64(mem().Alloc) })
	r.NewGaugeFunc("go_memstats_heap_inuse_bytes", "Bytes in in-use heap spans.", func() float64 { return float64(mem().HeapInuse) })
	r.NewGaugeFunc("go_memstats_sys_bytes", "Bytes of memory obtained from the OS.", func() float64 { return float64(mem().Sys) })
	r.NewCollector("go_gc_cycles_total", "Completed GC cycles.", metrics.Counter, func() []metrics.Sample {
		return []metrics.Sample{{Value: float64(mem().NumGC)}}
	})
	r.NewGaugeFunc("process_start_time_seconds", "Start time of the process since the epoch in seconds.", func() float64 {
		return float64(started.UnixNano()) / 1e9
	})
	optional := func(f func() (float64, bool)) func() []metrics.Sample {
		return func() []metrics.Sample {
			if v, ok := f(); ok {
				return []metrics.Sample{{Value: v}}
			}
			return nil
		}
	}
	r.NewCollector("process_cpu_seconds_total", "Total user and system CPU time spent in seconds.", metrics.Counter, optional(cpuSeconds))
	r.NewCollector("process_resident_memory_bytes", "Resident memory size in bytes.", metrics.Gauge, optional(residentBytes))
	r.NewCollector("process_open_fds", "Number of open file descriptors.", metrics.Gauge, optional(openFDs))
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// ---- request instrumentation ----

// instrument counts and times every request the API listeners serve.
func (o *ops) instrument(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		o.inflight.Add(1)
		obs := &httpx.Observation{}
		sw := &opsWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			o.inflight.Add(-1)
			o.record(r, obs, sw, time.Since(start))
		}()
		h.ServeHTTP(sw, r.WithContext(httpx.WithObservation(r.Context(), obs)))
	})
}

func (o *ops) record(r *http.Request, obs *httpx.Observation, sw *opsWriter, took time.Duration) {
	api, service, op := obs.API, obs.Service, obs.Operation
	if api == "" {
		api, service, op = "other", otherService(r.URL.Path), ""
	}
	api, service, op = label(api), label(service), label(op)
	o.requests.Inc(api, service, op, fmt.Sprintf("%dxx", sw.status/100))
	if code := obs.ErrorCode; code != "" || sw.status >= 400 {
		if code == "" {
			code = fmt.Sprintf("HTTP%d", sw.status)
		}
		o.reqErrors.Inc(api, service, label(code))
	}
	if obs.AuthFailure != "" {
		o.authFails.Inc(api, label(obs.AuthFailure))
	}
	if !sw.hijacked { // a WebSocket session's length is not a latency
		o.duration.Observe(took.Seconds(), api, service)
	}
}

// otherService names the requests neither API claims, from a fixed list.
func otherService(p string) string {
	for _, m := range []struct{ prefix, name string }{
		{"/api/v1/health", "health"}, {metricsPath, "metrics"}, {s3.PresignPrefix, "presign"},
		{ec2.IMDSPath, "imds"}, {"/website/", "website"}, {"/lambda-url/", "lambda-url"},
		{"/apigw/", "apigw"}, {"/api/", "api"},
	} {
		if strings.HasPrefix(p, m.prefix) {
			return m.name
		}
	}
	return "console"
}

// label keeps metric label values bounded and printable: values the server
// derives from route patterns and its own names pass, anything else (which
// would only come from a bug) collapses to "other".
func label(s string) string {
	if len(s) > 100 {
		return "other"
	}
	for _, c := range s {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(" /{}._:-*$", c)
		if !ok {
			return "other"
		}
	}
	return s
}

// opsWriter records the response status.
type opsWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	hijacked    bool
}

func (w *opsWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *opsWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// ReadFrom keeps the underlying writer's sendfile path for object downloads.
func (w *opsWriter) ReadFrom(r io.Reader) (int64, error) {
	w.wroteHeader = true
	return io.Copy(w.ResponseWriter, r)
}

func (w *opsWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *opsWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.status, w.hijacked = http.StatusSwitchingProtocols, true
	return h.Hijack()
}

func (w *opsWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ---- Docker and disk state ----

func (o *ops) dockerState() dockerSnapshot {
	o.snapMu.Lock()
	s := o.snap
	o.snapMu.Unlock()
	if time.Since(s.at) < dockerTTL {
		return s
	}
	o.refreshMu.Lock()
	defer o.refreshMu.Unlock()
	o.snapMu.Lock()
	s = o.snap
	o.snapMu.Unlock()
	if time.Since(s.at) < dockerTTL { // another scrape refreshed it meanwhile
		return s
	}
	s = o.readDocker()
	o.snapMu.Lock()
	o.snap = s
	o.snapMu.Unlock()
	return s
}

func (o *ops) readDocker() dockerSnapshot {
	s := dockerSnapshot{at: time.Now()}
	if o.docker == nil {
		s.err = "no Docker client"
		return s
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerWait)
	defer cancel()
	cs, err := o.docker.ListManaged(ctx)
	if err != nil {
		s.err = err.Error()
		return s
	}
	for _, c := range cs {
		if runtime.Mine(c.Labels) {
			s.containers = append(s.containers, c)
		}
	}
	s.reachable = true
	if info, err := o.docker.Info(ctx); err == nil {
		s.info = info
	}
	return s
}

func (o *ops) diskState() diskSnapshot {
	o.diskMu.Lock()
	defer o.diskMu.Unlock()
	return o.disk
}

// runDisk measures the data directory and the volumes in the background:
// both walk files, so scrapes only read the last result.
func (o *ops) runDisk(ctx context.Context) {
	t := time.NewTicker(diskInterval)
	defer t.Stop()
	for {
		core.Beat("disk-usage", diskInterval)
		o.measureDisk(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (o *ops) measureDisk(ctx context.Context) {
	defer core.Recover("disk usage")
	d := diskSnapshot{dataDirBytes: dirSize(o.cfg.DataDir)}
	if o.docker != nil {
		vctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		v, err := o.docker.VolumeUsage(vctx)
		cancel()
		if err != nil {
			d.volumesErr = err.Error()
		}
		d.volumes = v
	} else {
		d.volumesErr = "no Docker client"
	}
	d.at = time.Now()
	o.diskMu.Lock()
	o.disk = d
	o.diskMu.Unlock()
}

func dirSize(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped
		}
		if e.Type().IsRegular() {
			if fi, err := e.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

type containerCount struct {
	Service string `json:"service"`
	State   string `json:"state"`
	Count   int    `json:"count"`
}

func countContainers(cs []docker.APIContainers) []containerCount {
	m := map[[2]string]int{}
	for _, c := range cs {
		m[[2]string{label(c.Labels[core.LabelService]), label(c.State)}]++
	}
	out := make([]containerCount, 0, len(m))
	for k, n := range m {
		out = append(out, containerCount{Service: k[0], State: k[1], Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].State < out[j].State
	})
	return out
}

type helperState struct{ name, state string }

// helperStates reports each helper's container state ("" when it has none).
func helperStates(cs []docker.APIContainers) []helperState {
	out := make([]helperState, 0, len(helpers))
	for _, h := range helpers {
		st := ""
		for _, c := range cs {
			if c.Labels[core.LabelService] == h.service && c.Labels[core.LabelResource] == "server" {
				if st == "" || c.State == "running" {
					st = c.State
				}
			}
		}
		out = append(out, helperState{h.name, st})
	}
	return out
}

// ---- metrics endpoint ----

func (o *ops) writeMetrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", metrics.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	o.reg.WriteText(w)
}

// basicToBearer turns HTTP basic credentials (access key ID and secret, as
// Prometheus' basic_auth sends them) into the bearer form the API accepts.
func basicToBearer(r *http.Request) *http.Request {
	h := r.Header.Get("Authorization")
	if len(h) < 6 || !strings.EqualFold(h[:6], "basic ") {
		return r
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[6:]))
	if err != nil {
		return r
	}
	id, secret, ok := strings.Cut(string(b), ":")
	if !ok || id == "" || strings.HasPrefix(id, "hcs_") {
		return r
	}
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+id+":"+secret)
	return r2
}

// ValidateMetricsAddr checks a --metrics-addr: its listener has no
// authentication, so it must be a loopback address unless insecure is set.
func ValidateMetricsAddr(addr string, insecure bool) error {
	if addr == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return fmt.Errorf("--metrics-addr %q: want host:port, e.g. 127.0.0.1:9464", addr)
	}
	if insecure || host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--metrics-addr %s serves metrics without authentication, so it must be a loopback address "+
			"(127.0.0.1, ::1); scrape the API's /metrics with an access key instead, or add --metrics-addr-insecure "+
			"if a firewall protects that address", addr)
	}
	return nil
}

// serveMetricsAddr serves /metrics without authentication on cfg.MetricsAddr.
func (o *ops) serveMetricsAddr(logf func(string, ...any)) (addr string, stop func(), err error) {
	if o.cfg.MetricsAddr == "" {
		return "", func() {}, nil
	}
	if err := ValidateMetricsAddr(o.cfg.MetricsAddr, o.cfg.MetricsAddrInsecure); err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", o.cfg.MetricsAddr)
	if err != nil {
		return "", nil, fmt.Errorf("metrics listener: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+metricsPath, func(w http.ResponseWriter, r *http.Request) { o.writeMetrics(w) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	logf("Prometheus metrics (no authentication) at http://%s%s", ln.Addr(), metricsPath)
	return ln.Addr().String(), func() { _ = srv.Close() }, nil
}

// ---- status ----

// Subsystem is one entry of the status document.
type Subsystem struct {
	Name    string     `json:"name"`
	Kind    string     `json:"kind"`   // core, helper, loop, disk
	Status  string     `json:"status"` // ok, warning, error, unknown
	Detail  string     `json:"detail,omitempty"`
	LastRun *time.Time `json:"last_run,omitempty"`
	LastAge *int64     `json:"last_run_age_seconds,omitempty"`
}

// Status is the document GET /api/v1/system/status returns.
type Status struct {
	Status        string             `json:"status"` // ok, degraded, down
	Version       string             `json:"version"`
	Commit        string             `json:"commit"`
	GoVersion     string             `json:"go_version"`
	Region        string             `json:"region"`
	AccountID     string             `json:"account_id"`
	StartedAt     time.Time          `json:"started_at"`
	UptimeSeconds int64              `json:"uptime_seconds"`
	Host          map[string]any     `json:"host"`
	Process       map[string]any     `json:"process"`
	Docker        DockerStatus       `json:"docker"`
	Subsystems    []Subsystem        `json:"subsystems"`
	Disk          DiskStatus         `json:"disk"`
	Containers    []containerCount   `json:"containers"`
	Lambda        *lambda.EnvStats   `json:"lambda,omitempty"`
	RecentErrors  []core.RecentError `json:"recent_errors"`
	Metrics       map[string]string  `json:"metrics"`
}

type DockerStatus struct {
	Reachable         bool   `json:"reachable"`
	ServerVersion     string `json:"server_version,omitempty"`
	OS                string `json:"os,omitempty"`
	KernelVersion     string `json:"kernel_version,omitempty"`
	Architecture      string `json:"architecture,omitempty"`
	CPUs              int    `json:"cpus,omitempty"`
	MemoryBytes       int64  `json:"memory_bytes,omitempty"`
	StorageDriver     string `json:"storage_driver,omitempty"`
	Containers        int    `json:"containers"`
	ContainersRunning int    `json:"containers_running"`
	Images            int    `json:"images"`
	Error             string `json:"error,omitempty"`
}

type DiskStatus struct {
	DataDir struct {
		Path         string `json:"path"`
		UsedBytes    *int64 `json:"used_bytes"`
		FSTotalBytes uint64 `json:"fs_total_bytes"`
		FSFreeBytes  uint64 `json:"fs_free_bytes"`
	} `json:"data_dir"`
	Volumes struct {
		Count     int    `json:"count"`
		SizeBytes int64  `json:"size_bytes"`
		Error     string `json:"error,omitempty"`
	} `json:"volumes"`
	MeasuredAt *time.Time `json:"measured_at"`
}

func (o *ops) status() Status {
	now := time.Now()
	st := Status{
		Version: Version, Commit: Commit, GoVersion: goruntime.Version(), Region: o.cfg.Region, AccountID: o.account,
		StartedAt: o.started.UTC(), UptimeSeconds: int64(now.Sub(o.started).Seconds()),
		Host:         map[string]any{"os": goruntime.GOOS, "arch": goruntime.GOARCH, "cpus": goruntime.NumCPU()},
		Metrics:      map[string]string{"path": metricsPath, "action": ActionGetMetrics, "listen_addr": o.cfg.MetricsAddr},
		RecentErrors: core.RecentErrors(), Containers: []containerCount{},
	}
	var ms goruntime.MemStats
	goruntime.ReadMemStats(&ms)
	st.Process = map[string]any{"pid": os.Getpid(), "goroutines": goruntime.NumGoroutine(), "heap_bytes": ms.HeapAlloc, "sys_bytes": ms.Sys}

	add := func(s Subsystem) { st.Subsystems = append(st.Subsystems, s) }
	add(Subsystem{Name: "api", Kind: "core", Status: "ok", Detail: "listening on " + o.cfg.APIAddr})

	ds := o.dockerState()
	st.Docker = DockerStatus{Reachable: ds.reachable, Error: ds.err}
	if i := ds.info; i != nil {
		st.Docker.ServerVersion, st.Docker.OS, st.Docker.KernelVersion = i.ServerVersion, i.OperatingSystem, i.KernelVersion
		st.Docker.Architecture, st.Docker.CPUs, st.Docker.MemoryBytes, st.Docker.StorageDriver = i.Architecture, i.NCPU, i.MemTotal, i.Driver
		st.Docker.Containers, st.Docker.ContainersRunning, st.Docker.Images = i.Containers, i.ContainersRunning, i.Images
	}
	if ds.reachable {
		detail := "reachable"
		if st.Docker.ServerVersion != "" {
			detail = "Docker " + st.Docker.ServerVersion
		}
		add(Subsystem{Name: "docker", Kind: "core", Status: "ok", Detail: detail})
		st.Containers = countContainers(ds.containers)
		for _, h := range helperStates(ds.containers) {
			s := Subsystem{Name: h.name, Kind: "helper", Status: "ok", Detail: h.state}
			switch h.state {
			case "running":
			case "restarting", "created":
				s.Status = "warning"
			case "":
				s.Status, s.Detail = "error", "not running"
			default:
				s.Status = "error"
			}
			add(s)
		}
	} else {
		add(Subsystem{Name: "docker", Kind: "core", Status: "error", Detail: ds.err})
		for _, h := range helpers {
			add(Subsystem{Name: h.name, Kind: "helper", Status: "unknown", Detail: "Docker is unreachable"})
		}
	}

	// Disk.
	d := o.diskState()
	st.Disk.DataDir.Path = o.cfg.DataDir
	total, free, fsOK := fsUsage(o.cfg.DataDir)
	st.Disk.DataDir.FSTotalBytes, st.Disk.DataDir.FSFreeBytes = total, free
	if !d.at.IsZero() {
		at := d.at.UTC()
		used := d.dataDirBytes
		st.Disk.MeasuredAt, st.Disk.DataDir.UsedBytes = &at, &used
		st.Disk.Volumes.Count, st.Disk.Volumes.SizeBytes, st.Disk.Volumes.Error = d.volumes.Count, d.volumes.Bytes, d.volumesErr
	}
	if fsOK && total > 0 {
		s := Subsystem{Name: "data-dir-disk", Kind: "disk", Status: "ok", Detail: fmt.Sprintf("%.0f%% free", 100*float64(free)/float64(total))}
		switch pct := float64(free) / float64(total); {
		case pct < 0.03:
			s.Status = "error"
		case pct < 0.10:
			s.Status = "warning"
		}
		add(s)
	}

	for _, l := range core.Loops() {
		run := l.LastRun.UTC()
		age := int64(now.Sub(l.LastRun).Seconds())
		s := Subsystem{Name: "loop:" + l.Name, Kind: "loop", Status: "ok", Detail: "every " + l.Interval.String(), LastRun: &run, LastAge: &age}
		if l.Stalled(now) {
			s.Status, s.Detail = "warning", fmt.Sprintf("no pass for %s (runs every %s)", now.Sub(l.LastRun).Round(time.Second), l.Interval)
		}
		add(s)
	}

	if o.lambda != nil {
		ls := o.lambda.EnvironmentStats()
		st.Lambda = &ls
	}

	st.Status = "ok"
	for _, s := range st.Subsystems {
		if s.Status == "error" {
			st.Status = "degraded"
		}
	}
	if !ds.reachable {
		st.Status = "down" // no service can run workloads without Docker
	}
	return st
}

// Routes serves the metrics and status endpoints on the API.
func (o *ops) Routes(rt *httpx.Router) {
	rt.Handle("GET /metrics", "homecloud:GetMetrics", func(c *httpx.Ctx) (any, error) {
		o.writeMetrics(c.W)
		c.MarkWritten()
		return nil, nil
	})
	rt.Handle("GET /api/v1/system/status", "homecloud:GetSystemStatus", func(c *httpx.Ctx) (any, error) {
		return o.status(), nil
	})
}
