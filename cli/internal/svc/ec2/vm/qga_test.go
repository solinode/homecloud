package vm

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestQGARequests(t *testing.T) {
	if got := string(QGASync(42)); got != `{"arguments":{"id":42},"execute":"guest-sync-delimited"}`+"\n" {
		t.Errorf("sync: %q", got)
	}
	if got := string(QGAExecStatusRequest(7)); got != `{"arguments":{"pid":7},"execute":"guest-exec-status"}`+"\n" {
		t.Errorf("status: %q", got)
	}
	var m map[string]any
	if err := json.Unmarshal(QGAExecRequest("/bin/sh", []string{"-c", "echo \"hi\""}), &m); err != nil {
		t.Fatal(err)
	}
	a := m["arguments"].(map[string]any)
	if m["execute"] != "guest-exec" || a["path"] != "/bin/sh" || a["capture-output"] != true || len(a["arg"].([]any)) != 2 {
		t.Errorf("exec: %v", m)
	}
}

func TestParseQGAReply(t *testing.T) {
	r, ok := ParseQGAReply([]byte("\xff{\"return\": 42}\n"))
	if !ok || !r.Synced || string(r.Return) != "42" {
		t.Errorf("sync reply: %+v %v", r, ok)
	}
	r, ok = ParseQGAReply([]byte(`{"return": {"pid": 9}}`))
	if !ok || r.Synced {
		t.Fatalf("reply: %+v %v", r, ok)
	}
	if pid, err := ParseExecPID(r.Return); err != nil || pid != 9 {
		t.Errorf("pid %d %v", pid, err)
	}
	r, ok = ParseQGAReply([]byte(`{"error": {"class": "CommandNotFound", "desc": "nope"}}`))
	if !ok || r.Err() == nil || !strings.Contains(r.Err().Error(), "nope") {
		t.Errorf("error reply: %+v %v", r, ok)
	}
	for _, junk := range []string{"", "garbage", `{"partial"`, `{"x":1}`, "\xff"} {
		if _, ok := ParseQGAReply([]byte(junk)); ok {
			t.Errorf("%q parsed as a reply", junk)
		}
	}
	if _, err := ParseExecPID(json.RawMessage(`{}`)); err == nil {
		t.Error("missing pid accepted")
	}
}

func TestParseExecStatus(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	st, err := ParseExecStatus(json.RawMessage(`{"exited":true,"exitcode":3,"out-data":"` + b64("out\n") + `","err-data":"` + b64("bad\n") + `"}`))
	if err != nil || !st.Exited || st.ExitCode != 3 || st.Stdout != "out\n" || st.Stderr != "bad\n" {
		t.Errorf("%+v %v", st, err)
	}
	st, _ = ParseExecStatus(json.RawMessage(`{"exited":false}`))
	if st.Exited {
		t.Errorf("running reported as exited: %+v", st)
	}
	st, _ = ParseExecStatus(json.RawMessage(`{"exited":true,"signal":9,"out-truncated":true}`))
	if st.ExitCode != 137 || !st.Truncated {
		t.Errorf("signal: %+v", st)
	}
	if _, err := ParseExecStatus(json.RawMessage(`{"exited":true,"out-data":"***"}`)); err == nil {
		t.Error("bad base64 accepted")
	}
}

func TestShellCommand(t *testing.T) {
	p, args := ShellCommand(`echo "a b"; exit 3`, 30)
	if p != "/bin/sh" || len(args) != 5 || args[3] != "30" || args[4] != `echo "a b"; exit 3` || !strings.Contains(args[1], "timeout -k 5") {
		t.Errorf("%s %q", p, args)
	}
}

const sampleProc = `cpu  1000 0 500 8000 100 0 0 0 0 0
cpu0 1000 0 500 8000 100 0 0 0 0 0
intr 1
==cpu2
cpu  1100 0 550 8050 100 0 0 0 0 0
cpu0 1100 0 550 8050 100 0 0 0 0 0
==meminfo
MemTotal:        2048000 kB
MemFree:          100000 kB
MemAvailable:    1024000 kB
==netdev
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    5000      10    0    0    0     0          0         0     5000      10    0    0    0     0       0          0
  ens3:    1000       5    0    0    0     0          0         0     2000       6    0    0    0     0       0          0
==diskstats
 252       0 vda 100 0 2000 50 30 0 400 20 0 0 0 0 0 0 0 0 0
 252       1 vda1 90 0 1800 40 25 0 350 10 0 0 0 0 0 0 0 0 0
   7       0 loop0 1 0 10 0 0 0 0 0 0 0 0 0 0 0 0 0 0
 259       0 nvme0n1 5 0 100 0 5 0 100 0 0 0 0 0 0 0 0 0 0
 259       1 nvme0n1p1 5 0 100 0 5 0 100 0 0 0 0 0 0 0 0 0 0
==procs
87
`

func TestParseGuestStats(t *testing.T) {
	g, err := ParseGuestStats(sampleProc)
	if err != nil {
		t.Fatal(err)
	}
	// 200 jiffies passed, 50 of them idle: 150/200 busy... total delta = 100+0+50+50+0 = 200, idle delta 50.
	if g.CPUPercent != 75 {
		t.Errorf("cpu %v, want 75", g.CPUPercent)
	}
	if g.MemoryTotal != 2048000*1024 || g.MemoryUsed != 1024000*1024 || g.MemoryPercent() != 50 {
		t.Errorf("memory %+v", g)
	}
	if g.NetRx != 1000 || g.NetTx != 2000 {
		t.Errorf("network rx %d tx %d (lo must not count)", g.NetRx, g.NetTx)
	}
	// vda (2000 sectors read, 400 written) and nvme0n1 (100, 100); partitions and loop devices excluded.
	if g.DiskRead != (2000+100)*512 || g.DiskWrite != (400+100)*512 {
		t.Errorf("disk read %d write %d", g.DiskRead, g.DiskWrite)
	}
	if g.Processes != 87 {
		t.Errorf("processes %d", g.Processes)
	}
}

func TestParseGuestStatsErrors(t *testing.T) {
	if _, err := ParseGuestStats("nothing useful\n"); err == nil {
		t.Error("output without /proc/stat accepted")
	}
	cpu := "cpu  1 0 1 1 0 0 0 0 0 0\n==cpu2\ncpu  2 0 2 2 0 0 0 0 0 0\n"
	if _, err := ParseGuestStats(cpu); err == nil || !strings.Contains(err.Error(), "MemTotal") {
		t.Errorf("missing meminfo: %v", err)
	}
	// An idle interval (no jiffies passed) reports 0, not NaN.
	g, err := ParseGuestStats("cpu  1 0 1 1 0\n==cpu2\ncpu  1 0 1 1 0\n==meminfo\nMemTotal: 10 kB\n")
	if err != nil || g.CPUPercent != 0 || g.MemoryUsed != 10*1024 {
		t.Errorf("%+v %v", g, err)
	}
}

func TestWholeDisk(t *testing.T) {
	for name, want := range map[string]bool{"vda": true, "vdb": true, "sda": true, "xvda": true, "nvme0n1": true,
		"vda1": false, "sda2": false, "nvme0n1p2": false, "loop0": false, "dm-0": false, "sr0": false, "vd": false} {
		if got := wholeDisk(name); got != want {
			t.Errorf("wholeDisk(%q) = %v", name, got)
		}
	}
}

func TestAttachCommands(t *testing.T) {
	if s := strings.Join(SerialAttachCommand(), " "); s != "socat -,raw,echo=0 UNIX-CONNECT:/run/serial.sock" {
		t.Error(s)
	}
	if s := strings.Join(QGAAttachCommand(), " "); s != "socat -t 1 - UNIX-CONNECT:/run/qga.sock" {
		t.Error(s)
	}
}

func TestSeedInstallsGuestAgent(t *testing.T) {
	f := Seed{InstanceID: "i", Hostname: "h", UserData: "#cloud-config\npackages: [htop]\n"}.Files()
	v := string(f["vendor-data"])
	if !strings.HasPrefix(v, "#!/bin/sh") || !strings.Contains(v, "qemu-guest-agent") || !strings.Contains(v, "systemctl enable --now qemu-guest-agent") {
		t.Errorf("vendor-data:\n%s", v)
	}
	if !strings.Contains(string(f["user-data"]), "htop") {
		t.Error("user data lost")
	}
}
