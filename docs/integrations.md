# Using HomeCloud in tests and CI

HomeCloud speaks the AWS protocols (SigV4) on one port, so any project can use it as a
drop-in AWS for integration tests: point `AWS_ENDPOINT_URL` at it and use the root
credentials it creates on first start. This page covers the ready-made integrations in
[`integrations/`](../integrations):

| | |
| --- | --- |
| [GitHub Action](#github-actions) | `integrations/github-action`: install, start, export `AWS_*` |
| [testcontainers-go](#testcontainers-go) | `integrations/testcontainers-go`: start HomeCloud from a Go test |
| [testcontainers-python](#testcontainers-python) | `integrations/testcontainers-python`: start HomeCloud from pytest |
| [Plain binary or Docker](#plain-binary-or-docker) | anything else |

Everything HomeCloud runs (MinIO for S3, Lambda runtimes, databases) is a container on
the Docker host, so all of these need Docker. Only **one HomeCloud runs per Docker host**
at a time: its helper containers have fixed names (`homecloud-s3`, ...) and host ports
(MinIO 9500/9501, ECR 5500, DNS 8053), and it refuses to start next to another
installation's containers. Do not run test suites that each start HomeCloud in parallel
on the same Docker host.

## GitHub Actions

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: solinode/homecloud/integrations/github-action@main
        with:
          version: latest      # or v0.3.0
          services-wait: s3    # wait for MinIO too
      - run: |
          aws s3 mb s3://artifacts
          aws sqs create-queue --queue-name jobs
          pytest               # boto3 reads AWS_ENDPOINT_URL
```

The action downloads the release, checks it against `checksums.txt`, runs `homecloud
serve` in the background (data and log under `$RUNNER_TEMP`), waits for
`/api/v1/health`, and exports `AWS_ENDPOINT_URL`, `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY` (masked), `AWS_REGION` and `AWS_DEFAULT_REGION`. After the job,
a post step prints the server log (collapsed), stops HomeCloud and removes its Docker
resources. Inputs, outputs and boto3/Terraform examples:
[integrations/github-action/README.md](../integrations/github-action/README.md).

## testcontainers-go

```go
import homecloud "github.com/solinode/homecloud/integrations/testcontainers-go"

hc, err := homecloud.Run(ctx, homecloud.DefaultImage)
testcontainers.CleanupContainer(t, hc)
if err != nil {
	t.Fatal(err)
}
s3c := s3.NewFromConfig(hc.AWSConfig(), func(o *s3.Options) { o.UsePathStyle = true })
```

`hc.EndpointURL()`, `hc.Credentials()` and `hc.Env()` give the connection details;
terminating the container also removes every container, network and volume HomeCloud
created. See [integrations/testcontainers-go/README.md](../integrations/testcontainers-go/README.md).

## testcontainers-python

```python
from testcontainers_homecloud import HomeCloudContainer

@pytest.fixture(scope="session")
def homecloud():
    with HomeCloudContainer() as hc:
        yield hc

def test_upload(homecloud):
    s3 = homecloud.get_client("s3")
    s3.create_bucket(Bucket="test")
```

See [integrations/testcontainers-python/README.md](../integrations/testcontainers-python/README.md).

### How the testcontainers modules run HomeCloud

Both modules start the image (default `ghcr.io/solinode/homecloud:latest`, configurable)
with the Docker socket mounted and **host networking**, and the API on
`127.0.0.1:18080` of the Docker host (configurable; not 8080, which is often taken).
HomeCloud then reaches the containers it starts on the host's loopback ports, and they
call it back through `host.docker.internal`, exactly as when it runs on the host. This
works on Linux (GitHub Actions included), OrbStack, and Docker Desktop with host
networking enabled (Settings > Resources > Network). They wait for the API and S3, read
the credentials with `homecloud aws-env` inside the container, and on stop remove
everything labelled with the account HomeCloud created.

Until the official image is published, build one from a checkout:

```sh
docker build -f integrations/testdata/Dockerfile -t homecloud:test cli
HOMECLOUD_IMAGE=homecloud:test go test ./...      # in integrations/testcontainers-go
HOMECLOUD_IMAGE=homecloud:test pytest             # in integrations/testcontainers-python
```

## Plain binary or Docker

With the binary (any CI with Docker, or a laptop):

```sh
curl -fsSL https://homecloud.pages.dev/scripts/install.sh | sh
export HOMECLOUD_DATA_DIR=$(mktemp -d)
homecloud serve --data-dir "$HOMECLOUD_DATA_DIR" > homecloud.log 2>&1 &
until curl -fs http://127.0.0.1:8080/api/v1/health; do sleep 1; done
eval "$(homecloud aws-env)"      # AWS_ENDPOINT_URL, keys, region
aws s3 ls
```

With Docker only, run the image the same way the testcontainers modules do. Until
`ghcr.io/solinode/homecloud` is published, use the image built above (`homecloud:test`);
afterwards, replace it with `ghcr.io/solinode/homecloud`:

```sh
docker build -f integrations/testdata/Dockerfile -t homecloud:test cli
docker run -d --name homecloud --network host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  homecloud:test serve --data-dir /data --addr 127.0.0.1:18080
until curl -fs http://127.0.0.1:18080/api/v1/health; do sleep 1; done
docker exec homecloud homecloud aws-env      # credentials; the endpoint is http://127.0.0.1:18080
```

To clean up afterwards, remove the HomeCloud container with its data volume (`-v`), then
everything labelled with its account (`docker logs homecloud | grep "created account"`
shows it):

```sh
docker rm -fv homecloud
docker rm -fv $(docker ps -aq --filter label=homecloud.account=ACCOUNT)
docker network rm $(docker network ls -q --filter label=homecloud.account=ACCOUNT)
docker volume rm $(docker volume ls -q --filter label=homecloud.account=ACCOUNT)
```

## SDK notes

- AWS CLI v2, boto3 1.28+, aws-sdk-go-v2, the JavaScript SDK v3 and the Terraform AWS
  provider 5.x read `AWS_ENDPOINT_URL`.
- Use path-style S3 addressing (`UsePathStyle`, `addressing_style: path`,
  `s3_use_path_style = true`) when the endpoint is an IP address or `localhost`.
- The region is `us-east-1` unless the server was started with another one.
