// Package iam implements identity and access management: users, groups,
// managed and inline policies, access keys, console sessions and the policy
// evaluation every API request goes through.
package iam

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"golang.org/x/crypto/bcrypt"
)

const (
	cUsers    = "iam_users"
	cGroups   = "iam_groups"
	cPolicies = "iam_policies"
	cKeys     = "iam_access_keys"
	cSessions = "iam_sessions"
	cAccount  = "account"

	RootUser   = "root"
	sessionTTL = 12 * time.Hour
)

func errf(format string, a ...any) error { return core.BadRequest(format, a...) }

type User struct {
	Name             string                    `json:"name"`
	ID               string                    `json:"id"`
	ARN              string                    `json:"arn"`
	Root             bool                      `json:"root,omitempty"`
	CreatedAt        time.Time                 `json:"created_at"`
	PasswordHash     string                    `json:"password_hash,omitempty"`
	PasswordSetAt    *time.Time                `json:"password_set_at,omitempty"`
	LastLogin        *time.Time                `json:"last_login,omitempty"`
	Groups           []string                  `json:"groups"`
	AttachedPolicies []string                  `json:"attached_policies"`
	InlinePolicies   map[string]PolicyDocument `json:"inline_policies"`
	Tags             core.Tags                 `json:"tags,omitempty"`
}

// view hides secrets when a user is returned over the API.
func (u User) view() map[string]any {
	return map[string]any{
		"name": u.Name, "id": u.ID, "arn": u.ARN, "root": u.Root, "created_at": u.CreatedAt,
		"console_access": u.PasswordHash != "", "password_set_at": u.PasswordSetAt, "last_login": u.LastLogin,
		"groups": nz(u.Groups), "attached_policies": nz(u.AttachedPolicies), "inline_policies": u.InlinePolicies, "tags": u.Tags,
	}
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

type Group struct {
	Name             string    `json:"name"`
	ARN              string    `json:"arn"`
	CreatedAt        time.Time `json:"created_at"`
	AttachedPolicies []string  `json:"attached_policies"`
}

type Policy struct {
	Name        string         `json:"name"`
	ARN         string         `json:"arn"`
	Description string         `json:"description"`
	Managed     bool           `json:"managed"` // built in, cannot be edited
	Document    PolicyDocument `json:"document"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type AccessKey struct {
	AccessKeyID string     `json:"access_key_id"`
	UserName    string     `json:"user_name"`
	SecretHash  string     `json:"secret_hash,omitempty"`
	Status      string     `json:"status"` // Active | Inactive
	CreatedAt   time.Time  `json:"created_at"`
	LastUsed    *time.Time `json:"last_used,omitempty"`
}

type session struct {
	Token    string    `json:"token"`
	UserName string    `json:"user_name"`
	Expires  time.Time `json:"expires"`
}

type account struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct {
	env      *svc.Env
	failMu   sync.Mutex
	failures map[string][]time.Time // client IP -> recent failed sign-ins
}

func New(env *svc.Env) *Service { return &Service{env: env, failures: map[string][]time.Time{}} }

const (
	maxFailures   = 10
	failureWindow = 5 * time.Minute
)

// throttled reports whether ip has too many recent failed sign-ins; fail records one.
func (s *Service) throttled(ip string, fail bool) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	recent := s.failures[ip][:0]
	for _, t := range s.failures[ip] {
		if time.Since(t) < failureWindow {
			recent = append(recent, t)
		}
	}
	if fail {
		recent = append(recent, time.Now())
	}
	if len(recent) == 0 {
		delete(s.failures, ip)
	} else {
		s.failures[ip] = recent
	}
	return len(recent) >= maxFailures
}

// BootstrapResult carries credentials that exist only at first start.
type BootstrapResult struct {
	AccountID    string
	RootPassword string
	AccessKeyID  string
	SecretKey    string
}

// LoadAccount returns the account ID, creating the account on first run.
func LoadAccount(s *store.Store) (string, bool, error) {
	a, err := store.Get[account](s, cAccount, "self")
	if err == nil {
		return a.ID, false, nil
	}
	a = account{ID: core.NewAccountID(), CreatedAt: core.Now()}
	return a.ID, true, store.Put(s, cAccount, "self", a)
}

// Bootstrap installs managed policies and, on first run, the root user.
func (s *Service) Bootstrap() (*BootstrapResult, error) {
	for _, bp := range builtinPolicies {
		p := Policy{Name: bp.Name, ARN: s.policyARN(bp.Name), Description: bp.Description, Managed: true, Document: bp.Doc, CreatedAt: core.Now(), UpdatedAt: core.Now()}
		if old, err := store.Get[Policy](s.env.Store, cPolicies, bp.Name); err == nil {
			p.CreatedAt, p.UpdatedAt = old.CreatedAt, old.UpdatedAt
		}
		if err := store.Put(s.env.Store, cPolicies, bp.Name, p); err != nil {
			return nil, err
		}
	}
	if store.Has(s.env.Store, cUsers, RootUser) {
		return nil, nil
	}
	pw := core.NewSecret(20)
	u := s.newUser(RootUser)
	u.Root = true
	u.AttachedPolicies = []string{"AdministratorAccess"}
	if err := setPassword(&u, pw); err != nil {
		return nil, err
	}
	if err := store.Put(s.env.Store, cUsers, u.Name, u); err != nil {
		return nil, err
	}
	k, secret, err := s.createKey(RootUser)
	if err != nil {
		return nil, err
	}
	return &BootstrapResult{AccountID: s.env.AccountID, RootPassword: pw, AccessKeyID: k.AccessKeyID, SecretKey: secret}, nil
}

// ResetRootPassword sets a new random root console password (used by `homecloud serve --reset-root`).
func (s *Service) ResetRootPassword() (string, error) {
	pw := core.NewSecret(20)
	_, err := store.Update(s.env.Store, cUsers, RootUser, func(u *User) error { return setPassword(u, pw) })
	return pw, err
}

func (s *Service) policyARN(name string) string { return s.env.ARN("iam", "policy/"+name) }

func (s *Service) newUser(name string) User {
	return User{
		Name: name, ID: "HCUA" + strings.ToUpper(core.RandHex(16)), ARN: s.env.ARN("iam", "user/"+name),
		CreatedAt: core.Now(), Groups: []string{}, AttachedPolicies: []string{}, InlinePolicies: map[string]PolicyDocument{},
	}
}

func setPassword(u *User, pw string) error {
	if len(pw) < 8 {
		return errf("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	now := core.Now()
	u.PasswordHash, u.PasswordSetAt = string(h), &now
	return nil
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func (s *Service) createKey(user string) (AccessKey, string, error) {
	secret := core.NewSecret(40)
	k := AccessKey{AccessKeyID: core.NewAccessKeyID(), UserName: user, SecretHash: hashSecret(secret), Status: "Active", CreatedAt: core.Now()}
	return k, secret, store.Put(s.env.Store, cKeys, k.AccessKeyID, k)
}

// ---- authentication & authorization ----

func (s *Service) Authenticate(r *http.Request) (*httpx.Principal, error) {
	token := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	} else if r.Method == http.MethodGet {
		token = r.URL.Query().Get("access_token") // lets the console link downloads directly
	}
	if token == "" {
		return nil, core.Errf(http.StatusUnauthorized, "MissingAuthenticationToken", "request is not signed: send 'Authorization: Bearer <session token | access key id>:<secret>'")
	}
	var user, keyID string
	if strings.HasPrefix(token, "hcs_") {
		sess, err := store.Get[session](s.env.Store, cSessions, hashSecret(token))
		if err != nil || time.Now().After(sess.Expires) {
			return nil, core.Errf(http.StatusUnauthorized, "ExpiredToken", "session is invalid or expired; sign in again")
		}
		user = sess.UserName
	} else {
		id, secret, ok := strings.Cut(token, ":")
		if !ok {
			return nil, core.Errf(http.StatusUnauthorized, "InvalidClientTokenId", "malformed credentials")
		}
		k, err := store.Get[AccessKey](s.env.Store, cKeys, id)
		if err != nil || subtle.ConstantTimeCompare([]byte(k.SecretHash), []byte(hashSecret(secret))) != 1 {
			return nil, core.Errf(http.StatusUnauthorized, "InvalidClientTokenId", "the access key or secret is not valid")
		}
		if k.Status != "Active" {
			return nil, core.Errf(http.StatusUnauthorized, "InvalidClientTokenId", "access key %s is inactive", id)
		}
		user, keyID = k.UserName, id
		if k.LastUsed == nil || time.Since(*k.LastUsed) > time.Minute {
			_, _ = store.Update(s.env.Store, cKeys, id, func(k *AccessKey) error { n := core.Now(); k.LastUsed = &n; return nil })
		}
	}
	u, err := store.Get[User](s.env.Store, cUsers, user)
	if err != nil {
		return nil, core.Errf(http.StatusUnauthorized, "InvalidClientTokenId", "user %q no longer exists", user)
	}
	return s.principal(u, keyID), nil
}

// Refresh re-reads a principal's user, access key and policies, so long-running
// work (CloudFormation stacks) acts with the caller's current permissions.
func (s *Service) Refresh(p *httpx.Principal) (*httpx.Principal, error) {
	u, err := store.Get[User](s.env.Store, cUsers, p.UserName)
	if err != nil {
		return nil, fmt.Errorf("user %s no longer exists", p.UserName)
	}
	if p.AccessKey != "" {
		k, err := store.Get[AccessKey](s.env.Store, cKeys, p.AccessKey)
		if err != nil || k.Status != "Active" {
			return nil, fmt.Errorf("access key %s is no longer active", p.AccessKey)
		}
	}
	return s.principal(u, p.AccessKey), nil
}

func (s *Service) principal(u User, keyID string) *httpx.Principal {
	p := &httpx.Principal{AccountID: s.env.AccountID, UserName: u.Name, ARN: u.ARN, Root: u.Root, AccessKey: keyID}
	if u.Root {
		p.Can = func(string, string) bool { return true }
		return p
	}
	docs := s.effectiveDocs(u)
	p.Can = func(action, resource string) bool { return evaluate(docs, action, resource) == allow }
	return p
}

// effectiveDocs gathers every policy that applies to u: attached, inline, and via groups.
func (s *Service) effectiveDocs(u User) []PolicyDocument {
	names := slices.Clone(u.AttachedPolicies)
	for _, g := range u.Groups {
		if grp, err := store.Get[Group](s.env.Store, cGroups, g); err == nil {
			names = append(names, grp.AttachedPolicies...)
		}
	}
	var docs []PolicyDocument
	for _, n := range names {
		if p, err := store.Get[Policy](s.env.Store, cPolicies, n); err == nil {
			docs = append(docs, p.Document)
		}
	}
	for _, d := range u.InlinePolicies {
		docs = append(docs, d)
	}
	return docs
}

var nameRe = regexp.MustCompile(`^[\w+=,.@-]{1,64}$`)

func validName(kind, n string) error {
	if !nameRe.MatchString(n) {
		return errf("%s name %q is invalid: use 1-64 letters, digits or +=,.@_-", kind, n)
	}
	return nil
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	iamRes := func(kind string) httpx.Opt { return httpx.Res("arn:hc:iam:local-1:{account}:" + kind) }

	r.Handle("POST /api/v1/auth/login", "", s.login, httpx.Public())
	r.Handle("POST /api/v1/auth/logout", "sts:Logout", s.logout)
	r.Handle("GET /api/v1/auth/whoami", "sts:GetCallerIdentity", s.whoami)

	r.Handle("GET /api/v1/iam/summary", "iam:GetAccountSummary", s.summary)
	r.Handle("GET /api/v1/iam/users", "iam:ListUsers", s.listUsers)
	r.Handle("POST /api/v1/iam/users", "iam:CreateUser", s.createUser)
	r.Handle("GET /api/v1/iam/users/{name}", "iam:GetUser", s.getUser, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}", "iam:DeleteUser", s.deleteUser, iamRes("user/{name}"))
	r.Handle("PUT /api/v1/iam/users/{name}/tags", "iam:TagUser", s.tagUser, iamRes("user/{name}"))
	r.Handle("PUT /api/v1/iam/users/{name}/password", "iam:UpdateLoginProfile", s.setUserPassword, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}/password", "iam:DeleteLoginProfile", s.deleteUserPassword, iamRes("user/{name}"))
	r.Handle("POST /api/v1/iam/users/{name}/policies", "iam:AttachUserPolicy", s.attachUserPolicy, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}/policies/{policy}", "iam:DetachUserPolicy", s.detachUserPolicy, iamRes("user/{name}"))
	r.Handle("PUT /api/v1/iam/users/{name}/inline-policies/{policy}", "iam:PutUserPolicy", s.putInline, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}/inline-policies/{policy}", "iam:DeleteUserPolicy", s.deleteInline, iamRes("user/{name}"))
	r.Handle("GET /api/v1/iam/users/{name}/access-keys", "iam:ListAccessKeys", s.listKeys, iamRes("user/{name}"))
	r.Handle("POST /api/v1/iam/users/{name}/access-keys", "iam:CreateAccessKey", s.createKeyRoute, iamRes("user/{name}"))
	r.Handle("PATCH /api/v1/iam/users/{name}/access-keys/{key}", "iam:UpdateAccessKey", s.updateKey, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}/access-keys/{key}", "iam:DeleteAccessKey", s.deleteKey, iamRes("user/{name}"))

	r.Handle("GET /api/v1/iam/groups", "iam:ListGroups", s.listGroups)
	r.Handle("POST /api/v1/iam/groups", "iam:CreateGroup", s.createGroup)
	r.Handle("GET /api/v1/iam/groups/{name}", "iam:GetGroup", s.getGroup, iamRes("group/{name}"))
	r.Handle("DELETE /api/v1/iam/groups/{name}", "iam:DeleteGroup", s.deleteGroup, iamRes("group/{name}"))
	r.Handle("POST /api/v1/iam/groups/{name}/members", "iam:AddUserToGroup", s.addMember, iamRes("group/{name}"))
	r.Handle("DELETE /api/v1/iam/groups/{name}/members/{user}", "iam:RemoveUserFromGroup", s.removeMember, iamRes("group/{name}"))
	r.Handle("POST /api/v1/iam/groups/{name}/policies", "iam:AttachGroupPolicy", s.attachGroupPolicy, iamRes("group/{name}"))
	r.Handle("DELETE /api/v1/iam/groups/{name}/policies/{policy}", "iam:DetachGroupPolicy", s.detachGroupPolicy, iamRes("group/{name}"))

	r.Handle("GET /api/v1/iam/policies", "iam:ListPolicies", s.listPolicies)
	r.Handle("POST /api/v1/iam/policies", "iam:CreatePolicy", s.createPolicy)
	r.Handle("GET /api/v1/iam/policies/{name}", "iam:GetPolicy", s.getPolicy, iamRes("policy/{name}"))
	r.Handle("PUT /api/v1/iam/policies/{name}", "iam:CreatePolicyVersion", s.updatePolicy, iamRes("policy/{name}"))
	r.Handle("DELETE /api/v1/iam/policies/{name}", "iam:DeletePolicy", s.deletePolicy, iamRes("policy/{name}"))
	r.Handle("POST /api/v1/iam/simulate", "iam:SimulatePrincipalPolicy", s.simulate)
}

func (s *Service) login(c *httpx.Ctx) (any, error) {
	var in struct{ Username, Password string }
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	ip := httpx.ClientIP(c.R)
	if s.throttled(ip, false) {
		return nil, core.Errf(http.StatusTooManyRequests, "TooManyRequests", "too many failed sign-in attempts; try again in a few minutes")
	}
	u, err := store.Get[User](s.env.Store, cUsers, in.Username)
	if err != nil || u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		s.throttled(ip, true)
		time.Sleep(300 * time.Millisecond)
		return nil, core.Errf(http.StatusUnauthorized, "AuthFailure", "incorrect user name or password")
	}
	token := "hcs_" + core.NewSecret(40)
	exp := time.Now().Add(sessionTTL)
	if err := store.Put(s.env.Store, cSessions, hashSecret(token), session{Token: "", UserName: u.Name, Expires: exp}); err != nil {
		return nil, err
	}
	_, _ = store.Update(s.env.Store, cUsers, u.Name, func(u *User) error { n := core.Now(); u.LastLogin = &n; return nil })
	s.pruneSessions()
	return map[string]any{"token": token, "expires": exp, "user": u.view(), "account_id": s.env.AccountID}, nil
}

func (s *Service) pruneSessions() {
	s.env.Store.Retain(cSessions, func(id string, raw json.RawMessage) bool {
		var ss session
		return json.Unmarshal(raw, &ss) == nil && time.Now().Before(ss.Expires)
	})
}

func (s *Service) logout(c *httpx.Ctx) (any, error) {
	h := strings.TrimPrefix(c.R.Header.Get("Authorization"), "Bearer ")
	if strings.HasPrefix(h, "hcs_") {
		_ = store.Delete(s.env.Store, cSessions, hashSecret(h))
	}
	return nil, nil
}

func (s *Service) whoami(c *httpx.Ctx) (any, error) {
	return map[string]any{"account_id": c.P.AccountID, "user_name": c.P.UserName, "arn": c.P.ARN, "root": c.P.Root, "region": s.env.Cfg.Region}, nil
}

func (s *Service) summary(c *httpx.Ctx) (any, error) {
	policies := store.List[Policy](s.env.Store, cPolicies)
	customer := 0
	for _, p := range policies {
		if !p.Managed {
			customer++
		}
	}
	return map[string]any{
		"account_id":        s.env.AccountID,
		"users":             len(store.List[User](s.env.Store, cUsers)),
		"groups":            len(store.List[Group](s.env.Store, cGroups)),
		"policies":          len(policies),
		"customer_policies": customer,
		"managed_policies":  len(policies) - customer,
		"access_keys":       len(store.List[AccessKey](s.env.Store, cKeys)),
	}, nil
}

func (s *Service) listUsers(c *httpx.Ctx) (any, error) {
	type keyInfo struct {
		count, active int
		lastUsed      *time.Time
	}
	keys := map[string]*keyInfo{}
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		ki := keys[k.UserName]
		if ki == nil {
			ki = &keyInfo{}
			keys[k.UserName] = ki
		}
		ki.count++
		if k.Status == "Active" {
			ki.active++
		}
		if k.LastUsed != nil && (ki.lastUsed == nil || k.LastUsed.After(*ki.lastUsed)) {
			ki.lastUsed = k.LastUsed
		}
	}
	out := []map[string]any{}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		v := u.view()
		ki := keys[u.Name]
		if ki == nil {
			ki = &keyInfo{}
		}
		v["access_key_count"], v["active_access_keys"], v["access_key_last_used"] = ki.count, ki.active, ki.lastUsed
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) createUser(c *httpx.Ctx) (any, error) {
	var in struct {
		Name     string    `json:"name"`
		Password string    `json:"password"`
		Groups   []string  `json:"groups"`
		Policies []string  `json:"policies"`
		Tags     core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := validName("user", in.Name); err != nil {
		return nil, err
	}
	if store.Has(s.env.Store, cUsers, in.Name) {
		return nil, core.Conflict("user %q already exists", in.Name)
	}
	u := s.newUser(in.Name)
	u.Tags = in.Tags
	if in.Password != "" {
		if err := setPassword(&u, in.Password); err != nil {
			return nil, err
		}
	}
	for _, p := range in.Policies {
		if !store.Has(s.env.Store, cPolicies, p) {
			return nil, core.NotFound("policy", p)
		}
		u.AttachedPolicies = addUnique(u.AttachedPolicies, p)
	}
	for _, g := range in.Groups {
		if !store.Has(s.env.Store, cGroups, g) {
			return nil, core.NotFound("group", g)
		}
		u.Groups = addUnique(u.Groups, g)
	}
	return u.view(), store.Put(s.env.Store, cUsers, u.Name, u)
}

func (s *Service) getUserOr404(name string) (User, error) {
	u, err := store.Get[User](s.env.Store, cUsers, name)
	if err != nil {
		return u, core.NotFound("user", name)
	}
	return u, nil
}

func (s *Service) getUser(c *httpx.Ctx) (any, error) {
	u, err := s.getUserOr404(c.Param("name"))
	if err != nil {
		return nil, err
	}
	v := u.view()
	keys := []AccessKey{}
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		if k.UserName == u.Name {
			k.SecretHash = ""
			keys = append(keys, k)
		}
	}
	v["access_keys"] = keys
	return v, nil
}

func (s *Service) deleteUser(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	u, err := s.getUserOr404(name)
	if err != nil {
		return nil, err
	}
	if u.Root {
		return nil, core.Conflict("the root user cannot be deleted")
	}
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		if k.UserName == name {
			_ = store.Delete(s.env.Store, cKeys, k.AccessKeyID)
		}
	}
	return nil, store.Delete(s.env.Store, cUsers, name)
}

func (s *Service) updateUser(name string, fn func(*User) error) (any, error) {
	u, err := store.Update(s.env.Store, cUsers, name, fn)
	if err == store.ErrNotFound {
		return nil, core.NotFound("user", name)
	}
	if err != nil {
		return nil, err
	}
	return u.view(), nil
}

func (s *Service) tagUser(c *httpx.Ctx) (any, error) {
	var in struct {
		Tags core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.updateUser(c.Param("name"), func(u *User) error { u.Tags = in.Tags; return nil })
}

func (s *Service) setUserPassword(c *httpx.Ctx) (any, error) {
	var in struct{ Password string }
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.updateUser(c.Param("name"), func(u *User) error { return setPassword(u, in.Password) })
}

func (s *Service) deleteUserPassword(c *httpx.Ctx) (any, error) {
	return s.updateUser(c.Param("name"), func(u *User) error {
		if u.Root {
			return core.Conflict("the root user must keep console access")
		}
		u.PasswordHash, u.PasswordSetAt = "", nil
		return nil
	})
}

func (s *Service) policyArg(c *httpx.Ctx) (string, error) {
	var in struct {
		Policy string `json:"policy"`
	}
	if err := c.Bind(&in); err != nil {
		return "", err
	}
	in.Policy = strings.TrimPrefix(in.Policy, s.policyARN(""))
	if !store.Has(s.env.Store, cPolicies, in.Policy) {
		return "", core.NotFound("policy", in.Policy)
	}
	return in.Policy, nil
}

func addUnique(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

func remove(list []string, v string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(x string) bool { return x == v })
}

func (s *Service) attachUserPolicy(c *httpx.Ctx) (any, error) {
	p, err := s.policyArg(c)
	if err != nil {
		return nil, err
	}
	return s.updateUser(c.Param("name"), func(u *User) error { u.AttachedPolicies = addUnique(u.AttachedPolicies, p); return nil })
}

func (s *Service) detachUserPolicy(c *httpx.Ctx) (any, error) {
	p := c.Param("policy")
	return s.updateUser(c.Param("name"), func(u *User) error {
		if u.Root && p == "AdministratorAccess" {
			return core.Conflict("AdministratorAccess cannot be detached from root")
		}
		if !slices.Contains(u.AttachedPolicies, p) {
			return core.NotFound("attached policy", p)
		}
		u.AttachedPolicies = remove(u.AttachedPolicies, p)
		return nil
	})
}

func (s *Service) putInline(c *httpx.Ctx) (any, error) {
	var d PolicyDocument
	if err := c.Bind(&d); err != nil {
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	name := c.Param("policy")
	if err := validName("policy", name); err != nil {
		return nil, err
	}
	if d.Version == "" {
		d.Version = "2012-10-17"
	}
	return s.updateUser(c.Param("name"), func(u *User) error {
		if u.InlinePolicies == nil {
			u.InlinePolicies = map[string]PolicyDocument{}
		}
		u.InlinePolicies[name] = d
		return nil
	})
}

func (s *Service) deleteInline(c *httpx.Ctx) (any, error) {
	return s.updateUser(c.Param("name"), func(u *User) error {
		if _, ok := u.InlinePolicies[c.Param("policy")]; !ok {
			return core.NotFound("inline policy", c.Param("policy"))
		}
		delete(u.InlinePolicies, c.Param("policy"))
		return nil
	})
}

func (s *Service) listKeys(c *httpx.Ctx) (any, error) {
	if _, err := s.getUserOr404(c.Param("name")); err != nil {
		return nil, err
	}
	out := []AccessKey{}
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		if k.UserName == c.Param("name") {
			k.SecretHash = ""
			out = append(out, k)
		}
	}
	return out, nil
}

func (s *Service) createKeyRoute(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if _, err := s.getUserOr404(name); err != nil {
		return nil, err
	}
	n := 0
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		if k.UserName == name {
			n++
		}
	}
	if n >= 2 {
		return nil, core.Errf(http.StatusConflict, "LimitExceeded", "user %q already has the maximum of 2 access keys", name)
	}
	k, secret, err := s.createKey(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"access_key_id": k.AccessKeyID, "secret_access_key": secret, "user_name": name, "status": k.Status, "created_at": k.CreatedAt}, nil
}

func (s *Service) keyOf(c *httpx.Ctx) (AccessKey, error) {
	k, err := store.Get[AccessKey](s.env.Store, cKeys, c.Param("key"))
	if err != nil || k.UserName != c.Param("name") {
		return k, core.NotFound("access key", c.Param("key"))
	}
	return k, nil
}

func (s *Service) updateKey(c *httpx.Ctx) (any, error) {
	var in struct{ Status string }
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Status != "Active" && in.Status != "Inactive" {
		return nil, errf("status must be Active or Inactive")
	}
	if _, err := s.keyOf(c); err != nil {
		return nil, err
	}
	k, err := store.Update(s.env.Store, cKeys, c.Param("key"), func(k *AccessKey) error { k.Status = in.Status; return nil })
	k.SecretHash = ""
	return k, err
}

func (s *Service) deleteKey(c *httpx.Ctx) (any, error) {
	if _, err := s.keyOf(c); err != nil {
		return nil, err
	}
	return nil, store.Delete(s.env.Store, cKeys, c.Param("key"))
}

// ---- groups ----

func (s *Service) groupMembers(name string) []string {
	out := []string{}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if slices.Contains(u.Groups, name) {
			out = append(out, u.Name)
		}
	}
	return out
}

func (s *Service) groupView(g Group) map[string]any {
	return map[string]any{"name": g.Name, "arn": g.ARN, "created_at": g.CreatedAt, "attached_policies": nz(g.AttachedPolicies), "members": s.groupMembers(g.Name)}
}

func (s *Service) listGroups(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, g := range store.List[Group](s.env.Store, cGroups) {
		out = append(out, s.groupView(g))
	}
	return out, nil
}

func (s *Service) createGroup(c *httpx.Ctx) (any, error) {
	var in struct {
		Name     string   `json:"name"`
		Policies []string `json:"policies"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := validName("group", in.Name); err != nil {
		return nil, err
	}
	if store.Has(s.env.Store, cGroups, in.Name) {
		return nil, core.Conflict("group %q already exists", in.Name)
	}
	for _, p := range in.Policies {
		if !store.Has(s.env.Store, cPolicies, p) {
			return nil, core.NotFound("policy", p)
		}
	}
	g := Group{Name: in.Name, ARN: s.env.ARN("iam", "group/"+in.Name), CreatedAt: core.Now(), AttachedPolicies: []string{}}
	for _, p := range in.Policies {
		g.AttachedPolicies = addUnique(g.AttachedPolicies, p)
	}
	return s.groupView(g), store.Put(s.env.Store, cGroups, g.Name, g)
}

func (s *Service) getGroup(c *httpx.Ctx) (any, error) {
	g, err := store.Get[Group](s.env.Store, cGroups, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("group", c.Param("name"))
	}
	return s.groupView(g), nil
}

func (s *Service) deleteGroup(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if !store.Has(s.env.Store, cGroups, name) {
		return nil, core.NotFound("group", name)
	}
	for _, m := range s.groupMembers(name) {
		_, _ = store.Update(s.env.Store, cUsers, m, func(u *User) error { u.Groups = remove(u.Groups, name); return nil })
	}
	return nil, store.Delete(s.env.Store, cGroups, name)
}

func (s *Service) addMember(c *httpx.Ctx) (any, error) {
	var in struct {
		User string `json:"user"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	g := c.Param("name")
	if !store.Has(s.env.Store, cGroups, g) {
		return nil, core.NotFound("group", g)
	}
	return s.updateUser(in.User, func(u *User) error { u.Groups = addUnique(u.Groups, g); return nil })
}

func (s *Service) removeMember(c *httpx.Ctx) (any, error) {
	g := c.Param("name")
	return s.updateUser(c.Param("user"), func(u *User) error {
		if !slices.Contains(u.Groups, g) {
			return core.NotFound("group membership", g)
		}
		u.Groups = remove(u.Groups, g)
		return nil
	})
}

func (s *Service) updateGroup(name string, fn func(*Group) error) (any, error) {
	g, err := store.Update(s.env.Store, cGroups, name, fn)
	if err == store.ErrNotFound {
		return nil, core.NotFound("group", name)
	}
	if err != nil {
		return nil, err
	}
	return s.groupView(g), nil
}

func (s *Service) attachGroupPolicy(c *httpx.Ctx) (any, error) {
	p, err := s.policyArg(c)
	if err != nil {
		return nil, err
	}
	return s.updateGroup(c.Param("name"), func(g *Group) error { g.AttachedPolicies = addUnique(g.AttachedPolicies, p); return nil })
}

func (s *Service) detachGroupPolicy(c *httpx.Ctx) (any, error) {
	return s.updateGroup(c.Param("name"), func(g *Group) error {
		if !slices.Contains(g.AttachedPolicies, c.Param("policy")) {
			return core.NotFound("attached policy", c.Param("policy"))
		}
		g.AttachedPolicies = remove(g.AttachedPolicies, c.Param("policy"))
		return nil
	})
}

// ---- policies ----

func (s *Service) attachments(name string) map[string][]string {
	users, groups := []string{}, []string{}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if slices.Contains(u.AttachedPolicies, name) {
			users = append(users, u.Name)
		}
	}
	for _, g := range store.List[Group](s.env.Store, cGroups) {
		if slices.Contains(g.AttachedPolicies, name) {
			groups = append(groups, g.Name)
		}
	}
	return map[string][]string{"users": users, "groups": groups}
}

func (s *Service) listPolicies(c *httpx.Ctx) (any, error) {
	scope := c.Query("scope") // all | managed | local
	out := []map[string]any{}
	for _, p := range store.List[Policy](s.env.Store, cPolicies) {
		if (scope == "managed" && !p.Managed) || (scope == "local" && p.Managed) {
			continue
		}
		a := s.attachments(p.Name)
		out = append(out, map[string]any{"name": p.Name, "arn": p.ARN, "description": p.Description, "managed": p.Managed,
			"created_at": p.CreatedAt, "updated_at": p.UpdatedAt, "attachment_count": len(a["users"]) + len(a["groups"])})
	}
	return out, nil
}

func (s *Service) createPolicy(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Document    PolicyDocument `json:"document"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := validName("policy", in.Name); err != nil {
		return nil, err
	}
	if err := in.Document.Validate(); err != nil {
		return nil, err
	}
	if store.Has(s.env.Store, cPolicies, in.Name) {
		return nil, core.Conflict("policy %q already exists", in.Name)
	}
	if in.Document.Version == "" {
		in.Document.Version = "2012-10-17"
	}
	p := Policy{Name: in.Name, ARN: s.policyARN(in.Name), Description: in.Description, Document: in.Document, CreatedAt: core.Now(), UpdatedAt: core.Now()}
	return p, store.Put(s.env.Store, cPolicies, p.Name, p)
}

func (s *Service) getPolicy(c *httpx.Ctx) (any, error) {
	p, err := store.Get[Policy](s.env.Store, cPolicies, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("policy", c.Param("name"))
	}
	return map[string]any{"policy": p, "attachments": s.attachments(p.Name)}, nil
}

func (s *Service) updatePolicy(c *httpx.Ctx) (any, error) {
	var in struct {
		Description *string        `json:"description"`
		Document    PolicyDocument `json:"document"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := in.Document.Validate(); err != nil {
		return nil, err
	}
	if in.Document.Version == "" {
		in.Document.Version = "2012-10-17"
	}
	p, err := store.Update(s.env.Store, cPolicies, c.Param("name"), func(p *Policy) error {
		if p.Managed {
			return core.Conflict("managed policy %q cannot be modified", p.Name)
		}
		p.Document, p.UpdatedAt = in.Document, core.Now()
		if in.Description != nil {
			p.Description = *in.Description
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("policy", c.Param("name"))
	}
	return p, err
}

func (s *Service) deletePolicy(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	p, err := store.Get[Policy](s.env.Store, cPolicies, name)
	if err != nil {
		return nil, core.NotFound("policy", name)
	}
	if p.Managed {
		return nil, core.Conflict("managed policy %q cannot be deleted", name)
	}
	if a := s.attachments(name); len(a["users"])+len(a["groups"]) > 0 {
		return nil, core.Errf(http.StatusConflict, "DeleteConflict", "policy %q is still attached to %d user(s) and %d group(s)", name, len(a["users"]), len(a["groups"]))
	}
	return nil, store.Delete(s.env.Store, cPolicies, name)
}

func (s *Service) simulate(c *httpx.Ctx) (any, error) {
	var in struct {
		User     string   `json:"user"`
		Actions  []string `json:"actions"`
		Resource string   `json:"resource"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	u, err := s.getUserOr404(in.User)
	if err != nil {
		return nil, err
	}
	if in.Resource == "" {
		in.Resource = "*"
	}
	docs := s.effectiveDocs(u)
	results := []map[string]string{}
	for _, a := range in.Actions {
		d := "implicitDeny"
		if u.Root {
			d = "allowed"
		} else {
			switch evaluate(docs, a, in.Resource) {
			case allow:
				d = "allowed"
			case explicitDeny:
				d = "explicitDeny"
			}
		}
		results = append(results, map[string]string{"action": a, "resource": in.Resource, "decision": d})
	}
	return results, nil
}

// String renders a bootstrap result for the terminal.
func (b *BootstrapResult) String() string {
	return fmt.Sprintf("account %s\n  root password:     %s\n  access key id:     %s\n  secret access key: %s", b.AccountID, b.RootPassword, b.AccessKeyID, b.SecretKey)
}
