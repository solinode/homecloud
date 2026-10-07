#!/usr/bin/env bash
# Six subnets in the VPC, and the private route table sends 0.0.0.0/0 to the NAT gateway.
set -euo pipefail
vpc=$($TF output -raw vpc_id)
n=$(aws ec2 describe-subnets --filters "Name=vpc-id,Values=$vpc" --query 'length(Subnets)')
[ "$n" = 6 ] || { echo "want 6 subnets, got $n"; exit 1; }
nat=$($TF output -json natgw_ids | jq -r '.[0]')
rt=$($TF output -json private_route_table_ids | jq -r '.[0]')
got=$(aws ec2 describe-route-tables --route-table-ids "$rt" \
  --query "RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'].NatGatewayId | [0]" --output text)
[ "$got" = "$nat" ] || { echo "private default route goes to $got, want $nat"; exit 1; }
state=$(aws ec2 describe-nat-gateways --nat-gateway-ids "$nat" --query 'NatGateways[0].State' --output text)
[ "$state" = available ] || { echo "NAT gateway state $state"; exit 1; }
echo "vpc ok"
