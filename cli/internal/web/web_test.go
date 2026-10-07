package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleCannotBeFramed(t *testing.T) {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("console can be framed: %v", w.Header())
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("console sends Referer headers")
	}
}
