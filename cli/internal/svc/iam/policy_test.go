package iam

import (
	"encoding/json"
	"testing"
)

func TestEvaluate(t *testing.T) {
	var d PolicyDocument
	if err := json.Unmarshal([]byte(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"s3:*","Resource":"*"},
		{"Effect":"Allow","Action":["ec2:Describe*"],"Resource":["*"]},
		{"Effect":"Deny","Action":"s3:DeleteBucket","Resource":"arn:hc:s3:::prod-*"}]}`), &d); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		action, resource string
		want             decision
	}{
		{"s3:GetObject", "arn:hc:s3:::photos", allow},
		{"S3:getobject", "arn:hc:s3:::photos", allow},
		{"s3:DeleteBucket", "arn:hc:s3:::prod-data", explicitDeny},
		{"s3:DeleteBucket", "arn:hc:s3:::dev-data", allow},
		{"ec2:DescribeInstances", "*", allow},
		{"ec2:RunInstances", "*", implicitDeny},
		{"iam:CreateUser", "*", implicitDeny},
	}
	for _, c := range cases {
		if got := evaluate([]PolicyDocument{d}, c.action, c.resource); got != c.want {
			t.Errorf("%s on %s: got %v want %v", c.action, c.resource, got, c.want)
		}
	}
}

func TestMatchSlashes(t *testing.T) {
	if !match("arn:hc:iam:local-1:1:user/*", "arn:hc:iam:local-1:1:user/alice", false) {
		t.Error("wildcard should cross the slash")
	}
	if match("arn:hc:iam:local-1:1:user/bob", "arn:hc:iam:local-1:1:user/alice", false) {
		t.Error("different users must not match")
	}
}

func TestValidate(t *testing.T) {
	if err := (PolicyDocument{}).Validate(); err == nil {
		t.Error("empty document should be invalid")
	}
	if err := (PolicyDocument{Statement: []Statement{{Effect: "Maybe", Action: StringList{"*"}, Resource: StringList{"*"}}}}).Validate(); err == nil {
		t.Error("bad effect should be invalid")
	}
}

func TestReadOnlyExcludesSecrets(t *testing.T) {
	for _, bp := range builtinPolicies {
		if bp.Name != "ReadOnlyAccess" {
			continue
		}
		if evaluate([]PolicyDocument{bp.Doc}, "secretsmanager:GetSecretValue", "*") != explicitDeny {
			t.Fatal("ReadOnlyAccess can read secret values")
		}
		if evaluate([]PolicyDocument{bp.Doc}, "ec2:DescribeInstances", "*") != allow {
			t.Fatal("ReadOnlyAccess cannot describe")
		}
	}
}
