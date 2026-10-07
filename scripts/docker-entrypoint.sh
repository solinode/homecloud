#!/bin/sh
# Entrypoint of the HomeCloud container image (see Dockerfile).
#
# Arguments are a homecloud command ("serve", "version", "serve --tls-self-signed")
# or any other program. homecloud runs as the unprivileged "homecloud" user,
# made a member of the group that owns the mounted Docker socket. Note that
# access to the Docker socket is equivalent to root on the Docker host: the
# user switch limits what a bug in HomeCloud can do inside this container,
# not on the host. Set HOMECLOUD_RUN_AS_ROOT=1 to keep root.
set -eu

if [ $# -eq 0 ]; then
  set -- serve
fi
case "$1" in
  -*) set -- homecloud serve "$@" ;;
  homecloud) ;;
  *) command -v "$1" >/dev/null 2>&1 || set -- homecloud "$@" ;;
esac

if [ "$(id -u)" != 0 ] || [ "${HOMECLOUD_RUN_AS_ROOT:-0}" = 1 ] || [ "$1" != homecloud ]; then
  exec "$@"
fi

user=homecloud
sock=/var/run/docker.sock
case "${DOCKER_HOST:-}" in
  unix://*) sock=${DOCKER_HOST#unix://} ;;
esac

if [ -S "$sock" ]; then
  gid=$(stat -c %g "$sock")
  group=$(getent group "$gid" | cut -d: -f1)
  if [ -z "$group" ]; then
    group=docker-host
    addgroup -S -g "$gid" "$group"
  fi
  addgroup "$user" "$group" 2>/dev/null || true
  if ! su-exec "$user" test -r "$sock" -a -w "$sock"; then
    echo "homecloud: the Docker socket $sock is not accessible to its group; running as root" >&2
    exec "$@"
  fi
fi

# A bind-mounted data directory may belong to someone else.
data=${HOMECLOUD_DATA_DIR:-/data}
mkdir -p "$data"
if [ "$(stat -c %u "$data")" != "$(id -u "$user")" ]; then
  chown -R "$user:$user" "$data"
fi

exec su-exec "$user" "$@"
