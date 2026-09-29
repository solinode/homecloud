package lambda_test

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

type roles struct{ iam *iam.Service }

func (r roles) LambdaRole(ref string) (string, error) {
	role, err := r.iam.ServiceRole(ref, "lambda.amazonaws.com")
	return role.ARN, err
}

func (r roles) LambdaCredentials(ref, session string, ttl time.Duration) (lambda.Credentials, error) {
	c, err := r.iam.AssumeRoleForService(ref, "lambda.amazonaws.com", session, ttl)
	return lambda.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Expiration: c.Expiration}, err
}

func zipFile(t *testing.T, name, content string) string {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name)
	w.Write([]byte(content))
	zw.Close()
	p := filepath.Join(t.TempDir(), "code.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestAWSControlPlane drives the Lambda API with the AWS CLI (no Docker: invocations aren't exercised).
func TestAWSControlPlane(t *testing.T) {
	h := awstest.New(t)
	cw, err := cloudwatch.New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	l := lambda.New(h.Env, cw, vpc.New(h.Env))
	l.Roles = roles{h.IAM}
	l.RegisterAWS()
	l.Routes(h.Router)
	acct := h.Env.AccountID
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "fn-role", "assume_role_policy": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}}}})
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "ec2-role", "assume_role_policy": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "ec2.amazonaws.com"}, "Action": "sts:AssumeRole"}}}})
	role := "arn:aws:iam::" + acct + ":role/fn-role"
	code := zipFile(t, "app.py", "def handler(e, c):\n    return 1\n")

	if out, err := h.AWSErr(t, "lambda", "create-function", "--function-name", "f1", "--runtime", "python3.12", "--handler", "app.handler",
		"--role", "arn:aws:iam::"+acct+":role/ec2-role", "--zip-file", "fileb://"+code); err == nil || !strings.Contains(out, "InvalidParameterValueException") {
		t.Fatalf("a role that doesn't trust Lambda must be rejected: %s", out)
	}
	fc := h.AWSJSON(t, "lambda", "create-function", "--function-name", "f1", "--runtime", "python3.12", "--handler", "app.handler",
		"--role", role, "--zip-file", "fileb://"+code, "--environment", "Variables={A=1}", "--tags", "team=x")
	if fc["State"] != "Active" || fc["Role"] != role || fc["Version"] != "$LATEST" || fc["PackageType"] != "Zip" || fc["LastUpdateStatus"] != "Successful" {
		t.Fatalf("create-function: %v", fc)
	}
	if out, err := h.AWSErr(t, "lambda", "create-function", "--function-name", "f1", "--runtime", "python3.12", "--handler", "app.handler",
		"--role", role, "--zip-file", "fileb://"+code); err == nil || !strings.Contains(out, "ResourceConflictException") {
		t.Fatalf("duplicate create: %s", out)
	}
	h.AWS(t, "lambda", "wait", "function-active-v2", "--function-name", "f1")
	gf := h.AWSJSON(t, "lambda", "get-function", "--function-name", "f1")
	if gf["Tags"].(map[string]any)["team"] != "x" || !strings.Contains(gf["Code"].(map[string]any)["Location"].(string), "/lambda-code/") {
		t.Fatalf("get-function: %v", gf)
	}
	if out, err := h.AWSErr(t, "lambda", "get-function", "--function-name", "nope"); err == nil || !strings.Contains(out, "ResourceNotFoundException") {
		t.Fatalf("missing function: %s", out)
	}
	// Reserved environment variables are rejected, as in AWS.
	if out, err := h.AWSErr(t, "lambda", "update-function-configuration", "--function-name", "f1", "--environment", "Variables={AWS_REGION=x}"); err == nil || !strings.Contains(out, "InvalidParameterValueException") {
		t.Fatalf("reserved env: %s", out)
	}

	// Versions and aliases.
	v1 := h.AWSJSON(t, "lambda", "publish-version", "--function-name", "f1")
	if v1["Version"] != "1" || !strings.HasSuffix(v1["FunctionArn"].(string), ":f1:1") {
		t.Fatalf("publish: %v", v1)
	}
	if again := h.AWSJSON(t, "lambda", "publish-version", "--function-name", "f1"); again["Version"] != "1" {
		t.Fatalf("unchanged code must not publish a new version: %v", again)
	}
	h.AWS(t, "lambda", "update-function-configuration", "--function-name", "f1", "--timeout", "10")
	if v2 := h.AWSJSON(t, "lambda", "publish-version", "--function-name", "f1"); v2["Version"] != "2" || v2["Timeout"].(float64) != 10 {
		t.Fatalf("second version: %v", v2)
	}
	if v := h.AWSJSON(t, "lambda", "get-function-configuration", "--function-name", "f1:1"); v["Timeout"].(float64) != 3 {
		t.Fatalf("version 1 must keep its configuration: %v", v)
	}
	al := h.AWSJSON(t, "lambda", "create-alias", "--function-name", "f1", "--name", "live", "--function-version", "1",
		"--routing-config", "AdditionalVersionWeights={2=0.1}")
	if al["AliasArn"] != "arn:aws:lambda:us-east-1:"+acct+":function:f1:live" {
		t.Fatalf("alias: %v", al)
	}
	if out, err := h.AWSErr(t, "lambda", "delete-function", "--function-name", "f1", "--qualifier", "2"); err == nil || !strings.Contains(out, "ResourceConflictException") {
		t.Fatalf("deleting a routed version: %s", out)
	}
	if vs := h.AWSJSON(t, "lambda", "list-versions-by-function", "--function-name", "f1"); len(vs["Versions"].([]any)) != 3 {
		t.Fatalf("versions: %v", vs)
	}

	// Resource policy.
	st := h.AWSJSON(t, "lambda", "add-permission", "--function-name", "f1", "--statement-id", "sns", "--action", "lambda:InvokeFunction",
		"--principal", "sns.amazonaws.com", "--source-arn", "arn:aws:sns:us-east-1:"+acct+":t")
	if !strings.Contains(st["Statement"].(string), `"Service":"sns.amazonaws.com"`) {
		t.Fatalf("add-permission: %v", st)
	}
	if pol := h.AWSJSON(t, "lambda", "get-policy", "--function-name", "f1"); !strings.Contains(pol["Policy"].(string), `"Sid":"sns"`) {
		t.Fatalf("get-policy: %v", pol)
	}
	h.AWS(t, "lambda", "remove-permission", "--function-name", "f1", "--statement-id", "sns")
	if out, err := h.AWSErr(t, "lambda", "get-policy", "--function-name", "f1"); err == nil || !strings.Contains(out, "ResourceNotFoundException") {
		t.Fatalf("empty policy: %s", out)
	}

	// Concurrency, async config, layers, URLs, account settings.
	h.AWS(t, "lambda", "put-function-concurrency", "--function-name", "f1", "--reserved-concurrent-executions", "5")
	if c := h.AWSJSON(t, "lambda", "get-function-concurrency", "--function-name", "f1"); c["ReservedConcurrentExecutions"].(float64) != 5 {
		t.Fatalf("concurrency: %v", c)
	}
	if a := h.AWSJSON(t, "lambda", "get-account-settings"); a["AccountLimit"].(map[string]any)["UnreservedConcurrentExecutions"].(float64) != float64(lambda.AccountConcurrency-5) {
		t.Fatalf("account settings: %v", a)
	}
	h.AWS(t, "lambda", "create-function", "--function-name", "dest", "--runtime", "nodejs20.x", "--handler", "index.handler", "--role", role,
		"--zip-file", "fileb://"+zipFile(t, "index.mjs", "export const handler = async () => 1;"))
	ic := h.AWSJSON(t, "lambda", "put-function-event-invoke-config", "--function-name", "f1", "--qualifier", "live", "--maximum-retry-attempts", "1",
		"--destination-config", `{"OnSuccess":{"Destination":"arn:aws:lambda:us-east-1:`+acct+`:function:dest"}}`)
	if ic["MaximumRetryAttempts"].(float64) != 1 || !strings.HasSuffix(ic["FunctionArn"].(string), ":f1:live") {
		t.Fatalf("event invoke config: %v", ic)
	}
	layerZip := zipFile(t, "python/lib.py", "X = 1\n")
	lv := h.AWSJSON(t, "lambda", "publish-layer-version", "--layer-name", "libs", "--zip-file", "fileb://"+layerZip, "--compatible-runtimes", "python3.12")
	if lv["Version"].(float64) != 1 || lv["LayerVersionArn"] != "arn:aws:lambda:us-east-1:"+acct+":layer:libs:1" {
		t.Fatalf("layer: %v", lv)
	}
	up := h.AWSJSON(t, "lambda", "update-function-configuration", "--function-name", "f1", "--layers", lv["LayerVersionArn"].(string))
	if ls := up["Layers"].([]any); len(ls) != 1 {
		t.Fatalf("function layers: %v", up)
	}
	if ll := h.AWSJSON(t, "lambda", "list-layers", "--compatible-runtime", "python3.12"); len(ll["Layers"].([]any)) != 1 {
		t.Fatalf("list-layers: %v", ll)
	}
	u := h.AWSJSON(t, "lambda", "create-function-url-config", "--function-name", "f1", "--auth-type", "AWS_IAM")
	if u["AuthType"] != "AWS_IAM" || !strings.Contains(u["FunctionUrl"].(string), "/lambda-url/f1/") {
		t.Fatalf("function url: %v", u)
	}
	ARN := "arn:aws:lambda:us-east-1:" + acct + ":function:f1"
	h.AWS(t, "lambda", "tag-resource", "--resource", ARN, "--tags", "env=dev")
	if tags := h.AWSJSON(t, "lambda", "list-tags", "--resource", ARN); tags["Tags"].(map[string]any)["env"] != "dev" {
		t.Fatalf("tags: %v", tags)
	}
	if lf := h.AWSJSON(t, "lambda", "list-functions", "--max-items", "1"); len(lf["Functions"].([]any)) != 1 || lf["NextToken"] == nil {
		t.Fatalf("pagination: %v", lf)
	}
	if lf := h.AWSJSON(t, "lambda", "list-functions"); len(lf["Functions"].([]any)) != 2 {
		t.Fatalf("list-functions: %v", lf)
	}

	// A user without lambda permissions is denied.
	akid, secret := h.User(t, "reader", "S3ReadOnlyAccess")
	if out, err := h.AWSAs(t, akid, secret, "", "lambda", "get-function", "--function-name", "f1"); err == nil || !strings.Contains(out, "AccessDeniedException") {
		t.Fatalf("unauthorized get-function: %v %s", err, out)
	}
	h.AWS(t, "lambda", "delete-function", "--function-name", "f1")
	if out, err := h.AWSErr(t, "lambda", "get-alias", "--function-name", "f1", "--name", "live"); err == nil || !strings.Contains(out, "ResourceNotFoundException") {
		t.Fatalf("aliases must go with the function: %s", out)
	}
}
