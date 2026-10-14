# Security policy

## Supported versions

HomeCloud is before 1.0. Security fixes go into the latest release only; there are no backports to
older minor versions.

| Version | Supported |
| --- | --- |
| Latest release (see [Releases](https://github.com/solinode/homecloud/releases)) | Yes |
| `main` | Yes, fixes land here first |
| Older releases | No: upgrade with `homecloud upgrade` |

## Verifying releases

From v0.5.0 on, every release is signed by the repository's release workflow
(`.github/workflows/release.yml`) with [Sigstore](https://www.sigstore.dev/) keyless signing: the
workflow gets a short-lived certificate for its GitHub identity, and each signature is recorded in the
public Rekor transparency log. No long-lived signing key exists. A release contains:

| Asset | What it is |
| --- | --- |
| `homecloud-<os>-<arch>.tar.gz` / `.zip` | the binaries |
| `homecloud-<os>-<arch>.spdx.json` | an SPDX SBOM of each binary (Go modules from its build info) |
| `checksums.txt` | SHA-256 of every archive and SBOM |
| `checksums.txt.sigstore.json` | the Sigstore bundle (signature, certificate, transparency log proof) over `checksums.txt` |

Plus GitHub build provenance (SLSA) for each archive, and for the image `ghcr.io/solinode/homecloud`: a
cosign signature, an SPDX SBOM attestation and build provenance.

The signer identity is always the release workflow at the release's tag:

```text
certificate identity: https://github.com/solinode/homecloud/.github/workflows/release.yml@refs/tags/<tag>
OIDC issuer:          https://token.actions.githubusercontent.com
```

**What checks it for you.** `homecloud upgrade` verifies the bundle against that identity (with
[sigstore-go](https://github.com/sigstore/sigstore-go), using Sigstore's trust root fetched through TUF)
and then each download against `checksums.txt`; it refuses a release whose signature is missing or does not
verify. Releases v0.4.1 and older were published before signing and install only with
`homecloud upgrade --version v0.4.1 --allow-unsigned`, which prints a warning. The install scripts
verify the signature when [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) is
installed, and otherwise the SHA-256 checksum only (they say which).

**By hand**, for example for v0.5.0 on Linux x86-64 (needs cosign 2.4 or newer and, for provenance, the
GitHub CLI):

```bash
V=v0.5.0
gh release download "$V" --repo solinode/homecloud \
  -p checksums.txt -p checksums.txt.sigstore.json -p homecloud-linux-amd64.tar.gz

# 1. checksums.txt was signed by the release workflow of this tag
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/solinode/homecloud/.github/workflows/release.yml@refs/tags/$V" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# 2. the archive matches it
sha256sum --ignore-missing -c checksums.txt     # macOS: shasum -a 256 --ignore-missing -c checksums.txt

# 3. optional: GitHub build provenance of the archive
gh attestation verify homecloud-linux-amd64.tar.gz --repo solinode/homecloud \
  --signer-workflow solinode/homecloud/.github/workflows/release.yml
```

The container image:

```bash
IMG=ghcr.io/solinode/homecloud:0.5.0
ID="https://github.com/solinode/homecloud/.github/workflows/release.yml@refs/tags/v0.5.0"
ISSUER=https://token.actions.githubusercontent.com

cosign verify "$IMG" --certificate-identity "$ID" --certificate-oidc-issuer "$ISSUER"
cosign verify-attestation "$IMG" --type spdxjson --certificate-identity "$ID" --certificate-oidc-issuer "$ISSUER" \
  | jq -r .payload | base64 -d | jq .predicate > image.spdx.json     # the SBOM
gh attestation verify "oci://$IMG" --repo solinode/homecloud
```

The image SBOM describes the image as resolved for linux/amd64; both architectures are built from the same
sources and Alpine packages.

## Reporting a vulnerability

Please **do not open a public issue, pull request or Discord message** for a security problem.

Report it privately through GitHub's security advisories:
**[Report a vulnerability](https://github.com/solinode/homecloud/security/advisories/new)**
(Security tab, "Report a vulnerability"). Only the maintainers can see the report.

A useful report includes:

- the HomeCloud version (`homecloud version`) and how it is run (OS, Docker Engine / Docker Desktop /
  OrbStack, flags such as `--addr`, TLS, a reverse proxy);
- what an attacker needs (network access to the API, an IAM user with which permissions, a function or
  instance they control, ...);
- steps to reproduce, ideally an AWS CLI, boto3 or `curl` sequence;
- the impact you expect (privilege escalation, reading another user's data, escaping a container, ...).

## What to expect

HomeCloud is maintained by volunteers, so these are goals rather than guarantees:

- an acknowledgement within a few days;
- an initial assessment (confirmed or not, and severity) after that, discussed in the private advisory;
- a fix in a new release, with a GitHub security advisory and a CHANGELOG entry crediting you unless you
  prefer otherwise. We ask that you keep the details private until the fixed release is out.

## Scope

In scope: the `homecloud` binary (server, CLI, embedded console), the install scripts, and the release
artifacts. Of particular interest: authentication (SigV4, sessions, unsigned Cognito operations), IAM
authorization and confused-deputy paths between services, SSRF through webhooks and integrations, isolation
between tenants' resources, and anything that reaches the host from inside a function, task or instance.

Known and accepted behavior is not a vulnerability by itself. In particular:

- HomeCloud needs the Docker socket, which is root-equivalent on the host. HomeCloud administrators (the root
  user and anyone with broad IAM permissions) are host administrators.
- Instances are containers that share the host kernel; they are not a VM boundary.
- The accepted findings of the [October 2026 security audit](docs/security-audit-2026-10.md) (for example
  SigV4 replay within the clock-skew window, or RFC 1918 webhook targets being allowed by default).

The audit report describes the threat model, what was reviewed and what was fixed. Hardening advice for
servers is in [Install on a server](docs/install-server.md#13-security-notes).
