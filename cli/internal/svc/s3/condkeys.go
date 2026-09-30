package s3

import (
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// requestKeys are the S3 condition keys of the request: the ones policies use to
// scope listings (s3:prefix, s3:delimiter, s3:max-keys), uploads (s3:x-amz-acl,
// s3:x-amz-server-side-encryption, s3:RequestObjectTag/<k>, ...) and access by
// signature type.
func (a *s3req) requestKeys(action string) map[string][]string {
	keys := map[string][]string{}
	q, h := a.query, a.q.R.Header
	if strings.HasPrefix(action, "s3:ListBucket") {
		for param, key := range map[string]string{"prefix": "s3:prefix", "delimiter": "s3:delimiter", "max-keys": "s3:max-keys"} {
			if _, ok := q[param]; ok {
				keys[key] = []string{q.Get(param)}
			}
		}
	}
	for _, name := range []string{"x-amz-acl", "x-amz-server-side-encryption", "x-amz-server-side-encryption-aws-kms-key-id",
		"x-amz-storage-class", "x-amz-copy-source", "x-amz-metadata-directive", "x-amz-tagging-directive",
		"x-amz-object-lock-mode", "x-amz-object-lock-legal-hold", "x-amz-grant-read", "x-amz-grant-write",
		"x-amz-grant-read-acp", "x-amz-grant-write-acp", "x-amz-grant-full-control", "x-amz-content-sha256"} {
		if v := h.Get(name); v != "" {
			keys["s3:"+name] = []string{v}
		}
	}
	if v := q.Get("versionId"); v != "" {
		keys["s3:versionid"] = []string{v}
	}
	if tags, err := url.ParseQuery(h.Get("X-Amz-Tagging")); err == nil {
		for k, v := range tags {
			keys["s3:requestobjecttag/"+strings.ToLower(k)] = v[:1]
		}
	}
	switch {
	case a.q.Sig != nil:
		keys["s3:signatureversion"] = []string{"AWS4-HMAC-SHA256"}
		keys["s3:authtype"] = []string{"REST-HEADER"}
		if a.q.Sig.Presigned {
			keys["s3:authtype"] = []string{"REST-QUERY-STRING"}
		}
	default:
		if _, ok := q["Signature"]; ok {
			keys["s3:signatureversion"] = []string{"AWS"}
			keys["s3:authtype"] = []string{"REST-QUERY-STRING"}
		} else if strings.HasPrefix(h.Get("Authorization"), "AWS ") {
			keys["s3:signatureversion"] = []string{"AWS"}
			keys["s3:authtype"] = []string{"REST-HEADER"}
		}
	}
	return keys
}

// existingTagActions are the actions that support s3:ExistingObjectTag.
var existingTagActions = map[string]bool{
	"s3:GetObject": true, "s3:GetObjectVersion": true, "s3:GetObjectAcl": true, "s3:GetObjectVersionAcl": true,
	"s3:GetObjectTagging": true, "s3:GetObjectVersionTagging": true, "s3:PutObjectTagging": true, "s3:PutObjectVersionTagging": true,
	"s3:DeleteObjectTagging": true, "s3:DeleteObjectVersionTagging": true, "s3:DeleteObject": true, "s3:DeleteObjectVersion": true,
	"s3:PutObjectAcl": true, "s3:PutObjectVersionAcl": true, "s3:GetObjectRetention": true, "s3:PutObjectRetention": true,
}

// access builds the resource-side inputs of an authorization check: the bucket
// policy, the request's condition keys, and (looked up only if a policy
// mentions it) the tags of the object addressed.
func (s *Service) access(a *s3req, action, resource string) (httpx.Access, error) {
	acc := httpx.Access{Keys: a.requestKeys(action)}
	bucket := bucketOf(resource, a.bucket)
	if bucket != "" && a.op.name != "CreateBucket" {
		text, err := s.bucketPolicy(a.ctx(), bucket)
		if err != nil && a.q.P == nil {
			return acc, err
		}
		acc.Policy = text
	}
	if existingTagActions[action] {
		if b, k, ok := strings.Cut(strings.TrimPrefix(resource, "arn:aws:s3:::"), "/"); ok && k != "" {
			version := a.query.Get("versionId")
			acc.LazyMarker = "existingobjecttag"
			acc.Lazy = func() map[string][]string {
				out := map[string][]string{}
				cl, err := s.cl()
				if err != nil {
					return out
				}
				t, err := cl.GetObjectTagging(a.ctx(), b, k, minio.GetObjectTaggingOptions{VersionID: version})
				if err != nil || t == nil {
					return out
				}
				for tk, tv := range t.ToMap() {
					out["s3:existingobjecttag/"+strings.ToLower(tk)] = []string{tv}
				}
				return out
			}
		}
	}
	return acc, nil
}
