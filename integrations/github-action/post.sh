#!/usr/bin/env bash
# Post step, run after every job: prints the server log (collapsed), stops this
# HomeCloud and removes the containers, networks and volumes of its account, so
# a self-hosted runner is left as it was. Run by post.js; state arrives as HC_*.
set -uo pipefail

log="${HC_LOG:-}"
pid="${HC_PID:-}"
data="${HC_DATA:-}"
[ -n "$log" ] || { echo "HomeCloud was not started"; exit 0; }

if [ -f "$log" ]; then
  # Masks registered by setup.sh hold for the whole job; masking again covers a
  # setup step that failed before it could.
  pw=$(sed -n 's/.*password: \([^ ]*\).*/\1/p' "$log" | head -n1)
  [ -n "$pw" ] && echo "::add-mask::$pw"
  echo "::group::HomeCloud server log"
  cat "$log"
  echo "::endgroup::"
  account=$(sed -n 's/.*first start: created account \([0-9]*\).*/\1/p' "$log" | head -n1)
fi

if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
  echo "Stopping HomeCloud (pid $pid)"
  kill "$pid" 2>/dev/null
  for _ in $(seq 1 20); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.5
  done
  kill -9 "$pid" 2>/dev/null || true
fi

if [ -n "${account:-}" ] && command -v docker >/dev/null 2>&1; then
  echo "Removing the Docker resources of HomeCloud account $account"
  f="label=homecloud.account=$account"
  ids=$(docker ps -aq --filter "$f")
  [ -n "$ids" ] && docker rm -fv $ids >/dev/null
  ids=$(docker network ls -q --filter "$f")
  [ -n "$ids" ] && docker network rm $ids >/dev/null
  ids=$(docker volume ls -q --filter "$f")
  [ -n "$ids" ] && docker volume rm -f $ids >/dev/null
fi
[ -n "$data" ] && rm -rf "$data"
exit 0
