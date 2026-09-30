package ec2_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"golang.org/x/net/websocket"
)

// vmAccessChecks exercises what the guest agent and the serial console give a
// running VM instance: run-command, CloudWatch metrics from the guest and the
// browser terminal. Every wait has a deadline.
func (f *filterEnv) vmAccessChecks(t *testing.T, id string) {
	t.Helper()
	h := f.h
	type cmdResult struct {
		Status   string `json:"status"`
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	runCmd := func(command string, timeout int) (int, cmdResult, string) {
		code, body := h.NativeAs(t, h.AccessKeyID, h.SecretKey, "POST", "/api/v1/ec2/instances/"+id+"/commands",
			map[string]any{"command": command, "timeout_seconds": timeout})
		var r cmdResult
		_ = json.Unmarshal(body, &r)
		return code, r, string(body)
	}

	// run-command: stdout, stderr and the exit code come from the guest. The
	// agent may need a moment after the boot marker.
	var r cmdResult
	waitFor(t, "run-command through the guest agent", vmTestBudget, func() bool {
		code, res, body := runCmd(`echo out-$(hostname); echo err-text >&2; exit 3`, 60)
		if code != http.StatusOK {
			t.Logf("run-command: %d %s", code, body)
			return false
		}
		r = res
		return true
	})
	if r.Status != "Failed" || r.ExitCode != 3 || !strings.HasPrefix(r.Stdout, "out-ip-") || strings.TrimSpace(r.Stderr) != "err-text" {
		t.Errorf("run-command result: %+v", r)
	}
	if _, r, body := runCmd(`cat /etc/os-release | head -1; uname -m`, 60); r.Status != "Success" || r.ExitCode != 0 || !strings.Contains(r.Stdout, "NAME=") {
		t.Errorf("second run-command: %s", body)
	}
	start := time.Now()
	if _, r, body := runCmd(`sleep 120`, 3); r.Status != "TimedOut" || time.Since(start) > 60*time.Second {
		t.Errorf("a command past its timeout: %s (took %s)", body, time.Since(start))
	}
	// The same IAM action as container instances: a user without ssm:SendCommand is refused.
	akid, secret := h.User(t, "vm-nossm")
	if code, body := h.NativeAs(t, akid, secret, "POST", "/api/v1/ec2/instances/"+id+"/commands", map[string]any{"command": "id"}); code != http.StatusForbidden {
		t.Errorf("run-command without permission: %d %s", code, body)
	}
	t.Logf("run-command through the guest agent verified")

	// CloudWatch metrics describe the guest (a QEMU container has a handful of
	// processes; a booted Ubuntu has dozens, and its memory is the guest's).
	cw, err := cloudwatch.New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	cw.GuestUsage = f.ec2.GuestUsage
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { cw.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	dims := map[string]string{"InstanceId": id}
	last := func(name string) (float64, bool) {
		pts, _ := cw.Statistics("HC/EC2", name, dims, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), time.Minute)
		if len(pts) == 0 {
			return 0, false
		}
		return pts[len(pts)-1].Maximum, true
	}
	waitFor(t, "guest metrics in CloudWatch", 6*time.Minute, func() bool {
		_, a := last("CPUUtilization")
		_, b := last("MemoryUtilization")
		_, c := last("ProcessCount")
		return a && b && c
	})
	if v, _ := last("ProcessCount"); v < 20 {
		t.Errorf("ProcessCount %v: measures the QEMU container, not the guest", v)
	}
	if v, _ := last("MemoryUsed"); v < 50<<20 || v > 1<<30 {
		t.Errorf("MemoryUsed %v bytes: expected the t3.micro guest's (between 50 MiB and 1 GiB)", v)
	}
	if v, _ := last("MemoryUtilization"); v <= 0 || v >= 100 {
		t.Errorf("MemoryUtilization %v", v)
	}
	if v, ok := last("CPUUtilization"); !ok || v < 0 || v > 100 {
		t.Errorf("CPUUtilization %v", v)
	}
	for _, name := range []string{"NetworkIn", "NetworkOut", "DiskReadBytes", "DiskWriteBytes"} {
		if _, ok := last(name); !ok {
			t.Errorf("no %s datapoint", name)
		}
	}
	t.Logf("guest metrics published")

	// The browser terminal is the serial console: a login prompt appears.
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(h.URL, "http")+"/api/v1/ec2/instances/"+id+"/terminal", h.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header.Set("Authorization", "Bearer "+h.AccessKeyID+":"+h.SecretKey)
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("terminal: %v", err)
	}
	defer conn.Close()
	frames := make(chan string, 256)
	go func() {
		defer close(frames)
		for {
			var m string
			if websocket.Message.Receive(conn, &m) != nil {
				return
			}
			frames <- m
		}
	}()
	var got strings.Builder
	deadline := time.After(3 * time.Minute)
	nudge := time.NewTicker(3 * time.Second)
	defer nudge.Stop()
	for !strings.Contains(got.String(), " login:") {
		select {
		case m, ok := <-frames:
			if !ok {
				t.Fatalf("terminal closed before a login prompt; got %q", got.String())
			}
			got.WriteString(m)
		case <-nudge.C:
			_ = websocket.Message.Send(conn, `{"t":"i","d":"\r"}`)
		case <-deadline:
			t.Fatalf("no login prompt on the serial terminal; got %q", got.String())
		}
	}
	t.Logf("serial terminal shows the login prompt")
	// The log (GetConsoleOutput) still has the boot output with a client attached.
	if out := h.AWSJSON(t, "ec2", "get-console-output", "--instance-id", id)["Output"].(string); !strings.Contains(out, "Cloud-init v.") {
		t.Errorf("console output lost the boot log: %.300s", out)
	}
}
