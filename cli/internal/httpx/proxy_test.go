package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func req(peer string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = peer
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func TestProxyTrustClient(t *testing.T) {
	tr, err := ParseTrustedProxies([]string{"10.0.0.0/8", "192.168.1.1, 2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		r          *http.Request
		ip         string
		wantSecure bool
	}{
		{"untrusted peer ignores headers", req("203.0.113.5:1234", map[string]string{"X-Forwarded-For": "1.2.3.4", "X-Forwarded-Proto": "https"}), "203.0.113.5", false},
		{"trusted peer, no headers", req("10.1.1.1:80", nil), "10.1.1.1", false},
		{"trusted peer, single client", req("10.1.1.1:80", map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Forwarded-Proto": "https"}), "198.51.100.7", true},
		{"rightmost untrusted wins over forged left entry", req("10.1.1.1:80", map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.7, 10.2.2.2"}), "198.51.100.7", false},
		{"forged entry behind a real client", req("10.1.1.1:80", map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.7"}), "198.51.100.7", false},
		{"all trusted falls back to leftmost", req("10.1.1.1:80", map[string]string{"X-Forwarded-For": "10.9.9.9, 192.168.1.1"}), "10.9.9.9", false},
		{"garbage keeps the peer", req("10.1.1.1:80", map[string]string{"X-Forwarded-For": "not-an-ip"}), "10.1.1.1", false},
		{"ipv6 client with port", req("192.168.1.1:80", map[string]string{"X-Forwarded-For": "[2606:4700::1]:5555"}), "2606:4700::1", false},
		{"proto http", req("10.1.1.1:80", map[string]string{"X-Forwarded-Proto": "http"}), "10.1.1.1", false},
	}
	for _, c := range cases {
		ip, secure := tr.Client(c.r)
		if ip != c.ip || secure != c.wantSecure {
			t.Errorf("%s: got %s secure=%v, want %s secure=%v", c.name, ip, secure, c.ip, c.wantSecure)
		}
	}
	if _, err := ParseTrustedProxies([]string{"bogus"}); err == nil {
		t.Fatal("expected an error for a bad entry")
	}
}

func TestProxyWrapFeedsRequestContext(t *testing.T) {
	tr, _ := ParseTrustedProxies([]string{"127.0.0.1"})
	var got map[string][]string
	var ip string
	h := tr.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, ip = RequestContext(r), ClientIP(r) }))
	h.ServeHTTP(httptest.NewRecorder(), req("127.0.0.1:5", map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Forwarded-Proto": "https"}))
	if ip != "198.51.100.7" || got["aws:sourceip"][0] != "198.51.100.7" || got["aws:securetransport"][0] != "true" {
		t.Fatalf("trusted: ip=%s ctx=%v", ip, got)
	}
	h.ServeHTTP(httptest.NewRecorder(), req("198.51.100.99:5", map[string]string{"X-Forwarded-For": "1.1.1.1", "X-Forwarded-Proto": "https"}))
	if ip != "198.51.100.99" || got["aws:securetransport"][0] != "false" {
		t.Fatalf("untrusted: ip=%s ctx=%v", ip, got)
	}
	// Nothing trusted: Wrap is a no-op.
	empty, _ := ParseTrustedProxies(nil)
	h = empty.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ip = ClientIP(r) }))
	h.ServeHTTP(httptest.NewRecorder(), req("127.0.0.1:5", map[string]string{"X-Forwarded-For": "1.1.1.1"}))
	if ip != "127.0.0.1" {
		t.Fatalf("default trusts nothing, got %s", ip)
	}
}
