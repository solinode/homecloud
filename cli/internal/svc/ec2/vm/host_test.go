package vm

import (
	"strings"
	"testing"
)

func TestQEMUHostHVF(t *testing.T) {
	m := Machine{Name: "i-0abc", Arch: ArchAArch64, VCPUs: 2, MemoryMB: 2048, Disks: []Disk{{ID: "vol-0123456789abcdef01234"}}}
	dir := "/Users/me/.homecloud/guests/i-0abc"
	s := joined(m.HostArgs(dir, "/opt/homebrew/share/qemu/edk2-aarch64-code.fd"))
	nbd := "server.type=unix,server.path=" + dir + "/nbd.sock"
	for _, want := range []string{
		"-machine virt,gic-version=max,pflash0=fw-code,pflash1=fw-vars",
		"-accel hvf -cpu host",
		"-smp 2 -m 2048",
		"driver=file,node-name=fw-code,read-only=on,filename=/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
		"driver=nbd,node-name=fw-vars," + nbd + ",export=fw-vars",
		"driver=nbd,node-name=root," + nbd + ",export=root",
		"-device virtio-blk-pci,drive=root,bootindex=1",
		"driver=nbd,node-name=data0," + nbd + ",export=vol-0123456789abcdef01234",
		"-device virtio-blk-pci,drive=data0,serial=vol-0123456789abcdef",
		"driver=nbd,node-name=seed," + nbd + ",export=seed,read-only=on",
		"-netdev stream,id=net0,server=off,addr.type=unix,addr.path=" + dir + "/net.sock",
		"virtio-net-pci,netdev=net0,mac=" + m.MAC() + ",romfile=,addr=0x3",
		"-chardev socket,id=ser0,path=" + dir + "/serial.sock,server=on,wait=off,logfile=" + dir + "/console.log,logappend=on -serial chardev:ser0",
		"-chardev socket,id=qga0,path=" + dir + "/qga.sock,server=on,wait=off",
		"-device virtserialport,bus=vser0.0,chardev=qga0,name=org.qemu.guest_agent.0",
		"-qmp unix:" + dir + "/qmp.sock,server=on,wait=off",
		"-nodefaults",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("command line lacks %q:\n%s", want, s)
		}
	}
	for _, bad := range []string{"-sandbox", "kvm", "tcg", "/vm/", "/run/", "/tmp/"} {
		if strings.Contains(s, bad) {
			t.Errorf("host command line has %q:\n%s", bad, s)
		}
	}
	// The read-only exports must stay read-only, the writable ones writable.
	if strings.Contains(s, "export=root,read-only") || strings.Contains(s, "export=fw-vars,read-only") {
		t.Errorf("a writable disk is read-only:\n%s", s)
	}
}

// The guest sees the same devices in the same order whichever way it runs:
// its network configuration names the card, and disks keep their names.
func TestQEMUHostDevicesMatchContainer(t *testing.T) {
	m := Machine{Name: "i-1", Arch: ArchAArch64, VCPUs: 1, MemoryMB: 1024, Disks: []Disk{{ID: "vol-a"}, {ID: "vol-b"}}}
	devices := func(args []string) []string {
		var out []string
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-device" {
				out = append(out, args[i+1])
			}
		}
		return out
	}
	if c, h := devices(m.Args()), devices(m.HostArgs("/d", "/fw")); joined(c) != joined(h) {
		t.Errorf("devices differ:\ncontainer %v\nhost      %v", c, h)
	}
}

func TestHostHelperCommands(t *testing.T) {
	if got := joined(RelayCommand(NBDSocket)); got != "socat -t 5 - UNIX-CONNECT:/run/nbd.sock" {
		t.Errorf("relay %q", got)
	}
	if got := joined(HostExitCommand(3)); got != "/bin/sh -c echo 3 >/run/hc-vm-host.exit" {
		t.Errorf("exit %q", got)
	}
}
