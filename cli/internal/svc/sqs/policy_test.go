package sqs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/core"
)

func setQueuePolicy(t *testing.T, h *awstest.Harness, url, policy string) {
	t.Helper()
	attrs, _ := json.Marshal(map[string]string{"Policy": policy})
	h.AWS(t, "sqs", "set-queue-attributes", "--queue-url", url, "--attributes", string(attrs))
}

func TestQueuePolicyEnforcement(t *testing.T) {
	h, _ := awsHarness(t)
	acct := h.Env.AccountID
	url := h.AWSJSON(t, "sqs", "create-queue", "--queue-name", "jobs")["QueueUrl"].(string)
	arn := "arn:aws:sqs:us-east-1:" + acct + ":jobs"
	bk, bs := h.User(t, "bob")
	ck, cs := h.User(t, "carol", "AmazonSQSFullAccess")
	dk, ds := h.User(t, "dave")

	denied := func(ak, sk string, args ...string) {
		t.Helper()
		out, err := h.AWSAs(t, ak, sk, "", args...)
		if err == nil || !strings.Contains(out, "AccessDenied") {
			t.Fatalf("aws %s: want AccessDenied, got %v %s", strings.Join(args, " "), err, out)
		}
	}
	ok := func(ak, sk string, args ...string) {
		t.Helper()
		if out, err := h.AWSAs(t, ak, sk, "", args...); err != nil {
			t.Fatalf("aws %s: %v %s", strings.Join(args, " "), err, out)
		}
	}
	send := []string{"sqs", "send-message", "--queue-url", url, "--message-body", "hi"}
	receive := []string{"sqs", "receive-message", "--queue-url", url}

	// No identity policy and no resource policy: denied.
	denied(bk, bs, send...)

	// The queue policy grants bob SendMessage, and nothing else.
	setQueuePolicy(t, h, url, fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Sid":"bob","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:user/bob"},"Action":"sqs:SendMessage","Resource":"%s"}]}`, acct, arn))
	ok(bk, bs, send...)
	denied(bk, bs, receive...)
	denied(dk, ds, send...)

	// The same grant works over the native API.
	if st, b := h.NativeAs(t, bk, bs, "POST", "/api/v1/sqs/queues/jobs/messages", map[string]any{"body": "native"}); st != 200 {
		t.Fatalf("native send by bob: %d %s", st, b)
	}
	if st, _ := h.NativeAs(t, dk, ds, "POST", "/api/v1/sqs/queues/jobs/messages", map[string]any{"body": "native"}); st != 403 {
		t.Fatalf("native send by dave: %d", st)
	}

	// An explicit Deny wins over the identity policy (carol has full access) and
	// over another Allow in the same policy.
	setQueuePolicy(t, h, url, fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"},
		{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::%s:user/carol"},"Action":"sqs:*","Resource":"%s"}]}`, arn, acct, arn))
	denied(ck, cs, send...)
	denied(ck, cs, receive...)
	ok(bk, bs, send...) // Principal "*" allows everyone else
	ok(dk, ds, send...)
	ok(h.AccessKeyID, h.SecretKey, send...) // the account root is never locked out
	ok(h.AccessKeyID, h.SecretKey, "sqs", "set-queue-attributes", "--queue-url", url, "--attributes", "VisibilityTimeout=31")

	// Naming the account only delegates to identity policies.
	setQueuePolicy(t, h, url, fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"sqs:SendMessage","Resource":"%s"}]}`, acct, arn))
	denied(bk, bs, send...)
	ok(ck, cs, send...)

	// Conditions: TLS only.
	setQueuePolicy(t, h, url, fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"},
		{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"%s","Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`, arn, arn))
	denied(bk, bs, send...) // the test endpoint speaks plain HTTP
	denied(ck, cs, send...)

	// Conditions: the caller's address and identity.
	setQueuePolicy(t, h, url, fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s","Condition":{"IpAddress":{"aws:SourceIp":["10.0.0.0/8","192.168.0.0/16"]}}}]}`, arn))
	denied(bk, bs, send...)
	setQueuePolicy(t, h, url, fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s","Condition":{"IpAddress":{"aws:SourceIp":["127.0.0.0/8","::1/128"]},"StringLike":{"aws:username":"b*"}}}]}`, arn))
	ok(bk, bs, send...)
	denied(dk, ds, send...)

	// boto3 sees the same thing.
	out := h.Python(t, fmt.Sprintf(`
def client(k, s):
    return boto3.client("sqs", aws_access_key_id=k, aws_secret_access_key=s)
client(%q, %q).send_message(QueueUrl=%q, MessageBody="from boto3")
try:
    client(%q, %q).send_message(QueueUrl=%q, MessageBody="nope")
    print("sent")
except botocore.exceptions.ClientError as e:
    print(e.response["Error"]["Code"])
`, bk, bs, url, dk, ds, url))
	if strings.TrimSpace(out) != "AccessDenied" && strings.TrimSpace(out) != "AccessDeniedException" {
		t.Fatalf("boto3 as dave: %q", out)
	}

	// AddPermission / RemovePermission statements are enforced too.
	h.AWS(t, "sqs", "set-queue-attributes", "--queue-url", url, "--attributes", `{"Policy":""}`)
	denied(dk, ds, send...)
	h.AWS(t, "sqs", "add-permission", "--queue-url", url, "--label", "share", "--aws-account-ids", acct, "--actions", "SendMessage")
	denied(dk, ds, send...) // the account root only delegates
	ok(ck, cs, send...)
}

func TestAllowDelivery(t *testing.T) {
	h, s := awsHarness(t)
	acct := h.Env.AccountID
	h.AWS(t, "sqs", "create-queue", "--queue-name", "open")
	h.AWS(t, "sqs", "create-queue", "--queue-name", "guarded")
	src := core.Source{Service: "sns.amazonaws.com", ARN: "arn:aws:sns:us-east-1:" + acct + ":alerts"}
	if !s.AllowDelivery("open", src) {
		t.Fatal("a queue without a policy accepts deliveries")
	}
	url := h.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "guarded")["QueueUrl"].(string)
	arn := "arn:aws:sqs:us-east-1:" + acct + ":guarded"
	setQueuePolicy(t, h, url, fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"%s","Condition":{"ArnEquals":{"aws:SourceArn":"%s"}}}]}`, arn, src.ARN))
	if !s.AllowDelivery("guarded", src) {
		t.Error("the topic in aws:SourceArn is allowed")
	}
	if s.AllowDelivery("guarded", core.Source{Service: "sns.amazonaws.com", ARN: "arn:aws:sns:us-east-1:" + acct + ":other"}) {
		t.Error("another topic is not allowed")
	}
	if s.AllowDelivery("guarded", core.Source{Service: "events.amazonaws.com", ARN: src.ARN}) {
		t.Error("another service is not allowed")
	}
	setQueuePolicy(t, h, url, fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"},
		{"Effect":"Deny","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"%s"}]}`, arn, arn))
	if s.AllowDelivery("guarded", src) {
		t.Error("an explicit Deny for the service wins")
	}
}
