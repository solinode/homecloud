package ec2_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMissingContainerInstanceIsRecreated removes the container of a running
// container instance, as on a host restored from a backup (volumes, no
// containers): the instance comes back stopped with its volume and data
// instead of being terminated (which deleted its delete-on-termination volume).
func TestMissingContainerInstanceIsRecreated(t *testing.T) {
	h := liveHarness(t)
	d := h.Env.Docker
	var out []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(h.Native(t, "POST", "/api/v1/ec2/instances", map[string]any{
		"image_id": "ami-nginx", "volumes": []any{map[string]any{"size_gb": 1, "mount_path": "/data"}},
	}), &out); err != nil || len(out) != 1 {
		t.Fatalf("launch: %v %v", out, err)
	}
	id := out[0].ID
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "terminate-instances", "--instance-ids", id) })

	type inst struct {
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
		ContainerID string `json:"container_id"`
		Volumes     []struct {
			VolumeID string `json:"volume_id"`
		} `json:"volumes"`
	}
	get := func() inst {
		var i inst
		_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/ec2/instances/"+id, nil), &i)
		return i
	}
	waitFor(t, "running", 2*time.Minute, func() bool { return get().State == "running" })
	before := get()
	ctx := context.Background()
	if res, err := d.Exec(ctx, before.ContainerID, []string{"sh", "-c", "echo kept > /data/file"}, nil); err != nil || res.ExitCode != 0 {
		t.Fatalf("write: %v %+v", err, res)
	}
	if err := d.Remove(before.ContainerID); err != nil {
		t.Fatal(err)
	}

	after := get()
	if after.State != "stopped" || after.ContainerID == "" || after.ContainerID == before.ContainerID || !strings.HasPrefix(after.StateReason, "Server.Restore") {
		t.Fatalf("after the container went: %+v", after)
	}
	if len(after.Volumes) != 1 || after.Volumes[0].VolumeID != before.Volumes[0].VolumeID {
		t.Fatalf("volumes: %+v", after.Volumes)
	}
	h.Native(t, "POST", "/api/v1/ec2/instances/"+id+"/start", nil)
	waitFor(t, "running again", 2*time.Minute, func() bool { return get().State == "running" })
	res, err := d.Exec(ctx, get().ContainerID, []string{"cat", "/data/file"}, nil)
	if err != nil || !strings.Contains(res.Stdout, "kept") {
		t.Fatalf("data after recreation: %v %+v", err, res)
	}
}
