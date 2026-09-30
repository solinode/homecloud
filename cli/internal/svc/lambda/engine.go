package lambda

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// Concurrency limits. A function without reserved concurrency may run up to
// DefaultConcurrency environments at once; reservations are carved out of
// AccountConcurrency, leaving at least MinUnreserved for everything else.
var (
	DefaultConcurrency = 10
	AccountConcurrency = 100
	MinUnreserved      = 10
	// syncQueueWait is how long a synchronous invocation waits for a free
	// environment before it is throttled.
	syncQueueWait = 2 * time.Second
	// credsTTL is the lifetime of an environment's role credentials; the
	// environment is recycled before they expire.
	credsTTL = time.Hour
)

const riePort = 8080

// execEnv is one execution environment: a container running the Runtime
// Interface Emulator, which serves one invocation at a time.
type execEnv struct {
	id       string // container ID
	fn       string
	version  string
	revision string // configuration the environment was built from
	slot     int    // VPC address slot
	// The environment's place in its VPC, for security group enforcement
	// (only functions with a VPC configuration have groups).
	vpcID, ip string
	groups    []string
	inVPC     bool
	port      int    // host port of the emulator (0: reach it with docker exec)
	stream    string // CloudWatch log stream
	busy      bool
	used      bool // has served an invocation (the first one is a cold start)
	dead      bool // destroy when released
	lastUsed  time.Time
	expires   time.Time // role credentials expiry (zero without a role)
	logs      *lineLog
	stop      context.CancelFunc
}

// pool holds a function's environments across its versions.
type pool struct {
	envs     []*execEnv
	starting int
	slots    map[int]bool  // VPC address slots in use (including environments being torn down)
	changed  chan struct{} // closed and replaced whenever capacity may have freed up
}

func (s *Service) pool(name string) *pool {
	p := s.pools[name]
	if p == nil {
		p = &pool{slots: map[int]bool{}, changed: make(chan struct{})}
		s.pools[name] = p
	}
	return p
}

// notify wakes invocations waiting for capacity. Call with s.mu held.
func (p *pool) notify() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *pool) busyCount() int {
	n := p.starting
	for _, e := range p.envs {
		if e.busy {
			n++
		}
	}
	return n
}

func (p *pool) freeSlot() int {
	for i := 0; ; i++ {
		if !p.slots[i] {
			p.slots[i] = true
			return i
		}
	}
}

// limit is the function's concurrency limit.
func limitFor(f Function) int {
	if f.ReservedConcurrency != nil {
		return *f.ReservedConcurrency
	}
	return DefaultConcurrency
}

func (s *Service) wakeAll(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pools[name]; p != nil {
		p.notify()
	}
}

// throttled is the error for invocations over the concurrency limit.
func throttled(f Function) error {
	reason := "Rate Exceeded."
	if f.ReservedConcurrency != nil {
		reason = "Rate Exceeded. (ReservedFunctionConcurrentInvocationLimitExceeded)"
	}
	return core.Errf(http.StatusTooManyRequests, "TooManyRequests", "%s", reason)
}

// acquire returns an idle environment for the function version (or starts one),
// waiting up to wait for capacity. cfg is the version's configuration; lim the
// function's concurrency limit.
func (s *Service) acquire(ctx context.Context, cfg Function, lim int, wait time.Duration) (*execEnv, error) {
	deadline := time.Now().Add(wait)
	for {
		s.mu.Lock()
		p := s.pool(cfg.Name)
		if p.busyCount() < lim {
			for _, e := range p.envs {
				if !e.busy && !e.dead && e.version == cfg.version() && e.revision == cfg.RevisionID && !e.expiring(cfg) {
					e.busy = true
					s.mu.Unlock()
					return e, nil
				}
			}
			// Make room: environments beyond the limit that are idle (other
			// versions, stale configurations) are torn down first.
			if len(p.envs)+p.starting >= lim {
				for _, e := range p.envs {
					if !e.busy {
						s.removeLocked(p, e)
						break
					}
				}
			}
			p.starting++
			slot := p.freeSlot()
			s.mu.Unlock()
			e, err := s.startEnv(ctx, cfg, slot)
			s.mu.Lock()
			p.starting--
			if err != nil {
				delete(p.slots, slot)
				p.notify()
				s.mu.Unlock()
				return nil, err
			}
			e.busy = true
			p.envs = append(p.envs, e)
			s.mu.Unlock()
			if e.inVPC {
				s.vpc.FirewallChanged() // peers allowing this function's groups learn its address now
			}
			return e, nil
		}
		ch := p.changed
		s.mu.Unlock()
		left := time.Until(deadline)
		if left <= 0 {
			return nil, throttled(cfg)
		}
		t := time.NewTimer(left)
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
}

// expiring reports whether the environment's credentials run out before an
// invocation could finish.
func (e *execEnv) expiring(f Function) bool {
	return !e.expires.IsZero() && time.Until(e.expires) < time.Duration(f.TimeoutSec)*time.Second+5*time.Minute
}

// release returns an environment to its pool.
func (s *Service) release(e *execEnv) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pool(e.fn)
	e.busy, e.used, e.lastUsed = false, true, time.Now()
	if e.dead {
		s.removeLocked(p, e)
	}
	p.notify()
}

// removeLocked takes an environment out of its pool and destroys it in the
// background. Call with s.mu held.
func (s *Service) removeLocked(p *pool, e *execEnv) {
	for i, x := range p.envs {
		if x == e {
			p.envs = append(p.envs[:i], p.envs[i+1:]...)
			break
		}
	}
	go func() {
		s.destroy(e)
		s.mu.Lock()
		delete(p.slots, e.slot)
		p.notify()
		s.mu.Unlock()
	}()
}

func (s *Service) destroy(e *execEnv) {
	if e.stop != nil {
		e.stop()
	}
	if s.env.Docker != nil {
		_ = s.env.Docker.Remove(e.id)
	}
	if e.inVPC && s.vpc != nil {
		s.vpc.FirewallChanged() // its address leaves every group it was in
	}
}

// member is an environment as a security group member. The emulator port is
// published on the host for invocations and stays reachable from there; from
// inside the VPC nothing reaches a function, as in AWS.
func (s *Service) member(e *execEnv, cid string) vpc.Member {
	return vpc.Member{Kind: "lambda", ID: e.fn, VpcID: e.vpcID, IP: e.ip, ContainerID: cid, Groups: e.groups, ExternalTCP: []int{riePort}}
}

// fwMembers lists the environments of functions with a VPC configuration.
func (s *Service) fwMembers() []vpc.Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []vpc.Member
	for _, p := range s.pools {
		for _, e := range p.envs {
			if e.inVPC && !e.dead {
				out = append(out, s.member(e, e.id))
			}
		}
	}
	return out
}

// invalidate retires the environments of a function version (after a
// configuration or code change): idle ones now, busy ones when they finish.
func (s *Service) invalidate(name, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pools[name]
	if p == nil {
		return
	}
	for _, e := range append([]*execEnv(nil), p.envs...) {
		if version != "" && e.version != version {
			continue
		}
		if e.busy {
			e.dead = true
		} else {
			s.removeLocked(p, e)
		}
	}
	p.notify()
}

// retireAll destroys every environment of a function and releases its addresses.
func (s *Service) retireAll(name string) {
	s.invalidate(name, "")
	if s.vpc != nil {
		s.vpc.Release("lambda:" + name)
		for i := 0; i < AccountConcurrency; i++ {
			s.vpc.Release(slotOwner(name, i))
		}
	}
}

func slotOwner(fn string, slot int) string { return "lambda:" + fn + ":" + strconv.Itoa(slot) }

// warmCount is the number of environments a function has.
func (s *Service) warmCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pools[name]; p != nil {
		return len(p.envs)
	}
	return 0
}

// concurrent is the number of invocations a function is running.
func (s *Service) concurrent(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pools[name]; p != nil {
		return p.busyCount()
	}
	return 0
}

// ---- images ----

func hostArch() string {
	if goruntime.GOARCH == "arm64" {
		return "arm64"
	}
	return "x86_64"
}

// imageFor is the container image a function version runs.
func (s *Service) imageFor(f Function) string {
	if f.isImage() {
		if s.ResolveImage != nil {
			return s.ResolveImage(f.ImageURI)
		}
		return f.ImageURI
	}
	rt, _ := findRuntime(f.Runtime)
	return rt.Image
}

func (s *Service) imageReady(img string) bool {
	if s.env.Docker == nil {
		return true
	}
	_, err := s.env.Docker.C.InspectImage(img)
	return err == nil
}

func (s *Service) pull(ctx context.Context, img string) error {
	if s.env.Docker == nil {
		return nil
	}
	m, _ := s.prepMu.LoadOrStore(img, &sync.Mutex{})
	l := m.(*sync.Mutex)
	l.Lock()
	defer l.Unlock()
	return s.env.Docker.EnsureImage(ctx, img)
}

// refresh pulls an image even if a copy exists (a tag may have moved); a
// failed pull falls back to the local copy.
func (s *Service) refresh(ctx context.Context, img string) error {
	if s.env.Docker == nil {
		return nil
	}
	m, _ := s.prepMu.LoadOrStore(img, &sync.Mutex{})
	l := m.(*sync.Mutex)
	l.Lock()
	defer l.Unlock()
	repo, tag := img, "latest"
	if r, d, ok := strings.Cut(img, "@"); ok {
		repo, tag = r, d
	} else if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		repo, tag = img[:i], img[i+1:]
	}
	err := s.env.Docker.C.PullImage(docker.PullImageOptions{Repository: repo, Tag: tag, Context: ctx, InactivityTimeout: 2 * time.Minute}, docker.AuthConfiguration{})
	if err != nil && s.imageReady(img) {
		return nil
	}
	return err
}

// imageDigest is a local image's content digest (hex), the CodeSha256 of image functions.
func (s *Service) imageDigest(img string) string {
	if s.env.Docker == nil {
		return ""
	}
	ii, err := s.env.Docker.C.InspectImage(img)
	if err != nil {
		return ""
	}
	for _, d := range ii.RepoDigests {
		if _, h, ok := strings.Cut(d, "@sha256:"); ok {
			return h
		}
	}
	return strings.TrimPrefix(ii.ID, "sha256:")
}

// prepare pulls a function's image and moves it from Pending (or an update
// from InProgress) to Active/Successful, or Failed.
func (s *Service) prepare(name, revision string, create bool) {
	defer core.Recover("lambda prepare " + name)
	f, err := s.getLatest(name)
	if err != nil {
		return
	}
	img := s.imageFor(f)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var perr error
	if f.isImage() {
		perr = s.refresh(ctx, img)
	} else {
		perr = s.pull(ctx, img)
	}
	digest := ""
	if perr == nil && f.isImage() {
		digest = s.imageDigest(img)
	}
	_, _ = store.Update(s.env.Store, cFunctions, name, func(x *Function) error {
		if x.RevisionID != revision {
			return errors.New("superseded")
		}
		if digest != "" {
			x.CodeSHA256 = digest
		}
		if perr != nil {
			log.Printf("lambda: prepare %s: %v", name, perr)
			code := "ImageAccessDenied"
			if !f.isImage() {
				code = "InternalError"
			}
			if create {
				x.State, x.StateReason, x.StateReasonCode = "Failed", "Could not pull image "+img+": "+perr.Error(), code
			} else {
				x.LastUpdateStatus, x.LastUpdateStatusReason = "Failed", "Could not pull image "+img+": "+perr.Error()
			}
			return nil
		}
		x.State, x.StateReason, x.StateReasonCode = "Active", "", ""
		x.LastUpdateStatus, x.LastUpdateStatusReason = "Successful", ""
		return nil
	})
	if perr == nil && f.isImage() {
		s.invalidate(name, latest) // environments started during the pull may run an older image
	}
}

// ---- environments ----

func (s *Service) containerName(fn string, slot int) string {
	return svc.ContainerName("lambda", fmt.Sprintf("%s-%d-%s", fn, slot, core.RandHex(4)))
}

// startEnv creates and starts an execution environment for a function version.
func (s *Service) startEnv(ctx context.Context, f Function, slot int) (*execEnv, error) {
	if s.env.Docker == nil {
		return nil, core.Errf(http.StatusServiceUnavailable, "ServiceException", "Docker is not available")
	}
	img := s.imageFor(f)
	if err := s.pull(ctx, img); err != nil {
		return nil, core.Errf(http.StatusBadGateway, "ServiceException", "pull %s: %v", img, err)
	}
	// The code package and layers, extracted into /var/task and /opt.
	files := map[string][]byte{}
	for _, l := range f.Layers {
		lv, err := s.layerByARN(l)
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(s.layerFile(lv))
		if err != nil {
			return nil, fmt.Errorf("read layer %s: %w", l, err)
		}
		fs, err := unzip(b)
		if err != nil {
			return nil, fmt.Errorf("unpack layer %s: %w", l, err)
		}
		for n, data := range fs {
			files["opt/"+n] = data
		}
	}
	if !f.isImage() {
		code, err := os.ReadFile(s.codeFile(f))
		if err != nil {
			return nil, fmt.Errorf("read code: %w", err)
		}
		fs, err := unzip(code)
		if err != nil {
			return nil, fmt.Errorf("unpack code: %w", err)
		}
		for n, data := range fs {
			files["var/task/"+n] = data
		}
	}
	envID := core.RandHex(32)
	stream := time.Now().UTC().Format("2006/01/02") + "/[" + f.version() + "]" + envID
	env := map[string]string{
		"AWS_LAMBDA_FUNCTION_NAME": f.Name, "AWS_LAMBDA_FUNCTION_VERSION": f.version(),
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": strconv.FormatInt(f.MemoryMB, 10), "AWS_LAMBDA_FUNCTION_TIMEOUT": strconv.Itoa(f.TimeoutSec),
		"AWS_LAMBDA_LOG_GROUP_NAME": f.LogGroup, "AWS_LAMBDA_LOG_STREAM_NAME": stream, "AWS_LAMBDA_INITIALIZATION_TYPE": "on-demand",
		"AWS_REGION": core.Region, "AWS_DEFAULT_REGION": core.Region,
	}
	if s.env.ContainerAPI != "" {
		env["AWS_ENDPOINT_URL"] = s.env.ContainerAPI
	}
	for k, v := range f.Environment {
		env[k] = v
	}
	var expires time.Time
	if f.Role != "" {
		if s.Roles == nil {
			return nil, core.Errf(http.StatusBadGateway, "ServiceException", "execution roles are not available")
		}
		c, err := s.Roles.LambdaCredentials(f.Role, f.Name, credsTTL)
		if err != nil {
			return nil, core.Errf(http.StatusBadGateway, "ServiceException", "The function's execution role could not be assumed: %v", err)
		}
		env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"], env["AWS_SESSION_TOKEN"] = c.AccessKeyID, c.SecretAccessKey, c.SessionToken
		expires = c.Expiration
	}
	spec := runtime.RunSpec{
		Name:       s.containerName(f.Name, slot),
		Image:      img,
		Env:        env,
		Labels:     runtime.Labels("lambda", f.Name, map[string]string{"homecloud.lambda.version": f.version()}),
		MemoryMB:   f.MemoryMB,
		NanoCPUs:   int64(max(0.25, float64(f.MemoryMB)/1769) * 1e9), // CPU scales with memory, as in AWS
		Ports:      []runtime.Port{{ContainerPort: riePort, HostIP: "127.0.0.1"}},
		ExtraHosts: []string{runtime.HostAlias},
	}
	if f.isImage() {
		if ic := f.ImageConfig; ic != nil {
			spec.Entrypoint, spec.Cmd, spec.WorkingDir = ic.EntryPoint, ic.Command, ic.WorkingDirectory
		}
	} else {
		spec.Cmd = []string{f.Handler}
	}
	var pl *vpc.Placement
	if s.vpc != nil {
		var err error
		pl, err = s.vpc.Place(f.SubnetID, slotOwner(f.Name, slot))
		if err != nil {
			return nil, err
		}
		spec.Network, spec.IP = pl.Network, pl.IP
		if s.DNSFor != nil {
			spec.DNS = s.DNSFor(pl.VPC.ID)
		}
	}
	id, err := s.env.Docker.Run(ctx, spec)
	if err != nil {
		return nil, err
	}
	e := &execEnv{id: id, fn: f.Name, version: f.version(), revision: f.RevisionID, slot: slot, stream: stream, expires: expires,
		lastUsed: time.Now(), logs: newLineLog()}
	if pl != nil {
		e.vpcID, e.ip, e.groups, e.inVPC = pl.VPC.ID, pl.IP, f.SecurityGroupIDs, f.SubnetID != ""
	}
	fail := func(err error) (*execEnv, error) {
		s.destroy(e)
		return nil, err
	}
	if len(files) > 0 {
		if err := s.env.Docker.CopyIn(ctx, id, "/", files, 0o755); err != nil {
			return fail(fmt.Errorf("copy code: %w", err))
		}
	}
	if err := s.env.Docker.Start(id); err != nil {
		return fail(err)
	}
	if e.inVPC {
		s.vpc.ProtectNow(ctx, s.member(e, id))
	}
	lctx, stop := context.WithCancel(context.Background())
	e.stop = stop
	go s.follow(lctx, e)
	e.port = s.env.Docker.HostPort(id, riePort)
	if err := s.waitReady(ctx, e); err != nil {
		return fail(err)
	}
	return e, nil
}

// follow copies an environment's container output into its line log.
func (s *Service) follow(ctx context.Context, e *execEnv) {
	defer core.Recover("lambda logs")
	w := e.logs.writer()
	defer w.Close()
	_ = s.env.Docker.C.Logs(docker.LogsOptions{Context: ctx, Container: e.id, OutputStream: w, ErrorStream: w,
		Stdout: true, Stderr: true, Follow: true, Tail: "all"})
}

var rieClient = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: time.Minute}}

func invokeURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/2015-03-31/functions/function/invocations", port)
}

// waitReady waits for the emulator to accept requests.
func (s *Service) waitReady(ctx context.Context, e *execEnv) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if st := s.env.Docker.State(e.id); st != "running" {
			return core.Errf(http.StatusBadGateway, "ServiceException", "execution environment exited during startup: %s", tail(e.logs.text(), 1024))
		}
		if e.port > 0 {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", e.port), nil)
			if resp, err := rieClient.Do(req); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				return nil
			}
		} else {
			r, err := s.env.Docker.Exec(ctx, e.id, []string{"curl", "-s", "-o", "/dev/null", "http://127.0.0.1:8080/"}, nil)
			if err == nil && r.ExitCode == 0 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return core.Errf(http.StatusBadGateway, "ServiceException", "execution environment did not start")
}

// post sends one invocation to the environment's emulator and returns its
// response body.
func (s *Service) post(ctx context.Context, e *execEnv, reqID, clientCtx string, payload []byte) ([]byte, error) {
	if e.port > 0 {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, invokeURL(e.port), bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Amzn-RequestId", reqID)
		if clientCtx != "" {
			req.Header.Set("X-Amz-Client-Context", clientCtx)
		}
		resp, err := rieClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(io.LimitReader(resp.Body, 7<<20))
	}
	// No published port (the environment is on an isolated network): call the
	// emulator from inside the container.
	cmd := []string{"curl", "-s", "-S", "-X", "POST", "-H", "X-Amzn-RequestId: " + reqID, "--data-binary", "@-"}
	if clientCtx != "" {
		cmd = append(cmd, "-H", "X-Amz-Client-Context: "+clientCtx)
	}
	r, err := s.env.Docker.Exec(ctx, e.id, append(cmd, "http://127.0.0.1:8080/2015-03-31/functions/function/invocations"), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 {
		return nil, fmt.Errorf("emulator request failed: %s", strings.TrimSpace(r.Stderr))
	}
	return []byte(r.Stdout), nil
}

// Run reaps idle environments until ctx is done.
func (s *Service) Run(ctx context.Context) {
	if s.env.Docker != nil {
		// Environments from a previous server run are stale.
		if cs, err := s.env.Docker.ManagedContainers(); err == nil {
			for _, c := range cs {
				if c.Labels["homecloud.service"] == "lambda" {
					_ = s.env.Docker.Remove(c.ID)
				}
			}
		}
		for _, f := range store.List[Function](s.env.Store, cFunctions) {
			if s.vpc != nil {
				s.vpc.Release("lambda:" + f.Name) // pre-pool allocation
			}
			if f.State == "Pending" || f.LastUpdateStatus == "InProgress" {
				go s.prepare(f.Name, f.RevisionID, f.State == "Pending")
			}
		}
	}
	go s.runAsync(ctx)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.reap()
	}
}

func (s *Service) reap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pools {
		for _, e := range append([]*execEnv(nil), p.envs...) {
			if e.busy {
				continue
			}
			idle := time.Since(e.lastUsed) > idleTimeout
			expiring := !e.expires.IsZero() && time.Until(e.expires) < 20*time.Minute
			if idle || expiring {
				s.removeLocked(p, e)
			}
		}
	}
}

// ---- container output ----

type logLine struct {
	t    time.Time
	text string
}

// lineLog buffers an environment's output lines.
type lineLog struct {
	mu     sync.Mutex
	lines  []logLine
	base   int // absolute index of lines[0]
	signal chan struct{}
}

func newLineLog() *lineLog { return &lineLog{signal: make(chan struct{})} }

func (l *lineLog) add(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, logLine{time.Now(), text})
	if len(l.lines) > 20000 { // a runaway function: keep the newest lines
		drop := len(l.lines) - 10000
		l.lines = append([]logLine(nil), l.lines[drop:]...)
		l.base += drop
	}
	close(l.signal)
	l.signal = make(chan struct{})
}

// mark is the absolute index of the next line.
func (l *lineLog) mark() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.base + len(l.lines)
}

// until waits (up to d) for a line containing needle at or after from, and
// returns the lines from from through it (or everything so far on timeout).
// Returned lines are dropped from the buffer.
func (l *lineLog) until(from int, needle string, d time.Duration) []logLine {
	deadline := time.Now().Add(d)
	for {
		l.mu.Lock()
		start := max(from-l.base, 0)
		for i := start; i < len(l.lines); i++ {
			if strings.Contains(l.lines[i].text, needle) {
				out := append([]logLine(nil), l.lines[start:i+1]...)
				l.lines = append([]logLine(nil), l.lines[i+1:]...)
				l.base += i + 1
				l.mu.Unlock()
				return out
			}
		}
		ch := l.signal
		left := time.Until(deadline)
		if left <= 0 {
			out := append([]logLine(nil), l.lines[start:]...)
			l.base += len(l.lines)
			l.lines = nil
			l.mu.Unlock()
			return out
		}
		l.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(left):
		}
	}
}

func (l *lineLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, x := range l.lines {
		b.WriteString(x.text + "\n")
	}
	return b.String()
}

// writer returns an io.Writer that splits output into lines.
func (l *lineLog) writer() io.WriteCloser {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			l.add(strings.TrimRight(sc.Text(), "\r"))
		}
		io.Copy(io.Discard, pr)
	}()
	return pw
}

// isEmulatorLine matches the emulator's own diagnostics, which are not function output:
// "29 Sep 2026 13:04:37,154 [INFO] (rapid) ...".
func isEmulatorLine(s string) bool {
	if len(s) < 26 || s[2] != ' ' || s[6] != ' ' || s[20] != ',' {
		return false
	}
	return strings.Contains(s[:min(len(s), 48)], "] (rapid) ")
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
