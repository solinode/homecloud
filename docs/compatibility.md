# Terraform compatibility

HomeCloud is tested against the Terraform modules people actually use: the community
[`terraform-aws-modules`](https://registry.terraform.io/namespaces/terraform-aws-modules) modules,
at pinned versions, with the stock `hashicorp/aws` provider (6.x) and OpenTofu. Nothing in the
configurations is HomeCloud specific except path-style S3 addressing; the provider reaches HomeCloud
through `AWS_ENDPOINT_URL`.

Each scenario in [`compat/terraform/`](../compat/terraform) runs on a fresh HomeCloud and must:

1. **apply** without errors,
2. pass a **check** that the resources really work (an object read back, a message delivered, a
   function invoked through its alias and its queue, nginx served through the load balancer, a DNS
   answer, a decrypted ciphertext, an assumed role...),
3. be **idempotent**: a second `plan` shows no changes,
4. **destroy** cleanly.

The suite runs nightly (and on demand) in the
[`Terraform compatibility`](../.github/workflows/compat.yml) workflow, which uploads
`results.json`, `results.md` and every scenario's log as an artifact.

## Results

Last run, from [`compat/results.md`](../compat/results.md):

17 of 17 scenarios pass (OpenTofu v1.12.6, hashicorp/aws 6.67.0, 2026-10-07T15:58:59Z).

| Scenario | Modules | Apply | Check | Idempotent | Destroy | Notes |
|---|---|---|---|---|---|---|
| acm | route53@6.5.1<br>acm@6.3.1 | pass | pass | pass | pass |  |
| apigateway-v2 | lambda@8.9.0<br>apigateway-v2@6.1.1 | pass | pass | pass | pass |  |
| cloudwatch | cloudwatch/log-group@5.7.3<br>cloudwatch/log-stream@5.7.3<br>cloudwatch/log-metric-filter@5.7.3<br>cloudwatch/metric-alarm@5.7.3<br>cloudwatch/metric-alarms-by-multiple-dimensions@5.7.3<br>cloudwatch/composite-alarm@5.7.3<br>cloudwatch/query-definition@5.7.3 | pass | pass | pass | pass |  |
| dynamodb-table | dynamodb-table@5.5.2 | pass | pass | pass | pass |  |
| ecs-alb | alb@10.5.1<br>ecs@7.6.1 | pass | pass | pass | pass |  |
| eventbridge | eventbridge@4.3.2 | pass | pass | pass | pass |  |
| iam | iam/iam-policy@6.8.2<br>iam/iam-role@6.8.2<br>iam/iam-user@6.8.2<br>iam/iam-group@6.8.2 | pass | pass | pass | pass |  |
| kms | kms@4.2.2 | pass | pass | pass | pass |  |
| lambda | lambda@8.9.0<br>lambda/alias@8.9.0 | pass | pass | pass | pass |  |
| rds | security-group/postgresql@6.0.0<br>rds@7.2.2 | pass | pass | pass | pass |  |
| route53 | route53@6.5.1 | pass | pass | pass | pass |  |
| s3-bucket | s3-bucket@5.16.1 | pass | pass | pass | pass |  |
| secrets-manager | secrets-manager@2.2.0 | pass | pass | pass | pass |  |
| security-group | security-group/http-80@6.0.0<br>security-group@6.0.0 | pass | pass | pass | pass |  |
| sqs-sns | sqs@5.2.2<br>sns@7.2.0 | pass | pass | pass | pass |  |
| ssm-parameter | ssm-parameter@2.1.2 | pass | pass | pass | pass |  |
| vpc | vpc@6.7.3 | pass | pass | pass | pass |  |

Per service (each scenario counts for every service it uses):

| Service | Scenarios passing |
|---|---|
| acm | 1/1 |
| apigateway | 1/1 |
| cloudwatch | 1/1 |
| cognito | 1/1 |
| dynamodb | 1/1 |
| ec2 | 5/5 |
| ecs | 1/1 |
| elbv2 | 1/1 |
| events | 1/1 |
| iam | 5/5 |
| kms | 3/3 |
| lambda | 2/2 |
| logs | 4/4 |
| rds | 1/1 |
| route53 | 2/2 |
| s3 | 1/1 |
| scheduler | 1/1 |
| secretsmanager | 2/2 |
| sns | 2/2 |
| sqs | 3/3 |
| ssm | 1/1 |
| sts | 1/1 |

## Scenarios

| Scenario | What it builds |
|---|---|
| `vpc` | `vpc` module: public, private and database subnets in two zones, internet gateway, per-tier route tables, a NAT gateway with its Elastic IP, the default security group, network ACL and route table |
| `s3-bucket` | `s3-bucket` module: versioning, AES256 default encryption, lifecycle rules (expiration, noncurrent versions, abort incomplete uploads), ownership controls, public access block, CORS, a bucket policy plus the module's TLS-only statements |
| `sqs-sns` | `sqs` and `sns` modules: a topic subscribed to a queue (raw delivery, filter policy) with a dead-letter queue and a queue policy for the topic, and a FIFO topic feeding a FIFO queue |
| `dynamodb-table` | `dynamodb-table` module: on-demand table with GSI, LSI, TTL, stream, point-in-time recovery and encryption; a provisioned table |
| `lambda` | `lambda` module: Python function packaged from local source, execution role with inline policy, log group, published version, `alias` submodule, EventBridge permission, SQS event source mapping |
| `rds` | `rds` module: PostgreSQL 16 with a module-made subnet group and parameter group, master password in Secrets Manager; `security-group` `postgresql` preset |
| `ecs-alb` | `ecs` module (cluster, Fargate service with task and execution roles, log group, security group and its default autoscaling) and `alb` module (listener, IP target group) serving nginx |
| `security-group` | `security-group` module 6.x per-rule resources (CIDR, IPv6, self and group references, port ranges, tags, exclusive rules) and the `http-80` preset |
| `iam` | `iam` submodules: `iam-policy`, `iam-role` (service and account trust, managed and inline policies, instance profile), `iam-user` with an access key, `iam-group` with members and policies |
| `kms` | `kms` module: symmetric key with rotation, a key policy from owners, administrators, users and a service statement, aliases and a grant; an ECC signing key |
| `secrets-manager` | `secrets-manager` module: JSON secret with a resource policy, a generated password (`GetRandomPassword`), a secret under a customer managed key |
| `ssm-parameter` | `ssm-parameter` module: String, StringList, SecureString (default and customer key) and Advanced-tier parameters |
| `cloudwatch` | `cloudwatch` submodules: log group and stream, metric filter, metric alarm with SNS actions, alarms over several dimensions, a metric math alarm, a composite alarm, a saved Logs Insights query; a dashboard |
| `eventbridge` | `eventbridge` module: custom bus with pattern rules to SQS (input transformer, dead-letter queue, retry policy), a scheduled rule, an EventBridge Scheduler group and schedule, the target IAM roles and log delivery source |
| `apigateway-v2` | `apigateway-v2` module: HTTP API with CORS, Lambda proxy routes (payload 1.0 and 2.0), a Cognito JWT authorizer, an HTTP proxy route, the `$default` stage with access logs |
| `route53` | `route53` module: public zone with A, AAAA, CNAME, MX, TXT, SRV and CAA records; private zone associated with a VPC |
| `acm` | `acm` module: certificate with SANs validated through Route 53 records the module writes |

## Run it yourself

```bash
compat/run.sh                    # builds bin/homecloud, starts it on free ports, runs every scenario
compat/run.sh vpc s3-bucket      # just these
HC_KEEP=1 compat/run.sh lambda   # keep the server, its data dir and containers for debugging
```

You need Go, Docker (set `DOCKER_HOST` for OrbStack or Colima), OpenTofu or Terraform, the AWS CLI
and `jq`. The runner starts HomeCloud with its own data directory, runs each scenario in a scratch
copy (so no state lands in the repository), and removes the server's containers, networks, volumes
and data directory at the end. Results go to `compat/results.json` and `compat/results.md`; logs to
`compat/logs/`.

To add a scenario, create `compat/terraform/<name>/` with a `main.tf` (a `# services:` comment line
feeds the per-service score), a `providers.tf` link to `../_shared/providers.tf`, and optionally an
executable `check.sh` that runs after apply with `TF` set to the Terraform binary.

## What HomeCloud does not do

Some things these modules create are kept as records rather than acted on, as on a single host they
have no equivalent. They apply, re-plan and destroy like on AWS; see
[AWS compatibility](aws-compat.md) for details.

- **NAT gateways**: a VPC with an attached internet gateway and a default route to it reaches the
  internet from every subnet; private subnets do not need the NAT gateway to get out.
- **Network ACLs**: stored and reported, never enforced (security groups are).
- **Application Auto Scaling**: targets, policies and scheduled actions are stored; HomeCloud does not
  change an ECS service's desired count.
- **S3 `AbortIncompleteMultipartUpload`** lifecycle actions are stored; MinIO cannot run them.
- **CloudWatch Logs vended delivery** (delivery sources, destinations, deliveries) are stored; no logs
  flow through them.
- Single region: module features that create resources in other regions (Secrets Manager replicas,
  multi-Region KMS replicas, cross-region RDS replicas) are refused with an AWS-shaped error and are not
  part of the suite.
