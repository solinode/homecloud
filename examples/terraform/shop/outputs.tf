output "alb_url" {
  value = "http://${aws_lb.web.dns_name}"
}

output "alb_dns_name" {
  value = aws_lb.web.dns_name
}

output "api_url" {
  value = "${aws_apigatewayv2_api.api.api_endpoint}/hello"
}

output "queue_url" {
  value = aws_sqs_queue.orders.id
}

output "topic_arn" {
  value = aws_sns_topic.orders.arn
}

output "bucket" {
  value = aws_s3_bucket.assets.bucket
}

output "table" {
  value = aws_dynamodb_table.orders.name
}

output "db_endpoint" {
  value = aws_db_instance.main.address
}

output "db_port" {
  value = aws_db_instance.main.port
}

output "db_secret_arn" {
  value = aws_db_instance.main.master_user_secret[0].secret_arn
}

output "web_log_group" {
  value = aws_cloudwatch_log_group.web.name
}

output "consumer_log_group" {
  value = aws_cloudwatch_log_group.consumer.name
}

output "private_record" {
  value = aws_route53_record.app.fqdn
}
