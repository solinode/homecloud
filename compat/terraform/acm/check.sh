#!/usr/bin/env bash
# The certificate is issued and covers every name.
set -euo pipefail
arn=$($TF output -raw certificate_arn)
[ "$(aws acm describe-certificate --certificate-arn "$arn" --query Certificate.Status --output text)" = ISSUED ]
[ "$(aws acm describe-certificate --certificate-arn "$arn" --query 'length(Certificate.SubjectAlternativeNames)')" = 3 ]
echo "acm ok"
