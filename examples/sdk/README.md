# AWS SDKs against HomeCloud

Each program calls `sts get-caller-identity`, then creates an S3 bucket, an SQS queue and a DynamoDB
table, puts and gets an object, a message and an item, and deletes what it made. The configuration each
SDK needs is explained in [docs/aws-compat.md](../../docs/aws-compat.md#sdks).

```bash
eval "$(homecloud aws-env)"

cd examples/sdk
(cd javascript && npm install && node index.mjs)        # AWS SDK for JavaScript v3
(cd go && go run .)                                     # AWS SDK for Go v2
(cd java && mvn -q compile exec:java)                   # AWS SDK for Java v2 (Java 17+, Maven)
```

Expected output (the account differs):

```
caller: arn:aws:iam::123456789012:user/root
s3: hello from Go
sqs: hello from Go
dynamodb: hello from Go
```

For a server started with `--tls-self-signed`, `homecloud aws-env` also exports `AWS_CA_BUNDLE`; the Go
SDK reads it, Node.js needs `NODE_EXTRA_CA_CERTS="$AWS_CA_BUNDLE"`, and Java needs a trust store (see
the docs).
