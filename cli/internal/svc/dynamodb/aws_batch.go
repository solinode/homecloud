package dynamodb

import (
	"crypto/sha256"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	bolt "go.etcd.io/bbolt"
)

const (
	maxBatchGet   = 100
	maxBatchWrite = 25
	maxTransact   = 100
	batchGetBytes = 16 << 20
)

// ---- BatchGetItem ----

type keysAndAttrs struct {
	Keys                     []Item
	AttributesToGet          []string
	ConsistentRead           bool
	ProjectionExpression     *string
	ExpressionAttributeNames map[string]string
}

func (s *Service) awsBatchGet(q *awsapi.Req) (any, error) {
	var in struct {
		RequestItems           map[string]keysAndAttrs
		ReturnConsumedCapacity string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.RequestItems) == 0 {
		return nil, validation("1 validation error detected: Value null at 'requestItems' failed to satisfy constraint: Member must have length greater than or equal to 1")
	}
	if err := validReturnCapacity(in.ReturnConsumedCapacity); err != nil {
		return nil, err
	}
	total := 0
	for _, r := range in.RequestItems {
		total += len(r.Keys)
	}
	if total > maxBatchGet {
		return nil, validation("Too many items requested for the BatchGetItem call")
	}
	type plan struct {
		table string
		keys  [][]byte
		items []Item
		proj  *projection
		cons  bool
	}
	var plans []plan
	for _, name := range sortedKeys(in.RequestItems) {
		r := in.RequestItems[name]
		if err := q.Authorize("dynamodb:BatchGetItem", s.tableARN(name)); err != nil {
			return nil, err
		}
		if len(r.Keys) == 0 {
			return nil, validation("1 validation error detected: Value '[]' at 'requestItems.%s.member.keys' failed to satisfy constraint: Member must have length greater than or equal to 1", name)
		}
		ctx, err := newExprCtx(r.ExpressionAttributeNames, nil)
		if err != nil {
			return nil, err
		}
		proj, err := buildProjection(ctx, r.ProjectionExpression, r.AttributesToGet)
		if err != nil {
			return nil, err
		}
		if err := ctx.finish(); err != nil {
			return nil, err
		}
		t, err := s.itemTable(name)
		if err != nil {
			return nil, err
		}
		p := plan{table: name, proj: proj, cons: r.ConsistentRead}
		seen := map[string]bool{}
		for _, k := range r.Keys {
			kb, err := t.schema().keyFrom(k)
			if err != nil {
				return nil, err
			}
			if seen[string(kb)] {
				return nil, validation("Provided list of item keys contains duplicates")
			}
			seen[string(kb)] = true
			p.keys = append(p.keys, kb)
			p.items = append(p.items, k)
		}
		plans = append(plans, p)
	}
	responses := map[string][]Item{}
	unprocessed := map[string]any{}
	var caps []map[string]any
	size := 0
	for _, p := range plans {
		got := []Item{}
		var left []Item
		units := 0.0
		for i, k := range p.keys {
			if size >= batchGetBytes {
				left = append(left, p.items[i])
				continue
			}
			it, err := s.getOne(p.table, k)
			if err != nil {
				return nil, err
			}
			units += capacityUnits(itemSize(it), false, p.cons)
			if it != nil {
				size += itemSize(it)
				got = append(got, p.proj.apply(it))
			}
		}
		responses[p.table] = got
		if len(left) > 0 {
			r := in.RequestItems[p.table]
			r.Keys = left
			unprocessed[p.table] = r
		}
		if c := consumed(in.ReturnConsumedCapacity, p.table, units, false, "", false); c != nil {
			caps = append(caps, c)
		}
	}
	res := map[string]any{"Responses": responses, "UnprocessedKeys": unprocessed}
	if caps != nil {
		res["ConsumedCapacity"] = caps
	}
	return res, nil
}

// ---- BatchWriteItem ----

type writeRequest struct {
	PutRequest    *struct{ Item Item }
	DeleteRequest *struct{ Key Item }
}

func (s *Service) awsBatchWrite(q *awsapi.Req) (any, error) {
	var in struct {
		RequestItems                map[string][]writeRequest
		ReturnConsumedCapacity      string
		ReturnItemCollectionMetrics string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.RequestItems) == 0 {
		return nil, validation("1 validation error detected: Value null at 'requestItems' failed to satisfy constraint: Member must have length greater than or equal to 1")
	}
	if err := validReturnCapacity(in.ReturnConsumedCapacity); err != nil {
		return nil, err
	}
	total := 0
	for _, reqs := range in.RequestItems {
		total += len(reqs)
	}
	if total > maxBatchWrite {
		return nil, validation("Too many items requested for the BatchWriteItem call")
	}
	type op struct {
		key []byte
		put Item // nil: delete
	}
	plans := map[string][]op{}
	names := sortedKeys(in.RequestItems)
	for _, name := range names {
		reqs := in.RequestItems[name]
		if err := q.Authorize("dynamodb:BatchWriteItem", s.tableARN(name)); err != nil {
			return nil, err
		}
		if len(reqs) == 0 {
			return nil, validation("1 validation error detected: Value '[]' at 'requestItems.%s.member' failed to satisfy constraint: Member must have length greater than or equal to 1", name)
		}
		t, err := s.itemTable(name)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, r := range reqs {
			var o op
			switch {
			case r.PutRequest != nil && r.DeleteRequest == nil:
				if r.PutRequest.Item == nil {
					return nil, validation("1 validation error detected: Value null at 'requestItems.%s.member.putRequest.item' failed to satisfy constraint: Member must not be null", name)
				}
				if o.key, err = prepItem(t, r.PutRequest.Item); err != nil {
					return nil, err
				}
				o.put = r.PutRequest.Item
			case r.DeleteRequest != nil && r.PutRequest == nil:
				if o.key, err = t.schema().keyFrom(r.DeleteRequest.Key); err != nil {
					return nil, err
				}
			default:
				return nil, validation("Supplied AttributeValue has more than one datatypes set, must contain exactly one of the supported datatypes")
			}
			if seen[string(o.key)] {
				return nil, validation("Provided list of item keys contains duplicates")
			}
			seen[string(o.key)] = true
			plans[name] = append(plans[name], o)
		}
	}
	var caps []map[string]any
	for _, name := range names {
		units := 0.0
		err := s.itemTx(name, func(t *Table, tb *bolt.Bucket) error {
			for _, o := range plans[name] {
				if o.put != nil {
					old, err := s.putTx(tb, t, o.key, o.put, condition{})
					if err != nil {
						return err
					}
					units += capacityUnits(max(itemSize(old), itemSize(o.put)), true, false)
				} else {
					old, err := s.deleteTx(tb, t, o.key, condition{})
					if err != nil {
						return err
					}
					units += capacityUnits(itemSize(old), true, false)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if c := consumed(in.ReturnConsumedCapacity, name, units, true, "", false); c != nil {
			caps = append(caps, c)
		}
	}
	res := map[string]any{"UnprocessedItems": map[string]any{}}
	if caps != nil {
		res["ConsumedCapacity"] = caps
	}
	return res, nil
}

// ---- transactions ----

type txToken struct {
	hash [32]byte
	at   time.Time
	resp any
}

const tokenTTL = 10 * time.Minute

type txCommon struct {
	TableName                           string
	Key                                 Item
	ConditionExpression                 *string
	ReturnValuesOnConditionCheckFailure string
	exprIn
}

type transactWriteItem struct {
	ConditionCheck *txCommon
	Put            *struct {
		txCommon
		Item Item
	}
	Delete *txCommon
	Update *struct {
		txCommon
		UpdateExpression string
	}
}

// txOp is one validated action of a write transaction.
type txOp struct {
	kind  string // ConditionCheck, Put, Delete, Update
	table string
	key   []byte
	keyIt Item
	item  Item
	cond  condition
	upd   *updateExpr
}

func (s *Service) prepTxOp(q *awsapi.Req, w transactWriteItem) (txOp, error) {
	var o txOp
	var c *txCommon
	n := 0
	if w.ConditionCheck != nil {
		n++
		o.kind, c = "ConditionCheck", w.ConditionCheck
	}
	if w.Put != nil {
		n++
		o.kind, c = "Put", &w.Put.txCommon
	}
	if w.Delete != nil {
		n++
		o.kind, c = "Delete", w.Delete
	}
	if w.Update != nil {
		n++
		o.kind, c = "Update", &w.Update.txCommon
	}
	if n != 1 {
		return o, validation("TransactItems can only contain one of Check, Put, Update or Delete")
	}
	actions := map[string]string{"ConditionCheck": "dynamodb:ConditionCheckItem", "Put": "dynamodb:PutItem", "Delete": "dynamodb:DeleteItem", "Update": "dynamodb:UpdateItem"}
	if err := q.Authorize(actions[o.kind], s.tableARN(c.TableName)); err != nil {
		return o, err
	}
	o.table = c.TableName
	ctx, err := c.exprIn.ctx()
	if err != nil {
		return o, err
	}
	if o.kind == "ConditionCheck" && c.ConditionExpression == nil {
		return o, validation("1 validation error detected: Value null at 'transactItems.1.member.conditionCheck.conditionExpression' failed to satisfy constraint: Member must not be null")
	}
	if o.kind == "Update" {
		if o.upd, err = parseUpdate(w.Update.UpdateExpression, ctx); err != nil {
			return o, err
		}
	}
	if o.cond, err = buildCondition(ctx, c.ConditionExpression, nil, "", c.ReturnValuesOnConditionCheckFailure); err != nil {
		return o, err
	}
	if err := ctx.finish(); err != nil {
		return o, err
	}
	t, err := s.itemTable(c.TableName)
	if err != nil {
		return o, err
	}
	if o.kind == "Put" {
		if w.Put.Item == nil {
			return o, validation("1 validation error detected: Value null at 'transactItems.1.member.put.item' failed to satisfy constraint: Member must not be null")
		}
		o.item = w.Put.Item
		o.key, err = prepItem(t, o.item)
	} else {
		o.keyIt = c.Key
		o.key, err = t.schema().keyFrom(c.Key)
	}
	if err != nil {
		return o, err
	}
	if o.upd != nil {
		for _, a := range o.upd.actions {
			if t.schema().has(a.path[0].Name) {
				return o, validation("One or more parameter values were invalid: Cannot update attribute %s. This attribute is part of the key", a.path[0].Name)
			}
		}
	}
	return o, nil
}

func (s *Service) awsTransactWrite(q *awsapi.Req) (any, error) {
	var in struct {
		TransactItems               []transactWriteItem
		ClientRequestToken          string
		ReturnConsumedCapacity      string
		ReturnItemCollectionMetrics string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.TransactItems) == 0 || len(in.TransactItems) > maxTransact {
		return nil, validation("1 validation error detected: Value at 'transactItems' failed to satisfy constraint: Member must have length less than or equal to %d and greater than or equal to 1", maxTransact)
	}
	if err := validReturnCapacity(in.ReturnConsumedCapacity); err != nil {
		return nil, err
	}
	ops := make([]txOp, len(in.TransactItems))
	seen := map[string]bool{}
	for i, w := range in.TransactItems {
		o, err := s.prepTxOp(q, w)
		if err != nil {
			return nil, err
		}
		id := o.table + "\x00" + string(o.key)
		if seen[id] {
			return nil, validation("Transaction request cannot include multiple operations on one item")
		}
		seen[id] = true
		ops[i] = o
	}
	var hash [32]byte
	if in.ClientRequestToken != "" {
		if len(in.ClientRequestToken) > 36 {
			return nil, validation("1 validation error detected: Value at 'clientRequestToken' failed to satisfy constraint: Member must have length less than or equal to 36")
		}
		hash = sha256.Sum256(q.Body)
		s.tokMu.Lock()
		for k, v := range s.tokens {
			if time.Since(v.at) > tokenTTL {
				delete(s.tokens, k)
			}
		}
		prev, ok := s.tokens[in.ClientRequestToken]
		s.tokMu.Unlock()
		if ok {
			if prev.hash != hash {
				return nil, errf("IdempotentParameterMismatchException", "The request uses the same client token as a previous, but non-identical request.")
			}
			return prev.resp, nil
		}
	}
	units := map[string]float64{}
	err := s.transact(ops, units)
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	if in.ReturnConsumedCapacity == "TOTAL" || in.ReturnConsumedCapacity == "INDEXES" {
		var caps []map[string]any
		for _, name := range sortedKeys(units) {
			caps = append(caps, consumed(in.ReturnConsumedCapacity, name, units[name], true, "", false))
		}
		res["ConsumedCapacity"] = caps
	}
	if in.ClientRequestToken != "" {
		s.tokMu.Lock()
		s.tokens[in.ClientRequestToken] = txToken{hash: hash, at: time.Now(), resp: res}
		s.tokMu.Unlock()
	}
	return res, nil
}

// transact applies a write transaction atomically: every condition is checked
// against the state before the transaction, then all writes are applied in one
// bbolt transaction.
func (s *Service) transact(ops []txOp, units map[string]float64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tables := map[string]*Table{}
	for _, o := range ops {
		if tables[o.table] == nil {
			t, err := s.itemTable(o.table)
			if err != nil {
				return err
			}
			tables[o.table] = t
		}
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		buckets := map[string]*bolt.Bucket{}
		for name, t := range tables {
			tb, err := tableBucket(tx, t)
			if err != nil {
				return errNotFoundGeneric
			}
			buckets[name] = tb
		}
		reasons := make([]map[string]any, len(ops))
		olds := make([]Item, len(ops))
		failed := false
		for i, o := range ops {
			reasons[i] = map[string]any{"Code": "None"}
			old, err := getItem(buckets[o.table], o.key)
			if err != nil {
				return err
			}
			olds[i] = old
			if err := o.cond.check(old); err != nil {
				r := map[string]any{"Code": "ConditionalCheckFailed", "Message": "The conditional request failed"}
				if o.cond.returnOld && old != nil {
					r["Item"] = old
				}
				reasons[i] = r
				failed = true
			}
		}
		if !failed {
			for i, o := range ops {
				t, tb := tables[o.table], buckets[o.table]
				var err error
				switch o.kind {
				case "Put":
					err = s.writeItem(tb, t, o.key, olds[i], o.item, nil)
					units[o.table] += 2 * capacityUnits(max(itemSize(o.item), itemSize(olds[i])), true, false)
				case "Delete":
					if olds[i] != nil {
						err = s.writeItem(tb, t, o.key, olds[i], nil, nil)
					}
					units[o.table] += 2 * capacityUnits(itemSize(olds[i]), true, false)
				case "Update":
					base := olds[i]
					if base == nil {
						base = cloneItem(o.keyIt)
					}
					var nu Item
					if nu, err = o.upd.apply(base, t.schema()); err == nil {
						if err = checkIndexKeys(t, nu); err == nil && itemSize(nu) > maxItemBytes {
							err = errItemTooLarge
						}
					}
					if err == nil {
						err = s.writeItem(tb, t, o.key, olds[i], nu, nil)
					}
					units[o.table] += 2 * capacityUnits(max(itemSize(nu), itemSize(olds[i])), true, false)
				case "ConditionCheck":
					units[o.table] += 2 * capacityUnits(itemSize(olds[i]), false, true)
				}
				if err != nil {
					var ae *apiError
					if errors.As(err, &ae) && ae.Code == "ValidationException" {
						reasons[i] = map[string]any{"Code": "ValidationError", "Message": ae.Message}
						failed = true
						break
					}
					return err
				}
			}
		}
		if failed {
			codes := make([]string, len(reasons))
			for i, r := range reasons {
				codes[i] = r["Code"].(string)
			}
			e := errf("TransactionCanceledException", "Transaction cancelled, please refer cancellation reasons for specific reasons [%s]", strings.Join(codes, ", "))
			e.Fields = map[string]any{"CancellationReasons": reasons}
			return e // rolls back the bbolt transaction
		}
		return nil
	})
}

func (s *Service) awsTransactGet(q *awsapi.Req) (any, error) {
	var in struct {
		TransactItems []struct {
			Get *struct {
				TableName                string
				Key                      Item
				ProjectionExpression     *string
				ExpressionAttributeNames map[string]string
			}
		}
		ReturnConsumedCapacity string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.TransactItems) == 0 || len(in.TransactItems) > maxTransact {
		return nil, validation("1 validation error detected: Value at 'transactItems' failed to satisfy constraint: Member must have length less than or equal to %d and greater than or equal to 1", maxTransact)
	}
	type get struct {
		table string
		key   []byte
		proj  *projection
	}
	gets := make([]get, len(in.TransactItems))
	for i, ti := range in.TransactItems {
		g := ti.Get
		if g == nil {
			return nil, validation("1 validation error detected: Value null at 'transactItems.%d.member.get' failed to satisfy constraint: Member must not be null", i+1)
		}
		if err := q.Authorize("dynamodb:GetItem", s.tableARN(g.TableName)); err != nil {
			return nil, err
		}
		ctx, err := newExprCtx(g.ExpressionAttributeNames, nil)
		if err != nil {
			return nil, err
		}
		proj, err := buildProjection(ctx, g.ProjectionExpression, nil)
		if err != nil {
			return nil, err
		}
		if err := ctx.finish(); err != nil {
			return nil, err
		}
		t, err := s.itemTable(g.TableName)
		if err != nil {
			return nil, err
		}
		key, err := t.schema().keyFrom(g.Key)
		if err != nil {
			return nil, err
		}
		gets[i] = get{g.TableName, key, proj}
	}
	// One read transaction gives a consistent snapshot across tables.
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]map[string]any, len(gets))
	units := map[string]float64{}
	err := s.db.View(func(tx *bolt.Tx) error {
		for i, g := range gets {
			t, err := s.itemTable(g.table)
			if err != nil {
				return err
			}
			tb, err := tableBucket(tx, t)
			if err != nil {
				return errNotFoundGeneric
			}
			it, err := getItem(tb, g.key)
			if err != nil {
				return err
			}
			units[g.table] += 2 * capacityUnits(itemSize(it), false, true)
			out[i] = map[string]any{}
			if it != nil {
				out[i]["Item"] = g.proj.apply(it)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res := map[string]any{"Responses": out}
	if in.ReturnConsumedCapacity == "TOTAL" || in.ReturnConsumedCapacity == "INDEXES" {
		names := make([]string, 0, len(units))
		for n := range units {
			names = append(names, n)
		}
		sort.Strings(names)
		var caps []map[string]any
		for _, n := range names {
			caps = append(caps, consumed(in.ReturnConsumedCapacity, n, units[n], false, "", false))
		}
		res["ConsumedCapacity"] = caps
	}
	return res, nil
}
