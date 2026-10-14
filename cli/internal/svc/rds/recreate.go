package rds

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const (
	// reasonGone is the status reason of an instance whose container vanished.
	reasonGone = "the database container disappeared"
	// reasonRecreateFailed prefixes the status reason when recreating a container failed.
	reasonRecreateFailed = "could not recreate the container: "
	// reasonRecreating is the status reason while a container is recreated.
	reasonRecreating = "recreating the container from the data volume"
)

// recoverContainer recreates, in the background, the container of an instance
// whose container is gone but whose data volume is there, as after `homecloud
// restore` onto a host that has the volumes but no containers. The new
// container has the instance's engine, version, class, address, aliases,
// password and published port; an instance that was stopped stays stopped.
// It reports whether it took the instance over.
func (s *Service) recoverContainer(i Instance) bool {
	switch {
	case slices.Contains([]string{"available", "stopped", "starting", "stopping", "rebooting", "backing-up", "modifying"}, i.Status):
	case i.Status == "failed" && (i.StatusReason == reasonGone || strings.HasPrefix(i.StatusReason, reasonRecreateFailed)):
	default:
		return false // never created completely (creating, restoring), failed otherwise, or being deleted
	}
	if i.ContainerID != "" && s.env.Docker.State(i.ContainerID) != "missing" {
		return false
	}
	e, ok := findEngine(i.Engine)
	if !ok {
		return false
	}
	if e.DataDir != "" {
		if _, err := s.env.Docker.C.InspectVolume(volumeName(i.ID)); err != nil {
			return false // nothing to recover from
		}
	}
	unlock, err := s.lock(i.ID)
	if err != nil {
		return false
	}
	start := i.Status != "stopped" && i.Status != "stopping"
	status, reason := "starting", reasonRecreating
	if !start {
		status, reason = "stopped", ""
	}
	// Forget the old container so nothing acts on it meanwhile (sync skips an instance without one).
	if _, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		x.ContainerID, x.Status, x.StatusReason = "", status, reason
		return nil
	}); err != nil {
		unlock()
		return false
	}
	go func() {
		defer unlock()
		defer core.Recover("rds recreate " + i.ID)
		s.recreate(i, e, start)
	}()
	return true
}

// recreate builds the container of instance i again over its existing data
// volume and, when start is set, starts it and waits until it is ready.
func (s *Service) recreate(i Instance, e Engine, start bool) {
	fail := func(err error) {
		log.Printf("rds: %s: %s%v", i.ID, reasonRecreateFailed, err)
		s.setStatus(i.ID, "failed", reasonRecreateFailed+err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	v, err := s.vpc.GetVPC(i.VpcID)
	if err != nil {
		fail(fmt.Errorf("vpc %s: %w", i.VpcID, err))
		return
	}
	pass, err := s.password(i)
	if err != nil {
		fail(fmt.Errorf("master password: %w", err))
		return
	}
	spec := s.runSpec(i, e, v.Network, pass, i.Endpoint.PublicPort)
	var cid string
	if c, err := s.env.Docker.Inspect(spec.Name); err == nil {
		cid = c.ID // a container of that name is there (its ID changed): use it
	} else if cid, err = s.env.Docker.Run(ctx, spec); err != nil {
		fail(err)
		return
	}
	setContainer := func(cid string) error {
		_, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.ContainerID = cid; return nil })
		return err
	}
	if err := setContainer(cid); err != nil {
		_ = s.env.Docker.Remove(cid)
		fail(err)
		return
	}
	s.vpc.FirewallChanged() // enforce its security groups on the new container
	if !start {
		s.setStatus(i.ID, "stopped", "")
		log.Printf("rds: %s: recreated its container from the data volume (stopped)", i.ID)
		return
	}
	if err := s.env.Docker.Start(cid); err != nil {
		if !i.PubliclyAccessible || i.Endpoint.PublicPort == 0 {
			fail(err)
			return
		}
		// The host port it had is taken on this host: publish on any free one.
		log.Printf("rds: %s: host port %d: %v; publishing on another port", i.ID, i.Endpoint.PublicPort, err)
		_ = s.env.Docker.Remove(cid)
		spec = s.runSpec(i, e, v.Network, pass, 0)
		spec.Start = true
		if cid, err = s.env.Docker.Run(ctx, spec); err != nil {
			fail(err)
			return
		}
		if err := setContainer(cid); err != nil {
			fail(err)
			return
		}
		s.vpc.FirewallChanged()
	}
	if err := s.waitReady(ctx, cid, e, i.MasterUsername, pass, i.DBName); err != nil {
		fail(err)
		return
	}
	_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		x.Status, x.StatusReason = "available", ""
		if x.PubliclyAccessible {
			x.Endpoint.PublicPort = s.env.Docker.HostPort(cid, x.Endpoint.Port)
		}
		x.Endpoint.ConnectHint = connectHint(*x)
		return nil
	})
	log.Printf("rds: %s: recreated its container from the data volume", i.ID)
}
