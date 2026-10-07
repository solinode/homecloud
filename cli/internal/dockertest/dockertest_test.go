package dockertest

import (
	"os/exec"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
)

func TestIsTest(t *testing.T) {
	for acct, want := range map[string]bool{"": false, "123456789012": false, "hctest-": false, "hctest-ab12": true, "efstest123": false, "xhctest-ab": false} {
		if isTest(acct) != want {
			t.Errorf("isTest(%q) = %v", acct, !want)
		}
	}
}

func volumeExists(d *runtime.Docker, name string) bool {
	_, err := d.C.InspectVolume(name)
	return err == nil
}

func networkExists(d *runtime.Docker, name string) bool {
	_, err := d.C.NetworkInfo(name)
	return err == nil
}

// TestRemoveAndSweepScope checks that cleanup and the sweep touch only
// test-labelled objects of the matching account (and, for the sweep, old enough).
func TestRemoveAndSweepScope(t *testing.T) {
	d := Start(t) // skips without Docker; its cleanup removes this test's account
	mine := runtime.Account
	other := NewAccount()
	t.Cleanup(func() { Remove(d, other, t.Logf) })

	suffix := core.RandHex(4)
	mk := func(name, acct string) {
		t.Helper()
		labels := map[string]string{}
		if acct != "-" {
			labels[core.LabelAccount] = acct
		}
		if err := d.CreateVolume(name, labels); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.RemoveVolume(name) })
	}
	vMine, vOther, vReal, vNone := "hctest-v-mine-"+suffix, "hctest-v-other-"+suffix, "hctest-v-real-"+suffix, "hctest-v-none-"+suffix
	mk(vMine, mine)
	mk(vOther, other)
	mk(vReal, "123456789012")
	mk(vNone, "-")
	nName := "hctest-n-" + suffix
	if _, err := d.C.CreateNetwork(docker.CreateNetworkOptions{Name: nName, Driver: "bridge", Labels: map[string]string{core.LabelAccount: mine}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.RemoveNetwork(nName) })

	// A sweep with the real one-hour threshold leaves brand-new objects alone.
	Sweep(d, StaleAfter, t.Logf)
	if !volumeExists(d, vMine) || !volumeExists(d, vOther) || !networkExists(d, nName) {
		t.Fatal("sweep removed objects younger than the threshold")
	}

	// Sweeping everything older than "now" removes test-labelled objects only.
	time.Sleep(1100 * time.Millisecond)
	// Narrowed to this test's two accounts so concurrent packages are untouched.
	sweep(d, time.Second, func(a string) bool { return a == mine || a == other }, t.Logf)
	if volumeExists(d, vMine) || volumeExists(d, vOther) {
		t.Error("sweep left stale test-labelled volumes behind")
	}
	// Network ages come from the docker CLI; without it the sweep leaves networks alone.
	if _, err := exec.LookPath("docker"); err == nil && networkExists(d, nName) {
		t.Error("sweep left a stale test-labelled network behind")
	}
	if !volumeExists(d, vReal) || !volumeExists(d, vNone) {
		t.Error("sweep removed objects that are not test-labelled")
	}

	// Remove refuses anything that is not a test account.
	Remove(d, "123456789012", t.Logf)
	Remove(d, "", t.Logf)
	if !volumeExists(d, vReal) || !volumeExists(d, vNone) {
		t.Error("Remove touched non-test objects")
	}
}

func TestRemoveOnlyMatchingAccount(t *testing.T) {
	d := Start(t)
	mine := runtime.Account
	other := NewAccount()
	t.Cleanup(func() { Remove(d, other, t.Logf) })
	a, b := "hctest-r-a-"+core.RandHex(4), "hctest-r-b-"+core.RandHex(4)
	if err := d.CreateVolume(a, map[string]string{core.LabelAccount: mine}); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateVolume(b, map[string]string{core.LabelAccount: other}); err != nil {
		t.Fatal(err)
	}
	Remove(d, mine, t.Logf)
	if volumeExists(d, a) {
		t.Error("own volume survived Remove")
	}
	if !volumeExists(d, b) {
		t.Error("Remove deleted another test account's volume")
	}
}
