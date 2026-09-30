package cognito_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cognito"
)

var resetRe = regexp.MustCompile(`password reset code for user "([^"]+)" in pool \S+ is (\d{6})`)

func srpSetup(t *testing.T, h *awstest.Harness) (pool, client string) {
	t.Helper()
	pool = sub(h.AWSJSON(t, "cognito-idp", "create-user-pool", "--pool-name", "srp"), "UserPool")["Id"].(string)
	client = sub(h.AWSJSON(t, "cognito-idp", "create-user-pool-client", "--user-pool-id", pool, "--client-name", "web",
		"--explicit-auth-flows", "ALLOW_USER_SRP_AUTH", "ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"), "UserPoolClient")["ClientId"].(string)
	return
}

func timestamp() string { return time.Now().UTC().Format("Mon Jan 2 15:04:05 UTC 2006") }

func initiateSRP(t *testing.T, h *awstest.Harness, client, user, srpA string) (int, map[string]any) {
	return call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_SRP_AUTH", "ClientId": client,
		"AuthParameters": map[string]string{"USERNAME": user, "SRP_A": srpA}})
}

func respondSRP(t *testing.T, h *awstest.Harness, client string, resp map[string]string) (int, map[string]any) {
	return call(t, h, "RespondToAuthChallenge", map[string]any{"ChallengeName": "PASSWORD_VERIFIER", "ClientId": client, "ChallengeResponses": resp})
}

func challengeParams(out map[string]any) map[string]string {
	cp := map[string]string{}
	for k, v := range sub(out, "ChallengeParameters") {
		cp[k], _ = v.(string)
	}
	return cp
}

// srpSignIn runs USER_SRP_AUTH and returns the last response.
func srpSignIn(t *testing.T, h *awstest.Harness, pool, client, user, password string) (int, map[string]any) {
	t.Helper()
	a, answer := cognito.SRPClient(pool, password)
	st, out := initiateSRP(t, h, client, user, a)
	if st != 200 {
		return st, out
	}
	if out["ChallengeName"] != "PASSWORD_VERIFIER" {
		t.Fatalf("challenge: %v", out)
	}
	return respondSRP(t, h, client, answer(challengeParams(out), timestamp()))
}

func wantErr(t *testing.T, st int, out map[string]any, typ string) {
	t.Helper()
	if st != 400 || out["__type"] != typ {
		t.Fatalf("want %s, got %d %v", typ, st, out)
	}
}

func wantTokens(t *testing.T, h *awstest.Harness, pool string, st int, out map[string]any, user string) {
	t.Helper()
	res := sub(out, "AuthenticationResult")
	if st != 200 || res == nil {
		t.Fatalf("want tokens, got %d %v", st, out)
	}
	if c := verifyWithJWKS(t, h, pool, res["AccessToken"].(string)); c["username"] != user || c["token_use"] != "access" {
		t.Fatalf("access claims %v", c)
	}
	if c := verifyWithJWKS(t, h, pool, res["IdToken"].(string)); c["token_use"] != "id" {
		t.Fatalf("id claims %v", c)
	}
}

func TestSRPSignIn(t *testing.T) {
	logs := captureLog(t)
	h, cog := newCognito(t)
	pool, client := srpSetup(t, h)

	// Admin-created users: the temporary password goes through SRP into NEW_PASSWORD_REQUIRED.
	h.AWS(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "ann", "--temporary-password", tempPass)
	st, out := srpSignIn(t, h, pool, client, "ann", tempPass)
	if st != 200 || out["ChallengeName"] != "NEW_PASSWORD_REQUIRED" || out["Session"] == "" {
		t.Fatalf("temp password: %d %v", st, out)
	}
	st, out = call(t, h, "RespondToAuthChallenge", map[string]any{"ChallengeName": "NEW_PASSWORD_REQUIRED", "ClientId": client, "Session": out["Session"],
		"ChallengeResponses": map[string]string{"USERNAME": "ann", "NEW_PASSWORD": newPass}})
	wantTokens(t, h, pool, st, out, "ann")
	// The new password (not the temporary one) works through SRP afterwards.
	st, out = srpSignIn(t, h, pool, client, "ann", newPass)
	wantTokens(t, h, pool, st, out, "ann")
	st, out = srpSignIn(t, h, pool, client, "ann", tempPass)
	wantErr(t, st, out, "NotAuthorizedException")

	// Wrong password; user names are case-insensitive like the other flows.
	st, out = srpSignIn(t, h, pool, client, "ann", "Wrong#Pass9")
	wantErr(t, st, out, "NotAuthorizedException")
	st, out = srpSignIn(t, h, pool, client, "ANN", newPass)
	if st != 200 || sub(out, "AuthenticationResult") == nil {
		t.Fatalf("case-insensitive user: %d %v", st, out)
	}

	// An unknown user gets a challenge like anyone else and fails at the answer.
	a, answer := cognito.SRPClient(pool, newPass)
	st, out = initiateSRP(t, h, client, "nobody", a)
	if st != 200 || out["ChallengeName"] != "PASSWORD_VERIFIER" {
		t.Fatalf("unknown user challenge: %d %v", st, out)
	}
	cp := challengeParams(out)
	if again := challengeParams(mustCall(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_SRP_AUTH", "ClientId": client,
		"AuthParameters": map[string]string{"USERNAME": "nobody", "SRP_A": a}})); again["SALT"] != cp["SALT"] {
		t.Fatal("the salt of an unknown user changes between calls")
	}
	st, out = respondSRP(t, h, client, answer(cp, timestamp()))
	wantErr(t, st, out, "NotAuthorizedException")

	// A challenge answers once: replaying a correct answer fails, and a wrong answer burns the challenge.
	a, answer = cognito.SRPClient(pool, newPass)
	_, out = initiateSRP(t, h, client, "ann", a)
	resp := answer(challengeParams(out), timestamp())
	if st, out := respondSRP(t, h, client, resp); st != 200 || sub(out, "AuthenticationResult") == nil {
		t.Fatalf("first answer: %d %v", st, out)
	}
	st, out = respondSRP(t, h, client, resp)
	wantErr(t, st, out, "NotAuthorizedException")
	_, out = initiateSRP(t, h, client, "ann", a)
	cp = challengeParams(out)
	bad := answer(cp, timestamp())
	bad["PASSWORD_CLAIM_SIGNATURE"] = "AAAA" + bad["PASSWORD_CLAIM_SIGNATURE"][4:]
	st, out = respondSRP(t, h, client, bad)
	wantErr(t, st, out, "NotAuthorizedException")
	st, out = respondSRP(t, h, client, answer(cp, timestamp()))
	wantErr(t, st, out, "NotAuthorizedException")
	// A signature made for a stale timestamp is refused.
	_, out = initiateSRP(t, h, client, "ann", a)
	st, out = respondSRP(t, h, client, answer(challengeParams(out), time.Now().UTC().Add(-2*time.Hour).Format("Mon Jan 2 15:04:05 UTC 2006")))
	wantErr(t, st, out, "NotAuthorizedException")

	// SRP_A must not be 0 mod N.
	for _, bad := range []string{"0", "", "zz"} {
		if st, out := initiateSRP(t, h, client, "ann", bad); st != 400 || out["__type"] != "InvalidParameterException" {
			t.Fatalf("SRP_A %q: %d %v", bad, st, out)
		}
	}
	if st, out := initiateSRP(t, h, client, "ann", cognito.SRPNHex); st != 400 || out["__type"] != "InvalidParameterException" {
		t.Fatalf("SRP_A = N: %d %v", st, out)
	}

	// Users from before SRP support have no verifier: SRP fails until USER_PASSWORD_AUTH (or a password change) creates one.
	if err := cognito.ClearVerifier(cog, pool, "ann"); err != nil {
		t.Fatal(err)
	}
	st, out = srpSignIn(t, h, pool, client, "ann", newPass)
	wantErr(t, st, out, "NotAuthorizedException")
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client,
		"AuthParameters": map[string]string{"USERNAME": "ann", "PASSWORD": newPass}}); st != 200 || sub(out, "AuthenticationResult") == nil {
		t.Fatalf("password auth: %d %v", st, out)
	}
	if !cognito.HasVerifier(cog, pool, "ann") {
		t.Fatal("USER_PASSWORD_AUTH did not derive a verifier")
	}
	st, out = srpSignIn(t, h, pool, client, "ann", newPass)
	wantTokens(t, h, pool, st, out, "ann")

	// Unconfirmed users get UserNotConfirmedException once the password is proven.
	h.Native(t, "PATCH", "/api/v1/cognito/user-pools/"+pool, map[string]any{"auto_confirm": false})
	if st, out := call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "fred", "Password": newPass}); st != 200 || out["UserConfirmed"] != false {
		t.Fatalf("sign up: %d %v", st, out)
	}
	st, out = srpSignIn(t, h, pool, client, "fred", newPass)
	wantErr(t, st, out, "UserNotConfirmedException")
	st, out = srpSignIn(t, h, pool, client, "fred", "Wrong#Pass9")
	wantErr(t, st, out, "NotAuthorizedException")
	h.AWS(t, "cognito-idp", "admin-confirm-sign-up", "--user-pool-id", pool, "--username", "fred")
	st, out = srpSignIn(t, h, pool, client, "fred", newPass)
	wantTokens(t, h, pool, st, out, "fred")

	// Disabled users cannot sign in.
	h.AWS(t, "cognito-idp", "admin-disable-user", "--user-pool-id", pool, "--username", "fred")
	st, out = srpSignIn(t, h, pool, client, "fred", newPass)
	wantErr(t, st, out, "NotAuthorizedException")

	// AdminSetUserPassword and ChangePassword refresh the verifier.
	h.AWS(t, "cognito-idp", "admin-set-user-password", "--user-pool-id", pool, "--username", "ann", "--password", "Set#ByAdmin3", "--permanent")
	st, out = srpSignIn(t, h, pool, client, "ann", "Set#ByAdmin3")
	wantTokens(t, h, pool, st, out, "ann")
	st, out = srpSignIn(t, h, pool, client, "ann", newPass)
	wantErr(t, st, out, "NotAuthorizedException")
	_, out = srpSignIn(t, h, pool, client, "ann", "Set#ByAdmin3")
	access := sub(out, "AuthenticationResult")
	if st, out := call(t, h, "ChangePassword", map[string]any{"AccessToken": access["AccessToken"], "PreviousPassword": "Set#ByAdmin3", "ProposedPassword": "Chan#ged4Pw"}); st != 200 {
		t.Fatalf("change password: %d %v", st, out)
	}
	st, out = srpSignIn(t, h, pool, client, "ann", "Chan#ged4Pw")
	wantTokens(t, h, pool, st, out, "ann")
	st, out = srpSignIn(t, h, pool, client, "ann", "Set#ByAdmin3")
	wantErr(t, st, out, "NotAuthorizedException")

	for _, pw := range []string{tempPass, newPass, "Set#ByAdmin3", "Chan#ged4Pw"} {
		if strings.Contains(logs.String(), pw) {
			t.Fatalf("password %q logged", pw)
		}
	}
}

func mustCall(t *testing.T, h *awstest.Harness, op string, body any) map[string]any {
	t.Helper()
	st, out := call(t, h, op, body)
	if st != 200 {
		t.Fatalf("%s: %d %v", op, st, out)
	}
	return out
}

func TestSRPFlowGating(t *testing.T) {
	h, _ := newCognito(t)
	pool, _ := srpSetup(t, h)
	noSRP := sub(h.AWSJSON(t, "cognito-idp", "create-user-pool-client", "--user-pool-id", pool, "--client-name", "pw",
		"--explicit-auth-flows", "ALLOW_USER_PASSWORD_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"), "UserPoolClient")["ClientId"].(string)
	a, _ := cognito.SRPClient(pool, newPass)
	st, out := initiateSRP(t, h, noSRP, "ann", a)
	wantErr(t, st, out, "InvalidParameterException")
	if !strings.Contains(out["message"].(string), "not enabled") {
		t.Fatalf("message %v", out)
	}
	// A client with a secret needs SECRET_HASH for SRP too.
	withSecret := sub(h.AWSJSON(t, "cognito-idp", "create-user-pool-client", "--user-pool-id", pool, "--client-name", "sec", "--generate-secret",
		"--explicit-auth-flows", "ALLOW_USER_SRP_AUTH"), "UserPoolClient")["ClientId"].(string)
	st, out = initiateSRP(t, h, withSecret, "ann", a)
	wantErr(t, st, out, "NotAuthorizedException")
}

func TestForgotPassword(t *testing.T) {
	logs := captureLog(t)
	h, _ := newCognito(t)
	pool, client := srpSetup(t, h)
	h.AWS(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "gus", "--temporary-password", tempPass)
	h.AWS(t, "cognito-idp", "admin-set-user-password", "--user-pool-id", pool, "--username", "gus", "--password", newPass, "--permanent")

	if st, out := call(t, h, "ForgotPassword", map[string]any{"ClientId": client, "Username": "nobody"}); st != 400 || out["__type"] != "UserNotFoundException" {
		t.Fatalf("unknown user: %d %v", st, out)
	}
	out := mustCall(t, h, "ForgotPassword", map[string]any{"ClientId": client, "Username": "gus"})
	if sub(out, "CodeDeliveryDetails")["DeliveryMedium"] == nil {
		t.Fatalf("delivery: %v", out)
	}
	m := resetRe.FindStringSubmatch(logs.String())
	if m == nil || m[1] != "gus" {
		t.Fatalf("no reset code in the log: %q", logs.String())
	}
	body := map[string]any{"ClientId": client, "Username": "gus", "ConfirmationCode": m[2], "Password": "Reset#Pass5"}
	wrong := map[string]any{"ClientId": client, "Username": "gus", "ConfirmationCode": "000000", "Password": "Reset#Pass5"}
	if m[2] == "000000" {
		wrong["ConfirmationCode"] = "000001"
	}
	if st, out := call(t, h, "ConfirmForgotPassword", wrong); st != 400 || out["__type"] != "CodeMismatchException" {
		t.Fatalf("wrong code: %d %v", st, out)
	}
	weak := map[string]any{"ClientId": client, "Username": "gus", "ConfirmationCode": m[2], "Password": "weak"}
	if st, out := call(t, h, "ConfirmForgotPassword", weak); st != 400 || out["__type"] != "InvalidPasswordException" {
		t.Fatalf("weak password: %d %v", st, out)
	}
	// A session from before the reset stops working.
	_, out = srpSignIn(t, h, pool, client, "gus", newPass)
	refresh := sub(out, "AuthenticationResult")["RefreshToken"]
	mustCall(t, h, "ConfirmForgotPassword", body)
	if st, out := call(t, h, "ConfirmForgotPassword", body); st != 400 || out["__type"] != "ExpiredCodeException" {
		t.Fatalf("code reuse: %d %v", st, out)
	}
	if st, out := call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "REFRESH_TOKEN_AUTH", "ClientId": client, "AuthParameters": map[string]string{"REFRESH_TOKEN": refresh.(string)}}); st != 400 {
		t.Fatalf("old refresh token still works: %d %v", st, out)
	}
	st, out := srpSignIn(t, h, pool, client, "gus", "Reset#Pass5")
	wantTokens(t, h, pool, st, out, "gus")
	st, out = srpSignIn(t, h, pool, client, "gus", newPass)
	wantErr(t, st, out, "NotAuthorizedException")

	// AdminResetUserPassword: the old password stops working until a reset is confirmed.
	h.AWS(t, "cognito-idp", "admin-reset-user-password", "--user-pool-id", pool, "--username", "gus")
	if o := h.AWSJSON(t, "cognito-idp", "admin-get-user", "--user-pool-id", pool, "--username", "gus"); o["UserStatus"] != "RESET_REQUIRED" {
		t.Fatalf("status %v", o)
	}
	st, out = srpSignIn(t, h, pool, client, "gus", "Reset#Pass5")
	wantErr(t, st, out, "PasswordResetRequiredException")
	st, out = call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": client, "AuthParameters": map[string]string{"USERNAME": "gus", "PASSWORD": "Reset#Pass5"}})
	wantErr(t, st, out, "PasswordResetRequiredException")
	ms := resetRe.FindAllStringSubmatch(logs.String(), -1)
	code := ms[len(ms)-1][2]
	mustCall(t, h, "ConfirmForgotPassword", map[string]any{"ClientId": client, "Username": "gus", "ConfirmationCode": code, "Password": "Again#Pass6"})
	st, out = srpSignIn(t, h, pool, client, "gus", "Again#Pass6")
	wantTokens(t, h, pool, st, out, "gus")

	// Unconfirmed users cannot reset.
	h.Native(t, "PATCH", "/api/v1/cognito/user-pools/"+pool, map[string]any{"auto_confirm": false})
	call(t, h, "SignUp", map[string]any{"ClientId": client, "Username": "una", "Password": newPass})
	if st, out := call(t, h, "ForgotPassword", map[string]any{"ClientId": client, "Username": "una"}); st != 400 || out["__type"] != "InvalidParameterException" {
		t.Fatalf("unconfirmed: %d %v", st, out)
	}
	for _, pw := range []string{newPass, "Reset#Pass5", "Again#Pass6"} {
		if strings.Contains(logs.String(), pw) {
			t.Fatalf("password %q logged", pw)
		}
	}
}

// pycognito's AWSSRP is a reference client: it must sign in against HomeCloud
// and get tokens that verify with the pool's JWKS. Skipped without pycognito.
func TestSRPWithPycognito(t *testing.T) {
	py := os.Getenv("HC_TEST_PYTHON")
	if py == "" {
		py = "python3"
	}
	if exec.Command(py, "-c", "import pycognito, boto3").Run() != nil {
		t.Skip("python with pycognito and boto3 not available (set HC_TEST_PYTHON)")
	}
	h, _ := newCognito(t)
	pool, client := srpSetup(t, h)
	h.AWS(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "pat", "--temporary-password", tempPass)
	script := `
from pycognito.aws_srp import AWSSRP
from botocore import UNSIGNED
from botocore.config import Config
idp = boto3.client("cognito-idp", config=Config(signature_version=UNSIGNED))
def signin(user, password):
    return AWSSRP(username=user, password=password, pool_id=POOL, client_id=CLIENT, client=idp).authenticate_user()
# Temporary password: PASSWORD_VERIFIER answers with NEW_PASSWORD_REQUIRED (pycognito's
# authenticate_user raises ForceChangePasswordException and drops the session, so drive the steps).
srp = AWSSRP(username="pat", password="` + tempPass + `", pool_id=POOL, client_id=CLIENT, client=idp)
params = srp.get_auth_params()
r = idp.initiate_auth(AuthFlow="USER_SRP_AUTH", AuthParameters=params, ClientId=CLIENT)
assert r["ChallengeName"] == "PASSWORD_VERIFIER", r
r = idp.respond_to_auth_challenge(ClientId=CLIENT, ChallengeName="PASSWORD_VERIFIER",
    ChallengeResponses=srp.process_challenge(r["ChallengeParameters"], params))
assert r["ChallengeName"] == "NEW_PASSWORD_REQUIRED", r
r = idp.respond_to_auth_challenge(ClientId=CLIENT, ChallengeName="NEW_PASSWORD_REQUIRED", Session=r["Session"],
    ChallengeResponses={"USERNAME": "pat", "NEW_PASSWORD": "` + newPass + `"})
assert "AccessToken" in r["AuthenticationResult"], r
r = signin("pat", "` + newPass + `")
res = r["AuthenticationResult"]
try:
    signin("pat", "Wrong#Pass9")
    raise SystemExit("wrong password accepted")
except idp.exceptions.NotAuthorizedException:
    pass
print(json.dumps({"access": res["AccessToken"], "id": res["IdToken"]}))
`
	script = strings.NewReplacer("POOL", `"`+pool+`"`, "CLIENT", `"`+client+`"`).Replace(script)
	out := h.Python(t, script)
	var toks struct {
		Access string `json:"access"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &toks); err != nil || toks.Access == "" {
		t.Fatalf("python output %q: %v", out, err)
	}
	if c := verifyWithJWKS(t, h, pool, toks.Access); c["username"] != "pat" {
		t.Fatalf("claims %v", c)
	}
	if c := verifyWithJWKS(t, h, pool, toks.ID); c["token_use"] != "id" {
		t.Fatalf("claims %v", c)
	}
}
