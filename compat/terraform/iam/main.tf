# services: iam, sts
# A customer managed policy, a role (service trust, AWS managed and customer managed policies, an
# inline policy, an instance profile), users with an access key and an inline policy, and a group with
# members and policies.

data "aws_caller_identity" "current" {}

module "policy" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-policy"
  version = "6.8.2"

  name        = "${var.name}-read-logs"
  path        = "/compat/"
  description = "Read CloudWatch Logs"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["logs:Describe*", "logs:Get*", "logs:FilterLogEvents"]
      Resource = "*"
    }]
  })

  tags = { suite = "compat" }
}

module "role" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role"
  version = "6.8.2"

  name                 = "${var.name}-app"
  description          = "Application role"
  max_session_duration = 7200

  trust_policy_permissions = {
    Services = {
      actions = ["sts:AssumeRole"]
      principals = [{
        type        = "Service"
        identifiers = ["ec2.amazonaws.com", "lambda.amazonaws.com"]
      }]
    }
    Account = {
      actions = ["sts:AssumeRole", "sts:TagSession"]
      principals = [{
        type        = "AWS"
        identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
      }]
    }
  }

  policies = {
    ReadOnly = "arn:aws:iam::aws:policy/ReadOnlyAccess"
    Logs     = module.policy.arn
  }

  create_inline_policy = true
  inline_policy_permissions = {
    Queue = {
      actions   = ["sqs:SendMessage"]
      resources = ["arn:aws:sqs:us-east-1:${data.aws_caller_identity.current.account_id}:${var.name}-*"]
    }
  }

  create_instance_profile = true

  tags = { suite = "compat" }
}

module "deployer" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-user"
  version = "6.8.2"

  name                 = "${var.name}-deployer"
  force_destroy        = true
  create_login_profile = false
  create_access_key    = true

  create_inline_policy = true
  inline_policy_permissions = {
    AssumeApp = {
      actions   = ["sts:AssumeRole"]
      resources = [module.role.arn]
    }
  }

  tags = { suite = "compat" }
}

module "auditor" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-user"
  version = "6.8.2"

  name                 = "${var.name}-auditor"
  force_destroy        = true
  create_login_profile = false
  create_access_key    = false
}

module "group" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-group"
  version = "6.8.2"

  name  = "${var.name}-ops"
  users = [module.deployer.name, module.auditor.name]

  permissions = {
    AssumeApp = {
      actions   = ["sts:AssumeRole"]
      resources = [module.role.arn]
    }
  }

  policies = {
    Logs = module.policy.arn
  }

  tags = { suite = "compat" }
}

output "role_arn" {
  value = module.role.arn
}

output "access_key_id" {
  value = module.deployer.access_key_id
}

output "secret_access_key" {
  value     = module.deployer.access_key_secret
  sensitive = true
}
