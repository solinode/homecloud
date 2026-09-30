package vm

import (
	"strings"
	"testing"
)

func TestQEMUExtraDisks(t *testing.T) {
	m := Machine{Name: "i-1", Arch: ArchX8664, VCPUs: 1, MemoryMB: 1024,
		Disks: []Disk{{ID: "vol-0123456789abcdef0"}, {ID: "vol-fedcba9876543210f"}}}
	s := strings.Join(m.Args(), " ")
	for _, want := range []string{
		"-drive file=/disks/vol-0123456789abcdef0/disk.img,if=none,id=data0,format=raw",
		"-device virtio-blk-pci,drive=data0,serial=vol-0123456789abcdef",
		"-drive file=/disks/vol-fedcba9876543210f/disk.img,if=none,id=data1,format=raw",
		"-device virtio-blk-pci,drive=data1,serial=vol-fedcba9876543210",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("command line lacks %q:\n%s", want, s)
		}
	}
	// The NIC first, at a fixed slot; the root disk, then the extra disks in order, the seed last.
	order := []string{"virtio-net-pci,netdev=net0", "id=root", "id=data0", "id=data1", "id=seed"}
	last := -1
	for _, o := range order {
		i := strings.Index(s, o)
		if i < 0 || i < last {
			t.Fatalf("%q out of order in:\n%s", o, s)
		}
		last = i
	}
	if !strings.Contains(s, "addr=0x3") {
		t.Errorf("the NIC has no fixed PCI slot:\n%s", s)
	}
	// Adding and removing disks leaves the NIC's part of the command line alone.
	bare := Machine{Name: "i-1", Arch: ArchX8664, VCPUs: 1, MemoryMB: 1024}
	nic := func(m Machine) string {
		a := strings.Join(m.Args(), " ")
		i := strings.Index(a, "-netdev")
		return a[i : i+strings.Index(a[i:], "-drive")]
	}
	if nic(m) != nic(bare) {
		t.Errorf("the NIC changed with the disks:\n%s\n%s", nic(m), nic(bare))
	}
}

func TestDiskSerial(t *testing.T) {
	if got := (Disk{ID: "vol-0123456789abcdef0"}).Serial(); got != "vol-0123456789abcdef" || len(got) != 20 {
		t.Errorf("serial %q", got)
	}
	if got := (Disk{ID: "vol-1"}).Serial(); got != "vol-1" {
		t.Errorf("serial %q", got)
	}
	if got := (Disk{ID: "vol-x"}).Path(); got != "/disks/vol-x/disk.img" {
		t.Errorf("path %q", got)
	}
}
