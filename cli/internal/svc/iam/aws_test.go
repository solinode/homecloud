package iam_test

// These tests drive the IAM API with the real AWS CLI and boto3 through the
// in-process awstest endpoint. They skip when the tools are missing.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

func wantErr(t *testing.T, out string, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(out, code) {
		t.Fatalf("want %s, got err=%v:\n%s", code, err, out)
	}
}

func decode(t *testing.T, out string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return m
}

// userKeys creates a user with an inline policy and returns an access key.
func userKeys(t *testing.T, h *awstest.Harness, name, policy string) (string, string) {
	t.Helper()
	h.AWS(t, "iam", "create-user", "--user-name", name)
	if policy != "" {
		h.AWS(t, "iam", "put-user-policy", "--user-name", name, "--policy-name", "p", "--policy-document", policy)
	}
	k := h.AWSJSON(t, "iam", "create-access-key", "--user-name", name)["AccessKey"].(map[string]any)
	return k["AccessKeyId"].(string), k["SecretAccessKey"].(string)
}

func TestAWSUserLifecycle(t *testing.T) {
	h := awstest.New(t)
	u := h.AWSJSON(t, "iam", "create-user", "--user-name", "alice", "--path", "/eng/", "--tags", "Key=team,Value=blue")["User"].(map[string]any)
	if u["Arn"] != "arn:aws:iam::"+h.Env.AccountID+":user/eng/alice" || u["Path"] != "/eng/" || !strings.HasPrefix(u["UserId"].(string), "HCUA") {
		t.Fatalf("user %v", u)
	}
	out, err := h.AWSErr(t, "iam", "create-user", "--user-name", "alice")
	wantErr(t, out, err, "EntityAlreadyExists")
	out, err = h.AWSErr(t, "iam", "get-user", "--user-name", "nobody")
	wantErr(t, out, err, "NoSuchEntity")

	got := h.AWSJSON(t, "iam", "get-user", "--user-name", "alice")["User"].(map[string]any)
	if tags := got["Tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["Value"] != "blue" {
		t.Fatalf("tags %v", got)
	}
	if me := h.AWSJSON(t, "iam", "get-user")["User"].(map[string]any); me["UserName"] != "root" {
		t.Fatalf("get-user without a name should return the caller: %v", me)
	}
	h.AWS(t, "iam", "tag-user", "--user-name", "alice", "--tags", "Key=env,Value=dev", "Key=team,Value=red")
	h.AWS(t, "iam", "untag-user", "--user-name", "alice", "--tag-keys", "env")
	tags := h.AWSJSON(t, "iam", "list-user-tags", "--user-name", "alice")["Tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["Value"] != "red" {
		t.Fatalf("list-user-tags %v", tags)
	}

	// Login profile.
	h.AWS(t, "iam", "create-login-profile", "--user-name", "alice", "--password", "correct-horse-1", "--password-reset-required")
	out, err = h.AWSErr(t, "iam", "create-login-profile", "--user-name", "alice", "--password", "correct-horse-1")
	wantErr(t, out, err, "EntityAlreadyExists")
	lp := h.AWSJSON(t, "iam", "get-login-profile", "--user-name", "alice")["LoginProfile"].(map[string]any)
	if lp["PasswordResetRequired"] != true {
		t.Fatalf("login profile %v", lp)
	}
	h.AWS(t, "iam", "update-login-profile", "--user-name", "alice", "--no-password-reset-required")
	out, err = h.AWSErr(t, "iam", "update-login-profile", "--user-name", "alice", "--password", "short")
	wantErr(t, out, err, "PasswordPolicyViolation")

	// Access keys: two at most.
	k1 := h.AWSJSON(t, "iam", "create-access-key", "--user-name", "alice")["AccessKey"].(map[string]any)
	h.AWS(t, "iam", "create-access-key", "--user-name", "alice")
	out, err = h.AWSErr(t, "iam", "create-access-key", "--user-name", "alice")
	wantErr(t, out, err, "LimitExceeded")
	id := k1["AccessKeyId"].(string)
	h.AWS(t, "iam", "update-access-key", "--user-name", "alice", "--access-key-id", id, "--status", "Inactive")
	keys := h.AWSJSON(t, "iam", "list-access-keys", "--user-name", "alice")["AccessKeyMetadata"].([]any)
	inactive := 0
	for _, k := range keys {
		if k.(map[string]any)["Status"] == "Inactive" {
			inactive++
		}
	}
	if len(keys) != 2 || inactive != 1 {
		t.Fatalf("keys %v", keys)
	}
	lu := h.AWSJSON(t, "iam", "get-access-key-last-used", "--access-key-id", id)
	if lu["UserName"] != "alice" {
		t.Fatalf("last used %v", lu)
	}

	// AWS refuses to delete a user that still has keys, a password, policies or groups.
	out, err = h.AWSErr(t, "iam", "delete-user", "--user-name", "alice")
	wantErr(t, out, err, "DeleteConflict")

	// Rename keeps the keys.
	h.AWS(t, "iam", "update-user", "--user-name", "alice", "--new-user-name", "alice2", "--new-path", "/ops/")
	out, err = h.AWSErr(t, "iam", "get-user", "--user-name", "alice")
	wantErr(t, out, err, "NoSuchEntity")
	if u := h.AWSJSON(t, "iam", "get-user", "--user-name", "alice2")["User"].(map[string]any); u["Arn"] != "arn:aws:iam::"+h.Env.AccountID+":user/ops/alice2" || u["UserId"] != got["UserId"] {
		t.Fatalf("renamed user %v", u)
	}
	for _, k := range h.AWSJSON(t, "iam", "list-access-keys", "--user-name", "alice2")["AccessKeyMetadata"].([]any) {
		h.AWS(t, "iam", "delete-access-key", "--user-name", "alice2", "--access-key-id", k.(map[string]any)["AccessKeyId"].(string))
	}
	h.AWS(t, "iam", "delete-login-profile", "--user-name", "alice2")
	h.AWS(t, "iam", "delete-user", "--user-name", "alice2")
	out, err = h.AWSErr(t, "iam", "delete-user", "--user-name", "alice2")
	wantErr(t, out, err, "NoSuchEntity")

	// Paginated listing (the CLI follows markers).
	for _, n := range []string{"u1", "u2", "u3", "u4", "u5"} {
		h.AWS(t, "iam", "create-user", "--user-name", n)
	}
	all := h.AWSJSON(t, "iam", "list-users", "--page-size", "2")["Users"].([]any)
	if len(all) != 6 { // root + 5
		t.Fatalf("list-users with pages of 2 returned %d users", len(all))
	}
	first := h.AWSJSON(t, "iam", "list-users", "--max-items", "2")
	if len(first["Users"].([]any)) != 2 || first["NextToken"] == nil {
		t.Fatalf("max-items: %v", first)
	}
	if u := h.AWSJSON(t, "iam", "list-users", "--path-prefix", "/nothing/")["Users"].([]any); len(u) != 0 {
		t.Fatalf("path prefix: %v", u)
	}
}

func TestBoto3PoliciesAndGroups(t *testing.T) {
	h := awstest.New(t)
	acct := h.Env.AccountID
	out := h.Python(t, `
iam = boto3.client("iam")
C = botocore.exceptions.ClientError
def code(f, *a, **k):
    try:
        f(*a, **k)
        return "OK"
    except C as e:
        return e.response["Error"]["Code"] + ":" + str(e.response["ResponseMetadata"]["HTTPStatusCode"])

doc = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": ["s3:GetObject"], "Resource": "arn:aws:s3:::data/*"}]}
p = iam.create_policy(PolicyName="reader", Path="/app/", Description="read data", PolicyDocument=json.dumps(doc),
                      Tags=[{"Key": "owner", "Value": "me"}])["Policy"]
arn = p["Arn"]
assert arn == "arn:aws:iam::`+acct+`:policy/app/reader", arn
assert p["DefaultVersionId"] == "v1" and p["AttachmentCount"] == 0 and p["Tags"][0]["Key"] == "owner", p
print("dup", code(iam.create_policy, PolicyName="reader", PolicyDocument=json.dumps(doc)))
print("malformed", code(iam.create_policy, PolicyName="bad", PolicyDocument='{"Statement":[{"Effect":"Allow"}]}'))
print("notjson", code(iam.create_policy, PolicyName="bad", PolicyDocument='nope'))
g = iam.get_policy(PolicyArn=arn)["Policy"]
assert g["Description"] == "read data", g
v = iam.get_policy_version(PolicyArn=arn, VersionId="v1")["PolicyVersion"]
assert v["Document"] == doc and v["IsDefaultVersion"], v
print("nosuchpolicy", code(iam.get_policy, PolicyArn="arn:aws:iam::`+acct+`:policy/nope"))

# Versions: five at most; the default cannot be deleted.
for i in range(2, 6):
    d = dict(doc, Statement=[dict(doc["Statement"][0], Resource="arn:aws:s3:::data%d/*" % i)])
    r = iam.create_policy_version(PolicyArn=arn, PolicyDocument=json.dumps(d), SetAsDefault=(i == 3))["PolicyVersion"]
    assert r["VersionId"] == "v%d" % i, r
print("sixth", code(iam.create_policy_version, PolicyArn=arn, PolicyDocument=json.dumps(doc)))
vs = iam.list_policy_versions(PolicyArn=arn)["Versions"]
assert [x["VersionId"] for x in vs] == ["v5", "v4", "v3", "v2", "v1"], vs
assert [x["VersionId"] for x in vs if x["IsDefaultVersion"]] == ["v3"], vs
assert iam.get_policy(PolicyArn=arn)["Policy"]["DefaultVersionId"] == "v3"
print("deldefault", code(iam.delete_policy_version, PolicyArn=arn, VersionId="v3"))
iam.delete_policy_version(PolicyArn=arn, VersionId="v1")
iam.set_default_policy_version(PolicyArn=arn, VersionId="v5")
assert iam.get_policy_version(PolicyArn=arn, VersionId="v5")["PolicyVersion"]["Document"]["Statement"][0]["Resource"] == "arn:aws:s3:::data5/*"
v6 = iam.create_policy_version(PolicyArn=arn, PolicyDocument=json.dumps(doc))["PolicyVersion"]
assert v6["VersionId"] == "v6" and not v6["IsDefaultVersion"], v6
print("delversions", code(iam.delete_policy, PolicyArn=arn))

# AWS managed policies at AWS's ARNs.
admin = iam.get_policy(PolicyArn="arn:aws:iam::aws:policy/AdministratorAccess")["Policy"]
assert admin["AttachmentCount"] == 1, admin  # root
lam = iam.get_policy(PolicyArn="arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole")["Policy"]
assert lam["Path"] == "/service-role/", lam
ld = iam.get_policy_version(PolicyArn=lam["Arn"], VersionId=lam["DefaultVersionId"])["PolicyVersion"]["Document"]
assert "logs:PutLogEvents" in ld["Statement"][0]["Action"], ld
print("awsmodify", code(iam.create_policy_version, PolicyArn=lam["Arn"], PolicyDocument=json.dumps(doc)))
print("awsdelete", code(iam.delete_policy, PolicyArn=lam["Arn"]))
aws = [p["PolicyName"] for pg in iam.get_paginator("list_policies").paginate(Scope="AWS", PaginationConfig={"PageSize": 7}) for p in pg["Policies"]]
assert len(aws) > 40 and len(set(aws)) == len(aws) and "AmazonS3FullAccess" in aws and "reader" not in aws, aws
local = [p["PolicyName"] for p in iam.list_policies(Scope="Local")["Policies"]]
assert local == ["reader"], local
attached = [p["PolicyName"] for p in iam.list_policies(OnlyAttached=True)["Policies"]]
assert attached == ["AdministratorAccess"], attached
assert [p["PolicyName"] for p in iam.list_policies(PathPrefix="/app/")["Policies"]] == ["reader"]

# Groups: members, managed and inline policies.
iam.create_group(GroupName="devs", Path="/eng/")
print("dupgroup", code(iam.create_group, GroupName="devs"))
iam.create_user(UserName="bob")
iam.add_user_to_group(GroupName="devs", UserName="bob")
gg = iam.get_group(GroupName="devs")
assert gg["Group"]["Arn"] == "arn:aws:iam::`+acct+`:group/eng/devs" and [u["UserName"] for u in gg["Users"]] == ["bob"], gg
assert [x["GroupName"] for x in iam.list_groups_for_user(UserName="bob")["Groups"]] == ["devs"]
iam.attach_group_policy(GroupName="devs", PolicyArn=arn)
iam.attach_group_policy(GroupName="devs", PolicyArn="arn:aws:iam::aws:policy/ReadOnlyAccess")
iam.put_group_policy(GroupName="devs", PolicyName="extra", PolicyDocument=json.dumps(doc))
assert iam.get_group_policy(GroupName="devs", PolicyName="extra")["PolicyDocument"] == doc
assert iam.list_group_policies(GroupName="devs")["PolicyNames"] == ["extra"]
ap = sorted(p["PolicyArn"] for p in iam.list_attached_group_policies(GroupName="devs")["AttachedPolicies"])
assert ap == sorted([arn, "arn:aws:iam::aws:policy/ReadOnlyAccess"]), ap
ents = iam.list_entities_for_policy(PolicyArn=arn)
assert [x["GroupName"] for x in ents["PolicyGroups"]] == ["devs"] and ents["PolicyUsers"] == [], ents
print("delattached", code(iam.delete_policy, PolicyArn=arn))
print("delgroup", code(iam.delete_group, GroupName="devs"))
print("attachmissing", code(iam.attach_group_policy, GroupName="devs", PolicyArn="arn:aws:iam::aws:policy/NoSuchPolicy"))
print("detachnot", code(iam.detach_group_policy, GroupName="devs", PolicyArn="arn:aws:iam::aws:policy/AdministratorAccess"))
iam.detach_group_policy(GroupName="devs", PolicyArn=arn)
iam.detach_group_policy(GroupName="devs", PolicyArn="arn:aws:iam::aws:policy/ReadOnlyAccess")
iam.delete_group_policy(GroupName="devs", PolicyName="extra")
iam.remove_user_from_group(GroupName="devs", UserName="bob")
iam.update_group(GroupName="devs", NewGroupName="developers")
iam.delete_group(GroupName="developers")

# User inline + managed policies.
iam.put_user_policy(UserName="bob", PolicyName="own", PolicyDocument=json.dumps(doc))
assert iam.get_user_policy(UserName="bob", PolicyName="own")["PolicyDocument"] == doc
iam.attach_user_policy(UserName="bob", PolicyArn="arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess")
assert [p["PolicyName"] for p in iam.list_attached_user_policies(UserName="bob")["AttachedPolicies"]] == ["AmazonS3ReadOnlyAccess"]
print("deluser", code(iam.delete_user, UserName="bob"))
iam.detach_user_policy(UserName="bob", PolicyArn="arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess")
iam.delete_user_policy(UserName="bob", PolicyName="own")
iam.delete_user(UserName="bob")

for v in ["v2", "v3", "v4", "v6"]:
    iam.delete_policy_version(PolicyArn=arn, VersionId=v)
iam.delete_policy(PolicyArn=arn)
print("gone", code(iam.get_policy, PolicyArn=arn))

s = iam.get_account_summary()["SummaryMap"]
assert s["Users"] == 1 and s["AccessKeysPerUserQuota"] == 2, s
assert iam.list_account_aliases()["AccountAliases"] == []
d = iam.get_account_authorization_details()
assert [u["UserName"] for u in d["UserDetailList"]] == ["root"] and d["UserDetailList"][0]["AttachedManagedPolicies"][0]["PolicyName"] == "AdministratorAccess", d["UserDetailList"]
print("done")
`)
	for _, want := range []string{
		"dup EntityAlreadyExists:409", "malformed MalformedPolicyDocument:400", "notjson MalformedPolicyDocument:400",
		"nosuchpolicy NoSuchEntity:404", "sixth LimitExceeded:409", "deldefault DeleteConflict:409", "delversions DeleteConflict:409",
		"awsmodify UnmodifiableEntity:400", "awsdelete UnmodifiableEntity:400", "dupgroup EntityAlreadyExists:409",
		"delattached DeleteConflict:409", "delgroup DeleteConflict:409", "attachmissing NoSuchEntity:404", "detachnot NoSuchEntity:404",
		"deluser DeleteConflict:409", "gone NoSuchEntity:404", "done",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestBoto3RolesAndInstanceProfiles(t *testing.T) {
	h := awstest.New(t)
	acct := h.Env.AccountID
	out := h.Python(t, `
iam = boto3.client("iam")
C = botocore.exceptions.ClientError
def code(f, *a, **k):
    try:
        f(*a, **k)
        return "OK"
    except C as e:
        return e.response["Error"]["Code"] + ":" + str(e.response["ResponseMetadata"]["HTTPStatusCode"])

trust = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"Service": "ec2.amazonaws.com"}, "Action": "sts:AssumeRole"}]}
r = iam.create_role(RoleName="web", Path="/svc/", AssumeRolePolicyDocument=json.dumps(trust), Description="web servers",
                    MaxSessionDuration=7200, Tags=[{"Key": "app", "Value": "web"}],
                    PermissionsBoundary="arn:aws:iam::aws:policy/PowerUserAccess")["Role"]
assert r["Arn"] == "arn:aws:iam::`+acct+`:role/svc/web" and r["MaxSessionDuration"] == 7200, r
assert r["AssumeRolePolicyDocument"] == trust, r["AssumeRolePolicyDocument"]
assert r["PermissionsBoundary"]["PermissionsBoundaryArn"] == "arn:aws:iam::aws:policy/PowerUserAccess", r
print("dup", code(iam.create_role, RoleName="web", AssumeRolePolicyDocument=json.dumps(trust)))
print("badtrust", code(iam.create_role, RoleName="x", AssumeRolePolicyDocument='{"Statement":[{"Effect":"Allow","Action":"s3:*","Principal":"*"}]}'))
g = iam.get_role(RoleName="web")["Role"]
assert g["Description"] == "web servers" and g["Tags"] == [{"Key": "app", "Value": "web"}] and "RoleLastUsed" in g, g
iam.update_role(RoleName="web", Description="web tier", MaxSessionDuration=3600)
assert iam.update_role_description(RoleName="web", Description="web tier 2")["Role"]["Description"] == "web tier 2"
trust2 = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"Service": ["ec2.amazonaws.com", "ecs-tasks.amazonaws.com"]}, "Action": "sts:AssumeRole"}]}
iam.update_assume_role_policy(RoleName="web", PolicyDocument=json.dumps(trust2))
assert iam.get_role(RoleName="web")["Role"]["AssumeRolePolicyDocument"] == trust2
iam.tag_role(RoleName="web", Tags=[{"Key": "tier", "Value": "front"}])
iam.untag_role(RoleName="web", TagKeys=["app"])
assert iam.list_role_tags(RoleName="web")["Tags"] == [{"Key": "tier", "Value": "front"}]
iam.delete_role_permissions_boundary(RoleName="web")
assert "PermissionsBoundary" not in iam.get_role(RoleName="web")["Role"]
iam.attach_role_policy(RoleName="web", PolicyArn="arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess")
iam.put_role_policy(RoleName="web", PolicyName="inline", PolicyDocument=json.dumps({"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "sqs:*", "Resource": "*"}]}))
assert iam.list_role_policies(RoleName="web")["PolicyNames"] == ["inline"]
assert iam.get_role_policy(RoleName="web", PolicyName="inline")["PolicyDocument"]["Statement"][0]["Action"] in ("sqs:*", ["sqs:*"])
names = [x["RoleName"] for pg in iam.get_paginator("list_roles").paginate(PaginationConfig={"PageSize": 1}) for x in pg["Roles"]]
assert names == ["web"], names

# Instance profiles.
ip = iam.create_instance_profile(InstanceProfileName="web-profile")["InstanceProfile"]
assert ip["Arn"] == "arn:aws:iam::`+acct+`:instance-profile/web-profile" and ip["Roles"] == [], ip
iam.add_role_to_instance_profile(InstanceProfileName="web-profile", RoleName="web")
print("second", code(iam.add_role_to_instance_profile, InstanceProfileName="web-profile", RoleName="web"))
assert [x["RoleName"] for x in iam.get_instance_profile(InstanceProfileName="web-profile")["InstanceProfile"]["Roles"]] == ["web"]
assert [x["InstanceProfileName"] for x in iam.list_instance_profiles_for_role(RoleName="web")["InstanceProfiles"]] == ["web-profile"]
assert [x["InstanceProfileName"] for x in iam.list_instance_profiles()["InstanceProfiles"]] == ["web-profile"]
print("delprofile", code(iam.delete_instance_profile, InstanceProfileName="web-profile"))
print("delrole", code(iam.delete_role, RoleName="web"))
iam.detach_role_policy(RoleName="web", PolicyArn="arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess")
iam.delete_role_policy(RoleName="web", PolicyName="inline")
print("delrole2", code(iam.delete_role, RoleName="web"))
iam.remove_role_from_instance_profile(InstanceProfileName="web-profile", RoleName="web")
iam.delete_instance_profile(InstanceProfileName="web-profile")
iam.delete_role(RoleName="web")
print("gone", code(iam.get_role, RoleName="web"))

# Service-linked roles.
slr = iam.create_service_linked_role(AWSServiceName="ecs.amazonaws.com")["Role"]
assert slr["RoleName"] == "AWSServiceRoleForECS" and slr["Path"] == "/aws-service-role/ecs.amazonaws.com/", slr
assert slr["AssumeRolePolicyDocument"]["Statement"][0]["Principal"]["Service"] == "ecs.amazonaws.com", slr
print("slrdup", code(iam.create_service_linked_role, AWSServiceName="ecs.amazonaws.com"))
print("slrdelete", code(iam.delete_role, RoleName="AWSServiceRoleForECS"))
t = iam.delete_service_linked_role(RoleName="AWSServiceRoleForECS")["DeletionTaskId"]
assert iam.get_service_linked_role_deletion_status(DeletionTaskId=t)["Status"] == "SUCCEEDED"

# Simulation.
iam.create_user(UserName="sim")
iam.attach_user_policy(UserName="sim", PolicyArn="arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess")
iam.put_user_policy(UserName="sim", PolicyName="ip", PolicyDocument=json.dumps({"Version": "2012-10-17", "Statement": [
    {"Effect": "Allow", "Action": "sqs:SendMessage", "Resource": "*", "Condition": {"IpAddress": {"aws:SourceIp": "10.0.0.0/8"}}}]}))
res = iam.simulate_principal_policy(PolicySourceArn="arn:aws:iam::`+acct+`:user/sim",
    ActionNames=["s3:GetObject", "s3:PutObject", "sqs:SendMessage"], ResourceArns=["arn:aws:s3:::b/k"],
    ContextEntries=[{"ContextKeyName": "aws:SourceIp", "ContextKeyValues": ["10.1.2.3"], "ContextKeyType": "ip"}])["EvaluationResults"]
print("sim", ",".join(x["EvalActionName"] + "=" + x["EvalDecision"] for x in res))
assert iam.get_context_keys_for_principal_policy(PolicySourceArn="arn:aws:iam::`+acct+`:user/sim")["ContextKeyNames"] == ["aws:SourceIp"]
res = iam.simulate_custom_policy(PolicyInputList=[json.dumps({"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "NotAction": "iam:*", "Resource": "*"}]})],
    ActionNames=["iam:CreateUser", "ec2:RunInstances"])["EvaluationResults"]
print("custom", ",".join(x["EvalActionName"] + "=" + x["EvalDecision"] for x in res))
print("done")
`)
	for _, want := range []string{
		"dup EntityAlreadyExists:409", "badtrust MalformedPolicyDocument:400", "second LimitExceeded:409",
		"delprofile DeleteConflict:409", "delrole DeleteConflict:409", "delrole2 DeleteConflict:409", "gone NoSuchEntity:404",
		"slrdup EntityAlreadyExists:409", "slrdelete UnmodifiableEntity:400",
		"sim s3:GetObject=allowed,s3:PutObject=implicitDeny,sqs:SendMessage=allowed",
		"custom iam:CreateUser=implicitDeny,ec2:RunInstances=allowed", "done",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

// A user allowed only iam:CreateUser must not be able to hand the new user
// (or itself) more permissions.
func TestAWSPrivilegeEscalation(t *testing.T) {
	h := awstest.New(t)
	acct := h.Env.AccountID
	akid, secret := userKeys(t, h, "creator", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreateUser","Resource":"*"}]}`)
	as := func(args ...string) (string, error) { return h.AWSAs(t, akid, secret, "", args...) }
	if out, err := as("iam", "create-user", "--user-name", "minion"); err != nil {
		t.Fatalf("create-user: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"iam", "attach-user-policy", "--user-name", "minion", "--policy-arn", "arn:aws:iam::aws:policy/AdministratorAccess"},
		{"iam", "attach-user-policy", "--user-name", "creator", "--policy-arn", "arn:aws:iam::aws:policy/AdministratorAccess"},
		{"iam", "put-user-policy", "--user-name", "creator", "--policy-name", "x", "--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`},
		{"iam", "create-access-key", "--user-name", "minion"},
		{"iam", "create-login-profile", "--user-name", "minion", "--password", "Password-123"},
		{"iam", "create-user", "--user-name", "tagged", "--tags", "Key=a,Value=b"},
		{"iam", "create-user", "--user-name", "bounded", "--permissions-boundary", "arn:aws:iam::aws:policy/AdministratorAccess"},
		{"iam", "create-policy-version", "--policy-arn", "arn:aws:iam::aws:policy/ReadOnlyAccess", "--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`},
		{"iam", "list-users"},
	} {
		out, err := as(args...)
		wantErr(t, out, err, "AccessDenied")
	}

	// Delegated administration: attach only a named policy, only to users under /team/.
	akid2, secret2 := userKeys(t, h, "delegate", `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"iam:CreateUser","Resource":"arn:aws:iam::*:user/team/*"},
		{"Effect":"Allow","Action":"iam:AttachUserPolicy","Resource":"arn:aws:iam::*:user/team/*",
		 "Condition":{"ArnEquals":{"iam:PolicyARN":"arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"}}}]}`)
	as2 := func(args ...string) (string, error) { return h.AWSAs(t, akid2, secret2, "", args...) }
	out, err := as2("iam", "create-user", "--user-name", "outsider")
	wantErr(t, out, err, "AccessDenied")
	if out, err := as2("iam", "create-user", "--user-name", "member", "--path", "/team/"); err != nil {
		t.Fatalf("create in /team/: %v\n%s", err, out)
	}
	if out, err := as2("iam", "attach-user-policy", "--user-name", "member", "--policy-arn", "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"); err != nil {
		t.Fatalf("attach allowed policy: %v\n%s", err, out)
	}
	out, err = as2("iam", "attach-user-policy", "--user-name", "member", "--policy-arn", "arn:aws:iam::aws:policy/AdministratorAccess")
	wantErr(t, out, err, "AccessDenied")

	// A permissions boundary caps an administrator.
	h.AWS(t, "iam", "create-user", "--user-name", "capped", "--permissions-boundary", "arn:aws:iam::aws:policy/AmazonS3FullAccess")
	h.AWS(t, "iam", "attach-user-policy", "--user-name", "capped", "--policy-arn", "arn:aws:iam::aws:policy/AdministratorAccess")
	k := h.AWSJSON(t, "iam", "create-access-key", "--user-name", "capped")["AccessKey"].(map[string]any)
	out, err = h.AWSAs(t, k["AccessKeyId"].(string), k["SecretAccessKey"].(string), "", "iam", "list-users")
	wantErr(t, out, err, "AccessDenied")
	out, err = h.AWSErr(t, "iam", "delete-policy", "--policy-arn", "arn:aws:iam::aws:policy/AmazonS3FullAccess")
	wantErr(t, out, err, "UnmodifiableEntity")

	// Temporary credentials of a role carry only the role's permissions.
	h.AWS(t, "iam", "create-role", "--role-name", "reader", "--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::`+acct+`:root"},"Action":"sts:AssumeRole","Condition":{"StringEquals":{"sts:ExternalId":"xyz"}}}]}`)
	h.AWS(t, "iam", "attach-role-policy", "--role-name", "reader", "--policy-arn", "arn:aws:iam::aws:policy/IAMReadOnlyAccess")
	out, err = h.AWSErr(t, "sts", "assume-role", "--role-arn", "arn:aws:iam::"+acct+":role/reader", "--role-session-name", "s1")
	wantErr(t, out, err, "AccessDenied") // the trust policy requires the external ID
	c := h.AWSJSON(t, "sts", "assume-role", "--role-arn", "arn:aws:iam::"+acct+":role/reader", "--role-session-name", "s1", "--external-id", "xyz")["Credentials"].(map[string]any)
	tak, tsk, tok := c["AccessKeyId"].(string), c["SecretAccessKey"].(string), c["SessionToken"].(string)
	if out, err := h.AWSAs(t, tak, tsk, tok, "iam", "list-users"); err != nil || !strings.Contains(out, "creator") {
		t.Fatalf("role session list-users: %v\n%s", err, out)
	}
	out, err = h.AWSAs(t, tak, tsk, tok, "iam", "create-user", "--user-name", "x")
	wantErr(t, out, err, "AccessDenied")
	out, err = h.AWSAs(t, tak, tsk, tok, "iam", "get-user")
	wantErr(t, out, err, "ValidationError")
	if lu := h.AWSJSON(t, "iam", "get-role", "--role-name", "reader")["Role"].(map[string]any)["RoleLastUsed"].(map[string]any); lu["Region"] != "us-east-1" {
		t.Fatalf("RoleLastUsed %v", lu)
	}
}

func TestAWSConditions(t *testing.T) {
	h := awstest.New(t)
	// The test endpoint listens on 127.0.0.1.
	local, localSecret := userKeys(t, h, "local", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*",
		"Condition":{"IpAddress":{"aws:SourceIp":"127.0.0.1/32"}}}]}`)
	if out, err := h.AWSAs(t, local, localSecret, "", "iam", "list-users"); err != nil {
		t.Fatalf("from 127.0.0.1: %v\n%s", err, out)
	}
	remote, remoteSecret := userKeys(t, h, "remote", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*",
		"Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}}]}`)
	out, err := h.AWSAs(t, remote, remoteSecret, "", "iam", "list-users")
	wantErr(t, out, err, "AccessDenied")
	region, regionSecret := userKeys(t, h, "region", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListUsers","Resource":"*",
		"Condition":{"StringEquals":{"aws:RequestedRegion":"eu-west-1"}}}]}`)
	out, err = h.AWSAs(t, region, regionSecret, "", "iam", "list-users")
	wantErr(t, out, err, "AccessDenied")
	denyTLS, denyTLSSecret := userKeys(t, h, "tls", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:*","Resource":"*"},
		{"Effect":"Deny","Action":"*","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`)
	out, err = h.AWSAs(t, denyTLS, denyTLSSecret, "", "iam", "list-users")
	wantErr(t, out, err, "AccessDenied")

	// Self-service: users manage only their own keys (${aws:username}).
	self, selfSecret := userKeys(t, h, "self", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Action":["iam:ListAccessKeys","iam:CreateAccessKey","iam:GetUser"],"Resource":"arn:aws:iam::*:user/${aws:username}"}]}`)
	if out, err := h.AWSAs(t, self, selfSecret, "", "iam", "list-access-keys"); err != nil || !strings.Contains(out, self) {
		t.Fatalf("own keys: %v\n%s", err, out)
	}
	out, err = h.AWSAs(t, self, selfSecret, "", "iam", "list-access-keys", "--user-name", "local")
	wantErr(t, out, err, "AccessDenied")

	// Tag-based access: aws:ResourceTag on the target user.
	h.AWS(t, "iam", "create-user", "--user-name", "blue1", "--tags", "Key=team,Value=blue")
	h.AWS(t, "iam", "create-user", "--user-name", "red1", "--tags", "Key=team,Value=red")
	tag, tagSecret := userKeys(t, h, "tagger", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:GetUser","Resource":"*",
		"Condition":{"StringEquals":{"aws:ResourceTag/team":"blue"}}}]}`)
	if out, err := h.AWSAs(t, tag, tagSecret, "", "iam", "get-user", "--user-name", "blue1"); err != nil {
		t.Fatalf("tagged user: %v\n%s", err, out)
	}
	out, err = h.AWSAs(t, tag, tagSecret, "", "iam", "get-user", "--user-name", "red1")
	wantErr(t, out, err, "AccessDenied")
}

// The native API and the AWS API share one model: legacy HomeCloud policy
// names resolve to the AWS-named policies.
func TestNativeAndAWSShareModel(t *testing.T) {
	h := awstest.New(t)
	h.Native(t, "POST", "/api/v1/iam/users", map[string]any{"name": "legacy", "policies": []string{"S3ReadOnlyAccess", "LambdaFullAccess"}})
	ap := h.AWSJSON(t, "iam", "list-attached-user-policies", "--user-name", "legacy")["AttachedPolicies"].([]any)
	var arns []string
	for _, p := range ap {
		arns = append(arns, p.(map[string]any)["PolicyArn"].(string))
	}
	if strings.Join(arns, ",") != "arn:aws:iam::aws:policy/AWSLambda_FullAccess,arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess" {
		t.Fatalf("attached %v", arns)
	}
	var p struct {
		Policy struct {
			Name string `json:"name"`
			ARN  string `json:"arn"`
		} `json:"policy"`
	}
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/iam/policies/S3FullAccess", nil), &p)
	if p.Policy.Name != "AmazonS3FullAccess" || p.Policy.ARN != "arn:aws:iam::aws:policy/AmazonS3FullAccess" {
		t.Fatalf("legacy name lookup: %+v", p)
	}
	h.Native(t, "DELETE", "/api/v1/iam/users/legacy/policies/S3ReadOnlyAccess", nil)
	// Native policy edits become new default versions.
	h.AWS(t, "iam", "create-policy", "--policy-name", "edit", "--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	for i := 0; i < 6; i++ {
		h.Native(t, "PUT", "/api/v1/iam/policies/edit", map[string]any{"document": map[string]any{"Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:PutObject", "Resource": "*"}}}})
	}
	vs := h.AWSJSON(t, "iam", "list-policy-versions", "--policy-arn", "arn:aws:iam::"+h.Env.AccountID+":policy/edit")["Versions"].([]any)
	if len(vs) != 5 || vs[0].(map[string]any)["VersionId"] != "v7" || vs[0].(map[string]any)["IsDefaultVersion"] != true {
		t.Fatalf("versions %v", vs)
	}
	// Groups gained inline policies in the native API too.
	h.Native(t, "POST", "/api/v1/iam/groups", map[string]any{"name": "g"})
	h.Native(t, "PUT", "/api/v1/iam/groups/g/inline-policies/p", map[string]any{"Statement": []any{map[string]any{"Effect": "Allow", "Action": "sqs:*", "Resource": "*"}}})
	if names := h.AWSJSON(t, "iam", "list-group-policies", "--group-name", "g")["PolicyNames"].([]any); len(names) != 1 {
		t.Fatalf("group inline %v", names)
	}
	// Instance profiles over the native API.
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "r", "assume_role_policy": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`})
	h.Native(t, "POST", "/api/v1/iam/instance-profiles", map[string]any{"name": "ip", "role": "r"})
	if roles := h.AWSJSON(t, "iam", "get-instance-profile", "--instance-profile-name", "ip")["InstanceProfile"].(map[string]any)["Roles"].([]any); len(roles) != 1 {
		t.Fatalf("profile roles %v", roles)
	}
}
