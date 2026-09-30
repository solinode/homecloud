package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The console opens an object inline with the viewer's session token in the URL
// (?access_token=), so a script inside the object could read the token from its
// own location. Inline views must therefore run no scripts.
func TestInlineObjectViewsRunNoScripts(t *testing.T) {
	h := sandboxUserContent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/s3/b/object?key=x.html&inline=true&access_token=hcs_secret", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "sandbox;") || strings.Contains(csp, "allow-scripts") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("CSP %q lets an object run scripts or load resources", csp)
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("the token-bearing URL would be sent as a Referer")
	}
	// Hosted websites keep their scripts (they are sandboxed to an opaque origin and carry no token).
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/website/b/index.html", nil))
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-scripts") {
		t.Fatal("website sandbox lost allow-scripts")
	}
}
