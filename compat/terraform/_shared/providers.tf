# Shared by every scenario (each scenario links to this file).
#
# The stock AWS provider, unmodified. compat/run.sh exports AWS_ENDPOINT_URL (and the credentials
# from `homecloud aws-env`), which the provider uses for every service, so nothing here is
# HomeCloud specific except path-style S3 addressing.
terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.67.0"
    }
  }
}

provider "aws" {
  region = "us-east-1"

  # HomeCloud serves buckets by path, so bucket names need not resolve as host names.
  s3_use_path_style = true
}

variable "name" {
  description = "Prefix for resource names."
  type        = string
  default     = "compat"
}
