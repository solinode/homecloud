# services: apigateway, lambda, cognito, logs
# An HTTP API with CORS, Lambda proxy routes (payload 2.0 and 1.0), a JWT authorizer backed by a
# Cognito user pool, an HTTP proxy route, the $default stage with access logs, and the Lambda
# permissions the module creates.

module "function" {
  source  = "terraform-aws-modules/lambda/aws"
  version = "8.9.0"

  function_name = "${var.name}-api"
  handler       = "index.handler"
  runtime       = "python3.12"
  source_path   = "${path.module}/src"
  publish       = true

  allowed_triggers = {
    api = {
      service    = "apigateway"
      source_arn = "${module.api.api_execution_arn}/*/*"
    }
  }
}

resource "aws_cognito_user_pool" "users" {
  name = "${var.name}-users"
}

resource "aws_cognito_user_pool_client" "web" {
  name         = "web"
  user_pool_id = aws_cognito_user_pool.users.id
}

module "api" {
  source  = "terraform-aws-modules/apigateway-v2/aws"
  version = "6.1.1"

  name          = "${var.name}-http"
  description   = "HomeCloud compatibility suite"
  protocol_type = "HTTP"

  cors_configuration = {
    allow_headers = ["content-type", "authorization"]
    allow_methods = ["GET", "POST"]
    allow_origins = ["https://app.example.test"]
  }

  create_domain_name = false

  authorizers = {
    cognito = {
      authorizer_type  = "JWT"
      identity_sources = ["$request.header.Authorization"]
      name             = "cognito"
      jwt_configuration = {
        audience = [aws_cognito_user_pool_client.web.id]
        issuer   = "https://${aws_cognito_user_pool.users.endpoint}"
      }
    }
  }

  routes = {
    "GET /hello" = {
      integration = {
        uri                    = module.function.lambda_function_arn
        payload_format_version = "2.0"
        timeout_milliseconds   = 10000
      }
    }
    "POST /items/{id}" = {
      integration = {
        uri                    = module.function.lambda_function_arn
        payload_format_version = "1.0"
      }
    }
    "GET /private" = {
      authorization_type = "JWT"
      authorizer_key     = "cognito"
      integration = {
        uri                    = module.function.lambda_function_arn
        payload_format_version = "2.0"
      }
    }
    "GET /ext/{proxy+}" = {
      integration = {
        type   = "HTTP_PROXY"
        uri    = "https://example.com/{proxy}"
        method = "GET"
      }
    }
  }

  stage_access_log_settings = {
    create_log_group            = true
    log_group_retention_in_days = 7
    format = jsonencode({
      requestId = "$context.requestId"
      status    = "$context.status"
      path      = "$context.path"
    })
  }

  stage_default_route_settings = {
    detailed_metrics_enabled = true
    throttling_burst_limit   = 100
    throttling_rate_limit    = 100
  }

  tags = { suite = "compat" }
}

output "api_endpoint" {
  value = module.api.api_endpoint
}
