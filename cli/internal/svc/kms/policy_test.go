package kms_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestKeyPolicyEnforcement(t *testing.T) {
	h, _, _ := setup(t)
	acct := h.Env.AccountID
	m := meta(t, h, "kms", "create-key")
	id := m["KeyId"].(string)
	bk, bs := h.User(t, "bob")
	ck, cs := h.User(t, "carol", "KMSFullAccess")
	dk, ds := h.User(t, "dave")

	denied := func(ak, sk string, args ...string) {
		t.Helper()
		out, err := h.AWSAs(t, ak, sk, "", args...)
		if err == nil || !strings.Contains(out, "AccessDenied") {
			t.Fatalf("aws %s: want AccessDenied, got %v %s", strings.Join(args, " "), err, out)
		}
	}
	ok := func(ak, sk string, args ...string) {
		t.Helper()
		if out, err := h.AWSAs(t, ak, sk, "", args...); err != nil {
			t.Fatalf("aws %s: %v %s", strings.Join(args, " "), err, out)
		}
	}
	encrypt := []string{"kms", "encrypt", "--key-id", id, "--plaintext", "eA=="}
	describe := []string{"kms", "describe-key", "--key-id", id}
	setPolicy := func(policy string) {
		t.Helper()
		h.AWS(t, "kms", "put-key-policy", "--key-id", id, "--policy-name", "default", "--policy", policy)
	}
	root := fmt.Sprintf(`{"Sid":"root","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"kms:*","Resource":"*"}`, acct)
	stmt := func(effect, principal, actions, cond string) string {
		if cond != "" {
			cond = `,"Condition":` + cond
		}
		return fmt.Sprintf(`{"Effect":"%s","Principal":%s,"Action":%s,"Resource":"*"%s}`, effect, principal, actions, cond)
	}
	user := func(name string) string { return fmt.Sprintf(`{"AWS":"arn:aws:iam::%s:user/%s"}`, acct, name) }
	policy := func(stmts ...string) string {
		return `{"Version":"2012-10-17","Statement":[` + strings.Join(stmts, ",") + `]}`
	}

	// The default key policy delegates to IAM: existing behavior.
	ok(ck, cs, encrypt...)
	denied(bk, bs, encrypt...)
	if p := h.AWSJSON(t, "kms", "get-key-policy", "--key-id", id, "--policy-name", "default")["Policy"].(string); !strings.Contains(p, ":root") {
		t.Fatalf("default key policy: %s", p)
	}

	// The key policy can grant a principal directly, without IAM policies.
	setPolicy(policy(root, stmt("Allow", user("bob"), `["kms:Encrypt","kms:Decrypt","kms:DescribeKey"]`, "")))
	ok(bk, bs, encrypt...)
	ok(bk, bs, describe...)
	denied(bk, bs, "kms", "schedule-key-deletion", "--key-id", id)
	denied(dk, ds, encrypt...)
	ok(ck, cs, encrypt...)

	// Without the account root in the key policy, IAM policies alone do not help.
	setPolicy(policy(stmt("Allow", user("bob"), `"kms:*"`, "")))
	denied(ck, cs, encrypt...)
	ok(bk, bs, encrypt...)
	ok(h.AccessKeyID, h.SecretKey, encrypt...) // the account root is never locked out
	ok(h.AccessKeyID, h.SecretKey, "kms", "put-key-policy", "--key-id", id, "--policy-name", "default", "--policy", policy(root))

	// An explicit Deny in the key policy beats IAM permissions.
	setPolicy(policy(root, stmt("Deny", user("carol"), `"kms:Encrypt"`, "")))
	denied(ck, cs, encrypt...)
	ok(ck, cs, describe...)

	// Conditions: TLS only, and a network range.
	setPolicy(policy(root, stmt("Deny", `"*"`, `"kms:*"`, `{"Bool":{"aws:SecureTransport":"false"}}`)))
	denied(ck, cs, encrypt...)
	setPolicy(policy(root, stmt("Deny", `"*"`, `"kms:Encrypt"`, `{"NotIpAddress":{"aws:SourceIp":["127.0.0.0/8","::1/128"]}}`)))
	ok(ck, cs, encrypt...)
	setPolicy(policy(root, stmt("Deny", `"*"`, `"kms:Encrypt"`, `{"NotIpAddress":{"aws:SourceIp":["10.0.0.0/8"]}}`)))
	denied(ck, cs, encrypt...)

	// A public principal narrowed by kms:CallerAccount.
	setPolicy(policy(root, stmt("Allow", `{"AWS":"*"}`, `["kms:Encrypt"]`, fmt.Sprintf(`{"StringEquals":{"kms:CallerAccount":"%s"}}`, acct))))
	ok(dk, ds, encrypt...)
	setPolicy(policy(root, stmt("Allow", `{"AWS":"*"}`, `["kms:Encrypt"]`, `{"StringEquals":{"kms:CallerAccount":"999999999999"}}`)))
	denied(dk, ds, encrypt...)

	// A key created with a key policy starts with it.
	own := meta(t, h, "kms", "create-key", "--policy", policy(root, stmt("Allow", user("dave"), `"kms:Encrypt"`, "")))["KeyId"].(string)
	ok(dk, ds, "kms", "encrypt", "--key-id", own, "--plaintext", "eA==")
	denied(bk, bs, "kms", "encrypt", "--key-id", own, "--plaintext", "eA==")
}
