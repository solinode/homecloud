# services: secretsmanager, kms
# A secret with a JSON value and a resource policy, a secret whose password Secrets Manager generates
# (GetRandomPassword), and a secret encrypted with a customer managed KMS key.

data "aws_caller_identity" "current" {}

resource "aws_kms_key" "secrets" {
  description             = "${var.name} secrets"
  deletion_window_in_days = 7
}

module "app_config" {
  source  = "terraform-aws-modules/secrets-manager/aws"
  version = "2.2.0"

  name                    = "${var.name}/app/config"
  description             = "Application settings"
  recovery_window_in_days = 0

  secret_string = jsonencode({
    username = "app"
    endpoint = "db.internal"
  })

  create_policy       = true
  block_public_policy = true
  policy_statements = {
    read = {
      sid = "AccountRead"
      principals = [{
        type        = "AWS"
        identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
      }]
      actions   = ["secretsmanager:GetSecretValue"]
      resources = ["*"]
    }
  }

  tags = { suite = "compat" }
}

module "generated" {
  source  = "terraform-aws-modules/secrets-manager/aws"
  version = "2.2.0"

  name_prefix             = "${var.name}-generated-"
  recovery_window_in_days = 0

  create_random_password           = true
  random_password_length           = 40
  random_password_override_special = "!@#$%^&*()_+"
}

module "encrypted" {
  source  = "terraform-aws-modules/secrets-manager/aws"
  version = "2.2.0"

  name                    = "${var.name}/encrypted"
  recovery_window_in_days = 0
  kms_key_id              = aws_kms_key.secrets.arn
  secret_string           = "s3cr3t-value"
}

output "config_arn" {
  value = module.app_config.secret_arn
}

output "generated_arn" {
  value = module.generated.secret_arn
}

output "encrypted_arn" {
  value = module.encrypted.secret_arn
}
