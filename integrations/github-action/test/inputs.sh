#!/usr/bin/env bash
# Checks setup.sh's input validation without installing anything:
#   bash integrations/github-action/test/inputs.sh
set -u
setup="$(cd "$(dirname "$0")/.." && pwd)/setup.sh"
fails=0

# expect ok|fail PORT TIMEOUT SERVICES
expect() {
  local want=$1 out got=ok
  out=$(HC_VALIDATE_ONLY=1 HC_PORT="$2" HC_WAIT_TIMEOUT="$3" HC_SERVICES_WAIT="$4" \
    GITHUB_OUTPUT=/dev/null GITHUB_ENV=/dev/null GITHUB_STATE= RUNNER_TEMP="${TMPDIR:-/tmp}" bash "$setup" 2>&1) || got=fail
  if [ "$got" != "$want" ]; then
    echo "FAIL: port='$2' timeout='$3' services='$4': want $want, got $got: $out"
    fails=$((fails + 1))
  else
    echo "ok:   port='$2' timeout='$3' services='$4' -> $want"
  fi
}

expect ok 8080 180 ""
expect ok 8080 08 "s3, sqs"                    # leading zero is decimal, not octal
expect ok 08080 180 "S3"
expect ok 8080 180 $'s3\necr\ndynamodb,lambda'
expect fail 8080 abc ""
expect fail 8080 0 ""
expect ok 8080 "" ""                           # empty means the default (180)
expect fail 8080 -5 ""
expect fail http 180 ""
expect fail 70000 180 ""
expect fail 8080 180 "s33"
expect fail 8080 180 "s3,nosuchservice"

[ "$fails" -eq 0 ] && echo "all input checks passed" || { echo "$fails input checks failed"; exit 1; }
