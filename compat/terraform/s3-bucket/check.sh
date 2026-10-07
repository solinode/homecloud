#!/usr/bin/env bash
# Versioning, encryption and lifecycle are reported back as configured.
set -euo pipefail
b=$($TF output -raw bucket)
[ "$(aws s3api get-bucket-versioning --bucket "$b" --query Status --output text)" = Enabled ]
aws s3api get-bucket-encryption --bucket "$b" | grep -q AES256
[ "$(aws s3api get-bucket-lifecycle-configuration --bucket "$b" --query 'length(Rules)')" = 2 ]
echo "s3 ok"
