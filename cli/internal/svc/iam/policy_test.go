package iam

import (
	"encoding/json"
	"testing"
	"time"
)

func mustDoc(t *testing.T, s string) PolicyDocument {
	t.Helper()
	d, err := ParsePolicy(s)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return d
}

func TestEvaluate(t *testing.T) {
	var d PolicyDocument
	if err := json.Unmarshal([]byte(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*"},
		{"Effect":"Allow","Action":["ec2:Describe*"],"Resource":["*"]},
		{"Effect":"Deny","Action":"s3:DeleteBucket","Resource":"arn:aws:s3:::prod-*"}]}`), &d); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		action, resource string
		want             decision
	}{
		{"s3:GetObject", "arn:aws:s3:::photos", allow},
		{"S3:getobject", "arn:aws:s3:::photos", allow},
		{"s3:DeleteBucket", "arn:aws:s3:::prod-data", explicitDeny},
		{"s3:DeleteBucket", "arn:aws:s3:::dev-data", allow},
		{"ec2:DescribeInstances", "*", allow},
		{"ec2:RunInstances", "*", implicitDeny},
		{"iam:CreateUser", "*", implicitDeny},
	}
	for _, c := range cases {
		if got := evaluate([]PolicyDocument{d}, c.action, c.resource, nil); got != c.want {
			t.Errorf("%s on %s: got %v want %v", c.action, c.resource, got, c.want)
		}
	}
}

func TestMatchSlashes(t *testing.T) {
	if !match("arn:aws:iam::1:user/*", "arn:aws:iam::1:user/alice", false) {
		t.Error("wildcard should cross the slash")
	}
	if match("arn:aws:iam::1:user/bob", "arn:aws:iam::1:user/alice", false) {
		t.Error("different users must not match")
	}
}

func TestValidate(t *testing.T) {
	if err := (PolicyDocument{}).Validate(); err == nil {
		t.Error("empty document should be invalid")
	}
	if err := (PolicyDocument{Statement: Statements{{Effect: "Maybe", Action: StringList{"*"}, Resource: StringList{"*"}}}}).Validate(); err == nil {
		t.Error("bad effect should be invalid")
	}
	bad := []string{
		`not json`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Bogus":1}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","NotAction":"s3:Get*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"StringFancy":{"aws:username":"a"}}}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"not-an-ip"}}}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"NumericLessThan":{"s3:max-keys":"ten"}}}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:*","Resource":"*"}]}`,
		`{"Version":"2020-01-01","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3","Resource":"*"}]}`,
	}
	for _, b := range bad {
		d, err := ParsePolicy(b)
		if err == nil {
			err = d.Validate()
		}
		if err == nil {
			t.Errorf("accepted invalid policy %s", b)
		}
	}
	// A single statement object is valid, as in AWS.
	mustDoc(t, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:*","Resource":"*"}}`)
}

func TestReadOnlyExcludesSecrets(t *testing.T) {
	bp, ok := builtin("ReadOnlyAccess")
	if !ok {
		t.Fatal("no ReadOnlyAccess")
	}
	if evaluate([]PolicyDocument{bp.Doc}, "secretsmanager:GetSecretValue", "*", nil) != explicitDeny {
		t.Fatal("ReadOnlyAccess can read secret values")
	}
	if evaluate([]PolicyDocument{bp.Doc}, "ec2:DescribeInstances", "*", nil) != allow {
		t.Fatal("ReadOnlyAccess cannot describe")
	}
}

func TestBuiltinsValid(t *testing.T) {
	seen := map[string]bool{}
	for _, bp := range builtinPolicies {
		if seen[bp.Name] {
			t.Errorf("duplicate built-in %s", bp.Name)
		}
		seen[bp.Name] = true
		if err := bp.Doc.Validate(); err != nil {
			t.Errorf("%s: %v", bp.Name, err)
		}
	}
	for old, n := range legacyPolicyNames {
		if _, ok := builtin(n); !ok {
			t.Errorf("legacy name %s maps to missing policy %s", old, n)
		}
		if seen[old] {
			t.Errorf("legacy name %s is also a built-in", old)
		}
	}
	pu, _ := builtin("PowerUserAccess")
	if evaluate([]PolicyDocument{pu.Doc}, "iam:CreateUser", "*", nil) != implicitDeny ||
		evaluate([]PolicyDocument{pu.Doc}, "s3:PutObject", "*", nil) != allow ||
		evaluate([]PolicyDocument{pu.Doc}, "iam:CreateServiceLinkedRole", "*", nil) != allow {
		t.Error("PowerUserAccess: NotAction evaluation is wrong")
	}
}

func TestNotResource(t *testing.T) {
	d := mustDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"s3:*","NotResource":["arn:aws:s3:::public","arn:aws:s3:::public/*"]},
		{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`)
	if evaluate([]PolicyDocument{d}, "s3:GetObject", "arn:aws:s3:::public/a.txt", nil) != allow {
		t.Error("public object should be allowed")
	}
	if evaluate([]PolicyDocument{d}, "s3:GetObject", "arn:aws:s3:::private/a.txt", nil) != explicitDeny {
		t.Error("other buckets should be denied")
	}
}

func cond(t *testing.T, c string, ctx CondContext) bool {
	t.Helper()
	d := mustDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":`+c+`}]}`)
	return evaluate([]PolicyDocument{d}, "s3:GetObject", "arn:aws:s3:::b/k", ctx) == allow
}

func TestConditions(t *testing.T) {
	now := time.Now().UTC()
	ctx := CondContext{
		"aws:sourceip": {"127.0.0.1"}, "aws:username": {"alice"}, "aws:securetransport": {"false"},
		"aws:principalarn": {"arn:aws:iam::123456789012:user/alice"}, "aws:requestedregion": {"us-east-1"},
		"aws:principaltag/team": {"blue"}, "s3:max-keys": {"50"}, "aws:tagkeys": {"env", "team"},
	}
	cases := []struct {
		cond string
		want bool
	}{
		{`{"IpAddress":{"aws:SourceIp":"127.0.0.1"}}`, true},
		{`{"IpAddress":{"aws:SourceIp":["10.0.0.0/8","127.0.0.0/8"]}}`, true},
		{`{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}`, false},
		{`{"NotIpAddress":{"aws:SourceIp":"10.0.0.0/8"}}`, true},
		{`{"StringEquals":{"aws:username":"alice"}}`, true},
		{`{"StringEquals":{"aws:username":"Alice"}}`, false},
		{`{"StringEqualsIgnoreCase":{"aws:username":"ALICE"}}`, true},
		{`{"StringNotEquals":{"aws:username":["bob","carol"]}}`, true},
		{`{"StringNotEquals":{"aws:username":["alice","carol"]}}`, false},
		{`{"StringLike":{"aws:username":"al*"}}`, true},
		{`{"StringNotLike":{"aws:username":"al?ce"}}`, false},
		{`{"StringEquals":{"AWS:UserName":"alice"}}`, true}, // keys are case-insensitive
		{`{"ArnLike":{"aws:PrincipalArn":"arn:aws:iam::*:user/*"}}`, true},
		{`{"ArnEquals":{"aws:PrincipalArn":"arn:aws:iam::123456789012:user/bob"}}`, false},
		{`{"ArnNotLike":{"aws:PrincipalArn":"arn:aws:iam::*:role/*"}}`, true},
		{`{"Bool":{"aws:SecureTransport":"true"}}`, false},
		{`{"Bool":{"aws:SecureTransport":false}}`, true},
		{`{"NumericLessThanEquals":{"s3:max-keys":50}}`, true},
		{`{"NumericLessThan":{"s3:max-keys":"50"}}`, false},
		{`{"NumericGreaterThan":{"s3:max-keys":"10"}}`, true},
		{`{"DateGreaterThan":{"aws:CurrentTime":"2020-01-01T00:00:00Z"}}`, true},
		{`{"DateLessThan":{"aws:CurrentTime":"` + now.Add(-time.Hour).Format(time.RFC3339) + `"}}`, false},
		{`{"DateLessThan":{"aws:EpochTime":"` + now.Add(time.Hour).Format(time.RFC3339) + `"}}`, true},
		{`{"StringEquals":{"aws:RequestedRegion":"us-east-1"}}`, true},
		{`{"StringEquals":{"aws:PrincipalTag/team":"blue"}}`, true},
		// Missing keys: positive operators fail, negated ones hold, IfExists holds.
		{`{"StringEquals":{"aws:ResourceTag/env":"prod"}}`, false},
		{`{"StringNotEquals":{"aws:ResourceTag/env":"prod"}}`, true},
		{`{"StringEqualsIfExists":{"aws:ResourceTag/env":"prod"}}`, true},
		{`{"StringEqualsIfExists":{"aws:username":"bob"}}`, false},
		{`{"Null":{"aws:ResourceTag/env":"true"}}`, true},
		{`{"Null":{"aws:username":"true"}}`, false},
		{`{"Null":{"aws:username":"false"}}`, true},
		// Several operators and keys must all hold.
		{`{"StringEquals":{"aws:username":"alice"},"IpAddress":{"aws:SourceIp":"127.0.0.1/32"}}`, true},
		{`{"StringEquals":{"aws:username":"alice","aws:RequestedRegion":"eu-west-1"}}`, false},
		// Multi-valued keys.
		{`{"ForAllValues:StringEquals":{"aws:TagKeys":["env","team","owner"]}}`, true},
		{`{"ForAllValues:StringEquals":{"aws:TagKeys":["env"]}}`, false},
		{`{"ForAnyValue:StringEquals":{"aws:TagKeys":["team"]}}`, true},
		{`{"ForAnyValue:StringEquals":{"aws:TagKeys":["owner"]}}`, false},
		{`{"ForAllValues:StringEquals":{"aws:NoSuchKey":["x"]}}`, true},
		{`{"ForAnyValue:StringEquals":{"aws:NoSuchKey":["x"]}}`, false},
		// Policy variables.
		{`{"StringEquals":{"aws:username":"${aws:username}"}}`, true},
		{`{"StringLike":{"aws:PrincipalArn":"arn:aws:iam::*:user/${aws:username}"}}`, true},
		{`{"StringEquals":{"aws:username":"${aws:PrincipalTag/missing}"}}`, false},
	}
	for _, c := range cases {
		if got := cond(t, c.cond, ctx); got != c.want {
			t.Errorf("%s: got %v want %v", c.cond, got, c.want)
		}
	}
}

func TestConditionDenyAndVariables(t *testing.T) {
	d := mustDoc(t, `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"iam:*AccessKey*","Resource":"arn:aws:iam::*:user/${aws:username}"},
		{"Effect":"Deny","Action":"*","Resource":"*","Condition":{"NotIpAddress":{"aws:SourceIp":"127.0.0.1"}}}]}`)
	alice := CondContext{"aws:username": {"alice"}, "aws:sourceip": {"127.0.0.1"}}
	if evaluate([]PolicyDocument{d}, "iam:CreateAccessKey", "arn:aws:iam::1:user/alice", alice) != allow {
		t.Error("alice should manage her own keys")
	}
	if evaluate([]PolicyDocument{d}, "iam:CreateAccessKey", "arn:aws:iam::1:user/bob", alice) != implicitDeny {
		t.Error("alice must not manage bob's keys")
	}
	remote := CondContext{"aws:username": {"alice"}, "aws:sourceip": {"192.0.2.1"}}
	if evaluate([]PolicyDocument{d}, "iam:CreateAccessKey", "arn:aws:iam::1:user/alice", remote) != explicitDeny {
		t.Error("requests from other addresses must be denied")
	}
	// Without a username (a role session) the variable resolves to nothing.
	if evaluate([]PolicyDocument{d}, "iam:CreateAccessKey", "arn:aws:iam::1:user/alice", CondContext{"aws:sourceip": {"127.0.0.1"}}) != implicitDeny {
		t.Error("unresolved variable must not match")
	}
	// Version 2008-10-17 has no policy variables.
	old := mustDoc(t, `{"Version":"2008-10-17","Statement":[{"Effect":"Allow","Action":"iam:*","Resource":"arn:aws:iam::*:user/${aws:username}"}]}`)
	if evaluate([]PolicyDocument{old}, "iam:GetUser", "arn:aws:iam::1:user/alice", alice) != implicitDeny {
		t.Error("variables are literal in 2008-10-17 policies")
	}
}

func TestBoundary(t *testing.T) {
	admin := doc("Allow", "*")
	b := doc("Allow", "s3:*", "sqs:*")
	if decide([]PolicyDocument{admin}, &b, "s3:GetObject", "*", nil) != allow {
		t.Error("boundary should allow s3")
	}
	if decide([]PolicyDocument{admin}, &b, "iam:CreateUser", "*", nil) != implicitDeny {
		t.Error("boundary should cap admin")
	}
	if decide(nil, &b, "s3:GetObject", "*", nil) != implicitDeny {
		t.Error("a boundary grants nothing by itself")
	}
	empty := PolicyDocument{}
	if decide([]PolicyDocument{admin}, &empty, "s3:GetObject", "*", nil) != implicitDeny {
		t.Error("a missing boundary allows nothing")
	}
}

func TestTrustConditions(t *testing.T) {
	d, err := ParseTrust(json.RawMessage(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},
		"Action":"sts:AssumeRole","Condition":{"StringEquals":{"sts:ExternalId":"s3cret"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	caller := "arn:aws:iam::123456789012:user/alice"
	if d.trusts("sts:AssumeRole", caller, "", "123456789012", CondContext{"sts:externalid": {"s3cret"}}) != allow {
		t.Error("matching external id should be trusted")
	}
	if d.trusts("sts:AssumeRole", caller, "", "123456789012", CondContext{}) != implicitDeny {
		t.Error("missing external id must not be trusted")
	}
}

func TestSubstitute(t *testing.T) {
	ctx := CondContext{"aws:username": {"bob"}}
	if s, ok := substitute("home/${aws:username}/${*}", ctx, true); !ok || s != `home/bob/\*` {
		t.Errorf("got %q %v", s, ok)
	}
	if s, ok := substitute("${aws:userid, 'none'}", ctx, false); !ok || s != "none" {
		t.Errorf("default: got %q %v", s, ok)
	}
	if _, ok := substitute("${aws:userid}", ctx, false); ok {
		t.Error("missing variable should not resolve")
	}
	if !match(`home/bob/\*`, "home/bob/*", false) || match(`home/bob/\*`, "home/bob/x", false) {
		t.Error("${*} must match a literal asterisk")
	}
}
