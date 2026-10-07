#!/usr/bin/env bash
# Runs the Terraform compatibility suite against a fresh HomeCloud.
#
#   compat/run.sh                 # every scenario under compat/terraform/
#   compat/run.sh vpc s3-bucket   # just these
#
# For each scenario it runs: init, apply, the scenario's check.sh (if any), a plan that must show
# no changes, and destroy. Results go to compat/results.json and compat/results.md.
#
# Environment:
#   TF            tofu or terraform (default: whichever is on PATH, tofu first)
#   HC_BIN        homecloud binary (default: bin/homecloud, built with `make build` unless HC_SKIP_BUILD=1)
#   HC_ENDPOINT   use an already running HomeCloud (with AWS_* credentials in the environment)
#                 instead of starting a fresh one
#   HC_KEEP=1     keep the server, its data dir and its containers after the run (for debugging)
#   HC_OUT        where results go (default: compat/)
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SUITE=$ROOT/compat/terraform
OUT=${HC_OUT:-$ROOT/compat}
TF=${TF:-$(command -v tofu || command -v terraform)}
[ -n "$TF" ] || { echo "need tofu or terraform on PATH" >&2; exit 2; }

if [ $# -gt 0 ]; then
  SCENARIOS=("$@")
else
  SCENARIOS=()
  for d in "$SUITE"/*/; do
    n=$(basename "$d")
    case $n in _*) continue ;; esac
    SCENARIOS+=("$n")
  done
fi

WORK=$(mktemp -d "${TMPDIR:-/tmp}/hc-compat.XXXXXX")
export TF_PLUGIN_CACHE_DIR=${TF_PLUGIN_CACHE_DIR:-$WORK/plugins}
mkdir -p "$TF_PLUGIN_CACHE_DIR"
export TF_IN_AUTOMATION=1 TF_INPUT=0 CHECKPOINT_DISABLE=1
SERVER_PID= ACCOUNT= DATA=

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }

cleanup() {
  if [ -n "$SERVER_PID" ] && [ "${HC_KEEP:-}" != 1 ]; then
    kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null
    if [ -n "$ACCOUNT" ]; then
      f="label=homecloud.account=$ACCOUNT"
      ids=$(docker ps -aq --filter "$f"); [ -n "$ids" ] && docker rm -f $ids >/dev/null
      ids=$(docker network ls -q --filter "$f"); [ -n "$ids" ] && docker network rm $ids >/dev/null
      ids=$(docker volume ls -q --filter "$f"); [ -n "$ids" ] && docker volume rm -f $ids >/dev/null
    fi
    rm -rf "$DATA"
  elif [ -n "$SERVER_PID" ]; then
    echo "kept: server pid $SERVER_PID, data dir $DATA, account $ACCOUNT"
  fi
  [ "${HC_KEEP:-}" = 1 ] || rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

start_server() {
  local bin=${HC_BIN:-$ROOT/bin/homecloud}
  if [ -z "${HC_BIN:-}" ] && [ "${HC_SKIP_BUILD:-}" != 1 ]; then
    (cd "$ROOT" && make build >/dev/null) || { echo "build failed" >&2; exit 2; }
  fi
  DATA=$WORK/data
  mkdir -p "$DATA"
  local api s3 s3c dns
  api=$(free_port) s3=$(free_port) s3c=$(free_port) dns=$(free_port)
  "$bin" serve --data-dir "$DATA" --addr "127.0.0.1:$api" --s3-port "$s3" --s3-console-port "$s3c" \
    --dns-port "$dns" >"$WORK/serve.log" 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 90); do
    curl -fsS "http://127.0.0.1:$api/api/v1/health" >/dev/null 2>&1 && break
    kill -0 "$SERVER_PID" 2>/dev/null || { cat "$WORK/serve.log"; echo "server exited" >&2; exit 2; }
    sleep 1
  done
  for _ in $(seq 1 180); do
    grep -q "s3: MinIO ready" "$WORK/serve.log" && break
    sleep 1
  done
  grep -q "s3: MinIO ready" "$WORK/serve.log" || { cat "$WORK/serve.log"; echo "S3 not ready" >&2; exit 2; }
  ACCOUNT=$(sed -n 's/.*created account \([0-9]*\).*/\1/p' "$WORK/serve.log" | head -1)
  eval "$(HOMECLOUD_DATA_DIR=$DATA "$bin" aws-env)"
  # The S3 client puts the account in the host name: use a name, not an IP address.
  export AWS_ENDPOINT_URL=http://localhost:$api
  export HC_DNS_PORT=$dns
  echo "HomeCloud $AWS_ENDPOINT_URL (account $ACCOUNT, data $DATA)"
}

if [ -n "${HC_ENDPOINT:-}" ]; then
  export AWS_ENDPOINT_URL=$HC_ENDPOINT
else
  start_server
fi
export AWS_REGION=${AWS_REGION:-us-east-1} AWS_DEFAULT_REGION=${AWS_REGION:-us-east-1}
export AWS_PAGER=

# step <log> <cmd...>: runs a step with its output in <log>; returns its exit code.
step() { local log=$1; shift; "$@" >>"$log" 2>&1; }

# first_error <log>: a one-line reason for a failure.
first_error() {
  grep -m1 -E 'Error: |error:|FAIL' "$1" | sed -e 's/\x1b\[[0-9;]*m//g' -e 's/^[│ ]*//' | cut -c1-240
}

RESULTS=$WORK/results.tsv
: >"$RESULTS"
LOGS=$OUT/logs
mkdir -p "$LOGS"
for s in "${SCENARIOS[@]}"; do
  dir=$SUITE/$s
  log=$LOGS/$s.log
  : >"$log"
  [ -d "$dir" ] || { echo "no scenario $s" >&2; continue; }
  # Each scenario runs in a scratch copy so .terraform and state never land in the repo.
  run=$WORK/run/$s
  mkdir -p "$run"
  cp -RL "$dir/." "$run/"
  apply=fail idem=skip destroy=skip check=skip notes=
  start=$(date +%s)
  echo "== $s"
  if ! step "$log" "$TF" -chdir="$run" init -no-color; then
    apply=fail notes="init: $(first_error "$log")"
  elif step "$log" "$TF" -chdir="$run" apply -no-color -auto-approve; then
    apply=pass
    if [ -x "$run/check.sh" ]; then
      if (cd "$run" && TF="$TF" ./check.sh) >>"$log" 2>&1; then check=pass; else check=fail; notes="check: $(tail -1 "$log" | cut -c1-200)"; fi
    fi
    echo "--- plan (must show no changes)" >>"$log"
    plan=$run/plan.out
    "$TF" -chdir="$run" plan -no-color -detailed-exitcode >"$plan" 2>&1
    rc=$?
    cat "$plan" >>"$log"
    case $rc in
      0) idem=pass ;;
      2) idem=fail; [ -n "$notes" ] || notes="plan shows changes: $(grep -E '^  # |^ +[~+-] [a-z_]+ ' "$plan" | head -4 | sed 's/^ *//' | paste -sd';' -)" ;;
      *) idem=fail; [ -n "$notes" ] || notes="plan: $(first_error "$plan")" ;;
    esac
  else
    notes="apply: $(first_error "$log")"
  fi
  if [ -f "$run/terraform.tfstate" ] || [ -d "$run/.terraform" ]; then
    echo "--- destroy" >>"$log"
    if step "$log" "$TF" -chdir="$run" destroy -no-color -auto-approve; then
      destroy=pass
    else
      destroy=fail
      [ -n "$notes" ] || notes="destroy: $(grep -A3 'Error: ' "$log" | tail -4 | tr '\n' ' ' | sed 's/[│ ]\+/ /g' | cut -c1-240)"
    fi
  fi
  secs=$(( $(date +%s) - start ))
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$s" "$apply" "$check" "$idem" "$destroy" "$secs" "$notes" >>"$RESULTS"
  printf '   apply=%s check=%s idempotent=%s destroy=%s (%ss) %s\n' "$apply" "$check" "$idem" "$destroy" "$secs" "$notes"
done

# Exits non-zero when any scenario that ran failed a step.
python3 "$ROOT/compat/report.py" "$RESULTS" "$OUT" "$("$TF" version | head -1)"
