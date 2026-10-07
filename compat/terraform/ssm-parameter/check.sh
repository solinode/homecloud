#!/usr/bin/env bash
# The hierarchy holds all six parameters and SecureStrings decrypt.
set -euo pipefail
p=$($TF output -raw prefix)
[ "$(aws ssm get-parameters-by-path --path "$p" --query 'length(Parameters)')" = 6 ]
[ "$(aws ssm get-parameter --name "$p/password" --with-decryption --query Parameter.Value --output text)" = hunter2-compat ]
[ "$(aws ssm get-parameter --name "$p/token" --with-decryption --query Parameter.Value --output text)" = token-compat ]
echo "ssm ok"
