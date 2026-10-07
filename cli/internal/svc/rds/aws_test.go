package rds_test

import (
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/dockertest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/rds"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// noDocker builds an RDS endpoint without containers: enough for parameter
// groups, engine metadata, errors and IAM.
func noDocker(t *testing.T) (*awstest.Harness, *vpc.Service) {
	t.Helper()
	h := awstest.New(t)
	v := vpc.New(h.Env)
	rds.New(h.Env, v, h.Secrets).RegisterAWS()
	return h, v
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func TestAWSParameterGroupsAndMetadata(t *testing.T) {
	h, _ := noDocker(t)

	pg := h.AWSJSON(t, "rds", "create-db-parameter-group", "--db-parameter-group-name", "Tuned", "--db-parameter-group-family", "postgres17",
		"--description", "tuned", "--tags", "Key=env,Value=qa")["DBParameterGroup"].(map[string]any)
	if pg["DBParameterGroupName"] != "tuned" || !strings.HasSuffix(str(pg, "DBParameterGroupArn"), ":pg:tuned") || pg["DBParameterGroupFamily"] != "postgres17" {
		t.Fatalf("create: %v", pg)
	}
	if o, err := h.AWSErr(t, "rds", "create-db-parameter-group", "--db-parameter-group-name", "tuned", "--db-parameter-group-family", "postgres17", "--description", "x"); err == nil || !strings.Contains(o, "DBParameterGroupAlreadyExists") {
		t.Fatalf("duplicate: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "rds", "create-db-parameter-group", "--db-parameter-group-name", "bad", "--db-parameter-group-family", "oracle19", "--description", "x"); err == nil || !strings.Contains(o, "InvalidParameterValue") {
		t.Fatalf("family: %v %s", err, o)
	}
	h.AWS(t, "rds", "modify-db-parameter-group", "--db-parameter-group-name", "tuned",
		"--parameters", "ParameterName=log_statement,ParameterValue=all,ApplyMethod=immediate")
	ps := h.AWSJSON(t, "rds", "describe-db-parameters", "--db-parameter-group-name", "tuned", "--source", "user")["Parameters"].([]any)
	if len(ps) != 1 || ps[0].(map[string]any)["ParameterValue"] != "all" {
		t.Fatalf("user parameters: %v", ps)
	}
	if o := h.AWS(t, "rds", "describe-db-parameters", "--db-parameter-group-name", "default.postgres17"); !strings.Contains(o, "max_connections") {
		t.Fatalf("default parameters: %s", o)
	}
	h.AWS(t, "rds", "reset-db-parameter-group", "--db-parameter-group-name", "tuned", "--reset-all-parameters")
	if ps := h.AWSJSON(t, "rds", "describe-db-parameters", "--db-parameter-group-name", "tuned", "--source", "user")["Parameters"].([]any); len(ps) != 0 {
		t.Fatalf("after reset: %v", ps)
	}
	if o := h.AWS(t, "rds", "describe-db-parameter-groups"); !strings.Contains(o, `"tuned"`) {
		t.Fatalf("describe: %s", o)
	}
	if o, err := h.AWSErr(t, "rds", "modify-db-parameter-group", "--db-parameter-group-name", "default.postgres17", "--parameters", "ParameterName=a,ParameterValue=b,ApplyMethod=immediate"); err == nil || !strings.Contains(o, "InvalidDBParameterGroupState") {
		t.Fatalf("modify default: %v %s", err, o)
	}

	// Tags on a parameter group.
	arn := str(pg, "DBParameterGroupArn")
	h.AWS(t, "rds", "add-tags-to-resource", "--resource-name", arn, "--tags", "Key=team,Value=db")
	tl := h.AWSJSON(t, "rds", "list-tags-for-resource", "--resource-name", arn)["TagList"].([]any)
	if len(tl) != 2 {
		t.Fatalf("tags: %v", tl)
	}
	h.AWS(t, "rds", "remove-tags-from-resource", "--resource-name", arn, "--tag-keys", "env")
	tl = h.AWSJSON(t, "rds", "list-tags-for-resource", "--resource-name", arn)["TagList"].([]any)
	if len(tl) != 1 || tl[0].(map[string]any)["Key"] != "team" {
		t.Fatalf("tags after remove: %v", tl)
	}

	h.AWS(t, "rds", "delete-db-parameter-group", "--db-parameter-group-name", "tuned")
	if o, err := h.AWSErr(t, "rds", "describe-db-parameter-groups", "--db-parameter-group-name", "tuned"); err == nil || !strings.Contains(o, "DBParameterGroupNotFound") {
		t.Fatalf("describe deleted: %v %s", err, o)
	}

	// Engine metadata.
	if o := h.AWS(t, "rds", "describe-db-engine-versions", "--engine", "postgres"); !strings.Contains(o, `"postgres17"`) || strings.Contains(o, "mysql") {
		t.Fatalf("engine versions: %s", o)
	}
	if o := h.AWS(t, "rds", "describe-orderable-db-instance-options", "--engine", "mysql"); !strings.Contains(o, "db.t3.micro") {
		t.Fatalf("orderable: %s", o)
	}

	// Errors for missing things.
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"describe-db-instances", "--db-instance-identifier", "nope"}, "DBInstanceNotFound"},
		{[]string{"delete-db-instance", "--db-instance-identifier", "nope", "--skip-final-snapshot"}, "DBInstanceNotFound"},
		{[]string{"describe-db-snapshots", "--db-snapshot-identifier", "nope"}, "DBSnapshotNotFound"},
		{[]string{"delete-db-snapshot", "--db-snapshot-identifier", "nope"}, "DBSnapshotNotFound"},
		{[]string{"describe-db-subnet-groups", "--db-subnet-group-name", "nope"}, "DBSubnetGroupNotFoundFault"},
		{[]string{"create-db-instance", "--db-instance-identifier", "x", "--db-instance-class", "db.t3.micro", "--engine", "oracle-ee"}, "InvalidParameterValue"},
		{[]string{"create-db-instance", "--db-instance-identifier", "x", "--db-instance-class", "db.t3.micro", "--engine", "postgres", "--port", "6000", "--master-user-password", "password123"}, "InvalidParameterValue"},
		{[]string{"list-tags-for-resource", "--resource-name", "arn:aws:rds:us-east-1:123456789012:db:nope"}, "DBInstanceNotFound"},
	} {
		if o, err := h.AWSErr(t, append([]string{"rds"}, c.args...)...); err == nil || !strings.Contains(o, c.code) {
			t.Errorf("%v: want %s, got %v %s", c.args, c.code, err, o)
		}
	}
}

func TestAWSIAM(t *testing.T) {
	h, _ := noDocker(t)
	h.AWS(t, "rds", "create-db-parameter-group", "--db-parameter-group-name", "mine", "--db-parameter-group-family", "mysql8.4", "--description", "d")
	akid, secret := h.User(t, "ro", "ReadOnlyAccess")
	if o, err := h.AWSAs(t, akid, secret, "", "rds", "describe-db-parameter-groups"); err != nil {
		t.Fatalf("read-only describe: %v %s", err, o)
	}
	for _, args := range [][]string{
		{"rds", "create-db-instance", "--db-instance-identifier", "x", "--db-instance-class", "db.t3.micro", "--engine", "postgres", "--master-user-password", "password123"},
		{"rds", "delete-db-instance", "--db-instance-identifier", "unknown", "--skip-final-snapshot"}, // authorized before existence
		{"rds", "reboot-db-instance", "--db-instance-identifier", "unknown"},
		{"rds", "create-db-snapshot", "--db-instance-identifier", "unknown", "--db-snapshot-identifier", "s"},
		{"rds", "delete-db-parameter-group", "--db-parameter-group-name", "mine"},
		{"rds", "add-tags-to-resource", "--resource-name", "arn:aws:rds:us-east-1:" + h.Env.AccountID + ":pg:mine", "--tags", "Key=a,Value=b"},
	} {
		if o, err := h.AWSAs(t, akid, secret, "", args...); err == nil || !strings.Contains(o, "AccessDenied") {
			t.Fatalf("%v as read-only: %v %s", args, err, o)
		}
	}
	// A policy scoped to one instance ARN.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "one-db", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "rds:*", "Resource": "arn:aws:rds:us-east-1:" + h.Env.AccountID + ":db:allowed"}}}})
	akid2, secret2 := h.User(t, "scoped", "one-db")
	if o, err := h.AWSAs(t, akid2, secret2, "", "rds", "describe-db-instances", "--db-instance-identifier", "allowed"); err == nil || !strings.Contains(o, "DBInstanceNotFound") {
		t.Fatalf("scoped, allowed id: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, akid2, secret2, "", "rds", "describe-db-instances", "--db-instance-identifier", "other"); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("scoped, other id: %v %s", err, o)
	}
	found := false
	for _, a := range h.AuditLog() {
		if strings.HasPrefix(a, "rds:CreateDBInstance ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry: %v", h.AuditLog())
	}
}

// withDocker adds real containers and the default VPC; the test skips without Docker.
func withDocker(t *testing.T) (*awstest.Harness, *vpc.Service, []string) {
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
	rds.New(h.Env, v, h.Secrets).RegisterAWS()
	h.Secrets.RegisterAWS()
	var subs []string
	for _, s := range v.Subnets() {
		subs = append(subs, s.ID)
	}
	if len(subs) < 2 {
		t.Fatalf("subnets: %v", subs)
	}
	return h, v, subs
}

func waitStatus(t *testing.T, h *awstest.Harness, id, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		d := h.AWSJSON(t, "rds", "describe-db-instances", "--db-instance-identifier", id)["DBInstances"].([]any)[0].(map[string]any)
		if d["DBInstanceStatus"] == want {
			return d
		}
		if d["DBInstanceStatus"] == "failed" || time.Now().After(deadline) {
			t.Fatalf("%s: status %v, want %s: %v", id, d["DBInstanceStatus"], want, d)
		}
		time.Sleep(time.Second)
	}
}

func TestAWSSubnetGroups(t *testing.T) {
	h, _, subs := withDocker(t)
	g := h.AWSJSON(t, "rds", "create-db-subnet-group", "--db-subnet-group-name", "apps", "--db-subnet-group-description", "d",
		"--subnet-ids", subs[0], subs[1], "--tags", "Key=a,Value=b")["DBSubnetGroup"].(map[string]any)
	if g["DBSubnetGroupName"] != "apps" || len(g["Subnets"].([]any)) != 2 || !strings.HasSuffix(str(g, "DBSubnetGroupArn"), ":subgrp:apps") {
		t.Fatalf("group: %v", g)
	}
	if o, err := h.AWSErr(t, "rds", "create-db-subnet-group", "--db-subnet-group-name", "apps", "--db-subnet-group-description", "d", "--subnet-ids", subs[0]); err == nil || !strings.Contains(o, "DBSubnetGroupAlreadyExists") {
		t.Fatalf("duplicate: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "rds", "create-db-subnet-group", "--db-subnet-group-name", "bad", "--db-subnet-group-description", "d", "--subnet-ids", "subnet-nope"); err == nil || !strings.Contains(o, "InvalidSubnet") {
		t.Fatalf("bad subnet: %v %s", err, o)
	}
	h.AWS(t, "rds", "modify-db-subnet-group", "--db-subnet-group-name", "apps", "--subnet-ids", subs[0], "--db-subnet-group-description", "changed")
	d := h.AWSJSON(t, "rds", "describe-db-subnet-groups", "--db-subnet-group-name", "apps")["DBSubnetGroups"].([]any)[0].(map[string]any)
	if d["DBSubnetGroupDescription"] != "changed" || len(d["Subnets"].([]any)) != 1 {
		t.Fatalf("modified: %v", d)
	}
	if o := h.AWS(t, "rds", "list-tags-for-resource", "--resource-name", str(g, "DBSubnetGroupArn")); !strings.Contains(o, `"a"`) {
		t.Fatalf("tags: %s", o)
	}
	h.AWS(t, "rds", "delete-db-subnet-group", "--db-subnet-group-name", "apps")
	if o, err := h.AWSErr(t, "rds", "delete-db-subnet-group", "--db-subnet-group-name", "apps"); err == nil || !strings.Contains(o, "DBSubnetGroupNotFoundFault") {
		t.Fatalf("delete twice: %v %s", err, o)
	}
}

func TestAWSInstanceLifecycle(t *testing.T) {
	h, v, subs := withDocker(t)
	h.AWS(t, "rds", "create-db-subnet-group", "--db-subnet-group-name", "apps", "--db-subnet-group-description", "d", "--subnet-ids", subs[0])
	h.AWS(t, "rds", "create-db-parameter-group", "--db-parameter-group-name", "pg17", "--db-parameter-group-family", "postgres17", "--description", "d")
	sg := v.DefaultSecurityGroup(v.Subnets()[0].VpcID)

	// Creation returns immediately in the creating state, with no endpoint yet.
	// The engine's own port is accepted for a private database (Terraform's rds module sends it).
	c := h.AWSJSON(t, "rds", "create-db-instance", "--db-instance-identifier", "Orders", "--db-instance-class", "db.t3.micro", "--engine", "postgres",
		"--engine-version", "17", "--allocated-storage", "20", "--master-username", "app", "--manage-master-user-password", "--db-name", "shop", "--port", "5432", "--preferred-maintenance-window", "Mon:00:00-Mon:03:00",
		"--db-subnet-group-name", "apps", "--db-parameter-group-name", "pg17", "--vpc-security-group-ids", sg,
		"--backup-retention-period", "0", "--tags", "Key=env,Value=qa")["DBInstance"].(map[string]any)
	if c["DBInstanceIdentifier"] != "orders" || c["DBInstanceStatus"] != "creating" || c["Endpoint"] != nil || c["Engine"] != "postgres" {
		t.Fatalf("create: %v", c)
	}
	t.Cleanup(func() {
		_, _ = h.AWSErr(t, "rds", "modify-db-instance", "--db-instance-identifier", "orders", "--no-deletion-protection")
		_, _ = h.AWSErr(t, "rds", "delete-db-instance", "--db-instance-identifier", "orders", "--skip-final-snapshot")
		_, _ = h.AWSErr(t, "rds", "delete-db-instance", "--db-instance-identifier", "orders-copy", "--skip-final-snapshot")
	})
	if !strings.HasSuffix(str(c, "DBInstanceArn"), ":db:orders") || c["DBSubnetGroup"].(map[string]any)["DBSubnetGroupName"] != "apps" {
		t.Fatalf("create details: %v", c)
	}
	if o, err := h.AWSErr(t, "rds", "create-db-instance", "--db-instance-identifier", "orders", "--db-instance-class", "db.t3.micro", "--engine", "postgres", "--master-user-password", "password123"); err == nil || !strings.Contains(o, "DBInstanceAlreadyExists") {
		t.Fatalf("duplicate: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "rds", "delete-db-subnet-group", "--db-subnet-group-name", "apps"); err == nil || !strings.Contains(o, "InvalidDBSubnetGroupStateFault") {
		t.Fatalf("subnet group in use: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "rds", "delete-db-parameter-group", "--db-parameter-group-name", "pg17"); err == nil || !strings.Contains(o, "InvalidDBParameterGroupState") {
		t.Fatalf("parameter group in use: %v %s", err, o)
	}
	// The instance may already be available on a fast host; only a reboot
	// accepted while it is still being created is wrong.
	if o, err := h.AWSErr(t, "rds", "reboot-db-instance", "--db-instance-identifier", "orders"); err != nil && !strings.Contains(o, "InvalidDBInstanceState") {
		t.Fatalf("reboot while creating: %v %s", err, o)
	} else if err == nil && strings.Contains(o, `"creating"`) {
		t.Fatalf("reboot accepted while creating: %s", o)
	}

	d := waitStatus(t, h, "orders", "available")
	ep := d["Endpoint"].(map[string]any)
	if d["PreferredMaintenanceWindow"] != "mon:00:00-mon:03:00" { // AWS lower-cases it
		t.Fatalf("maintenance window %v", d["PreferredMaintenanceWindow"])
	}
	if ep["Port"].(float64) != 5432 || !strings.HasSuffix(str(ep, "Address"), ".internal") {
		t.Fatalf("endpoint: %v", ep)
	}
	ms := d["MasterUserSecret"].(map[string]any)
	if !strings.Contains(str(ms, "SecretArn"), ":secret:rds!orders") {
		t.Fatalf("master secret: %v", ms)
	}
	// The managed secret is a real Secrets Manager secret holding the password.
	sec := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", str(ms, "SecretArn"))
	if !strings.Contains(str(sec, "SecretString"), `"username": "app"`) && !strings.Contains(str(sec, "SecretString"), `"username":"app"`) {
		t.Fatalf("secret: %v", sec)
	}
	if d["DBParameterGroups"].([]any)[0].(map[string]any)["DBParameterGroupName"] != "pg17" || d["VpcSecurityGroups"].([]any)[0].(map[string]any)["VpcSecurityGroupId"] != sg {
		t.Fatalf("groups: %v", d)
	}

	// Filters and listing.
	if l := h.AWSJSON(t, "rds", "describe-db-instances", "--filters", "Name=engine,Values=mysql")["DBInstances"].([]any); len(l) != 0 {
		t.Fatalf("engine filter: %v", l)
	}
	if l := h.AWSJSON(t, "rds", "describe-db-instances", "--filters", "Name=db-instance-id,Values=orders")["DBInstances"].([]any); len(l) != 1 {
		t.Fatalf("id filter: %v", l)
	}

	// Modify: deletion protection blocks delete; tags via ARN.
	m := h.AWSJSON(t, "rds", "modify-db-instance", "--db-instance-identifier", "orders", "--deletion-protection", "--backup-retention-period", "0",
		"--preferred-maintenance-window", "mon:03:00-mon:04:00", "--apply-immediately")["DBInstance"].(map[string]any)
	if m["DeletionProtection"] != true || m["PreferredMaintenanceWindow"] != "mon:03:00-mon:04:00" {
		t.Fatalf("modify: %v", m)
	}
	if o, err := h.AWSErr(t, "rds", "delete-db-instance", "--db-instance-identifier", "orders", "--skip-final-snapshot"); err == nil || !strings.Contains(o, "InvalidParameterCombination") {
		t.Fatalf("delete protected: %v %s", err, o)
	}
	h.AWS(t, "rds", "modify-db-instance", "--db-instance-identifier", "orders", "--no-deletion-protection")
	arn := str(d, "DBInstanceArn")
	h.AWS(t, "rds", "add-tags-to-resource", "--resource-name", arn, "--tags", "Key=team,Value=db")
	if o := h.AWS(t, "rds", "list-tags-for-resource", "--resource-name", arn); !strings.Contains(o, `"team"`) || !strings.Contains(o, `"env"`) {
		t.Fatalf("tags: %s", o)
	}

	// Stop, start, reboot.
	if s := h.AWSJSON(t, "rds", "stop-db-instance", "--db-instance-identifier", "orders")["DBInstance"].(map[string]any); s["DBInstanceStatus"] != "stopped" && s["DBInstanceStatus"] != "stopping" {
		t.Fatalf("stop: %v", s)
	}
	waitStatus(t, h, "orders", "stopped")
	if o, err := h.AWSErr(t, "rds", "stop-db-instance", "--db-instance-identifier", "orders"); err == nil || !strings.Contains(o, "InvalidDBInstanceState") {
		t.Fatalf("stop twice: %v %s", err, o)
	}
	h.AWS(t, "rds", "start-db-instance", "--db-instance-identifier", "orders")
	waitStatus(t, h, "orders", "available")
	h.AWS(t, "rds", "reboot-db-instance", "--db-instance-identifier", "orders")
	waitStatus(t, h, "orders", "available")

	// Snapshots and restore.
	sn := h.AWSJSON(t, "rds", "create-db-snapshot", "--db-instance-identifier", "orders", "--db-snapshot-identifier", "orders-snap", "--tags", "Key=k,Value=v")["DBSnapshot"].(map[string]any)
	if sn["DBSnapshotIdentifier"] != "orders-snap" || sn["SnapshotType"] != "manual" || sn["Engine"] != "postgres" || !strings.HasSuffix(str(sn, "DBSnapshotArn"), ":snapshot:orders-snap") {
		t.Fatalf("snapshot: %v", sn)
	}
	if o, err := h.AWSErr(t, "rds", "create-db-snapshot", "--db-instance-identifier", "orders", "--db-snapshot-identifier", "orders-snap"); err == nil || !strings.Contains(o, "DBSnapshotAlreadyExists") {
		t.Fatalf("duplicate snapshot: %v %s", err, o)
	}
	if l := h.AWSJSON(t, "rds", "describe-db-snapshots", "--db-instance-identifier", "orders", "--snapshot-type", "manual")["DBSnapshots"].([]any); len(l) != 1 {
		t.Fatalf("snapshots: %v", l)
	}
	if o := h.AWS(t, "rds", "list-tags-for-resource", "--resource-name", str(sn, "DBSnapshotArn")); !strings.Contains(o, `"k"`) {
		t.Fatalf("snapshot tags: %s", o)
	}
	r := h.AWSJSON(t, "rds", "restore-db-instance-from-db-snapshot", "--db-instance-identifier", "orders-copy", "--db-snapshot-identifier", "orders-snap",
		"--db-instance-class", "db.t3.micro")["DBInstance"].(map[string]any)
	if r["DBInstanceIdentifier"] != "orders-copy" || r["DBInstanceStatus"] != "creating" {
		t.Fatalf("restore: %v", r)
	}
	waitStatus(t, h, "orders-copy", "available")
	if o, err := h.AWSErr(t, "rds", "restore-db-instance-from-db-snapshot", "--db-instance-identifier", "orders-copy", "--db-snapshot-identifier", "orders-snap"); err == nil || !strings.Contains(o, "DBInstanceAlreadyExists") {
		t.Fatalf("restore over existing: %v %s", err, o)
	}
	h.AWS(t, "rds", "delete-db-instance", "--db-instance-identifier", "orders-copy", "--skip-final-snapshot")

	// Delete with a final snapshot; without one the call is refused.
	if o, err := h.AWSErr(t, "rds", "delete-db-instance", "--db-instance-identifier", "orders"); err == nil || !strings.Contains(o, "InvalidParameterCombination") {
		t.Fatalf("delete without final snapshot: %v %s", err, o)
	}
	del := h.AWSJSON(t, "rds", "delete-db-instance", "--db-instance-identifier", "orders", "--final-db-snapshot-identifier", "orders-final")["DBInstance"].(map[string]any)
	if del["DBInstanceStatus"] != "deleting" {
		t.Fatalf("delete: %v", del)
	}
	if o, err := h.AWSErr(t, "rds", "describe-db-instances", "--db-instance-identifier", "orders"); err == nil || !strings.Contains(o, "DBInstanceNotFound") {
		t.Fatalf("describe deleted: %v %s", err, o)
	}
	if o := h.AWS(t, "rds", "describe-db-snapshots", "--db-snapshot-identifier", "orders-final"); !strings.Contains(o, "orders-final") {
		t.Fatalf("final snapshot: %s", o)
	}
	h.AWS(t, "rds", "delete-db-snapshot", "--db-snapshot-identifier", "orders-final")
	h.AWS(t, "rds", "delete-db-snapshot", "--db-snapshot-identifier", "orders-snap")
	if o, err := h.AWSErr(t, "rds", "describe-db-snapshots", "--db-snapshot-identifier", "orders-snap"); err == nil || !strings.Contains(o, "DBSnapshotNotFound") {
		t.Fatalf("snapshot gone: %v %s", err, o)
	}
}

func TestAWSBoto3(t *testing.T) {
	h, _, _ := withDocker(t)
	out := h.Python(t, `
rds = boto3.client("rds")
r = rds.create_db_instance(DBInstanceIdentifier="py-db", DBInstanceClass="db.t3.micro", Engine="mysql", MasterUsername="admin",
    MasterUserPassword="password123", AllocatedStorage=20, BackupRetentionPeriod=0, Tags=[{"Key": "a", "Value": "b"}])["DBInstance"]
print(r["DBInstanceStatus"], r["Engine"])
rds.get_waiter("db_instance_available").wait(DBInstanceIdentifier="py-db", WaiterConfig={"Delay": 2, "MaxAttempts": 150})
d = rds.describe_db_instances(DBInstanceIdentifier="py-db")["DBInstances"][0]
print(d["DBInstanceStatus"], d["Endpoint"]["Port"], d["TagList"][0]["Key"])
try:
    rds.describe_db_instances(DBInstanceIdentifier="nope")
except rds.exceptions.DBInstanceNotFoundFault as e:
    print("notfound", e.response["Error"]["Code"])
rds.delete_db_instance(DBInstanceIdentifier="py-db", SkipFinalSnapshot=True)
rds.get_waiter("db_instance_deleted").wait(DBInstanceIdentifier="py-db", WaiterConfig={"Delay": 2, "MaxAttempts": 60})
print("deleted")
`)
	for _, want := range []string{"creating mysql", "available 3306 a", "notfound DBInstanceNotFound", "deleted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
