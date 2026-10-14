import * as cdk from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as sqs from 'aws-cdk-lib/aws-sqs';
import { SqsEventSource } from 'aws-cdk-lib/aws-lambda-event-sources';

// An SQS queue feeding a Lambda function that writes each message to a DynamoDB table,
// plus an S3 bucket. Nothing here is HomeCloud specific: the same app deploys to AWS.
export class BasicStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    const bucket = new s3.Bucket(this, 'Uploads', {
      versioned: true,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    const table = new dynamodb.Table(this, 'Orders', {
      partitionKey: { name: 'id', type: dynamodb.AttributeType.STRING },
      billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    const queue = new sqs.Queue(this, 'Jobs', {
      visibilityTimeout: cdk.Duration.seconds(60),
    });

    const logGroup = new logs.LogGroup(this, 'ConsumerLogs', {
      retention: logs.RetentionDays.ONE_WEEK,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
    });

    const consumer = new lambda.Function(this, 'Consumer', {
      runtime: lambda.Runtime.NODEJS_22_X,
      handler: 'index.handler',
      timeout: cdk.Duration.seconds(30),
      logGroup,
      environment: { TABLE_NAME: table.tableName },
      code: lambda.Code.fromInline(`
const { DynamoDBClient, PutItemCommand } = require('@aws-sdk/client-dynamodb');
const db = new DynamoDBClient({});
exports.handler = async (event) => {
  for (const r of event.Records) {
    console.log('order received:', r.body);
    await db.send(new PutItemCommand({
      TableName: process.env.TABLE_NAME,
      Item: { id: { S: r.messageId }, body: { S: r.body } },
    }));
  }
};
`),
    });
    table.grantWriteData(consumer);
    consumer.addEventSource(new SqsEventSource(queue, { batchSize: 5 }));

    new cdk.CfnOutput(this, 'BucketName', { value: bucket.bucketName });
    new cdk.CfnOutput(this, 'TableName', { value: table.tableName });
    new cdk.CfnOutput(this, 'QueueUrl', { value: queue.queueUrl });
    new cdk.CfnOutput(this, 'FunctionName', { value: consumer.functionName });
    new cdk.CfnOutput(this, 'LogGroupName', { value: logGroup.logGroupName });
  }
}
