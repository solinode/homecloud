#!/bin/bash
# Temporary: which container settings let passt start?
uname -r
sysctl kernel.apparmor_restrict_unprivileged_userns fs.nr_open 2>&1
docker info --format '{{.SecurityOptions}}'
docker build -q -t probe -f cli/internal/svc/ec2/vm/runner.Dockerfile cli/internal/svc/ec2/vm
docker network create probenet >/dev/null
run() {
  echo "=== $*"
  docker run --rm --network probenet --entrypoint bash "$@" probe -c 'echo "nofile $(ulimit -Sn) $(ulimit -Hn)"; passt -f -s /tmp/p.sock -t all -u all >/tmp/passt.log 2>&1 & pid=$!; sleep 1.5; if kill -0 $pid 2>/dev/null; then echo "PASST ALIVE"; else echo "PASST DEAD"; cat /tmp/passt.log; fi' 2>&1
}
U="--ulimit nofile=1048576:1048576"
run
run $U
run $U --security-opt seccomp=unconfined
run $U --security-opt seccomp=unconfined --security-opt apparmor=unconfined
run $U --cap-add SYS_ADMIN
run $U --cap-add SYS_ADMIN --security-opt apparmor=unconfined
run --ulimit nofile=4194304:4194304 --security-opt seccomp=unconfined
docker network rm probenet >/dev/null
