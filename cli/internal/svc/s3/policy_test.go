package s3

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestBucketPolicyConditions drives bucket policies with condition keys through
// the AWS CLI as users with and without identity permissions.
func TestBucketPolicyConditions(t *testing.T) {
	h, _ := newHarness(t)
	acct := h.Env.AccountID
	b := bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	dir := t.TempDir()
	body := dir + "/f"
	_ = os.WriteFile(body, []byte("payload"), 0o600)
	for _, k := range []string{"docs/a.txt", "docs/b.txt", "other/c.txt", "root.txt"} {
		h.AWS(t, "s3", "cp", body, "s3://"+b+"/"+k)
	}

	full := "arn:aws:s3:::" + b
	user := func(name string) string { return fmt.Sprintf("arn:aws:iam::%s:user/%s", acct, name) }
	putPolicy := func(stmts ...string) {
		t.Helper()
		h.AWS(t, "s3api", "put-bucket-policy", "--bucket", b, "--policy", `{"Version":"2012-10-17","Statement":[`+strings.Join(stmts, ",")+`]}`)
	}
	stmt := func(effect, principal, actions, resource, cond string) string {
		if cond != "" {
			cond = `,"Condition":` + cond
		}
		return fmt.Sprintf(`{"Effect":"%s","Principal":%s,"Action":%s,"Resource":%s%s}`, effect, principal, actions, resource, cond)
	}
	awsP := func(name string) string { return `{"AWS":"` + user(name) + `"}` }
	objs := `"` + full + `/*"`
	bkt := `"` + full + `"`
	both := `["` + full + `","` + full + `/*"]`

	ak, as := h.User(t, "alice") // no identity policy
	fk, fs := h.User(t, "frank", "AmazonS3FullAccess")

	denied := func(ak, sk string, args ...string) {
		t.Helper()
		out, err := h.AWSAs(t, ak, sk, "", args...)
		if err == nil || !strings.Contains(out, "AccessDenied") && !strings.Contains(out, "403") {
			t.Fatalf("aws %s: want a denial, got %v %s", strings.Join(args, " "), err, out)
		}
	}
	ok := func(ak, sk string, args ...string) string {
		t.Helper()
		out, err := h.AWSAs(t, ak, sk, "", args...)
		if err != nil {
			t.Fatalf("aws %s: %v %s", strings.Join(args, " "), err, out)
		}
		return out
	}
	get := func(key string) []string {
		return []string{"s3api", "get-object", "--bucket", b, "--key", key, dir + "/out"}
	}

	// aws:SecureTransport: the test endpoint is plain HTTP.
	putPolicy(stmt("Deny", `"*"`, `"s3:*"`, both, `{"Bool":{"aws:SecureTransport":"false"}}`))
	denied(fk, fs, get("root.txt")...)
	ok(h.AccessKeyID, h.SecretKey, get("root.txt")...) // the account root is never locked out
	putPolicy(stmt("Deny", `"*"`, `"s3:*"`, both, `{"Bool":{"aws:SecureTransport":"true"}}`))
	ok(fk, fs, get("root.txt")...)
	// A grant restricted to TLS does not apply to plain HTTP.
	putPolicy(stmt("Allow", awsP("alice"), `"s3:GetObject"`, objs, `{"Bool":{"aws:SecureTransport":"true"}}`))
	denied(ak, as, get("root.txt")...)

	// aws:SourceIp, for a named user and for anonymous callers.
	putPolicy(stmt("Allow", awsP("alice"), `"s3:GetObject"`, objs, `{"IpAddress":{"aws:SourceIp":["10.0.0.0/8","127.0.0.0/8","::1/128"]}}`))
	ok(ak, as, get("root.txt")...)
	putPolicy(stmt("Allow", awsP("alice"), `"s3:GetObject"`, objs, `{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}`))
	denied(ak, as, get("root.txt")...)
	putPolicy(stmt("Deny", awsP("frank"), `"s3:GetObject"`, objs, `{"NotIpAddress":{"aws:SourceIp":"10.0.0.0/8"}}`))
	denied(fk, fs, get("root.txt")...)
	curl := func(args ...string) string {
		out, _ := exec.Command("curl", append([]string{"-s", "-o", "/dev/null", "-w", "%{http_code}"}, args...)...).Output()
		return string(out)
	}
	putPolicy(stmt("Allow", `"*"`, `"s3:GetObject"`, objs, `{"IpAddress":{"aws:SourceIp":["10.0.0.0/8"]}}`))
	if code := curl(h.URL + "/" + b + "/root.txt"); code != "403" {
		t.Fatalf("anonymous GET from outside the allowed range: %s", code)
	}
	putPolicy(stmt("Allow", `"*"`, `"s3:GetObject"`, objs, `{"IpAddress":{"aws:SourceIp":["127.0.0.0/8","::1/128"]}}`))
	if code := curl(h.URL + "/" + b + "/root.txt"); code != "200" {
		t.Fatalf("anonymous GET from an allowed range: %s", code)
	}

	// aws:PrincipalArn and aws:username.
	putPolicy(stmt("Allow", `"*"`, `"s3:GetObject"`, objs, `{"ArnLike":{"aws:PrincipalArn":"arn:aws:iam::`+acct+`:user/al*"}}`))
	ok(ak, as, get("root.txt")...)
	// MinIO cannot store ArnLike; HomeCloud still returns the document as it was put.
	if p := ok(h.AccessKeyID, h.SecretKey, "s3api", "get-bucket-policy", "--bucket", b); !strings.Contains(p, "ArnLike") {
		t.Fatalf("get-bucket-policy: %s", p)
	}
	for _, bad := range []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`, // no Principal
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"*","Condition":{"Frobnicate":{"aws:SourceIp":"1.1.1.1"}}}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Maybe","Principal":"*","Action":"s3:GetObject","Resource":"*"}]}`} {
		out, err := h.AWSAs(t, h.AccessKeyID, h.SecretKey, "", "s3api", "put-bucket-policy", "--bucket", b, "--policy", bad)
		if err == nil || !strings.Contains(out, "MalformedPolicy") {
			t.Fatalf("malformed policy accepted: %v %s", err, out)
		}
	}
	gk, gs := h.User(t, "gina")
	denied(gk, gs, get("root.txt")...)
	putPolicy(stmt("Deny", `"*"`, `"s3:GetObject"`, objs, `{"StringEquals":{"aws:username":"frank"}}`))
	denied(fk, fs, get("root.txt")...)
	putPolicy(stmt("Allow", `"*"`, `"s3:GetObject"`, objs, `{"StringEquals":{"aws:PrincipalAccount":"`+acct+`"}}`))
	ok(ak, as, get("root.txt")...)

	// s3:prefix, s3:delimiter and s3:max-keys scope listings.
	list := func(args ...string) []string {
		return append([]string{"s3api", "list-objects-v2", "--bucket", b}, args...)
	}
	putPolicy(stmt("Allow", awsP("alice"), `"s3:ListBucket"`, bkt, `{"StringLike":{"s3:prefix":["docs/*","docs"]}}`))
	out := ok(ak, as, list("--prefix", "docs/")...)
	if !strings.Contains(out, "docs/a.txt") || strings.Contains(out, "other/c.txt") {
		t.Fatalf("prefixed listing: %s", out)
	}
	denied(ak, as, list("--prefix", "other/")...)
	denied(ak, as, list()...)
	putPolicy(stmt("Allow", awsP("alice"), `"s3:ListBucket"`, bkt, `{"StringEquals":{"s3:prefix":[""],"s3:delimiter":["/"]}}`))
	ok(ak, as, list("--delimiter", "/", "--prefix", "")...)
	denied(ak, as, list("--prefix", "docs/", "--delimiter", "/")...)
	putPolicy(stmt("Allow", awsP("alice"), `"s3:ListBucket"`, bkt, `{"NumericLessThanEquals":{"s3:max-keys":"5"}}`))
	ok(ak, as, list("--max-keys", "3")...)
	denied(ak, as, list("--max-keys", "50")...)
	denied(ak, as, list()...) // no max-keys: the key is absent, so the condition fails
	putPolicy(stmt("Deny", `"*"`, `"s3:ListBucket"`, bkt, `{"NumericGreaterThan":{"s3:max-keys":"10"}}`))
	ok(fk, fs, list("--max-keys", "5")...)
	denied(fk, fs, list("--max-keys", "500")...)

	// The user's own identity policy can be scoped by prefix too, using policy variables.
	h.Native(t, "PUT", "/api/v1/iam/users/alice/inline-policies/scoped", map[string]any{"Version": "2012-10-17", "Statement": []any{
		map[string]any{"Effect": "Allow", "Action": "s3:ListBucket", "Resource": full,
			"Condition": map[string]any{"StringLike": map[string]any{"s3:prefix": []string{"${aws:username}/*", "other/*"}}}}}})
	putPolicy(stmt("Allow", awsP("frank"), `"s3:GetObject"`, objs, ""))
	ok(ak, as, list("--prefix", "other/")...)
	denied(ak, as, list("--prefix", "docs/")...)
	h.Native(t, "DELETE", "/api/v1/iam/users/alice/inline-policies/scoped", nil)

	// s3:x-amz-acl.
	putPolicy(stmt("Allow", awsP("alice"), `"s3:PutObject"`, objs, ""),
		stmt("Deny", `"*"`, `"s3:PutObject"`, objs, `{"StringEquals":{"s3:x-amz-acl":["public-read","public-read-write"]}}`))
	ok(ak, as, "s3api", "put-object", "--bucket", b, "--key", "up/plain", "--body", body)
	ok(ak, as, "s3api", "put-object", "--bucket", b, "--key", "up/private", "--body", body, "--acl", "private")
	denied(ak, as, "s3api", "put-object", "--bucket", b, "--key", "up/public", "--body", body, "--acl", "public-read")
	ok(h.AccessKeyID, h.SecretKey, "s3api", "put-object", "--bucket", b, "--key", "up/root-public", "--body", body, "--acl", "public-read")

	// s3:RequestObjectTag/<key>: uploads must be tagged.
	putPolicy(stmt("Allow", awsP("alice"), `["s3:PutObject","s3:PutObjectTagging"]`, objs, ""),
		stmt("Deny", awsP("alice"), `"s3:PutObject"`, objs, `{"Null":{"s3:RequestObjectTag/owner":"true"}}`))
	denied(ak, as, "s3api", "put-object", "--bucket", b, "--key", "tagged/untagged", "--body", body)
	ok(ak, as, "s3api", "put-object", "--bucket", b, "--key", "tagged/ok", "--body", body, "--tagging", "owner=alice")
	putPolicy(stmt("Allow", awsP("alice"), `["s3:PutObject","s3:PutObjectTagging"]`, objs, `{"StringEquals":{"s3:RequestObjectTag/env":"prod"}}`))
	denied(ak, as, "s3api", "put-object", "--bucket", b, "--key", "tagged/dev", "--body", body, "--tagging", "env=dev")
	ok(ak, as, "s3api", "put-object", "--bucket", b, "--key", "tagged/prod", "--body", body, "--tagging", "env=prod")

	// s3:ExistingObjectTag/<key>: access follows the tags an object already has.
	h.AWS(t, "s3api", "put-object", "--bucket", b, "--key", "secret.txt", "--body", body, "--tagging", "classification=secret")
	h.AWS(t, "s3api", "put-object", "--bucket", b, "--key", "shared.txt", "--body", body, "--tagging", "classification=public")
	putPolicy(stmt("Allow", awsP("alice"), `"s3:GetObject"`, objs, `{"StringEquals":{"s3:ExistingObjectTag/classification":"public"}}`))
	ok(ak, as, get("shared.txt")...)
	denied(ak, as, get("secret.txt")...)
	putPolicy(stmt("Deny", `"*"`, `"s3:GetObject"`, objs, `{"StringEquals":{"s3:ExistingObjectTag/classification":"secret"}}`))
	ok(fk, fs, get("shared.txt")...)
	denied(fk, fs, get("secret.txt")...)

	// The same policies apply on the native API.
	putPolicy(stmt("Deny", awsP("frank"), `"s3:ListBucket"`, bkt, `{"Bool":{"aws:SecureTransport":"false"}}`))
	if st, _ := h.NativeAs(t, fk, fs, "GET", "/api/v1/s3/buckets/"+b+"/objects", nil); st != 403 {
		t.Fatalf("native list by frank: %d", st)
	}
	putPolicy(stmt("Deny", awsP("frank"), `"s3:ListBucket"`, bkt, `{"Bool":{"aws:SecureTransport":"true"}}`))
	if st, body := h.NativeAs(t, fk, fs, "GET", "/api/v1/s3/buckets/"+b+"/objects", nil); st != 200 {
		t.Fatalf("native list by frank: %d %s", st, body)
	}
	putPolicy(stmt("Allow", awsP("alice"), `"s3:ListBucket"`, bkt, ""))
	if st, body := h.NativeAs(t, ak, as, "GET", "/api/v1/s3/buckets/"+b+"/objects", nil); st != 200 {
		t.Fatalf("native list by alice through the bucket policy: %d %s", st, body)
	}

	h.AWS(t, "s3", "rb", "--force", "s3://"+b)
}
