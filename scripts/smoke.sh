#!/usr/bin/env bash
# End-to-end smoke test: exercises every HomeCloud service through the CLI
# against a running server, and cleans up after itself.
#
#   homecloud serve &            # or: HOMECLOUD_ENDPOINT=... HOMECLOUD_ACCESS_KEY_ID=... ./scripts/smoke.sh
#   ./scripts/smoke.sh
#
# Set HC=/path/to/homecloud to choose the binary (default: homecloud on PATH).
set -euo pipefail

HC=${HC:-homecloud}
RUN=smoke$RANDOM
PASS=0
FAILED=()
CLEANUP=()

hc() { "$HC" "$@"; }
json() { "$HC" -o json "$@"; }
field() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval('d' + sys.argv[1]))" "$1"; }
cleanup() {
  for ((i = ${#CLEANUP[@]} - 1; i >= 0; i--)); do eval "${CLEANUP[$i]}" >/dev/null 2>&1 || true; done
}
trap cleanup EXIT
defer() { CLEANUP+=("$1"); }

check() { # check NAME COMMAND...
  local name=$1; shift
  if out=$("$@" 2>&1); then
    PASS=$((PASS + 1)); printf '  \033[32m✓\033[0m %s\n' "$name"
  else
    FAILED+=("$name"); printf '  \033[31m✗\033[0m %s\n%s\n' "$name" "$(echo "$out" | tail -5 | sed 's/^/      /')"
  fi
}
wait_for() { # wait_for SECONDS COMMAND... (until the command succeeds)
  local deadline=$((SECONDS + $1)); shift
  until "$@" >/dev/null 2>&1; do
    [ $SECONDS -ge $deadline ] && return 1
    sleep 2
  done
}
contains() { grep -q -- "$2" <<<"$1"; }

echo "HomeCloud smoke test ($RUN)"
check "server reachable" hc whoami

echo "IAM"
defer "hc iam delete-user $RUN-user"
check "create user with S3 read-only" hc iam create-user $RUN-user --policy S3ReadOnlyAccess
check "simulator denies PutObject" bash -c "$HC iam simulate $RUN-user s3:PutObject | grep -q implicitDeny"

echo "S3"
echo "hello $RUN" > /tmp/$RUN.txt
defer "hc s3 rb s3://$RUN-bucket --force"
check "make bucket" hc s3 mb s3://$RUN-bucket
check "upload" hc s3 cp /tmp/$RUN.txt s3://$RUN-bucket/dir/
check "download round-trip" bash -c "$HC s3 cp s3://$RUN-bucket/dir/$RUN.txt /tmp/$RUN.out && diff /tmp/$RUN.txt /tmp/$RUN.out"
check "presigned URL" bash -c "curl -fsS \"\$($HC s3 presign s3://$RUN-bucket/dir/$RUN.txt)\" | grep -q $RUN"

echo "Secrets, KMS, Parameter Store"
defer "hc secrets delete $RUN/db --force"
check "create secret" hc secrets create $RUN/db '{"password":"p4ss"}'
check "read secret" bash -c "$HC secrets get $RUN/db | grep -q p4ss"
defer "hc api POST /api/v1/kms/keys/alias%2F$RUN/schedule-deletion -d '{\"pending_window_days\":7}'"
check "KMS encrypt/decrypt" bash -c "$HC kms create --alias alias/$RUN >/dev/null && [ \"\$($HC kms decrypt \$($HC kms encrypt alias/$RUN s3cret))\" = s3cret ]"
defer "hc ssm delete /$RUN/url"
check "SecureString parameter" bash -c "$HC ssm put /$RUN/url postgres://x --type SecureString >/dev/null && $HC ssm get /$RUN/url --decrypt | grep -q postgres"

echo "VPC and EC2"
SG=$(json vpc create-sg $RUN-web | field "['id']")
defer "hc api DELETE /api/v1/vpc/security-groups/$SG"
check "open port 80" hc vpc allow "$SG" 80
INST=$(json ec2 run --name $RUN --image ami-nginx --type t3.nano --sg "$SG" | field "[0]['id']")
defer "hc ec2 terminate $INST"
check "instance running" bash -c "$HC ec2 describe $INST | grep -q running"
PORT=$(json ec2 describe "$INST" | field "['public_ports']['80/tcp']")
check "published port serves nginx" wait_for 30 bash -c "curl -fsS localhost:$PORT | grep -qi nginx"
check "run command" bash -c "$HC ec2 exec $INST -- 'cat /var/lib/homecloud/instance.json' | grep -q $INST"

echo "Lambda and API Gateway"
defer "hc lambda delete $RUN-fn"
check "create function" hc lambda create $RUN-fn --runtime python3.12
check "invoke" bash -c "$HC lambda invoke $RUN-fn -p '{\"name\":\"smoke\"}' | grep -q 'Hello, smoke'"
check "function URL" bash -c "url=\$($HC -o json lambda url $RUN-fn | python3 -c 'import json,sys;print(json.load(sys.stdin)[\"function_url\"][\"url\"])'); curl -fsS \"\${url}x?name=url\" | grep -q 'Hello, url'"

echo "SQS, SNS, EventBridge"
defer "hc sqs delete $RUN-q"
check "create queue" hc sqs create $RUN-q
check "send/receive" bash -c "$HC sqs send $RUN-q hi >/dev/null && $HC sqs receive $RUN-q --wait 2 --delete | grep -q hi"
defer "hc api DELETE /api/v1/sns/topics/$RUN-t"
check "SNS fan-out to SQS" bash -c "$HC sns create $RUN-t >/dev/null && $HC sns subscribe $RUN-t sqs $RUN-q --raw >/dev/null && $HC sns publish $RUN-t fanout >/dev/null && sleep 1 && $HC sqs receive $RUN-q --wait 3 --delete | grep -q fanout"
QARN=$(json sqs ls | python3 -c "import json,sys; print([q['arn'] for q in json.load(sys.stdin) if q['name']=='$RUN-q'][0])")
defer "hc events delete $RUN-rule"
check "event rule routes to queue" bash -c "$HC events rule $RUN-rule --pattern '{\"source\":[\"$RUN\"]}' --target $QARN >/dev/null && $HC events put $RUN Test >/dev/null && sleep 1 && $HC sqs receive $RUN-q --wait 3 --delete | grep -q $RUN"

echo "DynamoDB"
defer "hc dynamodb drop $RUN-t"
check "create table" hc dynamodb create $RUN-t --pk user --sk ts:N
check "put and query" bash -c "$HC dynamodb put $RUN-t '{\"user\":\"a\",\"ts\":2}' >/dev/null && $HC dynamodb put $RUN-t '{\"user\":\"a\",\"ts\":1}' >/dev/null && $HC dynamodb query $RUN-t a | python3 -c 'import json,sys; assert [i[\"ts\"] for i in json.load(sys.stdin)] == [1,2]'"

echo "Step Functions and CloudFormation"
cat > /tmp/$RUN.asl.json <<EOF
{"StartAt":"P","States":{"P":{"Type":"Pass","Result":{"ok":true},"ResultPath":"\$.r","Next":"C"},
 "C":{"Type":"Choice","Choices":[{"Variable":"\$.r.ok","BooleanEquals":true,"Next":"Done"}],"Default":"Bad"},
 "Done":{"Type":"Succeed"},"Bad":{"Type":"Fail"}}}
EOF
defer "hc api DELETE /api/v1/sfn/state-machines/$RUN-sm"
check "state machine execution" bash -c "$HC sfn create $RUN-sm /tmp/$RUN.asl.json >/dev/null && $HC sfn start $RUN-sm | grep -q SUCCEEDED"
cat > /tmp/$RUN.stack.yaml <<EOF
Resources:
  Queue: {Type: HC::SQS::Queue, Properties: {name: $RUN-stackq}}
  Table: {Type: HC::DynamoDB::Table, Properties: {name: $RUN-stackt, partition_key: {name: id}}}
Outputs:
  QueueArn: {Value: !GetAtt Queue.arn}
EOF
defer "hc cfn delete $RUN-stack"
check "stack create" hc cfn create $RUN-stack /tmp/$RUN.stack.yaml
check "stack delete removes resources" bash -c "$HC cfn delete $RUN-stack >/dev/null && sleep 2 && ! $HC sqs ls | grep -q $RUN-stackq"

echo "Cognito"
POOL=$(json cognito create-pool $RUN-pool | field "['id']")
defer "hc api DELETE /api/v1/cognito/user-pools/$POOL"
CLIENT=$(json cognito create-client "$POOL" web | field "['id']")
check "sign-in issues tokens" bash -c "$HC cognito create-user $POOL alice --password 'passw0rd123' >/dev/null && $HC cognito login $POOL alice passw0rd123 --client $CLIENT | grep -q id_token"

if [ "${SMOKE_DATABASES:-1}" = 1 ]; then
  echo "RDS"
  defer "hc rds delete $RUN-db"
  check "create postgres" hc rds create $RUN-db --engine postgres
  check "database available" wait_for 240 bash -c "$HC rds describe $RUN-db | grep -q available"
  check "query editor" bash -c "$HC rds query $RUN-db 'select 40+2 as answer' | grep -q 42"
fi

echo
echo "passed: $PASS   failed: ${#FAILED[@]}"
if [ ${#FAILED[@]} -gt 0 ]; then
  printf '  - %s\n' "${FAILED[@]}"
  exit 1
fi
