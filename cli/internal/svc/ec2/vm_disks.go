package ec2

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2/vm"
)

// VM disks.
//
// Every EBS volume of a VM instance is a Docker volume mounted into the VM
// container. The root volume (EBS device /dev/xvda, mounted at /vm) holds
// disk.qcow2, an overlay on the cached cloud image (or a standalone copy of an
// image made from an instance). Any other volume is mounted at /disks/<volume
// ID> and holds disk.img, a sparse raw image of the volume's size that the
// guest sees as a virtio disk: /dev/vdb, /dev/vdc, ... in the order of the EBS
// device names (/dev/sdf before /dev/sdg), and always as
// /dev/disk/by-id/virtio-<first 20 characters of the volume ID>.
//
// Docker cannot add a mount to a running container, so attaching or detaching
// a volume rebuilds the VM container (vm.go rebuildVM): the guest is shut down
// gracefully and boots again with the new set of disks. There is no hot-plug.
//
// Snapshots, images and backups of a root volume are flattened (qemu-img
// convert) into a standalone qcow2 so that they do not depend on the cached
// cloud image. Other volumes are copied as they are.

// vmRoot is the root volume attachment of a VM instance.
func vmRoot(i Instance) (VolumeAttachment, bool) {
	for _, v := range i.Volumes {
		if v.MountPath == vm.DiskDir {
			return v, true
		}
	}
	return VolumeAttachment{}, false
}

// vmDataDisks are the extra volumes of a VM instance in guest order: by EBS
// device name, then volume ID.
func vmDataDisks(i Instance) []VolumeAttachment {
	var out []VolumeAttachment
	for _, v := range i.Volumes {
		if v.MountPath != vm.DiskDir {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b VolumeAttachment) int {
		if c := strings.Compare(a.Device, b.Device); c != 0 {
			return c
		}
		return strings.Compare(a.VolumeID, b.VolumeID)
	})
	return out
}

// vmExtraVolumes names and places the extra volumes of a VM launch: each is a
// virtio disk known by its EBS device (/dev/sdf, /dev/sdg, ... when the caller
// names none) and mounted in the VM container under /disks by volume ID. A
// mount path given by the caller is replaced: one at /vm would be taken for
// the root disk, whose qcow2 QEMU trusts.
func vmExtraVolumes(vols []VolumeSpec) ([]VolumeSpec, error) {
	out := make([]VolumeSpec, len(vols))
	seen := map[string]bool{}
	for n, v := range vols {
		if v.Device == "" {
			v.Device = fmt.Sprintf("/dev/sd%c", 'f'+n)
		}
		if !deviceRE.MatchString(v.Device) || v.Device == "/dev/xvda" || v.Device == "/dev/sda1" || v.Device == "/dev/sda" {
			return nil, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "Value (%s) for parameter device is invalid for an extra volume of a VM instance.", v.Device)
		}
		if seen[v.Device] {
			return nil, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "Attachment point %s is listed twice", v.Device)
		}
		seen[v.Device] = true
		v.MountPath = devicePath(v.Device)
		out[n] = v
	}
	return out, nil
}

// vmFlatten copies the Docker volume src into the empty volume dst: a root
// volume becomes a standalone qcow2, any other volume is copied as it is.
func (s *Service) vmFlatten(ctx context.Context, src, dst string) error {
	runner, err := s.vmRunner(ctx)
	if err != nil {
		return err
	}
	_, err = s.env.Docker.RunOnce(ctx, vmFlattenSpec(runner, src, s.vmImageVolume(), dst))
	return err
}

// vmFlattenSpec is the helper container of vmFlatten. It reads disks that
// someone other than HomeCloud may have written (hc-vm-checkdisk vets them),
// so it gets no network: qemu-img must never be able to reach the host's
// services or the provider's metadata service on anyone's behalf.
func vmFlattenSpec(runner, src, images, dst string) runtime.RunSpec {
	return runtime.RunSpec{
		Image: runner, Entrypoint: []string{"/usr/local/bin/hc-vm-flatten"}, Cmd: []string{"/src", "/dst"},
		Network: "none",
		Mounts: []runtime.Mount{
			{Volume: src, Target: "/src", ReadOnly: true},
			{Volume: images, Target: vm.ImagesDir, ReadOnly: true},
			{Volume: dst, Target: "/dst"},
		},
	}
}

// snapshotCopy fills a snapshot's volume from a volume.
func (s *Service) snapshotCopy(ctx context.Context, v Volume, dst string) error {
	if v.VMRoot {
		return s.vmFlatten(ctx, volumeName(v.ID), dst)
	}
	return s.copyVolume(ctx, volumeName(v.ID), dst)
}

func amiVolume(id string) string { return "hc-" + id }

// createVMImage captures a VM instance's root disk as an AMI: a flattened
// copy in a Docker volume of its own, so the image does not depend on the
// instance, its volume or the cloud image cache. Launching it copies the disk
// again into the new instance's root volume.
func (s *Service) createVMImage(i Instance, name, description string, tags core.Tags) (Image, error) {
	if i.State != "running" && i.State != "stopped" {
		return Image{}, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance %s is %s", i.ID, i.State)
	}
	root, ok := vmRoot(i)
	if !ok {
		return Image{}, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance %s has no root volume", i.ID)
	}
	if name == "" {
		name = i.ID + "-image"
	}
	for _, im := range store.List[Image](s.env.Store, cImages) {
		if im.Name == name {
			return Image{}, core.Errf(http.StatusConflict, "InvalidAMIName.Duplicate", "AMI name %s is already in use by AMI %s", name, im.ID)
		}
	}
	arch, _ := vm.ArchOf(hostArch(s))
	id := core.NewID("ami")
	vol := amiVolume(id)
	if err := s.env.Docker.CreateVolume(vol, runtime.Labels("ami", id, nil)); err != nil {
		return Image{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	// A running guest's disk is read while it is open (crash-consistent);
	// stop the instance for a clean image.
	if err := s.vmFlatten(ctx, volumeName(root.VolumeID), vol); err != nil {
		_ = s.env.Docker.RemoveVolume(vol)
		return Image{}, fmt.Errorf("capture image: %w", err)
	}
	im := Image{ID: id, Name: name, Description: description, Platform: "linux", Owner: s.env.AccountID, State: "available",
		CreatedAt: core.Now().Format(time.RFC3339), SourceInstance: i.ID, Tags: tags,
		VMDisk: vol, VMUser: i.VMUser, VMArch: string(arch), VMDiskGB: i.VMDiskGB}
	if err := store.Put(s.env.Store, cImages, im.ID, im); err != nil {
		_ = s.env.Docker.RemoveVolume(vol)
		return Image{}, err
	}
	return im.normalized(), nil
}

// FlattenForBackup gives homecloud backup a standalone copy of a VM root
// volume (the volume's own file is an overlay on the cloud image cache, which a
// backup leaves out). It returns the name of a temporary volume to archive in
// its place and a function that removes it; "" for any other volume.
func (s *Service) FlattenForBackup(ctx context.Context, volume string, labels map[string]string) (string, func(), error) {
	if labels[vm.LabelRoot] != "true" {
		return "", nil, nil
	}
	tmp := "hc-tmp-flat-" + core.RandHex(6)
	// Not labelled as managed: a leftover must never be picked up by a later backup.
	if err := s.env.Docker.CreateVolume(tmp, map[string]string{"homecloud.helper": "backup"}); err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = s.env.Docker.RemoveVolume(tmp) }
	if err := s.vmFlatten(ctx, volume, tmp); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("flatten %s: %w", volume, err)
	}
	return tmp, cleanup, nil
}

var (
	vmRecMu sync.Mutex
	vmRec   = map[string]bool{}
)

// vmRecoverContainer recreates the VM container of an instance whose
// container is gone but whose root volume is there, as after homecloud restore
// on a host that has the volumes but no containers. The instance comes back
// stopped: starting it boots the restored disk. It reports false when there is
// nothing to recover from (the volume or the network is missing too).
func (s *Service) vmRecoverContainer(i Instance) (Instance, bool) {
	vmRecMu.Lock()
	if vmRec[i.ID] {
		vmRecMu.Unlock()
		return i, true // another call is on it
	}
	vmRec[i.ID] = true
	vmRecMu.Unlock()
	defer func() { vmRecMu.Lock(); delete(vmRec, i.ID); vmRecMu.Unlock() }()

	root, ok := vmRoot(i)
	if !ok {
		return i, false
	}
	if _, err := s.env.Docker.C.InspectVolume(volumeName(root.VolumeID)); err != nil {
		return i, false
	}
	v, err := s.vpc.GetVPC(i.VpcID)
	if err != nil {
		return i, false
	}
	if _, err := s.env.Docker.C.NetworkInfo(v.Network); err != nil {
		return i, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	if _, err := s.vmRunner(ctx); err != nil { // the runner image may not exist on this host yet
		log.Printf("ec2: recreate the virtual machine container of %s: %v", i.ID, err)
		return i, false
	}
	cid, err := s.env.Docker.Run(ctx, s.vmRunSpec(i, v.Network))
	if err != nil {
		log.Printf("ec2: recreate the virtual machine container of %s: %v", i.ID, err)
		return i, false
	}
	if err := s.env.Docker.CopyIn(ctx, cid, "/", s.vmSeed(i), 0o644); err != nil {
		_ = s.env.Docker.Remove(cid)
		return i, false
	}
	out, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		x.ContainerID, x.State, x.StateReason, x.PublicPorts = cid, "stopped", "Server.Restore: the virtual machine's container was recreated from its disk", map[string]int{}
		return nil
	})
	if err != nil {
		_ = s.env.Docker.Remove(cid)
		return i, false
	}
	log.Printf("ec2: %s: recreated its virtual machine container from the root volume (stopped; start it to boot)", i.ID)
	return out, true
}
