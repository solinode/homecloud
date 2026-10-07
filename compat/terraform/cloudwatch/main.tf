# services: cloudwatch, logs, sns
# Log groups and a log stream, a metric filter feeding a metric alarm that notifies SNS, an alarm over
# several dimensions, a metric math alarm, a composite alarm, a dashboard and a Logs Insights query.

resource "aws_sns_topic" "alerts" {
  name = "${var.name}-alerts"
}

module "log_group" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/log-group"
  version = "5.7.3"

  name              = "/${var.name}/app"
  retention_in_days = 14
  tags              = { suite = "compat" }
}

module "log_stream" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/log-stream"
  version = "5.7.3"

  name           = "web-1"
  log_group_name = module.log_group.cloudwatch_log_group_name
}

module "error_filter" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/log-metric-filter"
  version = "5.7.3"

  name           = "${var.name}-errors"
  log_group_name = module.log_group.cloudwatch_log_group_name
  pattern        = "ERROR"

  metric_transformation_namespace = "Compat/App"
  metric_transformation_name      = "Errors"
  metric_transformation_value     = "1"
}

module "error_alarm" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "5.7.3"

  alarm_name          = "${var.name}-errors"
  alarm_description   = "Too many errors in the app log"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  threshold           = 1
  period              = 60
  unit                = "Count"
  namespace           = "Compat/App"
  metric_name         = "Errors"
  statistic           = "Sum"
  treat_missing_data  = "notBreaching"

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

module "queue_alarms" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarms-by-multiple-dimensions"
  version = "5.7.3"

  alarm_name          = "${var.name}-queue-depth-"
  alarm_description   = "Queue backlog"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 2
  threshold           = 100
  period              = 300
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  statistic           = "Maximum"

  dimensions = {
    orders = { QueueName = "${var.name}-orders" }
    jobs   = { QueueName = "${var.name}-jobs" }
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
}

module "error_rate" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/metric-alarm"
  version = "5.7.3"

  alarm_name          = "${var.name}-error-rate"
  alarm_description   = "Error rate over 5%"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  threshold           = 5

  metric_query = [
    {
      id          = "rate"
      expression  = "100 * errors / MAX([errors, requests])"
      label       = "Error rate"
      return_data = true
    },
    {
      id = "errors"
      metric = [{
        namespace   = "Compat/App"
        metric_name = "Errors"
        period      = 60
        stat        = "Sum"
      }]
    },
    {
      id = "requests"
      metric = [{
        namespace   = "Compat/App"
        metric_name = "Requests"
        period      = 60
        stat        = "Sum"
      }]
    },
  ]
}

module "composite" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/composite-alarm"
  version = "5.7.3"

  alarm_name        = "${var.name}-app-unhealthy"
  alarm_description = "Errors and a high error rate"
  alarm_rule        = "ALARM(${module.error_alarm.cloudwatch_metric_alarm_id}) AND ALARM(${module.error_rate.cloudwatch_metric_alarm_id})"
  alarm_actions     = [aws_sns_topic.alerts.arn]
}

module "query" {
  source  = "terraform-aws-modules/cloudwatch/aws//modules/query-definition"
  version = "5.7.3"

  name            = "${var.name}/errors"
  log_group_names = [module.log_group.cloudwatch_log_group_name]
  query_string    = "fields @timestamp, @message | filter @message like /ERROR/ | sort @timestamp desc | limit 20"
}

resource "aws_cloudwatch_dashboard" "app" {
  dashboard_name = "${var.name}-app"
  dashboard_body = jsonencode({
    widgets = [{
      type   = "metric"
      x      = 0
      y      = 0
      width  = 12
      height = 6
      properties = {
        metrics = [["Compat/App", "Errors"]]
        period  = 60
        stat    = "Sum"
        region  = "us-east-1"
        title   = "Errors"
      }
    }]
  })
}

output "log_group" {
  value = module.log_group.cloudwatch_log_group_name
}

output "alarm" {
  value = module.error_alarm.cloudwatch_metric_alarm_id
}
