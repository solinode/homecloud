# services: ecs, elbv2, ec2, iam, logs
# An ECS cluster with a Fargate nginx service (task and execution roles, a log group and a security
# group made by the module) registered in an application load balancer's target group.

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, 2)
}

resource "aws_vpc" "this" {
  cidr_block           = "10.40.0.0/16"
  enable_dns_hostnames = true
  tags                 = { Name = "${var.name}-ecs" }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
}

resource "aws_subnet" "public" {
  count                   = 2
  vpc_id                  = aws_vpc.this.id
  cidr_block              = cidrsubnet(aws_vpc.this.cidr_block, 8, count.index + 1)
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = true
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
}

resource "aws_route_table_association" "public" {
  count          = 2
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

module "alb" {
  source  = "terraform-aws-modules/alb/aws"
  version = "10.5.1"

  name    = "${var.name}-web"
  vpc_id  = aws_vpc.this.id
  subnets = aws_subnet.public[*].id

  enable_deletion_protection = false

  security_group_ingress_rules = {
    http = {
      from_port   = 80
      to_port     = 80
      ip_protocol = "tcp"
      cidr_ipv4   = "0.0.0.0/0"
    }
  }
  security_group_egress_rules = {
    all = {
      ip_protocol = "-1"
      cidr_ipv4   = aws_vpc.this.cidr_block
    }
  }

  listeners = {
    http = {
      port     = 80
      protocol = "HTTP"
      forward = {
        target_group_key = "web"
      }
    }
  }

  target_groups = {
    web = {
      backend_protocol     = "HTTP"
      backend_port         = 80
      target_type          = "ip"
      deregistration_delay = 5
      health_check = {
        enabled             = true
        path                = "/"
        matcher             = "200"
        interval            = 10
        timeout             = 5
        healthy_threshold   = 2
        unhealthy_threshold = 2
      }
      create_attachment = false
    }
  }

  tags = { suite = "compat" }
}

module "ecs" {
  source  = "terraform-aws-modules/ecs/aws"
  version = "7.6.1"

  cluster_name = "${var.name}-cluster"

  default_capacity_provider_strategy = {
    FARGATE = {
      weight = 100
    }
  }

  services = {
    web = {
      cpu           = 256
      memory        = 512
      desired_count = 1

      container_definitions = {
        nginx = {
          essential              = true
          image                  = "nginx:alpine"
          readonlyRootFilesystem = false
          portMappings = [{
            name          = "http"
            containerPort = 80
            protocol      = "tcp"
          }]
        }
      }

      load_balancer = {
        service = {
          target_group_arn = module.alb.target_groups["web"].arn
          container_name   = "nginx"
          container_port   = 80
        }
      }

      subnet_ids       = aws_subnet.public[*].id
      assign_public_ip = true

      security_group_ingress_rules = {
        alb = {
          from_port                    = 80
          ip_protocol                  = "tcp"
          referenced_security_group_id = module.alb.security_group_id
        }
      }
      security_group_egress_rules = {
        all = {
          ip_protocol = "-1"
          cidr_ipv4   = "0.0.0.0/0"
        }
      }
    }
  }

  tags = { suite = "compat" }
}

output "alb_name" {
  value = "${var.name}-web"
}

output "cluster" {
  value = module.ecs.cluster_name
}
