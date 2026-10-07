package ec2_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/system"
	"golang.org/x/crypto/ssh"
)

// TestVMDisksImagesAndBackup exercises the disk side of VM instances on a real
// guest: an extra volume at launch, a snapshot restored and attached to the
// running guest, a detach, an image made from the instance (launched into a new
// instance after the first is terminated) and a backup and restore of the
// instance's disk. It boots the guest about five times: slow when emulated.
//
//	HC_TEST_VM=1 go test -timeout 90m -run TestVMDisksImagesAndBackup ./internal/svc/ec2
func TestVMDisksImagesAndBackup(t *testing.T) {
	if os.Getenv("HC_TEST_VM") != "1" {
		t.Skip("set HC_TEST_VM=1 to boot a real VM (slow; downloads a cloud image)")
	}
	// dockertest (newVMLab) labels everything this test creates with an account of
	// its own and removes it afterwards; the backup below archives only that
	// account's volumes (never another installation's).
	lab := newVMLab(t)
	acct := runtime.Account
	f, h, signer, sg := lab.filterEnv, lab.h, lab.signer, lab.sg
	d := h.Env.Docker
	// Volumes that outlive their instance (detached or not deleted on termination) and snapshots.
	var leftVolumes, leftSnapshots []string
	t.Cleanup(func() {
		for _, v := range leftVolumes {
			_, _ = h.AWSErr(t, "ec2", "delete-volume", "--volume-id", v)
		}
		for _, s := range leftSnapshots {
			_, _ = h.AWSErr(t, "ec2", "delete-snapshot", "--snapshot-id", s)
		}
	})

	// volumes maps device names to volume IDs for an instance.
	volumes := func(instance string) map[string]string {
		out := map[string]string{}
		for _, v := range h.AWSJSON(t, "ec2", "describe-volumes", "--filters", "Name=attachment.instance-id,Values="+instance)["Volumes"].([]any) {
			m := v.(map[string]any)
			att := m["Attachments"].([]any)[0].(map[string]any)
			out[att["Device"].(string)] = m["VolumeId"].(string)
		}
		return out
	}
	serial := func(volID string) string { return "/dev/disk/by-id/virtio-" + volID[:20] }
	waitVolume := func(volID, state string) {
		waitFor(t, volID+" "+state, vmTestBudget, func() bool {
			vs := h.AWSJSON(t, "ec2", "describe-volumes", "--volume-ids", volID)["Volumes"].([]any)
			m := vs[0].(map[string]any)
			if state == "attached" {
				a := m["Attachments"].([]any)
				return m["State"] == "in-use" && len(a) == 1 && a[0].(map[string]any)["State"] == "attached"
			}
			return m["State"] == state
		})
	}

	// 1. Launch with an extra 2 GiB volume; the guest sees it as a virtio disk.
	run := h.AWSJSON(t, "ec2", "run-instances", "--image-id", vmTestImage, "--instance-type", "t3.micro", "--key-name", "vmtest",
		"--subnet-id", f.subnet, "--security-group-ids", sg,
		"--block-device-mappings", `[{"DeviceName":"/dev/xvda","Ebs":{"VolumeSize":10}},{"DeviceName":"/dev/sdf","Ebs":{"VolumeSize":2,"DeleteOnTermination":true}}]`)
	inst := run["Instances"].([]any)[0].(map[string]any)
	id, az := inst["InstanceId"].(string), inst["Placement"].(map[string]any)["AvailabilityZone"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "terminate-instances", "--instance-ids", id) })
	i := f.vmWaitState(t, id, "running")
	vols := volumes(id)
	if len(vols) != 2 || vols["/dev/xvda"] == "" || vols["/dev/sdf"] == "" {
		t.Fatalf("volumes of %s: %v", id, vols)
	}
	data1 := vols["/dev/sdf"]
	leftVolumes = append(leftVolumes, data1)
	c := f.sshDial(t, id, i.PublicPorts["22/tcp"], signer, vmTestUser)
	if got := sshRun(t, c, "lsblk -dbno NAME,SIZE,SERIAL | grep "+data1[:20]); !strings.Contains(got, "2147483648") {
		t.Fatalf("the data disk is not 2 GiB with serial %s: %q", data1[:20], got)
	}
	sshRun(t, c, "test -b "+serial(data1))
	sshRun(t, c, "sudo mkfs.ext4 -q "+serial(data1)+" && sudo mkdir -p /mnt/data && sudo mount "+serial(data1)+" /mnt/data && echo on-disk | sudo tee /mnt/data/data.txt && sync")
	sshRun(t, c, "echo root-marker > /home/"+vmTestUser+"/marker.txt && sync")
	c.Close()
	t.Logf("extra volume %s is a virtio disk in the guest", data1)

	// 2. Snapshot it, restore the snapshot to a new volume and attach that to the running guest.
	snap := h.AWSJSON(t, "ec2", "create-snapshot", "--volume-id", data1)["SnapshotId"].(string)
	leftSnapshots = append(leftSnapshots, snap)
	waitFor(t, "snapshot "+snap, vmTestBudget, func() bool {
		ss := h.AWSJSON(t, "ec2", "describe-snapshots", "--snapshot-ids", snap)["Snapshots"].([]any)
		return ss[0].(map[string]any)["State"] == "completed"
	})
	data2 := h.AWSJSON(t, "ec2", "create-volume", "--snapshot-id", snap, "--availability-zone", az)["VolumeId"].(string)
	leftVolumes = append(leftVolumes, data2)
	waitVolume(data2, "available")
	h.AWS(t, "ec2", "attach-volume", "--volume-id", data2, "--instance-id", id, "--device", "/dev/sdg")
	waitVolume(data2, "attached") // the guest was rebuilt (reboots) with the new disk
	c = f.sshDial(t, id, 0, signer, vmTestUser)
	sshRun(t, c, "test -b "+serial(data2))
	if got := sshRun(t, c, "sudo mkdir -p /mnt/snap && sudo mount "+serial(data2)+" /mnt/snap && cat /mnt/snap/data.txt"); got != "on-disk" {
		t.Errorf("the snapshot's disk holds %q", got)
	}
	if got := sshRun(t, c, "cat /home/"+vmTestUser+"/marker.txt"); got != "root-marker" {
		t.Errorf("root disk after the attach: %q", got)
	}
	c.Close()
	t.Logf("snapshot %s restored to %s and attached to the running guest", snap, data2)

	// 3. Detach the first data volume: its disk leaves the guest, the other stays.
	h.AWS(t, "ec2", "detach-volume", "--volume-id", data1)
	waitVolume(data1, "available")
	c = f.sshDial(t, id, 0, signer, vmTestUser)
	if out, err := run1(c, "test -e "+serial(data1)); err == nil {
		t.Errorf("the detached disk is still in the guest: %s", out)
	}
	sshRun(t, c, "test -b "+serial(data2))
	c.Close()
	// The root volume cannot be detached.
	if msg, err := h.AWSErr(t, "ec2", "detach-volume", "--volume-id", vols["/dev/xvda"]); err == nil || !strings.Contains(msg, "root volume") {
		t.Errorf("detaching the root volume: %v %s", err, msg)
	}

	// 4. An image from the instance (stopped, so it is clean) is a standalone copy of its disk.
	h.AWS(t, "ec2", "stop-instances", "--instance-ids", id)
	f.vmWaitState(t, id, "stopped")
	ami := h.AWSJSON(t, "ec2", "create-image", "--instance-id", id, "--name", "vm-custom-image")["ImageId"].(string)
	im := h.AWSJSON(t, "ec2", "describe-images", "--image-ids", ami)["Images"].([]any)[0].(map[string]any)
	if im["Hypervisor"] != "kvm" || im["VirtualizationType"] != "hvm" || im["State"] != "available" {
		t.Errorf("image %v", im)
	}
	var amiVol string
	var imgs []struct {
		ID     string `json:"id"`
		VMDisk string `json:"vm_disk"`
	}
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/ec2/images", nil), &imgs)
	for _, x := range imgs {
		if x.ID == ami {
			amiVol = x.VMDisk
		}
	}
	if amiVol == "" {
		t.Fatalf("image %s has no disk volume", ami)
	}
	t.Logf("image %s made from %s (volume %s)", ami, id, amiVol)

	// 5. Back up the stopped instance, lose its container and disk, restore: the disk is
	// standalone in the archive (no backing image) and boots.
	rootVol := vols["/dev/xvda"]
	cid := f.vmGet(id).ContainerID
	cfg := core.DefaultConfig()
	cfg.DataDir = t.TempDir()
	b := &system.Backup{Cfg: cfg, Docker: d, AccountID: acct, Version: "test", OwnVolumesOnly: true, Flatten: f.ec2.FlattenForBackup}
	// The archive holds whole disks: a file, not memory.
	archive, err := os.Create(filepath.Join(t.TempDir(), "backup.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if err := b.Write(context.Background(), archive, true, t.Logf); err != nil {
		t.Fatal(err)
	}
	size, _ := archive.Seek(0, io.SeekCurrent)
	t.Logf("backup: %d bytes", size)
	if size < 50<<20 {
		t.Fatalf("the backup (%d bytes) cannot hold the instance's disk", size)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(cid); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveVolume("hc-" + rootVol); err != nil {
		t.Fatal(err)
	}
	restoreCfg := t.TempDir()
	if _, err := system.Restore(context.Background(), d, archive, system.RestoreOptions{DataDir: restoreCfg, Force: true, Logf: t.Logf}); err != nil {
		t.Fatal(err)
	}
	// The record still exists: reading it recreates the VM container from the restored disk.
	var got vmInstance
	waitFor(t, "container recreated", 5*time.Minute, func() bool {
		got = f.vmGet(id)
		return got.State == "stopped" && got.ContainerID != "" && got.ContainerID != cid
	})
	h.AWS(t, "ec2", "start-instances", "--instance-ids", id)
	f.vmWaitState(t, id, "running")
	c = f.sshDial(t, id, 0, signer, vmTestUser)
	if got := sshRun(t, c, "cat /home/"+vmTestUser+"/marker.txt"); got != "root-marker" {
		t.Errorf("the restored disk holds %q", got)
	}
	c.Close()
	if info := vmExec(t, d, f.vmGet(id).ContainerID, "qemu-img info -U --output=json /vm/disk.qcow2"); strings.Contains(info, "backing-filename") {
		t.Errorf("the restored root disk still has a backing file: %s", info)
	}
	t.Logf("backup restored: %s boots from its restored disk", id)

	// 6. Terminate the source instance; the image launches on its own.
	h.AWS(t, "ec2", "terminate-instances", "--instance-ids", id)
	f.vmWaitState(t, id, "terminated")
	run2 := h.AWSJSON(t, "ec2", "run-instances", "--image-id", ami, "--instance-type", "t3.micro", "--key-name", "vmtest", "--subnet-id", f.subnet, "--security-group-ids", sg)
	id2 := run2["Instances"].([]any)[0].(map[string]any)["InstanceId"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "terminate-instances", "--instance-ids", id2) })
	i2 := f.vmWaitState(t, id2, "running")
	c = f.sshDial(t, id2, i2.PublicPorts["22/tcp"], signer, vmTestUser)
	if got := sshRun(t, c, "cat /home/"+vmTestUser+"/marker.txt"); got != "root-marker" {
		t.Errorf("the instance launched from the image holds %q", got)
	}
	if got := sshRun(t, c, "hostname"); got == id || !strings.HasPrefix(got, "ip-") {
		t.Errorf("hostname %q", got)
	}
	c.Close()
	h.AWS(t, "ec2", "terminate-instances", "--instance-ids", id2)
	f.vmWaitState(t, id2, "terminated")
	h.AWS(t, "ec2", "deregister-image", "--image-id", ami)
	if _, err := d.C.InspectVolume(amiVol); err == nil {
		t.Errorf("image volume %s remains after deregistering", amiVol)
	}
	// The restored-and-terminated instance's volumes were deleted with it (DeleteOnTermination).
	if _, err := d.C.InspectVolume("hc-" + rootVol); err == nil {
		t.Errorf("root volume %s remains after terminate", rootVol)
	}
}

// run1 runs a command over SSH and returns its output and error.
func run1(c *ssh.Client, cmd string) (string, error) {
	s, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	out, err := s.CombinedOutput(cmd)
	return string(out), err
}

// vmExec runs a command in a container and returns its output.
func vmExec(t *testing.T, d *runtime.Docker, cid, cmd string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := d.Exec(ctx, cid, []string{"/bin/sh", "-c", cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Stdout + res.Stderr
}
