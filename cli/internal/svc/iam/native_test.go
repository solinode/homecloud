package iam_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

// nativeAs calls the native API with an access key and returns the status and body.
func nativeAs(t *testing.T, h *awstest.Harness, akid, secret, method, path string, body any) (int, string) {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+akid+":"+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestNativeRoles(t *testing.T) {
	h := awstest.New(t)
	root := func(method, path string, body any) (int, string) {
		return nativeAs(t, h, h.AccessKeyID, h.SecretKey, method, path, body)
	}
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "fn", "path": "/svc/", "assume_role_policy": trust, "tags": map[string]string{"a": "1"}})

	// Tags: merge with PUT, remove with DELETE ?keys=, replace with PATCH.
	h.Native(t, "PUT", "/api/v1/iam/roles/fn/tags", map[string]any{"tags": map[string]string{"b": "2"}})
	h.Native(t, "DELETE", "/api/v1/iam/roles/fn/tags?keys=a", nil)
	tags := h.AWSJSON(t, "iam", "list-role-tags", "--role-name", "fn")["Tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["Key"] != "b" {
		t.Fatalf("tags %v", tags)
	}
	h.Native(t, "PATCH", "/api/v1/iam/roles/fn", map[string]any{"tags": map[string]string{"c": "3"}})
	if tags := h.AWSJSON(t, "iam", "list-role-tags", "--role-name", "fn")["Tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["Key"] != "c" {
		t.Fatalf("patched tags %v", tags)
	}

	// Revoking sessions by ARN revokes the role's sessions.
	h.AWS(t, "iam", "update-assume-role-policy", "--role-name", "fn", "--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::`+h.Env.AccountID+`:user/root"},"Action":"sts:AssumeRole"}]}`)
	c := h.AWSJSON(t, "sts", "assume-role", "--role-arn", "arn:aws:iam::"+h.Env.AccountID+":role/svc/fn", "--role-session-name", "s1")["Credentials"].(map[string]any)
	arn := url.PathEscape("arn:aws:iam::" + h.Env.AccountID + ":role/svc/fn")
	if st, body := root("POST", "/api/v1/iam/roles/"+arn+"/revoke-sessions", nil); st != 200 {
		t.Fatalf("revoke: %d %s", st, body)
	}
	out, err := h.AWSAs(t, c["AccessKeyId"].(string), c["SecretAccessKey"].(string), c["SessionToken"].(string), "sts", "get-caller-identity")
	wantErr(t, out, err, "InvalidClientTokenId")

	// Trust policies need an Action and a real principal.
	for _, bad := range []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"}}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{},"Action":"sts:AssumeRole"}]}`,
	} {
		if st, body := root("POST", "/api/v1/iam/roles", map[string]any{"name": "bad", "assume_role_policy": bad}); st != 400 || !strings.Contains(body, "MalformedPolicyDocument") {
			t.Fatalf("bad trust accepted: %d %s", st, body)
		}
	}

	// Attaching policies at creation is authorized against the role's real ARN (with its path).
	akid, secret := userKeys(t, h, "maker", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreateRole","Resource":"*"},
		{"Effect":"Allow","Action":"iam:AttachRolePolicy","Resource":"arn:aws:iam::*:role/team/*"}]}`)
	if st, body := nativeAs(t, h, akid, secret, "POST", "/api/v1/iam/roles", map[string]any{"name": "r1", "assume_role_policy": trust, "policies": []string{"AmazonS3ReadOnlyAccess"}}); st != 403 {
		t.Fatalf("attach outside /team/: %d %s", st, body)
	}
	if st, body := nativeAs(t, h, akid, secret, "POST", "/api/v1/iam/roles", map[string]any{"name": "r2", "path": "/team/", "assume_role_policy": trust, "policies": []string{"AmazonS3ReadOnlyAccess"}}); st != 200 {
		t.Fatalf("attach inside /team/: %d %s", st, body)
	}

	// Names are unique ignoring case.
	doc := map[string]any{"Statement": []any{map[string]any{"Effect": "Allow", "Action": "s3:*", "Resource": "*"}}}
	for _, c := range []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/iam/roles", map[string]any{"name": "FN", "assume_role_policy": trust}},
		{"/api/v1/iam/users", map[string]any{"name": "ROOT"}},
		{"/api/v1/iam/policies", map[string]any{"name": "administratoraccess", "document": doc}},
		{"/api/v1/iam/policies", map[string]any{"name": "s3fullaccess", "document": doc}},
	} {
		if st, out := root("POST", c.path, c.body); st != 409 || !strings.Contains(out, "EntityAlreadyExists") {
			t.Errorf("%s %v: %d %s", c.path, c.body, st, out)
		}
	}
	h.AWS(t, "iam", "create-group", "--group-name", "Ops")
	out, err = h.AWSErr(t, "iam", "create-group", "--group-name", "ops")
	wantErr(t, out, err, "EntityAlreadyExists")
	h.AWS(t, "iam", "create-instance-profile", "--instance-profile-name", "Web")
	out, err = h.AWSErr(t, "iam", "create-instance-profile", "--instance-profile-name", "WEB")
	wantErr(t, out, err, "EntityAlreadyExists")
	h.AWS(t, "iam", "create-user", "--user-name", "carol")
	h.AWS(t, "iam", "update-user", "--user-name", "carol", "--new-user-name", "Carol") // a case-only rename is fine
	out, err = h.AWSErr(t, "iam", "update-user", "--user-name", "Carol", "--new-user-name", "ROOT")
	wantErr(t, out, err, "EntityAlreadyExists")
}
