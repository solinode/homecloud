package dynamodb

import (
	"strings"
	"testing"
)

func TestBoto3LegacyAndPartiQL(t *testing.T) {
	h, _ := harness(t)
	out := h.Python(t, botoPrelude+`
client.create_table(TableName="legacy", KeySchema=[{"AttributeName": "h", "KeyType": "HASH"}, {"AttributeName": "r", "KeyType": "RANGE"}],
    AttributeDefinitions=[{"AttributeName": "h", "AttributeType": "S"}, {"AttributeName": "r", "AttributeType": "N"}], BillingMode="PAY_PER_REQUEST")
for i in range(5):
    client.put_item(TableName="legacy", Item={"h": {"S": "a"}, "r": {"N": str(i)}, "v": {"N": str(i * 10)}, "l": {"L": [{"S": "x"}]}})
# Expected / conditional operator.
expect_error("ConditionalCheckFailedException", lambda: client.put_item(TableName="legacy", Item={"h": {"S": "a"}, "r": {"N": "1"}}, Expected={"h": {"Exists": False}}))
client.put_item(TableName="legacy", Item={"h": {"S": "a"}, "r": {"N": "1"}, "v": {"N": "11"}}, Expected={"v": {"ComparisonOperator": "EQ", "AttributeValueList": [{"N": "10"}]}})
expect_error("ValidationException", lambda: client.put_item(TableName="legacy", Item={"h": {"S": "a"}, "r": {"N": "1"}}, Expected={"h": {"Exists": False}}, ConditionExpression="attribute_exists(h)"), "Can not use both expression and non-expression parameters")
# KeyConditions + QueryFilter + AttributesToGet.
r = client.query(TableName="legacy", KeyConditions={"h": {"ComparisonOperator": "EQ", "AttributeValueList": [{"S": "a"}]}, "r": {"ComparisonOperator": "GE", "AttributeValueList": [{"N": "2"}]}},
    QueryFilter={"v": {"ComparisonOperator": "LT", "AttributeValueList": [{"N": "40"}]}}, AttributesToGet=["r"])
assert r["Items"] == [{"r": {"N": "2"}}, {"r": {"N": "3"}}], r
r = client.scan(TableName="legacy", ScanFilter={"v": {"ComparisonOperator": "IN", "AttributeValueList": [{"N": "0"}, {"N": "40"}]}}, Select="COUNT")
assert r["Count"] == 2 and "Items" not in r, r
# AttributeUpdates: PUT, ADD (number and list append), DELETE.
r = client.update_item(TableName="legacy", Key={"h": {"S": "a"}, "r": {"N": "0"}}, AttributeUpdates={
    "v": {"Action": "ADD", "Value": {"N": "5"}}, "l": {"Action": "ADD", "Value": {"L": [{"S": "y"}]}}, "n": {"Action": "PUT", "Value": {"S": "new"}}},
    ReturnValues="ALL_NEW")["Attributes"]
assert r["v"] == {"N": "5"} and r["l"] == {"L": [{"S": "x"}, {"S": "y"}]} and r["n"] == {"S": "new"}, r
r = client.update_item(TableName="legacy", Key={"h": {"S": "a"}, "r": {"N": "0"}}, AttributeUpdates={"n": {"Action": "DELETE"}}, ReturnValues="ALL_NEW")["Attributes"]
assert "n" not in r

# PartiQL.
client.execute_statement(Statement="INSERT INTO legacy VALUE {'h': 'p', 'r': 1, 'tags': <<'a', 'b'>>, 'm': {'k': [1, true, null]}}")
expect_error("DuplicateItemException", lambda: client.execute_statement(Statement="INSERT INTO \"legacy\" VALUE {'h': 'p', 'r': 1}"))
r = client.execute_statement(Statement="SELECT * FROM legacy WHERE h = ? AND r = ?", Parameters=[{"S": "p"}, {"N": "1"}])
assert r["Items"][0]["tags"]["SS"] and r["Items"][0]["m"]["M"]["k"]["L"][1] == {"BOOL": True}, r
client.execute_statement(Statement="UPDATE legacy SET cnt = 1 SET m.k[0] = 5 REMOVE tags WHERE h = 'p' AND r = 1")
client.execute_statement(Statement="UPDATE legacy SET cnt = cnt + 2 WHERE h = 'p' AND r = 1")
r = client.execute_statement(Statement="SELECT cnt, m.k FROM legacy WHERE h = 'p'")
assert r["Items"] == [{"cnt": {"N": "3"}, "m": {"M": {"k": {"L": [{"N": "5"}, {"BOOL": True}, {"NULL": True}]}}}}], r
expect_error("ConditionalCheckFailedException", lambda: client.execute_statement(Statement="UPDATE legacy SET cnt = 9 WHERE h = 'nope' AND r = 1"))
r = client.execute_statement(Statement="SELECT r FROM legacy WHERE h = 'a' AND v >= 20 AND begins_with(h, 'a')")
assert sorted(int(x["r"]["N"]) for x in r["Items"]) == [2, 3, 4], r
r = client.execute_statement(Statement="SELECT * FROM legacy WHERE n IS MISSING AND v IN [0, 5, 30]")
assert sorted(int(x["r"]["N"]) for x in r["Items"]) == [0, 3], r
# Paging through a scan with Limit/NextToken.
got, tok = [], None
while True:
    kw = {"Statement": "SELECT r FROM legacy", "Limit": 2}
    if tok: kw["NextToken"] = tok
    r = client.execute_statement(**kw)
    got += r["Items"]
    tok = r.get("NextToken")
    if not tok: break
assert len(got) == 6, got
client.execute_statement(Statement="DELETE FROM legacy WHERE h = 'p' AND r = 1")
assert client.execute_statement(Statement="SELECT * FROM legacy WHERE h = 'p'")["Items"] == []
# Transactions and batches.
client.execute_transaction(TransactStatements=[
    {"Statement": "INSERT INTO legacy VALUE {'h': 't', 'r': 1}"},
    {"Statement": "UPDATE legacy SET v = 100 WHERE h = 'a' AND r = 4"}])
e = expect_error("TransactionCanceledException", lambda: client.execute_transaction(TransactStatements=[
    {"Statement": "INSERT INTO legacy VALUE {'h': 't', 'r': 2}"},
    {"Statement": "UPDATE legacy SET v = 1 WHERE h = 'a' AND r = 4 AND v = 5"}]))
assert [c["Code"] for c in e.response["CancellationReasons"]] == ["None", "ConditionalCheckFailed"], e.response
r = client.batch_execute_statement(Statements=[
    {"Statement": "SELECT * FROM legacy WHERE h = 't' AND r = 1"},
    {"Statement": "INSERT INTO legacy VALUE {'h': 't', 'r': 1}"},
    {"Statement": "INSERT INTO legacy VALUE {'h': 't', 'r': 3}"}])["Responses"]
assert r[0]["Item"]["h"] == {"S": "t"} and r[1]["Error"]["Code"] == "DuplicateItem" and "Error" not in r[2], r
print("OK")
`)
	if !strings.Contains(out, "OK") {
		t.Fatalf("output: %s", out)
	}
}
