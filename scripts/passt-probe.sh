#!/bin/bash
# Temporary: which container security settings let passt start?
uname -r; cat /etc/os-release | head -2
sysctl kernel.apparmor_restrict_unprivileged_userns kernel.unprivileged_userns_clone user.max_user_namespaces 2>&1
docker info --format '{{.SecurityOptions}}'
docker version --format '{{.Server.Version}}'
docker build -q -t probe -f cli/internal/svc/ec2/vm/runner.Dockerfile cli/internal/svc/ec2/vm
docker network create probenet >/dev/null
run() {
  echo "=== $*"
  docker run --rm --network probenet --entrypoint bash "$@" probe -c 'passt -f -s /tmp/p.sock -t all -u all >/tmp/passt.log 2>&1 & pid=$!; sleep 1.5; if kill -0 $pid 2>/dev/null; then echo "PASST ALIVE"; else echo "PASST DEAD"; cat /tmp/passt.log; fi; grep -E "^Cap(Eff|Bnd)" /proc/self/status' 2>&1
}
# options come before the image: reorder by putting them in "$@"
run
run --security-opt seccomp=unconfined
run --security-opt seccomp=unconfined --security-opt apparmor=unconfined
run --security-opt apparmor=unconfined
run --cap-add SYS_ADMIN
run --cap-add SYS_ADMIN --security-opt apparmor=unconfined
run --cap-add SYS_ADMIN --security-opt seccomp=unconfined
run --cap-add SYS_ADMIN --security-opt seccomp=unconfined --security-opt apparmor=unconfined
run --privileged
docker network rm probenet >/dev/null
