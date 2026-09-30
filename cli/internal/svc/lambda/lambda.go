// Package lambda implements serverless functions on AWS's official Lambda base
// images. Each function version has a pool of execution environments
// (containers running the Runtime Interface Emulator); an environment handles
// one invocation at a time and stays warm between invocations, and the pool
// scales out up to the function's concurrency limit. Functions can assume an
// execution role, have published versions and aliases, layers, asynchronous
// invocation with retries and destinations, and container-image packages.
// Invocations are logged to CloudWatch Logs and measured in CloudWatch Metrics.
//
// The same operations are served by the native API (/api/v1/lambda) and the
// AWS Lambda REST API (aws.go).
package lambda

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

const (
	cFunctions   = "lambda_functions"
	cVersions    = "lambda_versions"    // "<fn>:<n>" -> Function snapshot
	cAliases     = "lambda_aliases"     // "<fn>:<alias>" -> Alias
	cInvokeCfgs  = "lambda_invoke_cfgs" // "<fn>:<qualifier>" -> EventInvokeConfig
	maxCodeBytes = 50 << 20
	idleTimeout  = 15 * time.Minute
	metricsNS    = "HC/Lambda"
	latest       = "$LATEST"
)

// FunctionURL is a function's dedicated HTTP endpoint.
type FunctionURL struct {
	Enabled      bool       `json:"enabled"`
	AuthType     string     `json:"auth_type"` // NONE | HC_IAM (AWS_IAM in the AWS API)
	URL          string     `json:"url,omitempty"`
	Qualifier    string     `json:"qualifier,omitempty"`
	Cors         *URLCors   `json:"cors,omitempty"`
	InvokeMode   string     `json:"invoke_mode,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	LastModified *time.Time `json:"last_modified,omitempty"`
}

// URLCors is a function URL's CORS configuration (AWS shape).
type URLCors struct {
	AllowCredentials bool     `json:"AllowCredentials,omitempty"`
	AllowHeaders     []string `json:"AllowHeaders,omitempty"`
	AllowMethods     []string `json:"AllowMethods,omitempty"`
	AllowOrigins     []string `json:"AllowOrigins,omitempty"`
	ExposeHeaders    []string `json:"ExposeHeaders,omitempty"`
	MaxAge           int      `json:"MaxAge,omitempty"`
}

// ImageConfig overrides a container image's entrypoint, command and working directory.
type ImageConfig struct {
	EntryPoint       []string `json:"EntryPoint,omitempty"`
	Command          []string `json:"Command,omitempty"`
	WorkingDirectory string   `json:"WorkingDirectory,omitempty"`
}

// Function is a function's $LATEST configuration, or (in cVersions) an
// immutable published version of it.
type Function struct {
	Name                   string                       `json:"name"`
	ARN                    string                       `json:"arn"`
	Runtime                string                       `json:"runtime"`
	Handler                string                       `json:"handler"`
	Description            string                       `json:"description"`
	MemoryMB               int64                        `json:"memory_mb"`
	TimeoutSec             int                          `json:"timeout_seconds"`
	Environment            map[string]string            `json:"environment"`
	CodeSHA256             string                       `json:"code_sha256"`
	CodeSize               int64                        `json:"code_size"`
	State                  string                       `json:"state"` // Pending | Active | Failed
	StateReason            string                       `json:"state_reason,omitempty"`
	StateReasonCode        string                       `json:"state_reason_code,omitempty"`
	LastUpdateStatus       string                       `json:"last_update_status,omitempty"` // InProgress | Successful | Failed
	LastUpdateStatusReason string                       `json:"last_update_status_reason,omitempty"`
	URL                    FunctionURL                  `json:"function_url"`
	LogGroup               string                       `json:"log_group"`
	SubnetID               string                       `json:"subnet_id,omitempty"`
	SecurityGroupIDs       []string                     `json:"security_group_ids,omitempty"`
	Role                   string                       `json:"role,omitempty"`         // execution role ARN
	PackageType            string                       `json:"package_type,omitempty"` // Zip (default) | Image
	ImageURI               string                       `json:"image_uri,omitempty"`
	ImageConfig            *ImageConfig                 `json:"image_config,omitempty"`
	Architectures          []string                     `json:"architectures,omitempty"`
	Layers                 []string                     `json:"layers,omitempty"` // layer version ARNs, in order
	DeadLetterTarget       string                       `json:"dead_letter_target,omitempty"`
	ReservedConcurrency    *int                         `json:"reserved_concurrency,omitempty"`
	Version                string                       `json:"version,omitempty"` // "$LATEST" or a published version number
	VersionDescription     string                       `json:"version_description,omitempty"`
	LastVersion            int                          `json:"last_version,omitempty"` // highest published version ($LATEST only)
	RevisionID             string                       `json:"revision_id,omitempty"`
	PublishedFrom          string                       `json:"published_from,omitempty"` // $LATEST revision a version was published from
	Policy                 map[string][]PolicyStatement `json:"policy,omitempty"`         // resource policy by qualifier ("" = unqualified)
	LastModified           time.Time                    `json:"last_modified"`
	CreatedAt              time.Time                    `json:"created_at"`
	Tags                   core.Tags                    `json:"tags,omitempty"`
}

func (f Function) version() string {
	if f.Version == "" {
		return latest
	}
	return f.Version
}

func (f Function) isImage() bool { return f.PackageType == "Image" }

// qualifiedARN is the function's ARN with a version or alias.
func (f Function) qualifiedARN(q string) string {
	if q == "" {
		return f.ARN
	}
	return f.ARN + ":" + q
}

// Service implements Lambda.
type Service struct {
	env *svc.Env
	cw  *cloudwatch.Service
	vpc *vpc.Service

	mu    sync.Mutex
	pools map[string]*pool // by function name
	// prepMu serialises image pulls per image.
	prepMu sync.Map
	async  chan *asyncEvent
	urlKey []byte // signs code download links
	// Now is the clock (tests).
	now func() time.Time

	// Auth authenticates function URL calls with auth type HC_IAM.
	Auth httpx.Authenticator
	// Queues is the SQS service, for event source mappings.
	Queues QueueSource
	// Streams reads DynamoDB streams, for event source mappings.
	Streams StreamSource
	// invokeHook replaces invocation in tests.
	invokeHook func(ctx context.Context, ref string, payload []byte, o InvokeOptions) (*InvokeResult, error)
	// VerifyJWT validates user pool tokens for API Gateway routes that require them.
	VerifyJWT JWTVerifier
	// CheckAuthorizer verifies that a user pool (and optional app client of it) exists.
	CheckAuthorizer func(pool, client string) error
	// DNSFor returns resolver addresses for containers in a VPC (Route 53).
	DNSFor func(vpcID string) []string
	// Roles validates execution roles and issues their credentials (IAM).
	Roles RoleSource
	// Deliver sends a payload to an SQS queue, SNS topic or EventBridge bus by
	// ARN (async invocation destinations and dead-letter queues).
	Deliver func(ctx context.Context, arn string, payload []byte) error
	// PutEvent publishes an event to an EventBridge bus (async invocation destinations).
	PutEvent func(ctx context.Context, bus, source, detailType string, resources []string, detail []byte) error
	// TargetExists reports whether a destination ARN exists.
	TargetExists func(arn string) bool
	// GetObject reads an S3 object (function code from S3Bucket/S3Key).
	GetObject func(ctx context.Context, bucket, key, version string) ([]byte, error)
	// ResolveImage maps an image URI (e.g. an ECR repository URI) to one the
	// local Docker engine can pull.
	ResolveImage func(uri string) string
	// HTTP carries HTTP_PROXY integration requests; it refuses loopback, link-local
	// and metadata addresses at dial time (tests point it at local backends).
	HTTP *http.Client
}

// Credentials are temporary credentials for a function's execution role.
type Credentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	Expiration                                 time.Time
}

// RoleSource is implemented by IAM (wired in server.go).
type RoleSource interface {
	// LambdaRole checks that a role (name or ARN) exists and trusts
	// lambda.amazonaws.com, and returns its ARN.
	LambdaRole(ref string) (string, error)
	// LambdaCredentials issues temporary credentials for the role.
	LambdaCredentials(ref, session string, ttl time.Duration) (Credentials, error)
}

func New(env *svc.Env, cw *cloudwatch.Service, v *vpc.Service) *Service {
	s := &Service{env: env, cw: cw, vpc: v, pools: map[string]*pool{}, async: make(chan *asyncEvent, 10000),
		urlKey: []byte(core.NewSecret(32)), now: time.Now, HTTP: core.SafeClient(0, 0)}
	if v != nil {
		v.RegisterMembers(s.fwMembers)
	}
	return s
}

// authorizer checks that the caller may perform action on resource.
type authorizer func(action, resource string) error

func allowAll(string, string) error { return nil }

func (s *Service) fnARN(name string) string { return s.env.ARN("lambda", "function:"+name) }

func (s *Service) codePath(name string) string { return s.env.Cfg.Path("lambda", name+".zip") }

// codeFile is where a function version's code package lives.
func (s *Service) codeFile(f Function) string {
	if f.version() == latest {
		return s.codePath(f.Name)
	}
	return s.env.Cfg.Path("lambda", "versions", f.Name, f.Version+".zip")
}

func (s *Service) urlFor(name string) string {
	return s.publicBase() + "/lambda-url/" + name + "/"
}

func fnNotFound(arn string) error {
	return core.Errf(http.StatusNotFound, "ResourceNotFound", "Function not found: %s", arn)
}

// getLatest returns a function's $LATEST record.
func (s *Service) getLatest(name string) (Function, error) {
	f, err := store.Get[Function](s.env.Store, cFunctions, name)
	if err != nil {
		return f, fnNotFound(s.fnARN(name))
	}
	if f.Version == "" {
		f.Version = latest
	}
	if f.PackageType == "" {
		f.PackageType = "Zip"
	}
	if f.LastUpdateStatus == "" {
		f.LastUpdateStatus = "Successful"
	}
	return f, nil
}

// ---- code handling ----

// codeInput accepts code as a base64 zip or inline files.
type codeInput struct {
	ZipBase64 string            `json:"zip_base64"`
	Files     map[string]string `json:"files"`
}

func (ci codeInput) zip() ([]byte, error) {
	if ci.ZipBase64 != "" {
		b, err := base64.StdEncoding.DecodeString(ci.ZipBase64)
		if err != nil {
			return nil, core.BadRequest("zip_base64 is not valid base64")
		}
		return b, checkZip(b)
	}
	if len(ci.Files) == 0 {
		return nil, nil
	}
	return zipFiles(ci.Files)
}

func checkZip(b []byte) error {
	if _, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); err != nil {
		return core.BadRequest("Could not unzip uploaded file. Please check your file, then try to upload again. (%v)", err)
	}
	return nil
}

func zipFiles(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		clean := path.Clean(strings.TrimPrefix(n, "/"))
		if strings.HasPrefix(clean, "..") {
			return nil, core.BadRequest("invalid file name %q", n)
		}
		w, err := zw.Create(clean)
		if err != nil {
			return nil, err
		}
		w.Write([]byte(files[n]))
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func unzip(b []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := path.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 4*maxCodeBytes+1))
		rc.Close()
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > 5*maxCodeBytes {
			return nil, core.BadRequest("Unzipped size must be smaller than %d bytes", 5*maxCodeBytes)
		}
		out[clean] = data
	}
	return out, nil
}

func sha256B64(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func writeFile(p string, b []byte) error {
	if err := os.MkdirAll(path.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Service) saveCode(name string, b []byte) (string, error) {
	if len(b) > maxCodeBytes {
		return "", core.Errf(http.StatusRequestEntityTooLarge, "RequestEntityTooLargeException", "code zip exceeds %d MB", maxCodeBytes>>20)
	}
	return sha256B64(b), writeFile(s.codePath(name), b)
}

// Exists reports whether a function exists (used by other services). ref may
// be qualified (name:alias) or an ARN.
func (s *Service) Exists(ref string) bool {
	name, qual := parseRef(ref)
	if !store.Has(s.env.Store, cFunctions, name) {
		return false
	}
	if qual == "" {
		return true
	}
	_, _, err := s.resolve(name, qual)
	return err == nil
}

var (
	nameRe      = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	aliasRe     = regexp.MustCompile(`^(?:[a-zA-Z_-][a-zA-Z0-9_-]{0,127})$`)
	envKeyRe    = regexp.MustCompile(`^[a-zA-Z]([a-zA-Z0-9_])*$`)
	reservedEnv = map[string]bool{"_HANDLER": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true, "AWS_EXECUTION_ENV": true,
		"AWS_LAMBDA_FUNCTION_NAME": true, "AWS_LAMBDA_FUNCTION_MEMORY_SIZE": true, "AWS_LAMBDA_FUNCTION_VERSION": true,
		"AWS_LAMBDA_INITIALIZATION_TYPE": true, "AWS_LAMBDA_LOG_GROUP_NAME": true, "AWS_LAMBDA_LOG_STREAM_NAME": true,
		"AWS_ACCESS_KEY": true, "AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
		"AWS_LAMBDA_RUNTIME_API": true, "LAMBDA_TASK_ROOT": true, "LAMBDA_RUNTIME_DIR": true}
)

// parseRef splits a function reference: "name", "name:qualifier", a full or
// partial ARN ("arn:aws:lambda:us-east-1:123456789012:function:name[:q]",
// "123456789012:function:name").
func parseRef(ref string) (name, qualifier string) {
	ref = core.CanonicalARN(ref)
	if i := strings.Index(ref, "function:"); i >= 0 && (strings.HasPrefix(ref, "arn:") || i > 0) {
		ref = ref[i+len("function:"):]
	}
	name, qualifier, _ = strings.Cut(ref, ":")
	return name, qualifier
}

// ---- configuration ----

type configInput struct {
	Runtime          string            `json:"runtime"`
	Handler          string            `json:"handler"`
	Description      *string           `json:"description"`
	MemoryMB         int64             `json:"memory_mb"`
	TimeoutSec       int               `json:"timeout_seconds"`
	Environment      map[string]string `json:"environment"`
	SubnetID         *string           `json:"subnet_id"`
	SecurityGroupIDs []string          `json:"security_group_ids"`
	Tags             core.Tags         `json:"tags"`
	Role             *string           `json:"role"`
	Layers           *[]string         `json:"layers"`
	Architectures    []string          `json:"architectures"`
	ImageConfig      *ImageConfig      `json:"image_config"`
	DeadLetterTarget *string           `json:"dead_letter_target"`
}

// applyConfig validates and applies the parts of a configuration change that
// need no other service; roles, layers and targets are checked by checkRefs.
func applyConfig(f *Function, in configInput) error {
	if in.Runtime != "" {
		if f.isImage() {
			return core.BadRequest("Runtime is not supported for functions with package type Image")
		}
		if _, ok := findRuntime(in.Runtime); !ok {
			return core.BadRequest("Value %s at 'runtime' failed to satisfy constraint: unsupported runtime", in.Runtime)
		}
		f.Runtime = in.Runtime
	}
	if in.Handler != "" {
		rt, _ := findRuntime(f.Runtime)
		if !f.isImage() && !rt.validHandler(in.Handler) {
			return core.BadRequest("handler %q must look like file.function for %s", in.Handler, f.Runtime)
		}
		f.Handler = in.Handler
	}
	if in.Description != nil {
		if len(*in.Description) > 256 {
			return core.BadRequest("description must be at most 256 characters")
		}
		f.Description = *in.Description
	}
	if in.MemoryMB != 0 {
		if in.MemoryMB < 128 || in.MemoryMB > 10240 {
			return core.BadRequest("MemorySize must be between 128 and 10240 MB")
		}
		f.MemoryMB = in.MemoryMB
	}
	if in.TimeoutSec != 0 {
		if in.TimeoutSec < 1 || in.TimeoutSec > 900 {
			return core.BadRequest("Timeout must be between 1 and 900 seconds")
		}
		f.TimeoutSec = in.TimeoutSec
	}
	if in.Environment != nil {
		size := 0
		for k, v := range in.Environment {
			if !envKeyRe.MatchString(k) {
				return core.BadRequest("environment variable name %q must start with a letter and contain only letters, digits and _", k)
			}
			if reservedEnv[k] {
				return core.BadRequest("Lambda was unable to configure your environment variables because the environment variables you have provided contains reserved keys that are currently not supported for modification. Reserved keys used in this request: %s", k)
			}
			size += len(k) + len(v)
		}
		if size > 4096 {
			return core.BadRequest("environment variables exceed 4 KB")
		}
		f.Environment = in.Environment
	}
	if in.SubnetID != nil {
		f.SubnetID = *in.SubnetID
	}
	if in.SecurityGroupIDs != nil {
		f.SecurityGroupIDs = in.SecurityGroupIDs
	}
	if in.Tags != nil {
		f.Tags = in.Tags
	}
	if in.Architectures != nil {
		if len(in.Architectures) != 1 || (in.Architectures[0] != "x86_64" && in.Architectures[0] != "arm64") {
			return core.BadRequest("Architectures must be [\"x86_64\"] or [\"arm64\"]")
		}
		f.Architectures = in.Architectures
	}
	if in.ImageConfig != nil {
		if !f.isImage() {
			return core.BadRequest("ImageConfig is only supported for functions with package type Image")
		}
		f.ImageConfig = in.ImageConfig
	}
	if in.Layers != nil {
		if f.isImage() && len(*in.Layers) > 0 {
			return core.BadRequest("Layers are not supported for functions with package type Image")
		}
		if len(*in.Layers) > 5 {
			return core.BadRequest("Cannot reference more than 5 layers.")
		}
	}
	return nil
}

// checkRefs validates references to other resources in a configuration change
// (role, subnet, layers, dead-letter target) and resolves them in place.
func (s *Service) checkRefs(authz authorizer, in *configInput) error {
	if in.Role != nil && *in.Role != "" {
		if s.Roles == nil {
			return core.BadRequest("execution roles are not available")
		}
		arn, err := s.Roles.LambdaRole(*in.Role)
		if err != nil {
			var ce *core.Error
			if errors.As(err, &ce) && ce.Code == "AccessDenied" {
				return core.BadRequest("The role defined for the function cannot be assumed by Lambda.")
			}
			return core.BadRequest("The role defined for the function cannot be assumed by Lambda. (%v)", err)
		}
		if err := authz("iam:PassRole", arn); err != nil {
			return err
		}
		*in.Role = arn
	}
	if in.SubnetID != nil && *in.SubnetID != "" && s.vpc != nil {
		if _, err := s.vpc.SubnetVPC(*in.SubnetID); err != nil {
			return core.BadRequest("subnet %s does not exist", *in.SubnetID)
		}
	}
	if in.Layers != nil {
		out := make([]string, 0, len(*in.Layers))
		for _, l := range *in.Layers {
			lv, err := s.layerByARN(l)
			if err != nil {
				return err
			}
			if err := authz("lambda:GetLayerVersion", lv.ARN); err != nil {
				return err
			}
			out = append(out, lv.ARN)
		}
		*in.Layers = out
	}
	if in.DeadLetterTarget != nil && *in.DeadLetterTarget != "" {
		if err := s.checkTarget(authz, *in.DeadLetterTarget, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyRefs(f *Function, in configInput) {
	if in.Role != nil {
		f.Role = *in.Role
	}
	if in.Layers != nil {
		f.Layers = *in.Layers
	}
	if in.DeadLetterTarget != nil {
		f.DeadLetterTarget = *in.DeadLetterTarget
	}
}

// createSpec is a new function.
type createSpec struct {
	Name string
	configInput
	PackageType string
	Zip         []byte // nil: the runtime's template
	ImageURI    string
	Publish     bool
	VersionDesc string
}

func (s *Service) createFunction(ctx context.Context, authz authorizer, in createSpec) (Function, error) {
	if !nameRe.MatchString(in.Name) {
		return Function{}, core.BadRequest("function name must be 1-64 letters, digits, hyphens or underscores")
	}
	if in.PackageType == "" {
		in.PackageType = "Zip"
		if in.ImageURI != "" {
			in.PackageType = "Image"
		}
	}
	now := core.Now()
	f := Function{Name: in.Name, ARN: s.fnARN(in.Name), MemoryMB: 128, TimeoutSec: 3, Environment: map[string]string{},
		LogGroup: "/aws/lambda/" + in.Name, URL: FunctionURL{AuthType: "NONE"}, CreatedAt: now, LastModified: now,
		PackageType: in.PackageType, Version: latest, RevisionID: uuid(), LastUpdateStatus: "Successful", Architectures: []string{hostArch()}}
	var rt Runtime
	switch in.PackageType {
	case "Zip":
		if in.Runtime == "" {
			in.Runtime = "python3.12"
		}
		var ok bool
		if rt, ok = findRuntime(in.Runtime); !ok {
			return Function{}, core.BadRequest("Value %s at 'runtime' failed to satisfy constraint: unsupported runtime", in.Runtime)
		}
		f.Runtime, f.Handler = rt.Name, rt.DefaultHandler
		if in.ImageURI != "" {
			return Function{}, core.BadRequest("ImageUri is only supported for functions with package type Image")
		}
	case "Image":
		if in.ImageURI == "" {
			return Function{}, core.BadRequest("ImageUri is required for functions with package type Image")
		}
		if in.Runtime != "" || in.Handler != "" {
			return Function{}, core.BadRequest("Runtime and Handler are not supported for functions with package type Image")
		}
		f.ImageURI = in.ImageURI
	default:
		return Function{}, core.BadRequest("PackageType must be Zip or Image")
	}
	if err := applyConfig(&f, in.configInput); err != nil {
		return Function{}, err
	}
	if err := s.checkRefs(authz, &in.configInput); err != nil {
		return Function{}, err
	}
	s.applyRefs(&f, in.configInput)
	if store.Has(s.env.Store, cFunctions, in.Name) {
		return Function{}, core.Conflict("Function already exist: %s", in.Name)
	}
	if f.isImage() {
		sum := sha256.Sum256([]byte(f.ImageURI))
		f.CodeSHA256 = hex.EncodeToString(sum[:])
	} else {
		code := in.Zip
		if code == nil {
			if rt.Template == "" {
				return Function{}, core.BadRequest("code is required for runtime %s", rt.Name)
			}
			code, _ = zipFiles(map[string]string{rt.DefaultFile: rt.Template})
			f.Handler = rt.DefaultHandler
		}
		sum, err := s.saveCode(f.Name, code)
		if err != nil {
			return Function{}, err
		}
		f.CodeSHA256, f.CodeSize = sum, int64(len(code))
	}
	f.State = "Pending"
	// Runtime images are pulled once; container images are (re)pulled on every
	// create and code update, since their tag may have moved.
	if img := s.imageFor(f); s.imageReady(img) && (!f.isImage() || s.env.Docker == nil) {
		f.State = "Active"
	}
	if err := store.Put(s.env.Store, cFunctions, f.Name, f); err != nil {
		return f, err
	}
	if f.State == "Pending" {
		go s.prepare(f.Name, f.RevisionID, true)
	}
	if in.Publish {
		if _, err := s.publishVersion(f.Name, in.VersionDesc, "", ""); err != nil {
			return f, err
		}
		f, _ = s.getLatest(f.Name)
	}
	return f, nil
}

// update applies fn to a function's $LATEST record, bumping its revision.
func (s *Service) update(name, revision string, fn func(*Function) error) (Function, error) {
	f, err := store.Update(s.env.Store, cFunctions, name, func(f *Function) error {
		if revision != "" && f.RevisionID != "" && revision != f.RevisionID {
			return core.Errf(http.StatusPreconditionFailed, "PreconditionFailed",
				"The Revision Id provided does not match the latest Revision Id. Call the GetFunction/GetAlias API to retrieve the latest Revision Id")
		}
		if err := fn(f); err != nil {
			return err
		}
		f.LastModified = time.Now().UTC()
		f.RevisionID = uuid()
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return f, fnNotFound(s.fnARN(name))
	}
	return f, err
}

// modify changes a function's $LATEST record without touching its code or
// configuration revision (policies, URLs, concurrency).
func (s *Service) modify(name string, fn func(*Function) error) (Function, error) {
	f, err := store.Update(s.env.Store, cFunctions, name, fn)
	if errors.Is(err, store.ErrNotFound) {
		return f, fnNotFound(s.fnARN(name))
	}
	return f, err
}

func (s *Service) updateConfiguration(authz authorizer, name, revision string, in configInput) (Function, error) {
	cur, err := s.getLatest(name)
	if err != nil {
		return cur, err
	}
	probe := cur
	if err := applyConfig(&probe, in); err != nil {
		return cur, err
	}
	if err := s.checkRefs(authz, &in); err != nil {
		return cur, err
	}
	var pull bool
	f, err := s.update(name, revision, func(f *Function) error {
		oldImage := s.imageFor(*f)
		if err := applyConfig(f, in); err != nil {
			return err
		}
		s.applyRefs(f, in)
		f.LastUpdateStatus, f.LastUpdateStatusReason = "Successful", ""
		if img := s.imageFor(*f); img != oldImage && !s.imageReady(img) {
			f.LastUpdateStatus, pull = "InProgress", true
		}
		return nil
	})
	if err != nil {
		return f, err
	}
	s.invalidate(name, latest)
	if pull {
		go s.prepare(name, f.RevisionID, false)
	}
	return s.getLatest(name)
}

// codeSpec is new code for a function.
type codeSpec struct {
	Zip      []byte
	ImageURI string
	Publish  bool
	Revision string
}

func (s *Service) updateCode(name string, in codeSpec) (Function, error) {
	cur, err := s.getLatest(name)
	if err != nil {
		return cur, err
	}
	var sum string
	switch {
	case cur.isImage():
		if in.ImageURI == "" || in.Zip != nil {
			return cur, core.BadRequest("Please provide ImageUri when updating a function with packageType Image.")
		}
		h := sha256.Sum256([]byte(in.ImageURI))
		sum = hex.EncodeToString(h[:])
	default:
		if in.ImageURI != "" {
			return cur, core.BadRequest("Please don't provide ImageUri when updating a function with packageType Zip.")
		}
		if in.Zip == nil {
			return cur, core.BadRequest("Please provide a source for function code (ZipFile or S3Bucket/S3Key).")
		}
		if sum, err = s.saveCode(name, in.Zip); err != nil {
			return cur, err
		}
	}
	var pull bool
	f, err := s.update(name, in.Revision, func(f *Function) error {
		f.CodeSHA256, f.LastUpdateStatus, f.LastUpdateStatusReason = sum, "Successful", ""
		if f.isImage() {
			f.ImageURI = in.ImageURI
			if s.env.Docker != nil {
				f.LastUpdateStatus, pull = "InProgress", true
			}
		} else {
			f.CodeSize = int64(len(in.Zip))
		}
		return nil
	})
	if err != nil {
		return f, err
	}
	s.invalidate(name, latest)
	if pull {
		go s.prepare(name, f.RevisionID, false)
	}
	if in.Publish {
		if v, err := s.publishVersion(name, "", "", ""); err != nil {
			return f, err
		} else {
			return v, nil
		}
	}
	return s.getLatest(name)
}

// deleteFunction deletes a function (every version) or one published version.
func (s *Service) deleteFunction(name, qualifier string) error {
	f, err := s.getLatest(name)
	if err != nil {
		return err
	}
	if qualifier != "" && qualifier != latest {
		if _, err := strconv.Atoi(qualifier); err != nil {
			return core.BadRequest("Deletion of aliases is not supported by DeleteFunction; use DeleteAlias")
		}
		if !store.Has(s.env.Store, cVersions, name+":"+qualifier) {
			return fnNotFound(f.qualifiedARN(qualifier))
		}
		for _, a := range s.aliases(name) {
			if a.FunctionVersion == qualifier || a.Weights[qualifier] > 0 {
				return core.Conflict("Unable to delete version because the following aliases reference it: [%s]", a.Name)
			}
		}
		s.invalidate(name, qualifier)
		_ = os.Remove(s.codeFile(Function{Name: name, Version: qualifier}))
		_ = store.Delete(s.env.Store, cInvokeCfgs, name+":"+qualifier)
		return store.Delete(s.env.Store, cVersions, name+":"+qualifier)
	}
	s.retireAll(name)
	for _, m := range store.List[Mapping](s.env.Store, cMappings) {
		if n, _ := parseRef(m.FunctionName); n == name {
			_ = store.Delete(s.env.Store, cMappings, m.ID)
		}
	}
	prefix := name + ":"
	for _, coll := range []string{cVersions, cAliases, cInvokeCfgs} {
		_ = s.env.Store.Retain(coll, func(id string, _ json.RawMessage) bool { return !strings.HasPrefix(id, prefix) })
	}
	_ = os.RemoveAll(s.env.Cfg.Path("lambda", "versions", name))
	_ = os.Remove(s.codePath(name))
	return store.Delete(s.env.Store, cFunctions, name)
}

// reservedTotal sums the reserved concurrency of every function except one.
func (s *Service) reservedTotal(except string) int {
	n := 0
	for _, f := range store.List[Function](s.env.Store, cFunctions) {
		if f.Name != except && f.ReservedConcurrency != nil {
			n += *f.ReservedConcurrency
		}
	}
	return n
}

func (s *Service) putConcurrency(name string, n int) (Function, error) {
	if _, err := s.getLatest(name); err != nil {
		return Function{}, err
	}
	if n < 0 {
		return Function{}, core.BadRequest("ReservedConcurrentExecutions must be at least 0")
	}
	if s.reservedTotal(name)+n > AccountConcurrency-MinUnreserved {
		return Function{}, core.BadRequest("Specified ReservedConcurrentExecutions for function decreases account's UnreservedConcurrentExecution below its minimum value of [%d].", MinUnreserved)
	}
	f, err := s.modify(name, func(f *Function) error { f.ReservedConcurrency = &n; return nil })
	s.wakeAll(name)
	return f, err
}

func (s *Service) deleteConcurrency(name string) error {
	if _, err := s.getLatest(name); err != nil {
		return err
	}
	_, err := s.modify(name, func(f *Function) error { f.ReservedConcurrency = nil; return nil })
	s.wakeAll(name)
	return err
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
