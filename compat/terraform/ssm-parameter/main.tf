# services: ssm, kms
# String, StringList and SecureString parameters (default and customer managed keys) in a hierarchy,
# with an allowed pattern, the advanced tier and tags.

resource "aws_kms_key" "params" {
  description             = "${var.name} parameters"
  deletion_window_in_days = 7
}

locals {
  parameters = {
    "/${var.name}/app/endpoint" = {
      value       = "https://api.example.test"
      description = "API endpoint"
    }
    "/${var.name}/app/zones" = {
      type   = "StringList"
      values = ["us-east-1a", "us-east-1b"]
    }
    "/${var.name}/app/port" = {
      value           = "8080"
      allowed_pattern = "^[0-9]+$"
    }
    "/${var.name}/app/password" = {
      value       = "hunter2-compat"
      secure_type = true
    }
    "/${var.name}/app/token" = {
      value       = "token-compat"
      secure_type = true
      key_id      = aws_kms_key.params.arn
    }
    "/${var.name}/app/large" = {
      value = "advanced tier parameter"
      tier  = "Advanced"
    }
  }
}

module "param" {
  source  = "terraform-aws-modules/ssm-parameter/aws"
  version = "2.1.2"

  for_each = local.parameters

  name            = each.key
  value           = try(each.value.value, null)
  values          = try(each.value.values, [])
  type            = try(each.value.type, null)
  secure_type     = try(each.value.secure_type, null)
  description     = try(each.value.description, null)
  tier            = try(each.value.tier, null)
  key_id          = try(each.value.key_id, null)
  allowed_pattern = try(each.value.allowed_pattern, null)

  tags = { suite = "compat" }
}

output "prefix" {
  value = "/${var.name}/app"
}
