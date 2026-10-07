#!/usr/bin/env bash
# A message published to the topic with a matching attribute arrives raw in the queue.
set -euo pipefail
topic=$($TF output -raw topic_arn)
queue=$($TF output -raw queue_url)
aws sns publish --topic-arn "$topic" --message '{"order":1}' \
  --message-attributes '{"kind":{"DataType":"String","StringValue":"order"}}' >/dev/null
body=$(aws sqs receive-message --queue-url "$queue" --wait-time-seconds 10 --query 'Messages[0].Body' --output text)
[ "$body" = '{"order":1}' ] || { echo "got body: $body"; exit 1; }
echo "sqs-sns ok"
