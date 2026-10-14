package cognito_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

// signedCall sends a SigV4-signed cognito-idp request straight to the server,
// skipping the client-side parameter validation the AWS CLI and boto3 do, so
// out-of-range values reach the service as they would from any SDK.
func signedCall(t *testing.T, h *awstest.Harness, op string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	hdr := http.Header{}
	hdr.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+op)
	hdr.Set("Content-Type", "application/x-amz-json-1.1")
	st, _, raw := h.Signed(t, "cognito-idp", "POST", "/", hdr, b)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return st, out
}

func policy(n any) map[string]any {
	return map[string]any{"PasswordPolicy": map[string]any{"MinimumLength": n}}
}

// AWS accepts a PasswordPolicy MinimumLength of 6-99; anything else is an
// InvalidParameterException, on create and on update, over both APIs.
func TestPasswordPolicyMinimumLengthBounds(t *testing.T) {
	h, _ := newCognito(t)
	for _, n := range []any{6, 99} {
		st, out := signedCall(t, h, "CreateUserPool", map[string]any{"PoolName": "ok", "Policies": policy(n)})
		if st != 200 || sub(out, "UserPool", "Policies", "PasswordPolicy")["MinimumLength"] != float64(n.(int)) {
			t.Fatalf("MinimumLength %v: %d %v", n, st, out)
		}
	}
	for _, n := range []any{0, 5, 100, 128, -1, 8.5, "8"} {
		st, out := signedCall(t, h, "CreateUserPool", map[string]any{"PoolName": "bad", "Policies": policy(n)})
		if st != 400 || out["__type"] != "InvalidParameterException" || !strings.Contains(out["message"].(string), "minimumLength") {
			t.Fatalf("create MinimumLength %v: %d %v", n, st, out)
		}
	}

	pool := sub(h.AWSJSON(t, "cognito-idp", "create-user-pool", "--pool-name", "upd"), "UserPool")["Id"].(string)
	st, out := signedCall(t, h, "UpdateUserPool", map[string]any{"UserPoolId": pool, "Policies": policy(100)})
	if st != 400 || out["__type"] != "InvalidParameterException" {
		t.Fatalf("update MinimumLength 100: %d %v", st, out)
	}
	pp := sub(h.AWSJSON(t, "cognito-idp", "describe-user-pool", "--user-pool-id", pool), "UserPool", "Policies", "PasswordPolicy")
	if pp["MinimumLength"] != float64(8) {
		t.Fatalf("a rejected update changed the policy: %v", pp)
	}
	if o, err := h.AWSErr(t, "cognito-idp", "update-user-pool", "--user-pool-id", pool, "--policies", "PasswordPolicy={MinimumLength=100}"); err == nil || !strings.Contains(o, "InvalidParameterException") {
		t.Fatalf("aws cli update MinimumLength 100: %v %s", err, o)
	}

	// A given policy is taken as a whole: the Go SDK v2 leaves false flags out
	// of the request, so a missing character class is not required.
	st, out = signedCall(t, h, "UpdateUserPool", map[string]any{"UserPoolId": pool, "Policies": map[string]any{"PasswordPolicy": map[string]any{"MinimumLength": 12, "RequireNumbers": true}}})
	if st != 200 {
		t.Fatalf("update: %d %v", st, out)
	}
	pp = sub(h.AWSJSON(t, "cognito-idp", "describe-user-pool", "--user-pool-id", pool), "UserPool", "Policies", "PasswordPolicy")
	if pp["MinimumLength"] != float64(12) || pp["RequireNumbers"] != true || pp["RequireUppercase"] != false || pp["RequireLowercase"] != false || pp["RequireSymbols"] != false {
		t.Fatalf("partial policy: %v", pp)
	}
	// Without a policy a new pool requires every character class.
	pp = sub(h.AWSJSON(t, "cognito-idp", "create-user-pool", "--pool-name", "dflt"), "UserPool", "Policies", "PasswordPolicy")
	if pp["MinimumLength"] != float64(8) || pp["RequireUppercase"] != true || pp["RequireSymbols"] != true {
		t.Fatalf("default policy: %v", pp)
	}

	// The native API enforces the same bounds.
	for n, want := range map[int]int{6: 200, 99: 200, 5: 400, 100: 400, 128: 400} {
		st, body := h.NativeAs(t, h.AccessKeyID, h.SecretKey, "POST", "/api/v1/cognito/user-pools",
			map[string]any{"name": "native", "password_policy": map[string]any{"min_length": n}})
		if st/100 != want/100 {
			t.Fatalf("native min_length %d: %d %s", n, st, body)
		}
	}
}
