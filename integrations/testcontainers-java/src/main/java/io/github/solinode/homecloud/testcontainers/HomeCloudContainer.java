package io.github.solinode.homecloud.testcontainers;

import com.github.dockerjava.api.DockerClient;
import com.github.dockerjava.api.model.Bind;
import com.github.dockerjava.api.model.Container;
import com.github.dockerjava.api.model.Network;
import com.github.dockerjava.api.command.InspectContainerResponse;
import com.github.dockerjava.api.command.InspectVolumeResponse;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URI;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.testcontainers.DockerClientFactory;
import org.testcontainers.containers.GenericContainer;
import org.testcontainers.containers.wait.strategy.LogMessageWaitStrategy;
import org.testcontainers.containers.wait.strategy.WaitAllStrategy;
import org.testcontainers.utility.DockerImageName;
import software.amazon.awssdk.auth.credentials.AwsBasicCredentials;
import software.amazon.awssdk.auth.credentials.AwsCredentialsProvider;
import software.amazon.awssdk.auth.credentials.StaticCredentialsProvider;
import software.amazon.awssdk.awscore.client.builder.AwsClientBuilder;
import software.amazon.awssdk.regions.Region;
import software.amazon.awssdk.services.dynamodb.DynamoDbClient;
import software.amazon.awssdk.services.lambda.LambdaClient;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.sqs.SqsClient;

/**
 * Testcontainers module for <a href="https://github.com/solinode/homecloud">HomeCloud</a>, a
 * self-hosted AWS: one container that speaks the AWS APIs (S3, SQS, DynamoDB, Lambda, ...) on a
 * single port with SigV4.
 *
 * <pre>{@code
 * try (HomeCloudContainer hc = new HomeCloudContainer()) {
 *     hc.start();
 *     S3Client s3 = hc.getS3Client();
 *     s3.createBucket(b -> b.bucket("test"));
 * }
 * }</pre>
 *
 * <p>HomeCloud starts each service's backing containers (MinIO for S3, Lambda runtimes,
 * databases) on the Docker host, so the container mounts the Docker socket and uses host
 * networking: it reaches those containers on the host's loopback ports and they call it back
 * through {@code host.docker.internal}. That works on Linux, OrbStack and Docker Desktop with
 * host networking enabled. {@link #stop()} removes every container, network and volume
 * HomeCloud created.
 *
 * <p>Only one HomeCloud runs per Docker host at a time (its helper containers have fixed names
 * and host ports): do not run tests that use it in parallel.
 */
public class HomeCloudContainer extends GenericContainer<HomeCloudContainer> {

    /** The official HomeCloud image. */
    public static final String DEFAULT_IMAGE = "ghcr.io/solinode/homecloud:latest";

    /** The port the API listens on, on the Docker host (not 8080, which is often taken). */
    public static final int DEFAULT_PORT = 18080;

    /** The Docker socket on the Docker host. */
    public static final String DEFAULT_DOCKER_SOCKET = "/var/run/docker.sock";

    static final String ACCOUNT_LABEL = "homecloud.account";
    private static final String DATA_DIR = "/data";
    private static final Pattern ACCOUNT_RE = Pattern.compile("first start: created account (\\d+)");
    private static final Map<String, String> READY_LINES;

    static {
        Map<String, String> m = new HashMap<>();
        m.put("s3", "s3: MinIO ready");
        m.put("ecr", "ecr: registry ready");
        m.put("route53", "route53: DNS ready");
        READY_LINES = Collections.unmodifiableMap(m);
    }

    private int port = DEFAULT_PORT;
    private String dockerSocket = DEFAULT_DOCKER_SOCKET;
    private List<String> waitForServices = Collections.singletonList("s3");
    private Duration startupTimeout = Duration.ofMinutes(3);
    private String accountId;
    private Credentials credentials;

    /** HomeCloud from {@link #DEFAULT_IMAGE}. */
    public HomeCloudContainer() {
        this(DEFAULT_IMAGE);
    }

    /**
     * HomeCloud from image: the official one or a local build.
     *
     * @param image any image whose entrypoint runs the {@code homecloud} binary
     */
    public HomeCloudContainer(String image) {
        this(DockerImageName.parse(image));
    }

    /**
     * HomeCloud from image: the official one or a local build.
     *
     * @param image any image whose entrypoint runs the {@code homecloud} binary
     */
    public HomeCloudContainer(DockerImageName image) {
        super(image);
    }

    /**
     * Sets the port the API listens on, on the Docker host's 127.0.0.1 (default {@value
     * #DEFAULT_PORT}).
     *
     * @param port a free TCP port on the Docker host
     * @return this container
     */
    public HomeCloudContainer withPort(int port) {
        this.port = port;
        return self();
    }

    /**
     * Sets the path of the Docker socket on the Docker host (default {@value
     * #DEFAULT_DOCKER_SOCKET}, right for Linux, OrbStack and Docker Desktop).
     *
     * @param path socket path on the Docker host
     * @return this container
     */
    public HomeCloudContainer withDockerSocket(String path) {
        this.dockerSocket = path;
        return self();
    }

    /**
     * Sets which background services {@link #start()} waits for besides the API: "s3", "ecr",
     * "route53". The default is "s3"; pass none to wait for the API only. Every other service runs
     * inside the HomeCloud process and is ready with the API.
     *
     * @param services service names
     * @return this container
     */
    public HomeCloudContainer withWaitForServices(String... services) {
        List<String> l = new ArrayList<>();
        for (String s : services) {
            l.add(s.toLowerCase(Locale.ROOT));
        }
        this.waitForServices = l;
        return self();
    }

    /**
     * Sets how long {@link #start()} waits for the API and services (default 3 minutes; the first
     * start pulls MinIO).
     *
     * @param timeout startup timeout
     * @return this container
     */
    public HomeCloudContainer withHomeCloudStartupTimeout(Duration timeout) {
        this.startupTimeout = timeout;
        return self();
    }

    @Override
    protected void configure() {
        withNetworkMode("host");
        // Host networking: nothing to publish, and the generic port checks do not apply.
        setExposedPorts(Collections.emptyList());
        // configure() runs on every start: replace the socket bind of an earlier one.
        getBinds().removeIf(b -> "/var/run/docker.sock".equals(b.getVolume().getPath()));
        getBinds().add(Bind.parse(dockerSocket + ":/var/run/docker.sock"));
        withEnv("HOMECLOUD_DATA_DIR", DATA_DIR);
        withCommand("serve", "--data-dir", DATA_DIR, "--addr", "127.0.0.1:" + port);
        WaitAllStrategy all = new WaitAllStrategy(WaitAllStrategy.Mode.WITH_MAXIMUM_OUTER_TIMEOUT)
                .withStartupTimeout(startupTimeout);
        all.withStrategy(logLine("listening on"));
        for (String s : waitForServices) {
            String line = READY_LINES.get(s);
            if (line != null) {
                all.withStrategy(logLine(line));
            }
        }
        waitingFor(all);
    }

    private LogMessageWaitStrategy logLine(String text) {
        // Each child gets the full timeout too: WITH_MAXIMUM_OUTER_TIMEOUT does not set it.
        return (LogMessageWaitStrategy) new LogMessageWaitStrategy()
                .withRegEx(".*" + Pattern.quote(text) + ".*\\s*")
                .withStartupTimeout(startupTimeout);
    }

    @Override
    public void start() {
        if (getContainerId() == null) {
            // A new run (also after stop()): forget the previous run's account and keys.
            accountId = null;
            credentials = null;
        }
        try {
            super.start();
        } catch (RuntimeException e) {
            // HomeCloud may already have created helper containers: clean them up too.
            try {
                stop();
            } catch (RuntimeException suppressed) {
                e.addSuppressed(suppressed);
            }
            throw e;
        }
    }

    @Override
    protected void waitUntilContainerStarted() {
        try {
            super.waitUntilContainerStarted();
        } finally {
            // As early as possible, so stop() also cleans up after a failed start.
            captureAccount();
        }
    }

    @Override
    protected void containerIsStarted(InspectContainerResponse containerInfo) {
        captureAccount();
        if (accountId == null) {
            throw new IllegalStateException("homecloud: no account in the first-start log:\n" + getLogs());
        }
        waitHealthy(Duration.ofSeconds(30));
        credentials = readCredentials();
    }

    /** Stops HomeCloud and removes every container, network and volume it created. */
    @Override
    public void stop() {
        captureAccount();
        String account = accountId;
        try {
            super.stop();
        } finally {
            if (account != null) {
                removeResources(DockerClientFactory.lazyClient(), account);
            }
        }
    }

    private void captureAccount() {
        if (accountId != null || getContainerId() == null) {
            return;
        }
        try {
            Matcher m = ACCOUNT_RE.matcher(getLogs());
            if (m.find()) {
                accountId = m.group(1);
            }
        } catch (RuntimeException ignored) {
            // the container is already gone
        }
    }

    // Connection details

    /**
     * The URL of the HomeCloud API, e.g. {@code http://127.0.0.1:18080}: what AWS_ENDPOINT_URL or
     * an SDK's endpoint override should be.
     *
     * @return the endpoint URL
     */
    public String getEndpoint() {
        String host = getHost();
        if ("localhost".equals(host)) {
            host = "127.0.0.1"; // keeps SDKs from trying IPv6 first or bucket.localhost names
        }
        return "http://" + host + ":" + port;
    }

    /**
     * {@link #getEndpoint()} as a URI, for {@code endpointOverride}.
     *
     * @return the endpoint URI
     */
    public URI getEndpointUri() {
        return URI.create(getEndpoint());
    }

    /**
     * The root access key HomeCloud created on first start, and the region.
     *
     * @return the credentials
     */
    public Credentials getCredentials() {
        if (credentials == null) {
            throw new IllegalStateException("HomeCloud is not started");
        }
        return credentials;
    }

    /**
     * The root access key ID.
     *
     * @return the access key ID
     */
    public String getAccessKey() {
        return getCredentials().getAccessKeyId();
    }

    /**
     * The root secret access key.
     *
     * @return the secret access key
     */
    public String getSecretKey() {
        return getCredentials().getSecretAccessKey();
    }

    /**
     * The region HomeCloud runs in, e.g. {@code us-east-1}.
     *
     * @return the region name
     */
    public String getRegion() {
        return getCredentials().getRegion();
    }

    /**
     * The AWS account ID HomeCloud created on first start.
     *
     * @return the account ID
     */
    public String getAccountId() {
        return accountId;
    }

    /**
     * The root credentials as an AWS SDK v2 provider.
     *
     * @return a static credentials provider
     */
    public AwsCredentialsProvider getCredentialsProvider() {
        Credentials c = getCredentials();
        return StaticCredentialsProvider.create(AwsBasicCredentials.create(c.getAccessKeyId(), c.getSecretAccessKey()));
    }

    /**
     * AWS_* environment variables for tools that read them (AWS CLI, Terraform, an SDK in a
     * subprocess).
     *
     * @return AWS_ENDPOINT_URL, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION and
     *     AWS_DEFAULT_REGION
     */
    public Map<String, String> getAwsEnv() {
        Credentials c = getCredentials();
        Map<String, String> env = new LinkedHashMap<>();
        env.put("AWS_ENDPOINT_URL", getEndpoint());
        env.put("AWS_ACCESS_KEY_ID", c.getAccessKeyId());
        env.put("AWS_SECRET_ACCESS_KEY", c.getSecretAccessKey());
        env.put("AWS_REGION", c.getRegion());
        env.put("AWS_DEFAULT_REGION", c.getRegion());
        return env;
    }

    /**
     * Points any AWS SDK v2 client builder at HomeCloud: endpoint override, root credentials and
     * region. For S3 also set {@code forcePathStyle(true)} (or use {@link #getS3Client()}).
     *
     * <pre>{@code
     * SnsClient sns = hc.configure(SnsClient.builder()).build();
     * }</pre>
     *
     * @param builder a client builder
     * @param <B> the builder type
     * @return the same builder
     */
    public <B extends AwsClientBuilder<B, ?>> B configure(B builder) {
        return builder.endpointOverride(getEndpointUri())
                .credentialsProvider(getCredentialsProvider())
                .region(Region.of(getRegion()));
    }

    /**
     * An S3 client with path-style addressing. Needs {@code software.amazon.awssdk:s3}.
     *
     * @return a new client; close it when done
     */
    public S3Client getS3Client() {
        return configure(S3Client.builder()).forcePathStyle(true).build();
    }

    /**
     * An SQS client. Needs {@code software.amazon.awssdk:sqs}.
     *
     * @return a new client; close it when done
     */
    public SqsClient getSqsClient() {
        return configure(SqsClient.builder()).build();
    }

    /**
     * A DynamoDB client. Needs {@code software.amazon.awssdk:dynamodb}.
     *
     * @return a new client; close it when done
     */
    public DynamoDbClient getDynamoDbClient() {
        return configure(DynamoDbClient.builder()).build();
    }

    /**
     * A Lambda client. Needs {@code software.amazon.awssdk:lambda}.
     *
     * @return a new client; close it when done
     */
    public LambdaClient getLambdaClient() {
        return configure(LambdaClient.builder()).build();
    }

    // Clean-up

    /**
     * Removes the containers, networks and volumes HomeCloud created for account on the Docker
     * host. {@link #stop()} calls it; call it yourself to clean up after a test run that was
     * killed.
     *
     * @param account the account ID ({@link #getAccountId()})
     */
    public static void removeResources(String account) {
        removeResources(DockerClientFactory.lazyClient(), account);
    }

    static void removeResources(DockerClient docker, String account) {
        if (account == null || account.isEmpty()) {
            return;
        }
        String label = ACCOUNT_LABEL + "=" + account;
        List<String> labels = Collections.singletonList(label);
        RuntimeException failure = null;
        for (Container c : docker.listContainersCmd().withShowAll(true).withLabelFilter(labels).exec()) {
            try {
                docker.removeContainerCmd(c.getId()).withForce(true).withRemoveVolumes(true).exec();
            } catch (RuntimeException e) {
                failure = add(failure, e);
            }
        }
        for (Network n : docker.listNetworksCmd().withFilter("label", labels).exec()) {
            try {
                docker.removeNetworkCmd(n.getId()).exec();
            } catch (RuntimeException e) {
                failure = add(failure, e);
            }
        }
        List<InspectVolumeResponse> volumes = docker.listVolumesCmd().withFilter("label", labels).exec().getVolumes();
        if (volumes != null) {
            for (InspectVolumeResponse v : volumes) {
                try {
                    docker.removeVolumeCmd(v.getName()).exec();
                } catch (RuntimeException e) {
                    failure = add(failure, e);
                }
            }
        }
        if (failure != null) {
            throw failure;
        }
    }

    private static RuntimeException add(RuntimeException first, RuntimeException next) {
        if (first == null) {
            return next;
        }
        first.addSuppressed(next);
        return first;
    }

    // Internals

    private void waitHealthy(Duration timeout) {
        String url = getEndpoint() + "/api/v1/health";
        long deadline = System.nanoTime() + timeout.toNanos();
        Exception last = null;
        while (System.nanoTime() < deadline) {
            try {
                HttpURLConnection conn = (HttpURLConnection) URI.create(url).toURL().openConnection();
                conn.setConnectTimeout(2000);
                conn.setReadTimeout(2000);
                int code = conn.getResponseCode();
                try (InputStream ignored = code < 400 ? conn.getInputStream() : conn.getErrorStream()) {
                    // drain nothing; close the stream
                }
                if (code == 200) {
                    return;
                }
                last = new IOException("status " + code);
            } catch (IOException e) {
                last = e;
            }
            try {
                Thread.sleep(250);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                throw new IllegalStateException("interrupted", e);
            }
        }
        throw new IllegalStateException("homecloud: " + url
                + " not reachable (does this Docker setup support host networking?): " + last, last);
    }

    private Credentials readCredentials() {
        ExecResult r;
        try {
            r = execInContainer("homecloud", "aws-env");
        } catch (IOException e) {
            throw new IllegalStateException("homecloud aws-env: " + e, e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException("homecloud aws-env: interrupted", e);
        }
        String output = r.getStdout() + r.getStderr();
        if (r.getExitCode() != 0) {
            throw new IllegalStateException("homecloud aws-env exited " + r.getExitCode() + ": " + output);
        }
        Map<String, String> env = parseAwsEnv(r.getStdout());
        String key = env.get("AWS_ACCESS_KEY_ID");
        String secret = env.get("AWS_SECRET_ACCESS_KEY");
        if (key == null || key.isEmpty() || secret == null || secret.isEmpty()) {
            throw new IllegalStateException("homecloud aws-env printed no credentials: " + output);
        }
        String region = env.get("AWS_REGION");
        return new Credentials(key, secret, region == null || region.isEmpty() ? "us-east-1" : region);
    }

    /** Parses {@code export NAME="value"} lines as printed by {@code homecloud aws-env}. */
    static Map<String, String> parseAwsEnv(String out) {
        Map<String, String> env = new HashMap<>();
        for (String line : out.split("\\r?\\n")) {
            line = line.trim();
            if (line.startsWith("export ")) {
                line = line.substring("export ".length());
            }
            int eq = line.indexOf('=');
            if (eq <= 0) {
                continue;
            }
            String value = line.substring(eq + 1);
            if (value.length() >= 2 && value.startsWith("\"") && value.endsWith("\"")) {
                value = value.substring(1, value.length() - 1).replace("\\\"", "\"").replace("\\\\", "\\");
            }
            env.put(line.substring(0, eq), value);
        }
        return env;
    }

    /** The root access key HomeCloud creates on first start, and its region. */
    public static final class Credentials {
        private final String accessKeyId;
        private final String secretAccessKey;
        private final String region;

        /**
         * Credentials and region.
         *
         * @param accessKeyId access key ID
         * @param secretAccessKey secret access key
         * @param region region name
         */
        public Credentials(String accessKeyId, String secretAccessKey, String region) {
            this.accessKeyId = accessKeyId;
            this.secretAccessKey = secretAccessKey;
            this.region = region;
        }

        /**
         * The access key ID.
         *
         * @return the access key ID
         */
        public String getAccessKeyId() {
            return accessKeyId;
        }

        /**
         * The secret access key.
         *
         * @return the secret access key
         */
        public String getSecretAccessKey() {
            return secretAccessKey;
        }

        /**
         * The region, e.g. us-east-1.
         *
         * @return the region name
         */
        public String getRegion() {
            return region;
        }

        @Override
        public String toString() {
            return "Credentials{accessKeyId=" + accessKeyId + ", region=" + region + "}";
        }
    }
}
