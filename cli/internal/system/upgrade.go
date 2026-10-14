package system

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	bolt "go.etcd.io/bbolt"
)

// Upgrade backups: "homecloud upgrade" saves the data directory's state before
// it replaces the binary, so "homecloud upgrade --rollback" can return to the
// previous version together with data that version understands.
//
// They hold what a new version's startup migrations may rewrite: the state
// file, config, credentials, the master key, TLS files, the DynamoDB database,
// queue/topic/state machine files, metrics and the data version stamp. Large,
// append-only or content-addressed data that migrations leave alone stays out
// (logs, CloudTrail, RDS snapshot files, Lambda code), as do Docker volumes:
// for those, use "homecloud backup".

// UpgradeBackupsDir is the directory, inside the data directory, holding upgrade backups.
const UpgradeBackupsDir = "upgrade-backups"

// KeepUpgradeBackups is how many upgrade backups (and rolled-back states) are kept.
const KeepUpgradeBackups = 3

const (
	upgradeManifest = "upgrade.json"
	rolledBackDir   = "rolled-back-"
)

// upgradeSkip lists top-level data directory entries upgrade backups leave out.
var upgradeSkip = map[string]bool{
	UpgradeBackupsDir: true, "backups": true, "backup-legacy-region": true,
	"server.log": true, "logs": true, "ecs-logs": true, "trail": true,
	"rds-snapshots": true, "lambda": true,
}

func inUpgradeBackup(name string) bool {
	return !upgradeSkip[name] && !strings.HasSuffix(name, ".tmp") && !strings.HasSuffix(name, ".migrate") &&
		!strings.HasPrefix(name, ".rollback-")
}

// UpgradeBackup describes one saved state.
type UpgradeBackup struct {
	Dir        string    `json:"-"`
	From       string    `json:"from"` // the version that was running (and is restored on rollback)
	To         string    `json:"to"`
	CreatedAt  time.Time `json:"created_at"`
	Executable string    `json:"executable,omitempty"`
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"`
}

// SaveUpgradeBackup copies dataDir's state into
// <dataDir>/upgrade-backups/<from>-<time>/ and prunes old backups.
func SaveUpgradeBackup(dataDir string, b UpgradeBackup) (UpgradeBackup, error) {
	if b.CreatedAt.IsZero() {
		b.CreatedAt = core.Now()
	}
	root := filepath.Join(dataDir, UpgradeBackupsDir)
	if err := mkdirOwned(root, dataDir); err != nil {
		return b, err
	}
	name := fmt.Sprintf("%s-%s", safeName(strings.TrimPrefix(b.From, "v")), b.CreatedAt.UTC().Format("20060102-150405"))
	dst := filepath.Join(root, name)
	for i := 2; exists(dst); i++ {
		dst = filepath.Join(root, fmt.Sprintf("%s-%d", name, i))
	}
	tmp := dst + ".partial"
	_ = os.RemoveAll(tmp)
	if err := mkdirOwned(tmp, dataDir); err != nil {
		return b, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return b, err
	}
	for _, e := range entries {
		if !inUpgradeBackup(e.Name()) {
			continue
		}
		n, size, err := copyTree(filepath.Join(dataDir, e.Name()), filepath.Join(tmp, e.Name()))
		if err != nil {
			return b, fmt.Errorf("back up %s: %w", e.Name(), err)
		}
		b.Files += n
		b.Bytes += size
	}
	mb, _ := json.MarshalIndent(b, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, upgradeManifest), append(mb, '\n'), 0o600); err != nil {
		return b, err
	}
	if fi, err := os.Stat(dataDir); err == nil {
		matchOwner(filepath.Join(tmp, upgradeManifest), fi)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return b, err
	}
	ok = true
	b.Dir = dst
	return b, PruneUpgradeBackups(dataDir, KeepUpgradeBackups)
}

// ListUpgradeBackups returns the upgrade backups in dataDir, newest first.
func ListUpgradeBackups(dataDir string) ([]UpgradeBackup, error) {
	root := filepath.Join(dataDir, UpgradeBackupsDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []UpgradeBackup
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mb, err := os.ReadFile(filepath.Join(root, e.Name(), upgradeManifest))
		if err != nil {
			continue // a partial copy or a rolled-back state
		}
		var b UpgradeBackup
		if json.Unmarshal(mb, &b) != nil {
			continue
		}
		b.Dir = filepath.Join(root, e.Name())
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// PruneUpgradeBackups keeps the newest keep upgrade backups and the newest
// keep rolled-back states, and removes leftovers of interrupted copies.
func PruneUpgradeBackups(dataDir string, keep int) error {
	backups, err := ListUpgradeBackups(dataDir)
	if err != nil {
		return err
	}
	for i := keep; i < len(backups); i++ {
		if err := os.RemoveAll(backups[i].Dir); err != nil {
			return err
		}
	}
	root := filepath.Join(dataDir, UpgradeBackupsDir)
	entries, _ := os.ReadDir(root)
	var rolled []string
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".partial"):
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		case strings.HasPrefix(e.Name(), rolledBackDir):
			rolled = append(rolled, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(rolled))) // names end in a timestamp
	for i := keep; i < len(rolled); i++ {
		if err := os.RemoveAll(filepath.Join(root, rolled[i])); err != nil {
			return err
		}
	}
	return nil
}

// RestoreUpgradeBackup puts the state saved in b back into dataDir. The
// current state is moved to <dataDir>/upgrade-backups/rolled-back-<version>-<time>
// (returned), so nothing is lost. The server must be stopped.
func RestoreUpgradeBackup(dataDir string, b UpgradeBackup, currentVersion string) (string, error) {
	stage := filepath.Join(dataDir, ".rollback-"+core.RandHex(6))
	if err := mkdirOwned(stage, dataDir); err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	saved, err := os.ReadDir(b.Dir)
	if err != nil {
		return "", err
	}
	// 1. Copy the backup next to the data (same file system, so the moves below are renames).
	var names []string
	for _, e := range saved {
		if e.Name() == upgradeManifest {
			continue
		}
		if _, _, err := copyTree(filepath.Join(b.Dir, e.Name()), filepath.Join(stage, e.Name())); err != nil {
			return "", fmt.Errorf("copy %s: %w", e.Name(), err)
		}
		names = append(names, e.Name())
	}
	// 2. Move the current state aside.
	base := filepath.Join(dataDir, UpgradeBackupsDir,
		fmt.Sprintf("%s%s-%s", rolledBackDir, core.Now().Format("20060102-150405"), safeName(strings.TrimPrefix(currentVersion, "v"))))
	aside := base
	for i := 2; exists(aside); i++ {
		aside = fmt.Sprintf("%s-%d", base, i)
	}
	if err := mkdirOwned(aside, dataDir); err != nil {
		return "", err
	}
	current, err := os.ReadDir(dataDir)
	if err != nil {
		return "", err
	}
	var moved []string
	undo := func() {
		for _, n := range moved {
			_ = os.Rename(filepath.Join(aside, n), filepath.Join(dataDir, n))
		}
	}
	for _, e := range current {
		if !inUpgradeBackup(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(dataDir, e.Name()), filepath.Join(aside, e.Name())); err != nil {
			undo()
			return "", fmt.Errorf("move %s aside: %w", e.Name(), err)
		}
		moved = append(moved, e.Name())
	}
	// 3. Move the saved state in.
	for i, n := range names {
		if err := os.Rename(filepath.Join(stage, n), filepath.Join(dataDir, n)); err != nil {
			for _, done := range names[:i] {
				_ = os.RemoveAll(filepath.Join(dataDir, done))
			}
			undo()
			return "", fmt.Errorf("restore %s: %w", n, err)
		}
	}
	return aside, PruneUpgradeBackups(dataDir, KeepUpgradeBackups)
}

// copyTree copies a file or directory (regular files only) and returns the
// number of files and bytes copied. bbolt databases (*.db) are checked after
// copying, since the server may be writing them: a torn copy is retried.
func copyTree(src, dst string) (int, int64, error) {
	fi, err := os.Lstat(src)
	if err != nil {
		return 0, 0, err
	}
	switch {
	case fi.IsDir():
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return 0, 0, err
		}
		matchOwner(dst, fi)
		entries, err := os.ReadDir(src)
		if err != nil {
			return 0, 0, err
		}
		var files int
		var bytes int64
		for _, e := range entries {
			n, b, err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()))
			if err != nil {
				return 0, 0, err
			}
			files += n
			bytes += b
		}
		return files, bytes, nil
	case !fi.Mode().IsRegular():
		return 0, 0, nil
	}
	var size int64
	isDB := isBolt(src)
	for attempt := 1; ; attempt++ {
		if size, err = copyFile(src, dst, fi.Mode().Perm()); err != nil {
			return 0, 0, err
		}
		matchOwner(dst, fi)
		if !isDB {
			break
		}
		err = checkBolt(dst)
		if err == nil {
			break
		}
		if attempt == 5 {
			return 0, 0, fmt.Errorf("%s changed while it was copied (%v); stop the server and try again", filepath.Base(src), err)
		}
		time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
	}
	return 1, size, nil
}

func copyFile(src, dst string, perm os.FileMode) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// isBolt reports whether path starts with a bbolt meta page (its magic number
// follows the 16-byte page header).
func isBolt(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [20]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return false
	}
	return binary.LittleEndian.Uint32(hdr[16:]) == 0xED0CDAED
}

// checkBolt opens a copied bbolt database and checks it page by page.
func checkBolt(path string) error {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	return db.View(func(tx *bolt.Tx) error {
		var first error
		for err := range tx.Check() { // drained: the checker runs until done
			if first == nil {
				first = err
			}
		}
		return first
	})
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// mkdirOwned creates dir (and parents) owned like the directory like.
func mkdirOwned(dir, like string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(like); err == nil {
		matchOwner(dir, fi)
		if parent := filepath.Dir(dir); parent != filepath.Clean(like) {
			matchOwner(parent, fi) // upgrade-backups itself
		}
	}
	return nil
}

// safeName keeps version strings usable as directory names.
func safeName(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == ' ' {
			return '_'
		}
		return r
	}, s)
}
