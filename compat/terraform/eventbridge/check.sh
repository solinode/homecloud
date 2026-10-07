#!/usr/bin/env bash
# A large order put on the bus reaches the queue twice: as is (rule "created") and transformed ("large").
set -euo pipefail
bus=$($TF output -raw bus_name)
q=$($TF output -raw queue_url)
aws events put-events --entries "[{\"EventBusName\":\"$bus\",\"Source\":\"compat.orders\",\"DetailType\":\"OrderCreated\",\"Detail\":\"{\\\"id\\\":7,\\\"total\\\":250}\"}]" \
  --query FailedEntryCount | grep -qx 0
got=
for _ in $(seq 1 10); do
  got="$got $(aws sqs receive-message --queue-url "$q" --max-number-of-messages 10 --wait-time-seconds 2 --query 'Messages[].Body' --output text)"
  case $got in *large_order*OrderCreated* | *OrderCreated*large_order*) echo "eventbridge ok"; exit 0 ;; esac
done
echo "queue got: $got"; exit 1
