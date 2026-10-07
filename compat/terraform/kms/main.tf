# services: kms, iam
# A symmetric key with rotation, a key policy built by the module (owners, administrators, users and a
# service statement with a condition), aliases and a grant; plus an asymmetric signing key.

data "aws_caller_identity" "current" {}

resource "aws_iam_role" "app" {
  name = "${var.name}-kms-app"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { Service = "lambda.amazonaws.com" }
    }]
  })
}

module "key" {
  source  = "terraform-aws-modules/kms/aws"
  version = "4.2.2"

  description             = "HomeCloud compatibility suite"
  deletion_window_in_days = 7
  enable_key_rotation     = true

  key_owners         = [data.aws_caller_identity.current.arn]
  key_administrators = [data.aws_caller_identity.current.arn]
  key_users          = [aws_iam_role.app.arn]

  key_statements = [{
    sid       = "CloudWatchLogs"
    actions   = ["kms:Encrypt*", "kms:Decrypt*", "kms:GenerateDataKey*", "kms:Describe*"]
    resources = ["*"]
    principals = [{
      type        = "Service"
      identifiers = ["logs.us-east-1.amazonaws.com"]
    }]
    condition = [{
      test     = "ArnLike"
      variable = "kms:EncryptionContext:aws:logs:arn"
      values   = ["arn:aws:logs:us-east-1:${data.aws_caller_identity.current.account_id}:log-group:*"]
    }]
  }]

  aliases = ["${var.name}/app", "${var.name}/data"]

  grants = {
    app = {
      grantee_principal = aws_iam_role.app.arn
      operations        = ["Encrypt", "Decrypt", "GenerateDataKey"]
      constraints = [{
        encryption_context_equals = { team = "compat" }
      }]
    }
  }

  tags = { suite = "compat" }
}

module "signing" {
  source  = "terraform-aws-modules/kms/aws"
  version = "4.2.2"

  description              = "Signing key"
  deletion_window_in_days  = 7
  key_usage                = "SIGN_VERIFY"
  customer_master_key_spec = "ECC_NIST_P256"
  enable_key_rotation      = false

  aliases = ["${var.name}/signing"]
}

output "alias" {
  value = "alias/${var.name}/app"
}
