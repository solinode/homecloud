package awstest

import (
	"strings"
	"testing"
)

func TestSTS(t *testing.T) {
	h := New(t)
	id := h.AWSJSON(t, "sts", "get-caller-identity")
	if id["Account"] != h.Env.AccountID || !strings.HasSuffix(id["Arn"].(string), ":user/root") {
		t.Fatalf("identity %v", id)
	}
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "app",
		"assume_role_policy": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": "arn:aws:iam::" + h.Env.AccountID + ":root"}, "Action": "sts:AssumeRole"}}}})
	out := h.Python(t, `
sts = boto3.client("sts")
c = sts.assume_role(RoleArn="arn:aws:iam::`+h.Env.AccountID+`:role/app", RoleSessionName="s1")["Credentials"]
s2 = boto3.client("sts", aws_access_key_id=c["AccessKeyId"], aws_secret_access_key=c["SecretAccessKey"], aws_session_token=c["SessionToken"])
print(s2.get_caller_identity()["Arn"])
try:
    boto3.client("sts", aws_access_key_id=c["AccessKeyId"], aws_secret_access_key="wrong", aws_session_token=c["SessionToken"]).get_caller_identity()
    print("BAD: wrong secret accepted")
except botocore.exceptions.ClientError as e:
    print(e.response["Error"]["Code"])
`)
	if !strings.Contains(out, ":assumed-role/app/s1") || !strings.Contains(out, "SignatureDoesNotMatch") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	// A user without sts:AssumeRole permission can't assume a role trusted only via the account.
	akid, secret := h.User(t, "reader", "S3ReadOnlyAccess")
	if out, err := h.AWSAs(t, akid, secret, "", "sts", "assume-role", "--role-arn", "arn:aws:iam::"+h.Env.AccountID+":role/app", "--role-session-name", "x1"); err == nil || !strings.Contains(out, "AccessDenied") {
		t.Fatalf("assume-role without permission: %v %s", err, out)
	}
}
