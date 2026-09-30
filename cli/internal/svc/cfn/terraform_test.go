package cfn_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTerraformCloudFormationStack applies, re-plans, updates and destroys an
// aws_cloudformation_stack with Terraform or OpenTofu. Opt-in
// (HC_TEST_TERRAFORM=1) since it downloads the provider; set TF_PLUGIN_CACHE_DIR
// to reuse it.
func TestTerraformCloudFormationStack(t *testing.T) {
	if os.Getenv("HC_TEST_TERRAFORM") == "" {
		t.Skip("set HC_TEST_TERRAFORM=1 to run")
	}
	bin, err := exec.LookPath("terraform")
	if err != nil {
		if bin, err = exec.LookPath("tofu"); err != nil {
			t.Skip("terraform/tofu not installed")
		}
	}
	e := newEnv(t, false)
	dir := t.TempDir()
	cfg := func(retention string) string {
		return `
terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}
provider "aws" {
  region                  = "us-east-1"
  skip_metadata_api_check = true
}
resource "aws_cloudformation_stack" "app" {
  name         = "tf-app"
  capabilities = ["CAPABILITY_IAM"]
  parameters   = { Retention = "` + retention + `" }
  tags         = { team = "core" }
  template_body = jsonencode({
    Parameters = { Retention = { Type = "Number", Default = 60 } }
    Resources = {
      Queue = { Type = "AWS::SQS::Queue", Properties = { QueueName = "tf-queue", MessageRetentionPeriod = { Ref = "Retention" } } }
      Role  = { Type = "AWS::IAM::Role", Properties = { AssumeRolePolicyDocument = { Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "lambda.amazonaws.com" }, Action = "sts:AssumeRole" }] } } }
    }
    Outputs = {
      QueueUrl = { Value = { Ref = "Queue" } }
      RoleArn  = { Value = { "Fn::GetAtt" = ["Role", "Arn"] } }
    }
  })
}
output "queue_url" { value = aws_cloudformation_stack.app.outputs["QueueUrl"] }
`
	}
	write := func(retention string) {
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(cfg(retention)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := append(e.Environ(e.AccessKeyID, e.SecretKey, ""), "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	write("60")
	if out, err := run("init", "-input=false", "-no-color"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if out, err := run("apply", "-auto-approve", "-input=false", "-no-color"); err != nil || !strings.Contains(out, "queue_url") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if out, err := run("plan", "-detailed-exitcode", "-input=false", "-no-color"); err != nil {
		t.Fatalf("plan after apply is not empty: %v\n%s", err, out)
	}
	write("120")
	if out, err := run("apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if st := e.stack(t, "tf-app"); str(st, "StackStatus") != "UPDATE_COMPLETE" {
		t.Fatalf("after update: %v", st)
	}
	if out, err := run("destroy", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
	e.waitGone(t, "tf-app")
}
