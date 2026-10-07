package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContainerEndpoint(t *testing.T) {
	public := Profile{Endpoint: "http://cloud.example.com:18080"}
	t.Setenv("HOMECLOUD_DATA_DIR", t.TempDir())
	// Outside the image nothing changes.
	t.Setenv("HOMECLOUD_IN_CONTAINER", "")
	if got := containerEndpoint(public); got != public.Endpoint {
		t.Fatalf("outside a container: %s", got)
	}
	t.Setenv("HOMECLOUD_IN_CONTAINER", "1")
	t.Setenv("HOMECLOUD_ADDR", "0.0.0.0:8080")
	if got := containerEndpoint(public); got != "http://127.0.0.1:8080" {
		t.Fatalf("in the image: %s", got)
	}
	// serve --addr is saved in config.json and wins over the image default.
	dir := t.TempDir()
	t.Setenv("HOMECLOUD_DATA_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"api_addr":"0.0.0.0:18080"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := containerEndpoint(public); got != "http://127.0.0.1:18080" {
		t.Fatalf("with --addr: %s", got)
	}
	t.Setenv("HOMECLOUD_DATA_DIR", t.TempDir())
	// A real certificate does not cover 127.0.0.1; the self-signed one does.
	if got := containerEndpoint(Profile{Endpoint: "https://cloud.example.com"}); got != "https://cloud.example.com" {
		t.Fatalf("https with a public certificate: %s", got)
	}
	if got := containerEndpoint(Profile{Endpoint: "https://localhost:8080", CAFile: "/data/tls/cert.pem"}); got != "https://127.0.0.1:8080" {
		t.Fatalf("https self-signed: %s", got)
	}
	// An explicit endpoint wins.
	t.Setenv("HOMECLOUD_ENDPOINT", "http://other:8080")
	if got := containerEndpoint(Profile{Endpoint: "http://other:8080"}); got != "http://other:8080" {
		t.Fatalf("HOMECLOUD_ENDPOINT: %s", got)
	}
}
