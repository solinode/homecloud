package s3

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

func TestResolveActions(t *testing.T) {
	cases := []struct {
		method, bucket, key, query string
		header                     string
		op, action                 string
	}{
		{"GET", "", "", "", "", "ListBuckets", "s3:ListAllMyBuckets"},
		{"PUT", "b", "", "", "", "CreateBucket", "s3:CreateBucket"},
		{"HEAD", "b", "", "", "", "HeadBucket", "s3:ListBucket"},
		{"GET", "b", "", "list-type=2&prefix=a", "", "ListObjectsV2", "s3:ListBucket"},
		{"GET", "b", "", "versions", "", "ListObjectVersions", "s3:ListBucketVersions"},
		{"GET", "b", "", "uploads", "", "ListMultipartUploads", "s3:ListBucketMultipartUploads"},
		{"DELETE", "b", "", "tagging", "", "DeleteBucketTagging", "s3:PutBucketTagging"},
		{"GET", "b", "", "policy", "", "GetBucketPolicy", "s3:GetBucketPolicy"},
		{"DELETE", "b", "", "lifecycle", "", "DeleteBucketLifecycle", "s3:PutLifecycleConfiguration"},
		{"POST", "b", "", "delete", "", "DeleteObjects", "s3:DeleteObject"},
		{"GET", "b", "k", "", "", "GetObject", "s3:GetObject"},
		{"GET", "b", "k", "versionId=1", "", "GetObject", "s3:GetObjectVersion"},
		{"HEAD", "b", "k", "", "", "HeadObject", "s3:GetObject"},
		{"PUT", "b", "k", "", "", "PutObject", "s3:PutObject"},
		{"PUT", "b", "k", "", "X-Amz-Copy-Source: b/x", "CopyObject", "s3:PutObject"},
		{"PUT", "b", "k", "partNumber=1&uploadId=u", "", "UploadPart", "s3:PutObject"},
		{"PUT", "b", "k", "partNumber=1&uploadId=u", "X-Amz-Copy-Source: b/x", "UploadPartCopy", "s3:PutObject"},
		{"POST", "b", "k", "uploads", "", "CreateMultipartUpload", "s3:PutObject"},
		{"POST", "b", "k", "uploadId=u", "", "CompleteMultipartUpload", "s3:PutObject"},
		{"DELETE", "b", "k", "uploadId=u", "", "AbortMultipartUpload", "s3:AbortMultipartUpload"},
		{"GET", "b", "k", "uploadId=u", "", "ListParts", "s3:ListMultipartUploadParts"},
		{"DELETE", "b", "k", "versionId=3", "", "DeleteObject", "s3:DeleteObjectVersion"},
		{"PUT", "b", "k", "tagging&versionId=3", "", "PutObjectTagging", "s3:PutObjectVersionTagging"},
		{"GET", "b", "k", "acl", "", "GetObjectAcl", "s3:GetObjectAcl"},
		{"GET", "b", "k", "retention", "", "GetObjectRetention", "s3:GetObjectRetention"},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		h := http.Header{}
		if k, v, ok := strings.Cut(c.header, ": "); ok {
			h.Set(k, v)
		}
		op, err := resolve(c.method, c.bucket, c.key, q, h)
		if err != nil || op.name != c.op || op.action != c.action {
			t.Errorf("%s /%s/%s?%s: got %+v %v, want %s %s", c.method, c.bucket, c.key, c.query, op, err, c.op, c.action)
		}
	}
	if _, err := resolve("GET", "b", "", url.Values{"torrent": nil}, http.Header{}); err == nil {
		t.Error("unknown subresource accepted")
	}
}

func TestVirtualHostParsing(t *testing.T) {
	s := New(svctest.Env(t), nil)
	for _, c := range []struct{ host, bucket, rest string }{
		{"photos.localhost:8080", "photos", "localhost:8080"},
		{"photos.s3.localhost:8080", "photos", "localhost:8080"},
		{"localhost:8080", "", "localhost:8080"},
		{"127.0.0.1:8080", "", "127.0.0.1:8080"},
		{"s3.localhost", "", "s3.localhost"},
		{"my.dotted.bucket.localhost", "my.dotted.bucket", "localhost"},
		{"example.com", "", "example.com"},
	} {
		b, rest := s.vhostBucket(c.host)
		if b != c.bucket || rest != c.rest {
			t.Errorf("%s: %q %q, want %q %q", c.host, b, rest, c.bucket, c.rest)
		}
	}
}

// TestChunkedUnsignedTrailer decodes a botocore-style unsigned aws-chunked body
// and re-encodes it with the exact Content-Length MinIO is told.
func TestChunkedUnsignedTrailer(t *testing.T) {
	for _, n := range []int{0, 1, reencodeChunk - 1, reencodeChunk, 3*reencodeChunk + 17} {
		data := bytes.Repeat([]byte("z"), n)
		var body bytes.Buffer
		if n > 0 {
			half := n / 2
			for _, c := range [][]byte{data[:half], data[half:]} {
				if len(c) == 0 {
					continue
				}
				fmt.Fprintf(&body, "%x\r\n", len(c))
				body.Write(c)
				body.WriteString("\r\n")
			}
		}
		body.WriteString("0\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n")
		cr := newChunkedReader(&body, nil, true, int64(n))
		names := []string{"x-amz-checksum-crc32"}
		want, err := unsignedTrailerLength(int64(n), names)
		if err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(newReencoder(cr, names))
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if int64(len(out)) != want || !bytes.HasSuffix(out, []byte("0\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n")) {
			t.Fatalf("n=%d: re-encoded %d bytes, predicted %d", n, len(out), want)
		}
		back, err := io.ReadAll(newChunkedReader(bytes.NewReader(out), nil, true, int64(n)))
		if err != nil || !bytes.Equal(back, data) {
			t.Fatalf("n=%d: round trip: %v", n, err)
		}
	}
	// Length mismatches are rejected.
	bad := "5\r\nhello\r\n0\r\n\r\n"
	if _, err := io.ReadAll(newChunkedReader(strings.NewReader(bad), nil, false, 4)); err == nil {
		t.Fatal("body longer than the decoded length accepted")
	}
	if _, err := io.ReadAll(newChunkedReader(strings.NewReader(bad), nil, false, 6)); err == nil {
		t.Fatal("body shorter than the decoded length accepted")
	}
}
