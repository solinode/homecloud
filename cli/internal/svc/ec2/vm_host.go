package ec2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2/vm"
)

// Host-run VM guests: QEMU with Hypervisor.framework on a Mac (see host.go in
// the vm package for the design). This file chooses the backend, runs the
// host side of a guest (the "host runner") and gives the rest of EC2 the
// guest's console, agent and QMP sockets.

// labelVMHost marks a VM container whose guest runs on the host.
const labelVMHost = "homecloud.vm-host"

// virtHVF is Instance.Virtualization for a guest run on the host with Hypervisor.framework.
const virtHVF = "hvf"

// VMHostCommand, when set, returns the command that runs the host runner of a
// guest in its own process (`homecloud vm-host DIR`), so that guests outlive a
// restart of the server. Without it (tests) the runner is a goroutine.
var VMHostCommand func(dir string) *exec.Cmd

// vmAccel is how VM instances launched now run: "hvf" (QEMU on this Mac with
// Hypervisor.framework), "kvm" (QEMU in the VM container with /dev/kvm) or
// "emulated" (QEMU in the VM container, TCG). HC_VM_ACCEL (or the vm_accel
// setting) =hvf|kvm|tcg forces one; =container skips the host backend but
// still probes for KVM.
func (s *Service) vmAccel() string {
	s.vm.accelOnce.Do(func() {
		switch s.vmAccelSetting() {
		case "hvf":
			s.vm.accel = virtHVF
			if why := vmHostUnsupported(hostArch(s)); why != "" {
				log.Printf("ec2: VM acceleration hvf was chosen, but %s: VM launches will fail", why)
			}
			return
		case "tcg", "kvm", "container":
		default:
			// (HC_VM_NET=user is user-mode networking in the container, which a guest on the host cannot have.)
			if goruntime.GOOS == "darwin" && os.Getenv("HC_VM_NET") != "user" {
				if why := s.vmHostProbe(); why != "" {
					log.Printf("ec2: VM instances cannot run on this Mac with Hypervisor.framework (%s); they run in the Docker VM instead", why)
				} else {
					log.Printf("ec2: VM instances run on this Mac with Hypervisor.framework (%s)", s.vm.qemu)
					s.vm.accel = virtHVF
					return
				}
			}
		}
		s.vm.accel = "emulated"
		if s.vmKVM() {
			s.vm.accel = "kvm"
		}
	})
	return s.vm.accel
}

// vmAccelSetting is the configured VM backend: HC_VM_ACCEL, else the
// server's vm_accel setting ("" or "auto" chooses).
func (s *Service) vmAccelSetting() string {
	if v := os.Getenv("HC_VM_ACCEL"); v != "" {
		return v
	}
	return s.env.Cfg.VMAccel
}

// vmQEMUBinary finds qemu-system-<arch> for host-run guests: HC_VM_QEMU, the
// PATH, then Homebrew's prefixes (a server started by launchd has a short PATH).
func vmQEMUBinary(arch vm.Arch) string {
	if p := os.Getenv("HC_VM_QEMU"); p != "" {
		return p
	}
	if p, err := exec.LookPath(arch.QEMU()); err == nil {
		return p
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		p := filepath.Join(dir, arch.QEMU())
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// vmHostFirmware finds the UEFI firmware for host-run aarch64 guests:
// HC_VM_FIRMWARE, or the one installed with QEMU (Homebrew's qemu has it in
// <prefix>/share/qemu).
func vmHostFirmware(qemu string) string {
	if p := os.Getenv("HC_VM_FIRMWARE"); p != "" {
		return p
	}
	if qemu == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(qemu); err == nil {
		qemu = real
	}
	p := filepath.Join(filepath.Dir(filepath.Dir(qemu)), vm.HostFirmware)
	if st, err := os.Stat(p); err != nil || st.IsDir() || strings.Contains(p, ",") {
		return "" // (a comma would end QEMU's option)
	}
	return p
}

// vmHostUnsupported says why guests cannot run on this host with
// Hypervisor.framework, or "" when nothing rules it out before trying.
func vmHostUnsupported(dockerArch string) string {
	switch {
	case goruntime.GOOS != "darwin":
		return "it needs macOS"
	case goruntime.GOARCH != "arm64":
		return "it needs an Apple Silicon Mac"
	case runtime.InContainer():
		return "HomeCloud runs in a container"
	case dockerArch != "arm64":
		return "the Docker host is not arm64"
	case vmQEMUBinary(vm.ArchAArch64) == "":
		return "qemu-system-aarch64 was not found (brew install qemu)"
	case vmHostFirmware(vmQEMUBinary(vm.ArchAArch64)) == "":
		return "QEMU's UEFI firmware (" + vm.HostFirmware + ") was not found next to qemu-system-aarch64; set HC_VM_FIRMWARE"
	}
	return ""
}

// vmHostProbe checks that QEMU starts with Hypervisor.framework here: it
// boots an empty machine, paused, and quits it through QMP.
func (s *Service) vmHostProbe() string {
	if why := vmHostUnsupported(hostArch(s)); why != "" {
		return why
	}
	qemu := vmQEMUBinary(vm.ArchAArch64)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, qemu, "-machine", "virt", "-accel", "hvf", "-cpu", "host", "-m", "64",
		"-nodefaults", "-no-user-config", "-display", "none", "-S", "-qmp", "stdio")
	cmd.Stdin = strings.NewReader(`{"execute":"qmp_capabilities"}` + "\n" + `{"execute":"quit"}` + "\n")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil || !strings.Contains(out.String(), `"QMP"`) {
		return fmt.Sprintf("%s -accel hvf failed: %v %s", qemu, err, strings.TrimSpace(errb.String()))
	}
	s.vm.qemu = qemu
	return ""
}

// vmHostBase is the directory holding the run directories of host-run guests.
// Unix socket paths are short (104 bytes on macOS): a data directory with a
// long path uses the temporary directory instead, as does one with a comma,
// which would end a QEMU option holding a socket path.
func (s *Service) vmHostBase() string {
	base := filepath.Join(s.env.Cfg.DataDir, "vm")
	if s.env.Cfg.DataDir == "" || len(base) > 64 || strings.Contains(base, ",") {
		base = filepath.Join(os.TempDir(), fmt.Sprintf("homecloud-vm-%d", os.Getuid()))
	}
	return base
}

// vmHostDir is the run directory of a host-run guest.
func (s *Service) vmHostDir(id string) string { return filepath.Join(s.vmHostBase(), id) }

// vmHostConfig is what the host runner of a guest reads from its run directory.
type vmHostConfig struct {
	Instance  string   `json:"instance"`
	Container string   `json:"container"`
	QEMU      string   `json:"qemu"`
	Args      []string `json:"args"`
}

const vmHostConfigFile = "config.json"

// vmHostWatch starts, once, the loop that gives every running host-mode VM
// container its host runner, whoever started the container (launch, start,
// reboot, rebuild, or a container left running across a server restart).
func (s *Service) vmHostWatch() {
	if s.env.Docker == nil {
		return
	}
	s.vm.watchOnce.Do(func() {
		go func() {
			defer core.Recover("ec2 vm host watch")
			for {
				s.vmHostScan()
				time.Sleep(time.Second)
			}
		}()
	})
}

// vmHostScan starts a host runner for each running host-mode VM container of
// this installation that has none.
func (s *Service) vmHostScan() {
	cs, err := s.env.Docker.C.ListContainers(docker.ListContainersOptions{Filters: map[string][]string{
		"label":  {labelVMHost + "=1", core.LabelAccount + "=" + runtime.Account},
		"status": {"running"},
	}})
	if err != nil {
		return
	}
	for _, c := range cs {
		inst, err := store.Get[Instance](s.env.Store, cInstances, c.Labels[core.LabelResource])
		// Only the instance's own container, and only while it runs on the host:
		// a container left over from a rebuild must not get the guest's disks.
		if err != nil || inst.State == "terminated" || !vmHostMode(inst) || c.ID != inst.ContainerID {
			continue
		}
		if err := s.vmHostEnsure(inst, c.ID); err != nil {
			log.Printf("ec2: %s: starting the host side of the guest: %v", inst.ID, err)
		}
	}
}

// vmHostEnsure starts the host runner of a guest unless one is running.
func (s *Service) vmHostEnsure(inst Instance, cid string) error {
	dir := s.vmHostDir(inst.ID)
	s.vm.spawnMu.Lock()
	defer s.vm.spawnMu.Unlock()
	if t, ok := s.vm.spawned[cid]; ok && time.Since(t) < 5*time.Second {
		return nil // just started: it may not hold its lock yet
	}
	if err := privateDir(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := privateDir(dir); err != nil {
		return err
	}
	if vmHostRunning(dir) {
		return nil
	}
	qemu := s.vm.qemu
	if qemu == "" {
		qemu = vmQEMUBinary(s.vmMachine(inst).Arch)
	}
	cfg := vmHostConfig{Instance: inst.ID, Container: cid, QEMU: qemu, Args: s.vmMachine(inst).HostArgs(dir, vmHostFirmware(qemu))}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, vmHostConfigFile), b, 0o600); err != nil {
		return err
	}
	if s.vm.spawned == nil {
		s.vm.spawned = map[string]time.Time{}
	}
	for k, t := range s.vm.spawned {
		if time.Since(t) > time.Minute {
			delete(s.vm.spawned, k)
		}
	}
	s.vm.spawned[cid] = time.Now()
	if VMHostCommand == nil {
		go func() {
			defer core.Recover("ec2 vm host " + inst.ID)
			if err := RunVMHost(context.Background(), s.env.Docker, dir); err != nil && !errors.Is(err, errVMHostBusy) {
				log.Printf("ec2: %s: host runner: %v", inst.ID, err)
			}
		}()
		return nil
	}
	cmd := VMHostCommand(dir)
	lf, err := os.OpenFile(filepath.Join(dir, "runner.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	cmd.Stdout, cmd.Stderr = lf, lf
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

var errVMHostBusy = errors.New("another host runner serves this guest")

// RunVMHost is the host runner of one guest (dir is its run directory, with
// config.json): it waits for the VM container to have the disks and passt
// ready, starts QEMU, relays its network card and disks into the container,
// copies the serial console into the container's log, and ends the container
// with QEMU's exit status. QEMU is killed when the container stops.
func RunVMHost(ctx context.Context, d *runtime.Docker, dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, vmHostConfigFile))
	if err != nil {
		return err
	}
	var cfg vmHostConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	unlock, ok, err := lockDir(dir)
	if err != nil {
		return err
	}
	if !ok {
		return errVMHostBusy
	}
	defer unlock()
	h := &vmHost{d: d, cfg: cfg, dir: dir}
	return h.run(ctx)
}

type vmHost struct {
	d   *runtime.Docker
	cfg vmHostConfig
	dir string

	consoleMu sync.Mutex
	console   io.WriteCloser
}

func (h *vmHost) path(name string) string { return filepath.Join(h.dir, name) }

func (h *vmHost) logf(format string, args ...any) {
	log.Printf("ec2: %s: host runner: %s", h.cfg.Instance, fmt.Sprintf(format, args...))
}

// write sends bytes to the container's log.
func (h *vmHost) Write(p []byte) (int, error) {
	h.consoleMu.Lock()
	defer h.consoleMu.Unlock()
	if h.console == nil {
		return len(p), nil
	}
	if _, err := h.console.Write(p); err != nil {
		h.console = nil
	}
	return len(p), nil
}

func (h *vmHost) running() bool { return h.d.State(h.cfg.Container) == "running" }

// containerGone reports whether the VM container has stopped or is gone. An
// error asking Docker (it restarts, say) is not taken as the container gone:
// that would kill a running guest.
func (h *vmHost) containerGone() bool {
	c, err := h.d.C.InspectContainer(h.cfg.Container)
	if err != nil {
		var nsc *docker.NoSuchContainer
		return errors.As(err, &nsc)
	}
	return !c.State.Running
}

func (h *vmHost) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	killStaleQEMU(h.path("qemu.pid"))

	// The container's entrypoint prepares the disks (which can mean copying an
	// image) and starts passt and the NBD server.
	deadline := time.Now().Add(30 * time.Minute)
	for {
		if !h.running() {
			return fmt.Errorf("the VM container %.12s is not running", h.cfg.Container)
		}
		ectx, ecancel := context.WithTimeout(ctx, 20*time.Second)
		res, err := h.d.Exec(ectx, h.cfg.Container, []string{"test", "-f", vm.HostReadyFile}, nil)
		ecancel()
		if err == nil && res.ExitCode == 0 {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("the VM container did not get ready")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// The container's log gets the guest's console (and QEMU's own messages).
	pr, pw := io.Pipe()
	h.console = pw
	consoleDone := make(chan struct{})
	go func() {
		defer close(consoleDone)
		h.execStream(ctx, vm.ConsoleCommand(), pr, io.Discard)
		_ = pr.Close()
	}()

	for _, f := range []string{vm.HostNetSocket, vm.HostNBDSocket, vm.HostSerialSocket, vm.HostQGASocket, vm.HostQMPSocket, vm.HostConsoleLog} {
		_ = os.Remove(h.path(f))
	}
	var wg sync.WaitGroup
	var listeners []net.Listener
	closeListeners := func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}
	defer closeListeners()
	for _, r := range []struct{ name, target string }{{vm.HostNetSocket, vm.PasstSocket}, {vm.HostNBDSocket, vm.NBDSocket}} {
		ln, err := net.Listen("unix", h.path(r.name))
		if err != nil {
			fmt.Fprintf(h, "[homecloud] could not start QEMU on the host: %v\n", err)
			h.finish(ctx, 1, consoleDone)
			return err
		}
		listeners = append(listeners, ln)
		wg.Add(1)
		go func(ln net.Listener, target string) {
			defer wg.Done()
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					h.execStream(ctx, vm.RelayCommand(target), c, c)
				}()
			}
		}(ln, r.target)
	}

	cmd := exec.Command(h.cfg.QEMU, h.cfg.Args...)
	cmd.Dir = h.dir
	cmd.Stdout, cmd.Stderr = h, h
	start := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(h, "[homecloud] could not start QEMU on the host: %v\n", err)
		h.finish(ctx, 1, consoleDone)
		return err
	}
	_ = os.WriteFile(h.path("qemu.pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0o600)
	h.logf("QEMU started (pid %d)", cmd.Process.Pid)
	exited := make(chan struct{})
	go func() { // QEMU goes when the container goes (stop, kill, terminate)
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-exited:
				return
			case <-t.C:
				if h.containerGone() {
					h.logf("the VM container stopped: killing QEMU")
					_ = cmd.Process.Kill()
					return
				}
			}
		}
	}()
	tailDone := make(chan struct{})
	go func() { defer close(tailDone); h.tail(exited) }()
	err := cmd.Wait()
	close(exited)
	<-tailDone
	_ = os.Remove(h.path("qemu.pid"))
	status := 0
	if err != nil {
		status = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() > 0 {
			status = ee.ExitCode()
		}
		fmt.Fprintf(h, "[homecloud] QEMU exited: %v\n", err)
	}
	h.logf("QEMU exited after %s (%v)", time.Since(start).Round(time.Second), err)
	h.finish(ctx, status, consoleDone)
	cancel()
	closeListeners()
	wg.Wait()
	return nil
}

// finish records QEMU's exit status in the container (which then exits) and
// closes the console stream.
func (h *vmHost) finish(ctx context.Context, status int, consoleDone <-chan struct{}) {
	if h.running() {
		ectx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if _, err := h.d.Exec(ectx, h.cfg.Container, vm.HostExitCommand(status), nil); err != nil {
			h.logf("record the exit status in the container: %v", err)
		}
		cancel()
		// Hold the run directory until the container is down: a container still
		// running without a runner would get a new one (and a new QEMU).
		for n := 0; n < 100 && h.running(); n++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	h.consoleMu.Lock()
	if h.console != nil {
		_ = h.console.Close()
		h.console = nil
	}
	h.consoleMu.Unlock()
	select {
	case <-consoleDone:
	case <-time.After(10 * time.Second):
	}
}

// vmHostConsoleMax bounds the serial console log on the host: what has been
// copied to the container's log is cut away past it, or a guest printing
// without end would fill the host's disk.
const vmHostConsoleMax = 4 << 20

// tail copies what QEMU logs of the serial console to the container's log
// until QEMU has exited, then copies the rest.
func (h *vmHost) tail(exited <-chan struct{}) {
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	buf := make([]byte, 64<<10)
	for {
		done := false
		select {
		case <-exited:
			done = true
		default:
		}
		if f == nil {
			f, _ = os.Open(h.path(vm.HostConsoleLog))
		}
		if f != nil {
			for {
				n, err := f.Read(buf)
				if n > 0 {
					_, _ = h.Write(buf[:n])
				}
				if err != nil || n == 0 {
					break
				}
			}
			// QEMU appends (logappend=on), so its next write lands at the start.
			if off, err := f.Seek(0, io.SeekCurrent); err == nil && off > vmHostConsoleMax {
				if os.Truncate(h.path(vm.HostConsoleLog), 0) == nil {
					_, _ = f.Seek(0, io.SeekStart)
				}
			}
		}
		if done {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// execStream runs cmd in the VM container with its standard input and output
// joined to in and out, until either side ends.
func (h *vmHost) execStream(ctx context.Context, cmd []string, in io.Reader, out io.Writer) {
	ex, err := h.d.C.CreateExec(docker.CreateExecOptions{
		Container: h.cfg.Container, Cmd: cmd, AttachStdin: true, AttachStdout: true, AttachStderr: true, Context: ctx,
	})
	if err != nil {
		if h.running() {
			h.logf("docker exec %v: %v", cmd, err)
		}
		return
	}
	var stderr bytes.Buffer
	err = h.d.C.StartExec(ex.ID, docker.StartExecOptions{InputStream: in, OutputStream: out, ErrorStream: &stderr, Context: ctx})
	if errors.Is(err, net.ErrClosed) {
		err = nil // QEMU closed its end (it exited or let go of a disk)
	}
	if (err != nil || stderr.Len() > 0) && h.running() && ctx.Err() == nil {
		h.logf("docker exec %v: %v %s", cmd, err, strings.TrimSpace(stderr.String()))
	}
}

// vmHostCleanup removes a terminated instance's run directory once its host
// runner (which kills QEMU when the container goes) has exited.
func (s *Service) vmHostCleanup(id string) {
	defer core.Recover("ec2 vm host cleanup " + id)
	dir := s.vmHostDir(id)
	if _, err := os.Stat(dir); err != nil {
		return
	}
	for n := 0; n < 60 && vmHostRunning(dir); n++ {
		time.Sleep(time.Second)
	}
	if !vmHostRunning(dir) {
		killStaleQEMU(filepath.Join(dir, "qemu.pid")) // a runner that died left its QEMU
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("ec2: %s: remove the guest's run directory: %v", id, err)
	}
}

// ---- the guest's sockets, for the rest of EC2 ----

// vmHostDial connects to one of a host-run guest's sockets (vm.HostQGASocket, ...).
func (s *Service) vmHostDial(ctx context.Context, inst Instance, name string) (net.Conn, error) {
	var dl net.Dialer
	return dl.DialContext(ctx, "unix", filepath.Join(s.vmHostDir(inst.ID), name))
}

// vmQMP sends one QMP command to a host-run guest's QEMU.
func (s *Service) vmQMP(ctx context.Context, inst Instance, command string) error {
	c, err := s.vmHostDial(ctx, inst, vm.HostQMPSocket)
	if err != nil {
		return err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	br := bufio.NewReader(c)
	if _, err := br.ReadBytes('\n'); err != nil { // the greeting
		return err
	}
	for _, cmd := range []string{"qmp_capabilities", command} {
		if _, err := fmt.Fprintf(c, `{"execute":%q}`+"\n", cmd); err != nil {
			return err
		}
		for { // skip events up to the reply
			line, err := br.ReadBytes('\n')
			if err != nil {
				return err
			}
			var r struct {
				Return json.RawMessage `json:"return"`
				Error  *struct {
					Desc string `json:"desc"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &r) != nil {
				continue
			}
			if r.Error != nil {
				return errors.New(r.Error.Desc)
			}
			if r.Return != nil {
				break
			}
		}
	}
	return nil
}

// vmHostMode reports whether an instance's guest runs on the host.
func vmHostMode(i Instance) bool { return i.Virtualization == virtHVF }
