package server

import (
	"os"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

func TestClientEndpoint(t *testing.T) {
	cases := []struct {
		name string
		cfg  core.Config
		want string
	}{
		{"loopback", core.Config{APIAddr: "127.0.0.1:8080"}, "http://127.0.0.1:8080"},
		{"wildcard v4", core.Config{APIAddr: "0.0.0.0:8080"}, "http://127.0.0.1:8080"},
		{"wildcard v6", core.Config{APIAddr: "[::]:8080"}, "http://127.0.0.1:8080"},
		{"bare port", core.Config{APIAddr: ":8080"}, "http://127.0.0.1:8080"},
		{"wildcard with public url", core.Config{APIAddr: "0.0.0.0:8080", PublicURL: "https://cloud.example.com/"}, "https://cloud.example.com"},
		{"specific address", core.Config{APIAddr: "192.168.1.5:8080", TLSCert: "c"}, "https://192.168.1.5:8080"},
	}
	for _, c := range cases {
		if got := ClientEndpoint(c.cfg); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFixWildcardEndpoint(t *testing.T) {
	dir := t.TempDir()
	cfg := core.Config{DataDir: dir, APIAddr: "0.0.0.0:8080"}
	if err := os.WriteFile(CredentialsPath(dir), []byte(`{"endpoint":"http://0.0.0.0:8080","access_key_id":"AK","region":"us-east-1","ca_file":"/x.pem"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixWildcardEndpoint(cfg)
	b, _ := os.ReadFile(CredentialsPath(dir))
	s := string(b)
	if !strings.Contains(s, `"http://127.0.0.1:8080"`) || !strings.Contains(s, "/x.pem") || !strings.Contains(s, "AK") {
		t.Fatalf("not repaired or fields lost: %s", s)
	}
	// A good endpoint is left alone.
	fixWildcardEndpoint(core.Config{DataDir: dir, APIAddr: "0.0.0.0:9999"})
	if b2, _ := os.ReadFile(CredentialsPath(dir)); string(b2) != s {
		t.Fatalf("rewrote a valid endpoint: %s", b2)
	}
}
