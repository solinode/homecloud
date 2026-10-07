# services: events, scheduler, sqs, iam
# A custom event bus with pattern rules routed to SQS (plain, with an input transformer, and with a
# dead-letter queue and retry policy), a scheduled rule on the default bus, an EventBridge Scheduler
# group and schedule, and the IAM role the module makes for its targets.

resource "aws_sqs_queue" "orders" {
  name = "${var.name}-eb-orders"
}

resource "aws_sqs_queue" "dlq" {
  name = "${var.name}-eb-dlq"
}

resource "aws_sqs_queue_policy" "orders" {
  queue_url = aws_sqs_queue.orders.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.orders.arn
    }]
  })
}

module "bus" {
  source  = "terraform-aws-modules/eventbridge/aws"
  version = "4.3.2"

  bus_name = "${var.name}-orders"

  attach_sqs_policy = true
  sqs_target_arns   = [aws_sqs_queue.orders.arn, aws_sqs_queue.dlq.arn]

  rules = {
    created = {
      description   = "New orders"
      event_pattern = jsonencode({ source = ["compat.orders"], "detail-type" = ["OrderCreated"] })
    }
    large = {
      description = "Orders over 100"
      event_pattern = jsonencode({
        source = ["compat.orders"]
        detail = { total = [{ numeric = [">", 100] }] }
      })
      state = "ENABLED"
    }
  }

  targets = {
    created = [{
      name = "to-queue"
      arn  = aws_sqs_queue.orders.arn
    }]
    large = [{
      name            = "to-queue-transformed"
      arn             = aws_sqs_queue.orders.arn
      dead_letter_arn = aws_sqs_queue.dlq.arn
      retry_policy = {
        maximum_retry_attempts       = 3
        maximum_event_age_in_seconds = 300
      }
      input_transformer = {
        input_paths    = { id = "$.detail.id" }
        input_template = "{\"large_order\": <id>}"
      }
    }]
  }

  tags = { suite = "compat" }
}

module "cron" {
  source  = "terraform-aws-modules/eventbridge/aws"
  version = "4.3.2"

  create_bus  = false
  create_role = false

  rules = {
    nightly = {
      description         = "Nightly job"
      schedule_expression = "cron(0 3 * * ? *)"
    }
  }
  targets = {
    nightly = [{
      name = "nightly-to-queue"
      arn  = aws_sqs_queue.orders.arn
    }]
  }
}

module "scheduler" {
  source  = "terraform-aws-modules/eventbridge/aws"
  version = "4.3.2"

  create_bus = false
  role_name  = "${var.name}-scheduler"

  attach_sqs_policy = true
  sqs_target_arns   = [aws_sqs_queue.orders.arn]

  schedule_groups = {
    jobs = {
      name = "${var.name}-jobs"
    }
  }

  schedules = {
    hourly = {
      group_name          = "jobs"
      description         = "Hourly report"
      schedule_expression = "rate(1 hour)"
      timezone            = "Europe/London"
      arn                 = aws_sqs_queue.orders.arn
      input               = jsonencode({ job = "report" })
    }
  }
}

output "bus_name" {
  value = module.bus.eventbridge_bus_name
}

output "queue_url" {
  value = aws_sqs_queue.orders.url
}
