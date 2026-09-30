package ec2_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
)

// lambdaRoles lets the function service resolve roles through IAM.
type lambdaRoles struct{ iam *iam.Service }

func (r lambdaRoles) LambdaRole(ref string) (string, error) {
	role, err := r.iam.ServiceRole(ref, "lambda.amazonaws.com")
	return role.ARN, err
}

func (r lambdaRoles) LambdaCredentials(ref, session string, ttl time.Duration) (lambda.Credentials, error) {
	c, err := r.iam.AssumeRoleForService(ref, "lambda.amazonaws.com", session, ttl)
	return lambda.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Expiration: c.Expiration}, err
}

const connectCode = `import socket
def handler(event, context):
    out = {}
    for host, port in event["targets"]:
        try:
            socket.create_connection((host, port), timeout=2).close()
            out["%s:%d" % (host, port)] = True
        except Exception:
            out["%s:%d" % (host, port)] = False
    return out
`

// A function with a VPC configuration is a member of its security groups:
// what it reaches depends on the targets' groups (and its own egress rules).
func TestSecurityGroupsApplyToLambdaInVPC(t *testing.T) {
	f := newFilterEnv(t)
	h := f.h
	cw, err := cloudwatch.New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	l := lambda.New(h.Env, cw, f.vpc)
	l.Roles = lambdaRoles{h.IAM}
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "fn-role", "assume_role_policy": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}}}})
	l.RegisterAWS()
	l.Routes(h.Router)
	sgFn, sgTarget := f.group(t, "fn"), f.group(t, "target")
	target := f.launch(t, sgTarget)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("app.py")
	_, _ = w.Write([]byte(connectCode))
	_ = zw.Close()
	code := filepath.Join(t.TempDir(), "code.zip")
	if err := os.WriteFile(code, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	h.AWS(t, "lambda", "create-function", "--function-name", "sgprobe", "--runtime", "python3.12", "--handler", "app.handler",
		"--role", "arn:aws:iam::"+h.Env.AccountID+":role/fn-role", "--zip-file", "fileb://"+code,
		"--vpc-config", fmt.Sprintf("SubnetIds=%s,SecurityGroupIds=%s", f.subnet, sgFn))
	t.Cleanup(func() { _, _ = h.AWSErr(t, "lambda", "delete-function", "--function-name", "sgprobe") })
	h.AWS(t, "lambda", "wait", "function-active-v2", "--function-name", "sgprobe")

	probe := func() bool {
		out := filepath.Join(t.TempDir(), "out.json")
		payload := fmt.Sprintf(`{"targets": [["%s", 80]]}`, target.ip)
		if _, err := h.AWSErr(t, "lambda", "invoke", "--function-name", "sgprobe", "--cli-binary-format", "raw-in-base64-out", "--payload", payload, out); err != nil {
			return false
		}
		b, _ := os.ReadFile(out)
		var r map[string]bool
		return json.Unmarshal(b, &r) == nil && r[target.ip+":80"]
	}
	waitUntil := func(want bool, what string) {
		t.Helper()
		end := time.Now().Add(2 * time.Minute)
		for time.Now().Before(end) {
			if probe() == want {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s: function reaches target = %v, want %v", what, !want, want)
	}
	waitUntil(false, "target allows nothing")
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sgTarget, "--ip-permissions",
		fmt.Sprintf("IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId=%s}]", sgFn))
	waitUntil(true, "target allows the function's group")
	h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", sgTarget, "--ip-permissions",
		fmt.Sprintf("IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId=%s}]", sgFn))
	waitUntil(false, "rule revoked")
}
