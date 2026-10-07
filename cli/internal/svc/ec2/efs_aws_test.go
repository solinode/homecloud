package ec2_test

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/dockertest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

func strOf(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// efsNoDocker serves EFS without containers: enough for errors and IAM.
func efsNoDocker(t *testing.T) *awstest.Harness {
	t.Helper()
	h := awstest.New(t)
	ec2.New(h.Env, vpc.New(h.Env)).RegisterEFSAWS()
	return h
}

// efsLive serves EFS on real Docker with the default VPC, or skips.
func efsLive(t *testing.T) (*awstest.Harness, *vpc.Service) {
	t.Helper()
	h := awstest.New(t)
	h.Env.Docker = dockertest.Start(t)
	v := vpc.New(h.Env)
	dockertest.DefaultVPC(t, v.EnsureDefault)
	t.Cleanup(func() {
		for _, x := range v.List() {
			_ = v.DeleteVPC(x.ID)
		}
	})
	ec2.New(h.Env, v).RegisterEFSAWS()
	return h, v
}

func TestEFSAWSLifecycle(t *testing.T) {
	h, v := efsLive(t)
	// Two subnets in different availability zones.
	subs := v.Subnets()
	var a, b vpc.Subnet
	for _, s := range subs {
		switch {
		case a.ID == "":
			a = s
		case s.AvailabilityZone != a.AvailabilityZone && b.ID == "":
			b = s
		}
	}
	if a.ID == "" || b.ID == "" {
		t.Fatalf("need two subnets in different zones: %v", subs)
	}
	sg := v.DefaultSecurityGroup(a.VpcID)

	fs := h.AWSJSON(t, "efs", "create-file-system", "--creation-token", "tok-1", "--performance-mode", "generalPurpose",
		"--tags", "Key=Name,Value=shared-data", "Key=env,Value=qa")
	id := strOf(fs, "FileSystemId")
	t.Cleanup(func() {
		var left struct {
			MountTargets []struct{ MountTargetId string }
		}
		if o, err := h.AWSErr(t, "efs", "describe-mount-targets", "--file-system-id", id); err == nil && json.Unmarshal([]byte(o), &left) == nil {
			for _, m := range left.MountTargets {
				_, _ = h.AWSErr(t, "efs", "delete-mount-target", "--mount-target-id", m.MountTargetId)
			}
		}
		_, _ = h.AWSErr(t, "efs", "delete-file-system", "--file-system-id", id)
	})
	if !strings.HasPrefix(id, "fs-") || fs["LifeCycleState"] != "available" || fs["Name"] != "shared-data" || fs["CreationToken"] != "tok-1" ||
		fs["PerformanceMode"] != "generalPurpose" || fs["ThroughputMode"] != "bursting" || fs["NumberOfMountTargets"] != float64(0) ||
		fs["FileSystemArn"] != "arn:aws:elasticfilesystem:us-east-1:"+h.Env.AccountID+":file-system/"+id || fs["OwnerId"] != h.Env.AccountID {
		t.Fatalf("create: %v", fs)
	}
	if _, err := h.Env.Docker.C.InspectVolume("hc-" + id); err != nil {
		t.Fatalf("backing volume: %v", err)
	}

	// The creation token makes creation idempotent: a repeat is refused with the existing ID.
	if o, err := h.AWSErr(t, "efs", "create-file-system", "--creation-token", "tok-1"); err == nil || !strings.Contains(o, "FileSystemAlreadyExists") {
		t.Fatalf("repeat token: %v %s", err, o)
	}
	if l := h.AWSJSON(t, "efs", "describe-file-systems")["FileSystems"].([]any); len(l) != 1 {
		t.Fatalf("the repeat must not create a file system: %v", l)
	}
	if l := h.AWSJSON(t, "efs", "describe-file-systems", "--creation-token", "tok-1")["FileSystems"].([]any); len(l) != 1 {
		t.Fatalf("by token: %v", l)
	}
	d := h.AWSJSON(t, "efs", "describe-file-systems", "--file-system-id", id)["FileSystems"].([]any)[0].(map[string]any)
	if tags := d["Tags"].([]any); len(tags) != 2 {
		t.Fatalf("tags: %v", d)
	}
	if o, err := h.AWSErr(t, "efs", "describe-file-systems", "--file-system-id", "fs-0123456789abcdef0"); err == nil || !strings.Contains(o, "FileSystemNotFound") {
		t.Fatalf("describe unknown: %v %s", err, o)
	}

	// Throughput.
	if o, err := h.AWSErr(t, "efs", "update-file-system", "--file-system-id", id, "--throughput-mode", "provisioned"); err == nil || !strings.Contains(o, "BadRequest") {
		t.Fatalf("provisioned without throughput: %v %s", err, o)
	}
	u := h.AWSJSON(t, "efs", "update-file-system", "--file-system-id", id, "--throughput-mode", "provisioned", "--provisioned-throughput-in-mibps", "10")
	if u["ThroughputMode"] != "provisioned" || u["ProvisionedThroughputInMibps"] != float64(10) {
		t.Fatalf("update: %v", u)
	}

	// Mount targets take an address from their subnet and one per zone.
	mt := h.AWSJSON(t, "efs", "create-mount-target", "--file-system-id", id, "--subnet-id", a.ID, "--security-groups", sg)
	mtID := strOf(mt, "MountTargetId")
	if !strings.HasPrefix(mtID, "fsmt-") || mt["LifeCycleState"] != "available" || mt["SubnetId"] != a.ID || mt["AvailabilityZoneName"] != a.AvailabilityZone ||
		!strings.HasPrefix(strOf(mt, "NetworkInterfaceId"), "eni-") {
		t.Fatalf("mount target: %v", mt)
	}
	if ip, err := netip.ParseAddr(strOf(mt, "IpAddress")); err != nil || !netip.MustParsePrefix(a.CIDR).Contains(ip) {
		t.Fatalf("mount target address %v is not in %s", mt["IpAddress"], a.CIDR)
	}
	if o, err := h.AWSErr(t, "efs", "create-mount-target", "--file-system-id", id, "--subnet-id", a.ID); err == nil || !strings.Contains(o, "MountTargetConflict") {
		t.Fatalf("second target in the zone: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "efs", "create-mount-target", "--file-system-id", id, "--subnet-id", "subnet-0123456789abcdef0"); err == nil || !strings.Contains(o, "SubnetNotFound") {
		t.Fatalf("unknown subnet: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "efs", "create-mount-target", "--file-system-id", id, "--subnet-id", b.ID, "--security-groups", "sg-0123456789abcdef0"); err == nil || !strings.Contains(o, "SecurityGroupNotFound") {
		t.Fatalf("unknown group: %v %s", err, o)
	}
	mt2 := h.AWSJSON(t, "efs", "create-mount-target", "--file-system-id", id, "--subnet-id", b.ID)
	if mt2["IpAddress"] == mt["IpAddress"] {
		t.Fatalf("addresses must differ: %v %v", mt, mt2)
	}
	// With no groups given the VPC's default group applies.
	if g := h.AWSJSON(t, "efs", "describe-mount-target-security-groups", "--mount-target-id", strOf(mt2, "MountTargetId"))["SecurityGroups"].([]any); len(g) != 1 || g[0] != sg {
		t.Fatalf("default group: %v", g)
	}
	if l := h.AWSJSON(t, "efs", "describe-mount-targets", "--file-system-id", id)["MountTargets"].([]any); len(l) != 2 {
		t.Fatalf("mount targets: %v", l)
	}
	if l := h.AWSJSON(t, "efs", "describe-mount-targets", "--mount-target-id", mtID)["MountTargets"].([]any); len(l) != 1 {
		t.Fatalf("by id: %v", l)
	}
	if n := h.AWSJSON(t, "efs", "describe-file-systems", "--file-system-id", id)["FileSystems"].([]any)[0].(map[string]any)["NumberOfMountTargets"]; n != float64(2) {
		t.Fatalf("NumberOfMountTargets: %v", n)
	}
	if o, err := h.AWSErr(t, "efs", "describe-mount-targets"); err == nil || !strings.Contains(o, "BadRequest") {
		t.Fatalf("describe without a selector: %v %s", err, o)
	}
	// The default group is in use by the targets.
	if _, err := h.AWSErr(t, "efs", "modify-mount-target-security-groups", "--mount-target-id", mtID, "--security-groups", sg); err != nil {
		t.Fatalf("modify groups: %v", err)
	}

	// A file system with mount targets cannot be deleted.
	if o, err := h.AWSErr(t, "efs", "delete-file-system", "--file-system-id", id); err == nil || !strings.Contains(o, "FileSystemInUse") {
		t.Fatalf("delete with mount targets: %v %s", err, o)
	}

	// Access points: stored with their POSIX identity and root directory.
	ap := h.AWSJSON(t, "efs", "create-access-point", "--file-system-id", id, "--client-token", "ap-tok", "--tags", "Key=Name,Value=app",
		"--posix-user", "Uid=1000,Gid=1001", "--root-directory", "Path=/app,CreationInfo={OwnerUid=1000,OwnerGid=1001,Permissions=755}")
	apID := strOf(ap, "AccessPointId")
	root := ap["RootDirectory"].(map[string]any)
	pu := ap["PosixUser"].(map[string]any)
	if !strings.HasPrefix(apID, "fsap-") || ap["AccessPointArn"] != "arn:aws:elasticfilesystem:us-east-1:"+h.Env.AccountID+":access-point/"+apID ||
		root["Path"] != "/app" || root["CreationInfo"].(map[string]any)["Permissions"] != "755" || pu["Uid"] != float64(1000) || pu["Gid"] != float64(1001) ||
		ap["Name"] != "app" || ap["FileSystemId"] != id || ap["LifeCycleState"] != "available" {
		t.Fatalf("access point: %v", ap)
	}
	if o, err := h.AWSErr(t, "efs", "create-access-point", "--file-system-id", id, "--client-token", "ap-tok"); err == nil || !strings.Contains(o, "AccessPointAlreadyExists") {
		t.Fatalf("repeat client token: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "efs", "create-access-point", "--file-system-id", "fs-0123456789abcdef0"); err == nil || !strings.Contains(o, "FileSystemNotFound") {
		t.Fatalf("access point on unknown file system: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "efs", "create-access-point", "--file-system-id", id, "--root-directory", "Path=relative"); err == nil || !strings.Contains(o, "BadRequest") {
		t.Fatalf("relative path: %v %s", err, o)
	}
	if l := h.AWSJSON(t, "efs", "describe-access-points", "--file-system-id", id)["AccessPoints"].([]any); len(l) != 1 {
		t.Fatalf("access points: %v", l)
	}
	if l := h.AWSJSON(t, "efs", "describe-access-points", "--access-point-id", apID)["AccessPoints"].([]any); len(l) != 1 {
		t.Fatalf("access point by id: %v", l)
	}
	if o, err := h.AWSErr(t, "efs", "describe-access-points", "--access-point-id", "fsap-0123456789abcdef0"); err == nil || !strings.Contains(o, "AccessPointNotFound") {
		t.Fatalf("unknown access point: %v %s", err, o)
	}

	// Tags on the file system and on the access point.
	h.AWS(t, "efs", "tag-resource", "--resource-id", id, "--tags", "Key=team,Value=storage", "Key=Name,Value=renamed")
	tl := h.AWSJSON(t, "efs", "list-tags-for-resource", "--resource-id", id)["Tags"].([]any)
	if len(tl) != 3 {
		t.Fatalf("tags: %v", tl)
	}
	if d := h.AWSJSON(t, "efs", "describe-file-systems", "--file-system-id", id)["FileSystems"].([]any)[0].(map[string]any); d["Name"] != "renamed" {
		t.Fatalf("the Name tag names the file system: %v", d)
	}
	h.AWS(t, "efs", "untag-resource", "--resource-id", id, "--tag-keys", "env", "team")
	if tl := h.AWSJSON(t, "efs", "list-tags-for-resource", "--resource-id", id)["Tags"].([]any); len(tl) != 1 {
		t.Fatalf("tags after untag: %v", tl)
	}
	h.AWS(t, "efs", "tag-resource", "--resource-id", apID, "--tags", "Key=k,Value=v")
	if o := h.AWS(t, "efs", "list-tags-for-resource", "--resource-id", apID); !strings.Contains(o, `"k"`) || !strings.Contains(o, `"app"`) {
		t.Fatalf("access point tags: %s", o)
	}
	if o, err := h.AWSErr(t, "efs", "tag-resource", "--resource-id", "fs-0123456789abcdef0", "--tags", "Key=a,Value=b"); err == nil || !strings.Contains(o, "FileSystemNotFound") {
		t.Fatalf("tag unknown: %v %s", err, o)
	}

	// Lifecycle, backup and policy are stored.
	h.AWS(t, "efs", "put-lifecycle-configuration", "--file-system-id", id, "--lifecycle-policies", "TransitionToIA=AFTER_30_DAYS")
	if o := h.AWS(t, "efs", "describe-lifecycle-configuration", "--file-system-id", id); !strings.Contains(o, "AFTER_30_DAYS") {
		t.Fatalf("lifecycle: %s", o)
	}
	if b := h.AWSJSON(t, "efs", "describe-backup-policy", "--file-system-id", id)["BackupPolicy"].(map[string]any); b["Status"] != "DISABLED" {
		t.Fatalf("backup policy: %v", b)
	}
	h.AWS(t, "efs", "put-backup-policy", "--file-system-id", id, "--backup-policy", "Status=ENABLED")
	if b := h.AWSJSON(t, "efs", "describe-backup-policy", "--file-system-id", id)["BackupPolicy"].(map[string]any); b["Status"] != "ENABLED" {
		t.Fatalf("backup policy after put: %v", b)
	}
	if o, err := h.AWSErr(t, "efs", "describe-file-system-policy", "--file-system-id", id); err == nil || !strings.Contains(o, "PolicyNotFound") {
		t.Fatalf("policy before put: %v %s", err, o)
	}
	h.AWS(t, "efs", "put-file-system-policy", "--file-system-id", id, "--policy", `{"Version":"2012-10-17","Statement":[]}`)
	if o := h.AWS(t, "efs", "describe-file-system-policy", "--file-system-id", id); !strings.Contains(o, "2012-10-17") {
		t.Fatalf("policy: %s", o)
	}

	// Tear down: access point, mount targets, then the file system and its volume.
	h.AWS(t, "efs", "delete-access-point", "--access-point-id", apID)
	if o, err := h.AWSErr(t, "efs", "delete-access-point", "--access-point-id", apID); err == nil || !strings.Contains(o, "AccessPointNotFound") {
		t.Fatalf("delete twice: %v %s", err, o)
	}
	h.AWS(t, "efs", "delete-mount-target", "--mount-target-id", mtID)
	h.AWS(t, "efs", "delete-mount-target", "--mount-target-id", strOf(mt2, "MountTargetId"))
	if o, err := h.AWSErr(t, "efs", "delete-mount-target", "--mount-target-id", mtID); err == nil || !strings.Contains(o, "MountTargetNotFound") {
		t.Fatalf("delete mount target twice: %v %s", err, o)
	}
	// A deleted mount target frees its address, so the next one reuses it.
	mt3 := h.AWSJSON(t, "efs", "create-mount-target", "--file-system-id", id, "--subnet-id", a.ID)
	if mt3["IpAddress"] != mt["IpAddress"] {
		t.Fatalf("the released address should be reused: %v vs %v", mt3["IpAddress"], mt["IpAddress"])
	}
	h.AWS(t, "efs", "delete-mount-target", "--mount-target-id", strOf(mt3, "MountTargetId"))
	h.AWS(t, "efs", "delete-file-system", "--file-system-id", id)
	if o, err := h.AWSErr(t, "efs", "describe-file-systems", "--file-system-id", id); err == nil || !strings.Contains(o, "FileSystemNotFound") {
		t.Fatalf("describe deleted: %v %s", err, o)
	}
	if _, err := h.Env.Docker.C.InspectVolume("hc-" + id); err == nil {
		t.Fatal("the volume must be removed with the file system")
	}
	if o, err := h.AWSErr(t, "efs", "delete-file-system", "--file-system-id", id); err == nil || !strings.Contains(o, "FileSystemNotFound") {
		t.Fatalf("delete twice: %v %s", err, o)
	}
}

func TestEFSAWSBoto3(t *testing.T) {
	h, v := efsLive(t)
	subnet := v.Subnets()[0].ID
	out := h.Python(t, `
efs = boto3.client("efs")
fs = efs.create_file_system(CreationToken="py-1", Tags=[{"Key": "Name", "Value": "py"}], Encrypted=True)
fid = fs["FileSystemId"]
print(fs["LifeCycleState"], fs["Name"], fs["Encrypted"])
try:
    efs.create_file_system(CreationToken="py-1")
except efs.exceptions.FileSystemAlreadyExists as e:
    print("dup", e.response["Error"]["Code"])
mt = efs.create_mount_target(FileSystemId=fid, SubnetId="`+subnet+`")
print(len(efs.describe_mount_targets(FileSystemId=fid)["MountTargets"]))
try:
    efs.delete_file_system(FileSystemId=fid)
except efs.exceptions.FileSystemInUse as e:
    print("inuse", e.response["Error"]["Code"])
efs.delete_mount_target(MountTargetId=mt["MountTargetId"])
efs.delete_file_system(FileSystemId=fid)
try:
    efs.describe_file_systems(FileSystemId=fid)
except efs.exceptions.FileSystemNotFound as e:
    print("gone", e.response["Error"]["Code"])
`)
	for _, want := range []string{"available py True", "dup FileSystemAlreadyExists", "1\n", "inuse FileSystemInUse", "gone FileSystemNotFound"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestEFSAWSIAM(t *testing.T) {
	h := efsNoDocker(t)
	akid, secret := h.User(t, "ro", "ReadOnlyAccess")
	if o, err := h.AWSAs(t, akid, secret, "", "efs", "describe-file-systems"); err != nil {
		t.Fatalf("read-only describe: %v %s", err, o)
	}
	for _, args := range [][]string{
		{"efs", "create-file-system", "--creation-token", "t"},
		{"efs", "create-file-system", "--creation-token", "t", "--tags", "Key=a,Value=b"},
		{"efs", "update-file-system", "--file-system-id", "fs-0123456789abcdef1", "--throughput-mode", "elastic"},
		{"efs", "delete-file-system", "--file-system-id", "fs-0123456789abcdef1"}, // authorized before existence
		{"efs", "create-mount-target", "--file-system-id", "fs-0123456789abcdef1", "--subnet-id", "subnet-0123456789abcdef1"},
		{"efs", "delete-mount-target", "--mount-target-id", "fsmt-0123456789abcdef1"},
		{"efs", "create-access-point", "--file-system-id", "fs-0123456789abcdef1"},
		{"efs", "delete-access-point", "--access-point-id", "fsap-0123456789abcdef1"},
		{"efs", "tag-resource", "--resource-id", "fs-0123456789abcdef1", "--tags", "Key=a,Value=b"},
		{"efs", "untag-resource", "--resource-id", "fs-0123456789abcdef1", "--tag-keys", "a"},
		{"efs", "put-lifecycle-configuration", "--file-system-id", "fs-0123456789abcdef1", "--lifecycle-policies", "TransitionToIA=AFTER_7_DAYS"},
		{"efs", "put-file-system-policy", "--file-system-id", "fs-0123456789abcdef1", "--policy", "{}"},
	} {
		if o, err := h.AWSAs(t, akid, secret, "", args...); err == nil || !strings.Contains(o, "AccessDenied") {
			t.Fatalf("%v as read-only: %v %s", args, err, o)
		}
	}
	// Denials name the action and resource.
	o, _ := h.AWSAs(t, akid, secret, "", "efs", "delete-file-system", "--file-system-id", "fs-0123456789abcdef1")
	if !strings.Contains(o, "elasticfilesystem:DeleteFileSystem") || !strings.Contains(o, "file-system/fs-0123456789abcdef1") {
		t.Fatalf("denial message: %s", o)
	}

	// A policy scoped to one file system ARN.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "one-fs", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "elasticfilesystem:*", "Resource": "arn:aws:elasticfilesystem:us-east-1:" + h.Env.AccountID + ":file-system/fs-0123456789abcdef2"}}}})
	akid2, secret2 := h.User(t, "scoped", "one-fs")
	if o, err := h.AWSAs(t, akid2, secret2, "", "efs", "describe-file-systems", "--file-system-id", "fs-0123456789abcdef2"); err == nil || !strings.Contains(o, "FileSystemNotFound") {
		t.Fatalf("scoped, allowed id: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, akid2, secret2, "", "efs", "describe-file-systems", "--file-system-id", "fs-0123456789abcdef3"); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("scoped, other id: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, akid2, secret2, "", "efs", "delete-file-system", "--file-system-id", "fs-0123456789abcdef3"); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("scoped delete, other id: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, akid2, secret2, "", "efs", "delete-mount-target", "--mount-target-id", "fsmt-0123456789abcdef0"); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("scoped mount target lookup must not reveal existence: %v %s", err, o)
	}
	found := false
	for _, a := range h.AuditLog() {
		if strings.HasPrefix(a, "elasticfilesystem:DescribeFileSystems ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry: %v", h.AuditLog())
	}
}

func TestEFSAWSErrors(t *testing.T) {
	h := efsNoDocker(t)
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"describe-file-systems", "--file-system-id", "fs-0123456789abcdef0"}, "FileSystemNotFound"},
		{[]string{"delete-file-system", "--file-system-id", "fs-0123456789abcdef0"}, "FileSystemNotFound"},
		{[]string{"describe-mount-targets", "--mount-target-id", "fsmt-0123456789abcdef0"}, "MountTargetNotFound"},
		{[]string{"describe-access-points", "--access-point-id", "fsap-0123456789abcdef0"}, "AccessPointNotFound"},
		{[]string{"describe-lifecycle-configuration", "--file-system-id", "fs-0123456789abcdef0"}, "FileSystemNotFound"},
		{[]string{"list-tags-for-resource", "--resource-id", "fs-0123456789abcdef0"}, "FileSystemNotFound"},
		{[]string{"list-tags-for-resource", "--resource-id", "junk"}, "BadRequest"},
		{[]string{"create-file-system", "--creation-token", "x", "--performance-mode", "turbo"}, "BadRequest"},
		{[]string{"create-file-system", "--creation-token", "x", "--throughput-mode", "provisioned"}, "BadRequest"},
	} {
		if o, err := h.AWSErr(t, append([]string{"efs"}, c.args...)...); err == nil || !strings.Contains(o, c.code) {
			t.Errorf("%v: want %s, got %v %s", c.args, c.code, err, o)
		}
	}
}
