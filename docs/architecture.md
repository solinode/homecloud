# HomeCloud architecture

HomeCloud is a single Go binary (`homecloud`) that is both the server and the CLI. `homecloud serve` starts one HTTP server that hosts:

- the REST API under `/api/v1`, one package per service in `cli/internal/svc/`;
- the web console, a static Next.js export embedded into the binary at build time;
- public data-plane endpoints: function URLs (`/lambda-url/...`), HTTP APIs (`/apigw/...`) and static websites (`/website/...`).

Every resource that needs a process (instances, databases, caches, functions, the S3 server) runs as a **Docker container**. HomeCloud labels everything it creates with `homecloud.managed=true`, `homecloud.service` and `homecloud.resource`, and never touches objects without those labels, so it can share a Docker host with anything else.

```
            browser / homecloud CLI / curl / AWS SDK (S3)
                              │
               ┌──────────────▼───────────────┐
               │   homecloud serve  (:8080)   │
               │  httpx router ─ IAM check ─ CloudTrail
               │  ┌─────┬─────┬─────┬──────┐  │
               │  │ ec2 │ s3  │ rds │lambda│… │   state.json, dynamodb.db,
               │  └──┬──┴──┬──┴──┬──┴──┬───┘  │   logs/, trail/, sqs/, snapshots
               └─────┼─────┼─────┼─────┼──────┘
                     │ Docker Engine API │
      ┌──────────────▼─────▼─────▼─────▼─────────────────┐
      │ VPC network hc-vpc-… (10.88.0.0/16, bridge)       │
      │  i-0abc (10.88.0.4)  pg1.rds.internal  s3.internal │
      │  hc-lambda-fn        cache1.elasticache.internal   │
      └───────────────────────────────────────────────────┘
```

## Request path

1. `httpx.Router` matches the route. Each route declares an IAM action (e.g. `ec2:RunInstances`) and optionally a resource ARN template (`arn:hc:s3:::{bucket}`).
2. `iam.Service.Authenticate` resolves the caller from a console session token (`hcs_…`) or an access key (`<id>:<secret>`). Secrets are stored as SHA-256 hashes; console passwords as bcrypt.
3. The caller's effective policies (attached, inline, and via groups) are evaluated like AWS: an explicit `Deny` wins, otherwise any matching `Allow` grants, otherwise deny. The root user bypasses checks; `sts:*` identity calls are always allowed.
4. The handler runs. Mutating calls and denied calls are appended to the CloudTrail log (`trail/YYYY-MM-DD.jsonl`, 90-day retention).

## State

| What | Where |
| --- | --- |
| Resource records (users, instances, queues, rules, …) | `state.json`, a set of JSON collections written atomically on every change (`internal/store`) |
| DynamoDB items | `dynamodb.db` (embedded bbolt), one bucket per table, keys encoded to preserve sort order |
| SQS messages | in memory, snapshotted to `sqs/<queue>.json` every 2 s and on shutdown |
| Metrics | in memory (24 h per series), snapshotted to `metrics.json` |
| Logs | `logs/<group>/<stream>.jsonl`; container log groups (`/hc/<service>/<id>`) read Docker logs live |
| Lambda code | `lambda/<function>.zip` |
| Database snapshots | `rds-snapshots/<snapshot>.bak` (engine-native dumps) |
| Secrets | encrypted in `state.json` with AES-256-GCM under `master.key` |
| Object data | the `homecloud-s3-data` Docker volume behind MinIO |

The data directory defaults to `~/.homecloud` (override with `--data-dir` or `HOMECLOUD_DATA_DIR`). Back it up together with the Docker volumes whose names start with `hc-` and `homecloud-`.

## Services

**VPC.** A VPC is a Docker bridge network spanning its CIDR. Subnets are ranges of it, and IPs are allocated per resource (AWS-style: the first four and last address of each subnet are reserved), so every instance, database and function gets a stable private IP and DNS aliases. A default VPC (`10.88.0.0/16` or the next free `/16`) is created on first start. Security-group ingress rules become published host ports when a resource launches.

**EC2.** An instance is a container with CPU quota and memory limits taken from its instance type. OS images (Ubuntu, Debian, …) boot a small init script that runs user data once and then idles, so the instance behaves like a VM that stays up; application images (e.g. `ami-nginx`) run their own entrypoint. Instance metadata is written to `/var/lib/homecloud/instance.json`. Volumes are named Docker volumes. "Create image" commits the container into `homecloud/ami:<id>`. The browser terminal is a WebSocket bridged to `docker exec` with a TTY.

**S3.** HomeCloud runs one MinIO server (`homecloud-s3`) with generated root credentials stored in Secrets Manager, attaches it to every VPC as `s3.internal`, and exposes both the native S3 endpoint (default port 9500) and a management API used by the console and CLI.

**RDS / ElastiCache / DocumentDB.** Each database is a container from the official engine image with a named volume, placed in a subnet with DNS `<id>.rds.internal` (or `.elasticache.internal`). Master credentials are generated into the secret `rds!<id>`. Readiness is probed over TCP inside the container. Snapshots are engine-native dumps (`pg_dumpall`, `mysqldump`, `mariadb-dump`, `mongodump`, RDB files) and restore into a new instance. A background loop takes daily automated backups and prunes them after the retention period.

**Lambda.** Each function gets a warm container from the runtime image (`python:3.x-slim`, `node:2x-slim`) holding its code in `/var/task` and a small bootstrap in `/opt/homecloud`. Every invocation is a new process in that container (`docker exec`) that receives the event on stdin and writes the result to stdout; stdout and stderr from the handler become CloudWatch logs in `/aws/lambda/<name>`. Containers are recycled when code or configuration changes and after 15 idle minutes. Timeouts kill the environment. CPU scales with memory as in AWS.

**API Gateway / function URLs.** Requests are converted to the API Gateway v2 (HTTP API) payload format, and responses follow the same rules (`statusCode`, `headers`, `body`, `isBase64Encoded`, or a bare value returned as JSON).

**SQS.** Queues keep messages in memory with visibility timeouts, delays, long polling (up to 20 s), FIFO ordering per message group with 5-minute deduplication, and dead-letter queues driven by `maxReceiveCount`. Lambda event source mappings poll queues and delete messages on success (supporting `batchItemFailures`).

**SNS.** Publishing fans a message out to matching subscriptions (filter policies on message attributes) over SQS, Lambda or HTTP(S) with retries.

**EventBridge.** Scheduled rules use AWS `rate()` and six-field `cron()` expressions, checked every minute. Pattern rules match published events using exact values, `prefix`, `anything-but`, `exists` and `numeric` operators. Targets are Lambda functions, SQS queues and SNS topics, addressed by ARN.

**Auto Scaling.** A group owns instances tagged `hc:autoscaling:groupName`. Every 10 s it terminates members that stopped (and launches replacements), launches or terminates (newest first) to reach the desired capacity, spreading launches across its subnets, and registers running members with its target groups. Target-tracking policies compare the group's average `HC/EC2` CPU or memory utilization over the last three minutes with the target, scale out proportionally and scale in one instance at a time, within min/max and after a cooldown.

**EFS.** A file system is a named Docker volume that any number of instances mount at launch (`file_systems` in RunInstances), so they share files like an NFS mount.

**ECR.** HomeCloud runs a Docker Distribution registry (`homecloud-ecr`, loopback port 5500 by default) and manages repositories, tags and deletions through its API. Images pushed with `docker push localhost:5500/...` can be used by ECS task definitions and registered as AMIs.

**ELB.** Each load balancer is an nginx container with a private IP in a subnet (`<name>.elb.internal`); internet-facing listeners are published on the host. HomeCloud renders nginx configuration from listeners, path/host rules and target groups, health-checks every target from inside the VPC on the target group's interval and thresholds, and reloads nginx when targets register, deregister or change health. Upstreams share state across workers for true round robin.

**ECS.** Task definitions are versioned (`family:revision`); secrets listed in a task definition are read from Secrets Manager (optionally a JSON key) and injected as environment variables at launch. A reconciler runs every 5 s: it replaces exited tasks, starts tasks for the current revision one at a time, retires tasks from previous revisions once the new ones are up, and registers/deregisters task IPs with the service's target group. Services are reachable inside the VPC as `<service>.ecs.internal`.

**KMS & Parameter Store.** KMS keys hold versioned AES-256 key material encrypted under the master key; ciphertext blobs embed the key ID and version, so rotated keys still decrypt old data, and the encryption context is bound as AEAD associated data. SecureString parameters are encrypted with a service-managed key (`alias/hc/ssm`) or a customer key, bound to the parameter's ARN.

**Step Functions.** Definitions are Amazon States Language JSON, validated on create. Each execution runs in the server as an interpreter over the state graph with InputPath → Parameters → task → ResultSelector → ResultPath → OutputPath processing, `States.Format`/`JsonToString`/`StringToJson`/`Array` intrinsics, the `$$` context object, retries with exponential backoff, catchers, concurrent Parallel branches and Map iterations. Task resources are Lambda functions (direct ARN or `arn:hc:states:::lambda:invoke`), `arn:hc:states:::sqs:sendMessage` and `arn:hc:states:::sns:publish`. Every transition is recorded in the execution history (`sfn/<execution>.json`); executions interrupted by a restart are marked ABORTED. State machines can be EventBridge targets.

**CloudFormation.** Templates (YAML with short tags, or JSON) are parsed into a dependency graph from `Ref`, `GetAtt`, `Sub` references and `DependsOn`. Each resource type maps onto the service's own create/describe/delete API; resource properties are the API's request fields. The engine calls those routes in-process with the caller's identity (so stacks can do exactly what the caller could), waits for asynchronous resources (instances, databases, load balancers) to become ready, and rolls back on failure. Updates replace changed resources and everything that depends on them; deletes run in reverse order and honour `DeletionPolicy: Retain`.

**Cognito.** Each user pool has its own RSA-2048 signing key (encrypted under the master key) and issues RS256 ID and access tokens with issuer `http(s)://<host>:<port>/cognito/<pool>`; the public JWKS and OpenID discovery documents let any app verify them. Refresh tokens are opaque and stored hashed; global sign-out revokes refresh tokens and every access token issued before it. Application-facing endpoints (`/cognito/<pool>/sign-up`, `/auth`, `/respond`, `/userinfo`, `/change-password`, `/sign-out`) need no IAM credentials; sign-in failures are throttled per user and client IP. API Gateway routes with `authorization: JWT` validate tokens against the API's authorizer pool and pass the claims to the function in `requestContext.authorizer.jwt.claims`.

**CloudWatch.** A collector samples every managed container every 30 s (CPU, memory, network, disk I/O, processes) into namespaces `HC/EC2`, `HC/RDS`, `HC/ElastiCache`; Lambda publishes `HC/Lambda` invocations, errors and duration. Alarms evaluate on each cycle and notify SNS topics or webhooks on state changes.

## Security notes

- The API binds to `127.0.0.1` by default. When exposing it, use TLS (`--tls-cert/--tls-key` or `--tls-self-signed`; the CLI trusts a self-signed certificate through `ca_file` in its credentials).
- HomeCloud needs access to the Docker socket, which is root-equivalent on the host; treat HomeCloud administrators as host administrators.
- Console sign-in is throttled after 10 failures per client IP in 5 minutes. Access-key secrets and session tokens are stored as SHA-256 hashes; passwords as bcrypt; secrets and SecureStrings are encrypted under `master.key` (keep it with your backups, and keep it private).
- The container registry listens on loopback only. MinIO's S3 endpoint and database ports marked public listen on all interfaces and require credentials.
- One Docker host runs one HomeCloud installation; the server refuses to start against another installation's containers.

## Limits and differences from AWS

- Instances are containers, not VMs: they share the host kernel, and their "disk" is the container's writable layer. VM-backed instances (QEMU/KVM) are on the roadmap.
- Security groups control which ports are published on the host and apply at launch; traffic inside a VPC is unrestricted.
- Volume sizes are advisory (Docker volumes are not size-capped).
- The management API is REST/JSON rather than the AWS wire protocols; only S3 is wire-compatible (via MinIO).
- HomeCloud runs on one Docker host today.
