package secrets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func setup(t *testing.T) (*awstest.Harness, *secrets.Service, *kms.Service, *clock) {
	h := awstest.New(t)
	clk := &clock{t: time.Now()}
	sec := h.Secrets
	sec.Now = clk.Now
	k := kms.New(h.Env, sec)
	k.Now = clk.Now
	sec.KMS = k
	sec.RegisterAWS()
	k.RegisterAWS()
	return h, sec, k, clk
}

// expectErr runs the AWS CLI and requires it to fail with code.
func expectErr(t *testing.T, h *awstest.Harness, code string, args ...string) {
	t.Helper()
	out, err := h.AWSErr(t, args...)
	if err == nil || !strings.Contains(out, code) {
		t.Fatalf("aws %s: want %s, got err=%v\n%s", strings.Join(args, " "), code, err, out)
	}
}

func TestSecretsManagerCLI(t *testing.T) {
	h, sec, _, clk := setup(t)
	// Rotations run in the background; let them finish before the test ends.
	t.Cleanup(func() {
		for deadline := time.Now().Add(60 * time.Second); sec.Rotating() && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
		}
	})
	arnRe := regexp.MustCompile(`^arn:aws:secretsmanager:us-east-1:` + h.Env.AccountID + `:secret:app/db-[A-Za-z0-9]{6}$`)

	c := h.AWSJSON(t, "secretsmanager", "create-secret", "--name", "app/db", "--secret-string", `{"user":"a","pass":"1"}`,
		"--description", "db creds", "--tags", "Key=env,Value=dev", "Key=team,Value=core")
	arn := c["ARN"].(string)
	if !arnRe.MatchString(arn) || c["VersionId"] == "" {
		t.Fatalf("create-secret: %v", c)
	}
	v1 := c["VersionId"].(string)
	expectErr(t, h, "ResourceExistsException", "secretsmanager", "create-secret", "--name", "app/db", "--secret-string", "x")

	// Lookup by name, full ARN and partial ARN.
	partial := arn[:len(arn)-7]
	for _, id := range []string{"app/db", arn, partial} {
		g := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", id)
		if g["SecretString"] != `{"user":"a","pass":"1"}` || g["ARN"] != arn || g["VersionId"] != v1 {
			t.Fatalf("get-secret-value %s: %v", id, g)
		}
	}
	expectErr(t, h, "ResourceNotFoundException", "secretsmanager", "get-secret-value", "--secret-id", "nope")
	expectErr(t, h, "ResourceNotFoundException", "secretsmanager", "get-secret-value", "--secret-id", arn[:len(arn)-6]+"zzzzzz")

	// New versions move AWSCURRENT and AWSPREVIOUS.
	p := h.AWSJSON(t, "secretsmanager", "put-secret-value", "--secret-id", "app/db", "--secret-string", "v2")
	v2 := p["VersionId"].(string)
	if cur := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "app/db"); cur["SecretString"] != "v2" {
		t.Fatalf("current: %v", cur)
	}
	if prev := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "app/db", "--version-stage", "AWSPREVIOUS"); prev["VersionId"] != v1 {
		t.Fatalf("previous: %v", prev)
	}
	if byID := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "app/db", "--version-id", v1); byID["SecretString"] != `{"user":"a","pass":"1"}` {
		t.Fatalf("by version id: %v", byID)
	}
	expectErr(t, h, "ResourceNotFoundException", "secretsmanager", "get-secret-value", "--secret-id", "app/db", "--version-stage", "AWSPENDING")
	d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", arn)
	stages := d["VersionIdsToStages"].(map[string]any)
	if len(stages) != 2 || d["Description"] != "db creds" || len(d["Tags"].([]any)) != 2 || d["LastChangedDate"] == nil || d["RotationEnabled"] != false {
		t.Fatalf("describe: %v", d)
	}

	// Move AWSCURRENT back to v1 (AWSPREVIOUS follows to v2).
	expectErr(t, h, "InvalidParameterException", "secretsmanager", "update-secret-version-stage", "--secret-id", "app/db",
		"--version-stage", "AWSCURRENT", "--move-to-version-id", v1)
	h.AWS(t, "secretsmanager", "update-secret-version-stage", "--secret-id", "app/db", "--version-stage", "AWSCURRENT",
		"--move-to-version-id", v1, "--remove-from-version-id", v2)
	if cur := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "app/db"); cur["VersionId"] != v1 {
		t.Fatalf("after stage move: %v", cur)
	}
	h.AWS(t, "secretsmanager", "update-secret-version-stage", "--secret-id", "app/db", "--version-stage", "TEST", "--move-to-version-id", v2)
	vl := h.AWSJSON(t, "secretsmanager", "list-secret-version-ids", "--secret-id", "app/db")
	if n := len(vl["Versions"].([]any)); n != 2 {
		t.Fatalf("list-secret-version-ids: %v", vl)
	}
	if g := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "app/db", "--version-stage", "TEST"); g["VersionId"] != v2 {
		t.Fatalf("custom stage: %v", g)
	}

	// Update metadata and value.
	u := h.AWSJSON(t, "secretsmanager", "update-secret", "--secret-id", "app/db", "--description", "new", "--secret-string", "v3")
	if u["VersionId"] == nil {
		t.Fatalf("update-secret: %v", u)
	}
	if d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", "app/db"); d["Description"] != "new" {
		t.Fatalf("describe after update: %v", d)
	}

	// Tags.
	h.AWS(t, "secretsmanager", "tag-resource", "--secret-id", "app/db", "--tags", "Key=cost,Value=1")
	h.AWS(t, "secretsmanager", "untag-resource", "--secret-id", "app/db", "--tag-keys", "env", "team")
	if d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", "app/db"); len(d["Tags"].([]any)) != 1 {
		t.Fatalf("tags: %v", d["Tags"])
	}

	// Random passwords.
	pw := h.AWSJSON(t, "secretsmanager", "get-random-password", "--password-length", "20", "--exclude-punctuation")["RandomPassword"].(string)
	if len(pw) != 20 || !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(pw) {
		t.Fatalf("password %q", pw)
	}

	// Resource policies.
	pol := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + h.Env.AccountID + `:root"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
	public := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
	h.AWS(t, "secretsmanager", "put-resource-policy", "--secret-id", "app/db", "--resource-policy", pol)
	if g := h.AWSJSON(t, "secretsmanager", "get-resource-policy", "--secret-id", "app/db"); g["ResourcePolicy"] != pol {
		t.Fatalf("get-resource-policy: %v", g)
	}
	expectErr(t, h, "PublicPolicyException", "secretsmanager", "put-resource-policy", "--secret-id", "app/db", "--resource-policy", public, "--block-public-policy")
	expectErr(t, h, "MalformedPolicyDocumentException", "secretsmanager", "put-resource-policy", "--secret-id", "app/db", "--resource-policy", "{")
	if v := h.AWSJSON(t, "secretsmanager", "validate-resource-policy", "--resource-policy", public); v["PolicyValidationPassed"] != false {
		t.Fatalf("validate public: %v", v)
	}
	if v := h.AWSJSON(t, "secretsmanager", "validate-resource-policy", "--resource-policy", pol); v["PolicyValidationPassed"] != true {
		t.Fatalf("validate: %v", v)
	}
	h.AWS(t, "secretsmanager", "delete-resource-policy", "--secret-id", "app/db")
	if g := h.AWSJSON(t, "secretsmanager", "get-resource-policy", "--secret-id", "app/db"); g["ResourcePolicy"] != nil {
		t.Fatalf("policy not deleted: %v", g)
	}

	// Deletion with a recovery window, restore, and purge after the window.
	expectErr(t, h, "InvalidParameterException", "secretsmanager", "delete-secret", "--secret-id", "app/db", "--recovery-window-in-days", "3")
	expectErr(t, h, "InvalidParameterException", "secretsmanager", "delete-secret", "--secret-id", "app/db", "--recovery-window-in-days", "7", "--force-delete-without-recovery")
	del := h.AWSJSON(t, "secretsmanager", "delete-secret", "--secret-id", "app/db", "--recovery-window-in-days", "7")
	if dd, _ := del["DeletionDate"].(string); dd == "" {
		t.Fatalf("delete-secret: %v", del)
	}
	expectErr(t, h, "InvalidRequestException", "secretsmanager", "get-secret-value", "--secret-id", "app/db")
	expectErr(t, h, "InvalidRequestException", "secretsmanager", "put-secret-value", "--secret-id", "app/db", "--secret-string", "x")
	expectErr(t, h, "InvalidRequestException", "secretsmanager", "create-secret", "--name", "app/db", "--secret-string", "x")
	if d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", "app/db"); d["DeletedDate"] == nil {
		t.Fatalf("DeletedDate missing: %v", d)
	}
	if l := h.AWSJSON(t, "secretsmanager", "list-secrets"); len(l["SecretList"].([]any)) != 0 {
		t.Fatalf("deleted secret listed: %v", l)
	}
	if l := h.AWSJSON(t, "secretsmanager", "list-secrets", "--include-planned-deletion"); len(l["SecretList"].([]any)) != 1 {
		t.Fatalf("include-planned-deletion: %v", l)
	}
	h.AWS(t, "secretsmanager", "restore-secret", "--secret-id", "app/db")
	h.AWS(t, "secretsmanager", "get-secret-value", "--secret-id", "app/db")
	h.AWS(t, "secretsmanager", "delete-secret", "--secret-id", "app/db", "--recovery-window-in-days", "7")
	clk.Add(6 * 24 * time.Hour)
	sec.PurgeExpired()
	h.AWS(t, "secretsmanager", "describe-secret", "--secret-id", "app/db")
	clk.Add(25 * time.Hour)
	sec.PurgeExpired()
	expectErr(t, h, "ResourceNotFoundException", "secretsmanager", "describe-secret", "--secret-id", "app/db")

	// Force delete.
	h.AWS(t, "secretsmanager", "create-secret", "--name", "tmp", "--secret-string", "x")
	h.AWS(t, "secretsmanager", "delete-secret", "--secret-id", "tmp", "--force-delete-without-recovery")
	expectErr(t, h, "ResourceNotFoundException", "secretsmanager", "describe-secret", "--secret-id", "tmp")

	// Service-managed secrets stay protected.
	if _, err := sec.Put("rds!db-1", `{"password":"p"}`, "Master credentials", "rds"); err != nil {
		t.Fatal(err)
	}
	expectErr(t, h, "InvalidRequestException", "secretsmanager", "put-secret-value", "--secret-id", "rds!db-1", "--secret-string", "x")
	expectErr(t, h, "InvalidRequestException", "secretsmanager", "delete-secret", "--secret-id", "rds!db-1")
	expectErr(t, h, "InvalidRequestException", "secretsmanager", "update-secret", "--secret-id", "rds!db-1", "--description", "x")
	if d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", "rds!db-1"); d["OwningService"] != "rds" {
		t.Fatalf("managed describe: %v", d)
	}
}

func TestSecretsManagerBoto3(t *testing.T) {
	h, _, _, _ := setup(t)
	out := h.Python(t, `
sm = boto3.client("secretsmanager")
err = lambda f: (lambda: (f(), "OK"))
def code(f):
    try:
        f()
        return "OK"
    except botocore.exceptions.ClientError as e:
        return e.response["Error"]["Code"]

# Binary secrets.
sm.create_secret(Name="bin", SecretBinary=b"\x00\x01\xff")
r = sm.get_secret_value(SecretId="bin")
assert r["SecretBinary"] == b"\x00\x01\xff" and "SecretString" not in r, r

# Idempotent ClientRequestToken.
tok = "a" * 32
r1 = sm.put_secret_value(SecretId="bin", SecretString="s", ClientRequestToken=tok)
r2 = sm.put_secret_value(SecretId="bin", SecretString="s", ClientRequestToken=tok)
assert r1["VersionId"] == r2["VersionId"] == tok, (r1, r2)
print("conflict", code(lambda: sm.put_secret_value(SecretId="bin", SecretString="other", ClientRequestToken=tok)))
print("both", code(lambda: sm.create_secret(Name="x", SecretString="a", SecretBinary=b"b")))

# Pagination and filters.
for i in range(7):
    sm.create_secret(Name="app/svc%d" % i, SecretString="v%d" % i, Description="svc", Tags=[{"Key": "tier", "Value": "web" if i % 2 else "db"}])
names = []
for page in sm.get_paginator("list_secrets").paginate(Filters=[{"Key": "name", "Values": ["app/"]}], PaginationConfig={"PageSize": 3}):
    assert len(page["SecretList"]) <= 3
    names += [s["Name"] for s in page["SecretList"]]
assert sorted(names) == ["app/svc%d" % i for i in range(7)], names
web = sm.list_secrets(Filters=[{"Key": "tag-value", "Values": ["web"]}])["SecretList"]
assert len(web) == 3, web
neg = sm.list_secrets(Filters=[{"Key": "name", "Values": ["!app/"]}])["SecretList"]
assert [s["Name"] for s in neg] == ["bin"], neg
print("badfilter", code(lambda: sm.list_secrets(Filters=[{"Key": "color", "Values": ["x"]}])))
print("badtoken", code(lambda: sm.list_secrets(NextToken="garbage")))

# BatchGetSecretValue.
b = sm.batch_get_secret_value(SecretIdList=["app/svc0", "app/svc1", "missing"])
assert sorted(v["SecretString"] for v in b["SecretValues"]) == ["v0", "v1"], b
assert b["Errors"][0]["SecretId"] == "missing" and b["Errors"][0]["ErrorCode"] == "ResourceNotFoundException", b
vals, tok = [], None
while True:
    kw = dict(Filters=[{"Key": "description", "Values": ["svc"]}], MaxResults=4)
    if tok:
        kw["NextToken"] = tok
    page = sm.batch_get_secret_value(**kw)
    assert len(page["SecretValues"]) <= 4
    vals += [v["Name"] for v in page["SecretValues"]]
    tok = page.get("NextToken")
    if not tok:
        break
assert sorted(vals) == ["app/svc%d" % i for i in range(7)], vals

# Version ids.
vs = sm.list_secret_version_ids(SecretId="bin", IncludeDeprecated=True)["Versions"]
assert len(vs) == 2, vs
print("done")
`)
	for _, want := range []string{"conflict ResourceExistsException", "both InvalidParameterException", "badfilter InvalidParameterException", "badtoken InvalidNextTokenException", "done"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSecretsManagerKMSAndIAM(t *testing.T) {
	h, _, _, _ := setup(t)
	key := h.AWSJSON(t, "kms", "create-key", "--description", "secrets")["KeyMetadata"].(map[string]any)
	h.AWS(t, "kms", "create-alias", "--alias-name", "alias/secrets", "--target-key-id", key["KeyId"].(string))
	h.AWS(t, "secretsmanager", "create-secret", "--name", "k", "--secret-string", "protected", "--kms-key-id", "alias/secrets")
	d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", "k")
	if d["KmsKeyId"] != key["Arn"] {
		t.Fatalf("KmsKeyId: %v", d)
	}
	if g := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "k"); g["SecretString"] != "protected" {
		t.Fatalf("get: %v", g)
	}
	vl := h.AWSJSON(t, "secretsmanager", "list-secret-version-ids", "--secret-id", "k")
	if ids := vl["Versions"].([]any)[0].(map[string]any)["KmsKeyIds"]; ids == nil {
		t.Fatalf("KmsKeyIds: %v", vl)
	}
	expectErr(t, h, "InvalidParameterException", "secretsmanager", "create-secret", "--name", "k2", "--secret-string", "x", "--kms-key-id", "alias/none")
	// Moving to the default key re-encrypts existing versions.
	h.AWS(t, "kms", "disable-key", "--key-id", key["KeyId"].(string))
	expectErr(t, h, "DecryptionFailure", "secretsmanager", "get-secret-value", "--secret-id", "k")
	h.AWS(t, "kms", "enable-key", "--key-id", key["KeyId"].(string))
	h.AWS(t, "secretsmanager", "update-secret", "--secret-id", "k", "--kms-key-id", "alias/aws/secretsmanager")
	h.AWS(t, "kms", "disable-key", "--key-id", key["KeyId"].(string))
	if g := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "k"); g["SecretString"] != "protected" {
		t.Fatalf("after re-encrypt: %v", g)
	}

	// IAM: read-only users can describe and list but not read values.
	akid, secret := h.User(t, "ro", "ReadOnlyAccess")
	if out, err := h.AWSAs(t, akid, secret, "", "secretsmanager", "describe-secret", "--secret-id", "k"); err != nil {
		t.Fatalf("read-only describe: %v %s", err, out)
	}
	for _, args := range [][]string{
		{"secretsmanager", "get-secret-value", "--secret-id", "k"},
		{"secretsmanager", "get-secret-value", "--secret-id", "does-not-exist"},
		{"secretsmanager", "put-secret-value", "--secret-id", "k", "--secret-string", "x"},
		{"secretsmanager", "create-secret", "--name", "ro-made", "--secret-string", "x"},
		{"secretsmanager", "delete-secret", "--secret-id", "k"},
	} {
		if out, err := h.AWSAs(t, akid, secret, "", args...); err == nil || !strings.Contains(out, "AccessDeniedException") {
			t.Fatalf("%v as read-only: %v %s", args, err, out)
		}
	}
	// A user scoped to app/* secrets (AWS-style name-?????? ARN pattern).
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "app-secrets", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "secretsmanager:*", "Resource": "arn:aws:secretsmanager:*:*:secret:app/*"}}}})
	akid2, secret2 := h.User(t, "app", "app-secrets")
	if out, err := h.AWSAs(t, akid2, secret2, "", "secretsmanager", "create-secret", "--name", "app/x", "--secret-string", "v"); err != nil {
		t.Fatalf("scoped create: %v %s", err, out)
	}
	if out, err := h.AWSAs(t, akid2, secret2, "", "secretsmanager", "get-secret-value", "--secret-id", "k"); err == nil || !strings.Contains(out, "AccessDeniedException") {
		t.Fatalf("scoped user read k: %v %s", err, out)
	}
}

// rotationFunction runs a Python rotation function modelled on AWS's
// SecretsManagerRotationTemplate, calling back into the endpoint with boto3.
const rotationFunction = `
import boto3, json, os, sys, botocore
event = json.loads(os.environ["EVENT"])
sm = boto3.client("secretsmanager")
arn, token, step = event["SecretId"], event["ClientRequestToken"], event["Step"]
meta = sm.describe_secret(SecretId=arn)
if not meta["RotationEnabled"]:
    raise ValueError("rotation not enabled")
versions = meta["VersionIdsToStages"]
if token not in versions:
    raise ValueError("no stage for rotation")
if "AWSCURRENT" in versions[token]:
    sys.exit(0)
if "AWSPENDING" not in versions[token]:
    raise ValueError("not pending")
if step == "createSecret":
    sm.get_secret_value(SecretId=arn, VersionStage="AWSCURRENT")
    try:
        sm.get_secret_value(SecretId=arn, VersionId=token, VersionStage="AWSPENDING")
    except sm.exceptions.ResourceNotFoundException:
        pw = sm.get_random_password(ExcludeCharacters="/@\"'\\")["RandomPassword"]
        sm.put_secret_value(SecretId=arn, ClientRequestToken=token, SecretString=pw, VersionStages=["AWSPENDING"])
elif step == "testSecret":
    if os.environ.get("FAIL_TEST"):
        raise ValueError("test failed")
    sm.get_secret_value(SecretId=arn, VersionId=token, VersionStage="AWSPENDING")
elif step == "finishSecret":
    current = None
    for v, stages in versions.items():
        if "AWSCURRENT" in stages:
            current = v
    sm.update_secret_version_stage(SecretId=arn, VersionStage="AWSCURRENT", MoveToVersionId=token, RemoveFromVersionId=current)
`

func TestSecretRotation(t *testing.T) {
	h, sec, _, clk := setup(t)
	py := os.Getenv("HC_TEST_PYTHON")
	if py == "" {
		py = "python3"
	}
	if exec.Command(py, "-c", "import boto3").Run() != nil {
		t.Skip("python with boto3 not available (set HC_TEST_PYTHON)")
	}
	script := filepath.Join(t.TempDir(), "rotate.py")
	if err := os.WriteFile(script, []byte(rotationFunction), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var steps []string
	failTest := false
	sec.SetLambda(secrets.InvokerFunc(func(ctx context.Context, name string, payload []byte) ([]byte, string, error) {
		var ev struct{ Step string }
		_ = json.Unmarshal(payload, &ev)
		mu.Lock()
		steps = append(steps, name+":"+ev.Step)
		fail := failTest
		mu.Unlock()
		cmd := exec.CommandContext(ctx, py, script)
		cmd.Env = append(h.Environ(h.AccessKeyID, h.SecretKey, ""), "EVENT="+string(payload))
		if fail {
			cmd.Env = append(cmd.Env, "FAIL_TEST=1")
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return []byte(`{"errorMessage":` + strings.TrimSpace(strconvQuote(out.String())) + `}`), "Unhandled", nil
		}
		return []byte("null"), "", nil
	}))

	h.AWS(t, "secretsmanager", "create-secret", "--name", "rot", "--secret-string", "initial")
	fn := "arn:aws:lambda:us-east-1:" + h.Env.AccountID + ":function:rotator"
	expectErr(t, h, "InvalidRequestException", "secretsmanager", "rotate-secret", "--secret-id", "rot")
	r := h.AWSJSON(t, "secretsmanager", "rotate-secret", "--secret-id", "rot", "--rotation-lambda-arn", fn,
		"--rotation-rules", "AutomaticallyAfterDays=30")
	token := r["VersionId"].(string)
	sec.WaitRotations()
	if strings.Join(steps, ",") != "rotator:createSecret,rotator:setSecret,rotator:testSecret,rotator:finishSecret" {
		t.Fatalf("steps: %v", steps)
	}
	cur := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "rot")
	if cur["VersionId"] != token || cur["SecretString"] == "initial" {
		t.Fatalf("after rotation: %v", cur)
	}
	if prev := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "rot", "--version-stage", "AWSPREVIOUS"); prev["SecretString"] != "initial" {
		t.Fatalf("previous after rotation: %v", prev)
	}
	d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", "rot")
	if d["RotationEnabled"] != true || d["RotationLambdaARN"] != fn || d["LastRotatedDate"] == nil || d["NextRotationDate"] == nil {
		t.Fatalf("describe after rotation: %v", d)
	}
	if st := d["VersionIdsToStages"].(map[string]any)[token].([]any); len(st) != 1 || st[0] != "AWSCURRENT" {
		t.Fatalf("stages of the rotated version: %v", st)
	}

	// Scheduled rotation after 30 days.
	rotated := cur["SecretString"]
	clk.Add(29 * 24 * time.Hour)
	sec.PurgeExpired()
	sec.WaitRotations()
	if g := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "rot"); g["SecretString"] != rotated {
		t.Fatal("rotated before the schedule")
	}
	clk.Add(2 * 24 * time.Hour)
	sec.PurgeExpired()
	sec.WaitRotations()
	if g := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "rot"); g["SecretString"] == rotated {
		t.Fatal("scheduled rotation did not run")
	}

	// A failing rotation leaves AWSPENDING; a new rotation is refused until it is cleared.
	mu.Lock()
	failTest = true
	mu.Unlock()
	r = h.AWSJSON(t, "secretsmanager", "rotate-secret", "--secret-id", "rot")
	sec.WaitRotations()
	pending := r["VersionId"].(string)
	if g := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "rot", "--version-stage", "AWSPENDING"); g["VersionId"] != pending {
		t.Fatalf("pending version: %v", g)
	}
	expectErr(t, h, "InvalidRequestException", "secretsmanager", "rotate-secret", "--secret-id", "rot")
	h.AWS(t, "secretsmanager", "update-secret-version-stage", "--secret-id", "rot", "--version-stage", "AWSPENDING", "--remove-from-version-id", pending)
	mu.Lock()
	failTest = false
	mu.Unlock()
	h.AWS(t, "secretsmanager", "rotate-secret", "--secret-id", "rot", "--no-rotate-immediately")
	h.AWS(t, "secretsmanager", "cancel-rotate-secret", "--secret-id", "rot")
	if d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", "rot"); d["RotationEnabled"] != false {
		t.Fatalf("cancel-rotate-secret: %v", d)
	}
	// Invoking the function needs lambda:InvokeFunction.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "sm-only", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "secretsmanager:*", "Resource": "*"}}}})
	akid, secret := h.User(t, "smonly", "sm-only")
	if out, err := h.AWSAs(t, akid, secret, "", "secretsmanager", "rotate-secret", "--secret-id", "rot", "--rotation-lambda-arn", fn); err == nil || !strings.Contains(out, "AccessDeniedException") {
		t.Fatalf("rotate without lambda permission: %v %s", err, out)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestSecretsNativeAPI(t *testing.T) {
	h, sec, _, _ := setup(t)
	sec.Routes(h.Router)
	b := string(h.Native(t, "POST", "/api/v1/secrets", map[string]any{"name": "native/one", "value": "v1", "description": "d"}))
	if !strings.Contains(b, `:secret:native/one-`) || strings.Contains(b, "ciphertext") {
		t.Fatalf("native create: %s", b)
	}
	h.Native(t, "PUT", "/api/v1/secrets/native%2Fone/value", map[string]any{"value": "v2"})
	if g := h.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "native/one"); g["SecretString"] != "v2" {
		t.Fatalf("aws read of native write: %v", g)
	}
	if v := string(h.Native(t, "GET", "/api/v1/secrets/native%2Fone/value?version_stage=AWSPREVIOUS", nil)); !strings.Contains(strings.ReplaceAll(v, " ", ""), `"value":"v1"`) {
		t.Fatalf("native previous: %s", v)
	}
	h.Native(t, "DELETE", "/api/v1/secrets/native%2Fone?recovery_days=2", nil)
	if d := h.AWSJSON(t, "secretsmanager", "describe-secret", "--secret-id", "native/one"); d["DeletedDate"] == nil {
		t.Fatalf("native delete: %v", d)
	}
	h.Native(t, "POST", "/api/v1/secrets/native%2Fone/restore", nil)
	if l := string(h.Native(t, "GET", "/api/v1/secrets", nil)); !strings.Contains(l, "native/one") {
		t.Fatalf("native list: %s", l)
	}
	if p := string(h.Native(t, "POST", "/api/v1/secrets/random-password?length=16", nil)); !regexp.MustCompile(`"password": "[A-Za-z0-9]{16}"`).MatchString(p) {
		t.Fatalf("native random password: %s", p)
	}
}
