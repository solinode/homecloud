package s3

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func getStatus(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The website endpoint reads with the server's own storage credentials, so it
// may only serve keys the bucket policy lets an anonymous caller read: a policy
// that grants one prefix, or carries a Deny, does not publish the whole bucket.
func TestWebsiteServesOnlyWhatThePolicyAllows(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	dir := t.TempDir()
	_ = os.WriteFile(dir+"/f", []byte("content"), 0o600)
	h.AWS(t, "s3", "cp", dir+"/f", "s3://"+b+"/pub/index.html")
	h.AWS(t, "s3", "cp", dir+"/f", "s3://"+b+"/pub/secret.txt")
	h.AWS(t, "s3", "cp", dir+"/f", "s3://"+b+"/private/data.txt")
	h.AWS(t, "s3api", "put-bucket-website", "--bucket", b, "--website-configuration", `{"IndexDocument":{"Suffix":"index.html"}}`)
	h.AWS(t, "s3api", "put-bucket-policy", "--bucket", b, "--policy", `{"Version":"2012-10-17","Statement":[
	 {"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::`+b+`/pub/*"},
	 {"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::`+b+`/pub/secret.txt"}]}`)

	if st, body := getStatus(t, h.URL+"/website/"+b+"/pub/index.html"); st != 200 || body != "content" {
		t.Fatalf("allowed page: %d %q", st, body)
	}
	for _, p := range []string{"/private/data.txt", "/pub/secret.txt", "/index.html"} {
		if st, body := getStatus(t, h.URL+"/website/"+b+p); st != 403 || strings.Contains(body, "content") {
			t.Fatalf("%s: %d %q", p, st, body)
		}
	}
}

// A SigV2 presigned URL signs only the sub-resources SigV2 knows; appending
// another one (?retention, ?publicAccessBlock, ...) must not reuse the signature.
func TestSigV2PresignedURLCannotGainSubresources(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	out := h.Python(t, `
s3 = boto3.client("s3", config=botocore.config.Config(signature_version="s3"))
print(s3.generate_presigned_url("get_object", Params={"Bucket": "`+b+`", "Key": "f"}, ExpiresIn=300))
`)
	u := strings.TrimSpace(out)
	if !strings.Contains(u, "AWSAccessKeyId=") {
		t.Skipf("not a SigV2 URL: %s", u)
	}
	for _, sub := range []string{"retention", "publicAccessBlock", "legal-hold"} {
		st, body := getStatus(t, u+"&"+sub)
		if st != 403 || !strings.Contains(body, "signature version 2") {
			t.Fatalf("?%s appended to a SigV2 URL: %d %s", sub, st, body)
		}
	}
}

// Locking an object through upload headers needs the retention / legal hold
// permissions, not just s3:PutObject.
func TestObjectLockHeadersNeedTheirOwnPermissions(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	dir := t.TempDir()
	_ = os.WriteFile(dir+"/f", []byte("x"), 0o600)
	ak, sk := h.User(t, "uploader")
	h.Native(t, "PUT", "/api/v1/iam/users/uploader/inline-policies/one", map[string]any{
		"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:PutObject",
			"Resource": []string{"arn:aws:s3:::" + b + "/*"}}}})
	out, err := h.AWSAs(t, ak, sk, "", "s3api", "put-object", "--bucket", b, "--key", "k", "--body", dir+"/f", "--object-lock-legal-hold-status", "ON")
	mustFail(t, out, err, "s3:PutObjectLegalHold")
	out, err = h.AWSAs(t, ak, sk, "", "s3api", "put-object", "--bucket", b, "--key", "k", "--body", dir+"/f",
		"--object-lock-mode", "COMPLIANCE", "--object-lock-retain-until-date", "2999-01-01T00:00:00Z")
	mustFail(t, out, err, "s3:PutObjectRetention")
}

// Keys in a DeleteObjects body are forwarded as they are, so "allowed/../x" must
// not be authorized as a key under allowed/.
func TestDeleteObjectsRejectsDotSegments(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	dir := t.TempDir()
	_ = os.WriteFile(dir+"/f", []byte("x"), 0o600)
	h.AWS(t, "s3", "cp", dir+"/f", "s3://"+b+"/keep")
	ak, sk := h.User(t, "deleter")
	h.Native(t, "PUT", "/api/v1/iam/users/deleter/inline-policies/one", map[string]any{
		"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:*",
			"Resource": []string{"arn:aws:s3:::" + b, "arn:aws:s3:::" + b + "/allowed/*"}}}})
	out, err := h.AWSAs(t, ak, sk, "", "s3api", "delete-objects", "--bucket", b, "--delete", `{"Objects":[{"Key":"allowed/../keep"}]}`)
	mustFail(t, out, err, "InvalidArgument")
	if out := h.AWS(t, "s3", "ls", "s3://"+b); !strings.Contains(out, "keep") {
		t.Fatalf("object deleted through a dot segment: %s", out)
	}
}
