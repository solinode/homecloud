package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/dockertest"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// These tests run a throwaway MinIO container (skipped without Docker) and
// drive the AWS S3 endpoint with the real AWS CLI, boto3 and minio-go.

var (
	minioOnce sync.Once
	minioAddr string
	minioName string
	minioErr  string
)

const minioUser, minioPass = "awstestroot", "awstest-secret-key-123"

func TestMain(m *testing.M) {
	code := m.Run()
	if minioName != "" {
		_ = exec.Command("docker", "rm", "-f", "-v", minioName).Run()
	}
	os.Exit(code)
}

func startMinIO(t *testing.T) string {
	t.Helper()
	minioOnce.Do(func() {
		if exec.Command("docker", "info").Run() != nil {
			minioErr = "docker not available"
			return
		}
		dockertest.SweepStale(t.Logf)
		name := "s3awstest-" + strings.ToLower(core.RandHex(6))
		out, err := exec.Command("docker", "run", "-d", "--name", name, "--user", "0", "-p", "127.0.0.1::9000",
			"--label", core.LabelAccount+"="+dockertest.NewAccount(),
			"-e", "MINIO_ROOT_USER="+minioUser, "-e", "MINIO_ROOT_PASSWORD="+minioPass,
			minioImage, "server", "/data").CombinedOutput()
		if err != nil {
			minioErr = "start minio: " + string(out)
			return
		}
		minioName = name
		out, err = exec.Command("docker", "port", name, "9000/tcp").Output()
		if err != nil {
			minioErr = "docker port: " + err.Error()
			return
		}
		minioAddr = strings.TrimSpace(strings.Split(strings.TrimSpace(string(out)), "\n")[0])
	})
	if minioErr != "" {
		t.Skip(minioErr)
	}
	return minioAddr
}

// newHarness starts an AWS endpoint with S3 registered against the test MinIO.
func newHarness(t *testing.T) (*awstest.Harness, *Service) {
	t.Helper()
	addr := startMinIO(t)
	h := awstest.New(t)
	s := New(h.Env, h.Secrets)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.UseMinIO(ctx, addr, minioUser, minioPass); err != nil {
		t.Fatal(err)
	}
	s.Routes(h.Router)
	s.RegisterAWS()
	return h, s
}

func bucketName() string { return "t-" + strings.ToLower(core.RandHex(8)) }

func writeRandom(t *testing.T, path string, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, _ = rand.Read(b)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

func sameFile(t *testing.T, a, b string) {
	t.Helper()
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	if err1 != nil || err2 != nil || !bytes.Equal(x, y) {
		t.Fatalf("%s and %s differ (%v %v, %d vs %d bytes)", a, b, err1, err2, len(x), len(y))
	}
}

func mustFail(t *testing.T, out string, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(out, code) {
		t.Fatalf("expected %s, got err=%v:\n%s", code, err, out)
	}
}

func pythonBin(t *testing.T) string {
	p := os.Getenv("HC_TEST_PYTHON")
	if p == "" {
		p = "python3"
	}
	if exec.Command(p, "-c", "import boto3").Run() != nil {
		t.Skip("python with boto3 not available (set HC_TEST_PYTHON)")
	}
	return p
}

// runPy runs a boto3 script with the given environment.
func runPy(t *testing.T, env []string, script string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "s.py")
	_ = os.WriteFile(f, []byte("import boto3, botocore, json, os\nfrom botocore.config import Config\n"+script+"\n"), 0o600)
	cmd := exec.Command(pythonBin(t), f)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("python: %v\n%s\n%s", err, out.String(), errb.String())
	}
	return out.String()
}

func runAWS(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	aws, err := exec.LookPath("aws")
	if err != nil {
		t.Skip("aws CLI not installed")
	}
	cmd := exec.Command(aws, append([]string{"--output", "json"}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// payloads records the x-amz-content-sha256 modes seen per operation.
type payloads struct {
	mu   sync.Mutex
	seen map[string]bool
}

func tracePayloads(t *testing.T) *payloads {
	p := &payloads{seen: map[string]bool{}}
	traceBody = func(op, hash string) {
		if len(hash) == 64 {
			hash = "sha256"
		}
		p.mu.Lock()
		p.seen[op+" "+hash] = true
		p.mu.Unlock()
	}
	t.Cleanup(func() { traceBody = nil })
	return p
}

func (p *payloads) has(s string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[s]
}

func TestAWSCLIHighLevel(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	dir := t.TempDir()
	_ = os.MkdirAll(dir+"/src/sub", 0o700)
	_ = os.WriteFile(dir+"/src/a.txt", []byte("alpha\n"), 0o600)
	_ = os.WriteFile(dir+"/src/sub/b.txt", []byte("beta\n"), 0o600)
	writeRandom(t, dir+"/big.bin", 25<<20+123) // multipart (8 MB parts)

	h.AWS(t, "s3", "cp", dir+"/big.bin", "s3://"+b+"/big.bin")
	h.AWS(t, "s3", "cp", "s3://"+b+"/big.bin", dir+"/big.back")
	sameFile(t, dir+"/big.bin", dir+"/big.back")
	head := h.AWSJSON(t, "s3api", "head-object", "--bucket", b, "--key", "big.bin")
	if !strings.HasSuffix(head["ETag"].(string), `-4"`) || head["ContentLength"].(float64) != 25<<20+123 {
		t.Fatalf("multipart object: %v", head)
	}

	h.AWS(t, "s3", "sync", dir+"/src", "s3://"+b+"/site")
	out := h.AWS(t, "s3", "ls", "--recursive", "s3://"+b+"/site/")
	if !strings.Contains(out, "site/a.txt") || !strings.Contains(out, "site/sub/b.txt") {
		t.Fatalf("ls after sync: %s", out)
	}
	if out := h.AWS(t, "s3", "sync", dir+"/src", "s3://"+b+"/site"); strings.Contains(out, "upload:") {
		t.Fatalf("second sync re-uploaded: %s", out)
	}
	out = h.AWS(t, "s3", "ls", "s3://"+b+"/site/")
	if !strings.Contains(out, "PRE sub/") || !strings.Contains(out, "a.txt") {
		t.Fatalf("ls with delimiter: %s", out)
	}
	h.AWS(t, "s3", "sync", "s3://"+b+"/site", dir+"/down")
	sameFile(t, dir+"/src/sub/b.txt", dir+"/down/sub/b.txt")
	h.AWS(t, "s3", "cp", "s3://"+b+"/site/a.txt", "s3://"+b+"/copy/a.txt")
	h.AWS(t, "s3", "mv", "s3://"+b+"/copy/a.txt", "s3://"+b+"/moved/a.txt")
	h.AWS(t, "s3", "rm", "--recursive", "s3://"+b+"/site")
	if out := h.AWS(t, "s3", "ls", "--recursive", "s3://"+b); strings.Contains(out, "site/") || !strings.Contains(out, "moved/a.txt") {
		t.Fatalf("after rm/mv: %s", out)
	}
	if out, err := h.AWSErr(t, "s3", "rb", "s3://"+b); err == nil {
		t.Fatalf("rb of a non-empty bucket succeeded: %s", out)
	}
	h.AWS(t, "s3", "rb", "--force", "s3://"+b)
	if out := h.AWS(t, "s3", "ls"); strings.Contains(out, b) {
		t.Fatalf("bucket still listed: %s", out)
	}
}

func TestAWSS3API(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	dir := t.TempDir()
	s3api := func(args ...string) map[string]any { return h.AWSJSON(t, append([]string{"s3api"}, args...)...) }
	s3apiErr := func(code string, args ...string) {
		t.Helper()
		out, err := h.AWSErr(t, append([]string{"s3api"}, args...)...)
		mustFail(t, out, err, code)
	}

	loc := s3api("create-bucket", "--bucket", b)
	if loc["Location"] != "/"+b {
		t.Fatalf("create-bucket: %v", loc)
	}
	s3apiErr("BucketAlreadyOwnedByYou", "create-bucket", "--bucket", b)
	s3api("head-bucket", "--bucket", b)
	s3apiErr("404", "head-bucket", "--bucket", "no-such-"+b)
	if l := s3api("get-bucket-location", "--bucket", b); l["LocationConstraint"] != nil {
		t.Fatalf("location: %v", l)
	}
	s3apiErr("InvalidBucketName", "create-bucket", "--bucket", "Bad_Name")

	// The bucket is visible to the native API and console.
	var native []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/s3/buckets", nil), &native)
	found := false
	for _, nb := range native {
		found = found || nb["name"] == b
	}
	if !found {
		t.Fatalf("native API does not list %s: %v", b, native)
	}

	// Objects, ranges, conditionals, checksums.
	_ = os.WriteFile(dir+"/f.txt", []byte("0123456789"), 0o600)
	put := s3api("put-object", "--bucket", b, "--key", "dir/f.txt", "--body", dir+"/f.txt", "--content-type", "text/plain",
		"--metadata", "color=blue", "--checksum-algorithm", "SHA256")
	etag := put["ETag"].(string)
	if put["ChecksumSHA256"] == nil {
		t.Fatalf("put-object checksum not returned: %v", put)
	}
	got := s3api("get-object", "--bucket", b, "--key", "dir/f.txt", "--range", "bytes=2-4", "--checksum-mode", "ENABLED", dir+"/range.txt")
	if data, _ := os.ReadFile(dir + "/range.txt"); string(data) != "234" || got["ContentRange"] != "bytes 2-4/10" {
		t.Fatalf("range: %q %v", data, got)
	}
	hd := s3api("head-object", "--bucket", b, "--key", "dir/f.txt", "--checksum-mode", "ENABLED")
	if hd["ContentType"] != "text/plain" || hd["Metadata"].(map[string]any)["color"] != "blue" || hd["ETag"] != etag || hd["ChecksumSHA256"] == nil {
		t.Fatalf("head-object: %v", hd)
	}
	s3apiErr("304", "get-object", "--bucket", b, "--key", "dir/f.txt", "--if-none-match", etag, dir+"/x")
	s3apiErr("PreconditionFailed", "get-object", "--bucket", b, "--key", "dir/f.txt", "--if-match", `"nope"`, dir+"/x")
	s3apiErr("NoSuchKey", "get-object", "--bucket", b, "--key", "missing", dir+"/x")

	// Listing.
	for i := 0; i < 5; i++ {
		s3api("put-object", "--bucket", b, "--key", fmt.Sprintf("list/k%d", i), "--body", dir+"/f.txt")
	}
	v1 := s3api("list-objects", "--bucket", b, "--prefix", "list/", "--max-items", "10")
	if len(v1["Contents"].([]any)) != 5 {
		t.Fatalf("list-objects: %v", v1)
	}
	page := s3api("list-objects-v2", "--bucket", b, "--prefix", "list/", "--max-keys", "2", "--no-paginate")
	if page["IsTruncated"] != true || page["NextContinuationToken"] == nil || len(page["Contents"].([]any)) != 2 {
		t.Fatalf("list-objects-v2 page: %v", page)
	}
	page2 := s3api("list-objects-v2", "--bucket", b, "--prefix", "list/", "--max-keys", "10", "--no-paginate",
		"--continuation-token", page["NextContinuationToken"].(string))
	if len(page2["Contents"].([]any)) != 3 {
		t.Fatalf("continuation: %v", page2)
	}
	delim := s3api("list-objects-v2", "--bucket", b, "--delimiter", "/")
	if len(delim["CommonPrefixes"].([]any)) != 2 {
		t.Fatalf("delimiter: %v", delim)
	}

	// Copy, tagging, delete-objects.
	s3api("copy-object", "--bucket", b, "--key", "copy.txt", "--copy-source", b+"/dir/f.txt")
	s3api("put-object-tagging", "--bucket", b, "--key", "copy.txt", "--tagging", "TagSet=[{Key=env,Value=dev}]")
	if tg := s3api("get-object-tagging", "--bucket", b, "--key", "copy.txt"); !strings.Contains(fmt.Sprint(tg["TagSet"]), "env") {
		t.Fatalf("object tags: %v", tg)
	}
	s3api("delete-object-tagging", "--bucket", b, "--key", "copy.txt")
	del := s3api("delete-objects", "--bucket", b, "--delete", `{"Objects":[{"Key":"list/k0"},{"Key":"list/k1"}]}`)
	if len(del["Deleted"].([]any)) != 2 {
		t.Fatalf("delete-objects: %v", del)
	}
	s3api("delete-object", "--bucket", b, "--key", "list/k2")

	// Bucket tagging round-trips through HomeCloud's bucket metadata.
	s3apiErr("NoSuchTagSet", "get-bucket-tagging", "--bucket", b)
	s3api("put-bucket-tagging", "--bucket", b, "--tagging", "TagSet=[{Key=team,Value=core},{Key=cost,Value=42}]")
	if tg := s3api("get-bucket-tagging", "--bucket", b); len(tg["TagSet"].([]any)) != 2 {
		t.Fatalf("bucket tags: %v", tg)
	}
	var nb map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/s3/buckets/"+b, nil), &nb)
	if nb["tags"].(map[string]any)["team"] != "core" {
		t.Fatalf("native bucket tags: %v", nb)
	}
	s3api("delete-bucket-tagging", "--bucket", b)

	// Versioning.
	s3api("put-bucket-versioning", "--bucket", b, "--versioning-configuration", "Status=Enabled")
	if v := s3api("get-bucket-versioning", "--bucket", b); v["Status"] != "Enabled" {
		t.Fatalf("versioning: %v", v)
	}
	v1put := s3api("put-object", "--bucket", b, "--key", "ver.txt", "--body", dir+"/f.txt")
	s3api("put-object", "--bucket", b, "--key", "ver.txt", "--body", dir+"/range.txt")
	vers := s3api("list-object-versions", "--bucket", b, "--prefix", "ver.txt")
	if len(vers["Versions"].([]any)) != 2 {
		t.Fatalf("versions: %v", vers)
	}
	old := s3api("get-object", "--bucket", b, "--key", "ver.txt", "--version-id", v1put["VersionId"].(string), dir+"/old.txt")
	if data, _ := os.ReadFile(dir + "/old.txt"); string(data) != "0123456789" || old["VersionId"] != v1put["VersionId"] {
		t.Fatalf("old version: %q %v", data, old)
	}
	s3api("delete-object", "--bucket", b, "--key", "ver.txt", "--version-id", v1put["VersionId"].(string))

	// Multipart by hand, including UploadPartCopy, ListParts and ListMultipartUploads.
	writeRandom(t, dir+"/part1", 5<<20)
	mp := s3api("create-multipart-upload", "--bucket", b, "--key", "mp.bin")
	id := mp["UploadId"].(string)
	p1 := s3api("upload-part", "--bucket", b, "--key", "mp.bin", "--upload-id", id, "--part-number", "1", "--body", dir+"/part1")
	p2 := s3api("upload-part-copy", "--bucket", b, "--key", "mp.bin", "--upload-id", id, "--part-number", "2", "--copy-source", b+"/dir/f.txt")
	if lp := s3api("list-parts", "--bucket", b, "--key", "mp.bin", "--upload-id", id); len(lp["Parts"].([]any)) != 2 {
		t.Fatalf("list-parts: %v", lp)
	}
	if lu := s3api("list-multipart-uploads", "--bucket", b); len(lu["Uploads"].([]any)) != 1 {
		t.Fatalf("list-multipart-uploads: %v", lu)
	}
	parts := fmt.Sprintf(`{"Parts":[{"PartNumber":1,"ETag":%q},{"PartNumber":2,"ETag":%q}]}`, p1["ETag"], p2["CopyPartResult"].(map[string]any)["ETag"])
	s3api("complete-multipart-upload", "--bucket", b, "--key", "mp.bin", "--upload-id", id, "--multipart-upload", parts)
	if hd := s3api("head-object", "--bucket", b, "--key", "mp.bin"); hd["ContentLength"].(float64) != 5<<20+10 {
		t.Fatalf("completed multipart: %v", hd)
	}
	ab := s3api("create-multipart-upload", "--bucket", b, "--key", "aborted.bin")
	s3api("abort-multipart-upload", "--bucket", b, "--key", "aborted.bin", "--upload-id", ab["UploadId"].(string))

	// Bucket policy (stored in MinIO, evaluated by HomeCloud).
	s3apiErr("NoSuchBucketPolicy", "get-bucket-policy", "--bucket", b)
	pol := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*"}]}`, b)
	s3api("put-bucket-policy", "--bucket", b, "--policy", pol)
	if p := s3api("get-bucket-policy", "--bucket", b); !strings.Contains(p["Policy"].(string), "s3:GetObject") {
		t.Fatalf("policy: %v", p)
	}
	if ps := s3api("get-bucket-policy-status", "--bucket", b); ps["PolicyStatus"].(map[string]any)["IsPublic"] != true {
		t.Fatalf("policy status: %v", ps)
	}
	s3api("delete-bucket-policy", "--bucket", b)

	// Lifecycle (MinIO), CORS and encryption (stored by HomeCloud).
	s3apiErr("NoSuchLifecycleConfiguration", "get-bucket-lifecycle-configuration", "--bucket", b)
	s3api("put-bucket-lifecycle-configuration", "--bucket", b, "--lifecycle-configuration",
		`{"Rules":[{"ID":"expire","Status":"Enabled","Filter":{"Prefix":"tmp/"},"Expiration":{"Days":7}}]}`)
	if lc := s3api("get-bucket-lifecycle-configuration", "--bucket", b); !strings.Contains(fmt.Sprint(lc), "expire") {
		t.Fatalf("lifecycle: %v", lc)
	}
	// MinIO cannot run AbortIncompleteMultipartUpload; the rule is kept and
	// returned as put (Terraform's s3-bucket module commonly sets one).
	s3api("put-bucket-lifecycle-configuration", "--bucket", b, "--lifecycle-configuration",
		`{"Rules":[{"ID":"abort","Status":"Enabled","Filter":{"Prefix":""},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":3}},`+
			`{"ID":"both","Status":"Enabled","Filter":{"Prefix":"logs/"},"Expiration":{"Days":30},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":1}}]}`)
	lc := s3api("get-bucket-lifecycle-configuration", "--bucket", b)
	rules, _ := lc["Rules"].([]any)
	if len(rules) != 2 || rules[1].(map[string]any)["AbortIncompleteMultipartUpload"] == nil || lc["TransitionDefaultMinimumObjectSize"] != "all_storage_classes_128K" {
		t.Fatalf("lifecycle with abort rules: %v", lc)
	}
	s3apiErr("InvalidRequest", "put-bucket-lifecycle-configuration", "--bucket", b, "--lifecycle-configuration",
		`{"Rules":[{"ID":"none","Status":"Enabled","Filter":{"Prefix":""}}]}`)
	s3api("delete-bucket-lifecycle", "--bucket", b)
	s3apiErr("NoSuchLifecycleConfiguration", "get-bucket-lifecycle-configuration", "--bucket", b)
	s3apiErr("NoSuchCORSConfiguration", "get-bucket-cors", "--bucket", b)
	s3api("put-bucket-cors", "--bucket", b, "--cors-configuration", `{"CORSRules":[{"AllowedMethods":["GET"],"AllowedOrigins":["*"]}]}`)
	if c := s3api("get-bucket-cors", "--bucket", b); !strings.Contains(fmt.Sprint(c), "GET") {
		t.Fatalf("cors: %v", c)
	}
	s3api("delete-bucket-cors", "--bucket", b)
	s3apiErr("NoSuchCORSConfiguration", "get-bucket-cors", "--bucket", b)
	s3apiErr("ServerSideEncryptionConfigurationNotFoundError", "get-bucket-encryption", "--bucket", b)
	s3api("put-bucket-encryption", "--bucket", b, "--server-side-encryption-configuration",
		`{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}`)
	if e := s3api("get-bucket-encryption", "--bucket", b); !strings.Contains(fmt.Sprint(e), "AES256") {
		t.Fatalf("encryption: %v", e)
	}
	s3api("delete-bucket-encryption", "--bucket", b)

	// ACLs.
	acl := s3api("get-bucket-acl", "--bucket", b)
	if !strings.Contains(fmt.Sprint(acl["Grants"]), "FULL_CONTROL") {
		t.Fatalf("bucket acl: %v", acl)
	}
	s3api("put-bucket-acl", "--bucket", b, "--acl", "private")
	if oa := s3api("get-object-acl", "--bucket", b, "--key", "copy.txt"); !strings.Contains(fmt.Sprint(oa["Grants"]), "FULL_CONTROL") {
		t.Fatalf("object acl: %v", oa)
	}
	s3api("put-object-acl", "--bucket", b, "--key", "copy.txt", "--acl", "private")
	s3apiErr("NoSuchKey", "get-object-acl", "--bucket", b, "--key", "missing")

	// Website configuration drives HomeCloud's website hosting.
	s3apiErr("NoSuchWebsiteConfiguration", "get-bucket-website", "--bucket", b)
	s3api("put-bucket-website", "--bucket", b, "--website-configuration", `{"IndexDocument":{"Suffix":"index.html"},"ErrorDocument":{"Key":"404.html"}}`)
	if w := s3api("get-bucket-website", "--bucket", b); w["IndexDocument"].(map[string]any)["Suffix"] != "index.html" {
		t.Fatalf("website: %v", w)
	}
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/s3/buckets/"+b, nil), &nb)
	if nb["website"] != true || nb["error_document"] != "404.html" {
		t.Fatalf("native website: %v", nb)
	}
	s3api("delete-bucket-website", "--bucket", b)

	// Subresources Terraform reads on refresh.
	if l := s3api("get-bucket-logging", "--bucket", b); len(l) != 0 {
		t.Fatalf("logging: %v", l)
	}
	if rp := s3api("get-bucket-request-payment", "--bucket", b); rp["Payer"] != "BucketOwner" {
		t.Fatalf("request payment: %v", rp)
	}
	s3api("get-bucket-accelerate-configuration", "--bucket", b)
	s3api("get-bucket-notification-configuration", "--bucket", b)
	s3apiErr("ReplicationConfigurationNotFoundError", "get-bucket-replication", "--bucket", b)
	s3apiErr("ObjectLockConfigurationNotFoundError", "get-object-lock-configuration", "--bucket", b)
	s3apiErr("OwnershipControlsNotFoundError", "get-bucket-ownership-controls", "--bucket", b)
	s3apiErr("NoSuchPublicAccessBlockConfiguration", "get-public-access-block", "--bucket", b)
	s3api("put-public-access-block", "--bucket", b, "--public-access-block-configuration", "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true")
	if pab := s3api("get-public-access-block", "--bucket", b); pab["PublicAccessBlockConfiguration"].(map[string]any)["BlockPublicAcls"] != true {
		t.Fatalf("public access block: %v", pab)
	}

	// Delete the bucket through the AWS API: HomeCloud forgets its metadata.
	h.Python(t, `
b = boto3.resource("s3").Bucket("`+b+`")
b.object_versions.delete()
b.delete()
`)
	s3apiErr("NoSuchBucket", "get-bucket-tagging", "--bucket", b)
	if _, err := store.Get[bucketMeta](h.Env.Store, cBuckets, b); err == nil {
		t.Fatal("bucket metadata survived DeleteBucket")
	}

	// A bucket created through the native API appears in the AWS API.
	nb2 := bucketName()
	h.Native(t, "POST", "/api/v1/s3/buckets", map[string]any{"name": nb2, "tags": map[string]string{"made": "native"}})
	if out := h.AWS(t, "s3", "ls"); !strings.Contains(out, nb2) {
		t.Fatalf("aws s3 ls misses native bucket: %s", out)
	}
	if tg := s3api("get-bucket-tagging", "--bucket", nb2); !strings.Contains(fmt.Sprint(tg), "native") {
		t.Fatalf("native tags via AWS: %v", tg)
	}
	h.AWS(t, "s3", "rb", "s3://"+nb2)
}

func TestAWSIAMAndSTS(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	dir := t.TempDir()
	_ = os.WriteFile(dir+"/f", []byte("data"), 0o600)
	h.AWS(t, "s3", "cp", dir+"/f", "s3://"+b+"/f")

	ak, sk := h.User(t, "reader", "S3ReadOnlyAccess")
	if out, err := h.AWSAs(t, ak, sk, "", "s3", "ls", "s3://"+b); err != nil || !strings.Contains(out, " f") {
		t.Fatalf("reader ls: %v %s", err, out)
	}
	if _, err := h.AWSAs(t, ak, sk, "", "s3", "cp", "s3://"+b+"/f", dir+"/g"); err != nil {
		t.Fatalf("reader get: %v", err)
	}
	out, err := h.AWSAs(t, ak, sk, "", "s3api", "put-object", "--bucket", b, "--key", "nope", "--body", dir+"/f")
	mustFail(t, out, err, "AccessDenied")
	out, err = h.AWSAs(t, ak, sk, "", "s3", "rm", "s3://"+b+"/f")
	mustFail(t, out, err, "AccessDenied")
	out, err = h.AWSAs(t, ak, sk, "", "s3api", "delete-objects", "--bucket", b, "--delete", `{"Objects":[{"Key":"f"}]}`)
	mustFail(t, out, err, "AccessDenied")
	out, err = h.AWSAs(t, ak, sk, "", "s3", "mb", "s3://"+b+"-2")
	mustFail(t, out, err, "AccessDenied")
	// Copying needs read on the source as well as write on the destination.
	ck, cs := h.User(t, "copier")
	h.Native(t, "PUT", "/api/v1/iam/users/copier/inline-policies/w", map[string]any{
		"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::" + b + "/*"}}})
	out, err = h.AWSAs(t, ck, cs, "", "s3api", "copy-object", "--bucket", b, "--key", "c", "--copy-source", b+"/f")
	mustFail(t, out, err, "AccessDenied")
	if out, err := h.AWSAs(t, ck, cs, "", "s3api", "put-object", "--bucket", b, "--key", "w", "--body", dir+"/f"); err != nil {
		t.Fatalf("copier put: %v %s", err, out)
	}

	// Temporary credentials from STS.
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "s3role", "policies": []string{"S3FullAccess"},
		"assume_role_policy": map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{
			"Effect": "Allow", "Principal": map[string]any{"AWS": "arn:aws:iam::" + h.Env.AccountID + ":root"}, "Action": "sts:AssumeRole"}}}})
	cr := h.AWSJSON(t, "sts", "assume-role", "--role-arn", "arn:aws:iam::"+h.Env.AccountID+":role/s3role", "--role-session-name", "tsess")
	c := cr["Credentials"].(map[string]any)
	tk, ts, tt := c["AccessKeyId"].(string), c["SecretAccessKey"].(string), c["SessionToken"].(string)
	if out, err := h.AWSAs(t, tk, ts, tt, "s3", "cp", dir+"/f", "s3://"+b+"/from-role"); err != nil {
		t.Fatalf("role put: %v %s", err, out)
	}
	out, err = h.AWSAs(t, tk, "wrong", tt, "s3", "ls", "s3://"+b)
	mustFail(t, out, err, "SignatureDoesNotMatch")
	out, err = h.AWSAs(t, "AKIAUNKNOWN000000000", "x", "", "s3", "ls")
	mustFail(t, out, err, "InvalidAccessKeyId")

	// A bucket policy can grant a user what their identity policies do not,
	// and deny what they do.
	nk, ns := h.User(t, "nobody")
	out, err = h.AWSAs(t, nk, ns, "", "s3", "cp", "s3://"+b+"/f", dir+"/n")
	mustFail(t, out, err, "403")
	pol := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:user/nobody"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*"},
{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::%s:user/reader"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/f"}]}`, h.Env.AccountID, b, h.Env.AccountID, b)
	h.AWS(t, "s3api", "put-bucket-policy", "--bucket", b, "--policy", pol)
	if out, err := h.AWSAs(t, nk, ns, "", "s3", "cp", "s3://"+b+"/f", dir+"/n"); err != nil {
		t.Fatalf("policy grant: %v %s", err, out)
	}
	out, err = h.AWSAs(t, ak, sk, "", "s3", "cp", "s3://"+b+"/f", dir+"/r")
	mustFail(t, out, err, "403")
	h.AWS(t, "s3", "cp", "s3://"+b+"/f", dir+"/root") // root is never locked out
	h.AWS(t, "s3", "rb", "--force", "s3://"+b)
}

func TestAWSPresignedAndAnonymous(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	h.AWS(t, "s3", "mb", "s3://"+b)
	dir := t.TempDir()
	_ = os.WriteFile(dir+"/f", []byte("presigned body"), 0o600)
	h.AWS(t, "s3", "cp", dir+"/f", "s3://"+b+"/f")

	curl := func(args ...string) (int, string) {
		t.Helper()
		out, err := exec.Command("curl", append([]string{"-s", "-w", "\n%{http_code}"}, args...)...).Output()
		if err != nil {
			t.Fatalf("curl: %v", err)
		}
		s := string(out)
		i := strings.LastIndex(s, "\n")
		code := 0
		fmt.Sscan(s[i+1:], &code)
		return code, s[:i]
	}
	// AWS CLI presign (GET).
	u := strings.TrimSpace(h.AWS(t, "s3", "presign", "s3://"+b+"/f", "--expires-in", "300"))
	if code, body := curl(u); code != 200 || body != "presigned body" {
		t.Fatalf("presigned GET: %d %s", code, body)
	}
	if code, _ := curl(strings.Replace(u, "X-Amz-Signature=", "X-Amz-Signature=0", 1)); code != 403 {
		t.Fatalf("tampered presigned URL: %d", code)
	}
	// boto3 presigned GET and PUT.
	out := h.Python(t, `
s3 = boto3.client("s3")
print(s3.generate_presigned_url("get_object", Params={"Bucket": "`+b+`", "Key": "f"}, ExpiresIn=300))
print(s3.generate_presigned_url("put_object", Params={"Bucket": "`+b+`", "Key": "up.txt"}, ExpiresIn=300))
`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code, body := curl(lines[0]); code != 200 || body != "presigned body" {
		t.Fatalf("boto3 presigned GET: %d %s", code, body)
	}
	if code, body := curl("-X", "PUT", "-H", "Content-Type:", "--data-binary", "uploaded via presigned PUT", lines[1]); code != 200 {
		t.Fatalf("presigned PUT: %d %s", code, body)
	}
	h.AWS(t, "s3", "cp", "s3://"+b+"/up.txt", dir+"/up")
	if data, _ := os.ReadFile(dir + "/up"); string(data) != "uploaded via presigned PUT" {
		t.Fatalf("presigned PUT content: %q", data)
	}

	// Anonymous requests: denied until a bucket policy allows them.
	if code, body := curl(h.URL + "/" + b + "/f"); code != 403 || !strings.Contains(body, "AccessDenied") {
		t.Fatalf("anonymous GET of a private bucket: %d %s", code, body)
	}
	h.Native(t, "PUT", "/api/v1/s3/buckets/"+b+"/access", map[string]any{"public": true})
	if code, body := curl(h.URL + "/" + b + "/f"); code != 200 || body != "presigned body" {
		t.Fatalf("anonymous GET of a public bucket: %d %s", code, body)
	}
	if code, _ := curl("-X", "PUT", "--data-binary", "x", h.URL+"/"+b+"/anon"); code != 403 {
		t.Fatalf("anonymous PUT: %d", code)
	}
	if code, _ := curl(h.URL + "/" + b + "/?list-type=2"); code != 403 {
		t.Fatalf("anonymous list: %d", code)
	}
	// Virtual-hosted style, anonymous and signed.
	port := h.URL[strings.LastIndex(h.URL, ":")+1:]
	if code, body := curl("-H", "Host: "+b+".localhost:"+port, h.URL+"/f"); code != 200 || body != "presigned body" {
		t.Fatalf("virtual-hosted anonymous GET: %d %s", code, body)
	}
	out = runPy(t, append(h.Environ(h.AccessKeyID, h.SecretKey, ""), "AWS_ENDPOINT_URL=http://localhost:"+port), `
import socket
s3 = boto3.client("s3", config=Config(s3={"addressing_style": "virtual"}))
try:
    socket.getaddrinfo("`+b+`.localhost", `+port+`)
except OSError:
    print("NORESOLVE")
    raise SystemExit
s3.put_object(Bucket="`+b+`", Key="vh.txt", Body=b"virtual")
print(s3.get_object(Bucket="`+b+`", Key="vh.txt")["Body"].read().decode())
print(len(s3.list_objects_v2(Bucket="`+b+`")["Contents"]))
`)
	if !strings.Contains(out, "NORESOLVE") && !strings.Contains(out, "virtual\n") {
		t.Fatalf("virtual-hosted boto3: %s", out)
	}
	t.Logf("virtual-hosted boto3: %s", strings.TrimSpace(out))
	// Expired presigned URL.
	out = h.Python(t, `
import datetime
from botocore.signers import RequestSigner
s3 = boto3.client("s3")
print(s3.generate_presigned_url("get_object", Params={"Bucket": "`+b+`", "Key": "f"}, ExpiresIn=1))
`)
	time.Sleep(2 * time.Second)
	if code, _ := curl(strings.TrimSpace(out)); code != 403 {
		t.Fatalf("expired presigned URL: %d", code)
	}
	h.AWS(t, "s3", "rb", "--force", "s3://"+b)
}

func TestAWSBoto3(t *testing.T) {
	h, _ := newHarness(t)
	b := bucketName()
	dir := t.TempDir()
	writeRandom(t, dir+"/big.bin", 21<<20)
	out := h.Python(t, `
s3 = boto3.client("s3")
b = "`+b+`"
s3.create_bucket(Bucket=b)
s3.upload_file("`+dir+`/big.bin", b, "big.bin")
s3.download_file(b, "big.bin", "`+dir+`/big.back")
for i in range(25):
    s3.put_object(Bucket=b, Key="p/%02d" % i, Body=b"x" * i)
pages = s3.get_paginator("list_objects_v2").paginate(Bucket=b, Prefix="p/", PaginationConfig={"PageSize": 10})
keys = [o["Key"] for page in pages for o in page.get("Contents", [])]
print("keys", len(keys), keys[0], keys[-1])
r = s3.put_object(Bucket=b, Key="crc", Body=b"checksum me", ChecksumAlgorithm="CRC32")
g = s3.get_object(Bucket=b, Key="crc", ChecksumMode="ENABLED")
print("crc", r.get("ChecksumCRC32") == g.get("ChecksumCRC32") != None, g["Body"].read())
s3.copy({"Bucket": b, "Key": "big.bin"}, b, "big.copy")
print("copy", s3.head_object(Bucket=b, Key="big.copy")["ContentLength"])
res = boto3.resource("s3")
res.Bucket(b).objects.all().delete()
res.Bucket(b).delete()
try:
    s3.head_bucket(Bucket=b)
except botocore.exceptions.ClientError as e:
    print("gone", e.response["Error"]["Code"])
`)
	sameFile(t, dir+"/big.bin", dir+"/big.back")
	for _, want := range []string{"keys 25 p/00 p/24", "crc True b'checksum me'", fmt.Sprintf("copy %d", 21<<20), "gone 404"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// tlsFront serves the endpoint over HTTPS (SDKs send aws-chunked uploads with
// checksum trailers only over TLS) and returns its URL and CA bundle.
func tlsFront(t *testing.T, target string) (string, string) {
	u, _ := url.Parse(target)
	rp := httputil.NewSingleHostReverseProxy(u)
	orig := rp.Director
	rp.Director = func(r *http.Request) {
		host := r.Host
		orig(r)
		r.Host = host // the client signed the TLS front's host
	}
	ts := httptest.NewTLSServer(rp)
	t.Cleanup(ts.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600)
	return ts.URL, ca
}

func TestAWSStreamingTrailers(t *testing.T) {
	h, _ := newHarness(t)
	tr := tracePayloads(t)
	front, ca := tlsFront(t, h.URL)
	env := append(h.Environ(h.AccessKeyID, h.SecretKey, ""), "AWS_ENDPOINT_URL="+front, "AWS_CA_BUNDLE="+ca)
	b := bucketName()
	dir := t.TempDir()
	writeRandom(t, dir+"/big.bin", 17<<20+5)
	_ = os.WriteFile(dir+"/small.txt", []byte("small object over TLS"), 0o600)
	for _, args := range [][]string{
		{"s3", "mb", "s3://" + b},
		{"s3", "cp", dir + "/small.txt", "s3://" + b + "/small.txt"},
		{"s3", "cp", dir + "/big.bin", "s3://" + b + "/big.bin"},
		{"s3", "cp", "s3://" + b + "/big.bin", dir + "/big.back"},
		{"s3api", "put-object", "--bucket", b, "--key", "sha", "--body", dir + "/small.txt", "--checksum-algorithm", "SHA256"},
		{"s3api", "delete-objects", "--bucket", b, "--delete", `{"Objects":[{"Key":"sha"}]}`},
	} {
		if out, err := runAWS(t, env, args...); err != nil {
			t.Fatalf("aws %v: %v\n%s", args, err, out)
		}
	}
	sameFile(t, dir+"/big.bin", dir+"/big.back")
	out := runPy(t, env, `
s3 = boto3.client("s3")
b = "`+b+`"
s3.put_object(Bucket=b, Key="py", Body=b"python trailer", ChecksumAlgorithm="CRC32")
g = s3.get_object(Bucket=b, Key="py", ChecksumMode="ENABLED")
print(g["Body"].read(), g.get("ChecksumCRC32"))
s3.upload_file("`+dir+`/big.bin", b, "big2", ExtraArgs={"ChecksumAlgorithm": "SHA1"})
s3.download_file(b, "big2", "`+dir+`/big2.back")
s3.put_object(Bucket=b, Key="empty", Body=b"")
print(s3.head_object(Bucket=b, Key="empty")["ContentLength"])
`)
	if !strings.Contains(out, "b'python trailer'") || strings.Contains(out, "None") || !strings.HasSuffix(strings.TrimSpace(out), "0") {
		t.Fatalf("boto3 over TLS:\n%s", out)
	}
	sameFile(t, dir+"/big.bin", dir+"/big2.back")
	if !tr.has("PutObject "+streamingUnsignedTrl) || !tr.has("UploadPart "+streamingUnsignedTrl) {
		t.Fatalf("expected unsigned-trailer uploads, saw %v", tr.seen)
	}
	if out, err := runAWS(t, env, "s3", "rb", "--force", "s3://"+b); err != nil {
		t.Fatalf("rb: %v %s", err, out)
	}
}

// TestSignedStreaming uses minio-go, which signs each chunk
// (STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]) over plain HTTP.
func TestSignedStreaming(t *testing.T) {
	h, _ := newHarness(t)
	tr := tracePayloads(t)
	u, _ := url.Parse(h.URL)
	ctx := context.Background()
	mc, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(h.AccessKeyID, h.SecretKey, ""), Region: "us-east-1", TrailingHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	b := bucketName()
	if err := mc.MakeBucket(ctx, b, minio.MakeBucketOptions{}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 300<<10+7)
	_, _ = rand.Read(data)
	if _, err := mc.PutObject(ctx, b, "signed", bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{DisableContentSha256: false}); err != nil {
		t.Fatalf("signed streaming put: %v", err)
	}
	if _, err := mc.PutObject(ctx, b, "trailer", bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{Checksum: minio.ChecksumCRC32C}); err != nil {
		t.Fatalf("signed trailer put: %v", err)
	}
	for _, k := range []string{"signed", "trailer"} {
		o, err := mc.GetObject(ctx, b, k, minio.GetObjectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(o)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: read back %d bytes, %v", k, len(got), err)
		}
	}
	st, err := mc.StatObject(ctx, b, "trailer", minio.StatObjectOptions{Checksum: true})
	if err != nil || st.ChecksumCRC32C == "" {
		t.Fatalf("trailer checksum not stored: %v %+v", err, st)
	}
	if !tr.has("PutObject "+streamingSigned) || !tr.has("PutObject "+streamingSignedTrailer) {
		t.Fatalf("expected signed streaming uploads, saw %v", tr.seen)
	}

	// A tampered chunk is rejected and nothing is stored.
	body := buildSignedChunked(t, h, u.Host, "/"+b+"/tampered", []byte("hello chunked world"), true)
	req, _ := http.NewRequest("PUT", h.URL+"/"+b+"/tampered", bytes.NewReader(body.payload))
	req.Header = body.header
	req.ContentLength = int64(len(body.payload))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || !strings.Contains(string(msg), "SignatureDoesNotMatch") {
		t.Fatalf("tampered chunk: %d %s", resp.StatusCode, msg)
	}
	if _, err := mc.StatObject(ctx, b, "tampered", minio.StatObjectOptions{}); err == nil {
		t.Fatal("tampered upload was stored")
	}
	// The same request untampered succeeds.
	body = buildSignedChunked(t, h, u.Host, "/"+b+"/ok", []byte("hello chunked world"), false)
	req, _ = http.NewRequest("PUT", h.URL+"/"+b+"/ok", bytes.NewReader(body.payload))
	req.Header = body.header
	req.ContentLength = int64(len(body.payload))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("hand-built chunked upload: %d %s", resp.StatusCode, msg)
	}
	_ = mc.RemoveObject(ctx, b, "signed", minio.RemoveObjectOptions{})
	_ = mc.RemoveObject(ctx, b, "trailer", minio.RemoveObjectOptions{})
	_ = mc.RemoveObject(ctx, b, "ok", minio.RemoveObjectOptions{})
	if err := mc.RemoveBucket(ctx, b); err != nil {
		t.Fatal(err)
	}
}

type chunkedReq struct {
	header  http.Header
	payload []byte
}

// buildSignedChunked signs a two-chunk STREAMING-AWS4-HMAC-SHA256-PAYLOAD
// upload by hand; tamper flips a byte after signing.
func buildSignedChunked(t *testing.T, h *awstest.Harness, host, path string, data []byte, tamper bool) chunkedReq {
	t.Helper()
	now := time.Now().UTC()
	date := now.Format("20060102T150405Z")
	scope := now.Format("20060102") + "/us-east-1/s3/aws4_request"
	hdr := http.Header{}
	hdr.Set("X-Amz-Date", date)
	hdr.Set("X-Amz-Content-Sha256", streamingSigned)
	hdr.Set("X-Amz-Decoded-Content-Length", fmt.Sprint(len(data)))
	hdr.Set("Content-Encoding", "aws-chunked")
	signed := []string{"content-encoding", "host", "x-amz-content-sha256", "x-amz-date", "x-amz-decoded-content-length"}
	creq := strings.Join([]string{"PUT", path, "",
		"content-encoding:aws-chunked", "host:" + host, "x-amz-content-sha256:" + streamingSigned, "x-amz-date:" + date,
		"x-amz-decoded-content-length:" + fmt.Sprint(len(data)), "", strings.Join(signed, ";"), streamingSigned}, "\n")
	key := hmacKey(h.SecretKey, now.Format("20060102"))
	seed := hexHMAC(key, "AWS4-HMAC-SHA256\n"+date+"\n"+scope+"\n"+sha(creq))
	hdr.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+h.AccessKeyID+"/"+scope+", SignedHeaders="+strings.Join(signed, ";")+", Signature="+seed)
	var buf bytes.Buffer
	prev := seed
	half := len(data) / 2
	for _, chunk := range [][]byte{data[:half], data[half:], nil} {
		sig := hexHMAC(key, strings.Join([]string{"AWS4-HMAC-SHA256-PAYLOAD", date, scope, prev, emptySHA256, sha(string(chunk))}, "\n"))
		prev = sig
		fmt.Fprintf(&buf, "%x;chunk-signature=%s\r\n", len(chunk), sig)
		buf.Write(chunk)
		buf.WriteString("\r\n")
	}
	p := buf.Bytes()
	if tamper {
		i := bytes.Index(p, data[half:])
		p[i] ^= 1
	}
	return chunkedReq{hdr, p}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:])
}

func hmacRaw(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hexHMAC(key []byte, data string) string { return fmt.Sprintf("%x", hmacRaw(key, data)) }

func hmacKey(secret, day string) []byte {
	k := hmacRaw([]byte("AWS4"+secret), day)
	k = hmacRaw(k, "us-east-1")
	k = hmacRaw(k, "s3")
	return hmacRaw(k, "aws4_request")
}
