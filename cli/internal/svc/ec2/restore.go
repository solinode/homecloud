package ec2

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Instances whose container is gone (after `homecloud restore` onto a host that
// has the volumes but no containers) are recreated, stopped, from what a
// backup holds: a VM from its root volume (vmRecoverContainer), a container
// instance from its image with its EBS volumes and file systems mounted again.

var (
	recoverMu     sync.Mutex
	recoverFailed = map[string]time.Time{} // instance -> last failed recovery
)

// recoverRetry is how long sync waits before trying a failed recovery again.
const recoverRetry = time.Minute

// recoverMissing recreates the container of an instance whose container is
// missing. When that is not possible but some of its volumes are there, the
// instance is kept (stopped, with the reason) rather than terminated, which
// would delete them; it reports false only when the instance has nothing left
// to recover and may be terminated.
func (s *Service) recoverMissing(i Instance) (Instance, bool) {
	recoverMu.Lock()
	last, failed := recoverFailed[i.ID]
	recoverMu.Unlock()
	if failed && time.Since(last) < recoverRetry {
		return i, true
	}
	var ri Instance
	var ok bool
	if i.IsVM() {
		ri, ok = s.vmRecoverContainer(i)
	} else {
		ri, ok = s.recoverContainerInstance(i)
	}
	recoverMu.Lock()
	if ok {
		delete(recoverFailed, i.ID)
	} else {
		recoverFailed[i.ID] = time.Now()
	}
	recoverMu.Unlock()
	if ok {
		return ri, true
	}
	if !s.volumesRemain(i) {
		return i, false
	}
	const reason = "Server.InternalError: the instance's container is missing and could not be recreated yet; its volumes are kept"
	if i.State != "stopped" || i.StateReason != reason {
		i, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
			x.State, x.StateReason, x.PublicPorts = "stopped", reason, map[string]int{}
			return nil
		})
	}
	return i, true
}

// volumesRemain reports whether any EBS volume of the instance still exists.
func (s *Service) volumesRemain(i Instance) bool {
	for _, v := range i.Volumes {
		if _, err := s.env.Docker.C.InspectVolume(volumeName(v.VolumeID)); err == nil {
			return true
		}
	}
	return false
}

// recoverContainerInstance recreates the container of a container instance from
// its image, mounting its volumes and file systems at their paths, on its
// private IP. The container's own writable layer is not in backups, so changes
// made outside volumes are lost; the instance comes back stopped. It reports
// false when something it needs is missing (a volume, the network, the image).
func (s *Service) recoverContainerInstance(i Instance) (Instance, bool) {
	vmRecMu.Lock()
	if vmRec[i.ID] {
		vmRecMu.Unlock()
		return i, true // another call is on it
	}
	vmRec[i.ID] = true
	vmRecMu.Unlock()
	defer func() { vmRecMu.Lock(); delete(vmRec, i.ID); vmRecMu.Unlock() }()

	// Docker would create a missing named volume empty: require every one.
	for _, v := range i.Volumes {
		if _, err := s.env.Docker.C.InspectVolume(volumeName(v.VolumeID)); err != nil {
			return i, false
		}
	}
	for _, m := range i.FileSystems {
		if _, err := s.env.Docker.C.InspectVolume(fsVolume(m.FileSystemID)); err != nil {
			return i, false
		}
	}
	v, err := s.vpc.GetVPC(i.VpcID)
	if err != nil {
		return i, false
	}
	if _, err := s.env.Docker.C.NetworkInfo(v.Network); err != nil {
		return i, false
	}
	if i.RootImage != "" {
		if _, err := s.env.Docker.C.InspectImage(i.RootImage); err != nil {
			i.RootImage = "" // the disk snapshot image is not in backups: start from the AMI
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cid, err := s.env.Docker.Run(ctx, s.runSpec(i, v.Network))
	if err != nil {
		log.Printf("ec2: recreate the container of %s: %v", i.ID, err)
		return i, false
	}
	files, _ := s.bootFiles(i)
	if err := s.env.Docker.CopyIn(ctx, cid, "/", files, 0o644); err != nil {
		log.Printf("ec2: recreate the container of %s: %v", i.ID, err)
		_ = s.env.Docker.Remove(cid)
		return i, false
	}
	out, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		x.ContainerID, x.RootImage, x.PublicPorts = cid, i.RootImage, map[string]int{}
		x.State, x.StateReason = "stopped", "Server.Restore: the instance's container was recreated from its image and volumes"
		return nil
	})
	if err != nil {
		_ = s.env.Docker.Remove(cid)
		return i, false
	}
	s.vpc.FirewallChanged()
	log.Printf("ec2: %s: recreated its container from its image and volumes (stopped; start it to boot)", i.ID)
	return out, true
}
