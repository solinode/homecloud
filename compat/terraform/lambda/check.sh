#!/usr/bin/env bash
# Invoking the alias runs the code; a message sent to the queue reaches the function's log.
set -euo pipefail
fn=$($TF output -raw function_name)
out=$(mktemp)
aws lambda invoke --function-name "$fn" --qualifier live --cli-binary-format raw-in-base64-out \
  --payload '{"echo":"ping"}' "$out" >/dev/null
grep -q '"greeting": "hello"' "$out" && grep -q '"echo": "ping"' "$out" || { cat "$out"; exit 1; }
aws sqs send-message --queue-url "$($TF output -raw queue_url)" --message-body compat-job >/dev/null
lg=$($TF output -raw log_group)
for _ in $(seq 1 30); do
  aws logs filter-log-events --log-group-name "$lg" --filter-pattern compat-job --query 'length(events)' 2>/dev/null | grep -qv '^0$' && { echo "lambda ok"; exit 0; }
  sleep 2
done
echo "queue message never reached the function"; exit 1
