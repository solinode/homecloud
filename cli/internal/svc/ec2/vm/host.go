package vm

import (
	"fmt"
	"path/filepath"
	"strconv"
)

// Host-run guests (macOS).
//
// Docker Desktop and OrbStack run containers in a Linux VM without nested
// virtualization, so QEMU in the VM container could only emulate the CPU. On
// an Apple Silicon Mac HomeCloud can instead run QEMU on the host itself with
// Hypervisor.framework (-accel hvf). The VM container stays: it still holds
// the instance's private address, security groups, published ports, passt, the
// disks (Docker volumes) and the console log. In host mode its entrypoint
// prepares the disks and the seed as usual, starts passt and serves the disks
// with qemu-storage-daemon (NBD) on Unix sockets, then waits. A host runner
// (HomeCloud on the Mac) starts QEMU and joins the guest's network card and
// disks to those sockets through `docker exec` streams, so nothing listens on a
// network port and only a Docker API client (root-equivalent anyway) can reach
// them. The guest's serial console is copied into the container's log, and the
// container exits when QEMU does, so stop, start, reboot, console output and
// state reconciliation work as for a guest inside the container.

// Paths in a VM container in host mode.
const (
	// NBDSocket is where qemu-storage-daemon serves the guest's disks.
	NBDSocket = "/run/nbd.sock"
	// PasstSocket is passt's socket for the guest's network card.
	PasstSocket = "/tmp/passt.sock"
	// HostReadyFile appears once the disks and passt are ready for QEMU.
	HostReadyFile = "/run/hc-vm-host.ready"
	// HostExitFile is where the host runner writes QEMU's exit status, which
	// ends the container.
	HostExitFile = "/run/hc-vm-host.exit"
)

// NBD export names. Extra volumes are exported under their volume ID.
const (
	ExportRoot   = "root"
	ExportSeed   = "seed"
	ExportFWVars = "fw-vars"
)

// HostFirmware is the UEFI firmware of host-run aarch64 guests, relative to
// QEMU's installation prefix (Homebrew's qemu ships it). Debian's AAVMF build,
// which guests in the container boot, hangs before its boot manager under
// Hypervisor.framework; both keep their variables in the same format, so a
// guest's variable store (on its root volume) works with either.
const HostFirmware = "share/qemu/edk2-aarch64-code.fd"

// Files of a host-run guest in its run directory on the host.
const (
	HostNetSocket    = "net.sock"    // QEMU's network card (relayed to passt)
	HostNBDSocket    = "nbd.sock"    // QEMU's disks (relayed to qemu-storage-daemon)
	HostSerialSocket = "serial.sock" // the guest's serial console
	HostQGASocket    = "qga.sock"    // qemu-guest-agent
	HostQMPSocket    = "qmp.sock"    // QMP (power controls)
	HostConsoleLog   = "console.log" // everything the guest printed on the serial console
)

// HostArgs is the QEMU command line (without the binary) for a guest run on
// the host with Hypervisor.framework. dir is the guest's run directory on the
// host: QEMU connects to the network and disk relays there and serves the
// serial console, guest agent and QMP sockets there. firmware is the UEFI
// firmware file on the host (aarch64; see HostFirmware).
//
// The devices match Args, in the same order and at the same addresses, so the
// guest sees the same hardware whichever way it runs (its network
// configuration names the network card).
func (m Machine) HostArgs(dir, firmware string) []string {
	vcpus := max(1, m.VCPUs)
	p := func(name string) string { return filepath.Join(dir, name) }
	nbd := func(node, export string, readOnly bool) string {
		s := fmt.Sprintf("driver=nbd,node-name=%s,server.type=unix,server.path=%s,export=%s", node, p(HostNBDSocket), export)
		if readOnly {
			s += ",read-only=on"
		}
		return s
	}
	args := []string{
		"-name", m.Name,
		"-nodefaults", "-no-user-config",
	}
	machine := "q35"
	if m.Arch == ArchAArch64 {
		// The firmware is QEMU's own on the host; its variables live on the root volume.
		args = append(args,
			"-blockdev", "driver=file,node-name=fw-code,read-only=on,filename="+firmware,
			"-blockdev", nbd("fw-vars", ExportFWVars, false),
		)
		machine = "virt,gic-version=max,pflash0=fw-code,pflash1=fw-vars"
	}
	args = append(args,
		"-machine", machine,
		"-accel", "hvf", "-cpu", "host",
		"-smp", strconv.Itoa(vcpus), "-m", strconv.FormatInt(m.MemoryMB, 10),
		"-netdev", "stream,id=net0,server=off,addr.type=unix,addr.path="+p(HostNetSocket),
		"-device", "virtio-net-pci,netdev=net0,mac="+m.MAC()+",romfile=,addr=0x3",
		"-blockdev", nbd("root", ExportRoot, false),
		"-device", "virtio-blk-pci,drive=root,bootindex=1",
	)
	for i, d := range m.Disks {
		node := fmt.Sprintf("data%d", i)
		args = append(args,
			"-blockdev", nbd(node, d.ID, false),
			"-device", fmt.Sprintf("virtio-blk-pci,drive=%s,serial=%s", node, d.Serial()),
		)
	}
	args = append(args,
		"-blockdev", nbd("seed", ExportSeed, true),
		"-device", "virtio-blk-pci,drive=seed",
		"-device", "virtio-rng-pci",
		"-display", "none", "-monitor", "none",
		"-chardev", "socket,id=ser0,path="+p(HostSerialSocket)+",server=on,wait=off,logfile="+p(HostConsoleLog)+",logappend=on",
		"-serial", "chardev:ser0",
		"-chardev", "socket,id=qga0,path="+p(HostQGASocket)+",server=on,wait=off",
		"-device", "virtio-serial-pci,id=vser0",
		"-device", "virtserialport,bus=vser0.0,chardev=qga0,name="+QGAChannel,
		"-qmp", "unix:"+p(HostQMPSocket)+",server=on,wait=off",
	)
	return args
}

// RelayCommand is what a `docker exec` runs in the VM container to join its
// standard input and output to a Unix socket there (passt's or the NBD server's).
func RelayCommand(socket string) []string {
	return []string{"socat", "-t", "5", "-", "UNIX-CONNECT:" + socket}
}

// ConsoleCommand is what a `docker exec` runs in the VM container to copy its
// standard input (the guest's console) to the container's log.
func ConsoleCommand() []string {
	return []string{"/bin/sh", "-c", "exec cat >/proc/1/fd/1"}
}

// HostExitCommand records QEMU's exit status in the VM container, which then exits with it.
func HostExitCommand(status int) []string {
	return []string{"/bin/sh", "-c", "echo " + strconv.Itoa(status) + " >" + HostExitFile}
}
