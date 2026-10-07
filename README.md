# HomeCloud

**A self-hosted, AWS-compatible cloud in one binary: point your real Terraform, AWS CLI and SDK code at it, and it runs on real containers on your machine.**

<p align="center">
  <a href="https://homecloud.pages.dev/demo/"><b>Live demo console</b></a> ·
  <a href="#quick-start"><b>Quick start</b></a> ·
  <a href="docs/aws-compat.md"><b>AWS compatibility</b></a> ·
  <a href="docs/comparison.md"><b>vs. LocalStack, moto, MinIO…</b></a> ·
  <a href="https://discord.gg/pemra9uaC9"><b>Discord</b></a>
</p>

- **Your AWS code, unchanged.** HomeCloud speaks the AWS wire protocols (SigV4, awsJson, awsQuery, REST). Set `AWS_ENDPOINT_URL` and the AWS CLI, boto3 and the Terraform AWS provider work against it, with IAM policies enforced as on AWS.
- **Real compute, not mocks.** Lambda runs on AWS's official runtime images, RDS is a real PostgreSQL/MySQL/MariaDB, S3 is MinIO, EC2 instances are containers you can shell into, load balancers are nginx, security groups are iptables rules. Your integration tests hit the same kind of thing production does.
- **See what happened.** A web console modeled on AWS's, CloudWatch logs and metrics for every resource, and a CloudTrail record of every AWS API call, so a failed test run can be inspected instead of guessed at.

Built for **developers who want a real AWS-compatible target for local development and CI**. It also suits **college labs** teaching AWS without accounts or bills, and **small teams and homelabs** that want AWS tooling on their own hardware.

<p align="center">
  <img src="docs/images/console-home.png" alt="HomeCloud console home" width="100%">
</p>

---

## Quick start

You need **Docker** (Docker Engine on Linux, Docker Desktop or OrbStack on macOS and Windows).

```bash
curl -fsSL https://homecloud.pages.dev/scripts/install.sh | sh   # Linux / macOS
homecloud serve
```

<details><summary>Windows (PowerShell)</summary>

```powershell
irm https://homecloud.pages.dev/scripts/install.ps1 | iex
homecloud serve
```
</details>

On first start HomeCloud creates your account, prints the **root console password** once, and writes CLI credentials to `~/.homecloud/credentials`. Open **http://127.0.0.1:8080** and sign in as `root`. The first start pulls container images (MinIO, and later the engines you use), so it takes longer than the next ones.

Then, in another terminal, use your normal AWS tools:

```bash
eval "$(homecloud aws-env)"        # sets AWS_ENDPOINT_URL, access keys and region
aws sts get-caller-identity
aws s3 mb s3://demo && echo hi | aws s3 cp - s3://demo/hello.txt
aws sqs create-queue --queue-name jobs
```

**Or with Docker only**, nothing to install:

```bash
docker run -d --name homecloud -p 127.0.0.1:8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock -v homecloud-data:/data \
  ghcr.io/solinode/homecloud
docker logs homecloud                                # the root console password (once)
eval "$(docker exec homecloud homecloud aws-env)"   # point the AWS CLI at it
```

HomeCloud starts its services as containers next to its own on the same Docker host; mounting the socket gives it control of that host, as running the binary does. See [Docker](docs/install-server.md#docker) in the server guide for exposing it, TLS and upgrades.

Other ways to install: build from source with Go 1.25+ and Node.js 22+ (`make`, binary in `bin/homecloud`), or follow **[Install on a server](docs/install-server.md)** for a VPS or home server (system service, TLS, firewall, backups).

### In CI

HomeCloud's own end-to-end job runs it on a stock GitHub Actions runner. The same pattern works for your tests:

```bash
curl -fsSL https://homecloud.pages.dev/scripts/install.sh | sh
nohup homecloud serve > homecloud.log 2>&1 &
for i in $(seq 1 60); do curl -fsS localhost:8080/api/v1/health && break; sleep 2; done
eval "$(homecloud aws-env)"
# ... terraform apply / pytest / your test suite ...
```

S3 starts in the background on first boot; if your tests use S3 right away, wait for `s3: MinIO ready` in the log (see [`.github/workflows/ci.yml`](.github/workflows/ci.yml)).

---

## Works with

| Tool | Status |
| --- | --- |
| **AWS CLI v2** | Tested: the compatibility test suite drives the real `aws` CLI in CI |
| **boto3** (Python SDK) | Tested: the compatibility test suite runs boto3 in CI, including Cognito SRP sign-in through pycognito |
| **Terraform / OpenTofu** (`hashicorp/aws` provider) | Tested: opt-in Terraform tests in the suite (IAM, S3, Route 53, CloudFormation and more; `HC_TEST_TERRAFORM=1`), and [`examples/terraform/shop`](examples/terraform/shop/README.md) applies, re-plans clean and destroys on a fresh install |
| **CloudFormation** (`aws cloudformation deploy`) | Tested: stacks, change sets and the boto3 waiters ([details](docs/aws-compat.md#cloudformation)) |
| **AWS CDK** | Partly: CDK-synthesized templates deploy through CloudFormation change sets; `cdk bootstrap` / `cdk deploy` end to end is not yet verified |
| **Other AWS SDKs** (JavaScript, Go, Java, …) and **Pulumi** | Expected to work (same protocols and SigV4), not yet covered by tests. Reports welcome |

Run `homecloud aws-env` to get the environment variables. For Terraform, point the provider's `endpoints {}` block at HomeCloud and use `s3_use_path_style = true`; the [shop example](examples/terraform/shop/README.md) shows a complete provider block.

---

## Services

"AWS API" means the AWS CLI, SDKs and Terraform can manage it; every service is also in the console, the `homecloud` CLI and the [native REST API](docs/api.md). The notes name the main gaps; [docs/aws-compat.md](docs/aws-compat.md) lists operations and differences per service.

| Service | AWS equivalent | Status | Notes |
| --- | --- | --- | --- |
| Compute | EC2, EBS, AMIs | AWS API | Instances are **containers**, not VMs (VM-backed instances are in progress, [#3](https://github.com/solinode/homecloud/issues/3)). Key pairs, user data, volumes, snapshots, launch templates, Elastic IPs (records only), IMDSv1/v2, browser shell |
| Auto Scaling | EC2 Auto Scaling | AWS API | Target tracking on CPU; scheduled actions and lifecycle hooks are not implemented |
| Networking | VPC, security groups | AWS API | Security groups enforced inside the VPC; network ACLs recorded, not enforced; no peering; IPv4 only |
| Load balancing | ELB v2 | AWS API | Application load balancers (HTTP/HTTPS, path/host rules). No network load balancers |
| DNS | Route 53 | AWS API | Public and private zones served by CoreDNS; no routing policies or health checks |
| Certificates | ACM | AWS API | Issued from HomeCloud's private CA, not a public one |
| Object storage | S3 | AWS API | MinIO underneath; versioning, lifecycle expiry, presigned URLs, bucket policies, websites |
| Shared files | EFS | AWS API | Docker volumes mounted into instances and tasks; no NFS endpoint |
| Containers | ECS (Fargate), ECR | AWS API | One container per task definition |
| Databases | RDS | AWS API | PostgreSQL, MySQL, MariaDB; no Multi-AZ, read replicas or Aurora |
| Caches | ElastiCache | AWS API | Redis, Valkey, Memcached; one node per cluster, no TLS |
| Document DB | DocumentDB-style MongoDB | Native API only | MongoDB through the console, CLI and native API |
| Functions | Lambda | AWS API | AWS runtime images, versions, aliases, layers, async invoke, SQS and DynamoDB stream triggers, function URLs |
| HTTP APIs | API Gateway v2 | AWS API | HTTP APIs with Lambda/HTTP proxy and JWT authorizers; REST and WebSocket APIs are not supported |
| Queues | SQS | AWS API | Standard and FIFO, DLQs and redrive |
| Pub/sub | SNS | AWS API | SQS, Lambda and HTTP(S) subscriptions with filter policies; e-mail and SMS messages are written to the server log, not sent |
| Key-value | DynamoDB | AWS API | Expressions, GSIs/LSIs, transactions, PartiQL, TTL, streams |
| Workflows | Step Functions | AWS API | Lambda, SQS and SNS tasks; no activities |
| Events | EventBridge, Scheduler | AWS API | Buses, rules, schedules; no archives or replays |
| Identity | IAM, STS | AWS API | Policies with conditions, roles, temporary credentials, permissions boundaries, simulator |
| App identity | Cognito user pools | AWS API | SRP and password sign-in, JWTs; no MFA or hosted UI; codes go to the server log instead of e-mail/SMS |
| Secrets and keys | Secrets Manager, KMS, SSM Parameter Store | AWS API | Single region, so replication and multi-Region replicas are refused |
| Monitoring | CloudWatch, CloudWatch Logs | AWS API | Metrics, metric math, alarms, Logs Insights; anomaly bands are a statistical approximation, not AWS's model |
| Infrastructure as code | CloudFormation | AWS API | Change sets, updates with rollback; no nested stacks, custom resources or `AWS::Serverless` |
| Audit | CloudTrail | AWS API | Every AWS-protocol call and every mutating or denied native call; trails deliver to S3 |

Overall limits: HomeCloud runs on **one Docker host** and serves **one region** (`us-east-1`) and one account. Multi-node clusters are a [design](docs/design/multi-node.md) ([#55](https://github.com/solinode/homecloud/issues/55)), not a feature.

---

## The console

Built into the binary: run `homecloud serve` and open http://127.0.0.1:8080, or **[try the demo](https://homecloud.pages.dev/demo/)** in your browser (sample data, runs entirely client-side, nothing to install).

<table>
  <tr>
    <td width="50%"><img src="docs/images/ec2-instances.png" alt="EC2 instances"><p align="center"><b>EC2</b>: instances in your VPCs</p></td>
    <td width="50%"><img src="docs/images/lambda-function.png" alt="Lambda function"><p align="center"><b>Lambda</b>: functions, versions, aliases, in-browser editor</p></td>
  </tr>
  <tr>
    <td><img src="docs/images/s3-bucket.png" alt="S3 bucket"><p align="center"><b>S3</b>: buckets, objects, presigned links</p></td>
    <td><img src="docs/images/iam-role.png" alt="IAM role"><p align="center"><b>IAM</b>: users, roles and policies</p></td>
  </tr>
  <tr>
    <td><img src="docs/images/dynamodb-table.png" alt="DynamoDB table"><p align="center"><b>DynamoDB</b>: tables, queries, item editor</p></td>
    <td><img src="docs/images/cloudwatch.png" alt="CloudWatch"><p align="center"><b>CloudWatch</b>: metrics, alarms and logs</p></td>
  </tr>
</table>

---

## The `homecloud` CLI

Besides the AWS tools, HomeCloud has its own shorter CLI for every service:

```bash
homecloud ec2 run --name web --image ami-nginx --sg sg-xxxx   # an instance
homecloud rds create orders-db --engine postgres --public     # a database, password in Secrets Manager
homecloud lambda create resize --runtime python3.12 --code ./resize
homecloud sqs create jobs --dlq jobs-dlq && homecloud lambda trigger resize jobs
homecloud cfn create pipeline docs/examples/pipeline.yaml -p Env=dev
homecloud api GET /api/v1/cloudwatch/alarms                    # anything else, raw
```

Run `homecloud --help` or `homecloud <service> --help` for every command. Operations: `homecloud service install` (launchd or systemd), `homecloud backup` / `restore`, `homecloud upgrade`, `homecloud doctor`.

To reach the server from other machines, bind it to a LAN or Tailscale address with TLS:

```bash
homecloud serve --addr 0.0.0.0:8080 --public-host homelab.tailnet.ts.net --tls-self-signed
```

---

## Documentation

- [AWS compatibility](docs/aws-compat.md): supported operations per service, IAM behavior, differences from AWS
- [Comparison](docs/comparison.md) with LocalStack, moto, MinIO, OpenStack and the AWS free tier
- [Architecture](docs/architecture.md): how each service is built, where state lives, limits
- [Install on a server](docs/install-server.md): VPS or home server, TLS, firewall, backups, upgrades
- [Native API reference](docs/api.md)
- [Security audit, October 2026](docs/security-audit-2026-10.md) and the [security policy](SECURITY.md)
- Designs: [multi-node clusters](docs/design/multi-node.md), [edge compute](docs/design/edge.md)
- [Changelog](CHANGELOG.md)

## Security

HomeCloud needs the Docker socket, which is root-equivalent on the host: treat HomeCloud administrators as host administrators. The API binds to `127.0.0.1` by default. Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## Roadmap

- **In progress:** VM-backed instances with QEMU/KVM ([#3](https://github.com/solinode/homecloud/issues/3)), a container image for one-command starts
- **Planned:** multi-node clusters ([#55](https://github.com/solinode/homecloud/issues/55)), edge compute and hardware integrations ([#56](https://github.com/solinode/homecloud/issues/56))

See the [open issues](https://github.com/solinode/homecloud/issues) for everything planned.

## Contributing

Start with **[CONTRIBUTING.md](CONTRIBUTING.md)**: how to build, run the tests and pick up a [good first issue](https://github.com/solinode/homecloud/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22). Missing an AWS operation your code needs? Open a [compatibility gap](https://github.com/solinode/homecloud/issues/new?template=compatibility_gap.yml) issue. By contributing you agree to the [Contributor License Agreement](CLA.md) and the [Code of Conduct](CODE_OF_CONDUCT.md).

Chat with us on **[Discord](https://discord.gg/pemra9uaC9)**.

### Support partners

<p>
  <a href="https://tailscale.com/"><img src="https://logovectorseek.com/wp-content/uploads/2023/04/tailscale-inc-logo-vector.png" alt="Tailscale" height="50"/></a>
  &nbsp;&nbsp;
  <a href="https://coderabbit.ai/"><img src="https://sindresorhus.com/assets/thanks/coderabbit-logo.png" alt="CodeRabbit" height="30"/></a>
</p>

## License

[GNU AGPL-3.0](LICENSE). If you run a modified HomeCloud as a service for others, share your changes.
