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

## Coverage

| Service | Signing name | Protocol | Notes |
|---|---|---|---|
| STS | sts | awsQuery | GetCallerIdentity, AssumeRole, GetSessionToken |
| IAM | iam | awsQuery | Users, groups, roles, managed policies (with versions, `arn:aws:iam::aws:policy/...`), inline policies, access keys, instance profiles, permissions boundaries, conditions, simulation |
| S3 | s3 | restXml | Objects, multipart, presigned URLs (SigV4 and SigV2), chunked/trailer uploads, versioning, lifecycle, policy, tagging, website; path and virtual-host addressing |
| Lambda | lambda | restJson1 | Functions (zip, S3, container image), invoke (sync/async/tail), versions, aliases, layers, permissions, concurrency, destinations, event source mappings (SQS, DynamoDB streams), function URLs |
| DynamoDB | dynamodb | awsJson 1.0 | Tables, GSIs/LSIs, all item ops with expressions, batch, transactions, PartiQL, TTL, streams |
| SQS | sqs | awsJson 1.0 + awsQuery | Standard and FIFO queues, DLQs and redrive, batch ops, long polling |
| SNS | sns | awsQuery | Topics (incl. FIFO), subscriptions (sqs, lambda, http/s, email), filter policies, signed messages |
| Secrets Manager | secretsmanager | awsJson 1.1 | Versions and staging labels, rotation via Lambda, resource policies |
| SSM Parameter Store | ssm | awsJson 1.1 | Parameters, hierarchies, versions and labels, SecureString |
| KMS | kms | awsJson 1.1 | Symmetric, RSA, ECC and HMAC keys, aliases, rotation, data keys, grants |
| CloudWatch | monitoring | awsJson 1.0 + awsQuery | Metrics, GetMetricData with math, alarms, dashboards |
| CloudWatch Logs | logs | awsJson 1.1 | Groups, streams, events, filter patterns, Logs Insights, metric and subscription filters |
| EventBridge | events | awsJson 1.1 | Buses, rules with full pattern syntax, targets with transforms |
| EventBridge Scheduler | scheduler | restJson1 | Schedules and groups |
| Elastic Load Balancing v2 | elasticloadbalancing | awsQuery | Application load balancers, target groups (instance and ip), listeners (HTTP/HTTPS with ACM certificates), rules (path and host conditions; forward, redirect and fixed-response actions), attributes, tags |
| EC2 Auto Scaling | autoscaling | awsQuery | Groups from EC2 launch templates, target-tracking policies (average CPU), target group attachment, tags, suspend/resume, scaling activities |
| Elastic Container Registry | ecr | awsJson 1.1 | Repositories, images (list, describe, batch get/delete), `get-login-password` for `docker login` against the HomeCloud registry, lifecycle and repository policies (stored, not enforced), tag mutability and scan settings (stored), tags |
| Elastic Container Service | ecs | awsJson 1.1 | Clusters, task definitions (one container per task; images, ports, environment, Secrets Manager and SSM secrets, `awslogs` to CloudWatch Logs, task role credentials), services (Fargate/awsvpc, ELBv2 target group, rolling deployments with deployment status), RunTask/StopTask, capacity providers (FARGATE), tags |
| Step Functions | states | awsJson 1.0 | State machines, executions, task tokens, service integrations |

Notes:
- Terraform's S3 support prefixes the endpoint host with the account ID, so use `http://localhost:PORT`, not an IP address.
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
