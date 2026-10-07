# How HomeCloud compares

HomeCloud is one of several ways to run "AWS without AWS". This page tries to be honest about where it
fits, where it is the better choice, and where something else is. Other projects change quickly: check
their current documentation and licensing before deciding, and tell us (an issue or a pull request) if
anything here is out of date.

## The short version

- **You want AWS APIs with real compute behind them, a console, and IAM enforced, on your own machine or
  server, for free:** HomeCloud.
- **You need the widest possible AWS service coverage for local testing, or services HomeCloud lacks
  (Kinesis, EKS, Athena, REST API Gateway, ...):** LocalStack, Pro for most of those.
- **You want fast, in-process mocks for unit tests in Python:** moto.
- **You only need S3-compatible storage, plus Kubernetes for compute:** MinIO and k3s.
- **You need a multi-node private cloud with real VMs, and have people to run it:** OpenStack.
- **You need exactly AWS's behavior:** AWS itself (free tier, or a sandbox account).

## At a glance

| | HomeCloud | LocalStack Community | LocalStack Pro | moto (server mode) | MinIO + k3s | OpenStack | AWS free tier |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Speaks AWS APIs | Yes, about 30 services | Yes, core services | Yes, the most services of any emulator | Yes, very broad API surface | S3 only (MinIO) | Its own APIs; S3 through Swift middleware | It is AWS |
| What runs behind the API | Real engines in Docker: Lambda runtime images, PostgreSQL/MySQL/MariaDB, Redis/Valkey, MinIO, nginx, CoreDNS | Emulation; some services start real containers (for example Lambda) | Emulation plus real engines for several services | Mostly in-memory models | Real storage and real Kubernetes | Real VMs, networks, volumes | Real AWS |
| IAM enforcement | On by default, same evaluator for every service | Not enforced | Available, opt-in | Partial | MinIO policies only | Keystone (not IAM) | Yes |
| Web console | Built in, modeled on AWS's | Hosted web app, needs an account | Hosted web app | Basic dashboard | MinIO console, Kubernetes dashboards | Horizon | AWS console |
| Persistence | State on disk by default; backup and restore commands | Ephemeral by default | Persistence and snapshots | In memory | Yes | Yes | Yes |
| Deployment | One Go binary plus Docker | One container | One container plus a licence | Python package or container | Two systems to install and wire | Many services, multi-node | None |
| Multi-node | No (designed, not built) | No | No | No | k3s yes; MinIO distributed mode | Yes | Yes |
| Regions / accounts | One region, one account | Many | Many | Many | n/a | n/a | All |
| Cost and licence | Free, AGPL-3.0 | Free tier; check current terms | Paid subscription | Free, Apache-2.0 | Free (AGPL MinIO, Apache k3s); see MinIO note below | Free, Apache-2.0 | Free within limits, then billed |
| Maturity | Young: first release September 2026 | Mature, large community | Mature, commercial support | Mature, widely used | Mature | Mature | n/a |

"Yes" for a feature never means "the same as AWS"; every emulator, HomeCloud included, has gaps. HomeCloud's
are listed per service in [aws-compat.md](aws-compat.md) and [architecture.md](architecture.md#limits-and-differences-from-aws).

## LocalStack (Community and Pro)

LocalStack is the best-known AWS emulator and the closest comparison.

**Where HomeCloud is better**

- **Real workloads by default.** RDS gives you a real PostgreSQL, MySQL or MariaDB; ElastiCache a real Redis
  or Valkey; EC2 instances are containers you can SSH into; ECS services sit behind a real load balancer;
  security groups really filter traffic between them. With LocalStack Community several of these services
  are not available, and with Pro the depth varies per service.
- **IAM is always on.** Every request, from the AWS CLI, the SDKs, Terraform or the console, goes through the
  same policy evaluator with resource policies, conditions, permissions boundaries and `iam:PassRole`. Tests
  that pass against HomeCloud have at least been checked for the permissions they need.
- **A console in the box.** The web console ships inside the binary and works offline, with metrics, logs
  and a CloudTrail audit trail of API calls.
- **Persistent and multi-user.** It is built to be left running for a team or a classroom: users, roles and
  access keys, backups, upgrades, TLS, a system service.
- **Fully open source.** Every feature is in the AGPL-3.0 code; there is no paid tier.

**Where LocalStack is better**

- **Service coverage.** LocalStack Pro covers far more AWS services and operations than HomeCloud. If your
  stack uses Kinesis, EKS, Athena, Glue, OpenSearch, REST API Gateway, WebSocket APIs, AppSync or similar,
  HomeCloud does not have them.
- **Multi-region and multi-account** setups work; HomeCloud has one region (`us-east-1`) and one account.
- **Startup and footprint for CI.** A single container that emulates most services in-process is light;
  HomeCloud starts real engines (MinIO on first boot, databases and runtimes on demand), which takes longer
  and uses more memory.
- **Ecosystem and maturity.** Years of production use, `tflocal`/`cdklocal`/`awslocal` wrappers, testing
  integrations, extensive docs and commercial support. HomeCloud is young.

## moto (server mode)

moto is a Python library that mocks AWS; `moto_server` exposes the same mocks over HTTP.

**HomeCloud is better** when the test needs something to actually run: a Lambda function on the real runtime,
a database you can query, a queue that triggers a function, traffic through a load balancer. It is also a
long-running server with a console and persistent state, which moto is not meant to be.

**moto is better** for fast, isolated unit tests: it starts in milliseconds, resets between tests, runs
in-process in Python (`@mock_aws`) and models a very wide range of services and operations. For a unit test
of code that calls `put_item`, moto is the simpler tool.

## MinIO, optionally with k3s

MinIO is an excellent S3-compatible object store (HomeCloud uses it for S3), and k3s is a light Kubernetes.

**HomeCloud is better** if your code expects AWS APIs beyond S3: IAM, SQS, SNS, Lambda, DynamoDB, RDS,
CloudFormation and the rest are not something MinIO or Kubernetes provide. HomeCloud gives you those
behind one endpoint and one set of credentials.

**MinIO and k3s are better** if you want Kubernetes-native workloads, need S3 at scale (distributed, erasure
coded MinIO across many disks and nodes), or don't need the rest of the AWS API. Note that MinIO's free
distribution has changed: it no longer publishes free container images, which is why HomeCloud runs
Chainguard's build of the same server (see the [changelog](../CHANGELOG.md), 0.1.1).

## OpenStack

OpenStack is a full private cloud: real VMs, software-defined networks, block and object storage, across
many machines.

**HomeCloud is better** for AWS tooling and for size: OpenStack has its own APIs (Nova, Neutron, Cinder,
Keystone), so Terraform's AWS provider, boto3 and the AWS CLI don't target it, and it takes a team to install
and operate. HomeCloud is one binary on one Docker host, with AWS's managed services (Lambda, SQS, DynamoDB,
RDS, ...) that OpenStack does not offer in AWS form.

**OpenStack is better** for real infrastructure: multi-node clusters, live migration, hardware-virtualized
VMs with strong isolation, quotas and multi-tenancy at scale. HomeCloud instances are containers by default
(two VM images boot real QEMU/KVM guests, without live migration), and it runs on a single node.

## The AWS free tier

**AWS is better** at being AWS: every service, every region, exact behavior, and what you test is what you
ship. For final integration or staging tests, nothing replaces it.

**HomeCloud is better** when you can't or shouldn't use a real account: offline or air-gapped environments,
classrooms where students would each need an account and a payment card, CI that should not hold cloud
credentials or risk a bill, and experiments you want to break and reset freely. The free tier covers limited
usage of some services for a limited time (AWS has changed its terms for new accounts, so check the current
ones); anything beyond that is billed.

## HomeCloud's limits, in one place

- **Single node.** One Docker host runs everything; [multi-node clusters](design/multi-node.md) are designed,
  not built.
- **One region, one account.** Cross-region features (replication, multi-Region keys' replicas) are refused.
- **Smaller service coverage** than LocalStack Pro, and a subset of operations within each service
  ([aws-compat.md](aws-compat.md)).
- **Instances are containers** sharing the host kernel, except the two VM images (Ubuntu 24.04, Debian 12), which run as QEMU guests.
- **Young project.** The first release was in September 2026. Expect rough edges, and please
  [report them](https://github.com/solinode/homecloud/issues/new/choose).
- **Docker socket access** is root-equivalent on the host, so HomeCloud administrators are host administrators.
