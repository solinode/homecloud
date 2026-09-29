package system

import (
	"archive/tar"
	"bytes"
	"io"
	"context"
	"os"
	"path/filepath"
	"testing"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	d, err := runtime.New()
	if err != nil || d.C.Ping() != nil {
		t.Skip("Docker not available")
	}
	ctx := context.Background()
	acct := "bk" + core.RandHex(10)
	vol := "hc-test-backup-" + core.RandHex(8)
	labels := map[string]string{core.LabelManaged: "true", core.LabelAccount: acct, core.LabelService: "test"}
	if err := d.CreateVolume(vol, labels); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.RemoveVolume(vol) })
	// Put a file into the volume.
	id, err := createHelper(ctx, d, vol)
	if err != nil {
		t.Fatal(err)
	}
	var tb bytes.Buffer
	if err := writeTestTar(&tb, "hello.txt", "volume data"); err != nil {
		t.Fatal(err)
	}
	if err := d.C.UploadToContainer(id, docker.UploadToContainerOptions{Path: "/v", InputStream: &tb}); err != nil {
		t.Fatal(err)
	}
	_ = d.Remove(id)

	src := t.TempDir()
	must(t, os.WriteFile(filepath.Join(src, "state.json"), []byte(`{"a":{}}`), 0o600))
	must(t, os.MkdirAll(filepath.Join(src, "lambda"), 0o700))
	must(t, os.WriteFile(filepath.Join(src, "lambda", "f.zip"), []byte("zip"), 0o600))
	must(t, os.WriteFile(filepath.Join(src, "server.log"), []byte("noise"), 0o600))
	must(t, os.WriteFile(filepath.Join(src, "open.db"), []byte("live file"), 0o600))

	cfg := core.DefaultConfig()
	cfg.DataDir = src
	b := &Backup{Cfg: cfg, Docker: d, AccountID: acct, Version: "test", OwnVolumesOnly: true,
		Snapshots: map[string]func(w io.Writer) error{"open.db": func(w io.Writer) error { _, err := w.Write([]byte("snapshot")); return err }}}
	var archive bytes.Buffer
	if err := b.Write(ctx, &archive, true, t.Logf); err != nil {
		t.Fatal(err)
	}
	must(t, d.RemoveVolume(vol))

	dst := filepath.Join(t.TempDir(), "restored")
	m, err := Restore(ctx, d, bytes.NewReader(archive.Bytes()), RestoreOptions{DataDir: dst, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if m.AccountID != acct || len(m.Volumes) != 1 || m.Volumes[0].Name != vol {
		t.Fatalf("manifest %+v", m)
	}
	for f, want := range map[string]string{"state.json": `{"a":{}}`, "lambda/f.zip": "zip", "open.db": "snapshot"} {
		got, err := os.ReadFile(filepath.Join(dst, f))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", f, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "server.log")); err == nil {
		t.Error("server.log should not be backed up")
	}
	v, err := d.C.InspectVolume(vol)
	if err != nil || v.Labels[core.LabelAccount] != acct {
		t.Fatalf("restored volume %+v %v", v, err)
	}
	id, err = createHelper(ctx, d, vol)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Remove(id)
	var out bytes.Buffer
	must(t, d.C.DownloadFromContainer(id, docker.DownloadFromContainerOptions{Path: "/v/hello.txt", OutputStream: &out}))
	if !bytes.Contains(out.Bytes(), []byte("volume data")) {
		t.Fatal("volume content not restored")
	}
	// Restoring again over existing data needs --force.
	if _, err := Restore(ctx, d, bytes.NewReader(archive.Bytes()), RestoreOptions{DataDir: dst}); err == nil {
		t.Fatal("restore over a non-empty data dir without force succeeded")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeTestTar(w io.Writer, name, content string) error {
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return err
	}
	return tw.Close()
}
