// Package dynamodb implements DynamoDB tables on an embedded bbolt database,
// served over the AWS DynamoDB and DynamoDB Streams JSON protocols (aws*.go,
// streams.go) and HomeCloud's native REST API (this file).
//
// Items are stored as typed DynamoDB attribute values (attr.go) under
// order-preserving keys (keys.go), with materialized secondary indexes and
// per-table change streams (storage.go). The native API speaks plain JSON:
// strings, numbers, booleans, null, arrays and objects map to S, N, BOOL,
// NULL, L and M; sets and binary values are returned as arrays and base64.
package dynamodb

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	bolt "go.etcd.io/bbolt"
)

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:dynamodb:{region}:{account}:table/{name}")
	h := func(fn httpx.Handler) httpx.Handler {
		return func(c *httpx.Ctx) (any, error) {
			v, err := fn(c)
			return v, toNativeError(err)
		}
	}
	r.Handle("GET /api/v1/dynamodb/tables", "dynamodb:ListTables", h(s.listTables))
	r.Handle("POST /api/v1/dynamodb/tables", "dynamodb:CreateTable", h(s.nativeCreateTable))
	r.Handle("GET /api/v1/dynamodb/tables/{name}", "dynamodb:DescribeTable", h(s.nativeDescribe), res)
	r.Handle("PATCH /api/v1/dynamodb/tables/{name}", "dynamodb:UpdateTable", h(s.nativeUpdateTable), res)
	r.Handle("DELETE /api/v1/dynamodb/tables/{name}", "dynamodb:DeleteTable", h(s.nativeDeleteTable), res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/items", "dynamodb:PutItem", h(s.nativePutItem), res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/items/get", "dynamodb:GetItem", h(s.nativeGetItem), res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/items/update", "dynamodb:UpdateItem", h(s.nativeUpdateItem), res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/items/delete", "dynamodb:DeleteItem", h(s.nativeDeleteItem), res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/batch-write", "dynamodb:BatchWriteItem", h(s.nativeBatchWrite), res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/query", "dynamodb:Query", h(s.nativeQuery), res)
	r.Handle("POST /api/v1/dynamodb/tables/{name}/scan", "dynamodb:Scan", h(s.nativeScan), res)
}

func toNativeError(err error) error {
	var ae *apiError
	if err != nil && errors.As(err, &ae) {
		status := ae.Status
		if ae.Code == "ResourceNotFoundException" {
			status = http.StatusNotFound
		}
		if ae.Code == "ResourceInUseException" {
			status = http.StatusConflict
		}
		return core.Errf(status, ae.Code, "%s", ae.Message)
	}
	return err
}

// view is the native representation of a table.
func (s *Service) view(t *Table) map[string]any {
	n, size := s.stats(t)
	b, _ := json.Marshal(t)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	delete(m, "storage_version")
	m["item_count"], m["size_bytes"] = n, size
	if st := t.stream(); st != nil {
		m["stream_arn"] = t.streamARN(st.Label)
		m["stream_view_type"] = st.ViewType
	}
	return m
}

func (s *Service) listTables(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, t := range store.List[Table](s.env.Store, cTables) {
		t := t
		out = append(out, s.view(&t))
	}
	return out, nil
}

func (s *Service) nativeCreateTable(c *httpx.Ctx) (any, error) {
	var in struct {
		Table
		StreamViewType string `json:"stream_view_type"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t := in.Table
	t.Attributes = nil
	t.Streams = nil
	if in.StreamViewType != "" {
		if err := validStreamSpec(&streamSpec{StreamEnabled: true, StreamViewType: in.StreamViewType}); err != nil {
			return nil, err
		}
		t.Streams = []StreamInfo{{ViewType: in.StreamViewType}}
	}
	t.BillingMode = "PAY_PER_REQUEST"
	for _, k := range append([]*KeyDef{&t.PartitionKey}, t.SortKey) {
		if k != nil && k.Type == "" {
			k.Type = "S"
		}
	}
	for i := range t.Indexes {
		for _, k := range []*KeyDef{&t.Indexes[i].PartitionKey, t.Indexes[i].SortKey} {
			if k != nil && k.Type == "" {
				k.Type = "S"
			}
		}
	}
	if err := s.createTable(&t); err != nil {
		return nil, err
	}
	return s.view(&t), nil
}

func (s *Service) nativeDescribe(c *httpx.Ctx) (any, error) {
	t, err := s.getTable(c.Param("name"))
	if err != nil {
		return nil, err
	}
	return s.view(t), nil
}

func (s *Service) nativeUpdateTable(c *httpx.Ctx) (any, error) {
	var in struct {
		TTLAttribute   *string   `json:"ttl_attribute"`
		AddIndex       *Index    `json:"add_index"`
		RemoveIndex    string    `json:"remove_index"`
		Tags           core.Tags `json:"tags"`
		StreamViewType *string   `json:"stream_view_type"` // "" disables the stream
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.alterTable(c.Param("name"), func(t *Table, tx *bolt.Tx) error {
		if in.TTLAttribute != nil {
			t.TTLAttribute = *in.TTLAttribute
		}
		if in.AddIndex != nil {
			ix := *in.AddIndex
			for _, k := range []*KeyDef{&ix.PartitionKey, ix.SortKey} {
				if k != nil && k.Type == "" {
					k.Type = "S"
				}
			}
			ix.Status = ""
			if err := s.addIndex(t, tx, ix); err != nil {
				if ae, ok := err.(*apiError); ok && strings.Contains(ae.Message, "already exists") {
					return core.Conflict("index %q already exists", ix.Name)
				}
				return err
			}
		}
		if in.RemoveIndex != "" {
			if err := s.removeIndex(t, tx, in.RemoveIndex); err != nil {
				return err
			}
		}
		if in.Tags != nil {
			t.Tags = in.Tags
		}
		if in.StreamViewType != nil {
			if *in.StreamViewType == "" {
				if t.stream() != nil {
					return s.setStream(t, tx, false, "")
				}
			} else {
				if err := validStreamSpec(&streamSpec{StreamEnabled: true, StreamViewType: *in.StreamViewType}); err != nil {
					return err
				}
				if st := t.stream(); st != nil {
					if st.ViewType == *in.StreamViewType {
						return nil
					}
					if err := s.setStream(t, tx, false, ""); err != nil {
						return err
					}
				}
				return s.setStream(t, tx, true, *in.StreamViewType)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.view(t), nil
}

func (s *Service) nativeDeleteTable(c *httpx.Ctx) (any, error) {
	_, _, _, err := s.deleteTable(c.Param("name"))
	return nil, err
}

// ---- conversions ----

// Condition is a native-API attribute comparison used by filters and
// conditional writes.
type Condition struct {
	Attr   string     `json:"attr"`
	Op     string     `json:"op"` // eq ne lt le gt ge begins_with contains exists not_exists between
	Value  PlainValue `json:"value"`
	Value2 PlainValue `json:"value2"`
}

func (c Condition) toExpr() (*expr, error) {
	if c.Attr == "" {
		return nil, validation("condition attribute is required")
	}
	p := pathExpr(c.Attr)
	val := func(x PlainValue) (*expr, error) {
		v, err := fromPlain(x.V)
		if err != nil {
			return nil, err
		}
		return valExpr(v), nil
	}
	switch c.Op {
	case "exists":
		return &expr{kind: eFunc, op: "attribute_exists", args: []*expr{p}}, nil
	case "not_exists":
		return &expr{kind: eFunc, op: "attribute_not_exists", args: []*expr{p}}, nil
	}
	v, err := val(c.Value)
	if err != nil {
		return nil, err
	}
	switch c.Op {
	case "eq", "":
		return &expr{kind: eCmp, op: "=", args: []*expr{p, v}}, nil
	case "ne", "lt", "le", "gt", "ge":
		sym := map[string]string{"ne": "<>", "lt": "<", "le": "<=", "gt": ">", "ge": ">="}[c.Op]
		return &expr{kind: eCmp, op: sym, args: []*expr{p, v}}, nil
	case "between":
		v2, err := val(c.Value2)
		if err != nil {
			return nil, err
		}
		return &expr{kind: eBetween, args: []*expr{p, v, v2}}, nil
	case "begins_with", "contains":
		return &expr{kind: eFunc, op: c.Op, args: []*expr{p, v}}, nil
	}
	return nil, validation("unknown condition operator %q", c.Op)
}

func conditionsExpr(conds []Condition) (*expr, error) {
	var out *expr
	for _, c := range conds {
		e, err := c.toExpr()
		if err != nil {
			return nil, err
		}
		out = combine(out, e, "AND")
	}
	return out, nil
}

// coerceKey converts a plain key value to the key's declared type when the
// conversion is lossless ("42" for an N key, 42 for an S key).
func coerceKey(def KeyDef, v AV) AV {
	switch {
	case def.Type == "N" && v.Kind == kS:
		if d, err := parseNumber(strings.TrimSpace(v.S)); err == nil {
			return Num(d.String())
		}
	case def.Type == "S" && v.Kind == kN:
		return Str(v.S)
	}
	return v
}

func coerceKeys(it Item, defs ...*KeyDef) {
	for _, d := range defs {
		if d == nil {
			continue
		}
		if v, ok := it[d.Name]; ok {
			it[d.Name] = coerceKey(*d, v)
		}
	}
}

func plainToItem(p PlainItem) (Item, error) {
	if p == nil {
		return nil, validation("item is required")
	}
	return itemFromPlain(p)
}

func (s *Service) nativeKey(t *Table, p PlainItem) (Item, []byte, error) {
	if p == nil {
		return nil, nil, validation("key is required")
	}
	key, err := itemFromPlain(p)
	if err != nil {
		return nil, nil, err
	}
	coerceKeys(key, &t.PartitionKey, t.SortKey)
	kb, err := t.schema().keyFrom(key)
	return key, kb, err
}

func plainItems(items []Item) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i] = itemToPlain(it)
	}
	return out
}

// ---- items ----

func (s *Service) nativePutItem(c *httpx.Ctx) (any, error) {
	var in struct {
		Item      PlainItem   `json:"item"`
		Condition []Condition `json:"condition"`
		ReturnOld bool        `json:"return_old"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.getTable(c.Param("name"))
	if err != nil {
		return nil, err
	}
	it, err := plainToItem(in.Item)
	if err != nil {
		return nil, err
	}
	coerceKeys(it, &t.PartitionKey, t.SortKey)
	cond, err := conditionsExpr(in.Condition)
	if err != nil {
		return nil, err
	}
	key, err := prepItem(t, it)
	if err != nil {
		return nil, err
	}
	var old Item
	err = s.itemTx(t.Name, func(t *Table, tb *bolt.Bucket) error {
		old, err = s.putTx(tb, t, key, it, condition{expr: cond})
		return err
	})
	if err != nil {
		return nil, err
	}
	if in.ReturnOld {
		return map[string]any{"old_item": itemToPlain(old)}, nil
	}
	return map[string]any{}, nil
}

func (s *Service) nativeGetItem(c *httpx.Ctx) (any, error) {
	var in struct {
		Key        PlainItem `json:"key"`
		Projection []string  `json:"projection"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.getTable(c.Param("name"))
	if err != nil {
		return nil, err
	}
	_, kb, err := s.nativeKey(t, in.Key)
	if err != nil {
		return nil, err
	}
	it, err := s.getOne(t.Name, kb)
	if err != nil {
		return nil, err
	}
	if it == nil {
		return map[string]any{"item": nil}, nil
	}
	var proj *projection
	if len(in.Projection) > 0 {
		proj = projectionOf(in.Projection)
	}
	return map[string]any{"item": itemToPlain(proj.apply(it))}, nil
}

func (s *Service) nativeUpdateItem(c *httpx.Ctx) (any, error) {
	var in struct {
		Key       PlainItem             `json:"key"`
		Set       map[string]PlainValue `json:"set"`
		Remove    []string              `json:"remove"`
		Add       map[string]PlainValue `json:"add"`
		Condition []Condition           `json:"condition"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.getTable(c.Param("name"))
	if err != nil {
		return nil, err
	}
	key, kb, err := s.nativeKey(t, in.Key)
	if err != nil {
		return nil, err
	}
	u := &updateExpr{}
	for _, a := range sortedKeys(in.Set) {
		v, err := fromPlain(in.Set[a].V)
		if err != nil {
			return nil, err
		}
		u.actions = append(u.actions, updateAction{kind: "SET", path: docPath{{Name: a}}, val: valExpr(v)})
	}
	for _, a := range in.Remove {
		u.actions = append(u.actions, updateAction{kind: "REMOVE", path: docPath{{Name: a}}})
	}
	for _, a := range sortedKeys(in.Add) {
		v, err := fromPlain(in.Add[a].V)
		if err != nil {
			return nil, err
		}
		if v.Kind != kN {
			return nil, validation("add values must be numbers")
		}
		u.actions = append(u.actions, updateAction{kind: "ADD", path: docPath{{Name: a}}, val: valExpr(v)})
	}
	for _, a := range u.actions {
		if t.schema().has(a.path[0].Name) {
			return nil, validation("key attribute %q cannot be updated", a.path[0].Name)
		}
	}
	paths := make([]docPath, len(u.actions))
	for i, a := range u.actions {
		paths[i] = a.path
	}
	if err := checkPaths(updWhat, paths); err != nil {
		return nil, err
	}
	cond, err := conditionsExpr(in.Condition)
	if err != nil {
		return nil, err
	}
	var fn updater
	if len(u.actions) > 0 {
		fn = exprUpdater(u, t.schema())
	}
	var nu Item
	err = s.itemTx(t.Name, func(t *Table, tb *bolt.Bucket) error {
		_, nu, err = s.updateTx(tb, t, key, kb, fn, condition{expr: cond})
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"item": itemToPlain(nu)}, nil
}

func (s *Service) nativeDeleteItem(c *httpx.Ctx) (any, error) {
	var in struct {
		Key       PlainItem   `json:"key"`
		Condition []Condition `json:"condition"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.getTable(c.Param("name"))
	if err != nil {
		return nil, err
	}
	_, kb, err := s.nativeKey(t, in.Key)
	if err != nil {
		return nil, err
	}
	cond, err := conditionsExpr(in.Condition)
	if err != nil {
		return nil, err
	}
	var old Item
	err = s.itemTx(t.Name, func(t *Table, tb *bolt.Bucket) error {
		old, err = s.deleteTx(tb, t, kb, condition{expr: cond})
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"old_item": itemToPlain(old)}, nil
}

func (s *Service) nativeBatchWrite(c *httpx.Ctx) (any, error) {
	var in struct {
		Puts    []PlainItem `json:"puts"`
		Deletes []PlainItem `json:"deletes"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Puts)+len(in.Deletes) > 1000 {
		return nil, validation("a batch holds at most 1000 writes")
	}
	t, err := s.getTable(c.Param("name"))
	if err != nil {
		return nil, err
	}
	type op struct {
		key []byte
		it  Item
	}
	var puts, dels []op
	for _, p := range in.Puts {
		it, err := plainToItem(p)
		if err != nil {
			return nil, err
		}
		coerceKeys(it, &t.PartitionKey, t.SortKey)
		k, err := prepItem(t, it)
		if err != nil {
			return nil, err
		}
		puts = append(puts, op{k, it})
	}
	for _, p := range in.Deletes {
		_, k, err := s.nativeKey(t, p)
		if err != nil {
			return nil, err
		}
		dels = append(dels, op{key: k})
	}
	err = s.itemTx(t.Name, func(t *Table, tb *bolt.Bucket) error {
		for _, o := range puts {
			if _, err := s.putTx(tb, t, o.key, o.it, condition{}); err != nil {
				return err
			}
		}
		for _, o := range dels {
			if _, err := s.deleteTx(tb, t, o.key, condition{}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]int{"written": len(puts), "deleted": len(dels)}, nil
}

type pageInput struct {
	Limit      int         `json:"limit"`
	StartKey   PlainItem   `json:"start_key"`
	Filter     []Condition `json:"filter"`
	Projection []string    `json:"projection"`
}

func (s *Service) nativeRead(t *Table, in pageInput, r readReq, ix *Index) (any, error) {
	var err error
	if r.filter, err = conditionsExpr(in.Filter); err != nil {
		return nil, err
	}
	if len(in.Projection) > 0 {
		r.proj = projectionOf(in.Projection)
	}
	r.limit = in.Limit
	if r.limit <= 0 || r.limit > 1000 {
		r.limit = 1000
	}
	r.nativeLimit = true
	if in.StartKey != nil {
		start, err := itemFromPlain(in.StartKey)
		if err != nil {
			return nil, err
		}
		coerceKeys(start, &t.PartitionKey, t.SortKey)
		if ix != nil {
			coerceKeys(start, &ix.PartitionKey, ix.SortKey)
		}
		r.start = start
	}
	res, err := s.read(t.Name, r)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"items": plainItems(res.items), "count": res.count, "scanned_count": res.scanned}
	if res.last != nil {
		out["last_evaluated_key"] = itemToPlain(res.last)
	}
	return out, nil
}

func (s *Service) nativeQuery(c *httpx.Ctx) (any, error) {
	var in struct {
		pageInput
		Index          string     `json:"index"`
		PartitionValue PlainValue `json:"partition_value"`
		SortCondition  *Condition `json:"sort_condition"`
		Forward        *bool      `json:"forward"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.getTable(c.Param("name"))
	if err != nil {
		return nil, err
	}
	ks := t.schema()
	var ix *Index
	if in.Index != "" {
		if ix, _ = t.index(in.Index); ix == nil {
			return nil, validation("index %q does not exist", in.Index)
		}
		ks = ix.schema()
	}
	pv, err := fromPlain(in.PartitionValue.V)
	if err != nil {
		return nil, err
	}
	kc := keyCond{PK: coerceKey(ks.PK, pv)}
	if err := checkKeyAttr(ks.PK, kc.PK, ""); err != nil {
		return nil, err
	}
	if sc := in.SortCondition; sc != nil && ks.SK != nil {
		ops := map[string]string{"eq": "=", "": "=", "lt": "<", "le": "<=", "gt": ">", "ge": ">=", "between": "BETWEEN", "begins_with": "begins_with"}
		op, ok := ops[sc.Op]
		if !ok {
			return nil, validation("sort_condition op must be eq, lt, le, gt, ge, between or begins_with")
		}
		v1, err := fromPlain(sc.Value.V)
		if err != nil {
			return nil, err
		}
		kc.SKOp, kc.SK1 = op, coerceKey(*ks.SK, v1)
		if op == "BETWEEN" {
			v2, err := fromPlain(sc.Value2.V)
			if err != nil {
				return nil, err
			}
			kc.SK2 = coerceKey(*ks.SK, v2)
		}
		for _, v := range []AV{kc.SK1, kc.SK2} {
			if v.Kind != kInvalid && v.Kind.String() != ks.SK.Type {
				return nil, validation("sort key %q is of type %s", ks.SK.Name, ks.SK.Type)
			}
		}
		if op == "begins_with" && ks.SK.Type == "N" {
			return nil, validation("begins_with needs a string or binary sort key")
		}
	}
	r := readReq{index: in.Index, kc: &kc, forward: in.Forward == nil || *in.Forward}
	return s.nativeRead(t, in.pageInput, r, ix)
}

func (s *Service) nativeScan(c *httpx.Ctx) (any, error) {
	var in struct {
		pageInput
		Index string `json:"index"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := s.getTable(c.Param("name"))
	if err != nil {
		return nil, err
	}
	var ix *Index
	if in.Index != "" {
		if ix, _ = t.index(in.Index); ix == nil {
			return nil, validation("index %q does not exist", in.Index)
		}
	}
	return s.nativeRead(t, in.pageInput, readReq{index: in.Index, forward: true}, ix)
}
