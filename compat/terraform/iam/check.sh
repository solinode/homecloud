#!/usr/bin/env bash
# The new user's access key works, and the user can assume the role (its inline policy allows it).
set -euo pipefail
role=$($TF output -raw role_arn)
export AWS_ACCESS_KEY_ID=$($TF output -raw access_key_id)
export AWS_SECRET_ACCESS_KEY=$($TF output -raw secret_access_key)
unset AWS_SESSION_TOKEN
aws sts get-caller-identity --query Arn --output text | grep -q ':user/compat-deployer'
aws sts assume-role --role-arn "$role" --role-session-name compat --query AssumedRoleUser.Arn --output text | grep -q 'assumed-role/compat-app/compat'
echo "iam ok"
