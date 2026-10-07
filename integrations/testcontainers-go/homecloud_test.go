package homecloud_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"

	homecloud "github.com/solinode/homecloud/integrations/testcontainers-go"
)

// image is HOMECLOUD_IMAGE (e.g. a local build of integrations/testdata/Dockerfile)
// or the official image.
func image() string {
	if v := os.Getenv("HOMECLOUD_IMAGE"); v != "" {
		return v
	}
	return homecloud.DefaultImage
}

func TestHomeCloud(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	hc, err := homecloud.Run(ctx, image())
	if hc != nil {
		t.Cleanup(func() {
			if err := testcontainers.TerminateContainer(hc); err != nil {
				t.Errorf("terminate: %v", err)
			}
			assertNoResources(t, hc.AccountID())
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hc.EndpointURL(), "http://") || hc.Credentials().AccessKeyID == "" || hc.AccountID() == "" {
		t.Fatalf("endpoint %q, credentials %+v, account %q", hc.EndpointURL(), hc.Credentials().AccessKeyID, hc.AccountID())
	}
	cfg := hc.AWSConfig()

	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatalf("sts: %v", err)
	}
	if aws.ToString(id.Account) != hc.AccountID() {
		t.Errorf("caller account %s, want %s", aws.ToString(id.Account), hc.AccountID())
	}

	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
	if _, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("tc-go")}); err != nil {
		t.Fatalf("s3 create bucket: %v", err)
	}
	if _, err := s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("tc-go"), Key: aws.String("a.txt"), Body: strings.NewReader("hello")}); err != nil {
		t.Fatalf("s3 put: %v", err)
	}
	obj, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("tc-go"), Key: aws.String("a.txt")})
	if err != nil {
		t.Fatalf("s3 get: %v", err)
	}
	b, _ := io.ReadAll(obj.Body)
	obj.Body.Close()
	if string(b) != "hello" {
		t.Errorf("s3 body %q", b)
	}

	sq := sqs.NewFromConfig(cfg)
	q, err := sq.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("tc-go")})
	if err != nil {
		t.Fatalf("sqs create: %v", err)
	}
	if _, err := sq.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: q.QueueUrl, MessageBody: aws.String("hi")}); err != nil {
		t.Fatalf("sqs send: %v", err)
	}
	msgs, err := sq.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl, WaitTimeSeconds: 1})
	if err != nil || len(msgs.Messages) != 1 || aws.ToString(msgs.Messages[0].Body) != "hi" {
		t.Fatalf("sqs receive: %v %+v", err, msgs)
	}

	ddb := dynamodb.NewFromConfig(cfg)
	if _, err := ddb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String("tc-go"),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode:          ddbtypes.BillingModePayPerRequest,
	}); err != nil {
		t.Fatalf("dynamodb create: %v", err)
	}
	item := map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: "1"}}
	if _, err := ddb.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String("tc-go"), Item: item}); err != nil {
		t.Fatalf("dynamodb put: %v", err)
	}
	got, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String("tc-go"), Key: item})
	if err != nil || got.Item["id"] == nil {
		t.Fatalf("dynamodb get: %v %+v", err, got)
	}

	if _, err := lambda.NewFromConfig(cfg).ListFunctions(ctx, &lambda.ListFunctionsInput{}); err != nil {
		t.Fatalf("lambda list: %v", err)
	}
}

// assertNoResources checks that Terminate left nothing of the account behind.
func assertNoResources(t *testing.T, account string) {
	t.Helper()
	ctx := context.Background()
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		t.Error(err)
		return
	}
	defer cli.Close()
	f := client.Filters{}.Add("label", "homecloud.account="+account)
	cs, _ := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	ns, _ := cli.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	vs, _ := cli.VolumeList(ctx, client.VolumeListOptions{Filters: f})
	if len(cs.Items)+len(ns.Items)+len(vs.Items) > 0 {
		t.Errorf("left behind: %d containers, %d networks, %d volumes", len(cs.Items), len(ns.Items), len(vs.Items))
	}
}
