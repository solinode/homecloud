# Using AWS tools with HomeCloud

HomeCloud speaks the AWS wire protocols, so the AWS CLI, the AWS SDKs and tools built on them
(Terraform, CDK, Pulumi, boto3 scripts) work against it. Point them at your HomeCloud endpoint
and use a HomeCloud access key:

```bash
export AWS_ENDPOINT_URL=http://localhost:8080   # your HomeCloud API address
export AWS_ACCESS_KEY_ID=HCIA...                # a HomeCloud access key (IAM > Users > Access keys)
export AWS_SECRET_ACCESS_KEY=...
export AWS_REGION=us-east-1                     # HomeCloud's region (see `homecloud whoami`)
aws sts get-caller-identity
```

`homecloud aws-env` prints these lines for the current CLI credentials.

Requests are authenticated with AWS Signature Version 4 and authorized with the same IAM
policies as the HomeCloud API and console. ARNs use the `aws` partition
(`arn:aws:sqs:us-east-1:123456789012:jobs`), so tools that validate ARNs accept them.

Temporary credentials work as in AWS: create an IAM role whose trust policy allows your user
(or a service such as `lambda.amazonaws.com`), then `aws sts assume-role`. Functions, tasks and
instances with a role receive credentials and `AWS_ENDPOINT_URL` in their environment, so SDK
calls from inside them reach HomeCloud.

## Policies and authorization

Every request is decided the way AWS does within one account: an explicit `Deny` in the
caller's identity policies (or permissions boundary) or in the target's resource policy always
wins; otherwise the request is allowed if the identity policies allow it or the resource policy
grants the caller. A resource policy that names the account (`arn:aws:iam::<account>:root`)
only delegates to identity policies; one that names a user or role ARN, or `"*"`, grants access
on its own.

Resource policies that are enforced, over both the AWS and the native API:

| Resource | Policy | Notes |
|---|---|---|
| S3 bucket | bucket policy (`PutBucketPolicy`) | Anonymous callers are allowed by `Principal: "*"`. Policies MinIO cannot store (`ArnLike`, `aws:PrincipalArn`, ...) are kept by HomeCloud and returned as written. |
| SQS queue | `Policy` attribute, `AddPermission` | |
| SNS topic | `Policy` attribute | |
| KMS key | key policy | Required, as in AWS: the default policy grants the account root, which delegates to IAM, so existing keys behave as before. A key policy without the root statement makes IAM policies ineffective for that key. Grants are stored but do not authorize requests. |

The account's root user is not subject to resource-policy `Deny` statements, so a bad policy
cannot lock the administrator out.

Service-to-service deliveries honor the target's policy too. SNS delivering to a queue (and
its dead-letter queue) and EventBridge delivering to a queue or topic are evaluated as the
service principal (`sns.amazonaws.com`, `events.amazonaws.com`) with `aws:SourceArn` (the topic or
rule) and `aws:SourceAccount`. A queue or topic with a policy needs an `Allow` for the service and
no matching `Deny`. Unlike AWS, a queue or topic with no policy at all still accepts deliveries,
because the subscription or target was authorized when it was created. CloudWatch alarm actions
and Lambda destinations are not yet evaluated against the target's policy.

Condition operators: `String*` (`StringEquals`, `StringLike`, `IgnoreCase`, `Not` variants),
`Arn*`, `IpAddress`/`NotIpAddress`, `Bool`, `Numeric*`, `Date*`, `BinaryEquals` and `Null`, with the
`IfExists`, `ForAnyValue` and `ForAllValues` modifiers, and `${aws:username}`-style policy
variables. Condition keys:

- Global: `aws:SourceIp`, `aws:SecureTransport`, `aws:UserAgent`, `aws:Referer`, `aws:TlsVersion`,
  `aws:CurrentTime`, `aws:EpochTime`, `aws:PrincipalArn`, `aws:PrincipalAccount`,
  `aws:PrincipalType`, `aws:PrincipalTag/<k>`, `aws:username`, `aws:userid`,
  `aws:RequestedRegion`, `aws:SourceArn` and `aws:SourceAccount` (service deliveries).
- S3: `s3:prefix`, `s3:delimiter`, `s3:max-keys` (listings), `s3:x-amz-acl` and the other
  `s3:x-amz-*` request headers (server-side encryption, storage class, copy source, grants),
  `s3:versionid`, `s3:authType`, `s3:signatureversion`, `s3:RequestObjectTag/<k>` (the
  `x-amz-tagging` header of uploads and copies) and `s3:ExistingObjectTag/<k>` (looked up only
  when a policy uses it). Tags sent in a `PutObjectTagging` body are not available as
  `s3:RequestObjectTag`.
- KMS: `kms:CallerAccount`.

`aws:SourceIp` is the address of the connection; `X-Forwarded-For` is not trusted because
HomeCloud has no notion of trusted proxies. `aws:SecureTransport` is true only when the
connection to HomeCloud itself is TLS, so behind a TLS-terminating proxy it is false.

## Coverage

| Service | Signing name | Protocol | Notes |
|---|---|---|---|
| STS | sts | awsQuery | GetCallerIdentity, AssumeRole, GetSessionToken |
| IAM | iam | awsQuery | Users, groups, roles, managed policies (with versions, `arn:aws:iam::aws:policy/...`), inline policies, access keys, instance profiles, permissions boundaries, conditions, simulation |
| S3 | s3 | restXml | Objects, multipart, presigned URLs (SigV4 and SigV2), chunked/trailer uploads, versioning, lifecycle, bucket policies with condition keys, tagging, website; path and virtual-host addressing |
| Lambda | lambda | restJson1 | Functions (zip, S3, container image), invoke (sync/async/tail), versions, aliases, layers, permissions, concurrency, destinations, event source mappings (SQS, DynamoDB streams), function URLs |
| DynamoDB | dynamodb | awsJson 1.0 | Tables, GSIs/LSIs, all item ops with expressions, batch, transactions, PartiQL, TTL, streams |
| SQS | sqs | awsJson 1.0 + awsQuery | Standard and FIFO queues, DLQs and redrive, batch ops, long polling, queue policies |
| SNS | sns | awsQuery | Topics (incl. FIFO), subscriptions (sqs, lambda, http/s, email), filter policies, topic policies, signed messages |
| Secrets Manager | secretsmanager | awsJson 1.1 | Versions and staging labels, rotation via Lambda, resource policies |
| SSM Parameter Store | ssm | awsJson 1.1 | Parameters, hierarchies, versions and labels, SecureString |
| KMS | kms | awsJson 1.1 | Symmetric, RSA, ECC and HMAC keys, aliases, rotation, data keys, key policies, grants (stored) |
| CloudWatch | monitoring | awsJson 1.0 + awsQuery | Metrics, GetMetricData with math, alarms, dashboards |
| CloudWatch Logs | logs | awsJson 1.1 | Groups, streams, events, filter patterns, Logs Insights, metric and subscription filters |
| EventBridge | events | awsJson 1.1 | Buses, rules with full pattern syntax, targets with transforms |
| EventBridge Scheduler | scheduler | restJson1 | Schedules and groups |
| Elastic Load Balancing v2 | elasticloadbalancing | awsQuery | Application load balancers, target groups (instance and ip), listeners (HTTP/HTTPS with ACM certificates), rules (path and host conditions; forward, redirect and fixed-response actions), attributes, tags |
| EC2 | ec2 | ec2 Query | Instances, security groups, key pairs, volumes and snapshots, VPC networking, launch templates, Elastic IPs (see notes) |
| EC2 Auto Scaling | autoscaling | awsQuery | Groups from EC2 launch templates, target-tracking policies (average CPU), target group attachment, tags, suspend/resume, scaling activities |
| Elastic Container Registry | ecr | awsJson 1.1 | Repositories, images (list, describe, batch get/delete), `get-login-password` for `docker login` against the HomeCloud registry, lifecycle and repository policies (stored, not enforced), tag mutability and scan settings (stored), tags |
| Elastic Container Service | ecs | awsJson 1.1 | Clusters, task definitions (one container per task; images, ports, environment, Secrets Manager and SSM secrets, `awslogs` to CloudWatch Logs, task role credentials), services (Fargate/awsvpc, ELBv2 target group, rolling deployments with deployment status), RunTask/StopTask, capacity providers (FARGATE), tags |
| Relational Database Service | rds | awsQuery | PostgreSQL, MySQL and MariaDB instances backed by containers: create, describe (with filters), modify, delete (final snapshot), reboot, start, stop; DB snapshots and restore; DB subnet groups; DB parameter groups (stored, not applied to the engine); `ManageMasterUserPassword` (the secret lives in Secrets Manager); engine versions and orderable options; tags. Multi-AZ, read replicas, clusters (Aurora) and engine upgrades are not supported |
| Step Functions | states | awsJson 1.0 | State machines, executions, task tokens, service integrations |

Notes:
- Terraform's S3 support prefixes the endpoint host with the account ID, so use `http://localhost:PORT`, not an IP address.
- EC2 Elastic IPs (`AllocateAddress`, `DescribeAddresses`, `AssociateAddress`, `DisassociateAddress`, `ReleaseAddress`) are records, not routed addresses. An allocation (`eipalloc-...`) reserves a stable address from the pool in the `elastic_ip_pool` config setting (default `203.0.113.0/24`, a documentation range), and an association (`eipassoc-...`) makes an instance report it as its public IP in `DescribeInstances`, network interfaces and the instance metadata service. It follows the instance across stop and start and is released from it when the instance is terminated. Only `vpc` addresses, one Elastic IP per instance (a second association replaces the first) and instance or primary-interface targets are supported, and bringing your own address is not. Traffic to an instance still arrives on the host ports published by its security groups at the HomeCloud host (`public_host`), not on the Elastic IP.
- Security group changes (`Authorize`/`Revoke...Ingress`, `ModifyInstanceAttribute --groups`, `ModifyNetworkInterfaceAttribute --groups`) apply to running instances: HomeCloud recreates the instance's container when its published ports change (processes restart, disk, volumes and IP are kept). Egress rules are recorded but not enforced.
- `StartSyncExecution` clients must disable host-prefix injection (`AWS_DISABLE_HOST_PREFIX_INJECTION=true`).

## Adding operations (for contributors)

The `internal/awsapi` package verifies signatures, resolves the service from the credential
scope and decodes the protocol. A service registers its operations once at startup:

```go
awsapi.Register(&awsapi.Service{
	Name: "sqs", JSONPrefix: "AmazonSQS", JSONVersion: "1.0",
	Ops: map[string]awsapi.Op{"SendMessage": s.awsSendMessage},
})

func (s *Service) awsSendMessage(q *awsapi.Req) (any, error) {
	var in struct{ QueueUrl, MessageBody string }
	if err := q.Bind(&in); err != nil { // awsJson body; awsQuery uses q.Param/q.List/q.Map
		return nil, err
	}
	if err := q.Authorize("sqs:SendMessage", arn); err != nil {
		return nil, err
	}
	... call the same internal functions the native API uses ...
	return map[string]any{"MessageId": id}, nil // JSON, or XML for awsQuery (see xml.go)
}
```

- **Always call `q.Authorize`** with the IAM action and resource ARN before acting. It also sets
  what CloudTrail records.
- Return `*awsapi.Error` for AWS-specific error codes (`awsapi.Errorf(400, "QueueDoesNotExist", ...)`);
  `core.Error`s are mapped through `Service.ErrorCode` and `awsapi.DefaultErrorCode`.
- Share logic with the native handlers. Refactor a handler's body into a method that both call
  rather than duplicating validation.
- REST services (S3, Lambda) set `REST` and route by method and path themselves.
- Test with the real AWS CLI and boto3 (`AWS_ENDPOINT_URL` pointing at a test server).
