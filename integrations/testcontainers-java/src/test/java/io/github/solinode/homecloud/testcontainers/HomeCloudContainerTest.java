package io.github.solinode.homecloud.testcontainers;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

import com.github.dockerjava.api.DockerClient;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.testcontainers.DockerClientFactory;
import software.amazon.awssdk.core.sync.RequestBody;
import software.amazon.awssdk.services.dynamodb.DynamoDbClient;
import software.amazon.awssdk.services.dynamodb.model.AttributeValue;
import software.amazon.awssdk.services.dynamodb.model.BillingMode;
import software.amazon.awssdk.services.dynamodb.model.KeyType;
import software.amazon.awssdk.services.dynamodb.model.ScalarAttributeType;
import software.amazon.awssdk.services.lambda.LambdaClient;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.sqs.SqsClient;
import software.amazon.awssdk.services.sqs.model.Message;
import software.amazon.awssdk.services.sts.StsClient;

/** Runs against HOMECLOUD_IMAGE (a local build, e.g. homecloud:test) or the official image. */
class HomeCloudContainerTest {

    static final String IMAGE = System.getenv().getOrDefault("HOMECLOUD_IMAGE", HomeCloudContainer.DEFAULT_IMAGE);

    static HomeCloudContainer hc;

    @BeforeAll
    static void start() {
        assumeTrue(DockerClientFactory.instance().isDockerAvailable(), "Docker is not available");
        hc = new HomeCloudContainer(IMAGE);
        hc.start();
    }

    @AfterAll
    static void stopAndCheckCleanup() {
        if (hc == null) {
            return;
        }
        String account = hc.getAccountId();
        hc.stop();
        assertNotNull(account);
        DockerClient docker = DockerClientFactory.lazyClient();
        List<String> label = Collections.singletonList(HomeCloudContainer.ACCOUNT_LABEL + "=" + account);
        assertEquals(0, docker.listContainersCmd().withShowAll(true).withLabelFilter(label).exec().size(), "containers left");
        assertEquals(0, docker.listNetworksCmd().withFilter("label", label).exec().size(), "networks left");
        List<?> volumes = docker.listVolumesCmd().withFilter("label", label).exec().getVolumes();
        assertTrue(volumes == null || volumes.isEmpty(), "volumes left");
    }

    @Test
    void connectionDetails() {
        assertTrue(hc.getEndpoint().startsWith("http://"));
        assertFalse(hc.getAccessKey().isEmpty());
        assertFalse(hc.getSecretKey().isEmpty());
        assertFalse(hc.getRegion().isEmpty());
        Map<String, String> env = hc.getAwsEnv();
        assertEquals(hc.getEndpoint(), env.get("AWS_ENDPOINT_URL"));
        try (StsClient sts = hc.configure(StsClient.builder()).build()) {
            assertEquals(hc.getAccountId(), sts.getCallerIdentity().account());
        }
    }

    @Test
    void s3PutGet() {
        try (S3Client s3 = hc.getS3Client()) {
            s3.createBucket(b -> b.bucket("tc-java"));
            s3.putObject(b -> b.bucket("tc-java").key("a.txt"), RequestBody.fromString("hello"));
            assertEquals("hello", s3.getObjectAsBytes(b -> b.bucket("tc-java").key("a.txt")).asUtf8String());
            assertEquals(1, s3.listObjectsV2(b -> b.bucket("tc-java")).keyCount());
        }
    }

    @Test
    void sqs() {
        try (SqsClient sqs = hc.getSqsClient()) {
            String url = sqs.createQueue(b -> b.queueName("tc-java")).queueUrl();
            sqs.sendMessage(b -> b.queueUrl(url).messageBody("hi"));
            List<Message> msgs = sqs.receiveMessage(b -> b.queueUrl(url).waitTimeSeconds(1)).messages();
            assertEquals(1, msgs.size());
            assertEquals("hi", msgs.get(0).body());
        }
    }

    @Test
    void dynamoDb() {
        try (DynamoDbClient ddb = hc.getDynamoDbClient()) {
            ddb.createTable(b -> b.tableName("tc-java")
                    .attributeDefinitions(a -> a.attributeName("id").attributeType(ScalarAttributeType.S))
                    .keySchema(k -> k.attributeName("id").keyType(KeyType.HASH))
                    .billingMode(BillingMode.PAY_PER_REQUEST));
            ddb.putItem(b -> b.tableName("tc-java").item(Map.of(
                    "id", AttributeValue.fromS("1"), "n", AttributeValue.fromN("2"))));
            Map<String, AttributeValue> item = ddb.getItem(b -> b.tableName("tc-java")
                    .key(Map.of("id", AttributeValue.fromS("1")))).item();
            assertEquals("2", item.get("n").n());
        }
    }

    @Test
    void lambdaList() {
        try (LambdaClient lambda = hc.getLambdaClient()) {
            assertTrue(lambda.listFunctions().functions().isEmpty());
        }
    }
}
