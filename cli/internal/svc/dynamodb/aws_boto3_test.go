package dynamodb

import (
	"strings"
	"testing"
	"time"
)

const botoPrelude = `
from decimal import Decimal
from boto3.dynamodb.conditions import Key, Attr
from botocore.exceptions import ClientError
ddb = boto3.resource("dynamodb")
client = boto3.client("dynamodb")

def expect_error(code, fn, contains=None):
    try:
        fn()
    except ClientError as e:
        got = e.response["Error"]["Code"]
        msg = e.response["Error"].get("Message", "")
        assert got == code, "expected %s, got %s: %s" % (code, got, msg)
        if contains:
            assert contains in msg, "message %r lacks %r" % (msg, contains)
        return e
    raise AssertionError("expected %s, call succeeded" % code)
`

func TestBoto3Items(t *testing.T) {
	h, _ := harness(t)
	out := h.Python(t, botoPrelude+`
t = ddb.create_table(
    TableName="orders",
    KeySchema=[{"AttributeName": "pk", "KeyType": "HASH"}, {"AttributeName": "sk", "KeyType": "RANGE"}],
    AttributeDefinitions=[{"AttributeName": "pk", "AttributeType": "S"}, {"AttributeName": "sk", "AttributeType": "N"},
                          {"AttributeName": "status", "AttributeType": "S"}, {"AttributeName": "total", "AttributeType": "N"},
                          {"AttributeName": "created", "AttributeType": "S"}],
    GlobalSecondaryIndexes=[{"IndexName": "by_status", "KeySchema": [{"AttributeName": "status", "KeyType": "HASH"}, {"AttributeName": "total", "KeyType": "RANGE"}],
                             "Projection": {"ProjectionType": "INCLUDE", "NonKeyAttributes": ["note"]}}],
    LocalSecondaryIndexes=[{"IndexName": "by_created", "KeySchema": [{"AttributeName": "pk", "KeyType": "HASH"}, {"AttributeName": "created", "KeyType": "RANGE"}],
                            "Projection": {"ProjectionType": "KEYS_ONLY"}}],
    BillingMode="PAY_PER_REQUEST")
t.wait_until_exists()
assert t.table_status == "ACTIVE"

# batch_writer splits into 25-item BatchWriteItem calls.
with t.batch_writer() as bw:
    for i in range(60):
        pk = "cust%d" % (i % 3)
        it = {"pk": pk, "sk": i, "total": Decimal(i) * Decimal("1.25"), "note": "n%d" % i, "tags": {"a", "b%d" % (i % 2)},
              "created": "2024-01-%02d" % (i % 28 + 1), "nested": {"list": [1, "two", {"three": 3}], "flag": True, "none": None}}
        if i % 4 == 0:
            it["status"] = "OPEN"
        bw.put_item(Item=it)
t.reload()
assert t.item_count == 60, t.item_count

# Query with sort key range, descending, and Decimal types.
r = t.query(KeyConditionExpression=Key("pk").eq("cust0") & Key("sk").between(3, 30), ScanIndexForward=False)
sks = [int(x["sk"]) for x in r["Items"]]
assert sks == [30, 27, 24, 21, 18, 15, 12, 9, 6, 3], sks
assert r["Items"][0]["total"] == Decimal("37.5")
assert r["Items"][0]["tags"] == {"a", "b0"}
assert r["Items"][0]["nested"]["list"][2]["three"] == 3 and r["Items"][0]["nested"]["none"] is None

# Limit + pagination with ExclusiveStartKey: filter applies after Limit.
seen, start, pages = [], None, 0
while True:
    kw = dict(KeyConditionExpression=Key("pk").eq("cust1"), Limit=7, FilterExpression=Attr("tags").contains("b1"))
    if start: kw["ExclusiveStartKey"] = start
    r = t.query(**kw)
    pages += 1
    assert r["ScannedCount"] <= 7
    seen += [int(x["sk"]) for x in r["Items"]]
    start = r.get("LastEvaluatedKey")
    if not start: break
assert seen == [i for i in range(60) if i % 3 == 1 and i % 2 == 1], seen
assert pages == 3, pages
expect_error("ValidationException", lambda: t.query(KeyConditionExpression=Key("pk").eq("cust1"), FilterExpression=Attr("sk").gt(0)), "Filter Expression can only contain non-primary key attributes")

# Paginator + Select COUNT.
p = client.get_paginator("query")
total = sum(pg["Count"] for pg in p.paginate(TableName="orders", KeyConditionExpression="pk = :p", ExpressionAttributeValues={":p": {"S": "cust2"}}, Select="COUNT", PaginationConfig={"PageSize": 4}))
assert total == 20, total

# GSI with INCLUDE projection: only keys + note are visible.
r = t.query(IndexName="by_status", KeyConditionExpression=Key("status").eq("OPEN") & Key("total").gte(Decimal("50")))
assert [x["total"] for x in r["Items"]] == [Decimal(i) * Decimal("1.25") for i in range(40, 60, 4)], r["Items"]
assert set(r["Items"][0].keys()) == {"pk", "sk", "status", "total", "note"}, r["Items"][0].keys()
expect_error("ValidationException", lambda: t.query(IndexName="by_status", KeyConditionExpression=Key("status").eq("OPEN"), Select="ALL_ATTRIBUTES"))
expect_error("ValidationException", lambda: t.query(IndexName="by_status", KeyConditionExpression=Key("status").eq("OPEN"), ConsistentRead=True), "Consistent reads are not supported")
# LSI (KEYS_ONLY) can still fetch every attribute.
r = t.query(IndexName="by_created", KeyConditionExpression=Key("pk").eq("cust0") & Key("created").begins_with("2024-01-0"), Select="ALL_ATTRIBUTES")
assert r["Count"] > 0 and "note" in r["Items"][0]
r = t.query(IndexName="by_created", KeyConditionExpression=Key("pk").eq("cust0"))
assert set(r["Items"][0].keys()) == {"pk", "sk", "created"} and r["Count"] == 20

# Scan with filter, projection and parallel segments.
r = t.scan(FilterExpression=Attr("status").not_exists() & Attr("total").lt(10), ProjectionExpression="pk, sk, nested.#l[1]", ExpressionAttributeNames={"#l": "list"})
assert r["Count"] == 6 and r["Items"][0]["nested"] == {"list": ["two"]}, r
allkeys = set()
for seg in range(4):
    for pg in client.get_paginator("scan").paginate(TableName="orders", Segment=seg, TotalSegments=4, PaginationConfig={"PageSize": 5}):
        for it in pg["Items"]:
            k = (it["pk"]["S"], it["sk"]["N"])
            assert k not in allkeys
            allkeys.add(k)
assert len(allkeys) == 60

# UpdateItem: arithmetic, nested paths, list_append, sets, ReturnValues.
r = t.update_item(Key={"pk": "cust0", "sk": 0},
    UpdateExpression="SET total = total + :d, nested.list[1] = :s, nested.more = list_append(if_not_exists(nested.more, :empty), :l), cnt = if_not_exists(cnt, :z) + :one REMOVE note ADD tags :t",
    ExpressionAttributeValues={":d": Decimal("0.005"), ":s": "TWO", ":empty": [], ":l": [1, 2], ":z": 0, ":one": 1, ":t": {"c"}},
    ReturnValues="ALL_NEW")["Attributes"]
assert r["total"] == Decimal("0.005") and r["nested"]["list"][1] == "TWO" and r["nested"]["more"] == [1, 2] and r["cnt"] == 1, r
assert r["tags"] == {"a", "b0", "c"} and "note" not in r, r
r = t.update_item(Key={"pk": "cust0", "sk": 0}, UpdateExpression="DELETE tags :a", ExpressionAttributeValues={":a": {"a", "zz"}}, ReturnValues="UPDATED_NEW")
assert r["Attributes"] == {"tags": {"b0", "c"}}, r
expect_error("ValidationException", lambda: t.update_item(Key={"pk": "cust0", "sk": 0}, UpdateExpression="ADD tags :a DELETE tags :a", ExpressionAttributeValues={":a": {"a"}}), "overlap")
r = t.update_item(Key={"pk": "cust0", "sk": 0}, UpdateExpression="SET cnt = cnt + :one", ExpressionAttributeValues={":one": 1}, ReturnValues="UPDATED_OLD")
assert r["Attributes"] == {"cnt": 1}, r
# Conditional failures, with and without the old item.
e = expect_error("ConditionalCheckFailedException", lambda: t.update_item(Key={"pk": "cust0", "sk": 0}, UpdateExpression="SET cnt = :z",
    ConditionExpression="cnt = :v", ExpressionAttributeValues={":z": 0, ":v": 99}, ReturnValuesOnConditionCheckFailure="ALL_OLD"))
assert e.response["Item"]["cnt"] == {"N": "2"}, e.response
expect_error("ConditionalCheckFailedException", lambda: t.put_item(Item={"pk": "cust0", "sk": 0}, ConditionExpression=Attr("pk").not_exists()))
t.put_item(Item={"pk": "new", "sk": 1, "v": 1}, ConditionExpression=Attr("pk").not_exists())
old = t.delete_item(Key={"pk": "new", "sk": 1}, ReturnValues="ALL_OLD")["Attributes"]
assert old == {"pk": "new", "sk": 1, "v": 1}, old
assert "Item" not in t.get_item(Key={"pk": "new", "sk": 1})
# Update creates the item when missing.
r = t.update_item(Key={"pk": "fresh", "sk": 5}, UpdateExpression="ADD n :one", ExpressionAttributeValues={":one": 1}, ReturnValues="ALL_NEW")
assert r["Attributes"] == {"pk": "fresh", "sk": 5, "n": 1}, r

# Arbitrary precision numbers survive the round trip.
big = Decimal("12345678901234567890.123456789012345678")
t.put_item(Item={"pk": "num", "sk": -1, "big": big, "neg": Decimal("-0.000001")})
got = t.get_item(Key={"pk": "num", "sk": -1}, ConsistentRead=True)["Item"]
assert got["big"] == big and got["neg"] == Decimal("-0.000001"), got
# Binary values and sets.
t.put_item(Item={"pk": "bin", "sk": 0, "b": b"\x00\x01\xff", "bs": {b"a", b"b"}})
got = t.get_item(Key={"pk": "bin", "sk": 0})["Item"]
assert got["b"].value == b"\x00\x01\xff" and {x.value for x in got["bs"]} == {b"a", b"b"}, got

# Validation errors.
expect_error("ValidationException", lambda: t.put_item(Item={"pk": "", "sk": 1}), "empty string")
expect_error("ValidationException", lambda: t.put_item(Item={"pk": "x", "sk": "notanumber"}), "Type mismatch")
expect_error("ValidationException", lambda: t.put_item(Item={"pk": "x"}), "Missing the key sk")
expect_error("ValidationException", lambda: t.put_item(Item={"pk": "x", "sk": 1, "status": 5}), "Index Key")
expect_error("ValidationException", lambda: t.get_item(Key={"pk": "x"}), "does not match the schema")
expect_error("ValidationException", lambda: client.put_item(TableName="orders", Item={"pk": {"S": "x"}, "sk": {"N": "1"}}, ConditionExpression="attribute_exists(pk)", ExpressionAttributeValues={":unused": {"S": "u"}}), "unused in expressions")
expect_error("ValidationException", lambda: t.put_item(Item={"pk": "x", "sk": 1, "big": "x" * (401 * 1024)}), "Item size has exceeded")
expect_error("ValidationException", lambda: t.update_item(Key={"pk": "x", "sk": 1}, UpdateExpression="SET pk = :v", ExpressionAttributeValues={":v": "y"}), "part of the key")
expect_error("ResourceNotFoundException", lambda: ddb.Table("nope").get_item(Key={"pk": "x"}))

# BatchGetItem.
r = ddb.batch_get_item(RequestItems={"orders": {"Keys": [{"pk": "cust0", "sk": 3}, {"pk": "cust1", "sk": 4}, {"pk": "missing", "sk": 0}], "ProjectionExpression": "sk"}})
assert sorted(int(x["sk"]) for x in r["Responses"]["orders"]) == [3, 4] and r["UnprocessedKeys"] == {}, r

# Transactions.
client.transact_write_items(TransactItems=[
    {"Put": {"TableName": "orders", "Item": {"pk": {"S": "tx"}, "sk": {"N": "1"}, "v": {"N": "1"}}, "ConditionExpression": "attribute_not_exists(pk)"}},
    {"Update": {"TableName": "orders", "Key": {"pk": {"S": "cust0"}, "sk": {"N": "3"}}, "UpdateExpression": "SET v = :v", "ExpressionAttributeValues": {":v": {"S": "tx"}}}},
    {"ConditionCheck": {"TableName": "orders", "Key": {"pk": {"S": "cust0"}, "sk": {"N": "6"}}, "ConditionExpression": "attribute_exists(pk)"}},
])
e = expect_error("TransactionCanceledException", lambda: client.transact_write_items(TransactItems=[
    {"Delete": {"TableName": "orders", "Key": {"pk": {"S": "tx"}, "sk": {"N": "1"}}}},
    {"Put": {"TableName": "orders", "Item": {"pk": {"S": "tx"}, "sk": {"N": "2"}}, "ConditionExpression": "attribute_exists(pk)", "ReturnValuesOnConditionCheckFailure": "ALL_OLD"}},
    {"ConditionCheck": {"TableName": "orders", "Key": {"pk": {"S": "cust0"}, "sk": {"N": "3"}}, "ConditionExpression": "v = :v", "ExpressionAttributeValues": {":v": {"S": "other"}}, "ReturnValuesOnConditionCheckFailure": "ALL_OLD"}},
]))
codes = [c["Code"] for c in e.response["CancellationReasons"]]
assert codes == ["None", "ConditionalCheckFailed", "ConditionalCheckFailed"], e.response
assert e.response["CancellationReasons"][2]["Item"]["v"] == {"S": "tx"}, e.response
assert "Item" in t.get_item(Key={"pk": "tx", "sk": 1}), "transaction was not rolled back"
expect_error("ValidationException", lambda: client.transact_write_items(TransactItems=[
    {"Delete": {"TableName": "orders", "Key": {"pk": {"S": "tx"}, "sk": {"N": "1"}}}},
    {"ConditionCheck": {"TableName": "orders", "Key": {"pk": {"S": "tx"}, "sk": {"N": "1"}}, "ConditionExpression": "attribute_exists(pk)"}}]), "multiple operations on one item")
# Idempotency: the same token applies once.
req = dict(ClientRequestToken="tok-1", TransactItems=[{"Update": {"TableName": "orders", "Key": {"pk": {"S": "tx"}, "sk": {"N": "1"}}, "UpdateExpression": "ADD v :one", "ExpressionAttributeValues": {":one": {"N": "1"}}}}])
client.transact_write_items(**req)
client.transact_write_items(**req)
assert t.get_item(Key={"pk": "tx", "sk": 1})["Item"]["v"] == 2
req["TransactItems"][0]["Update"]["ExpressionAttributeValues"][":one"]["N"] = "5"
expect_error("IdempotentParameterMismatchException", lambda: client.transact_write_items(**req))
r = client.transact_get_items(TransactItems=[{"Get": {"TableName": "orders", "Key": {"pk": {"S": "tx"}, "sk": {"N": "1"}}, "ProjectionExpression": "v"}},
                                             {"Get": {"TableName": "orders", "Key": {"pk": {"S": "zz"}, "sk": {"N": "1"}}}}])
assert r["Responses"] == [{"Item": {"v": {"N": "2"}}}, {}], r

# Consumed capacity.
r = client.get_item(TableName="orders", Key={"pk": {"S": "tx"}, "sk": {"N": "1"}}, ReturnConsumedCapacity="TOTAL")
assert r["ConsumedCapacity"]["CapacityUnits"] == 0.5, r
r = client.put_item(TableName="orders", Item={"pk": {"S": "cap"}, "sk": {"N": "1"}, "blob": {"S": "x" * 3000}}, ReturnConsumedCapacity="INDEXES")
assert r["ConsumedCapacity"]["CapacityUnits"] == 3.0, r

# UpdateTable: add a GSI (backfilled) and delete it again.
client.update_table(TableName="orders", AttributeDefinitions=[{"AttributeName": "note", "AttributeType": "S"}],
    GlobalSecondaryIndexUpdates=[{"Create": {"IndexName": "by_note", "KeySchema": [{"AttributeName": "note", "KeyType": "HASH"}], "Projection": {"ProjectionType": "ALL"}}}])
r = t.query(IndexName="by_note", KeyConditionExpression=Key("note").eq("n7"))
assert r["Count"] == 1 and r["Items"][0]["sk"] == 7, r
d = client.describe_table(TableName="orders")["Table"]
assert {g["IndexName"]: g["ItemCount"] for g in d["GlobalSecondaryIndexes"]}["by_note"] == 59, d["GlobalSecondaryIndexes"]
client.update_table(TableName="orders", GlobalSecondaryIndexUpdates=[{"Delete": {"IndexName": "by_note"}}])
expect_error("ValidationException", lambda: t.query(IndexName="by_note", KeyConditionExpression=Key("note").eq("n7")))
d = client.describe_table(TableName="orders")["Table"]
# 1 MB page limit: with 350 KB items a page stops after crossing 1 MB.
for i in range(4):
    client.put_item(TableName="orders", Item={"pk": {"S": "big"}, "sk": {"N": str(i)}, "blob": {"S": "x" * (350 * 1024)}})
r = client.query(TableName="orders", KeyConditionExpression="pk = :p", ExpressionAttributeValues={":p": {"S": "big"}})
assert r["Count"] == 3 and r["LastEvaluatedKey"] == {"pk": {"S": "big"}, "sk": {"N": "2"}}, (r["Count"], r.get("LastEvaluatedKey"))
r = client.query(TableName="orders", KeyConditionExpression="pk = :p", ExpressionAttributeValues={":p": {"S": "big"}}, ExclusiveStartKey=r["LastEvaluatedKey"])
assert r["Count"] == 1 and "LastEvaluatedKey" not in r
d = client.describe_table(TableName="orders")["Table"]
t.reload()
assert d["ItemCount"] == t.item_count and d["TableSizeBytes"] > 0 and d["BillingModeSummary"]["BillingMode"] == "PAY_PER_REQUEST"
print("OK")
`)
	if !strings.Contains(out, "OK") {
		t.Fatalf("output: %s", out)
	}
}

func TestBoto3StreamsAndTTL(t *testing.T) {
	h, s := harness(t)
	h.Python(t, botoPrelude+`
client.create_table(TableName="events", KeySchema=[{"AttributeName": "id", "KeyType": "HASH"}],
    AttributeDefinitions=[{"AttributeName": "id", "AttributeType": "S"}], ProvisionedThroughput={"ReadCapacityUnits": 5, "WriteCapacityUnits": 5},
    StreamSpecification={"StreamEnabled": True, "StreamViewType": "NEW_AND_OLD_IMAGES"})
client.update_time_to_live(TableName="events", TimeToLiveSpecification={"Enabled": True, "AttributeName": "exp"})
t = ddb.Table("events")
t.put_item(Item={"id": "a", "v": 1})
t.put_item(Item={"id": "a", "v": 1})            # no change: no stream record
t.update_item(Key={"id": "a"}, UpdateExpression="SET v = :v", ExpressionAttributeValues={":v": 2})
t.delete_item(Key={"id": "a"})
import time
t.put_item(Item={"id": "old", "exp": int(time.time()) - 3600})  # expired an hour ago
t.put_item(Item={"id": "ancient", "exp": 1000000000})  # more than five years ago: never expired
t.put_item(Item={"id": "live", "exp": 4102444800})  # 2100
`)
	s.Sweep()
	out := h.Python(t, botoPrelude+`
t = ddb.Table("events")
assert "Item" not in t.get_item(Key={"id": "old"}), "expired item still present"
assert "Item" in t.get_item(Key={"id": "live"})
st = boto3.client("dynamodbstreams")
arn = client.describe_table(TableName="events")["Table"]["LatestStreamArn"]
assert st.list_streams(TableName="events")["Streams"][0]["StreamArn"] == arn
desc = st.describe_stream(StreamArn=arn)["StreamDescription"]
assert desc["StreamStatus"] == "ENABLED" and desc["StreamViewType"] == "NEW_AND_OLD_IMAGES"
shard = desc["Shards"][0]["ShardId"]
it = st.get_shard_iterator(StreamArn=arn, ShardId=shard, ShardIteratorType="TRIM_HORIZON")["ShardIterator"]
recs = []
while it:
    r = st.get_records(ShardIterator=it, Limit=2)
    recs += r["Records"]
    if not r["Records"]: break
    it = r.get("NextShardIterator")
events = [(r["eventName"], r["dynamodb"]["Keys"]["id"]["S"]) for r in recs]
assert events == [("INSERT", "a"), ("MODIFY", "a"), ("REMOVE", "a"), ("INSERT", "old"), ("INSERT", "ancient"), ("INSERT", "live"), ("REMOVE", "old")], events
mod = recs[1]["dynamodb"]
assert mod["OldImage"]["v"] == {"N": "1"} and mod["NewImage"]["v"] == {"N": "2"}, mod
assert recs[6]["userIdentity"] == {"Type": "Service", "PrincipalId": "dynamodb.amazonaws.com"}, recs[6]
assert "Item" in t.get_item(Key={"id": "ancient"})
seqs = [int(r["dynamodb"]["SequenceNumber"]) for r in recs]
assert seqs == sorted(seqs)
# AFTER_SEQUENCE_NUMBER resumes after a checkpoint; LATEST sees only new records.
it = st.get_shard_iterator(StreamArn=arn, ShardId=shard, ShardIteratorType="AFTER_SEQUENCE_NUMBER", SequenceNumber=recs[4]["dynamodb"]["SequenceNumber"])["ShardIterator"]
assert len(st.get_records(ShardIterator=it)["Records"]) == 2
latest = st.get_shard_iterator(StreamArn=arn, ShardId=shard, ShardIteratorType="LATEST")["ShardIterator"]
t.put_item(Item={"id": "z"})
r = st.get_records(ShardIterator=latest)
assert [x["eventName"] for x in r["Records"]] == ["INSERT"] and "NewImage" in r["Records"][0]["dynamodb"]
# Disabling the stream closes the shard; a new stream gets a new label.
client.update_table(TableName="events", StreamSpecification={"StreamEnabled": False})
d = st.describe_stream(StreamArn=arn)["StreamDescription"]
assert d["StreamStatus"] == "DISABLED" and "EndingSequenceNumber" in d["Shards"][0]["SequenceNumberRange"], d
client.update_table(TableName="events", StreamSpecification={"StreamEnabled": True, "StreamViewType": "KEYS_ONLY"})
arn2 = client.describe_table(TableName="events")["Table"]["LatestStreamArn"]
assert arn2 != arn and len(st.list_streams(TableName="events")["Streams"]) == 2
print("OK")
`)
	if !strings.Contains(out, "OK") {
		t.Fatalf("output: %s", out)
	}
	// The in-process reader used by Lambda event source mappings.
	arn, ok := s.LatestStreamARN("events")
	if !ok {
		t.Fatal("no stream")
	}
	h.AWS(t, "dynamodb", "put-item", "--table-name", "events", "--item", `{"id":{"S":"k1"}}`)
	recs, cp, err := s.ReadStream(arn, "", 10)
	if err != nil || len(recs) != 1 || recs[0].EventName != "INSERT" || recs[0].Dynamodb.NewImage != nil {
		t.Fatalf("ReadStream: %v %+v", err, recs)
	}
	if recs2, cp2, _ := s.ReadStream(arn, cp, 10); len(recs2) != 0 || cp2 != cp {
		t.Fatalf("ReadStream after checkpoint: %v %s", recs2, cp2)
	}
	_ = time.Second
}

func TestIAMReadOnlyDeniedWrites(t *testing.T) {
	h, _ := harness(t)
	h.AWS(t, "dynamodb", "create-table", "--table-name", "acl", "--attribute-definitions", "AttributeName=id,AttributeType=S",
		"--key-schema", "AttributeName=id,KeyType=HASH", "--billing-mode", "PAY_PER_REQUEST")
	h.AWS(t, "dynamodb", "put-item", "--table-name", "acl", "--item", `{"id":{"S":"1"}}`)
	akid, secret := h.User(t, "reader", "ReadOnlyAccess")
	out, err := h.AWSAs(t, akid, secret, "", "dynamodb", "get-item", "--table-name", "acl", "--key", `{"id":{"S":"1"}}`)
	if err != nil || !strings.Contains(out, `"1"`) {
		t.Fatalf("reader get-item: %v %s", err, out)
	}
	out, err = h.AWSAs(t, akid, secret, "", "dynamodb", "put-item", "--table-name", "acl", "--item", `{"id":{"S":"2"}}`)
	if err == nil || !strings.Contains(out, "AccessDeniedException") || !strings.Contains(out, "dynamodb:PutItem") {
		t.Fatalf("reader put-item: %v %s", err, out)
	}
	out, err = h.AWSAs(t, akid, secret, "", "dynamodb", "transact-write-items", "--transact-items", `[{"Put":{"TableName":"acl","Item":{"id":{"S":"3"}}}}]`)
	if err == nil || !strings.Contains(out, "AccessDeniedException") {
		t.Fatalf("reader transact: %v %s", err, out)
	}
	found := false
	for _, a := range h.AuditLog() {
		if strings.HasPrefix(a, "dynamodb:PutItem arn:aws:dynamodb:") && strings.HasSuffix(a, ":table/acl") {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit: %v", h.AuditLog())
	}
}
