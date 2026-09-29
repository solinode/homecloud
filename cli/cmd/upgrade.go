package cmd

import (
	"archive/tar"
	"archive/zip"
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
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const releaseRepo = "homecloudhq/homecloud"

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	HTMLURL    string `json:"html_url"`
}

func init() {
	var version string
	var check bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the homecloud binary to the latest release",
		Long: `Downloads the latest HomeCloud release for this platform from GitHub, verifies
its SHA-256 checksum and replaces this binary. Restart the server afterwards
(or "homecloud service install" again) to run the new version; data is migrated
automatically on start.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			hc := &http.Client{Timeout: 5 * time.Minute}
			rel, err := findRelease(hc, version)
			if err != nil {
				return err
			}
			current := "v" + strings.TrimPrefix(Version, "v")
			if rel.TagName == current || (version == "" && compareVersions(rel.TagName, current) <= 0) {
				fmt.Printf("homecloud %s is up to date (latest release: %s)\n", current, rel.TagName)
				return nil
			}
			fmt.Printf("current: %s   available: %s (%s)\n", current, rel.TagName, rel.HTMLURL)
			if check {
				return nil
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			bin, err := downloadRelease(hc, rel.TagName)
			if err != nil {
				return err
			}
			if err := replaceExecutable(exe, bin); err != nil {
				return fmt.Errorf("install %s: %w (try again with sudo)", exe, err)
			}
			fmt.Printf("upgraded %s to %s; restart the server to use it\n", exe, rel.TagName)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "install this release tag (e.g. v0.2.0) instead of the latest")
	cmd.Flags().BoolVar(&check, "check", false, "only report whether an upgrade is available")
	RootCmd.AddCommand(cmd)
}

func findRelease(hc *http.Client, tag string) (ghRelease, error) {
	var rel ghRelease
	url := "https://api.github.com/repos/" + releaseRepo + "/releases?per_page=10"
	if tag != "" {
		url = "https://api.github.com/repos/" + releaseRepo + "/releases/tags/" + tag
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

func fetch(hc *http.Client, url string) ([]byte, error) {
	resp, err := hc.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 500<<20))
}

// downloadRelease returns the verified homecloud binary of a release for this platform.
func downloadRelease(hc *http.Client, tag string) ([]byte, error) {
	name := fmt.Sprintf("homecloud-%s-%s", goruntime.GOOS, goruntime.GOARCH)
	archive := name + ".tar.gz"
	if goruntime.GOOS == "windows" {
		archive = name + ".zip"
	}
	base := "https://github.com/" + releaseRepo + "/releases/download/" + tag + "/"
	sums, err := fetch(hc, base+"checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("checksums: %w", err)
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
	fmt.Printf("downloading %s...\n", archive)
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
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		fmt.Sscanf(p, "%d", &out[i])
	}
	return out
}
