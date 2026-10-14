import { CreateTableCommand, DynamoDBClient, GetItemCommand, PutItemCommand } from "@aws-sdk/client-dynamodb";
import { LambdaClient, ListFunctionsCommand } from "@aws-sdk/client-lambda";
import { CreateBucketCommand, GetObjectCommand, PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { CreateQueueCommand, ReceiveMessageCommand, SendMessageCommand, SQSClient } from "@aws-sdk/client-sqs";
import { GetCallerIdentityCommand, STSClient } from "@aws-sdk/client-sts";
import { getContainerRuntimeClient } from "testcontainers";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { DEFAULT_IMAGE, HomeCloudContainer, parseAwsEnv, type StartedHomeCloudContainer } from "../src/index.js";

// A local build (docker build -t homecloud:test .) or the official image.
const IMAGE = process.env.HOMECLOUD_IMAGE || DEFAULT_IMAGE;

describe("parseAwsEnv", () => {
  it("parses quoted exports", () => {
    const env = parseAwsEnv('export AWS_ACCESS_KEY_ID="AKIAEXAMPLE"\nexport AWS_SECRET_ACCESS_KEY="a/b+c\\"d"\n# x\n');
    expect(env).toEqual({ AWS_ACCESS_KEY_ID: "AKIAEXAMPLE", AWS_SECRET_ACCESS_KEY: 'a/b+c"d' });
  });
});

async function expectNoLeftovers(account: string): Promise<void> {
  const docker = (await getContainerRuntimeClient()).container.dockerode;
  const filters = JSON.stringify({ label: [`homecloud.account=${account}`] });
  expect(await docker.listContainers({ all: true, filters })).toHaveLength(0);
  expect(await docker.listNetworks({ filters })).toHaveLength(0);
  expect((await docker.listVolumes({ filters })).Volumes ?? []).toHaveLength(0);
}

describe("HomeCloudContainer", () => {
  const container = new HomeCloudContainer(IMAGE);
  let hc: StartedHomeCloudContainer;

  beforeAll(async () => {
    hc = await container.start();
  });

  afterAll(async () => {
    if (!hc) return;
    const account = hc.getAccountId();
    await hc.stop();
    await expectNoLeftovers(account);
  });

  // Stops HomeCloud and starts the same container definition again; later tests use the new one.
  it("starts again as a new HomeCloud", async () => {
    const first = hc.getAccountId();
    await hc.stop();
    await expectNoLeftovers(first);
    hc = await container.start();
    expect(hc.getAccountId()).not.toBe(first);
    const s3 = new S3Client(hc.getS3ClientConfig());
    await s3.send(new CreateBucketCommand({ Bucket: "tc-node-restart" }));
  });

  it("exposes connection details", async () => {
    expect(hc.getEndpoint()).toMatch(/^http:\/\//);
    const creds = hc.getCredentials();
    expect(creds.accessKeyId).not.toBe("");
    expect(creds.secretAccessKey).not.toBe("");
    expect(hc.getRegion()).toBe(creds.region);
    expect(hc.getAwsEnv().AWS_ENDPOINT_URL).toBe(hc.getEndpoint());
    const sts = new STSClient(hc.getClientConfig());
    expect((await sts.send(new GetCallerIdentityCommand({}))).Account).toBe(hc.getAccountId());
  });

  it("S3 put and get", async () => {
    const s3 = new S3Client(hc.getS3ClientConfig());
    await s3.send(new CreateBucketCommand({ Bucket: "tc-node" }));
    await s3.send(new PutObjectCommand({ Bucket: "tc-node", Key: "a.txt", Body: "hello" }));
    const obj = await s3.send(new GetObjectCommand({ Bucket: "tc-node", Key: "a.txt" }));
    expect(await obj.Body?.transformToString()).toBe("hello");
  });

  it("SQS", async () => {
    const sqs = new SQSClient(hc.getClientConfig());
    const { QueueUrl } = await sqs.send(new CreateQueueCommand({ QueueName: "tc-node" }));
    await sqs.send(new SendMessageCommand({ QueueUrl, MessageBody: "hi" }));
    const { Messages } = await sqs.send(new ReceiveMessageCommand({ QueueUrl, WaitTimeSeconds: 1 }));
    expect(Messages?.map((m) => m.Body)).toEqual(["hi"]);
  });

  it("DynamoDB", async () => {
    const ddb = new DynamoDBClient(hc.getClientConfig());
    await ddb.send(
      new CreateTableCommand({
        TableName: "tc-node",
        AttributeDefinitions: [{ AttributeName: "id", AttributeType: "S" }],
        KeySchema: [{ AttributeName: "id", KeyType: "HASH" }],
        BillingMode: "PAY_PER_REQUEST",
      }),
    );
    await ddb.send(new PutItemCommand({ TableName: "tc-node", Item: { id: { S: "1" }, n: { N: "2" } } }));
    const { Item } = await ddb.send(new GetItemCommand({ TableName: "tc-node", Key: { id: { S: "1" } } }));
    expect(Item?.n?.N).toBe("2");
  });

  it("Lambda", async () => {
    const lambda = new LambdaClient(hc.getClientConfig());
    expect((await lambda.send(new ListFunctionsCommand({}))).Functions).toEqual([]);
  });
});
