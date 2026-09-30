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

`aws:SourceIp` is the address of the connection, and `aws:SecureTransport` is true only when the
connection to HomeCloud itself is TLS. `X-Forwarded-For` and `X-Forwarded-Proto` are ignored unless the
connection comes from a proxy listed in `--trusted-proxies`; then the rightmost `X-Forwarded-For` entry
that is not itself a trusted proxy is the source address, and `X-Forwarded-Proto: https` makes
`aws:SecureTransport` true.

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
| Secrets Manager | secretsmanager | awsJson 1.1 | Versions and staging labels, rotation via Lambda, resource policies. Replication to other regions is not available: `AddReplicaRegions` and `ReplicateSecretToRegions` fail with `InvalidRequestException` because HomeCloud serves a single region |
| SSM Parameter Store | ssm | awsJson 1.1 | Parameters, hierarchies, versions and labels, SecureString |
| KMS | kms | awsJson 1.1 | Symmetric, RSA, ECC and HMAC keys, aliases, rotation, data keys, key policies, grants (stored), imported key material (`Origin=EXTERNAL`: `GetParametersForImport`, `ImportKeyMaterial` with RSAES_OAEP and RSA_AES_KEY_WRAP, expiry, `DeleteImportedKeyMaterial`; re-import needs the same material), multi-Region primary keys (`mrk-` ids). HomeCloud is one region, so `ReplicateKey` and `UpdatePrimaryRegion` return `UnsupportedOperationException` |
| CloudWatch | monitoring | awsJson 1.0 + awsQuery | Metrics, GetMetricData with math (including `FILL(m, value \| REPEAT \| LINEAR)` and `ANOMALY_DETECTION_BAND`), alarms including anomaly detection alarms (`ThresholdMetricId`), anomaly detectors (`PutAnomalyDetector`, `DescribeAnomalyDetectors`, `DeleteAnomalyDetector`; single-metric and metric-math, `ExcludedTimeRanges`, `MetricTimezone`), dashboards |
| CloudWatch Logs | logs | awsJson 1.1 | Groups, streams, events, filter patterns, Logs Insights, metric and subscription filters |
| EventBridge | events | awsJson 1.1 | Buses, rules with full pattern syntax, targets with transforms |
| EventBridge Scheduler | scheduler | restJson1 | Schedules and groups |
| Elastic Load Balancing v2 | elasticloadbalancing | awsQuery | Application load balancers, target groups (instance and ip), listeners (HTTP/HTTPS with ACM certificates), rules (path and host conditions; forward, redirect and fixed-response actions), attributes, tags |
| EC2 | ec2 | ec2 Query | Instances (containers, or virtual machines from the `(VM)` AMIs), security groups, key pairs, volumes and snapshots, VPC networking, launch templates, Elastic IPs (see notes) |
| EC2 Auto Scaling | autoscaling | awsQuery | Groups from EC2 launch templates, target-tracking policies (average CPU), target group attachment, tags, suspend/resume, scaling activities |
| Elastic Container Registry | ecr | awsJson 1.1 | Repositories, images (list, describe, batch get/delete), `get-login-password` for `docker login` against the HomeCloud registry, lifecycle and repository policies (stored, not enforced), tag mutability and scan settings (stored), tags |
| Elastic Container Service | ecs | awsJson 1.1 | Clusters, task definitions (one container per task; images, ports, environment, Secrets Manager and SSM secrets, `awslogs` to CloudWatch Logs, task role credentials), services (Fargate/awsvpc, ELBv2 target group, rolling deployments with deployment status), RunTask/StopTask, capacity providers (FARGATE), tags |
| CloudTrail | cloudtrail | awsJson 1.1 | `LookupEvents` (attributes EventId, EventName, Username, ResourceName, ResourceType, EventSource, AccessKeyId, ReadOnly; time range; pagination), trails (`CreateTrail`, `DescribeTrails`, `GetTrail`, `ListTrails`, `UpdateTrail`, `DeleteTrail`, `GetTrailStatus`, `StartLogging`, `StopLogging`) with log delivery to S3 in the AWS layout, event selectors (stored), tags (see notes) |
| Cognito user pools | cognito-idp | awsJson 1.1 | Pools, app clients (with secrets), users, groups and tags; `AdminCreateUser`, `AdminSetUserPassword`, `ListUsers` (filters), `SignUp`/`ConfirmSignUp`, `InitiateAuth` (`USER_SRP_AUTH`, `USER_PASSWORD_AUTH`, `REFRESH_TOKEN_AUTH`), `RespondToAuthChallenge` (`PASSWORD_VERIFIER`, `NEW_PASSWORD_REQUIRED`), `ForgotPassword`/`ConfirmForgotPassword`, `AdminResetUserPassword`, `GetUser`, `GlobalSignOut`; RS256 tokens with a JWKS, usable by API Gateway authorizers (see notes) |
| Relational Database Service | rds | awsQuery | PostgreSQL, MySQL and MariaDB instances backed by containers: create, describe (with filters), modify, delete (final snapshot), reboot, start, stop; DB snapshots and restore; DB subnet groups; DB parameter groups (stored, not applied to the engine); `ManageMasterUserPassword` (the secret lives in Secrets Manager); engine versions and orderable options; tags. Multi-AZ, read replicas, clusters (Aurora) and engine upgrades are not supported |
| ElastiCache | elasticache | awsQuery | Redis, Valkey and Memcached clusters backed by containers: create (optionally from a snapshot), describe with `ShowCacheNodeInfo` endpoints, modify (node type, snapshot retention, security groups), reboot, delete (final snapshot); replication groups with a primary endpoint and a reader endpoint; cache subnet groups; snapshots and restore (Redis and Valkey); engine versions; tags. Redis and Valkey run without a password unless `AuthToken` is set. One node per cluster and one primary per replication group: replicas, cluster mode, automatic failover, Multi-AZ, scaling out, `AuthToken` rotation, TLS (`TransitEncryptionEnabled` is refused), cache security groups, parameter groups (`CacheParameterGroupName` is stored and reported), users and serverless caches are not supported. `AtRestEncryptionEnabled` is stored and reported, not applied |
| Elastic File System | elasticfilesystem | restJson1 | File systems backed by Docker volumes (`CreationToken` idempotency, `UpdateFileSystem` throughput settings stored), mount targets (an address from the subnet, one per zone, security groups), access points, tags, lifecycle configuration, backup policy and file system policy (stored, not applied). Instances and tasks mount a file system through the native `file_systems` launch option; the NFS mount target address is a record, and access point POSIX users and root directories are stored and reported but not enforced. `SizeInBytes` is measured when the file system is read through the native API. Replication, `PutAccountPreferences` and provisioned throughput billing are not supported |
| API Gateway (HTTP APIs) | apigateway | restJson1 (apigatewayv2) | APIs (with CORS configuration and quick create), routes (`$default`, `METHOD /path`, `{proxy+}`), integrations (`AWS_PROXY` Lambda with payload format 1.0 or 2.0, `HTTP_PROXY`), stages (`$default`, named, auto-deploy, stage variables), deployments, JWT authorizers (Cognito user pool issuers), tags. `ApiEndpoint` is `<public_host>:<port>/apigw/<api-id>`; a named stage is the first path segment and `$default` serves the root. Lambda integrations need the function's resource policy to allow `apigateway.amazonaws.com` (`lambda:InvokeFunction`, optionally with `SourceArn` `arn:aws:execute-api:<region>:<account>:<api-id>/<stage>/<METHOD>/<route path>` and `SourceAccount`), as in AWS; APIs created through the native API keep working without one. Routes are live as soon as they are saved: a deployment only records a point in time and does not snapshot the configuration. WebSocket APIs, REST APIs (`/restapis`), custom domains, VPC links, models, `AWS_IAM` and Lambda (`REQUEST`) authorizers, `MOCK`/`AWS`/`HTTP` and non-Lambda service integrations, request/response mapping, throttling and access logs are not supported |
| Route 53 | route53 | restXml | Public and private hosted zones (private with VPC association), `ChangeResourceRecordSets` (CREATE/UPSERT/DELETE, applied atomically) for A, AAAA, CNAME, TXT, MX, SRV, NS, CAA, PTR and alias records to load balancers, `ListResourceRecordSets` (with start-at and paging), `GetChange` (always `INSYNC`), Associate/DisassociateVPC, `ListHostedZonesByName`/`ByVPC`, hosted zone comments, tags. Records are served by HomeCloud's DNS, so they resolve (see notes) |
| Certificate Manager | acm | awsJson 1.1 | `RequestCertificate` (DNS validation; issued at once from HomeCloud's private CA), `ImportCertificate` (and re-import), `DescribeCertificate` (including the DNS validation CNAME records), `ListCertificates`, `GetCertificate`, `ExportCertificate` (private certificates), `DeleteCertificate` (`ResourceInUseException` while a load balancer listener uses it), `RenewCertificate`, tags. Certificate ARNs work in ELBv2 HTTPS listeners |
| Step Functions | states | awsJson 1.0 | State machines, executions, task tokens, service integrations |
| CloudFormation | cloudformation | awsQuery | Stacks (create, update, delete, describe, list with status filters, events, resources, `GetTemplate`, `GetTemplateSummary`, `ValidateTemplate`), change sets (`CREATE` and `UPDATE`, so `aws cloudformation deploy`, CDK and Terraform's `aws_cloudformation_stack` work), exports and imports, termination protection, service roles, `TemplateURL` from HomeCloud S3. See [CloudFormation](#cloudformation) |

Notes:
- Terraform's S3 support prefixes the endpoint host with the account ID, so use `http://localhost:PORT`, not an IP address.
- EC2 VM instances: the AMIs `ami-ubuntu-24-04-vm` and `ami-debian-12-vm` (named `homecloud/vm/...`, so an `aws_ami` lookup for the usual `ubuntu/images/...` names still finds the container images) boot the official cloud image as a QEMU virtual machine; `DescribeImages` and `DescribeInstances` report `VirtualizationType: hvm` and `Hypervisor: kvm` for them (`xen` for container images). `RunInstances` takes the same parameters: `KeyName` authorizes the key for the image's default user (`ubuntu`, `debian`), `UserData` goes to cloud-init unchanged (scripts, `#cloud-config`), and the root `BlockDeviceMappings` entry (`/dev/xvda`, `/dev/sda1` or `/dev/sda`) sets the root disk size (default 8 GiB, at least 4) and `DeleteOnTermination`. The root disk shows up in `DescribeVolumes` and `DescribeInstances` as an attached volume at `/dev/xvda`. `GetConsoleOutput` returns the guest's serial console. Instances stay `pending` until the guest has booted. Guests are emulated, and slow, when the Docker host has no `/dev/kvm`; the native API's `virtualization` field says `kvm` or `emulated`. Extra `BlockDeviceMappings` (for example `/dev/sdf`), `AttachVolume` and `DetachVolume` work: the volumes are virtio disks in the guest (`/dev/vdb`, `/dev/vdc`, ... in device-name order, always `/dev/disk/by-id/virtio-<first 20 characters of the volume ID>`), but there is no hot-plug: attaching or detaching rebuilds the VM container, so the guest reboots (the attachment is `attaching`/`detaching` until then). The root volume cannot be detached. `CreateSnapshot` of the root volume and `CreateImage` of a VM instance flatten the disk into a standalone copy (a running guest's disk is copied crash-consistently); an image made from an instance launches into a new instance on a host of the same architecture, with a root volume at least as large as the image's disk. Not supported yet, with an `UnsupportedOperation` error: file systems at launch, the browser terminal and run-command. CloudWatch metrics of VM disks are not covered either.
- EC2 Elastic IPs (`AllocateAddress`, `DescribeAddresses`, `AssociateAddress`, `DisassociateAddress`, `ReleaseAddress`) are records, not routed addresses. An allocation (`eipalloc-...`) reserves a stable address from the pool in the `elastic_ip_pool` config setting (default `203.0.113.0/24`, a documentation range), and an association (`eipassoc-...`) makes an instance report it as its public IP in `DescribeInstances`, network interfaces and the instance metadata service. It follows the instance across stop and start and is released from it when the instance is terminated. Only `vpc` addresses, one Elastic IP per instance (a second association replaces the first) and instance or primary-interface targets are supported, and bringing your own address is not. Traffic to an instance still arrives on the host ports published by its security groups at the HomeCloud host (`public_host`), not on the Elastic IP.
- Security groups are enforced inside the VPC. Ingress rules (protocol, ports or ICMP type/code, and an IPv4 CIDR or a referenced group) decide which resources may open connections to an instance, ECS task, RDS or ElastiCache node, load balancer or VPC-attached Lambda environment; anything else is dropped, replies to allowed connections flow back, and a resource with no group is in the VPC's default group, which allows traffic from its own members. Egress rules are enforced once the default allow-all egress rule has been revoked (DNS and the metadata service always stay reachable). A load balancer's traffic to its targets, health checks included, is judged by the targets' groups. Rules are programmed live into each container's network namespace when rules or group membership change, when resources start, restart or terminate, and when the members of a referenced group change; see [architecture.md](architecture.md#limits-and-differences-from-aws) for the mechanism and its limits (prefix lists and IPv6 sources match nothing, EFS mount targets are recorded only, network ACLs are not enforced). Rules that publish host ports (`0.0.0.0/0` or `127.0.0.1/32`, TCP/UDP, up to 32 ports) still recreate an instance's container when its published ports change (processes restart, disk, volumes and IP are kept); filtering changes never restart anything. Requests use the query API's `IpPermissions.N.Groups` member (`UserIdGroupPairs` is accepted too) for group sources.
- Route 53 records are rendered into CoreDNS zone files and picked up within a few seconds; `GetChange` reports `INSYNC` immediately. Public zones answer on the host's DNS port and inside VPCs, private zones only inside their associated VPCs. Each zone has a default apex NS and SOA (shown in listings, not deletable). Alias targets are load balancer DNS names (`<name>.elb.internal`, as returned by ELBv2) and follow the balancer's address. Not supported: routing policies (weighted, latency, failover, geolocation, multivalue; `SetIdentifier` is rejected), health checks, DNSSEC, traffic policies, delegation sets, other alias targets (CloudFront, S3 websites), and `SPF`/`NAPTR`/`DS` records. Names are absolute with or without the trailing dot; a wildcard is returned as `\052`. A public zone's name server is `ns.<zone>`.
- ACM certificates come from HomeCloud's **private CA**, not a public one: browsers and clients must trust the CA (`GET /api/v1/acm/ca?format=pem`). Requests are issued immediately and are `ISSUED` with `Type` `AMAZON_ISSUED` (so Terraform's validation flow runs); DNS validation records are reported (so `aws_acm_certificate_validation` and Route 53 record loops work) but not checked. Keys are ECDSA P-256 whatever `KeyAlgorithm` asks. `ExportCertificate` returns the key encrypted with the passphrase (legacy PEM encryption, `openssl ec -passin`); imported certificates cannot be exported, as in AWS. A tag policy, `UpdateCertificateOptions` and account configuration are accepted but not applied.
- `StartSyncExecution` clients must disable host-prefix injection (`AWS_DISABLE_HOST_PREFIX_INJECTION=true`).
- CloudTrail records every call made over the AWS protocol (reads included) and every mutating or denied call of the native API, kept for 90 days. `LookupEvents` returns them newest first with the AWS event-record JSON in `CloudTrailEvent`; request parameters and response elements are not recorded (they may hold passwords and secrets), so `requestParameters` and `responseElements` are `null`. Calls made without credentials (Cognito sign-in) are not recorded. `EventName` is the IAM action's name, `Username` the calling IAM user, and `ResourceName`/`ResourceType` come from the ARN the call was authorized against. Trails deliver gzipped `{"Records": [...]}` files to their S3 bucket every minute and on `StopLogging`, under `[prefix/]AWSLogs/<account>/CloudTrail/us-east-1/YYYY/MM/DD/<account>_CloudTrail_us-east-1_<time>_<id>.json.gz`, from `StartLogging` on. Not implemented: digest files (log file validation is stored only), data and insight events (event selectors are stored; management events are always recorded), SNS/CloudWatch Logs delivery, organization trails, the bucket policy check, and `LookupEvents` on regions other than us-east-1.
- Cognito SignUp, ConfirmSignUp, ResendConfirmationCode, ForgotPassword, ConfirmForgotPassword, InitiateAuth, RespondToAuthChallenge, GetUser, GlobalSignOut, ChangePassword, UpdateUserAttributes, DeleteUser and RevokeToken need no signature (as in AWS; the SDKs send them unsigned); every other Cognito operation needs SigV4 and IAM. A signature that is present must be valid. Sign-in supports `USER_SRP_AUTH` (SRP-6a with Cognito's 3072-bit group, as amazon-cognito-identity-js, Amplify, pycognito and warrant speak it: `PASSWORD_VERIFIER` challenge, `PASSWORD_CLAIM_SIGNATURE` answer), `USER_PASSWORD_AUTH`, `ADMIN_USER_PASSWORD_AUTH`/`ADMIN_NO_SRP_AUTH` and `REFRESH_TOKEN_AUTH`; custom auth, device SRP (`DEVICE_SRP_AUTH`), MFA, hosted UI/OAuth flows, identity providers, domains and attribute verification are not implemented. HomeCloud keeps an SRP salt and verifier next to each password hash, refreshed whenever a password is set (`AdminCreateUser`, `SignUp`, `AdminSetUserPassword`, `ChangePassword`, `NEW_PASSWORD_REQUIRED`, `ConfirmForgotPassword`). Users created before SRP support have none: their SRP sign-in fails with `NotAuthorizedException` until their password is set again or they sign in once with `USER_PASSWORD_AUTH`, which derives it. Each challenge's `SECRET_BLOCK` answers once and expires after five minutes (challenges are kept in memory, so a restart drops them); unknown users get a challenge too and fail at the answer, and a wrong password, `UserNotConfirmedException`, `PasswordResetRequiredException` or a `NEW_PASSWORD_REQUIRED` challenge only shows after the password is proven. `ForgotPassword` writes a six-digit code (valid one hour, never a password) to the server log like the sign-up code; `ConfirmForgotPassword` sets the password, confirms the user and signs out their tokens; `AdminResetUserPassword` sets the status to `RESET_REQUIRED` until a reset is confirmed. A client's `ExplicitAuthFlows`, when set, is enforced (`ALLOW_USER_SRP_AUTH` for SRP). Tokens are the JWTs the native API issues: RS256, `kid` in the pool's JWKS at `<endpoint>/cognito/<pool-id>/.well-known/jwks.json`, and accepted by API Gateway JWT authorizers. Clients with a secret verify `SECRET_HASH`. HomeCloud sends no e-mail or SMS: a pool confirms self-registered users at once (`UserConfirmed: true`) unless `auto_confirm` is turned off through the native API (`PATCH /api/v1/cognito/user-pools/{id}`), in which case SignUp writes a six-digit code to the server log (never a password) for `ConfirmSignUp`, and an administrator can use `AdminConfirmSignUp`. `AdminCreateUser` cannot e-mail a temporary password: pass `TemporaryPassword` or call `AdminSetUserPassword`. Other pool settings (Lambda triggers, verification messages, recovery settings) are stored and echoed by `DescribeUserPool` but not acted on. A pool created over the AWS protocol requires upper- and lowercase letters, numbers and symbols by default, as AWS does; refresh token lifetimes are rounded up to whole days.

## CloudFormation

`aws cloudformation ...`, `aws cloudformation deploy`, boto3 (including the `stack_create_complete`,
`stack_update_complete` and `stack_delete_complete` waiters), CDK and Terraform's
`aws_cloudformation_stack` work against the same engine as the native `/api/v1/cloudformation` API.

**Operations:** `CreateStack`, `UpdateStack`, `DeleteStack`, `DescribeStacks`, `ListStacks`
(`StackStatusFilter`; deleted stacks stay listed and readable by stack ID), `DescribeStackEvents`,
`DescribeStackResources`, `DescribeStackResource`, `ListStackResources`, `GetTemplate`,
`GetTemplateSummary`, `ValidateTemplate`, `CreateChangeSet` (`CREATE` and `UPDATE`),
`DescribeChangeSet`, `ExecuteChangeSet`, `DeleteChangeSet`, `ListChangeSets`, `ListExports`,
`ListImports`, `UpdateTerminationProtection`, `ContinueUpdateRollback`.

**Templates:** JSON or YAML with the short-form tags. Sections: `Parameters` (String, Number,
List, CommaDelimitedList, `AWS::SSM::Parameter::Value<...>`; `Default`, `AllowedValues`,
`AllowedPattern`, length and value constraints, `NoEcho`, shown as `****`), `Mappings`,
`Conditions`, `Resources` (`Condition`, `DependsOn`, `DeletionPolicy` `Retain`/`Delete`,
`UpdateReplacePolicy` `Retain`), `Outputs` (`Condition`, `Export`). Intrinsics: `Ref`,
`Fn::GetAtt`, `Fn::Sub`, `Fn::Join`, `Fn::Select`, `Fn::Split`, `Fn::If`, `Fn::Equals`, `Fn::And`,
`Fn::Or`, `Fn::Not`, `Condition`, `Fn::FindInMap`, `Fn::ImportValue`, `Fn::Base64`, `Fn::GetAZs`,
`Fn::Length`, `Fn::ToJsonString`. Pseudo parameters: `AWS::AccountId`, `AWS::Region`,
`AWS::StackName`, `AWS::StackId`, `AWS::Partition`, `AWS::URLSuffix`, `AWS::NoValue`,
`AWS::NotificationARNs`. `TemplateURL` may name an object in a HomeCloud bucket (virtual-hosted or
path style); it is read with the caller's permissions. Stack IDs are
`arn:aws:cloudformation:<region>:<account>:stack/<name>/<uuid>`.

**Authorization:** every operation is authorized as `cloudformation:<Action>` on the stack ARN.
Resources are created, updated and deleted with the permissions of the calling principal
(re-read for each call), so a user without `sqs:CreateQueue` cannot make a queue through a stack:
the stack fails and rolls back with the denied action in the event. With `RoleARN`, the caller
needs `iam:PassRole` and the role's trust policy must allow `cloudformation.amazonaws.com`; the
stack then acts as that role.

**Capabilities:** templates with IAM resources need `CAPABILITY_IAM`, or `CAPABILITY_NAMED_IAM`
when they name a role, user, group or managed policy (`InsufficientCapabilitiesException`).

**Failure handling:** create failures roll back (`ROLLBACK_IN_PROGRESS`, `ROLLBACK_COMPLETE`);
`DisableRollback`/`OnFailure=DO_NOTHING` keeps what was made (`CREATE_FAILED`),
`OnFailure=DELETE` removes the stack. A resource type HomeCloud cannot create fails the stack
with `Resource type X is not supported by HomeCloud` in the events; a malformed type name is
rejected up front. The AWS CLI and SDK waiters poll with long delays, so create, update and delete
requests wait up to three seconds for a quick operation to finish before returning.

**Supported resource types** (properties are translated to the native API):

| Service | Types |
|---|---|
| S3 | `AWS::S3::Bucket` (versioning, object lock, `PublicRead`, website, tags), `AWS::S3::BucketPolicy` |
| SQS | `AWS::SQS::Queue` (FIFO, redrive, encryption, tags), `AWS::SQS::QueuePolicy` |
| SNS | `AWS::SNS::Topic` (with inline subscriptions), `AWS::SNS::Subscription`, `AWS::SNS::TopicPolicy` |
| DynamoDB | `AWS::DynamoDB::Table` (GSIs, LSIs, streams, TTL, PITR, deletion protection) |
| Lambda | `AWS::Lambda::Function` (inline ZipFile for Node.js, Python and Ruby, S3 code, images), `Permission`, `Url`, `EventSourceMapping`, `Version`, `Alias`, `LayerVersion` |
| API Gateway v2 | `AWS::ApiGatewayV2::Api` (HTTP), `Integration` (Lambda proxy), `Route`, `Authorizer` (JWT), `Stage`, `Deployment` |
| IAM | `AWS::IAM::Role`, `User`, `Group`, `Policy`, `ManagedPolicy`, `InstanceProfile` |
| SSM, Secrets Manager, KMS | `AWS::SSM::Parameter`, `AWS::SecretsManager::Secret` (including `GenerateSecretString`), `AWS::KMS::Key`, `AWS::KMS::Alias` |
| Logs, CloudWatch, EventBridge, Step Functions | `AWS::Logs::LogGroup`, `AWS::CloudWatch::Alarm`, `AWS::Events::Rule`, `AWS::StepFunctions::StateMachine` |
| EC2 | `AWS::EC2::VPC`, `Subnet`, `SecurityGroup`, `SecurityGroupIngress`, `InternetGateway`, `VPCGatewayAttachment`, `RouteTable`, `Route`, `SubnetRouteTableAssociation`, `EIP`, `EIPAssociation`, `Volume`, `Instance` |
| ELBv2 | `AWS::ElasticLoadBalancingV2::TargetGroup`, `LoadBalancer` (application), `Listener`, `ListenerRule` |
| ECS, ECR | `AWS::ECS::TaskDefinition`, `AWS::ECS::Service` (default cluster), `AWS::ECR::Repository` |
| RDS | `AWS::RDS::DBInstance`, `DBSubnetGroup` and `DBParameterGroup` (recorded by the stack only) |
| Route 53, ACM | `AWS::Route53::HostedZone`, `RecordSet`, `RecordSetGroup`, `AWS::ACM::Certificate` |
| CDK | `AWS::CDK::Metadata` (accepted, creates nothing) |

**Differences from AWS:**
- Updates change these in place, where the service behind them can: SQS queue attributes and tags,
  SNS topic display name, tags and FIFO deduplication, SQS/SNS policies, SSM parameter value and
  description, Secrets Manager secret value, description and tags (a `GenerateSecretString` secret
  keeps its value), Lambda code, configuration and reserved concurrency, DynamoDB TTL, stream and tags,
  S3 versioning, website and tags, IAM role trust policy, managed and inline policies, IAM inline
  `Policy`, CloudWatch alarm settings, log group retention, EventBridge rule pattern, schedule and
  targets, ECS service desired count and task definition, security group ingress rules, Route 53
  record sets. Changing any other property replaces the resource, and change sets report
  `Replacement` `True`, `False` or `Conditional` per resource, with per-property `Details`. A
  replacement creates the new resource first and deletes the old one during
  `UPDATE_COMPLETE_CLEANUP_IN_PROGRESS`, so a replacement that keeps a custom physical name fails as
  in AWS (`CloudFormation cannot update a stack when a custom-named resource requires replacing`);
  give the resource a new name or none. Resources the update drops are deleted in the same cleanup
  phase, after the update succeeded. Types without an in-place update (everything not listed, and
  the `HC::` types) are always replaced; DynamoDB billing settings are accepted and not applied, as
  capacity is not metered.
- A failed update rolls back to the previous template and parameters
  (`UPDATE_ROLLBACK_IN_PROGRESS`, `UPDATE_ROLLBACK_COMPLETE_CLEANUP_IN_PROGRESS`,
  `UPDATE_ROLLBACK_COMPLETE`): modified resources get their old properties back and resources the
  update created are deleted. A rollback that cannot finish ends in `UPDATE_ROLLBACK_FAILED` and
  resumes with `ContinueUpdateRollback` (`ResourcesToSkip` gives up on a resource). `DisableRollback`
  on `UpdateStack` or `ExecuteChangeSet` (and `aws cloudformation deploy --disable-rollback`) leaves the
  stack in `UPDATE_FAILED` with what was done; the old resources of replacements stay until the
  next successful update or the deletion of the stack. Rollback configuration, monitoring alarms and
  `CancelUpdateStack` are not supported.
- Properties HomeCloud cannot express are ignored when harmless and rejected with `Property X is
  not supported by HomeCloud` when they change behavior. Only `us-east-1` exists; stack sets,
  drift detection, macros and transforms (`AWS::Serverless`), nested stacks, custom resources,
  stack policies, `ResourceSignal`/wait conditions, `Fn::Cidr`, import change sets, resource
  import and `CancelUpdateStack` are not supported. Tags on a stack are stored but not
  propagated to its resources. `AWS::ECS::Cluster`, EFS, ElastiCache and other services without a
  listed type are not available in templates yet.
- Deleting an S3 bucket that still holds objects fails the resource (`DELETE_FAILED`), as in AWS.

## Deploy a full app with Terraform

[`examples/terraform/shop`](../examples/terraform/shop/README.md) is a realistic stack written with the
standard `hashicorp/aws` provider (Terraform or OpenTofu): a VPC with public subnets, an ALB with HTTP and
HTTPS (ACM certificate validated through Route 53), an ECS Fargate service, RDS Postgres with a managed
Secrets Manager password, S3, DynamoDB, SNS to SQS to Lambda, and an API Gateway HTTP API. Only the provider's
`endpoints {}` block points at HomeCloud. `apply`, a second `plan` with no changes, and `destroy` all run
clean against a fresh install; the README shows how to check each piece.

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
