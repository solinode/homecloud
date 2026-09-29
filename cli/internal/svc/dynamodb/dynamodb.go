// Package dynamodb implements key-value and document tables on an embedded
// bbolt database: items are JSON documents addressed by a partition key and an
// optional sort key, with range queries, scans with filters, global secondary
// indexes (evaluated by scan), conditional writes, atomic counters and TTL.
package dynamodb

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	bolt "go.etcd.io/bbolt"
)

const (
	cTables      = "dynamodb_tables"
	maxItemBytes = 400 << 10
)

type KeyDef struct {
	Name string `json:"name"`
	Type string `json:"type"` // S | N
}

type Index struct {
	Name         string  `json:"name"`
	PartitionKey KeyDef  `json:"partition_key"`
	SortKey      *KeyDef `json:"sort_key,omitempty"`
}

type Table struct {
	Name         string    `json:"name"`
	ARN          string    `json:"arn"`
	PartitionKey KeyDef    `json:"partition_key"`
	SortKey      *KeyDef   `json:"sort_key,omitempty"`
	Indexes      []Index   `json:"global_secondary_indexes"`
	TTLAttribute string    `json:"ttl_attribute,omitempty"`
	Status       string    `json:"status"`
	BillingMode  string    `json:"billing_mode"`
	CreatedAt    time.Time `json:"created_at"`
	Tags         core.Tags `json:"tags,omitempty"`
}

type Item = map[string]any

type Service struct {
	env *svc.Env
	db  *bolt.DB
}

func New(env *svc.Env) (*Service, error) {
	db, err := bolt.Open(env.Cfg.Path("dynamodb.db"), 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open dynamodb store: %w (is another HomeCloud server using %s?)", err, env.Cfg.DataDir)
	}
	return &Service{env: env, db: db}, nil
}

func (s *Service) Close() error { return s.db.Close() }

// ---- key encoding ----

// encodeKey produces an order-preserving byte encoding of a key attribute.
func encodeKey(def KeyDef, v any) ([]byte, error) {
	switch def.Type {
	case "S":
		str, ok := v.(string)
		if !ok || str == "" || strings.ContainsRune(str, 0) {
			return nil, core.BadRequest("key attribute %q must be a non-empty string", def.Name)
		}
		return []byte(str), nil
	case "N":
		f, ok := v.(float64)
		if !ok {
			return nil, core.BadRequest("key attribute %q must be a number", def.Name)
		}
		bits := math.Float64bits(f)
		if f >= 0 {
			bits ^= 1 << 63
		} else {
			bits = ^bits
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, bits)
		return b, nil
	}
	return nil, fmt.Errorf("bad key type %q", def.Type)
}

func (t Table) itemKey(it Item) ([]byte, error) {
	pk, err := encodeKey(t.PartitionKey, it[t.PartitionKey.Name])
	if err != nil {
		return nil, err
	}
	k := append(pk, 0)
	if t.SortKey != nil {
		sk, err := encodeKey(*t.SortKey, it[t.SortKey.Name])
		if err != nil {
			return nil, err
		}
		k = append(k, sk...)
	}
	return k, nil
}

func (s *Service) table(name string) (Table, error) {
	t, err := store.Get[Table](s.env.Store, cTables, name)
	if err != nil {
		return t, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "table %q does not exist", name)
	}
	return t, nil
}

func expired(t Table, it Item) bool {
	if t.TTLAttribute == "" {
		return false
	}
	v, ok := it[t.TTLAttribute].(float64)
	return ok && v > 0 && int64(v) < time.Now().Unix()
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:dynamodb:local-1:{account}:table/{name}")
	r.Handle("GET /api/v1/dynamodb/tables", "dynamodb:ListTables", s.listTables)
	r.Handle("POST /api/v1/dynamodb/tables", "dynamodb:CreateTable", s.createTable)
	r.Handle("GET /api/v1/dynamodb/tables/{name}", "dynamodb:DescribeTable", s.describe, res)
	r.Handle("PATCH /api/v1/dynamodb/tables/{name}", "dynamodb:UpdateTable", s.updateTable, res)
	r.Handle("DELETE /api/v1/dynamodb/tables/{name}", "dynamodb:DeleteTable", s.deleteTable, res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/items", "dynamodb:PutItem", s.putItem, res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/items/get", "dynamodb:GetItem", s.getItem, res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/items/update", "dynamodb:UpdateItem", s.updateItem, res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/items/delete", "dynamodb:DeleteItem", s.deleteItem, res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/batch-write", "dynamodb:BatchWriteItem", s.batchWrite, res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/query", "dynamodb:Query", s.query, res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/scan", "dynamodb:Scan", s.scan, res)
}

func (s *Service) stats(t Table) (int, int64) {
	count, size := 0, int64(0)
	_ = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(t.Name))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			count++
			size += int64(len(v))
			return nil
		})
	})
	return count, size
}

func (s *Service) view(t Table) map[string]any {
	n, size := s.stats(t)
	b, _ := json.Marshal(t)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["item_count"], m["size_bytes"] = n, size
	return m
}

func (s *Service) listTables(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, t := range store.List[Table](s.env.Store, cTables) {
		out = append(out, s.view(t))
	}
	return out, nil
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,255}$`)

func validKey(k *KeyDef, what string) error {
	if k.Name == "" {
		return core.BadRequest("%s name is required", what)
	}
	if k.Type == "" {
		k.Type = "S"
	}
	if k.Type != "S" && k.Type != "N" {
		return core.BadRequest("%s type must be S or N", what)
	}
	return nil
}

func (s *Service) createTable(c *httpx.Ctx) (any, error) {
	var in Table
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !nameRe.MatchString(in.Name) {
		return nil, core.BadRequest("table names are 3-255 letters, digits, dots, hyphens or underscores")
	}
	if store.Has(s.env.Store, cTables, in.Name) {
		return nil, core.Errf(http.StatusConflict, "ResourceInUseException", "table %q already exists", in.Name)
	}
	if err := validKey(&in.PartitionKey, "partition_key"); err != nil {
		return nil, err
	}
	if in.SortKey != nil {
		if err := validKey(in.SortKey, "sort_key"); err != nil {
			return nil, err
		}
	}
	for i := range in.Indexes {
		if in.Indexes[i].Name == "" {
			return nil, core.BadRequest("index name is required")
		}
		if err := validKey(&in.Indexes[i].PartitionKey, "index partition_key"); err != nil {
			return nil, err
		}
		if in.Indexes[i].SortKey != nil {
			if err := validKey(in.Indexes[i].SortKey, "index sort_key"); err != nil {
				return nil, err
			}
		}
	}
	if in.Indexes == nil {
		in.Indexes = []Index{}
	}
	t := Table{Name: in.Name, ARN: s.env.ARN("dynamodb", "table/"+in.Name), PartitionKey: in.PartitionKey, SortKey: in.SortKey,
		Indexes: in.Indexes, TTLAttribute: in.TTLAttribute, Status: "ACTIVE", BillingMode: "PAY_PER_REQUEST", CreatedAt: core.Now(), Tags: in.Tags}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(t.Name))
		return err
	}); err != nil {
		return nil, err
	}
	return s.view(t), store.Put(s.env.Store, cTables, t.Name, t)
}

func (s *Service) describe(c *httpx.Ctx) (any, error) {
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	return s.view(t), nil
}

func (s *Service) updateTable(c *httpx.Ctx) (any, error) {
	var in struct {
		TTLAttribute *string   `json:"ttl_attribute"`
		AddIndex     *Index    `json:"add_index"`
		RemoveIndex  string    `json:"remove_index"`
		Tags         core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.AddIndex != nil {
		if err := validKey(&in.AddIndex.PartitionKey, "index partition_key"); err != nil {
			return nil, err
		}
	}
	t, err := store.Update(s.env.Store, cTables, c.Param("name"), func(t *Table) error {
		if in.TTLAttribute != nil {
			t.TTLAttribute = *in.TTLAttribute
		}
		if in.AddIndex != nil {
			for _, ix := range t.Indexes {
				if ix.Name == in.AddIndex.Name {
					return core.Conflict("index %q already exists", ix.Name)
				}
			}
			t.Indexes = append(t.Indexes, *in.AddIndex)
		}
		if in.RemoveIndex != "" {
			for i, ix := range t.Indexes {
				if ix.Name == in.RemoveIndex {
					t.Indexes = append(t.Indexes[:i], t.Indexes[i+1:]...)
					break
				}
			}
		}
		if in.Tags != nil {
			t.Tags = in.Tags
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "table %q does not exist", c.Param("name"))
	}
	if err != nil {
		return nil, err
	}
	return s.view(t), nil
}

func (s *Service) deleteTable(c *httpx.Ctx) (any, error) {
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(t.Name)) == nil {
			return nil
		}
		return tx.DeleteBucket([]byte(t.Name))
	}); err != nil {
		return nil, err
	}
	return nil, store.Delete(s.env.Store, cTables, t.Name)
}

// ---- items ----

// Condition is a simple attribute comparison used by filters and conditional writes.
type Condition struct {
	Attr   string `json:"attr"`
	Op     string `json:"op"` // eq ne lt le gt ge begins_with contains exists not_exists between
	Value  any    `json:"value"`
	Value2 any    `json:"value2"`
}

func compare(a, b any) (int, bool) {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case string:
		y, ok := b.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(x, y), true
	case bool:
		y, ok := b.(bool)
		return 0, ok && x == y
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if bytes.Equal(ab, bb) {
		return 0, true
	}
	return 0, false
}

func (c Condition) eval(it Item) bool {
	v, present := it[c.Attr]
	switch c.Op {
	case "exists":
		return present
	case "not_exists":
		return !present
	}
	if !present {
		return c.Op == "ne"
	}
	cmp, ok := compare(v, c.Value)
	switch c.Op {
	case "eq", "":
		return ok && cmp == 0
	case "ne":
		return !ok || cmp != 0
	case "lt":
		return ok && cmp < 0
	case "le":
		return ok && cmp <= 0
	case "gt":
		return ok && cmp > 0
	case "ge":
		return ok && cmp >= 0
	case "between":
		hi, ok2 := compare(v, c.Value2)
		return ok && ok2 && cmp >= 0 && hi <= 0
	case "begins_with":
		s, _ := v.(string)
		p, _ := c.Value.(string)
		return strings.HasPrefix(s, p)
	case "contains":
		switch x := v.(type) {
		case string:
			p, _ := c.Value.(string)
			return strings.Contains(x, p)
		case []any:
			for _, e := range x {
				if cmp, ok := compare(e, c.Value); ok && cmp == 0 {
					return true
				}
			}
		}
	}
	return false
}

func all(conds []Condition, it Item) bool {
	for _, c := range conds {
		if !c.eval(it) {
			return false
		}
	}
	return true
}

var errConditionFailed = core.Errf(http.StatusBadRequest, "ConditionalCheckFailedException", "the conditional request failed")

func (s *Service) putItem(c *httpx.Ctx) (any, error) {
	var in struct {
		Item      Item        `json:"item"`
		Condition []Condition `json:"condition"`
		ReturnOld bool        `json:"return_old"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	var old Item
	err = s.db.Update(func(tx *bolt.Tx) error {
		var err error
		old, err = s.put(tx, t, in.Item, in.Condition)
		return err
	})
	if err != nil {
		return nil, err
	}
	if in.ReturnOld {
		return map[string]any{"old_item": old}, nil
	}
	return map[string]any{}, nil
}

func (s *Service) put(tx *bolt.Tx, t Table, it Item, cond []Condition) (Item, error) {
	k, err := t.itemKey(it)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}
	if len(b) > maxItemBytes {
		return nil, core.BadRequest("item size exceeds 400 KB")
	}
	bk := tx.Bucket([]byte(t.Name))
	var old Item
	if raw := bk.Get(k); raw != nil {
		_ = json.Unmarshal(raw, &old)
		if expired(t, old) {
			old = nil
		}
	}
	if len(cond) > 0 && !all(cond, orEmpty(old)) {
		return nil, errConditionFailed
	}
	return old, bk.Put(k, b)
}

func orEmpty(it Item) Item {
	if it == nil {
		return Item{}
	}
	return it
}

func (s *Service) keyItem(t Table, key Item) ([]byte, error) {
	if key == nil {
		return nil, core.BadRequest("key is required")
	}
	return t.itemKey(key)
}

func (s *Service) getItem(c *httpx.Ctx) (any, error) {
	var in struct {
		Key        Item     `json:"key"`
		Projection []string `json:"projection"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	k, err := s.keyItem(t, in.Key)
	if err != nil {
		return nil, err
	}
	var it Item
	err = s.db.View(func(tx *bolt.Tx) error {
		if raw := tx.Bucket([]byte(t.Name)).Get(k); raw != nil {
			return json.Unmarshal(raw, &it)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if it == nil || expired(t, it) {
		return map[string]any{"item": nil}, nil
	}
	return map[string]any{"item": project(it, in.Projection)}, nil
}

func project(it Item, attrs []string) Item {
	if len(attrs) == 0 {
		return it
	}
	out := Item{}
	for _, a := range attrs {
		if v, ok := it[a]; ok {
			out[a] = v
		}
	}
	return out
}

func (s *Service) updateItem(c *httpx.Ctx) (any, error) {
	var in struct {
		Key       Item               `json:"key"`
		Set       map[string]any     `json:"set"`
		Remove    []string           `json:"remove"`
		Add       map[string]float64 `json:"add"`
		Condition []Condition        `json:"condition"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	keyAttrs := map[string]bool{t.PartitionKey.Name: true}
	if t.SortKey != nil {
		keyAttrs[t.SortKey.Name] = true
	}
	for a := range in.Set {
		if keyAttrs[a] {
			return nil, core.BadRequest("key attribute %q cannot be updated", a)
		}
	}
	k, err := s.keyItem(t, in.Key)
	if err != nil {
		return nil, err
	}
	var it Item
	err = s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(t.Name))
		if raw := bk.Get(k); raw != nil {
			_ = json.Unmarshal(raw, &it)
			if expired(t, it) {
				it = nil
			}
		}
		if len(in.Condition) > 0 && !all(in.Condition, orEmpty(it)) {
			return errConditionFailed
		}
		if it == nil {
			it = Item{}
			for a := range keyAttrs {
				it[a] = in.Key[a]
			}
		}
		for a, v := range in.Set {
			it[a] = v
		}
		for _, a := range in.Remove {
			if !keyAttrs[a] {
				delete(it, a)
			}
		}
		for a, n := range in.Add {
			cur, _ := it[a].(float64)
			it[a] = cur + n
		}
		b, err := json.Marshal(it)
		if err != nil {
			return err
		}
		if len(b) > maxItemBytes {
			return core.BadRequest("item size exceeds 400 KB")
		}
		return bk.Put(k, b)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": it}, nil
}

func (s *Service) deleteItem(c *httpx.Ctx) (any, error) {
	var in struct {
		Key       Item        `json:"key"`
		Condition []Condition `json:"condition"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	k, err := s.keyItem(t, in.Key)
	if err != nil {
		return nil, err
	}
	var old Item
	err = s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(t.Name))
		if raw := bk.Get(k); raw != nil {
			_ = json.Unmarshal(raw, &old)
		}
		if len(in.Condition) > 0 && !all(in.Condition, orEmpty(old)) {
			return errConditionFailed
		}
		return bk.Delete(k)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"old_item": old}, nil
}

func (s *Service) batchWrite(c *httpx.Ctx) (any, error) {
	var in struct {
		Puts    []Item `json:"puts"`
		Deletes []Item `json:"deletes"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Puts)+len(in.Deletes) > 1000 {
		return nil, core.BadRequest("a batch holds at most 1000 writes")
	}
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		for _, it := range in.Puts {
			if _, err := s.put(tx, t, it, nil); err != nil {
				return err
			}
		}
		bk := tx.Bucket([]byte(t.Name))
		for _, key := range in.Deletes {
			k, err := t.itemKey(key)
			if err != nil {
				return err
			}
			if err := bk.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]int{"written": len(in.Puts), "deleted": len(in.Deletes)}, nil
}

type pageInput struct {
	Limit      int         `json:"limit"`
	StartKey   Item        `json:"start_key"`
	Filter     []Condition `json:"filter"`
	Projection []string    `json:"projection"`
}

func (s *Service) query(c *httpx.Ctx) (any, error) {
	var in struct {
		pageInput
		Index          string     `json:"index"`
		PartitionValue any        `json:"partition_value"`
		SortCondition  *Condition `json:"sort_condition"`
		Forward        *bool      `json:"forward"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	forward := in.Forward == nil || *in.Forward
	pkDef, skDef := t.PartitionKey, t.SortKey
	if in.Index != "" {
		found := false
		for _, ix := range t.Indexes {
			if ix.Name == in.Index {
				pkDef, skDef, found = ix.PartitionKey, ix.SortKey, true
			}
		}
		if !found {
			return nil, core.BadRequest("index %q does not exist", in.Index)
		}
	}
	if _, err := encodeKey(pkDef, in.PartitionValue); err != nil {
		return nil, err
	}
	var items []Item
	scanned := 0
	err = s.db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(t.Name))
		match := func(raw []byte) {
			var it Item
			if json.Unmarshal(raw, &it) != nil || expired(t, it) {
				return
			}
			if cmp, ok := compare(it[pkDef.Name], in.PartitionValue); !ok || cmp != 0 {
				return
			}
			if in.SortCondition != nil && skDef != nil {
				sc := *in.SortCondition
				sc.Attr = skDef.Name
				if !sc.eval(it) {
					return
				}
			}
			items = append(items, it)
		}
		if in.Index == "" {
			// Primary key queries walk only the partition's key range.
			pk, _ := encodeKey(pkDef, in.PartitionValue)
			prefix := append(pk, 0)
			cur := bk.Cursor()
			for k, v := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = cur.Next() {
				scanned++
				match(v)
			}
			return nil
		}
		return bk.ForEach(func(k, v []byte) error { scanned++; match(v); return nil })
	})
	if err != nil {
		return nil, err
	}
	if skDef != nil {
		sort.SliceStable(items, func(i, j int) bool {
			c, _ := compare(items[i][skDef.Name], items[j][skDef.Name])
			return c < 0
		})
	}
	if !forward {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}
	return s.page(t, items, in.pageInput, scanned), nil
}

// page applies start key, filter, limit and projection to an ordered result.
func (s *Service) page(t Table, items []Item, in pageInput, scanned int) map[string]any {
	keyOf := func(it Item) string {
		k, _ := t.itemKey(it)
		return string(k)
	}
	if in.StartKey != nil {
		sk := keyOf(in.StartKey)
		for i, it := range items {
			if keyOf(it) == sk {
				items = items[i+1:]
				break
			}
		}
	}
	limit := in.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	out := []Item{}
	var last Item
	evaluated := 0
	for _, it := range items {
		if evaluated >= limit {
			break
		}
		evaluated++
		last = it
		if all(in.Filter, it) {
			out = append(out, project(it, in.Projection))
		}
	}
	res := map[string]any{"items": out, "count": len(out), "scanned_count": scanned}
	if evaluated < len(items) && last != nil {
		lk := Item{t.PartitionKey.Name: last[t.PartitionKey.Name]}
		if t.SortKey != nil {
			lk[t.SortKey.Name] = last[t.SortKey.Name]
		}
		res["last_evaluated_key"] = lk
	}
	return res
}

func (s *Service) scan(c *httpx.Ctx) (any, error) {
	var in pageInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.table(c.Param("name"))
	if err != nil {
		return nil, err
	}
	var items []Item
	err = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(t.Name)).ForEach(func(k, v []byte) error {
			var it Item
			if json.Unmarshal(v, &it) == nil && !expired(t, it) {
				items = append(items, it)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return s.page(t, items, in, len(items)), nil
}

// Run deletes items whose TTL attribute has passed.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, tb := range store.List[Table](s.env.Store, cTables) {
			if tb.TTLAttribute == "" {
				continue
			}
			_ = s.db.Update(func(tx *bolt.Tx) error {
				bk := tx.Bucket([]byte(tb.Name))
				if bk == nil {
					return nil
				}
				var dead [][]byte
				_ = bk.ForEach(func(k, v []byte) error {
					var it Item
					if json.Unmarshal(v, &it) == nil && expired(tb, it) {
						dead = append(dead, append([]byte(nil), k...))
					}
					return nil
				})
				for _, k := range dead {
					_ = bk.Delete(k)
				}
				return nil
			})
		}
	}
}
