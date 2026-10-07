package ec2

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

// EBS volume attachment and snapshots.
//
// Volumes are Docker volumes mounted into the instance at a directory
// (/mnt/sdf for device /dev/sdf); containers have no block devices. Docker
// cannot add or remove mounts of an existing container, so attaching or
// detaching a volume recreates the instance's container from a snapshot of
// its disk (docker commit), with the same name, address and settings: running
// processes restart, as after a reboot. Snapshots are copies of the volume's
// files in a Docker volume of their own.

const (
	cSnapshots     = "ec2_snapshots"
	cModifications = "ec2_volume_modifications"
	helperImage    = "alpine:3.20"
)

type Snapshot struct {
	ID          string     `json:"id"`
	VolumeID    string     `json:"volume_id"`
	VolumeSize  int        `json:"volume_size"`
	State       string     `json:"state"` // pending | completed | error
	Message     string     `json:"message,omitempty"`
	Description string     `json:"description,omitempty"`
	StartTime   time.Time  `json:"start_time"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Encrypted   bool       `json:"encrypted,omitempty"`
	Tags        core.Tags  `json:"tags,omitempty"`
}

// VolumeModification is the record DescribeVolumesModifications returns.
type VolumeModification struct {
	VolumeID                             string    `json:"volume_id"`
	StartTime                            time.Time `json:"start_time"`
	OriginalSize, TargetSize             int
	OriginalType, TargetType             string
	OriginalIops, TargetIops             int
	OriginalThroughput, TargetThroughput int
}

func snapVolume(id string) string { return "hc-" + id }

func (s *Service) snapshot(id string) (Snapshot, error) {
	sn, err := store.Get[Snapshot](s.env.Store, cSnapshots, id)
	if err != nil {
		return sn, core.Errf(http.StatusBadRequest, "InvalidSnapshot.NotFound", "The snapshot '%s' does not exist.", id)
	}
	return sn, nil
}

// helper runs a short-lived utility container with the given mounts.
func (s *Service) helper(ctx context.Context, mounts []docker.HostMount, cmd []string) (string, error) {
	if err := s.env.Docker.EnsureImage(ctx, helperImage); err != nil {
		return "", err
	}
	c, err := s.env.Docker.C.CreateContainer(docker.CreateContainerOptions{Context: ctx,
		Config:     &docker.Config{Image: helperImage, Cmd: cmd, Labels: runtime.Labels("ec2", "helper", nil)},
		HostConfig: &docker.HostConfig{Mounts: mounts, NetworkMode: "none"},
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = s.env.Docker.Remove(c.ID) }()
	if err := s.env.Docker.C.StartContainerWithContext(c.ID, nil, ctx); err != nil {
		return "", err
	}
	code, err := s.env.Docker.C.WaitContainerWithContext(c.ID, ctx)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	_ = s.env.Docker.C.Logs(docker.LogsOptions{Context: ctx, Container: c.ID, OutputStream: &out, ErrorStream: &out, Stdout: true, Stderr: true})
	if code != 0 {
		return out.String(), fmt.Errorf("exit %d: %s", code, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// copyVolume copies every file of one Docker volume into another.
func (s *Service) copyVolume(ctx context.Context, src, dst string) error {
	_, err := s.helper(ctx, []docker.HostMount{{Type: "volume", Source: src, Target: "/src", ReadOnly: true}, {Type: "volume", Source: dst, Target: "/dst"}},
		[]string{"cp", "-a", "/src/.", "/dst/"})
	return err
}

// CreateSnapshot starts copying a volume; the snapshot is "pending" until done.
func (s *Service) CreateSnapshot(volumeID, description string, tags core.Tags) (Snapshot, error) {
	v, err := store.Get[Volume](s.env.Store, cVolumes, volumeID)
	if err != nil {
		return Snapshot{}, core.NotFound("volume", volumeID)
	}
	if v.State == "creating" || v.State == "error" {
		return Snapshot{}, core.Errf(http.StatusBadRequest, "IncorrectState", "volume %s is %s", v.ID, v.State)
	}
	sn := Snapshot{ID: core.NewID("snap"), VolumeID: v.ID, VolumeSize: v.SizeGB, State: "pending", Description: description,
		StartTime: core.Now(), Encrypted: v.Encrypted, Tags: tags}
	if err := s.env.Docker.CreateVolume(snapVolume(sn.ID), runtime.Labels("ebs-snapshot", sn.ID, nil)); err != nil {
		return sn, err
	}
	if err := store.Put(s.env.Store, cSnapshots, sn.ID, sn); err != nil {
		return sn, err
	}
	go func() {
		defer core.Recover("snapshot " + sn.ID)
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		err := s.snapshotCopy(ctx, v, snapVolume(sn.ID))
		_, _ = store.Update(s.env.Store, cSnapshots, sn.ID, func(x *Snapshot) error {
			now := core.Now()
			x.CompletedAt = &now
			if err != nil {
				x.State, x.Message = "error", err.Error()
			} else {
				x.State = "completed"
			}
			return nil
		})
	}()
	return sn, nil
}

// DeleteSnapshot removes a snapshot and its data.
func (s *Service) DeleteSnapshot(id string) error {
	sn, err := s.snapshot(id)
	if err != nil {
		return err
	}
	for _, v := range store.List[Volume](s.env.Store, cVolumes) {
		if v.SnapshotID == sn.ID && v.State == "creating" {
			return core.Errf(http.StatusBadRequest, "InvalidSnapshot.InUse", "snapshot %s is being restored to %s", sn.ID, v.ID)
		}
	}
	if err := s.env.Docker.RemoveVolume(snapVolume(sn.ID)); err != nil {
		return err
	}
	return store.Delete(s.env.Store, cSnapshots, sn.ID)
}

// ModifyVolume changes a volume's recorded size, type and performance.
// Docker volumes have no size limit, so nothing is resized on disk.
func (s *Service) ModifyVolume(id string, size int, typ string, iops, throughput int) (VolumeModification, error) {
	var mod VolumeModification
	v, err := store.Update(s.env.Store, cVolumes, id, func(x *Volume) error {
		mod = VolumeModification{VolumeID: x.ID, StartTime: core.Now(), OriginalSize: x.SizeGB, OriginalType: x.VolumeType,
			OriginalIops: x.Iops, OriginalThroughput: x.Throughput}
		if size != 0 {
			if size < x.SizeGB {
				return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "new size %d GiB is smaller than the current size %d GiB", size, x.SizeGB)
			}
			x.SizeGB = size
		}
		if typ != "" {
			x.VolumeType = typ
		}
		if iops != 0 {
			x.Iops = iops
		}
		if throughput != 0 {
			x.Throughput = throughput
		}
		mod.TargetSize, mod.TargetType, mod.TargetIops, mod.TargetThroughput = x.SizeGB, x.VolumeType, x.Iops, x.Throughput
		return nil
	})
	if err == store.ErrNotFound {
		return mod, core.NotFound("volume", id)
	}
	if err != nil {
		return mod, err
	}
	_ = v
	return mod, store.Put(s.env.Store, cModifications, mod.VolumeID, mod)
}

var deviceRE = regexp.MustCompile(`^/dev/(sd[a-z][0-9]*|xvd[a-z]+[0-9]*|nvme[0-9]+n[0-9]+|hd[a-z])$`)

// AttachVolume attaches an available volume to an instance as device. The
// volume is "attaching" until the instance's container has been recreated
// with the new mount.
func (s *Service) AttachVolume(volumeID, instanceID, device string) (Volume, error) {
	if !deviceRE.MatchString(device) {
		return Volume{}, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "Value (%s) for parameter device is invalid.", device)
	}
	i, err := s.get(instanceID)
	if err != nil {
		return Volume{}, err
	}
	if i.State != "running" && i.State != "stopped" {
		return Volume{}, core.Errf(http.StatusBadRequest, "IncorrectState", "Instance '%s' is not 'running' or 'stopped'.", i.ID)
	}
	if i.IsVM() && (device == "/dev/xvda" || device == "/dev/sda1" || device == "/dev/sda") {
		return Volume{}, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "Value (%s) for parameter device is invalid: it is the root device of a VM instance.", device)
	}
	for _, a := range i.Volumes {
		if a.Device == device {
			return Volume{}, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "Attachment point %s is already in use", device)
		}
	}
	now := core.Now()
	v, err := store.Update(s.env.Store, cVolumes, volumeID, func(x *Volume) error {
		if x.State != "available" {
			return core.Errf(http.StatusBadRequest, "VolumeInUse", "vol '%s' is %s", x.ID, x.State)
		}
		if x.AvailabilityZone != i.AvailabilityZone {
			return core.Errf(http.StatusBadRequest, "InvalidVolume.ZoneMismatch", "The volume '%s' is not in the same availability zone as instance '%s'", x.ID, i.ID)
		}
		x.State, x.AttachedTo, x.MountPath, x.Device, x.AttachTime, x.DeleteOnTermination = "in-use", i.ID, devicePath(device), device, now, false
		x.AttachState = "attaching"
		return nil
	})
	if err == store.ErrNotFound {
		return v, core.NotFound("volume", volumeID)
	}
	if err != nil {
		return v, err
	}
	if _, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		x.Volumes = append(x.Volumes, VolumeAttachment{VolumeID: v.ID, MountPath: devicePath(device), Device: device, AttachTime: now})
		return nil
	}); err != nil {
		s.releaseVolumes(Instance{Volumes: []VolumeAttachment{{VolumeID: v.ID}}})
		return v, err
	}
	go func() {
		defer core.Recover("attach " + v.ID)
		state := "attached"
		if err := s.recreate(i.ID); err != nil {
			log.Printf("ec2: attach %s to %s: %v", v.ID, i.ID, err)
			// Roll back the attachment.
			_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
				x.Volumes = slices.DeleteFunc(x.Volumes, func(a VolumeAttachment) bool { return a.VolumeID == v.ID })
				return nil
			})
			s.releaseVolumes(Instance{Volumes: []VolumeAttachment{{VolumeID: v.ID}}})
			return
		}
		_, _ = store.Update(s.env.Store, cVolumes, v.ID, func(x *Volume) error {
			if x.AttachedTo == i.ID {
				x.AttachState = state
			}
			return nil
		})
	}()
	return v, nil
}

// DetachVolume detaches a volume; the volume is "in-use"/"detaching" until the
// instance's container has been recreated without it.
func (s *Service) DetachVolume(volumeID, instanceID, device string, force bool) (Volume, error) {
	v, err := store.Get[Volume](s.env.Store, cVolumes, volumeID)
	if err != nil {
		return v, core.NotFound("volume", volumeID)
	}
	if v.AttachedTo == "" {
		return v, core.Errf(http.StatusBadRequest, "IncorrectState", "Volume '%s' is in the 'available' state.", v.ID)
	}
	if (instanceID != "" && instanceID != v.AttachedTo) || (device != "" && v.Device != "" && device != v.Device) {
		return v, core.Errf(http.StatusBadRequest, "InvalidAttachment.NotFound", "Volume '%s' is not attached to that instance and device", v.ID)
	}
	i, err := s.get(v.AttachedTo)
	if err != nil {
		return v, err
	}
	if root, ok := vmRoot(i); ok && root.VolumeID == v.ID {
		return v, core.Errf(http.StatusBadRequest, "OperationNotPermitted", "Volume '%s' is the root volume of instance %s; terminate the instance to release it.", v.ID, i.ID)
	}
	v, err = store.Update(s.env.Store, cVolumes, v.ID, func(x *Volume) error { x.AttachState = "detaching"; return nil })
	if err != nil {
		return v, err
	}
	go func() {
		defer core.Recover("detach " + v.ID)
		_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
			x.Volumes = slices.DeleteFunc(x.Volumes, func(a VolumeAttachment) bool { return a.VolumeID == v.ID })
			return nil
		})
		if i.State != "terminated" && i.ContainerID != "" {
			if err := s.recreate(i.ID); err != nil {
				log.Printf("ec2: detach %s from %s: %v", v.ID, i.ID, err)
			}
		}
		_, _ = store.Update(s.env.Store, cVolumes, v.ID, func(x *Volume) error {
			x.State, x.AttachedTo, x.MountPath, x.Device, x.AttachTime, x.DeleteOnTermination, x.AttachState = "available", "", "", "", time.Time{}, false, ""
			return nil
		})
	}()
	return v, nil
}

// SetDeleteOnTermination changes an attachment's DeleteOnTermination flag.
func (s *Service) SetDeleteOnTermination(instanceID, device string, del bool) error {
	var volID string
	_, err := store.Update(s.env.Store, cInstances, instanceID, func(x *Instance) error {
		for k := range x.Volumes {
			if x.Volumes[k].Device == device {
				x.Volumes[k].DeleteOnTermination = del
				volID = x.Volumes[k].VolumeID
				return nil
			}
		}
		return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "no volume is attached at %s", device)
	})
	if err == store.ErrNotFound {
		return core.NotFound("instance", instanceID)
	}
	if err != nil {
		return err
	}
	_, _ = store.Update(s.env.Store, cVolumes, volID, func(x *Volume) error { x.DeleteOnTermination = del; return nil })
	return nil
}

var (
	recreateMu sync.Mutex
	busyMu     sync.Mutex
	busy       = map[string]bool{}
)

func isBusy(id string) bool {
	busyMu.Lock()
	defer busyMu.Unlock()
	return busy[id]
}

func setBusy(id string, b bool) {
	busyMu.Lock()
	defer busyMu.Unlock()
	if b {
		busy[id] = true
	} else {
		delete(busy, id)
	}
}

// recreate replaces an instance's container with one built from a snapshot
// of its disk and the instance's current settings (volumes, security groups).
// A running instance is restarted.
func (s *Service) recreate(id string) error {
	recreateMu.Lock()
	defer recreateMu.Unlock()
	setBusy(id, true)
	defer setBusy(id, false)
	i, err := store.Get[Instance](s.env.Store, cInstances, id)
	if err != nil {
		return err
	}
	if i.ContainerID == "" || i.State == "terminated" {
		return nil
	}
	if i.IsVM() {
		return s.rebuildVM(i)
	}
	v, err := s.vpc.GetVPC(i.VpcID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	running := s.env.Docker.State(i.ContainerID) == "running"
	if running {
		if err := s.env.Docker.Stop(i.ContainerID, 10); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	restore := func() {
		if running {
			_ = s.env.Docker.Start(i.ContainerID)
			s.metadataRoute(ctx, i.ContainerID, i.VpcID)
		}
	}
	tag := "homecloud/instance-disk:" + i.ID + "-" + core.RandHex(6)
	repo, t, _ := strings.Cut(tag, ":")
	if _, err := s.env.Docker.C.CommitContainer(docker.CommitContainerOptions{Context: ctx, Container: i.ContainerID, Repository: repo, Tag: t,
		Run: &docker.Config{Labels: runtime.Labels("ec2", i.ID, nil)}}); err != nil {
		restore()
		return fmt.Errorf("snapshot disk: %w", err)
	}
	name := svc.ContainerName("ec2", i.ID)
	if err := s.env.Docker.C.RenameContainer(docker.RenameContainerOptions{ID: i.ContainerID, Name: name + "-old", Context: ctx}); err != nil {
		_ = s.env.Docker.C.RemoveImage(tag)
		restore()
		return fmt.Errorf("rename: %w", err)
	}
	i.RootImage = tag
	cid, err := s.env.Docker.Run(ctx, s.runSpec(i, v.Network))
	if err != nil {
		_ = s.env.Docker.C.RenameContainer(docker.RenameContainerOptions{ID: i.ContainerID, Name: name, Context: ctx})
		_ = s.env.Docker.C.RemoveImage(tag)
		restore()
		return fmt.Errorf("recreate: %w", err)
	}
	_ = s.env.Docker.Remove(i.ContainerID)
	if running {
		if err := s.env.Docker.Start(cid); err != nil {
			log.Printf("ec2: start recreated %s: %v", i.ID, err)
		} else {
			s.metadataRoute(ctx, cid, i.VpcID)
			s.vpc.ProtectNow(ctx, s.member(i, cid))
		}
	}
	old := ""
	_, err = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		old = x.RootImage
		x.ContainerID, x.RootImage = cid, tag
		if running {
			x.PublicPorts = s.env.Docker.PublishedPorts(cid)
		}
		return nil
	})
	if old != "" && old != tag {
		_ = s.env.Docker.C.RemoveImage(old)
	}
	return err
}
