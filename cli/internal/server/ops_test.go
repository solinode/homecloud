package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
)

// fakeAuth accepts "Bearer <user>:secret"; admin may do anything, reader may
// only read metrics, nobody may do nothing.
type fakeAuth struct{}

func (fakeAuth) Authenticate(r *http.Request) (*httpx.Principal, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	user, secret, ok := strings.Cut(tok, ":")
	if !ok || secret != "secret" {
		return nil, core.Errf(http.StatusUnauthorized, "InvalidClientTokenId", "bad credentials")
	}
	allow := map[string]func(string) bool{
		"admin":  func(string) bool { return true },
		"reader": func(a string) bool { return a == ActionGetMetrics },
		"nobody": func(string) bool { return false },
	}[user]
	if allow == nil {
		return nil, core.Errf(http.StatusUnauthorized, "InvalidClientTokenId", "unknown key")
	}
	return &httpx.Principal{UserName: user, ARN: "arn:aws:iam::000000000000:user/" + user,
		Can: func(action, _ string) bool { return allow(action) }}, nil
}

type fakeLambda struct{}

func (fakeLambda) EnvironmentStats() lambda.EnvStats {
	return lambda.EnvStats{Idle: 2, Busy: 1, ColdStarts: 3, WarmStarts: 40}
}

var testAWSService = &awsapi.Service{Name: "hcmetricstest", JSONPrefix: "HCMetricsTest", JSONVersion: "1.0",
	Ops:       map[string]awsapi.Op{"Ping": func(q *awsapi.Req) (any, error) { return map[string]string{}, nil }},
	PublicOps: map[string]bool{"Ping": true}}

// testOps builds the request path the server uses: instrumentation, the AWS
// handler for AWS requests, and the native router with the ops routes.
func testOps(t *testing.T) (*ops, http.Handler) {
	t.Helper()
	awsapi.Register(testAWSService)
	cfg := core.Config{DataDir: t.TempDir(), APIAddr: "127.0.0.1:0", Region: "us-east-1"}
	o := newOps(cfg, nil, fakeLambda{}, "000000000000", time.Now().Add(-time.Hour))
	o.measureDisk(t.Context())
	mux := http.NewServeMux()
	rt := &httpx.Router{Mux: mux, Auth: fakeAuth{}, Account: "000000000000"}
	o.Routes(rt)
	rt.Handle("GET /api/v1/s3/buckets/{bucket}", "s3:ListBucket", func(c *httpx.Ctx) (any, error) {
		if c.Param("bucket") == "missing-bucket-7f3a" {
			return nil, core.Errf(http.StatusNotFound, "NoSuchBucket", "no bucket")
		}
		return map[string]string{"bucket": c.Param("bucket")}, nil
	}, httpx.Res("arn:aws:s3:::{bucket}"))
	aws := &awsapi.Handler{Account: "000000000000"}
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == metricsPath && !awsapi.IsSigned(r) {
			mux.ServeHTTP(w, basicToBearer(r))
			return
		}
		if awsapi.Match(r) {
			aws.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return o, o.instrument(root)
}

func do(h http.Handler, method, path, auth string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	w := do(h, "GET", "/metrics", "Bearer admin:secret", nil)
	if w.Code != 200 {
		t.Fatalf("scrape: %d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content type %q", ct)
	}
	return w.Body.String()
}

func TestMetricsExposition(t *testing.T) {
	core.Beat("test-loop", time.Minute)
	_, h := testOps(t)
	// Native calls with IDs in the path, one failing.
	for _, b := range []string{"photos-1a2b", "backups-9z8y", "missing-bucket-7f3a"} {
		do(h, "GET", "/api/v1/s3/buckets/"+b, "Bearer admin:secret", nil)
	}
	do(h, "GET", "/api/v1/s3/buckets/photos-1a2b", "Bearer bogus-key-123:wrong", nil) // auth failure
	do(h, "GET", "/api/v1/s3/buckets/photos-1a2b", "Bearer nobody:secret", nil)       // denied
	// AWS calls: one known operation, one made-up operation.
	do(h, "POST", "/", "", map[string]string{"X-Amz-Target": "HCMetricsTest.Ping", "Content-Type": "application/x-amz-json-1.0"})
	do(h, "POST", "/", "", map[string]string{"X-Amz-Target": "HCMetricsTest.Op-req-5c1d9e", "Content-Type": "application/x-amz-json-1.0"})
	// Console and user content.
	do(h, "GET", "/s3/buckets/photos-1a2b", "", nil)
	do(h, "GET", "/website/photos-1a2b/index.html", "", nil)

	out := scrape(t, h)
	for _, want := range []string{
		`homecloud_build_info{version="`,
		`homecloud_http_requests_total{api="native",service="s3",operation="GET /api/v1/s3/buckets/{bucket}",status_class="2xx"} 2`,
		`homecloud_http_requests_total{api="native",service="s3",operation="GET /api/v1/s3/buckets/{bucket}",status_class="4xx"} 3`,
		`homecloud_http_request_errors_total{api="native",service="s3",code="NoSuchBucket"} 1`,
		`homecloud_http_request_errors_total{api="native",service="s3",code="AccessDenied"} 1`,
		`homecloud_auth_failures_total{api="native",code="InvalidClientTokenId"} 1`,
		`homecloud_http_requests_total{api="aws",service="hcmetricstest",operation="Ping",status_class="2xx"} 1`,
		`homecloud_http_requests_total{api="aws",service="hcmetricstest",operation="unknown",status_class="4xx"} 1`,
		`homecloud_auth_failures_total{api="aws",code="MissingAuthenticationToken"} 1`,
		`homecloud_http_requests_total{api="other",service="console",operation="",status_class=`,
		`homecloud_http_requests_total{api="other",service="website",operation="",status_class=`,
		`homecloud_http_request_duration_seconds_bucket{api="native",service="s3",le="0.005"}`,
		`homecloud_http_request_duration_seconds_bucket{api="native",service="s3",le="+Inf"} 5`,
		`homecloud_http_request_duration_seconds_count{api="native",service="s3"} 5`,
		"homecloud_http_requests_in_flight 1", // the scrape itself
		"homecloud_docker_up 0",
		"homecloud_data_dir_bytes ",
		`homecloud_loop_last_run_age_seconds{loop="test-loop"}`,
		`homecloud_loop_interval_seconds{loop="test-loop"} 60`,
		`homecloud_lambda_environments{state="idle"} 2`,
		`homecloud_lambda_starts_total{kind="cold"} 3`,
		"go_goroutines ", "process_start_time_seconds ",
		"# TYPE homecloud_http_request_duration_seconds histogram",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// No ID or caller-chosen text may become a label value.
	for _, bad := range []string{"photos-1a2b", "backups-9z8y", "missing-bucket-7f3a", "bogus-key", "req-5c1d9e", "index.html"} {
		if strings.Contains(out, bad) {
			t.Errorf("high-cardinality value %q leaked into the metrics", bad)
		}
	}
	// Every sample line is well-formed and its labels come from the known set.
	sample := regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(.*)\})? (\S+)$`)
	pair := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"`)
	allowed := map[string]bool{"version": true, "commit": true, "go_version": true, "api": true, "service": true, "operation": true,
		"status_class": true, "code": true, "le": true, "state": true, "helper": true, "loop": true, "kind": true}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		m := sample.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("malformed sample line %q", line)
			continue
		}
		for _, p := range pair.FindAllStringSubmatch(m[3], -1) {
			if !allowed[p[1]] {
				t.Errorf("unexpected label %q in %q", p[1], line)
			}
		}
	}
}

func TestMetricsAndStatusAuth(t *testing.T) {
	_, h := testOps(t)
	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
	cases := []struct {
		path, auth string
		want       int
	}{
		{"/metrics", "", http.StatusUnauthorized},
		{"/metrics", "Bearer reader:wrong", http.StatusUnauthorized},
		{"/metrics", basic("reader", "wrong"), http.StatusUnauthorized},
		{"/metrics", "Bearer nobody:secret", http.StatusForbidden},
		{"/metrics", "Bearer reader:secret", http.StatusOK},
		{"/metrics", basic("reader", "secret"), http.StatusOK},
		{"/api/v1/system/status", "", http.StatusUnauthorized},
		{"/api/v1/system/status", "Bearer reader:secret", http.StatusForbidden}, // GetMetrics does not grant status
		{"/api/v1/system/status", "Bearer admin:secret", http.StatusOK},
	}
	for _, c := range cases {
		if w := do(h, "GET", c.path, c.auth, nil); w.Code != c.want {
			t.Errorf("GET %s with %q: %d, want %d: %s", c.path, c.auth, w.Code, c.want, w.Body)
		}
	}
	// Basic auth is accepted only on /metrics.
	if w := do(h, "GET", "/api/v1/system/status", basic("admin", "secret"), nil); w.Code != http.StatusUnauthorized {
		t.Errorf("basic auth on status: %d", w.Code)
	}
}

func TestStatusShape(t *testing.T) {
	core.Beat("test-loop", time.Minute)
	core.ReportError("test", "something broke")
	_, h := testOps(t)
	w := do(h, "GET", "/api/v1/system/status", "Bearer admin:secret", nil)
	if w.Code != 200 {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"status", "version", "commit", "go_version", "region", "account_id", "started_at", "uptime_seconds",
		"host", "process", "docker", "subsystems", "disk", "containers", "lambda", "recent_errors", "metrics"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("status lacks %q", k)
		}
	}
	if doc["status"] != "down" { // no Docker in this test
		t.Errorf("overall status %v, want down without Docker", doc["status"])
	}
	if up, _ := doc["uptime_seconds"].(float64); up < 3500 {
		t.Errorf("uptime %v", doc["uptime_seconds"])
	}
	subs := map[string]map[string]any{}
	for _, s := range doc["subsystems"].([]any) {
		m := s.(map[string]any)
		subs[m["name"].(string)] = m
		switch m["status"] {
		case "ok", "warning", "error", "unknown":
		default:
			t.Errorf("subsystem %v has status %v", m["name"], m["status"])
		}
	}
	for name, want := range map[string]string{"api": "ok", "docker": "error", "minio": "unknown", "ecr": "unknown", "dns": "unknown", "imds": "unknown", "loop:test-loop": "ok"} {
		if s := subs[name]; s == nil || s["status"] != want {
			t.Errorf("subsystem %s: %v, want status %s", name, s, want)
		}
	}
	if subs["loop:test-loop"]["last_run_age_seconds"] == nil {
		t.Error("loop without last_run_age_seconds")
	}
	disk := doc["disk"].(map[string]any)
	dd := disk["data_dir"].(map[string]any)
	if dd["path"] == "" || dd["used_bytes"] == nil || disk["measured_at"] == nil {
		t.Errorf("disk: %v", disk)
	}
	lam := doc["lambda"].(map[string]any)
	if lam["environments_idle"] != 2.0 || lam["warm_starts"] != 40.0 {
		t.Errorf("lambda: %v", lam)
	}
	errs := doc["recent_errors"].([]any)
	if len(errs) == 0 || errs[0].(map[string]any)["message"] != "something broke" {
		t.Errorf("recent errors: %v", errs)
	}
	if m := doc["metrics"].(map[string]any); m["action"] != ActionGetMetrics || m["path"] != "/metrics" {
		t.Errorf("metrics: %v", m)
	}
}

func TestValidateMetricsAddr(t *testing.T) {
	for _, c := range []struct {
		addr     string
		insecure bool
		ok       bool
	}{
		{"", false, true},
		{"127.0.0.1:9464", false, true},
		{"[::1]:9464", false, true},
		{"localhost:9464", false, true},
		{"0.0.0.0:9464", false, false},
		{":9464", false, false},
		{"192.168.1.10:9464", false, false},
		{"0.0.0.0:9464", true, true},
		{"127.0.0.1", false, false},
	} {
		if err := ValidateMetricsAddr(c.addr, c.insecure); (err == nil) != c.ok {
			t.Errorf("ValidateMetricsAddr(%q, %v) = %v", c.addr, c.insecure, err)
		}
	}
}

func TestMetricsAddrListener(t *testing.T) {
	o, _ := testOps(t)
	o.cfg.MetricsAddr = "127.0.0.1:0"
	addr, stop, err := o.serveMetricsAddr(t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	resp, err := http.Get("http://" + addr + "/metrics") // no credentials
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "homecloud_build_info") {
		t.Fatalf("metrics listener: %d %s", resp.StatusCode, b)
	}
	// Only /metrics is served there.
	if resp, err := http.Get("http://" + addr + "/api/v1/system/status"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status on the metrics listener: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	o.cfg.MetricsAddr = "0.0.0.0:9464"
	if _, _, err := o.serveMetricsAddr(t.Logf); err == nil {
		t.Fatal("a public metrics address without --metrics-addr-insecure was accepted")
	}
}
