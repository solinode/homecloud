#!/usr/bin/env bash
# The instance is available and its master password secret holds the user name.
set -euo pipefail
id=$($TF output -raw db_identifier)
[ "$(aws rds describe-db-instances --db-instance-identifier "$id" --query 'DBInstances[0].DBInstanceStatus' --output text)" = available ]
aws secretsmanager get-secret-value --secret-id "$($TF output -raw secret_arn)" --query SecretString --output text | grep -q appadmin
echo "rds ok"
