// Package awstest runs an in-process AWS-protocol endpoint (IAM, STS and
// whatever services a test registers) without Docker, and drives it with the
// real AWS CLI and boto3, so AWS compatibility is tested against the tools
// people actually use.
//
//	h := awstest.New(t)
//	sqsSvc := sqs.New(h.Env)
//	sqsSvc.RegisterAWS()
//	out := h.AWS(t, "sqs", "create-queue", "--queue-name", "jobs")
//	res := h.Python(t, `print(boto3.client("sqs").list_queues())`)
//
// The AWS CLI is found on PATH (or HC_TEST_AWS); Python with boto3 is
// HC_TEST_PYTHON (default python3). Tests skip when a tool is missing.
package awstest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

// Harness is a running test endpoint.
type Harness struct {
	Env     *svc.Env
	IAM     *iam.Service
	Secrets *secrets.Service
	URL     string
	// Root credentials.
	AccessKeyID, SecretKey string
	// Mux serves the native HomeCloud API (register routes with Router).
	Mux    *http.ServeMux
	Router *httpx.Router
	// Audit records every audited call (action, resource); read it with AuditLog.
	Audit   []string
	auditMu sync.Mutex
	// barrier orders a test's setup writes (fields set on services after New)
	// before the requests that read them; the race detector can't see that
	// ordering through the AWS CLI subprocess.
	barrier sync.Mutex
}

func (h *Harness) sync() { h.barrier.Lock(); h.barrier.Unlock() }

// AuditLog returns a copy of the audited calls so far.
func (h *Harness) AuditLog() []string {
	h.auditMu.Lock()
	defer h.auditMu.Unlock()
	return append([]string(nil), h.Audit...)
}

// New starts an endpoint with IAM (root user) and STS.
func New(t *testing.T) *Harness {
	t.Helper()
	env := svctest.Env(t)
	sec, err := secrets.New(env)
	if err != nil {
		t.Fatal(err)
	}
	im := iam.New(env)
	im.Seal = sec
	boot, err := im.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	im.RegisterAWS()
	h := &Harness{Env: env, IAM: im, Secrets: sec, AccessKeyID: boot.AccessKeyID, SecretKey: boot.SecretKey, Mux: http.NewServeMux()}
	audit := func(p *httpx.Principal, action, resource string, r *http.Request, status int, took time.Duration) {
		h.auditMu.Lock()
		h.Audit = append(h.Audit, action+" "+resource)
		h.auditMu.Unlock()
	}
	h.Router = &httpx.Router{Mux: h.Mux, Auth: im, Account: env.AccountID, Audit: audit}
	im.Routes(h.Router)
	aws := &awsapi.Handler{Creds: im, Account: env.AccountID, Audit: audit}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.sync()
		if awsapi.Match(r) && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			aws.ServeHTTP(w, r)
			return
		}
		h.Mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h.URL = srv.URL
	return h
}

// Environ returns the environment for AWS tools using the given credentials.
func (h *Harness) Environ(akid, secret, token string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AWS_") {
			env = append(env, kv)
		}
	}
	env = append(env, "AWS_ENDPOINT_URL="+h.URL, "AWS_ACCESS_KEY_ID="+akid, "AWS_SECRET_ACCESS_KEY="+secret,
		"AWS_REGION=us-east-1", "AWS_DEFAULT_REGION=us-east-1", "AWS_PAGER=", "AWS_MAX_ATTEMPTS=1", "AWS_RETRY_MODE=standard",
		"AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null", "AWS_EC2_METADATA_DISABLED=true")
	if token != "" {
		env = append(env, "AWS_SESSION_TOKEN="+token)
	}
	return env
}

func awsBin(t *testing.T) string {
	if p := os.Getenv("HC_TEST_AWS"); p != "" {
		return p
	}
	p, err := exec.LookPath("aws")
	if err != nil {
		t.Skip("aws CLI not installed")
	}
	return p
}

// AWS runs the AWS CLI as root with JSON output and returns stdout; it fails
// the test if the command fails.
func (h *Harness) AWS(t *testing.T, args ...string) string {
	t.Helper()
	out, err := h.AWSErr(t, args...)
	if err != nil {
		t.Fatalf("aws %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// AWSErr runs the AWS CLI as root and returns combined output and the error.
func (h *Harness) AWSErr(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return h.AWSAs(t, h.AccessKeyID, h.SecretKey, "", args...)
}

// AWSAs runs the AWS CLI with the given credentials.
func (h *Harness) AWSAs(t *testing.T, akid, secret, token string, args ...string) (string, error) {
	t.Helper()
	h.sync()
	cmd := exec.Command(awsBin(t), append([]string{"--output", "json"}, args...)...)
	cmd.Env = h.Environ(akid, secret, token)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// AWSJSON runs the AWS CLI as root and decodes its JSON output.
func (h *Harness) AWSJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out := h.AWS(t, args...)
	m := map[string]any{}
	if strings.TrimSpace(out) == "" {
		return m
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("aws %s: output is not JSON: %v\n%s", strings.Join(args, " "), err, out)
	}
	return m
}

func python(t *testing.T) string {
	p := os.Getenv("HC_TEST_PYTHON")
	if p == "" {
		p = "python3"
	}
	if exec.Command(p, "-c", "import boto3").Run() != nil {
		t.Skip("python with boto3 not available (set HC_TEST_PYTHON)")
	}
	return p
}

// Python runs a Python script as root with boto3 imported and returns stdout;
// the test fails if the script raises.
func (h *Harness) Python(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "script.py")
	src := "import boto3, json, botocore\n" + script + "\n"
	if err := os.WriteFile(f, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	h.sync()
	cmd := exec.Command(python(t), f)
	cmd.Env = h.Environ(h.AccessKeyID, h.SecretKey, "")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("python: %v\n%s\n%s", err, out.String(), errb.String())
	}
	return out.String()
}

// User creates an IAM user with the given managed policies and returns its keys.
func (h *Harness) User(t *testing.T, name string, policies ...string) (akid, secret string) {
	t.Helper()
	out := h.nativeAsRoot(t, "POST", "/api/v1/iam/users", map[string]any{"name": name, "policies": policies, "create_access_key": true})
	var res struct {
		AccessKey struct {
			AccessKeyID     string `json:"access_key_id"`
			SecretAccessKey string `json:"secret_access_key"`
		} `json:"access_key"`
	}
	_ = json.Unmarshal(out, &res)
	if res.AccessKey.AccessKeyID == "" {
		out = h.nativeAsRoot(t, "POST", "/api/v1/iam/users/"+name+"/access-keys", nil)
		var k struct {
			AccessKeyID     string `json:"access_key_id"`
			SecretAccessKey string `json:"secret_access_key"`
		}
		_ = json.Unmarshal(out, &k)
		return k.AccessKeyID, k.SecretAccessKey
	}
	return res.AccessKey.AccessKeyID, res.AccessKey.SecretAccessKey
}

func (h *Harness) nativeAsRoot(t *testing.T, method, path string, body any) []byte {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	h.sync()
	req, _ := http.NewRequest(method, h.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+h.AccessKeyID+":"+h.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b.String())
	}
	return b.Bytes()
}

// NativeAs calls the native HomeCloud API with an access key and returns the
// status and body, without failing on error statuses.
func (h *Harness) NativeAs(t *testing.T, akid, secret, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	h.sync()
	req, _ := http.NewRequest(method, h.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+akid+":"+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.Bytes()
}

// Native calls the native HomeCloud API as root and returns the response body.
func (h *Harness) Native(t *testing.T, method, path string, body any) []byte {
	return h.nativeAsRoot(t, method, path, body)
}
