// Package system implements installation-wide operations: backup and restore.
//
// A backup is a gzipped tar archive:
//
//	manifest.json          account, region, version, volumes and their labels
//	data/...               the data directory (state, keys, code, logs, ...)
//	volumes/<name>.json    a volume's labels
//	volumes/<name>.tar     each HomeCloud-managed Docker volume (databases, S3
//	                       objects, registry images, EBS volumes, file systems)
//
// Files that services hold open (bbolt databases) are captured through
// Snapshot functions; containers using a volume are paused while it is copied,
// so databases get a crash-consistent image.
package system

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
)

const (
	helperImage    = "alpine:3.20"
	manifestFormat = 1
)

// Manifest describes a backup.
type Manifest struct {
	Format    int               `json:"format"`
	Version   string            `json:"homecloud_version"`
	AccountID string            `json:"account_id"`
	Region    string            `json:"region"`
	CreatedAt time.Time         `json:"created_at"`
	Volumes   []VolumeEntry     `json:"volumes"`
	Skipped   map[string]string `json:"skipped,omitempty"` // volume -> reason
}

type VolumeEntry struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Size   int64             `json:"archive_bytes"`
}

// Backup writes backups of one installation.
type Backup struct {
	Cfg       core.Config
	Docker    *runtime.Docker
	AccountID string
	Version   string
	// OwnVolumesOnly skips managed volumes without an account label. Volumes
	// created before HomeCloud labeled accounts have none; since one Docker host
	// runs one installation, they are included by default.
	OwnVolumesOnly bool
	// Snapshots capture files that a service keeps open, by path relative to
	// the data directory (e.g. "dynamodb.db"); they replace a plain copy.
	Snapshots map[string]func(io.Writer) error
}

// excluded reports whether a data-directory path stays out of backups.
func excluded(rel string) bool {
	base := filepath.Base(rel)
	switch {
	case rel == "server.log", strings.HasSuffix(base, ".tmp"), strings.HasSuffix(base, ".migrate"),
		strings.HasPrefix(rel, "backup-legacy-region"), strings.HasPrefix(rel, "backups"):
		return true
	}
	return false
}

// Write streams a backup to w. With volumes=false only the data directory is saved.
func (b *Backup) Write(ctx context.Context, w io.Writer, volumes bool, logf func(string, ...any)) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	m := Manifest{Format: manifestFormat, Version: b.Version, AccountID: b.AccountID, Region: b.Cfg.Region, CreatedAt: core.Now(), Skipped: map[string]string{}}

	// Data directory.
	err := filepath.WalkDir(b.Cfg.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(b.Cfg.DataDir, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if excluded(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if snap := b.Snapshots[rel]; snap != nil {
			return addFunc(tw, "data/"+rel, snap)
		}
		return addFile(tw, "data/"+rel, p)
	})
	if err != nil {
		return fmt.Errorf("back up data directory: %w", err)
	}

	// Volumes.
	if volumes {
		vols, err := b.Docker.C.ListVolumes(docker.ListVolumesOptions{Context: ctx, Filters: map[string][]string{"label": {core.LabelManaged + "=true"}}})
		if err != nil {
			return err
		}
		sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
		for _, v := range vols {
			if acct := v.Labels[core.LabelAccount]; acct != b.AccountID && (acct != "" || b.OwnVolumesOnly) {
				continue // another installation's volume
			}
			lb, _ := json.Marshal(v.Labels)
			if err := addBytes(tw, "volumes/"+v.Name+".json", lb); err != nil {
				return err
			}
			n, err := b.addVolume(ctx, tw, v.Name)
			if err != nil {
				m.Skipped[v.Name] = err.Error()
				logf("backup: volume %s skipped: %v", v.Name, err)
				continue
			}
			m.Volumes = append(m.Volumes, VolumeEntry{Name: v.Name, Labels: v.Labels, Size: n})
		}
	}

	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := addBytes(tw, "manifest.json", mb); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// addVolume copies a volume through a stopped helper container, pausing the
// containers that use it meanwhile.
func (b *Backup) addVolume(ctx context.Context, tw *tar.Writer, name string) (int64, error) {
	var paused []string
	if cs, err := b.Docker.C.ListContainers(docker.ListContainersOptions{Filters: map[string][]string{"volume": {name}, "status": {"running"}}}); err == nil {
		for _, c := range cs {
			if b.Docker.C.PauseContainer(c.ID) == nil {
				paused = append(paused, c.ID)
			}
		}
	}
	defer func() {
		for _, id := range paused {
			_ = b.Docker.C.UnpauseContainer(id)
		}
	}()
	id, err := createHelper(ctx, b.Docker, name)
	if err != nil {
		return 0, err
	}
	defer func() { _ = b.Docker.Remove(id) }()

	// The archive API needs the size up front for our tar entry: spool to a temp file.
	tmp, err := os.CreateTemp("", "homecloud-volume-*.tar")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := b.Docker.C.DownloadFromContainer(id, docker.DownloadFromContainerOptions{Path: "/v/.", OutputStream: tmp, Context: ctx}); err != nil {
		return 0, err
	}
	for _, id := range paused {
		_ = b.Docker.C.UnpauseContainer(id)
	}
	paused = nil
	st, err := tmp.Stat()
	if err != nil {
		return 0, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	hdr := &tar.Header{Name: "volumes/" + name + ".tar", Mode: 0o600, Size: st.Size(), ModTime: time.Now()}
	if err := tw.WriteHeader(hdr); err != nil {
		return 0, err
	}
	_, err = io.Copy(tw, tmp)
	return st.Size(), err
}

// createHelper creates (without starting) a container with volume mounted at /v;
// Docker's archive API reads and writes the volume through it.
func createHelper(ctx context.Context, d *runtime.Docker, volume string) (string, error) {
	if err := d.EnsureImage(ctx, helperImage); err != nil {
		return "", err
	}
	c, err := d.C.CreateContainer(docker.CreateContainerOptions{
		Context:    ctx,
		Config:     &docker.Config{Image: helperImage, Cmd: []string{"true"}, Labels: map[string]string{"homecloud.helper": "backup"}},
		HostConfig: &docker.HostConfig{Mounts: []docker.HostMount{{Type: "volume", Source: volume, Target: "/v"}}},
	})
	if err != nil {
		return "", err
	}
	return c.ID, nil
}

func addFile(tw *tar.Writer, name, src string) error {
	f, err := os.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // removed while walking
		}
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(st.Mode().Perm()), Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	// Copy exactly Size bytes; a file growing meanwhile (logs) must not overrun the entry.
	_, err = io.CopyN(tw, f, st.Size())
	return err
}

func addBytes(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func addFunc(tw *tar.Writer, name string, fn func(io.Writer) error) error {
	tmp, err := os.CreateTemp("", "homecloud-snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := fn(tmp); err != nil {
		return fmt.Errorf("snapshot %s: %w", name, err)
	}
	st, err := tmp.Stat()
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: st.Size(), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, tmp)
	return err
}

// RestoreOptions control Restore.
type RestoreOptions struct {
	DataDir string
	// Force allows restoring over a non-empty data directory (moved aside to
	// <data-dir>.before-restore-<time>) and replacing existing volumes.
	Force bool
	// SkipVolumes restores only the data directory.
	SkipVolumes bool
	Logf        func(string, ...any)
}

// Restore unpacks a backup into a data directory and recreates its volumes.
// The server must not be running.
func Restore(ctx context.Context, d *runtime.Docker, r io.Reader, o RestoreOptions) (*Manifest, error) {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("not a HomeCloud backup (gzip): %w", err)
	}
	if entries, err := os.ReadDir(o.DataDir); err == nil && len(entries) > 0 {
		if !o.Force {
			return nil, fmt.Errorf("data directory %s is not empty; use --force to move it aside and restore", o.DataDir)
		}
		aside := strings.TrimRight(o.DataDir, string(os.PathSeparator)) + ".before-restore-" + time.Now().Format("20060102-150405")
		if err := os.Rename(o.DataDir, aside); err != nil {
			return nil, err
		}
		logf("moved the existing data directory to %s", aside)
	}
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	var m *Manifest
	labels := map[string]map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, fmt.Errorf("read backup: %w", err)
		}
		name := path.Clean(h.Name)
		if strings.HasPrefix(name, "..") || strings.HasPrefix(name, "/") {
			return m, fmt.Errorf("backup contains an unsafe path %q", h.Name)
		}
		switch {
		case name == "manifest.json":
			var mm Manifest
			if err := json.NewDecoder(tr).Decode(&mm); err != nil {
				return m, fmt.Errorf("manifest: %w", err)
			}
			m = &mm
		case strings.HasPrefix(name, "data/"):
			dst := filepath.Join(o.DataDir, filepath.FromSlash(strings.TrimPrefix(name, "data/")))
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return m, err
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode&0o777)|0o600)
			if err != nil {
				return m, err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return m, err
			}
			if err := f.Close(); err != nil {
				return m, err
			}
		case strings.HasPrefix(name, "volumes/") && strings.HasSuffix(name, ".json"):
			vol := strings.TrimSuffix(strings.TrimPrefix(name, "volumes/"), ".json")
			l := map[string]string{}
			if err := json.NewDecoder(tr).Decode(&l); err != nil {
				return m, fmt.Errorf("volume %s labels: %w", vol, err)
			}
			labels[vol] = l
		case strings.HasPrefix(name, "volumes/") && strings.HasSuffix(name, ".tar"):
			if o.SkipVolumes {
				continue
			}
			vol := strings.TrimSuffix(strings.TrimPrefix(name, "volumes/"), ".tar")
			if strings.ContainsAny(vol, "/\\") {
				return m, fmt.Errorf("backup contains an invalid volume name %q", vol)
			}
			if err := restoreVolume(ctx, d, vol, labels[vol], tr, o.Force); err != nil {
				return m, fmt.Errorf("volume %s: %w", vol, err)
			}
			logf("restored volume %s", vol)
		}
	}
	if m == nil {
		return nil, errors.New("backup has no manifest.json")
	}
	return m, nil
}

func restoreVolume(ctx context.Context, d *runtime.Docker, name string, labels map[string]string, r io.Reader, force bool) error {
	if v, err := d.C.InspectVolume(name); err == nil && v != nil {
		if !force {
			return fmt.Errorf("volume already exists (use --force to replace it)")
		}
		if err := d.RemoveVolume(name); err != nil {
			return fmt.Errorf("remove existing volume (is a container still using it?): %w", err)
		}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	labels[core.LabelManaged] = "true"
	if err := d.CreateVolume(name, labels); err != nil {
		return err
	}
	id, err := createHelper(ctx, d, name)
	if err != nil {
		return err
	}
	defer func() { _ = d.Remove(id) }()
	return d.C.UploadToContainer(id, docker.UploadToContainerOptions{Path: "/v", InputStream: r, Context: ctx})
}
