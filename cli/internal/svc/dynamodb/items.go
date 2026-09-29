package dynamodb

import (
	"bytes"

	bolt "go.etcd.io/bbolt"
)

// Item operations shared by the AWS and native APIs. Callers hold s.mu.RLock
// (via s.itemTx / s.itemView) so the table's schema cannot change underneath.

func orEmpty(it Item) Item {
	if it == nil {
		return Item{}
	}
	return it
}

var errItemTooLarge = validation("Item size has exceeded the maximum allowed size")

// prepItem validates an item to be written and returns its primary key.
func prepItem(t *Table, it Item) ([]byte, error) {
	if err := validateItem(it); err != nil {
		return nil, err
	}
	key, err := t.schema().keyOf(it)
	if err != nil {
		return nil, err
	}
	if err := checkIndexKeys(t, it); err != nil {
		return nil, err
	}
	if itemSize(it) > maxItemBytes {
		return nil, errItemTooLarge
	}
	return key, nil
}

// condition is a parsed ConditionExpression and what to return when it fails.
type condition struct {
	expr      *expr
	returnOld bool // ReturnValuesOnConditionCheckFailure=ALL_OLD
}

func (c condition) check(old Item) error {
	if c.expr != nil && !evalCond(c.expr, orEmpty(old)) {
		return conditionFailed(old, c.returnOld)
	}
	return nil
}

// itemTx runs fn in a write transaction on the table's storage.
func (s *Service) itemTx(name string, fn func(t *Table, tb *bolt.Bucket) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, err := s.getTable(name)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		tb, err := tableBucket(tx, t)
		if err != nil {
			return err
		}
		return fn(t, tb)
	})
}

func (s *Service) putTx(tb *bolt.Bucket, t *Table, key []byte, it Item, c condition) (Item, error) {
	old, err := getItem(tb, key)
	if err != nil {
		return nil, err
	}
	if err := c.check(old); err != nil {
		return nil, err
	}
	return old, s.writeItem(tb, t, key, old, it, nil)
}

// updater computes the new item from the old one (or from the key when the
// item does not exist).
type updater func(base Item) (Item, error)

func (s *Service) updateTx(tb *bolt.Bucket, t *Table, keyItem Item, key []byte, upd updater, c condition) (old, nu Item, err error) {
	if old, err = getItem(tb, key); err != nil {
		return nil, nil, err
	}
	if err := c.check(old); err != nil {
		return nil, nil, err
	}
	base := old
	if base == nil {
		base = cloneItem(keyItem)
	}
	if upd != nil {
		if nu, err = upd(base); err != nil {
			return nil, nil, err
		}
	} else {
		nu = cloneItem(base)
	}
	if err := checkIndexKeys(t, nu); err != nil {
		return nil, nil, err
	}
	if itemSize(nu) > maxItemBytes {
		return nil, nil, errItemTooLarge
	}
	return old, nu, s.writeItem(tb, t, key, old, nu, nil)
}

func (s *Service) deleteTx(tb *bolt.Bucket, t *Table, key []byte, c condition) (Item, error) {
	old, err := getItem(tb, key)
	if err != nil {
		return nil, err
	}
	if err := c.check(old); err != nil {
		return nil, err
	}
	if old == nil {
		return nil, nil
	}
	return old, s.writeItem(tb, t, key, old, nil, nil)
}

// exprUpdater applies an update expression.
func exprUpdater(u *updateExpr, ks schema) updater {
	return func(base Item) (Item, error) { return u.apply(base, ks) }
}

// ---- reads ----

const pageBytes = 1 << 20

type readReq struct {
	index       string
	kc          *keyCond // nil for scans
	filter      *expr
	proj        *projection
	allAttrs    bool // LSI query with Select=ALL_ATTRIBUTES: read the full item
	countOnly   bool
	forward     bool
	limit       int
	start       Item
	segment     int
	segments    int // 0: not a parallel scan
	byteLimit   int
	nativeLimit bool // native API: no 1MB page limit
}

type readRes struct {
	items   []Item
	count   int
	scanned int
	bytes   int
	last    Item
}

// read runs a query (kc != nil) or scan over the table or one of its indexes.
func (s *Service) read(name string, r readReq) (readRes, error) {
	var res readRes
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, err := s.getTable(name)
	if err != nil {
		return res, err
	}
	var ix *Index
	ks := t.schema()
	if r.index != "" {
		if ix, _ = t.index(r.index); ix == nil {
			return res, validation("The table does not have the specified index: %s", r.index)
		}
		ks = ix.schema()
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		tb, err := tableBucket(tx, t)
		if err != nil {
			return err
		}
		items := tb.Bucket(bItems)
		b := items
		if ix != nil {
			if b = tb.Bucket(idxBucketName(ix.Name)); b == nil {
				return validation("The table does not have the specified index: %s", r.index)
			}
		}
		var prefix, lower []byte
		if r.kc != nil {
			prefix = appendComponent(nil, r.kc.PK)
			lower = append(append([]byte(nil), prefix...), r.kc.lowerBound()...)
		}
		var startKey []byte
		if r.start != nil {
			if startKey, err = s.startKey(t, ix, r.start); err != nil {
				return err
			}
			if prefix != nil && !bytes.HasPrefix(startKey, prefix) {
				return validation("The provided starting key is invalid: The provided key element does not match the schema")
			}
		}
		c := b.Cursor()
		var k, v []byte
		if r.forward {
			if startKey != nil && bytes.Compare(startKey, lower) >= 0 {
				k, v = c.Seek(startKey)
				if bytes.Equal(k, startKey) {
					k, v = c.Next()
				}
			} else if lower != nil {
				k, v = c.Seek(lower)
			} else {
				k, v = c.First()
			}
		} else {
			switch {
			case startKey != nil:
				if k, _ = c.Seek(startKey); k == nil {
					k, v = c.Last()
				} else {
					k, v = c.Prev()
				}
			case prefix != nil:
				end := prefixSuccessor(prefix)
				if end == nil {
					k, v = c.Last()
				} else if k, _ = c.Seek(end); k == nil {
					k, v = c.Last()
				} else {
					k, v = c.Prev()
				}
			default:
				k, v = c.Last()
			}
		}
		step := func() {
			if r.forward {
				k, v = c.Next()
			} else {
				k, v = c.Prev()
			}
		}
		stopped := false
		for ; k != nil; step() {
			if prefix != nil && !bytes.HasPrefix(k, prefix) {
				break
			}
			raw := v
			if ix != nil {
				raw = items.Get(v)
				if raw == nil {
					continue
				}
			}
			it, err := decodeItem(raw)
			if err != nil {
				return err
			}
			if r.kc != nil && r.kc.SKOp != "" && ks.SK != nil {
				m := r.kc.skMatch(it[ks.SK.Name])
				if !r.forward {
					m = -m
				}
				if m < 0 {
					continue
				}
				if m > 0 {
					break
				}
			}
			if r.segments > 0 && segmentOf(k, r.segments) != r.segment {
				continue
			}
			visible := it
			if ix != nil && !r.allAttrs {
				visible = t.projectFor(ix, it)
			}
			res.scanned++
			res.bytes += itemSize(visible)
			res.last = visible
			if r.filter == nil || evalCond(r.filter, visible) {
				res.count++
				if !r.countOnly {
					res.items = append(res.items, r.proj.apply(visible))
				}
			}
			if (r.limit > 0 && res.scanned >= r.limit) || (!r.nativeLimit && res.bytes >= pageBytes) {
				stopped = true
				break
			}
		}
		if stopped {
			step()
			more := k != nil && (prefix == nil || bytes.HasPrefix(k, prefix))
			if !more {
				res.last = nil
			}
		} else {
			res.last = nil
		}
		return nil
	})
	if res.last != nil {
		lk := t.schema().project(res.last)
		if ix != nil {
			for n, v := range ix.schema().project(res.last) {
				lk[n] = v
			}
		}
		res.last = lk
	}
	return res, err
}

// startKey encodes an ExclusiveStartKey for the table or index.
func (s *Service) startKey(t *Table, ix *Index, start Item) ([]byte, error) {
	bad := validation("The provided starting key is invalid: The provided key element does not match the schema")
	if err := validateItem(start); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range t.schema().names() {
		want[n] = true
	}
	if ix != nil {
		for _, n := range ix.schema().names() {
			want[n] = true
		}
	}
	if len(start) != len(want) {
		return nil, bad
	}
	for n := range want {
		if _, ok := start[n]; !ok {
			return nil, bad
		}
	}
	tk, err := t.schema().keyOf(start)
	if err != nil {
		return nil, bad
	}
	if ix == nil {
		return tk, nil
	}
	k, ok := indexKeyOf(ix.schema(), tk, start)
	if !ok {
		return nil, bad
	}
	return k, nil
}
