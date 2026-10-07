FROM debian:trixie-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
# The guest architecture is the Docker host's: aarch64 guests use UEFI (AAVMF)
# firmware, x86_64 guests use SeaBIOS (bundled with QEMU).
RUN set -eu; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      arm64) pkgs="qemu-system-arm qemu-efi-aarch64" ;; \
      amd64) pkgs="qemu-system-x86" ;; \
      *) echo "unsupported architecture $arch" >&2; exit 1 ;; \
    esac; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      $pkgs qemu-utils passt genisoimage socat curl ca-certificates; \
    rm -rf /var/lib/apt/lists/*; \
    # passt runs from a path of its own: a Docker host with the passt package
    # attaches its AppArmor profile, written for the host's passt version, to
    # /usr/bin/passt, and it would confine this one too (on GitHub's Ubuntu
    # 24.04 runners that passt lost its network at the first inbound
    # connection). The AVX2 build is found next to the binary.
    mkdir -p /usr/local/libexec/homecloud; \
    cp -L /usr/bin/passt /usr/local/libexec/homecloud/passt; \
    if [ -e /usr/bin/passt.avx2 ]; then cp -L /usr/bin/passt.avx2 /usr/local/libexec/homecloud/passt.avx2; fi
COPY hc-vm-run /usr/local/bin/hc-vm-run
COPY hc-vm-fetch /usr/local/bin/hc-vm-fetch
COPY hc-vm-ctl /usr/local/bin/hc-vm-ctl
COPY hc-vm-flatten /usr/local/bin/hc-vm-flatten
RUN chmod 0755 /usr/local/bin/hc-vm-*
ENTRYPOINT ["/usr/local/bin/hc-vm-run"]
