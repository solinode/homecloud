// AWS SDK for JavaScript v3 against HomeCloud: STS, S3, SQS and DynamoDB.
//
//   eval "$(homecloud aws-env)"
//   npm install && node index.mjs
import { STSClient, GetCallerIdentityCommand } from "@aws-sdk/client-sts";
import {
  S3Client, CreateBucketCommand, PutObjectCommand, GetObjectCommand, DeleteObjectCommand, DeleteBucketCommand,
} from "@aws-sdk/client-s3";
import {
  SQSClient, CreateQueueCommand, SendMessageCommand, ReceiveMessageCommand, DeleteQueueCommand,
} from "@aws-sdk/client-sqs";
import {
  DynamoDBClient, CreateTableCommand, PutItemCommand, GetItemCommand, DeleteTableCommand, waitUntilTableExists,
} from "@aws-sdk/client-dynamodb";

// The SDK reads AWS_ENDPOINT_URL, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_REGION
// from the environment by itself; they are spelled out here to show what each one is.
const config = {
  endpoint: process.env.AWS_ENDPOINT_URL,           // e.g. http://localhost:8080
  region: process.env.AWS_REGION ?? "us-east-1",
  credentials: {
    accessKeyId: process.env.AWS_ACCESS_KEY_ID,      // a HomeCloud access key
    secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY,
  },
};

const sts = new STSClient(config);
const s3 = new S3Client({ ...config, forcePathStyle: true }); // buckets by path, not by host name
const sqs = new SQSClient(config);
const ddb = new DynamoDBClient(config);

const id = await sts.send(new GetCallerIdentityCommand({}));
console.log("caller:", id.Arn);

const suffix = Date.now().toString(36);
const bucket = `sdk-js-${suffix}`;
await s3.send(new CreateBucketCommand({ Bucket: bucket }));
await s3.send(new PutObjectCommand({ Bucket: bucket, Key: "hello.txt", Body: "hello from JavaScript" }));
const obj = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "hello.txt" }));
console.log("s3:", await obj.Body.transformToString());
await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "hello.txt" }));
await s3.send(new DeleteBucketCommand({ Bucket: bucket }));

const { QueueUrl } = await sqs.send(new CreateQueueCommand({ QueueName: `sdk-js-${suffix}` }));
await sqs.send(new SendMessageCommand({ QueueUrl, MessageBody: "hello from JavaScript" }));
const recv = await sqs.send(new ReceiveMessageCommand({ QueueUrl, WaitTimeSeconds: 5 }));
console.log("sqs:", recv.Messages?.[0]?.Body);
await sqs.send(new DeleteQueueCommand({ QueueUrl }));

const table = `sdk-js-${suffix}`;
await ddb.send(new CreateTableCommand({
  TableName: table, BillingMode: "PAY_PER_REQUEST",
  AttributeDefinitions: [{ AttributeName: "id", AttributeType: "S" }],
  KeySchema: [{ AttributeName: "id", KeyType: "HASH" }],
}));
await waitUntilTableExists({ client: ddb, maxWaitTime: 120 }, { TableName: table });
await ddb.send(new PutItemCommand({ TableName: table, Item: { id: { S: "1" }, msg: { S: "hello from JavaScript" } } }));
const item = await ddb.send(new GetItemCommand({ TableName: table, Key: { id: { S: "1" } } }));
console.log("dynamodb:", item.Item.msg.S);
await ddb.send(new DeleteTableCommand({ TableName: table }));
