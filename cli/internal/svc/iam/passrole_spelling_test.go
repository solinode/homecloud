package iam_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// A scoped iam:PassRole policy must judge the role the caller really names: a
// bare name, or an ARN whose path part matches an Allow pattern, is still the
// role "admin" and must hit the Deny on it.
func TestPassRoleIsJudgedOnTheResolvedRole(t *testing.T) {
	h := awstest.New(t)
	acct := h.Env.AccountID
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	h.AWS(t, "iam", "create-role", "--role-name", "admin", "--assume-role-policy-document", trust)
	h.AWS(t, "iam", "create-role", "--role-name", "dev-app", "--assume-role-policy-document", trust)
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "pass-dev", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{"Effect": "Allow", "Action": "iam:PassRole", "Resource": "arn:aws:iam::" + acct + ":role/dev-*"},
			map[string]any{"Effect": "Deny", "Action": "iam:PassRole", "Resource": "arn:aws:iam::" + acct + ":role/admin"},
			map[string]any{"Effect": "Allow", "Action": "iam:PassRole", "Resource": "*"},
		}}})
	akid, secret := h.User(t, "dev", "pass-dev")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+akid+":"+secret)
	p, err := h.IAM.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range []string{"admin", "arn:aws:iam::" + acct + ":role/admin", "arn:aws:iam::" + acct + ":role/dev-/admin", "arn:aws:iam::" + acct + ":role/x/admin"} {
		if p.Permits("iam:PassRole", res, httpx.Access{}) {
			t.Errorf("iam:PassRole on %q was allowed despite the Deny on role/admin", res)
		}
	}
	for _, res := range []string{"dev-app", "arn:aws:iam::" + acct + ":role/dev-app"} {
		if !p.Permits("iam:PassRole", res, httpx.Access{}) {
			t.Errorf("iam:PassRole on %q was refused", res)
		}
	}
}
