package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A cached image that no longer matches its checksum is downloaded again, not used.
func TestFetchVerifiesTheCachedImage(t *testing.T) {
	for _, tool := range []string{"sh", "curl", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "hc-vm-fetch")
	if err := os.WriteFile(script, []byte(fetchScript), 0o755); err != nil {
		t.Fatal(err)
	}
	images := filepath.Join(dir, "images")
	if err := os.Mkdir(images, 0o755); err != nil {
		t.Fatal(err)
	}
	good := []byte("the real image")
	src := filepath.Join(dir, "upstream.img")
	if err := os.WriteFile(src, good, 0o644); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(good)
	sum := hex.EncodeToString(h[:])
	run := func() ([]byte, error) {
		cmd := exec.Command("sh", script, "file://"+src, "base.qcow2", sum)
		cmd.Env = append(os.Environ(), "HC_VM_IMAGES="+images)
		return cmd.CombinedOutput()
	}
	cached := filepath.Join(images, "base.qcow2")
	// A corrupted (or replaced) cache file.
	if err := os.WriteFile(cached, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(cached); string(got) != string(good) {
		t.Fatalf("cache holds %q after the fetch, want the verified image", got)
	}
	// A good cache file is kept (no download: the source is gone).
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err != nil {
		t.Fatalf("fetch with a valid cache: %v\n%s", err, out)
	}
	// A download that does not match is refused and leaves nothing behind.
	if err := os.WriteFile(src, []byte("something else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("tampered again"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil {
		t.Fatalf("fetch accepted a mismatching download:\n%s", out)
	}
	if _, err := os.Stat(cached); !os.IsNotExist(err) {
		t.Errorf("a mismatching image is in the cache: %v", err)
	}
}
