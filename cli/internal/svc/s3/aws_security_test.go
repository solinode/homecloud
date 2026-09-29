package s3

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestAWSSecurity covers requests that must not reach MinIO with its root
// credentials unless HomeCloud authorized exactly what MinIO will do.
func TestAWSSecurity(t *testing.T) {
	h, _ := newHarness(t)
	b, other := bucketName(), bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	h.AWS(t, "s3", "mb", "s3://"+other)
	dir := t.TempDir()
	_ = os.WriteFile(dir+"/f", []byte("secret"), 0o600)
	h.AWS(t, "s3", "cp", dir+"/f", "s3://"+other+"/secret")
	h.AWS(t, "s3", "cp", dir+"/f", "s3://"+b+"/f")

	// A user limited to one bucket.
	ak, sk := h.User(t, "scoped")
	h.Native(t, "PUT", "/api/v1/iam/users/scoped/inline-policies/one", map[string]any{
		"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:*",
			"Resource": []string{"arn:aws:s3:::" + b, "arn:aws:s3:::" + b + "/*"}}}})
	if _, err := h.AWSAs(t, ak, sk, "", "s3", "cp", "s3://"+b+"/f", dir+"/ok"); err != nil {
		t.Fatalf("scoped user reading its bucket: %v", err)
	}
	// Path traversal out of the bucket.
	out, err := h.AWSAs(t, ak, sk, "", "s3api", "get-object", "--bucket", b, "--key", "../"+other+"/secret", dir+"/x")
	mustFail(t, out, err, "InvalidArgument")
	// Copy from a bucket the user cannot read, via header or lifted query parameter.
	out, err = h.AWSAs(t, ak, sk, "", "s3api", "copy-object", "--bucket", b, "--key", "stolen", "--copy-source", other+"/secret")
	mustFail(t, out, err, "AccessDenied")
	// MinIO's admin API lives under /minio/.
	out, err = h.AWSAs(t, ak, sk, "", "s3api", "list-objects-v2", "--bucket", "minio")
	mustFail(t, out, err, "InvalidBucketName")
	h.Python(t, `
s3 = boto3.client("s3", aws_access_key_id="`+ak+`", aws_secret_access_key="`+sk+`")
def lift(request, **kw):
    request.url += ("&" if "?" in request.url else "?") + "x-amz-copy-source=`+other+`%2Fsecret"
s3.meta.events.register("before-sign.s3.PutObject", lift)
try:
    s3.put_object(Bucket="`+b+`", Key="stolen2", Body=b"")
    raise SystemExit("copy via query parameter was allowed")
except botocore.exceptions.ClientError as e:
    assert e.response["Error"]["Code"] == "AccessDenied", e.response
# MinIO extension headers are dropped: force-delete of a non-empty bucket fails.
def force(request, **kw):
    request.headers["x-minio-force-delete"] = "true"
s3.meta.events.register("before-sign.s3.DeleteBucket", force)
try:
    s3.delete_bucket(Bucket="`+b+`")
    raise SystemExit("x-minio-force-delete honoured")
except botocore.exceptions.ClientError as e:
    assert e.response["Error"]["Code"] == "BucketNotEmpty", e.response
# A denied upload leaves the connection usable (Expect: 100-continue handling).
r = boto3.client("s3", aws_access_key_id="`+ak+`", aws_secret_access_key="`+sk+`")
for i in range(3):
    try:
        r.put_object(Bucket="`+other+`", Key="x", Body=b"y" * 2048)
        raise SystemExit("put to other bucket allowed")
    except botocore.exceptions.ClientError as e:
        assert e.response["Error"]["Code"] == "AccessDenied"
    assert r.get_object(Bucket="`+b+`", Key="f")["Body"].read() == b"secret"
`)
	if out := h.AWS(t, "s3", "ls", "s3://"+b); strings.Contains(out, "stolen") {
		t.Fatalf("copy succeeded: %s", out)
	}
	// Unsigned requests for unknown buckets are left to the console / native API.
	resp, err := http.Get(h.URL + "/no-such-bucket-xyz/key")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Amz-Request-Id") != "" {
		t.Fatalf("unsigned request for an unknown bucket reached the S3 endpoint")
	}
	// Anonymous requests never reach MinIO for a private bucket, even for listing.
	for _, u := range []string{"/" + b, "/" + b + "/f", "/" + b + "?policy", "/" + b + "/f?tagging"} {
		resp, err := http.Get(h.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("anonymous GET %s: %d", u, resp.StatusCode)
		}
	}
	h.AWS(t, "s3", "rb", "--force", "s3://"+b)
	h.AWS(t, "s3", "rb", "--force", "s3://"+other)
}
