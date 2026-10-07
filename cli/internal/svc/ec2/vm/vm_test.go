package vm

import (
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

func joined(args []string) string { return strings.Join(args, " ") }

func TestQEMUAArch64Emulated(t *testing.T) {
	m := Machine{Name: "i-0abc", Arch: ArchAArch64, VCPUs: 2, MemoryMB: 4096}
	c := m.Command()
	if c[0] != "qemu-system-aarch64" {
		t.Fatalf("binary %q", c[0])
	}
	s := joined(c)
	for _, want := range []string{
		"-machine virt,gic-version=max",
		"-accel tcg,thread=multi -cpu max,pauth-impdef=on",
		"-smp 2 -m 4096",
		"if=pflash,format=raw,unit=0,readonly=on,file=/usr/share/AAVMF/AAVMF_CODE.no-secboot.fd",
		"if=pflash,format=raw,unit=1,file=/vm/efivars.fd",
		"file=/vm/disk.qcow2,if=none,id=root,format=qcow2",
		"virtio-blk-pci,drive=root,bootindex=1",
		"file=/vm/seed.iso,if=none,id=seed,format=raw,readonly=on",
		"-netdev stream,id=net0,addr.type=unix,addr.path=/tmp/passt.sock,server=off",
		"virtio-net-pci,netdev=net0,mac=" + m.MAC(),
		"-chardev socket,id=ser0,path=/run/serial.sock,server=on,wait=off,logfile=/dev/stdout,logappend=on -serial chardev:ser0",
		"-chardev socket,id=qga0,path=/run/qga.sock,server=on,wait=off",
		"-device virtio-serial-pci,id=vser0",
		"-device virtserialport,bus=vser0.0,chardev=qga0,name=org.qemu.guest_agent.0",
		"-qmp unix:/run/qmp.sock,server=on,wait=off",
		"-nodefaults",
		"-sandbox on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("command line lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "kvm") {
		t.Errorf("emulated machine mentions kvm: %s", s)
	}
}

func TestQEMUX86KVM(t *testing.T) {
	m := Machine{Name: "i-1", Arch: ArchX8664, KVM: true, VCPUs: 4, MemoryMB: 16384}
	c := m.Command()
	if c[0] != "qemu-system-x86_64" {
		t.Fatalf("binary %q", c[0])
	}
	s := joined(c)
	for _, want := range []string{"-machine q35", "-accel kvm -cpu host", "-smp 4 -m 16384"} {
		if !strings.Contains(s, want) {
			t.Errorf("command line lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "pflash") || strings.Contains(s, "tcg") {
		t.Errorf("x86 KVM machine has firmware drives or TCG: %s", s)
	}
}

func TestQEMUDefaultsAndMAC(t *testing.T) {
	// Fractional instance types round up to a vCPU in the caller; zero must not produce -smp 0.
	if s := joined(Machine{Name: "a", Arch: ArchX8664, MemoryMB: 512}.Args()); !strings.Contains(s, "-smp 1 ") {
		t.Errorf("no vCPU count: %s", s)
	}
	a, b := Machine{Name: "i-a"}.MAC(), Machine{Name: "i-b"}.MAC()
	if a == b || a != (Machine{Name: "i-a"}).MAC() {
		t.Errorf("MAC not stable per instance: %s %s", a, b)
	}
	if !strings.HasPrefix(a, "52:54:00:") || len(a) != 17 {
		t.Errorf("MAC %q", a)
	}
}

func TestSeedFiles(t *testing.T) {
	f := Seed{InstanceID: "i-0abc", Hostname: "ip-10-0-1-5", PublicKey: "ssh-ed25519 AAAA test\n", UserData: "#!/bin/sh\necho hi\n"}.Files()
	meta := string(f["meta-data"])
	for _, want := range []string{
		`instance-id: "i-0abc"`, `local-hostname: "ip-10-0-1-5"`, `hostname: "ip-10-0-1-5"`,
		"public-keys:\n  - \"ssh-ed25519 AAAA test\"\n",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("meta-data lacks %q:\n%s", want, meta)
		}
	}
	if string(f["user-data"]) != "#!/bin/sh\necho hi\n" {
		t.Errorf("user data changed: %q", f["user-data"])
	}
	// #cloud-config passes through too.
	cc := "#cloud-config\nwrite_files:\n- path: /x\n  content: y\n"
	if got := string(Seed{UserData: cc}.Files()["user-data"]); got != cc {
		t.Errorf("cloud-config changed: %q", got)
	}
}

func TestSeedWithoutKeyOrUserData(t *testing.T) {
	f := Seed{InstanceID: "i-1", Hostname: "h"}.Files()
	if strings.Contains(string(f["meta-data"]), "public-keys") {
		t.Errorf("empty key produced public-keys:\n%s", f["meta-data"])
	}
	if !strings.HasPrefix(string(f["user-data"]), "#cloud-config") {
		t.Errorf("empty user data must still be a valid document: %q", f["user-data"])
	}
	// A key with quotes cannot break the YAML.
	m := string(Seed{InstanceID: "i", Hostname: "h", PublicKey: `ssh-rsa A"B\C x`}.Files()["meta-data"])
	if !strings.Contains(m, `"ssh-rsa A\"B\\C x"`) {
		t.Errorf("key not escaped:\n%s", m)
	}
}

func TestReady(t *testing.T) {
	boot := "[  32.2] cloud-init[464]: Cloud-init v. 22.4.2 running 'modules:final' at Wed\n"
	if d, p := Ready(boot); d || p {
		t.Errorf("running is not ready: %v %v", d, p)
	}
	done := boot + "2026-09-30T14:31:25Z [   32.8] cloud-init[464]: Cloud-init v. 22.4.2 finished at Wed, 30 Sep 2026 14:31:25 +0000. Datasource DataSourceNoCloud [seed=/dev/vdb][dsmode=net].  Up 32.80 seconds\n"
	if d, _ := Ready(done); !d {
		t.Error("finished marker not recognised")
	}
	if _, p := Ready("Debian GNU/Linux 12 exp1 ttyAMA0\n\nexp1 login: "); !p {
		t.Error("login prompt not recognised")
	}
}

func TestRunnerTagFollowsContent(t *testing.T) {
	a := RunnerTag()
	if !strings.HasPrefix(a, "homecloud/vm-runner:") || len(a) != len("homecloud/vm-runner:")+12 {
		t.Fatalf("tag %q", a)
	}
	if a != RunnerTag() {
		t.Error("tag not stable")
	}
	old := runScript
	runScript += "\n# changed\n"
	defer func() { runScript = old }()
	if RunnerTag() == a {
		t.Error("tag did not change with the entrypoint")
	}
	ctx := RunnerContext()
	for _, f := range []string{"Dockerfile", "hc-vm-run", "hc-vm-fetch", "hc-vm-ctl"} {
		if len(ctx[f]) == 0 {
			t.Errorf("build context lacks %s", f)
		}
	}
	if !strings.Contains(runnerDockerfile, "@sha256:") {
		t.Error("runner base image must be pinned by digest")
	}
}

func TestParseChecksums(t *testing.T) {
	sha256 := strings.Repeat("ab", 32)
	sha512 := strings.Repeat("cd", 64)
	got, err := ParseChecksums("# header\n" + sha256 + " *ubuntu.img\n\n" + sha512 + "  debian.qcow2\n" + strings.ToUpper(sha256) + "  upper.img\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["ubuntu.img"] != sha256 || got["debian.qcow2"] != sha512 || got["upper.img"] != sha256 || len(got) != 3 {
		t.Errorf("parsed %v", got)
	}
	for _, bad := range []string{"zz  file\n", sha256 + "\n", sha256[:60] + "  file\n", sha256 + "   \n"} {
		if _, err := ParseChecksums(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCatalogIsPinned(t *testing.T) {
	for key, b := range Bases {
		if b.Key != key || b.Release == "" || b.DefaultUser == "" {
			t.Errorf("%s: incomplete %+v", key, b)
		}
		for _, arch := range []Arch{ArchAArch64, ArchX8664} {
			f, err := b.For(arch)
			if err != nil {
				t.Errorf("%s: %v", key, err)
				continue
			}
			if !strings.HasPrefix(f.URL, "https://") {
				t.Errorf("%s/%s: URL %q is not https", key, arch, f.URL)
			}
			if !ValidSum(f.Sum) {
				t.Errorf("%s/%s: checksum %q", key, arch, f.Sum)
			}
			if !strings.Contains(f.URL, "-"+arch.Deb()) {
				t.Errorf("%s/%s: URL %q is for another architecture", key, arch, f.URL)
			}
			if !strings.Contains(f.URL, strings.TrimPrefix(b.Release, "release-")) {
				t.Errorf("%s/%s: URL %q is not pinned to release %s", key, arch, f.URL, b.Release)
			}
			if n := b.CacheName(arch); !strings.HasSuffix(n, ".qcow2") || strings.ContainsAny(n, "/ ") {
				t.Errorf("cache name %q", n)
			}
		}
		if b.CacheName(ArchAArch64) == b.CacheName(ArchX8664) {
			t.Errorf("%s: both architectures share a cache file", key)
		}
	}
	if _, err := (Base{Key: "x"}).For(ArchX8664); err == nil {
		t.Error("missing architecture accepted")
	}
}

func TestArchOf(t *testing.T) {
	for in, want := range map[string]Arch{"arm64": ArchAArch64, "aarch64": ArchAArch64, "x86_64": ArchX8664, "amd64": ArchX8664} {
		if got, err := ArchOf(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := ArchOf("riscv64"); err == nil {
		t.Error("riscv64 accepted")
	}
}

// TestCatalogMatchesPublishedChecksums compares the pinned checksums with the
// vendors' published files (a pin goes stale when a release is withdrawn). It
// needs the network: HC_TEST_NET=1.
func TestCatalogMatchesPublishedChecksums(t *testing.T) {
	if os.Getenv("HC_TEST_NET") != "1" {
		t.Skip("set HC_TEST_NET=1 to check the pinned checksums against the vendors' files")
	}
	c := http.Client{Timeout: time.Minute}
	sums := map[string]map[string]string{}
	for key, b := range Bases {
		for arch, f := range b.Files {
			dir := f.URL[:strings.LastIndex(f.URL, "/")+1]
			file := path.Base(f.URL)
			name := "SHA256SUMS"
			if len(f.Sum) == 128 {
				name = "SHA512SUMS"
			}
			if sums[dir+name] == nil {
				resp, err := c.Get(dir + name)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Errorf("%s: %s", dir+name, resp.Status)
					continue
				}
				if sums[dir+name], err = ParseChecksums(string(body)); err != nil {
					t.Fatalf("%s: %v", dir+name, err)
				}
			}
			if got := sums[dir+name][file]; got != f.Sum {
				t.Errorf("%s/%s: pinned %s, published %q", key, arch, f.Sum, got)
			}
		}
	}
}
