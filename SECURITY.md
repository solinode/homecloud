# Security policy

## Supported versions

HomeCloud is before 1.0. Security fixes go into the latest release only; there are no backports to
older minor versions.

| Version | Supported |
| --- | --- |
| Latest release (see [Releases](https://github.com/solinode/homecloud/releases)) | Yes |
| `main` | Yes, fixes land here first |
| Older releases | No: upgrade with `homecloud upgrade` |

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
