// Package httpx is the small HTTP framework the HomeCloud API is built on:
// routing with per-route IAM actions, JSON binding and uniform error bodies.
package httpx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	AccountID string `json:"account_id"`
	UserName  string `json:"user_name"`
	ARN       string `json:"arn"`
	Root      bool   `json:"root"`
	AccessKey string `json:"access_key,omitempty"`
	// RoleName and SessionName are set for temporary role credentials (STS).
	RoleName    string `json:"role_name,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	// Can reports whether the principal may perform action on resource. IAM
	// evaluates policy conditions against Context at call time.
	Can func(action, resource string) bool `json:"-"`
	// Identity evaluates the principal's identity policies with extra request
	// condition keys (s3:prefix, ...) and tells an explicit Deny from an
	// implicit one. When nil, Can decides.
	Identity func(action, resource string, keys map[string][]string) Decision `json:"-"`
	// Mentions reports whether any of the principal's identity policies contains
	// the (lower-case) text; it lets costly condition keys be looked up only
	// when some policy uses them.
	Mentions func(text string) bool `json:"-"`
	// Context holds the IAM condition keys of the request (lower-case keys such
	// as "aws:sourceip" or "aws:username"). IAM fills the identity keys; see
	// AddRequestContext for the request keys.
	Context map[string][]string `json:"-"`
	// ResolveResource maps the resource a caller named to the one it denotes (IAM
	// resolves role names and paths for iam:PassRole), so that a policy on the real
	// resource cannot be dodged by another spelling of it.
	ResolveResource func(action, resource string) string `json:"-"`
}

// AddRequestContext records the IAM global condition keys that come from the
// HTTP request (see RequestContext) on p.
func (p *Principal) AddRequestContext(r *http.Request) {
	if p.Context == nil {
		p.Context = map[string][]string{}
	}
	for k, v := range RequestContext(r) {
		p.Context[k] = v
	}
}

type principalKey struct{}

// WithPrincipal marks an in-process request as made by p. Only server code can
// set context values, so this lets services call other services' routes with
// the original caller's permissions (e.g. CloudFormation stacks).
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, error)
}

// AuditFunc receives one record per authorized API call (CloudTrail).
type AuditFunc func(p *Principal, action, resource string, r *http.Request, status int, took time.Duration)

type Handler func(c *Ctx) (any, error)

// Unscoped collects routes with path parameters but no resource ARN; the server
// refuses to start if any exist (a guard against authorization regressions).
var Unscoped []string

type Router struct {
	Mux     *http.ServeMux
	Auth    Authenticator
	Account string
	Audit   AuditFunc
}

type routeOpts struct {
	resource string
	public   bool
	deferred bool
	maxBody  int64
}

// PublicBodyLimit is the request body cap of routes marked SmallBody: sign-in
// and similar calls whose bodies are a few hundred bytes.
const PublicBodyLimit = 64 << 10

// SmallBody caps the request body at PublicBodyLimit; use it on public routes,
// which are reachable without credentials.
func SmallBody() Opt { return func(o *routeOpts) { o.maxBody = PublicBodyLimit } }

type Opt func(*routeOpts)

// Res sets the resource ARN template checked against the route's action;
// {name} placeholders are filled from path parameters.
func Res(tmpl string) Opt { return func(o *routeOpts) { o.resource = tmpl } }

// Public marks a route that needs no authentication.
func Public() Opt { return func(o *routeOpts) { o.public = true } }

// Deferred authenticates the caller but leaves authorization to the handler,
// which must call Ctx.Authorize with the real resource ARN (for routes whose
// resource is only known after a lookup, e.g. by alias or query parameter).
func Deferred() Opt { return func(o *routeOpts) { o.deferred = true } }

var placeholder = regexp.MustCompile(`\{([a-zA-Z_]+)\}`)

// Handle registers pattern ("GET /api/v1/...") guarded by the IAM action.
func (rt *Router) Handle(pattern, action string, h Handler, opts ...Opt) {
	o := routeOpts{resource: "*"}
	for _, f := range opts {
		f(&o)
	}
	if strings.Contains(pattern, "{") && o.resource == "*" && !o.public && !o.deferred {
		// Scoped Allow/Deny statements never match "*": per-resource routes must say which resource they touch.
		Unscoped = append(Unscoped, pattern)
	}
	rt.Mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		c := &Ctx{W: w, R: r, Account: rt.Account}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		c.W = sw
		if o.maxBody > 0 {
			r.Body = http.MaxBytesReader(sw, r.Body, o.maxBody)
		}
		resource := strings.ReplaceAll(strings.ReplaceAll(o.resource, "{account}", rt.Account), "{region}", core.Region)
		resource = placeholder.ReplaceAllStringFunc(resource, func(m string) string {
			return r.PathValue(m[1 : len(m)-1])
		})
		if !o.public {
			p, ok := r.Context().Value(principalKey{}).(*Principal)
			if !ok {
				var err error
				if p, err = rt.Auth.Authenticate(r); err != nil {
					WriteError(sw, err)
					return
				}
			}
			c.P = p
			// Identity calls (sts:*) are always allowed, as in AWS.
			if !o.deferred && !strings.HasPrefix(action, "sts:") && !p.Permits(action, resource, Access{}) {
				WriteError(sw, core.Errf(http.StatusForbidden, "AccessDenied", "%s is not authorized to perform %s on %s", p.ARN, action, resource))
				rt.audit(p, action, resource, r, sw.status, time.Since(start))
				return
			}
		}
		out, err := h(c)
		if err != nil {
			WriteError(sw, err)
		} else if out != nil && !c.written {
			WriteJSON(sw, http.StatusOK, out)
		} else if out == nil && !c.written {
			WriteJSON(sw, http.StatusOK, map[string]bool{"ok": true})
		}
		rt.audit(c.P, action, resource, r, sw.status, time.Since(start))
	})
}

func (rt *Router) audit(p *Principal, action, resource string, r *http.Request, status int, took time.Duration) {
	if rt.Audit != nil && p != nil {
		rt.Audit(p, action, resource, r, status, took)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("connection does not support hijacking")
	}
	s.status = http.StatusSwitchingProtocols
	return h.Hijack()
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type Ctx struct {
	W       http.ResponseWriter
	R       *http.Request
	P       *Principal
	Account string
	written bool
}

func (c *Ctx) Param(name string) string { return c.R.PathValue(name) }
func (c *Ctx) Query(name string) string { return c.R.URL.Query().Get(name) }

func (c *Ctx) QueryInt(name string, def int) int {
	if v, err := strconv.Atoi(c.Query(name)); err == nil {
		return v
	}
	return def
}

// Bind decodes the JSON request body into v. An empty body leaves v untouched.
func (c *Ctx) Bind(v any) error {
	body, err := io.ReadAll(io.LimitReader(c.R.Body, 64<<20))
	if err != nil {
		return core.BadRequest("read body: %v", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return core.BadRequest("invalid JSON body: %v", err)
	}
	return nil
}

// Authorize performs an additional IAM check inside a handler.
func (c *Ctx) Authorize(action, resource string) error {
	return c.AuthorizeWith(action, resource, Access{})
}

// AuthorizeWith is Authorize with a resource policy and request condition keys.
func (c *Ctx) AuthorizeWith(action, resource string, acc Access) error {
	if c.P == nil || c.P.Permits(action, resource, acc) {
		return nil
	}
	return core.Errf(http.StatusForbidden, "AccessDenied", "%s is not authorized to perform %s on %s", c.P.ARN, action, resource)
}

// Check is Authorize; it exists so a Ctx can stand in for an AWS request where handlers share logic.
func (c *Ctx) Check(action, resource string) error { return c.Authorize(action, resource) }

// MarkWritten tells the router the handler wrote its own response.
func (c *Ctx) MarkWritten() { c.written = true }

func (c *Ctx) JSON(status int, v any) (any, error) {
	c.written = true
	WriteJSON(c.W, status, v)
	return nil, nil
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func WriteError(w http.ResponseWriter, err error) {
	var ce *core.Error
	switch {
	case errors.As(err, &ce):
	case errors.Is(err, store.ErrNotFound):
		ce = core.Errf(http.StatusNotFound, "ResourceNotFound", "resource not found")
	default:
		log.Printf("internal error: %v", err)
		ce = core.Errf(http.StatusInternalServerError, "InternalError", "%v", err)
	}
	WriteJSON(w, ce.Status, map[string]any{"error": ce})
}
