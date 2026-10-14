# AWS CDK on HomeCloud

A small CDK app (TypeScript) deployed with the stock CDK CLI: `cdk bootstrap`, `cdk deploy`, `cdk diff`
and `cdk destroy` all run against HomeCloud. Nothing in the app is HomeCloud specific; the same code
deploys to AWS.

What it builds (`lib/stack.ts`):

| Resource | Notes |
| --- | --- |
| S3 bucket | versioned, `RemovalPolicy.DESTROY` |
| DynamoDB table | on-demand, partition key `id` |
| SQS queue | 60 s visibility timeout |
| Lambda function | Node.js 22, inline code (`lambda.Code.fromInline`), subscribed to the queue, writes each message to the table |
| CloudWatch log group | the function's `logGroup`, so `cdk destroy` removes it |
| IAM | the function's role and the policies CDK generates for the queue and the table |

Verified with CDK CLI 2.1145.0, `aws-cdk-lib` 2.273.0, Node.js 24 and HomeCloud 0.5.x (the bootstrap
stack needs the fixes listed at the end, so use a build from `main` until the next release).

## Prerequisites

* A running HomeCloud **served over HTTPS** (see [why](#why-https)). The built-in self-signed certificate
  is enough:

  ```bash
  homecloud serve --tls-self-signed
  ```

  It covers `localhost`, `*.localhost`, `127.0.0.1` and `--public-host`.
* Node.js 18 or newer and the AWS CLI (only to check the result).

## Run it

```bash
eval "$(homecloud aws-env)"            # AWS_ENDPOINT_URL=https://localhost:8080, keys, region, AWS_CA_BUNDLE
export NODE_EXTRA_CA_CERTS="$AWS_CA_BUNDLE"   # the CDK CLI is Node.js: trust HomeCloud's certificate

cd examples/cdk/basic
npm install
npx cdk bootstrap                      # creates the CDKToolkit stack (staging bucket, ECR repository, roles, SSM parameter)
npx cdk deploy --require-approval never --outputs-file outputs.json
```

The CDK CLI finds the account with `sts get-caller-identity`, as on AWS, and uses `us-east-1` from
`AWS_REGION`. No `--endpoint` flags, custom bootstrap template or legacy synthesizer is needed: the
default synthesizer assumes the bootstrap roles (`cdk-hnb659fds-deploy-role-...`,
`...-file-publishing-role-...`), uploads the template to the staging bucket and deploys it through a
CloudFormation change set.

## Check that it works

```bash
QUEUE=$(jq -r .HomeCloudBasic.QueueUrl outputs.json)
TABLE=$(jq -r .HomeCloudBasic.TableName outputs.json)
LOGS=$(jq -r .HomeCloudBasic.LogGroupName outputs.json)

aws sqs send-message --queue-url "$QUEUE" --message-body '{"order":1}'
aws logs tail "$LOGS" --since 5m       # ... INFO order received: {"order":1}
aws dynamodb scan --table-name "$TABLE" --query 'Items[].body.S'    # ["{\"order\":1}"]
```

`npx cdk diff` then reports no differences, and a second `npx cdk deploy` reports `(no changes)`.

## Tear down

The bucket is versioned, and CloudFormation (on AWS as on HomeCloud) does not delete a bucket that still
holds objects. Empty it first if you put anything in it, then destroy:

```bash
BUCKET=$(jq -r .HomeCloudBasic.BucketName outputs.json)
aws s3api delete-objects --bucket "$BUCKET" --delete "$(aws s3api list-object-versions --bucket "$BUCKET" \
  --query '{Objects: Versions[].{Key:Key,VersionId:VersionId}}')"
npx cdk destroy --force
```

`cdk destroy` removes the stack, the function's containers and everything above. The `CDKToolkit`
stack stays, as on AWS.

## Why HTTPS

The bootstrap stack's staging bucket has CDK's standard bucket policy, which denies every request made
without TLS (`aws:SecureTransport` is `false`). HomeCloud enforces bucket policies, so over plain HTTP the
asset upload in `cdk deploy` fails with

```
Bucket named 'cdk-hnb659fds-assets-<account>-us-east-1' exists, but we dont have access to it.
```

Serve HomeCloud with `--tls-self-signed` (or `--tls-cert`/`--tls-key`), as above. Buckets of your own
stacks with `enforceSSL: true` behave the same way.

## Notes

* Custom resources are not supported by HomeCloud's CloudFormation yet, so constructs that rely on one
  fail to deploy: `autoDeleteObjects: true` on a bucket, `BucketDeployment`, `AwsCustomResource`, log
  retention on a function's implicit log group (`logRetention`; use `logGroup` as here). Nested stacks
  and `AWS::Serverless` transforms are not supported either.
* The function's code is inline, so the only file asset is the template itself. Zip assets
  (`Code.fromAsset`) and container image assets (pushed to the bootstrap ECR repository) go through the
  same publishing roles but are not covered by this example yet.

## Fixed in HomeCloud while verifying this example

* The AWS managed policy `AWSCloudFormationReadOnlyAccess`, which the bootstrap stack attaches to its
  deployment action role, existed only as a missing-policy error.
* IAM rejected policy names longer than 64 characters; AWS allows 128 for policies (and groups and
  instance profiles). The bootstrap stack names an inline policy with 72.
* The `aws:ResourceAccount` condition key, which scopes the publishing roles' policies, was never set,
  so the roles were denied access to their own staging bucket.
* `AWS::Lambda::Function` `LoggingConfig` (CDK's `logGroup`) was ignored and logs went to
  `/aws/lambda/<name>`; Lambda now supports `LoggingConfig` (log group, format and levels) over the AWS
  API and in templates.
* `DeleteChangeSet` on a change set name that does not exist now succeeds when the stack exists, as in
  AWS; `cdk diff` relies on it and fell back to a template-only diff before.
