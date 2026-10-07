# services: acm, route53
# A certificate with subject alternative names, validated by DNS records the module writes into a
# Route 53 zone, waiting for validation.

module "zone" {
  source  = "terraform-aws-modules/route53/aws"
  version = "6.5.1"

  name          = "${var.name}-tls.example.test"
  force_destroy = true
}

module "acm" {
  source  = "terraform-aws-modules/acm/aws"
  version = "6.3.1"

  providers = {
    aws.acm = aws
    aws.dns = aws
  }

  domain_name = "${var.name}-tls.example.test"
  zone_id     = module.zone.id

  subject_alternative_names = [
    "*.${var.name}-tls.example.test",
    "api.${var.name}-tls.example.test",
  ]

  validation_method   = "DNS"
  wait_for_validation = true

  tags = { suite = "compat" }
}

output "certificate_arn" {
  value = module.acm.acm_certificate_arn
}
