package ec2

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// EFS: shared file systems that any number of instances mount at once.

const cFileSystems = "efs_file_systems"

type FileSystem struct {
	ID          string    `json:"id"`
	ARN         string    `json:"arn"`
	Name        string    `json:"name"`
	State       string    `json:"state"`
	ReadOnly    bool      `json:"read_only"`
	CreatedAt   time.Time `json:"created_at"`
	SizeBytes   int64     `json:"size_bytes"`
	SizeUpdated time.Time `json:"size_updated,omitempty"`
	Tags        core.Tags `json:"tags,omitempty"`
}

// FSMount attaches a file system to an instance at launch.
type FSMount struct {
	FileSystemID string `json:"file_system_id"`
	MountPath    string `json:"mount_path"`
	ReadOnly     bool   `json:"read_only"`
}

func fsVolume(id string) string { return "hc-" + id }

func nzFS(m []FSMount) []FSMount {
	if m == nil {
		return []FSMount{}
	}
	return m
}

func (s *Service) efsRoutes(r *httpx.Router) {
	res := httpx.Res("arn:aws:elasticfilesystem:{region}:{account}:file-system/{id}")
	r.Handle("GET /api/v1/efs/file-systems", "elasticfilesystem:DescribeFileSystems", s.listFS)
	r.Handle("POST /api/v1/efs/file-systems", "elasticfilesystem:CreateFileSystem", s.createFS)
	r.Handle("GET /api/v1/efs/file-systems/{id}", "elasticfilesystem:DescribeFileSystems", s.getFS, res)
	r.Handle("DELETE /api/v1/efs/file-systems/{id}", "elasticfilesystem:DeleteFileSystem", s.deleteFS, res)
}

// mountedBy lists live instances that mount a file system.
func (s *Service) mountedBy(id string) []string {
	out := []string{}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.State == "terminated" {
			continue
		}
		if slices.ContainsFunc(i.FileSystems, func(m FSMount) bool { return m.FileSystemID == id }) {
			out = append(out, i.ID)
		}
	}
	return out
}

func (s *Service) fsView(fs FileSystem) map[string]any {
	return map[string]any{"id": fs.ID, "arn": fs.ARN, "name": fs.Name, "state": fs.State, "read_only": fs.ReadOnly, "created_at": fs.CreatedAt,
		"size_bytes": fs.SizeBytes, "size_updated": fs.SizeUpdated, "tags": fs.Tags, "mounted_by": s.mountedBy(fs.ID)}
}

func (s *Service) listFS(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, fs := range store.List[FileSystem](s.env.Store, cFileSystems) {
		out = append(out, s.fsView(fs))
	}
	return out, nil
}

var fsName = regexp.MustCompile(`^[\w .+=@/-]{0,128}$`)

func (s *Service) createFS(c *httpx.Ctx) (any, error) {
	var in struct {
		Name     string    `json:"name"`
		ReadOnly bool      `json:"read_only"`
		Tags     core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !fsName.MatchString(in.Name) {
		return nil, core.BadRequest("invalid file system name")
	}
	id := core.NewID("fs")
	fs := FileSystem{ID: id, ARN: s.env.ARN("elasticfilesystem", "file-system/"+id), Name: in.Name, State: "available", ReadOnly: in.ReadOnly, CreatedAt: core.Now(), Tags: in.Tags}
	if err := s.env.Docker.CreateVolume(fsVolume(id), runtime.Labels("efs", id, nil)); err != nil {
		return nil, err
	}
	return s.fsView(fs), store.Put(s.env.Store, cFileSystems, id, fs)
}

// getFS reports the file system, measuring its size at most once a minute.
func (s *Service) getFS(c *httpx.Ctx) (any, error) {
	fs, err := store.Get[FileSystem](s.env.Store, cFileSystems, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("file system", c.Param("id"))
	}
	if time.Since(fs.SizeUpdated) > time.Minute {
		if n, err := s.measure(c.R.Context(), fs.ID); err == nil {
			fs, _ = store.Update(s.env.Store, cFileSystems, fs.ID, func(x *FileSystem) error { x.SizeBytes, x.SizeUpdated = n, core.Now(); return nil })
		}
	}
	return s.fsView(fs), nil
}

func (s *Service) measure(ctx context.Context, id string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cid, err := s.env.Docker.Run(ctx, runtime.RunSpec{
		Image: "alpine:3.20", Cmd: []string{"du", "-sb", "/fs"}, Labels: runtime.Labels("efs", id+"-du", nil),
		Mounts: []runtime.Mount{{Volume: fsVolume(id), Target: "/fs", ReadOnly: true}}, Start: true,
	})
	if err != nil {
		return 0, err
	}
	defer s.env.Docker.Remove(cid)
	if _, err := s.env.Docker.C.WaitContainerWithContext(cid, ctx); err != nil {
		return 0, err
	}
	out, err := s.env.Docker.Logs(cid, 5, time.Time{})
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				return n, nil
			}
		}
	}
	return 0, nil
}

func (s *Service) deleteFS(c *httpx.Ctx) (any, error) {
	id := c.Param("id")
	if !store.Has(s.env.Store, cFileSystems, id) {
		return nil, core.NotFound("file system", id)
	}
	if m := s.mountedBy(id); len(m) > 0 {
		return nil, core.Errf(http.StatusConflict, "FileSystemInUse", "file system %s is mounted by %s", id, strings.Join(m, ", "))
	}
	if err := s.env.Docker.RemoveVolume(fsVolume(id)); err != nil {
		return nil, err
	}
	return nil, store.Delete(s.env.Store, cFileSystems, id)
}
