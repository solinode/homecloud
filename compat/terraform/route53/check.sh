#!/usr/bin/env bash
# HomeCloud's DNS server answers for the public zone's records.
set -euo pipefail
z=$($TF output -raw zone_name)
n=$(aws route53 list-resource-record-sets --hosted-zone-id "$($TF output -raw zone_id)" --query 'length(ResourceRecordSets)')
[ "$n" = 9 ] || { echo "want 9 record sets (7 + NS + SOA), got $n"; exit 1; }
if [ -n "${HC_DNS_PORT:-}" ] && command -v dig >/dev/null; then
  for _ in $(seq 1 15); do
    [ "$(dig +short -p "$HC_DNS_PORT" @127.0.0.1 "$z" A)" = 203.0.113.10 ] && { echo "route53 ok (dns)"; exit 0; }
    sleep 2
  done
  echo "DNS did not answer $z A"; exit 1
fi
echo "route53 ok"
