package s3

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTerraform applies, re-plans and destroys S3 resources with Terraform or
// OpenTofu and the hashicorp/aws provider. Opt-in (HC_TEST_TERRAFORM=1) since
// it downloads the provider; set TF_PLUGIN_CACHE_DIR to reuse it.
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
	h, _ := newHarness(t)
	b := bucketName()
	dir := t.TempDir()
	cfg := `
terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}
provider "aws" {
  region                  = "us-east-1"
  s3_use_path_style       = true
  skip_metadata_api_check = true
}
resource "aws_s3_bucket" "b" {
  bucket = "` + b + `"
  tags   = { team = "core" }
}
resource "aws_s3_bucket_versioning" "v" {
  bucket = aws_s3_bucket.b.id
  versioning_configuration { status = "Enabled" }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "e" {
  bucket = aws_s3_bucket.b.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}
resource "aws_s3_bucket_public_access_block" "p" {
  bucket                  = aws_s3_bucket.b.id
  block_public_acls       = true
  block_public_policy     = false
  ignore_public_acls      = true
  restrict_public_buckets = false
}
resource "aws_s3_bucket_cors_configuration" "c" {
  bucket = aws_s3_bucket.b.id
  cors_rule {
    allowed_methods = ["GET"]
    allowed_origins = ["*"]
  }
}
resource "aws_s3_bucket_lifecycle_configuration" "l" {
  bucket = aws_s3_bucket.b.id
  rule {
    id     = "tmp"
    status = "Enabled"
    filter { prefix = "tmp/" }
    expiration { days = 3 }
  }
}
resource "aws_s3_bucket_website_configuration" "w" {
  bucket = aws_s3_bucket.b.id
  index_document { suffix = "index.html" }
}
resource "aws_s3_bucket_policy" "pol" {
  bucket = aws_s3_bucket.b.id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = "*", Action = "s3:GetObject", Resource = "${aws_s3_bucket.b.arn}/*" }]
  })
}
resource "aws_s3_object" "o" {
  bucket       = aws_s3_bucket.b.id
  key          = "index.html"
  content      = "<h1>hello</h1>"
  content_type = "text/html"
  depends_on   = [aws_s3_bucket_versioning.v]
}
`
	_ = os.WriteFile(filepath.Join(dir, "main.tf"), []byte(cfg), 0o600)
	// S3 Control (bucket tags) prefixes the host with the account ID, which
	// needs a host name rather than an IP address.
	endpoint := strings.Replace(h.URL, "127.0.0.1", "localhost", 1)
	env := append(h.Environ(h.AccessKeyID, h.SecretKey, ""), "AWS_ENDPOINT_URL="+endpoint, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
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
	if code, _ := exec.Command("curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", h.URL+"/"+b+"/index.html").Output(); string(code) != "200" {
		t.Fatalf("anonymous GET of the public object: %s", code)
	}
	if out, err := run("destroy", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("destroy: %v\n%s", err, out)
	}
}
