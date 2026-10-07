package homecloud_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

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

// TestRunFailureStillCleansUp makes Run fail after HomeCloud has started (and
// created its helper containers): the returned container must still know its
// account, so that Terminate removes them.
func TestRunFailureStillCleansUp(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	hc, err := homecloud.Run(ctx, image(), testcontainers.WithAdditionalWaitStrategyAndDeadline(
		20*time.Second, wait.ForLog("s3: MinIO ready"), wait.ForLog("a line HomeCloud never logs")))
	if err == nil {
		t.Error("Run succeeded; want a wait-strategy failure")
	}
	if hc == nil {
		t.Fatal("Run returned no container")
	}
	account := hc.AccountID()
	if account == "" {
		t.Error("no account after a failed start")
	}
	if n := countResources(t, account); n == 0 {
		t.Error("HomeCloud created no helper resources before the failure; the test proves nothing")
	}
	if err := testcontainers.TerminateContainer(hc); err != nil {
		t.Errorf("terminate: %v", err)
	}
	assertNoResources(t, account)
}

// assertNoResources checks that Terminate left nothing of the account behind.
func assertNoResources(t *testing.T, account string) {
	t.Helper()
	if n := countResources(t, account); n > 0 {
		t.Errorf("left behind: %d containers, networks and volumes of account %s", n, account)
	}
}

// countResources counts the containers, networks and volumes of account.
func countResources(t *testing.T, account string) int {
	t.Helper()
	if account == "" {
		t.Error("no account to look up")
		return -1
	}
	ctx := context.Background()
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		t.Error(err)
		return -1
	}
	defer cli.Close()
	f := client.Filters{}.Add("label", "homecloud.account="+account)
	cs, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		t.Errorf("list containers: %v", err)
	}
	ns, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		t.Errorf("list networks: %v", err)
	}
	vs, err := cli.VolumeList(ctx, client.VolumeListOptions{Filters: f})
	if err != nil {
		t.Errorf("list volumes: %v", err)
	}
	return len(cs.Items) + len(ns.Items) + len(vs.Items)
}
