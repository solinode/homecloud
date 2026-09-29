# Changelog

## 0.1.0 (2026-09-30)

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
