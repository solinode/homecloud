package vm

import (
	"crypto/sha256"
	"fmt"
	"strconv"
)

// Machine is a guest's hardware.
type Machine struct {
	Name     string // instance ID
	Arch     Arch
	KVM      bool // hardware acceleration; otherwise TCG emulation
	VCPUs    int
	MemoryMB int64
	// Disks are the extra volumes, attached as virtio disks after the root disk
	// in this order (so /dev/vdb, /dev/vdc, ...). Each is a raw image file
	// /disks/<ID>/disk.img, and its serial makes /dev/disk/by-id/virtio-<Serial>.
	Disks []Disk
}

// Disk is an extra volume of a VM.
type Disk struct {
	ID string // the volume ID
}

// DisksDir is where the VM container mounts the extra volumes (one per volume ID).
const DisksDir = "/disks"

// Path of the disk's image file in the VM container.
func (d Disk) Path() string { return DisksDir + "/" + d.ID + "/disk.img" }

// Serial is the virtio serial of the disk: the volume ID cut to the 20
// characters virtio-blk allows.
func (d Disk) Serial() string {
	if len(d.ID) > 20 {
		return d.ID[:20]
	}
	return d.ID
}

// MAC is a stable, locally administered address derived from the instance ID.
func (m Machine) MAC() string {
	sum := sha256.Sum256([]byte(m.Name))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", sum[0], sum[1], sum[2])
}

// Args is the QEMU command line (without the binary). The runner's entrypoint
// prepares /vm/disk.qcow2, /vm/seed.iso, /vm/efivars.fd and the passt socket
// before it starts QEMU.
//
// The guest's serial console goes to QEMU's stdout, i.e. the container's log,
// which is what GetConsoleOutput returns. QMP listens on /run/qmp.sock for the
// power controls (hc-vm-ctl).
func (m Machine) Args() []string {
	vcpus := max(1, m.VCPUs)
	args := []string{
		"-name", m.Name,
		"-nodefaults", "-no-user-config",
	}
	switch m.Arch {
	case ArchAArch64:
		args = append(args, "-machine", "virt,gic-version=max")
	default:
		args = append(args, "-machine", "q35")
	}
	if m.KVM {
		args = append(args, "-accel", "kvm", "-cpu", "host")
	} else if m.Arch == ArchAArch64 {
		// pauth-impdef makes pointer authentication cheap to emulate.
		args = append(args, "-accel", "tcg,thread=multi", "-cpu", "max,pauth-impdef=on")
	} else {
		args = append(args, "-accel", "tcg,thread=multi", "-cpu", "max")
	}
	args = append(args, "-smp", strconv.Itoa(vcpus), "-m", strconv.FormatInt(m.MemoryMB, 10))
	if m.Arch == ArchAArch64 {
		args = append(args,
			"-drive", "if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/AAVMF/AAVMF_CODE.no-secboot.fd",
			"-drive", "if=pflash,format=raw,unit=1,file="+DiskDir+"/efivars.fd",
		)
	}
	args = append(args,
		// The NIC is wired to passt inside the container (see hc-vm-run). It is
		// declared first, at a fixed PCI slot, so that its name in the guest
		// (enp0s3) does not change when disks are added or removed: the guest's
		// network configuration refers to it.
		"-netdev", "stream,id=net0,addr.type=unix,addr.path=/tmp/passt.sock,server=off",
		"-device", "virtio-net-pci,netdev=net0,mac="+m.MAC()+",romfile=,addr=0x3",
		// Root disk, extra volumes, then the cloud-init seed (label cidata, found by NoCloud).
		"-drive", "file="+DiskDir+"/disk.qcow2,if=none,id=root,format=qcow2",
		"-device", "virtio-blk-pci,drive=root,bootindex=1",
	)
	for i, d := range m.Disks {
		args = append(args,
			"-drive", fmt.Sprintf("file=%s,if=none,id=data%d,format=raw", d.Path(), i),
			"-device", fmt.Sprintf("virtio-blk-pci,drive=data%d,serial=%s", i, d.Serial()),
		)
	}
	args = append(args,
		"-drive", "file="+DiskDir+"/seed.iso,if=none,id=seed,format=raw,readonly=on",
		"-device", "virtio-blk-pci,drive=seed",
		"-device", "virtio-rng-pci",
		"-display", "none", "-monitor", "none",
		"-chardev", "stdio,id=ser0,signal=off", "-serial", "chardev:ser0",
		"-qmp", "unix:/run/qmp.sock,server=on,wait=off",
	)
	return args
}

// Command is the container command: the QEMU binary and its arguments.
func (m Machine) Command() []string {
	return append([]string{m.Arch.QEMU()}, m.Args()...)
}
