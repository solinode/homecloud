package ecs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ecs"
)

// stubRoles resolves every spelling of a role to its real ARN, as IAM does
// (role/dev-/admin and "admin" both name the role "admin").
type stubRoles struct{ acct string }

func (r stubRoles) TaskRole(ref string) (string, error) {
	name := ref[strings.LastIndex(ref, "/")+1:]
	return "arn:aws:iam::" + r.acct + ":role/" + name, nil
}
func (stubRoles) TaskCredentials(string, string, time.Duration) (ecs.Credentials, error) {
	return ecs.Credentials{}, nil
}

// Handing a task role to a task hands its credentials to the task's code, so
// every way of registering a task definition needs iam:PassRole on the role,
// judged on the role's real ARN.
func TestTaskRoleNeedsPassRole(t *testing.T) {
	h := awstest.New(t)
	e := ecs.New(h.Env, nil, nil, h.Secrets)
	e.Roles = stubRoles{h.Env.AccountID}
	e.Routes(h.Router)
	e.RegisterAWS()
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "ecs-dev", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{"Effect": "Allow", "Action": "ecs:*", "Resource": "*"},
			map[string]any{"Effect": "Allow", "Action": "iam:PassRole", "Resource": "arn:aws:iam::" + h.Env.AccountID + ":role/dev-*"},
		}}})
	akid, secret := h.User(t, "dev", "ecs-dev")

	td := func(role string) map[string]any {
		return map[string]any{"family": "f", "image": "alpine", "task_role": role}
	}
	for _, role := range []string{"admin", "arn:aws:iam::" + h.Env.AccountID + ":role/admin", "arn:aws:iam::" + h.Env.AccountID + ":role/dev-/admin"} {
		if code, body := h.NativeAs(t, akid, secret, "POST", "/api/v1/ecs/task-definitions", td(role)); code != 403 {
			t.Fatalf("native register with task role %q: %d %s", role, code, body)
		}
		in := `{"family":"g","taskRoleArn":"` + role + `","containerDefinitions":[{"name":"a","image":"alpine"}]}`
		if out, err := h.AWSAs(t, akid, secret, "", "ecs", "register-task-definition", "--cli-input-json", in); err == nil || !strings.Contains(out, "iam:PassRole") {
			t.Fatalf("AWS register with task role %q: %v %s", role, err, out)
		}
	}
	// Images of the local registry need the ECR pull permissions.
	for _, image := range []string{"localhost:5500/secret:1", "123456789012.dkr.ecr.us-east-1.amazonaws.com/secret@sha256:abc"} {
		h.Env.Cfg.ECRPort = 5500
		if code, body := h.NativeAs(t, akid, secret, "POST", "/api/v1/ecs/task-definitions", map[string]any{"family": "img", "image": image}); code != 403 || !strings.Contains(string(body), "ecr:BatchGetImage") {
			t.Fatalf("image %s: %d %s", image, code, body)
		}
	}
	if code, body := h.NativeAs(t, akid, secret, "POST", "/api/v1/ecs/task-definitions", map[string]any{"family": "img", "image": "nginx:alpine"}); code != 200 {
		t.Fatalf("a public image was refused: %d %s", code, body)
	}
	if code, body := h.NativeAs(t, akid, secret, "POST", "/api/v1/ecs/task-definitions", td("dev-app")); code != 200 {
		t.Fatalf("a role the caller may pass was refused: %d %s", code, body)
	}
}
