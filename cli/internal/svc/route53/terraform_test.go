package route53

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/acm"
)

// TestTerraform applies, re-plans and destroys hosted zones, records and an
// ACM certificate with its DNS validation records, using Terraform or OpenTofu
// and the hashicorp/aws provider. Opt-in (HC_TEST_TERRAFORM=1) since it
// downloads the provider; set TF_PLUGIN_CACHE_DIR to reuse it.
func TestTerraform(t *testing.T) {
	if os.Getenv("HC_TEST_TERRAFORM") == "" {
		t.Skip("set HC_TEST_TERRAFORM=1 to run")
	}
	bin, err := exec.LookPath("terraform")
	if err != nil {
		if bin, err = exec.LookPath("tofu"); err != nil {
			t.Skip("terraform/tofu not installed")
		}
	}
	h, _ := setup(t)
	a := acm.New(h.Env, h.Secrets)
	a.RegisterAWS()

	dir := t.TempDir()
	cfg := `
terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}
provider "aws" {
  region                      = "us-east-1"
  skip_metadata_api_check     = true
  skip_credentials_validation = true
  skip_requesting_account_id  = true
}
resource "aws_route53_zone" "pub" {
  name    = "tf.example.com"
  comment = "public zone"
  tags    = { team = "core" }
}
resource "aws_route53_zone" "priv" {
  name = "tf.internal"
  vpc {
    vpc_id = "vpc-aaa"
  }
}
resource "aws_route53_record" "a" {
  zone_id = aws_route53_zone.pub.zone_id
  name    = "www.tf.example.com"
  type    = "A"
  ttl     = 60
  records = ["192.0.2.1", "192.0.2.2"]
}
resource "aws_route53_record" "wild" {
  zone_id = aws_route53_zone.pub.zone_id
  name    = "*.tf.example.com"
  type    = "CNAME"
  ttl     = 60
  records = ["www.tf.example.com"]
}
resource "aws_route53_record" "txt" {
  zone_id = aws_route53_zone.pub.zone_id
  name    = "tf.example.com"
  type    = "TXT"
  ttl     = 60
  records = ["v=spf1 -all", "v=DMARC1; p=none"]
}
resource "aws_route53_record" "mx" {
  zone_id = aws_route53_zone.pub.zone_id
  name    = "tf.example.com"
  type    = "MX"
  ttl     = 60
  records = ["10 mail.tf.example.com"]
}
resource "aws_route53_record" "lb" {
  zone_id = aws_route53_zone.pub.zone_id
  name    = "app.tf.example.com"
  type    = "A"
  alias {
    name                   = "web.elb.internal"
    zone_id                = "Z35SXDOTRQ7X7K"
    evaluate_target_health = false
  }
}
resource "aws_route53_record" "priv" {
  zone_id = aws_route53_zone.priv.zone_id
  name    = "db.tf.internal"
  type    = "A"
  ttl     = 30
  records = ["10.88.0.9"]
}
resource "aws_acm_certificate" "c" {
  domain_name       = "www.tf.example.com"
  validation_method = "DNS"
  tags              = { env = "test" }
}
resource "aws_route53_record" "validation" {
  for_each = {
    for dvo in aws_acm_certificate.c.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      record = dvo.resource_record_value
      type   = dvo.resource_record_type
    }
  }
  allow_overwrite = true
  name            = each.value.name
  records         = [each.value.record]
  ttl             = 60
  type            = each.value.type
  zone_id         = aws_route53_zone.pub.zone_id
}
resource "aws_acm_certificate_validation" "c" {
  certificate_arn         = aws_acm_certificate.c.arn
  validation_record_fqdns = [for r in aws_route53_record.validation : r.fqdn]
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(h.Environ(h.AccessKeyID, h.SecretKey, ""), "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("init", "-input=false", "-no-color"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if out, err := run("apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if out, err := run("plan", "-detailed-exitcode", "-input=false", "-no-color"); err != nil {
		t.Fatalf("plan after apply is not empty: %v\n%s", err, out)
	}
	if out, err := run("destroy", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
}
