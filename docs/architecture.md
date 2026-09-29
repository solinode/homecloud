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

**CloudWatch.** A collector samples every managed container every 30 s (CPU, memory, network, disk I/O, processes) into namespaces `HC/EC2`, `HC/RDS`, `HC/ElastiCache`; Lambda publishes `HC/Lambda` invocations, errors and duration. Alarms evaluate on each cycle and notify SNS topics or webhooks on state changes.

## Limits and differences from AWS

- Instances are containers, not VMs: they share the host kernel, and their "disk" is the container's writable layer. VM-backed instances (QEMU/KVM) are on the roadmap.
- Security groups control which ports are published on the host and apply at launch; traffic inside a VPC is unrestricted.
- Volume sizes are advisory (Docker volumes are not size-capped).
- The management API is REST/JSON rather than the AWS wire protocols; only S3 is wire-compatible (via MinIO).
- HomeCloud runs on one Docker host today.
