package server

import (
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/dockertest"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
)

func TestOpsWithDocker(t *testing.T) {
	d := dockertest.Start(t)
	vol := "hc-test-ops-" + core.RandHex(4)
	if err := d.CreateVolume(vol, runtime.Labels("ebs", "vol-test", nil)); err != nil {
		t.Fatal(err)
	}
	cfg := core.Config{DataDir: t.TempDir(), APIAddr: "127.0.0.1:8080", Region: "us-east-1"}
	o := newOps(cfg, d, nil, runtime.Account, time.Now())
	o.measureDisk(t.Context())
	disk := o.diskState()
	if disk.volumesErr != "" || disk.volumes.Count != 1 {
		t.Fatalf("volumes: %+v", disk)
	}
	st := o.status()
	if !st.Docker.Reachable || st.Docker.ServerVersion == "" {
		t.Fatalf("docker: %+v", st.Docker)
	}
	// A fresh account has no helpers: the status says so instead of "ok".
	if st.Status != "degraded" {
		t.Fatalf("overall %s, want degraded without helpers", st.Status)
	}
	for _, s := range st.Subsystems {
		if s.Kind == "helper" && (s.Status != "error" || s.Detail != "not running") {
			t.Errorf("helper %+v", s)
		}
	}
}
