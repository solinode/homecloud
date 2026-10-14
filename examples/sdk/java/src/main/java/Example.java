// AWS SDK for Java v2 against HomeCloud: STS, S3, SQS and DynamoDB.
//
//   eval "$(homecloud aws-env)"
//   mvn -q compile exec:java
import java.net.URI;
import java.util.Map;

import software.amazon.awssdk.auth.credentials.AwsBasicCredentials;
import software.amazon.awssdk.auth.credentials.StaticCredentialsProvider;
import software.amazon.awssdk.core.sync.RequestBody;
import software.amazon.awssdk.regions.Region;
import software.amazon.awssdk.services.dynamodb.DynamoDbClient;
import software.amazon.awssdk.services.dynamodb.model.AttributeDefinition;
import software.amazon.awssdk.services.dynamodb.model.AttributeValue;
import software.amazon.awssdk.services.dynamodb.model.BillingMode;
import software.amazon.awssdk.services.dynamodb.model.KeySchemaElement;
import software.amazon.awssdk.services.dynamodb.model.KeyType;
import software.amazon.awssdk.services.dynamodb.model.ScalarAttributeType;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.sqs.SqsClient;
import software.amazon.awssdk.services.sts.StsClient;

public class Example {
    public static void main(String[] args) {
        // The SDK also reads these from the environment (AWS_ENDPOINT_URL since 2.21);
        // they are spelled out here to show what each one is.
        URI endpoint = URI.create(System.getenv("AWS_ENDPOINT_URL")); // e.g. http://localhost:8080
        Region region = Region.US_EAST_1;
        var creds = StaticCredentialsProvider.create(AwsBasicCredentials.create(
                System.getenv("AWS_ACCESS_KEY_ID"), System.getenv("AWS_SECRET_ACCESS_KEY")));

        try (var sts = StsClient.builder().endpointOverride(endpoint).region(region).credentialsProvider(creds).build()) {
            System.out.println("caller: " + sts.getCallerIdentity().arn());
        }

        String suffix = Long.toString(System.currentTimeMillis(), 36);

        // S3: buckets by path, not by host name.
        try (var s3 = S3Client.builder().endpointOverride(endpoint).region(region).credentialsProvider(creds)
                .forcePathStyle(true).build()) {
            String bucket = "sdk-java-" + suffix;
            s3.createBucket(b -> b.bucket(bucket));
            s3.putObject(b -> b.bucket(bucket).key("hello.txt"), RequestBody.fromString("hello from Java"));
            System.out.println("s3: " + s3.getObjectAsBytes(b -> b.bucket(bucket).key("hello.txt")).asUtf8String());
            s3.deleteObject(b -> b.bucket(bucket).key("hello.txt"));
            s3.deleteBucket(b -> b.bucket(bucket));
        }

        try (var sqs = SqsClient.builder().endpointOverride(endpoint).region(region).credentialsProvider(creds).build()) {
            String url = sqs.createQueue(b -> b.queueName("sdk-java-" + suffix)).queueUrl();
            sqs.sendMessage(b -> b.queueUrl(url).messageBody("hello from Java"));
            var msgs = sqs.receiveMessage(b -> b.queueUrl(url).waitTimeSeconds(5)).messages();
            System.out.println("sqs: " + msgs.get(0).body());
            sqs.deleteQueue(b -> b.queueUrl(url));
        }

        try (var ddb = DynamoDbClient.builder().endpointOverride(endpoint).region(region).credentialsProvider(creds).build()) {
            String table = "sdk-java-" + suffix;
            ddb.createTable(b -> b.tableName(table).billingMode(BillingMode.PAY_PER_REQUEST)
                    .attributeDefinitions(AttributeDefinition.builder().attributeName("id").attributeType(ScalarAttributeType.S).build())
                    .keySchema(KeySchemaElement.builder().attributeName("id").keyType(KeyType.HASH).build()));
            ddb.waiter().waitUntilTableExists(b -> b.tableName(table));
            ddb.putItem(b -> b.tableName(table).item(Map.of(
                    "id", AttributeValue.fromS("1"), "msg", AttributeValue.fromS("hello from Java"))));
            var item = ddb.getItem(b -> b.tableName(table).key(Map.of("id", AttributeValue.fromS("1")))).item();
            System.out.println("dynamodb: " + item.get("msg").s());
            ddb.deleteTable(b -> b.tableName(table));
        }
    }
}
