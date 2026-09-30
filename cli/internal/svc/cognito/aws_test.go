package cognito_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cognito"
)

const (
	tempPass = "Temp#Pass1"
	newPass  = "Better#Pass2"
)

func newCognito(t *testing.T) (*awstest.Harness, *cognito.Service) {
	t.Helper()
	h := awstest.New(t)
	cog := cognito.New(h.Env, h.Secrets)
	cog.RegisterAWS()
	cog.Routes(h.Router)
	return h, cog
}

// syncBuf collects the server log (confirmation codes go there) and lets the
// tests check that passwords never do.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func captureLog(t *testing.T) *syncBuf {
	buf := &syncBuf{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// call posts an unsigned awsJson request (no Authorization header at all).
func call(t *testing.T, h *awstest.Harness, op string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", h.URL+"/", bytes.NewReader(b))
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+op)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func sub(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		m, _ = m[k].(map[string]any)
	}
	return m
}

func setup(t *testing.T, h *awstest.Harness) (pool, client string) {
	t.Helper()
	pool = sub(h.AWSJSON(t, "cognito-idp", "create-user-pool", "--pool-name", "app"), "UserPool")["Id"].(string)
	client = sub(h.AWSJSON(t, "cognito-idp", "create-user-pool-client", "--user-pool-id", pool, "--client-name", "web",
		"--explicit-auth-flows", "ALLOW_USER_PASSWORD_AUTH", "ALLOW_ADMIN_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"), "UserPoolClient")["ClientId"].(string)
	return
}

// verifyWithJWKS checks a token's signature against the pool's published JWKS,
// as an API gateway or resource server would.
func verifyWithJWKS(t *testing.T, h *awstest.Harness, pool, token string) map[string]any {
	t.Helper()
	resp, err := http.Get(h.URL + "/cognito/" + pool + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var jw struct{ Keys []map[string]string }
	if err := json.NewDecoder(resp.Body).Decode(&jw); err != nil || len(jw.Keys) != 1 {
		t.Fatalf("jwks: %v %v", jw, err)
	}
	k := jw.Keys[0]
	n, _ := base64.RawURLEncoding.DecodeString(k["n"])
	e, _ := base64.RawURLEncoding.DecodeString(k["e"])
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	claims, err := cognito.Verify(token, pub)
	if err != nil {
		t.Fatalf("token does not verify against JWKS: %v", err)
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	if !strings.Contains(string(hdr), `"kid":"`+k["kid"]+`"`) {
		t.Fatalf("kid %s not in header %s", k["kid"], hdr)
	}
	return claims
}

func TestAWSFullFlow(t *testing.T) {
	logs := captureLog(t)
	h, cog := newCognito(t)
	pool, client := setup(t, h)
	if !strings.HasPrefix(pool, "us-east-1_") {
		t.Fatalf("pool id %s", pool)
	}
	d := sub(h.AWSJSON(t, "cognito-idp", "describe-user-pool", "--user-pool-id", pool), "UserPool")
	if d["Name"] != "app" || d["Arn"] != "arn:aws:cognito-idp:us-east-1:"+h.Env.AccountID+":userpool/"+pool || d["MfaConfiguration"] != "OFF" {
		t.Fatalf("describe pool %v", d)
	}
	if pp := sub(d, "Policies", "PasswordPolicy"); pp["MinimumLength"].(float64) != 8 || pp["RequireSymbols"] != true {
		t.Fatalf("policy %v", pp)
	}
	if l := h.AWSJSON(t, "cognito-idp", "list-user-pools", "--max-results", "10")["UserPools"].([]any); len(l) != 1 {
		t.Fatalf("list pools %v", l)
	}
	if o := h.AWSJSON(t, "cognito-idp", "list-user-pool-clients", "--user-pool-id", pool, "--max-results", "10")["UserPoolClients"].([]any); len(o) != 1 {
		t.Fatalf("list clients %v", o)
	}
	dc := sub(h.AWSJSON(t, "cognito-idp", "describe-user-pool-client", "--user-pool-id", pool, "--client-id", client), "UserPoolClient")
	if dc["ClientName"] != "web" || dc["ClientId"] != client || len(dc["ExplicitAuthFlows"].([]any)) != 3 {
		t.Fatalf("describe client %v", dc)
	}
	h.AWS(t, "cognito-idp", "update-user-pool", "--user-pool-id", pool, "--policies",
		"PasswordPolicy={MinimumLength=10,RequireUppercase=true,RequireLowercase=true,RequireNumbers=true,RequireSymbols=true}")
	if pp := sub(h.AWSJSON(t, "cognito-idp", "describe-user-pool", "--user-pool-id", pool), "UserPool", "Policies", "PasswordPolicy"); pp["MinimumLength"].(float64) != 10 {
		t.Fatalf("updated policy %v", pp)
	}
	h.AWS(t, "cognito-idp", "update-user-pool", "--user-pool-id", pool, "--policies",
		"PasswordPolicy={MinimumLength=8,RequireUppercase=true,RequireLowercase=true,RequireNumbers=true,RequireSymbols=true}")

	// Admin creates a user with a temporary password.
	u := sub(h.AWSJSON(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "alice", "--temporary-password", tempPass,
		"--user-attributes", "Name=email,Value=alice@example.com", "Name=email_verified,Value=true"), "User")
	if u["Username"] != "alice" || u["UserStatus"] != "FORCE_CHANGE_PASSWORD" || u["Enabled"] != true {
		t.Fatalf("created user %v", u)
	}
	if o, err := h.AWSErr(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "alice", "--temporary-password", tempPass); err == nil || !strings.Contains(o, "UsernameExistsException") {
		t.Fatalf("duplicate: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "weak", "--temporary-password", "short"); err == nil || !strings.Contains(o, "InvalidPasswordException") {
		t.Fatalf("weak password: %v %s", err, o)
	}
	g := h.AWSJSON(t, "cognito-idp", "admin-get-user", "--user-pool-id", pool, "--username", "alice")
	if g["UserStatus"] != "FORCE_CHANGE_PASSWORD" || len(g["UserAttributes"].([]any)) != 3 {
		t.Fatalf("admin-get-user %v", g)
	}

	// Sign-in over the unsigned wire: wrong password, then the forced change.
	auth := func(user, pass string) (int, map[string]any) {
		return call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client,
			"AuthParameters": map[string]string{"USERNAME": user, "PASSWORD": pass}})
	}
	if st, out := auth("alice", "wrong"); st != 400 || out["__type"] != "NotAuthorizedException" {
		t.Fatalf("wrong password: %d %v", st, out)
	}
	st, ch := auth("alice", tempPass)
	if st != 200 || ch["ChallengeName"] != "NEW_PASSWORD_REQUIRED" || ch["Session"] == "" {
		t.Fatalf("challenge: %d %v", st, ch)
	}
	if st, out := call(t, h, "RespondToAuthChallenge", map[string]any{"ChallengeName": "NEW_PASSWORD_REQUIRED", "ClientId": client, "Session": ch["Session"],
		"ChallengeResponses": map[string]string{"USERNAME": "alice", "NEW_PASSWORD": "weak"}}); st != 400 || out["__type"] != "InvalidPasswordException" {
		t.Fatalf("weak new password: %d %v", st, out)
	}
	// The CLI does the same without credentials being needed for the call.
	out := h.AWSJSON(t, "cognito-idp", "respond-to-auth-challenge", "--challenge-name", "NEW_PASSWORD_REQUIRED", "--client-id", client,
		"--session", ch["Session"].(string), "--challenge-responses", "USERNAME=alice,NEW_PASSWORD="+newPass)
	res := sub(out, "AuthenticationResult")
	if res["TokenType"] != "Bearer" || res["ExpiresIn"].(float64) != 3600 || res["RefreshToken"] == nil {
		t.Fatalf("tokens %v", res)
	}
	idTok, accTok, refTok := res["IdToken"].(string), res["AccessToken"].(string), res["RefreshToken"].(string)

	// Tokens verify against the JWKS and are accepted by API Gateway's authorizer.
	idc := verifyWithJWKS(t, h, pool, idTok)
	if idc["token_use"] != "id" || idc["aud"] != client || idc["cognito:username"] != "alice" || idc["email"] != "alice@example.com" ||
		!strings.HasSuffix(idc["iss"].(string), "/cognito/"+pool) {
		t.Fatalf("id claims %v", idc)
	}
	if ac := verifyWithJWKS(t, h, pool, accTok); ac["token_use"] != "access" || ac["client_id"] != client {
		t.Fatalf("access claims %v", ac)
	}
	if _, err := cog.VerifyToken(pool, idTok, client); err != nil {
		t.Fatalf("authorizer rejects ID token: %v", err)
	}
	if _, err := cog.VerifyToken(pool, accTok, ""); err != nil {
		t.Fatalf("authorizer rejects access token: %v", err)
	}
	if _, err := cog.VerifyToken(pool, idTok, "other-client"); err == nil {
		t.Fatal("wrong audience accepted")
	}
	if _, err := cog.VerifyToken(pool, idTok[:len(idTok)-4]+"AAAA", ""); err == nil {
		t.Fatal("tampered token accepted")
	}

	// Sign in again with the new password; the old one is gone.
	if st, out := auth("alice", tempPass); st != 400 {
		t.Fatalf("old password: %d %v", st, out)
	}
	if st, out := auth("alice", newPass); st != 200 || sub(out, "AuthenticationResult")["AccessToken"] == nil {
		t.Fatalf("sign in: %d %v", st, out)
	}
	if a := h.AWSJSON(t, "cognito-idp", "admin-get-user", "--user-pool-id", pool, "--username", "alice"); a["UserStatus"] != "CONFIRMED" {
		t.Fatalf("status after change: %v", a)
	}

	// GetUser with the access token (unsigned).
	st, gu := call(t, h, "GetUser", map[string]any{"AccessToken": accTok})
	if st != 200 || gu["Username"] != "alice" || !strings.Contains(toJSON(gu), `"email"`) {
		t.Fatalf("GetUser: %d %v", st, gu)
	}
	if st, out := call(t, h, "GetUser", map[string]any{"AccessToken": idTok}); st != 400 || out["__type"] != "NotAuthorizedException" {
		t.Fatalf("ID token as access token: %d %v", st, out)
	}
	if st, out := call(t, h, "GetUser", map[string]any{"AccessToken": "junk"}); st != 400 || out["__type"] != "NotAuthorizedException" {
		t.Fatalf("junk token: %d %v", st, out)
	}
	if o := h.AWSJSON(t, "cognito-idp", "get-user", "--access-token", accTok); o["Username"] != "alice" {
		t.Fatalf("CLI get-user %v", o)
	}

	// Refresh.
	st, rf := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "REFRESH_TOKEN_AUTH", "ClientId": client, "AuthParameters": map[string]string{"REFRESH_TOKEN": refTok}})
	if st != 200 || sub(rf, "AuthenticationResult")["AccessToken"] == nil || sub(rf, "AuthenticationResult")["RefreshToken"] != nil {
		t.Fatalf("refresh: %d %v", st, rf)
	}
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "REFRESH_TOKEN_AUTH", "ClientId": client, "AuthParameters": map[string]string{"REFRESH_TOKEN": "nope"}}); st != 400 {
		t.Fatalf("bad refresh: %d %v", st, out)
	}
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_SRP_AUTH", "ClientId": client, "AuthParameters": map[string]string{"USERNAME": "alice", "SRP_A": "1"}}); st != 400 || out["__type"] != "InvalidParameterException" {
		t.Fatalf("SRP: %d %v", st, out)
	}

	// Groups end up in the token.
	h.AWS(t, "cognito-idp", "create-group", "--user-pool-id", pool, "--group-name", "admins", "--description", "Admins", "--precedence", "1")
	if o, err := h.AWSErr(t, "cognito-idp", "create-group", "--user-pool-id", pool, "--group-name", "admins"); err == nil || !strings.Contains(o, "GroupExistsException") {
		t.Fatalf("duplicate group: %v %s", err, o)
	}
	h.AWS(t, "cognito-idp", "admin-add-user-to-group", "--user-pool-id", pool, "--username", "alice", "--group-name", "admins")
	if o := h.AWS(t, "cognito-idp", "admin-list-groups-for-user", "--user-pool-id", pool, "--username", "alice"); !strings.Contains(o, "admins") {
		t.Fatalf("groups for user: %s", o)
	}
	if o := h.AWS(t, "cognito-idp", "list-users-in-group", "--user-pool-id", pool, "--group-name", "admins"); !strings.Contains(o, "alice") {
		t.Fatalf("users in group: %s", o)
	}
	if o := h.AWS(t, "cognito-idp", "list-groups", "--user-pool-id", pool); !strings.Contains(o, "Admins") {
		t.Fatalf("list groups: %s", o)
	}
	if o, err := h.AWSErr(t, "cognito-idp", "admin-add-user-to-group", "--user-pool-id", pool, "--username", "alice", "--group-name", "nope"); err == nil || !strings.Contains(o, "ResourceNotFoundException") {
		t.Fatalf("unknown group: %v %s", err, o)
	}
	_, again := auth("alice", newPass)
	claims := verifyWithJWKS(t, h, pool, sub(again, "AuthenticationResult")["IdToken"].(string))
	if gs, _ := claims["cognito:groups"].([]any); len(gs) != 1 || gs[0] != "admins" {
		t.Fatalf("groups claim %v", claims["cognito:groups"])
	}
	h.AWS(t, "cognito-idp", "admin-remove-user-from-group", "--user-pool-id", pool, "--username", "alice", "--group-name", "admins")

	// ListUsers with filters and pagination.
	for _, n := range []string{"bob", "bea", "carl"} {
		h.AWS(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", n, "--temporary-password", tempPass,
			"--user-attributes", "Name=email,Value="+n+"@example.com")
	}
	if l := h.AWSJSON(t, "cognito-idp", "list-users", "--user-pool-id", pool, "--filter", `username ^= "b"`)["Users"].([]any); len(l) != 2 {
		t.Fatalf("prefix filter: %v", l)
	}
	if l := h.AWSJSON(t, "cognito-idp", "list-users", "--user-pool-id", pool, "--filter", `email = "carl@example.com"`)["Users"].([]any); len(l) != 1 {
		t.Fatalf("email filter: %v", l)
	}
	if o, err := h.AWSErr(t, "cognito-idp", "list-users", "--user-pool-id", pool, "--filter", `bogus = "x"`); err == nil || !strings.Contains(o, "InvalidParameterException") {
		t.Fatalf("bad filter: %v %s", err, o)
	}
	first := h.AWSJSON(t, "cognito-idp", "list-users", "--user-pool-id", pool, "--limit", "3")
	second := h.AWSJSON(t, "cognito-idp", "list-users", "--user-pool-id", pool, "--limit", "3", "--pagination-token", first["PaginationToken"].(string))
	if len(first["Users"].([]any)) != 3 || len(second["Users"].([]any)) != 1 || second["PaginationToken"] != nil {
		t.Fatalf("pagination: %v / %v", first, second)
	}

	// Admin password reset and disabling.
	h.AWS(t, "cognito-idp", "admin-set-user-password", "--user-pool-id", pool, "--username", "bob", "--password", newPass, "--permanent")
	if st, out := auth("bob", newPass); st != 200 || sub(out, "AuthenticationResult") == nil {
		t.Fatalf("bob after admin-set-user-password: %d %v", st, out)
	}
	h.AWS(t, "cognito-idp", "admin-disable-user", "--user-pool-id", pool, "--username", "bob")
	if st, out := auth("bob", newPass); st != 400 || out["__type"] != "NotAuthorizedException" {
		t.Fatalf("disabled user: %d %v", st, out)
	}
	h.AWS(t, "cognito-idp", "admin-enable-user", "--user-pool-id", pool, "--username", "bob")
	h.AWS(t, "cognito-idp", "admin-update-user-attributes", "--user-pool-id", pool, "--username", "bob", "--user-attributes", "Name=name,Value=Bobby")
	if o := h.AWS(t, "cognito-idp", "admin-get-user", "--user-pool-id", pool, "--username", "bob"); !strings.Contains(o, "Bobby") {
		t.Fatalf("attribute update: %s", o)
	}
	// Admin-driven auth flow.
	if o := h.AWSJSON(t, "cognito-idp", "admin-initiate-auth", "--user-pool-id", pool, "--client-id", client, "--auth-flow", "ADMIN_NO_SRP_AUTH",
		"--auth-parameters", "USERNAME=bob,PASSWORD="+newPass); sub(o, "AuthenticationResult") == nil {
		t.Fatalf("admin-initiate-auth: %v", o)
	}

	// Global sign-out revokes every token.
	if st, _ := call(t, h, "GlobalSignOut", map[string]any{"AccessToken": accTok}); st != 200 {
		t.Fatalf("GlobalSignOut: %d", st)
	}
	if st, out := call(t, h, "GetUser", map[string]any{"AccessToken": accTok}); st != 400 || out["__type"] != "NotAuthorizedException" {
		t.Fatalf("access token after sign-out: %d %v", st, out)
	}
	if _, err := cog.VerifyToken(pool, idTok, ""); err == nil {
		t.Fatal("authorizer accepts a signed-out ID token")
	}
	if st, _ := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "REFRESH_TOKEN_AUTH", "ClientId": client, "AuthParameters": map[string]string{"REFRESH_TOKEN": refTok}}); st != 400 {
		t.Fatalf("refresh after sign-out: %d", st)
	}
	h.AWS(t, "cognito-idp", "admin-user-global-sign-out", "--user-pool-id", pool, "--username", "bob")

	// Deleting.
	h.AWS(t, "cognito-idp", "admin-delete-user", "--user-pool-id", pool, "--username", "carl")
	if o, err := h.AWSErr(t, "cognito-idp", "admin-get-user", "--user-pool-id", pool, "--username", "carl"); err == nil || !strings.Contains(o, "UserNotFoundException") {
		t.Fatalf("deleted user: %v %s", err, o)
	}
	h.AWS(t, "cognito-idp", "delete-group", "--user-pool-id", pool, "--group-name", "admins")
	h.AWS(t, "cognito-idp", "delete-user-pool-client", "--user-pool-id", pool, "--client-id", client)
	if o, err := h.AWSErr(t, "cognito-idp", "describe-user-pool-client", "--user-pool-id", pool, "--client-id", client); err == nil || !strings.Contains(o, "ResourceNotFoundException") {
		t.Fatalf("deleted client: %v %s", err, o)
	}
	h.AWS(t, "cognito-idp", "delete-user-pool", "--user-pool-id", pool)
	if o, err := h.AWSErr(t, "cognito-idp", "describe-user-pool", "--user-pool-id", pool); err == nil || !strings.Contains(o, "ResourceNotFoundException") {
		t.Fatalf("deleted pool: %v %s", err, o)
	}

	// Passwords are never logged.
	for _, p := range []string{tempPass, newPass} {
		if strings.Contains(logs.String(), p) {
			t.Fatalf("a password was written to the log")
		}
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestAWSUnsignedBoundary(t *testing.T) {
	h, _ := newCognito(t)
	pool, client := setup(t, h)
	// Administrative operations need a signature and IAM; only the application
	// operations may be unsigned.
	for _, op := range []string{"ListUsers", "AdminCreateUser", "AdminGetUser", "DescribeUserPool", "DeleteUserPool", "CreateUserPool", "ListUserPools", "AdminSetUserPassword", "AdminInitiateAuth", "CreateUserPoolClient"} {
		st, out := call(t, h, op, map[string]any{"UserPoolId": pool, "Username": "x", "PoolName": "x", "MaxResults": 5, "ClientId": client, "AuthFlow": "ADMIN_NO_SRP_AUTH"})
		if st != 403 || out["__type"] != "MissingAuthenticationToken" {
			t.Fatalf("unsigned %s: %d %v", op, st, out)
		}
	}
	// A bad signature is rejected even for an application operation? Those are
	// still served: the signature is optional there but verified when present.
	req, _ := http.NewRequest("POST", h.URL+"/", strings.NewReader(`{"ClientId":"`+client+`","AuthFlow":"USER_PASSWORD_AUTH","AuthParameters":{"USERNAME":"x","PASSWORD":"y"}}`))
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService.InitiateAuth")
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+h.AccessKeyID+"/20200101/us-east-1/cognito-idp/aws4_request, SignedHeaders=host;x-amz-date, Signature=00")
	req.Header.Set("X-Amz-Date", "20200101T000000Z")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatalf("forged signature accepted")
	}
	// Unknown client and unknown operation.
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": "nope", "AuthParameters": map[string]string{"USERNAME": "a", "PASSWORD": "b"}}); st != 400 || out["__type"] != "ResourceNotFoundException" {
		t.Fatalf("unknown client: %d %v", st, out)
	}
	if st, out := call(t, h, "Nope", map[string]any{}); st == 200 {
		t.Fatalf("unknown op: %d %v", st, out)
	}
}

var codeRe = regexp.MustCompile(`confirmation code for user "([^"]+)" in pool \S+ is (\d{6})`)

func TestAWSSignUp(t *testing.T) {
	logs := captureLog(t)
	h, _ := newCognito(t)
	pool, client := setup(t, h)

	// Default: pools confirm self-registered users at once.
	st, out := call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "dora", "Password": newPass,
		"UserAttributes": []map[string]string{{"Name": "email", "Value": "dora@example.com"}}})
	if st != 200 || out["UserConfirmed"] != true || out["UserSub"] == "" {
		t.Fatalf("auto-confirm: %d %v", st, out)
	}
	if st, out := call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "dora", "Password": newPass}); st != 400 || out["__type"] != "UsernameExistsException" {
		t.Fatalf("duplicate sign-up: %d %v", st, out)
	}
	if st, out := call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "erin", "Password": "weak"}); st != 400 || out["__type"] != "InvalidPasswordException" {
		t.Fatalf("weak sign-up: %d %v", st, out)
	}
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client, "AuthParameters": map[string]string{"USERNAME": "dora", "PASSWORD": newPass}}); st != 200 || sub(out, "AuthenticationResult") == nil {
		t.Fatalf("sign in after sign-up: %d %v", st, out)
	}

	// With auto-confirm off, the user confirms with the code (logged, not mailed) or an admin confirms.
	h.Native(t, "PATCH", "/api/v1/cognito/user-pools/"+pool, map[string]any{"auto_confirm": false})
	st, out = call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "fred", "Password": newPass})
	if st != 200 || out["UserConfirmed"] != false || sub(out, "CodeDeliveryDetails")["DeliveryMedium"] == nil {
		t.Fatalf("unconfirmed: %d %v", st, out)
	}
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client, "AuthParameters": map[string]string{"USERNAME": "fred", "PASSWORD": newPass}}); st != 400 || out["__type"] != "UserNotConfirmedException" {
		t.Fatalf("unconfirmed sign-in: %d %v", st, out)
	}
	if st, out := call(t, h, "ConfirmSignUp", map[string]any{"ClientId": client, "Username": "fred", "ConfirmationCode": "000000"}); st != 400 || out["__type"] != "CodeMismatchException" {
		t.Fatalf("wrong code: %d %v", st, out)
	}
	m := codeRe.FindStringSubmatch(logs.String())
	if m == nil || m[1] != "fred" {
		t.Fatalf("no confirmation code in the log: %q", logs.String())
	}
	if st, out := call(t, h, "ConfirmSignUp", map[string]any{"ClientId": client, "Username": "fred", "ConfirmationCode": m[2]}); st != 200 {
		t.Fatalf("confirm: %d %v", st, out)
	}
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client, "AuthParameters": map[string]string{"USERNAME": "fred", "PASSWORD": newPass}}); st != 200 {
		t.Fatalf("sign in after confirm: %d %v", st, out)
	}
	if st, out := call(t, h, "ConfirmSignUp", map[string]any{"ClientId": client, "Username": "fred", "ConfirmationCode": m[2]}); st != 400 {
		t.Fatalf("confirming twice: %d %v", st, out)
	}
	// Resend and admin confirmation.
	call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "gina", "Password": newPass})
	if st, out := call(t, h, "ResendConfirmationCode", map[string]any{"ClientId": client, "Username": "gina"}); st != 200 || out["CodeDeliveryDetails"] == nil {
		t.Fatalf("resend: %d %v", st, out)
	}
	h.AWS(t, "cognito-idp", "admin-confirm-sign-up", "--user-pool-id", pool, "--username", "gina")
	if o := h.AWSJSON(t, "cognito-idp", "admin-get-user", "--user-pool-id", pool, "--username", "gina"); o["UserStatus"] != "CONFIRMED" {
		t.Fatalf("admin confirm: %v", o)
	}
	// Sign-up can be turned off for admin-only pools.
	h.AWS(t, "cognito-idp", "update-user-pool", "--user-pool-id", pool, "--admin-create-user-config", "AllowAdminCreateUserOnly=true")
	if st, out := call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "hal", "Password": newPass}); st != 400 || out["__type"] != "NotAuthorizedException" {
		t.Fatalf("admin-only pool: %d %v", st, out)
	}
	if strings.Contains(logs.String(), newPass) {
		t.Fatal("password logged")
	}
}

func TestAWSClientSecret(t *testing.T) {
	h, _ := newCognito(t)
	pool := sub(h.AWSJSON(t, "cognito-idp", "create-user-pool", "--pool-name", "sec"), "UserPool")["Id"].(string)
	c := sub(h.AWSJSON(t, "cognito-idp", "create-user-pool-client", "--user-pool-id", pool, "--client-name", "srv", "--generate-secret",
		"--explicit-auth-flows", "ALLOW_USER_PASSWORD_AUTH"), "UserPoolClient")
	client, secret := c["ClientId"].(string), c["ClientSecret"].(string)
	if secret == "" {
		t.Fatal("no client secret")
	}
	if d := sub(h.AWSJSON(t, "cognito-idp", "describe-user-pool-client", "--user-pool-id", pool, "--client-id", client), "UserPoolClient"); d["ClientSecret"] != secret {
		t.Fatalf("describe secret: %v", d)
	}
	h.AWS(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "ivy", "--temporary-password", tempPass)
	h.AWS(t, "cognito-idp", "admin-set-user-password", "--user-pool-id", pool, "--username", "ivy", "--password", newPass, "--permanent")
	hash := func(user string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(user + client))
		return base64.StdEncoding.EncodeToString(m.Sum(nil))
	}
	params := func(sh string) map[string]string {
		p := map[string]string{"USERNAME": "ivy", "PASSWORD": newPass}
		if sh != "" {
			p["SECRET_HASH"] = sh
		}
		return p
	}
	for name, sh := range map[string]string{"missing": "", "wrong": hash("someone-else")} {
		if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client, "AuthParameters": params(sh)}); st != 400 || out["__type"] != "NotAuthorizedException" {
			t.Fatalf("%s SECRET_HASH: %d %v", name, st, out)
		}
	}
	st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client, "AuthParameters": params(hash("ivy"))})
	if st != 200 || sub(out, "AuthenticationResult") == nil {
		t.Fatalf("valid SECRET_HASH: %d %v", st, out)
	}
	// Sign-up needs it too.
	if st, out := call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "jo", "Password": newPass}); st != 400 {
		t.Fatalf("sign-up without SecretHash: %d %v", st, out)
	}
	if st, out := call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "jo", "Password": newPass, "SecretHash": hash("jo")}); st != 200 {
		t.Fatalf("sign-up with SecretHash: %d %v", st, out)
	}
	// A client that lists only ALLOW_USER_PASSWORD_AUTH refuses other flows.
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "REFRESH_TOKEN_AUTH", "ClientId": client, "AuthParameters": map[string]string{"REFRESH_TOKEN": "x", "SECRET_HASH": hash("")}}); st != 400 {
		t.Fatalf("refresh not enabled: %d %v", st, out)
	}
}

func TestAWSBoto3(t *testing.T) {
	h, _ := newCognito(t)
	h.Python(t, `
idp = boto3.client("cognito-idp")
pool = idp.create_user_pool(PoolName="py", UserPoolTags={"env": "qa"},
    Policies={"PasswordPolicy": {"MinimumLength": 8, "RequireUppercase": False, "RequireLowercase": True, "RequireNumbers": True, "RequireSymbols": False}},
    AutoVerifiedAttributes=["email"], UsernameAttributes=["email"])["UserPool"]
pid = pool["Id"]
assert pool["UserPoolTags"] == {"env": "qa"}, pool
assert pool["AutoVerifiedAttributes"] == ["email"] and pool["UsernameAttributes"] == ["email"], pool
d = idp.describe_user_pool(UserPoolId=pid)["UserPool"]
assert d["Policies"]["PasswordPolicy"]["RequireUppercase"] is False, d
assert idp.list_tags_for_resource(ResourceArn=d["Arn"])["Tags"] == {"env": "qa"}
idp.tag_resource(ResourceArn=d["Arn"], Tags={"team": "core"})
assert idp.list_tags_for_resource(ResourceArn=d["Arn"])["Tags"]["team"] == "core"
idp.untag_resource(ResourceArn=d["Arn"], TagKeys=["env"])
assert "env" not in idp.list_tags_for_resource(ResourceArn=d["Arn"])["Tags"]
assert idp.get_user_pool_mfa_config(UserPoolId=pid)["MfaConfiguration"] == "OFF"

c = idp.create_user_pool_client(UserPoolId=pid, ClientName="app", ExplicitAuthFlows=["ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"],
    AccessTokenValidity=2, TokenValidityUnits={"AccessToken": "hours", "RefreshToken": "days"}, RefreshTokenValidity=7)["UserPoolClient"]
assert c["AccessTokenValidity"] == 2 and c["RefreshTokenValidity"] == 7, c
cid = c["ClientId"]
idp.admin_create_user(UserPoolId=pid, Username="kim@example.com", TemporaryPassword="temp1pass",
    UserAttributes=[{"Name": "name", "Value": "Kim"}])
u = idp.admin_get_user(UserPoolId=pid, Username="kim@example.com")
attrs = {a["Name"]: a["Value"] for a in u["UserAttributes"]}
assert attrs["email"] == "kim@example.com" and attrs["name"] == "Kim", attrs
idp.admin_set_user_password(UserPoolId=pid, Username="kim@example.com", Password="secret1pw", Permanent=True)

# The unsigned application calls: boto3 does not sign these.
from botocore.config import Config
from botocore import UNSIGNED
anon = boto3.client("cognito-idp", config=Config(signature_version=UNSIGNED))
r = anon.initiate_auth(AuthFlow="USER_PASSWORD_AUTH", ClientId=cid, AuthParameters={"USERNAME": "kim@example.com", "PASSWORD": "secret1pw"})
tok = r["AuthenticationResult"]
assert tok["TokenType"] == "Bearer" and tok["ExpiresIn"] == 7200, tok
gu = anon.get_user(AccessToken=tok["AccessToken"])
assert gu["Username"] == "kim@example.com"
anon.change_password(PreviousPassword="secret1pw", ProposedPassword="another2pw", AccessToken=tok["AccessToken"])
r2 = anon.initiate_auth(AuthFlow="USER_PASSWORD_AUTH", ClientId=cid, AuthParameters={"USERNAME": "kim@example.com", "PASSWORD": "another2pw"})
r3 = anon.initiate_auth(AuthFlow="REFRESH_TOKEN_AUTH", ClientId=cid, AuthParameters={"REFRESH_TOKEN": r2["AuthenticationResult"]["RefreshToken"]})
assert "AccessToken" in r3["AuthenticationResult"]
su = anon.sign_up(ClientId=cid, Username="lee@example.com", Password="secret1pw")
assert su["UserConfirmed"] is True and su["UserSub"]
try:
    anon.initiate_auth(AuthFlow="USER_PASSWORD_AUTH", ClientId=cid, AuthParameters={"USERNAME": "kim@example.com", "PASSWORD": "bad"})
    raise SystemExit("expected NotAuthorizedException")
except anon.exceptions.NotAuthorizedException:
    pass
anon.global_sign_out(AccessToken=r3["AuthenticationResult"]["AccessToken"])
try:
    anon.get_user(AccessToken=r3["AuthenticationResult"]["AccessToken"])
    raise SystemExit("token still valid after sign-out")
except anon.exceptions.NotAuthorizedException:
    pass
assert len(idp.list_users(UserPoolId=pid)["Users"]) == 2
idp.delete_user_pool(UserPoolId=pid)
print("ok")
`)
}

func TestAWSIAM(t *testing.T) {
	h, _ := newCognito(t)
	pool, client := setup(t, h)
	h.AWS(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "alice", "--temporary-password", tempPass)
	poolARN := "arn:aws:cognito-idp:us-east-1:" + h.Env.AccountID + ":userpool/" + pool

	akid, secret := h.User(t, "ro", "ReadOnlyAccess")
	for _, args := range [][]string{
		{"describe-user-pool", "--user-pool-id", pool},
		{"list-user-pools", "--max-results", "5"},
		{"list-users", "--user-pool-id", pool},
		{"admin-get-user", "--user-pool-id", pool, "--username", "alice"},
		{"describe-user-pool-client", "--user-pool-id", pool, "--client-id", client},
		{"list-user-pool-clients", "--user-pool-id", pool, "--max-results", "5"},
		{"list-groups", "--user-pool-id", pool},
	} {
		if o, err := h.AWSAs(t, akid, secret, "", append([]string{"cognito-idp"}, args...)...); err != nil {
			t.Fatalf("%v as read-only: %v %s", args, err, o)
		}
	}
	for _, args := range [][]string{
		{"create-user-pool", "--pool-name", "x"},
		{"delete-user-pool", "--user-pool-id", pool},
		{"update-user-pool", "--user-pool-id", pool},
		{"create-user-pool-client", "--user-pool-id", pool, "--client-name", "n"},
		{"delete-user-pool-client", "--user-pool-id", pool, "--client-id", client},
		{"admin-create-user", "--user-pool-id", pool, "--username", "mallory", "--temporary-password", tempPass},
		{"admin-delete-user", "--user-pool-id", pool, "--username", "alice"},
		{"admin-set-user-password", "--user-pool-id", pool, "--username", "alice", "--password", newPass, "--permanent"},
		{"admin-add-user-to-group", "--user-pool-id", pool, "--username", "alice", "--group-name", "g"},
		{"create-group", "--user-pool-id", pool, "--group-name", "g"},
		{"admin-initiate-auth", "--user-pool-id", pool, "--client-id", client, "--auth-flow", "ADMIN_NO_SRP_AUTH", "--auth-parameters", "USERNAME=alice,PASSWORD=" + tempPass},
		{"tag-resource", "--resource-arn", poolARN, "--tags", "a=b"},
	} {
		if o, err := h.AWSAs(t, akid, secret, "", append([]string{"cognito-idp"}, args...)...); err == nil || !strings.Contains(o, "AccessDenied") {
			t.Fatalf("%v as read-only: %v %s", args, err, o)
		}
	}
	// A policy scoped to the pool ARN allows administration of that pool only.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "one-pool", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "cognito-idp:*", "Resource": poolARN}}}})
	akid2, secret2 := h.User(t, "poolAdmin", "one-pool")
	if o, err := h.AWSAs(t, akid2, secret2, "", "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "bob", "--temporary-password", tempPass); err != nil {
		t.Fatalf("scoped admin: %v %s", err, o)
	}
	other := sub(h.AWSJSON(t, "cognito-idp", "create-user-pool", "--pool-name", "other"), "UserPool")["Id"].(string)
	if o, err := h.AWSAs(t, akid2, secret2, "", "cognito-idp", "admin-create-user", "--user-pool-id", other, "--username", "bob", "--temporary-password", tempPass); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("scoped admin on another pool: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, akid2, secret2, "", "cognito-idp", "create-user-pool", "--pool-name", "z"); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("scoped admin creating a pool: %v %s", err, o)
	}
	// The application operations work for anyone, without credentials or policy.
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client, "AuthParameters": map[string]string{"USERNAME": "alice", "PASSWORD": tempPass}}); st != 200 || out["ChallengeName"] != "NEW_PASSWORD_REQUIRED" {
		t.Fatalf("anonymous sign-in: %d %v", st, out)
	}
	found := false
	for _, a := range h.AuditLog() {
		if strings.HasPrefix(a, "cognito-idp:AdminCreateUser "+poolARN) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry: %v", h.AuditLog())
	}
}
