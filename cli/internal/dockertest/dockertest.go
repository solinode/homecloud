// Package dockertest gives Docker-backed tests a private, disposable account.
//
// Every test that creates Docker objects calls Start, which stamps the objects
// it creates with a unique "hctest-<random>" account label and removes every
// container, network and volume carrying that label when the test ends, even
// when it fails or panics. It also sweeps objects left by crashed earlier runs:
// only those whose account label starts with Prefix AND that are older than
// StaleAfter. Objects with any other account label (a real installation) or no
// label are never touched.
package dockertest

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
)

// Prefix starts every test account label.
const Prefix = core.TestAccountPrefix

// StaleAfter is how old a test-labelled object must be before a sweep removes it.
const StaleAfter = time.Hour

// NewAccount returns a fresh unique test account label.
func NewAccount() string { return Prefix + strings.ToLower(core.RandHex(6)) }

// isTest reports whether an account label belongs to a test (never true for the
// empty label or a real 12-digit account).
func isTest(acct string) bool { return len(acct) > len(Prefix) && strings.HasPrefix(acct, Prefix) }

var sweepOnce sync.Once

// Start connects to Docker (skipping the test when it is unavailable), sweeps
// stale test leftovers once per process, gives the test a fresh account label
// (runtime.Account) and registers cleanup of everything created under it.
func Start(t *testing.T) *runtime.Docker {
	t.Helper()
	d, err := runtime.New()
	if err != nil {
		t.Skip("Docker not available: ", err)
	}
	sweepOnce.Do(func() { Sweep(d, StaleAfter, t.Logf) })
	prev, acct := runtime.Account, NewAccount()
	runtime.Account = acct
	t.Cleanup(func() {
		Remove(d, acct, t.Logf)
		runtime.Account = prev
	})
	return d
}

// SweepStale removes stale test leftovers once per process when Docker is
// reachable; for tests that start Docker objects by hand.
func SweepStale(logf func(string, ...any)) {
	sweepOnce.Do(func() {
		if d, err := runtime.New(); err == nil {
			Sweep(d, StaleAfter, logf)
		}
	})
}

// VPCRangeHint explains how to recover from default-VPC range exhaustion.
const VPCRangeHint = "the Docker host has no free 10.88-119.0.0/16 range (leaked test networks? " +
	"list them with `docker network ls --filter label=homecloud.account` and remove the ones labelled hctest-*)"

// DefaultVPC runs ensure (a vpc Service's EnsureDefault). When the Docker host
// has no free range the test FAILS in CI (CI set) so exhaustion cannot hide
// real failures; locally it skips with a hint. Other errors skip as before.
func DefaultVPC(t *testing.T, ensure func(context.Context) error) {
	t.Helper()
	err := ensure(context.Background())
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "no free 10.x.0.0/16 range") {
		if os.Getenv("CI") != "" {
			t.Fatalf("default VPC: %v: %s", err, VPCRangeHint)
		}
		t.Skipf("default VPC: %v: %s", err, VPCRangeHint)
	}
	t.Skipf("cannot create the default VPC: %v", err)
}

// Remove force-removes every container, network and volume labelled with the
// test account. It refuses any account that is not a test account.
func Remove(d *runtime.Docker, acct string, logf func(string, ...any)) {
	if !isTest(acct) {
		return
	}
	sel := core.LabelAccount + "=" + acct
	cs, _ := d.C.ListContainers(docker.ListContainersOptions{All: true, Filters: map[string][]string{"label": {sel}}})
	for _, c := range cs {
		if c.Labels[core.LabelAccount] == acct {
			_ = d.C.RemoveContainer(docker.RemoveContainerOptions{ID: c.ID, Force: true, RemoveVolumes: true})
		}
	}
	ns, _ := d.C.FilteredListNetworks(docker.NetworkFilterOpts{"label": {sel: true}})
	for _, n := range ns {
		if n.Labels[core.LabelAccount] == acct {
			removeNetwork(d, n.ID)
		}
	}
	vs, _ := d.C.ListVolumes(docker.ListVolumesOptions{Filters: map[string][]string{"label": {sel}}})
	for _, v := range vs {
		if v.Labels[core.LabelAccount] == acct {
			_ = d.RemoveVolume(v.Name)
		}
	}
}

func removeNetwork(d *runtime.Docker, id string) {
	// Shared infrastructure containers (DNS, metadata) may still be attached.
	if info, err := d.C.NetworkInfo(id); err == nil {
		for cid := range info.Containers {
			_ = d.C.DisconnectNetwork(id, docker.NetworkConnectionOptions{Container: cid, Force: true})
		}
	}
	_ = d.RemoveNetwork(id)
}

// Sweep removes test-labelled objects (account starts with Prefix) older than
// maxAge. Objects with no or another account label are never touched.
func Sweep(d *runtime.Docker, maxAge time.Duration, logf func(string, ...any)) {
	sweep(d, maxAge, isTest, logf)
}

// sweep is Sweep with the account predicate injectable (and narrowable, for
// tests that must not disturb other packages' live test objects).
func sweep(d *runtime.Docker, maxAge time.Duration, isTest func(string) bool, logf func(string, ...any)) {
	cutoff := time.Now().Add(-maxAge)
	cs, _ := d.C.ListContainers(docker.ListContainersOptions{All: true, Filters: map[string][]string{"label": {core.LabelAccount}}})
	for _, c := range cs {
		if isTest(c.Labels[core.LabelAccount]) && time.Unix(c.Created, 0).Before(cutoff) {
			logf("dockertest: sweeping stale container %s (%s)", c.ID[:12], c.Labels[core.LabelAccount])
			_ = d.C.RemoveContainer(docker.RemoveContainerOptions{ID: c.ID, Force: true, RemoveVolumes: true})
		}
	}
	for id, created := range networkAges() {
		n, err := d.C.NetworkInfo(id)
		if err != nil || !isTest(n.Labels[core.LabelAccount]) || !created.Before(cutoff) {
			continue
		}
		logf("dockertest: sweeping stale network %s (%s)", n.Name, n.Labels[core.LabelAccount])
		removeNetwork(d, n.ID)
	}
	vs, _ := d.C.ListVolumes(docker.ListVolumesOptions{Filters: map[string][]string{"label": {core.LabelAccount}}})
	for _, v := range vs {
		if isTest(v.Labels[core.LabelAccount]) && !v.CreatedAt.IsZero() && v.CreatedAt.Before(cutoff) {
			logf("dockertest: sweeping stale volume %s (%s)", v.Name, v.Labels[core.LabelAccount])
			_ = d.RemoveVolume(v.Name)
		}
	}
}

// networkAges maps the ID of every account-labelled network to its creation
// time. The Docker client library does not expose it, so this asks the docker
// CLI; when that is unavailable no network is swept (never guess an age).
func networkAges() map[string]time.Time {
	out, err := exec.Command("docker", "network", "ls", "--no-trunc", "--filter", "label="+core.LabelAccount,
		"--format", "{{.ID}}|{{.CreatedAt}}").Output()
	if err != nil {
		return nil
	}
	m := map[string]time.Time{}
	for _, line := range strings.Split(string(out), "\n") {
		id, ts, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok {
			continue
		}
		// "2026-10-01 10:11:12.123456789 +0000 UTC"
		if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", ts); err == nil {
			m[id] = t
		}
	}
	return m
}
