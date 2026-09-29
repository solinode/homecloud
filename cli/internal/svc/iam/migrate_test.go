package iam

import (
	"slices"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

// Data written by earlier HomeCloud versions: managed policies under their
// HomeCloud names and account ARNs, customer policies without versions,
// groups without IDs.
func TestMigrateLegacyData(t *testing.T) {
	env := svctest.Env(t)
	st := env.Store
	legacy := Policy{Name: "S3FullAccess", ARN: "arn:hc:iam::" + env.AccountID + ":policy/S3FullAccess", Managed: true, Document: doc("Allow", "s3:*")}
	custom := Policy{Name: "mine", ARN: "arn:hc:iam::" + env.AccountID + ":policy/mine", Document: doc("Allow", "sqs:*"), CreatedAt: core.Now(), UpdatedAt: core.Now()}
	for _, p := range []Policy{legacy, custom} {
		if err := store.Put(st, cPolicies, p.Name, p); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Put(st, cUsers, "dev", User{Name: "dev", ID: "HCUA1", ARN: "arn:aws:iam::" + env.AccountID + ":user/dev",
		AttachedPolicies: []string{"S3FullAccess", "AmazonS3FullAccess", "mine"}})
	_ = store.Put(st, cGroups, "ops", Group{Name: "ops", ARN: "arn:aws:iam::" + env.AccountID + ":group/ops", AttachedPolicies: []string{"LambdaFullAccess"}})
	_ = store.Put(st, cRoles, "fn", Role{Name: "fn", ID: "HCRO1", AttachedPolicies: []string{"DynamoDBFullAccess"}})

	s := New(env)
	if _, err := s.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if store.Has(st, cPolicies, "S3FullAccess") {
		t.Error("legacy managed policy record should be gone")
	}
	u, _ := store.Get[User](st, cUsers, "dev")
	if !slices.Equal(u.AttachedPolicies, []string{"AmazonS3FullAccess", "mine"}) {
		t.Errorf("user policies %v", u.AttachedPolicies)
	}
	g, _ := store.Get[Group](st, cGroups, "ops")
	if !slices.Equal(g.AttachedPolicies, []string{"AWSLambda_FullAccess"}) || g.ID == "" || g.path() != "/" {
		t.Errorf("group %+v", g)
	}
	r, _ := store.Get[Role](st, cRoles, "fn")
	if !slices.Equal(r.AttachedPolicies, []string{"AmazonDynamoDBFullAccess"}) {
		t.Errorf("role policies %v", r.AttachedPolicies)
	}
	m, _ := store.Get[Policy](st, cPolicies, "mine")
	if m.ARN != "arn:aws:iam::"+env.AccountID+":policy/mine" || m.DefaultVersion != "v1" || len(m.Versions) != 1 || m.ID == "" {
		t.Errorf("customer policy %+v", m)
	}
	a, _ := store.Get[Policy](st, cPolicies, "AmazonS3FullAccess")
	if a.ARN != "arn:aws:iam::aws:policy/AmazonS3FullAccess" || !a.Managed {
		t.Errorf("managed policy %+v", a)
	}

	// Every way of naming a managed policy resolves to it.
	for _, ref := range []string{"AmazonS3FullAccess", "S3FullAccess", "arn:aws:iam::aws:policy/AmazonS3FullAccess",
		"arn:aws:iam::" + env.AccountID + ":policy/S3FullAccess", "arn:hc:iam::" + env.AccountID + ":policy/S3FullAccess"} {
		if p, err := s.resolvePolicy(ref); err != nil || p.Name != "AmazonS3FullAccess" {
			t.Errorf("%s: %v %v", ref, p.Name, err)
		}
	}
	if _, err := s.resolvePolicy("arn:aws:iam::aws:policy/mine"); err == nil {
		t.Error("a customer policy is not at an AWS ARN")
	}
	if _, err := s.resolvePolicy("arn:aws:iam::999999999999:policy/mine"); err == nil {
		t.Error("another account's ARN must not resolve")
	}

	// The migrated user's permissions still work.
	p := s.principal(u, "")
	if !p.Can("s3:PutObject", "arn:aws:s3:::b/k") || !p.Can("sqs:SendMessage", "*") || p.Can("ec2:RunInstances", "*") {
		t.Error("migrated permissions are wrong")
	}

	// A second bootstrap changes nothing.
	if _, err := s.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	a2, _ := store.Get[Policy](st, cPolicies, "AmazonS3FullAccess")
	if !a2.UpdatedAt.Equal(a.UpdatedAt) {
		t.Error("re-bootstrap should keep managed policy dates")
	}
}

// A customer policy named like a newly added AWS managed policy keeps its content.
func TestCustomerPolicyShadowsBuiltin(t *testing.T) {
	env := svctest.Env(t)
	mine := Policy{Name: "PowerUserAccess", ARN: "arn:aws:iam::" + env.AccountID + ":policy/PowerUserAccess", Document: doc("Allow", "sqs:*")}
	_ = store.Put(env.Store, cPolicies, mine.Name, mine)
	s := New(env)
	if _, err := s.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	p, _ := store.Get[Policy](env.Store, cPolicies, "PowerUserAccess")
	if p.Managed || p.Document.Statement[0].Action[0] != "sqs:*" {
		t.Errorf("customer policy overwritten: %+v", p)
	}
}
