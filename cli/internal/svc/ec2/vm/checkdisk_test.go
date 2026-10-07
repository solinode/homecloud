package vm

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// qcow2Header is a minimal qcow2 image header: what hc-vm-checkdisk reads.
func qcow2Header(version uint32, backing string, incompat uint64) []byte {
	b := make([]byte, 4096)
	copy(b, []byte{'Q', 'F', 'I', 0xfb})
	binary.BigEndian.PutUint32(b[4:], version)
	if backing != "" {
		binary.BigEndian.PutUint64(b[8:], 512)
		binary.BigEndian.PutUint32(b[16:], uint32(len(backing)))
		copy(b[512:], backing)
	}
	binary.BigEndian.PutUint32(b[20:], 16)       // cluster bits
	binary.BigEndian.PutUint64(b[24:], 1<<30)    // virtual size
	binary.BigEndian.PutUint64(b[72:], incompat) // incompatible features (v3)
	binary.BigEndian.PutUint32(b[100:], 104)     // header length
	return b
}

// A disk whose qcow2 header or file type would make QEMU or qemu-img open
// something other than the disk is refused before QEMU sees it.
func TestCheckDisk(t *testing.T) {
	for _, tool := range []string{"sh", "od", "dd", "tr", "wc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "hc-vm-checkdisk")
	if err := os.WriteFile(script, []byte(checkDiskScript), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	check := func(args ...string) (string, bool) {
		out, err := exec.Command("sh", append([]string{script}, args...)...).CombinedOutput()
		return string(out), err == nil
	}

	base := "/images/ubuntu-24.04-amd64-20260926-6a81c37564db.qcow2"
	cases := []struct {
		name string
		img  []byte
		dir  string // allowed backing directory ("" = none)
		ok   bool
	}{
		{"overlay on the cached image", qcow2Header(3, base, 0), "/images", true},
		{"standalone v3", qcow2Header(3, "", 0), "", true},
		{"standalone v2", qcow2Header(2, "", 0), "/images", true},
		{"backing where none is allowed", qcow2Header(3, base, 0), "", false},
		{"backing on a host file", qcow2Header(3, "/etc/shadow", 0), "/images", false},
		{"backing out of the cache with ..", qcow2Header(3, "/images/../etc/passwd", 0), "/images", false},
		{"backing in a subdirectory of the cache", qcow2Header(3, "/images/x/disk.qcow2", 0), "/images", false},
		{"backing relative to the disk", qcow2Header(3, "disk.img", 0), "/images", false},
		{"json: backing", qcow2Header(3, `json:{"driver":"nbd","server":{"type":"inet","host":"169.254.169.254","port":"80"}}`, 0), "/images", false},
		{"protocol backing", qcow2Header(3, "nbd://10.0.0.1:10809/x", 0), "/images", false},
		{"backing with a newline", qcow2Header(3, "/images/a\n/etc/passwd", 0), "/images", false},
		{"external data file", qcow2Header(3, "", 4), "/images", false},
		{"external data file and a backing file", qcow2Header(3, base, 4|1), "/images", false},
		{"dirty but plain", qcow2Header(3, "", 1), "", true},
		{"not qcow2", []byte("raw disk contents, not a qcow2 header at all ...................................................................."), "/images", false},
		{"truncated header", []byte{'Q', 'F', 'I', 0xfb, 0, 0, 0, 3}, "/images", false},
	}
	for i, c := range cases {
		p := write("d"+string(rune('a'+i))+".qcow2", c.img)
		args := []string{"qcow2", p}
		if c.dir != "" {
			args = append(args, c.dir)
		}
		out, ok := check(args...)
		if ok != c.ok {
			t.Errorf("%s: accepted=%v, want %v\n%s", c.name, ok, c.ok, out)
		}
		if !ok && !strings.Contains(out, "[homecloud] refusing the disk") {
			t.Errorf("%s: no reason given: %q", c.name, out)
		}
	}

	// Symbolic links are refused for qcow2 and raw disks, even to a good image.
	good := write("good.qcow2", qcow2Header(3, "", 0))
	link := filepath.Join(dir, "link.qcow2")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if out, ok := check("qcow2", link, "/images"); ok {
		t.Errorf("a symlinked qcow2 was accepted: %s", out)
	}
	if out, ok := check("raw", link); ok {
		t.Errorf("a symlinked raw disk was accepted: %s", out)
	}
	dangling := filepath.Join(dir, "dangling.img")
	if err := os.Symlink("/vm/disk.qcow2", dangling); err != nil {
		t.Fatal(err)
	}
	if out, ok := check("raw", dangling); ok {
		t.Errorf("a dangling symlink was accepted: %s", out)
	}
	// A raw disk may be missing (it is created) or a regular file, nothing else.
	if out, ok := check("raw", filepath.Join(dir, "missing.img")); !ok {
		t.Errorf("a missing raw disk was refused: %s", out)
	}
	if out, ok := check("raw", good); !ok {
		t.Errorf("a regular raw disk was refused: %s", out)
	}
	if out, ok := check("raw", dir); ok {
		t.Errorf("a directory was accepted as a raw disk: %s", out)
	}
	if out, ok := check("qcow2", filepath.Join(dir, "missing.qcow2"), "/images"); ok {
		t.Errorf("a missing qcow2 was accepted: %s", out)
	}
}

// Every path that hands a volume's file to QEMU or qemu-img vets it first.
func TestDiskChecksAreWired(t *testing.T) {
	for _, want := range []string{
		`hc-vm-checkdisk qcow2 /ami/disk.qcow2`,
		`hc-vm-checkdisk qcow2 "$disk" /images`,
		`hc-vm-checkdisk raw /vm/efivars.fd`,
		`hc-vm-checkdisk raw "$img"`,
	} {
		if !strings.Contains(runScript, want) {
			t.Errorf("hc-vm-run lacks %q", want)
		}
	}
	// The data disk is vetted before truncate (which follows links) touches it.
	if strings.Index(runScript, `hc-vm-checkdisk raw "$img"`) > strings.Index(runScript, `truncate -s`) {
		t.Error("hc-vm-run truncates a data disk before checking it")
	}
	// The root disk is vetted after it is created and before QEMU starts.
	if strings.Index(runScript, `hc-vm-checkdisk qcow2 "$disk" /images`) > strings.Index(runScript, `"$@" &`) {
		t.Error("hc-vm-run starts QEMU before checking the root disk")
	}
	if i, j := strings.Index(flattenScript, "hc-vm-checkdisk qcow2"), strings.Index(flattenScript, "qemu-img convert"); i < 0 || i > j {
		t.Error("hc-vm-flatten converts a disk without checking it")
	}
	if !strings.Contains(runnerDockerfile, "COPY hc-vm-checkdisk /usr/local/bin/hc-vm-checkdisk") {
		t.Error("the runner image lacks hc-vm-checkdisk")
	}
}
