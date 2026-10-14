package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/system"
	bolt "go.etcd.io/bbolt"
)

// fakeReleases serves GitHub's release API and downloads for releases of
// solinode/homecloud; files maps tag -> asset name -> content.
type fakeReleases struct {
	latest string
	files  map[string]map[string][]byte
}

func (f *fakeReleases) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const api = "/repos/" + releaseRepo + "/releases"
	switch {
	case r.URL.Path == api:
		json.NewEncoder(w).Encode([]ghRelease{{TagName: f.latest, HTMLURL: "https://example.test/" + f.latest}})
	case strings.HasPrefix(r.URL.Path, api+"/tags/"):
		tag := strings.TrimPrefix(r.URL.Path, api+"/tags/")
		if f.files[tag] == nil {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(ghRelease{TagName: tag})
	case strings.HasPrefix(r.URL.Path, "/"+releaseRepo+"/releases/download/"):
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"+releaseRepo+"/releases/download/"), "/", 2)
		b, ok := f.files[parts[0]][parts[1]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

// releaseAssets builds a release for this platform holding bin: the archive
// and checksums.txt, plus a signature by signer unless it is "".
func releaseAssets(t *testing.T, sign func([]byte) []byte, bin []byte) map[string][]byte {
	t.Helper()
	name := fmt.Sprintf("homecloud-%s-%s", goruntime.GOOS, goruntime.GOARCH)
	var buf bytes.Buffer
	archive := name + ".tar.gz"
	if goruntime.GOOS == "windows" {
		archive = name + ".zip"
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(name + "/homecloud.exe")
		w.Write(bin)
		zw.Close()
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: name + "/homecloud", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
		tw.Write(bin)
		tw.Close()
		gz.Close()
	}
	sum := sha256.Sum256(buf.Bytes())
	sums := []byte(fmt.Sprintf("%x  %s\n%x  homecloud-plan9-mips.tar.gz\n", sum, archive, sha256.Sum256(nil)))
	files := map[string][]byte{archive: buf.Bytes(), "checksums.txt": sums}
	if sign != nil {
		files[signatureAsset] = sign(sums)
	}
	return files
}

type upgradeEnv struct {
	exe, dataDir string
	running      bool
	out          bytes.Buffer
	opts         upgradeOptions
}

func newUpgradeEnv(t *testing.T, rel *fakeReleases) *upgradeEnv {
	t.Helper()
	srv := httptest.NewServer(rel)
	t.Cleanup(srv.Close)
	e := &upgradeEnv{}
	dir := t.TempDir()
	e.exe = filepath.Join(dir, "bin", "homecloud")
	e.dataDir = filepath.Join(dir, "data")
	os.MkdirAll(filepath.Dir(e.exe), 0o755)
	if err := os.WriteFile(e.exe, []byte("OLD BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A server's data directory, including a DynamoDB (bbolt) database, and
	// data upgrade backups leave out.
	writeFiles(t, e.dataDir, map[string]string{
		"state.json": `{"v":1}`, "config.json": `{}`, "master.key": "KEY", "credentials": "{}",
		"sqs/q1.json": "{}", "lambda/fn.zip": "CODE", "logs/group/stream.log": "LOG", "server.log": "LOG",
	})
	db, err := bolt.Open(filepath.Join(e.dataDir, "dynamodb.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	db.Update(func(tx *bolt.Tx) error {
		b, _ := tx.CreateBucketIfNotExists([]byte("t"))
		return b.Put([]byte("k"), []byte("v1"))
	})
	db.Close()

	oldAPI, oldDL, oldExe, oldRunning, oldVersion := githubAPI, githubDownload, executablePath, serverRunning, Version
	githubAPI, githubDownload = srv.URL, srv.URL
	executablePath = func() (string, error) { return e.exe, nil }
	serverRunning = func(string) bool { return e.running }
	t.Cleanup(func() {
		githubAPI, githubDownload, executablePath, serverRunning, Version = oldAPI, oldDL, oldExe, oldRunning, oldVersion
	})
	e.opts = upgradeOptions{DataDir: e.dataDir, Out: &e.out, HTTP: srv.Client()}
	return e
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpgradeBackupAndRollback(t *testing.T) {
	vs := testSigstore(t)
	rel := &fakeReleases{latest: "v0.5.0", files: map[string]map[string][]byte{}}
	rel.files["v0.5.0"] = releaseAssets(t, func(b []byte) []byte { return signBundle(t, vs, signerIdentity("v0.5.0"), b) }, []byte("NEW BINARY"))
	e := newUpgradeEnv(t, rel)
	Version = "0.4.1"

	if err := runUpgrade(e.opts); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, e.out.String())
	}
	if got := readFile(t, e.exe); got != "NEW BINARY" {
		t.Fatalf("binary = %q", got)
	}
	if got := readFile(t, e.exe+".previous"); got != "OLD BINARY" {
		t.Fatalf("previous binary = %q", got)
	}
	if !strings.Contains(e.out.String(), "verified the signature of v0.5.0") {
		t.Errorf("output does not report the signature check:\n%s", e.out.String())
	}
	backups, err := system.ListUpgradeBackups(e.dataDir)
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v", backups, err)
	}
	b := backups[0]
	if b.From != "0.4.1" || b.To != "0.5.0" || b.Executable != e.exe {
		t.Errorf("backup manifest = %+v", b)
	}
	for _, f := range []string{"state.json", "config.json", "master.key", "credentials", "sqs/q1.json", "dynamodb.db"} {
		if _, err := os.Stat(filepath.Join(b.Dir, f)); err != nil {
			t.Errorf("backup lacks %s", f)
		}
	}
	for _, f := range []string{"lambda", "logs", "server.log", system.UpgradeBackupsDir} {
		if _, err := os.Stat(filepath.Join(b.Dir, f)); err == nil {
			t.Errorf("backup includes %s", f)
		}
	}

	// The new version starts (stamping the data) and changes it.
	if err := core.StampDataVersion(e.dataDir, "0.5.0"); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, e.dataDir, map[string]string{"state.json": `{"v":2}`, "sqs/q2.json": "{}"})
	if err := core.CheckDataVersion(e.dataDir, "0.4.1"); err == nil {
		t.Fatal("0.4.1 would start on data of 0.5.0")
	}

	// Rolling back needs the server stopped.
	Version = "0.5.0"
	e.running = true
	if err := runRollback(e.opts); err == nil || !strings.Contains(err.Error(), "stop it") {
		t.Fatalf("rollback with the server running: %v", err)
	}
	if got := readFile(t, e.exe); got != "NEW BINARY" {
		t.Fatal("a refused rollback changed the binary")
	}
	e.running = false
	if err := runRollback(e.opts); err != nil {
		t.Fatalf("rollback: %v\n%s", err, e.out.String())
	}
	if got := readFile(t, e.exe); got != "OLD BINARY" {
		t.Fatalf("binary after rollback = %q", got)
	}
	if _, err := os.Stat(e.exe + ".previous"); err == nil {
		t.Error(".previous left behind")
	}
	if got := readFile(t, filepath.Join(e.dataDir, "state.json")); got != `{"v":1}` {
		t.Errorf("state after rollback = %s", got)
	}
	for _, f := range []string{"sqs/q2.json", core.DataVersionFile} {
		if _, err := os.Stat(filepath.Join(e.dataDir, f)); err == nil {
			t.Errorf("%s survived the rollback", f)
		}
	}
	if got := readFile(t, filepath.Join(e.dataDir, "lambda", "fn.zip")); got != "CODE" {
		t.Errorf("lambda code = %q", got)
	}
	if err := core.CheckDataVersion(e.dataDir, "0.4.1"); err != nil {
		t.Errorf("0.4.1 refuses the restored data: %v", err)
	}
	db, err := bolt.Open(filepath.Join(e.dataDir, "dynamodb.db"), 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("restored DynamoDB database: %v", err)
	}
	db.Close()
	// The replaced state is kept.
	entries, _ := os.ReadDir(filepath.Join(e.dataDir, system.UpgradeBackupsDir))
	var aside string
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), "rolled-back-") {
			aside = filepath.Join(e.dataDir, system.UpgradeBackupsDir, en.Name())
		}
	}
	if aside == "" || readFile(t, filepath.Join(aside, "state.json")) != `{"v":2}` {
		t.Fatalf("rolled-back state not kept (entries %v)", entries)
	}

	// Nothing left to roll back to.
	if err := runRollback(e.opts); err == nil {
		t.Fatal("second rollback succeeded")
	}
}

func TestRollbackKeepsDataTheNewVersionNeverRan(t *testing.T) {
	vs := testSigstore(t)
	rel := &fakeReleases{latest: "v0.5.0", files: map[string]map[string][]byte{}}
	rel.files["v0.5.0"] = releaseAssets(t, func(b []byte) []byte { return signBundle(t, vs, signerIdentity("v0.5.0"), b) }, []byte("NEW BINARY"))
	e := newUpgradeEnv(t, rel)
	Version = "0.4.1"
	if err := runUpgrade(e.opts); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, e.out.String())
	}
	// The old server kept running and changed the data; the new version never started.
	writeFiles(t, e.dataDir, map[string]string{"state.json": `{"v":3}`})
	Version = "0.5.0"
	if err := runRollback(e.opts); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := readFile(t, filepath.Join(e.dataDir, "state.json")); got != `{"v":3}` {
		t.Errorf("state = %s, want the changes made after the upgrade kept", got)
	}
	if got := readFile(t, e.exe); got != "OLD BINARY" {
		t.Errorf("binary = %q", got)
	}
}

func TestUpgradeRefusesUnverifiedReleases(t *testing.T) {
	vs := testSigstore(t)
	other := func(b []byte) []byte {
		return signBundle(t, vs, "https://github.com/someone/homecloud/.github/workflows/release.yml@refs/tags/v0.5.0", b)
	}
	tampered := releaseAssets(t, func(b []byte) []byte { return signBundle(t, vs, signerIdentity("v0.5.0"), b) }, []byte("NEW BINARY"))
	for name, content := range tampered {
		if strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip") {
			tampered[name] = append(content, 0) // the archive no longer matches the signed checksums
		}
	}
	cases := []struct {
		name   string
		assets map[string][]byte
		want   string
	}{
		{"unsigned although newer than v0.5.0", releaseAssets(t, nil, []byte("NEW BINARY")), "every release from v0.5.0 on is signed"},
		{"signed by someone else", releaseAssets(t, other, []byte("NEW BINARY")), "does not verify"},
		{"archive does not match", tampered, "checksum mismatch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newUpgradeEnv(t, &fakeReleases{latest: "v0.5.0", files: map[string]map[string][]byte{"v0.5.0": c.assets}})
			Version = "0.4.1"
			e.opts.AllowUnsigned = true // only ever applies to releases before v0.5.0
			err := runUpgrade(e.opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if got := readFile(t, e.exe); got != "OLD BINARY" {
				t.Fatal("binary replaced")
			}
			if _, err := os.Stat(filepath.Join(e.dataDir, system.UpgradeBackupsDir)); err == nil {
				t.Error("an upgrade backup was made for a refused release")
			}
		})
	}
}

func TestUpgradeToUnsignedReleaseWithFlag(t *testing.T) {
	testSigstore(t)
	rel := &fakeReleases{latest: "v0.5.0", files: map[string]map[string][]byte{"v0.4.0": releaseAssets(t, nil, []byte("v0.4.0 BINARY"))}}
	e := newUpgradeEnv(t, rel)
	Version = "0.4.1"
	e.opts.Version = "v0.4.0"
	if err := runUpgrade(e.opts); err == nil || !strings.Contains(err.Error(), "--allow-unsigned") {
		t.Fatalf("unsigned v0.4.0 without --allow-unsigned: %v", err)
	}
	if got := readFile(t, e.exe); got != "OLD BINARY" {
		t.Fatal("binary replaced")
	}
	e.opts.AllowUnsigned = true
	if err := runUpgrade(e.opts); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if !strings.Contains(e.out.String(), "WARNING: v0.4.0 is not signed") {
		t.Errorf("no warning:\n%s", e.out.String())
	}
	if got := readFile(t, e.exe); got != "v0.4.0 BINARY" {
		t.Errorf("binary = %q", got)
	}
}

func TestUpgradeCheckAndUpToDate(t *testing.T) {
	testSigstore(t)
	e := newUpgradeEnv(t, &fakeReleases{latest: "v0.5.0"})
	Version = "0.5.0"
	if err := runUpgrade(e.opts); err != nil || !strings.Contains(e.out.String(), "up to date") {
		t.Fatalf("err = %v, out = %s", err, e.out.String())
	}
	Version = "0.4.1"
	e.opts.Check = true
	if err := runUpgrade(e.opts); err != nil || !strings.Contains(e.out.String(), "available: v0.5.0") {
		t.Fatalf("err = %v, out = %s", err, e.out.String())
	}
	if got := readFile(t, e.exe); got != "OLD BINARY" {
		t.Fatal("--check replaced the binary")
	}
}

func TestUnitDataDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "homecloud.service")
	os.WriteFile(p, []byte("[Service]\nExecStart=/usr/local/bin/homecloud serve\nEnvironment=HOMECLOUD_DATA_DIR=/var/lib/homecloud\n"), 0o644)
	if got := unitDataDir(p); got != "/var/lib/homecloud" {
		t.Fatalf("unitDataDir = %q", got)
	}
	if got := unitDataDir(p + ".missing"); got != "" {
		t.Fatalf("missing unit: %q", got)
	}
}
