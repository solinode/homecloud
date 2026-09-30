# Changelog

## Unreleased

- CloudWatch anomaly detection: `PutAnomalyDetector`, `DescribeAnomalyDetectors`, `DeleteAnomalyDetector`, `ANOMALY_DETECTION_BAND(m, stddevs)` in `GetMetricData` (upper and lower series) and anomaly alarms (`ThresholdMetricId` with `LessThanLowerOrGreaterThanUpperThreshold`, `LessThanLowerThreshold`, `GreaterThanUpperThreshold`; the detector is created automatically). The band is mean +/- k standard deviations of the metric's last 14 days, by hour of week, hour of day or a rolling window depending on how much history exists, and is absent below 10 datapoints or one hour of history; it is not AWS's machine learning model. `FILL(m, LINEAR)` interpolates between neighbouring datapoints.
- Cognito user pools support `USER_SRP_AUTH` (Amplify, amazon-cognito-identity-js, pycognito), `ForgotPassword`/`ConfirmForgotPassword` and `AdminResetUserPassword`. Existing users need their password set again (or one `USER_PASSWORD_AUTH` sign-in) before SRP works.
- CloudFormation updates behave like AWS: queues, topics, parameters, functions, roles, tables, alarms and other resources change in place, replacements create the new resource before deleting the old one, and a failed update rolls back to the previous template (`UPDATE_ROLLBACK_COMPLETE`, `ContinueUpdateRollback`, `DisableRollback`). A replacement that keeps a custom resource name now fails, as in AWS.

## 0.3.0 (2026-09-30)

Every service in the console now speaks the AWS protocols, security groups filter traffic inside a VPC, and there's a demo console on the website.

### AWS compatibility
- The AWS CLI, SDKs and Terraform now also work with API Gateway (HTTP APIs), Route 53, ACM, EFS, ElastiCache, CloudTrail, Cognito user pools and CloudFormation (stacks, change sets, `aws cloudformation deploy`, exports, 60+ resource types).
- `examples/terraform/shop`: a full app (VPC, load balancer with HTTPS, ECS, RDS, S3, DynamoDB, SNS, SQS, Lambda, API Gateway, Route 53) that deploys on a fresh install with the standard AWS provider, re-plans clean and destroys cleanly.

### Security
- Security groups filter traffic between resources inside a VPC, as on AWS: rules from CIDRs or other security groups, stateful replies, the default group allowing its own members. Covers instances, ECS tasks, RDS, ElastiCache, load balancers and Lambda functions in a VPC. Resources in different groups now need a rule between them.
- API Gateway checks the Lambda function's resource policy before invoking it.

### Website
- Try the console without installing anything: https://homecloud.pages.dev/demo/

### Fixed
- Security group rules whose source is another group were rejected when sent by the AWS SDKs and Terraform.
- Lambda event source mappings can be tagged, and Route 53 alias records keep `EvaluateTargetHealth`, so Terraform plans stay clean.
- API Gateway HTTP integrations escape path parameters and reject bodies over 10 MB.

## 0.2.0 (2026-09-30)

More services speak the AWS protocols, security groups and resource policies are enforced more strictly, and the console covers more of EC2, CloudWatch and EventBridge.

### AWS compatibility
- The AWS CLI, SDKs and Terraform now also work with Elastic Load Balancing v2 (load balancers, target groups, listeners, rules), EC2 Auto Scaling and launch templates, ECS, ECR and RDS (instances, snapshots and restores, subnet and parameter groups, master passwords in Secrets Manager).
- Elastic IPs: allocate, associate, disassociate and release, in the AWS API, the native API and the console.

### Security
- Security group changes apply to running instances (authorize/revoke, changing an instance's groups). Docker can't change a running container's ports, so the instance is recreated from its disk with the same ID and IP, and its processes restart.
- SQS queue policies, SNS topic policies and KMS key policies are enforced together with IAM policies; an explicit Deny wins. SNS and EventBridge deliveries are checked against the target's policy with `aws:SourceArn`.
- Condition keys in policies: `aws:SourceIp`, `aws:SecureTransport`, `aws:PrincipalArn`, `aws:username`, S3 keys such as `s3:prefix` and object tags, and more. S3 bucket policies use the same evaluator as IAM.

### Console
- EC2 key pairs (create, import, download), EBS snapshots, internet gateways and route tables, Elastic IPs.
- CloudWatch Logs Insights queries, metric filters and subscription filters, alarm history.
- EventBridge custom event buses and EventBridge Scheduler.

### Fixed
- `homecloud upgrade` and the install scripts download from solinode/homecloud instead of relying on GitHub's redirect from the old repository name.

## 0.1.1 (2026-09-30)

The first release of the rebuilt HomeCloud. It includes everything listed under 0.1.0 below, plus these fixes. (0.1.0 was withdrawn: S3 could not start on new installations.)

### Fixed
- S3 failed to start on new installations: MinIO no longer publishes free images on Docker Hub or quay.io. HomeCloud now uses Chainguard's MinIO build (`cgr.dev/chainguard/minio`), the same server built from source for amd64 and arm64. Existing installations keep their data.
- Data races in the test harness and in Secrets Manager and SNS settings replaced at runtime; a rotation with no Lambda invoker now records an error instead of crashing.

## 0.1.0 (withdrawn)

HomeCloud is rebuilt from a prototype CLI into a self-hosted cloud: one `homecloud` binary runs an authenticated REST API, the web console and around thirty AWS-style services on Docker.

### Services
- **Compute:** EC2 instances (types, AMI catalog, capture-to-image, user data, run-command, browser terminal, live resize), EBS-style volumes, EFS shared file systems, EC2 Auto Scaling groups with target tracking.
- **Containers:** ECS services and tasks with rolling deployments and Secrets Manager injection, ECR private registry.
- **Networking:** VPCs and subnets with real IP allocation and private DNS, security groups, application load balancers with HTTP/HTTPS listeners, routing rules and health checks, Route 53 public and private hosted zones, ACM certificates from a private CA.
- **Storage and databases:** S3 (MinIO) with versioning, lifecycle, presigned URLs and static websites; RDS (PostgreSQL, MySQL, MariaDB), ElastiCache (Redis, Valkey, Memcached) and MongoDB with snapshots, restores, automated backups and a query editor; DynamoDB-style tables.
- **Serverless and integration:** Lambda (Python, Node.js) with function URLs, API Gateway with JWT authorizers, SQS, SNS, EventBridge schedules and patterns, Step Functions.
- **Security and management:** IAM (users, groups, JSON policies, access keys, simulator), Cognito user pools, KMS, Secrets Manager, Parameter Store, CloudWatch metrics/logs/alarms, CloudTrail, CloudFormation-style stacks.

### AWS compatibility
- The API port speaks the AWS protocols (SigV4, awsJson, awsQuery, REST), so the AWS CLI, SDKs and Terraform work against HomeCloud with IAM enforced: STS, IAM, S3, Lambda, DynamoDB, SQS, SNS, Secrets Manager, Parameter Store, KMS, CloudWatch, CloudWatch Logs, EventBridge (and Scheduler) and Step Functions. See docs/aws-compat.md.
- IAM roles with trust policies and temporary credentials; functions receive role credentials.
- ARNs use the `aws` partition and the default region is `us-east-1`; existing installations are migrated at startup (with a backup).
- Lambda runs on AWS's official runtime images: warm environments, Go/Java/Ruby/.NET/custom runtimes, container images, versions, aliases, layers, async invocation with destinations, concurrency limits.

### Tooling
- `homecloud aws-env`, `service install`, `backup`/`restore` and `upgrade`.
- New CLI covering every service (`homecloud <service> ...`), `homecloud serve`, `configure`, `doctor`, and `api` for raw calls.
- Web console with pages for every service, embedded in the binary.
- Cross-compiled releases for Linux, macOS and Windows (amd64/arm64), CI with unit, race and end-to-end tests, arch-aware install scripts.

### Fixed (from 0.0.1)
- The Windows release archive contained a macOS binary.
- S3 commands used credentials that did not match the MinIO container, only the bare `s3` command started MinIO, and it could attach to other projects' MinIO containers.
- Compute state was written to whatever directory the CLI ran in.
