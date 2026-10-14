package server

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/client"
)

func TestSelfSignedTLS(t *testing.T) {
	dir := t.TempDir()
	cert, key, err := SelfSignedCert(dir, "homelab.local")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	})}
	go srv.ServeTLS(ln, cert, key)
	defer srv.Close()

	t.Setenv("HOMECLOUD_DATA_DIR", dir)
	creds, _ := json.Marshal(client.Profile{Endpoint: "https://" + ln.Addr().String(), AccessKeyID: "a", SecretAccessKey: "b", CAFile: cert})
	if err := os.WriteFile(filepath.Join(dir, "credentials"), creds, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := client.New()
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]bool
	if err := c.Do("GET", "/", nil, &out); err != nil || !out["ok"] {
		t.Fatalf("https call through the trusted self-signed cert failed: %v %v", out, err)
	}
	// Account-prefixed S3 Control hosts are covered too.
	pemBytes, _ := os.ReadFile(cert)
	blk, _ := pem.Decode(pemBytes)
	parsed, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"localhost", "123456789012.localhost", "homelab.local", "123456789012.homelab.local", "127.0.0.1"} {
		if err := parsed.VerifyHostname(h); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	// A second call must reuse the existing certificate rather than regenerate it.
	cert2, _, _ := SelfSignedCert(dir, "homelab.local")
	if cert2 != cert {
		t.Fatal("certificate path changed")
	}
}
