// Package lambda implements serverless functions. Each function gets a warm,
// resource-limited container holding its code; every invocation is a fresh
// process in that container, fed the event on stdin. Invocations are logged to
// CloudWatch Logs and measured in CloudWatch Metrics.
package lambda

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

const (
	cFunctions   = "lambda_functions"
	maxCodeBytes = 50 << 20
	idleTimeout  = 15 * time.Minute
	metricsNS    = "HC/Lambda"
)

type FunctionURL struct {
	Enabled  bool   `json:"enabled"`
	AuthType string `json:"auth_type"` // NONE | HC_IAM
	URL      string `json:"url,omitempty"`
}

type Function struct {
	Name         string            `json:"name"`
	ARN          string            `json:"arn"`
	Runtime      string            `json:"runtime"`
	Handler      string            `json:"handler"`
	Description  string            `json:"description"`
	MemoryMB     int64             `json:"memory_mb"`
	TimeoutSec   int               `json:"timeout_seconds"`
	Environment  map[string]string `json:"environment"`
	CodeSHA256   string            `json:"code_sha256"`
	CodeSize     int64             `json:"code_size"`
	State        string            `json:"state"`
	URL          FunctionURL       `json:"function_url"`
	LogGroup     string            `json:"log_group"`
	SubnetID     string            `json:"subnet_id,omitempty"`
	LastModified time.Time         `json:"last_modified"`
	CreatedAt    time.Time         `json:"created_at"`
	Tags         core.Tags         `json:"tags,omitempty"`
}

type warm struct {
	id       string
	codeSHA  string
	lastUsed time.Time
}

type Service struct {
	env     *svc.Env
	cw      *cloudwatch.Service
	vpc     *vpc.Service
	mu      sync.Mutex
	warm    map[string]*warm
	fnLocks sync.Map // function name -> *sync.Mutex (serialises container creation)
	// Auth authenticates function URL calls with auth type HC_IAM.
	Auth httpx.Authenticator
	// Queues is the SQS service, for event source mappings.
	Queues QueueSource
	// VerifyJWT validates user pool tokens for API Gateway routes that require them.
	VerifyJWT JWTVerifier
	// CheckAuthorizer verifies that a user pool (and optional app client of it) exists.
	CheckAuthorizer func(pool, client string) error
	// DNSFor returns resolver addresses for containers in a VPC (Route 53).
	DNSFor func(vpcID string) []string
}

func New(env *svc.Env, cw *cloudwatch.Service, v *vpc.Service) *Service {
	return &Service{env: env, cw: cw, vpc: v, warm: map[string]*warm{}}
}

func (s *Service) codePath(name string) string { return s.env.Cfg.Path("lambda", name+".zip") }

func (s *Service) urlFor(name string) string {
	_, port, _ := strings.Cut(s.env.Cfg.APIAddr, ":")
	return fmt.Sprintf("http://%s:%s/lambda-url/%s/", s.env.Cfg.PublicHost, port, name)
}

// ---- code handling ----

// codeInput accepts code as a base64 zip, inline files, or an S3-less template.
type codeInput struct {
	ZipBase64 string            `json:"zip_base64"`
	Files     map[string]string `json:"files"`
}

func (ci codeInput) zip() ([]byte, error) {
	if ci.ZipBase64 != "" {
		b, err := base64.StdEncoding.DecodeString(ci.ZipBase64)
		if err != nil {
			return nil, core.BadRequest("zip_base64 is not valid base64")
		}
		if _, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); err != nil {
			return nil, core.BadRequest("code is not a valid zip archive: %v", err)
		}
		return b, nil
	}
	if len(ci.Files) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(ci.Files))
	for n := range ci.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		clean := path.Clean(strings.TrimPrefix(n, "/"))
		if strings.HasPrefix(clean, "..") {
			return nil, core.BadRequest("invalid file name %q", n)
		}
		w, err := zw.Create(clean)
		if err != nil {
			return nil, err
		}
		w.Write([]byte(ci.Files[n]))
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func unzip(b []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := path.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxCodeBytes))
		rc.Close()
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > 4*maxCodeBytes {
			return nil, core.BadRequest("unzipped code exceeds %d MB", 4*maxCodeBytes>>20)
		}
		out[clean] = data
	}
	return out, nil
}

func (s *Service) saveCode(name string, b []byte) (string, error) {
	if len(b) > maxCodeBytes {
		return "", core.BadRequest("code zip exceeds %d MB", maxCodeBytes>>20)
	}
	if err := os.MkdirAll(s.env.Cfg.Path("lambda"), 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(sum[:]), os.WriteFile(s.codePath(name), b, 0o600)
}

// ---- warm containers ----

func (s *Service) fnLock(name string) *sync.Mutex {
	m, _ := s.fnLocks.LoadOrStore(name, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func containerName(fn string) string { return svc.ContainerName("lambda", fn) }

// container returns a running container with the function's current code.
func (s *Service) container(ctx context.Context, f Function) (string, error) {
	l := s.fnLock(f.Name)
	l.Lock()
	defer l.Unlock()
	s.mu.Lock()
	w := s.warm[f.Name]
	s.mu.Unlock()
	if w != nil && w.codeSHA == f.CodeSHA256+f.LastModified.String() && s.env.Docker.State(w.id) == "running" {
		s.mu.Lock()
		w.lastUsed = time.Now()
		s.mu.Unlock()
		return w.id, nil
	}
	_ = s.env.Docker.Remove(containerName(f.Name))
	rt, ok := findRuntime(f.Runtime)
	if !ok {
		return "", fmt.Errorf("unknown runtime %s", f.Runtime)
	}
	code, err := os.ReadFile(s.codePath(f.Name))
	if err != nil {
		return "", fmt.Errorf("read code: %w", err)
	}
	files, err := unzip(code)
	if err != nil {
		return "", fmt.Errorf("unpack code: %w", err)
	}
	env := map[string]string{"HC_FUNCTION_NAME": f.Name, "HC_FUNCTION_ARN": f.ARN, "HC_FUNCTION_MEMORY": fmt.Sprint(f.MemoryMB),
		"HC_FUNCTION_TIMEOUT": fmt.Sprint(f.TimeoutSec), "HC_HANDLER": f.Handler, "HC_REGION": s.env.Cfg.Region,
		"AWS_REGION": "us-east-1", "PYTHONDONTWRITEBYTECODE": "1", "LAMBDA_TASK_ROOT": "/var/task"}
	for k, v := range f.Environment {
		env[k] = v
	}
	pl, err := s.vpc.Place(f.SubnetID, "lambda:"+f.Name)
	if err != nil {
		return "", err
	}
	var dns []string
	if s.DNSFor != nil {
		dns = s.DNSFor(pl.VPC.ID)
	}
	id, err := s.env.Docker.Run(ctx, runtime.RunSpec{
		DNS:        dns,
		Name:       containerName(f.Name),
		Image:      rt.Image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done"},
		Env:        env,
		Labels:     runtime.Labels("lambda", f.Name, nil),
		MemoryMB:   f.MemoryMB,
		NanoCPUs:   int64(max(0.25, float64(f.MemoryMB)/1769) * 1e9), // CPU scales with memory, as in AWS
		Network:    pl.Network,
		IP:         pl.IP,
		WorkingDir: "/var/task",
	})
	if err != nil {
		return "", err
	}
	task := map[string][]byte{"opt/homecloud/" + rt.bootstrapFile: []byte(rt.bootstrap)}
	for n, b := range files {
		task["var/task/"+n] = b
	}
	if err := s.env.Docker.CopyIn(ctx, id, "/", task, 0o755); err != nil {
		_ = s.env.Docker.Remove(id)
		return "", fmt.Errorf("copy code: %w", err)
	}
	if err := s.env.Docker.Start(id); err != nil {
		_ = s.env.Docker.Remove(id)
		return "", err
	}
	s.mu.Lock()
	s.warm[f.Name] = &warm{id: id, codeSHA: f.CodeSHA256 + f.LastModified.String(), lastUsed: time.Now()}
	s.mu.Unlock()
	return id, nil
}

func (s *Service) retire(name string) {
	l := s.fnLock(name)
	l.Lock()
	defer l.Unlock()
	s.mu.Lock()
	delete(s.warm, name)
	s.mu.Unlock()
	_ = s.env.Docker.Remove(containerName(name))
	s.vpc.Release("lambda:" + name)
}

// Run stops containers of functions that have been idle for a while.
func (s *Service) Run(ctx context.Context) {
	// Containers from a previous server run are stale: the warm table is empty.
	if cs, err := s.env.Docker.ManagedContainers(); err == nil {
		for _, c := range cs {
			if c.Labels["homecloud.service"] == "lambda" {
				_ = s.env.Docker.Remove(c.ID)
			}
		}
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		var idle []string
		for n, w := range s.warm {
			if time.Since(w.lastUsed) > idleTimeout {
				idle = append(idle, n)
			}
		}
		s.mu.Unlock()
		for _, n := range idle {
			s.retire(n)
		}
	}
}

// ---- invocation ----

type InvokeResult struct {
	RequestID     string          `json:"request_id"`
	StatusCode    int             `json:"status_code"`
	Payload       json.RawMessage `json:"payload"`
	FunctionError string          `json:"function_error,omitempty"`
	Logs          string          `json:"logs"`
	DurationMS    float64         `json:"duration_ms"`
	BilledMS      int64           `json:"billed_duration_ms"`
	ColdStart     bool            `json:"cold_start"`
}

var ErrTooManyRequests = errors.New("too many requests")

// Invoke runs the function synchronously with payload as the event.
func (s *Service) Invoke(ctx context.Context, name string, payload []byte) (*InvokeResult, error) {
	f, err := store.Get[Function](s.env.Store, cFunctions, name)
	if err != nil {
		return nil, core.NotFound("function", name)
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if !json.Valid(payload) {
		return nil, core.BadRequest("payload must be JSON")
	}
	reqID := uuid()
	s.mu.Lock()
	cold := s.warm[name] == nil
	s.mu.Unlock()
	cid, err := s.container(ctx, f)
	if err != nil {
		s.cw.Put(metricsNS, "Errors", map[string]string{"FunctionName": name}, "Count", 1, time.Time{})
		return nil, fmt.Errorf("start function environment: %w", err)
	}
	rt, _ := findRuntime(f.Runtime)
	stream := time.Now().UTC().Format("2006/01/02") + "/[$LATEST]" + cid[:12]
	start := time.Now()
	ex, err := s.env.Docker.C.CreateExec(docker.CreateExecOptions{
		Container: cid, Cmd: rt.invoke, AttachStdin: true, AttachStdout: true, AttachStderr: true, WorkingDir: "/var/task",
		Env: []string{"HC_REQUEST_ID=" + reqID, "HC_LOG_GROUP=" + f.LogGroup, "HC_LOG_STREAM=" + stream, "AWS_LAMBDA_REQUEST_ID=" + reqID},
	})
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	tctx, cancel := context.WithTimeout(ctx, time.Duration(f.TimeoutSec)*time.Second)
	defer cancel()
	execErr := s.env.Docker.C.StartExec(ex.ID, docker.StartExecOptions{
		InputStream: bytes.NewReader(payload), OutputStream: &stdout, ErrorStream: &stderr, Context: tctx,
	})
	dur := time.Since(start)
	res := &InvokeResult{RequestID: reqID, StatusCode: 200, DurationMS: float64(dur.Microseconds()) / 1000, ColdStart: cold}
	res.BilledMS = int64(math.Ceil(res.DurationMS))
	timedOut := errors.Is(tctx.Err(), context.DeadlineExceeded)
	switch {
	case timedOut:
		res.FunctionError = "Unhandled"
		res.Payload, _ = json.Marshal(map[string]string{"errorMessage": fmt.Sprintf("Task timed out after %d.00 seconds", f.TimeoutSec), "errorType": "TimeoutError"})
		go s.retire(name) // kill the runaway process
	case execErr != nil:
		return nil, execErr
	default:
		var out struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			res.FunctionError = "Unhandled"
			res.Payload, _ = json.Marshal(map[string]string{"errorMessage": "Runtime exited without providing a reason", "errorType": "Runtime.ExitError"})
		} else if out.OK {
			res.Payload = out.Result
		} else {
			res.FunctionError = "Unhandled"
			res.Payload = out.Error
		}
	}
	if res.Payload == nil {
		res.Payload = json.RawMessage("null")
	}
	logs := stderr.String()
	res.Logs = logs
	// CloudWatch Logs, in the familiar START/END/REPORT shape.
	events := []cloudwatch.LogEvent{{Timestamp: start, Message: fmt.Sprintf("START RequestId: %s Version: $LATEST", reqID)}}
	for _, line := range strings.Split(strings.TrimRight(logs, "\n"), "\n") {
		if line != "" {
			events = append(events, cloudwatch.LogEvent{Timestamp: time.Now(), Message: line})
		}
	}
	if timedOut {
		events = append(events, cloudwatch.LogEvent{Timestamp: time.Now(), Message: fmt.Sprintf("%s Task timed out after %d.00 seconds", time.Now().UTC().Format(time.RFC3339), f.TimeoutSec)})
	}
	events = append(events,
		cloudwatch.LogEvent{Timestamp: time.Now(), Message: "END RequestId: " + reqID},
		cloudwatch.LogEvent{Timestamp: time.Now(), Message: fmt.Sprintf("REPORT RequestId: %s\tDuration: %.2f ms\tBilled Duration: %d ms\tMemory Size: %d MB%s", reqID, res.DurationMS, res.BilledMS, f.MemoryMB, map[bool]string{true: "\tInit: cold start", false: ""}[cold])},
	)
	if err := s.cw.Append(f.LogGroup, stream, events...); err != nil {
		log.Printf("lambda: write logs: %v", err)
	}
	dims := map[string]string{"FunctionName": name}
	s.cw.Put(metricsNS, "Invocations", dims, "Count", 1, time.Time{})
	s.cw.Put(metricsNS, "Duration", dims, "Milliseconds", res.DurationMS, time.Time{})
	if res.FunctionError != "" {
		s.cw.Put(metricsNS, "Errors", dims, "Count", 1, time.Time{})
	} else {
		s.cw.Put(metricsNS, "Errors", dims, "Count", 0, time.Time{})
	}
	if len(res.Logs) > 4096 {
		res.Logs = res.Logs[len(res.Logs)-4096:]
	}
	return res, nil
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Exists reports whether a function exists (used by other services).
func (s *Service) Exists(name string) bool { return store.Has(s.env.Store, cFunctions, name) }

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:lambda:local-1:{account}:function:{name}")
	r.Handle("GET /api/v1/lambda/runtimes", "lambda:ListRuntimes", s.listRuntimes)
	r.Handle("GET /api/v1/lambda/functions", "lambda:ListFunctions", s.list)
	r.Handle("POST /api/v1/lambda/functions", "lambda:CreateFunction", s.create)
	r.Handle("GET /api/v1/lambda/functions/{name}", "lambda:GetFunction", s.get, res)
	r.Handle("PATCH /api/v1/lambda/functions/{name}", "lambda:UpdateFunctionConfiguration", s.updateConfig, res)
	r.Handle("PUT /api/v1/lambda/functions/{name}/code", "lambda:UpdateFunctionCode", s.updateCode, res)
	r.Handle("GET /api/v1/lambda/functions/{name}/code", "lambda:GetFunction", s.getCode, res)
	r.Handle("DELETE /api/v1/lambda/functions/{name}", "lambda:DeleteFunction", s.delete, res)
	r.Handle("POST /api/v1/lambda/functions/{name}/invoke", "lambda:InvokeFunction", s.invoke, res)
	r.Handle("PUT /api/v1/lambda/functions/{name}/url", "lambda:CreateFunctionUrlConfig", s.putURL, res)
	r.Handle("/lambda-url/{name}/{path...}", "", s.serveURL, httpx.Public())
	s.apigwRoutes(r)
	s.esmRoutes(r)
}

func (s *Service) listRuntimes(c *httpx.Ctx) (any, error) { return runtimes, nil }

func (s *Service) list(c *httpx.Ctx) (any, error) {
	return store.List[Function](s.env.Store, cFunctions), nil
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	f, err := store.Get[Function](s.env.Store, cFunctions, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("function", c.Param("name"))
	}
	s.mu.Lock()
	w := s.warm[f.Name]
	s.mu.Unlock()
	state := "Idle"
	if w != nil {
		state = "Warm"
	}
	return map[string]any{"configuration": f, "environment_state": state}, nil
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type configInput struct {
	Runtime     string            `json:"runtime"`
	Handler     string            `json:"handler"`
	Description *string           `json:"description"`
	MemoryMB    int64             `json:"memory_mb"`
	TimeoutSec  int               `json:"timeout_seconds"`
	Environment map[string]string `json:"environment"`
	SubnetID    *string           `json:"subnet_id"`
	Tags        core.Tags         `json:"tags"`
}

func applyConfig(f *Function, in configInput) error {
	if in.Runtime != "" {
		if _, ok := findRuntime(in.Runtime); !ok {
			return core.BadRequest("unsupported runtime %q", in.Runtime)
		}
		f.Runtime = in.Runtime
	}
	if in.Handler != "" {
		if !strings.Contains(in.Handler, ".") {
			return core.BadRequest("handler must look like file.function")
		}
		f.Handler = in.Handler
	}
	if in.Description != nil {
		f.Description = *in.Description
	}
	if in.MemoryMB != 0 {
		if in.MemoryMB < 128 || in.MemoryMB > 10240 {
			return core.BadRequest("memory_mb must be between 128 and 10240")
		}
		f.MemoryMB = in.MemoryMB
	}
	if in.TimeoutSec != 0 {
		if in.TimeoutSec < 1 || in.TimeoutSec > 900 {
			return core.BadRequest("timeout_seconds must be between 1 and 900")
		}
		f.TimeoutSec = in.TimeoutSec
	}
	if in.Environment != nil {
		f.Environment = in.Environment
	}
	if in.SubnetID != nil {
		f.SubnetID = *in.SubnetID
	}
	if in.Tags != nil {
		f.Tags = in.Tags
	}
	return nil
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in struct {
		Name string `json:"name"`
		configInput
		Code codeInput `json:"code"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !nameRe.MatchString(in.Name) {
		return nil, core.BadRequest("function name must be 1-64 letters, digits, hyphens or underscores")
	}
	if store.Has(s.env.Store, cFunctions, in.Name) {
		return nil, core.Conflict("function %q already exists", in.Name)
	}
	if in.Runtime == "" {
		in.Runtime = "python3.12"
	}
	rt, ok := findRuntime(in.Runtime)
	if !ok {
		return nil, core.BadRequest("unsupported runtime %q", in.Runtime)
	}
	f := Function{Name: in.Name, ARN: s.env.ARN("lambda", "function:"+in.Name), Runtime: rt.Name, Handler: rt.DefaultHandler,
		MemoryMB: 128, TimeoutSec: 3, Environment: map[string]string{}, State: "Active", LogGroup: "/aws/lambda/" + in.Name,
		URL: FunctionURL{AuthType: "NONE"}, CreatedAt: core.Now(), LastModified: core.Now()}
	if err := applyConfig(&f, in.configInput); err != nil {
		return nil, err
	}
	code, err := in.Code.zip()
	if err != nil {
		return nil, err
	}
	if code == nil {
		code, _ = codeInput{Files: map[string]string{rt.DefaultFile: rt.Template}}.zip()
		f.Handler = rt.DefaultHandler
	}
	sum, err := s.saveCode(f.Name, code)
	if err != nil {
		return nil, err
	}
	f.CodeSHA256, f.CodeSize = sum, int64(len(code))
	return f, store.Put(s.env.Store, cFunctions, f.Name, f)
}

func (s *Service) update(name string, fn func(*Function) error) (Function, error) {
	f, err := store.Update(s.env.Store, cFunctions, name, func(f *Function) error {
		if err := fn(f); err != nil {
			return err
		}
		f.LastModified = time.Now().UTC()
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return f, core.NotFound("function", name)
	}
	return f, err
}

func (s *Service) updateConfig(c *httpx.Ctx) (any, error) {
	var in configInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	f, err := s.update(c.Param("name"), func(f *Function) error { return applyConfig(f, in) })
	if err == nil {
		go s.retire(f.Name)
	}
	return f, err
}

func (s *Service) updateCode(c *httpx.Ctx) (any, error) {
	var in codeInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	code, err := in.zip()
	if err != nil {
		return nil, err
	}
	if code == nil {
		return nil, core.BadRequest("provide zip_base64 or files")
	}
	name := c.Param("name")
	if !store.Has(s.env.Store, cFunctions, name) {
		return nil, core.NotFound("function", name)
	}
	sum, err := s.saveCode(name, code)
	if err != nil {
		return nil, err
	}
	f, err := s.update(name, func(f *Function) error { f.CodeSHA256, f.CodeSize = sum, int64(len(code)); return nil })
	if err == nil {
		go s.retire(name)
	}
	return f, err
}

// getCode returns the function's files as text when they are small enough to edit in the console.
func (s *Service) getCode(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if !store.Has(s.env.Store, cFunctions, name) {
		return nil, core.NotFound("function", name)
	}
	b, err := os.ReadFile(s.codePath(name))
	if err != nil {
		return nil, err
	}
	if c.Query("format") == "zip" {
		c.W.Header().Set("Content-Type", "application/zip")
		c.W.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
		c.MarkWritten()
		c.W.Write(b)
		return nil, nil
	}
	files, err := unzip(b)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	editable := true
	for n, data := range files {
		if len(data) > 256<<10 || bytes.IndexByte(data, 0) >= 0 {
			editable = false
			continue
		}
		out[n] = string(data)
	}
	if len(files) > 50 {
		editable = false
	}
	sum := sha256.Sum256(b)
	return map[string]any{"files": out, "editable": editable, "file_count": len(files), "sha256_hex": hex.EncodeToString(sum[:])}, nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if !store.Has(s.env.Store, cFunctions, name) {
		return nil, core.NotFound("function", name)
	}
	s.retire(name)
	for _, m := range store.List[Mapping](s.env.Store, cMappings) {
		if m.FunctionName == name {
			_ = store.Delete(s.env.Store, cMappings, m.ID)
		}
	}
	_ = os.Remove(s.codePath(name))
	return nil, store.Delete(s.env.Store, cFunctions, name)
}

func (s *Service) invoke(c *httpx.Ctx) (any, error) {
	payload, err := io.ReadAll(io.LimitReader(c.R.Body, 6<<20))
	if err != nil {
		return nil, err
	}
	name := c.Param("name")
	if c.Query("invocation_type") == "Event" {
		if !s.Exists(name) {
			return nil, core.NotFound("function", name)
		}
		go func() {
			if _, err := s.Invoke(context.Background(), name, payload); err != nil {
				log.Printf("lambda: async invoke %s: %v", name, err)
			}
		}()
		return c.JSON(http.StatusAccepted, map[string]any{"status_code": 202})
	}
	return s.Invoke(c.R.Context(), name, payload)
}

func (s *Service) putURL(c *httpx.Ctx) (any, error) {
	var in FunctionURL
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.AuthType == "" {
		in.AuthType = "NONE"
	}
	if in.AuthType != "NONE" && in.AuthType != "HC_IAM" {
		return nil, core.BadRequest("auth_type must be NONE or HC_IAM")
	}
	if in.Enabled {
		in.URL = s.urlFor(c.Param("name"))
	} else {
		in.URL = ""
	}
	return s.update(c.Param("name"), func(f *Function) error { f.URL = in; return nil })
}
