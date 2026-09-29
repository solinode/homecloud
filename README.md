# ☁️ HomeCloud — The Cloud, Owned by You

**An open-source, self-hosted cloud platform.** HomeCloud runs AWS-style services (compute, object storage, managed databases, serverless functions, queues, pub/sub, key-value tables, networking, identity, monitoring) on your own hardware, from one binary, managed through a web console, a CLI and a REST API.

No third parties. No vendor lock-in. No surprise billing.

> 🛡 Built with privacy, transparency, and sovereignty at its core.

#### Support Partners

<p align="center" >
  <a href="https://tailscale.com/">
    <img src="https://logovectorseek.com/wp-content/uploads/2023/04/tailscale-inc-logo-vector.png"
         alt="Tailscale" height="70"/>
  </a>
</p>

<p align="center">
  <a href="https://coderabbit.ai/">
    <img src="https://sindresorhus.com/assets/thanks/coderabbit-logo.png"
         alt="Coderabbit" height="40"/>
  </a>
</p>

---

## 🧰 Services

| Service | AWS equivalent | What you get |
| --- | --- | --- |
| **Compute** | EC2, EBS, AMIs | Instances with CPU/memory limits from `t3.nano` to `r5.large`, 10 base images (Ubuntu, Debian, Amazon Linux, Rocky, Fedora, Alpine, …), user-data scripts, start/stop/reboot/resize, persistent volumes, capture an instance as a new image, run-command, and a shell in the browser |
| **Auto Scaling** | EC2 Auto Scaling | Groups that keep a desired number of instances across subnets, replace unhealthy ones, register them with load balancers and scale on CPU or memory targets |
| **Shared files** | EFS | File systems that any number of instances mount at the same time |
| **Containers** | ECS (Fargate), ECR | Versioned task definitions with secrets injected from Secrets Manager, services that keep N tasks running with rolling deployments and load-balancer registration, one-off tasks, and a private image registry you `docker push` to |
| **Networking** | VPC, ELB | VPCs and subnets with real private IP addressing, private DNS (`ip-10-88-0-4.internal`, `mydb.rds.internal`), security groups that decide which ports are published; application load balancers with HTTP/HTTPS listeners, HTTP→HTTPS redirects, path/host routing rules, target groups and health checks |
| **DNS** | Route 53 | Public and private hosted zones (A, AAAA, CNAME, TXT, MX, SRV, CAA, NS, PTR), alias records that follow instances, tasks, databases and load balancers; a resolver at each VPC's `.2` address and a LAN-facing DNS port |
| **Certificates** | ACM | A private certificate authority that issues TLS certificates for your domains and IPs, import of Let's Encrypt or other certificates, renewal, and HTTPS on load balancers |
| **Object storage** | S3 | Buckets, folders, uploads/downloads, versioning, public-read access, lifecycle expiry, presigned URLs, static website hosting, and a fully S3-compatible endpoint for AWS SDKs and `aws` CLI |
| **Databases** | RDS, ElastiCache, DocumentDB | PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey, Memcached with generated credentials in Secrets Manager, snapshots and restore, daily automated backups, resizing, password rotation and a query editor |
| **Serverless** | Lambda, API Gateway | Python 3.11–3.13 and Node.js 20/22 functions with warm environments, env vars, timeouts, logs and metrics; public function URLs; HTTP APIs with path parameters; SQS triggers with partial-batch failures |
| **Queues** | SQS | Standard and FIFO queues, visibility timeouts, delays, long polling, dead-letter queues with redrive, batches |
| **Pub/sub** | SNS | Topics fanning out to queues, functions and HTTP(S) webhooks, raw delivery, filter policies |
| **Key-value** | DynamoDB | Tables with partition/sort keys, range queries, scans with filters, secondary indexes, conditional writes, atomic counters, TTL |
| **Workflows** | Step Functions | State machines in Amazon States Language: Task (Lambda, SQS, SNS), Choice, Wait, Parallel, Map, Pass, Succeed, Fail, with Retry/Catch, JSONPath input/output processing, intrinsic functions and a full execution history |
| **Events** | EventBridge | Scheduled rules (`rate(...)`, `cron(...)`), event buses with pattern matching, targets in Lambda/SQS/SNS |
| **Identity** | IAM | Users, groups, managed and custom JSON policies with allow/deny and resource ARNs, access keys, console passwords, a policy simulator |
| **App identity** | Cognito | User pools with sign-up, sign-in, refresh tokens, forced password changes, groups and global sign-out; RS256 JWTs with a JWKS endpoint; API Gateway routes can require them |
| **Secrets & keys** | Secrets Manager, KMS, SSM Parameter Store | Versioned secrets, customer keys with encrypt/decrypt, data keys, rotation and encryption context, hierarchical configuration parameters with SecureString values |
| **Monitoring** | CloudWatch | Per-resource CPU/memory/network/disk metrics, custom metrics, alarms that notify SNS topics or webhooks, log groups for every function and container |
| **Infrastructure as code** | CloudFormation | YAML/JSON stack templates for 30 resource types across every service, with parameters, outputs, `!Ref`/`!GetAtt`/`!Sub`/`!Join`, dependency ordering, readiness waits, rollback, updates and ordered deletion |
| **Audit** | CloudTrail | A record of every change and every denied request, with who, what, when and from where |

Everything runs as containers on Docker, labelled so HomeCloud never touches containers it didn't create. See **[docs/architecture.md](docs/architecture.md)** for how each service is built and **[docs/api.md](docs/api.md)** for the full API reference.

---

## 🚀 Quick start

You need **Docker** (Docker Engine on Linux, or Docker Desktop / OrbStack on macOS and Windows).

### Install

**Linux / macOS**

```bash
curl -fsSL https://homecloud.drk1rd.systems/scripts/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://homecloud.drk1rd.systems/scripts/install.ps1 | iex
```

Or build from source with Go 1.23+ and Node.js 20+: `make` (the binary lands in `bin/homecloud`).

### Run

```bash
homecloud serve
```

On first start HomeCloud creates your account, prints the **root console password** once, and writes CLI credentials to `~/.homecloud/credentials`. Open **http://127.0.0.1:8080** and sign in as `root`.

To reach it from other machines, bind to your LAN or Tailscale address (add `--tls-self-signed`, or `--tls-cert`/`--tls-key`, to serve HTTPS):

```bash
homecloud serve --addr 0.0.0.0:8080 --public-host homelab.tailnet.ts.net --tls-self-signed
```

### Use it from the CLI

```bash
# Compute: a web server whose port 80 is published
homecloud vpc create-sg web && homecloud vpc allow sg-xxxx 80
homecloud ec2 run --name web --image ami-nginx --sg sg-xxxx
homecloud ec2 ssh i-xxxx

# Storage
homecloud s3 mb s3://photos
homecloud s3 cp ./beach.jpg s3://photos/2025/
homecloud s3 presign s3://photos/2025/beach.jpg

# A PostgreSQL database; the password lives in Secrets Manager
homecloud rds create orders-db --engine postgres --public
homecloud rds query orders-db "select version()"
homecloud rds password orders-db

# Serverless: a function behind an HTTP API, fed by a queue
homecloud lambda create resize --runtime python3.12 --code ./resize
homecloud lambda url resize
homecloud sqs create jobs --dlq jobs-dlq
homecloud lambda trigger resize jobs

# Schedules and events
homecloud events schedule nightly 'cron(0 3 ? * * *)' --target arn:hc:lambda:local-1:<account>:function:resize

# Containers behind a load balancer
docker tag myapi localhost:5500/myapi:1 && docker push localhost:5500/myapi:1
homecloud elb create-target-group api-tg --port 8000
homecloud elb create web --listen 80=api-tg
homecloud ecs register api --image localhost:5500/myapi:1 --port 8000 --secret DB_PASS=prod/db:password
homecloud ecs create-service api api --count 3 --target-group api-tg

# Infrastructure as code (see docs/examples/pipeline.yaml)
homecloud cfn create pipeline docs/examples/pipeline.yaml -p Env=dev
homecloud cfn delete pipeline

# Anything else
homecloud api GET /api/v1/cloudwatch/alarms
```

Run `homecloud --help` or `homecloud <service> --help` for every command.

### Use it from AWS tools

The S3 endpoint speaks the S3 protocol, so existing tools work unchanged:

```bash
homecloud s3 credentials          # endpoint + keys
aws --endpoint-url http://localhost:9500 s3 ls
```

---

## 🗺️ Roadmap

* ✅ **Phase 1:** Core cloud stack: compute, storage, networking, web console, CLI and API
* ✅ **Phase 2:** Serverless and event-driven services: Lambda, API Gateway, SQS, SNS, EventBridge, DynamoDB
* ✅ **Phase 3 (first cut):** Observability and governance: CloudWatch metrics/logs/alarms, CloudTrail, IAM, Secrets Manager
* ✅ **Containers:** ECS services and tasks, ECR registry, load balancers, shared file systems
* ✅ **Workflows:** Step Functions
* ✅ **Infrastructure as code:** CloudFormation-style stacks
* 🔄 **Next:** VM-backed instances (QEMU/KVM), multi-node clusters, a service catalog for one-click apps
* 🔄 **Phase 4:** Edge compute and hardware integrations

📍 **[Explore the full roadmap](https://github.com/orgs/homecloudhq/projects/1/views/1)**

---

## 🧑‍💻 Development

```
cli/        Go server + CLI (single binary)
  cmd/                 CLI commands
  internal/server      wires services into one HTTP API
  internal/svc/<name>  one package per service (ec2, s3, rds, lambda, ...)
  internal/httpx       routing, IAM checks, errors
  internal/runtime     Docker engine wrapper
  internal/store       persistent state
console/    Next.js web console (static export, embedded into the binary)
docs/       architecture and API reference
```

```bash
make test                          # Go unit tests
cd cli && go run . serve           # API on :8080
cd console && NEXT_PUBLIC_API_URL=http://127.0.0.1:8080 npm run dev   # console on :3000
make release                       # cross-compiled archives for 6 platforms in dist/
```

Releases are built by GitHub Actions when a `v*` tag is pushed.

---

## 💸 Sustainability & Support

HomeCloud is a community project, currently unfunded and maintained by volunteers.

### 💛 Ways to Support

* [ ] Sponsor development via GitHub Sponsors / Open Collective (Coming Soon)
* [ ] Contribute infrastructure, bugfixes, or UX improvements
* [ ] Share HomeCloud with your communities!

---

## 🤝 Get Involved

We’re building HomeCloud for the community, and we’d love for you to join us:

💬 **[Join the Discord Community](https://homecloud.suryansh.one/discord)**: connect, discuss, and collaborate.
🛠️ **Contribute Code**: check out **Issues** and **Pull Requests** to get started.
📣 **Share Feedback**: help shape what HomeCloud becomes.

🔹 **By contributing, you agree to our** [**Contributor License Agreement (CLA)**](./CLA.md).

---

## 🛡 License

HomeCloud is released under **GNU AGPL-3.0**: open, transparent, and libre.
If you deploy or modify it publicly, share your changes too.

---

## ⚡ HomeCloud: The Cloud, On Your Terms.
