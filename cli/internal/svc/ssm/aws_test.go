package ssm_test

import (
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ssm"
)

func setup(t *testing.T) (*awstest.Harness, *ssm.Service) {
	h := awstest.New(t)
	k := kms.New(h.Env, h.Secrets)
	s := ssm.New(h.Env, k)
	h.Secrets.KMS, s.Secrets = k, h.Secrets
	h.Secrets.RegisterAWS()
	k.RegisterAWS()
	s.RegisterAWS()
	s.Routes(h.Router)
	return h, s
}

func expectErr(t *testing.T, h *awstest.Harness, code string, args ...string) {
	t.Helper()
	out, err := h.AWSErr(t, args...)
	if err == nil || !strings.Contains(out, code) {
		t.Fatalf("aws %s: want %s, got err=%v\n%s", strings.Join(args, " "), code, err, out)
	}
}

func param(t *testing.T, h *awstest.Harness, args ...string) map[string]any {
	t.Helper()
	return h.AWSJSON(t, append([]string{"ssm", "get-parameter"}, args...)...)["Parameter"].(map[string]any)
}

func TestParameterStoreCLI(t *testing.T) {
	h, _ := setup(t)
	acct := h.Env.AccountID
	r := h.AWSJSON(t, "ssm", "put-parameter", "--name", "/app/db/url", "--value", "postgres://a", "--type", "String",
		"--description", "db url", "--tags", "Key=env,Value=dev")
	if r["Version"] != float64(1) || r["Tier"] != "Standard" {
		t.Fatalf("put-parameter: %v", r)
	}
	expectErr(t, h, "ParameterAlreadyExists", "ssm", "put-parameter", "--name", "/app/db/url", "--value", "x", "--type", "String")
	expectErr(t, h, "ValidationException", "ssm", "put-parameter", "--name", "/app/db/url", "--value", "x", "--overwrite", "--tags", "Key=a,Value=b")
	h.AWS(t, "ssm", "put-parameter", "--name", "/app/db/url", "--value", "postgres://b", "--overwrite")
	p := param(t, h, "--name", "/app/db/url")
	if p["Value"] != "postgres://b" || p["Version"] != float64(2) || p["ARN"] != "arn:aws:ssm:us-east-1:"+acct+":parameter/app/db/url" || p["DataType"] != "text" {
		t.Fatalf("get-parameter: %v", p)
	}
	// Selectors: version number and label; lookups by ARN.
	if p := param(t, h, "--name", "/app/db/url:1"); p["Value"] != "postgres://a" || p["Selector"] != ":1" {
		t.Fatalf("name:1: %v", p)
	}
	lab := h.AWSJSON(t, "ssm", "label-parameter-version", "--name", "/app/db/url", "--parameter-version", "1", "--labels", "stable", "1bad", "awsx")
	if lab["ParameterVersion"] != float64(1) || len(lab["InvalidLabels"].([]any)) != 2 {
		t.Fatalf("label-parameter-version: %v", lab)
	}
	if p := param(t, h, "--name", "/app/db/url:stable"); p["Version"] != float64(1) {
		t.Fatalf("name:label: %v", p)
	}
	h.AWS(t, "ssm", "label-parameter-version", "--name", "/app/db/url", "--labels", "stable") // moves to the latest version
	if p := param(t, h, "--name", "/app/db/url:stable"); p["Version"] != float64(2) {
		t.Fatalf("label moved: %v", p)
	}
	if p := param(t, h, "--name", "arn:aws:ssm:us-east-1:"+acct+":parameter/app/db/url"); p["Value"] != "postgres://b" {
		t.Fatalf("by ARN: %v", p)
	}
	un := h.AWSJSON(t, "ssm", "unlabel-parameter-version", "--name", "/app/db/url", "--parameter-version", "2", "--labels", "stable", "nope")
	if len(un["RemovedLabels"].([]any)) != 1 || len(un["InvalidLabels"].([]any)) != 1 {
		t.Fatalf("unlabel: %v", un)
	}
	expectErr(t, h, "ParameterVersionNotFound", "ssm", "get-parameter", "--name", "/app/db/url:stable")
	expectErr(t, h, "ParameterVersionNotFound", "ssm", "get-parameter", "--name", "/app/db/url:9")
	expectErr(t, h, "ParameterNotFound", "ssm", "get-parameter", "--name", "/nope")
	expectErr(t, h, "ParameterVersionNotFound", "ssm", "label-parameter-version", "--name", "/app/db/url", "--parameter-version", "7", "--labels", "x")

	// Validation.
	expectErr(t, h, "ValidationException", "ssm", "put-parameter", "--name", "/aws/x", "--value", "v", "--type", "String")
	expectErr(t, h, "ValidationException", "ssm", "put-parameter", "--name", "a/b", "--value", "v", "--type", "String")
	expectErr(t, h, "HierarchyLevelLimitExceededException", "ssm", "put-parameter", "--name", "/1/2/3/4/5/6/7/8/9/10/11/12/13/14/15/16", "--value", "v", "--type", "String")
	expectErr(t, h, "ParameterPatternMismatchException", "ssm", "put-parameter", "--name", "/app/port", "--value", "abc", "--type", "String", "--allowed-pattern", `^\d+$`)
	expectErr(t, h, "InvalidAllowedPatternException", "ssm", "put-parameter", "--name", "/app/port", "--value", "abc", "--type", "String", "--allowed-pattern", `(`)
	h.AWS(t, "ssm", "put-parameter", "--name", "/app/port", "--value", "80", "--type", "String", "--allowed-pattern", `^\d+$`)
	expectErr(t, h, "ParameterPatternMismatchException", "ssm", "put-parameter", "--name", "/app/port", "--value", "x", "--overwrite")
	expectErr(t, h, "ValidationException", "ssm", "put-parameter", "--name", "/app/big", "--value", strings.Repeat("x", 4097), "--type", "String")
	big := h.AWSJSON(t, "ssm", "put-parameter", "--name", "/app/big", "--value", strings.Repeat("x", 5000), "--type", "String", "--tier", "Intelligent-Tiering")
	if big["Tier"] != "Advanced" {
		t.Fatalf("intelligent tiering: %v", big)
	}
	expectErr(t, h, "ValidationException", "ssm", "put-parameter", "--name", "/app/big", "--value", "x", "--overwrite", "--tier", "Standard")
	expectErr(t, h, "ValidationException", "ssm", "put-parameter", "--name", "/app/ami", "--value", "not-an-ami", "--type", "String", "--data-type", "aws:ec2:image")
	h.AWS(t, "ssm", "put-parameter", "--name", "/app/ami", "--value", "ami-0123456789abcdef0", "--type", "String", "--data-type", "aws:ec2:image")
	h.AWS(t, "ssm", "put-parameter", "--name", "/app/hosts", "--value", "a,b,c", "--type", "StringList")

	// SecureString with the default key and a customer key.
	h.AWS(t, "ssm", "put-parameter", "--name", "/app/db/password", "--value", "s3cret", "--type", "SecureString")
	enc := param(t, h, "--name", "/app/db/password")
	if enc["Type"] != "SecureString" || enc["Value"] == "s3cret" {
		t.Fatalf("encrypted value returned: %v", enc)
	}
	if dec := param(t, h, "--name", "/app/db/password", "--with-decryption"); dec["Value"] != "s3cret" {
		t.Fatalf("decrypted: %v", dec)
	}
	key := h.AWSJSON(t, "kms", "create-key")["KeyMetadata"].(map[string]any)
	h.AWS(t, "kms", "create-alias", "--alias-name", "alias/params", "--target-key-id", key["KeyId"].(string))
	h.AWS(t, "ssm", "put-parameter", "--name", "/app/api-key", "--value", "k1", "--type", "SecureString", "--key-id", "alias/params")
	expectErr(t, h, "InvalidKeyId", "ssm", "put-parameter", "--name", "/app/other", "--value", "k1", "--type", "SecureString", "--key-id", "alias/none")
	h.AWS(t, "kms", "disable-key", "--key-id", "alias/params")
	expectErr(t, h, "DisabledException", "ssm", "get-parameter", "--name", "/app/api-key", "--with-decryption")
	h.AWS(t, "kms", "enable-key", "--key-id", "alias/params")

	// History.
	hist := h.AWSJSON(t, "ssm", "get-parameter-history", "--name", "/app/db/url")["Parameters"].([]any)
	if len(hist) != 2 || hist[0].(map[string]any)["Description"] != "db url" {
		t.Fatalf("history: %v", hist)
	}

	// Describe with filters; tags.
	d := h.AWSJSON(t, "ssm", "describe-parameters", "--parameter-filters", "Key=Type,Values=SecureString")["Parameters"].([]any)
	if len(d) != 2 {
		t.Fatalf("describe Type=SecureString: %v", d)
	}
	for _, x := range d {
		m := x.(map[string]any)
		if m["Name"] == "/app/db/password" && m["KeyId"] != "alias/aws/ssm" {
			t.Fatalf("default KeyId: %v", m)
		}
	}
	if d := h.AWSJSON(t, "ssm", "describe-parameters", "--parameter-filters", "Key=Path,Option=OneLevel,Values=/app/db")["Parameters"].([]any); len(d) != 2 {
		t.Fatalf("describe Path: %v", d)
	}
	if d := h.AWSJSON(t, "ssm", "describe-parameters", "--parameter-filters", "Key=Name,Option=Contains,Values=db")["Parameters"].([]any); len(d) != 2 {
		t.Fatalf("describe Name Contains: %v", d)
	}
	if d := h.AWSJSON(t, "ssm", "describe-parameters", "--parameter-filters", "Key=tag:env,Values=dev")["Parameters"].([]any); len(d) != 1 {
		t.Fatalf("describe tag: %v", d)
	}
	if d := h.AWSJSON(t, "ssm", "describe-parameters", "--filters", "Key=Name,Values=/app/db")["Parameters"].([]any); len(d) != 2 {
		t.Fatalf("describe legacy filter: %v", d)
	}
	expectErr(t, h, "InvalidFilterKey", "ssm", "describe-parameters", "--parameter-filters", "Key=Color,Values=x")
	expectErr(t, h, "InvalidFilterOption", "ssm", "describe-parameters", "--parameter-filters", "Key=Type,Option=Contains,Values=x")
	h.AWS(t, "ssm", "add-tags-to-resource", "--resource-type", "Parameter", "--resource-id", "/app/port", "--tags", "Key=team,Value=a", "Key=x,Value=y")
	h.AWS(t, "ssm", "remove-tags-from-resource", "--resource-type", "Parameter", "--resource-id", "/app/port", "--tag-keys", "x")
	if tl := h.AWSJSON(t, "ssm", "list-tags-for-resource", "--resource-type", "Parameter", "--resource-id", "/app/port")["TagList"].([]any); len(tl) != 1 {
		t.Fatalf("tags: %v", tl)
	}
	expectErr(t, h, "InvalidResourceType", "ssm", "list-tags-for-resource", "--resource-type", "Document", "--resource-id", "/app/port")
	expectErr(t, h, "InvalidResourceId", "ssm", "add-tags-to-resource", "--resource-type", "Parameter", "--resource-id", "/nope", "--tags", "Key=a,Value=b")

	// GetParameters / delete.
	gp := h.AWSJSON(t, "ssm", "get-parameters", "--names", "/app/port", "/app/db/password", "/missing", "--with-decryption")
	if len(gp["Parameters"].([]any)) != 2 || gp["InvalidParameters"].([]any)[0] != "/missing" {
		t.Fatalf("get-parameters: %v", gp)
	}
	h.AWS(t, "ssm", "delete-parameter", "--name", "/app/port")
	expectErr(t, h, "ParameterNotFound", "ssm", "delete-parameter", "--name", "/app/port")
	dp := h.AWSJSON(t, "ssm", "delete-parameters", "--names", "/app/hosts", "/missing")
	if len(dp["DeletedParameters"].([]any)) != 1 || len(dp["InvalidParameters"].([]any)) != 1 {
		t.Fatalf("delete-parameters: %v", dp)
	}
}

func TestParameterStoreBoto3(t *testing.T) {
	h, _ := setup(t)
	out := h.Python(t, `
ssm = boto3.client("ssm")
sm = boto3.client("secretsmanager")
def code(f):
    try:
        f()
        return "OK"
    except botocore.exceptions.ClientError as e:
        return e.response["Error"]["Code"]

for i in range(12):
    ssm.put_parameter(Name="/svc/a/p%02d" % i, Value="v%d" % i, Type="String")
for i in range(3):
    ssm.put_parameter(Name="/svc/a/deep/q%d" % i, Value="s%d" % i, Type="SecureString")
ssm.put_parameter(Name="/svc/b", Value="b", Type="String")

one = [p["Name"] for pg in ssm.get_paginator("get_parameters_by_path").paginate(Path="/svc/a") for p in pg["Parameters"]]
assert len(one) == 12, one
rec = [p for pg in ssm.get_paginator("get_parameters_by_path").paginate(Path="/svc/a", Recursive=True, WithDecryption=True, PaginationConfig={"PageSize": 5}) for p in pg["Parameters"]]
assert len(rec) == 15, len(rec)
assert sorted(p["Value"] for p in rec if p["Type"] == "SecureString") == ["s0", "s1", "s2"], rec
sec = [p["Name"] for pg in ssm.get_paginator("get_parameters_by_path").paginate(Path="/svc", Recursive=True, ParameterFilters=[{"Key": "Type", "Values": ["SecureString"]}]) for p in pg["Parameters"]]
assert len(sec) == 3, sec
print("badfilter", code(lambda: ssm.get_parameters_by_path(Path="/svc", ParameterFilters=[{"Key": "Name", "Values": ["x"]}])))
print("badmax", code(lambda: ssm.get_parameters_by_path(Path="/svc", MaxResults=11)))
print("badtoken", code(lambda: ssm.get_parameters_by_path(Path="/svc", NextToken="zzz")))
root = [p["Name"] for pg in ssm.get_paginator("get_parameters_by_path").paginate(Path="/", Recursive=True) for p in pg["Parameters"]]
assert len(root) == 16, root

desc = [p["Name"] for pg in ssm.get_paginator("describe_parameters").paginate(PaginationConfig={"PageSize": 4}) for p in pg["Parameters"]]
assert len(desc) == 16 and desc == sorted(desc), desc

# Labels select versions in GetParametersByPath.
ssm.put_parameter(Name="/svc/b", Value="b2", Type="String", Overwrite=True)
ssm.label_parameter_version(Name="/svc/b", ParameterVersion=1, Labels=["old"])
lbl = ssm.get_parameters_by_path(Path="/svc", ParameterFilters=[{"Key": "Label", "Option": "Equals", "Values": ["old"]}])["Parameters"]
assert len(lbl) == 1 and lbl[0]["Value"] == "b" and lbl[0]["Version"] == 1, lbl

# Version limit: 100 versions, the oldest is dropped unless labeled.
for i in range(100):
    ssm.put_parameter(Name="/svc/c", Value="c%d" % i, Type="String", Overwrite=True)
ssm.label_parameter_version(Name="/svc/c", ParameterVersion=1, Labels=["first"])
print("maxver", code(lambda: ssm.put_parameter(Name="/svc/c", Value="more", Type="String", Overwrite=True)))
ssm.unlabel_parameter_version(Name="/svc/c", ParameterVersion=1, Labels=["first"])
r = ssm.put_parameter(Name="/svc/c", Value="more", Type="String", Overwrite=True)
assert r["Version"] == 101, r
hist = [p["Version"] for pg in ssm.get_paginator("get_parameter_history").paginate(Name="/svc/c") for p in pg["Parameters"]]
assert len(hist) == 100 and hist[0] == 2, hist[:3]

# Secrets Manager references.
sm.create_secret(Name="shared/token", SecretString="tok-1")
ref = ssm.get_parameter(Name="/aws/reference/secretsmanager/shared/token", WithDecryption=True)["Parameter"]
assert ref["Value"] == "tok-1" and ref["Type"] == "SecureString" and ref["ARN"].startswith("arn:aws:secretsmanager:") and "SourceResult" in ref, ref
sm.put_secret_value(SecretId="shared/token", SecretString="tok-2")
prev = ssm.get_parameter(Name="/aws/reference/secretsmanager/shared/token:AWSPREVIOUS", WithDecryption=True)["Parameter"]
assert prev["Value"] == "tok-1", prev
print("refnodecrypt", code(lambda: ssm.get_parameter(Name="/aws/reference/secretsmanager/shared/token")))
print("refmissing", code(lambda: ssm.get_parameter(Name="/aws/reference/secretsmanager/none", WithDecryption=True)))
print("done")
`)
	for _, want := range []string{"badfilter InvalidFilterKey", "badmax ValidationException", "badtoken InvalidNextToken", "maxver ParameterMaxVersionLimitExceeded",
		"refnodecrypt ValidationException", "refmissing ParameterNotFound", "done"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestParameterStoreIAM(t *testing.T) {
	h, _ := setup(t)
	h.AWS(t, "ssm", "put-parameter", "--name", "/app/plain", "--value", "p", "--type", "String")
	h.AWS(t, "ssm", "put-parameter", "--name", "/app/secret", "--value", "s", "--type", "SecureString")
	h.AWS(t, "secretsmanager", "create-secret", "--name", "ref", "--secret-string", "r")
	akid, secret := h.User(t, "ro", "ReadOnlyAccess")
	if out, err := h.AWSAs(t, akid, secret, "", "ssm", "get-parameter", "--name", "/app/plain"); err != nil {
		t.Fatalf("read-only get: %v %s", err, out)
	}
	if out, err := h.AWSAs(t, akid, secret, "", "ssm", "get-parameter", "--name", "/app/secret"); err != nil {
		t.Fatalf("read-only get (encrypted): %v %s", err, out)
	}
	for _, args := range [][]string{
		{"ssm", "get-parameter", "--name", "/app/secret", "--with-decryption"},
		{"ssm", "get-parameters-by-path", "--path", "/app", "--with-decryption"},
		{"ssm", "get-parameter", "--name", "/aws/reference/secretsmanager/ref", "--with-decryption"},
		{"ssm", "put-parameter", "--name", "/app/new", "--value", "x", "--type", "String"},
		{"ssm", "delete-parameter", "--name", "/app/plain"},
	} {
		if out, err := h.AWSAs(t, akid, secret, "", args...); err == nil || !strings.Contains(out, "AccessDeniedException") {
			t.Fatalf("%v as read-only: %v %s", args, err, out)
		}
	}
	// A path-scoped user sees only its hierarchy.
	h.AWS(t, "ssm", "put-parameter", "--name", "/team/a", "--value", "a", "--type", "String")
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "team", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "ssm:*", "Resource": "arn:aws:ssm:*:*:parameter/team/*"}}}})
	akid2, secret2 := h.User(t, "team", "team")
	out, err := h.AWSAs(t, akid2, secret2, "", "ssm", "get-parameters-by-path", "--path", "/", "--recursive")
	if err != nil || !strings.Contains(out, "/team/a") || strings.Contains(out, "/app/plain") {
		t.Fatalf("scoped by-path: %v %s", err, out)
	}
	if out, err := h.AWSAs(t, akid2, secret2, "", "ssm", "get-parameter", "--name", "/app/plain"); err == nil || !strings.Contains(out, "AccessDeniedException") {
		t.Fatalf("scoped get outside path: %v %s", err, out)
	}
	// The native API still works (and shares the rules).
	h.Native(t, "PUT", "/api/v1/ssm/parameter", map[string]any{"name": "/native/x", "value": "n", "type": "SecureString"})
	if b := string(h.Native(t, "GET", "/api/v1/ssm/parameter?name=/native/x&with_decryption=true", nil)); !strings.Contains(strings.ReplaceAll(b, " ", ""), `"value":"n"`) {
		t.Fatalf("native get: %s", b)
	}
}
