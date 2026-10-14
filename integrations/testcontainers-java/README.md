# HomeCloud module for Testcontainers for Java

Start [HomeCloud](https://github.com/solinode/homecloud), a self-hosted AWS, from a JUnit
test and talk to it with the AWS SDK for Java v2.

```xml
<dependency>
  <groupId>io.github.solinode</groupId>
  <artifactId>testcontainers-homecloud</artifactId>
  <version>0.1.0</version>
  <scope>test</scope>
</dependency>
<!-- plus the AWS SDK service clients your tests use, e.g. -->
<dependency>
  <groupId>software.amazon.awssdk</groupId>
  <artifactId>s3</artifactId>
  <version>2.55.16</version>  <!-- or manage it with the software.amazon.awssdk:bom -->
  <scope>test</scope>
</dependency>
```

Gradle: `testImplementation("io.github.solinode:testcontainers-homecloud:0.1.0")`.

The artifact is available from Maven Central after its first release; until then, run
`mvn install` in this directory and depend on `0.1.0-SNAPSHOT`.

```java
import io.github.solinode.homecloud.testcontainers.HomeCloudContainer;

class UploadTest {
    static HomeCloudContainer hc = new HomeCloudContainer();

    @BeforeAll static void start() { hc.start(); }
    @AfterAll static void stop() { hc.stop(); }   // also removes MinIO, Lambda runtimes, ... it started

    @Test
    void upload() {
        try (S3Client s3 = hc.getS3Client()) {      // endpoint, root keys, region, path-style
            s3.createBucket(b -> b.bucket("test"));
            s3.putObject(b -> b.bucket("test").key("a.txt"), RequestBody.fromString("hi"));
        }
    }
}
```

With the Testcontainers JUnit 5 extension, `@Testcontainers` plus
`@Container static HomeCloudContainer hc = new HomeCloudContainer();` works too.

## API

| | |
| --- | --- |
| `new HomeCloudContainer()`, `new HomeCloudContainer(image)` | default image `ghcr.io/solinode/homecloud:latest` |
| `getEndpoint()`, `getEndpointUri()` | `http://localhost:18080` |
| `getCredentials()`, `getAccessKey()`, `getSecretKey()`, `getRegion()` | root keys and region |
| `getCredentialsProvider()` | an `AwsCredentialsProvider` |
| `configure(builder)` | endpoint override, credentials and region on any SDK v2 client builder: `hc.configure(SnsClient.builder()).build()` |
| `getS3Client()` (path-style), `getSqsClient()`, `getDynamoDbClient()`, `getLambdaClient()` | ready clients (add the service's SDK artifact) |
| `getAwsEnv()` | `AWS_ENDPOINT_URL`, keys and region for subprocesses (AWS CLI, Terraform) |
| `getAccountId()` | the account HomeCloud created |
| `stop()` | stops HomeCloud and removes every container, network and volume it created |
| `HomeCloudContainer.removeResources(account)` | the clean-up alone, for runs that were killed |

Options: `withPort(n)` (default 18080), `withWaitForServices("s3", "ecr", "route53")`
(default `s3`; pass none to wait only for the API), `withDockerSocket(path)`,
`withHomeCloudStartupTimeout(duration)` (default 3 minutes).

Requires Java 11+.

## How it runs

HomeCloud starts each service's backing containers (MinIO for S3, Lambda runtimes,
databases) on the Docker host, so the container gets the Docker socket
(`/var/run/docker.sock`) and **host networking**: it reaches those containers on the
host's loopback ports and they call it back through `host.docker.internal`. The API
listens on `127.0.0.1:<port>` of the Docker host. This works on Linux (including GitHub
Actions), OrbStack, and Docker Desktop with host networking turned on (Settings >
Resources > Network).

The endpoint names that address `localhost`, as `homecloud aws-env` does: S3 Control puts
the account ID in front of the endpoint's host (`123456789012.localhost`), and the
Terraform AWS provider calls it for every `aws_s3_bucket`; `123456789012.127.0.0.1` does
not resolve. Clients that try `::1` first fall back to `127.0.0.1`. S3 clients should use
path-style addressing (`getS3Client()` does), so bucket names need not resolve.

Only one HomeCloud can run per Docker host: its helper containers have fixed names
(`homecloud-s3`, ...) and host ports (9500, 9501, 5500, 8053). Do not run tests that
use it in parallel, nor next to a HomeCloud you run yourself on the same Docker host
(HomeCloud refuses to start then).

## Image and tests

Any image whose entrypoint runs the `homecloud` binary works. To test against a build
from a checkout:

```sh
docker build -t homecloud:test .                 # from the repository root
HOMECLOUD_IMAGE=homecloud:test mvn verify         # in integrations/testcontainers-java
```
