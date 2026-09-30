package vm

import (
	"crypto/sha256"
	"fmt"
	"strconv"
)

// Sockets inside the VM container: the guest's serial console (one client at a
// time), the qemu-guest-agent channel and its virtio-serial port name.
const (
	SerialSocket = "/run/serial.sock"
	QGASocket    = "/run/qga.sock"
	QGAChannel   = "org.qemu.guest_agent.0"
)

// Machine is a guest's hardware.
type Machine struct {
	Name     string // instance ID
	Arch     Arch
	KVM      bool // hardware acceleration; otherwise TCG emulation
	VCPUs    int
	MemoryMB int64
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
// The guest's serial console is served on SerialSocket (the browser terminal)
// and logged to QEMU's stdout, i.e. the container's log, which is what
// GetConsoleOutput returns. The guest agent's channel is QGASocket. QMP listens on /run/qmp.sock for the
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
		// Root disk and the cloud-init seed (label cidata, found by NoCloud).
		"-drive", "file="+DiskDir+"/disk.qcow2,if=none,id=root,format=qcow2",
		"-device", "virtio-blk-pci,drive=root,bootindex=1",
		"-drive", "file="+DiskDir+"/seed.iso,if=none,id=seed,format=raw,readonly=on",
		"-device", "virtio-blk-pci,drive=seed",
		// The NIC is wired to passt inside the container (see hc-vm-run).
		"-netdev", "stream,id=net0,addr.type=unix,addr.path=/tmp/passt.sock,server=off",
		"-device", "virtio-net-pci,netdev=net0,mac="+m.MAC()+",romfile=",
		"-device", "virtio-rng-pci",
		"-display", "none", "-monitor", "none",
		// The serial console is a socket the browser terminal attaches to;
		// logfile tees everything the guest prints to QEMU's stdout (the
		// container's log) whether or not anyone is attached.
		"-chardev", "socket,id=ser0,path="+SerialSocket+",server=on,wait=off,logfile=/dev/stdout,logappend=on",
		"-serial", "chardev:ser0",
		// qemu-guest-agent's channel (run-command, guest metrics).
		"-chardev", "socket,id=qga0,path="+QGASocket+",server=on,wait=off",
		"-device", "virtio-serial-pci,id=vser0",
		"-device", "virtserialport,bus=vser0.0,chardev=qga0,name="+QGAChannel,
		"-qmp", "unix:/run/qmp.sock,server=on,wait=off",
	)
	return args
}

// Command is the container command: the QEMU binary and its arguments.
func (m Machine) Command() []string {
	return append([]string{m.Arch.QEMU()}, m.Args()...)
}
