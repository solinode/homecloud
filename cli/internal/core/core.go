// Package core holds the types shared by every HomeCloud service: configuration,
// resource identifiers, ARNs and API errors.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

const (
	// LabelManaged marks every Docker object HomeCloud owns. HomeCloud never
	// touches containers, networks or volumes without it.
	LabelManaged  = "homecloud.managed"
	LabelService  = "homecloud.service"
	LabelResource = "homecloud.resource"
	LabelAccount  = "homecloud.account"
	DefaultRegion = "us-east-1"
	// Partition is the ARN partition. HomeCloud uses AWS's so that SDKs and
	// tools that validate ARNs (Terraform, CDK) accept HomeCloud's.
	Partition = "aws"
	// LegacyRegion is the region of installations created before HomeCloud
	// used AWS-style region names (migrated at startup).
	LegacyRegion = "local-1"
)

// Region is the installation's region, set from the config at startup.
var Region = DefaultRegion

type Config struct {
	DataDir    string `json:"data_dir"`
	APIAddr    string `json:"api_addr"`
	PublicHost string `json:"public_host"` // host name clients use to reach published ports
	// PublicURL is the base URL clients reach the API at when it differs from
	// scheme://public_host:<api port>, e.g. https://cloud.example.com behind a
	// reverse proxy on 443. Every URL HomeCloud generates for clients (API
	// Gateway endpoints, function URLs, queue URLs, issuers, website links)
	// starts with it. Workloads keep using the internal address.
	PublicURL string `json:"public_url,omitempty"`
	// TrustedProxies lists CIDRs (or bare IPs) of reverse proxies whose
	// X-Forwarded-For and X-Forwarded-Proto headers are believed.
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	// ServiceBind is the host address the MinIO and MinIO console ports are
	// published on (default 127.0.0.1: Docker publishing bypasses host
	// firewalls such as ufw); DNSBind is the same for the DNS server.
	ServiceBind string `json:"service_bind,omitempty"`
	DNSBind     string `json:"dns_bind,omitempty"`
	// ElasticIPPool is the IPv4 CIDR Elastic IPs are allocated from
	// (default 203.0.113.0/24, a documentation range: the addresses are
	// records, not routed).
	ElasticIPPool string `json:"elastic_ip_pool,omitempty"`
	S3Port        int    `json:"s3_port"`
	S3ConsolePort int    `json:"s3_console_port"`
	ECRPort       int    `json:"ecr_port"`
	DNSPort       int    `json:"dns_port"`           // host port serving public hosted zones (UDP and TCP)
	TLSCert       string `json:"tls_cert,omitempty"` // PEM files; empty = plain HTTP
	TLSKey        string `json:"tls_key,omitempty"`
	Region        string `json:"region"`
}

func DefaultDataDir() string {
	if d := os.Getenv("HOMECLOUD_DATA_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".homecloud"
	}
	return filepath.Join(home, ".homecloud")
}

func DefaultConfig() Config {
	return Config{
		DataDir:       DefaultDataDir(),
		APIAddr:       "127.0.0.1:8080",
		PublicHost:    "localhost",
		S3Port:        9500,
		S3ConsolePort: 9501,
		ECRPort:       5500,
		DNSPort:       8053,
		Region:        DefaultRegion,
	}
}

// TLS reports whether the API is served over HTTPS by HomeCloud itself.
func (c Config) TLS() bool { return c.TLSCert != "" }

// APIPort is the port the API listens on.
func (c Config) APIPort() string {
	if _, port, err := net.SplitHostPort(c.APIAddr); err == nil {
		return port
	}
	_, port, _ := strings.Cut(c.APIAddr, ":")
	return port
}

// PublicBase is the API's base URL as clients reach it, without a trailing slash.
func (c Config) PublicBase() string {
	if u := strings.TrimRight(strings.TrimSpace(c.PublicURL), "/"); u != "" {
		return u
	}
	scheme := "http"
	if c.TLS() {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(c.PublicHost, c.APIPort())
}

// PublicHostname is the host name in PublicBase.
func (c Config) PublicHostname() string {
	if u, err := url.Parse(c.PublicBase()); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return c.PublicHost
}

// ValidatePublicURL checks a --public-url value: an absolute http(s) URL with
// no query or fragment.
func ValidatePublicURL(v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("--public-url must look like https://cloud.example.com (scheme, host, optional port and path prefix), got %q", v)
	}
	return nil
}

// Normalize fills settings derived from others: with a public URL and no
// explicit public host, published ports (load balancers, instances) are
// reached through the URL's host name.
func (c *Config) Normalize() {
	if c.PublicURL != "" && (c.PublicHost == "" || c.PublicHost == "localhost") {
		if u, err := url.Parse(c.PublicURL); err == nil && u.Hostname() != "" {
			c.PublicHost = u.Hostname()
		}
	}
}

// ServiceBindAddr is where MinIO's ports are published on the host.
func (c Config) ServiceBindAddr() string {
	if c.ServiceBind != "" {
		return c.ServiceBind
	}
	return "127.0.0.1"
}

// ServiceDialHost is the address HomeCloud itself uses to reach MinIO on the
// host: loopback, or the bind address when MinIO is published on one specific
// non-loopback address.
func (c Config) ServiceDialHost() string {
	if ip := net.ParseIP(c.ServiceBindAddr()); ip != nil && !ip.IsUnspecified() {
		return ip.String()
	}
	return "127.0.0.1"
}

// DNSBindAddr is where the DNS server's port is published on the host.
func (c Config) DNSBindAddr() string {
	if c.DNSBind != "" {
		return c.DNSBind
	}
	return "127.0.0.1"
}

func (c Config) Path(parts ...string) string {
	return filepath.Join(append([]string{c.DataDir}, parts...)...)
}

// NewID returns an AWS-style identifier such as "i-0a1b2c3d4e5f67890".
func NewID(prefix string) string {
	return prefix + "-" + RandHex(17)
}

func RandHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)[:n]
}

const alnumUpper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randFrom(alphabet string, n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		sb.WriteByte(alphabet[k.Int64()])
	}
	return sb.String()
}

func NewAccessKeyID() string { return "HCIA" + randFrom(alnumUpper, 16) }
func NewSecret(n int) string { return randFrom(alnum, n) }

func NewAccountID() string {
	return randFrom("0123456789", 12)
}

// globalServices have ARNs without a region, as in AWS.
var globalServices = map[string]bool{"iam": true, "sts": true, "route53": true, "cloudfront": true, "organizations": true}

func ARN(account, service, resource string) string {
	region := Region
	if globalServices[service] {
		region = ""
	}
	return fmt.Sprintf("arn:%s:%s:%s:%s:%s", Partition, service, region, account, resource)
}

// CanonicalARN rewrites an ARN in HomeCloud's legacy form ("arn:hc:<service>:local-1:...")
// to the current partition and region; other strings are returned unchanged.
func CanonicalARN(s string) string {
	if !strings.HasPrefix(s, "arn:hc:") {
		return s
	}
	parts := strings.SplitN(s, ":", 6)
	if len(parts) < 6 {
		return s
	}
	parts[1] = Partition
	if globalServices[parts[2]] {
		parts[3] = ""
	} else if parts[3] == LegacyRegion {
		parts[3] = Region
	}
	return strings.Join(parts, ":")
}

// InternalErrorMessage is what clients are told when a request fails for a
// reason that is not a *Error: the real error (file paths, Docker daemon output,
// database errors) goes to the server log, not to the caller or the audit trail.
const InternalErrorMessage = "an internal error occurred; details are in the server log"

// IsLocalARN reports whether arn names a resource of this deployment: the same
// partition and account, and the deployment's region (or none, for global
// services). Code that delivers to "the resource named by this ARN" by looking
// up only its name must check this first, or an ARN for another account or
// region would be authorized as one resource and served as another.
func IsLocalARN(arn, account string) bool {
	parts := strings.SplitN(CanonicalARN(arn), ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[1] != Partition || parts[4] != account {
		return false
	}
	return parts[3] == Region || (parts[3] == "" && globalServices[parts[2]])
}

func Now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// Error is an API error with an AWS-style code and an HTTP status.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func Errf(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

func NotFound(kind, id string) *Error {
	return Errf(http.StatusNotFound, "ResourceNotFound", "%s %q does not exist", kind, id)
}

func BadRequest(format string, a ...any) *Error {
	return Errf(http.StatusBadRequest, "ValidationError", format, a...)
}

func Conflict(format string, a ...any) *Error {
	return Errf(http.StatusConflict, "ResourceConflict", format, a...)
}

// Tags are free-form key/value labels attached to resources.
type Tags map[string]string

// Recover logs a panic in a background goroutine instead of crashing the server.
// Use as: defer core.Recover("what is running")
func Recover(what string) {
	if r := recover(); r != nil {
		log.Printf("panic in %s: %v\n%s", what, r, debug.Stack())
	}
}
