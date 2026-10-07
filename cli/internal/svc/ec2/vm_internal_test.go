package ec2

import (
	"errors"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// A failed rebuild during a resize leaves the record describing the container
// that is still there (the old type); a rebuild that replaced the container
// keeps the new type.
func TestChangeTypeRollsBackAFailedVMRebuild(t *testing.T) {
	env := svctest.Env(t)
	s := New(env, vpc.New(env))
	inst := Instance{ID: "i-vmresize", State: "stopped", ContainerID: "c-old", Virtualization: "kvm", VMBase: "ubuntu-24.04",
		InstanceType: "t3.micro", VCPUs: 2, MemoryMB: 1024}
	if err := store.Put(env.Store, cInstances, inst.ID, inst); err != nil {
		t.Fatal(err)
	}
	setBusy(inst.ID, true) // no Docker here: keep get from syncing with it
	defer setBusy(inst.ID, false)
	prev := recreateVM
	defer func() { recreateVM = prev }()

	recreateVM = func(*Service, string) error { return errors.New("rename: boom") }
	if _, err := s.ChangeType(inst.ID, "t3.large"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("ChangeType = %v, want the rebuild error", err)
	}
	got, _ := store.Get[Instance](env.Store, cInstances, inst.ID)
	if got.InstanceType != "t3.micro" || got.VCPUs != 2 || got.MemoryMB != 1024 {
		t.Errorf("after a failed rebuild: %s %v vCPU %d MiB, want the old t3.micro", got.InstanceType, got.VCPUs, got.MemoryMB)
	}

	recreateVM = func(s *Service, id string) error {
		_, err := store.Update(s.env.Store, cInstances, id, func(x *Instance) error { x.ContainerID = "c-new"; return nil })
		if err != nil {
			return err
		}
		return errors.New("start the rebuilt virtual machine: boom")
	}
	if _, err := s.ChangeType(inst.ID, "t3.large"); err == nil {
		t.Fatal("ChangeType succeeded")
	}
	got, _ = store.Get[Instance](env.Store, cInstances, inst.ID)
	if got.InstanceType != "t3.large" || got.ContainerID != "c-new" {
		t.Errorf("after a rebuild that replaced the container: %s in %s, want t3.large in c-new", got.InstanceType, got.ContainerID)
	}
}

func TestPasstFailure(t *testing.T) {
	logs := "2026-10-07T13:57:58.1Z [homecloud] creating disk (10 GiB)\n" +
		"2026-10-07T13:57:59.0Z [homecloud] passt did not start; falling back to user-mode networking:\n" +
		"2026-10-07T13:57:59.0Z 0.5852: Failed to sandbox process, exiting\n" +
		"2026-10-07T13:57:59.1Z hint: the Docker host restricts unprivileged user namespaces\n" +
		"2026-10-07T13:57:59.2Z [homecloud] starting the virtual machine\n" +
		"2026-10-07T13:58:10.0Z [    1.0] Linux version ...\n"
	want := "[homecloud] passt did not start; falling back to user-mode networking:; 0.5852: Failed to sandbox process, exiting; hint: the Docker host restricts unprivileged user namespaces"
	if got := passtFailure(logs); got != want {
		t.Errorf("passtFailure =\n%q\nwant\n%q", got, want)
	}
	if got := passtFailure("2026-10-07T13:57:59.2Z [homecloud] starting the virtual machine\n"); got != "" {
		t.Errorf("passt started, but passtFailure = %q", got)
	}
}

func TestHostFwd(t *testing.T) {
	if got := hostFwd(nil); got != "" {
		t.Errorf("no ports: %q", got)
	}
	got := hostFwd([]runtime.Port{{ContainerPort: 22, Protocol: "tcp"}, {ContainerPort: 53, Protocol: "udp"}})
	if want := ",hostfwd=tcp::22-:22,hostfwd=udp::53-:53"; got != want {
		t.Errorf("hostFwd = %q, want %q", got, want)
	}
}

func TestVMImagesAreMarked(t *testing.T) {
	seen := 0
	for _, im := range catalog {
		n := im.normalized()
		if !im.IsVM() {
			if n.Virtualization != "container" || im.Ref == "" {
				t.Errorf("%s: container image %+v", im.ID, n)
			}
			continue
		}
		seen++
		if n.Virtualization != "vm" || im.Ref != "" || im.KeepAlive {
			t.Errorf("%s: VM image %+v", im.ID, n)
		}
		if len(im.Name) < 5 || im.Name[len(im.Name)-4:] != "(VM)" {
			t.Errorf("%s: name %q does not end in (VM)", im.ID, im.Name)
		}
		if im.AWSName == "" || im.OwnerID == "" {
			t.Errorf("%s: no AWS name or owner", im.ID)
		}
	}
	if seen < 2 {
		t.Errorf("only %d VM images in the catalog", seen)
	}
}

func TestErrVMUnsupported(t *testing.T) {
	if err := errVMUnsupported("attaching volumes to"); err == nil || err.Error() != "UnsupportedOperation: attaching volumes to VM instances is not supported yet" {
		t.Errorf("%v", err)
	}
}
