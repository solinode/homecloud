package dynamodb

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	bolt "go.etcd.io/bbolt"
)

func native(t *testing.T, h *awstest.Harness, method, path string, body any) map[string]any {
	t.Helper()
	out := h.Native(t, method, path, body)
	m := map[string]any{}
	if len(out) > 0 && out[0] == '{' {
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestNativeAPI(t *testing.T) {
	h, _ := harness(t)
	base := "/api/v1/dynamodb/tables"
	native(t, h, "POST", base, map[string]any{"name": "users", "partition_key": map[string]string{"name": "org"},
		"sort_key":                 map[string]string{"name": "n", "type": "N"},
		"global_secondary_indexes": []any{map[string]any{"name": "by_email", "partition_key": map[string]string{"name": "email", "type": "S"}}}})
	for i := 0; i < 5; i++ {
		native(t, h, "POST", base+"/users/items", map[string]any{"item": map[string]any{"org": "acme", "n": i, "email": "u" + string(rune('a'+i)), "score": 1.5, "big": json.Number("123456789012345678901234567890")}})
	}
	got := native(t, h, "POST", base+"/users/items/get", map[string]any{"key": map[string]any{"org": "acme", "n": "3"}})
	it := got["item"].(map[string]any)
	if it["email"] != "ud" || it["big"].(float64) != 123456789012345678901234567890 {
		t.Fatalf("get: %v", got)
	}
	upd := native(t, h, "POST", base+"/users/items/update", map[string]any{"key": map[string]any{"org": "acme", "n": 3},
		"set": map[string]any{"name": "Dee"}, "add": map[string]any{"score": 2}, "remove": []string{"big"}})
	if u := upd["item"].(map[string]any); u["score"].(float64) != 3.5 || u["name"] != "Dee" || u["big"] != nil {
		t.Fatalf("update: %v", upd)
	}
	q := native(t, h, "POST", base+"/users/query", map[string]any{"partition_value": "acme", "sort_condition": map[string]any{"op": "between", "value": 1, "value2": 3}, "forward": false})
	items := q["items"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["n"].(float64) != 3 {
		t.Fatalf("query: %v", q)
	}
	q = native(t, h, "POST", base+"/users/query", map[string]any{"index": "by_email", "partition_value": "ub"})
	if q["count"].(float64) != 1 {
		t.Fatalf("index query: %v", q)
	}
	p1 := native(t, h, "POST", base+"/users/scan", map[string]any{"limit": 2, "filter": []any{map[string]any{"attr": "score", "op": "ge", "value": 1}}})
	if p1["count"].(float64) != 2 || p1["last_evaluated_key"] == nil {
		t.Fatalf("scan page 1: %v", p1)
	}
	p2 := native(t, h, "POST", base+"/users/scan", map[string]any{"limit": 10, "start_key": p1["last_evaluated_key"]})
	if p2["count"].(float64) != 3 || p2["last_evaluated_key"] != nil {
		t.Fatalf("scan page 2: %v", p2)
	}
	native(t, h, "POST", base+"/users/batch-write", map[string]any{"deletes": []any{map[string]any{"org": "acme", "n": 0}}, "puts": []any{map[string]any{"org": "beta", "n": 1}}})
	d := native(t, h, "GET", base+"/users", nil)
	if d["item_count"].(float64) != 5 {
		t.Fatalf("describe: %v", d)
	}
	native(t, h, "PATCH", base+"/users", map[string]any{"add_index": map[string]any{"name": "by_name", "partition_key": map[string]string{"name": "name"}}, "ttl_attribute": "exp"})
	q = native(t, h, "POST", base+"/users/query", map[string]any{"index": "by_name", "partition_value": "Dee"})
	if q["count"].(float64) != 1 {
		t.Fatalf("backfilled index query: %v", q)
	}
	native(t, h, "PATCH", base+"/users", map[string]any{"remove_index": "by_name"})
	// Items written natively are readable over the AWS protocol with their types.
	a := h.AWSJSON(t, "dynamodb", "get-item", "--table-name", "users", "--key", `{"org":{"S":"acme"},"n":{"N":"3"}}`)
	if a["Item"].(map[string]any)["score"].(map[string]any)["N"] != "3.5" {
		t.Fatalf("aws view of native item: %v", a)
	}
	native(t, h, "DELETE", base+"/users", nil)
}

func TestMigrateV1(t *testing.T) {
	h := awstest.New(t)
	env := h.Env
	// Write a table in the version 1 layout: plain JSON items in a bucket named
	// after the table, float64 number keys.
	db, err := bolt.Open(env.Cfg.Path("dynamodb.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("legacy"))
		if err != nil {
			return err
		}
		for i, doc := range []string{
			`{"id":"a","n":1,"g":"x","doc":{"list":[1,"two",null,true]}}`,
			`{"id":"a","n":2.5,"g":"y"}`,
			`{"id":"b","n":-3}`,
		} {
			if err := b.Put([]byte{byte(i)}, []byte(doc)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	old := Table{Name: "legacy", ARN: env.ARN("dynamodb", "table/legacy"), PartitionKey: KeyDef{"id", "S"}, SortKey: &KeyDef{"n", "N"},
		Indexes: []Index{{Name: "by_g", PartitionKey: KeyDef{"g", "S"}}}, Status: "ACTIVE", BillingMode: "PAY_PER_REQUEST", CreatedAt: time.Now()}
	if err := store.Put(env.Store, cTables, "legacy", old); err != nil {
		t.Fatal(err)
	}
	s, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.RegisterAWS()
	tb, _ := s.getTable("legacy")
	if tb.Version != storeVersion || len(tb.Attributes) != 3 || tb.Indexes[0].Projection.Type != "ALL" {
		t.Fatalf("migrated table: %+v", tb)
	}
	q := h.AWSJSON(t, "dynamodb", "query", "--table-name", "legacy", "--key-condition-expression", "id = :a", "--expression-attribute-values", `{":a":{"S":"a"}}`)
	if q["Count"].(float64) != 2 {
		t.Fatalf("query migrated: %v", q)
	}
	first := q["Items"].([]any)[0].(map[string]any)
	if first["doc"].(map[string]any)["M"].(map[string]any)["list"].(map[string]any)["L"].([]any)[2].(map[string]any)["NULL"] != true {
		t.Fatalf("migrated types: %v", first)
	}
	g := h.AWSJSON(t, "dynamodb", "query", "--table-name", "legacy", "--index-name", "by_g", "--key-condition-expression", "g = :y", "--expression-attribute-values", `{":y":{"S":"y"}}`)
	if g["Count"].(float64) != 1 || g["Items"].([]any)[0].(map[string]any)["n"].(map[string]any)["N"] != "2.5" {
		t.Fatalf("migrated index: %v", g)
	}
	d := h.AWSJSON(t, "dynamodb", "describe-table", "--table-name", "legacy")
	if d["Table"].(map[string]any)["ItemCount"].(float64) != 3 {
		t.Fatalf("describe migrated: %v", d)
	}
	// The old bucket is gone and a second start does nothing.
	s.Close()
	s2, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	_ = s2.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("legacy")) != nil {
			t.Error("v1 bucket still present")
		}
		return nil
	})
	if n, _ := s2.stats(tb); n != 3 {
		t.Fatalf("items after restart: %d", n)
	}
}
