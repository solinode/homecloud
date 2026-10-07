package autoscaling_test

import (
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/autoscaling"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// profiles maps instance profile "<x>-prof" to role "<x>".
type profiles struct{ acct string }

func (p profiles) InstanceProfile(ref string) (arn, id, role string, err error) {
	name := strings.TrimSuffix(ref[strings.LastIndex(ref, "/")+1:], "-prof")
	return "arn:aws:iam::" + p.acct + ":instance-profile/" + name + "-prof", "AIPA" + name, "arn:aws:iam::" + p.acct + ":role/" + name, nil
}
func (profiles) InstanceCredentials(string, string, time.Duration) (ec2.Credentials, error) {
	return ec2.Credentials{}, nil
}

// An instance profile's role reaches whoever controls the instance, so putting
// one in a launch template, launching with it or creating an Auto Scaling group
// that launches from such a template all need iam:PassRole for the role.
func TestInstanceProfileNeedsPassRole(t *testing.T) {
	h := awstest.New(t)
	v := vpc.New(h.Env)
	e := ec2.New(h.Env, v)
	e.Roles = profiles{h.Env.AccountID}
	e.RegisterAWS()
	e.Routes(h.Router)
	cw, err := cloudwatch.New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	a := autoscaling.New(h.Env, e, nil, cw)
	a.RegisterAWS()
	a.Routes(h.Router)

	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "dev", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{"Effect": "Allow", "Action": []string{"ec2:*", "autoscaling:*"}, "Resource": "*"},
			map[string]any{"Effect": "Allow", "Action": "iam:PassRole", "Resource": "arn:aws:iam::" + h.Env.AccountID + ":role/dev-*"},
		}}})
	akid, secret := h.User(t, "dev", "dev")

	// Native launch.
	code, body := h.NativeAs(t, akid, secret, "POST", "/api/v1/ec2/instances", map[string]any{"image_id": "ami-x", "iam_instance_profile": "admin-prof"})
	if code != 403 || !strings.Contains(string(body), "iam:PassRole") {
		t.Fatalf("native launch with another role's profile: %d %s", code, body)
	}

	// Launch templates.
	if out, err := h.AWSAs(t, akid, secret, "", "ec2", "create-launch-template", "--launch-template-name", "evil",
		"--launch-template-data", `{"IamInstanceProfile":{"Name":"admin-prof"}}`); err == nil || !strings.Contains(out, "iam:PassRole") {
		t.Fatalf("template with another role's profile: %v %s", err, out)
	}
	h.AWSAs(t, akid, secret, "", "ec2", "create-launch-template", "--launch-template-name", "fine", "--launch-template-data", `{"IamInstanceProfile":{"Name":"dev-app-prof"}}`)

	// A template an administrator made with a powerful profile can't be used by
	// the developer to start a group.
	h.AWS(t, "ec2", "create-launch-template", "--launch-template-name", "admin-lt", "--launch-template-data", `{"IamInstanceProfile":{"Name":"admin-prof"},"InstanceType":"t3.micro"}`)
	code, body = h.NativeAs(t, akid, secret, "POST", "/api/v1/autoscaling/groups",
		map[string]any{"name": "g", "template": map[string]any{"name": "admin-lt", "version": "$Latest"}, "min_size": 0, "max_size": 1})
	if code != 403 || !strings.Contains(string(body), "iam:PassRole") {
		t.Fatalf("group from a template with another role's profile: %d %s", code, body)
	}
}
