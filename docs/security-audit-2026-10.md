# Security audit, October 2026

Pre-launch review of HomeCloud before it is exposed on public VPSes. Scope, in priority order:
authentication (SigV4, sessions, sign-in, unsigned operations), authorization (actions and resources,
`iam:PassRole`, confused deputies), SSRF, container and host isolation, secrets handling and the web
console. Every fix below has a regression test and its own commit on the `security-audit` branch.

Findings marked *fixed* are closed. *Accepted* findings are left on purpose, with the reasoning. Nothing
in the VM code (`svc/ec2/vm*.go`, `svc/ec2/vm/`) was reviewed or changed.

## Summary

| # | Severity | Finding | Status |
| --- | --- | --- | --- |
| 1 | High | AWS handler buffered up to 100 MB of request body before checking the signature | Fixed |
| 2 | High | API Gateway `HTTP_PROXY` integrations were an unrestricted, response-reading proxy into the host | Fixed |
| 3 | High | Inline object views ran scripts with the viewer's session token in the URL | Fixed |
| 4 | High | Native ECS task definitions (and CloudFormation) skipped `iam:PassRole` | Fixed |
| 5 | High | Native EC2 launch, launch templates and Auto Scaling skipped `iam:PassRole` for instance profiles | Fixed |
| 6 | Medium | Sign-in throttle could be bypassed with parallel requests; user existence observable by timing | Fixed |
| 7 | Medium | Open redirect after console sign-in (`/\host`, `/<tab>/host`) | Fixed |
| 8 | Medium | `iam:PassRole` and target ARNs judged on the caller's spelling, dodging Deny statements | Fixed |
| 9 | Medium | Outbound webhook blocklist missed IPv6-embedded IPv4, metadata and host-local addresses | Fixed |
| 10 | Medium | Lambda packages could decompress to gigabytes at every cold start | Fixed |
| 11 | Medium | Unexpected errors returned Go error text (paths, Docker output) to clients and CloudTrail | Fixed |
| 12 | Medium | Lambda and ECS could run images of any private ECR repository without `ecr:` permissions | Fixed |
| 13 | Low | SNS subscription dead-letter queue needed no `sqs:SendMessage` | Fixed |
| 14 | Low | `?access_token=` accepted access key secrets; temporary credentials could mint new ones | Fixed |
| 15 | Low | SigV4 accepted requests that did not sign `host` / `x-amz-target` | Fixed |
| 16 | Low | Console could be framed (clickjacking); public routes accepted 64 MB bodies | Fixed |
| 17 | Info | `homecloud:CreateBackup` is account-root (the archive contains `master.key`) | Accepted |
| 18 | Info | Sealed secrets use one key with no per-purpose AAD | Accepted |
| 19 | Info | SigV4 requests can be replayed inside the 15 minute skew window | Accepted |
| 20 | Info | Step Functions machines without a role act as a trusted principal | Accepted |
| 21 | Info | RFC 1918 targets stay reachable by default for webhooks and proxy integrations | Accepted |
| 22 | Info | Docker pulls user-chosen images from arbitrary registries | Accepted |
| 23 | High | Website hosting served any key of a bucket whose policy merely mentioned `*` and `s3:GetObject` | Fixed |
| 24 | High | Function / HTTP API responses could replace the sandbox CSP and run script on the console origin | Fixed |
| 25 | Medium | `DeleteObjects` body keys skipped the `..` check | Fixed |
| 26 | Medium | Cognito: no per-account limit on password guesses, none on self sign-up | Fixed |
| 27 | Medium | SigV2 presigned URLs accepted unsigned sub-resources (`?retention`, ...) | Fixed |
| 28 | Low | Object-lock upload headers needed only `s3:PutObject` | Fixed |
| 29 | Low | A bucket named `lambda-code` broke `/lambda-code/` downloads | Fixed |
| 30 | Medium | New Cognito user pools allow open, auto-confirmed self sign-up | Accepted |
| 31 | Low | S3 Block Public Access settings are stored but not enforced | Accepted |
| 32 | Low | Cognito `ForgotPassword`/`ConfirmSignUp` reveal whether a user exists | Accepted |
| 33 | Low | SNS unsubscribe link needs only the subscription ARN | Accepted |
| 34 | Low | Self-signed TLS bootstrap can leave a certificate without its key after a crash | Accepted |
| 35 | Low | `CopyObject` source parsing may differ from MinIO's (not confirmed) | Accepted |

## Fixed

### 1. Body buffered before authentication (awsapi)

`awsapi.Handler` read up to `maxBody` (100 MB) into memory and hashed it before it looked at the
signature. Anyone who could reach the port could hold 100 MB per connection open with an unsigned
request or a signature naming a made-up access key. The handler now checks the signature's freshness and
the access key first, verifies the signature before reading the body whenever the client declared
`X-Amz-Content-Sha256` (and always for presigned URLs), and caps unsigned (public operation) bodies at
1 MB. Native public routes (sign-in, Cognito) use a 64 KB cap instead of 64 MB. Servers also got an idle
timeout and a header size limit. Tests: `awsapi/preauth_test.go`, `svc/iam/login_test.go`.

### 2. API Gateway HTTP_PROXY SSRF

`proxyHTTP` used a default `http.Client`, so a caller with `apigateway:*` on an API could point an
integration at loopback services, the host provider's metadata service or the Docker bridge and read
the responses through the public API route. It now uses `core.SafeClient`: the destination is checked
after DNS resolution at dial time (so rebinding and redirects cannot get around it), proxy environment
variables are ignored and redirects are not followed. Test: `svc/lambda/apigw_ssrf_test.go`.

### 9. Webhook blocklist

`core.blockedIP` now also covers IPv4-mapped, NAT64 and 6to4 forms, `0.0.0.0/8`, `100.100.100.200`,
`192.0.0.192`, `fd00:ec2::/32` and every address of the host itself (its public address and the Docker
bridge gateways, through which the HomeCloud API and Docker daemon are reachable). See accepted risk 21
for private ranges. Test: `core/targets_test.go`.

### 3. Script in inline object views could read the session token

The console opens an object with `?access_token=<session>` in the URL and the response was sandboxed with
`allow-scripts`, so an uploaded HTML object could read `location.search` and send the viewer's session
away: any user who can write to a bucket could take over an administrator who previewed the object.
Inline views now use `sandbox; default-src 'none'` (no scripts, no outbound loads) and API responses send
`Referrer-Policy: no-referrer`. Hosted websites, function URLs and HTTP APIs keep their scripts; their
URLs carry no token. Test: `server/sandbox_test.go`.

### 4 and 5. iam:PassRole on native routes

The PassRole check existed only on the AWS wire API. The native ECS task definition route, the native
EC2 launch route, launch templates carrying an instance profile and Auto Scaling groups launching from
such a template did not check it, so a user with `ecs:RegisterTaskDefinition` + `ecs:RunTask`, or
`ec2:RunInstances`, could run code holding any role that trusts the service and read its credentials.
CloudFormation uses the same native routes. The check now lives in `registerTaskDef` and
`ec2.PassProfile` and is applied on every path. Tests: `svc/ecs/passrole_test.go`,
`svc/autoscaling/passrole_test.go`.

### 8. Resource spelling

Policies were evaluated against the resource string as the caller wrote it. `role/dev-/admin` or the bare
name `admin` matched an Allow on `role/dev-*` and missed a Deny on `role/admin`, while IAM resolved
both to the role `admin`. `Principal.Permits` now canonicalizes the resource and IAM resolves role
references to the real ARN for `iam:PassRole`. Deliveries to EventBridge, scheduler, CloudWatch and Step
Functions targets and Secrets Manager rotation functions now reject ARNs of another account or region
instead of delivering to the same-named local resource. Tests: `svc/iam/passrole_spelling_test.go`,
`server/targets_arn_test.go`.

### 6. Sign-in throttle

The per-IP throttle counted a failure only after bcrypt had finished, so a burst of parallel guesses all
passed the check. The slot is now reserved before the password check and returned on success. Unknown
users pay the same bcrypt cost as known ones, so response time no longer reveals which user names exist.
Test: `svc/iam/login_test.go`.

### 7. Console open redirect

The post-sign-in `next` parameter refused `//host` but accepted `/\host` and `/<tab>/host`, which browsers
treat as `//host`. It is now rejected on backslashes and control characters and must resolve to the
console's own origin (`console/app/login/page.tsx`). The console has no test runner; the predicate was
checked against the crafted values with Node and `tsc --noEmit` passes.

### 10. Lambda decompression bombs

Packages were only checked to be valid zips and then unpacked in memory at every cold start, with a second
copy when loaded into the container. `checkZip` now sums the declared uncompressed sizes against the 250 MB
limit and caps the entry count, and `unzip` never reads more than the budget left. Test:
`svc/lambda/zipbomb_test.go`.

### 11. Internal error text

500 responses, SQS batch results and, through the error message, CloudTrail `LookupEvents` carried raw Go
errors (file paths, Docker daemon output, database errors). Clients now get a fixed message; the detail
stays in the server log. Test: `awsapi/preauth_test.go`.

### 12. ECR image pulls

Lambda container functions and ECS task definitions pulled from the local registry with no `ecr:` check,
so any user able to create a function or task could run and read another team's private repository. Both
now need `ecr:BatchGetImage` and `ecr:GetDownloadUrlForLayer` on the repository. Test:
`svc/ecs/passrole_test.go`, `core/targets_test.go`.

### 13. SNS dead-letter queue

A `RedrivePolicy` only had to name an existing queue; failed deliveries then wrote into it. Naming one now
needs `sqs:SendMessage` on it. Test: `svc/sns/aws_test.go`.

### 14. Token handling

`?access_token=` accepted `accessKeyId:secret`; only console session tokens (revocable, expiring) are accepted
in a URL now. `sts:GetSessionToken` refuses temporary credentials, as AWS does, so a stolen session cannot
be renewed forever. Tests: `svc/iam/login_test.go`.

### 15. SigV4 signed headers

Requests whose `SignedHeaders` omit `host`, or `x-amz-target` when it is sent, are rejected, as AWS does.
Test: `awsapi/preauth_test.go`.

### 16. Console headers

The console is served with `X-Frame-Options: DENY`, `frame-ancestors 'none'`, `object-src 'none'`,
`base-uri`/`form-action 'self'` and `Referrer-Policy: no-referrer`. A script-src policy is not set: the
statically exported Next.js pages rely on inline bootstrap scripts. Test: `web/web_test.go`.

### 23. Website hosting and bucket policies

`isPublic` looked for the strings `"*"` and `s3:GetObject` in MinIO's copy of the bucket policy and then
served every key with the server's storage credentials. A policy granting one prefix, or with a Deny or a
condition, exposed the whole bucket. Each requested key (index, redirect probe, error document) is now
evaluated against the bucket policy as an anonymous caller. Test: `svc/s3/website_security_test.go`.

### 24. Sandbox headers overridden by functions

`respond` copied the function's headers over the sandbox `Content-Security-Policy`, and the HTTP_PROXY relay
copied upstream headers, so a function author could serve script on the console origin to an administrator
who opened its URL. The sandbox and `nosniff` headers are re-applied when the response is written. Test:
`server/sandbox_test.go`.

### 25-29. S3 and Cognito hardening

`DeleteObjects` body keys get the `..`/length checks URL keys already had (25). Password and SRP sign-in count
attempts per account across all addresses, and each pool bounds self sign-ups per window (26). SigV2 URLs
carrying a sub-resource outside SigV2's signed set are refused (27). Retention and legal-hold upload headers
need `s3:PutObjectRetention` / `s3:PutObjectLegalHold` (28). `/lambda-code/` and `/_s3/` are never claimed by
anonymous S3 routing (29). Tests: `svc/s3/website_security_test.go`, `svc/cognito/signup_limit_test.go`.

## Accepted risks

**30. Open Cognito sign-up by default.** New pools auto-confirm self sign-ups so the emulator works out of the
box, like AWS's own developer setups. Anyone who knows a client ID can register and obtain tokens that verify
for the pool; an API Gateway JWT authorizer with no audience therefore accepts any of them. On a public server
turn off self sign-up (`SelfSignUp=false`) or auto-confirm for pools that guard real APIs, and set the
authorizer's audience.

**31. Block Public Access is cosmetic.** The four settings are stored and returned, but public bucket policies
still take effect. Do not rely on them; review bucket policies instead.

**32. Cognito existence signals.** Recovery and confirmation calls distinguish unknown users. Sign-in itself
does not, and the per-account and per-address limits bound enumeration.

**33. SNS unsubscribe links.** As in AWS, the link is authorized by knowing the subscription ARN.

**34. TLS bootstrap.** `--tls-self-signed` writes the certificate before the key; a crash in between needs
the `tls/` directory removed. No security impact.

**35. CopyObject source parsing.** HomeCloud and MinIO may decode `+` and `%3F` in `x-amz-copy-source`
differently. The difference could not be confirmed in MinIO's code from this repository. Exposure is limited
to principals who can already read some source object, and the cross-bucket test in `aws_security_test.go`
still passes.

**17. `homecloud:CreateBackup` is account-root.** `GET /api/v1/system/backup` streams an archive containing
`master.key`, the state file (sealed secrets) and volumes. It needs `homecloud:CreateBackup`, which only
administrator policies carry. Requiring the root user would break administrators who are not root, so the
install guide now says to treat the action as root-equivalent and to encrypt backups off the machine.

**18. One sealing key, no AAD.** `secrets.Seal` uses AES-256-GCM with random 96-bit nonces under one key for
every purpose. Swapping ciphertexts between fields needs write access to the state file, which already means
host compromise (and the key file next to it). Changing the format needs a migration; tracked for a later release.

**19. Replay.** As in AWS, a captured SigV4 request can be replayed within 15 minutes (presigned URLs until
they expire). The mitigation is TLS, which the install guide requires for anything off a trusted network.

**20. Role-less Step Functions machines.** They run as a trusted principal; each task's permission was checked
against the creator when the definition was saved, and changing the definition re-checks it against the editor.
Anyone with `states:StartExecution` can start it. AWS requires a role, which HomeCloud deliberately relaxes for
small setups. Use a role (and `iam:PassRole`) for machines that matter.

**21. Private ranges stay reachable.** Webhooks and proxy integrations to RFC 1918 and unique-local addresses
work by default because the typical user calls their own LAN and VPC. On a shared VPS set
`HOMECLOUD_DENY_PRIVATE_TARGETS=1`.

**22. Image pulls.** The Docker daemon pulls user-chosen image names from any registry. Registries are
contacted over HTTPS (plain HTTP only for loopback), the response is not returned to the caller, and the
daemon already runs with the privileges of the administrator who set it up.

## Reviewed, not an issue

- **Sessions and CSRF.** The console authenticates with `Authorization: Bearer` from local storage; there are
  no cookies, so there is no session fixation and no CSRF on native routes. Sessions are stored hashed, expire,
  and end on password change or user deletion. CORS `*` is safe for the same reason.
- **Passwords and keys.** bcrypt for console passwords; access key secrets stored as SHA-256 plus a sealed
  copy, compared in constant time; `master.key` from `crypto/rand`, mode 0600 in a 0700 directory.
- **CloudTrail records** hold method, path (no query), action, resource, source and error only: no request
  parameters or response elements, so no secret values or presigned URLs.
- **Path traversal.** Function, layer and snapshot names are validated before any path join; zip extraction
  rejects `..`, never creates symlinks and stages through memory; backup restore rejects `..` and absolute
  names; S3 keys never touch the host file system.
- **Container isolation.** Workloads get named volumes only: no bind mounts, `--privileged`, Docker socket,
  host network/PID or devices; ECS volumes and mount points are rejected.
- **CloudFormation `TemplateURL`** only extracts a bucket and key and reads through the caller's own
  credentials; the URL host is never dialed.
- **Console XSS.** React escapes all rendered names, tags and log lines; the only `dangerouslySetInnerHTML`
  is the chart theme built from static configuration.

## Reported for the VM worker (not touched)

None. The VM code was excluded from this audit and has not been reviewed; it needs its own pass before
VM-backed instances are offered on public servers. `svc/ec2/imds.go` (the metadata service, outside the VM
files) is guarded by a per-process key held by the helper container and looks correct.
