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
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

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
}

type Group struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Precedence  int    `json:"precedence"`
}

type Client struct {
	ID                 string    `json:"id"`
	PoolID             string    `json:"pool_id"`
	Name               string    `json:"name"`
	SecretHash         string    `json:"secret_hash,omitempty"`
	AccessTokenMinutes int       `json:"access_token_minutes"`
	RefreshTokenDays   int       `json:"refresh_token_days"`
	CreatedAt          time.Time `json:"created_at"`
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
	LastSignIn   *time.Time        `json:"last_sign_in,omitempty"`
	TokensAfter  time.Time         `json:"tokens_after"` // access tokens issued before this are revoked
}

func (u User) view() map[string]any {
	return map[string]any{"username": u.Username, "sub": u.Sub, "attributes": u.Attributes, "status": u.Status, "enabled": u.Enabled,
		"groups": u.Groups, "created_at": u.CreatedAt, "last_sign_in": u.LastSignIn}
}

type refresh struct {
	PoolID   string    `json:"pool_id"`
	ClientID string    `json:"client_id"`
	Username string    `json:"username"`
	Expires  time.Time `json:"expires"`
}

type challenge struct {
	PoolID   string    `json:"pool_id"`
	ClientID string    `json:"client_id"`
	Username string    `json:"username"`
	Expires  time.Time `json:"expires"`
}

type Service struct {
	env     *svc.Env
	secrets *secrets.Service
	mu      sync.Mutex
	keys    map[string]*rsa.PrivateKey
	fails   map[string][]time.Time
}

func New(env *svc.Env, sec *secrets.Service) *Service {
	return &Service{env: env, secrets: sec, keys: map[string]*rsa.PrivateKey{}, fails: map[string][]time.Time{}}
}

func userKey(pool, username string) string { return pool + "/" + strings.ToLower(username) }
func hashToken(t string) string            { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

func (s *Service) issuer(pool string) string {
	scheme := "http"
	if s.env.Cfg.TLSCert != "" {
		scheme = "https"
	}
	_, port, _ := strings.Cut(s.env.Cfg.APIAddr, ":")
	return fmt.Sprintf("%s://%s:%s/cognito/%s", scheme, s.env.Cfg.PublicHost, port, pool)
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
	if len(pw) < p.MinLength {
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
	id := map[string]any{"sub": u.Sub, "aud": c.ID, "iss": s.issuer(p.ID), "token_use": "id", "auth_time": now.Unix(), "iat": now.Unix(),
		"exp": exp.Unix(), "cognito:username": u.Username, "cognito:groups": groups}
	for k, v := range u.Attributes {
		id[k] = v
	}
	access := map[string]any{"sub": u.Sub, "iss": s.issuer(p.ID), "client_id": c.ID, "token_use": "access", "scope": "openid profile",
		"auth_time": now.Unix(), "iat": now.Unix(), "exp": exp.Unix(), "jti": core.RandHex(32), "username": u.Username, "cognito:groups": groups}
	t := &tokens{ExpiresIn: int(exp.Sub(now).Seconds()), TokenType: "Bearer"}
	if t.IDToken, err = sign(k, p.KeyID, id); err != nil {
		return nil, err
	}
	if t.AccessToken, err = sign(k, p.KeyID, access); err != nil {
		return nil, err
	}
	if withRefresh {
		rt := core.NewSecret(64)
		r := refresh{PoolID: p.ID, ClientID: c.ID, Username: u.Username, Expires: now.Add(time.Duration(c.RefreshTokenDays) * 24 * time.Hour)}
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
	username, _ := claims["cognito:username"].(string)
	if username == "" {
		username, _ = claims["username"].(string)
	}
	u, err := store.Get[User](s.env.Store, cUsers, userKey(p.ID, username))
	if err != nil || !u.Enabled {
		return nil, fmt.Errorf("user is disabled or deleted")
	}
	if iat, _ := claims["iat"].(float64); int64(iat) < u.TokensAfter.Unix() {
		return nil, fmt.Errorf("token was revoked by a sign-out")
	}
	return claims, nil
}

// ---- admin routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:cognito-idp:local-1:{account}:userpool/{pool}")
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
	if !poolName.MatchString(in.Name) {
		return nil, core.BadRequest("pool name must be 1-128 characters")
	}
	pp := PasswordPolicy{MinLength: 8, RequireLowercase: true, RequireNumbers: true}
	if in.PasswordPolicy != nil {
		pp = *in.PasswordPolicy
		if pp.MinLength < 6 {
			pp.MinLength = 6
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	id := core.DefaultRegion + "_" + strings.ToUpper(core.RandHex(9))
	p := Pool{ID: id, ARN: s.env.ARN("cognito-idp", "userpool/"+id), Name: in.Name, PasswordPolicy: pp, AutoConfirm: in.AutoConfirm == nil || *in.AutoConfirm,
		SelfSignUp: in.SelfSignUp == nil || *in.SelfSignUp, Groups: []Group{}, KeyID: core.RandHex(16),
		PrivateKeyCT: s.secrets.Encrypt(x509.MarshalPKCS1PrivateKey(key)), CreatedAt: core.Now()}
	return s.poolView(p), store.Put(s.env.Store, cPools, id, p)
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
	p, err := store.Update(s.env.Store, cPools, c.Param("pool"), func(p *Pool) error {
		if in.PasswordPolicy != nil {
			p.PasswordPolicy = *in.PasswordPolicy
			if p.PasswordPolicy.MinLength < 6 {
				p.PasswordPolicy.MinLength = 6
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
	if err == store.ErrNotFound {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "user pool %q does not exist", c.Param("pool"))
	}
	if err != nil {
		return nil, err
	}
	return s.poolView(p), nil
}

func (s *Service) deletePool(c *httpx.Ctx) (any, error) {
	id := c.Param("pool")
	if _, err := s.pool(id); err != nil {
		return nil, err
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
	_ = s.env.Store.Retain(cRefresh, func(_ string, raw json.RawMessage) bool { return !strings.Contains(string(raw), `"pool_id":"`+id+`"`) })
	s.mu.Lock()
	delete(s.keys, id)
	s.mu.Unlock()
	return nil, store.Delete(s.env.Store, cPools, id)
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
	if _, err := s.pool(c.Param("pool")); err != nil {
		return nil, err
	}
	if in.AccessTokenMinutes == 0 {
		in.AccessTokenMinutes = 60
	}
	if in.RefreshTokenDays == 0 {
		in.RefreshTokenDays = 30
	}
	if in.AccessTokenMinutes < 5 || in.AccessTokenMinutes > 1440 || in.RefreshTokenDays < 1 || in.RefreshTokenDays > 3650 {
		return nil, core.BadRequest("access_token_minutes must be 5-1440 and refresh_token_days 1-3650")
	}
	cl := Client{ID: strings.ToLower(core.NewSecret(26)), PoolID: c.Param("pool"), Name: in.Name, AccessTokenMinutes: in.AccessTokenMinutes,
		RefreshTokenDays: in.RefreshTokenDays, CreatedAt: core.Now()}
	out := map[string]any{"id": cl.ID, "name": cl.Name, "access_token_minutes": cl.AccessTokenMinutes, "refresh_token_days": cl.RefreshTokenDays}
	if in.GenerateSecret {
		secret := core.NewSecret(48)
		cl.SecretHash = hashToken(secret)
		out["client_secret"] = secret // shown once
	}
	return out, store.Put(s.env.Store, cClients, cl.ID, cl)
}

func (s *Service) deleteClient(c *httpx.Ctx) (any, error) {
	cl, err := store.Get[Client](s.env.Store, cClients, c.Param("client"))
	if err != nil || cl.PoolID != c.Param("pool") {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "client %q does not exist", c.Param("client"))
	}
	return nil, store.Delete(s.env.Store, cClients, cl.ID)
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

func (s *Service) newUser(p Pool, username, password string, attrs map[string]string, status string) (User, error) {
	if !usernameRe.MatchString(username) {
		return User{}, core.BadRequest("invalid username")
	}
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
	for k := range attrs {
		if k == "sub" || strings.HasPrefix(k, "cognito:") {
			return User{}, core.BadRequest("attribute %q is reserved", k)
		}
	}
	h2 := core.RandHex(32)
	u := User{PoolID: p.ID, Username: username, Sub: h2[0:8] + "-" + h2[8:12] + "-" + h2[12:16] + "-" + h2[16:20] + "-" + h2[20:32],
		Attributes: attrs, PasswordHash: string(h), Status: status, Enabled: true, Groups: []string{}, CreatedAt: core.Now()}
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
	generated := ""
	if in.Password == "" {
		generated = core.NewSecret(10) + "a1" // satisfies the default policy
		in.Password, in.TemporaryPassword = generated, true
	}
	status := "CONFIRMED"
	if in.TemporaryPassword {
		status = "FORCE_CHANGE_PASSWORD"
	}
	for _, g := range in.Groups {
		if !slices.ContainsFunc(p.Groups, func(x Group) bool { return x.Name == g }) {
			return nil, core.NotFound("group", g)
		}
	}
	u, err := s.newUser(p, in.Username, in.Password, in.Attributes, status)
	if err != nil {
		return nil, err
	}
	if len(in.Groups) > 0 {
		u, _ = store.Update(s.env.Store, cUsers, userKey(p.ID, u.Username), func(x *User) error { x.Groups = in.Groups; return nil })
	}
	v := u.view()
	if generated != "" {
		v["temporary_password"] = generated
	}
	return v, nil
}

func (s *Service) userOf(c *httpx.Ctx) (User, error) {
	u, err := store.Get[User](s.env.Store, cUsers, userKey(c.Param("pool"), c.Param("user")))
	if err != nil {
		return u, core.Errf(http.StatusNotFound, "UserNotFoundException", "user %q does not exist", c.Param("user"))
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
	u, err := s.userOf(c)
	if err != nil {
		return nil, err
	}
	return nil, store.Delete(s.env.Store, cUsers, userKey(u.PoolID, u.Username))
}

func (s *Service) updateUser(c *httpx.Ctx, fn func(*User) error) (any, error) {
	if _, err := s.userOf(c); err != nil {
		return nil, err
	}
	u, err := store.Update(s.env.Store, cUsers, userKey(c.Param("pool"), c.Param("user")), fn)
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
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return nil, err
	}
	return s.updateUser(c, func(u *User) error {
		for k, v := range in.Attributes {
			if k == "sub" || strings.HasPrefix(k, "cognito:") {
				return core.BadRequest("attribute %q is reserved", k)
			}
			if v == "" {
				delete(u.Attributes, k)
			} else {
				u.Attributes[k] = v
			}
		}
		if in.Enabled != nil {
			u.Enabled = *in.Enabled
		}
		if in.Groups != nil {
			for _, g := range *in.Groups {
				if !slices.ContainsFunc(p.Groups, func(x Group) bool { return x.Name == g }) {
					return core.NotFound("group", g)
				}
			}
			u.Groups = *in.Groups
		}
		if in.Confirm && u.Status == "UNCONFIRMED" {
			u.Status = "CONFIRMED"
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
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return nil, err
	}
	if err := p.PasswordPolicy.check(in.Password); err != nil {
		return nil, err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return s.updateUser(c, func(u *User) error {
		u.PasswordHash = string(h)
		if in.Permanent {
			u.Status = "CONFIRMED"
		} else {
			u.Status = "FORCE_CHANGE_PASSWORD"
		}
		return nil
	})
}

func (s *Service) revokeAll(pool, username string) {
	_ = s.env.Store.Retain(cRefresh, func(_ string, raw json.RawMessage) bool {
		return !(strings.Contains(string(raw), `"pool_id":"`+pool+`"`) && strings.Contains(string(raw), `"username":"`+username+`"`))
	})
	_, _ = store.Update(s.env.Store, cUsers, userKey(pool, username), func(u *User) error { u.TokensAfter = time.Now().Add(time.Second); return nil })
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
	if !poolName.MatchString(g.Name) {
		return nil, core.BadRequest("invalid group name")
	}
	p, err := store.Update(s.env.Store, cPools, c.Param("pool"), func(p *Pool) error {
		if slices.ContainsFunc(p.Groups, func(x Group) bool { return x.Name == g.Name }) {
			return core.Conflict("group %q already exists", g.Name)
		}
		p.Groups = append(p.Groups, g)
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "user pool %q does not exist", c.Param("pool"))
	}
	if err != nil {
		return nil, err
	}
	return s.poolView(p), nil
}

func (s *Service) deleteGroup(c *httpx.Ctx) (any, error) {
	g := c.Param("group")
	p, err := store.Update(s.env.Store, cPools, c.Param("pool"), func(p *Pool) error {
		n := len(p.Groups)
		p.Groups = slices.DeleteFunc(p.Groups, func(x Group) bool { return x.Name == g })
		if len(p.Groups) == n {
			return core.NotFound("group", g)
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "user pool %q does not exist", c.Param("pool"))
	}
	if err != nil {
		return nil, err
	}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if u.PoolID == p.ID && slices.Contains(u.Groups, g) {
			_, _ = store.Update(s.env.Store, cUsers, userKey(p.ID, u.Username), func(x *User) error {
				x.Groups = slices.DeleteFunc(x.Groups, func(y string) bool { return y == g })
				return nil
			})
		}
	}
	return s.poolView(p), nil
}

// ---- public (application-facing) routes ----

func (s *Service) publicRoutes(r *httpx.Router) {
	r.Handle("POST /cognito/{pool}/sign-up", "", s.signUp, httpx.Public())
	r.Handle("POST /cognito/{pool}/auth", "", s.auth, httpx.Public())
	r.Handle("POST /cognito/{pool}/respond", "", s.respond, httpx.Public())
	r.Handle("POST /cognito/{pool}/change-password", "", s.changePassword, httpx.Public())
	r.Handle("GET /cognito/{pool}/userinfo", "", s.userinfo, httpx.Public())
	r.Handle("POST /cognito/{pool}/sign-out", "", s.signOut, httpx.Public())
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

func (s *Service) throttled(key string, fail bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.fails[key][:0]
	for _, t := range s.fails[key] {
		if time.Since(t) < 5*time.Minute {
			recent = append(recent, t)
		}
	}
	if fail {
		recent = append(recent, time.Now())
	}
	s.fails[key] = recent
	return len(recent) >= 10
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
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return nil, err
	}
	if !p.SelfSignUp {
		return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "self sign-up is disabled for this user pool")
	}
	if _, err := s.client(p, in.ClientID, in.ClientSecret); err != nil {
		return nil, err
	}
	status := "UNCONFIRMED"
	if p.AutoConfirm {
		status = "CONFIRMED"
	}
	u, err := s.newUser(p, in.Username, in.Password, in.Attributes, status)
	if err != nil {
		return nil, err
	}
	return map[string]any{"user_sub": u.Sub, "user_confirmed": u.Status == "CONFIRMED"}, nil
}

func (s *Service) auth(c *httpx.Ctx) (any, error) {
	var in authInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	p, err := s.pool(c.Param("pool"))
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
		tk := p.ID + "/" + strings.ToLower(in.Username) + "/" + httpx.ClientIP(c.R)
		if s.throttled(tk, false) {
			return nil, core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many failed attempts; try again later")
		}
		u, err := store.Get[User](s.env.Store, cUsers, userKey(p.ID, in.Username))
		if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
			s.throttled(tk, true)
			return nil, deny
		}
		if !u.Enabled {
			return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "user is disabled")
		}
		switch u.Status {
		case "UNCONFIRMED":
			return nil, core.Errf(http.StatusBadRequest, "UserNotConfirmedException", "user is not confirmed")
		case "FORCE_CHANGE_PASSWORD":
			session := core.NewSecret(48)
			if err := store.Put(s.env.Store, cSessions, hashToken(session), challenge{PoolID: p.ID, ClientID: cl.ID, Username: u.Username, Expires: time.Now().Add(5 * time.Minute)}); err != nil {
				return nil, err
			}
			return map[string]any{"challenge": "NEW_PASSWORD_REQUIRED", "session": session}, nil
		}
		return s.issue(p, cl, u, true)
	case "REFRESH_TOKEN_AUTH":
		rt, err := store.Get[refresh](s.env.Store, cRefresh, hashToken(in.RefreshToken))
		if err != nil || rt.PoolID != p.ID || rt.ClientID != cl.ID || time.Now().After(rt.Expires) {
			return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "invalid refresh token")
		}
		u, err := store.Get[User](s.env.Store, cUsers, userKey(p.ID, rt.Username))
		if err != nil || !u.Enabled {
			return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "user is disabled or deleted")
		}
		return s.issue(p, cl, u, false)
	}
	return nil, core.BadRequest("flow must be USER_PASSWORD_AUTH or REFRESH_TOKEN_AUTH")
}

func (s *Service) respond(c *httpx.Ctx) (any, error) {
	var in authInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return nil, err
	}
	ch, err := store.Get[challenge](s.env.Store, cSessions, hashToken(in.Session))
	if err != nil || ch.PoolID != p.ID || time.Now().After(ch.Expires) {
		return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "invalid or expired session")
	}
	if err := p.PasswordPolicy.check(in.NewPassword); err != nil {
		return nil, err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u, err := store.Update(s.env.Store, cUsers, userKey(p.ID, ch.Username), func(u *User) error {
		u.PasswordHash, u.Status = string(h), "CONFIRMED"
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
	p, err := s.pool(c.Param("pool"))
	if err != nil {
		return p, User{}, err
	}
	tok := strings.TrimPrefix(c.R.Header.Get("Authorization"), "Bearer ")
	claims, err := s.VerifyToken(p.ID, tok, "")
	if err != nil {
		return p, User{}, core.Errf(http.StatusUnauthorized, "NotAuthorizedException", "%v", err)
	}
	if claims["token_use"] != "access" {
		return p, User{}, core.Errf(http.StatusUnauthorized, "NotAuthorizedException", "an access token is required")
	}
	u, err := store.Get[User](s.env.Store, cUsers, userKey(p.ID, fmt.Sprint(claims["username"])))
	return p, u, err
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
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Old)) != nil {
		return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "incorrect password")
	}
	if err := p.PasswordPolicy.check(in.New); err != nil {
		return nil, err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(in.New), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	_, err = store.Update(s.env.Store, cUsers, userKey(p.ID, u.Username), func(x *User) error { x.PasswordHash = string(h); return nil })
	return nil, err
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
