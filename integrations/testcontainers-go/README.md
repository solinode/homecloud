# HomeCloud module for testcontainers-go

Start [HomeCloud](https://github.com/solinode/homecloud), a self-hosted AWS, from a Go
test and talk to it with aws-sdk-go-v2.

```sh
go get github.com/solinode/homecloud/integrations/testcontainers-go
```

```go
import (
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	homecloud "github.com/solinode/homecloud/integrations/testcontainers-go"
)

func TestUpload(t *testing.T) {
	ctx := context.Background()
	hc, err := homecloud.Run(ctx, homecloud.DefaultImage)
	testcontainers.CleanupContainer(t, hc) // also removes MinIO, Lambda runtimes, ... it started
	if err != nil {
		t.Fatal(err)
	}
	cfg := hc.AWSConfig() // endpoint, root keys and region
	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
	// ...
}
```

## API

| | |
| --- | --- |
| `Run(ctx, image, opts...)` | starts HomeCloud and waits for the API (and S3, by default) |
| `hc.EndpointURL()` | `http://127.0.0.1:18080` |
| `hc.Endpoint(ctx, "http")` | same, through the `testcontainers.Container` interface |
| `hc.Credentials()` | root `AccessKeyID`, `SecretAccessKey`, `Region` |
| `hc.AWSConfig()` | `aws.Config` for any `NewFromConfig` |
| `hc.Env()` | `AWS_ENDPOINT_URL`, keys and region for subprocesses (AWS CLI, Terraform) |
| `hc.AccountID()` | the account HomeCloud created |
| `hc.Terminate(ctx)` | stops HomeCloud and removes every container, network and volume it created |
| `RemoveResources(ctx, account)` | the clean-up alone, for runs that were killed |

Options: `WithPort(n)` (default 18080), `WithWaitForServices("s3", "ecr", "route53")`
(default `s3`; pass none to wait only for the API), `WithDockerSocket(path)`, plus any
generic `testcontainers` option.

## How it runs

HomeCloud starts each service's backing containers (MinIO for S3, Lambda runtimes,
databases) on the Docker host, so the container gets the Docker socket
(`/var/run/docker.sock`) and **host networking**: it reaches those containers on the
host's loopback ports and they call it back through `host.docker.internal`. The API
listens on `127.0.0.1:<port>` of the Docker host. This works on Linux (including GitHub
Actions), OrbStack, and Docker Desktop with host networking turned on (Settings >
Resources > Network).

Only one HomeCloud can run per Docker host: its helper containers have fixed names
(`homecloud-s3`, ...) and host ports (9500, 9501, 5500, 8053). Do not run tests that
use it in parallel, nor next to a HomeCloud you run yourself on the same Docker host
(HomeCloud refuses to start then).

## Image

`DefaultImage` is `ghcr.io/solinode/homecloud:latest`. Any image whose entrypoint is
the `homecloud` binary works; to build one from a checkout:

```sh
docker build -f integrations/testdata/Dockerfile -t homecloud:test cli
HOMECLOUD_IMAGE=homecloud:test go test ./...
```

The module's own test reads `HOMECLOUD_IMAGE` (default `DefaultImage`).
