package s3

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

func TestPresignedURLsGoThroughTheAPI(t *testing.T) {
	var gotPath, gotHost, gotQuery string
	minio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHost, gotQuery = r.URL.EscapedPath(), r.Host, r.URL.RawQuery
		_, _ = io.WriteString(w, "object body")
	}))
	defer minio.Close()
	host := strings.TrimPrefix(minio.URL, "http://")

	env := svctest.Env(t)
	env.Cfg.APIAddr, env.Cfg.PublicURL = "0.0.0.0:8080", "https://cloud.example.com"
	s := &Service{env: env, user: "minioroot", direct: host}

	raw := "http://" + host + "/photos/a%20b.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=minioroot%2F20260101%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=abc"
	pub := s.publicPresigned(raw)
	if !strings.HasPrefix(pub, "https://cloud.example.com/_s3/photos/a%20b.txt?X-Amz-Algorithm=") {
		t.Fatalf("public URL: %s", pub)
	}

	// A client calls the API; MinIO sees its own host and the original path and signature.
	req := httptest.NewRequest("GET", strings.TrimPrefix(pub, "https://cloud.example.com"), nil)
	rec := httptest.NewRecorder()
	s.ServePresigned(rec, req)
	if rec.Code != 200 || rec.Body.String() != "object body" {
		t.Fatalf("proxied: %d %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/photos/a%20b.txt" || gotHost != host || !strings.Contains(gotQuery, "X-Amz-Signature=abc") {
		t.Fatalf("MinIO saw path=%s host=%s query=%s", gotPath, gotHost, gotQuery)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("object bytes must be sandboxed")
	}

	// Only presigned requests made with MinIO's own key are forwarded.
	for name, target := range map[string]string{
		"unsigned":     "/_s3/photos/a.txt",
		"someone else": "/_s3/photos/a.txt?X-Amz-Signature=x&X-Amz-Credential=other%2F20260101%2Fus-east-1%2Fs3%2Faws4_request",
	} {
		rec := httptest.NewRecorder()
		s.ServePresigned(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	s.ServePresigned(rec, httptest.NewRequest("DELETE", strings.TrimPrefix(pub, "https://cloud.example.com"), nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("DELETE forwarded: %d", rec.Code)
	}
}
