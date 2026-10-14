# Monitoring HomeCloud

HomeCloud exposes Prometheus metrics about itself (API traffic, errors, latency, Docker, helper
containers, background loops, Lambda and disk use) and a status endpoint that the console's
**System status** page is built on. This guide covers access, scrape configs, the metric reference, a
Grafana dashboard and example alert rules.

These are metrics about the HomeCloud server. Metrics about your own resources (instances, queues,
functions) are in CloudWatch, as in AWS.

## Contents

1. [What is exposed](#1-what-is-exposed)
2. [Access](#2-access)
3. [A user for Prometheus](#3-a-user-for-prometheus)
4. [Prometheus scrape configs](#4-prometheus-scrape-configs)
5. [Metric reference](#5-metric-reference)
6. [Status endpoint and console page](#6-status-endpoint-and-console-page)
7. [Grafana dashboard](#7-grafana-dashboard)
8. [Alert rules](#8-alert-rules)

## 1. What is exposed

| Endpoint | Format | Access |
| --- | --- | --- |
| `GET /metrics` on the API port | Prometheus text format 0.0.4 | IAM action `homecloud:GetMetrics` |
| `GET /metrics` on `--metrics-addr` (off by default) | Prometheus text format 0.0.4 | none; loopback only unless `--metrics-addr-insecure` |
| `GET /api/v1/system/status` | JSON | IAM action `homecloud:GetSystemStatus` |
| `GET /api/v1/health` | JSON: `status`, `version`, `region`, `uptime_seconds` | public, unchanged |

`/metrics` is always on. Both IAM actions apply to resource `*`.

## 2. Access

Metrics are not anonymous. They name the services you use, the operations called on them, error codes
and counts, the HomeCloud version and commit, and disk sizes: enough to fingerprint the server and to
see what it is used for. The API port is often reachable from a LAN, a tailnet or the internet, so
`/metrics` on it requires a caller that IAM allows `homecloud:GetMetrics`, like every other route.

`/metrics` on the API port accepts three kinds of credentials:

- **HTTP basic auth**: username = access key ID, password = secret access key (Prometheus `basic_auth`).
- **Bearer token** `Authorization: Bearer <ACCESS_KEY_ID>:<SECRET>` (Prometheus `authorization` with
  `type: Bearer`, or `bearer_token_file`).
- **A console session token** (`Authorization: Bearer hcs_...`), so a signed-in browser can open it.

A request without credentials, or with credentials whose user is not allowed `homecloud:GetMetrics`,
gets an error and is counted in `homecloud_auth_failures_total` or the error counters like any other.
Basic auth is accepted on `/metrics` only; the rest of the API takes the bearer forms.

### Unauthenticated listener

If Prometheus runs on the same machine and you would rather not manage a key, HomeCloud can also serve
`/metrics` on a second address with no authentication:

```bash
homecloud serve --metrics-addr 127.0.0.1:9464
```

It is off by default and saved to `config.json` (`metrics_addr`) like the other `serve` flags. Because
that listener has no auth, HomeCloud refuses to start when the address is not a loopback address
(`127.0.0.1`, `::1`, `localhost`). To bind it elsewhere anyway, for example to a Docker bridge address
that a Prometheus container scrapes, add `--metrics-addr-insecure` (`metrics_addr_insecure` in
`config.json`) and restrict the port with your firewall:

```bash
homecloud serve --metrics-addr 172.17.0.1:9464 --metrics-addr-insecure
```

Only `/metrics` is served there; the status endpoint and everything else stay on the API port.

## 3. A user for Prometheus

Create a user that can do nothing except read metrics, and give it an access key. With the `homecloud`
CLI (as an administrator):

```bash
cat > metrics-policy.json <<'EOF'
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": "homecloud:GetMetrics", "Resource": "*" }
  ]
}
EOF
homecloud iam create-policy HomeCloudMetricsRead --document @metrics-policy.json \
  --description "Read /metrics"
homecloud iam create-user prometheus --policy HomeCloudMetricsRead   # no --password: API only
homecloud iam create-access-key prometheus                           # the secret is shown once
homecloud iam simulate prometheus homecloud:GetMetrics s3:ListAllMyBuckets
```

The last command should allow `homecloud:GetMetrics` and deny everything else.

The same with the AWS CLI, using an inline policy (`eval "$(homecloud aws-env)"` first, or set
`AWS_ENDPOINT_URL` to your HomeCloud):

```bash
aws iam create-user --user-name prometheus
aws iam put-user-policy --user-name prometheus --policy-name metrics-read \
  --policy-document file://metrics-policy.json
aws iam create-access-key --user-name prometheus
```

To also let the user read the status endpoint (for example for a script or a second exporter), add
`homecloud:GetSystemStatus` to the `Action` list. Rotate the key like any other: create a new one, update
the Prometheus credentials file, then delete the old key.

Put the credentials where Prometheus can read them and nobody else can:

```bash
sudo install -d -m 0750 -o root -g prometheus /etc/prometheus/homecloud
printf '%s' 'HCIA...'           | sudo tee /etc/prometheus/homecloud/access_key_id >/dev/null
printf '%s' 'SECRET'            | sudo tee /etc/prometheus/homecloud/secret_access_key >/dev/null
printf '%s:%s' 'HCIA...' 'SECRET' | sudo tee /etc/prometheus/homecloud/bearer >/dev/null
sudo chmod 0640 /etc/prometheus/homecloud/*
sudo chgrp prometheus /etc/prometheus/homecloud/*
```

## 4. Prometheus scrape configs

The console's **System status** page shows a scrape config for your server that you can copy; the
examples below are the same with placeholders. Replace `cloud.example.com` with your endpoint.

**Basic auth** (simplest; works with every Prometheus version):

```yaml
scrape_configs:
  - job_name: homecloud
    scheme: https
    metrics_path: /metrics
    static_configs:
      - targets: ["cloud.example.com"]
    basic_auth:
      username_file: /etc/prometheus/homecloud/access_key_id   # recent Prometheus; older: username: HCIA...
      password_file: /etc/prometheus/homecloud/secret_access_key
```

**Bearer token** from a file containing `ACCESS_KEY_ID:SECRET`:

```yaml
scrape_configs:
  - job_name: homecloud
    scheme: https
    static_configs:
      - targets: ["cloud.example.com"]
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/homecloud/bearer
```

**Self-signed certificate** (`--tls-self-signed`): copy the server's `<data-dir>/tls/cert.pem` to the
Prometheus host and point `ca_file` at it. The `server_name` must match a name in the certificate,
which is the `--public-host` it was created for:

```yaml
scrape_configs:
  - job_name: homecloud
    scheme: https
    static_configs:
      - targets: ["homelab:8443"]
    tls_config:
      ca_file: /etc/prometheus/homecloud/ca.pem
      server_name: homelab.tailnet.ts.net
    basic_auth:
      username: HCIA...
      password_file: /etc/prometheus/homecloud/secret_access_key
```

**Local listener** (`--metrics-addr 127.0.0.1:9464`, Prometheus on the same host), no credentials:

```yaml
scrape_configs:
  - job_name: homecloud
    static_configs:
      - targets: ["127.0.0.1:9464"]
```

A 15 to 60 second `scrape_interval` is plenty. Disk sizes are measured every 5 minutes in the
background, so scraping does not walk the data directory. Check the target under **Status > Targets** in
Prometheus, or by hand:

```bash
curl -s -u 'HCIA...:SECRET' https://cloud.example.com/metrics | grep homecloud_build_info
```

## 5. Metric reference

All HomeCloud metrics start with `homecloud_`. Counters reset when HomeCloud restarts; use `rate()` or
`increase()`.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `homecloud_build_info` | gauge (always 1) | `version`, `commit`, `go_version` | The running binary |
| `homecloud_start_time_seconds` | gauge | | Start time, Unix seconds |
| `homecloud_http_requests_total` | counter | `api`, `service`, `operation`, `status_class` | Requests served (see below) |
| `homecloud_http_request_errors_total` | counter | `api`, `service`, `code` | Error responses by error code (`AccessDenied`, `NoSuchBucket`, ...) |
| `homecloud_http_request_duration_seconds` | histogram | `api`, `service` | Request duration; buckets .005 .01 .025 .05 .1 .25 .5 1 2.5 5 10 30 |
| `homecloud_http_requests_in_flight` | gauge | | Requests being served now |
| `homecloud_auth_failures_total` | counter | `api` (`native`, `aws`), `code` | Rejected credentials: `InvalidClientTokenId`, `SignatureDoesNotMatch`, `ExpiredToken`, `MissingAuthenticationToken`, ... |
| `homecloud_docker_up` | gauge | | 1 when the Docker Engine answers, else 0 |
| `homecloud_containers` | gauge | `service`, `state` | HomeCloud containers per service and state (`running`, `exited`, `created`, `restarting`, `paused`, `dead`) |
| `homecloud_helper_up` | gauge | `helper` (`minio`, `ecr`, `dns`, `imds`) | 1 when the helper container is running |
| `homecloud_data_dir_bytes` | gauge | | Size of the data directory (measured every 5 minutes) |
| `homecloud_data_dir_filesystem_size_bytes` | gauge | | Size of the filesystem holding the data directory |
| `homecloud_data_dir_filesystem_free_bytes` | gauge | | Free space on that filesystem |
| `homecloud_docker_volumes` | gauge | | Number of HomeCloud Docker volumes |
| `homecloud_docker_volumes_bytes` | gauge | | Their total size (measured every 5 minutes) |
| `homecloud_loop_last_run_timestamp_seconds` | gauge | `loop` | When a background loop last completed, Unix seconds |
| `homecloud_loop_last_run_age_seconds` | gauge | `loop` | Seconds since then |
| `homecloud_loop_interval_seconds` | gauge | `loop` | How often the loop is meant to run |
| `homecloud_lambda_environments` | gauge | `state` (`idle`, `busy`, `starting`) | Lambda execution environments |
| `homecloud_lambda_starts_total` | counter | `kind` (`cold`, `warm`) | Invocations that started a new environment or reused one |

Request labels:

| `api` | `service` | `operation` |
| --- | --- | --- |
| `native` (the REST API under `/api/v1`) | IAM action prefix, e.g. `s3`, `ec2` | Route pattern, e.g. `GET /api/v1/s3/buckets/{bucket}` |
| `aws` (AWS wire protocols) | AWS service, e.g. `sqs`, `dynamodb` | AWS operation, e.g. `SendMessage`; `unknown` when not recognized |
| `other` | `console`, `website`, `lambda-url`, `apigw`, `presign`, `imds`, `health`, `metrics`, `other` | empty |

`status_class` is `2xx`, `3xx`, `4xx` or `5xx`. Operations are route patterns and AWS operation names,
never raw paths, so the number of series stays bounded.

Background loops (`loop` label): `cloudwatch`, `ecs`, `elb`, `sqs`, `lambda-reaper`, `lambda-pollers`,
`events`, `autoscaling`, `route53`, `dynamodb`, `cloudtrail`, `firewall`, `imds`, `maintenance` (hourly
cleanup) and `disk-usage` (the 5-minute disk measurement). The status page marks a loop `warning` when it
has not completed a pass for three intervals plus 30 seconds.

Process metrics: `go_goroutines`, `go_memstats_alloc_bytes`, `go_memstats_heap_inuse_bytes`,
`go_memstats_sys_bytes`, `go_gc_cycles_total`, `process_start_time_seconds`, `process_cpu_seconds_total`
(Linux and macOS), `process_resident_memory_bytes` and `process_open_fds` (Linux only).

## 6. Status endpoint and console page

`GET /api/v1/system/status` returns one JSON document with the overall state and everything the metrics
show, plus details from Docker and the latest errors. It needs `homecloud:GetSystemStatus`:

```bash
homecloud api GET /api/v1/system/status
```

Without the CLI, call it as any other native API route (see the [API reference](api.md)).

The main fields:

| Field | Content |
| --- | --- |
| `status` | `down` when Docker is unreachable (no workload can run), `degraded` when any subsystem is in `error`, otherwise `ok` (warnings do not change it) |
| `version`, `commit`, `go_version`, `region`, `account_id` | The server |
| `started_at`, `uptime_seconds` | Process start |
| `host`, `process` | OS, architecture, CPUs; PID, goroutines, heap and memory from the OS |
| `docker` | `reachable`, server version, OS, kernel, CPUs, memory, storage driver, container and image counts, `error` when unreachable |
| `subsystems` | One entry per `core` part (`api`, `docker`), `helper` (`minio`, `ecr`, `dns`, `imds`), `disk` (`data-dir-disk`: `warning` below 10% free, `error` below 3%) and `loop` (`loop:ecs`, ...), each with `status` (`ok`, `warning`, `error`, `unknown`) and `detail`; loops add `last_run` and `last_run_age_seconds` |
| `disk` | `data_dir` (path, used bytes, filesystem total and free), `volumes` (count, size), `measured_at` (`null` until the first measurement) |
| `containers` | Count per service and state |
| `lambda` | Idle, busy and starting environments; cold and warm starts since start |
| `recent_errors` | Up to 50 background errors, newest first: `time`, `source`, `message` |
| `metrics` | Path, required action, and the `--metrics-addr` listener (empty when off) |

The console's **System status** page shows the same data: the overall state, each subsystem, Docker
and disk details, Lambda environments, recent errors, and a ready-to-copy Prometheus scrape config for
this server. Viewing it needs `homecloud:GetSystemStatus`.

For a plain up/down check from a load balancer or uptime monitor, keep using the public
`GET /api/v1/health`.

## 7. Grafana dashboard

[`docs/grafana/homecloud.json`](grafana/homecloud.json) is a dashboard for Grafana 10 and later with
version, uptime, Docker and helper state, request rate, errors and auth failures, p50/p95/p99 latency,
in-flight requests, containers, background loop age, Lambda environments and starts, data directory and
volume sizes, and Go memory and goroutines.

To import it: **Dashboards > New > Import**, upload the file (or paste its contents), and pick your
Prometheus data source. Its `uid` is `homecloud`, so importing again replaces it. The `Job` and
`Instance` variables at the top select which server to show when you scrape more than one.

With file provisioning, copy it into a dashboards folder that a provider watches; the `Data source`
variable defaults to your default Prometheus data source.

## 8. Alert rules

Example Prometheus rules. They assume the scrape job is named `homecloud`; adjust thresholds to your
traffic.

```yaml
groups:
  - name: homecloud
    rules:
      - alert: HomeCloudDown
        expr: up{job="homecloud"} == 0
        for: 2m
        labels: {severity: critical}
        annotations:
          summary: "HomeCloud {{ $labels.instance }} cannot be scraped"
          description: "The server is down, unreachable, or the Prometheus credentials were rejected."

      - alert: HomeCloudDockerUnreachable
        expr: homecloud_docker_up == 0
        for: 1m
        labels: {severity: critical}
        annotations:
          summary: "HomeCloud cannot reach the Docker Engine"
          description: "No service can start or stop containers. Check `systemctl status docker` and `homecloud doctor`."

      - alert: HomeCloudHelperDown
        expr: homecloud_helper_up == 0
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "HomeCloud helper {{ $labels.helper }} is not running"

      - alert: HomeCloudLoopStalled
        expr: |
          homecloud_loop_last_run_age_seconds
            > 3 * homecloud_loop_interval_seconds
          and homecloud_loop_interval_seconds > 0
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "Background loop {{ $labels.loop }} has not run for {{ $value | humanizeDuration }}"
          description: "See recent_errors in `homecloud api GET /api/v1/system/status`."

      - alert: HomeCloudHigh5xxRate
        expr: |
          sum by (instance) (rate(homecloud_http_requests_total{status_class="5xx"}[5m]))
            / sum by (instance) (rate(homecloud_http_requests_total[5m]))
          > 0.05
          and sum by (instance) (rate(homecloud_http_requests_total[5m])) > 0.1
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "More than 5% of HomeCloud requests fail with a 5xx"

      - alert: HomeCloudAuthFailureSpike
        expr: sum by (instance) (increase(homecloud_auth_failures_total[10m])) > 50
        labels: {severity: warning}
        annotations:
          summary: "{{ $value | humanize }} rejected credentials in 10 minutes"
          description: "Possibly a leaked or revoked key in use, a clock skew problem, or someone guessing keys."

      - alert: HomeCloudDataDirDiskLow
        expr: |
          homecloud_data_dir_filesystem_free_bytes
            / homecloud_data_dir_filesystem_size_bytes < 0.10
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "Less than 10% free on the HomeCloud data directory's filesystem"

      - alert: HomeCloudHighLatency
        expr: |
          histogram_quantile(0.95,
            sum by (instance, le) (rate(homecloud_http_request_duration_seconds_bucket[5m]))
          ) > 2
        for: 15m
        labels: {severity: warning}
        annotations:
          summary: "HomeCloud p95 request latency is {{ $value | humanizeDuration }}"
```

Notes:

- `HomeCloudDown` uses Prometheus's own `up` series, so it also fires when the `prometheus` user's key
  is deleted or loses `homecloud:GetMetrics`.
- Some operations are slow by design (long polling `ReceiveMessage`, Lambda invokes, image pushes), so
  a global p95 above a few seconds is not always a problem. Add `service!~"sqs|lambda"` to the
  latency rule's selector if those dominate your traffic.
- Docker volumes usually live on a different filesystem (`/var/lib/docker`) than the data directory;
  watch that one with node_exporter.
