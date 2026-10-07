# services: route53, ec2
# A public zone with A, AAAA, CNAME, MX, TXT, SRV and CAA records (single and multi-value), and a
# private zone associated with a VPC.

resource "aws_vpc" "this" {
  cidr_block           = "10.60.0.0/16"
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags                 = { Name = "${var.name}-dns" }
}

module "public" {
  source  = "terraform-aws-modules/route53/aws"
  version = "6.5.1"

  name          = "${var.name}.example.test"
  comment       = "Public zone"
  force_destroy = true

  records = {
    apex = {
      full_name = "${var.name}.example.test"
      type      = "A"
      ttl       = 300
      records   = ["203.0.113.10"]
    }
    www = {
      type    = "CNAME"
      ttl     = 300
      records = ["${var.name}.example.test"]
    }
    v6 = {
      name    = "ipv6"
      type    = "AAAA"
      ttl     = 300
      records = ["2001:db8::10"]
    }
    mail = {
      full_name = "${var.name}.example.test"
      type      = "MX"
      ttl       = 3600
      records   = ["10 mx1.example.test", "20 mx2.example.test"]
    }
    spf = {
      full_name = "${var.name}.example.test"
      type      = "TXT"
      ttl       = 300
      records   = ["v=spf1 -all", "compat=yes"]
    }
    sip = {
      name    = "_sip._tcp"
      type    = "SRV"
      ttl     = 300
      records = ["10 5 5060 sip.example.test"]
    }
    caa = {
      full_name = "${var.name}.example.test"
      type      = "CAA"
      ttl       = 300
      records   = ["0 issue \"letsencrypt.org\""]
    }
  }

  tags = { suite = "compat" }
}

module "private" {
  source  = "terraform-aws-modules/route53/aws"
  version = "6.5.1"

  name          = "internal.${var.name}.test"
  comment       = "Private zone"
  force_destroy = true

  vpc = {
    main = {
      vpc_id = aws_vpc.this.id
    }
  }

  records = {
    db = {
      type    = "A"
      ttl     = 60
      records = ["10.60.0.10"]
    }
  }

  tags = { suite = "compat" }
}

output "zone_id" {
  value = module.public.id
}

output "zone_name" {
  value = "${var.name}.example.test"
}
