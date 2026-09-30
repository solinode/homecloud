// Package cognito implements user pools for application sign-up and sign-in:
// users with attributes and groups, app clients, RS256 ID/access tokens with a
// JWKS endpoint for verification, refresh tokens, forced password changes and
// global sign-out. API Gateway can require these tokens on routes.
package cognito

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"golang.org/x/crypto/bcrypt"
)

const (
	cPools    = "cognito_pools"
	cClients  = "cognito_clients"
	cUsers    = "cognito_users"
	cRefresh  = "cognito_refresh_tokens"
	cSessions = "cognito_challenges"
)

type PasswordPolicy struct {
	MinLength        int  `json:"min_length"`
	RequireUppercase bool `json:"require_uppercase"`
	RequireLowercase bool `json:"require_lowercase"`
	RequireNumbers   bool `json:"require_numbers"`
	RequireSymbols   bool `json:"require_symbols"`
}

type Pool struct {
	ID             string         `json:"id"`
	ARN            string         `json:"arn"`
	Name           string         `json:"name"`
	PasswordPolicy PasswordPolicy `json:"password_policy"`
	AutoConfirm    bool           `json:"auto_confirm"`
	SelfSignUp     bool           `json:"self_sign_up"`
	Groups         []Group        `json:"groups"`
	KeyID          string         `json:"key_id"`
	PrivateKeyCT   string         `json:"private_key_ct,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ModifiedAt     time.Time      `json:"modified_at,omitempty"`
	Tags           core.Tags      `json:"tags,omitempty"`
	// Extra holds AWS settings HomeCloud stores and echoes without acting on
	// them (LambdaConfig, MfaConfiguration, AccountRecoverySetting, ...).
	Extra map[string]any `json:"extra,omitempty"`
}

type Group struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Precedence  int    `json:"precedence"`
	RoleARN     string `json:"role_arn,omitempty"`
	// CreatedAt is set by AWS-created groups (CreationDate).
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type Client struct {
	ID                 string    `json:"id"`
	PoolID             string    `json:"pool_id"`
	Name               string    `json:"name"`
	SecretHash         string    `json:"secret_hash,omitempty"`
	AccessTokenMinutes int       `json:"access_token_minutes"`
	RefreshTokenDays   int       `json:"refresh_token_days"`
	CreatedAt          time.Time `json:"created_at"`
	ModifiedAt         time.Time `json:"modified_at,omitempty"`
	// SecretCT is the client secret encrypted, so AWS clients can prove it with
	// SECRET_HASH (an HMAC needs the secret itself). Clients created before it
	// existed have only SecretHash and accept the native secret check only.
	SecretCT string `json:"secret_ct,omitempty"`
	// Extra holds AWS settings echoed by DescribeUserPoolClient (ExplicitAuthFlows, ...).
	Extra map[string]any `json:"extra,omitempty"`
}

type User struct {
	PoolID       string            `json:"pool_id"`
	Username     string            `json:"username"`
	Sub          string            `json:"sub"`
	Attributes   map[string]string `json:"attributes"`
	PasswordHash string            `json:"password_hash,omitempty"`
	Status       string            `json:"status"` // CONFIRMED | UNCONFIRMED | FORCE_CHANGE_PASSWORD
	Enabled      bool              `json:"enabled"`
	Groups       []string          `json:"groups"`
	CreatedAt    time.Time         `json:"created_at"`
	ModifiedAt   time.Time         `json:"modified_at,omitempty"`
	LastSignIn   *time.Time        `json:"last_sign_in,omitempty"`
	// ConfirmCode is the hash of the pending sign-up confirmation code.
	ConfirmCode        string    `json:"confirm_code,omitempty"`
	ConfirmCodeExpires time.Time `json:"confirm_code_expires,omitempty"`
	// ResetCode is the hash of the pending password-reset code (ForgotPassword).
	ResetCode        string    `json:"reset_code,omitempty"`
	ResetCodeExpires time.Time `json:"reset_code_expires,omitempty"`
	// SRPSalt and SRPVerifier (hex) let USER_SRP_AUTH verify the password
	// without HomeCloud holding it. Users created before SRP support have none
	// until their password is set again or they sign in with USER_PASSWORD_AUTH.
	SRPSalt     string `json:"srp_salt,omitempty"`
	SRPVerifier string `json:"srp_verifier,omitempty"`
	// TokenVersion is embedded in every token; global sign-out increments it.
	TokenVersion int `json:"token_version"`
}

func (u User) view() map[string]any {
	return map[string]any{"username": u.Username, "sub": u.Sub, "attributes": u.Attributes, "status": u.Status, "enabled": u.Enabled,
		"groups": u.Groups, "created_at": u.CreatedAt, "last_sign_in": u.LastSignIn}
}

type refresh struct {
	PoolID   string    `json:"pool_id"`
	ClientID string    `json:"client_id"`
	Username string    `json:"username"`
	Sub      string    `json:"sub"`
	Version  int       `json:"version"`
	Expires  time.Time `json:"expires"`
}

type challenge struct {
	PoolID   string    `json:"pool_id"`
	ClientID string    `json:"client_id"`
	Username string    `json:"username"`
	Expires  time.Time `json:"expires"`
}

type Service struct {
	env      *svc.Env
	secrets  *secrets.Service
	mu       sync.Mutex
	createMu sync.Mutex
	keys     map[string]*rsa.PrivateKey
	fails    map[string][]time.Time
	srp      map[string]*srpPending // pending PASSWORD_VERIFIER challenges by hash of SECRET_BLOCK
	srpKey   []byte                 // derives stable fake salts for unknown users
}

// dummyHash equalises sign-in timing for unknown users.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("homecloud-timing-equaliser"), bcrypt.DefaultCost)

func New(env *svc.Env, sec *secrets.Service) *Service {
	return &Service{env: env, secrets: sec, keys: map[string]*rsa.PrivateKey{}, fails: map[string][]time.Time{},
		srp: map[string]*srpPending{}, srpKey: []byte(core.RandHex(32))}
}

func userKey(pool, username string) string { return pool + "/" + strings.ToLower(username) }
func hashToken(t string) string            { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

func (s *Service) issuer(pool string) string {
	return s.env.Cfg.PublicBase() + "/cognito/" + pool
}

func (s *Service) key(p Pool) (*rsa.PrivateKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k := s.keys[p.ID]; k != nil {
		return k, nil
	}
	der, err := s.secrets.Decrypt(p.PrivateKeyCT)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, err
	}
	s.keys[p.ID] = k
	return k, nil
}

func (s *Service) pool(id string) (Pool, error) {
	p, err := store.Get[Pool](s.env.Store, cPools, id)
	if err != nil {
		return p, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "user pool %q does not exist", id)
	}
	return p, nil
}

func (p PasswordPolicy) check(pw string) error {
	var up, low, num, sym bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			up = true
		case unicode.IsLower(r):
			low = true
		case unicode.IsDigit(r):
			num = true
		default:
			sym = true
		}
	}
	var missing []string
	if utf8.RuneCountInString(pw) < p.MinLength {
		missing = append(missing, fmt.Sprintf("at least %d characters", p.MinLength))
	}
	if p.RequireUppercase && !up {
		missing = append(missing, "an uppercase letter")
	}
	if p.RequireLowercase && !low {
		missing = append(missing, "a lowercase letter")
	}
	if p.RequireNumbers && !num {
		missing = append(missing, "a number")
	}
	if p.RequireSymbols && !sym {
		missing = append(missing, "a symbol")
	}
	if len(missing) > 0 {
		return core.Errf(http.StatusBadRequest, "InvalidPasswordException", "password must contain %s", strings.Join(missing, ", "))
	}
	return nil
}

// ---- tokens ----

type tokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func (s *Service) issue(p Pool, c Client, u User, withRefresh bool) (*tokens, error) {
	k, err := s.key(p)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	exp := now.Add(time.Duration(c.AccessTokenMinutes) * time.Minute)
	groups := u.Groups
	if groups == nil {
		groups = []string{}
	}
	// Attributes first, so they can never overwrite a registered claim.
	id := map[string]any{}
	for k, v := range u.Attributes {
		id[k] = v
	}
	for k, v := range map[string]any{"sub": u.Sub, "aud": c.ID, "iss": s.issuer(p.ID), "token_use": "id", "auth_time": now.Unix(), "iat": now.Unix(),
		"exp": exp.Unix(), "cognito:username": u.Username, "cognito:groups": groups, "hc:tv": u.TokenVersion} {
		id[k] = v
	}
	access := map[string]any{"sub": u.Sub, "iss": s.issuer(p.ID), "client_id": c.ID, "token_use": "access", "scope": "openid profile",
		"auth_time": now.Unix(), "iat": now.Unix(), "exp": exp.Unix(), "jti": core.RandHex(32), "username": u.Username, "cognito:groups": groups,
		"hc:tv": u.TokenVersion}
	t := &tokens{ExpiresIn: int(exp.Sub(now).Seconds()), TokenType: "Bearer"}
	if t.IDToken, err = sign(k, p.KeyID, id); err != nil {
		return nil, err
	}
	if t.AccessToken, err = sign(k, p.KeyID, access); err != nil {
		return nil, err
	}
	if withRefresh {
		rt := core.NewSecret(64)
		r := refresh{PoolID: p.ID, ClientID: c.ID, Username: u.Username, Sub: u.Sub, Version: u.TokenVersion,
			Expires: now.Add(time.Duration(c.RefreshTokenDays) * 24 * time.Hour)}
		if err := store.Put(s.env.Store, cRefresh, hashToken(rt), r); err != nil {
			return nil, err
		}
		t.RefreshToken = rt
	}
	_, _ = store.Update(s.env.Store, cUsers, userKey(p.ID, u.Username), func(x *User) error { n := core.Now(); x.LastSignIn = &n; return nil })
	return t, nil
}

// VerifyToken validates a pool token for API Gateway's JWT authorizer.
// audience (a client ID) is optional.
// CheckClient verifies that a pool exists and, when clientID is set, that the client belongs to it.
func (s *Service) CheckClient(poolID, clientID string) error {
	if _, err := s.pool(poolID); err != nil {
		return core.NotFound("user pool", poolID)
	}
	if clientID == "" {
		return nil
	}
	cl, err := store.Get[Client](s.env.Store, cClients, clientID)
	if err != nil || cl.PoolID != poolID {
		return core.NotFound("app client", clientID)
	}
	return nil
}

func (s *Service) VerifyToken(poolID, token, audience string) (map[string]any, error) {
	p, err := s.pool(poolID)
	if err != nil {
		return nil, err
	}
	if kidOf(token) != p.KeyID {
		return nil, errSignature
	}
	k, err := s.key(p)
	if err != nil {
		return nil, err
	}
	claims, err := Verify(token, &k.PublicKey)
	if err != nil {
		return nil, err
	}
	if claims["iss"] != s.issuer(p.ID) {
		return nil, fmt.Errorf("token was issued by %v", claims["iss"])
	}
	if audience != "" && claims["aud"] != audience && claims["client_id"] != audience {
		return nil, fmt.Errorf("token is not for client %s", audience)
	}
	var username string
	switch claims["token_use"] {
	case "id":
		username, _ = claims["cognito:username"].(string)
	case "access":
		username, _ = claims["username"].(string)
	default:
		return nil, fmt.Errorf("unknown token_use")
	}
	u, err := store.Get[User](s.env.Store, cUsers, userKey(p.ID, username))
	if err != nil || !u.Enabled || claims["sub"] != u.Sub {
		return nil, fmt.Errorf("user is disabled or deleted")
	}
	if tv, _ := claims["hc:tv"].(float64); int(tv) != u.TokenVersion {
		return nil, fmt.Errorf("token was revoked by a sign-out")
	}
	return claims, nil
}

// ---- admin routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:cognito-idp:{region}:{account}:userpool/{pool}")
	r.Handle("GET /api/v1/cognito/user-pools", "cognito-idp:ListUserPools", s.listPools)
	r.Handle("POST /api/v1/cognito/user-pools", "cognito-idp:CreateUserPool", s.createPool)
	r.Handle("GET /api/v1/cognito/user-pools/{pool}", "cognito-idp:DescribeUserPool", s.getPool, res)
	r.Handle("PATCH /api/v1/cognito/user-pools/{pool}", "cognito-idp:UpdateUserPool", s.updatePool, res)
	r.Handle("DELETE /api/v1/cognito/user-pools/{pool}", "cognito-idp:DeleteUserPool", s.deletePool, res)
	r.Handle("GET /api/v1/cognito/user-pools/{pool}/clients", "cognito-idp:ListUserPoolClients", s.listClients, res)
	r.Handle("POST /api/v1/cognito/user-pools/{pool}/clients", "cognito-idp:CreateUserPoolClient", s.createClient, res)
	r.Handle("DELETE /api/v1/cognito/user-pools/{pool}/clients/{client}", "cognito-idp:DeleteUserPoolClient", s.deleteClient, res)
	r.Handle("GET /api/v1/cognito/user-pools/{pool}/users", "cognito-idp:ListUsers", s.listUsers, res)
	r.Handle("POST /api/v1/cognito/user-pools/{pool}/users", "cognito-idp:AdminCreateUser", s.adminCreateUser, res)
	r.Handle("GET /api/v1/cognito/user-pools/{pool}/users/{user}", "cognito-idp:AdminGetUser", s.adminGetUser, res)
	r.Handle("DELETE /api/v1/cognito/user-pools/{pool}/users/{user}", "cognito-idp:AdminDeleteUser", s.adminDeleteUser, res)
	r.Handle("PATCH /api/v1/cognito/user-pools/{pool}/users/{user}", "cognito-idp:AdminUpdateUserAttributes", s.adminUpdateUser, res)
	r.Handle("POST /api/v1/cognito/user-pools/{pool}/users/{user}/password", "cognito-idp:AdminSetUserPassword", s.adminSetPassword, res)
	r.Handle("POST /api/v1/cognito/user-pools/{pool}/users/{user}/sign-out", "cognito-idp:AdminUserGlobalSignOut", s.adminSignOut, res)
	r.Handle("POST /api/v1/cognito/user-pools/{pool}/groups", "cognito-idp:CreateGroup", s.createGroup, res)
	r.Handle("DELETE /api/v1/cognito/user-pools/{pool}/groups/{group}", "cognito-idp:DeleteGroup", s.deleteGroup, res)
	s.publicRoutes(r)
}

var poolName = regexp.MustCompile(`^[\w\s+=,.@-]{1,128}$`)

func (s *Service) poolView(p Pool) map[string]any {
	users, clients := 0, 0
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if u.PoolID == p.ID {
			users++
		}
	}
	for _, c := range store.List[Client](s.env.Store, cClients) {
		if c.PoolID == p.ID {
			clients++
		}
	}
	groups := p.Groups
	if groups == nil {
		groups = []Group{}
	}
	return map[string]any{"id": p.ID, "arn": p.ARN, "name": p.Name, "password_policy": p.PasswordPolicy, "auto_confirm": p.AutoConfirm,
		"self_sign_up": p.SelfSignUp, "groups": groups, "created_at": p.CreatedAt, "users": users, "clients": clients,
		"issuer": s.issuer(p.ID), "jwks_uri": s.issuer(p.ID) + "/.well-known/jwks.json"}
}

func (s *Service) listPools(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, p := range store.List[Pool](s.env.Store, cPools) {
		out = append(out, s.poolView(p))
	}
	return out, nil
}

func (s *Service) createPool(c *httpx.Ctx) (any, error) {
	in := struct {
		Name           string          `json:"name"`
		PasswordPolicy *PasswordPolicy `json:"password_policy"`
		AutoConfirm    *bool           `json:"auto_confirm"`
		SelfSignUp     *bool           `json:"self_sign_up"`
	}{}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	pp := PasswordPolicy{MinLength: 8, RequireLowercase: true, RequireNumbers: true}
	if in.PasswordPolicy != nil {
		pp = *in.PasswordPolicy
	}
	p, err := s.newPool(in.Name, pp, in.AutoConfirm == nil || *in.AutoConfirm, in.SelfSignUp == nil || *in.SelfSignUp, nil, nil)
	if err != nil {
		return nil, err
	}
	return s.poolView(p), nil
}

// checkPolicy validates and normalizes a password policy.
func checkPolicy(pp *PasswordPolicy) error {
	if pp.MinLength == 0 {
		pp.MinLength = 8
	}
	if pp.MinLength < 6 || pp.MinLength > 99 {
		return core.BadRequest("password_policy.min_length must be 6-99")
	}
	return nil
}

// newPool creates and stores a user pool with a fresh signing key. Shared by
// the native API and the AWS protocol layer.
func (s *Service) newPool(name string, pp PasswordPolicy, autoConfirm, selfSignUp bool, tags core.Tags, extra map[string]any) (Pool, error) {
	if !poolName.MatchString(name) {
		return Pool{}, core.BadRequest("pool name must be 1-128 characters")
	}
	if err := checkPolicy(&pp); err != nil {
		return Pool{}, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Pool{}, err
	}
	id := core.Region + "_" + strings.ToUpper(core.RandHex(9))
	p := Pool{ID: id, ARN: s.env.ARN("cognito-idp", "userpool/"+id), Name: name, PasswordPolicy: pp, AutoConfirm: autoConfirm,
		SelfSignUp: selfSignUp, Groups: []Group{}, KeyID: core.RandHex(16), PrivateKeyCT: s.secrets.Encrypt(x509.MarshalPKCS1PrivateKey(key)),
		CreatedAt: core.Now(), ModifiedAt: core.Now(), Tags: tags, Extra: extra}
	return p, store.Put(s.env.Store, cPools, id, p)
}

// modPool updates a pool under the store's lock.
func (s *Service) modPool(id string, fn func(*Pool) error) (Pool, error) {
	p, err := store.Update(s.env.Store, cPools, id, func(p *Pool) error {
		if err := fn(p); err != nil {
			return err
		}
		p.ModifiedAt = core.Now()
		return nil
	})
	if err == store.ErrNotFound {
		return p, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "user pool %q does not exist", id)
	}
	return p, err
}

func (s *Service) getPool(c *httpx.Ctx) (any, error) {
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return nil, err
	}
	return s.poolView(p), nil
}

func (s *Service) updatePool(c *httpx.Ctx) (any, error) {
	in := struct {
		PasswordPolicy *PasswordPolicy `json:"password_policy"`
		AutoConfirm    *bool           `json:"auto_confirm"`
		SelfSignUp     *bool           `json:"self_sign_up"`
	}{}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	p, err := s.modPool(c.Param("pool"), func(p *Pool) error {
		if in.PasswordPolicy != nil {
			p.PasswordPolicy = *in.PasswordPolicy
			if err := checkPolicy(&p.PasswordPolicy); err != nil {
				return err
			}
		}
		if in.AutoConfirm != nil {
			p.AutoConfirm = *in.AutoConfirm
		}
		if in.SelfSignUp != nil {
			p.SelfSignUp = *in.SelfSignUp
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.poolView(p), nil
}

func (s *Service) deletePool(c *httpx.Ctx) (any, error) {
	return nil, s.removePool(c.Param("pool"))
}

// removePool deletes a pool with its users, clients and refresh tokens.
func (s *Service) removePool(id string) error {
	if _, err := s.pool(id); err != nil {
		return err
	}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if u.PoolID == id {
			_ = store.Delete(s.env.Store, cUsers, userKey(id, u.Username))
		}
	}
	for _, cl := range store.List[Client](s.env.Store, cClients) {
		if cl.PoolID == id {
			_ = store.Delete(s.env.Store, cClients, cl.ID)
		}
	}
	_ = s.env.Store.Retain(cRefresh, func(_ string, raw json.RawMessage) bool {
		var r refresh
		return json.Unmarshal(raw, &r) != nil || r.PoolID != id
	})
	s.mu.Lock()
	delete(s.keys, id)
	s.mu.Unlock()
	return store.Delete(s.env.Store, cPools, id)
}

func (s *Service) listClients(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, cl := range store.List[Client](s.env.Store, cClients) {
		if cl.PoolID == c.Param("pool") {
			out = append(out, map[string]any{"id": cl.ID, "name": cl.Name, "has_secret": cl.SecretHash != "",
				"access_token_minutes": cl.AccessTokenMinutes, "refresh_token_days": cl.RefreshTokenDays, "created_at": cl.CreatedAt})
		}
	}
	return out, nil
}

func (s *Service) createClient(c *httpx.Ctx) (any, error) {
	in := struct {
		Name               string `json:"name"`
		GenerateSecret     bool   `json:"generate_secret"`
		AccessTokenMinutes int    `json:"access_token_minutes"`
		RefreshTokenDays   int    `json:"refresh_token_days"`
	}{}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	cl, secret, err := s.newClient(c.Param("pool"), in.Name, in.GenerateSecret, in.AccessTokenMinutes, in.RefreshTokenDays, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"id": cl.ID, "name": cl.Name, "access_token_minutes": cl.AccessTokenMinutes, "refresh_token_days": cl.RefreshTokenDays}
	if secret != "" {
		out["client_secret"] = secret // shown once
	}
	return out, nil
}

// newClient creates an app client and returns it with its secret (when
// generated). Zero token lifetimes select the defaults.
func (s *Service) newClient(poolID, name string, genSecret bool, accessMinutes, refreshDays int, extra map[string]any) (Client, string, error) {
	if _, err := s.pool(poolID); err != nil {
		return Client{}, "", err
	}
	if accessMinutes == 0 {
		accessMinutes = 60
	}
	if refreshDays == 0 {
		refreshDays = 30
	}
	if accessMinutes < 5 || accessMinutes > 1440 || refreshDays < 1 || refreshDays > 3650 {
		return Client{}, "", core.BadRequest("access_token_minutes must be 5-1440 and refresh_token_days 1-3650")
	}
	cl := Client{ID: strings.ToLower(core.NewSecret(26)), PoolID: poolID, Name: name, AccessTokenMinutes: accessMinutes,
		RefreshTokenDays: refreshDays, CreatedAt: core.Now(), ModifiedAt: core.Now(), Extra: extra}
	secret := ""
	if genSecret {
		secret = core.NewSecret(48)
		cl.SecretHash = hashToken(secret)
		cl.SecretCT = s.secrets.Encrypt([]byte(secret))
	}
	return cl, secret, store.Put(s.env.Store, cClients, cl.ID, cl)
}

func (s *Service) deleteClient(c *httpx.Ctx) (any, error) {
	return nil, s.removeClient(c.Param("pool"), c.Param("client"))
}

func (s *Service) removeClient(poolID, clientID string) error {
	cl, err := store.Get[Client](s.env.Store, cClients, clientID)
	if err != nil || cl.PoolID != poolID {
		return core.Errf(http.StatusNotFound, "ResourceNotFoundException", "client %q does not exist", clientID)
	}
	return store.Delete(s.env.Store, cClients, cl.ID)
}

func (s *Service) listUsers(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	filter := strings.ToLower(c.Query("filter"))
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if u.PoolID != c.Param("pool") {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(u.Username+" "+u.Attributes["email"]), filter) {
			continue
		}
		out = append(out, u.view())
	}
	return out, nil
}

var usernameRe = regexp.MustCompile(`^[\p{L}\p{M}\p{S}\p{N}\p{P}]{1,128}$`)

// reservedClaims may not be used as user attribute names.
var reservedClaims = map[string]bool{"sub": true, "aud": true, "iss": true, "exp": true, "iat": true, "nbf": true, "auth_time": true,
	"token_use": true, "client_id": true, "username": true, "jti": true, "scope": true, "event_id": true, "origin_jti": true}

func checkAttrs(attrs map[string]string) error {
	for k, v := range attrs {
		if reservedClaims[k] || strings.HasPrefix(k, "cognito:") || strings.HasPrefix(k, "hc:") || k == "" || len(k) > 64 {
			return core.BadRequest("attribute name %q is reserved or invalid", k)
		}
		if len(v) > 2048 {
			return core.BadRequest("attribute %q is longer than 2048 characters", k)
		}
	}
	return nil
}

func (s *Service) newUser(p Pool, username, password string, attrs map[string]string, status string) (User, error) {
	if !usernameRe.MatchString(username) {
		return User{}, core.BadRequest("invalid username")
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if store.Has(s.env.Store, cUsers, userKey(p.ID, username)) {
		return User{}, core.Errf(http.StatusConflict, "UsernameExistsException", "user %q already exists", username)
	}
	if err := p.PasswordPolicy.check(password); err != nil {
		return User{}, err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	if attrs == nil {
		attrs = map[string]string{}
	}
	if err := checkAttrs(attrs); err != nil {
		return User{}, err
	}
	salt, ver := newVerifier(p.ID, username, password)
	h2 := core.RandHex(32)
	u := User{PoolID: p.ID, Username: username, Sub: h2[0:8] + "-" + h2[8:12] + "-" + h2[12:16] + "-" + h2[16:20] + "-" + h2[20:32],
		Attributes: attrs, PasswordHash: string(h), SRPSalt: salt, SRPVerifier: ver, Status: status, Enabled: true, Groups: []string{}, CreatedAt: core.Now(), ModifiedAt: core.Now()}
	return u, store.Put(s.env.Store, cUsers, userKey(p.ID, username), u)
}

func (s *Service) adminCreateUser(c *httpx.Ctx) (any, error) {
	in := struct {
		Username          string            `json:"username"`
		Password          string            `json:"password"`
		TemporaryPassword bool              `json:"temporary_password"`
		Attributes        map[string]string `json:"attributes"`
		Groups            []string          `json:"groups"`
	}{}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return nil, err
	}
	u, generated, err := s.adminCreate(p, in.Username, in.Password, in.TemporaryPassword, in.Attributes, in.Groups)
	if err != nil {
		return nil, err
	}
	v := u.view()
	if generated != "" {
		v["temporary_password"] = generated
	}
	return v, nil
}

// adminCreate creates a confirmed user, or one that must change the
// temporary password at first sign-in. Without a password a temporary one is
// generated and returned.
func (s *Service) adminCreate(p Pool, username, password string, temporary bool, attrs map[string]string, groups []string) (User, string, error) {
	generated := ""
	if password == "" {
		// Satisfy any policy: long enough, with every character class.
		generated = core.NewSecret(max(p.PasswordPolicy.MinLength, 12)) + "aA1!"
		password, temporary = generated, true
	}
	status := "CONFIRMED"
	if temporary {
		status = "FORCE_CHANGE_PASSWORD"
	}
	for _, g := range groups {
		if !slices.ContainsFunc(p.Groups, func(x Group) bool { return x.Name == g }) {
			return User{}, "", core.NotFound("group", g)
		}
	}
	u, err := s.newUser(p, username, password, attrs, status)
	if err != nil {
		return u, "", err
	}
	if len(groups) > 0 {
		u, _ = store.Update(s.env.Store, cUsers, userKey(p.ID, u.Username), func(x *User) error { x.Groups = groups; return nil })
	}
	return u, generated, nil
}

func (s *Service) userOf(c *httpx.Ctx) (User, error) {
	return s.getUser(c.Param("pool"), c.Param("user"))
}

func (s *Service) getUser(pool, name string) (User, error) {
	u, err := store.Get[User](s.env.Store, cUsers, userKey(pool, name))
	if err != nil {
		return u, core.Errf(http.StatusNotFound, "UserNotFoundException", "user %q does not exist", name)
	}
	return u, nil
}

func (s *Service) adminGetUser(c *httpx.Ctx) (any, error) {
	u, err := s.userOf(c)
	if err != nil {
		return nil, err
	}
	return u.view(), nil
}

func (s *Service) adminDeleteUser(c *httpx.Ctx) (any, error) {
	return nil, s.removeUser(c.Param("pool"), c.Param("user"))
}

func (s *Service) removeUser(pool, name string) error {
	u, err := s.getUser(pool, name)
	if err != nil {
		return err
	}
	s.revokeAll(u.PoolID, u.Username)
	return store.Delete(s.env.Store, cUsers, userKey(u.PoolID, u.Username))
}

func (s *Service) modUser(pool, name string, fn func(*User) error) (User, error) {
	if _, err := s.getUser(pool, name); err != nil {
		return User{}, err
	}
	return store.Update(s.env.Store, cUsers, userKey(pool, name), func(u *User) error {
		if err := fn(u); err != nil {
			return err
		}
		u.ModifiedAt = core.Now()
		return nil
	})
}

func (s *Service) updateUser(c *httpx.Ctx, fn func(*User) error) (any, error) {
	u, err := s.modUser(c.Param("pool"), c.Param("user"), fn)
	if err != nil {
		return nil, err
	}
	return u.view(), nil
}

func (s *Service) adminUpdateUser(c *httpx.Ctx) (any, error) {
	in := struct {
		Attributes map[string]string `json:"attributes"`
		Enabled    *bool             `json:"enabled"`
		Groups     *[]string         `json:"groups"`
		Confirm    bool              `json:"confirm"`
	}{}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	u, err := s.patchUser(c.Param("pool"), c.Param("user"), in.Attributes, in.Enabled, in.Groups, in.Confirm)
	if err != nil {
		return nil, err
	}
	return u.view(), nil
}

// patchUser changes attributes (an empty value deletes one), the enabled
// flag, group membership, and confirms an unconfirmed user.
func (s *Service) patchUser(pool, name string, attrs map[string]string, enabled *bool, groups *[]string, confirm bool) (User, error) {
	p, err := s.pool(pool)
	if err != nil {
		return User{}, err
	}
	if err := checkAttrs(attrs); err != nil {
		return User{}, err
	}
	return s.modUser(pool, name, func(u *User) error {
		if u.Attributes == nil {
			u.Attributes = map[string]string{}
		}
		for k, v := range attrs {
			if v == "" {
				delete(u.Attributes, k)
			} else {
				u.Attributes[k] = v
			}
		}
		if enabled != nil {
			u.Enabled = *enabled
		}
		if groups != nil {
			for _, g := range *groups {
				if !slices.ContainsFunc(p.Groups, func(x Group) bool { return x.Name == g }) {
					return core.NotFound("group", g)
				}
			}
			u.Groups = slices.Clone(*groups)
		}
		if confirm && u.Status == "UNCONFIRMED" {
			u.Status, u.ConfirmCode = "CONFIRMED", ""
		}
		return nil
	})
}

func (s *Service) adminSetPassword(c *httpx.Ctx) (any, error) {
	in := struct {
		Password  string `json:"password"`
		Permanent bool   `json:"permanent"`
	}{}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	u, err := s.setPassword(c.Param("pool"), c.Param("user"), in.Password, in.Permanent)
	if err != nil {
		return nil, err
	}
	return u.view(), nil
}

// setPassword replaces a user's password: permanent, or temporary (the user
// must choose a new one at the next sign-in).
func (s *Service) setPassword(pool, name, password string, permanent bool) (User, error) {
	p, err := s.pool(pool)
	if err != nil {
		return User{}, err
	}
	if err := p.PasswordPolicy.check(password); err != nil {
		return User{}, err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	cur, err := s.getUser(pool, name)
	if err != nil {
		return User{}, err
	}
	salt, ver := newVerifier(pool, cur.Username, password)
	return s.modUser(pool, name, func(u *User) error {
		u.PasswordHash, u.SRPSalt, u.SRPVerifier = string(h), salt, ver
		u.ResetCode = ""
		if permanent {
			u.Status = "CONFIRMED"
		} else {
			u.Status = "FORCE_CHANGE_PASSWORD"
		}
		return nil
	})
}

// revokeAll invalidates every token of a user: refresh tokens are deleted and
// the token version moves on, so outstanding ID and access tokens stop verifying.
func (s *Service) revokeAll(pool, username string) {
	_ = s.env.Store.Retain(cRefresh, func(_ string, raw json.RawMessage) bool {
		var r refresh
		return json.Unmarshal(raw, &r) != nil || !(r.PoolID == pool && strings.EqualFold(r.Username, username))
	})
	_, _ = store.Update(s.env.Store, cUsers, userKey(pool, username), func(u *User) error { u.TokenVersion++; return nil })
}

func (s *Service) adminSignOut(c *httpx.Ctx) (any, error) {
	u, err := s.userOf(c)
	if err != nil {
		return nil, err
	}
	s.revokeAll(u.PoolID, u.Username)
	return nil, nil
}

func (s *Service) createGroup(c *httpx.Ctx) (any, error) {
	var g Group
	if err := c.Bind(&g); err != nil {
		return nil, err
	}
	p, err := s.newGroup(c.Param("pool"), g)
	if err != nil {
		return nil, err
	}
	return s.poolView(p), nil
}

// newGroup adds a group to a pool.
func (s *Service) newGroup(pool string, g Group) (Pool, error) {
	if !poolName.MatchString(g.Name) {
		return Pool{}, core.BadRequest("invalid group name")
	}
	return s.modPool(pool, func(p *Pool) error {
		if slices.ContainsFunc(p.Groups, func(x Group) bool { return x.Name == g.Name }) {
			return core.Errf(http.StatusConflict, "GroupExistsException", "group %q already exists", g.Name)
		}
		g.CreatedAt = core.Now()
		p.Groups = append(p.Groups, g)
		return nil
	})
}

func (s *Service) deleteGroup(c *httpx.Ctx) (any, error) {
	p, err := s.removeGroup(c.Param("pool"), c.Param("group"))
	if err != nil {
		return nil, err
	}
	return s.poolView(p), nil
}

// removeGroup deletes a group and its memberships.
func (s *Service) removeGroup(pool, g string) (Pool, error) {
	p, err := s.modPool(pool, func(p *Pool) error {
		n := len(p.Groups)
		p.Groups = slices.DeleteFunc(p.Groups, func(x Group) bool { return x.Name == g })
		if len(p.Groups) == n {
			return core.Errf(http.StatusNotFound, "ResourceNotFoundException", "group %q does not exist", g)
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if u.PoolID == p.ID && slices.Contains(u.Groups, g) {
			_, _ = store.Update(s.env.Store, cUsers, userKey(p.ID, u.Username), func(x *User) error {
				x.Groups = slices.DeleteFunc(x.Groups, func(y string) bool { return y == g })
				return nil
			})
		}
	}
	return p, nil
}

// ---- public (application-facing) routes ----

func (s *Service) publicRoutes(r *httpx.Router) {
	r.Handle("POST /cognito/{pool}/sign-up", "", s.signUp, httpx.Public(), httpx.SmallBody())
	r.Handle("POST /cognito/{pool}/auth", "", s.auth, httpx.Public(), httpx.SmallBody())
	r.Handle("POST /cognito/{pool}/respond", "", s.respond, httpx.Public(), httpx.SmallBody())
	r.Handle("POST /cognito/{pool}/change-password", "", s.changePassword, httpx.Public(), httpx.SmallBody())
	r.Handle("GET /cognito/{pool}/userinfo", "", s.userinfo, httpx.Public())
	r.Handle("POST /cognito/{pool}/sign-out", "", s.signOut, httpx.Public(), httpx.SmallBody())
	r.Handle("GET /cognito/{pool}/.well-known/jwks.json", "", s.jwks, httpx.Public())
	r.Handle("GET /cognito/{pool}/.well-known/openid-configuration", "", s.discovery, httpx.Public())
}

func (s *Service) client(p Pool, id, secret string) (Client, error) {
	cl, err := store.Get[Client](s.env.Store, cClients, id)
	if err != nil || cl.PoolID != p.ID {
		return cl, core.Errf(http.StatusBadRequest, "ResourceNotFoundException", "app client %q does not exist in this pool", id)
	}
	if cl.SecretHash != "" && subtle.ConstantTimeCompare([]byte(cl.SecretHash), []byte(hashToken(secret))) != 1 {
		return cl, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "client secret is missing or wrong")
	}
	return cl, nil
}

// attempt records a sign-in attempt for key and reports whether it is allowed.
// Attempts are counted before the password check so parallel guesses cannot
// slip past the limit; a success clears the record.
func (s *Service) attempt(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.fails[key][:0]
	for _, t := range s.fails[key] {
		if time.Since(t) < 5*time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 10 {
		s.fails[key] = recent
		return false
	}
	s.fails[key] = append(recent, time.Now())
	if len(s.fails) > 100000 { // bound memory under a flood of distinct keys
		for k, ts := range s.fails {
			if len(ts) == 0 || time.Since(ts[len(ts)-1]) > 5*time.Minute {
				delete(s.fails, k)
			}
		}
	}
	return true
}

func (s *Service) succeeded(key string) {
	s.mu.Lock()
	delete(s.fails, key)
	s.mu.Unlock()
}

type authInput struct {
	ClientID     string            `json:"client_id"`
	ClientSecret string            `json:"client_secret"`
	Flow         string            `json:"flow"`
	Username     string            `json:"username"`
	Password     string            `json:"password"`
	RefreshToken string            `json:"refresh_token"`
	Attributes   map[string]string `json:"attributes"`
	Session      string            `json:"session"`
	NewPassword  string            `json:"new_password"`
}

func (s *Service) signUp(c *httpx.Ctx) (any, error) {
	var in authInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	u, err := s.selfSignUp(c.Param("pool"), in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"user_sub": u.Sub, "user_confirmed": u.Status == "CONFIRMED"}, nil
}

// selfSignUp registers a user without administrator credentials. The user is
// confirmed at once when the pool auto-confirms; otherwise a confirmation code
// is issued (see issueCode) and the user must confirm it or be confirmed by an
// administrator before signing in.
func (s *Service) selfSignUp(poolID string, in authInput) (User, error) {
	p, err := s.pool(poolID)
	if err != nil {
		return User{}, err
	}
	if !p.SelfSignUp {
		return User{}, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "self sign-up is disabled for this user pool")
	}
	if _, err := s.client(p, in.ClientID, in.ClientSecret); err != nil {
		return User{}, err
	}
	status := "UNCONFIRMED"
	if p.AutoConfirm {
		status = "CONFIRMED"
	}
	u, err := s.newUser(p, in.Username, in.Password, in.Attributes, status)
	if err != nil {
		return u, err
	}
	if status == "UNCONFIRMED" {
		if err := s.issueCode(p.ID, u.Username); err != nil {
			return u, err
		}
	}
	return u, nil
}

// issueCode creates a sign-up confirmation code for the user. HomeCloud sends
// no e-mail or SMS, so the code is written to the server log instead.
func (s *Service) issueCode(pool, username string) error {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	if _, err := s.modUser(pool, username, func(u *User) error {
		u.ConfirmCode, u.ConfirmCodeExpires = hashToken(code), time.Now().Add(24*time.Hour)
		return nil
	}); err != nil {
		return err
	}
	log.Printf("cognito: confirmation code for user %q in pool %s is %s (nothing is e-mailed; AdminConfirmSignUp confirms without it)", username, pool, code)
	return nil
}

// confirmSignUp confirms a self-registered user with the code from issueCode.
func (s *Service) confirmSignUp(poolID, username, code string) error {
	if _, err := s.pool(poolID); err != nil {
		return err
	}
	key := poolID + "/confirm/" + strings.ToLower(username)
	if !s.attempt(key) {
		return core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many failed attempts; try again later")
	}
	u, err := s.getUser(poolID, username)
	if err != nil {
		return err
	}
	if u.Status != "UNCONFIRMED" {
		return core.Errf(http.StatusBadRequest, "NotAuthorizedException", "user cannot be confirmed. Current status is %s", u.Status)
	}
	if u.ConfirmCode == "" || time.Now().After(u.ConfirmCodeExpires) {
		return core.Errf(http.StatusBadRequest, "ExpiredCodeException", "the confirmation code has expired; request a new one")
	}
	if subtle.ConstantTimeCompare([]byte(u.ConfirmCode), []byte(hashToken(strings.TrimSpace(code)))) != 1 {
		return core.Errf(http.StatusBadRequest, "CodeMismatchException", "invalid verification code provided")
	}
	s.succeeded(key)
	_, err = s.modUser(poolID, username, func(u *User) error { u.Status, u.ConfirmCode = "CONFIRMED", ""; return nil })
	return err
}

func (s *Service) auth(c *httpx.Ctx) (any, error) {
	var in authInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.authenticate(c.Param("pool"), in, httpx.ClientIP(c.R))
}

// authenticate runs a sign-in flow (USER_PASSWORD_AUTH or REFRESH_TOKEN_AUTH).
// It returns *tokens, or a map with "challenge" and "session" when the user
// must first choose a new password.
func (s *Service) authenticate(poolID string, in authInput, ip string) (any, error) {
	p, err := s.pool(poolID)
	if err != nil {
		return nil, err
	}
	cl, err := s.client(p, in.ClientID, in.ClientSecret)
	if err != nil {
		return nil, err
	}
	deny := core.Errf(http.StatusBadRequest, "NotAuthorizedException", "incorrect username or password")
	switch in.Flow {
	case "", "USER_PASSWORD_AUTH":
		tk := p.ID + "/" + strings.ToLower(in.Username) + "/" + ip
		if !s.attempt(tk) {
			return nil, core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many failed attempts; try again later")
		}
		u, err := store.Get[User](s.env.Store, cUsers, userKey(p.ID, in.Username))
		hash := []byte(u.PasswordHash)
		if err != nil {
			hash = dummyHash
		}
		if bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) != nil || err != nil {
			return nil, deny
		}
		s.succeeded(tk)
		if u.SRPVerifier == "" { // a user from before SRP support: derive the verifier now that the password is known
			salt, ver := newVerifier(p.ID, u.Username, in.Password)
			_, _ = store.Update(s.env.Store, cUsers, userKey(p.ID, u.Username), func(x *User) error {
				if x.SRPVerifier == "" && x.PasswordHash == u.PasswordHash {
					x.SRPSalt, x.SRPVerifier = salt, ver
				}
				return nil
			})
		}
		return s.signedIn(p, cl, u)
	case "REFRESH_TOKEN_AUTH":
		rt, err := store.Get[refresh](s.env.Store, cRefresh, hashToken(in.RefreshToken))
		if err != nil || rt.PoolID != p.ID || rt.ClientID != cl.ID || time.Now().After(rt.Expires) {
			return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "invalid refresh token")
		}
		u, err := store.Get[User](s.env.Store, cUsers, userKey(p.ID, rt.Username))
		if err != nil || !u.Enabled || u.Sub != rt.Sub || u.TokenVersion != rt.Version {
			return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "refresh token is no longer valid")
		}
		return s.issue(p, cl, u, false)
	}
	return nil, core.BadRequest("flow must be USER_PASSWORD_AUTH or REFRESH_TOKEN_AUTH")
}

// signedIn finishes a sign-in whose password is proven: the user's status
// decides between tokens and a challenge or error.
func (s *Service) signedIn(p Pool, cl Client, u User) (any, error) {
	if !u.Enabled {
		return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "user is disabled")
	}
	switch u.Status {
	case "UNCONFIRMED":
		return nil, core.Errf(http.StatusBadRequest, "UserNotConfirmedException", "user is not confirmed")
	case "RESET_REQUIRED":
		return nil, core.Errf(http.StatusBadRequest, "PasswordResetRequiredException", "Password reset required for the user")
	case "FORCE_CHANGE_PASSWORD":
		session := core.NewSecret(48)
		if err := store.Put(s.env.Store, cSessions, hashToken(session), challenge{PoolID: p.ID, ClientID: cl.ID, Username: u.Username, Expires: time.Now().Add(5 * time.Minute)}); err != nil {
			return nil, err
		}
		return map[string]any{"challenge": "NEW_PASSWORD_REQUIRED", "session": session, "username": u.Username}, nil
	}
	return s.issue(p, cl, u, true)
}

func (s *Service) respond(c *httpx.Ctx) (any, error) {
	var in authInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.respondChallenge(c.Param("pool"), in)
}

// respondChallenge answers the NEW_PASSWORD_REQUIRED challenge and signs the
// user in.
func (s *Service) respondChallenge(poolID string, in authInput) (any, error) {
	p, err := s.pool(poolID)
	if err != nil {
		return nil, err
	}
	ch, err := store.Get[challenge](s.env.Store, cSessions, hashToken(in.Session))
	if err != nil || ch.PoolID != p.ID || time.Now().After(ch.Expires) ||
		(in.ClientID != "" && in.ClientID != ch.ClientID) || (in.Username != "" && !strings.EqualFold(in.Username, ch.Username)) {
		return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "invalid or expired session")
	}
	if err := p.PasswordPolicy.check(in.NewPassword); err != nil {
		return nil, err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	salt, ver := newVerifier(p.ID, ch.Username, in.NewPassword)
	u, err := store.Update(s.env.Store, cUsers, userKey(p.ID, ch.Username), func(u *User) error {
		if !u.Enabled {
			return core.Errf(http.StatusBadRequest, "NotAuthorizedException", "user is disabled")
		}
		u.PasswordHash, u.Status = string(h), "CONFIRMED"
		u.SRPSalt, u.SRPVerifier = salt, ver
		return nil
	})
	if err != nil {
		return nil, err
	}
	_ = store.Delete(s.env.Store, cSessions, hashToken(in.Session))
	cl, err := store.Get[Client](s.env.Store, cClients, ch.ClientID)
	if err != nil {
		return nil, err
	}
	return s.issue(p, cl, u, true)
}

// bearer verifies the request's access token and returns its user.
func (s *Service) bearer(c *httpx.Ctx) (Pool, User, error) {
	return s.verifyAccess(c.Param("pool"), strings.TrimPrefix(c.R.Header.Get("Authorization"), "Bearer "))
}

// PoolOfToken reads the user pool ID from a token's issuer without trusting
// the token; verify it with verifyAccess.
func (s *Service) poolOfToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := b64.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	i := strings.LastIndex(c.Iss, "/cognito/")
	if i < 0 {
		return ""
	}
	return c.Iss[i+len("/cognito/"):]
}

// verifyAccess verifies an access token of the pool and returns its user.
func (s *Service) verifyAccess(poolID, tok string) (Pool, User, error) {
	p, err := s.pool(poolID)
	if err != nil {
		return p, User{}, err
	}
	claims, err := s.VerifyToken(p.ID, tok, "")
	if err != nil {
		return p, User{}, core.Errf(http.StatusUnauthorized, "NotAuthorizedException", "%v", err)
	}
	if claims["token_use"] != "access" {
		return p, User{}, core.Errf(http.StatusUnauthorized, "NotAuthorizedException", "an access token is required")
	}
	u, err := store.Get[User](s.env.Store, cUsers, userKey(p.ID, fmt.Sprint(claims["username"])))
	if err != nil || u.Sub != claims["sub"] {
		return p, User{}, core.Errf(http.StatusUnauthorized, "NotAuthorizedException", "user no longer exists")
	}
	return p, u, nil
}

func (s *Service) userinfo(c *httpx.Ctx) (any, error) {
	_, u, err := s.bearer(c)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"sub": u.Sub, "username": u.Username, "groups": u.Groups}
	for k, v := range u.Attributes {
		out[k] = v
	}
	return out, nil
}

func (s *Service) changePassword(c *httpx.Ctx) (any, error) {
	in := struct {
		Old string `json:"previous_password"`
		New string `json:"proposed_password"`
	}{}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	p, u, err := s.bearer(c)
	if err != nil {
		return nil, err
	}
	return nil, s.changePasswordOf(p, u, in.Old, in.New)
}

// changePasswordOf sets a new password after checking the old one.
func (s *Service) changePasswordOf(p Pool, u User, oldPw, newPw string) error {
	tk := p.ID + "/change/" + u.Sub
	if !s.attempt(tk) {
		return core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many failed attempts; try again later")
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(oldPw)) != nil {
		return core.Errf(http.StatusBadRequest, "NotAuthorizedException", "incorrect password")
	}
	s.succeeded(tk)
	if err := p.PasswordPolicy.check(newPw); err != nil {
		return err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(newPw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	salt, ver := newVerifier(p.ID, u.Username, newPw)
	_, err = s.modUser(p.ID, u.Username, func(x *User) error {
		x.PasswordHash, x.SRPSalt, x.SRPVerifier = string(h), salt, ver
		return nil
	})
	return err
}

func (s *Service) signOut(c *httpx.Ctx) (any, error) {
	p, u, err := s.bearer(c)
	if err != nil {
		return nil, err
	}
	s.revokeAll(p.ID, u.Username)
	return nil, nil
}

func (s *Service) jwks(c *httpx.Ctx) (any, error) {
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return nil, err
	}
	k, err := s.key(p)
	if err != nil {
		return nil, err
	}
	return map[string]any{"keys": []any{jwk(&k.PublicKey, p.KeyID)}}, nil
}

func (s *Service) discovery(c *httpx.Ctx) (any, error) {
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return nil, err
	}
	iss := s.issuer(p.ID)
	return map[string]any{"issuer": iss, "jwks_uri": iss + "/.well-known/jwks.json", "userinfo_endpoint": iss + "/userinfo",
		"id_token_signing_alg_values_supported": []string{"RS256"}, "subject_types_supported": []string{"public"},
		"response_types_supported": []string{"token"}}, nil
}
