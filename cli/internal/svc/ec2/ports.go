package ec2

import (
	"fmt"
	"log"
	"slices"
	"sync"

	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Security group rules decide which container ports are published on the
// host. Docker cannot change the published ports of an existing container, so
// when a group's rules change (or an instance's groups do) each affected
// instance whose published ports no longer match is recreated from a snapshot
// of its disk (see recreate): the writable layer, volumes, private IP, ID and
// tags are kept, the processes restart. Ports that stay open keep their host
// port. Instances whose ports already match are left untouched, so rules that
// are recorded but not enforceable (other protocols, CIDRs) never restart
// anything.

var (
	portSyncMu  sync.Mutex
	portSyncing = map[string]bool{} // instance -> a sync loop is running
	portDirty   = map[string]bool{} // instance -> changed again while syncing
)

// portKey identifies a published port binding.
func portKey(p runtime.Port) string {
	proto := p.Protocol
	if proto == "" {
		proto = "tcp"
	}
	ip := p.HostIP
	if ip == "" {
		ip = "0.0.0.0"
	}
	return fmt.Sprintf("%d/%s@%s", p.ContainerPort, proto, ip)
}

// currentBindings returns the host bindings of an existing container, keyed like portKey.
func (s *Service) currentBindings(cid string) (map[string]int, bool) {
	c, err := s.env.Docker.Inspect(cid)
	if err != nil || c.HostConfig == nil {
		return nil, false
	}
	out := map[string]int{}
	for p, bs := range c.HostConfig.PortBindings {
		var n int
		var proto string
		if _, err := fmt.Sscanf(string(p), "%d/%s", &n, &proto); err != nil {
			continue
		}
		for _, b := range bs {
			hp := 0
			fmt.Sscanf(b.HostPort, "%d", &hp)
			ip := b.HostIP
			if ip == "" {
				ip = "0.0.0.0"
			}
			out[fmt.Sprintf("%d/%s@%s", n, proto, ip)] = hp
		}
	}
	return out, true
}

// portsFor is the port set an instance should publish, reusing the host
// ports of bindings it already has.
func (s *Service) portsFor(inst Instance) []runtime.Port {
	ports := s.vpc.PublishedPorts(inst.SecurityGroups)
	if inst.ContainerID == "" {
		return ports
	}
	if cur, ok := s.currentBindings(inst.ContainerID); ok {
		for i := range ports {
			if hp := cur[portKey(ports[i])]; hp > 0 {
				ports[i].HostPort = hp
			}
		}
	}
	return ports
}

// portsMatch reports whether the container already publishes exactly the wanted ports.
func (s *Service) portsMatch(inst Instance) bool {
	cur, ok := s.currentBindings(inst.ContainerID)
	if !ok {
		return true // cannot tell: do not disturb
	}
	want := s.vpc.PublishedPorts(inst.SecurityGroups)
	if len(want) != len(cur) {
		return false
	}
	for _, p := range want {
		if _, ok := cur[portKey(p)]; !ok {
			return false
		}
	}
	return true
}

// groupChanged applies a changed security group to the instances using it.
func (s *Service) groupChanged(sg string) {
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.State != "terminated" && slices.Contains(i.SecurityGroups, sg) {
			s.syncPortsAsync(i.ID)
		}
	}
}

// syncPortsAsync brings an instance's published ports in line with its
// groups in the background. Changes arriving meanwhile are coalesced.
func (s *Service) syncPortsAsync(id string) {
	portSyncMu.Lock()
	if portSyncing[id] {
		portDirty[id] = true
		portSyncMu.Unlock()
		return
	}
	portSyncing[id] = true
	portSyncMu.Unlock()
	go func() {
		for {
			s.syncPorts(id)
			portSyncMu.Lock()
			if !portDirty[id] {
				delete(portSyncing, id)
				portSyncMu.Unlock()
				return
			}
			delete(portDirty, id)
			portSyncMu.Unlock()
		}
	}()
}

// syncPorts recreates the instance's container when its published ports differ from its groups' rules.
func (s *Service) syncPorts(id string) {
	i, err := store.Get[Instance](s.env.Store, cInstances, id)
	if err != nil || i.ContainerID == "" || (i.State != "running" && i.State != "stopped") || isBusy(id) {
		return
	}
	if s.portsMatch(i) {
		return
	}
	if err := s.recreate(id); err != nil {
		log.Printf("ec2: apply security groups to %s: %v", id, err)
	}
}
