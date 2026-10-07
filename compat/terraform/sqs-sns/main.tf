# services: sqs, sns
# An SNS topic fanned out to an SQS queue (with a dead-letter queue and a queue policy allowing the
# topic), with a filter policy and raw delivery, plus a FIFO topic feeding a FIFO queue.

module "queue" {
  source  = "terraform-aws-modules/sqs/aws"
  version = "5.2.2"

  name                       = "${var.name}-orders"
  visibility_timeout_seconds = 30
  message_retention_seconds  = 86400

  create_dlq = true
  redrive_policy = {
    maxReceiveCount = 3
  }

  create_queue_policy = true
  queue_policy_statements = {
    sns = {
      sid     = "SNSPublish"
      actions = ["sqs:SendMessage"]
      principals = [{
        type        = "Service"
        identifiers = ["sns.amazonaws.com"]
      }]
      conditions = [{
        test     = "ArnEquals"
        variable = "aws:SourceArn"
        values   = [module.topic.topic_arn]
      }]
    }
  }

  tags = { suite = "compat" }
}

module "topic" {
  source  = "terraform-aws-modules/sns/aws"
  version = "7.2.0"

  name = "${var.name}-orders"

  subscriptions = {
    sqs = {
      protocol             = "sqs"
      endpoint             = module.queue.queue_arn
      raw_message_delivery = true
      filter_policy        = jsonencode({ kind = ["order"] })
    }
  }

  tags = { suite = "compat" }
}

module "fifo_queue" {
  source  = "terraform-aws-modules/sqs/aws"
  version = "5.2.2"

  name                        = "${var.name}-events"
  fifo_queue                  = true
  content_based_deduplication = true

  create_queue_policy = true
  queue_policy_statements = {
    sns = {
      sid     = "SNSPublish"
      actions = ["sqs:SendMessage"]
      principals = [{
        type        = "Service"
        identifiers = ["sns.amazonaws.com"]
      }]
      conditions = [{
        test     = "ArnEquals"
        variable = "aws:SourceArn"
        values   = [module.fifo_topic.topic_arn]
      }]
    }
  }
}

module "fifo_topic" {
  source  = "terraform-aws-modules/sns/aws"
  version = "7.2.0"

  name                        = "${var.name}-events"
  fifo_topic                  = true
  content_based_deduplication = true

  subscriptions = {
    sqs = {
      protocol = "sqs"
      endpoint = module.fifo_queue.queue_arn
    }
  }
}

output "topic_arn" {
  value = module.topic.topic_arn
}

output "queue_url" {
  value = module.queue.queue_url
}
