package server

import (
	"io"
	"net"
	"net/http"
	"testing"
)

func TestWorkloadListenAddr(t *testing.T) {
	if a, ok := workloadListenAddr("linux", "172.17.0.1", false); !ok || a != "172.17.0.1:0" {
		t.Fatalf("linux: %q %v", a, ok)
	}
	// Without a bridge gateway there is no address that is safe to use on Linux;
	// never fall back to a wildcard.
	if a, ok := workloadListenAddr("linux", "", false); ok {
		t.Fatalf("linux without gateway: %q", a)
	}
	for _, goos := range []string{"darwin", "windows"} {
		if a, ok := workloadListenAddr(goos, "", false); !ok || a != "127.0.0.1:0" {
			t.Fatalf("%s: %q %v", goos, a, ok)
		}
	}
	// In a container only the API port is published: listen on every address of
	// the container, where the workloads' networks reach it.
	if a, ok := workloadListenAddr("linux", "172.17.0.1", true); !ok || a != ":0" {
		t.Fatalf("containerized: %q %v", a, ok)
	}
}

func TestServeWorkloadsIsPlainHTTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := serveWorkloads(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }), t.Logf)
	defer stop()
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); string(b) != "ok" {
		t.Fatalf("body %q", b)
	}
}
