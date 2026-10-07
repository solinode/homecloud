package ec2_test

import (
	"strings"
	"testing"
)

// TestNetworkACLs manages the default ACL as the terraform-aws-modules VPC
// module does (tags, an IPv6 entry), and a custom ACL with a subnet.
func TestNetworkACLs(t *testing.T) {
	h := liveHarness(t)
	vpcID := h.AWSJSON(t, "ec2", "create-vpc", "--cidr-block", "10.32.0.0/16")["Vpc"].(map[string]any)["VpcId"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "delete-vpc", "--vpc-id", vpcID) })
	sn := h.AWSJSON(t, "ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.32.1.0/24")["Subnet"].(map[string]any)["SubnetId"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "delete-subnet", "--subnet-id", sn) })

	acls := func(args ...string) []any {
		return h.AWSJSON(t, append([]string{"ec2", "describe-network-acls"}, args...)...)["NetworkAcls"].([]any)
	}
	def := acls("--filters", "Name=vpc-id,Values="+vpcID, "Name=default,Values=true")
	if len(def) != 1 {
		t.Fatalf("default ACL: %v", def)
	}
	d := def[0].(map[string]any)
	defID := d["NetworkAclId"].(string)
	assoc := d["Associations"].([]any)[0].(map[string]any)
	if len(d["Entries"].([]any)) != 4 || assoc["SubnetId"] != sn {
		t.Fatalf("default ACL: %v", d)
	}
	h.AWS(t, "ec2", "create-tags", "--resources", defID, "--tags", "Key=Name,Value=default")
	h.AWS(t, "ec2", "create-network-acl-entry", "--network-acl-id", defID, "--ingress", "--rule-number", "101", "--protocol", "-1",
		"--rule-action", "allow", "--ipv6-cidr-block", "::/0")
	if o, err := h.AWSErr(t, "ec2", "create-network-acl-entry", "--network-acl-id", defID, "--ingress", "--rule-number", "101", "--protocol", "-1",
		"--rule-action", "allow", "--cidr-block", "0.0.0.0/0"); err == nil || !strings.Contains(o, "NetworkAclEntryAlreadyExists") {
		t.Fatalf("duplicate entry: %v %s", err, o)
	}
	d = acls("--network-acl-ids", defID)[0].(map[string]any)
	if len(d["Entries"].([]any)) != 5 || len(d["Tags"].([]any)) != 1 {
		t.Fatalf("after changes: %v", d)
	}

	custom := h.AWSJSON(t, "ec2", "create-network-acl", "--vpc-id", vpcID)["NetworkAcl"].(map[string]any)["NetworkAclId"].(string)
	h.AWS(t, "ec2", "create-network-acl-entry", "--network-acl-id", custom, "--egress", "--rule-number", "100", "--protocol", "tcp",
		"--port-range", "From=443,To=443", "--rule-action", "allow", "--cidr-block", "0.0.0.0/0")
	h.AWS(t, "ec2", "replace-network-acl-entry", "--network-acl-id", custom, "--egress", "--rule-number", "100", "--protocol", "tcp",
		"--port-range", "From=80,To=443", "--rule-action", "allow", "--cidr-block", "0.0.0.0/0")
	newAssoc := h.AWSJSON(t, "ec2", "replace-network-acl-association", "--association-id", assoc["NetworkAclAssociationId"].(string),
		"--network-acl-id", custom)["NewAssociationId"].(string)
	c := acls("--filters", "Name=association.subnet-id,Values="+sn)
	if len(c) != 1 || c[0].(map[string]any)["NetworkAclId"] != custom {
		t.Fatalf("subnet's ACL: %v", c)
	}
	if o, err := h.AWSErr(t, "ec2", "delete-network-acl", "--network-acl-id", custom); err == nil || !strings.Contains(o, "DependencyViolation") {
		t.Fatalf("delete an ACL in use: %v %s", err, o)
	}
	h.AWS(t, "ec2", "replace-network-acl-association", "--association-id", newAssoc, "--network-acl-id", defID)
	h.AWS(t, "ec2", "delete-network-acl-entry", "--network-acl-id", custom, "--egress", "--rule-number", "100")
	h.AWS(t, "ec2", "delete-network-acl", "--network-acl-id", custom)
	if o, err := h.AWSErr(t, "ec2", "delete-network-acl", "--network-acl-id", defID); err == nil || !strings.Contains(o, "InvalidParameterValue") {
		t.Fatalf("delete the default ACL: %v %s", err, o)
	}
}
