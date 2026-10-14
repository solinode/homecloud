package cmd

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// The install scripts are served from pages/ too; they must not drift.
func TestInstallScriptCopiesMatch(t *testing.T) {
	for _, f := range []string{"install.sh", "install.ps1"} {
		a, err := os.ReadFile(filepath.Join("..", "..", "scripts", f))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join("..", "..", "pages", "scripts", f))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("pages/scripts/%s differs from scripts/%s", f, f)
		}
	}
}

// TestInstallScript runs scripts/install.sh against a local release server,
// with and without a (fake) cosign on PATH.
func TestInstallScript(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("install.sh is for Linux and macOS")
	}
	if goruntime.GOARCH != "amd64" && goruntime.GOARCH != "arm64" {
		t.Skip("no release for", goruntime.GOARCH)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	bin := []byte("#!/bin/sh\necho \"HomeCloud CLI version fake\"\n")
	sign := func([]byte) []byte { return []byte(`{"fake":"bundle"}`) }
	tampered := releaseAssets(t, sign, bin)
	for name, content := range tampered {
		if strings.HasSuffix(name, ".tar.gz") {
			tampered[name] = append(content, 0)
		}
	}
	noEntry := releaseAssets(t, sign, bin)
	noEntry["checksums.txt"] = []byte("0000  homecloud-plan9-mips.tar.gz\n")
	rel := &fakeReleases{files: map[string]map[string][]byte{
		"v0.5.0": releaseAssets(t, sign, bin),
		"v0.5.1": releaseAssets(t, nil, bin), // signature missing
		"v0.4.0": releaseAssets(t, nil, bin), // before signing
		"v0.5.2": tampered,
		"v0.5.3": noEntry,
	}}
	srv := httptest.NewServer(rel)
	defer srv.Close()

	// A PATH with just the tools the script uses (and so no real cosign).
	tools := t.TempDir()
	for _, name := range []string{"sh", "uname", "tr", "curl", "mktemp", "rm", "sed", "head", "awk", "cut", "sha256sum", "shasum", "tar", "install", "grep", "cat", "gzip", "perl"} {
		if p, err := exec.LookPath(name); err == nil {
			os.Symlink(p, filepath.Join(tools, name))
		}
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	withCosign := t.TempDir()
	cosignLog := filepath.Join(withCosign, "cosign.args")
	os.WriteFile(filepath.Join(withCosign, "cosign"), []byte("#!/bin/sh\necho \"$@\" > '"+cosignLog+"'\nexit ${FAKE_COSIGN_EXIT:-0}\n"), 0o755)

	cases := []struct {
		name, version string
		cosign        bool
		cosignExit    string
		ok            bool
		want          string
	}{
		{"checksum only without cosign", "v0.5.0", false, "0", true, "cosign is not installed"},
		{"verified with cosign", "v0.5.0", true, "0", true, "Verified the release signature with cosign"},
		{"bad signature", "v0.5.0", true, "1", false, "Signature verification of v0.5.0 FAILED"},
		{"signature missing", "v0.5.1", true, "0", false, "could not be downloaded"},
		{"release before signing", "v0.4.0", true, "0", true, "v0.4.0 is not signed"},
		{"checksum mismatch", "v0.5.2", false, "0", false, "Checksum mismatch"},
		{"no checksum entry", "v0.5.3", false, "0", false, "has no entry for"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dest := t.TempDir()
			os.Remove(cosignLog)
			path := tools
			if c.cosign {
				path = withCosign + ":" + tools
			}
			cmd := exec.Command(filepath.Join(tools, "sh"), script)
			cmd.Env = []string{"PATH=" + path, "HOME=" + t.TempDir(), "INSTALL_DIR=" + dest, "HOMECLOUD_VERSION=" + c.version,
				"HOMECLOUD_RELEASES_URL=" + srv.URL + "/" + releaseRepo + "/releases/download", "FAKE_COSIGN_EXIT=" + c.cosignExit}
			out, err := cmd.CombinedOutput()
			if (err == nil) != c.ok {
				t.Fatalf("exit: %v (want ok=%v)\n%s", err, c.ok, out)
			}
			if !strings.Contains(string(out), c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, out)
			}
			_, statErr := os.Stat(filepath.Join(dest, "homecloud"))
			if c.ok != (statErr == nil) {
				t.Errorf("installed = %v, want %v", statErr == nil, c.ok)
			}
			if c.cosign && c.version == "v0.5.0" {
				args, _ := os.ReadFile(cosignLog)
				for _, w := range []string{"verify-blob", "--bundle", "--certificate-identity https://github.com/solinode/homecloud/.github/workflows/release.yml@refs/tags/v0.5.0",
					"--certificate-oidc-issuer https://token.actions.githubusercontent.com"} {
					if !strings.Contains(string(args), w) {
						t.Errorf("cosign called without %q: %s", w, args)
					}
				}
			}
		})
	}
}
