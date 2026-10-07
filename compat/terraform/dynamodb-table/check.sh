#!/usr/bin/env bash
# An item written to the table can be read back through the global index.
set -euo pipefail
t=$($TF output -raw table)
aws dynamodb put-item --table-name "$t" \
  --item '{"pk":{"S":"o#1"},"sk":{"S":"v1"},"status":{"S":"open"},"created":{"N":"1"}}'
n=$(aws dynamodb query --table-name "$t" --index-name by-status \
  --key-condition-expression '#s = :s' --expression-attribute-names '{"#s":"status"}' \
  --expression-attribute-values '{":s":{"S":"open"}}' --query Count)
[ "$n" = 1 ] || { echo "index query returned $n items"; exit 1; }
[ "$(aws dynamodb describe-time-to-live --table-name "$t" --query TimeToLiveDescription.TimeToLiveStatus --output text)" = ENABLED ]
echo "dynamodb ok"
