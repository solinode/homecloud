import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";

// An S3 bucket with an object, an SQS queue that triggers a Lambda function, and a
// DynamoDB table the function writes each message to. Nothing here is HomeCloud
// specific: the provider is pointed at HomeCloud by AWS_ENDPOINT_URL and Pulumi.yaml.

const bucket = new aws.s3.Bucket("uploads", { forceDestroy: true });

new aws.s3.BucketObject("readme", {
    bucket: bucket.id,
    key: "README.txt",
    content: "uploaded by Pulumi\n",
    contentType: "text/plain",
});

const table = new aws.dynamodb.Table("orders", {
    billingMode: "PAY_PER_REQUEST",
    hashKey: "id",
    attributes: [{ name: "id", type: "S" }],
});

const queue = new aws.sqs.Queue("jobs", { visibilityTimeoutSeconds: 60 });

const role = new aws.iam.Role("consumer-role", {
    assumeRolePolicy: JSON.stringify({
        Version: "2012-10-17",
        Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
    }),
});

new aws.iam.RolePolicyAttachment("consumer-sqs", {
    role: role.name,
    policyArn: aws.iam.ManagedPolicy.AWSLambdaSQSQueueExecutionRole,
});

new aws.iam.RolePolicy("consumer-table", {
    role: role.id,
    policy: table.arn.apply(arn => JSON.stringify({
        Version: "2012-10-17",
        Statement: [{ Effect: "Allow", Action: ["dynamodb:PutItem"], Resource: arn }],
    })),
});

// Declared so `pulumi destroy` removes it: Lambda creates the group on first invoke and,
// on AWS as on HomeCloud, leaves it behind when the function is deleted.
const logGroup = new aws.cloudwatch.LogGroup("consumer-logs", { retentionInDays: 7 });

const consumer = new aws.lambda.Function("consumer", {
    runtime: aws.lambda.Runtime.NodeJS24dX,
    handler: "index.handler",
    role: role.arn,
    timeout: 30,
    loggingConfig: { logFormat: "Text", logGroup: logGroup.name },
    environment: { variables: { TABLE_NAME: table.name } },
    code: new pulumi.asset.AssetArchive({
        "index.js": new pulumi.asset.StringAsset(`
const { DynamoDBClient, PutItemCommand } = require("@aws-sdk/client-dynamodb");
const db = new DynamoDBClient({});
exports.handler = async (event) => {
  for (const r of event.Records) {
    console.log("order received:", r.body);
    await db.send(new PutItemCommand({
      TableName: process.env.TABLE_NAME,
      Item: { id: { S: r.messageId }, body: { S: r.body } },
    }));
  }
};
`),
    }),
});

new aws.lambda.EventSourceMapping("consumer-trigger", {
    eventSourceArn: queue.arn,
    functionName: consumer.arn,
    batchSize: 5,
});

export const bucketName = bucket.id;
export const queueUrl = queue.url;
export const tableName = table.name;
export const functionName = consumer.name;
export const logGroupName = logGroup.name;
