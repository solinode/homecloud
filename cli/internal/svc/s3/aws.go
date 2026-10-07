package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/web"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/s3utils"
	"github.com/minio/minio-go/v7/pkg/signer"
)

// The AWS S3 endpoint. Requests signed with HomeCloud credentials (or
// anonymous requests to buckets whose policy allows them) are authorized with
// IAM using the S3 action of the operation, then either answered by HomeCloud
// (bucket bookkeeping: create, tagging, website, ACLs, CORS, ...) or proxied to
// MinIO, re-signed with MinIO's root credentials. Object bodies stream through
// without buffering; aws-chunked uploads are decoded and their chunk
// signatures verified with the caller's key.

const s3NS = "http://s3.amazonaws.com/doc/2006-03-01/"

// RegisterAWS serves S3 over the AWS REST protocol.
func (s *Service) RegisterAWS() {
	s.nativeOps = s.natives()
	awsapi.Register(&awsapi.Service{
		Name: "s3", REST: s.awsREST, RESTError: s.awsError, StreamBody: true, Unsigned: s.claimsUnsigned,
		ErrorCode: map[string]string{
			"InvalidClientTokenId": "InvalidAccessKeyId", "ResourceNotFound": "NoSuchKey", "ValidationError": "InvalidArgument",
			"BadRequest": "InvalidArgument", "Conflict": "OperationAborted", "AccessDenied": "AccessDenied",
			"InternalError": "InternalError", "ServiceUnavailable": "ServiceUnavailable",
		},
	})
}

// s3req is one S3 request.
type s3req struct {
	q       *awsapi.Req
	bucket  string
	key     string
	host    string // Host without the virtual-hosted bucket label
	query   url.Values
	op      s3op
	written bool
}

func (a *s3req) ctx() context.Context { return a.q.R.Context() }

func (a *s3req) bucketARN() string { return "arn:aws:s3:::" + a.bucket }
func (a *s3req) objectARN() string { return "arn:aws:s3:::" + a.bucket + "/" + a.key }

type s3op struct {
	name   string // AWS operation name
	action string // IAM action
	object bool   // resource is the object ARN (else the bucket ARN; "*" without a bucket)
}

func errNotImpl(what string) error {
	return awsapi.Errorf(http.StatusNotImplemented, "NotImplemented", "HomeCloud does not implement %s", what)
}

// ---- addressing ----

// hostBases are the host names virtual-hosted requests may use as a suffix.
func (s *Service) hostBases() []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || net.ParseIP(h) != nil || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	add(s.env.Cfg.PublicHost)
	add(s.env.Cfg.PublicHostname())
	if h, _, err := net.SplitHostPort(s.env.Cfg.APIAddr); err == nil {
		add(h)
	}
	add("localhost")
	return out
}

// vhostBucket returns the bucket of a virtual-hosted-style Host
// ("photos.localhost:8080", "photos.s3.localhost:8080") and the host without it.
func (s *Service) vhostBucket(hostport string) (bucket, rest string) {
	h, port, err := net.SplitHostPort(hostport)
	if err != nil {
		h, port = hostport, ""
	}
	h = strings.ToLower(h)
	for _, base := range s.hostBases() {
		label, ok := strings.CutSuffix(h, "."+base)
		if !ok {
			continue
		}
		label = strings.TrimSuffix(label, ".s3")
		if label == "" || label == "s3" || validBucket(label) != nil {
			return "", hostport
		}
		rest = base
		if port != "" {
			rest = net.JoinHostPort(base, port)
		}
		return label, rest
	}
	return "", hostport
}

func (s *Service) address(r *http.Request) (bucket, key, host string) {
	if isControl(r) {
		_, rest := s.vhostBucket(r.Host)
		return "", "", rest
	}
	if b, rest := s.vhostBucket(r.Host); b != "" {
		return b, strings.TrimPrefix(r.URL.Path, "/"), rest
	}
	bucket, key, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	return bucket, key, r.Host
}

// claimsUnsigned decides whether an unsigned request is an anonymous S3
// request (for an existing bucket) rather than a console or API request.
func (s *Service) claimsUnsigned(r *http.Request) bool {
	v2 := isV2Header(r)
	if r.Header.Get("Authorization") != "" && !v2 {
		return false
	}
	if b, _ := s.vhostBucket(r.Host); b != "" {
		return v2 || s.knownBucket(b)
	}
	p := r.URL.Path
	for _, pre := range []string{"/api/", "/website/", "/lambda-url/", "/apigw/", "/cognito/", "/_next/", "/lambda-code/", "/_s3/"} {
		if strings.HasPrefix(p, pre) {
			return false
		}
	}
	if v2 {
		return true // SigV2-signed: never a console request
	}
	bucket, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	if bucket == "" || validBucket(bucket) != nil || web.Serves(p) {
		return false
	}
	return s.knownBucket(bucket)
}

// nameCache remembers bucket names briefly so anonymous-request detection does
// not ask MinIO for every console request.
type nameCache struct {
	mu    sync.Mutex
	names map[string]bool
	at    time.Time
}

func (s *Service) knownBucket(name string) bool {
	s.names.mu.Lock()
	defer s.names.mu.Unlock()
	if s.names.names == nil || time.Since(s.names.at) > 5*time.Second {
		cl, err := s.cl()
		if err != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		bs, err := cl.ListBuckets(ctx)
		if err != nil {
			return false
		}
		s.names.names = map[string]bool{}
		for _, b := range bs {
			s.names.names[b.Name] = true
		}
		s.names.at = time.Now()
	}
	return s.names.names[name]
}

func (s *Service) forgetNames() {
	s.names.mu.Lock()
	s.names.names = nil
	s.names.mu.Unlock()
}

// ---- operations ----

type subOps struct{ sub, get, getAct, put, putAct, del, delAct string }

var bucketSubs = []subOps{
	{"acl", "GetBucketAcl", "s3:GetBucketAcl", "PutBucketAcl", "s3:PutBucketAcl", "", ""},
	{"policy", "GetBucketPolicy", "s3:GetBucketPolicy", "PutBucketPolicy", "s3:PutBucketPolicy", "DeleteBucketPolicy", "s3:DeleteBucketPolicy"},
	{"policyStatus", "GetBucketPolicyStatus", "s3:GetBucketPolicyStatus", "", "", "", ""},
	{"versioning", "GetBucketVersioning", "s3:GetBucketVersioning", "PutBucketVersioning", "s3:PutBucketVersioning", "", ""},
	{"tagging", "GetBucketTagging", "s3:GetBucketTagging", "PutBucketTagging", "s3:PutBucketTagging", "DeleteBucketTagging", "s3:PutBucketTagging"},
	{"lifecycle", "GetBucketLifecycleConfiguration", "s3:GetLifecycleConfiguration", "PutBucketLifecycleConfiguration", "s3:PutLifecycleConfiguration", "DeleteBucketLifecycle", "s3:PutLifecycleConfiguration"},
	{"cors", "GetBucketCors", "s3:GetBucketCORS", "PutBucketCors", "s3:PutBucketCORS", "DeleteBucketCors", "s3:PutBucketCORS"},
	{"encryption", "GetBucketEncryption", "s3:GetEncryptionConfiguration", "PutBucketEncryption", "s3:PutEncryptionConfiguration", "DeleteBucketEncryption", "s3:PutEncryptionConfiguration"},
	{"website", "GetBucketWebsite", "s3:GetBucketWebsite", "PutBucketWebsite", "s3:PutBucketWebsite", "DeleteBucketWebsite", "s3:DeleteBucketWebsite"},
	{"logging", "GetBucketLogging", "s3:GetBucketLogging", "PutBucketLogging", "s3:PutBucketLogging", "", ""},
	{"replication", "GetBucketReplication", "s3:GetReplicationConfiguration", "PutBucketReplication", "s3:PutReplicationConfiguration", "DeleteBucketReplication", "s3:PutReplicationConfiguration"},
	{"requestPayment", "GetBucketRequestPayment", "s3:GetBucketRequestPayment", "PutBucketRequestPayment", "s3:PutBucketRequestPayment", "", ""},
	{"object-lock", "GetObjectLockConfiguration", "s3:GetBucketObjectLockConfiguration", "PutObjectLockConfiguration", "s3:PutBucketObjectLockConfiguration", "", ""},
	{"accelerate", "GetBucketAccelerateConfiguration", "s3:GetAccelerateConfiguration", "PutBucketAccelerateConfiguration", "s3:PutAccelerateConfiguration", "", ""},
	{"notification", "GetBucketNotificationConfiguration", "s3:GetBucketNotification", "PutBucketNotificationConfiguration", "s3:PutBucketNotification", "", ""},
	{"ownershipControls", "GetBucketOwnershipControls", "s3:GetBucketOwnershipControls", "PutBucketOwnershipControls", "s3:PutBucketOwnershipControls", "DeleteBucketOwnershipControls", "s3:PutBucketOwnershipControls"},
	{"publicAccessBlock", "GetPublicAccessBlock", "s3:GetBucketPublicAccessBlock", "PutPublicAccessBlock", "s3:PutBucketPublicAccessBlock", "DeletePublicAccessBlock", "s3:PutBucketPublicAccessBlock"},
	{"analytics", "ListBucketAnalyticsConfigurations", "s3:GetAnalyticsConfiguration", "", "", "", ""},
	{"metrics", "ListBucketMetricsConfigurations", "s3:GetMetricsConfiguration", "", "", "", ""},
	{"inventory", "ListBucketInventoryConfigurations", "s3:GetInventoryConfiguration", "", "", "", ""},
	{"intelligent-tiering", "ListBucketIntelligentTieringConfigurations", "s3:GetIntelligentTieringConfiguration", "", "", "", ""},
	{"location", "GetBucketLocation", "s3:GetBucketLocation", "", "", "", ""},
	{"uploads", "ListMultipartUploads", "s3:ListBucketMultipartUploads", "", "", "", ""},
	{"versions", "ListObjectVersions", "s3:ListBucketVersions", "", "", "", ""},
}

var objectSubs = []subOps{
	{"acl", "GetObjectAcl", "s3:GetObjectAcl", "PutObjectAcl", "s3:PutObjectAcl", "", ""},
	{"tagging", "GetObjectTagging", "s3:GetObjectTagging", "PutObjectTagging", "s3:PutObjectTagging", "DeleteObjectTagging", "s3:DeleteObjectTagging"},
	{"retention", "GetObjectRetention", "s3:GetObjectRetention", "PutObjectRetention", "s3:PutObjectRetention", "", ""},
	{"legal-hold", "GetObjectLegalHold", "s3:GetObjectLegalHold", "PutObjectLegalHold", "s3:PutObjectLegalHold", "", ""},
	{"attributes", "GetObjectAttributes", "s3:GetObjectAttributes", "", "", "", ""},
}

// Query parameters that are values rather than subresources.
var valueParams = map[string]bool{
	"prefix": true, "delimiter": true, "marker": true, "max-keys": true, "list-type": true, "continuation-token": true,
	"start-after": true, "fetch-owner": true, "encoding-type": true, "key-marker": true, "version-id-marker": true,
	"upload-id-marker": true, "max-uploads": true, "max-parts": true, "part-number-marker": true, "versionId": true,
	"partNumber": true, "uploadId": true, "id": true, "max-buckets": true, "bucket-region": true,
	"x-id": true, "AWSAccessKeyId": true, "Signature": true, "Expires": true, "content-type": true, "content-md5": true,
	"response-content-type": true, "response-content-language": true, "response-expires": true,
	"response-cache-control": true, "response-content-disposition": true, "response-content-encoding": true,
}

// versioned turns an object action into its version-specific form when a
// versionId is given (s3:GetObject → s3:GetObjectVersion).
func versioned(action string, q url.Values) string {
	if q.Get("versionId") == "" {
		return action
	}
	switch action {
	case "s3:GetObject", "s3:DeleteObject", "s3:GetObjectAcl", "s3:PutObjectAcl", "s3:GetObjectTagging",
		"s3:PutObjectTagging", "s3:DeleteObjectTagging", "s3:GetObjectAttributes":
		return strings.Replace(action, "Object", "ObjectVersion", 1)
	}
	return action
}

func findSub(subs []subOps, q url.Values) (subOps, bool) {
	for _, s := range subs {
		if _, ok := q[s.sub]; ok {
			return s, true
		}
	}
	return subOps{}, false
}

// resolve maps method + path + query to the S3 operation.
func resolve(method, bucket, key string, q url.Values, h http.Header) (s3op, error) {
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-") || valueParams[k] {
			continue
		}
		known := false
		for _, s := range append(append([]subOps{}, bucketSubs...), objectSubs...) {
			if s.sub == k {
				known = true
			}
		}
		if !known && k != "delete" && k != "restore" && k != "select" && k != "select-type" {
			return s3op{}, errNotImpl("the ?" + k + " subresource")
		}
	}
	if bucket == "" {
		if method == http.MethodGet {
			return s3op{"ListBuckets", "s3:ListAllMyBuckets", false}, nil
		}
		return s3op{}, awsapi.Errorf(http.StatusMethodNotAllowed, "MethodNotAllowed", "the specified method is not allowed against this resource")
	}
	pick := func(subs []subOps, object bool) (s3op, bool, error) {
		sub, ok := findSub(subs, q)
		if !ok {
			return s3op{}, false, nil
		}
		var name, act string
		switch method {
		case http.MethodGet, http.MethodHead:
			name, act = sub.get, sub.getAct
		case http.MethodPut:
			name, act = sub.put, sub.putAct
		case http.MethodDelete:
			name, act = sub.del, sub.delAct
		}
		if name == "" {
			return s3op{}, true, errNotImpl(method + " ?" + sub.sub)
		}
		if object {
			act = versioned(act, q)
		}
		return s3op{name, act, object}, true, nil
	}
	if key == "" {
		if op, ok, err := pick(bucketSubs, false); ok || err != nil {
			return op, err
		}
		switch method {
		case http.MethodGet:
			if q.Get("list-type") == "2" {
				return s3op{"ListObjectsV2", "s3:ListBucket", false}, nil
			}
			return s3op{"ListObjects", "s3:ListBucket", false}, nil
		case http.MethodHead:
			return s3op{"HeadBucket", "s3:ListBucket", false}, nil
		case http.MethodPut:
			return s3op{"CreateBucket", "s3:CreateBucket", false}, nil
		case http.MethodDelete:
			return s3op{"DeleteBucket", "s3:DeleteBucket", false}, nil
		case http.MethodPost:
			if _, ok := q["delete"]; ok {
				return s3op{"DeleteObjects", "s3:DeleteObject", false}, nil
			}
			return s3op{}, errNotImpl("browser-based POST uploads")
		}
		return s3op{}, awsapi.Errorf(http.StatusMethodNotAllowed, "MethodNotAllowed", "the specified method is not allowed against this resource")
	}
	if len(key) > 1024 {
		return s3op{}, awsapi.Errorf(http.StatusBadRequest, "KeyTooLongError", "your key is too long")
	}
	_, hasUpload := q["uploadId"]
	switch method {
	case http.MethodGet, http.MethodHead:
		if hasUpload && method == http.MethodGet {
			return s3op{"ListParts", "s3:ListMultipartUploadParts", true}, nil
		}
		if op, ok, err := pick(objectSubs, true); ok || err != nil {
			return op, err
		}
		if method == http.MethodHead {
			return s3op{"HeadObject", versioned("s3:GetObject", q), true}, nil
		}
		return s3op{"GetObject", versioned("s3:GetObject", q), true}, nil
	case http.MethodPut:
		copying := h.Get("X-Amz-Copy-Source") != ""
		if hasUpload {
			if copying {
				return s3op{"UploadPartCopy", "s3:PutObject", true}, nil
			}
			return s3op{"UploadPart", "s3:PutObject", true}, nil
		}
		if op, ok, err := pick(objectSubs, true); ok || err != nil {
			return op, err
		}
		if copying {
			return s3op{"CopyObject", "s3:PutObject", true}, nil
		}
		return s3op{"PutObject", "s3:PutObject", true}, nil
	case http.MethodDelete:
		if hasUpload {
			return s3op{"AbortMultipartUpload", "s3:AbortMultipartUpload", true}, nil
		}
		if op, ok, err := pick(objectSubs, true); ok || err != nil {
			return op, err
		}
		return s3op{"DeleteObject", versioned("s3:DeleteObject", q), true}, nil
	case http.MethodPost:
		if _, ok := q["uploads"]; ok {
			return s3op{"CreateMultipartUpload", "s3:PutObject", true}, nil
		}
		if hasUpload {
			return s3op{"CompleteMultipartUpload", "s3:PutObject", true}, nil
		}
		if _, ok := q["restore"]; ok {
			return s3op{"RestoreObject", "s3:RestoreObject", true}, nil
		}
		if _, ok := q["select"]; ok {
			return s3op{"SelectObjectContent", "s3:GetObject", true}, nil
		}
	}
	return s3op{}, awsapi.Errorf(http.StatusMethodNotAllowed, "MethodNotAllowed", "the specified method is not allowed against this resource")
}

// ---- request handling ----

func (s *Service) awsREST(q *awsapi.Req) {
	a := &s3req{q: q, query: q.R.URL.Query()}
	a.bucket, a.key, a.host = s.address(q.R)
	err := s.serveAWS(a)
	if err != nil {
		if a.written {
			log.Printf("s3: %s %s/%s failed after the response started: %v", a.op.name, a.bucket, a.key, err)
			return
		}
		q.Fail(err)
	}
}

func (s *Service) serveAWS(a *s3req) error {
	q := a.q
	if q.R.Header.Get("Expect") == "100-continue" && q.R.ContentLength == 0 {
		// Go sends 100 Continue only when a body is read. botocore reuses a
		// connection wrongly after a final response to an Expect request that
		// got no 100 Continue, so send it for empty bodies too.
		q.W.WriteHeader(http.StatusContinue)
	}
	if q.P == nil && q.Sig == nil {
		if _, err := s.sigV2(a); err != nil {
			return err
		}
	}
	liftQueryHeaders(a)
	if isControl(q.R) {
		return s.serveControl(a)
	}
	if a.bucket != "" && (validBucket(a.bucket) != nil || a.bucket == "minio") {
		// "minio" is reserved: MinIO serves its admin API under /minio/.
		return awsapi.Errorf(http.StatusBadRequest, "InvalidBucketName", "the specified bucket is not valid")
	}
	if err := checkKey(a.key); err != nil {
		return err
	}
	op, err := resolve(q.R.Method, a.bucket, a.key, a.query, q.R.Header)
	if err != nil {
		return err
	}
	a.op, q.Op = op, op.name
	for _, h := range []string{"X-Amz-Expected-Bucket-Owner", "X-Amz-Source-Expected-Bucket-Owner"} {
		if v := q.R.Header.Get(h); v != "" && v != q.Account {
			return awsapi.Errorf(http.StatusForbidden, "AccessDenied", "Access Denied")
		}
	}
	res := "*"
	switch {
	case op.object:
		res = a.objectARN()
	case a.bucket != "":
		res = a.bucketARN()
	}
	if err := s.authorize(a, op.action, res); err != nil {
		return err
	}
	if strings.EqualFold(q.R.Header.Get("X-Amz-Bypass-Governance-Retention"), "true") {
		if err := s.authorize(a, "s3:BypassGovernanceRetention", res); err != nil {
			return err
		}
		if err := s.authorize(a, op.action, res); err != nil { // recorded as the call's action
			return err
		}
	}
	// Uploads can set retention or a legal hold through headers: AWS needs the
	// matching permissions for that, not just s3:PutObject.
	if op.object && (op.action == "s3:PutObject" || op.name == "CopyObject" || op.name == "CreateMultipartUpload") {
		h := q.R.Header
		need := ""
		if h.Get("X-Amz-Object-Lock-Mode") != "" || h.Get("X-Amz-Object-Lock-Retain-Until-Date") != "" {
			need = "s3:PutObjectRetention"
		}
		if h.Get("X-Amz-Object-Lock-Legal-Hold") != "" {
			need = "s3:PutObjectLegalHold"
		}
		if need != "" {
			if err := s.authorize(a, need, res); err != nil {
				return err
			}
			if err := s.authorize(a, op.action, res); err != nil { // recorded as the call's action
				return err
			}
		}
	}
	if _, err := s.cl(); err != nil {
		return err
	}
	if h := s.nativeOps[op.name]; h != nil {
		return h(a)
	}
	return s.forwardOp(a)
}

// checkKey rejects keys MinIO would resolve as paths ("a/../b").
func checkKey(key string) error {
	for _, seg := range strings.Split(key, "/") {
		if seg == "." || seg == ".." {
			return awsapi.Errorf(http.StatusBadRequest, "InvalidArgument", "object keys with . or .. path segments are not supported")
		}
	}
	return nil
}

// liftQueryHeaders turns x-amz-* query parameters into headers: presigned URLs
// (SigV2, and SigV4 from some SDKs) carry signed headers in the query. It runs
// before the operation is resolved and authorized.
func liftQueryHeaders(a *s3req) {
	v2 := isV2Query(a.query)
	for k, vv := range a.query {
		lk := strings.ToLower(k)
		if strings.HasPrefix(k, "X-Amz-") || len(vv) == 0 || lk == "x-amz-security-token" {
			continue
		}
		if strings.HasPrefix(lk, "x-amz-") || v2 && (lk == "content-type" || lk == "content-md5") {
			if a.q.R.Header.Get(k) == "" {
				a.q.R.Header.Set(k, vv[0])
			}
			delete(a.query, k)
		}
	}
}

// authorize checks IAM and the bucket policy.
func (s *Service) authorize(a *s3req, action, resource string) error {
	q := a.q
	acc, err := s.access(a, action, resource)
	if err != nil {
		return err
	}
	if q.P == nil {
		// Anonymous: only a bucket policy naming everyone can allow it. The
		// request keys (source IP, TLS) come from the connection.
		acc.Keys = mergeKeys(httpx.RequestContext(q.R), acc.Keys)
		if httpx.PermitsAnonymous(action, resource, acc) {
			return nil
		}
	}
	return q.AuthorizeWith(action, resource, acc)
}

func mergeKeys(a, b map[string][]string) map[string][]string {
	out := make(map[string][]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func bucketOf(arn, def string) string {
	if rest, ok := strings.CutPrefix(arn, "arn:aws:s3:::"); ok {
		b, _, _ := strings.Cut(rest, "/")
		return b
	}
	return def
}

func (s *Service) awsError(q *awsapi.Req, e *awsapi.Error) {
	res := e.Resource
	if res == "" {
		res = q.R.URL.Path
	}
	h := q.W.Header()
	h.Set("Content-Type", "application/xml")
	q.W.WriteHeader(e.Status)
	if q.R.Method == http.MethodHead {
		return
	}
	fmt.Fprintf(q.W, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Error><Code>%s</Code><Message>%s</Message><Resource>%s</Resource><RequestId>%s</RequestId></Error>",
		xmlEsc(e.Code), xmlEsc(e.Message), xmlEsc(res), q.RequestID)
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (a *s3req) writeXML(status int, v any) error {
	b, err := xml.Marshal(v)
	if err != nil {
		return err
	}
	a.writeRaw(status, append([]byte(xml.Header), b...))
	return nil
}

func (a *s3req) writeRaw(status int, b []byte) {
	a.q.W.Header().Set("Content-Type", "application/xml")
	a.q.W.Header().Set("Content-Length", strconv.Itoa(len(b)))
	a.written = true
	a.q.W.WriteHeader(status)
	if a.q.R.Method != http.MethodHead {
		_, _ = a.q.W.Write(b)
	}
}

func (a *s3req) noContent() error {
	a.written = true
	a.q.W.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- request bodies ----

type upload struct {
	r      io.Reader
	length int64
	hash   string         // x-amz-content-sha256 for MinIO
	cr     *chunkedReader // set for aws-chunked bodies
	names  []string       // checksum trailers
	adjust func(http.Header)
}

func isHexHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func dropEncoding(h http.Header, enc string) {
	var keep []string
	for _, v := range h.Values("Content-Encoding") {
		for _, e := range strings.Split(v, ",") {
			if e = strings.TrimSpace(e); e != "" && !strings.EqualFold(e, enc) {
				keep = append(keep, e)
			}
		}
	}
	h.Del("Content-Encoding")
	if len(keep) > 0 {
		h.Set("Content-Encoding", strings.Join(keep, ","))
	}
}

// traceBody, when set (tests), observes each request's payload hash.
var traceBody func(op, hash string)

// body prepares the request body for MinIO.
func (s *Service) body(a *s3req) (*upload, error) {
	r := a.q.R
	hash := r.Header.Get("X-Amz-Content-Sha256")
	if a.q.Sig != nil && a.q.Sig.PayloadHash != "" {
		hash = a.q.Sig.PayloadHash
	}
	if hash == "" {
		hash = awsapi.UnsignedPayload
	}
	if traceBody != nil {
		traceBody(a.op.name, hash)
	}
	if !strings.HasPrefix(hash, "STREAMING-") {
		if hash != awsapi.UnsignedPayload && !isHexHash(hash) {
			return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidArgument", "invalid x-amz-content-sha256")
		}
		return &upload{r: r.Body, length: r.ContentLength, hash: hash}, nil
	}
	decoded, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64)
	if err != nil || decoded < 0 {
		return nil, awsapi.Errorf(http.StatusLengthRequired, "MissingContentLength", "x-amz-decoded-content-length is required for aws-chunked uploads")
	}
	var cs *chunkSigner
	switch hash {
	case streamingSigned, streamingSignedTrailer:
		if a.q.Sig == nil || a.q.Sig.Presigned {
			return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidRequest", "signed aws-chunked uploads need a signed request")
		}
		cs = newChunkSigner(a.q.Sig, a.q.Secret)
	case streamingUnsignedTrl:
	default:
		return nil, errNotImpl("x-amz-content-sha256 " + hash)
	}
	trailer := strings.HasSuffix(hash, "-TRAILER")
	names := trailerNames(r.Header)
	if trailer && len(names) == 0 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidRequest", "x-amz-trailer is required for %s", hash)
	}
	if !trailer {
		names = nil
	}
	cr := newChunkedReader(r.Body, cs, trailer, decoded)
	up := &upload{r: cr, length: decoded, hash: awsapi.UnsignedPayload, cr: cr, names: names}
	up.adjust = func(h http.Header) {
		dropEncoding(h, "aws-chunked")
		h.Del("X-Amz-Decoded-Content-Length")
		h.Del("X-Amz-Trailer")
	}
	if !trailer {
		return up, nil
	}
	n, err := unsignedTrailerLength(decoded, names)
	if err != nil {
		return nil, err
	}
	up.r, up.length, up.hash = newReencoder(cr, names), n, streamingUnsignedTrl
	up.adjust = func(h http.Header) {
		dropEncoding(h, "aws-chunked")
		enc := h.Get("Content-Encoding")
		if enc != "" {
			h.Set("Content-Encoding", "aws-chunked,"+enc)
		} else {
			h.Set("Content-Encoding", "aws-chunked")
		}
		h.Set("X-Amz-Decoded-Content-Length", strconv.FormatInt(decoded, 10))
		h.Set("X-Amz-Trailer", strings.Join(names, ","))
	}
	return up, nil
}

// readBody reads a small request body (configuration documents), verifying
// its payload hash. Checksum trailers are returned as headers.
func (s *Service) readBody(a *s3req, limit int64) ([]byte, http.Header, error) {
	up, err := s.body(a)
	if err != nil {
		return nil, nil, err
	}
	src := up.r
	if up.cr != nil {
		src = up.cr
	}
	b, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		var ae *awsapi.Error
		if errors.As(err, &ae) {
			return nil, nil, ae
		}
		return nil, nil, awsapi.Errorf(http.StatusBadRequest, "IncompleteBody", "read body: %v", err)
	}
	if int64(len(b)) > limit {
		return nil, nil, awsapi.Errorf(http.StatusBadRequest, "MaxMessageLengthExceeded", "the request body is too large")
	}
	if isHexHash(up.hash) && awsapi.HashHex(b) != up.hash {
		return nil, nil, awsapi.Errorf(http.StatusBadRequest, "XAmzContentSHA256Mismatch", "the provided x-amz-content-sha256 header does not match what was computed")
	}
	extra := http.Header{}
	if up.cr != nil {
		for k, v := range up.cr.trailers {
			extra[k] = v
		}
	}
	return b, extra, nil
}

// ---- proxying to MinIO ----

var stripHeaders = []string{
	"Authorization", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256", "Host", "Content-Length",
	"Connection", "Keep-Alive", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding",
	"Upgrade", "Expect", "X-Amz-Expected-Bucket-Owner", "X-Amz-Source-Expected-Bucket-Owner", "X-Amz-Acl",
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Cookie", transitionSizeHeader,
}

var presignParams = []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders",
	"X-Amz-Signature", "X-Amz-Security-Token", "X-Amz-Content-Sha256", "x-id"}

// send forwards the request to MinIO with body (nil for none).
func (s *Service) send(a *s3req, method string, body io.Reader, length int64, hash string, adjust func(http.Header)) (*http.Response, error) {
	s.mu.RLock()
	cl, user, pass := s.client, s.user, s.pass
	s.mu.RUnlock()
	if cl == nil {
		return nil, core.Errf(http.StatusServiceUnavailable, "ServiceUnavailable", "S3 is %s", s.status)
	}
	u := *cl.EndpointURL()
	u.Path = "/" + a.bucket
	if a.key != "" {
		u.Path += "/" + a.key
	}
	u.RawPath = s3utils.EncodePath(u.Path)
	q := url.Values{}
	for k, v := range a.query {
		q[k] = v
	}
	for _, k := range presignParams {
		q.Del(k)
	}
	for _, k := range v2QueryParams {
		q.Del(k)
	}
	q.Del("x-amz-security-token")
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	req, err := http.NewRequestWithContext(a.ctx(), method, "http://minio", nil)
	if err != nil {
		return nil, err
	}
	req.URL = &u
	req.Header = a.q.R.Header.Clone()
	for _, h := range stripHeaders {
		req.Header.Del(h)
	}
	for k := range req.Header {
		// MinIO extensions (x-minio-force-delete, x-minio-extract, ...) are not
		// part of the S3 API and are not authorized by HomeCloud.
		if strings.HasPrefix(k, "X-Amz-Grant-") || strings.HasPrefix(k, "X-Minio-") {
			req.Header.Del(k)
		}
	}
	if adjust != nil {
		adjust(req.Header)
	}
	req.Header.Set("X-Amz-Content-Sha256", hash)
	req.Host = a.host
	if body != nil && length != 0 {
		req.Body = io.NopCloser(body)
		req.ContentLength = length
	} else {
		req.Body = http.NoBody
		req.ContentLength = 0
	}
	signed := signer.SignV4(*req, user, pass, "", signingRegion)
	resp, err := s.proxyClient().Do(signed)
	if err != nil {
		var ae *awsapi.Error
		if errors.As(err, &ae) {
			return nil, ae
		}
		if a.ctx().Err() != nil {
			return nil, awsapi.Errorf(499, "RequestCanceled", "the client closed the request")
		}
		log.Printf("s3: proxy %s %s: %v", method, u.Path, err)
		return nil, awsapi.Errorf(http.StatusBadGateway, "InternalError", "the storage backend did not respond")
	}
	return resp, nil
}

var (
	proxyOnce sync.Once
	proxyHTTP *http.Client
)

func (s *Service) proxyClient() *http.Client {
	proxyOnce.Do(func() {
		proxyHTTP = &http.Client{
			Transport: &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second,
				DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	})
	return proxyHTTP
}

var skipResponseHeaders = map[string]bool{
	"X-Amz-Request-Id": true, "X-Amz-Id-2": true, "Server": true, "Connection": true, "Keep-Alive": true,
	"Transfer-Encoding": true, "Strict-Transport-Security": true, "Vary": true,
	"Access-Control-Allow-Origin": true, "Access-Control-Allow-Credentials": true, "Access-Control-Expose-Headers": true,
}

// relay copies MinIO's response to the client.
func (s *Service) relay(a *s3req, resp *http.Response) error {
	defer resp.Body.Close()
	h := a.q.W.Header()
	for k, vv := range resp.Header {
		if skipResponseHeaders[k] {
			continue
		}
		if strings.HasPrefix(k, "X-Amz-Meta-") {
			k = strings.ToLower(k) // user metadata keys keep the case S3 uses
		}
		h[k] = vv
	}
	if h.Get("X-Amz-Bucket-Region") != "" {
		h.Set("X-Amz-Bucket-Region", s.region())
	}
	// Object bytes are user content served from the console's origin.
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads")
	h.Set("X-Content-Type-Options", "nosniff")
	a.written = true
	a.q.W.WriteHeader(resp.StatusCode)
	if a.q.R.Method == http.MethodHead {
		return nil
	}
	_, err := io.Copy(a.q.W, resp.Body)
	return err
}

func (s *Service) region() string {
	if s.env.Cfg.Region != "" {
		return s.env.Cfg.Region
	}
	return core.Region
}

// forwardOp proxies an operation MinIO implements.
func (s *Service) forwardOp(a *s3req) error {
	r := a.q.R
	switch a.op.name {
	case "CopyObject", "UploadPartCopy":
		src := r.Header.Get("X-Amz-Copy-Source")
		b, k, ver, err := parseCopySource(src)
		if err != nil {
			return err
		}
		if err := checkKey(k); err != nil {
			return err
		}
		act := "s3:GetObject"
		if ver != "" {
			act = "s3:GetObjectVersion"
		}
		if err := s.authorize(a, act, "arn:aws:s3:::"+b+"/"+k); err != nil {
			return err
		}
		if err := s.authorize(a, a.op.action, a.objectARN()); err != nil { // recorded as the call's resource
			return err
		}
		if a.op.name == "CopyObject" && r.Header.Get("X-Amz-Tagging-Directive") == "REPLACE" && r.Header.Get("X-Amz-Tagging") != "" {
			if err := s.authorize(a, "s3:PutObjectTagging", a.objectARN()); err != nil {
				return err
			}
		}
	case "PutObject":
		if r.Header.Get("X-Amz-Tagging") != "" {
			if err := s.authorize(a, "s3:PutObjectTagging", a.objectARN()); err != nil {
				return err
			}
			if err := s.authorize(a, "s3:PutObject", a.objectARN()); err != nil {
				return err
			}
		}
	case "DeleteObjects":
		return s.deleteObjects(a)
	case "PutBucketPolicy":
		return s.putBucketPolicy(a)
	case "GetBucketPolicy":
		return s.getBucketPolicy(a)
	case "PutBucketLifecycleConfiguration":
		return s.awsPutLifecycle(a)
	case "GetBucketLifecycleConfiguration":
		if ok, err := s.awsGetLifecycle(a); ok {
			return err
		}
	case "CreateMultipartUpload":
		if r.Header.Get("X-Amz-Tagging") != "" {
			if err := s.authorize(a, "s3:PutObjectTagging", a.objectARN()); err != nil {
				return err
			}
		}
	}
	up, err := s.body(a)
	if err != nil {
		return err
	}
	resp, err := s.send(a, r.Method, up.r, up.length, up.hash, up.adjust)
	if err != nil {
		return err
	}
	switch a.op.name {
	case "DeleteBucket":
		if resp.StatusCode/100 == 2 {
			_ = store.Delete(s.env.Store, cBuckets, a.bucket)
			s.forgetNames()
			s.forgetPolicy(a.bucket)
		}
	case "DeleteBucketPolicy":
		s.forgetPolicy(a.bucket)
		if resp.StatusCode/100 == 2 {
			_ = s.updateMeta(a.bucket, func(m *bucketMeta) { m.setConfig("policy", ""); m.setConfig("policyNorm", "") })
		}
	case "DeleteBucketLifecycle":
		if resp.StatusCode/100 == 2 {
			s.forgetLifecycle(a.bucket)
		}
	case "PutBucketLifecycleConfiguration", "GetBucketLifecycleConfiguration":
		// MinIO has no transition size setting; report what was set (AWS's
		// default otherwise), as the Terraform provider waits for it.
		if resp.StatusCode/100 == 2 {
			if a.op.name == "PutBucketLifecycleConfiguration" {
				v := r.Header.Get(transitionSizeHeader)
				_ = s.updateMeta(a.bucket, func(m *bucketMeta) { m.setConfig("transitionMinSize", v) })
			}
			v := s.meta(a.bucket).Config["transitionMinSize"]
			if v == "" {
				v = "all_storage_classes_128K"
			}
			resp.Header.Set(transitionSizeHeader, v)
		}
	}
	return s.relay(a, resp)
}

const transitionSizeHeader = "X-Amz-Transition-Default-Minimum-Object-Size"

// parseCopySource parses x-amz-copy-source ("bucket/key?versionId=v", URL-encoded).
func parseCopySource(src string) (bucket, key, version string, err error) {
	src, query, _ := strings.Cut(src, "?")
	if query != "" {
		if v, e := url.ParseQuery(query); e == nil {
			version = v.Get("versionId")
		}
	}
	p, e := url.PathUnescape(strings.TrimPrefix(src, "/"))
	if e != nil {
		return "", "", "", awsapi.Errorf(http.StatusBadRequest, "InvalidArgument", "invalid x-amz-copy-source")
	}
	bucket, key, _ = strings.Cut(p, "/")
	if bucket == "" || key == "" {
		return "", "", "", awsapi.Errorf(http.StatusBadRequest, "InvalidArgument", "x-amz-copy-source must be bucket/key")
	}
	return bucket, key, version, nil
}

// deleteObjects authorizes each key, then forwards the (buffered) request.
func (s *Service) deleteObjects(a *s3req) error {
	b, extra, err := s.readBody(a, 2<<20)
	if err != nil {
		return err
	}
	var in struct {
		Objects []struct {
			Key       string `xml:"Key"`
			VersionID string `xml:"VersionId"`
		} `xml:"Object"`
	}
	if err := xml.Unmarshal(b, &in); err != nil {
		return awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed")
	}
	for _, o := range in.Objects {
		// Keys in the body are forwarded as they are, so they need the same check
		// as the key in the URL: "allowed/../secret" is authorized as a key under
		// allowed/ but may name another object.
		if err := checkKey(o.Key); err != nil {
			return err
		}
		if len(o.Key) > 1024 || strings.ContainsRune(o.Key, 0) {
			return awsapi.Errorf(http.StatusBadRequest, "KeyTooLongError", "the object key is invalid")
		}
		act := "s3:DeleteObject"
		if o.VersionID != "" {
			act = "s3:DeleteObjectVersion"
		}
		if err := s.authorize(a, act, "arn:aws:s3:::"+a.bucket+"/"+o.Key); err != nil {
			return err
		}
	}
	resp, err := s.sendBuffered(a, b, extra)
	if err != nil {
		return err
	}
	return s.relay(a, resp)
}

// sendBuffered forwards a body read with readBody; checksum trailers become headers.
func (s *Service) sendBuffered(a *s3req, b []byte, extra http.Header) (*http.Response, error) {
	return s.send(a, a.q.R.Method, bytes.NewReader(b), int64(len(b)), awsapi.HashHex(b), func(h http.Header) {
		dropEncoding(h, "aws-chunked")
		h.Del("X-Amz-Decoded-Content-Length")
		h.Del("X-Amz-Trailer")
		for k, v := range extra {
			h[http.CanonicalHeaderKey(k)] = v
		}
	})
}

// Bucket policies: MinIO normalizes the JSON it stores ("*" becomes
// {"AWS":["*"]}, strings become lists), while AWS returns the document as it
// was put, which tools such as Terraform compare against. HomeCloud keeps the
// original text and returns it while MinIO still holds the same policy.
func (s *Service) putBucketPolicy(a *s3req) error {
	b, extra, err := s.readBody(a, 20<<10)
	if err != nil {
		return err
	}
	if err := validateBucketPolicy(b); err != nil {
		return err
	}
	resp, err := s.sendBuffered(a, b, extra)
	if err != nil {
		return err
	}
	s.forgetPolicy(a.bucket)
	if resp.StatusCode == http.StatusBadRequest {
		// MinIO cannot express some policies (ArnLike, aws:PrincipalArn, ...).
		// HomeCloud evaluates the policy itself, so it keeps the document and
		// leaves MinIO without one (its own endpoint then grants nothing).
		resp.Body.Close()
		cl, err := s.cl()
		if err != nil {
			return err
		}
		if err := cl.SetBucketPolicy(a.ctx(), a.bucket, ""); err != nil {
			return s3err(err)
		}
		resp = &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}
	}
	if resp.StatusCode/100 == 2 {
		cl, _ := s.cl()
		norm, _ := cl.GetBucketPolicy(a.ctx(), a.bucket)
		_ = s.updateMeta(a.bucket, func(m *bucketMeta) {
			m.setConfig("policy", string(b))
			m.setConfig("policyNorm", awsapi.HashHex([]byte(norm)))
		})
	}
	return s.relay(a, resp)
}

func (s *Service) getBucketPolicy(a *s3req) error {
	resp, err := s.send(a, http.MethodGet, nil, 0, awsapi.UnsignedPayload, nil)
	if err != nil {
		return err
	}
	m := s.meta(a.bucket)
	if resp.StatusCode == http.StatusNotFound && m.Config["policy"] != "" && m.Config["policyNorm"] == awsapi.HashHex(nil) {
		// A policy MinIO could not store (see putBucketPolicy).
		resp.Body.Close()
		body := []byte(m.Config["policy"])
		resp.StatusCode, resp.Status = http.StatusOK, "200 OK"
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		resp.Header.Set("Content-Type", "application/json")
		return s.relay(a, resp)
	}
	if resp.StatusCode != http.StatusOK || m.Config["policy"] == "" {
		return s.relay(a, resp)
	}
	norm, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	body := norm
	if awsapi.HashHex(norm) == m.Config["policyNorm"] {
		body = []byte(m.Config["policy"])
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return s.relay(a, resp)
}

// ---- operations answered by HomeCloud ----

func (s *Service) natives() map[string]func(*s3req) error {
	return map[string]func(*s3req) error{
		"ListBuckets":                                s.awsListBuckets,
		"CreateBucket":                               s.awsCreateBucket,
		"GetBucketLocation":                          s.awsGetBucketLocation,
		"GetBucketTagging":                           s.awsGetBucketTagging,
		"PutBucketTagging":                           s.awsPutBucketTagging,
		"DeleteBucketTagging":                        s.awsDeleteBucketTagging,
		"GetBucketWebsite":                           s.awsGetBucketWebsite,
		"PutBucketWebsite":                           s.awsPutBucketWebsite,
		"DeleteBucketWebsite":                        s.awsDeleteBucketWebsite,
		"GetBucketAcl":                               s.awsGetACL,
		"GetObjectAcl":                               s.awsGetACL,
		"PutBucketAcl":                               s.awsPutBucketACL,
		"PutObjectAcl":                               s.awsPutObjectACL,
		"GetBucketPolicyStatus":                      s.awsPolicyStatus,
		"GetBucketCors":                              s.storedGet,
		"PutBucketCors":                              s.storedPut,
		"DeleteBucketCors":                           s.storedDelete,
		"GetBucketEncryption":                        s.storedGet,
		"PutBucketEncryption":                        s.storedPut,
		"DeleteBucketEncryption":                     s.storedDelete,
		"GetBucketOwnershipControls":                 s.storedGet,
		"PutBucketOwnershipControls":                 s.storedPut,
		"DeleteBucketOwnershipControls":              s.storedDelete,
		"GetPublicAccessBlock":                       s.storedGet,
		"PutPublicAccessBlock":                       s.storedPut,
		"DeletePublicAccessBlock":                    s.storedDelete,
		"GetBucketLogging":                           s.storedGet,
		"PutBucketLogging":                           s.storedPut,
		"GetBucketRequestPayment":                    s.storedGet,
		"PutBucketRequestPayment":                    s.storedPut,
		"GetBucketAccelerateConfiguration":           s.storedGet,
		"PutBucketAccelerateConfiguration":           s.storedPut,
		"ListBucketAnalyticsConfigurations":          s.emptyConfigList,
		"ListBucketMetricsConfigurations":            s.emptyConfigList,
		"ListBucketInventoryConfigurations":          s.emptyConfigList,
		"ListBucketIntelligentTieringConfigurations": s.emptyConfigList,
	}
}

func (s *Service) requireBucket(a *s3req) error {
	cl, err := s.cl()
	if err != nil {
		return err
	}
	ok, err := cl.BucketExists(a.ctx(), a.bucket)
	if err != nil {
		return s3err(err)
	}
	if !ok {
		return &awsapi.Error{Status: http.StatusNotFound, Code: "NoSuchBucket", Message: "The specified bucket does not exist", Resource: "/" + a.bucket}
	}
	return nil
}

func (s *Service) ownerID() string {
	h := sha256.Sum256([]byte("homecloud:" + s.env.AccountID))
	return hex.EncodeToString(h[:])
}

type xmlOwner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

func (s *Service) owner() xmlOwner { return xmlOwner{ID: s.ownerID(), DisplayName: s.env.AccountID} }

func (s *Service) awsListBuckets(a *s3req) error {
	cl, _ := s.cl()
	bs, err := cl.ListBuckets(a.ctx())
	if err != nil {
		return s3err(err)
	}
	type bucket struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
		BucketRegion string `xml:"BucketRegion"`
	}
	out := struct {
		XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListAllMyBucketsResult"`
		Owner   xmlOwner `xml:"Owner"`
		Buckets []bucket `xml:"Buckets>Bucket"`
		Prefix  string   `xml:"Prefix,omitempty"`
	}{Owner: s.owner(), Prefix: a.query.Get("prefix")}
	sort.Slice(bs, func(i, j int) bool { return bs[i].Name < bs[j].Name })
	for _, b := range bs {
		if !strings.HasPrefix(b.Name, out.Prefix) {
			continue
		}
		out.Buckets = append(out.Buckets, bucket{b.Name, b.CreationDate.UTC().Format("2006-01-02T15:04:05.000Z"), s.region()})
	}
	return a.writeXML(http.StatusOK, out)
}

func (s *Service) awsCreateBucket(a *s3req) error {
	b, _, err := s.readBody(a, 64<<10)
	if err != nil {
		return err
	}
	var tags core.Tags
	if len(bytes.TrimSpace(b)) > 0 {
		var cfg struct {
			LocationConstraint string   `xml:"LocationConstraint"`
			Tags               []xmlTag `xml:"Tags>Tag"`
		}
		if err := xml.Unmarshal(b, &cfg); err != nil {
			return awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed")
		}
		if lc := cfg.LocationConstraint; lc != "" && lc != s.region() && lc != signingRegion {
			return awsapi.Errorf(http.StatusBadRequest, "IllegalLocationConstraintException",
				"the %s location constraint is incompatible with this endpoint's region (%s)", lc, s.region())
		}
		for _, t := range cfg.Tags {
			if tags == nil {
				tags = core.Tags{}
			}
			tags[t.Key] = t.Value
		}
	}
	h := a.q.R.Header
	acl := h.Get("X-Amz-Acl")
	switch acl {
	case "", "private", "public-read", "public-read-write", "authenticated-read", "bucket-owner-full-control":
	default:
		return awsapi.Errorf(http.StatusBadRequest, "InvalidArgument", "unsupported canned ACL %q", acl)
	}
	cl, _ := s.cl()
	lock := strings.EqualFold(h.Get("X-Amz-Bucket-Object-Lock-Enabled"), "true")
	if err := cl.MakeBucket(a.ctx(), a.bucket, minio.MakeBucketOptions{Region: signingRegion, ObjectLocking: lock}); err != nil {
		return s3err(err)
	}
	s.forgetNames()
	m := bucketMeta{Name: a.bucket, Tags: tags}
	if o := h.Get("X-Amz-Object-Ownership"); o != "" {
		m.setConfig("ownershipControls", fmt.Sprintf(`<OwnershipControls xmlns="%s"><Rule><ObjectOwnership>%s</ObjectOwnership></Rule></OwnershipControls>`, s3NS, xmlEsc(o)))
	}
	if err := store.Put(s.env.Store, cBuckets, a.bucket, m); err != nil {
		return err
	}
	if acl == "public-read" || acl == "public-read-write" {
		if err := cl.SetBucketPolicy(a.ctx(), a.bucket, publicReadPolicy(a.bucket)); err != nil {
			return s3err(err)
		}
		s.forgetPolicy(a.bucket)
	}
	a.q.W.Header().Set("Location", "/"+a.bucket)
	a.q.W.Header().Set("Content-Length", "0")
	a.written = true
	a.q.W.WriteHeader(http.StatusOK)
	return nil
}

func (s *Service) awsGetBucketLocation(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	loc := s.region()
	if loc == "us-east-1" {
		loc = ""
	}
	return a.writeXML(http.StatusOK, struct {
		XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ LocationConstraint"`
		Value   string   `xml:",chardata"`
	}{Value: loc})
}

type xmlTag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

func (s *Service) awsGetBucketTagging(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	m := s.meta(a.bucket)
	if len(m.Tags) == 0 {
		return awsapi.Errorf(http.StatusNotFound, "NoSuchTagSet", "The TagSet does not exist")
	}
	out := struct {
		XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ Tagging"`
		Tags    []xmlTag `xml:"TagSet>Tag"`
	}{}
	keys := make([]string, 0, len(m.Tags))
	for k := range m.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Tags = append(out.Tags, xmlTag{k, m.Tags[k]})
	}
	return a.writeXML(http.StatusOK, out)
}

func (s *Service) awsPutBucketTagging(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	b, _, err := s.readBody(a, 64<<10)
	if err != nil {
		return err
	}
	var in struct {
		Tags []xmlTag `xml:"TagSet>Tag"`
	}
	if err := xml.Unmarshal(b, &in); err != nil {
		return awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed")
	}
	if len(in.Tags) > 50 {
		return awsapi.Errorf(http.StatusBadRequest, "InvalidTag", "a bucket can have at most 50 tags")
	}
	tags := core.Tags{}
	for _, t := range in.Tags {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			return awsapi.Errorf(http.StatusBadRequest, "InvalidTag", "invalid tag %q", t.Key)
		}
		if _, dup := tags[t.Key]; dup {
			return awsapi.Errorf(http.StatusBadRequest, "InvalidTag", "duplicate tag key %q", t.Key)
		}
		tags[t.Key] = t.Value
	}
	if err := s.updateMeta(a.bucket, func(m *bucketMeta) { m.Tags = tags }); err != nil {
		return err
	}
	return a.noContent()
}

func (s *Service) awsDeleteBucketTagging(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	if err := s.updateMeta(a.bucket, func(m *bucketMeta) { m.Tags = nil }); err != nil {
		return err
	}
	return a.noContent()
}

func (s *Service) updateMeta(bucket string, f func(m *bucketMeta)) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	m := s.meta(bucket)
	f(&m)
	return store.Put(s.env.Store, cBuckets, bucket, m)
}

func (s *Service) awsGetBucketWebsite(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	m := s.meta(a.bucket)
	if !m.Website {
		return awsapi.Errorf(http.StatusNotFound, "NoSuchWebsiteConfiguration", "The specified bucket does not have a website configuration")
	}
	type doc struct {
		Suffix string `xml:"Suffix"`
	}
	type errDoc struct {
		Key string `xml:"Key"`
	}
	out := struct {
		XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ WebsiteConfiguration"`
		Index   doc      `xml:"IndexDocument"`
		Error   *errDoc  `xml:"ErrorDocument,omitempty"`
	}{Index: doc{m.IndexDocument}}
	if m.ErrorDocument != "" {
		out.Error = &errDoc{m.ErrorDocument}
	}
	return a.writeXML(http.StatusOK, out)
}

func (s *Service) awsPutBucketWebsite(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	b, _, err := s.readBody(a, 256<<10)
	if err != nil {
		return err
	}
	var in struct {
		Index    *struct{ Suffix string } `xml:"IndexDocument"`
		Error    *struct{ Key string }    `xml:"ErrorDocument"`
		Redirect *struct{}                `xml:"RedirectAllRequestsTo"`
		Rules    *struct{}                `xml:"RoutingRules"`
	}
	if err := xml.Unmarshal(b, &in); err != nil {
		return awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed")
	}
	if in.Redirect != nil || in.Rules != nil {
		return errNotImpl("website redirects and routing rules")
	}
	if in.Index == nil || in.Index.Suffix == "" || strings.Contains(in.Index.Suffix, "/") {
		return awsapi.Errorf(http.StatusBadRequest, "InvalidArgument", "IndexDocument Suffix is required and may not contain a slash")
	}
	if err := s.updateMeta(a.bucket, func(m *bucketMeta) {
		m.Website, m.IndexDocument, m.ErrorDocument = true, in.Index.Suffix, ""
		if in.Error != nil {
			m.ErrorDocument = in.Error.Key
		}
	}); err != nil {
		return err
	}
	a.writeRaw(http.StatusOK, nil)
	return nil
}

func (s *Service) awsDeleteBucketWebsite(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	if err := s.updateMeta(a.bucket, func(m *bucketMeta) { m.Website = false }); err != nil {
		return err
	}
	return a.noContent()
}

// ACLs: HomeCloud buckets and objects are owned by the account; access is
// governed by IAM and bucket policies. ACLs read as owner FULL_CONTROL.
func (s *Service) awsGetACL(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	if a.key != "" {
		cl, _ := s.cl()
		if _, err := cl.StatObject(a.ctx(), a.bucket, a.key, minio.StatObjectOptions{VersionID: a.query.Get("versionId")}); err != nil {
			return s3err(err)
		}
	}
	o := s.owner()
	body := fmt.Sprintf(`%s<AccessControlPolicy xmlns="%s"><Owner><ID>%s</ID><DisplayName>%s</DisplayName></Owner><AccessControlList>`+
		`<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>%s</ID><DisplayName>%s</DisplayName></Grantee>`+
		`<Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`, xml.Header, s3NS, o.ID, xmlEsc(o.DisplayName), o.ID, xmlEsc(o.DisplayName))
	a.writeRaw(http.StatusOK, []byte(body))
	return nil
}

func (s *Service) awsPutBucketACL(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	if _, _, err := s.readBody(a, 256<<10); err != nil {
		return err
	}
	switch acl := a.q.R.Header.Get("X-Amz-Acl"); acl {
	case "public-read", "public-read-write":
		// The HomeCloud equivalent: a public-read bucket policy (unless one exists).
		cl, _ := s.cl()
		if p, err := cl.GetBucketPolicy(a.ctx(), a.bucket); err == nil && strings.TrimSpace(p) == "" {
			if err := cl.SetBucketPolicy(a.ctx(), a.bucket, publicReadPolicy(a.bucket)); err != nil {
				return s3err(err)
			}
			s.forgetPolicy(a.bucket)
		}
	case "", "private", "bucket-owner-full-control", "authenticated-read", "log-delivery-write":
	default:
		return awsapi.Errorf(http.StatusBadRequest, "InvalidArgument", "unsupported canned ACL %q", acl)
	}
	a.writeRaw(http.StatusOK, nil)
	return nil
}

func (s *Service) awsPutObjectACL(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	if _, _, err := s.readBody(a, 256<<10); err != nil {
		return err
	}
	cl, _ := s.cl()
	if _, err := cl.StatObject(a.ctx(), a.bucket, a.key, minio.StatObjectOptions{VersionID: a.query.Get("versionId")}); err != nil {
		return s3err(err)
	}
	a.writeRaw(http.StatusOK, nil)
	return nil
}

func (s *Service) awsPolicyStatus(a *s3req) error {
	text, err := s.bucketPolicy(a.ctx(), a.bucket)
	if err != nil {
		return err
	}
	if text == "" {
		return awsapi.Errorf(http.StatusNotFound, "NoSuchBucketPolicy", "The bucket policy does not exist")
	}
	return a.writeXML(http.StatusOK, struct {
		XMLName  xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ PolicyStatus"`
		IsPublic bool     `xml:"IsPublic"`
	}{IsPublic: publicPolicy(text)})
}

// Bucket configuration HomeCloud stores but does not act on (CORS for the
// MinIO endpoint, encryption at rest, logging, ...): documents round-trip.
var storedDefaults = map[string]struct {
	status int
	code   string
	body   string
}{
	"cors":              {404, "NoSuchCORSConfiguration", "The CORS configuration does not exist"},
	"encryption":        {404, "ServerSideEncryptionConfigurationNotFoundError", "The server side encryption configuration was not found"},
	"ownershipControls": {404, "OwnershipControlsNotFoundError", "The bucket ownership controls were not found"},
	"publicAccessBlock": {404, "NoSuchPublicAccessBlockConfiguration", "The public access block configuration was not found"},
	"logging":           {200, "", `<BucketLoggingStatus xmlns="` + s3NS + `"></BucketLoggingStatus>`},
	"requestPayment":    {200, "", `<RequestPaymentConfiguration xmlns="` + s3NS + `"><Payer>BucketOwner</Payer></RequestPaymentConfiguration>`},
	"accelerate":        {200, "", `<AccelerateConfiguration xmlns="` + s3NS + `"></AccelerateConfiguration>`},
}

func (a *s3req) sub() string {
	for _, s := range bucketSubs {
		if _, ok := a.query[s.sub]; ok {
			return s.sub
		}
	}
	return ""
}

func (m *bucketMeta) setConfig(sub, doc string) {
	if m.Config == nil {
		m.Config = map[string]string{}
	}
	if doc == "" {
		delete(m.Config, sub)
	} else {
		m.Config[sub] = doc
	}
}

func (s *Service) storedGet(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	sub := a.sub()
	if doc := s.meta(a.bucket).Config[sub]; doc != "" {
		a.writeRaw(http.StatusOK, []byte(xml.Header+doc))
		return nil
	}
	d := storedDefaults[sub]
	if d.code != "" {
		return awsapi.Errorf(d.status, d.code, "%s", d.body)
	}
	a.writeRaw(http.StatusOK, []byte(xml.Header+d.body))
	return nil
}

func (s *Service) storedPut(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	b, _, err := s.readBody(a, 256<<10)
	if err != nil {
		return err
	}
	b = bytes.TrimSpace(b)
	if len(b) == 0 || !wellFormed(b) {
		return awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed")
	}
	if i := bytes.Index(b, []byte("?>")); bytes.HasPrefix(b, []byte("<?xml")) && i > 0 {
		b = bytes.TrimSpace(b[i+2:])
	}
	sub := a.sub()
	if err := s.updateMeta(a.bucket, func(m *bucketMeta) { m.setConfig(sub, string(b)) }); err != nil {
		return err
	}
	a.writeRaw(http.StatusOK, nil)
	return nil
}

func (s *Service) storedDelete(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	sub := a.sub()
	if err := s.updateMeta(a.bucket, func(m *bucketMeta) { m.setConfig(sub, "") }); err != nil {
		return err
	}
	return a.noContent()
}

func wellFormed(b []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(b))
	root := false
	for {
		t, err := d.Token()
		if err == io.EOF {
			return root
		}
		if err != nil {
			return false
		}
		if _, ok := t.(xml.StartElement); ok {
			root = true
		}
	}
}

func (s *Service) emptyConfigList(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	if a.query.Get("id") != "" {
		return awsapi.Errorf(http.StatusNotFound, "NoSuchConfiguration", "The specified configuration does not exist")
	}
	root := map[string]string{
		"ListBucketAnalyticsConfigurations":          "ListBucketAnalyticsConfigurationResult",
		"ListBucketMetricsConfigurations":            "ListMetricsConfigurationsResult",
		"ListBucketInventoryConfigurations":          "ListInventoryConfigurationsResult",
		"ListBucketIntelligentTieringConfigurations": "ListBucketIntelligentTieringConfigurationsOutput",
	}[a.op.name]
	a.writeRaw(http.StatusOK, []byte(fmt.Sprintf(`%s<%s xmlns="%s"><IsTruncated>false</IsTruncated></%s>`, xml.Header, root, s3NS, root)))
	return nil
}
