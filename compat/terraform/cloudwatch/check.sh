#!/usr/bin/env bash
# An ERROR line in the log group becomes a data point of the filter's metric.
set -euo pipefail
lg=$($TF output -raw log_group)
now=$(($(date +%s) * 1000))
aws logs put-log-events --log-group-name "$lg" --log-stream-name web-1 \
  --log-events "timestamp=$now,message=ERROR compat check" >/dev/null
start=$(date -u -d '-10 min' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-10M +%Y-%m-%dT%H:%M:%SZ)
end=$(date -u -d '+5 min' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+5M +%Y-%m-%dT%H:%M:%SZ)
for _ in $(seq 1 15); do
  n=$(aws cloudwatch get-metric-statistics --namespace Compat/App --metric-name Errors --statistics Sum \
    --period 60 --start-time "$start" --end-time "$end" --query 'length(Datapoints)')
  [ "$n" != 0 ] && { echo "cloudwatch ok"; exit 0; }
  sleep 2
done
echo "metric filter produced no data points"; exit 1
