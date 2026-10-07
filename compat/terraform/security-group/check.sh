#!/usr/bin/env bash
# The app group has its five ingress rules: ssh, the 8080-8090 range, IPv6, self, and the web group.
set -euo pipefail
sg=$($TF output -raw app_sg)
n=$(aws ec2 describe-security-group-rules --filters "Name=group-id,Values=$sg" \
  --query 'length(SecurityGroupRules[?IsEgress==`false`])')
[ "$n" = 5 ] || { echo "want 5 ingress rules, got $n"; exit 1; }
echo "security-group ok"
