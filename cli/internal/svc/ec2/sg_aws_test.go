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

// TestSecurityGroupRuleTags: Terraform's per-rule resources tag a rule when
// they authorize it (TagSpecifications) and later through CreateTags.
func TestSecurityGroupRuleTags(t *testing.T) {
	h := liveHarness(t)
	vpcID := h.AWSJSON(t, "ec2", "create-vpc", "--cidr-block", "10.8.0.0/16")["Vpc"].(map[string]any)["VpcId"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "delete-vpc", "--vpc-id", vpcID) })
	g := h.AWSJSON(t, "ec2", "create-security-group", "--group-name", "tagged", "--description", "t", "--vpc-id", vpcID)["GroupId"].(string)
	out := h.AWSJSON(t, "ec2", "authorize-security-group-ingress", "--group-id", g, "--ip-permissions", "IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=10.0.0.0/8}]",
		"--tag-specifications", "ResourceType=security-group-rule,Tags=[{Key=Name,Value=ssh}]")
	rule := out["SecurityGroupRules"].([]any)[0].(map[string]any)
	id := rule["SecurityGroupRuleId"].(string)
	if len(rule["Tags"].([]any)) != 1 {
		t.Fatalf("authorize: %v", rule)
	}
	h.AWS(t, "ec2", "create-tags", "--resources", id, "--tags", "Key=team,Value=core")
	got := h.AWSJSON(t, "ec2", "describe-security-group-rules", "--filters", "Name=tag:team,Values=core")["SecurityGroupRules"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["SecurityGroupRuleId"] != id || len(got[0].(map[string]any)["Tags"].([]any)) != 2 {
		t.Fatalf("describe by tag: %v", got)
	}
	h.AWS(t, "ec2", "delete-tags", "--resources", id, "--tags", "Key=team")
	got = h.AWSJSON(t, "ec2", "describe-security-group-rules", "--security-group-rule-ids", id)["SecurityGroupRules"].([]any)
	if len(got[0].(map[string]any)["Tags"].([]any)) != 1 {
		t.Fatalf("after delete-tags: %v", got)
	}
}
