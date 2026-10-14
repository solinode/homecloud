//go:build !windows

package ec2

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// lockDir takes the host runner's lock on a guest's run directory. ok is
// false when another runner holds it.
func lockDir(dir string) (unlock func(), ok bool, err error) {
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true, nil
}

// vmHostRunning reports whether a host runner holds a guest's run directory.
func vmHostRunning(dir string) bool {
	unlock, ok, err := lockDir(dir)
	if err != nil {
		return false
	}
	if ok {
		unlock()
		return false
	}
	return true
}

// detach makes a host runner its own session, so it outlives the server and
// a Ctrl-C in the server's terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// privateDir makes sure dir is a directory of this user that nobody else can
// enter (creating it if need be): a run directory holds the command line a
// host runner executes and the guest's sockets, and its parent may be in a
// shared temporary directory where another user could have made it first.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || !ok || int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not a directory of this user", dir)
	}
	if st.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// killStaleQEMU kills the QEMU a host runner that died left behind (its
// network and disks went with the runner). The process must still be a QEMU
// of this run directory: its number may since have gone to another process,
// another guest's QEMU included.
func killStaleQEMU(pidFile string) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	defer os.Remove(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return
	}
	out, err := exec.Command("ps", "-ww", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || !strings.Contains(string(out), "qemu-system") || !strings.Contains(string(out), filepath.Dir(pidFile)+string(filepath.Separator)) {
		return // gone, or the number was reused
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	for n := 0; n < 50 && syscall.Kill(pid, 0) == nil; n++ {
		time.Sleep(100 * time.Millisecond)
	}
}
