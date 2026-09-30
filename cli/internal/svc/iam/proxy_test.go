package iam_test

import (
	"net/http"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// callWith sends a native API request carrying extra headers.
func callWith(t *testing.T, h *awstest.Harness, akid, secret string, hdr map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", h.URL+"/api/v1/iam/users", nil)
	req.Header.Set("Authorization", "Bearer "+akid+":"+secret)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Behind a trusted proxy the client address and scheme come from the
// forwarding headers; otherwise the headers are ignored.
func TestTrustedProxyFeedsPolicyConditions(t *testing.T) {
	h := awstest.New(t)
	ak, sk := userKeys(t, h, "office", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*",
		"Condition":{"IpAddress":{"aws:SourceIp":"203.0.113.0/24"},"Bool":{"aws:SecureTransport":"true"}}}]}`)
	forwarded := map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"}

	// Default: nothing is trusted, so the headers change nothing.
	if st := callWith(t, h, ak, sk, forwarded); st != http.StatusForbidden {
		t.Fatalf("untrusted headers were honoured: %d", st)
	}
	trust, err := httpx.ParseTrustedProxies([]string{"127.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	h.Middleware = trust.Wrap
	if st := callWith(t, h, ak, sk, forwarded); st != http.StatusOK {
		t.Fatalf("trusted proxy headers not applied: %d", st)
	}
	// Forged left-hand entries don't help: the rightmost untrusted address counts.
	if st := callWith(t, h, ak, sk, map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.1", "X-Forwarded-Proto": "https"}); st != http.StatusForbidden {
		t.Fatalf("forged X-Forwarded-For accepted: %d", st)
	}
	// Plain HTTP at the proxy fails the SecureTransport condition.
	if st := callWith(t, h, ak, sk, map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "http"}); st != http.StatusForbidden {
		t.Fatalf("http via proxy treated as secure: %d", st)
	}
}
