// Package s3 implements object storage on a HomeCloud-managed MinIO server.
// This package adds the management API, the console file browser, presigned
// URLs, static website hosting and the AWS S3 endpoint on the HomeCloud API
// port (aws.go), which authorizes requests with IAM and proxies them to MinIO.
package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

const (
	containerName = "homecloud-s3"
	dataVolume    = "homecloud-s3-data"
	// MinIO no longer publishes free images on Docker Hub or quay.io;
	// Chainguard builds the same server from source (amd64 and arm64).
	minioImage    = "cgr.dev/chainguard/minio:latest"
	rootSecret    = "homecloud/s3/root"
	cBuckets      = "s3_buckets"
	signingRegion = "us-east-1"
)

// bucketMeta is HomeCloud-side configuration MinIO does not hold.
type bucketMeta struct {
	Name          string    `json:"name"`
	Website       bool      `json:"website"`
	IndexDocument string    `json:"index_document"`
	ErrorDocument string    `json:"error_document"`
	Tags          core.Tags `json:"tags,omitempty"`
	// Config holds bucket configuration documents set through the AWS API that
	// HomeCloud stores without acting on (cors, encryption, logging, ...), by subresource.
	Config map[string]string `json:"aws_config,omitempty"`
}

type Service struct {
	env     *svc.Env
	secrets *secrets.Service
	mu      sync.RWMutex
	client  *minio.Client
	signer  *minio.Client // same credentials, MinIO's host address: presigned URLs are served through /_s3/
	direct  string        // host:port HomeCloud reaches MinIO at
	user    string
	pass    string
	status  string

	metaMu    sync.Mutex // serializes read-modify-write of bucketMeta
	policies  policyCache
	names     nameCache
	nativeOps map[string]func(*s3req) error // AWS operations answered by HomeCloud
}

func New(env *svc.Env, sec *secrets.Service) *Service {
	s := &Service{env: env, secrets: sec, status: "starting"}
	httpx.RegisterPolicyProvider("s3", s.policyProvider)
	return s
}

// Endpoint is MinIO's own address: on the host it runs on by default. Remote
// clients use the API endpoint (the AWS S3 protocol, authorized by IAM), or
// publish MinIO with --s3-bind 0.0.0.0.
func (s *Service) Endpoint() string {
	host := s.env.Cfg.PublicHost
	if ip := net.ParseIP(s.env.Cfg.ServiceBindAddr()); ip != nil && ip.IsLoopback() {
		host = s.env.Cfg.ServiceBindAddr()
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.env.Cfg.S3Port))
}

// Start ensures the MinIO container runs and waits until it answers.
func (s *Service) Start(ctx context.Context, vpcs []vpc.VPC) error {
	creds, _, err := s.secrets.Value(rootSecret, "", "")
	if err != nil {
		creds = fmt.Sprintf(`{"user":"hc-%s","password":"%s"}`, strings.ToLower(core.RandHex(8)), core.NewSecret(32))
		if _, err := s.secrets.Put(rootSecret, creds, "Root credentials of the HomeCloud S3 (MinIO) server", "s3"); err != nil {
			return err
		}
	}
	var c struct{ User, Password string }
	if err := json.Unmarshal([]byte(creds), &c); err != nil {
		return fmt.Errorf("s3 root secret is corrupt: %w", err)
	}
	s.user, s.pass = c.User, c.Password

	d := s.env.Docker
	bind := s.env.Cfg.ServiceBindAddr()
	if d.State(containerName) != "missing" && mismatched(d, bind, "9000/tcp", "9001/tcp") {
		// Published on other addresses (all of them, before this was configurable):
		// recreate it; the data lives in a named volume.
		log.Printf("s3: recreating MinIO to publish its ports on %s only", bind)
		if err := d.Remove(containerName); err != nil {
			return fmt.Errorf("recreate MinIO: %w", err)
		}
	}
	switch d.State(containerName) {
	case "running":
	case "missing":
		if err := d.CreateVolume(dataVolume, runtime.Labels("s3", "data", nil)); err != nil {
			return err
		}
		_, err := d.Run(ctx, runtime.RunSpec{
			Name:  containerName,
			Image: minioImage,
			Cmd:   []string{"server", "/data", "--console-address", ":9001"},
			// Root, as before: existing data volumes were written by a root MinIO.
			User:    "0",
			Env:     map[string]string{"MINIO_ROOT_USER": s.user, "MINIO_ROOT_PASSWORD": s.pass},
			Labels:  runtime.Labels("s3", "server", nil),
			Ports:   []runtime.Port{{ContainerPort: 9000, HostPort: s.env.Cfg.S3Port, HostIP: bind}, {ContainerPort: 9001, HostPort: s.env.Cfg.S3ConsolePort, HostIP: bind}},
			Mounts:  []runtime.Mount{{Volume: dataVolume, Target: "/data"}},
			Restart: "unless-stopped",
			Start:   true,
		})
		if err != nil {
			return fmt.Errorf("start MinIO: %w", err)
		}
	default:
		if err := d.Start(containerName); err != nil {
			return fmt.Errorf("start MinIO: %w", err)
		}
	}
	for _, v := range vpcs {
		s.ConnectNetwork(v)
	}
	dial := net.JoinHostPort(s.env.Cfg.ServiceDialHost(), strconv.Itoa(s.env.Cfg.S3Port))
	return s.connect(ctx, dial, dial)
}

// UseMinIO points the service at an already running MinIO server (host:port)
// instead of the managed container. Tests use it.
func (s *Service) UseMinIO(ctx context.Context, hostport, user, pass string) error {
	s.user, s.pass = user, pass
	return s.connect(ctx, hostport, hostport)
}

func (s *Service) connect(ctx context.Context, hostport, public string) error {
	opts := &minio.Options{Creds: credentials.NewStaticV4(s.user, s.pass, ""), Region: signingRegion}
	cl, err := minio.New(hostport, opts)
	if err != nil {
		return err
	}
	signer, err := minio.New(public, opts)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err = cl.ListBuckets(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("MinIO did not become ready: %w", err)
		}
		time.Sleep(time.Second)
	}
	s.mu.Lock()
	s.client, s.signer, s.direct, s.status = cl, signer, hostport, "available"
	s.mu.Unlock()
	return nil
}

// ConnectNetwork makes S3 reachable inside a VPC as s3.internal, at the VPC's
// reserved S3 address (a dynamic address could collide with reserved ones).
func (s *Service) ConnectNetwork(v vpc.VPC) {
	want := vpc.S3Address(v.CIDR)
	if c, err := s.env.Docker.Inspect(containerName); err == nil && c.NetworkSettings != nil {
		if ep, ok := c.NetworkSettings.Networks[v.Network]; ok {
			if ep.IPAddress == want {
				return
			}
			_ = s.env.Docker.C.DisconnectNetwork(v.Network, docker.NetworkConnectionOptions{Container: containerName, Force: true})
		}
	}
	network := v.Network
	if err := s.env.Docker.ConnectIP(network, containerName, want, "s3.internal"); err != nil {
		log.Printf("s3: connect to %s: %v", network, err)
	}
}

func (s *Service) cl() (*minio.Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.client == nil {
		return nil, core.Errf(http.StatusServiceUnavailable, "ServiceUnavailable", "S3 is %s", s.status)
	}
	return s.client, nil
}

// Client exposes the MinIO client to other services (e.g. Lambda code storage).
func (s *Service) Client() (*minio.Client, error) { return s.cl() }

func s3err(err error) error {
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		st := er.StatusCode
		if st == 0 {
			st = http.StatusBadRequest
		}
		return core.Errf(st, er.Code, "%s", er.Message)
	}
	return err
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	b := httpx.Res("arn:aws:s3:::{bucket}")
	r.Handle("GET /api/v1/s3/status", "s3:ListAllMyBuckets", s.statusRoute)
	// Deliberately not a Get* action: read-only policies must not grant MinIO root keys.
	r.Handle("GET /api/v1/s3/credentials", "s3:AdministerServiceCredentials", s.creds)
	r.Handle("GET /api/v1/s3/buckets", "s3:ListAllMyBuckets", s.listBuckets)
	r.Handle("POST /api/v1/s3/buckets", "s3:CreateBucket", s.createBucket)
	r.Handle("GET /api/v1/s3/buckets/{bucket}", "s3:GetBucketLocation", s.getBucket, b)
	r.Handle("DELETE /api/v1/s3/buckets/{bucket}", "s3:DeleteBucket", s.deleteBucket, b)
	r.Handle("PUT /api/v1/s3/buckets/{bucket}/versioning", "s3:PutBucketVersioning", s.putVersioning, b)
	r.Handle("PUT /api/v1/s3/buckets/{bucket}/access", "s3:PutBucketPolicy", s.putAccess, b)
	r.Handle("PUT /api/v1/s3/buckets/{bucket}/policy", "s3:PutBucketPolicy", s.putPolicy, b)
	r.Handle("PUT /api/v1/s3/buckets/{bucket}/website", "s3:PutBucketWebsite", s.putWebsite, b)
	r.Handle("PUT /api/v1/s3/buckets/{bucket}/lifecycle", "s3:PutLifecycleConfiguration", s.putLifecycle, b)
	r.Handle("PUT /api/v1/s3/buckets/{bucket}/tags", "s3:PutBucketTagging", s.putTags, b)
	r.Handle("GET /api/v1/s3/buckets/{bucket}/objects", "s3:ListBucket", s.listObjects, b)
	r.Handle("PUT /api/v1/s3/buckets/{bucket}/object", "s3:PutObject", s.putObject, b)
	r.Handle("GET /api/v1/s3/buckets/{bucket}/object", "s3:GetObject", s.getObject, b)
	r.Handle("GET /api/v1/s3/buckets/{bucket}/object/meta", "s3:GetObject", s.headObject, b)
	r.Handle("DELETE /api/v1/s3/buckets/{bucket}/object", "s3:DeleteObject", s.deleteObject, b)
	r.Handle("POST /api/v1/s3/buckets/{bucket}/folders", "s3:PutObject", s.createFolder, b)
	r.Handle("POST /api/v1/s3/buckets/{bucket}/copy", "s3:PutObject", s.copyObject, b)
	r.Handle("POST /api/v1/s3/buckets/{bucket}/presign", "s3:GetObject", s.presign, b)
	r.Handle("GET /website/{bucket}/{key...}", "", s.website, httpx.Public())
}

func (s *Service) statusRoute(c *httpx.Ctx) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{"status": s.status, "endpoint": s.Endpoint(), "region": signingRegion,
		"console_url": fmt.Sprintf("http://%s:%d", s.env.Cfg.PublicHost, s.env.Cfg.S3ConsolePort)}, nil
}

func (s *Service) creds(c *httpx.Ctx) (any, error) {
	return map[string]string{"endpoint": s.Endpoint(), "region": signingRegion, "access_key_id": s.user, "secret_access_key": s.pass}, nil
}

func (s *Service) meta(bucket string) bucketMeta {
	m, err := store.Get[bucketMeta](s.env.Store, cBuckets, bucket)
	if err != nil {
		m = bucketMeta{Name: bucket}
	}
	return m
}

func (s *Service) listBuckets(c *httpx.Ctx) (any, error) {
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	bs, err := cl.ListBuckets(c.R.Context())
	if err != nil {
		return nil, s3err(err)
	}
	out := []map[string]any{}
	for _, b := range bs {
		if !c.P.Can("s3:ListBucket", "arn:aws:s3:::"+b.Name) && !c.P.Can("s3:ListAllMyBuckets", "*") {
			continue
		}
		m := s.meta(b.Name)
		out = append(out, map[string]any{"name": b.Name, "created_at": b.CreationDate, "region": s.env.Cfg.Region,
			"arn": "arn:aws:s3:::" + b.Name, "website": m.Website, "public": s.isPublic(c.R.Context(), cl, b.Name)})
	}
	return out, nil
}

func validBucket(name string) error {
	if len(name) < 3 || len(name) > 63 {
		return core.BadRequest("bucket names must be 3-63 characters")
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || ((r == '-' || r == '.') && i > 0 && i < len(name)-1)
		if !ok {
			return core.BadRequest("bucket names may contain only lowercase letters, digits, dots and hyphens, and must start and end with a letter or digit")
		}
	}
	return nil
}

func (s *Service) createBucket(c *httpx.Ctx) (any, error) {
	var in struct {
		Name       string    `json:"name"`
		Versioning bool      `json:"versioning"`
		Public     bool      `json:"public"`
		ObjectLock bool      `json:"object_lock"`
		Tags       core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := validBucket(in.Name); err != nil {
		return nil, err
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	if err := cl.MakeBucket(ctx, in.Name, minio.MakeBucketOptions{Region: signingRegion, ObjectLocking: in.ObjectLock}); err != nil {
		return nil, s3err(err)
	}
	// Configure the new bucket; if any step fails, remove it rather than leave it half-made.
	err = func() error {
		if in.Versioning {
			if err := cl.SetBucketVersioning(ctx, in.Name, minio.BucketVersioningConfiguration{Status: "Enabled"}); err != nil {
				return s3err(err)
			}
		}
		if in.Public {
			if err := cl.SetBucketPolicy(ctx, in.Name, publicReadPolicy(in.Name)); err != nil {
				return s3err(err)
			}
		}
		return store.Put(s.env.Store, cBuckets, in.Name, bucketMeta{Name: in.Name, Tags: in.Tags})
	}()
	s.forgetNames()
	s.forgetPolicy(in.Name)
	if err != nil {
		_ = cl.RemoveBucketWithOptions(context.Background(), in.Name, minio.RemoveBucketOptions{ForceDelete: true})
		return nil, err
	}
	return map[string]any{"name": in.Name, "arn": "arn:aws:s3:::" + in.Name}, nil
}

func publicReadPolicy(bucket string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, bucket)
}

func (s *Service) isPublic(ctx context.Context, cl *minio.Client, bucket string) bool {
	p, err := cl.GetBucketPolicy(ctx, bucket)
	return err == nil && strings.Contains(p, `"*"`) && strings.Contains(p, "s3:GetObject")
}

func (s *Service) getBucket(c *httpx.Ctx) (any, error) {
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	name := c.Param("bucket")
	ok, err := cl.BucketExists(ctx, name)
	if err != nil {
		return nil, s3err(err)
	}
	if !ok {
		return nil, core.Errf(http.StatusNotFound, "NoSuchBucket", "bucket %q does not exist", name)
	}
	ver, _ := cl.GetBucketVersioning(ctx, name)
	policy, _ := cl.GetBucketPolicy(ctx, name)
	var count, size int64
	truncated := false
	for o := range cl.ListObjects(ctx, name, minio.ListObjectsOptions{Recursive: true}) {
		if o.Err != nil {
			break
		}
		count++
		size += o.Size
		if count >= 100000 {
			truncated = true
			break
		}
	}
	var rules []map[string]any
	if lc, err := cl.GetBucketLifecycle(ctx, name); err == nil && lc != nil {
		for _, r := range lc.Rules {
			rules = append(rules, map[string]any{"id": r.ID, "prefix": r.RuleFilter.Prefix, "expiration_days": int(r.Expiration.Days), "status": r.Status})
		}
	}
	m := s.meta(name)
	var created time.Time
	if bs, err := cl.ListBuckets(ctx); err == nil {
		for _, b := range bs {
			if b.Name == name {
				created = b.CreationDate
			}
		}
	}
	return map[string]any{
		"name": name, "arn": "arn:aws:s3:::" + name, "region": s.env.Cfg.Region, "versioning": ver.Status, "created_at": created,
		"public": s.isPublic(ctx, cl, name), "policy": policy, "object_count": count, "size_bytes": size, "stats_truncated": truncated,
		"website": m.Website, "index_document": m.IndexDocument, "error_document": m.ErrorDocument, "lifecycle_rules": rules, "tags": m.Tags,
		"website_url": fmt.Sprintf("%s/website/%s/", s.env.Cfg.PublicBase(), name),
	}, nil
}

func (s *Service) deleteBucket(c *httpx.Ctx) (any, error) {
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	name := c.Param("bucket")
	if err := cl.RemoveBucketWithOptions(c.R.Context(), name, minio.RemoveBucketOptions{ForceDelete: c.Query("force") == "true"}); err != nil {
		return nil, s3err(err)
	}
	_ = store.Delete(s.env.Store, cBuckets, name)
	s.forgetNames()
	s.forgetPolicy(name)
	return nil, nil
}

func (s *Service) putVersioning(c *httpx.Ctx) (any, error) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	st := "Suspended"
	if in.Enabled {
		st = "Enabled"
	}
	return nil, s3err(cl.SetBucketVersioning(c.R.Context(), c.Param("bucket"), minio.BucketVersioningConfiguration{Status: st}))
}

func (s *Service) putAccess(c *httpx.Ctx) (any, error) {
	var in struct {
		Public bool `json:"public"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	p := ""
	if in.Public {
		p = publicReadPolicy(c.Param("bucket"))
	}
	defer s.forgetPolicy(c.Param("bucket"))
	return nil, s3err(cl.SetBucketPolicy(c.R.Context(), c.Param("bucket"), p))
}

func (s *Service) putPolicy(c *httpx.Ctx) (any, error) {
	var in struct {
		Policy string `json:"policy"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Policy != "" && !json.Valid([]byte(in.Policy)) {
		return nil, core.BadRequest("policy must be a JSON document")
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	defer s.forgetPolicy(c.Param("bucket"))
	return nil, s3err(cl.SetBucketPolicy(c.R.Context(), c.Param("bucket"), in.Policy))
}

func (s *Service) putWebsite(c *httpx.Ctx) (any, error) {
	var in struct {
		Enabled       bool   `json:"enabled"`
		IndexDocument string `json:"index_document"`
		ErrorDocument string `json:"error_document"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	name := c.Param("bucket")
	if err := s.mustExist(c, name); err != nil {
		return nil, err
	}
	m := s.meta(name)
	m.Website, m.IndexDocument, m.ErrorDocument = in.Enabled, in.IndexDocument, in.ErrorDocument
	if m.IndexDocument == "" {
		m.IndexDocument = "index.html"
	}
	return m, store.Put(s.env.Store, cBuckets, name, m)
}

func (s *Service) putLifecycle(c *httpx.Ctx) (any, error) {
	var in struct {
		Rules []struct {
			ID             string `json:"id"`
			Prefix         string `json:"prefix"`
			ExpirationDays int    `json:"expiration_days"`
		} `json:"rules"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	cfg := lifecycle.NewConfiguration()
	for i, r := range in.Rules {
		if r.ExpirationDays < 1 {
			return nil, core.BadRequest("rule %d: expiration_days must be at least 1", i)
		}
		id := r.ID
		if id == "" {
			id = fmt.Sprintf("rule-%d", i+1)
		}
		cfg.Rules = append(cfg.Rules, lifecycle.Rule{ID: id, Status: "Enabled", RuleFilter: lifecycle.Filter{Prefix: r.Prefix},
			Expiration: lifecycle.Expiration{Days: lifecycle.ExpirationDays(r.ExpirationDays)}})
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	return nil, s3err(cl.SetBucketLifecycle(c.R.Context(), c.Param("bucket"), cfg))
}

func (s *Service) listObjects(c *httpx.Ctx) (any, error) {
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	prefix := c.Query("prefix")
	recursive := c.Query("recursive") == "true"
	limit := c.QueryInt("limit", 1000)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	objects := []map[string]any{}
	prefixes := []string{}
	truncated := false
	last := ""
	ctx, cancel := context.WithCancel(c.R.Context())
	defer cancel()
	for o := range cl.ListObjects(ctx, c.Param("bucket"), minio.ListObjectsOptions{Prefix: prefix, Recursive: recursive,
		WithVersions: c.Query("versions") == "true", StartAfter: c.Query("start_after")}) {
		if o.Err != nil {
			return nil, s3err(o.Err)
		}
		if len(objects)+len(prefixes) >= limit {
			truncated = true
			break
		}
		last = o.Key
		if strings.HasSuffix(o.Key, "/") && o.Size == 0 && !recursive {
			if o.Key != prefix {
				prefixes = append(prefixes, o.Key)
			}
			continue
		}
		objects = append(objects, map[string]any{"key": o.Key, "size": o.Size, "last_modified": o.LastModified, "etag": o.ETag,
			"storage_class": "STANDARD", "version_id": o.VersionID, "is_latest": o.IsLatest, "delete_marker": o.IsDeleteMarker})
	}
	out := map[string]any{"bucket": c.Param("bucket"), "prefix": prefix, "prefixes": prefixes, "objects": objects, "truncated": truncated}
	if truncated {
		out["next_start_after"] = last // pass as start_after to get the next page
	}
	return out, nil
}

func objectKey(c *httpx.Ctx) (string, error) {
	k := c.Query("key")
	if k == "" || len(k) > 1024 {
		return "", core.BadRequest("key query parameter is required (max 1024 bytes)")
	}
	return k, nil
}

func (s *Service) putObject(c *httpx.Ctx) (any, error) {
	key, err := objectKey(c)
	if err != nil {
		return nil, err
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	ct := c.R.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		if t := mime.TypeByExtension(path.Ext(key)); t != "" {
			ct = t
		}
	}
	meta := map[string]string{}
	for k, v := range c.R.Header {
		if m, ok := strings.CutPrefix(strings.ToLower(k), "x-hc-meta-"); ok && len(v) > 0 {
			meta[m] = v[0]
		}
	}
	info, err := cl.PutObject(c.R.Context(), c.Param("bucket"), key, c.R.Body, c.R.ContentLength, minio.PutObjectOptions{ContentType: ct, UserMetadata: meta})
	if err != nil {
		return nil, s3err(err)
	}
	return map[string]any{"bucket": info.Bucket, "key": info.Key, "etag": info.ETag, "size": info.Size, "version_id": info.VersionID}, nil
}

func (s *Service) getObject(c *httpx.Ctx) (any, error) {
	key, err := objectKey(c)
	if err != nil {
		return nil, err
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	return nil, s.stream(c, cl, c.Param("bucket"), key, c.Query("version_id"), c.Query("inline") != "true", http.StatusOK)
}

func (s *Service) stream(c *httpx.Ctx, cl *minio.Client, bucket, key, version string, attachment bool, status int) error {
	obj, err := cl.GetObject(c.R.Context(), bucket, key, minio.GetObjectOptions{VersionID: version})
	if err != nil {
		return s3err(err)
	}
	defer obj.Close()
	st, err := obj.Stat()
	if err != nil {
		return s3err(err)
	}
	h := c.W.Header()
	h.Set("Content-Type", st.ContentType)
	h.Set("Content-Length", strconv.FormatInt(st.Size, 10))
	h.Set("ETag", st.ETag)
	h.Set("Last-Modified", st.LastModified.UTC().Format(http.TimeFormat))
	if attachment {
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(key)}))
	}
	c.MarkWritten()
	c.W.WriteHeader(status)
	_, _ = io.Copy(c.W, obj)
	return nil
}

func (s *Service) headObject(c *httpx.Ctx) (any, error) {
	key, err := objectKey(c)
	if err != nil {
		return nil, err
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	st, err := cl.StatObject(c.R.Context(), c.Param("bucket"), key, minio.StatObjectOptions{VersionID: c.Query("version_id")})
	if err != nil {
		return nil, s3err(err)
	}
	return map[string]any{"key": st.Key, "size": st.Size, "content_type": st.ContentType, "etag": st.ETag, "last_modified": st.LastModified,
		"version_id": st.VersionID, "metadata": st.UserMetadata, "arn": "arn:aws:s3:::" + c.Param("bucket") + "/" + st.Key,
		"url": fmt.Sprintf("%s/%s/%s", s.Endpoint(), url.PathEscape(c.Param("bucket")), escapeKey(st.Key))}, nil
}

// escapeKey URL-encodes each segment of an object key, keeping the slashes.
func escapeKey(k string) string {
	parts := strings.Split(k, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (s *Service) putTags(c *httpx.Ctx) (any, error) {
	var in struct {
		Tags core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.mustExist(c, c.Param("bucket")); err != nil {
		return nil, err
	}
	m := s.meta(c.Param("bucket"))
	m.Tags = in.Tags
	return m, store.Put(s.env.Store, cBuckets, m.Name, m)
}

func (s *Service) deleteObject(c *httpx.Ctx) (any, error) {
	key, err := objectKey(c)
	if err != nil {
		return nil, err
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	ctx := c.R.Context()
	bucket := c.Param("bucket")
	if c.Query("recursive") == "true" {
		// permanent=true also removes old versions and delete markers, so the
		// prefix disappears even from versioned buckets.
		permanent := c.Query("permanent") == "true"
		lctx, cancel := context.WithCancel(ctx)
		defer cancel()
		objs := make(chan minio.ObjectInfo)
		var listErr error
		go func() {
			defer close(objs)
			for o := range cl.ListObjects(lctx, bucket, minio.ListObjectsOptions{Prefix: key, Recursive: true, WithVersions: permanent}) {
				if o.Err != nil {
					listErr = o.Err
					return
				}
				select {
				case objs <- o:
				case <-lctx.Done():
					return
				}
			}
		}()
		for e := range cl.RemoveObjects(lctx, bucket, objs, minio.RemoveObjectsOptions{}) {
			if e.Err != nil {
				cancel()
				for range objs { // let the lister finish
				}
				return nil, s3err(e.Err)
			}
		}
		if listErr != nil {
			return nil, s3err(listErr)
		}
		return map[string]any{"deleted_prefix": key}, nil
	}
	return nil, s3err(cl.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{VersionID: c.Query("version_id")}))
}

func (s *Service) createFolder(c *httpx.Ctx) (any, error) {
	var in struct {
		Key string `json:"key"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Key == "" {
		return nil, core.BadRequest("key is required")
	}
	if !strings.HasSuffix(in.Key, "/") {
		in.Key += "/"
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	_, err = cl.PutObject(c.R.Context(), c.Param("bucket"), in.Key, strings.NewReader(""), 0, minio.PutObjectOptions{})
	return map[string]string{"key": in.Key}, s3err(err)
}

func (s *Service) copyObject(c *httpx.Ctx) (any, error) {
	var in struct {
		SourceBucket string `json:"source_bucket"`
		SourceKey    string `json:"source_key"`
		Key          string `json:"key"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.SourceBucket == "" {
		in.SourceBucket = c.Param("bucket")
	}
	if err := c.Authorize("s3:GetObject", "arn:aws:s3:::"+in.SourceBucket); err != nil {
		return nil, err
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	info, err := cl.CopyObject(c.R.Context(), minio.CopyDestOptions{Bucket: c.Param("bucket"), Object: in.Key},
		minio.CopySrcOptions{Bucket: in.SourceBucket, Object: in.SourceKey})
	if err != nil {
		return nil, s3err(err)
	}
	return map[string]any{"key": info.Key, "etag": info.ETag}, nil
}

func (s *Service) presign(c *httpx.Ctx) (any, error) {
	var in struct {
		Key            string `json:"key"`
		Method         string `json:"method"`
		ExpiresSeconds int    `json:"expires_seconds"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Key == "" {
		return nil, core.BadRequest("key is required")
	}
	if in.ExpiresSeconds <= 0 {
		in.ExpiresSeconds = 3600
	}
	if in.ExpiresSeconds > 7*24*3600 {
		return nil, core.BadRequest("expires_seconds may not exceed 7 days")
	}
	s.mu.RLock()
	signer := s.signer
	s.mu.RUnlock()
	if signer == nil {
		return nil, core.Errf(http.StatusServiceUnavailable, "ServiceUnavailable", "S3 is not ready")
	}
	exp := time.Duration(in.ExpiresSeconds) * time.Second
	var err error
	var u fmt.Stringer
	switch strings.ToUpper(in.Method) {
	case "", "GET":
		u, err = signer.PresignedGetObject(c.R.Context(), c.Param("bucket"), in.Key, exp, nil)
	case "PUT":
		if err := c.Authorize("s3:PutObject", "arn:aws:s3:::"+c.Param("bucket")); err != nil {
			return nil, err
		}
		u, err = signer.PresignedPutObject(c.R.Context(), c.Param("bucket"), in.Key, exp)
	default:
		return nil, core.BadRequest("method must be GET or PUT")
	}
	if err != nil {
		return nil, s3err(err)
	}
	return map[string]any{"url": s.publicPresigned(u.String()), "expires_at": time.Now().Add(exp).UTC()}, nil
}

func (s *Service) mustExist(c *httpx.Ctx, bucket string) error {
	cl, err := s.cl()
	if err != nil {
		return err
	}
	ok, err := cl.BucketExists(c.R.Context(), bucket)
	if err != nil {
		return s3err(err)
	}
	if !ok {
		return core.Errf(http.StatusNotFound, "NoSuchBucket", "bucket %q does not exist", bucket)
	}
	return nil
}

// website serves a bucket with website hosting enabled, without authentication.
// As in AWS, the bucket must also allow public reads.
func (s *Service) website(c *httpx.Ctx) (any, error) {
	bucket, key := c.Param("bucket"), c.Param("key")
	m := s.meta(bucket)
	if !m.Website {
		return nil, core.Errf(http.StatusNotFound, "NoSuchWebsiteConfiguration", "bucket %q does not host a website", bucket)
	}
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	// The site is served with the server's own storage credentials, so each key
	// must be one the bucket policy lets an anonymous caller read: a policy that
	// merely mentions "*" and s3:GetObject (for one prefix, with a Deny or a
	// condition) does not make the whole bucket public.
	policy, _ := s.bucketPolicy(c.R.Context(), bucket)
	acc := httpx.Access{Policy: policy, Keys: httpx.RequestContext(c.R)}
	public := func(k string) bool {
		return policy != "" && httpx.PermitsAnonymous("s3:GetObject", "arn:aws:s3:::"+bucket+"/"+k, acc)
	}
	denied := core.Errf(http.StatusForbidden, "AccessDenied", "bucket %q hosts a website but does not allow public reads of this object", bucket)
	if key == "" || strings.HasSuffix(key, "/") {
		key += m.IndexDocument
	}
	if !public(key) {
		return nil, denied
	}
	if _, err := cl.StatObject(c.R.Context(), bucket, key, minio.StatObjectOptions{}); err != nil {
		if public(key + "/" + m.IndexDocument) {
			if _, err2 := cl.StatObject(c.R.Context(), bucket, key+"/"+m.IndexDocument, minio.StatObjectOptions{}); err2 == nil {
				http.Redirect(c.W, c.R, c.R.URL.Path+"/", http.StatusFound)
				c.MarkWritten()
				return nil, nil
			}
		}
		if m.ErrorDocument != "" && public(m.ErrorDocument) {
			return nil, s.stream(c, cl, bucket, m.ErrorDocument, "", false, http.StatusNotFound)
		}
		return nil, core.Errf(http.StatusNotFound, "NoSuchKey", "%s not found", key)
	}
	return nil, s.stream(c, cl, bucket, key, "", false, http.StatusOK)
}

// mismatched reports whether MinIO is published on addresses other than bind.
// If Docker can't inspect it right now, the container is left as it is.
func mismatched(d *runtime.Docker, bind string, ports ...string) bool {
	ok, err := d.BoundTo(containerName, bind, ports...)
	if err != nil {
		log.Printf("s3: inspect MinIO: %v (keeping the container)", err)
		return false
	}
	return !ok
}
