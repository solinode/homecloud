package s3

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	_ "github.com/homecloudhq/homecloud/cli/internal/svc/iam" // installs the policy engine
)

const testPolicy = `{"Version":"2012-10-17","Statement":[
	{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/public/*"},
	{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam::111122223333:user/alice"]},"Action":["s3:Put*"],"Resource":"arn:aws:s3:::b/*"},
	{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/cond/*","Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}},
	{"Effect":"Allow","Principal":"*","Action":"s3:ListBucket","Resource":"arn:aws:s3:::b","Condition":{"StringLike":{"s3:prefix":["docs/*"]}}},
	{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::111122223333:role/ci"},"Action":"s3:*","Resource":"arn:aws:s3:::b/secret*"},
	{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":["arn:aws:s3:::b","arn:aws:s3:::b/*"],"Condition":{"Bool":{"aws:SecureTransport":"false"},"StringEquals":{"s3:x-amz-acl":"public-read"}}}]}`

func TestBucketPolicyEvaluate(t *testing.T) {
	allowAll := func(string, string, map[string][]string) httpx.Decision { return httpx.Allowed }
	alice := &httpx.Principal{AccountID: "111122223333", ARN: "arn:aws:iam::111122223333:user/alice", Context: map[string][]string{"aws:securetransport": {"true"}}}
	bob := &httpx.Principal{AccountID: "111122223333", ARN: "arn:aws:iam::111122223333:user/bob", Identity: allowAll, Context: map[string][]string{"aws:securetransport": {"true"}}}
	ci := &httpx.Principal{AccountID: "111122223333", ARN: "arn:aws:sts::111122223333:assumed-role/ci/s", RoleName: "ci", Identity: allowAll,
		Context: map[string][]string{"aws:principalarn": {"arn:aws:iam::111122223333:role/ci"}}}
	keys := func(kv ...string) map[string][]string {
		m := map[string][]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = []string{kv[i+1]}
		}
		return m
	}
	anon := func(action, res string, k map[string][]string) bool {
		return httpx.PermitsAnonymous(action, res, httpx.Access{Policy: testPolicy, Keys: k})
	}
	perm := func(p *httpx.Principal, action, res string, k map[string][]string) bool {
		return p.Permits(action, res, httpx.Access{Policy: testPolicy, Keys: k})
	}
	for _, c := range []struct {
		name string
		got  bool
		want bool
	}{
		{"anonymous public read", anon("s3:GetObject", "arn:aws:s3:::b/public/x", nil), true},
		{"anonymous private", anon("s3:GetObject", "arn:aws:s3:::b/private/x", nil), false},
		{"anonymous from outside the range", anon("s3:GetObject", "arn:aws:s3:::b/cond/x", keys("aws:sourceip", "192.168.1.1")), false},
		{"anonymous from inside the range", anon("s3:GetObject", "arn:aws:s3:::b/cond/x", keys("aws:sourceip", "10.1.2.3")), true},
		{"list with the right prefix", anon("s3:ListBucket", "arn:aws:s3:::b", keys("s3:prefix", "docs/a")), true},
		{"list with another prefix", anon("s3:ListBucket", "arn:aws:s3:::b", keys("s3:prefix", "other/")), false},
		{"list without a prefix", anon("s3:ListBucket", "arn:aws:s3:::b", nil), false},
		{"named user without identity policy", perm(alice, "s3:PutObject", "arn:aws:s3:::b/k", nil), true},
		{"named user other action", perm(alice, "s3:DeleteObject", "arn:aws:s3:::b/k", nil), false},
		{"identity allow", perm(bob, "s3:DeleteObject", "arn:aws:s3:::b/k", nil), true},
		{"deny by role", perm(ci, "s3:GetObject", "arn:aws:s3:::b/secret.txt", nil), false},
		{"role elsewhere", perm(ci, "s3:GetObject", "arn:aws:s3:::b/public/x", nil), true},
		{"deny on plain HTTP with public-read", perm(bob, "s3:PutObject", "arn:aws:s3:::b/k", keys("aws:securetransport", "false", "s3:x-amz-acl", "public-read")), false},
		{"plain HTTP without the ACL", perm(bob, "s3:PutObject", "arn:aws:s3:::b/k", keys("aws:securetransport", "false")), true},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	root := &httpx.Principal{AccountID: "111122223333", Root: true, Identity: allowAll,
		Context: map[string][]string{"aws:securetransport": {"false"}}}
	if !perm(root, "s3:PutObject", "arn:aws:s3:::b/k", keys("s3:x-amz-acl", "public-read")) {
		t.Error("the account root is not subject to resource policy denies")
	}
}

func TestRequestKeys(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/b?list-type=2&prefix=docs%2F&delimiter=%2F&max-keys=5", nil)
	r.Header.Set("X-Amz-Acl", "public-read")
	r.Header.Set("X-Amz-Tagging", "Team=blue&env=prod")
	a := &s3req{q: &awsapi.Req{R: r}, query: r.URL.Query()}
	k := a.requestKeys("s3:ListBucket")
	for key, want := range map[string]string{
		"s3:prefix": "docs/", "s3:delimiter": "/", "s3:max-keys": "5", "s3:x-amz-acl": "public-read",
		"s3:requestobjecttag/team": "blue", "s3:requestobjecttag/env": "prod",
	} {
		if len(k[key]) != 1 || k[key][0] != want {
			t.Errorf("%s = %v, want %s", key, k[key], want)
		}
	}
	if k := a.requestKeys("s3:GetObject"); k["s3:prefix"] != nil {
		t.Errorf("s3:prefix only applies to listings: %v", k)
	}
	if !publicPolicy(`{"Statement":{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":"s3:GetObject","Resource":"*"}}`) ||
		publicPolicy(`{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":"true"}}}]}`) {
		t.Error("publicPolicy")
	}
}
