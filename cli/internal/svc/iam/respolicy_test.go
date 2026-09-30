package iam

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

const acct = "123456789012"

func user(name string) *httpx.Principal {
	return &httpx.Principal{AccountID: acct, UserName: name, ARN: "arn:aws:iam::" + acct + ":user/" + name,
		Context: map[string][]string{"aws:principalarn": {"arn:aws:iam::" + acct + ":user/" + name}, "aws:username": {name},
			"aws:principalaccount": {acct}, "aws:securetransport": {"true"}, "aws:sourceip": {"10.1.2.3"}}}
}

func withIdentity(p *httpx.Principal, docs ...PolicyDocument) *httpx.Principal {
	p.Identity = identityFunc(p, docs, nil)
	return p
}

func TestResourcePolicyEvaluation(t *testing.T) {
	queue := "arn:aws:sqs:us-east-1:" + acct + ":jobs"
	allowSend := doc("Allow", "sqs:SendMessage")
	none := PolicyDocument{Version: policyVersion}
	denySend := doc("Deny", "sqs:SendMessage")

	pol := func(stmts string) httpx.Access {
		return httpx.Access{Policy: `{"Version":"2012-10-17","Statement":[` + stmts + `]}`}
	}
	grantAlice := `{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + acct + `:user/alice"},"Action":"sqs:SendMessage","Resource":"` + queue + `"}`
	grantAny := `{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"` + queue + `"}`
	grantRoot := `{"Effect":"Allow","Principal":{"AWS":"` + acct + `"},"Action":"sqs:SendMessage","Resource":"` + queue + `"}`
	denyBob := `{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::` + acct + `:user/bob"},"Action":"sqs:*","Resource":"` + queue + `"}`
	denyAcct := `{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::` + acct + `:root"},"Action":"sqs:SendMessage","Resource":"` + queue + `"}`
	denyInsecure := `{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"` + queue + `","Condition":{"Bool":{"aws:SecureTransport":"false"}}}`

	insecure := user("alice")
	insecure.Context["aws:securetransport"] = []string{"false"}

	for _, c := range []struct {
		name string
		p    *httpx.Principal
		acc  httpx.Access
		want bool
	}{
		{"identity only", withIdentity(user("alice"), allowSend), httpx.Access{}, true},
		{"no grant anywhere", withIdentity(user("alice"), none), httpx.Access{}, false},
		{"resource policy grants the named user", withIdentity(user("alice"), none), pol(grantAlice), true},
		{"resource policy does not grant others", withIdentity(user("bob"), none), pol(grantAlice), false},
		{"resource policy grants everyone", withIdentity(user("bob"), none), pol(grantAny), true},
		{"naming the account only delegates", withIdentity(user("bob"), none), pol(grantRoot), false},
		{"delegation plus identity allow", withIdentity(user("bob"), allowSend), pol(grantRoot), true},
		{"resource deny beats identity allow", withIdentity(user("bob"), allowSend), pol(denyBob), false},
		{"resource deny beats a resource allow", withIdentity(user("bob"), none), pol(grantAny + "," + denyBob), false},
		{"resource deny of the account", withIdentity(user("carol"), allowSend), pol(denyAcct), false},
		{"identity deny beats resource allow", withIdentity(user("alice"), denySend), pol(grantAlice), false},
		{"deny on plain HTTP applies", withIdentity(insecure, allowSend), pol(denyInsecure), false},
		{"deny on plain HTTP is off over TLS", withIdentity(user("alice"), allowSend), pol(denyInsecure), true},
		{"other resource statements do not apply", withIdentity(user("bob"), none), pol(`{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"arn:aws:sqs:us-east-1:` + acct + `:other"}`), false},
		{"action match ignores case", withIdentity(user("bob"), none), pol(`{"Effect":"Allow","Principal":"*","Action":"SQS:sendmessage","Resource":"*"}`), true},
		{"unparseable policy grants nothing", withIdentity(user("bob"), none), httpx.Access{Policy: "{nope"}, false},
		{"NotPrincipal deny leaves the named user alone", withIdentity(user("alice"), allowSend),
			pol(`{"Effect":"Deny","NotPrincipal":{"AWS":"arn:aws:iam::` + acct + `:user/alice"},"Action":"sqs:*","Resource":"*"}`), true},
		{"NotPrincipal deny hits everyone else", withIdentity(user("bob"), allowSend),
			pol(`{"Effect":"Deny","NotPrincipal":{"AWS":"arn:aws:iam::` + acct + `:user/alice"},"Action":"sqs:*","Resource":"*"}`), false},
	} {
		if got := c.p.Permits("sqs:SendMessage", queue, c.acc); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	// The account root is not locked out by resource policy denies.
	root := user("root")
	root.Root = true
	root.Identity = func(string, string, map[string][]string) httpx.Decision { return httpx.Allowed }
	if !root.Permits("sqs:SendMessage", queue, pol(denyBob+","+denyAcct)) {
		t.Error("root must not be subject to resource policy denies")
	}
	// Anonymous callers need a Principal "*" allow.
	if !httpx.PermitsAnonymous("sqs:SendMessage", queue, pol(grantAny)) || httpx.PermitsAnonymous("sqs:SendMessage", queue, pol(grantAlice)) {
		t.Error("anonymous access")
	}
	if httpx.PermitsAnonymous("sqs:SendMessage", queue, pol(grantAny+","+denyInsecure)) == false {
		t.Error("anonymous caller has no aws:SecureTransport key, so the Bool deny does not apply")
	}
}

func TestKeyPolicyEvaluation(t *testing.T) {
	key := "arn:aws:kms:us-east-1:" + acct + ":key/1234"
	def := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + acct + `:root"},"Action":"kms:*","Resource":"*"}]}`
	direct := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + acct + `:user/alice"},"Action":"kms:Decrypt","Resource":"*"}]}`
	noRoot := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + acct + `:user/admin"},"Action":"kms:*","Resource":"*"}]}`
	deny := def[:len(def)-2] + `,{"Effect":"Deny","Principal":"*","Action":"kms:Decrypt","Resource":"*","Condition":{"StringNotEquals":{"aws:PrincipalAccount":"` + acct + `"}}}]}`
	kmsAll := doc("Allow", "kms:*")
	none := PolicyDocument{Version: policyVersion}
	for _, c := range []struct {
		name string
		p    *httpx.Principal
		pol  string
		want bool
	}{
		{"default policy delegates to IAM", withIdentity(user("bob"), kmsAll), def, true},
		{"default policy without IAM permission", withIdentity(user("bob"), none), def, false},
		{"key policy names the user", withIdentity(user("alice"), none), direct, true},
		{"key policy names another user", withIdentity(user("bob"), none), direct, false},
		{"IAM alone is not enough without delegation", withIdentity(user("bob"), kmsAll), direct, false},
		{"IAM alone is not enough without root", withIdentity(user("bob"), kmsAll), noRoot, false},
		{"condition deny does not fire for the account", withIdentity(user("bob"), kmsAll), deny, true},
	} {
		if got := c.p.Permits("kms:Decrypt", key, httpx.Access{Policy: c.pol, KeyPolicy: true}); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestServiceDelivery(t *testing.T) {
	queue := "arn:aws:sqs:us-east-1:" + acct + ":jobs"
	pol := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"` + queue + `",` +
		`"Condition":{"ArnEquals":{"aws:SourceArn":"arn:aws:sns:us-east-1:` + acct + `:alerts"},"StringEquals":{"aws:SourceAccount":"` + acct + `"}}}]}`
	ev := func(service, src string) httpx.Verdict {
		keys := map[string][]string{}
		if src != "" {
			keys["aws:sourcearn"] = []string{src}
			keys["aws:sourceaccount"] = []string{acct}
		}
		return evalResourcePolicy(pol, httpx.Who{Service: service}, "sqs:SendMessage", queue, keys)
	}
	if !ev("sns.amazonaws.com", "arn:aws:sns:us-east-1:"+acct+":alerts").Allow {
		t.Error("the topic named in aws:SourceArn must be allowed")
	}
	if ev("sns.amazonaws.com", "arn:aws:sns:us-east-1:"+acct+":other").Allow {
		t.Error("another topic must not be allowed")
	}
	if ev("events.amazonaws.com", "arn:aws:sns:us-east-1:"+acct+":alerts").Allow {
		t.Error("another service must not be allowed")
	}
	if ev("sns.amazonaws.com", "").Allow {
		t.Error("a delivery without a source must not satisfy an aws:SourceArn condition")
	}
	// An IAM principal is not a service.
	if evalResourcePolicy(pol, httpx.Who{Principal: user("bob")}, "sqs:SendMessage", queue, nil).Allow {
		t.Error("users are not the sns service")
	}
}

func TestConditionOperators(t *testing.T) {
	now := time.Now().UTC()
	ctx := CondContext{
		"aws:sourceip":              {"203.0.113.9"},
		"aws:securetransport":       {"true"},
		"aws:principalarn":          {"arn:aws:iam::" + acct + ":role/deploy/ci"},
		"aws:principalaccount":      {acct},
		"aws:username":              {"alice"},
		"aws:userid":                {"AIDAEXAMPLE"},
		"s3:prefix":                 {"home/alice/docs"},
		"s3:delimiter":              {"/"},
		"s3:max-keys":               {"50"},
		"s3:x-amz-acl":              {"private"},
		"s3:existingobjecttag/team": {"blue"},
		"s3:requestobjecttag/env":   {"prod"},
		"aws:tagkeys":               {"a", "b"},
	}
	for _, c := range []struct {
		name string
		cond string
		want bool
	}{
		{"IpAddress in range", `{"IpAddress":{"aws:SourceIp":["10.0.0.0/8","203.0.113.0/24"]}}`, true},
		{"IpAddress out of range", `{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}`, false},
		{"NotIpAddress", `{"NotIpAddress":{"aws:SourceIp":"203.0.113.0/24"}}`, false},
		{"NotIpAddress elsewhere", `{"NotIpAddress":{"aws:SourceIp":"10.0.0.0/8"}}`, true},
		{"IPv6 CIDR does not match IPv4", `{"IpAddress":{"aws:SourceIp":"2001:db8::/32"}}`, false},
		{"Bool SecureTransport", `{"Bool":{"aws:SecureTransport":"true"}}`, true},
		{"Bool SecureTransport false", `{"Bool":{"aws:SecureTransport":"false"}}`, false},
		{"ArnLike principal", `{"ArnLike":{"aws:PrincipalArn":"arn:aws:iam::*:role/deploy/*"}}`, true},
		{"ArnNotLike principal", `{"ArnNotLike":{"aws:PrincipalArn":"arn:aws:iam::*:role/deploy/*"}}`, false},
		{"StringEquals account", `{"StringEquals":{"aws:PrincipalAccount":"` + acct + `"}}`, true},
		{"StringNotEquals account", `{"StringNotEquals":{"aws:PrincipalAccount":"` + acct + `"}}`, false},
		{"username variable", `{"StringLike":{"s3:prefix":"home/${aws:username}/*"}}`, true},
		{"userid", `{"StringEquals":{"aws:userid":"AIDAEXAMPLE"}}`, true},
		{"prefix StringLike", `{"StringLike":{"s3:prefix":["docs/*","home/*"]}}`, true},
		{"prefix StringNotLike", `{"StringNotLike":{"s3:prefix":["home/*"]}}`, false},
		{"delimiter", `{"StringEquals":{"s3:delimiter":"/"}}`, true},
		{"max-keys numeric", `{"NumericLessThanEquals":{"s3:max-keys":"100"}}`, true},
		{"max-keys numeric too big", `{"NumericLessThan":{"s3:max-keys":"10"}}`, false},
		{"acl", `{"StringEquals":{"s3:x-amz-acl":["public-read","public-read-write"]}}`, false},
		{"acl not equals", `{"StringNotEquals":{"s3:x-amz-acl":["public-read","public-read-write"]}}`, true},
		{"existing object tag", `{"StringEquals":{"s3:ExistingObjectTag/team":"blue"}}`, true},
		{"existing object tag mismatch", `{"StringEquals":{"s3:ExistingObjectTag/team":"red"}}`, false},
		{"request object tag", `{"StringEquals":{"s3:RequestObjectTag/env":"prod"}}`, true},
		{"missing key with Null true", `{"Null":{"s3:RequestObjectTag/absent":"true"}}`, true},
		{"present key with Null true", `{"Null":{"s3:prefix":"true"}}`, false},
		{"present key with Null false", `{"Null":{"s3:prefix":"false"}}`, true},
		{"StringEqualsIfExists on a missing key", `{"StringEqualsIfExists":{"s3:versionid":"1"}}`, true},
		{"StringEquals on a missing key", `{"StringEquals":{"s3:versionid":"1"}}`, false},
		{"ForAnyValue", `{"ForAnyValue:StringEquals":{"aws:TagKeys":["b","z"]}}`, true},
		{"ForAllValues", `{"ForAllValues:StringEquals":{"aws:TagKeys":["a","b","c"]}}`, true},
		{"ForAllValues fails", `{"ForAllValues:StringEquals":{"aws:TagKeys":["a"]}}`, false},
		{"ForAllValues on a missing key", `{"ForAllValues:StringEquals":{"aws:Nothing":["a"]}}`, true},
		{"CurrentTime after", `{"DateGreaterThan":{"aws:CurrentTime":"` + now.Add(-time.Hour).Format(time.RFC3339) + `"}}`, true},
		{"CurrentTime before", `{"DateLessThan":{"aws:CurrentTime":"` + now.Add(-time.Hour).Format(time.RFC3339) + `"}}`, false},
		{"EpochTime", `{"NumericGreaterThan":{"aws:EpochTime":"` + "1000000000" + `"}}`, true},
		{"several conditions are ANDed", `{"Bool":{"aws:SecureTransport":"true"},"StringEquals":{"aws:username":"bob"}}`, false},
	} {
		got, err := evalConditions(json.RawMessage(c.cond), ctx, true)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
