# testcontainers-homecloud

A [Testcontainers for Node.js](https://node.testcontainers.org/) module for
[HomeCloud](https://github.com/solinode/homecloud), a self-hosted AWS: start it from a
test and talk to it with the AWS SDK for JavaScript v3.

```sh
npm install --save-dev testcontainers-homecloud
```

(Before the first npm release, build it from a checkout: `npm ci && npm run build &&
npm pack` in this directory, then install the `.tgz`.)

```ts
import { S3Client, CreateBucketCommand } from "@aws-sdk/client-s3";
import { SQSClient } from "@aws-sdk/client-sqs";
import { HomeCloudContainer, type StartedHomeCloudContainer } from "testcontainers-homecloud";

let hc: StartedHomeCloudContainer;
beforeAll(async () => {
  hc = await new HomeCloudContainer().start();
}, 300_000);
afterAll(() => hc?.stop()); // also removes MinIO, Lambda runtimes, ... it started

test("upload", async () => {
  const s3 = new S3Client(hc.getS3ClientConfig()); // endpoint, root keys, region, path-style
  await s3.send(new CreateBucketCommand({ Bucket: "test" }));
  const sqs = new SQSClient(hc.getClientConfig());
  // ...
});
```

ESM and CommonJS, with TypeScript types. Requires Node.js 20+.

## API

| | |
| --- | --- |
| `new HomeCloudContainer(image?)` | default image `ghcr.io/solinode/homecloud:latest` |
| `.start()` | starts HomeCloud, waits for the API (and S3, by default), reads the credentials |
| `hc.getEndpoint()` | `http://127.0.0.1:18080` |
| `hc.getCredentials()` | `{ accessKeyId, secretAccessKey, region }` |
| `hc.getRegion()` | `us-east-1` unless configured otherwise |
| `hc.getClientConfig()` | `{ endpoint, region, credentials }` for any SDK v3 client |
| `hc.getS3ClientConfig()` | the same with `forcePathStyle: true` |
| `hc.getAwsEnv()` | `AWS_ENDPOINT_URL`, keys and region for subprocesses (AWS CLI, Terraform) |
| `hc.getAccountId()` | the account HomeCloud created |
| `hc.stop()` | stops HomeCloud and removes every container, network and volume it created |
| `removeResources(account)` | the clean-up alone, for runs that were killed |

Options on `HomeCloudContainer`: `withPort(n)` (default 18080),
`withWaitForServices("s3", "ecr", "route53")` (default `s3`; pass none to wait only for
the API), `withDockerSocket(path)`, `withStartupTimeout(ms)` (default 3 minutes), plus
the generic `GenericContainer` options.

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
use it in parallel (with vitest, `fileParallelism: false`), nor next to a HomeCloud you
run yourself on the same Docker host (HomeCloud refuses to start then).

## Image and tests

Any image whose entrypoint runs the `homecloud` binary works. To test against a build
from a checkout:

```sh
docker build -t homecloud:test .                 # from the repository root
npm ci && HOMECLOUD_IMAGE=homecloud:test npm test # in integrations/testcontainers-node
```

## License

AGPL-3.0-only, like HomeCloud.
