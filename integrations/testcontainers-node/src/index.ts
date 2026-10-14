/**
 * Testcontainers module for HomeCloud, a self-hosted AWS: one container that speaks the AWS
 * APIs (S3, SQS, DynamoDB, Lambda, ...) on a single port with SigV4.
 *
 *     const hc = await new HomeCloudContainer().start();
 *     const s3 = new S3Client(hc.getS3ClientConfig());
 *     ...
 *     await hc.stop(); // also removes MinIO, Lambda runtimes, ... HomeCloud started
 *
 * HomeCloud starts each service's backing containers (MinIO for S3, Lambda runtimes,
 * databases) on the Docker host, so the container mounts the Docker socket and uses host
 * networking: it reaches those containers on the host's loopback ports and they call it back
 * through host.docker.internal. That works on Linux, OrbStack and Docker Desktop with host
 * networking enabled.
 *
 * Only one HomeCloud runs per Docker host at a time (its helper containers have fixed names
 * and host ports): do not run tests that use it in parallel.
 */
import type { Readable } from "node:stream";
import {
  AbstractStartedContainer,
  GenericContainer,
  getContainerRuntimeClient,
  type StartedTestContainer,
  type StopOptions,
  type StoppedTestContainer,
  Wait,
} from "testcontainers";

/** The official HomeCloud image. */
export const DEFAULT_IMAGE = "ghcr.io/solinode/homecloud:latest";
/** The port the API listens on, on the Docker host (not 8080, which is often taken). */
export const DEFAULT_PORT = 18080;
/** The Docker socket on the Docker host. */
export const DEFAULT_DOCKER_SOCKET = "/var/run/docker.sock";

const DATA_DIR = "/data";
const ACCOUNT_LABEL = "homecloud.account";
const ACCOUNT_RE = /first start: created account (\d+)/;
/** Log lines of the services that start containers in the background. */
const READY_LINES: Record<string, string> = {
  s3: "s3: MinIO ready",
  ecr: "ecr: registry ready",
  route53: "route53: DNS ready",
};

/** The root access key HomeCloud creates on first start, and its region. */
export interface Credentials {
  accessKeyId: string;
  secretAccessKey: string;
  region: string;
}

/**
 * Client configuration for any AWS SDK for JavaScript v3 client:
 * `new SQSClient(hc.getClientConfig())`.
 */
export interface AwsClientConfig {
  endpoint: string;
  region: string;
  credentials: { accessKeyId: string; secretAccessKey: string };
}

/** A HomeCloud server for tests. Configure it, then call {@link HomeCloudContainer.start}. */
export class HomeCloudContainer extends GenericContainer {
  private port = DEFAULT_PORT;
  private dockerSocket = DEFAULT_DOCKER_SOCKET;
  private services: string[] = ["s3"];
  private timeoutMs = 180_000;
  private containerId?: string;
  private log = "";
  private userLogConsumer?: (stream: Readable) => unknown;

  /** @param image any image whose entrypoint runs the `homecloud` binary (default {@link DEFAULT_IMAGE}). */
  constructor(image: string = DEFAULT_IMAGE) {
    super(image);
  }

  /** Sets the port the API listens on, on the Docker host's 127.0.0.1 (default 18080). */
  withPort(port: number): this {
    this.port = port;
    return this;
  }

  /** Sets the Docker socket path on the Docker host (default /var/run/docker.sock). */
  withDockerSocket(path: string): this {
    this.dockerSocket = path;
    return this;
  }

  /**
   * Sets which background services start() waits for besides the API: "s3", "ecr",
   * "route53". The default is "s3"; pass none to wait for the API only. Every other service
   * runs inside the HomeCloud process and is ready with the API.
   */
  withWaitForServices(...services: string[]): this {
    this.services = services.map((s) => s.toLowerCase());
    return this;
  }

  override withStartupTimeout(startupTimeoutMs: number): this {
    this.timeoutMs = startupTimeoutMs;
    return super.withStartupTimeout(startupTimeoutMs);
  }

  protected override async containerCreated(containerId: string): Promise<void> {
    this.containerId = containerId;
  }

  override async start(): Promise<StartedHomeCloudContainer> {
    // Each start is a new HomeCloud with its own account: forget the previous run's.
    this.log = "";
    this.containerId = undefined;
    const strategies = [Wait.forLogMessage("listening on")];
    for (const s of this.services) {
      const line = READY_LINES[s];
      if (line) strategies.push(Wait.forLogMessage(line));
    }
    const userConsumer = this.userLogConsumer;
    // Drop the socket bind of an earlier start, so starting again does not mount it twice.
    this.hostConfig.Binds = (this.hostConfig.Binds ?? []).filter((b) => !b.includes(":/var/run/docker.sock"));
    this.withNetworkMode("host")
      .withBindMounts([{ source: this.dockerSocket, target: "/var/run/docker.sock", mode: "rw" }])
      .withEnvironment({ HOMECLOUD_DATA_DIR: DATA_DIR })
      .withCommand(["serve", "--data-dir", DATA_DIR, "--addr", `127.0.0.1:${this.port}`])
      .withWaitStrategy(Wait.forAll(strategies))
      .withStartupTimeout(this.timeoutMs);
    super.withLogConsumer((stream: Readable) => {
      // Kept so the account is known as early as possible: a failed start is cleaned up too.
      stream.on("data", (chunk: Buffer | string) => {
        if (!ACCOUNT_RE.test(this.log)) this.log += chunk.toString();
      });
      userConsumer?.(stream);
    });

    let started: StartedTestContainer | undefined;
    try {
      started = await super.start();
      const account = this.account() ?? (await readAccount(started));
      if (!account) throw new Error("homecloud: no account in the first-start log");
      const endpoint = `http://${endpointHost(started.getHost())}:${this.port}`;
      await waitHealthy(endpoint, 30_000);
      const credentials = await readCredentials(started);
      return new StartedHomeCloudContainer(started, endpoint, credentials, account);
    } catch (err) {
      const account = this.account() ?? (started ? await readAccount(started).catch(() => undefined) : undefined);
      await removeContainer(this.containerId).catch(() => undefined);
      if (account) await removeResources(account).catch(() => undefined);
      throw err;
    }
  }

  override withLogConsumer(logConsumer: (stream: Readable) => unknown): this {
    this.userLogConsumer = logConsumer;
    return this;
  }

  private account(): string | undefined {
    return ACCOUNT_RE.exec(this.log)?.[1];
  }
}

/** A running HomeCloud. */
export class StartedHomeCloudContainer extends AbstractStartedContainer {
  constructor(
    startedTestContainer: StartedTestContainer,
    private readonly endpoint: string,
    private readonly credentials: Credentials,
    private readonly accountId: string,
  ) {
    super(startedTestContainer);
  }

  /** The API URL, e.g. http://localhost:18080: what AWS_ENDPOINT_URL should be. */
  getEndpoint(): string {
    return this.endpoint;
  }

  /** The root access key HomeCloud created on first start, and the region. */
  getCredentials(): Credentials {
    return { ...this.credentials };
  }

  /** The region, e.g. us-east-1. */
  getRegion(): string {
    return this.credentials.region;
  }

  /** The AWS account ID HomeCloud created on first start. */
  getAccountId(): string {
    return this.accountId;
  }

  /** Config for any AWS SDK v3 client: `new DynamoDBClient(hc.getClientConfig())`. */
  getClientConfig(): AwsClientConfig {
    return {
      endpoint: this.endpoint,
      region: this.credentials.region,
      credentials: {
        accessKeyId: this.credentials.accessKeyId,
        secretAccessKey: this.credentials.secretAccessKey,
      },
    };
  }

  /** {@link getClientConfig} with path-style addressing, for `new S3Client(...)`. */
  getS3ClientConfig(): AwsClientConfig & { forcePathStyle: true } {
    return { ...this.getClientConfig(), forcePathStyle: true };
  }

  /** AWS_* environment variables for the AWS CLI, Terraform or an SDK in a subprocess. */
  getAwsEnv(): Record<string, string> {
    return {
      AWS_ENDPOINT_URL: this.endpoint,
      AWS_ACCESS_KEY_ID: this.credentials.accessKeyId,
      AWS_SECRET_ACCESS_KEY: this.credentials.secretAccessKey,
      AWS_REGION: this.credentials.region,
      AWS_DEFAULT_REGION: this.credentials.region,
    };
  }

  /** Stops HomeCloud and removes every container, network and volume it created. */
  override async stop(options?: Partial<StopOptions>): Promise<StoppedTestContainer> {
    // HomeCloud stops first, so it does not recreate what is being removed.
    let stopped: StoppedTestContainer;
    try {
      stopped = await super.stop({ remove: true, removeVolumes: true, ...options });
    } catch (err) {
      // Clean up the helpers even if HomeCloud itself could not be stopped (e.g. already removed).
      try {
        await removeResources(this.accountId);
      } catch (cleanupErr) {
        throw new AggregateError([err, cleanupErr], "homecloud: stop and clean-up failed");
      }
      throw err;
    }
    await removeResources(this.accountId);
    return stopped;
  }
}

/**
 * Removes the containers, networks and volumes HomeCloud created for account on the Docker
 * host. StartedHomeCloudContainer.stop() calls it; call it yourself to clean up after a test
 * run that was killed.
 */
export async function removeResources(account: string): Promise<void> {
  if (!account) return;
  const docker = (await getContainerRuntimeClient()).container.dockerode;
  const filters = JSON.stringify({ label: [`${ACCOUNT_LABEL}=${account}`] });
  const errors: unknown[] = [];
  for (const c of await docker.listContainers({ all: true, filters })) {
    await docker
      .getContainer(c.Id)
      .remove({ force: true, v: true })
      .catch((e: unknown) => errors.push(e));
  }
  for (const n of await docker.listNetworks({ filters })) {
    await docker
      .getNetwork(n.Id)
      .remove()
      .catch((e: unknown) => errors.push(e));
  }
  const { Volumes } = await docker.listVolumes({ filters });
  for (const v of Volumes ?? []) {
    await docker
      .getVolume(v.Name)
      .remove({ force: true })
      .catch((e: unknown) => errors.push(e));
  }
  if (errors.length > 0) {
    throw new AggregateError(errors, `homecloud: removing resources of account ${account} failed`);
  }
}

/**
 * The host name for the endpoint: a loopback Docker host is named `localhost`, as
 * `homecloud aws-env` does. S3 Control (which the Terraform AWS provider calls for every
 * bucket) puts the account ID in front of the endpoint's host, and `123456789012.127.0.0.1`
 * does not resolve. HomeCloud listens on 127.0.0.1 only; Node.js 20+ tries 127.0.0.1 when
 * ::1 refuses the connection.
 */
export function endpointHost(host: string): string {
  return host === "localhost" || host === "127.0.0.1" || host === "::1" ? "localhost" : host;
}

/** Parses the `export NAME="value"` lines printed by `homecloud aws-env`. */
export function parseAwsEnv(output: string): Record<string, string> {
  const env: Record<string, string> = {};
  for (let line of output.split(/\r?\n/)) {
    line = line.trim().replace(/^export /, "");
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    let value = line.slice(eq + 1);
    if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
      value = value.slice(1, -1).replace(/\\(["\\])/g, "$1");
    }
    env[line.slice(0, eq)] = value;
  }
  return env;
}

async function removeContainer(id: string | undefined): Promise<void> {
  if (!id) return;
  const docker = (await getContainerRuntimeClient()).container.dockerode;
  await docker.getContainer(id).remove({ force: true, v: true });
}

async function readAccount(container: StartedTestContainer): Promise<string | undefined> {
  const stream = await container.logs();
  let text = "";
  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => {
      stream.destroy();
      resolve();
    }, 2_000);
    stream.on("data", (chunk: Buffer | string) => {
      text += chunk.toString();
      if (ACCOUNT_RE.test(text)) {
        clearTimeout(timer);
        stream.destroy();
        resolve();
      }
    });
    stream.on("end", () => {
      clearTimeout(timer);
      resolve();
    });
    stream.on("error", () => {
      clearTimeout(timer);
      resolve();
    });
  });
  return ACCOUNT_RE.exec(text)?.[1];
}

async function waitHealthy(endpoint: string, timeoutMs: number): Promise<void> {
  const url = `${endpoint}/api/v1/health`;
  const deadline = Date.now() + timeoutMs;
  let last: unknown;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url, { signal: AbortSignal.timeout(2_000) });
      await res.body?.cancel();
      if (res.status === 200) return;
      last = new Error(`status ${res.status}`);
    } catch (e) {
      last = e;
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`homecloud: ${url} not reachable (does this Docker setup support host networking?): ${String(last)}`);
}

async function readCredentials(container: StartedTestContainer): Promise<Credentials> {
  const { exitCode, stdout, output } = await container.exec(["homecloud", "aws-env"]);
  if (exitCode !== 0) throw new Error(`homecloud aws-env exited ${exitCode}: ${output}`);
  const env = parseAwsEnv(stdout);
  const accessKeyId = env.AWS_ACCESS_KEY_ID;
  const secretAccessKey = env.AWS_SECRET_ACCESS_KEY;
  if (!accessKeyId || !secretAccessKey) throw new Error(`homecloud aws-env printed no credentials: ${output}`);
  return { accessKeyId, secretAccessKey, region: env.AWS_REGION || "us-east-1" };
}
