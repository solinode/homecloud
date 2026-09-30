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
//
// The native API and the AWS API (efs_aws.go) call the same functions below.
// A file system is a Docker volume; mount targets and access points are
// records (a mount target reserves a private IP in its subnet).

const (
	cFileSystems  = "efs_file_systems"
	cMountTargets = "efs_mount_targets"
	cAccessPoints = "efs_access_points"
)

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

	// Attributes recorded from the AWS API.
	CreationToken   string              `json:"creation_token,omitempty"`
	PerformanceMode string              `json:"performance_mode,omitempty"`
	ThroughputMode  string              `json:"throughput_mode,omitempty"`
	Provisioned     float64             `json:"provisioned_throughput,omitempty"`
	Encrypted       bool                `json:"encrypted,omitempty"`
	KmsKeyID        string              `json:"kms_key_id,omitempty"`
	AZName          string              `json:"az_name,omitempty"`
	Lifecycle       []map[string]string `json:"lifecycle,omitempty"`
	Backup          bool                `json:"backup,omitempty"`
	Policy          string              `json:"policy,omitempty"`
}

// MountTarget is the network endpoint of a file system in one subnet.
type MountTarget struct {
	ID             string    `json:"id"`
	FileSystemID   string    `json:"file_system_id"`
	SubnetID       string    `json:"subnet_id"`
	VpcID          string    `json:"vpc_id"`
	AZ             string    `json:"availability_zone"`
	IP             string    `json:"ip_address"`
	ENI            string    `json:"network_interface_id"`
	SecurityGroups []string  `json:"security_groups"`
	CreatedAt      time.Time `json:"created_at"`
}

// PosixUser is the identity an access point forces on its clients.
type PosixUser struct {
	Uid           int64   `json:"uid"`
	Gid           int64   `json:"gid"`
	SecondaryGids []int64 `json:"secondary_gids,omitempty"`
}

// CreationInfo owns the root directory of an access point when it is created.
type CreationInfo struct {
	OwnerUid    int64  `json:"owner_uid"`
	OwnerGid    int64  `json:"owner_gid"`
	Permissions string `json:"permissions"`
}

// AccessPoint is an application-specific entry point into a file system. The
// POSIX user and root directory are stored and reported; HomeCloud mounts the
// whole volume and does not enforce them.
type AccessPoint struct {
	ID           string        `json:"id"`
	ARN          string        `json:"arn"`
	FileSystemID string        `json:"file_system_id"`
	ClientToken  string        `json:"client_token,omitempty"`
	Name         string        `json:"name,omitempty"`
	PosixUser    *PosixUser    `json:"posix_user,omitempty"`
	RootPath     string        `json:"root_path"`
	CreationInfo *CreationInfo `json:"creation_info,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	Tags         core.Tags     `json:"tags,omitempty"`
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
	r.Handle("GET /api/v1/efs/file-systems/{id}/mount-targets", "elasticfilesystem:DescribeMountTargets", s.listMT, res)
	r.Handle("POST /api/v1/efs/file-systems/{id}/mount-targets", "elasticfilesystem:CreateMountTarget", s.createMT, res)
	r.Handle("DELETE /api/v1/efs/file-systems/{id}/mount-targets/{mt}", "elasticfilesystem:DeleteMountTarget", s.deleteMT, res)
	r.Handle("GET /api/v1/efs/file-systems/{id}/access-points", "elasticfilesystem:DescribeAccessPoints", s.listAP, res)
	r.Handle("POST /api/v1/efs/file-systems/{id}/access-points", "elasticfilesystem:CreateAccessPoint", s.createAP, res)
	r.Handle("DELETE /api/v1/efs/file-systems/{id}/access-points/{ap}", "elasticfilesystem:DeleteAccessPoint", s.deleteAP, res)
}

// ---- errors (the codes are AWS's, so both APIs report them) ----

func fsNotFound(id string) error {
	return core.Errf(http.StatusNotFound, "FileSystemNotFound", "File system '%s' does not exist.", id)
}
func mtNotFound(id string) error {
	return core.Errf(http.StatusNotFound, "MountTargetNotFound", "Mount target '%s' does not exist.", id)
}
func apNotFound(id string) error {
	return core.Errf(http.StatusNotFound, "AccessPointNotFound", "Access point '%s' does not exist.", id)
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

func (s *Service) mountTargets(fsID string) []MountTarget {
	var out []MountTarget
	for _, m := range store.List[MountTarget](s.env.Store, cMountTargets) {
		if fsID == "" || m.FileSystemID == fsID {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b MountTarget) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (s *Service) accessPoints(fsID string) []AccessPoint {
	var out []AccessPoint
	for _, a := range store.List[AccessPoint](s.env.Store, cAccessPoints) {
		if fsID == "" || a.FileSystemID == fsID {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b AccessPoint) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (s *Service) fsView(fs FileSystem) map[string]any {
	return map[string]any{"id": fs.ID, "arn": fs.ARN, "name": fs.Name, "state": fs.State, "read_only": fs.ReadOnly, "created_at": fs.CreatedAt,
		"size_bytes": fs.SizeBytes, "size_updated": fs.SizeUpdated, "tags": fs.Tags, "mounted_by": s.mountedBy(fs.ID),
		"mount_targets": s.mountTargets(fs.ID), "access_points": s.accessPoints(fs.ID)}
}

func (s *Service) listFS(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, fs := range store.List[FileSystem](s.env.Store, cFileSystems) {
		out = append(out, s.fsView(fs))
	}
	return out, nil
}

var fsName = regexp.MustCompile(`^[\w .+=@/-]{0,128}$`)

// fsInput is what creating a file system takes.
type fsInput struct {
	Name     string    `json:"name"`
	ReadOnly bool      `json:"read_only"`
	Tags     core.Tags `json:"tags"`

	// Set by the AWS API layer.
	Token         string  `json:"-"`
	Performance   string  `json:"-"`
	Throughput    string  `json:"-"`
	Provisioned   float64 `json:"-"`
	Encrypted     bool    `json:"-"`
	KmsKeyID      string  `json:"-"`
	AZName        string  `json:"-"`
	BackupEnabled bool    `json:"-"`
	skipNameCheck bool    // the AWS Name tag may hold characters the console name may not
}

func (s *Service) createFS(c *httpx.Ctx) (any, error) {
	var in fsInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	fs, _, err := s.createFileSystem(in)
	if err != nil {
		return nil, err
	}
	return s.fsView(fs), nil
}

// createFileSystem makes a file system. When in.Token names an existing one
// that file system is returned with created false.
func (s *Service) createFileSystem(in fsInput) (fs FileSystem, created bool, err error) {
	if !in.skipNameCheck && !fsName.MatchString(in.Name) {
		return fs, false, core.BadRequest("invalid file system name")
	}
	s.efsMu.Lock()
	defer s.efsMu.Unlock()
	if in.Token != "" {
		for _, x := range store.List[FileSystem](s.env.Store, cFileSystems) {
			if x.CreationToken == in.Token && x.State != "deleting" {
				return x, false, nil
			}
		}
	}
	id := core.NewID("fs")
	fs = FileSystem{ID: id, ARN: s.env.ARN("elasticfilesystem", "file-system/"+id), Name: in.Name, State: "available", ReadOnly: in.ReadOnly,
		CreatedAt: core.Now(), Tags: in.Tags, CreationToken: in.Token, PerformanceMode: in.Performance, ThroughputMode: in.Throughput,
		Provisioned: in.Provisioned, Encrypted: in.Encrypted, KmsKeyID: in.KmsKeyID, AZName: in.AZName, Backup: in.BackupEnabled}
	if err := s.env.Docker.CreateVolume(fsVolume(id), runtime.Labels("efs", id, nil)); err != nil {
		return fs, false, err
	}
	return fs, true, store.Put(s.env.Store, cFileSystems, id, fs)
}

func (s *Service) file(id string) (FileSystem, error) {
	fs, err := store.Get[FileSystem](s.env.Store, cFileSystems, id)
	if err != nil {
		return fs, fsNotFound(id)
	}
	return fs, nil
}

// getFS reports the file system, measuring its size at most once a minute.
func (s *Service) getFS(c *httpx.Ctx) (any, error) {
	fs, err := s.file(c.Param("id"))
	if err != nil {
		return nil, err
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
	return nil, s.deleteFileSystem(c.Param("id"))
}

// deleteFileSystem removes a file system; it must have no mount targets and
// no instance mounting it. Its access points go with it.
func (s *Service) deleteFileSystem(id string) error {
	s.efsMu.Lock()
	defer s.efsMu.Unlock()
	if !store.Has(s.env.Store, cFileSystems, id) {
		return fsNotFound(id)
	}
	if m := s.mountTargets(id); len(m) > 0 {
		return core.Errf(http.StatusConflict, "FileSystemInUse", "Cannot delete file system %s: it has %d mount target(s). Delete them first.", id, len(m))
	}
	if m := s.mountedBy(id); len(m) > 0 {
		return core.Errf(http.StatusConflict, "FileSystemInUse", "file system %s is mounted by %s", id, strings.Join(m, ", "))
	}
	if err := s.env.Docker.RemoveVolume(fsVolume(id)); err != nil {
		return err
	}
	for _, a := range s.accessPoints(id) {
		_ = store.Delete(s.env.Store, cAccessPoints, a.ID)
	}
	return store.Delete(s.env.Store, cFileSystems, id)
}

// editFS applies fn to a file system record.
func (s *Service) editFS(id string, fn func(*FileSystem) error) (FileSystem, error) {
	fs, err := store.Update(s.env.Store, cFileSystems, id, fn)
	if err == store.ErrNotFound {
		return fs, fsNotFound(id)
	}
	return fs, err
}

// ---- mount targets ----

type mtInput struct {
	SubnetID       string   `json:"subnet_id"`
	IPAddress      string   `json:"ip_address"`
	SecurityGroups []string `json:"security_groups"`
}

func (s *Service) listMT(c *httpx.Ctx) (any, error) {
	if _, err := s.file(c.Param("id")); err != nil {
		return nil, err
	}
	return append([]MountTarget{}, s.mountTargets(c.Param("id"))...), nil
}

func (s *Service) createMT(c *httpx.Ctx) (any, error) {
	var in mtInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.createMountTarget(c.Param("id"), in)
}

// createMountTarget adds a mount target in a subnet, one per availability zone.
func (s *Service) createMountTarget(fsID string, in mtInput) (MountTarget, error) {
	var mt MountTarget
	fs, err := s.file(fsID)
	if err != nil {
		return mt, err
	}
	if fs.State != "available" {
		return mt, core.Errf(http.StatusConflict, "IncorrectFileSystemLifeCycleState", "File system %s is %s", fsID, fs.State)
	}
	sn, err := s.vpc.GetSubnet(in.SubnetID)
	if err != nil {
		return mt, core.Errf(http.StatusBadRequest, "SubnetNotFound", "The subnet ID '%s' does not exist", in.SubnetID)
	}
	if fs.AZName != "" && fs.AZName != sn.AvailabilityZone {
		return mt, core.Errf(http.StatusBadRequest, "BadRequest", "One Zone file system %s only has mount targets in %s", fsID, fs.AZName)
	}
	sgs := in.SecurityGroups
	if len(sgs) == 0 {
		sgs = []string{s.vpc.DefaultSecurityGroup(sn.VpcID)}
	}
	for _, g := range sgs {
		sg, err := s.vpc.GetSecurityGroup(g)
		if err != nil {
			return mt, core.Errf(http.StatusBadRequest, "SecurityGroupNotFound", "The security group '%s' does not exist", g)
		}
		if sg.VpcID != sn.VpcID {
			return mt, core.Errf(http.StatusBadRequest, "SecurityGroupLimitExceeded", "The security group '%s' belongs to a different VPC than subnet %s", g, sn.ID)
		}
	}
	s.efsMu.Lock()
	defer s.efsMu.Unlock()
	for _, m := range s.mountTargets(fsID) {
		if m.AZ == sn.AvailabilityZone {
			return mt, core.Errf(http.StatusConflict, "MountTargetConflict", "mount target %s already exists in the availability zone %s", m.ID, m.AZ)
		}
	}
	id := core.NewID("fsmt")
	pl, err := s.vpc.Place(sn.ID, "efs-mt:"+id)
	if err != nil {
		return mt, core.Errf(http.StatusConflict, "NoFreeAddressesInSubnet", "%v", err)
	}
	if in.IPAddress != "" && in.IPAddress != pl.IP {
		s.vpc.Release("efs-mt:" + id)
		return mt, core.Errf(http.StatusBadRequest, "BadRequest", "HomeCloud allocates mount target addresses itself; omit IpAddress (next free address is %s)", pl.IP)
	}
	mt = MountTarget{ID: id, FileSystemID: fsID, SubnetID: sn.ID, VpcID: sn.VpcID, AZ: sn.AvailabilityZone, IP: pl.IP,
		ENI: core.NewID("eni"), SecurityGroups: sgs, CreatedAt: core.Now()}
	return mt, store.Put(s.env.Store, cMountTargets, id, mt)
}

func (s *Service) deleteMT(c *httpx.Ctx) (any, error) {
	mt, err := s.mountTarget(c.Param("mt"))
	if err != nil || mt.FileSystemID != c.Param("id") {
		return nil, mtNotFound(c.Param("mt"))
	}
	return nil, s.deleteMountTarget(mt.ID)
}

func (s *Service) mountTarget(id string) (MountTarget, error) {
	mt, err := store.Get[MountTarget](s.env.Store, cMountTargets, id)
	if err != nil {
		return mt, mtNotFound(id)
	}
	return mt, nil
}

func (s *Service) deleteMountTarget(id string) error {
	s.efsMu.Lock()
	defer s.efsMu.Unlock()
	if _, err := s.mountTarget(id); err != nil {
		return err
	}
	s.vpc.Release("efs-mt:" + id)
	return store.Delete(s.env.Store, cMountTargets, id)
}

// setMountTargetGroups replaces the security groups of a mount target.
func (s *Service) setMountTargetGroups(id string, sgs []string) error {
	mt, err := s.mountTarget(id)
	if err != nil {
		return err
	}
	if len(sgs) == 0 {
		return core.Errf(http.StatusBadRequest, "BadRequest", "SecurityGroups must contain at least one security group")
	}
	for _, g := range sgs {
		sg, err := s.vpc.GetSecurityGroup(g)
		if err != nil || sg.VpcID != mt.VpcID {
			return core.Errf(http.StatusBadRequest, "SecurityGroupNotFound", "The security group '%s' does not exist in VPC %s", g, mt.VpcID)
		}
	}
	_, err = store.Update(s.env.Store, cMountTargets, id, func(x *MountTarget) error { x.SecurityGroups = sgs; return nil })
	return err
}

// ---- access points ----

type apInput struct {
	ClientToken  string        `json:"client_token"`
	Tags         core.Tags     `json:"tags"`
	PosixUser    *PosixUser    `json:"posix_user"`
	RootPath     string        `json:"root_path"`
	CreationInfo *CreationInfo `json:"creation_info"`
}

func (s *Service) listAP(c *httpx.Ctx) (any, error) {
	if _, err := s.file(c.Param("id")); err != nil {
		return nil, err
	}
	return append([]AccessPoint{}, s.accessPoints(c.Param("id"))...), nil
}

func (s *Service) createAP(c *httpx.Ctx) (any, error) {
	var in apInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	ap, _, err := s.createAccessPoint(c.Param("id"), in)
	return ap, err
}

var permRe = regexp.MustCompile(`^[0-7]{3,4}$`)

// createAccessPoint adds an access point; a repeated ClientToken returns the
// existing one with created false.
func (s *Service) createAccessPoint(fsID string, in apInput) (ap AccessPoint, created bool, err error) {
	if _, err := s.file(fsID); err != nil {
		return ap, false, err
	}
	if in.RootPath == "" {
		in.RootPath = "/"
	}
	if !strings.HasPrefix(in.RootPath, "/") || len(in.RootPath) > 100 || strings.Contains(in.RootPath, "..") {
		return ap, false, core.Errf(http.StatusBadRequest, "BadRequest", "RootDirectory.Path must be an absolute path of up to 100 characters")
	}
	if ci := in.CreationInfo; ci != nil && !permRe.MatchString(ci.Permissions) {
		return ap, false, core.Errf(http.StatusBadRequest, "BadRequest", "CreationInfo.Permissions must be an octal mode such as 0755")
	}
	s.efsMu.Lock()
	defer s.efsMu.Unlock()
	if in.ClientToken != "" {
		for _, a := range s.accessPoints("") {
			if a.ClientToken == in.ClientToken {
				return a, false, nil
			}
		}
	}
	id := core.NewID("fsap")
	ap = AccessPoint{ID: id, ARN: s.env.ARN("elasticfilesystem", "access-point/"+id), FileSystemID: fsID, ClientToken: in.ClientToken, Name: in.Tags["Name"],
		PosixUser: in.PosixUser, RootPath: in.RootPath, CreationInfo: in.CreationInfo, CreatedAt: core.Now(), Tags: in.Tags}
	return ap, true, store.Put(s.env.Store, cAccessPoints, id, ap)
}

func (s *Service) accessPoint(id string) (AccessPoint, error) {
	ap, err := store.Get[AccessPoint](s.env.Store, cAccessPoints, id)
	if err != nil {
		return ap, apNotFound(id)
	}
	return ap, nil
}

func (s *Service) deleteAP(c *httpx.Ctx) (any, error) {
	ap, err := s.accessPoint(c.Param("ap"))
	if err != nil || ap.FileSystemID != c.Param("id") {
		return nil, apNotFound(c.Param("ap"))
	}
	return nil, s.deleteAccessPoint(ap.ID)
}

func (s *Service) deleteAccessPoint(id string) error {
	if _, err := s.accessPoint(id); err != nil {
		return err
	}
	return store.Delete(s.env.Store, cAccessPoints, id)
}

// ---- tags (file systems and access points) ----

// editEFSTags applies fn to the tags of the file system or access point with the given ID.
func (s *Service) editEFSTags(id string, fn func(core.Tags) core.Tags) (core.Tags, error) {
	switch {
	case strings.HasPrefix(id, "fsap-"):
		ap, err := s.accessPoint(id)
		if err != nil {
			return nil, err
		}
		t := fn(ap.Tags)
		_, err = store.Update(s.env.Store, cAccessPoints, id, func(x *AccessPoint) error { x.Tags, x.Name = t, t["Name"]; return nil })
		return t, err
	case strings.HasPrefix(id, "fs-"):
		fs, err := s.file(id)
		if err != nil {
			return nil, err
		}
		t := fn(fs.Tags)
		_, err = s.editFS(id, func(x *FileSystem) error {
			x.Tags = t
			if n, ok := t["Name"]; ok {
				x.Name = n
			} else if _, had := fs.Tags["Name"]; had {
				x.Name = ""
			}
			return nil
		})
		return t, err
	}
	return nil, core.Errf(http.StatusBadRequest, "BadRequest", "resource %q is not a file system or access point", id)
}
