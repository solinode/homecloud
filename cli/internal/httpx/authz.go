package httpx

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// Decision is the outcome of evaluating identity policies.
type Decision int

const (
	// NoDecision: no policy allows or denies the request (implicit deny).
	NoDecision Decision = iota
	Allowed
	Denied
)

// Verdict is what a resource-based policy says about a request.
type Verdict struct {
	// Deny: a matching Deny statement.
	Deny bool
	// Allow: a matching Allow statement that names the caller directly (the
	// caller's ARN, a service principal, or "*").
	Allow bool
	// Delegated: a matching Allow statement that names the caller's account
	// (its root); it hands the decision to the account's IAM policies.
	Delegated bool
}

// Who is the caller a resource policy is evaluated for: an authenticated
// principal, or a service delivering on behalf of a resource (SNS, EventBridge).
// A nil Principal with no Service is an anonymous caller.
type Who struct {
	Principal *Principal
	Service   string // "sns.amazonaws.com"
}

// EvalResourcePolicy evaluates a resource-based policy document (JSON). IAM
// installs it at start-up; ctx holds the condition keys of the request.
var EvalResourcePolicy func(policy string, who Who, action, resource string, ctx map[string][]string) Verdict

// Access carries the resource-side inputs of an authorization check.
type Access struct {
	// Policy is the resource-based policy of the target (bucket policy, queue
	// policy, topic policy, key policy), or "" for none.
	Policy string
	// KeyPolicy marks a KMS key policy: identity policies only apply when the
	// key policy delegates to the account (an Allow for its root), and a key
	// policy can grant access on its own.
	KeyPolicy bool
	// Keys are request-specific condition keys (s3:prefix, aws:SourceArn, ...),
	// lower-case names.
	Keys map[string][]string
	// Lazy computes keys that are costly to look up (s3:ExistingObjectTag/...).
	// It runs only when the resource policy or the principal's identity policies
	// mention LazyMarker (lower-case).
	Lazy       func() map[string][]string
	LazyMarker string
}

// lazyKeys adds the lazily computed keys when a policy needs them.
func (p *Principal) lazyKeys(acc Access, keys map[string][]string) map[string][]string {
	if acc.Lazy == nil {
		return keys
	}
	m := strings.ToLower(acc.LazyMarker)
	if strings.Contains(strings.ToLower(acc.Policy), m) || p != nil && p.Mentions != nil && p.Mentions(m) {
		return mergeKeys(keys, acc.Lazy())
	}
	return keys
}

// PolicyProvider returns the resource policy of the resource named by ARN, if
// the resource exists and has (or defaults to) one.
type PolicyProvider func(arn string) (Access, bool)

var (
	provMu    sync.RWMutex
	providers = map[string]PolicyProvider{}
)

// RegisterPolicyProvider makes a service's resource policies part of every
// authorization check on its ARNs, on both the native and the AWS API.
func RegisterPolicyProvider(service string, f PolicyProvider) {
	provMu.Lock()
	defer provMu.Unlock()
	providers[service] = f
}

// providerAccess looks up the resource policy for an ARN via the owning service.
func providerAccess(resource string) Access {
	if !strings.HasPrefix(resource, "arn:") {
		return Access{}
	}
	parts := strings.SplitN(resource, ":", 6)
	if len(parts) < 6 {
		return Access{}
	}
	provMu.RLock()
	f := providers[parts[2]]
	provMu.RUnlock()
	if f == nil {
		return Access{}
	}
	a, _ := f(resource)
	return a
}

// identity evaluates the principal's identity policies for the request.
func (p *Principal) identity(action, resource string, keys map[string][]string) Decision {
	switch {
	case p.Identity != nil:
		return p.Identity(action, resource, keys)
	case p.Can != nil && p.Can(action, resource):
		return Allowed
	}
	return NoDecision
}

// Permits decides whether the principal may perform action on resource, the AWS
// way: an explicit Deny in an identity policy or in the resource's policy
// always wins; otherwise the request is allowed if the identity policies allow
// it or the resource policy grants the principal directly (for KMS keys: the
// key policy grants it, or delegates to IAM). extra supplies the resource
// policy (when the caller already loaded it) and request condition keys. The
// account's root user is not subject to resource-policy Deny statements, so a
// bad policy cannot lock the administrator out.
func (p *Principal) Permits(action, resource string, extra Access) bool {
	if p == nil {
		return PermitsAnonymous(action, resource, extra)
	}
	resource = core.CanonicalARN(resource)
	if p.ResolveResource != nil {
		resource = p.ResolveResource(action, resource)
	}
	acc := providerAccess(resource)
	if extra.Policy != "" {
		acc.Policy, acc.KeyPolicy = extra.Policy, extra.KeyPolicy
	}
	keys := p.lazyKeys(Access{Policy: acc.Policy, Lazy: extra.Lazy, LazyMarker: extra.LazyMarker}, mergeKeys(acc.Keys, extra.Keys))
	id := p.identity(action, resource, keys)
	if id == Denied {
		return false
	}
	if acc.Policy == "" || EvalResourcePolicy == nil {
		return id == Allowed
	}
	if acc.KeyPolicy {
		keys = mergeKeys(keys, map[string][]string{"kms:calleraccount": {p.AccountID}})
	}
	v := EvalResourcePolicy(acc.Policy, Who{Principal: p}, action, resource, p.keys(keys))
	if v.Deny && !p.Root {
		return false
	}
	if acc.KeyPolicy {
		return v.Allow || (id == Allowed && (v.Delegated || p.Root))
	}
	return id == Allowed || v.Allow
}

// PermitsAnonymous decides an unauthenticated request: only a resource policy
// that names everyone can allow it.
func PermitsAnonymous(action, resource string, acc Access) bool {
	if acc.Policy == "" {
		acc.Policy = providerAccess(resource).Policy
	}
	if acc.Policy == "" || EvalResourcePolicy == nil {
		return false
	}
	var np *Principal
	v := EvalResourcePolicy(acc.Policy, Who{}, action, resource, np.lazyKeys(acc, acc.Keys))
	return v.Allow && !v.Deny
}

// PermitsService decides whether a service (sns.amazonaws.com, events.amazonaws.com)
// may deliver to resource. keys carry aws:SourceArn and aws:SourceAccount. A
// resource without a policy accepts deliveries (they were authorized when the
// subscription or target was created); one with a policy needs an Allow that
// names the service and no matching Deny.
func PermitsService(service, action, resource string, keys map[string][]string) bool {
	acc := providerAccess(resource)
	if acc.Policy == "" || EvalResourcePolicy == nil {
		return true
	}
	v := EvalResourcePolicy(acc.Policy, Who{Service: service}, action, resource, keys)
	return v.Allow && !v.Deny
}

// keys returns the principal's request context plus extra keys.
func (p *Principal) keys(extra map[string][]string) map[string][]string {
	return mergeKeys(p.Context, extra)
}

func mergeKeys(base, extra map[string][]string) map[string][]string {
	if len(extra) == 0 {
		return base
	}
	out := make(map[string][]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[strings.ToLower(k)] = v
	}
	return out
}

// RequestContext returns the IAM global condition keys that come from the HTTP
// request itself: aws:SourceIp, aws:SecureTransport, aws:UserAgent, aws:Referer
// and aws:TlsVersion. The source IP is the peer address of the connection and
// aws:SecureTransport is true only when the connection itself is TLS, unless the
// peer is a trusted proxy (--trusted-proxies): ProxyTrust.Wrap then substitutes
// the client address from X-Forwarded-For and the scheme from X-Forwarded-Proto.
// Without trusted proxies the forwarding headers are ignored.
func RequestContext(r *http.Request) map[string][]string {
	c := map[string][]string{
		"aws:sourceip":        {ClientIP(r)},
		"aws:securetransport": {strconv.FormatBool(r.TLS != nil)},
	}
	if ua := r.UserAgent(); ua != "" {
		c["aws:useragent"] = []string{ua}
	}
	if ref := r.Referer(); ref != "" {
		c["aws:referer"] = []string{ref}
	}
	if r.TLS != nil {
		if v, ok := map[uint16]string{0x0301: "1", 0x0302: "1.1", 0x0303: "1.2", 0x0304: "1.3"}[r.TLS.Version]; ok {
			c["aws:tlsversion"] = []string{v}
		}
	}
	return c
}

// ClientIP is the address of the connection's peer.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // IPv6 zone
	}
	return host
}
