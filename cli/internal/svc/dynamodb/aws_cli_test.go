package dynamodb

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

func harness(t *testing.T) (*awstest.Harness, *Service) {
	t.Helper()
	h := awstest.New(t)
	s, err := New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.RegisterAWS()
	s.Routes(h.Router)
	return h, s
}

func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

// Terraform's provider waits after every create and update until the table's
// (and each index's) warm throughput is ACTIVE, and fails when it is missing.
func TestAWSWarmThroughput(t *testing.T) {
	h, _ := harness(t)
	warm := func(m map[string]any) map[string]any { w, _ := m["WarmThroughput"].(map[string]any); return w }
	td := h.AWSJSON(t, "dynamodb", "create-table", "--table-name", "Warm",
		"--attribute-definitions", "AttributeName=id,AttributeType=S", "AttributeName=g,AttributeType=S",
		"--key-schema", "AttributeName=id,KeyType=HASH", "--billing-mode", "PAY_PER_REQUEST",
		"--global-secondary-indexes", `[{"IndexName":"ByG","KeySchema":[{"AttributeName":"g","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}]`)["TableDescription"].(map[string]any)
	if w := warm(td); w["Status"] != "ACTIVE" || w["ReadUnitsPerSecond"] != float64(12000) || w["WriteUnitsPerSecond"] != float64(4000) {
		t.Fatalf("default warm throughput %v", w)
	}
	if w := warm(td["GlobalSecondaryIndexes"].([]any)[0].(map[string]any)); w["Status"] != "ACTIVE" {
		t.Fatalf("index warm throughput %v", w)
	}
	h.AWS(t, "dynamodb", "update-table", "--table-name", "Warm", "--warm-throughput", "ReadUnitsPerSecond=15000,WriteUnitsPerSecond=5000")
	td = h.AWSJSON(t, "dynamodb", "describe-table", "--table-name", "Warm")["Table"].(map[string]any)
	if w := warm(td); w["ReadUnitsPerSecond"] != float64(15000) || w["WriteUnitsPerSecond"] != float64(5000) || w["Status"] != "ACTIVE" {
		t.Fatalf("updated warm throughput %v", w)
	}
}

func TestAWSCLI(t *testing.T) {
	h, _ := harness(t)
	out := h.AWSJSON(t, "dynamodb", "create-table", "--table-name", "Music",
		"--attribute-definitions", "AttributeName=Artist,AttributeType=S", "AttributeName=SongTitle,AttributeType=S", "AttributeName=Year,AttributeType=N",
		"--key-schema", "AttributeName=Artist,KeyType=HASH", "AttributeName=SongTitle,KeyType=RANGE",
		"--billing-mode", "PAY_PER_REQUEST",
		"--global-secondary-indexes", `[{"IndexName":"ByYear","KeySchema":[{"AttributeName":"Year","KeyType":"HASH"}],"Projection":{"ProjectionType":"KEYS_ONLY"}}]`,
		"--stream-specification", "StreamEnabled=true,StreamViewType=NEW_AND_OLD_IMAGES",
		"--tags", "Key=env,Value=test")
	td := out["TableDescription"].(map[string]any)
	if td["TableStatus"] != "ACTIVE" || !strings.HasSuffix(td["TableArn"].(string), ":table/Music") || td["LatestStreamArn"] == nil {
		t.Fatalf("create-table: %v", td)
	}
	h.AWS(t, "dynamodb", "wait", "table-exists", "--table-name", "Music")
	h.AWS(t, "dynamodb", "put-item", "--table-name", "Music", "--item",
		`{"Artist":{"S":"No One You Know"},"SongTitle":{"S":"Call Me Today"},"Year":{"N":"2015"},"Price":{"N":"1.50"},"Tags":{"SS":["a","b"]}}`)
	h.AWS(t, "dynamodb", "put-item", "--table-name", "Music", "--item",
		`{"Artist":{"S":"No One You Know"},"SongTitle":{"S":"Another"},"Year":{"N":"2019"}}`)
	got := h.AWSJSON(t, "dynamodb", "get-item", "--table-name", "Music", "--consistent-read", "--key",
		`{"Artist":{"S":"No One You Know"},"SongTitle":{"S":"Call Me Today"}}`)
	item := got["Item"].(map[string]any)
	if item["Price"].(map[string]any)["N"] != "1.5" {
		t.Fatalf("number not canonical: %v", item)
	}
	// Conditional put fails.
	o, err := h.AWSErr(t, "dynamodb", "put-item", "--table-name", "Music", "--item",
		`{"Artist":{"S":"No One You Know"},"SongTitle":{"S":"Another"}}`, "--condition-expression", "attribute_not_exists(Artist)")
	if err == nil || !strings.Contains(o, "ConditionalCheckFailedException") {
		t.Fatalf("conditional put: %v %s", err, o)
	}
	upd := h.AWSJSON(t, "dynamodb", "update-item", "--table-name", "Music", "--key",
		`{"Artist":{"S":"No One You Know"},"SongTitle":{"S":"Call Me Today"}}`,
		"--update-expression", "SET Price = Price + :inc, Plays = if_not_exists(Plays, :zero) + :one ADD Tags :t",
		"--expression-attribute-values", `{":inc":{"N":"0.25"},":zero":{"N":"0"},":one":{"N":"1"},":t":{"SS":["c"]}}`,
		"--return-values", "UPDATED_NEW")
	attrs := upd["Attributes"].(map[string]any)
	if attrs["Price"].(map[string]any)["N"] != "1.75" || attrs["Plays"].(map[string]any)["N"] != "1" || len(attrs["Tags"].(map[string]any)["SS"].([]any)) != 3 {
		t.Fatalf("update-item: %v", attrs)
	}
	qr := h.AWSJSON(t, "dynamodb", "query", "--table-name", "Music", "--key-condition-expression", "Artist = :a AND begins_with(SongTitle, :p)",
		"--expression-attribute-values", `{":a":{"S":"No One You Know"},":p":{"S":"Call"}}`)
	if qr["Count"].(float64) != 1 {
		t.Fatalf("query: %v", qr)
	}
	gi := h.AWSJSON(t, "dynamodb", "query", "--table-name", "Music", "--index-name", "ByYear", "--key-condition-expression", "#y = :y",
		"--expression-attribute-names", `{"#y":"Year"}`, "--expression-attribute-values", `{":y":{"N":"2019"}}`)
	items := gi["Items"].([]any)
	if len(items) != 1 || len(items[0].(map[string]any)) != 3 {
		t.Fatalf("gsi query (KEYS_ONLY): %v", gi)
	}
	// Pagination through the CLI's paginator (page size 1).
	sc := h.AWSJSON(t, "dynamodb", "scan", "--table-name", "Music", "--page-size", "1")
	if sc["Count"].(float64) != 2 {
		t.Fatalf("scan: %v", sc)
	}
	h.AWS(t, "dynamodb", "update-time-to-live", "--table-name", "Music", "--time-to-live-specification", "Enabled=true,AttributeName=expires")
	ttl := h.AWSJSON(t, "dynamodb", "describe-time-to-live", "--table-name", "Music")
	if ttl["TimeToLiveDescription"].(map[string]any)["TimeToLiveStatus"] != "ENABLED" {
		t.Fatalf("ttl: %v", ttl)
	}
	cb := h.AWSJSON(t, "dynamodb", "describe-continuous-backups", "--table-name", "Music")
	if cb["ContinuousBackupsDescription"].(map[string]any)["PointInTimeRecoveryDescription"].(map[string]any)["PointInTimeRecoveryStatus"] != "DISABLED" {
		t.Fatalf("backups: %v", cb)
	}
	tags := h.AWSJSON(t, "dynamodb", "list-tags-of-resource", "--resource-arn", td["TableArn"].(string))
	if len(tags["Tags"].([]any)) != 1 {
		t.Fatalf("tags: %v", tags)
	}
	// Streams through the CLI.
	streamARN := td["LatestStreamArn"].(string)
	ds := h.AWSJSON(t, "dynamodbstreams", "describe-stream", "--stream-arn", streamARN)
	shard := ds["StreamDescription"].(map[string]any)["Shards"].([]any)[0].(map[string]any)["ShardId"].(string)
	it := h.AWSJSON(t, "dynamodbstreams", "get-shard-iterator", "--stream-arn", streamARN, "--shard-id", shard, "--shard-iterator-type", "TRIM_HORIZON")
	recs := h.AWSJSON(t, "dynamodbstreams", "get-records", "--shard-iterator", it["ShardIterator"].(string))
	names := []string{}
	for _, r := range recs["Records"].([]any) {
		names = append(names, r.(map[string]any)["eventName"].(string))
	}
	if strings.Join(names, ",") != "INSERT,INSERT,MODIFY" {
		t.Fatalf("stream records: %v", names)
	}
	lt := h.AWSJSON(t, "dynamodb", "list-tables")
	if len(lt["TableNames"].([]any)) != 1 {
		t.Fatalf("list-tables: %v", lt)
	}
	h.AWS(t, "dynamodb", "delete-table", "--table-name", "Music")
	h.AWS(t, "dynamodb", "wait", "table-not-exists", "--table-name", "Music")
}
