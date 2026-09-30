#!/bin/bash
# Temporary: why does passt fail to bind /run/passt.sock?
uname -r
docker build -q -t probe -f cli/internal/svc/ec2/vm/runner.Dockerfile cli/internal/svc/ec2/vm
docker network create probenet >/dev/null
run() {
  echo "=== $*"
  docker run --rm --network probenet --entrypoint bash "$@" probe -c '
    id; ls -ld /run /tmp; touch /run/x && echo run-writable; grep -E "^Cap" /proc/self/status
    for sock in /run/passt.sock /tmp/p.sock; do
      rm -f $sock
      passt -f -s $sock -t all -u all >/tmp/passt.log 2>&1 & pid=$!; sleep 1.5
      if kill -0 $pid 2>/dev/null; then echo "$sock: PASST ALIVE"; else echo "$sock: PASST DEAD"; cat /tmp/passt.log; fi
      kill $pid 2>/dev/null; wait $pid 2>/dev/null
    done' 2>&1
}
U="--ulimit nofile=1048576:1048576 --security-opt seccomp=unconfined --security-opt apparmor=unconfined"
run $U
run $U --memory 1536m --cpu-period 100000 --cpu-quota 150000
run $U --device /dev/kvm
run $U --hostname ip-10-1-1-1 --dns 10.0.0.2 --add-host host.docker.internal:host-gateway
docker network rm probenet >/dev/null
