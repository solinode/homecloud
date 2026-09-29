package sns

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
)

// fakeLambda records invocations of the function "fn" (and fails "broken").
type fakeLambda struct {
	mu    sync.Mutex
	calls [][]byte
}

func (f *fakeLambda) Invoke(_ context.Context, name string, payload []byte) (*lambda.InvokeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, payload)
	if name == "broken" {
		return &lambda.InvokeResult{FunctionError: "Unhandled", Payload: []byte(`{"errorMessage":"boom"}`)}, nil
	}
	return &lambda.InvokeResult{StatusCode: 200, Payload: []byte("null")}, nil
}

func (f *fakeLambda) Exists(name string) bool { return name == "fn" || name == "broken" }

func (f *fakeLambda) wait(t *testing.T, n int) [][]byte {
	t.Helper()
	for i := 0; i < 100; i++ {
		f.mu.Lock()
		c := append([][]byte(nil), f.calls...)
		f.mu.Unlock()
		if len(c) >= n {
			return c
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("lambda invoked %d times, want %d", len(f.calls), n)
	return nil
}

func harness(t *testing.T) (*awstest.Harness, *Service, *fakeLambda) {
	t.Helper()
	h := awstest.New(t)
	q := sqs.New(h.Env)
	q.Routes(h.Router)
	q.RegisterAWS()
	fl := &fakeLambda{}
	s := New(h.Env, q, fl)
	s.Routes(h.Router)
	s.RegisterAWS()
	// Tests deliver to endpoints on 127.0.0.1.
	s.HTTP, s.CheckURL = &http.Client{Timeout: 5 * time.Second}, func(string) error { return nil }
	return h, s, fl
}

func TestAWSCLI(t *testing.T) {
	h, _, _ := harness(t)
	arn := h.AWSJSON(t, "sns", "create-topic", "--name", "events", "--attributes", "DisplayName=Events", "--tags", "Key=team,Value=core")["TopicArn"].(string)
	if arn != "arn:aws:sns:us-east-1:"+h.Env.AccountID+":events" {
		t.Fatalf("arn %s", arn)
	}
	if again := h.AWSJSON(t, "sns", "create-topic", "--name", "events", "--attributes", "DisplayName=Events")["TopicArn"]; again != arn {
		t.Fatalf("idempotent create returned %v", again)
	}
	if out, err := h.AWSErr(t, "sns", "create-topic", "--name", "events", "--attributes", "DisplayName=Other"); err == nil || !strings.Contains(out, "InvalidParameter") {
		t.Fatalf("conflicting create: %v %s", err, out)
	}
	topics := h.AWSJSON(t, "sns", "list-topics")["Topics"].([]any)
	if len(topics) != 1 || topics[0].(map[string]any)["TopicArn"] != arn {
		t.Fatalf("topics %v", topics)
	}
	h.AWS(t, "sns", "set-topic-attributes", "--topic-arn", arn, "--attribute-name", "DisplayName", "--attribute-value", "Renamed")
	attrs := h.AWSJSON(t, "sns", "get-topic-attributes", "--topic-arn", arn)["Attributes"].(map[string]any)
	if attrs["DisplayName"] != "Renamed" || attrs["Owner"] != h.Env.AccountID || !strings.Contains(attrs["Policy"].(string), "__default_policy_ID") {
		t.Fatalf("attributes %v", attrs)
	}
	qurl := h.AWSJSON(t, "sqs", "create-queue", "--queue-name", "inbox")["QueueUrl"].(string)
	qarn := "arn:aws:sqs:us-east-1:" + h.Env.AccountID + ":inbox"
	sub := h.AWSJSON(t, "sns", "subscribe", "--topic-arn", arn, "--protocol", "sqs", "--notification-endpoint", qarn)["SubscriptionArn"].(string)
	if !strings.HasPrefix(sub, arn+":") {
		t.Fatalf("subscription %s", sub)
	}
	sa := h.AWSJSON(t, "sns", "get-subscription-attributes", "--subscription-arn", sub)["Attributes"].(map[string]any)
	if sa["Protocol"] != "sqs" || sa["Endpoint"] != qarn || sa["PendingConfirmation"] != "false" || sa["RawMessageDelivery"] != "false" {
		t.Fatalf("subscription attributes %v", sa)
	}
	pub := h.AWSJSON(t, "sns", "publish", "--topic-arn", arn, "--subject", "Hi", "--message", "hello world",
		"--message-attributes", `{"kind":{"DataType":"String","StringValue":"greeting"}}`)
	msgs := h.AWSJSON(t, "sqs", "receive-message", "--queue-url", qurl)["Messages"].([]any)
	var env map[string]any
	if err := json.Unmarshal([]byte(msgs[0].(map[string]any)["Body"].(string)), &env); err != nil {
		t.Fatal(err)
	}
	if env["Type"] != "Notification" || env["MessageId"] != pub["MessageId"] || env["Message"] != "hello world" || env["Subject"] != "Hi" ||
		env["TopicArn"] != arn || env["SignatureVersion"] != "1" || env["Signature"] == "" ||
		env["MessageAttributes"].(map[string]any)["kind"].(map[string]any)["Value"] != "greeting" {
		t.Fatalf("envelope %v", env)
	}
	subs := h.AWSJSON(t, "sns", "list-subscriptions-by-topic", "--topic-arn", arn)["Subscriptions"].([]any)
	if len(subs) != 1 || subs[0].(map[string]any)["SubscriptionArn"] != sub {
		t.Fatalf("subscriptions %v", subs)
	}
	h.AWS(t, "sns", "tag-resource", "--resource-arn", arn, "--tags", "Key=env,Value=dev")
	h.AWS(t, "sns", "untag-resource", "--resource-arn", arn, "--tag-keys", "team")
	tags := h.AWSJSON(t, "sns", "list-tags-for-resource", "--resource-arn", arn)["Tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["Key"] != "env" {
		t.Fatalf("tags %v", tags)
	}
	h.AWS(t, "sns", "unsubscribe", "--subscription-arn", sub)
	if attrs := h.AWSJSON(t, "sns", "get-topic-attributes", "--topic-arn", arn)["Attributes"].(map[string]any); attrs["SubscriptionsDeleted"] != "1" || attrs["SubscriptionsConfirmed"] != "0" {
		t.Fatalf("counts %v", attrs)
	}
	h.AWS(t, "sns", "delete-topic", "--topic-arn", arn)
	h.AWS(t, "sns", "delete-topic", "--topic-arn", arn) // idempotent
	if out, err := h.AWSErr(t, "sns", "get-topic-attributes", "--topic-arn", arn); err == nil || !strings.Contains(out, "NotFound") {
		t.Fatalf("deleted topic: %v %s", err, out)
	}
}

const pyHelpers = `
import time, base64
sns = boto3.client("sns")
sqs = boto3.client("sqs")
def queue(name, attrs=None):
    url = sqs.create_queue(QueueName=name, Attributes=attrs or {})["QueueUrl"]
    return url, sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
def drain(url, n, wait=2, **kw):
    out = []
    deadline = time.time() + wait
    while len(out) < n and time.time() < deadline:
        r = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, WaitTimeSeconds=1, **kw)
        for m in r.get("Messages", []):
            out.append(m)
            sqs.delete_message(QueueUrl=url, ReceiptHandle=m["ReceiptHandle"])
    return out
`

func TestBoto3FanOut(t *testing.T) {
	h, _, _ := harness(t)
	h.Python(t, pyHelpers+`
topic = sns.create_topic(Name="orders")["TopicArn"]
raw_url, raw_arn = queue("raw")
env_url, env_arn = queue("env")
flt_url, flt_arn = queue("filtered")
body_url, body_arn = queue("body")
sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=raw_arn, Attributes={"RawMessageDelivery": "true"})
sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=env_arn)
sub = sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=flt_arn, ReturnSubscriptionArn=True,
                    Attributes={"FilterPolicy": json.dumps({"kind": ["big"], "price": [{"numeric": [">=", 100]}]})})["SubscriptionArn"]
sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=body_arn,
              Attributes={"FilterPolicyScope": "MessageBody", "FilterPolicy": json.dumps({"order": {"status": [{"prefix": "pa"}]}})})

attrs = {"kind": {"DataType": "String", "StringValue": "big"}, "price": {"DataType": "Number", "StringValue": "150"},
         "bin": {"DataType": "Binary", "BinaryValue": b"\x00\xff"}, "list": {"DataType": "String.Array", "StringValue": "[\"a\",1]"}}
mid = sns.publish(TopicArn=topic, Message=json.dumps({"order": {"status": "paid"}}), Subject="New order", MessageAttributes=attrs)["MessageId"]
sns.publish(TopicArn=topic, Message=json.dumps({"order": {"status": "open"}}), MessageAttributes={"kind": {"DataType": "String", "StringValue": "small"}})

raw = drain(raw_url, 2, MessageAttributeNames=["All"])
assert len(raw) == 2, raw
r = [m for m in raw if "paid" in m["Body"]][0]
assert json.loads(r["Body"]) == {"order": {"status": "paid"}}, r  # raw: the message itself
assert r["MessageAttributes"]["kind"]["StringValue"] == "big" and r["MessageAttributes"]["bin"]["BinaryValue"] == b"\x00\xff", r

env = drain(env_url, 2)
e = [json.loads(m["Body"]) for m in env if "paid" in json.loads(m["Body"])["Message"]][0]
for k in ["Type", "MessageId", "TopicArn", "Subject", "Message", "Timestamp", "SignatureVersion", "Signature", "SigningCertURL", "UnsubscribeURL", "MessageAttributes"]:
    assert k in e, (k, e)
assert e["MessageId"] == mid and e["Subject"] == "New order" and e["Type"] == "Notification"
assert e["MessageAttributes"]["bin"] == {"Type": "Binary", "Value": base64.b64encode(b"\x00\xff").decode()}, e
assert e["MessageAttributes"]["price"] == {"Type": "Number", "Value": "150"}
assert e["Timestamp"].endswith("Z")

flt = drain(flt_url, 2)
assert len(flt) == 1 and json.loads(flt[0]["Body"])["MessageId"] == mid, flt
bod = drain(body_url, 2)
assert len(bod) == 1 and json.loads(bod[0]["Body"])["MessageId"] == mid, bod

# Changing the filter policy takes effect.
sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="FilterPolicy", AttributeValue=json.dumps({"kind": [{"anything-but": "big"}]}))
a = sns.get_subscription_attributes(SubscriptionArn=sub)["Attributes"]
assert json.loads(a["FilterPolicy"]) == {"kind": [{"anything-but": "big"}]} and a["FilterPolicyScope"] == "MessageAttributes", a
sns.publish(TopicArn=topic, Message="m1", MessageAttributes={"kind": {"DataType": "String", "StringValue": "big"}})
sns.publish(TopicArn=topic, Message="m2", MessageAttributes={"kind": {"DataType": "String", "StringValue": "tiny"}})
got = [json.loads(m["Body"])["Message"] for m in drain(flt_url, 2)]
assert got == ["m2"], got
drain(raw_url, 2)
drain(env_url, 2)
try:
    sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="FilterPolicy", AttributeValue='{"kind": "big"}')
    raise SystemExit("bad filter policy accepted")
except sns.exceptions.InvalidParameterException:
    pass

# MessageStructure=json picks the sqs entry.
sns.publish(TopicArn=topic, MessageStructure="json", Message=json.dumps({"default": "dflt", "sqs": "for-sqs"}))
got = [m["Body"] for m in drain(raw_url, 1)]
assert got == ["for-sqs"], got
try:
    sns.publish(TopicArn=topic, MessageStructure="json", Message=json.dumps({"sqs": "x"}))
    raise SystemExit("json structure without default accepted")
except sns.exceptions.InvalidParameterException as e:
    assert "No default entry" in str(e)

# PublishBatch with a partial failure.
r = sns.publish_batch(TopicArn=topic, PublishBatchRequestEntries=[
    {"Id": "a", "Message": "b1"},
    {"Id": "b", "Message": "b2", "MessageAttributes": {"n": {"DataType": "Number", "StringValue": "nan-x"}}},
    {"Id": "c", "Message": "b3"}])
assert sorted(x["Id"] for x in r["Successful"]) == ["a", "c"], r
assert r["Failed"][0]["Id"] == "b" and r["Failed"][0]["SenderFault"], r
assert sorted(m["Body"] for m in drain(raw_url, 2)) == ["b1", "b3"]
try:
    sns.publish_batch(TopicArn=topic, PublishBatchRequestEntries=[{"Id": "x", "Message": "1"}, {"Id": "x", "Message": "2"}])
    raise SystemExit("duplicate batch ids accepted")
except sns.exceptions.BatchEntryIdsNotDistinctException:
    pass

# Errors.
try:
    sns.publish(TopicArn=topic.replace("orders", "nope"), Message="x")
    raise SystemExit("publish to missing topic accepted")
except sns.exceptions.NotFoundException:
    pass
try:
    sns.publish(TopicArn=topic, Message="")
    raise SystemExit("empty message accepted")
except sns.exceptions.InvalidParameterException:
    pass

# Pagination of ListSubscriptions and ListTopics.
for i in range(3):
    sns.create_topic(Name="t%d" % i)
arns = [t["TopicArn"] for p in sns.get_paginator("list_topics").paginate() for t in p["Topics"]]
assert len(arns) == 4, arns
subs = [s for p in sns.get_paginator("list_subscriptions").paginate() for s in p["Subscriptions"]]
assert len(subs) == 4 and all(s["Owner"] for s in subs), subs
`)
}

func TestBoto3FIFO(t *testing.T) {
	h, _, _ := harness(t)
	h.Python(t, pyHelpers+`
topic = sns.create_topic(Name="t.fifo", Attributes={"FifoTopic": "true", "ContentBasedDeduplication": "true"})["TopicArn"]
a = sns.get_topic_attributes(TopicArn=topic)["Attributes"]
assert a["FifoTopic"] == "true" and a["ContentBasedDeduplication"] == "true", a
try:
    sns.create_topic(Name="plain", Attributes={"FifoTopic": "true"})
    raise SystemExit("FIFO topic without .fifo accepted")
except sns.exceptions.InvalidParameterException:
    pass
url, arn = queue("q.fifo", {"FifoQueue": "true"})
sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=arn, Attributes={"RawMessageDelivery": "true"})
try:
    sns.subscribe(TopicArn=topic, Protocol="lambda", Endpoint="arn:aws:lambda:us-east-1:123456789012:function:fn")
    raise SystemExit("lambda subscription to a FIFO topic accepted")
except sns.exceptions.InvalidParameterException:
    pass
seqs = []
for body, g in [("a1", "A"), ("b1", "B"), ("a2", "A"), ("a1", "A")]:
    r = sns.publish(TopicArn=topic, Message=body, MessageGroupId=g)
    seqs.append(r["SequenceNumber"])
assert seqs[3] == seqs[0] and seqs[0] < seqs[1] < seqs[2], seqs
try:
    sns.publish(TopicArn=topic, Message="x")
    raise SystemExit("FIFO publish without group accepted")
except sns.exceptions.InvalidParameterException:
    pass
msgs = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, AttributeNames=["All"])["Messages"]
assert [m["Body"] for m in msgs] == ["a1", "b1", "a2"], msgs
assert msgs[0]["Attributes"]["MessageGroupId"] == "A"
`)
}

// TestHTTPSubscription drives the HTTP(S) confirmation flow and delivery
// against a local endpoint, verifying message signatures.
func TestHTTPSubscription(t *testing.T) {
	h, s, _ := harness(t)
	type req struct {
		hdr  http.Header
		body map[string]string
		raw  string
	}
	got := make(chan req, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		sm := map[string]string{}
		for k, v := range m {
			if s, ok := v.(string); ok {
				sm[k] = s
			}
		}
		got <- req{r.Header, sm, string(b)}
	}))
	defer srv.Close()
	next := func() req {
		select {
		case r := <-got:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("no request reached the endpoint")
		}
		return req{}
	}
	arn := h.AWSJSON(t, "sns", "create-topic", "--name", "hooks")["TopicArn"].(string)
	out := h.AWSJSON(t, "sns", "subscribe", "--topic-arn", arn, "--protocol", "http", "--notification-endpoint", srv.URL+"/hook")
	if out["SubscriptionArn"] != "pending confirmation" {
		t.Fatalf("subscribe %v", out)
	}
	conf := next()
	if conf.hdr.Get("x-amz-sns-message-type") != "SubscriptionConfirmation" || conf.body["Type"] != "SubscriptionConfirmation" || conf.body["Token"] == "" {
		t.Fatalf("confirmation %v %v", conf.hdr, conf.body)
	}
	cert := get(t, conf.body["SigningCertURL"])
	if err := VerifyNotification(conf.body, cert); err != nil {
		t.Fatalf("confirmation signature: %v", err)
	}
	subs := h.AWSJSON(t, "sns", "list-subscriptions-by-topic", "--topic-arn", arn)["Subscriptions"].([]any)
	if subs[0].(map[string]any)["SubscriptionArn"] != "PendingConfirmation" {
		t.Fatalf("pending subscription listed as %v", subs)
	}
	// Publishing to a pending subscription delivers nothing.
	h.AWS(t, "sns", "publish", "--topic-arn", arn, "--message", "too early")
	// Visiting SubscribeURL (as endpoint libraries do) confirms it, without credentials.
	if body := string(get(t, conf.body["SubscribeURL"])); !strings.Contains(body, "<SubscriptionArn>"+arn+":") {
		t.Fatalf("confirm via SubscribeURL: %s", body)
	}
	pub := h.AWSJSON(t, "sns", "publish", "--topic-arn", arn, "--message", "hello hook", "--subject", "S")
	n := next()
	if n.body["Message"] == "too early" {
		t.Fatal("a pending subscription received a notification")
	}
	if n.hdr.Get("x-amz-sns-message-type") != "Notification" || n.hdr.Get("x-amz-sns-message-id") != pub["MessageId"] ||
		!strings.HasPrefix(n.hdr.Get("x-amz-sns-subscription-arn"), arn+":") || n.body["Message"] != "hello hook" || n.body["Subject"] != "S" {
		t.Fatalf("notification %v %v", n.hdr, n.body)
	}
	if err := VerifyNotification(n.body, cert); err != nil {
		t.Fatalf("notification signature: %v", err)
	}
	subARN := n.hdr.Get("x-amz-sns-subscription-arn")
	// Signature version 2 (SHA256).
	h.AWS(t, "sns", "set-topic-attributes", "--topic-arn", arn, "--attribute-name", "SignatureVersion", "--attribute-value", "2")
	h.AWS(t, "sns", "publish", "--topic-arn", arn, "--message", "v2")
	if n := next(); n.body["SignatureVersion"] != "2" || VerifyNotification(n.body, cert) != nil {
		t.Fatalf("v2 notification %v", n.body)
	}
	// Raw delivery posts the bare message.
	h.AWS(t, "sns", "set-subscription-attributes", "--subscription-arn", subARN, "--attribute-name", "RawMessageDelivery", "--attribute-value", "true")
	h.AWS(t, "sns", "publish", "--topic-arn", arn, "--message", "raw body")
	if n := next(); n.raw != "raw body" || n.hdr.Get("x-amz-sns-rawdelivery") != "true" {
		t.Fatalf("raw delivery %q %v", n.raw, n.hdr)
	}
	// Unsubscribing via UnsubscribeURL sends UnsubscribeConfirmation.
	unsub := n.body["UnsubscribeURL"]
	if unsub == "" {
		t.Fatal("no UnsubscribeURL")
	}
	get(t, unsub)
	if u := next(); u.body["Type"] != "UnsubscribeConfirmation" {
		t.Fatalf("unsubscribe confirmation %v", u.body)
	}
	if subs := h.AWSJSON(t, "sns", "list-subscriptions-by-topic", "--topic-arn", arn)["Subscriptions"].([]any); len(subs) != 0 {
		t.Fatalf("subscriptions after unsubscribe %v", subs)
	}

	// ConfirmSubscription through the API with the token.
	h.AWS(t, "sns", "subscribe", "--topic-arn", arn, "--protocol", "http", "--notification-endpoint", srv.URL+"/other")
	c2 := next()
	sub2 := h.AWSJSON(t, "sns", "confirm-subscription", "--topic-arn", arn, "--token", c2.body["Token"], "--authenticate-on-unsubscribe", "true")["SubscriptionArn"].(string)
	sa := h.AWSJSON(t, "sns", "get-subscription-attributes", "--subscription-arn", sub2)["Attributes"].(map[string]any)
	if sa["PendingConfirmation"] != "false" || sa["ConfirmationWasAuthenticated"] != "true" {
		t.Fatalf("confirmed attributes %v", sa)
	}
	if out, err := h.AWSErr(t, "sns", "confirm-subscription", "--topic-arn", arn, "--token", "bogus"); err == nil || !strings.Contains(out, "InvalidParameter") {
		t.Fatalf("bogus token: %v %s", err, out)
	}
	_ = s
}

func get(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, b)
	}
	return b
}

func TestLambdaSubscription(t *testing.T) {
	h, _, fl := harness(t)
	arn := h.AWSJSON(t, "sns", "create-topic", "--name", "fn-topic")["TopicArn"].(string)
	fn := "arn:aws:lambda:us-east-1:" + h.Env.AccountID + ":function:fn"
	sub := h.AWSJSON(t, "sns", "subscribe", "--topic-arn", arn, "--protocol", "lambda", "--notification-endpoint", fn)["SubscriptionArn"].(string)
	h.AWS(t, "sns", "publish", "--topic-arn", arn, "--message", "to lambda", "--message-attributes", `{"a":{"DataType":"String","StringValue":"b"}}`)
	var ev struct {
		Records []struct {
			EventSource, EventVersion, EventSubscriptionArn string
			Sns                                             map[string]any
		}
	}
	if err := json.Unmarshal(fl.wait(t, 1)[0], &ev); err != nil {
		t.Fatal(err)
	}
	r := ev.Records[0]
	if r.EventSource != "aws:sns" || r.EventVersion != "1.0" || r.EventSubscriptionArn != sub {
		t.Fatalf("record %+v", r)
	}
	for _, k := range []string{"Type", "MessageId", "TopicArn", "Subject", "Message", "Timestamp", "SignatureVersion", "Signature", "SigningCertUrl", "UnsubscribeUrl", "MessageAttributes"} {
		if _, ok := r.Sns[k]; !ok {
			t.Fatalf("Sns record lacks %s: %v", k, r.Sns)
		}
	}
	if r.Sns["Subject"] != nil || r.Sns["Message"] != "to lambda" || r.Sns["MessageAttributes"].(map[string]any)["a"].(map[string]any)["Value"] != "b" {
		t.Fatalf("Sns record %v", r.Sns)
	}

	// A failing function sends the message to the subscription's dead-letter queue.
	dlqURL := h.AWSJSON(t, "sqs", "create-queue", "--queue-name", "sns-dlq")["QueueUrl"].(string)
	broken := "arn:aws:lambda:us-east-1:" + h.Env.AccountID + ":function:broken"
	h.AWS(t, "sns", "subscribe", "--topic-arn", arn, "--protocol", "lambda", "--notification-endpoint", broken,
		"--attributes", `{"RedrivePolicy":"{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-1:`+h.Env.AccountID+`:sns-dlq\"}"}`)
	h.AWS(t, "sns", "publish", "--topic-arn", arn, "--message", "doomed")
	for i := 0; i < 50; i++ {
		out := h.AWSJSON(t, "sqs", "receive-message", "--queue-url", dlqURL, "--message-attribute-names", "All")
		if msgs, ok := out["Messages"].([]any); ok {
			m := msgs[0].(map[string]any)
			if !strings.Contains(m["Body"].(string), "doomed") || m["MessageAttributes"].(map[string]any)["ErrorMessage"] == nil {
				t.Fatalf("dead-lettered message %v", m)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("failed delivery was not dead-lettered")
}

func TestIAMDenied(t *testing.T) {
	h, _, _ := harness(t)
	arn := h.AWSJSON(t, "sns", "create-topic", "--name", "private")["TopicArn"].(string)
	akid, secret := h.User(t, "queues-only", "SQSFullAccess")
	out, err := h.AWSAs(t, akid, secret, "", "sns", "publish", "--topic-arn", arn, "--message", "x")
	if err == nil || !strings.Contains(out, "AuthorizationError") {
		t.Fatalf("publish without permission: %v %s", err, out)
	}
	// Subscribing a queue needs sqs:SendMessage on it as well as sns:Subscribe.
	h.AWS(t, "sqs", "create-queue", "--queue-name", "mine")
	akid2, secret2 := h.User(t, "topics-only", "SNSFullAccess")
	out, err = h.AWSAs(t, akid2, secret2, "", "sns", "subscribe", "--topic-arn", arn, "--protocol", "sqs",
		"--notification-endpoint", "arn:aws:sqs:us-east-1:"+h.Env.AccountID+":mine")
	if err == nil || !strings.Contains(out, "AuthorizationError") {
		t.Fatalf("subscribe a queue without sqs:SendMessage: %v %s", err, out)
	}
}

// TestNativeAPI checks the native routes and their interplay with the AWS API.
func TestNativeAPI(t *testing.T) {
	h, _, _ := harness(t)
	h.Native(t, "POST", "/api/v1/sns/topics", map[string]any{"name": "native", "display_name": "N"})
	h.Native(t, "POST", "/api/v1/sqs/queues", map[string]any{"name": "nq"})
	var sub Subscription
	if err := json.Unmarshal(h.Native(t, "POST", "/api/v1/sns/topics/native/subscriptions", map[string]any{
		"protocol": "sqs", "endpoint": "nq", "raw_message_delivery": true, "filter_policy": map[string]any{"level": []string{"high"}}}), &sub); err != nil {
		t.Fatal(err)
	}
	if sub.Status != "Confirmed" || !strings.HasSuffix(sub.Endpoint, ":nq") {
		t.Fatalf("native subscribe %+v", sub)
	}
	h.Native(t, "POST", "/api/v1/sns/topics/native/publish", map[string]any{"message": "low", "message_attributes": map[string]any{"level": map[string]any{"data_type": "String", "string_value": "low"}}})
	h.Native(t, "POST", "/api/v1/sns/topics/native/publish", map[string]any{"message": "high", "message_attributes": map[string]any{"level": map[string]any{"data_type": "String", "string_value": "high"}}})
	msgs := h.AWSJSON(t, "sqs", "receive-message", "--queue-url", h.URL+"/"+h.Env.AccountID+"/nq", "--max-number-of-messages", "10")["Messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["Body"] != "high" {
		t.Fatalf("native filtered delivery %v", msgs)
	}
	// The console clears a filter policy with {}.
	h.Native(t, "PATCH", "/api/v1/sns/subscriptions/"+sub.ARN, map[string]any{"filter_policy": map[string]any{}})
	sa := h.AWSJSON(t, "sns", "get-subscription-attributes", "--subscription-arn", sub.ARN)["Attributes"].(map[string]any)
	if _, ok := sa["FilterPolicy"]; ok {
		t.Fatalf("filter policy not cleared: %v", sa)
	}
	var topics []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/sns/topics", nil), &topics)
	if len(topics) != 1 || topics[0]["subscriptions"] != 1.0 || topics[0]["messages_published"] != 2.0 {
		t.Fatalf("native topics %v", topics)
	}
	if strings.Contains(string(h.Native(t, "GET", "/api/v1/sns/subscriptions", nil)), "confirmation_token") {
		t.Fatal("native API leaks confirmation tokens")
	}
	// ... and sends raw_message_delivery=false for every protocol.
	h.Native(t, "POST", "/api/v1/sns/topics/native/subscriptions", map[string]any{"protocol": "lambda", "endpoint": "fn"})
	var subs []Subscription
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/sns/subscriptions?topic=native", nil), &subs)
	for _, x := range subs {
		h.Native(t, "PATCH", "/api/v1/sns/subscriptions/"+x.ARN, map[string]any{"raw_message_delivery": false, "filter_policy": map[string]any{"a": []string{"b"}}})
	}
	h.Native(t, "DELETE", "/api/v1/sns/subscriptions/"+sub.ARN, nil)
	h.Native(t, "DELETE", "/api/v1/sns/topics/native", nil)
}
