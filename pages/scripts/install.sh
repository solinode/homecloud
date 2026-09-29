#!/bin/sh
# HomeCloud installer for Linux and macOS.
#   curl -fsSL https://raw.githubusercontent.com/homecloudhq/homecloud/main/scripts/install.sh | sh
# Set HOMECLOUD_VERSION (e.g. v0.1.0) to pin a release, INSTALL_DIR to change the target.
set -eu

REPO="homecloudhq/homecloud"
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

if ! command -v docker >/dev/null 2>&1; then
  echo "HomeCloud runs every service on Docker, which is not installed."
  if [ "$os" = darwin ]; then
    echo "Install Docker Desktop or OrbStack, start it, then re-run this script."
  else
    echo "Install it with your package manager (e.g. 'curl -fsSL https://get.docker.com | sh'), then re-run this script."
  fi
  exit 1
fi

version="${HOMECLOUD_VERSION:-}"
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=1" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
fi
[ -n "$version" ] || { echo "Could not determine the latest release." >&2; exit 1; }

name="homecloud-$os-$arch"
url="https://github.com/$REPO/releases/download/$version/$name.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading HomeCloud $version for $os/$arch..."
curl -fsSL "$url" -o "$tmp/$name.tar.gz"
if curl -fsSL "https://github.com/$REPO/releases/download/$version/checksums.txt" -o "$tmp/checksums.txt" 2>/dev/null; then
  expected=$(grep " $name.tar.gz\$" "$tmp/checksums.txt" | cut -d' ' -f1)
  actual=$( (command -v sha256sum >/dev/null && sha256sum "$tmp/$name.tar.gz" || shasum -a 256 "$tmp/$name.tar.gz") | cut -d' ' -f1)
  [ -z "$expected" ] || [ "$expected" = "$actual" ] || { echo "Checksum mismatch!" >&2; exit 1; }
fi
tar -xzf "$tmp/$name.tar.gz" -C "$tmp"

if [ -w "$INSTALL_DIR" ]; then
  install -m 0755 "$tmp/$name/homecloud" "$INSTALL_DIR/homecloud"
else
  sudo install -m 0755 "$tmp/$name/homecloud" "$INSTALL_DIR/homecloud"
fi

echo "Installed $("$INSTALL_DIR/homecloud" version)"
echo
echo "Start your cloud with:   homecloud serve"
echo "Then open:               http://127.0.0.1:8080"
