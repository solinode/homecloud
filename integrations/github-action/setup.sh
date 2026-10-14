#!/usr/bin/env bash
# Installs HomeCloud, starts `homecloud serve` in the background and exports the
# AWS_* variables for the rest of the job. Run by main.js; inputs arrive as HC_*.
# Runs on Linux and macOS runners that have Docker (ubuntu-* runners do).
set -euo pipefail

REPO="solinode/homecloud"
version="${HC_VERSION:-latest}"
port="${HC_PORT:-8080}"
timeout="${HC_WAIT_TIMEOUT:-180}"
tmp="${RUNNER_TEMP:-$(mktemp -d)}"
bin_dir="$tmp/homecloud-bin"
data_dir="$tmp/homecloud-data"
log="$tmp/homecloud.log"
out="${GITHUB_OUTPUT:-/dev/null}"
envf="${GITHUB_ENV:-/dev/null}"

fail() { echo "::error::$*"; exit 1; }

# Inputs are checked before anything starts, so a bad value never leaves a
# half-started server (and its first-start log) behind.
case "$port" in '' | *[!0-9]*) fail "port must be a number, got '$port'" ;; esac
port=$((10#$port))
[ "$port" -ge 1 ] && [ "$port" -le 65535 ] || fail "port must be between 1 and 65535, got '$HC_PORT'"
case "$timeout" in '' | *[!0-9]*) fail "wait-timeout must be a whole number of seconds, got '$timeout'" ;; esac
timeout=$((10#$timeout))
[ "$timeout" -ge 1 ] || fail "wait-timeout must be at least 1 second"
case "$(echo "${HC_VERIFY_SIGNATURE:-true}" | tr '[:upper:]' '[:lower:]')" in
  true) verify_sig=1 ;;
  false) verify_sig= ;;
  *) fail "verify-signature must be true or false, got '$HC_VERIFY_SIGNATURE'" ;;
esac

# Services that start containers in the background log a line when they are ready;
# the others run inside the HomeCloud process and are ready with the API.
in_process=" acm apigateway autoscaling cloudformation cloudtrail cloudwatch cognito-idp dynamodb ec2 ecs efs elb elbv2 elasticloadbalancing events eventbridge iam kms lambda logs rds secretsmanager sns sqs ssm states stepfunctions sts "
waits=()
for svc in $(echo "${HC_SERVICES_WAIT:-}" | tr ',\n' '  ' | tr '[:upper:]' '[:lower:]'); do
  case "$svc" in
    s3 | ecr | route53 | dns) waits+=("$svc") ;;
    *) case "$in_process" in *" $svc "*) ;; *) fail "services-wait: unknown service '$svc' (background: s3, ecr, route53; in-process:$in_process)" ;; esac ;;
  esac
done
[ "${HC_VALIDATE_ONLY:-}" = 1 ] && { echo "inputs OK: port=$port wait-timeout=$timeout waits=${waits[*]:-} verify-signature=${verify_sig:-0}"; exit 0; }

command -v docker >/dev/null 2>&1 || fail "HomeCloud needs Docker on the runner (ubuntu-* runners have it)"
docker info >/dev/null 2>&1 || fail "Docker is installed but not running"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac
name="homecloud-$os-$arch"
if [ "$version" = latest ]; then
  # Resolve "latest" to its tag: the signature names the tag it was made for, and
  # every file then comes from the same release.
  version=$(curl -fsSL --retry 3 -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed -n 's#.*/releases/tag/##p') || true
  [ -n "$version" ] || fail "could not determine the latest HomeCloud release"
fi
case "$version" in v*) ;; *) version="v$version" ;; esac
base="https://github.com/$REPO/releases/download/$version"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# signed_release VERSION: true for v0.5.0 and later, which are all signed.
signed_release() {
  local v=${1#v} major rest minor
  v=${v%%-*}
  major=${v%%.*}
  rest=${v#*.}
  minor=${rest%%.*}
  case "$major$minor" in *[!0-9]* | "") return 1 ;; esac
  [ "$major" -gt 0 ] || [ "$minor" -ge 5 ]
}

# cosign for the signature check: the runner's own (for example from
# sigstore/cosign-installer), else this pinned release, checked against its SHA-256.
COSIGN_VERSION=v3.1.3
cosign_sha256() {
  case "$1" in
    linux-amd64) echo 4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71 ;;
    linux-arm64) echo c5d324e091826b0d7a78eb16fef316450b4eb9aaec045611c08ba06f5e73220a ;;
    darwin-amd64) echo 2347488e5d5b25336644024dfeca5601b190e91197a71a917bda44744aff106c ;;
    darwin-arm64) echo 5cf948c2f4dfe59687bdd0b8523709067383e03982cc543475c8a7dc70e92a76 ;;
  esac
}
find_cosign() {
  if cosign=$(command -v cosign); then
    return
  fi
  local dir="$tmp/homecloud-cosign" want got
  want=$(cosign_sha256 "$os-$arch")
  [ -n "$want" ] || fail "no pinned cosign for $os/$arch: install cosign first, or set verify-signature: false"
  mkdir -p "$dir"
  curl -fsSL --retry 3 "https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION/cosign-$os-$arch" -o "$dir/cosign" ||
    fail "download of cosign $COSIGN_VERSION failed"
  got=$(sha256 "$dir/cosign")
  [ "$got" = "$want" ] || fail "checksum mismatch for cosign $COSIGN_VERSION (expected $want, got $got)"
  chmod +x "$dir/cosign"
  cosign="$dir/cosign"
}

echo "::group::Install HomeCloud ($version, $os/$arch)"
dl="$tmp/homecloud-dl"
rm -rf "$dl" && mkdir -p "$dl" "$bin_dir"
curl -fsSL --retry 3 "$base/$name.tar.gz" -o "$dl/$name.tar.gz" || fail "download $base/$name.tar.gz failed"
curl -fsSL --retry 3 "$base/checksums.txt" -o "$dl/checksums.txt" || fail "download $base/checksums.txt failed"

# Releases from v0.5.0 on sign checksums.txt with Sigstore keyless signing in the
# release workflow (checksums.txt.sigstore.json). Verify it when it exists, so the
# checksum below is trusted; older releases are unsigned and get the checksum only.
if [ -z "$verify_sig" ]; then
  echo "verify-signature is false: checking the SHA-256 checksum only"
elif curl -fsSL --retry 3 "$base/checksums.txt.sigstore.json" -o "$dl/checksums.txt.sigstore.json" 2>/dev/null; then
  find_cosign
  echo "verifying the release signature with $cosign"
  identity="https://github.com/$REPO/.github/workflows/release.yml@refs/tags/$version"
  if ! "$cosign" verify-blob "$dl/checksums.txt" --bundle "$dl/checksums.txt.sigstore.json" \
    --certificate-identity "$identity" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com >"$dl/cosign.log" 2>&1; then
    cat "$dl/cosign.log"
    fail "signature verification of checksums.txt failed for $version (expected signer $identity)"
  fi
  echo "signature OK (checksums.txt signed by $identity)"
elif signed_release "$version"; then
  fail "$version should be signed, but $base/checksums.txt.sigstore.json could not be downloaded (set verify-signature: false to skip the check)"
else
  echo "$version is not signed (releases before v0.5.0 were not): checking the SHA-256 checksum only"
fi

expected=$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f {print $1}' "$dl/checksums.txt")
[ -n "$expected" ] || fail "checksums.txt has no entry for $name.tar.gz"
actual=$(sha256 "$dl/$name.tar.gz")
[ "$expected" = "$actual" ] || fail "checksum mismatch for $name.tar.gz (expected $expected, got $actual)"
echo "checksum OK ($actual)"
tar -xzf "$dl/$name.tar.gz" -C "$dl"
install -m 0755 "$dl/$name/homecloud" "$bin_dir/homecloud"
rm -rf "$dl"
[ -n "${GITHUB_PATH:-}" ] && echo "$bin_dir" >> "$GITHUB_PATH"
"$bin_dir/homecloud" version
echo "::endgroup::"
# Tests of the download and verification stop here (test.yml).
[ "${HC_INSTALL_ONLY:-}" = 1 ] && exit 0

echo "Starting homecloud serve on 127.0.0.1:$port (data: $data_dir, log: $log)"
mkdir -p "$data_dir"
export HOMECLOUD_DATA_DIR="$data_dir"
nohup "$bin_dir/homecloud" serve --data-dir "$data_dir" --addr "127.0.0.1:$port" > "$log" 2>&1 &
pid=$!
echo "$pid" > "$tmp/homecloud.pid"
if [ -n "${GITHUB_STATE:-}" ]; then
  { echo "log=$log"; echo "pid=$pid"; echo "data=$data_dir"; } >> "$GITHUB_STATE"
fi

# The first-start log carries the root console password: mask it as soon as it
# appears (post.sh masks it again before it prints the log).
masked=
mask_password() {
  [ -n "$masked" ] && return 0
  local pw
  pw=$(sed -n 's/.*password: \([^ ]*\).*/\1/p' "$log" | head -n1)
  if [ -n "$pw" ]; then echo "::add-mask::$pw"; masked=1; fi
  return 0
}

# The server listens on 127.0.0.1; AWS tools get it as localhost, like `homecloud aws-env`
# prints it: S3 Control (which the Terraform AWS provider calls for every bucket) puts the
# account ID in front of the host, and only a host name resolves that way.
endpoint="http://localhost:$port"
deadline=$((SECONDS + timeout))
until curl -fsS --connect-timeout 2 --max-time 5 "http://127.0.0.1:$port/api/v1/health" >/dev/null 2>&1; do
  mask_password
  kill -0 "$pid" 2>/dev/null || fail "homecloud serve exited before it became healthy (the post step prints its log)"
  [ "$SECONDS" -lt "$deadline" ] || fail "HomeCloud was not healthy after ${timeout}s"
  sleep 1
done
mask_password
echo "HomeCloud is healthy at $endpoint"

for svc in ${waits[@]+"${waits[@]}"}; do
  case "$svc" in
    s3) ready="s3: MinIO ready" ;;
    ecr) ready="ecr: registry ready" ;;
    route53 | dns) ready="route53: DNS ready" ;;
  esac
  echo "Waiting for $svc"
  until grep -q "$ready" "$log"; do
    kill -0 "$pid" 2>/dev/null || fail "homecloud serve exited while waiting for $svc"
    [ "$SECONDS" -lt "$deadline" ] || fail "$svc was not ready after ${timeout}s"
    sleep 1
  done
  echo "$svc is ready"
done

# Credentials and region, as `homecloud aws-env` prints them.
creds=$("$bin_dir/homecloud" aws-env | sed -n 's/^export \([A-Z_]*\)="\(.*\)"$/\1=\2/p')
get() { echo "$creds" | sed -n "s/^$1=//p"; }
key=$(get AWS_ACCESS_KEY_ID)
secret=$(get AWS_SECRET_ACCESS_KEY)
region=$(get AWS_REGION)
[ -n "$key" ] && [ -n "$secret" ] || fail "could not read HomeCloud credentials from $data_dir"
echo "::add-mask::$secret"
{
  echo "AWS_ENDPOINT_URL=$endpoint"
  echo "AWS_ACCESS_KEY_ID=$key"
  echo "AWS_SECRET_ACCESS_KEY=$secret"
  echo "AWS_REGION=$region"
  echo "AWS_DEFAULT_REGION=$region"
  echo "HOMECLOUD_DATA_DIR=$data_dir"
} >> "$envf"
{
  echo "endpoint=$endpoint"
  echo "access-key-id=$key"
  echo "region=$region"
  echo "log-file=$log"
} >> "$out"
echo "Exported AWS_ENDPOINT_URL=$endpoint, AWS_ACCESS_KEY_ID=$key, AWS_REGION=$region"
