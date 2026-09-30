package ec2

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2/vm"
)

// Access to a running VM guest: run-command and the CloudWatch metrics go
// through qemu-guest-agent (QGA) on the virtio-serial channel QEMU serves as a
// socket in the VM container; the browser terminal attaches to the serial
// console socket (see terminal.go).
//
// QEMU serves one QGA client at a time, so calls to one guest are serialized.
// Each call is its own `docker exec socat` session that performs the
// guest-sync handshake first (the channel may hold a previous client's leftovers).

// qgaSlots holds one semaphore per VM container.
var qgaSlots sync.Map

func qgaSlot(cid string) chan struct{} {
	v, _ := qgaSlots.LoadOrStore(cid, make(chan struct{}, 1))
	return v.(chan struct{})
}

var errGuestAgent = errors.New("the guest agent is not responding (it is installed by cloud-init on first boot; images without it, or a guest that is still booting, cannot run commands)")

// qgaCall sends one command to the guest agent and returns its reply. ctx
// bounds the whole call, including waiting for the agent and for other callers.
func (s *Service) qgaCall(ctx context.Context, cid string, req []byte) (vm.QGAReply, error) {
	slot := qgaSlot(cid)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		return vm.QGAReply{}, errGuestAgent
	}
	ectx, cancel := context.WithCancel(ctx)
	defer cancel()
	ex, err := s.env.Docker.C.CreateExec(docker.CreateExecOptions{
		Container: cid, Cmd: vm.QGAAttachCommand(), AttachStdin: true, AttachStdout: true, AttachStderr: true, Context: ectx,
	})
	if err != nil {
		return vm.QGAReply{}, err
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer outW.Close()
		_ = s.env.Docker.C.StartExec(ex.ID, docker.StartExecOptions{InputStream: inR, OutputStream: outW, ErrorStream: io.Discard, Context: ectx})
	}()
	defer func() {
		inW.Close()
		cancel()
		outR.Close()
		inR.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()
	lines := make(chan []byte, 16)
	go func() {
		defer close(lines)
		br := bufio.NewReader(outR)
		for {
			l, err := br.ReadBytes('\n')
			if len(l) > 0 {
				select {
				case lines <- l:
				case <-ectx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	write := func(b []byte) { go inW.Write(b) } // a stalled connection must not block the loop

	syncID := time.Now().UnixNano() & 0x7fffffff
	want := strconv.FormatInt(syncID, 10)
	synced := false
	write(vm.QGASync(syncID))
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return vm.QGAReply{}, errGuestAgent
		case <-tick.C:
			if !synced {
				write(vm.QGASync(syncID))
			}
		case l, ok := <-lines:
			if !ok {
				return vm.QGAReply{}, errors.New("the connection to the guest agent closed")
			}
			r, ok := vm.ParseQGAReply(l)
			if !ok {
				continue
			}
			if !synced {
				if r.Synced && string(r.Return) == want {
					synced = true
					write(req)
				}
				continue
			}
			if r.Synced {
				continue
			}
			return r, r.Err()
		}
	}
}

// guestExec runs a shell command in the guest through the agent and waits for
// it, up to timeout (enforced in the guest with timeout(1) and here).
func (s *Service) guestExec(ctx context.Context, inst Instance, command string, timeout time.Duration) (vm.ExecStatus, error) {
	path, args := vm.ShellCommand(command, int(timeout.Seconds()))
	call := func(req []byte) (vm.QGAReply, error) {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return s.qgaCall(cctx, inst.ContainerID, req)
	}
	r, err := call(vm.QGAExecRequest(path, args))
	if err != nil {
		return vm.ExecStatus{}, err
	}
	pid, err := vm.ParseExecPID(r.Return)
	if err != nil {
		return vm.ExecStatus{}, err
	}
	// The guest's timeout(1) ends the command at timeout (plus its 5s kill grace).
	deadline := time.Now().Add(timeout + 15*time.Second)
	for {
		r, err := call(vm.QGAExecStatusRequest(pid))
		if err != nil {
			return vm.ExecStatus{}, err
		}
		st, err := vm.ParseExecStatus(r.Return)
		if err != nil {
			return st, err
		}
		if st.Exited {
			return st, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return st, core.Errf(http.StatusGatewayTimeout, "RequestTimeout", "the command did not finish in %s", timeout)
		}
		select {
		case <-ctx.Done():
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// vmRunCommand is run-command for VM instances.
func (s *Service) vmRunCommand(ctx context.Context, i Instance, command string, timeoutSeconds int) (any, error) {
	timeout := time.Duration(timeoutSeconds) * time.Second
	start := time.Now()
	st, err := s.guestExec(ctx, i, command, timeout)
	if err != nil {
		var ce *core.Error
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, core.Errf(http.StatusConflict, "InvalidInstanceState", "%v", err)
	}
	status := "Success"
	switch {
	case st.ExitCode == 124 && time.Since(start) >= timeout:
		status = "TimedOut"
	case st.ExitCode != 0:
		status = "Failed"
	}
	return map[string]any{"command_id": core.NewID("cmd"), "instance_id": i.ID, "status": status, "exit_code": st.ExitCode,
		"stdout": st.Stdout, "stderr": st.Stderr, "duration_ms": time.Since(start).Milliseconds()}, nil
}

// GuestUsage samples a running VM instance's guest through the agent, for the
// CloudWatch collector. Unlike Docker's stats of the VM container (which
// measure QEMU), this is what the guest itself sees. An error means there is
// nothing to publish now (not running, agent not up yet).
func (s *Service) GuestUsage(ctx context.Context, instanceID string) (*runtime.Usage, error) {
	i, err := s.get(instanceID)
	if err != nil {
		return nil, err
	}
	if !i.IsVM() || i.State != "running" || i.ContainerID == "" {
		return nil, fmt.Errorf("%s is not a running VM", instanceID)
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	st, err := s.guestExec(ctx, i, vm.StatsCommand, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if st.ExitCode != 0 {
		return nil, fmt.Errorf("guest stats exited %d: %s", st.ExitCode, st.Stderr)
	}
	g, err := vm.ParseGuestStats(st.Stdout)
	if err != nil {
		return nil, err
	}
	return &runtime.Usage{
		CPUPercent: g.CPUPercent, MemoryBytes: g.MemoryUsed, MemoryLimit: g.MemoryTotal, MemoryPercent: g.MemoryPercent(),
		NetRxBytes: g.NetRx, NetTxBytes: g.NetTx, BlockRead: g.DiskRead, BlockWrite: g.DiskWrite, Pids: g.Processes,
	}, nil
}
