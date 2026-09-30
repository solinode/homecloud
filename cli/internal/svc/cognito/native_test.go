package cognito_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// post calls a public native route.
func post(t *testing.T, url string, body any, bearer string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// The native API and the AWS layer share their logic: a user made natively
// signs in over the AWS protocol, and tokens from either verify the same way.
func TestNativeAndAWSShareState(t *testing.T) {
	h, cog := newCognito(t)
	var pool map[string]any
	if err := json.Unmarshal(h.Native(t, "POST", "/api/v1/cognito/user-pools", map[string]any{"name": "native"}), &pool); err != nil {
		t.Fatal(err)
	}
	pid := pool["id"].(string)
	var cl map[string]any
	_ = json.Unmarshal(h.Native(t, "POST", "/api/v1/cognito/user-pools/"+pid+"/clients", map[string]any{"name": "c"}), &cl)
	cid := cl["id"].(string)
	h.Native(t, "POST", "/api/v1/cognito/user-pools/"+pid+"/groups", map[string]any{"name": "staff"})
	h.Native(t, "POST", "/api/v1/cognito/user-pools/"+pid+"/users", map[string]any{"username": "nat", "password": "pass1234", "groups": []string{"staff"}})

	// Native sign-in; the token is valid for the authorizer.
	base := h.URL + "/cognito/" + pid
	st, out := post(t, base+"/auth", map[string]any{"client_id": cid, "username": "nat", "password": "pass1234"}, "")
	if st != 200 || out["access_token"] == nil {
		t.Fatalf("native auth: %d %v", st, out)
	}
	if _, err := cog.VerifyToken(pid, out["id_token"].(string), cid); err != nil {
		t.Fatal(err)
	}

	// The AWS layer sees the native pool, client, user and group.
	if o := h.AWS(t, "cognito-idp", "list-users-in-group", "--user-pool-id", pid, "--group-name", "staff"); !bytes.Contains([]byte(o), []byte("nat")) {
		t.Fatalf("group members: %s", o)
	}
	st, out = call(t, h, "InitiateAuth", map[string]any{"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": cid, "AuthParameters": map[string]string{"USERNAME": "nat", "PASSWORD": "pass1234"}})
	if st != 200 || sub(out, "AuthenticationResult") == nil {
		t.Fatalf("AWS sign-in of a native user: %d %v", st, out)
	}
	claims := verifyWithJWKS(t, h, pid, sub(out, "AuthenticationResult")["IdToken"].(string))
	if gs, _ := claims["cognito:groups"].([]any); len(gs) != 1 || gs[0] != "staff" {
		t.Fatalf("groups %v", claims["cognito:groups"])
	}

	// Native forced-password-change flow.
	h.Native(t, "POST", "/api/v1/cognito/user-pools/"+pid+"/users", map[string]any{"username": "tmp", "password": "temp1234", "temporary_password": true})
	st, ch := post(t, base+"/auth", map[string]any{"client_id": cid, "username": "tmp", "password": "temp1234"}, "")
	if st != 200 || ch["challenge"] != "NEW_PASSWORD_REQUIRED" {
		t.Fatalf("native challenge: %d %v", st, ch)
	}
	st, out = post(t, base+"/respond", map[string]any{"session": ch["session"], "new_password": "fresh1234"}, "")
	if st != 200 || out["access_token"] == nil {
		t.Fatalf("native respond: %d %v", st, out)
	}
	// Native duplicate group keeps failing.
	if st, _ := h.NativeAs(t, h.AccessKeyID, h.SecretKey, "POST", "/api/v1/cognito/user-pools/"+pid+"/groups", map[string]any{"name": "staff"}); st != 409 {
		t.Fatalf("duplicate group status %d", st)
	}
}
