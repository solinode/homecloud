// AWS SDK for Go v2 against HomeCloud: STS, S3, SQS and DynamoDB.
//
//	eval "$(homecloud aws-env)"
//	go run .
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func main() {
	ctx := context.Background()
	// LoadDefaultConfig reads AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION and
	// AWS_ENDPOINT_URL from the environment. BaseEndpoint is set explicitly to show it.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	must(err)
	cfg.BaseEndpoint = aws.String(os.Getenv("AWS_ENDPOINT_URL")) // e.g. http://localhost:8080

	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	must(err)
	fmt.Println("caller:", *id.Arn)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)

	// S3: buckets by path, not by host name.
	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
	bucket := aws.String("sdk-go-" + suffix)
	_, err = s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: bucket})
	must(err)
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: bucket, Key: aws.String("hello.txt"), Body: strings.NewReader("hello from Go")})
	must(err)
	obj, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: aws.String("hello.txt")})
	must(err)
	body, _ := io.ReadAll(obj.Body)
	fmt.Println("s3:", string(body))
	_, err = s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: bucket, Key: aws.String("hello.txt")})
	must(err)
	_, err = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: bucket})
	must(err)

	q := sqs.NewFromConfig(cfg)
	qu, err := q.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("sdk-go-" + suffix)})
	must(err)
	_, err = q.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: qu.QueueUrl, MessageBody: aws.String("hello from Go")})
	must(err)
	rm, err := q.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: qu.QueueUrl, WaitTimeSeconds: 5})
	must(err)
	if len(rm.Messages) == 0 {
		log.Fatal("sqs: no message")
	}
	fmt.Println("sqs:", *rm.Messages[0].Body)
	_, err = q.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: qu.QueueUrl})
	must(err)

	db := dynamodb.NewFromConfig(cfg)
	table := aws.String("sdk-go-" + suffix)
	_, err = db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: table, BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash}},
	})
	must(err)
	must(dynamodb.NewTableExistsWaiter(db).Wait(ctx, &dynamodb.DescribeTableInput{TableName: table}, 2*time.Minute))
	_, err = db.PutItem(ctx, &dynamodb.PutItemInput{TableName: table, Item: map[string]ddbtypes.AttributeValue{
		"id": &ddbtypes.AttributeValueMemberS{Value: "1"}, "msg": &ddbtypes.AttributeValueMemberS{Value: "hello from Go"}}})
	must(err)
	item, err := db.GetItem(ctx, &dynamodb.GetItemInput{TableName: table, Key: map[string]ddbtypes.AttributeValue{
		"id": &ddbtypes.AttributeValueMemberS{Value: "1"}}})
	must(err)
	fmt.Println("dynamodb:", item.Item["msg"].(*ddbtypes.AttributeValueMemberS).Value)
	_, err = db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: table})
	must(err)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
