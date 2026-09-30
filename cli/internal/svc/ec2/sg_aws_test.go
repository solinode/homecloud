package ec2_test

import "testing"

// TestSecurityGroupRuleFromGroup checks a rule whose source is another security group, sent the way the
// SDKs and Terraform send it (IpPermissions.N.Groups.M.GroupId), is stored and read back the same way.
func TestSecurityGroupRuleFromGroup(t *testing.T) {
	h := liveHarness(t)
	vpcID := h.AWSJSON(t, "ec2", "create-vpc", "--cidr-block", "10.9.0.0/16")["Vpc"].(map[string]any)["VpcId"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "delete-vpc", "--vpc-id", vpcID) })
	a := h.AWSJSON(t, "ec2", "create-security-group", "--group-name", "a", "--description", "a", "--vpc-id", vpcID)["GroupId"].(string)
	b := h.AWSJSON(t, "ec2", "create-security-group", "--group-name", "b", "--description", "b", "--vpc-id", vpcID)["GroupId"].(string)
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", b, "--ip-permissions",
		"IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId="+a+",Description=from-a}]")

	g := h.AWSJSON(t, "ec2", "describe-security-groups", "--group-ids", b)["SecurityGroups"].([]any)[0].(map[string]any)
	perms := g["IpPermissions"].([]any)
	if len(perms) != 1 {
		t.Fatalf("permissions: %v", perms)
	}
	pairs := perms[0].(map[string]any)["UserIdGroupPairs"].([]any)
	if len(pairs) != 1 || pairs[0].(map[string]any)["GroupId"] != a {
		t.Fatalf("group pairs: %v", pairs)
	}

	// Revoking with the same shape removes it.
	h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", b, "--ip-permissions",
		"IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId="+a+"}]")
	g = h.AWSJSON(t, "ec2", "describe-security-groups", "--group-ids", b)["SecurityGroups"].([]any)[0].(map[string]any)
	if len(g["IpPermissions"].([]any)) != 0 {
		t.Fatalf("rule not revoked: %v", g["IpPermissions"])
	}
}
