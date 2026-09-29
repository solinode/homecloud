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

| Service | Protocol | Status |
|---|---|---|
| STS | awsQuery | GetCallerIdentity, AssumeRole, GetSessionToken |

Other services are being added; see the table in `CHANGELOG.md`.

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
