//go:build !windows

package ec2

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2/vm"
)

// A run directory is this user's alone: made 0700, tightened if it was not,
// and refused when it is not a directory of this user (a symlink planted in a
// shared temporary directory, say).
func TestVMHostPrivateDir(t *testing.T) {
	base := t.TempDir()
	d := filepath.Join(base, "new")
	if err := privateDir(d); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(d); st.Mode().Perm() != 0o700 {
		t.Errorf("new run directory has mode %v", st.Mode().Perm())
	}
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(loose, 0o777)
	if err := privateDir(loose); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(loose); st.Mode().Perm() != 0o700 {
		t.Errorf("existing run directory left with mode %v", st.Mode().Perm())
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(d, link); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(link); err == nil {
		t.Error("a symlink was taken for a run directory")
	}
}

// A data directory whose path has a comma (which would end a QEMU option)
// keeps the guests' sockets elsewhere.
func TestVMHostBaseComma(t *testing.T) {
	s := &Service{env: &svc.Env{Cfg: core.Config{DataDir: "/tmp/a,b"}}}
	if b := s.vmHostBase(); strings.Contains(b, ",") {
		t.Errorf("run directory base %q", b)
	}
	s.env.Cfg.DataDir = "/tmp/hc"
	if b := s.vmHostBase(); b != "/tmp/hc/vm" {
		t.Errorf("run directory base %q", b)
	}
}

// killStaleQEMU only kills a QEMU of the run directory the pid file is in:
// the number may have gone to another process since, another guest's QEMU included.
func TestVMHostKillStaleQEMUChecksProcess(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	start := func(d string) *exec.Cmd {
		// (Two commands, so the shell stays with this command line.)
		cmd := exec.Command("/bin/sh", "-c", "sleep 30; : qemu-system-aarch64 path="+d+"/net.sock")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return cmd
	}
	pidFile := filepath.Join(dir, "qemu.pid")
	foreign := start(other)
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(foreign.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	killStaleQEMU(pidFile)
	if err := syscall.Kill(foreign.Process.Pid, 0); err != nil {
		t.Errorf("killStaleQEMU killed another guest's QEMU: %v", err)
	}
	own := start(dir)
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(own.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- own.Wait() }() // (reaped, as launchd reaps an orphaned QEMU)
	killStaleQEMU(pidFile)
	select {
	case err := <-waited:
		if err == nil {
			t.Error("this guest's QEMU exited by itself")
		}
	case <-time.After(10 * time.Second):
		t.Error("killStaleQEMU left this guest's QEMU running")
	}
}

// The serial console log on the host does not grow without bound.
func TestVMHostConsoleLogBounded(t *testing.T) {
	h := &vmHost{dir: t.TempDir()}
	p := h.path(vm.HostConsoleLog)
	if err := os.WriteFile(p, make([]byte, vmHostConsoleMax+1), 0o600); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	close(exited)
	h.tail(exited)
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 0 {
		t.Errorf("console log is %d bytes after copying", st.Size())
	}
}
