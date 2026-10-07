# Set up HomeCloud (GitHub Action)

Runs [HomeCloud](https://github.com/solinode/homecloud), a self-hosted AWS, inside your
workflow so tests can call S3, SQS, DynamoDB, Lambda and the rest of the AWS API without
an AWS account.

The action:

1. downloads the HomeCloud release you ask for and verifies it against the release's `checksums.txt`,
2. starts `homecloud serve` in the background (data in `$RUNNER_TEMP/homecloud-data`, log in `$RUNNER_TEMP/homecloud.log`),
3. waits for `/api/v1/health` and for any services listed in `services-wait`,
4. exports `AWS_ENDPOINT_URL`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` (masked), `AWS_REGION`, `AWS_DEFAULT_REGION` and `HOMECLOUD_DATA_DIR` for the following steps, and puts `homecloud` on `PATH`,
5. prints the server log in a post step when the job fails.

HomeCloud runs every service on Docker, so use a runner with Docker: `ubuntu-latest` works
as is. macOS and Windows hosted runners have no Docker.

## Usage

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: solinode/homecloud/integrations/github-action@main
        with:
          services-wait: s3
      - run: aws s3 mb s3://my-bucket && aws s3 ls
```

### Inputs

| Input | Default | Description |
| --- | --- | --- |
| `version` | `latest` | Release to install, e.g. `v0.3.0` (the `v` is optional). |
| `port` | `8080` | Port the API listens on, on `127.0.0.1`. |
| `services-wait` | (none) | Comma or newline separated services to wait for. `s3`, `ecr` and `route53` start containers in the background; everything else runs inside the HomeCloud process and is ready with the API. |
| `wait-timeout` | `180` | Seconds to wait for the API and the services. |

### Outputs

| Output | Description |
| --- | --- |
| `endpoint` | e.g. `http://127.0.0.1:8080` (same as `AWS_ENDPOINT_URL`) |
| `access-key-id` | root access key ID |
| `region` | region HomeCloud serves (`us-east-1`) |
| `log-file` | path of the server log |

AWS CLI v2 and current AWS SDKs read `AWS_ENDPOINT_URL`, so most tools need no configuration.

## Examples

### AWS CLI

```yaml
      - uses: solinode/homecloud/integrations/github-action@main
        with:
          services-wait: s3
      - run: |
          aws s3 mb s3://artifacts
          aws sqs create-queue --queue-name jobs
          aws dynamodb list-tables
          aws lambda list-functions
```

### Python (boto3)

boto3 1.28 and later pick up `AWS_ENDPOINT_URL`:

```yaml
      - uses: solinode/homecloud/integrations/github-action@main
        with:
          services-wait: s3
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - run: pip install boto3 pytest && pytest
```

```python
import boto3

def test_upload():
    s3 = boto3.client("s3")  # endpoint, keys and region come from the environment
    s3.create_bucket(Bucket="test")
    s3.put_object(Bucket="test", Key="a.txt", Body=b"hi")
    assert s3.get_object(Bucket="test", Key="a.txt")["Body"].read() == b"hi"
```

### Terraform

The AWS provider (5.x and later) honours `AWS_ENDPOINT_URL`; tell it not to look for a real account:

```yaml
      - uses: solinode/homecloud/integrations/github-action@main
        with:
          services-wait: s3
      - uses: hashicorp/setup-terraform@v3
      - run: terraform init && terraform apply -auto-approve
```

```hcl
provider "aws" {
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true
}
```

### Using the step outputs

```yaml
      - id: homecloud
        uses: solinode/homecloud/integrations/github-action@main
      - run: ./run-tests.sh --endpoint "${{ steps.homecloud.outputs.endpoint }}"
```

## Notes

- One HomeCloud per Docker host: it names its helper containers (`homecloud-s3`, ...) and
  publishes MinIO on 9500/9501, ECR on 5500 and DNS on 8053 on `127.0.0.1`. Those ports
  must be free on the runner.
- The root console password from the first-start log is masked as well.
- This is a JavaScript action (`node24`) only because composite actions cannot declare a
  post step; all the work is in `setup.sh`, and it has no dependencies to install.
