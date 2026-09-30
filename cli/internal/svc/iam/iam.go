// Package iam implements identity and access management: users, groups,
// roles, instance profiles, managed and inline policies, access keys, console
// sessions and the policy evaluation every API request goes through.
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

	maxAccessKeys = 2
	maxAttached   = 20 // managed policies per user, group or role
)

func errf(format string, a ...any) error { return core.BadRequest(format, a...) }

// IAM error codes, as the AWS API reports them.
func noSuchEntity(format string, a ...any) error {
	return core.Errf(http.StatusNotFound, "NoSuchEntity", format, a...)
}
func alreadyExists(format string, a ...any) error {
	return core.Errf(http.StatusConflict, "EntityAlreadyExists", format, a...)
}
func deleteConflict(format string, a ...any) error {
	return core.Errf(http.StatusConflict, "DeleteConflict", format, a...)
}
func limitExceeded(format string, a ...any) error {
	return core.Errf(http.StatusConflict, "LimitExceeded", format, a...)
}
func unmodifiable(format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, "UnmodifiableEntity", format, a...)
}
func invalidInput(format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, "InvalidInput", format, a...)
}

// authz checks one permission for the caller (Ctx.Authorize or awsapi.Req.Authorize).
type authz func(action, resource string) error

type User struct {
	Name                  string                    `json:"name"`
	ID                    string                    `json:"id"`
	ARN                   string                    `json:"arn"`
	Path                  string                    `json:"path,omitempty"`
	Root                  bool                      `json:"root,omitempty"`
	CreatedAt             time.Time                 `json:"created_at"`
	PasswordHash          string                    `json:"password_hash,omitempty"`
	PasswordSetAt         *time.Time                `json:"password_set_at,omitempty"`
	PasswordResetRequired bool                      `json:"password_reset_required,omitempty"`
	LastLogin             *time.Time                `json:"last_login,omitempty"`
	Groups                []string                  `json:"groups"`
	AttachedPolicies      []string                  `json:"attached_policies"`
	InlinePolicies        map[string]PolicyDocument `json:"inline_policies"`
	PermissionsBoundary   string                    `json:"permissions_boundary,omitempty"` // policy ARN
	Tags                  core.Tags                 `json:"tags,omitempty"`
}

func (u User) path() string {
	if u.Path == "" {
		return "/"
	}
	return u.Path
}

// view hides secrets when a user is returned over the API.
func (u User) view() map[string]any {
	return map[string]any{
		"name": u.Name, "id": u.ID, "arn": u.ARN, "path": u.path(), "root": u.Root, "created_at": u.CreatedAt,
		"console_access": u.PasswordHash != "", "password_set_at": u.PasswordSetAt, "last_login": u.LastLogin,
		"groups": nz(u.Groups), "attached_policies": nz(u.AttachedPolicies), "inline_policies": u.InlinePolicies, "tags": u.Tags,
		"permissions_boundary": u.PermissionsBoundary,
	}
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

type Group struct {
	Name             string                    `json:"name"`
	ID               string                    `json:"id,omitempty"`
	ARN              string                    `json:"arn"`
	Path             string                    `json:"path,omitempty"`
	CreatedAt        time.Time                 `json:"created_at"`
	AttachedPolicies []string                  `json:"attached_policies"`
	InlinePolicies   map[string]PolicyDocument `json:"inline_policies,omitempty"`
}

func (g Group) path() string {
	if g.Path == "" {
		return "/"
	}
	return g.Path
}

type AccessKey struct {
	AccessKeyID string `json:"access_key_id"`
	UserName    string `json:"user_name"`
	SecretHash  string `json:"secret_hash,omitempty"`
	// SecretCT is the secret encrypted under the master key, needed to verify
	// AWS signatures (keys created before it existed gain it on first use).
	SecretCT  string     `json:"secret_ct,omitempty"`
	Status    string     `json:"status"` // Active | Inactive
	CreatedAt time.Time  `json:"created_at"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
}

func (k AccessKey) public() AccessKey {
	k.SecretHash, k.SecretCT = "", ""
	return k
}

type session struct {
	Token    string    `json:"token"`
	UserName string    `json:"user_name"`
	UserID   string    `json:"user_id"` // a re-created user with the same name is a different ID
	Expires  time.Time `json:"expires"`
}

type account struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct {
	env *svc.Env
	// Seal encrypts access key secrets and temporary credentials at rest.
	Seal     Sealer
	mu       sync.Mutex // serialises creates and renames of users, groups, policies and instance profiles
	roleMu   sync.Mutex
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

// releaseAttempt gives back the throttle slot of a sign-in that succeeded.
func (s *Service) releaseAttempt(ip string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if l := s.failures[ip]; len(l) > 1 {
		s.failures[ip] = l[:len(l)-1]
	} else {
		delete(s.failures, ip)
	}
}

var (
	dummyOnce sync.Once
	dummyBcr  string
)

// dummyHash is a valid bcrypt hash nobody knows the password of, compared
// against when a sign-in names an unknown user so the response time doesn't
// reveal which user names exist.
func dummyHash() string {
	dummyOnce.Do(func() {
		h, _ := bcrypt.GenerateFromPassword([]byte(core.NewSecret(24)), bcrypt.DefaultCost)
		dummyBcr = string(h)
	})
	return dummyBcr
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

// Bootstrap installs the managed policies, migrates stored data from older
// versions and, on first run, creates the root user.
func (s *Service) Bootstrap() (*BootstrapResult, error) {
	if err := s.installBuiltins(); err != nil {
		return nil, err
	}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if store.Has(s.env.Store, cUsers, RootUser) {
		return nil, nil
	}
	pw := core.NewSecret(20)
	u := s.newUser(RootUser, "/")
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

// SetRootPassword sets the root console password to pw (subject to the length
// rules of every console password).
func (s *Service) SetRootPassword(pw string) error {
	if !store.Has(s.env.Store, cUsers, RootUser) {
		return core.Errf(http.StatusNotFound, "NoSuchEntity", "this installation has no root user yet: start the server once first")
	}
	_, err := store.Update(s.env.Store, cUsers, RootUser, func(u *User) error { return setPassword(u, pw) })
	return err
}

func (s *Service) userARN(name, path string) string { return s.env.ARN("iam", "user"+path+name) }
func (s *Service) groupARN(name, path string) string {
	return s.env.ARN("iam", "group"+path+name)
}

func newID(prefix string) string { return prefix + strings.ToUpper(core.RandHex(16)) }

func (s *Service) newUser(name, path string) User {
	return User{
		Name: name, ID: newID("HCUA"), ARN: s.userARN(name, path), Path: path,
		CreatedAt: core.Now(), Groups: []string{}, AttachedPolicies: []string{}, InlinePolicies: map[string]PolicyDocument{},
	}
}

func setPassword(u *User, pw string) error {
	if len(pw) < 8 {
		return core.Errf(http.StatusBadRequest, "PasswordPolicyViolation", "password must be at least 8 characters")
	}
	if len(pw) > 128 {
		return core.Errf(http.StatusBadRequest, "PasswordPolicyViolation", "password must be at most 128 characters")
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
	if s.Seal != nil {
		k.SecretCT = s.Seal.Encrypt([]byte(secret))
	}
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
	var user, keyID, sessionUserID string
	if strings.HasPrefix(token, "hcs_") {
		sess, err := store.Get[session](s.env.Store, cSessions, hashSecret(token))
		if err != nil || time.Now().After(sess.Expires) {
			return nil, core.Errf(http.StatusUnauthorized, "ExpiredToken", "session is invalid or expired; sign in again")
		}
		user, sessionUserID = sess.UserName, sess.UserID
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
		if k.SecretCT == "" && s.Seal != nil {
			// Keys created before AWS signature support gain an encrypted secret on first use.
			_, _ = store.Update(s.env.Store, cKeys, id, func(k *AccessKey) error { k.SecretCT = s.Seal.Encrypt([]byte(secret)); return nil })
		}
		s.touchKey(k)
	}
	u, err := store.Get[User](s.env.Store, cUsers, user)
	if err != nil || (sessionUserID != "" && sessionUserID != u.ID) {
		return nil, core.Errf(http.StatusUnauthorized, "InvalidClientTokenId", "user %q no longer exists", user)
	}
	p := s.principal(u, keyID)
	p.AddRequestContext(r)
	return p, nil
}

// Refresh re-reads a principal's user, access key and policies, so long-running
// work (CloudFormation stacks) acts with the caller's current permissions.
func (s *Service) Refresh(p *httpx.Principal) (*httpx.Principal, error) {
	if strings.HasPrefix(p.AccessKey, tempKeyPrefix) {
		t, err := store.Get[tempCred](s.env.Store, cTempCreds, p.AccessKey)
		if err != nil || time.Now().After(t.Expires) {
			return nil, fmt.Errorf("temporary credentials %s have expired", p.AccessKey)
		}
		return s.tempPrincipal(t)
	}
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

// userContext is the identity part of a user's request context.
func (s *Service) userContext(u User) CondContext {
	typ := "User"
	if u.Root {
		typ = "Account"
	}
	c := CondContext{
		"aws:username": {u.Name}, "aws:userid": {u.ID}, "aws:principalarn": {u.ARN},
		"aws:principalaccount": {s.env.AccountID}, "aws:principaltype": {typ},
	}
	for k, v := range u.Tags {
		c["aws:principaltag/"+strings.ToLower(k)] = []string{v}
	}
	return c
}

func (s *Service) principal(u User, keyID string) *httpx.Principal {
	p := &httpx.Principal{AccountID: s.env.AccountID, UserName: u.Name, ARN: u.ARN, Root: u.Root, AccessKey: keyID, Context: s.userContext(u)}
	if u.Root {
		p.Can = func(string, string) bool { return true }
		p.Identity = func(string, string, map[string][]string) httpx.Decision { return httpx.Allowed }
		return p
	}
	docs := s.effectiveDocs(u)
	boundary := s.boundaryDoc(u.PermissionsBoundary)
	p.Can = func(action, resource string) bool {
		return decide(docs, boundary, action, resource, CondContext(p.Context)) == allow
	}
	p.Identity = identityFunc(p, docs, boundary)
	p.Mentions = mentionsFunc(docs, boundary)
	return p
}

// effectiveDocs gathers every policy that applies to u: attached, inline, and via groups.
func (s *Service) effectiveDocs(u User) []PolicyDocument {
	names := slices.Clone(u.AttachedPolicies)
	var docs []PolicyDocument
	for _, g := range u.Groups {
		if grp, err := store.Get[Group](s.env.Store, cGroups, g); err == nil {
			names = append(names, grp.AttachedPolicies...)
			for _, d := range grp.InlinePolicies {
				docs = append(docs, d)
			}
		}
	}
	docs = append(docs, s.managedDocs(names)...)
	for _, d := range u.InlinePolicies {
		docs = append(docs, d)
	}
	return docs
}

func (s *Service) managedDocs(names []string) []PolicyDocument {
	var docs []PolicyDocument
	for _, n := range names {
		if p, err := store.Get[Policy](s.env.Store, cPolicies, n); err == nil {
			docs = append(docs, p.Document)
		}
	}
	return docs
}

// boundaryDoc returns the permissions boundary policy, or nil for none. A
// boundary that no longer exists allows nothing.
func (s *Service) boundaryDoc(arn string) *PolicyDocument {
	if arn == "" {
		return nil
	}
	if p, err := s.resolvePolicy(arn); err == nil {
		return &p.Document
	}
	return &PolicyDocument{}
}

var (
	nameRe = regexp.MustCompile(`^[\w+=,.@-]{1,64}$`)
	pathRe = regexp.MustCompile(`^/([\x21-\x7E]+/)?$`)
)

func validName(kind, n string) error {
	if !nameRe.MatchString(n) {
		return core.Errf(http.StatusBadRequest, "ValidationError", "%s name %q is invalid: use 1-64 letters, digits or +=,.@_-", kind, n)
	}
	return nil
}

func validPath(p string) (string, error) {
	if p == "" {
		return "/", nil
	}
	if len(p) > 512 || !pathRe.MatchString(p) {
		return "", core.Errf(http.StatusBadRequest, "ValidationError", "path %q must begin and end with / and contain printable ASCII characters", p)
	}
	return p, nil
}

// nameTaken reports whether name is in use in coll other than by self. IAM
// names are unique ignoring case (Alice and alice cannot both exist), as in AWS.
func nameTaken[T any](s *Service, coll, name, self string, nameOf func(T) string) bool {
	if name != self && store.Has(s.env.Store, coll, name) {
		return true
	}
	for _, v := range store.List[T](s.env.Store, coll) {
		if n := nameOf(v); n != self && strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

func userName(u User) string   { return u.Name }
func groupName(g Group) string { return g.Name }

func addUnique(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

func remove(list []string, v string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(x string) bool { return x == v })
}

// ---- users ----

func (s *Service) getUserOr404(name string) (User, error) {
	u, err := store.Get[User](s.env.Store, cUsers, name)
	if err != nil {
		return u, noSuchEntity("The user with name %s cannot be found.", name)
	}
	return u, nil
}

// UserInput creates a user.
type UserInput struct {
	Name                  string    `json:"name"`
	Path                  string    `json:"path"`
	Password              string    `json:"password"`
	PasswordResetRequired bool      `json:"password_reset_required"`
	Groups                []string  `json:"groups"`
	Policies              []string  `json:"policies"`
	Tags                  core.Tags `json:"tags"`
	PermissionsBoundary   string    `json:"permissions_boundary"`
}

// CreateUser creates a user. Everything the request sets on the new user needs
// its own permission, or iam:CreateUser alone would be enough to mint an administrator.
func (s *Service) CreateUser(in UserInput, can authz) (User, error) {
	if err := validName("user", in.Name); err != nil {
		return User{}, err
	}
	path, err := validPath(in.Path)
	if err != nil {
		return User{}, err
	}
	if err := checkTags(in.Tags); err != nil {
		return User{}, err
	}
	userARN := s.userARN(in.Name, path)
	if in.Password != "" {
		if err := can("iam:CreateLoginProfile", userARN); err != nil {
			return User{}, err
		}
	}
	var policies []string
	for _, ref := range in.Policies {
		if err := can("iam:AttachUserPolicy", userARN); err != nil {
			return User{}, err
		}
		p, err := s.resolvePolicy(ref)
		if err != nil {
			return User{}, err
		}
		policies = addUnique(policies, p.Name)
	}
	for _, g := range in.Groups {
		if err := can("iam:AddUserToGroup", s.groupARNByName(g)); err != nil {
			return User{}, err
		}
		if !store.Has(s.env.Store, cGroups, g) {
			return User{}, noSuchEntity("The group with name %s cannot be found.", g)
		}
	}
	if len(in.Tags) > 0 {
		if err := can("iam:TagUser", userARN); err != nil {
			return User{}, err
		}
	}
	u := s.newUser(in.Name, path)
	if in.PermissionsBoundary != "" {
		if err := can("iam:PutUserPermissionsBoundary", userARN); err != nil {
			return User{}, err
		}
		b, err := s.resolvePolicy(in.PermissionsBoundary)
		if err != nil {
			return User{}, err
		}
		u.PermissionsBoundary = b.ARN
	}
	u.Tags = in.Tags
	if in.Password != "" {
		if err := setPassword(&u, in.Password); err != nil {
			return User{}, err
		}
		u.PasswordResetRequired = in.PasswordResetRequired
	}
	u.AttachedPolicies = append(u.AttachedPolicies, policies...)
	for _, g := range in.Groups {
		u.Groups = addUnique(u.Groups, g)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if nameTaken(s, cUsers, in.Name, "", userName) {
		return User{}, alreadyExists("User with name %s already exists.", in.Name)
	}
	return u, store.Put(s.env.Store, cUsers, u.Name, u)
}

// UserKeys returns a user's access keys (without secrets).
func (s *Service) UserKeys(user string) []AccessKey {
	out := []AccessKey{}
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		if k.UserName == user {
			out = append(out, k.public())
		}
	}
	return out
}

// DeleteUser deletes a user. Without force it fails, as in AWS, while the user
// still has access keys, a password, policies or group memberships; with force
// (the console) those go with the user.
func (s *Service) DeleteUser(name string, force bool) error {
	u, err := s.getUserOr404(name)
	if err != nil {
		return err
	}
	if u.Root {
		return deleteConflict("the root user cannot be deleted")
	}
	if !force {
		switch {
		case len(s.UserKeys(name)) > 0:
			return deleteConflict("Cannot delete entity, must delete access keys first.")
		case u.PasswordHash != "":
			return deleteConflict("Cannot delete entity, must delete login profile first.")
		case len(u.AttachedPolicies) > 0:
			return deleteConflict("Cannot delete entity, must detach all policies first.")
		case len(u.InlinePolicies) > 0:
			return deleteConflict("Cannot delete entity, must delete policies first.")
		case len(u.Groups) > 0:
			return deleteConflict("Cannot delete entity, must remove users from group first.")
		}
	}
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		if k.UserName == name {
			_ = store.Delete(s.env.Store, cKeys, k.AccessKeyID)
		}
	}
	s.endSessions(name)
	s.env.Store.Retain(cTempCreds, func(_ string, raw json.RawMessage) bool {
		var t tempCred
		return json.Unmarshal(raw, &t) == nil && (t.UserName != name || t.UserID != u.ID)
	})
	return store.Delete(s.env.Store, cUsers, name)
}

// UpdateUser applies fn to a user.
func (s *Service) UpdateUser(name string, fn func(*User) error) (User, error) {
	u, err := store.Update(s.env.Store, cUsers, name, fn)
	if err == store.ErrNotFound {
		return u, noSuchEntity("The user with name %s cannot be found.", name)
	}
	return u, err
}

// RenameUser changes a user's name and/or path (UpdateUser in AWS). The user
// keeps its ID, keys, groups and policies; console sessions end.
func (s *Service) RenameUser(name, newName, newPath string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.getUserOr404(name)
	if err != nil {
		return u, err
	}
	if newPath != "" {
		if u.Path, err = validPath(newPath); err != nil {
			return u, err
		}
	}
	renamed := newName != "" && newName != name
	if renamed {
		if u.Root {
			return u, unmodifiable("the root user cannot be renamed")
		}
		if err := validName("user", newName); err != nil {
			return u, err
		}
		if nameTaken(s, cUsers, newName, name, userName) {
			return u, alreadyExists("User with name %s already exists.", newName)
		}
		u.Name = newName
	}
	u.ARN = s.userARN(u.Name, u.path())
	if err := store.Put(s.env.Store, cUsers, u.Name, u); err != nil {
		return u, err
	}
	if !renamed {
		return u, nil
	}
	_ = store.Delete(s.env.Store, cUsers, name)
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		if k.UserName == name {
			_, _ = store.Update(s.env.Store, cKeys, k.AccessKeyID, func(k *AccessKey) error { k.UserName = newName; return nil })
		}
	}
	for _, t := range store.List[tempCred](s.env.Store, cTempCreds) {
		if t.UserName == name {
			_, _ = store.Update(s.env.Store, cTempCreds, t.AccessKeyID, func(t *tempCred) error { t.UserName = newName; return nil })
		}
	}
	s.endSessions(name)
	return u, nil
}

// SetLoginProfile sets a user's console password. create requires that the
// user has none yet (CreateLoginProfile); mustExist that it has one
// (UpdateLoginProfile). An empty password with mustExist only changes reset.
func (s *Service) SetLoginProfile(name, pw string, reset *bool, create, mustExist bool) (User, error) {
	u, err := s.UpdateUser(name, func(u *User) error {
		if create && u.PasswordHash != "" {
			return alreadyExists("Login Profile for user %s already exists.", u.Name)
		}
		if mustExist && u.PasswordHash == "" {
			return noSuchEntity("Login Profile for User %s cannot be found.", u.Name)
		}
		if pw != "" || !mustExist {
			if err := setPassword(u, pw); err != nil {
				return err
			}
		}
		if reset != nil {
			u.PasswordResetRequired = *reset
		}
		return nil
	})
	if err == nil && pw != "" {
		s.endSessions(name)
	}
	return u, err
}

// DeleteLoginProfile removes a user's console password.
func (s *Service) DeleteLoginProfile(name string, mustExist bool) (User, error) {
	u, err := s.UpdateUser(name, func(u *User) error {
		if u.Root {
			return unmodifiable("the root user must keep console access")
		}
		if mustExist && u.PasswordHash == "" {
			return noSuchEntity("Login Profile for User %s cannot be found.", u.Name)
		}
		u.PasswordHash, u.PasswordSetAt, u.PasswordResetRequired = "", nil, false
		return nil
	})
	if err == nil {
		s.endSessions(name)
	}
	return u, err
}

// endSessions signs a user out of every console session.
func (s *Service) endSessions(user string) {
	_ = s.env.Store.Retain(cSessions, func(_ string, raw json.RawMessage) bool {
		var ss session
		return json.Unmarshal(raw, &ss) != nil || ss.UserName != user
	})
}

func checkTags(t core.Tags) error {
	if len(t) > 50 {
		return limitExceeded("Cannot exceed quota for TagsPerEntity: 50")
	}
	for k, v := range t {
		if k == "" || len(k) > 128 || len(v) > 256 {
			return core.Errf(http.StatusBadRequest, "ValidationError", "tag keys must be 1-128 characters and values at most 256")
		}
	}
	return nil
}

func mergeTags(cur, add core.Tags) (core.Tags, error) {
	out := core.Tags{}
	for k, v := range cur {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out, checkTags(out)
}

func dropTags(cur core.Tags, keys []string) core.Tags {
	out := core.Tags{}
	for k, v := range cur {
		if !slices.Contains(keys, k) {
			out[k] = v
		}
	}
	return out
}

func (s *Service) attachTo(list []string, ref string) ([]string, error) {
	p, err := s.resolvePolicy(ref)
	if err != nil {
		return list, err
	}
	if !slices.Contains(list, p.Name) && len(list) >= maxAttached {
		return list, limitExceeded("Cannot exceed quota for PoliciesPerEntity: %d", maxAttached)
	}
	return addUnique(list, p.Name), nil
}

func (s *Service) detachFrom(list []string, ref, entity string) ([]string, error) {
	n := s.policyName(ref)
	if !slices.Contains(list, n) {
		return list, noSuchEntity("Policy %s was not found (it is not attached to %s).", ref, entity)
	}
	return remove(list, n), nil
}

// AttachUserPolicy attaches a managed policy (name or ARN) to a user.
func (s *Service) AttachUserPolicy(user, ref string) (User, error) {
	if _, err := s.resolvePolicy(ref); err != nil {
		return User{}, err
	}
	return s.UpdateUser(user, func(u *User) (err error) {
		u.AttachedPolicies, err = s.attachTo(u.AttachedPolicies, ref)
		return err
	})
}

// DetachUserPolicy detaches a managed policy (name or ARN) from a user.
func (s *Service) DetachUserPolicy(user, ref string) (User, error) {
	return s.UpdateUser(user, func(u *User) (err error) {
		if u.Root && s.policyName(ref) == "AdministratorAccess" {
			return unmodifiable("AdministratorAccess cannot be detached from root")
		}
		u.AttachedPolicies, err = s.detachFrom(u.AttachedPolicies, ref, "user "+u.Name)
		return err
	})
}

// PutUserPolicy sets an inline policy on a user.
func (s *Service) PutUserPolicy(user, name string, d PolicyDocument) (User, error) {
	if err := checkInline(name, d, 2048); err != nil {
		return User{}, err
	}
	return s.UpdateUser(user, func(u *User) error {
		if u.InlinePolicies == nil {
			u.InlinePolicies = map[string]PolicyDocument{}
		}
		u.InlinePolicies[name] = d
		return nil
	})
}

// DeleteUserPolicy removes an inline policy from a user.
func (s *Service) DeleteUserPolicy(user, name string) (User, error) {
	return s.UpdateUser(user, func(u *User) error {
		if _, ok := u.InlinePolicies[name]; !ok {
			return noSuchEntity("The user policy with name %s cannot be found.", name)
		}
		delete(u.InlinePolicies, name)
		return nil
	})
}

// checkInline validates an inline policy and its name. limit is the AWS size
// quota for the entity type (non-whitespace characters).
func checkInline(name string, d PolicyDocument, limit int) error {
	if err := validName("policy", name); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if n := docSize(d); n > limit*5 {
		// AWS sums inline policies per entity; HomeCloud only bounds each one generously.
		return limitExceeded("Maximum policy size of %d bytes exceeded", limit*5)
	}
	return nil
}

func docSize(d PolicyDocument) int {
	b, _ := json.Marshal(d)
	return len(b)
}

// SetUserBoundary sets (or with ref "" removes) a user's permissions boundary.
func (s *Service) SetUserBoundary(user, ref string) (User, error) {
	arn := ""
	if ref != "" {
		p, err := s.resolvePolicy(ref)
		if err != nil {
			return User{}, err
		}
		arn = p.ARN
	}
	return s.UpdateUser(user, func(u *User) error {
		if u.Root {
			return unmodifiable("the root user cannot have a permissions boundary")
		}
		u.PermissionsBoundary = arn
		return nil
	})
}

// CreateAccessKey creates an access key for a user (at most two per user).
func (s *Service) CreateAccessKey(user string) (AccessKey, string, error) {
	if _, err := s.getUserOr404(user); err != nil {
		return AccessKey{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.UserKeys(user)) >= maxAccessKeys {
		return AccessKey{}, "", limitExceeded("Cannot exceed quota for AccessKeysPerUser: %d", maxAccessKeys)
	}
	return s.createKey(user)
}

func (s *Service) userKey(user, id string) (AccessKey, error) {
	k, err := store.Get[AccessKey](s.env.Store, cKeys, id)
	if err != nil || k.UserName != user {
		return k, noSuchEntity("The Access Key with id %s cannot be found.", id)
	}
	return k, nil
}

// UpdateAccessKey activates or deactivates a key.
func (s *Service) UpdateAccessKey(user, id, status string) (AccessKey, error) {
	if status != "Active" && status != "Inactive" {
		return AccessKey{}, core.Errf(http.StatusBadRequest, "ValidationError", "status must be Active or Inactive")
	}
	if _, err := s.userKey(user, id); err != nil {
		return AccessKey{}, err
	}
	k, err := store.Update(s.env.Store, cKeys, id, func(k *AccessKey) error { k.Status = status; return nil })
	return k.public(), err
}

// DeleteAccessKey deletes a key.
func (s *Service) DeleteAccessKey(user, id string) error {
	if _, err := s.userKey(user, id); err != nil {
		return err
	}
	return store.Delete(s.env.Store, cKeys, id)
}

// ---- groups ----

func (s *Service) groupARNByName(name string) string {
	if g, err := store.Get[Group](s.env.Store, cGroups, name); err == nil {
		return g.ARN
	}
	return s.groupARN(name, "/")
}

func (s *Service) getGroupOr404(name string) (Group, error) {
	g, err := store.Get[Group](s.env.Store, cGroups, name)
	if err != nil {
		return g, noSuchEntity("The group with name %s cannot be found.", name)
	}
	return g, nil
}

func (s *Service) groupMembers(name string) []string {
	out := []string{}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if slices.Contains(u.Groups, name) {
			out = append(out, u.Name)
		}
	}
	return out
}

// CreateGroup creates a group; attaching policies needs iam:AttachGroupPolicy.
func (s *Service) CreateGroup(name, path string, policies []string, can authz) (Group, error) {
	if err := validName("group", name); err != nil {
		return Group{}, err
	}
	path, err := validPath(path)
	if err != nil {
		return Group{}, err
	}
	g := Group{Name: name, ID: newID("HCGA"), Path: path, ARN: s.groupARN(name, path), CreatedAt: core.Now(), AttachedPolicies: []string{}}
	if len(policies) > 0 {
		if err := can("iam:AttachGroupPolicy", g.ARN); err != nil {
			return Group{}, err
		}
	}
	for _, ref := range policies {
		if g.AttachedPolicies, err = s.attachTo(g.AttachedPolicies, ref); err != nil {
			return Group{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if nameTaken(s, cGroups, name, "", groupName) {
		return Group{}, alreadyExists("Group with name %s already exists.", name)
	}
	return g, store.Put(s.env.Store, cGroups, g.Name, g)
}

// DeleteGroup deletes a group. Without force it fails while the group has
// members or policies, as in AWS; with force members are removed.
func (s *Service) DeleteGroup(name string, force bool) error {
	g, err := s.getGroupOr404(name)
	if err != nil {
		return err
	}
	members := s.groupMembers(name)
	if !force {
		switch {
		case len(members) > 0:
			return deleteConflict("Cannot delete entity, must remove users from group first.")
		case len(g.AttachedPolicies) > 0:
			return deleteConflict("Cannot delete entity, must detach all policies first.")
		case len(g.InlinePolicies) > 0:
			return deleteConflict("Cannot delete entity, must delete policies first.")
		}
	}
	for _, m := range members {
		_, _ = store.Update(s.env.Store, cUsers, m, func(u *User) error { u.Groups = remove(u.Groups, name); return nil })
	}
	return store.Delete(s.env.Store, cGroups, name)
}

// RenameGroup changes a group's name and/or path (UpdateGroup in AWS).
func (s *Service) RenameGroup(name, newName, newPath string) (Group, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.getGroupOr404(name)
	if err != nil {
		return g, err
	}
	if newPath != "" {
		if g.Path, err = validPath(newPath); err != nil {
			return g, err
		}
	}
	renamed := newName != "" && newName != name
	if renamed {
		if err := validName("group", newName); err != nil {
			return g, err
		}
		if nameTaken(s, cGroups, newName, name, groupName) {
			return g, alreadyExists("Group with name %s already exists.", newName)
		}
		g.Name = newName
	}
	g.ARN = s.groupARN(g.Name, g.path())
	if err := store.Put(s.env.Store, cGroups, g.Name, g); err != nil {
		return g, err
	}
	if renamed {
		_ = store.Delete(s.env.Store, cGroups, name)
		for _, m := range s.groupMembers(name) {
			_, _ = store.Update(s.env.Store, cUsers, m, func(u *User) error {
				u.Groups = append(remove(u.Groups, name), newName)
				return nil
			})
		}
	}
	return g, nil
}

// UpdateGroup applies fn to a group.
func (s *Service) UpdateGroup(name string, fn func(*Group) error) (Group, error) {
	g, err := store.Update(s.env.Store, cGroups, name, fn)
	if err == store.ErrNotFound {
		return g, noSuchEntity("The group with name %s cannot be found.", name)
	}
	return g, err
}

// AddUserToGroup adds a user to a group.
func (s *Service) AddUserToGroup(group, user string) (User, error) {
	if _, err := s.getGroupOr404(group); err != nil {
		return User{}, err
	}
	return s.UpdateUser(user, func(u *User) error { u.Groups = addUnique(u.Groups, group); return nil })
}

// RemoveUserFromGroup removes a user from a group.
func (s *Service) RemoveUserFromGroup(group, user string) (User, error) {
	if _, err := s.getGroupOr404(group); err != nil {
		return User{}, err
	}
	return s.UpdateUser(user, func(u *User) error {
		if !slices.Contains(u.Groups, group) {
			return noSuchEntity("User %s is not in group %s.", u.Name, group)
		}
		u.Groups = remove(u.Groups, group)
		return nil
	})
}

// AttachGroupPolicy attaches a managed policy to a group.
func (s *Service) AttachGroupPolicy(group, ref string) (Group, error) {
	if _, err := s.resolvePolicy(ref); err != nil {
		return Group{}, err
	}
	return s.UpdateGroup(group, func(g *Group) (err error) {
		g.AttachedPolicies, err = s.attachTo(g.AttachedPolicies, ref)
		return err
	})
}

// DetachGroupPolicy detaches a managed policy from a group.
func (s *Service) DetachGroupPolicy(group, ref string) (Group, error) {
	return s.UpdateGroup(group, func(g *Group) (err error) {
		g.AttachedPolicies, err = s.detachFrom(g.AttachedPolicies, ref, "group "+g.Name)
		return err
	})
}

// PutGroupPolicy sets an inline policy on a group.
func (s *Service) PutGroupPolicy(group, name string, d PolicyDocument) (Group, error) {
	if err := checkInline(name, d, 5120); err != nil {
		return Group{}, err
	}
	return s.UpdateGroup(group, func(g *Group) error {
		if g.InlinePolicies == nil {
			g.InlinePolicies = map[string]PolicyDocument{}
		}
		g.InlinePolicies[name] = d
		return nil
	})
}

// DeleteGroupPolicy removes an inline policy from a group.
func (s *Service) DeleteGroupPolicy(group, name string) (Group, error) {
	return s.UpdateGroup(group, func(g *Group) error {
		if _, ok := g.InlinePolicies[name]; !ok {
			return noSuchEntity("The group policy with name %s cannot be found.", name)
		}
		delete(g.InlinePolicies, name)
		return nil
	})
}

// String renders a bootstrap result for the terminal.
func (b *BootstrapResult) String() string {
	return fmt.Sprintf("account %s\n  root password:     %s\n  access key id:     %s\n  secret access key: %s", b.AccountID, b.RootPassword, b.AccessKeyID, b.SecretKey)
}
