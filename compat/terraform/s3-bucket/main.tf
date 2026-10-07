# services: s3
# A bucket with versioning, default encryption, lifecycle rules, ownership controls, a public access
# block, CORS, tags and a bucket policy (the module's own TLS-only statements plus a custom one).

data "aws_caller_identity" "current" {}

data "aws_iam_policy_document" "bucket" {
  statement {
    sid       = "AccountRead"
    actions   = ["s3:GetObject"]
    resources = ["${module.bucket.s3_bucket_arn}/*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
  }
}

module "bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "5.16.1"

  bucket        = "${var.name}-bucket-${data.aws_caller_identity.current.account_id}"
  force_destroy = true

  control_object_ownership = true
  object_ownership         = "BucketOwnerEnforced"

  versioning = {
    enabled = true
  }

  server_side_encryption_configuration = {
    rule = {
      apply_server_side_encryption_by_default = {
        sse_algorithm = "AES256"
      }
    }
  }

  lifecycle_rule = [
    {
      id      = "expire-tmp"
      enabled = true
      filter = {
        prefix = "tmp/"
      }
      expiration = {
        days = 7
      }
      noncurrent_version_expiration = {
        noncurrent_days = 30
      }
    },
    {
      id                                     = "abort-uploads"
      enabled                                = true
      abort_incomplete_multipart_upload_days = 3
    },
  ]

  cors_rule = [
    {
      allowed_methods = ["GET", "PUT"]
      allowed_origins = ["https://app.example.test"]
      allowed_headers = ["*"]
      max_age_seconds = 3000
    }
  ]

  attach_policy                         = true
  policy                                = data.aws_iam_policy_document.bucket.json
  attach_deny_insecure_transport_policy = true
  attach_require_latest_tls_policy      = true

  tags = { suite = "compat" }
}

output "bucket" {
  value = module.bucket.s3_bucket_id
}
