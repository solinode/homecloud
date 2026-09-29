package iam

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const (
	cRoles     = "iam_roles"
	cTempCreds = "iam_temp_credentials"

	tempKeyPrefix     = "HCSA"
	defaultSessionTTL = time.Hour
)

// Sealer encrypts secrets at rest (the installation master key).
type Sealer interface {
	Encrypt(plain []byte) string
	Decrypt(ct string) ([]byte, error)
}

// Role is an identity with permissions that trusted principals (users,
// services such as Lambda or EC2) assume to get temporary credentials.
type Role struct {
	Name              string                    `json:"name"`
	ID                string                    `json:"id"`
	ARN               string                    `json:"arn"`
	Path              string                    `json:"path"`
	Description       string                    `json:"description"`
	TrustPolicy       PolicyDocument            `json:"assume_role_policy"`
	AttachedPolicies  []string                  `json:"attached_policies"`
	InlinePolicies    map[string]PolicyDocument `json:"inline_policies"`
	MaxSessionSeconds int                       `json:"max_session_duration"`
	CreatedAt         time.Time                 `json:"created_at"`
	LastUsed          *time.Time                `json:"last_used,omitempty"`
	Tags              core.Tags                 `json:"tags,omitempty"`
}

// tempCred is a set of temporary credentials issued by STS.
type tempCred struct {
	AccessKeyID string    `json:"access_key_id"`
	SecretCT    string    `json:"secret_ct"`
	TokenHash   string    `json:"token_hash"`
	RoleName    string    `json:"role_name,omitempty"`
	RoleID      string    `json:"role_id,omitempty"`
	SessionName string    `json:"session_name,omitempty"`
	UserName    string    `json:"user_name,omitempty"` // GetSessionToken: the user's own permissions
	UserID      string    `json:"user_id,omitempty"`
	IssuedTo    string    `json:"issued_to"` // caller ARN or service principal
	Expires     time.Time `json:"expires"`
}

// Credentials are temporary credentials handed to a caller.
type Credentials struct {
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	SessionToken    string    `json:"session_token"`
	Expiration      time.Time `json:"expiration"`
	AssumedRoleARN  string    `json:"assumed_role_arn,omitempty"`
	AssumedRoleID   string    `json:"assumed_role_id,omitempty"`
}

func (s *Service) roleARN(name, path string) string {
	return s.env.ARN("iam", "role"+path+name)
}

// roleName accepts a role name or ARN.
func roleName(ref string) string {
	ref = core.CanonicalARN(ref)
	if i := strings.Index(ref, ":role/"); i >= 0 {
		ref = ref[i+len(":role/"):]
		if j := strings.LastIndexByte(ref, '/'); j >= 0 {
			ref = ref[j+1:] // strip the path
		}
	}
	return ref
}

// GetRole returns a role by name or ARN.
func (s *Service) GetRole(ref string) (Role, error) {
	r, err := store.Get[Role](s.env.Store, cRoles, roleName(ref))
	if err != nil {
		return r, core.Errf(http.StatusNotFound, "NoSuchEntity", "role %s does not exist", roleName(ref))
	}
	return r, nil
}

func (s *Service) roleDocs(r Role) []PolicyDocument {
	var docs []PolicyDocument
	for _, n := range r.AttachedPolicies {
		if p, err := store.Get[Policy](s.env.Store, cPolicies, n); err == nil {
			docs = append(docs, p.Document)
		}
	}
	for _, d := range r.InlinePolicies {
		docs = append(docs, d)
	}
	return docs
}

func (s *Service) rolePrincipal(r Role, t tempCred) *httpx.Principal {
	docs := s.roleDocs(r)
	return &httpx.Principal{
		AccountID: s.env.AccountID, UserName: "assumed-role/" + r.Name + "/" + t.SessionName,
		ARN:       s.env.ARN("sts", "assumed-role/"+r.Name+"/"+t.SessionName),
		AccessKey: t.AccessKeyID, RoleName: r.Name, SessionName: t.SessionName,
		Can: func(action, resource string) bool { return evaluate(docs, action, resource) == allow },
	}
}

// issue mints temporary credentials.
func (s *Service) issue(t tempCred, ttl time.Duration) (Credentials, error) {
	if s.Seal == nil {
		return Credentials{}, fmt.Errorf("temporary credentials need the master key")
	}
	secret := core.NewSecret(40)
	token := "hcst_" + core.NewSecret(96)
	t.AccessKeyID = tempKeyPrefix + core.NewSecret(16)
	t.AccessKeyID = tempKeyPrefix + strings.ToUpper(t.AccessKeyID[len(tempKeyPrefix):])
	t.SecretCT, t.TokenHash = s.Seal.Encrypt([]byte(secret)), hashSecret(token)
	t.Expires = core.Now().Add(ttl)
	if err := store.Put(s.env.Store, cTempCreds, t.AccessKeyID, t); err != nil {
		return Credentials{}, err
	}
	return Credentials{AccessKeyID: t.AccessKeyID, SecretAccessKey: secret, SessionToken: token, Expiration: t.Expires}, nil
}

func stsTTL(seconds int, r *Role) (time.Duration, error) {
	if seconds == 0 {
		return defaultSessionTTL, nil
	}
	maxSecs := 12 * 3600
	if r != nil {
		maxSecs = r.MaxSessionSeconds
	}
	if seconds < 900 || seconds > maxSecs {
		return 0, core.Errf(http.StatusBadRequest, "ValidationError", "DurationSeconds must be between 900 and %d", maxSecs)
	}
	return time.Duration(seconds) * time.Second, nil
}

var sessionNameRe = nameRe // [\w+=,.@-]{1,64}

// AssumeRole issues credentials for role to the calling principal p, if the
// role's trust policy and (for account-wide trust) p's own policies allow it.
func (s *Service) AssumeRole(p *httpx.Principal, ref, sessionName string, seconds int) (Credentials, error) {
	r, err := s.GetRole(ref)
	if err != nil {
		return Credentials{}, err
	}
	if !sessionNameRe.MatchString(sessionName) || len(sessionName) < 2 {
		return Credentials{}, core.Errf(http.StatusBadRequest, "ValidationError", "RoleSessionName must be 2-64 characters of [\\w+=,.@-]")
	}
	ttl, err := stsTTL(seconds, &r)
	if err != nil {
		return Credentials{}, err
	}
	if p.RoleName != "" && ttl > time.Hour {
		ttl = time.Hour // role chaining is limited to one hour, as in AWS
	}
	denied := core.Errf(http.StatusForbidden, "AccessDenied", "User: %s is not authorized to perform: sts:AssumeRole on resource: %s", p.ARN, r.ARN)
	switch r.TrustPolicy.trusts("sts:AssumeRole", p.ARN, "", s.env.AccountID) {
	case explicitDeny, implicitDeny:
		return Credentials{}, denied
	}
	// A trust policy naming the account delegates the decision to the caller's policies.
	if !trustsExactly(r.TrustPolicy, p.ARN) && !p.Root && !p.Can("sts:AssumeRole", r.ARN) {
		return Credentials{}, denied
	}
	c, err := s.issue(tempCred{RoleName: r.Name, RoleID: r.ID, SessionName: sessionName, IssuedTo: p.ARN}, ttl)
	if err != nil {
		return c, err
	}
	s.touchRole(r.Name)
	c.AssumedRoleARN = s.env.ARN("sts", "assumed-role/"+r.Name+"/"+sessionName)
	c.AssumedRoleID = r.ID + ":" + sessionName
	return c, nil
}

func trustsExactly(d PolicyDocument, callerARN string) bool {
	for _, st := range d.Statement {
		if st.Effect == "Allow" && st.Principal != nil && slices.ContainsFunc(st.Principal.AWS, func(a string) bool { return core.CanonicalARN(a) == callerARN }) {
			return true
		}
	}
	return false
}

// AssumeRoleForService issues credentials for role to an AWS service principal
// (e.g. "lambda.amazonaws.com") acting on the account's behalf, if the role
// trusts that service. The service must check iam:PassRole when the role is
// configured.
func (s *Service) AssumeRoleForService(ref, service, sessionName string, ttl time.Duration) (Credentials, error) {
	r, err := s.GetRole(ref)
	if err != nil {
		return Credentials{}, err
	}
	if r.TrustPolicy.trusts("sts:AssumeRole", "", service, s.env.AccountID) != allow {
		return Credentials{}, core.Errf(http.StatusForbidden, "AccessDenied", "role %s does not trust %s (add it to the role's trust policy)", r.Name, service)
	}
	c, err := s.issue(tempCred{RoleName: r.Name, RoleID: r.ID, SessionName: sessionName, IssuedTo: service}, ttl)
	if err != nil {
		return c, err
	}
	s.touchRole(r.Name)
	c.AssumedRoleARN = s.env.ARN("sts", "assumed-role/"+r.Name+"/"+sessionName)
	return c, nil
}

// ServiceRolePrincipal returns the principal of role acting for an AWS
// service principal (e.g. "states.amazonaws.com") inside HomeCloud, if the
// role trusts that service. Unlike AssumeRoleForService it issues no
// credentials; the principal reflects the role's policies at call time. The
// service must check iam:PassRole when the role is configured.
func (s *Service) ServiceRolePrincipal(ref, service, sessionName string) (*httpx.Principal, error) {
	r, err := s.GetRole(ref)
	if err != nil {
		return nil, err
	}
	if r.TrustPolicy.trusts("sts:AssumeRole", "", service, s.env.AccountID) != allow {
		return nil, core.Errf(http.StatusForbidden, "AccessDenied", "role %s does not trust %s (add it to the role's trust policy)", r.Name, service)
	}
	s.touchRole(r.Name)
	return s.rolePrincipal(r, tempCred{RoleName: r.Name, RoleID: r.ID, SessionName: sessionName, IssuedTo: service}), nil
}

// SessionToken issues temporary credentials carrying a user's own permissions.
func (s *Service) SessionToken(p *httpx.Principal, seconds int) (Credentials, error) {
	if p.RoleName != "" {
		return Credentials{}, core.Errf(http.StatusForbidden, "AccessDenied", "GetSessionToken cannot be called with temporary role credentials")
	}
	ttl, err := stsTTL(seconds, nil)
	if err != nil {
		return Credentials{}, err
	}
	u, err := s.getUserOr404(p.UserName)
	if err != nil {
		return Credentials{}, err
	}
	return s.issue(tempCred{UserName: u.Name, UserID: u.ID, IssuedTo: p.ARN}, ttl)
}

func (s *Service) touchRole(name string) {
	_, _ = store.Update(s.env.Store, cRoles, name, func(r *Role) error {
		if r.LastUsed == nil || time.Since(*r.LastUsed) > time.Minute {
			n := core.Now()
			r.LastUsed = &n
		}
		return nil
	})
}

// SigningSecret resolves an access key (long-term or temporary) to its secret
// and principal, for AWS Signature Version 4.
func (s *Service) SigningSecret(akid, token string) (string, *httpx.Principal, error) {
	invalid := core.Errf(http.StatusForbidden, "InvalidClientTokenId", "The security token included in the request is invalid.")
	if s.Seal == nil {
		return "", nil, invalid
	}
	if strings.HasPrefix(akid, tempKeyPrefix) {
		t, err := store.Get[tempCred](s.env.Store, cTempCreds, akid)
		if err != nil || token == "" || subtle.ConstantTimeCompare([]byte(t.TokenHash), []byte(hashSecret(token))) != 1 {
			return "", nil, invalid
		}
		if time.Now().After(t.Expires) {
			return "", nil, core.Errf(http.StatusForbidden, "ExpiredToken", "The security token included in the request is expired")
		}
		secret, err := s.Seal.Decrypt(t.SecretCT)
		if err != nil {
			return "", nil, invalid
		}
		p, err := s.tempPrincipal(t)
		if err != nil {
			return "", nil, err
		}
		return string(secret), p, nil
	}
	k, err := store.Get[AccessKey](s.env.Store, cKeys, akid)
	if err != nil {
		return "", nil, invalid
	}
	if k.Status != "Active" {
		return "", nil, core.Errf(http.StatusForbidden, "InvalidClientTokenId", "access key %s is inactive", akid)
	}
	if k.SecretCT == "" {
		return "", nil, core.Errf(http.StatusForbidden, "InvalidClientTokenId",
			"access key %s was created before HomeCloud supported AWS signatures: use it once with the homecloud CLI, or create a new key", akid)
	}
	secret, err := s.Seal.Decrypt(k.SecretCT)
	if err != nil {
		return "", nil, invalid
	}
	u, err := store.Get[User](s.env.Store, cUsers, k.UserName)
	if err != nil {
		return "", nil, invalid
	}
	s.touchKey(k)
	return string(secret), s.principal(u, akid), nil
}

func (s *Service) tempPrincipal(t tempCred) (*httpx.Principal, error) {
	gone := core.Errf(http.StatusForbidden, "InvalidClientTokenId", "The security token included in the request is invalid.")
	if t.RoleName != "" {
		r, err := store.Get[Role](s.env.Store, cRoles, t.RoleName)
		if err != nil || r.ID != t.RoleID {
			return nil, gone // the role was deleted (or re-created)
		}
		return s.rolePrincipal(r, t), nil
	}
	u, err := store.Get[User](s.env.Store, cUsers, t.UserName)
	if err != nil || u.ID != t.UserID {
		return nil, gone
	}
	p := s.principal(u, t.AccessKeyID)
	return p, nil
}

func (s *Service) touchKey(k AccessKey) {
	if k.LastUsed == nil || time.Since(*k.LastUsed) > time.Minute {
		_, _ = store.Update(s.env.Store, cKeys, k.AccessKeyID, func(k *AccessKey) error { n := core.Now(); k.LastUsed = &n; return nil })
	}
}

// PurgeExpired removes expired temporary credentials.
func (s *Service) PurgeExpired() {
	s.env.Store.Retain(cTempCreds, func(id string, raw json.RawMessage) bool {
		var t tempCred
		return json.Unmarshal(raw, &t) == nil && time.Now().Before(t.Expires)
	})
}

// RevokeRoleSessions invalidates every outstanding credential for a role.
func (s *Service) RevokeRoleSessions(role string) {
	s.env.Store.Retain(cTempCreds, func(id string, raw json.RawMessage) bool {
		var t tempCred
		return json.Unmarshal(raw, &t) == nil && t.RoleName != role
	})
}

// ---- native API: roles ----

func (s *Service) roleRoutes(r *httpx.Router) {
	res := httpx.Res("arn:aws:iam::{account}:role/{name}")
	r.Handle("GET /api/v1/iam/roles", "iam:ListRoles", s.listRoles)
	r.Handle("POST /api/v1/iam/roles", "iam:CreateRole", s.createRoleRoute)
	r.Handle("GET /api/v1/iam/roles/{name}", "iam:GetRole", s.getRoleRoute, res)
	r.Handle("DELETE /api/v1/iam/roles/{name}", "iam:DeleteRole", s.deleteRoleRoute, res)
	r.Handle("PATCH /api/v1/iam/roles/{name}", "iam:UpdateRole", s.updateRoleRoute, res)
	r.Handle("PUT /api/v1/iam/roles/{name}/trust-policy", "iam:UpdateAssumeRolePolicy", s.putTrustRoute, res)
	r.Handle("POST /api/v1/iam/roles/{name}/policies", "iam:AttachRolePolicy", s.attachRolePolicyRoute, res)
	r.Handle("DELETE /api/v1/iam/roles/{name}/policies/{policy}", "iam:DetachRolePolicy", s.detachRolePolicyRoute, res)
	r.Handle("PUT /api/v1/iam/roles/{name}/inline-policies/{policy}", "iam:PutRolePolicy", s.putRoleInlineRoute, res)
	r.Handle("DELETE /api/v1/iam/roles/{name}/inline-policies/{policy}", "iam:DeleteRolePolicy", s.deleteRoleInlineRoute, res)
	r.Handle("POST /api/v1/iam/roles/{name}/revoke-sessions", "iam:PutRolePolicy", s.revokeRoute, res)
	r.Handle("POST /api/v1/sts/assume-role", "sts:AssumeRole", s.assumeRoleRoute)
}

func (s *Service) roleView(r Role) map[string]any {
	return map[string]any{"name": r.Name, "id": r.ID, "arn": r.ARN, "path": r.Path, "description": r.Description,
		"assume_role_policy": r.TrustPolicy, "attached_policies": nz(r.AttachedPolicies), "inline_policies": r.InlinePolicies,
		"max_session_duration": r.MaxSessionSeconds, "created_at": r.CreatedAt, "last_used": r.LastUsed, "tags": r.Tags,
		"trusted_services": trustedServices(r.TrustPolicy)}
}

func trustedServices(d PolicyDocument) []string {
	out := []string{}
	for _, st := range d.Statement {
		if st.Effect == "Allow" && st.Principal != nil {
			out = append(out, st.Principal.Service...)
		}
	}
	return out
}

func (s *Service) listRoles(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, r := range store.List[Role](s.env.Store, cRoles) {
		out = append(out, s.roleView(r))
	}
	return out, nil
}

// RoleInput creates a role.
type RoleInput struct {
	Name              string          `json:"name"`
	Path              string          `json:"path"`
	Description       string          `json:"description"`
	TrustPolicy       json.RawMessage `json:"assume_role_policy"`
	MaxSessionSeconds int             `json:"max_session_duration"`
	Policies          []string        `json:"policies"` // managed policies to attach
	Tags              core.Tags       `json:"tags"`
}

// ParseTrust parses a trust policy from JSON (a document, or a JSON string holding one).
func ParseTrust(raw json.RawMessage) (PolicyDocument, error) {
	var d PolicyDocument
	var str string
	if json.Unmarshal(raw, &str) == nil {
		raw = json.RawMessage(str)
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, core.Errf(http.StatusBadRequest, "MalformedPolicyDocument", "trust policy is not valid JSON: %v", err)
	}
	if d.Version == "" {
		d.Version = "2012-10-17"
	}
	if err := d.ValidateTrust(); err != nil {
		return d, core.Errf(http.StatusBadRequest, "MalformedPolicyDocument", "%s", strings.TrimPrefix(err.Error(), "ValidationError: "))
	}
	return d, nil
}

// CreateRole creates a role; the caller must be allowed to attach each policy.
func (s *Service) CreateRole(in RoleInput, canAttach func(policyARN string) error) (Role, error) {
	if err := validName("role", in.Name); err != nil {
		return Role{}, err
	}
	if in.Path == "" {
		in.Path = "/"
	}
	if !strings.HasPrefix(in.Path, "/") || !strings.HasSuffix(in.Path, "/") {
		return Role{}, core.BadRequest("path must begin and end with /")
	}
	trust, err := ParseTrust(in.TrustPolicy)
	if err != nil {
		return Role{}, err
	}
	if in.MaxSessionSeconds == 0 {
		in.MaxSessionSeconds = 3600
	}
	if in.MaxSessionSeconds < 3600 || in.MaxSessionSeconds > 43200 {
		return Role{}, core.BadRequest("max_session_duration must be 3600-43200 seconds")
	}
	for _, p := range in.Policies {
		n := strings.TrimPrefix(core.CanonicalARN(p), s.policyARN(""))
		if !store.Has(s.env.Store, cPolicies, n) {
			return Role{}, core.NotFound("policy", n)
		}
		if err := canAttach(s.policyARN(n)); err != nil {
			return Role{}, err
		}
	}
	r := Role{Name: in.Name, ID: "HCRO" + strings.ToUpper(core.RandHex(16)), ARN: s.roleARN(in.Name, in.Path), Path: in.Path,
		Description: in.Description, TrustPolicy: trust, InlinePolicies: map[string]PolicyDocument{}, MaxSessionSeconds: in.MaxSessionSeconds,
		CreatedAt: core.Now(), Tags: in.Tags, AttachedPolicies: []string{}}
	for _, p := range in.Policies {
		r.AttachedPolicies = addUnique(r.AttachedPolicies, strings.TrimPrefix(core.CanonicalARN(p), s.policyARN("")))
	}
	s.roleMu.Lock()
	defer s.roleMu.Unlock()
	if store.Has(s.env.Store, cRoles, r.Name) {
		return Role{}, core.Errf(http.StatusConflict, "EntityAlreadyExists", "role %q already exists", r.Name)
	}
	return r, store.Put(s.env.Store, cRoles, r.Name, r)
}

func (s *Service) createRoleRoute(c *httpx.Ctx) (any, error) {
	var in RoleInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	r, err := s.CreateRole(in, func(arn string) error { return c.Authorize("iam:AttachRolePolicy", s.roleARN(in.Name, "/")) })
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

func (s *Service) getRoleRoute(c *httpx.Ctx) (any, error) {
	r, err := s.GetRole(c.Param("name"))
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

// DeleteRole deletes a role and revokes its sessions. Like AWS, a role with
// policies attached must have them detached first.
func (s *Service) DeleteRole(name string) error {
	r, err := s.GetRole(name)
	if err != nil {
		return err
	}
	if len(r.AttachedPolicies) > 0 || len(r.InlinePolicies) > 0 {
		return core.Errf(http.StatusConflict, "DeleteConflict", "role %s still has policies; detach and delete them first", r.Name)
	}
	if err := store.Delete(s.env.Store, cRoles, r.Name); err != nil {
		return err
	}
	s.RevokeRoleSessions(r.Name)
	return nil
}

func (s *Service) deleteRoleRoute(c *httpx.Ctx) (any, error) {
	if c.Query("force") == "true" {
		_, _ = s.UpdateRole(c.Param("name"), func(r *Role) error {
			r.AttachedPolicies, r.InlinePolicies = []string{}, map[string]PolicyDocument{}
			return nil
		})
	}
	return nil, s.DeleteRole(c.Param("name"))
}

// UpdateRole applies fn to a role.
func (s *Service) UpdateRole(name string, fn func(*Role) error) (Role, error) {
	r, err := store.Update(s.env.Store, cRoles, roleName(name), fn)
	if err == store.ErrNotFound {
		return r, core.Errf(http.StatusNotFound, "NoSuchEntity", "role %s does not exist", roleName(name))
	}
	return r, err
}

func (s *Service) updateRoleRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Description       *string `json:"description"`
		MaxSessionSeconds *int    `json:"max_session_duration"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	r, err := s.UpdateRole(c.Param("name"), func(r *Role) error {
		if in.Description != nil {
			r.Description = *in.Description
		}
		if in.MaxSessionSeconds != nil {
			if *in.MaxSessionSeconds < 3600 || *in.MaxSessionSeconds > 43200 {
				return core.BadRequest("max_session_duration must be 3600-43200 seconds")
			}
			r.MaxSessionSeconds = *in.MaxSessionSeconds
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

func (s *Service) putTrustRoute(c *httpx.Ctx) (any, error) {
	var raw json.RawMessage
	if err := c.Bind(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	d, err := ParseTrust(raw)
	if err != nil {
		return nil, err
	}
	r, err := s.UpdateRole(c.Param("name"), func(r *Role) error { r.TrustPolicy = d; return nil })
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

// AttachRolePolicy attaches a managed policy (name or ARN) to a role.
func (s *Service) AttachRolePolicy(role, policy string) (Role, error) {
	n := strings.TrimPrefix(core.CanonicalARN(policy), s.policyARN(""))
	if !store.Has(s.env.Store, cPolicies, n) {
		return Role{}, core.Errf(http.StatusNotFound, "NoSuchEntity", "policy %s does not exist", n)
	}
	return s.UpdateRole(role, func(r *Role) error { r.AttachedPolicies = addUnique(r.AttachedPolicies, n); return nil })
}

// DetachRolePolicy detaches a managed policy (name or ARN) from a role.
func (s *Service) DetachRolePolicy(role, policy string) (Role, error) {
	n := strings.TrimPrefix(core.CanonicalARN(policy), s.policyARN(""))
	return s.UpdateRole(role, func(r *Role) error {
		if !slices.Contains(r.AttachedPolicies, n) {
			return core.Errf(http.StatusNotFound, "NoSuchEntity", "policy %s is not attached to role %s", n, r.Name)
		}
		r.AttachedPolicies = remove(r.AttachedPolicies, n)
		return nil
	})
}

func (s *Service) attachRolePolicyRoute(c *httpx.Ctx) (any, error) {
	p, err := s.policyArg(c)
	if err != nil {
		return nil, err
	}
	r, err := s.AttachRolePolicy(c.Param("name"), p)
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

func (s *Service) detachRolePolicyRoute(c *httpx.Ctx) (any, error) {
	r, err := s.DetachRolePolicy(c.Param("name"), c.Param("policy"))
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

// PutRolePolicy sets an inline policy on a role.
func (s *Service) PutRolePolicy(role, name string, d PolicyDocument) (Role, error) {
	if err := validName("policy", name); err != nil {
		return Role{}, err
	}
	if err := d.Validate(); err != nil {
		return Role{}, core.Errf(http.StatusBadRequest, "MalformedPolicyDocument", "%s", strings.TrimPrefix(err.Error(), "ValidationError: "))
	}
	if d.Version == "" {
		d.Version = "2012-10-17"
	}
	return s.UpdateRole(role, func(r *Role) error {
		if r.InlinePolicies == nil {
			r.InlinePolicies = map[string]PolicyDocument{}
		}
		r.InlinePolicies[name] = d
		return nil
	})
}

// DeleteRolePolicy removes an inline policy from a role.
func (s *Service) DeleteRolePolicy(role, name string) (Role, error) {
	return s.UpdateRole(role, func(r *Role) error {
		if _, ok := r.InlinePolicies[name]; !ok {
			return core.Errf(http.StatusNotFound, "NoSuchEntity", "role %s has no inline policy %s", r.Name, name)
		}
		delete(r.InlinePolicies, name)
		return nil
	})
}

func (s *Service) putRoleInlineRoute(c *httpx.Ctx) (any, error) {
	var d PolicyDocument
	if err := c.Bind(&d); err != nil {
		return nil, err
	}
	r, err := s.PutRolePolicy(c.Param("name"), c.Param("policy"), d)
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

func (s *Service) deleteRoleInlineRoute(c *httpx.Ctx) (any, error) {
	r, err := s.DeleteRolePolicy(c.Param("name"), c.Param("policy"))
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

func (s *Service) revokeRoute(c *httpx.Ctx) (any, error) {
	if _, err := s.GetRole(c.Param("name")); err != nil {
		return nil, err
	}
	s.RevokeRoleSessions(c.Param("name"))
	return nil, nil
}

func (s *Service) assumeRoleRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Role            string `json:"role"`
		SessionName     string `json:"session_name"`
		DurationSeconds int    `json:"duration_seconds"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.SessionName == "" {
		in.SessionName = "homecloud-" + core.RandHex(8)
	}
	return s.AssumeRole(c.P, in.Role, in.SessionName, in.DurationSeconds)
}

func store_getUser(s *Service, name string) (User, error) {
	return store.Get[User](s.env.Store, cUsers, name)
}
