# services: dynamodb
# An on-demand table with a sort key, a global and a local secondary index, TTL, a stream,
# point-in-time recovery and server-side encryption, plus a provisioned table.

module "table" {
  source  = "terraform-aws-modules/dynamodb-table/aws"
  version = "5.5.2"

  name                        = "${var.name}-orders"
  hash_key                    = "pk"
  range_key                   = "sk"
  billing_mode                = "PAY_PER_REQUEST"
  deletion_protection_enabled = false

  attributes = [
    { name = "pk", type = "S" },
    { name = "sk", type = "S" },
    { name = "status", type = "S" },
    { name = "created", type = "N" },
  ]

  global_secondary_indexes = [{
    name            = "by-status"
    hash_key        = "status"
    range_key       = "created"
    projection_type = "ALL"
  }]

  local_secondary_indexes = [{
    name               = "by-created"
    range_key          = "created"
    projection_type    = "INCLUDE"
    non_key_attributes = ["status"]
  }]

  ttl_enabled        = true
  ttl_attribute_name = "expires_at"

  stream_enabled   = true
  stream_view_type = "NEW_AND_OLD_IMAGES"

  point_in_time_recovery_enabled = true
  server_side_encryption_enabled = true

  tags = { suite = "compat" }
}

module "provisioned" {
  source  = "terraform-aws-modules/dynamodb-table/aws"
  version = "5.5.2"

  name           = "${var.name}-sessions"
  hash_key       = "id"
  billing_mode   = "PROVISIONED"
  read_capacity  = 5
  write_capacity = 5

  attributes = [{ name = "id", type = "S" }]
}

output "table" {
  value = module.table.dynamodb_table_id
}
