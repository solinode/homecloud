package cognito_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

const jsScript = `
const { CognitoUserPool, CognitoUser, AuthenticationDetails } = require("amazon-cognito-identity-js");
const p = new CognitoUserPool({ UserPoolId: process.env.POOL, ClientId: process.env.CLIENT, endpoint: process.env.ENDPOINT + "/" });
function signIn(pw) {
  return new Promise((resolve, reject) => {
    const u = new CognitoUser({ Username: "nia", Pool: p });
    u.authenticateUser(new AuthenticationDetails({ Username: "nia", Password: pw }), {
      onSuccess: (s) => resolve({ access: s.getAccessToken().getJwtToken(), id: s.getIdToken().getJwtToken() }),
      onFailure: reject,
    });
  });
}
(async () => {
  const ok = await signIn(process.env.PW);
  let code = "";
  try { await signIn("Wrong#Pass9"); } catch (e) { code = e.code || e.name; }
  if (code !== "NotAuthorizedException") throw new Error("wrong password gave " + code);
  console.log(JSON.stringify(ok));
})().catch((e) => { console.error(e); process.exit(1); });
`

// amazon-cognito-identity-js (what Amplify uses) signs in with USER_SRP_AUTH.
// Set HC_TEST_NODE_PATH to a node_modules directory containing it; skipped otherwise.
func TestSRPWithCognitoIdentityJS(t *testing.T) {
	nm := os.Getenv("HC_TEST_NODE_PATH")
	node, err := exec.LookPath("node")
	if nm == "" || err != nil {
		t.Skip("node or HC_TEST_NODE_PATH (node_modules with amazon-cognito-identity-js) not available")
	}
	if _, err := os.Stat(nm + "/amazon-cognito-identity-js"); err != nil {
		t.Skip("amazon-cognito-identity-js not installed in HC_TEST_NODE_PATH")
	}
	h, _ := newCognito(t)
	pool, client := srpSetup(t, h)
	h.AWS(t, "cognito-idp", "admin-create-user", "--user-pool-id", pool, "--username", "nia", "--temporary-password", tempPass)
	h.AWS(t, "cognito-idp", "admin-set-user-password", "--user-pool-id", pool, "--username", "nia", "--password", newPass, "--permanent")
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/t.js", []byte(jsScript), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, dir+"/t.js")
	cmd.Env = append(os.Environ(), "NODE_PATH="+nm, "POOL="+pool, "CLIENT="+client, "ENDPOINT="+h.URL, "PW="+newPass)
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v %s", err, b)
	}
	var toks struct {
		Access string `json:"access"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(b, &toks); err != nil {
		t.Fatalf("node output %q: %v", b, err)
	}
	if c := verifyWithJWKS(t, h, pool, toks.Access); c["username"] != "nia" {
		t.Fatalf("claims %v", c)
	}
	verifyWithJWKS(t, h, pool, toks.ID)
}
