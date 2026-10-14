package iam

import (
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

// The CDK bootstrap stack scopes its publishing roles' policies with
// aws:ResourceAccount; every resource a server holds is in its account.
func TestResourceAccountConditionKey(t *testing.T) {
	env := svctest.Env(t)
	s := New(env)
	d := mustDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetBucket*",
		"Resource":"arn:aws:s3:::assets","Condition":{"StringEquals":{"aws:ResourceAccount":["`+env.AccountID+`"]}}}]}`)
	other := mustDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetBucket*",
		"Resource":"arn:aws:s3:::assets","Condition":{"StringEquals":{"aws:ResourceAccount":"999999999999"}}}]}`)
	for name, ctx := range map[string]CondContext{
		"role": s.roleContext(Role{Name: "publish", ARN: "arn:aws:iam::" + env.AccountID + ":role/publish"}, tempCred{SessionName: "cdk"}),
		"user": s.userContext(User{Name: "dev", ARN: "arn:aws:iam::" + env.AccountID + ":user/dev"}),
	} {
		if evaluate([]PolicyDocument{d}, "s3:GetBucketLocation", "arn:aws:s3:::assets", ctx) != allow {
			t.Errorf("%s: aws:ResourceAccount does not match the server's account", name)
		}
		if evaluate([]PolicyDocument{other}, "s3:GetBucketLocation", "arn:aws:s3:::assets", ctx) != implicitDeny {
			t.Errorf("%s: aws:ResourceAccount matches another account", name)
		}
	}
}
