#!/bin/sh
# HomeCloud installer for Linux and macOS.
#   curl -fsSL https://homecloud.pages.dev/scripts/install.sh | sh
# Set HOMECLOUD_VERSION (e.g. v0.1.0) to pin a release, INSTALL_DIR to change the target.
# The download is checked against the release's SHA-256 checksums, and, when
# cosign is installed, the checksums against the release's Sigstore signature.
set -eu

REPO="solinode/homecloud"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $os (use install.ps1 on Windows)" >&2; exit 1 ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac

# The server needs Docker; the CLI alone (talking to a server elsewhere) does not.
no_docker=
if ! command -v docker >/dev/null 2>&1; then
  no_docker=1
  echo "Note: Docker is not installed. The homecloud CLI works without it, but 'homecloud serve' runs every service on Docker."
  if [ "$os" = darwin ]; then
    echo "      Install Docker Desktop or OrbStack and start it before running the server."
  else
    echo "      Install it with your package manager (e.g. 'curl -fsSL https://get.docker.com | sh') before running the server."
  fi
fi

version="${HOMECLOUD_VERSION:-}"
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=1" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
fi
[ -n "$version" ] || { echo "Could not determine the latest release." >&2; exit 1; }

name="homecloud-$os-$arch"
# HOMECLOUD_RELEASES_URL serves the same files from a mirror; the signature
# check below still requires this repository's release workflow as the signer.
base="${HOMECLOUD_RELEASES_URL:-https://github.com/$REPO/releases/download}/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading HomeCloud $version for $os/$arch..."
curl -fsSL "$base/$name.tar.gz" -o "$tmp/$name.tar.gz"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || { echo "Could not download checksums.txt for $version." >&2; exit 1; }

# signed_release VERSION: true for v0.5.0 and later, which are all signed.
signed_release() {
  v=${1#v}; v=${v%%-*}; major=${v%%.*}; rest=${v#*.}; minor=${rest%%.*}
  case "$major$minor" in *[!0-9]*|"") return 1 ;; esac
  [ "$major" -gt 0 ] || [ "$minor" -ge 5 ]
}

# Releases from v0.5.0 on sign checksums.txt with Sigstore (cosign keyless, by
# the release workflow). With cosign installed the signature is verified;
# without it, only the SHA-256 checksum is.
if command -v cosign >/dev/null 2>&1; then
  if curl -fsSL "$base/checksums.txt.sigstore.json" -o "$tmp/checksums.txt.sigstore.json" 2>/dev/null; then
    if ! cosign verify-blob "$tmp/checksums.txt" --bundle "$tmp/checksums.txt.sigstore.json" \
        --certificate-identity "https://github.com/$REPO/.github/workflows/release.yml@refs/tags/$version" \
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" >"$tmp/cosign.log" 2>&1; then
      cat "$tmp/cosign.log" >&2
      echo "Signature verification of $version FAILED: not installing." >&2
      exit 1
    fi
    echo "Verified the release signature with cosign."
  elif signed_release "$version"; then
    echo "The signature of $version (checksums.txt.sigstore.json) could not be downloaded: not installing." >&2
    exit 1
  else
    echo "Note: $version is not signed (releases before v0.5.0 were not); verifying its SHA-256 checksum only."
  fi
else
  echo "Note: cosign is not installed, so only the SHA-256 checksum is verified (install cosign to verify the release signature too)."
fi
expected=$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f {print $1}' "$tmp/checksums.txt")
actual=$( (command -v sha256sum >/dev/null && sha256sum "$tmp/$name.tar.gz" || shasum -a 256 "$tmp/$name.tar.gz") | cut -d' ' -f1)
[ -n "$expected" ] || { echo "checksums.txt has no entry for $name.tar.gz." >&2; exit 1; }
[ "$expected" = "$actual" ] || { echo "Checksum mismatch for $name.tar.gz: the download is corrupt or was tampered with." >&2; exit 1; }
tar -xzf "$tmp/$name.tar.gz" -C "$tmp"

if [ -w "$INSTALL_DIR" ]; then
  install -m 0755 "$tmp/$name/homecloud" "$INSTALL_DIR/homecloud"
else
  sudo install -m 0755 "$tmp/$name/homecloud" "$INSTALL_DIR/homecloud"
fi

echo "Installed $("$INSTALL_DIR/homecloud" version)"
echo
if [ -n "$no_docker" ]; then
  echo "Install Docker, then start your cloud with:   homecloud serve"
else
  echo "Start your cloud with:   homecloud serve"
fi
echo "Then open:               http://127.0.0.1:8080"
