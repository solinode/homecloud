#!/usr/bin/env bash
# The load balancer's published port serves nginx from the Fargate task.
set -euo pipefail
port=$(docker port "hc-elb-$($TF output -raw alb_name)" 80/tcp | head -1 | sed 's/.*://')
for _ in $(seq 1 60); do
  curl -fsS "http://localhost:$port/" 2>/dev/null | grep -q "Welcome to nginx" && { echo "ecs-alb ok"; exit 0; }
  sleep 3
done
echo "no nginx page behind the load balancer on port $port"; exit 1
