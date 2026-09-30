package vpc

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The firewall keeps every member's netfilter rules (fwrules.go) loaded in its
// container's network namespace. The rules are programmed by a short-lived
// helper container that joins the target's namespace with NET_ADMIN, so the
// mechanism is identical on Linux Docker, Docker Desktop and OrbStack (where
// containers live in a VM) and needs no host privileges; the target gets no
// extra capability. The helper image is built locally from sgfw.Dockerfile
// (Alpine pinned by digest plus the iptables package).
//
// Rules live in the namespace, which is rebuilt whenever a container starts,
// so they are re-applied on: launch, container (re)start (Docker start
// events), security group rule changes, group membership changes, and when
// the addresses behind a referenced group change (a member appears or
// disappears). A periodic pass catches anything missed.

//go:embed sgfw.Dockerfile
var fwDockerfile string

const fwScript = "iptables-restore -n < /hc-sg.rules && " +
	"(iptables -C INPUT -j " + chainIn + " 2>/dev/null || iptables -I INPUT 1 -j " + chainIn + ") && " +
	"(iptables -C OUTPUT -j " + chainOut + " 2>/dev/null || iptables -I OUTPUT 1 -j " + chainOut + ")"

type fwApplied struct {
	hash    string
	started string    // the container's StartedAt when applied
	retry   time.Time // do not retry a failed apply before this
}

type firewall struct {
	mu        sync.Mutex
	providers []func() []Member
	applied   map[string]fwApplied // container ID -> what is loaded
	running   bool                 // an async sync loop is running
	dirty     bool                 // a change arrived while it ran
	passMu    sync.Mutex           // serialises sync passes
	image     string
	imageTry  time.Time
}

// RegisterMembers adds a source of members: the resources of a service that
// have security groups.
func (s *Service) RegisterMembers(fn func() []Member) {
	if s == nil {
		return
	}
	s.fw.mu.Lock()
	defer s.fw.mu.Unlock()
	s.fw.providers = append(s.fw.providers, fn)
}

func (s *Service) fwMembers() []Member {
	s.fw.mu.Lock()
	ps := slices.Clone(s.fw.providers)
	s.fw.mu.Unlock()
	var out []Member
	for _, p := range ps {
		out = append(out, p()...)
	}
	return out
}

// FirewallChanged brings every member's rules up to date in the background.
// Call it after anything that changes who may talk to whom: a group's rules,
// a resource's groups, a resource appearing or disappearing.
func (s *Service) FirewallChanged() {
	if s == nil || s.env.Docker == nil {
		return
	}
	s.fw.mu.Lock()
	if s.fw.running {
		s.fw.dirty = true
		s.fw.mu.Unlock()
		return
	}
	s.fw.running = true
	s.fw.mu.Unlock()
	go func() {
		defer core.Recover("security group enforcement")
		for {
			s.syncFirewall(context.Background(), false)
			s.fw.mu.Lock()
			if !s.fw.dirty {
				s.fw.running = false
				s.fw.mu.Unlock()
				return
			}
			s.fw.dirty = false
			s.fw.mu.Unlock()
		}
	}()
}

// RunFirewall enforces security groups until ctx ends: it reacts to
// container starts and re-checks everything periodically.
func (s *Service) RunFirewall(ctx context.Context) {
	if s.env.Docker == nil {
		return
	}
	go s.watchStarts(ctx)
	s.syncFirewall(ctx, true)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.syncFirewall(ctx, n%6 == 0)
		}
	}
}

// watchStarts forgets what was loaded into a container when it starts (its
// namespace is new) and re-applies.
func (s *Service) watchStarts(ctx context.Context) {
	defer core.Recover("security group enforcement events")
	for ctx.Err() == nil {
		ch := make(chan *docker.APIEvents, 128)
		if err := s.env.Docker.C.AddEventListener(ch); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
	loop:
		for {
			select {
			case <-ctx.Done():
				_ = s.env.Docker.C.RemoveEventListener(ch)
				return
			case ev, ok := <-ch:
				if !ok {
					break loop
				}
				if ev.Type != "container" || ev.Action != "start" || ev.Actor.Attributes[core.LabelManaged] != "true" || ev.Actor.Attributes[core.LabelResource] == "sgfw" {
					continue
				}
				s.fw.mu.Lock()
				delete(s.fw.applied, ev.Actor.ID)
				s.fw.mu.Unlock()
				s.FirewallChanged()
			}
		}
	}
}

type fwJob struct {
	m    Member
	text string
	hash string
}

// syncFirewall loads the rules of every member whose loaded rules are out of
// date. With verify it first checks that containers were not restarted since.
func (s *Service) syncFirewall(ctx context.Context, verify bool) {
	s.fw.passMu.Lock()
	defer s.fw.passMu.Unlock()
	members := s.fwMembers()
	if len(members) == 0 {
		return
	}
	jobs, live := s.fwJobs(members, nil, verify)
	s.fw.mu.Lock()
	for id := range s.fw.applied {
		if !live[id] {
			delete(s.fw.applied, id)
		}
	}
	s.fw.mu.Unlock()
	s.applyJobs(ctx, jobs)
}

// fwJobs renders the ruleset of every member and returns those that need loading.
// Members in extra are added to the membership (a resource being launched).
func (s *Service) fwJobs(members, extra []Member, verify bool) ([]fwJob, map[string]bool) {
	members = append(slices.Clone(members), extra...)
	sgs := map[string]SecurityGroup{}
	for _, g := range store.List[SecurityGroup](s.env.Store, cSGs) {
		sgs[g.ID] = g
	}
	views := map[string]*fwView{}
	live := map[string]bool{}
	var jobs []fwJob
	for _, m := range members {
		if m.ContainerID == "" || m.IP == "" {
			continue
		}
		live[m.ContainerID] = true
		f, ok := views[m.VpcID]
		if !ok {
			v, err := store.Get[VPC](s.env.Store, cVPCs, m.VpcID)
			if err == nil {
				f = newFWView(v, s.DefaultSecurityGroup(v.ID), sgs, members)
			}
			views[m.VpcID] = f
		}
		if f == nil {
			continue
		}
		text := f.ruleset(m)
		h := rulesetHash(text)
		s.fw.mu.Lock()
		st := s.fw.applied[m.ContainerID]
		s.fw.mu.Unlock()
		if verify && st.hash != "" {
			ci, err := s.env.Docker.Inspect(m.ContainerID)
			if err != nil || !ci.State.Running || ci.State.StartedAt.String() != st.started {
				s.fw.mu.Lock()
				delete(s.fw.applied, m.ContainerID)
				s.fw.mu.Unlock()
				st = fwApplied{}
			}
		}
		if st.hash == h || time.Now().Before(st.retry) {
			continue
		}
		jobs = append(jobs, fwJob{m: m, text: text, hash: h})
	}
	return jobs, live
}

func (s *Service) applyJobs(ctx context.Context, jobs []fwJob) {
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer core.Recover("security group enforcement")
			s.applyJob(ctx, j)
		}()
	}
	wg.Wait()
}

func (s *Service) applyJob(ctx context.Context, j fwJob) {
	fail := func(err error) {
		if ci, e := s.env.Docker.Inspect(j.m.ContainerID); e != nil || !ci.State.Running {
			return // it went away meanwhile; applied when it starts
		}
		s.fw.mu.Lock()
		if s.fw.applied == nil {
			s.fw.applied = map[string]fwApplied{}
		}
		s.fw.applied[j.m.ContainerID] = fwApplied{retry: time.Now().Add(15 * time.Second)}
		s.fw.mu.Unlock()
		log.Printf("vpc: security groups of %s %s: %v", j.m.Kind, j.m.ID, err)
	}
	ci, err := s.env.Docker.Inspect(j.m.ContainerID)
	if err != nil || !ci.State.Running {
		s.fw.mu.Lock()
		delete(s.fw.applied, j.m.ContainerID)
		s.fw.mu.Unlock()
		return // applied when it starts
	}
	img, err := s.fwImage(ctx)
	if err != nil {
		log.Printf("vpc: security group helper image: %v (security groups are not enforced until it builds)", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := s.env.Docker.RunInNetns(cctx, j.m.ContainerID, img, []string{"sh", "-c", fwScript}, map[string][]byte{"hc-sg.rules": []byte(j.text)}); err != nil {
		fail(err)
		return
	}
	s.fw.mu.Lock()
	if s.fw.applied == nil {
		s.fw.applied = map[string]fwApplied{}
	}
	s.fw.applied[j.m.ContainerID] = fwApplied{hash: j.hash, started: ci.State.StartedAt.String()}
	s.fw.mu.Unlock()
}

// fwImage builds the helper image on first use.
func (s *Service) fwImage(ctx context.Context) (string, error) {
	s.fw.mu.Lock()
	defer s.fw.mu.Unlock()
	if s.fw.image != "" {
		return s.fw.image, nil
	}
	if time.Now().Before(s.fw.imageTry) {
		return "", fmt.Errorf("build failed recently")
	}
	tag := "homecloud/sgfw:" + rulesetHash(fwDockerfile)
	bctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := s.env.Docker.BuildImage(bctx, tag, fwDockerfile); err != nil {
		s.fw.imageTry = time.Now().Add(time.Minute)
		return "", err
	}
	s.fw.image = tag
	return tag, nil
}

// ProtectNow loads a just-started container's rules right away, before its
// resource is recorded as running (so peers never see it unprotected for
// longer than the container's own startup).
func (s *Service) ProtectNow(ctx context.Context, m Member) {
	if s == nil || s.env.Docker == nil {
		return
	}
	s.fw.passMu.Lock()
	defer s.fw.passMu.Unlock()
	members := s.fwMembers()
	members = slices.DeleteFunc(members, func(o Member) bool { return o.ID == m.ID && o.Kind == m.Kind })
	jobs, _ := s.fwJobs(members, []Member{m}, false)
	jobs = slices.DeleteFunc(jobs, func(j fwJob) bool { return j.m.ContainerID != m.ContainerID })
	s.applyJobs(ctx, jobs)
	s.FirewallChanged() // the new address may appear in other members' rules
}
