# services: ec2
# Security groups with the module's per-rule resources (aws_vpc_security_group_ingress_rule and
# egress rules): CIDR, self and group references, a port range, IPv6, exclusive rule management, and a
# preset submodule (http-80).

resource "aws_vpc" "this" {
  cidr_block = "10.50.0.0/16"
  tags       = { Name = "${var.name}-sg" }
}

module "web" {
  source  = "terraform-aws-modules/security-group/aws//modules/http-80"
  version = "6.0.0"

  name        = "${var.name}-web"
  description = "Web servers"
  vpc_id      = aws_vpc.this.id

  ingress_cidr_ipv4 = {
    any = "0.0.0.0/0"
  }

  egress_rules = {
    all = {
      ip_protocol = "-1"
      cidr_ipv4   = "0.0.0.0/0"
    }
  }
}

module "app" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "6.0.0"

  name        = "${var.name}-app"
  description = "Application servers"
  vpc_id      = aws_vpc.this.id

  ingress_rules = {
    ssh = {
      from_port   = 22
      to_port     = 22
      ip_protocol = "tcp"
      cidr_ipv4   = "10.50.0.0/24"
      description = "SSH from the admin subnet"
    }
    app-range = {
      from_port   = 8080
      to_port     = 8090
      ip_protocol = "tcp"
      cidr_ipv4   = "10.50.0.0/16"
      description = "App ports"
    }
    app-v6 = {
      from_port   = 8080
      ip_protocol = "tcp"
      cidr_ipv6   = "2001:db8::/64"
    }
    self = {
      ip_protocol                  = "-1"
      referenced_security_group_id = "self"
    }
    from-web = {
      from_port                    = 8080
      ip_protocol                  = "tcp"
      referenced_security_group_id = module.web.id
      tags                         = { tier = "web" }
    }
  }

  egress_rules = {
    https = {
      from_port   = 443
      ip_protocol = "tcp"
      cidr_ipv4   = "0.0.0.0/0"
    }
    postgres = {
      from_port   = 5432
      ip_protocol = "tcp"
      cidr_ipv4   = "10.50.0.0/16"
    }
  }

  tags = { suite = "compat" }
}

output "app_sg" {
  value = module.app.id
}
