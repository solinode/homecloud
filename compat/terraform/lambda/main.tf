# services: lambda, iam, sqs, logs
# A Python function packaged by the module from local source, with its execution role (managed and
# inline policies), a log group, environment variables, a published version and alias, a resource
# permission for EventBridge, and an SQS event source mapping.

data "aws_caller_identity" "current" {}

resource "aws_sqs_queue" "jobs" {
  name                       = "${var.name}-jobs"
  visibility_timeout_seconds = 60
}

module "function" {
  source  = "terraform-aws-modules/lambda/aws"
  version = "8.9.0"

  function_name = "${var.name}-worker"
  description   = "HomeCloud compatibility suite"
  handler       = "index.handler"
  runtime       = "python3.12"
  timeout       = 10
  memory_size   = 256
  publish       = true

  source_path = "${path.module}/src"

  environment_variables = {
    GREETING = "hello"
  }

  cloudwatch_logs_retention_in_days = 7

  attach_policy_statements = true
  policy_statements = {
    sqs = {
      effect    = "Allow"
      actions   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"]
      resources = [aws_sqs_queue.jobs.arn]
    }
  }

  allowed_triggers = {
    events = {
      principal  = "events.amazonaws.com"
      source_arn = "arn:aws:events:us-east-1:${data.aws_caller_identity.current.account_id}:rule/${var.name}-*"
    }
  }

  event_source_mapping = {
    sqs = {
      event_source_arn = aws_sqs_queue.jobs.arn
      batch_size       = 5
    }
  }

  tags = { suite = "compat" }
}

module "alias" {
  source  = "terraform-aws-modules/lambda/aws//modules/alias"
  version = "8.9.0"

  name             = "live"
  function_name    = module.function.lambda_function_name
  function_version = module.function.lambda_function_version
}

output "function_name" {
  value = module.function.lambda_function_name
}

output "queue_url" {
  value = aws_sqs_queue.jobs.url
}

output "log_group" {
  value = module.function.lambda_cloudwatch_log_group_name
}
