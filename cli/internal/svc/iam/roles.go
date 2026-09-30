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
	LastUsedRegion    string                    `json:"last_used_region,omitempty"`
	Tags              core.Tags                 `json:"tags,omitempty"`
	// PermissionsBoundary is the ARN of a managed policy that limits the role's permissions.
	PermissionsBoundary string `json:"permissions_boundary,omitempty"`
	// ServiceLinked roles belong to an AWS service (CreateServiceLinkedRole).
	ServiceLinked string `json:"service_linked,omitempty"`
}

func (r Role) path() string {
	if r.Path == "" {
		return "/"
	}
	return r.Path
}

// tempCred is a set of temporary credentials issued by STS.
type tempCred struct {
	AccessKeyID string     `json:"access_key_id"`
	SecretCT    string     `json:"secret_ct"`
	TokenHash   string     `json:"token_hash"`
	RoleName    string     `json:"role_name,omitempty"`
	RoleID      string     `json:"role_id,omitempty"`
	SessionName string     `json:"session_name,omitempty"`
	UserName    string     `json:"user_name,omitempty"` // GetSessionToken: the user's own permissions
	UserID      string     `json:"user_id,omitempty"`
	IssuedTo    string     `json:"issued_to"` // caller ARN or service principal
	Issued      *time.Time `json:"issued,omitempty"`
	Expires     time.Time  `json:"expires"`
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

// resolveResource maps the role a caller named in an iam:PassRole check (a bare
// name, or an ARN with a wrong path or account part) to the role's real ARN, so
// an Allow or Deny written for the real ARN applies whatever the spelling.
func (s *Service) resolveResource(action, resource string) string {
	if action == "iam:PassRole" {
		if r, err := s.GetRole(resource); err == nil {
			return r.ARN
		}
	}
	return resource
}

// GetRole returns a role by name or ARN.
func (s *Service) GetRole(ref string) (Role, error) {
	r, err := store.Get[Role](s.env.Store, cRoles, roleName(ref))
	if err != nil {
		return r, noSuchEntity("The role with name %s cannot be found.", roleName(ref))
	}
	return r, nil
}

func (s *Service) roleDocs(r Role) []PolicyDocument {
	docs := s.managedDocs(r.AttachedPolicies)
	for _, d := range r.InlinePolicies {
		docs = append(docs, d)
	}
	return docs
}

// roleContext is the identity part of a role session's request context.
func (s *Service) roleContext(r Role, t tempCred) CondContext {
	c := CondContext{
		"aws:userid": {r.ID + ":" + t.SessionName}, "aws:principalarn": {r.ARN},
		"aws:principalaccount": {s.env.AccountID}, "aws:principaltype": {"AssumedRole"},
		"aws:tokenissuetime": {t.Expires.Add(-time.Hour).UTC().Format(time.RFC3339)},
	}
	if t.Issued != nil {
		c["aws:tokenissuetime"] = []string{t.Issued.UTC().Format(time.RFC3339)}
	}
	if strings.HasSuffix(t.IssuedTo, ".amazonaws.com") {
		c["aws:principalservicename"] = []string{t.IssuedTo}
	}
	for k, v := range r.Tags {
		c["aws:principaltag/"+strings.ToLower(k)] = []string{v}
	}
	return c
}

func (s *Service) rolePrincipal(r Role, t tempCred) *httpx.Principal {
	docs := s.roleDocs(r)
	boundary := s.boundaryDoc(r.PermissionsBoundary)
	p := &httpx.Principal{
		AccountID: s.env.AccountID, UserName: "assumed-role/" + r.Name + "/" + t.SessionName,
		ARN:       s.env.ARN("sts", "assumed-role/"+r.Name+"/"+t.SessionName),
		AccessKey: t.AccessKeyID, RoleName: r.Name, SessionName: t.SessionName,
		Context: s.roleContext(r, t), ResolveResource: s.resolveResource,
	}
	p.Can = func(action, resource string) bool {
		return decide(docs, boundary, action, resource, CondContext(p.Context)) == allow
	}
	p.Identity = identityFunc(p, docs, boundary)
	p.Mentions = mentionsFunc(docs, boundary)
	return p
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
	now := core.Now()
	t.Issued, t.Expires = &now, now.Add(ttl)
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
func (s *Service) AssumeRole(p *httpx.Principal, ref, sessionName string, seconds int, externalID string) (Credentials, error) {
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
	ctx := CondContext(p.Context).with("sts:ExternalId", externalID, "sts:RoleSessionName", sessionName)
	switch r.TrustPolicy.trusts("sts:AssumeRole", p.ARN, "", s.env.AccountID, ctx) {
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
	s.touchRole(r.Name, regionOf(p))
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
	if r.TrustPolicy.trusts("sts:AssumeRole", "", service, s.env.AccountID, nil) != allow {
		return Credentials{}, core.Errf(http.StatusForbidden, "AccessDenied", "role %s does not trust %s (add it to the role's trust policy)", r.Name, service)
	}
	c, err := s.issue(tempCred{RoleName: r.Name, RoleID: r.ID, SessionName: sessionName, IssuedTo: service}, ttl)
	if err != nil {
		return c, err
	}
	s.touchRole(r.Name, core.Region)
	c.AssumedRoleARN = s.env.ARN("sts", "assumed-role/"+r.Name+"/"+sessionName)
	return c, nil
}

// ServiceRole returns a role (by name or ARN) if its trust policy lets the AWS
// service principal assume it, without issuing credentials. Services use it to
// validate a role when it is configured (after checking iam:PassRole).
func (s *Service) ServiceRole(ref, service string) (Role, error) {
	r, err := s.GetRole(ref)
	if err != nil {
		return r, err
	}
	if r.TrustPolicy.trusts("sts:AssumeRole", "", service, s.env.AccountID, nil) != allow {
		return r, core.Errf(http.StatusForbidden, "AccessDenied", "role %s does not trust %s (add it to the role's trust policy)", r.Name, service)
	}
	return r, nil
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
	if r.TrustPolicy.trusts("sts:AssumeRole", "", service, s.env.AccountID, nil) != allow {
		return nil, core.Errf(http.StatusForbidden, "AccessDenied", "role %s does not trust %s (add it to the role's trust policy)", r.Name, service)
	}
	s.touchRole(r.Name, core.Region)
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

func regionOf(p *httpx.Principal) string {
	if v := p.Context["aws:requestedregion"]; len(v) > 0 && v[0] != "" {
		return v[0]
	}
	return core.Region
}

func (s *Service) touchRole(name, region string) {
	_, _ = store.Update(s.env.Store, cRoles, name, func(r *Role) error {
		if r.LastUsed == nil || time.Since(*r.LastUsed) > time.Minute {
			n := core.Now()
			r.LastUsed, r.LastUsedRegion = &n, region
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
