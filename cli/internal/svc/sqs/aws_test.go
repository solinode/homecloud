package sqs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

func nativeGet(t *testing.T, h *awstest.Harness, path string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(h.Native(t, "GET", path, nil), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func awsHarness(t *testing.T) (*awstest.Harness, *Service) {
	t.Helper()
	h := awstest.New(t)
	s := New(h.Env)
	s.Routes(h.Router)
	s.RegisterAWS()
	return h, s
}

// TestMD5OfMessageAttributes checks the attribute digest against values
// produced by AWS.
func TestMD5OfMessageAttributes(t *testing.T) {
	got := md5OfAttributes(map[string]MessageAttribute{"timestamp": {DataType: "Number", StringValue: "1493147359900"}})
	if got != "235c5c510d26fb653d073faed50ae77c" {
		t.Fatalf("md5 %s", got)
	}
}

// pyHelpers are shared by the boto3 scripts: an independent implementation of
// the AWS message attribute digest, and a signed awsQuery request.
const pyHelpers = `
import hashlib, struct, os, time, threading, urllib.request, urllib.parse, urllib.error, base64
import xml.etree.ElementTree as ET
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest

def md5_attrs(attrs):
    buf = b""
    def enc(b):
        return struct.pack("!I", len(b)) + b
    for name in sorted(attrs):
        a = attrs[name]
        buf += enc(name.encode()) + enc(a["DataType"].encode())
        if "BinaryValue" in a:
            buf += b"\x02" + enc(a["BinaryValue"])
        else:
            buf += b"\x01" + enc(a["StringValue"].encode())
    return hashlib.md5(buf).hexdigest()

def md5(s):
    return hashlib.md5(s.encode()).hexdigest()

def query(url, params, service="sqs"):
    creds = boto3.Session().get_credentials()
    body = urllib.parse.urlencode(params)
    req = AWSRequest(method="POST", url=url, data=body, headers={"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"})
    SigV4Auth(creds, service, "us-east-1").add_auth(req)
    r = urllib.request.Request(url, data=body.encode(), headers=dict(req.headers), method="POST")
    try:
        resp = urllib.request.urlopen(r)
        return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

NS = {"q": "http://queue.amazonaws.com/doc/2012-11-05/"}
sqs = boto3.client("sqs")
`

func TestAWSCLI(t *testing.T) {
	h, _ := awsHarness(t)
	out := h.AWSJSON(t, "sqs", "create-queue", "--queue-name", "jobs", "--attributes", "VisibilityTimeout=45,DelaySeconds=0", "--tags", "team=core")
	url, _ := out["QueueUrl"].(string)
	if url != h.URL+"/"+h.Env.AccountID+"/jobs" {
		t.Fatalf("queue url %q", url)
	}
	// Idempotent create with the same attributes; a conflict with different ones.
	h.AWS(t, "sqs", "create-queue", "--queue-name", "jobs", "--attributes", "VisibilityTimeout=45")
	if o, err := h.AWSErr(t, "sqs", "create-queue", "--queue-name", "jobs", "--attributes", "VisibilityTimeout=10"); err == nil || !strings.Contains(o, "QueueAlreadyExists") {
		t.Fatalf("conflicting create: %v %s", err, o)
	}
	if got := h.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "jobs")["QueueUrl"]; got != url {
		t.Fatalf("get-queue-url %v", got)
	}
	attrs := h.AWSJSON(t, "sqs", "get-queue-attributes", "--queue-url", url, "--attribute-names", "All")["Attributes"].(map[string]any)
	if attrs["VisibilityTimeout"] != "45" || attrs["QueueArn"] != "arn:aws:sqs:us-east-1:"+h.Env.AccountID+":jobs" || attrs["SqsManagedSseEnabled"] != "true" {
		t.Fatalf("attributes %v", attrs)
	}
	sent := h.AWSJSON(t, "sqs", "send-message", "--queue-url", url, "--message-body", "hello",
		"--message-attributes", `{"n":{"DataType":"Number","StringValue":"42"},"s":{"DataType":"String","StringValue":"x"}}`)
	if sent["MD5OfMessageBody"] != "5d41402abc4b2a76b9719d911017c592" || sent["MD5OfMessageAttributes"] == nil {
		t.Fatalf("send %v", sent)
	}
	got := h.AWSJSON(t, "sqs", "receive-message", "--queue-url", url, "--attribute-names", "All", "--message-attribute-names", "All")
	msgs := got["Messages"].([]any)
	m := msgs[0].(map[string]any)
	if m["Body"] != "hello" || m["MD5OfMessageAttributes"] != sent["MD5OfMessageAttributes"] || m["Attributes"].(map[string]any)["ApproximateReceiveCount"] != "1" {
		t.Fatalf("receive %v", m)
	}
	h.AWS(t, "sqs", "delete-message", "--queue-url", url, "--receipt-handle", m["ReceiptHandle"].(string))
	if tags := h.AWSJSON(t, "sqs", "list-queue-tags", "--queue-url", url)["Tags"].(map[string]any); tags["team"] != "core" {
		t.Fatalf("tags %v", tags)
	}
	h.AWS(t, "sqs", "tag-queue", "--queue-url", url, "--tags", "env=dev")
	h.AWS(t, "sqs", "untag-queue", "--queue-url", url, "--tag-keys", "team")
	if tags := h.AWSJSON(t, "sqs", "list-queue-tags", "--queue-url", url)["Tags"].(map[string]any); tags["env"] != "dev" || tags["team"] != nil {
		t.Fatalf("tags %v", tags)
	}
	if l := h.AWSJSON(t, "sqs", "list-queues", "--queue-name-prefix", "jo")["QueueUrls"].([]any); len(l) != 1 {
		t.Fatalf("list %v", l)
	}
	h.AWS(t, "sqs", "purge-queue", "--queue-url", url)
	if o, err := h.AWSErr(t, "sqs", "purge-queue", "--queue-url", url); err == nil || !strings.Contains(o, "PurgeQueueInProgress") {
		t.Fatalf("second purge: %v %s", err, o)
	}
	h.AWS(t, "sqs", "delete-queue", "--queue-url", url)
	if o, err := h.AWSErr(t, "sqs", "get-queue-url", "--queue-name", "jobs"); err == nil || !strings.Contains(o, "NonExistentQueue") {
		t.Fatalf("deleted queue: %v %s", err, o)
	}
	// The native API sees queues created over the AWS API.
	h.AWS(t, "sqs", "create-queue", "--queue-name", "native")
	if v := nativeGet(t, h, "/api/v1/sqs/queues/native"); v["name"] != "native" {
		t.Fatalf("native get: %v", v)
	}
}

func TestBoto3(t *testing.T) {
	h, _ := awsHarness(t)
	h.Python(t, pyHelpers+`
url = sqs.create_queue(QueueName="work", Attributes={"ReceiveMessageWaitTimeSeconds": "0"})["QueueUrl"]
attrs = {
    "s": {"DataType": "String", "StringValue": "héllo"},
    "n": {"DataType": "Number", "StringValue": "3.14"},
    "b": {"DataType": "Binary", "BinaryValue": b"\x00\x01\xff"},
    "c": {"DataType": "String.custom", "StringValue": "v"},
    "app.x": {"DataType": "String", "StringValue": "1"},
}
r = sqs.send_message(QueueUrl=url, MessageBody="body ✓", MessageAttributes=attrs,
                     MessageSystemAttributes={"AWSTraceHeader": {"DataType": "String", "StringValue": "Root=1-abc"}})
assert r["MD5OfMessageBody"] == md5("body ✓"), r
assert r["MD5OfMessageAttributes"] == md5_attrs(attrs), (r, md5_attrs(attrs))
assert "MD5OfMessageSystemAttributes" in r

got = sqs.receive_message(QueueUrl=url, MessageAttributeNames=["All"], MessageSystemAttributeNames=["All"])["Messages"][0]
assert got["Body"] == "body ✓"
assert got["MessageAttributes"]["b"]["BinaryValue"] == b"\x00\x01\xff"
assert got["MD5OfMessageAttributes"] == md5_attrs(attrs)
a = got["Attributes"]
for k in ["SentTimestamp", "ApproximateReceiveCount", "ApproximateFirstReceiveTimestamp", "SenderId", "AWSTraceHeader"]:
    assert k in a, a
sqs.change_message_visibility(QueueUrl=url, ReceiptHandle=got["ReceiptHandle"], VisibilityTimeout=0)

# Attribute name filters: prefix.* and exact names; the digest covers the returned subset.
got = sqs.receive_message(QueueUrl=url, MessageAttributeNames=["app.*", "s"], AttributeNames=["ApproximateReceiveCount"])["Messages"][0]
assert set(got["MessageAttributes"]) == {"app.x", "s"}, got["MessageAttributes"]
assert got["MD5OfMessageAttributes"] == md5_attrs({k: attrs[k] for k in ["app.x", "s"]})
assert got["Attributes"] == {"ApproximateReceiveCount": "2"}, got["Attributes"]
sqs.delete_message(QueueUrl=url, ReceiptHandle=got["ReceiptHandle"])
# Deleting again with the stale handle succeeds, as in AWS; a garbage handle does not.
sqs.delete_message(QueueUrl=url, ReceiptHandle=got["ReceiptHandle"])
try:
    sqs.delete_message(QueueUrl=url, ReceiptHandle="not a handle")
    raise SystemExit("garbage receipt accepted")
except sqs.exceptions.ReceiptHandleIsInvalid:
    pass

# Batches with partial failures.
r = sqs.send_message_batch(QueueUrl=url, Entries=[
    {"Id": "ok1", "MessageBody": "one"},
    {"Id": "bad", "MessageBody": "x", "MessageAttributes": {"n": {"DataType": "Number", "StringValue": "abc"}}},
    {"Id": "ok2", "MessageBody": "two", "DelaySeconds": 0},
    {"Id": "badchars", "MessageBody": "\x01"},
])
assert sorted(e["Id"] for e in r["Successful"]) == ["ok1", "ok2"], r
assert sorted(e["Id"] for e in r["Failed"]) == ["bad", "badchars"], r
codes = {e["Id"]: e["Code"] for e in r["Failed"]}
assert codes["badchars"] == "InvalidMessageContents" and codes["bad"] == "InvalidParameterValue", codes
assert all(e["SenderFault"] for e in r["Failed"])
for e in r["Successful"]:
    assert e["MD5OfMessageBody"] in (md5("one"), md5("two"))
try:
    sqs.send_message_batch(QueueUrl=url, Entries=[{"Id": "a", "MessageBody": "1"}, {"Id": "a", "MessageBody": "2"}])
    raise SystemExit("duplicate ids accepted")
except sqs.exceptions.BatchEntryIdsNotDistinct:
    pass
try:
    sqs.send_message_batch(QueueUrl=url, Entries=[{"Id": str(i), "MessageBody": "x"} for i in range(11)])
    raise SystemExit("11 entries accepted")
except sqs.exceptions.TooManyEntriesInBatchRequest:
    pass
msgs = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10)["Messages"]
assert len(msgs) == 2, msgs
r = sqs.change_message_visibility_batch(QueueUrl=url, Entries=[{"Id": "v", "ReceiptHandle": msgs[0]["ReceiptHandle"], "VisibilityTimeout": 60}])
assert [e["Id"] for e in r["Successful"]] == ["v"], r
r = sqs.delete_message_batch(QueueUrl=url, Entries=[{"Id": "d1", "ReceiptHandle": msgs[0]["ReceiptHandle"]},
                                                     {"Id": "d2", "ReceiptHandle": msgs[1]["ReceiptHandle"]},
                                                     {"Id": "d3", "ReceiptHandle": "garbage!"}])
assert sorted(e["Id"] for e in r["Successful"]) == ["d1", "d2"] and r["Failed"][0]["Code"] == "ReceiptHandleIsInvalid", r
attrs = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible"])["Attributes"]
assert attrs == {"ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "0"}, attrs

# Delayed messages.
sqs.send_message(QueueUrl=url, MessageBody="later", DelaySeconds=1)
attrs = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["ApproximateNumberOfMessagesDelayed"])["Attributes"]
assert attrs["ApproximateNumberOfMessagesDelayed"] == "1", attrs
assert "Messages" not in sqs.receive_message(QueueUrl=url)
m = sqs.receive_message(QueueUrl=url, WaitTimeSeconds=3)["Messages"][0]
assert m["Body"] == "later"

# Long polling: a receive parks without blocking other requests and wakes on send.
def later():
    time.sleep(1)
    boto3.client("sqs").send_message(QueueUrl=url, MessageBody="wake")
threading.Thread(target=later).start()
start = time.time()
r = sqs.receive_message(QueueUrl=url, WaitTimeSeconds=10)
took = time.time() - start
assert r["Messages"][0]["Body"] == "wake" and 0.8 < took < 5, (r, took)
start = time.time()
r = sqs.receive_message(QueueUrl=url, WaitTimeSeconds=2)
assert "Messages" not in r and 1.8 < time.time() - start < 4

# Errors map to modeled exceptions.
try:
    sqs.get_queue_url(QueueName="nope")
    raise SystemExit("missing queue found")
except sqs.exceptions.QueueDoesNotExist as e:
    assert e.response["Error"]["Code"] == "AWS.SimpleQueueService.NonExistentQueue", e.response
try:
    sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=11)
    raise SystemExit("MaxNumberOfMessages=11 accepted")
except botocore.exceptions.ClientError as e:
    assert e.response["Error"]["Code"] == "InvalidParameterValue", e.response
try:
    sqs.set_queue_attributes(QueueUrl=url, Attributes={"Bogus": "1"})
    raise SystemExit("unknown attribute accepted")
except sqs.exceptions.InvalidAttributeName:
    pass
try:
    sqs.set_queue_attributes(QueueUrl=url, Attributes={"VisibilityTimeout": "999999"})
    raise SystemExit("bad attribute value accepted")
except sqs.exceptions.InvalidAttributeValue:
    pass

# Pagination.
for i in range(5):
    sqs.create_queue(QueueName="page-%d" % i)
p = sqs.get_paginator("list_queues")
urls = [u for page in p.paginate(QueueNamePrefix="page-", PaginationConfig={"PageSize": 2}) for u in page.get("QueueUrls", [])]
assert len(urls) == 5 and urls[0].endswith("/page-0"), urls
`)
}

func TestBoto3FIFO(t *testing.T) {
	h, _ := awsHarness(t)
	h.Python(t, pyHelpers+`
url = sqs.create_queue(QueueName="orders.fifo", Attributes={"FifoQueue": "true", "ContentBasedDeduplication": "true"})["QueueUrl"]
try:
    sqs.create_queue(QueueName="bad.fifo")
    raise SystemExit("fifo name without FifoQueue accepted")
except botocore.exceptions.ClientError as e:
    assert e.response["Error"]["Code"] == "InvalidParameterValue", e.response
a = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["All"])["Attributes"]
assert a["FifoQueue"] == "true" and a["DeduplicationScope"] == "queue" and a["FifoThroughputLimit"] == "perQueue", a

seqs = []
for body, group in [("a1", "A"), ("b1", "B"), ("a2", "A"), ("a1", "A"), ("b2", "B")]:
    r = sqs.send_message(QueueUrl=url, MessageBody=body, MessageGroupId=group)
    seqs.append(r["SequenceNumber"])
assert seqs[3] == seqs[0], seqs  # duplicate within the window: same sequence number, not enqueued
assert seqs[0] < seqs[1] < seqs[2] < seqs[4], seqs
r = sqs.send_message(QueueUrl=url, MessageBody="x", MessageGroupId="C", MessageDeduplicationId="d1")
r2 = sqs.send_message(QueueUrl=url, MessageBody="y", MessageGroupId="C", MessageDeduplicationId="d1")
assert r["MessageId"] == r2["MessageId"]
try:
    sqs.send_message(QueueUrl=url, MessageBody="x")
    raise SystemExit("missing group accepted")
except botocore.exceptions.ClientError as e:
    assert e.response["Error"]["Code"] == "MissingParameter", e.response
try:
    sqs.send_message(QueueUrl=url, MessageBody="x", MessageGroupId="A", DelaySeconds=5)
    raise SystemExit("per-message delay accepted on FIFO")
except botocore.exceptions.ClientError as e:
    assert e.response["Error"]["Code"] == "InvalidParameterValue", e.response

# Order within a group; a group is blocked while it has messages in flight.
got = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, AttributeNames=["All"], ReceiveRequestAttemptId="try1")["Messages"]
bodies = [m["Body"] for m in got]
assert bodies == ["a1", "b1", "a2", "b2", "x"], bodies
assert got[0]["Attributes"]["MessageGroupId"] == "A" and "SequenceNumber" in got[0]["Attributes"]
# Retrying with the same attempt id returns the same batch (same receipts).
again = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, ReceiveRequestAttemptId="try1")["Messages"]
assert [m["ReceiptHandle"] for m in again] == [m["ReceiptHandle"] for m in got]
sqs.send_message(QueueUrl=url, MessageBody="a3", MessageGroupId="A")
sqs.send_message(QueueUrl=url, MessageBody="d1", MessageGroupId="D")
got2 = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10)["Messages"]
assert [m["Body"] for m in got2] == ["d1"], got2  # A is blocked by in-flight a1/a2
for m in got:
    if m["Body"].startswith("a"):
        sqs.delete_message(QueueUrl=url, ReceiptHandle=m["ReceiptHandle"])
got3 = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10)["Messages"]
assert [m["Body"] for m in got3] == ["a3"], got3
# Deleting with an expired receipt on a FIFO queue fails.
sqs.change_message_visibility(QueueUrl=url, ReceiptHandle=got3[0]["ReceiptHandle"], VisibilityTimeout=0)
sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10)
try:
    sqs.delete_message(QueueUrl=url, ReceiptHandle=got3[0]["ReceiptHandle"])
    raise SystemExit("stale FIFO receipt accepted")
except sqs.exceptions.ReceiptHandleIsInvalid:
    pass

# Message-group dedup scope.
u2 = sqs.create_queue(QueueName="g.fifo", Attributes={"FifoQueue": "true", "DeduplicationScope": "messageGroup", "FifoThroughputLimit": "perMessageGroupId"})["QueueUrl"]
sqs.send_message(QueueUrl=u2, MessageBody="m", MessageGroupId="1", MessageDeduplicationId="same")
sqs.send_message(QueueUrl=u2, MessageBody="m", MessageGroupId="2", MessageDeduplicationId="same")
assert len(sqs.receive_message(QueueUrl=u2, MaxNumberOfMessages=10)["Messages"]) == 2
`)
}

func TestBoto3DeadLetterQueue(t *testing.T) {
	h, _ := awsHarness(t)
	h.Python(t, pyHelpers+`
dlq = sqs.create_queue(QueueName="dlq", Attributes={"RedriveAllowPolicy": json.dumps({"redrivePermission": "allowAll"})})["QueueUrl"]
dlq_arn = sqs.get_queue_attributes(QueueUrl=dlq, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
src = sqs.create_queue(QueueName="src", Attributes={"RedrivePolicy": json.dumps({"deadLetterTargetArn": dlq_arn, "maxReceiveCount": "2"})})["QueueUrl"]
rp = json.loads(sqs.get_queue_attributes(QueueUrl=src, AttributeNames=["RedrivePolicy"])["Attributes"]["RedrivePolicy"])
assert rp == {"deadLetterTargetArn": dlq_arn, "maxReceiveCount": 2}, rp
assert sqs.list_dead_letter_source_queues(QueueUrl=dlq)["queueUrls"] == [src]

sqs.send_message(QueueUrl=src, MessageBody="poison")
for i in range(2):
    assert len(sqs.receive_message(QueueUrl=src, VisibilityTimeout=0)["Messages"]) == 1
assert "Messages" not in sqs.receive_message(QueueUrl=src, VisibilityTimeout=0)
m = sqs.receive_message(QueueUrl=dlq, AttributeNames=["All"], VisibilityTimeout=0)["Messages"][0]
assert m["Body"] == "poison" and m["Attributes"]["DeadLetterQueueSourceArn"].endswith(":src"), m

# Redrive back to the source with a message move task.
task = sqs.start_message_move_task(SourceArn=dlq_arn)["TaskHandle"]
for i in range(50):
    t = sqs.list_message_move_tasks(SourceArn=dlq_arn)["Results"][0]
    if t["Status"] == "COMPLETED":
        break
    time.sleep(0.1)
assert t["Status"] == "COMPLETED" and t["ApproximateNumberOfMessagesMoved"] == 1, t
m = sqs.receive_message(QueueUrl=src, AttributeNames=["All"])["Messages"][0]
assert m["Body"] == "poison" and m["Attributes"]["ApproximateReceiveCount"] == "1", m
try:
    sqs.start_message_move_task(SourceArn=sqs.get_queue_attributes(QueueUrl=src, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"])
    raise SystemExit("move task from a non-DLQ accepted")
except botocore.exceptions.ClientError as e:
    assert e.response["Error"]["Code"] == "InvalidParameterValue", e.response

# A DLQ that denies redrive cannot be targeted.
deny = sqs.create_queue(QueueName="deny", Attributes={"RedriveAllowPolicy": json.dumps({"redrivePermission": "denyAll"})})["QueueUrl"]
deny_arn = sqs.get_queue_attributes(QueueUrl=deny, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
try:
    sqs.create_queue(QueueName="src2", Attributes={"RedrivePolicy": json.dumps({"deadLetterTargetArn": deny_arn, "maxReceiveCount": 1})})
    raise SystemExit("redrive to a denyAll DLQ accepted")
except sqs.exceptions.InvalidAttributeValue:
    pass
# Removing the redrive policy.
sqs.set_queue_attributes(QueueUrl=src, Attributes={"RedrivePolicy": ""})
assert "RedrivePolicy" not in sqs.get_queue_attributes(QueueUrl=src, AttributeNames=["All"])["Attributes"]

# Permissions edit the queue policy.
sqs.add_permission(QueueUrl=src, Label="p1", AWSAccountIds=["111122223333"], Actions=["SendMessage"])
pol = json.loads(sqs.get_queue_attributes(QueueUrl=src, AttributeNames=["Policy"])["Attributes"]["Policy"])
assert pol["Statement"][0]["Sid"] == "p1" and pol["Statement"][0]["Action"] == ["SQS:SendMessage"], pol
sqs.remove_permission(QueueUrl=src, Label="p1")
assert "Policy" not in sqs.get_queue_attributes(QueueUrl=src, AttributeNames=["All"])["Attributes"]
`)
}

// TestQueryProtocol drives the awsQuery dialect (older SDKs), including
// requests sent to the queue URL itself.
func TestQueryProtocol(t *testing.T) {
	h, _ := awsHarness(t)
	h.Python(t, pyHelpers+`
ep = os.environ["AWS_ENDPOINT_URL"]
st, body = query(ep + "/", {"Action": "CreateQueue", "Version": "2012-11-05", "QueueName": "qq",
    "Attribute.1.Name": "VisibilityTimeout", "Attribute.1.Value": "40", "Tag.1.Key": "k", "Tag.1.Value": "v"})
assert st == 200, body
url = ET.fromstring(body).find("q:CreateQueueResult/q:QueueUrl", NS).text
st, body = query(url, {"Action": "SendMessage", "Version": "2012-11-05", "MessageBody": "hi <&>",
    "MessageAttribute.1.Name": "n", "MessageAttribute.1.Value.DataType": "Number", "MessageAttribute.1.Value.StringValue": "7",
    "MessageAttribute.2.Name": "b", "MessageAttribute.2.Value.DataType": "Binary", "MessageAttribute.2.Value.BinaryValue": base64.b64encode(b"\x01\x02").decode()})
assert st == 200, body
res = ET.fromstring(body).find("q:SendMessageResult", NS)
exp = md5_attrs({"n": {"DataType": "Number", "StringValue": "7"}, "b": {"DataType": "Binary", "BinaryValue": b"\x01\x02"}})
assert res.find("q:MD5OfMessageAttributes", NS).text == exp, body
assert res.find("q:MD5OfMessageBody", NS).text == md5("hi <&>")
st, body = query(url, {"Action": "ReceiveMessage", "AttributeName.1": "All", "MessageAttributeName.1": "All", "MaxNumberOfMessages": "5"})
assert st == 200, body
msg = ET.fromstring(body).find("q:ReceiveMessageResult/q:Message", NS)
assert msg.find("q:Body", NS).text == "hi <&>", body
attrs = {a.find("q:Name", NS).text: a.find("q:Value", NS).text for a in msg.findall("q:Attribute", NS)}
assert attrs["ApproximateReceiveCount"] == "1", attrs
mattrs = {a.find("q:Name", NS).text: a.find("q:Value", NS) for a in msg.findall("q:MessageAttribute", NS)}
assert base64.b64decode(mattrs["b"].find("q:BinaryValue", NS).text) == b"\x01\x02", body
assert msg.find("q:MD5OfMessageAttributes", NS).text == exp
st, body = query(ep, {"Action": "DeleteMessage", "QueueUrl": url, "ReceiptHandle": msg.find("q:ReceiptHandle", NS).text})
assert st == 200 and "DeleteMessageResponse" in body, body
st, body = query(url, {"Action": "GetQueueAttributes", "AttributeName.1": "VisibilityTimeout", "AttributeName.2": "QueueArn"})
attrs = {a.find("q:Name", NS).text: a.find("q:Value", NS).text for a in ET.fromstring(body).findall("q:GetQueueAttributesResult/q:Attribute", NS)}
assert attrs["VisibilityTimeout"] == "40" and attrs["QueueArn"].endswith(":qq"), body
st, body = query(ep, {"Action": "SendMessageBatch", "QueueUrl": url,
    "SendMessageBatchRequestEntry.1.Id": "a", "SendMessageBatchRequestEntry.1.MessageBody": "one",
    "SendMessageBatchRequestEntry.2.Id": "b", "SendMessageBatchRequestEntry.2.MessageBody": "two", "SendMessageBatchRequestEntry.2.DelaySeconds": "9999"})
r = ET.fromstring(body).find("q:SendMessageBatchResult", NS)
assert [e.find("q:Id", NS).text for e in r.findall("q:SendMessageBatchResultEntry", NS)] == ["a"], body
assert [e.find("q:Code", NS).text for e in r.findall("q:BatchResultErrorEntry", NS)] == ["InvalidParameterValue"], body
st, body = query(ep, {"Action": "ListQueues", "QueueNamePrefix": "q"})
assert [u.text for u in ET.fromstring(body).findall("q:ListQueuesResult/q:QueueUrl", NS)] == [url], body
st, body = query(url, {"Action": "ListQueueTags"})
assert ET.fromstring(body).find("q:ListQueueTagsResult/q:Tag/q:Key", NS).text == "k", body
st, body = query(ep, {"Action": "GetQueueUrl", "QueueName": "missing"})
err = ET.fromstring(body).find("q:Error", NS)
assert st == 400 and err.find("q:Code", NS).text == "AWS.SimpleQueueService.NonExistentQueue", body
st, body = query(url, {"Action": "DeleteQueue"})
assert st == 200, body
`)
}

func TestIAMDenied(t *testing.T) {
	h, _ := awsHarness(t)
	url := h.AWSJSON(t, "sqs", "create-queue", "--queue-name", "locked")["QueueUrl"].(string)
	akid, secret := h.User(t, "reader", "ReadOnlyAccess")
	if out, err := h.AWSAs(t, akid, secret, "", "sqs", "get-queue-url", "--queue-name", "locked"); err != nil {
		t.Fatalf("reader get-queue-url: %v %s", err, out)
	}
	out, err := h.AWSAs(t, akid, secret, "", "sqs", "send-message", "--queue-url", url, "--message-body", "x")
	if err == nil || !strings.Contains(out, "AccessDenied") || !strings.Contains(out, "sqs:SendMessage") {
		t.Fatalf("send without permission: %v %s", err, out)
	}
	found := false
	for _, a := range h.AuditLog() {
		found = found || a == "sqs:SendMessage arn:aws:sqs:us-east-1:"+h.Env.AccountID+":locked"
	}
	if !found {
		t.Fatalf("audit %v", h.AuditLog())
	}
}

// TestNativeAPI checks the native routes still work alongside the AWS API.
func TestNativeAPI(t *testing.T) {
	h, _ := awsHarness(t)
	h.Native(t, "POST", "/api/v1/sqs/queues", map[string]any{"name": "dl"})
	h.Native(t, "POST", "/api/v1/sqs/queues", map[string]any{"name": "n", "visibility_timeout": 5,
		"redrive_policy": map[string]any{"dead_letter_queue": "dl", "max_receive_count": 3}})
	h.Native(t, "POST", "/api/v1/sqs/queues/n/messages", map[string]any{"body": "native", "message_attributes": map[string]any{"k": map[string]any{"data_type": "String", "string_value": "v"}}})
	got := h.AWSJSON(t, "sqs", "receive-message", "--queue-url", h.URL+"/"+h.Env.AccountID+"/n", "--message-attribute-names", "All")
	m := got["Messages"].([]any)[0].(map[string]any)
	if m["Body"] != "native" || m["MessageAttributes"].(map[string]any)["k"].(map[string]any)["StringValue"] != "v" {
		t.Fatalf("receive %v", m)
	}
	if v := nativeGet(t, h, "/api/v1/sqs/queues/n"); v["approximate_number_of_messages_not_visible"] != 1.0 {
		t.Fatalf("native view %v", v)
	}
	h.Native(t, "POST", "/api/v1/sqs/queues/n/messages/delete", map[string]any{"receipt_handle": m["ReceiptHandle"]})
	if v := nativeGet(t, h, "/api/v1/sqs/queues/dl"); fmt.Sprint(v["dead_letter_source_queues"]) != "[n]" {
		t.Fatalf("native dlq view %v", v)
	}
}
