package route53

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestCheckPortFree(t *testing.T) {
	// A free port passes.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	// Taken (TCP): a clear error that names the port and the way out.
	err = checkPortFree("127.0.0.1", port)
	if err == nil || !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), strconv.Itoa(port)) || !strings.Contains(err.Error(), "--dns-port") {
		t.Fatalf("busy tcp port: %v", err)
	}
	l.Close()
	if err := checkPortFree("127.0.0.1", port); err != nil {
		t.Fatalf("free port: %v", err)
	}
	// Taken (UDP only).
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	uport := pc.LocalAddr().(*net.UDPAddr).Port
	if err := checkPortFree("127.0.0.1", uport); err == nil {
		t.Fatal("busy udp port accepted")
	}
}

func TestPortBusyHintFor53(t *testing.T) {
	if h := portBusyHint(53); !strings.Contains(h, "systemd-resolved") || !strings.Contains(h, "--dns-bind") {
		t.Fatalf("port 53 conflict must explain how to free it: %s", h)
	}
	if h := portBusyHint(8053); strings.Contains(h, "systemd") {
		t.Fatalf("unexpected hint: %s", h)
	}
}

// Port 53 is valid to configure; only an actual conflict is an error, and a
// non-root process that cannot bind it (permission denied) is not a conflict.
func TestCheckPort53IsNotRejectedOutright(t *testing.T) {
	if err := checkPortFree("127.0.0.1", 53); err != nil && !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("unexpected error: %v", err)
	}
}
