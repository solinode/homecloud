package ec2

import (
	"bufio"
	"errors"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2/vm"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// The guest controls the agent's channel: a line that never ends must not be
// buffered without bound in the server.
func TestQGAReplyIsBounded(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader(`{"return":1}`+"\n"+strings.Repeat("A", 5000)+"\n"), 64)
	l, err := readLine(br, 1000)
	if err != nil || string(l) != `{"return":1}`+"\n" {
		t.Fatalf("first line %q, %v", l, err)
	}
	if l, err := readLine(br, 1000); !errors.Is(err, errQGAReplyTooLarge) || l != nil {
		t.Fatalf("an oversized line: %d bytes, %v; want errQGAReplyTooLarge", len(l), err)
	}
	// A line of exactly the limit (with its newline) still fits, longer than the buffer.
	br = bufio.NewReaderSize(strings.NewReader(strings.Repeat("B", 999)+"\n"), 16)
	if l, err := readLine(br, 1000); err != nil || len(l) != 1000 {
		t.Fatalf("a line at the limit: %d bytes, %v", len(l), err)
	}
	if qgaMaxReply > 64<<20 {
		t.Errorf("qgaMaxReply = %d", qgaMaxReply)
	}
}

// The helper that flattens disks someone else may have written has no network.
func TestVMFlattenHasNoNetwork(t *testing.T) {
	spec := vmFlattenSpec("runner", "hc-vol-a", "hc-vm-images", "hc-snap-b")
	if spec.Network != "none" {
		t.Errorf("flatten network %q, want none", spec.Network)
	}
	for _, m := range spec.Mounts {
		if (m.Target == "/src" || m.Target == vm.ImagesDir) && !m.ReadOnly {
			t.Errorf("%s is writable in the flatten helper", m.Target)
		}
	}
	if len(spec.Devices) > 0 || len(spec.CapAdd) > 0 || len(spec.SecurityOpt) > 0 {
		t.Errorf("flatten helper is not a plain container: %+v", spec)
	}
}

// The VM container: no extra capabilities, the image cache and AMI read-only,
// the console log bounded, and Docker's seccomp profile widened only by passt's four calls.
func TestVMContainerConfinement(t *testing.T) {
	archOnce.Do(func() { arch = "x86_64" }) // no Docker here
	env := svctest.Env(t)
	s := New(env, vpc.New(env))
	inst := Instance{ID: "i-vmsec", State: "running", Virtualization: "kvm", VMBase: "ubuntu-24.04", VMAMI: "hc-ami-x",
		VCPUs: 1, MemoryMB: 1024, PrivateIP: "10.0.0.5", PrivateDNS: "ip-10-0-0-5.internal", VMDiskGB: 8,
		Volumes: []VolumeAttachment{{VolumeID: "vol-root", MountPath: vm.DiskDir, Device: "/dev/xvda"}, {VolumeID: "vol-data", MountPath: "/mnt/sdf", Device: "/dev/sdf"}}}
	spec := s.vmRunSpec(inst, "net")
	if len(spec.CapAdd) > 0 {
		t.Errorf("VM container gets capabilities %v", spec.CapAdd)
	}
	if len(spec.Devices) != 1 || spec.Devices[0] != "/dev/kvm" {
		t.Errorf("devices %v, want only /dev/kvm", spec.Devices)
	}
	if spec.LogMaxBytes <= 0 || spec.LogMaxBytes > 64<<20 {
		t.Errorf("VM container log is not bounded: %d", spec.LogMaxBytes)
	}
	targets := map[string]bool{}
	for _, m := range spec.Mounts {
		targets[m.Target] = true
		if (m.Target == vm.ImagesDir || m.Target == "/ami") != m.ReadOnly {
			t.Errorf("mount %s read-only=%v", m.Target, m.ReadOnly)
		}
	}
	for _, want := range []string{vm.ImagesDir, vm.DiskDir, "/ami", vm.DisksDir + "/vol-data"} {
		if !targets[want] {
			t.Errorf("no mount at %s: %+v", want, spec.Mounts)
		}
	}
	var seccomp string
	for _, o := range spec.SecurityOpt {
		if v, ok := strings.CutPrefix(o, "seccomp="); ok {
			seccomp = v
		}
	}
	if seccomp == "" || seccomp == "unconfined" {
		t.Fatalf("VM container seccomp %q", seccomp)
	}
	if strings.Contains(spec.Cmd[0], "qemu") && !strings.Contains(strings.Join(spec.Cmd, " "), "-sandbox "+vm.QEMUSandbox) {
		t.Errorf("QEMU runs without its own sandbox: %v", spec.Cmd)
	}
}

// Extra volumes of a VM launch are placed by device; the caller cannot put one
// where the root disk goes or reuse the root device.
func TestVMExtraVolumes(t *testing.T) {
	got, err := vmExtraVolumes([]VolumeSpec{{MountPath: vm.DiskDir, SizeGB: 1}, {Device: "/dev/sdh", MountPath: "/images"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Device != "/dev/sdf" || got[0].MountPath != "/mnt/sdf" || got[1].Device != "/dev/sdh" || got[1].MountPath != "/mnt/sdh" {
		t.Errorf("volumes %+v", got)
	}
	for _, bad := range [][]VolumeSpec{
		{{Device: "/dev/xvda"}},
		{{Device: "/dev/sda1"}},
		{{Device: "/vm"}},
		{{Device: "/dev/sdf/../../vm"}},
		{{Device: "/dev/sdf"}, {Device: "/dev/sdf"}},
	} {
		if _, err := vmExtraVolumes(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
