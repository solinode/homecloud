terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.4"
    }
  }
}

variable "endpoint" {
  description = "HomeCloud API endpoint. Use a host name such as localhost, not an IP address: the S3 client puts the bucket name in the host."
  type        = string
  default     = "http://localhost:8080"
}

variable "name" {
  description = "Prefix for resource names."
  type        = string
  default     = "shop"
}

variable "domain" {
  description = "Public DNS zone the ALB certificate is issued for."
  type        = string
  default     = "shop.example.test"
}

# The credentials come from the standard environment (AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY,
# see `homecloud aws-env`). Every service the app uses is pointed at HomeCloud.
provider "aws" {
  region = "us-east-1"

  # HomeCloud serves the buckets by path, so the bucket name does not have to resolve as a host name.
  s3_use_path_style = true

  endpoints {
    acm            = var.endpoint
    apigatewayv2   = var.endpoint
    cloudwatchlogs = var.endpoint
    dynamodb       = var.endpoint
    ec2            = var.endpoint
    ecs            = var.endpoint
    elbv2          = var.endpoint
    iam            = var.endpoint
    lambda         = var.endpoint
    rds            = var.endpoint
    route53        = var.endpoint
    s3             = var.endpoint
    secretsmanager = var.endpoint
    sns            = var.endpoint
    sqs            = var.endpoint
    sts            = var.endpoint
  }
}

data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_caller_identity" "current" {}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)
  tags = {
    app = var.name
  }
}
