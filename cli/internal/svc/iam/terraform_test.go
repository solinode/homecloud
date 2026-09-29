package iam_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

// TestTerraform applies and destroys a Terraform configuration with the AWS
// provider against HomeCloud's IAM and STS. It needs terraform (HC_TEST_TERRAFORM
// or PATH) and network access to download the provider on first use; set
// TF_PLUGIN_CACHE_DIR to reuse the download.
func TestTerraform(t *testing.T) {
	bin := os.Getenv("HC_TEST_TERRAFORM")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("terraform"); err != nil {
			t.Skip("terraform not installed (set HC_TEST_TERRAFORM)")
		}
	}
	if testing.Short() {
		t.Skip("terraform test is slow")
	}
	h := awstest.New(t)
	dir := t.TempDir()
	write := func(policyAction string) {
		cfg := strings.ReplaceAll(tfConfig, "POLICY_ACTION", policyAction)
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(h.Environ(h.AccessKeyID, h.SecretKey, ""), "TF_VAR_endpoint="+h.URL, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("terraform %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	write("s3:GetObject")
	t.Log(must("init", "-input=false", "-no-color"))
	t.Log(must("apply", "-auto-approve", "-input=false", "-no-color"))
	t.Cleanup(func() { _, _ = run("destroy", "-auto-approve", "-input=false", "-no-color") })

	// Reading everything back must show no drift.
	if out, err := run("plan", "-detailed-exitcode", "-input=false", "-no-color"); err != nil {
		t.Fatalf("plan after apply is not empty: %v\n%s", err, out)
	}
	role := h.AWSJSON(t, "iam", "get-role", "--role-name", "tf-fn")["Role"].(map[string]any)
	if role["Path"] != "/tf/" || role["MaxSessionDuration"].(float64) != 7200 {
		t.Fatalf("role %v", role)
	}
	ap := h.AWS(t, "iam", "list-attached-role-policies", "--role-name", "tf-fn")
	if !strings.Contains(ap, "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole") || !strings.Contains(ap, "tf-read") {
		t.Fatalf("attachments %s", ap)
	}
	if out := h.AWS(t, "iam", "get-group", "--group-name", "tf-devs"); !strings.Contains(out, "tf-user") {
		t.Fatalf("group %s", out)
	}

	// Changing the policy document creates a new default version.
	write("s3:ListBucket")
	must("apply", "-auto-approve", "-input=false", "-no-color")
	vs := h.AWS(t, "iam", "list-policy-versions", "--policy-arn", "arn:aws:iam::"+h.Env.AccountID+":policy/tf/tf-read")
	if !strings.Contains(vs, `"v2"`) {
		t.Fatalf("versions %s", vs)
	}
	if out, err := run("plan", "-detailed-exitcode", "-input=false", "-no-color"); err != nil {
		t.Fatalf("plan after update is not empty: %v\n%s", err, out)
	}

	must("destroy", "-auto-approve", "-input=false", "-no-color")
	for _, args := range [][]string{
		{"iam", "get-role", "--role-name", "tf-fn"},
		{"iam", "get-user", "--user-name", "tf-user"},
		{"iam", "get-policy", "--policy-arn", "arn:aws:iam::" + h.Env.AccountID + ":policy/tf/tf-read"},
		{"iam", "get-instance-profile", "--instance-profile-name", "tf-profile"},
	} {
		out, err := h.AWSErr(t, args...)
		wantErr(t, out, err, "NoSuchEntity")
	}
}

const tfConfig = `
terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}

variable "endpoint" { type = string }

provider "aws" {
  region                      = "us-east-1"
  skip_metadata_api_check     = true
  skip_region_validation      = true
  endpoints {
    iam = var.endpoint
    sts = var.endpoint
  }
}

data "aws_caller_identity" "me" {}

data "aws_iam_policy_document" "trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "fn" {
  name                 = "tf-fn"
  path                 = "/tf/"
  description          = "function role"
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = 7200
  tags                 = { app = "tf" }
}

resource "aws_iam_policy" "read" {
  name        = "tf-read"
  path        = "/tf/"
  description = "read the bucket"
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["POLICY_ACTION"], Resource = "arn:aws:s3:::tf-bucket/*" }]
  })
  tags = { owner = data.aws_caller_identity.me.account_id }
}

resource "aws_iam_role_policy_attachment" "read" {
  role       = aws_iam_role.fn.name
  policy_arn = aws_iam_policy.read.arn
}

resource "aws_iam_role_policy_attachment" "logs" {
  role       = aws_iam_role.fn.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role_policy" "inline" {
  name = "queue"
  role = aws_iam_role.fn.id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "sqs:SendMessage", Resource = "*" }]
  })
}

resource "aws_iam_instance_profile" "p" {
  name = "tf-profile"
  role = aws_iam_role.fn.name
}

resource "aws_iam_user" "u" {
  name = "tf-user"
  path = "/people/"
  tags = { team = "blue" }
}

resource "aws_iam_access_key" "k" {
  user = aws_iam_user.u.name
}

resource "aws_iam_user_policy_attachment" "ro" {
  user       = aws_iam_user.u.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

resource "aws_iam_user_policy" "own" {
  name = "own-keys"
  user = aws_iam_user.u.name
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["iam:*AccessKey*"], Resource = "arn:aws:iam::*:user/people/$${aws:username}" }]
  })
}

resource "aws_iam_group" "devs" {
  name = "tf-devs"
}

resource "aws_iam_group_policy_attachment" "devs" {
  group      = aws_iam_group.devs.name
  policy_arn = aws_iam_policy.read.arn
}

resource "aws_iam_group_policy" "devs" {
  name  = "extra"
  group = aws_iam_group.devs.name
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "sqs:ReceiveMessage", Resource = "*" }]
  })
}

resource "aws_iam_group_membership" "devs" {
  name  = "devs"
  group = aws_iam_group.devs.name
  users = [aws_iam_user.u.name]
}

output "key" { value = aws_iam_access_key.k.id }
`
