package iam_test

import (
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

func TestNativePermissionsBoundary(t *testing.T) {
	h := awstest.New(t)
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "bound", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:*", "Resource": "*"}}}})
	h.Native(t, "POST", "/api/v1/iam/users", map[string]any{"name": "bu"})
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "br", "assume_role_policy": trust})
	for _, p := range []string{"/api/v1/iam/users/bu", "/api/v1/iam/roles/br"} {
		b := string(h.Native(t, "PUT", p+"/permissions-boundary", map[string]any{"policy": "bound"}))
		if !strings.Contains(b, ":policy/bound") {
			t.Fatalf("put boundary %s: %s", p, b)
		}
		h.Native(t, "DELETE", p+"/permissions-boundary", nil)
		if b := string(h.Native(t, "GET", p, nil)); strings.Contains(b, ":policy/bound") {
			t.Fatalf("delete boundary %s: %s", p, b)
		}
	}
}
