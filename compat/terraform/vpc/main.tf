# services: ec2
# A VPC with public, private and database subnets in two zones, an internet gateway, per-tier route
# tables, one NAT gateway for the private subnets, and the module's default security group, network
# ACL and route table management.

data "aws_availability_zones" "available" {
  state = "available"
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "6.7.3"

  name = "${var.name}-vpc"
  cidr = "10.20.0.0/16"
  azs  = slice(data.aws_availability_zones.available.names, 0, 2)

  public_subnets   = ["10.20.1.0/24", "10.20.2.0/24"]
  private_subnets  = ["10.20.11.0/24", "10.20.12.0/24"]
  database_subnets = ["10.20.21.0/24", "10.20.22.0/24"]

  create_database_subnet_group = false
  enable_dns_hostnames         = true
  enable_dns_support           = true

  enable_nat_gateway = true
  single_nat_gateway = true

  public_subnet_tags  = { tier = "public" }
  private_subnet_tags = { tier = "private" }
  tags                = { suite = "compat" }
}

output "vpc_id" {
  value = module.vpc.vpc_id
}

output "natgw_ids" {
  value = module.vpc.natgw_ids
}

output "private_route_table_ids" {
  value = module.vpc.private_route_table_ids
}
