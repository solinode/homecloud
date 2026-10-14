# testcontainers-homecloud

A [testcontainers-python](https://testcontainers-python.readthedocs.io/) module for
[HomeCloud](https://github.com/solinode/homecloud), a self-hosted AWS: start it from a
test and talk to it with boto3.

```sh
pip install testcontainers-homecloud
```

(Before the first PyPI release, install from the repository:
`pip install "testcontainers-homecloud @ git+https://github.com/solinode/homecloud#subdirectory=integrations/testcontainers-python"`.)

```python
import pytest
from testcontainers_homecloud import HomeCloudContainer

@pytest.fixture(scope="session")
def homecloud():
    with HomeCloudContainer() as hc:   # stop() also removes MinIO, Lambda runtimes, ... it started
        yield hc

def test_upload(homecloud):
    s3 = homecloud.get_client("s3")    # boto3 client: endpoint, root keys, region, path-style S3
    s3.create_bucket(Bucket="test")
    s3.put_object(Bucket="test", Key="a.txt", Body=b"hi")
```

## API

| | |
| --- | --- |
| `HomeCloudContainer(image=DEFAULT_IMAGE, port=18080, docker_socket="/var/run/docker.sock", wait_for_services=("s3",), startup_timeout=180)` | `wait_for_services` takes `s3`, `ecr`, `route53`; everything else is ready with the API |
| `get_endpoint()` | `http://localhost:18080` |
| `get_credentials()` | `Credentials(access_key_id, secret_access_key, region)` |
| `get_client(service, **kw)` / `get_resource(service, **kw)` | boto3 client / resource for HomeCloud |
| `aws_env()` | `AWS_ENDPOINT_URL`, keys and region for subprocesses (AWS CLI, Terraform) |
| `account_id` | the account HomeCloud created |
| `stop()` | stops HomeCloud and removes every container, network and volume it created |
| `remove_resources(docker_client, account_id)` | the clean-up alone, for runs that were killed |

## How it runs

HomeCloud starts each service's backing containers on the Docker host, so the container
gets the Docker socket and **host networking**; the API listens on `127.0.0.1:<port>` of
the Docker host. This works on Linux (including GitHub Actions), OrbStack, and Docker
Desktop with host networking turned on.

The endpoint names that address `localhost`, as `homecloud aws-env` does: S3 Control puts
the account ID in front of the endpoint's host (`123456789012.localhost`), and the
Terraform AWS provider calls it for every `aws_s3_bucket`; `123456789012.127.0.0.1` does
not resolve. Clients that try `::1` first fall back to `127.0.0.1`. S3 clients should use
path-style addressing (`get_client("s3")` does), so bucket names need not resolve.

Only one HomeCloud can run per Docker host: its helper containers have fixed names and
host ports (9500, 9501, 5500, 8053). Use one session-scoped fixture, do not run it under
`pytest-xdist`, and stop any HomeCloud you run yourself on the same Docker host first.

## Image

`DEFAULT_IMAGE` is `ghcr.io/solinode/homecloud:latest`. To test against a build from a
checkout:

```sh
docker build -f integrations/testdata/Dockerfile -t homecloud:test cli
cd integrations/testcontainers-python
pip install -e '.[test]'
HOMECLOUD_IMAGE=homecloud:test pytest
```
