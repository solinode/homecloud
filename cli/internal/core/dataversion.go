package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DataVersionFile, in the data directory, records the newest HomeCloud release
// that has run on it. Servers refuse to start on data written by a newer
// release: it may have migrated the data into a form older code misreads.
const DataVersionFile = "data-version.json"

// DataVersion is the content of DataVersionFile.
type DataVersion struct {
	Version   string    `json:"version"` // e.g. "0.5.0"
	UpdatedAt time.Time `json:"updated_at"`
}

// ReadDataVersion returns the data directory's stamp; ok is false when it has
// none (a new directory, or one last used by HomeCloud 0.4.x or older).
func ReadDataVersion(dataDir string) (v DataVersion, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dataDir, DataVersionFile))
	if errors.Is(err, os.ErrNotExist) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, false, fmt.Errorf("corrupt %s: %w", filepath.Join(dataDir, DataVersionFile), err)
	}
	return v, v.Version != "", nil
}

// NewerDataError reports a data directory last used by a newer HomeCloud.
type NewerDataError struct {
	DataDir, DataVersion, Version string
}

func (e *NewerDataError) Error() string {
	return fmt.Sprintf("the data directory %s was last used by HomeCloud %s, which is newer than this binary (%s).\n"+
		"Starting an older HomeCloud on newer data can corrupt it. Either:\n"+
		"  - run HomeCloud %s or newer again (homecloud upgrade --version v%s), or\n"+
		"  - if you rolled back on purpose, restore the state saved before the upgrade: homecloud upgrade --rollback", e.DataDir, e.DataVersion, e.Version, e.DataVersion, e.DataVersion)
}

// CheckDataVersion returns a *NewerDataError when dataDir was last used by a
// HomeCloud release newer than version. Development builds are not checked.
func CheckDataVersion(dataDir, version string) error {
	if IsDevVersion(version) {
		return nil
	}
	v, ok, err := ReadDataVersion(dataDir)
	if err != nil || !ok || IsDevVersion(v.Version) {
		return err
	}
	if CompareVersions(v.Version, version) > 0 {
		return &NewerDataError{DataDir: dataDir, DataVersion: strings.TrimPrefix(v.Version, "v"), Version: strings.TrimPrefix(version, "v")}
	}
	return nil
}

// StampDataVersion records version in dataDir unless the stamp already names
// the same or a newer release. Development builds leave it alone.
func StampDataVersion(dataDir, version string) error {
	if IsDevVersion(version) {
		return nil
	}
	version = strings.TrimPrefix(version, "v")
	cur, ok, err := ReadDataVersion(dataDir)
	if err != nil {
		return err
	}
	if ok && !IsDevVersion(cur.Version) && CompareVersions(cur.Version, version) >= 0 {
		return nil
	}
	b, _ := json.MarshalIndent(DataVersion{Version: version, UpdatedAt: Now()}, "", "  ")
	p := filepath.Join(dataDir, DataVersionFile)
	if err := os.WriteFile(p+".tmp", append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// IsDevVersion reports whether version is not a release number: "dev", a bare
// commit, or a "-dev" build. Release candidates ("0.5.0-rc1") and builds made
// after a tag ("v0.4.1-3-gabcdef") are releases for this purpose.
func IsDevVersion(version string) bool {
	v := strings.TrimPrefix(version, "v")
	if v == "" || v[0] < '0' || v[0] > '9' || strings.HasSuffix(v, "-dev") || strings.Contains(v, "-dev.") {
		return true
	}
	return len(strings.SplitN(strings.SplitN(v, "-", 2)[0], ".", 3)) != 3
}

// CompareVersions compares the numeric parts of two versions ("v1.2.3-rc1"
// counts as 1.2.3) and returns -1, 0 or 1.
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := range pa {
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
		out[i], _ = strconv.Atoi(p)
	}
	return out
}
