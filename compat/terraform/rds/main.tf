# services: rds, ec2, secretsmanager
# PostgreSQL 16 in a DB subnet group made by the module, with a custom parameter group, a security
# group, backups and a master password managed in Secrets Manager.

data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "this" {
  cidr_block           = "10.30.0.0/16"
  enable_dns_hostnames = true
  tags                 = { Name = "${var.name}-rds" }
}

resource "aws_subnet" "db" {
  count             = 2
  vpc_id            = aws_vpc.this.id
  cidr_block        = cidrsubnet(aws_vpc.this.cidr_block, 8, count.index + 1)
  availability_zone = data.aws_availability_zones.available.names[count.index]
}

module "db_sg" {
  source  = "terraform-aws-modules/security-group/aws//modules/postgresql"
  version = "6.0.0"

  name   = "${var.name}-db"
  vpc_id = aws_vpc.this.id

  ingress_cidr_ipv4 = {
    vpc = aws_vpc.this.cidr_block
  }
}

module "db" {
  source  = "terraform-aws-modules/rds/aws"
  version = "7.2.2"

  identifier = "${var.name}-pg"

  engine               = "postgres"
  engine_version       = "16"
  family               = "postgres16"
  major_engine_version = "16"
  instance_class       = "db.t3.micro"
  allocated_storage    = 20

  db_name  = "app"
  username = "appadmin"
  port     = 5432

  manage_master_user_password = true

  create_db_subnet_group = true
  subnet_ids             = aws_subnet.db[*].id
  vpc_security_group_ids = [module.db_sg.id]

  maintenance_window      = "Mon:00:00-Mon:03:00"
  backup_window           = "03:00-06:00"
  backup_retention_period = 1
  skip_final_snapshot     = true
  deletion_protection     = false

  parameters = [
    { name = "autovacuum", value = 1 },
    { name = "client_encoding", value = "utf8" },
  ]

  tags = { suite = "compat" }
}

output "db_identifier" {
  value = module.db.db_instance_identifier
}

output "secret_arn" {
  value = module.db.db_instance_master_user_secret_arn
}
