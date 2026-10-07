#!/usr/bin/env bash
# Each secret's value reads back; the generated password has the requested length.
set -euo pipefail
v() { aws secretsmanager get-secret-value --secret-id "$1" --query SecretString --output text; }
v "$($TF output -raw config_arn)" | grep -q db.internal
[ "$(v "$($TF output -raw generated_arn)" | tr -d '\n' | wc -c | tr -d ' ')" = 40 ]
[ "$(v "$($TF output -raw encrypted_arn)")" = s3cr3t-value ]
echo "secrets-manager ok"
