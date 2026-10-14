package cmd

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/system"
	"github.com/spf13/cobra"
)

const releaseRepo = "solinode/homecloud"

// Where releases come from; tests point these at a local server.
var (
	githubAPI      = "https://api.github.com"
	githubDownload = "https://github.com"
	executablePath = func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
	serverRunning = running
)

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	HTMLURL    string `json:"html_url"`
}

type upgradeOptions struct {
	Version       string // release tag to install; "" for the latest
	Check         bool
	AllowUnsigned bool
	DataDir       string
	Out           io.Writer
	HTTP          *http.Client
}

func init() {
	var o upgradeOptions
	var rollback bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the homecloud binary to the latest release (or roll back)",
		Long: `Downloads the latest HomeCloud release for this platform from GitHub, verifies
its Sigstore signature (made by the release workflow of this repository) and
SHA-256 checksum, and replaces this binary. Restart the server afterwards (or
"homecloud service install" again) to run the new version; data is migrated
automatically on start.

Before replacing the binary, upgrade saves the data directory's state (state
file, config, credentials, master key, DynamoDB data, queue files; not logs,
Lambda code or Docker volumes) to <data-dir>/upgrade-backups/<version>-<time>/
and keeps the current binary as <binary>.previous. The last 3 backups are kept.

"homecloud upgrade --rollback" (with the server stopped) puts the previous
binary back and, if the new version already ran on the data, the saved state
too; the state it replaces is kept in <data-dir>/upgrade-backups/rolled-back-*.

Releases before v0.5.0 are not signed: installing one needs --allow-unsigned.`,
		Example: "  sudo homecloud upgrade\n  homecloud upgrade --check\n  sudo homecloud upgrade --version v0.5.0\n  sudo systemctl stop homecloud && sudo homecloud upgrade --rollback",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("data-dir") {
				dir, err := upgradeDataDir()
				if err != nil {
					return err
				}
				o.DataDir = dir
			}
			o.Out = cmd.OutOrStdout()
			o.HTTP = &http.Client{Timeout: 5 * time.Minute}
			if rollback {
				return runRollback(o)
			}
			return runUpgrade(o)
		},
	}
	cmd.Flags().StringVar(&o.Version, "version", "", "install this release tag (e.g. v0.5.0) instead of the latest")
	cmd.Flags().BoolVar(&o.Check, "check", false, "only report whether an upgrade is available")
	cmd.Flags().BoolVar(&o.AllowUnsigned, "allow-unsigned", false, "allow installing a release without a signature (v0.4.1 and older), verified by checksum only")
	cmd.Flags().BoolVar(&rollback, "rollback", false, "restore the previous binary and, if needed, the state saved before the last upgrade (stop the server first)")
	cmd.Flags().StringVar(&o.DataDir, "data-dir", core.DefaultDataDir(), "the server's data directory (default: the installed service's, or ~/.homecloud)")
	RootCmd.AddCommand(cmd)
}

func runUpgrade(o upgradeOptions) error {
	rel, err := findRelease(o.HTTP, o.Version)
	if err != nil {
		return err
	}
	current := "v" + strings.TrimPrefix(Version, "v")
	if rel.TagName == current || (o.Version == "" && compareVersions(rel.TagName, current) <= 0) {
		fmt.Fprintf(o.Out, "homecloud %s is up to date (latest release: %s)\n", current, rel.TagName)
		return nil
	}
	fmt.Fprintf(o.Out, "current: %s   available: %s (%s)\n", current, rel.TagName, rel.HTMLURL)
	if o.Check {
		return nil
	}
	exe, err := executablePath()
	if err != nil {
		return err
	}
	bin, err := downloadRelease(o.HTTP, rel.TagName, o.AllowUnsigned, o.Out)
	if err != nil {
		return err
	}

	// A rollback point: the data directory's state and the current binary.
	if hasServerData(o.DataDir) {
		if serverRunning(o.DataDir) {
			fmt.Fprintf(o.Out, "note: the server is running; it keeps running %s until restarted, and a rollback returns the data to this moment\n", current)
		}
		b, err := system.SaveUpgradeBackup(o.DataDir, system.UpgradeBackup{
			From: strings.TrimPrefix(Version, "v"), To: strings.TrimPrefix(rel.TagName, "v"), Executable: exe})
		if err != nil {
			return fmt.Errorf("back up %s before upgrading (nothing was changed): %w", o.DataDir, err)
		}
		fmt.Fprintf(o.Out, "saved the state of %s to %s (%d files, %.1f MB)\n", o.DataDir, b.Dir, b.Files, float64(b.Bytes)/(1<<20))
	} else {
		fmt.Fprintf(o.Out, "no server data in %s: nothing to back up\n", o.DataDir)
	}
	if err := copyExecutable(exe, exe+".previous"); err != nil {
		return fmt.Errorf("keep the current binary as %s.previous: %w (try again with sudo)", exe, err)
	}
	if err := replaceExecutable(exe, bin); err != nil {
		return fmt.Errorf("install %s: %w (try again with sudo)", exe, err)
	}
	fmt.Fprintf(o.Out, "upgraded %s to %s; restart the server to use it\nto go back: stop the server, then run homecloud upgrade --rollback\n", exe, rel.TagName)
	return nil
}

func runRollback(o upgradeOptions) error {
	exe, err := executablePath()
	if err != nil {
		return err
	}
	prev := exe + ".previous"
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("no previous binary (%s): nothing to roll back to", prev)
	}
	dir, err := filepath.Abs(o.DataDir)
	if err != nil {
		return err
	}
	if serverRunning(dir) {
		return fmt.Errorf("a HomeCloud server is running for %s; stop it before rolling back", dir)
	}
	stamp, stamped, err := core.ReadDataVersion(dir)
	if err != nil {
		return err
	}
	backups, err := system.ListUpgradeBackups(dir)
	if err != nil {
		return err
	}
	target := "the previous version"
	switch {
	case len(backups) > 0:
		b := backups[0]
		target = "HomeCloud " + b.From
		// Only data a newer version has run on needs the saved state; data
		// still at the old version keeps everything done since the upgrade.
		if stamped && !core.IsDevVersion(stamp.Version) && !core.IsDevVersion(b.From) && core.CompareVersions(stamp.Version, b.From) > 0 {
			aside, err := system.RestoreUpgradeBackup(dir, b, stamp.Version)
			if err != nil {
				return fmt.Errorf("restore the state saved before the upgrade (nothing was changed): %w", err)
			}
			fmt.Fprintf(o.Out, "restored the state saved at %s (before upgrading from %s to %s)\nthe state of %s is kept in %s\n",
				b.CreatedAt.Local().Format(time.RFC1123), b.From, b.To, stamp.Version, aside)
		} else {
			fmt.Fprintf(o.Out, "the data in %s was not changed by a newer version; keeping it\n", dir)
		}
	case stamped:
		fmt.Fprintf(o.Out, "warning: no upgrade backup in %s; the data was last used by HomeCloud %s, which an older binary refuses to start on\n", dir, stamp.Version)
	}
	bin, err := os.ReadFile(prev)
	if err != nil {
		return err
	}
	if err := replaceExecutable(exe, bin); err != nil {
		return fmt.Errorf("install %s: %w (try again with sudo)", exe, err)
	}
	_ = os.Remove(prev)
	fmt.Fprintf(o.Out, "rolled %s back to %s; start the server again\n", exe, target)
	return nil
}

// hasServerData reports whether dir holds a server's data (not just CLI credentials).
func hasServerData(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "state.json"))
	return err == nil
}

// upgradeDataDir finds the data directory of this machine's server when
// --data-dir isn't given: $HOMECLOUD_DATA_DIR, the one a system service
// was installed with, or the invoking user's (also through sudo) ~/.homecloud.
func upgradeDataDir() (string, error) {
	if d := os.Getenv("HOMECLOUD_DATA_DIR"); d != "" {
		return d, nil
	}
	if d := unitDataDir(systemdPath(true)); d != "" {
		return d, nil
	}
	return serviceDataDir(false, core.DefaultDataDir(), os.Geteuid(), os.Getenv("SUDO_USER"), "", func(u string) (string, error) {
		usr, err := user.Lookup(u)
		if err != nil {
			return "", err
		}
		return usr.HomeDir, nil
	})
}

// unitDataDir reads HOMECLOUD_DATA_DIR from a systemd unit written by "homecloud service install".
func unitDataDir(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(s.Text()), "Environment=HOMECLOUD_DATA_DIR="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func findRelease(hc *http.Client, tag string) (ghRelease, error) {
	var rel ghRelease
	url := githubAPI + "/repos/" + releaseRepo + "/releases?per_page=10"
	if tag != "" {
		url = githubAPI + "/repos/" + releaseRepo + "/releases/tags/" + tag
	}
	resp, err := hc.Get(url)
	if err != nil {
		return rel, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rel, fmt.Errorf("GitHub returned %s for %s", resp.Status, url)
	}
	if tag != "" {
		return rel, json.NewDecoder(resp.Body).Decode(&rel)
	}
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return rel, err
	}
	for _, r := range rels {
		if !r.Draft {
			return r, nil // releases are listed newest first; prereleases count until 1.0
		}
	}
	return rel, errors.New("no releases published yet")
}

var errNotFound = errors.New("not found")

func fetch(hc *http.Client, url string) ([]byte, error) {
	resp, err := hc.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("download %s: %w", url, errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 500<<20))
}

// downloadRelease returns the verified homecloud binary of a release for this
// platform: checksums.txt must carry the release workflow's Sigstore signature
// (unless allowUnsigned and the release has none), and the archive must match it.
func downloadRelease(hc *http.Client, tag string, allowUnsigned bool, out io.Writer) ([]byte, error) {
	name := fmt.Sprintf("homecloud-%s-%s", goruntime.GOOS, goruntime.GOARCH)
	archive := name + ".tar.gz"
	if goruntime.GOOS == "windows" {
		archive = name + ".zip"
	}
	base := githubDownload + "/" + releaseRepo + "/releases/download/" + tag + "/"
	sums, err := fetch(hc, base+"checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("checksums: %w", err)
	}
	sig, err := fetch(hc, base+signatureAsset)
	switch {
	case errors.Is(err, errNotFound) && compareVersions(tag, firstSignedRelease) >= 0:
		return nil, fmt.Errorf("release %s has no signature (%s), but every release from %s on is signed: refusing to install it", tag, signatureAsset, firstSignedRelease)
	case errors.Is(err, errNotFound) && allowUnsigned:
		fmt.Fprintf(out, "WARNING: %s is not signed (releases before v0.5.0 were not); --allow-unsigned: trusting its checksums.txt from GitHub without a signature\n", tag)
	case errors.Is(err, errNotFound):
		return nil, fmt.Errorf("release %s has no signature (%s); releases before v0.5.0 were not signed. "+
			"To install it anyway, verified by checksum only, run again with --allow-unsigned", tag, signatureAsset)
	case err != nil:
		return nil, fmt.Errorf("signature: %w", err)
	default:
		if err := verifyChecksumsSignature(tag, sums, sig); err != nil {
			return nil, err
		}
		fmt.Fprintf(out, "verified the signature of %s: signed by %s\n", tag, signerIdentity(tag))
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == archive {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("release %s has no build for %s/%s", tag, goruntime.GOOS, goruntime.GOARCH)
	}
	fmt.Fprintf(out, "downloading %s...\n", archive)
	data, err := fetch(hc, base+archive)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s: the download is corrupt or was tampered with", archive)
	}
	exeName := "homecloud"
	if goruntime.GOOS == "windows" {
		exeName += ".exe"
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == exeName {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("%s not found in %s", exeName, archive)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not found in %s", exeName, archive)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == exeName && h.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
}

// copyExecutable copies exe to dst (replacing it) through a temporary file.
func copyExecutable(exe, dst string) error {
	b, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// replaceExecutable swaps exe for bin atomically where the OS allows it.
func replaceExecutable(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".homecloud-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if goruntime.GOOS == "windows" {
		// A running .exe can be renamed but not overwritten.
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), exe)
}

// compareVersions compares the numeric parts of two versions ("v1.2.3-rc1" -> 1,2,3).
func compareVersions(a, b string) int { return core.CompareVersions(a, b) }
