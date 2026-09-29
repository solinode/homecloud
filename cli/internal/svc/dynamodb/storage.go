package dynamodb

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	bolt "go.etcd.io/bbolt"
)

// Storage layout (bbolt, dynamodb.db):
//
//	table/<name>/items          encoded primary key -> item (DynamoDB JSON)
//	table/<name>/idx/<index>    index key + primary key -> primary key
//	table/<name>/stream/<label> 8-byte sequence number -> stream record
//	table/<name>/meta           counters ("n:<bucket>", "s:<bucket>", "seq:<label>")
//
// Version 1 stored plain JSON documents in a top-level bucket per table with
// float64 number keys; migrate converts it on startup.

// Service implements DynamoDB and DynamoDB Streams.
type Service struct {
	env *svc.Env
	db  *bolt.DB
	// mu orders schema changes (Lock) against item operations (RLock), so an
	// index backfill never misses a concurrent write.
	mu sync.RWMutex
	// Now is the clock (tests override it).
	Now func() time.Time

	tokMu  sync.Mutex
	tokens map[string]txToken // TransactWriteItems idempotency
}

func New(env *svc.Env) (*Service, error) {
	db, err := bolt.Open(env.Cfg.Path("dynamodb.db"), 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open dynamodb store: %w (is another HomeCloud server using %s?)", err, env.Cfg.DataDir)
	}
	s := &Service{env: env, db: db, tokens: map[string]txToken{}}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate dynamodb store: %w", err)
	}
	return s, nil
}

func (s *Service) Close() error { return s.db.Close() }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func tableBucketName(name string) []byte { return []byte("table/" + name) }

var (
	bItems = []byte("items")
	bMeta  = []byte("meta")
)

func idxBucketName(ix string) []byte       { return []byte("idx/" + ix) }
func streamBucketName(label string) []byte { return []byte("stream/" + label) }

func ensureTableBuckets(tx *bolt.Tx, t *Table) (*bolt.Bucket, error) {
	tb, err := tx.CreateBucketIfNotExists(tableBucketName(t.Name))
	if err != nil {
		return nil, err
	}
	for _, n := range [][]byte{bItems, bMeta} {
		if _, err := tb.CreateBucketIfNotExists(n); err != nil {
			return nil, err
		}
	}
	for _, ix := range t.allIndexes() {
		if _, err := tb.CreateBucketIfNotExists(idxBucketName(ix.Name)); err != nil {
			return nil, err
		}
	}
	if st := t.stream(); st != nil {
		if _, err := tb.CreateBucketIfNotExists(streamBucketName(st.Label)); err != nil {
			return nil, err
		}
	}
	return tb, nil
}

func tableBucket(tx *bolt.Tx, t *Table) (*bolt.Bucket, error) {
	tb := tx.Bucket(tableBucketName(t.Name))
	if tb == nil || tb.Bucket(bItems) == nil {
		return nil, tableNotFound(t.Name)
	}
	return tb, nil
}

// ---- counters ----

func counter(tb *bolt.Bucket, name string) int64 {
	m := tb.Bucket(bMeta)
	if m == nil {
		return 0
	}
	v := m.Get([]byte(name))
	if len(v) != 8 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(v))
}

func bump(tb *bolt.Bucket, name string, delta int64) error {
	if delta == 0 {
		return nil
	}
	m, err := tb.CreateBucketIfNotExists(bMeta)
	if err != nil {
		return err
	}
	n := counter(tb, name) + delta
	if n < 0 {
		n = 0
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(n))
	return m.Put([]byte(name), b)
}

// stats returns a table's item count and size in bytes.
func (s *Service) stats(t *Table) (count, size int64) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		if tb := tx.Bucket(tableBucketName(t.Name)); tb != nil {
			count, size = counter(tb, "n:items"), counter(tb, "s:items")
		}
		return nil
	})
	return
}

func (s *Service) indexStats(t *Table, ix string) (count, size int64) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		if tb := tx.Bucket(tableBucketName(t.Name)); tb != nil {
			count, size = counter(tb, "n:idx/"+ix), counter(tb, "s:idx/"+ix)
		}
		return nil
	})
	return
}

// ---- items ----

func getItem(tb *bolt.Bucket, key []byte) (Item, error) {
	raw := tb.Bucket(bItems).Get(key)
	if raw == nil {
		return nil, nil
	}
	return decodeItem(raw)
}

// checkIndexKeys rejects items whose index key attributes have the wrong type
// or are empty.
func checkIndexKeys(t *Table, it Item) error {
	for _, ix := range t.allIndexes() {
		for _, def := range []*KeyDef{&ix.PartitionKey, ix.SortKey} {
			if def == nil {
				continue
			}
			if v, ok := it[def.Name]; ok {
				if err := checkKeyAttr(*def, v, ix.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Identity marks writes made by DynamoDB itself (TTL deletions) in stream records.
type Identity struct {
	Type        string `json:"Type"`
	PrincipalID string `json:"PrincipalId"`
}

var ttlIdentity = &Identity{Type: "Service", PrincipalID: "dynamodb.amazonaws.com"}

// writeItem replaces the item at key (old is its current value, nil if absent)
// with nu (nil deletes it), maintaining indexes, counters and the stream.
func (s *Service) writeItem(tb *bolt.Bucket, t *Table, key []byte, old, nu Item, who *Identity) error {
	items := tb.Bucket(bItems)
	oldSize, newSize := int64(0), int64(0)
	if old != nil {
		oldSize = int64(itemSize(old))
	}
	if nu != nil {
		newSize = int64(itemSize(nu))
		if err := items.Put(key, encodeItem(nu)); err != nil {
			return err
		}
	} else if old != nil {
		if err := items.Delete(key); err != nil {
			return err
		}
	}
	dn := int64(0)
	switch {
	case old == nil && nu != nil:
		dn = 1
	case old != nil && nu == nil:
		dn = -1
	}
	if err := bump(tb, "n:items", dn); err != nil {
		return err
	}
	if err := bump(tb, "s:items", newSize-oldSize); err != nil {
		return err
	}
	for _, ix := range t.allIndexes() {
		ib, err := tb.CreateBucketIfNotExists(idxBucketName(ix.Name))
		if err != nil {
			return err
		}
		var ok, nk []byte
		var hasOld, hasNew bool
		if old != nil {
			ok, hasOld = indexKeyOf(ix.schema(), key, old)
		}
		if nu != nil {
			nk, hasNew = indexKeyOf(ix.schema(), key, nu)
		}
		name := "idx/" + ix.Name
		if hasOld {
			if err := ib.Delete(ok); err != nil {
				return err
			}
			_ = bump(tb, "n:"+name, -1)
			_ = bump(tb, "s:"+name, -int64(itemSize(t.projectFor(&ix, old))))
		}
		if hasNew {
			if err := ib.Put(nk, key); err != nil {
				return err
			}
			_ = bump(tb, "n:"+name, 1)
			_ = bump(tb, "s:"+name, int64(itemSize(t.projectFor(&ix, nu))))
		}
	}
	if st := t.stream(); st != nil && !(old != nil && nu != nil && equalItems(old, nu)) && (old != nil || nu != nil) {
		if err := s.appendStream(tb, t, st, old, nu, who); err != nil {
			return err
		}
	}
	return nil
}

// ---- streams ----

// StreamRecord is a DynamoDB Streams record (also the shape of the "Records"
// entries of a Lambda DynamoDB event, minus eventSourceARN).
type StreamRecord struct {
	EventID      string     `json:"eventID"`
	EventName    string     `json:"eventName"`
	EventVersion string     `json:"eventVersion"`
	EventSource  string     `json:"eventSource"`
	AwsRegion    string     `json:"awsRegion"`
	Dynamodb     StreamData `json:"dynamodb"`
	UserIdentity *Identity  `json:"userIdentity,omitempty"`
}

type StreamData struct {
	ApproximateCreationDateTime float64 `json:"ApproximateCreationDateTime"`
	Keys                        Item    `json:"Keys"`
	NewImage                    Item    `json:"NewImage,omitempty"`
	OldImage                    Item    `json:"OldImage,omitempty"`
	SequenceNumber              string  `json:"SequenceNumber"`
	SizeBytes                   int     `json:"SizeBytes"`
	StreamViewType              string  `json:"StreamViewType"`
}

// Sequence numbers look like AWS's: 21 decimal digits, increasing.
func formatSeq(n uint64) string { return fmt.Sprintf("1%020d", n) }

func parseSeq(s string) (uint64, bool) {
	if len(s) != 21 || s[0] != '1' {
		return 0, false
	}
	v, err := strconv.ParseUint(s[1:], 10, 64)
	return v, err == nil
}

func (s *Service) appendStream(tb *bolt.Bucket, t *Table, st *StreamInfo, old, nu Item, who *Identity) error {
	sb, err := tb.CreateBucketIfNotExists(streamBucketName(st.Label))
	if err != nil {
		return err
	}
	seqName := "seq:" + st.Label
	seq := uint64(counter(tb, seqName)) + 1
	if err := bump(tb, seqName, 1); err != nil {
		return err
	}
	now := s.now()
	rec := StreamRecord{EventID: core.RandHex(32), EventVersion: "1.1", EventSource: "aws:dynamodb", AwsRegion: core.Region, UserIdentity: who}
	switch {
	case old == nil:
		rec.EventName = "INSERT"
	case nu == nil:
		rec.EventName = "REMOVE"
	default:
		rec.EventName = "MODIFY"
	}
	src := nu
	if src == nil {
		src = old
	}
	d := &rec.Dynamodb
	d.ApproximateCreationDateTime = float64(now.Unix())
	d.Keys = t.schema().project(src)
	d.SequenceNumber = formatSeq(seq)
	d.StreamViewType = st.ViewType
	switch st.ViewType {
	case "NEW_IMAGE":
		d.NewImage = nu
	case "OLD_IMAGE":
		d.OldImage = old
	case "NEW_AND_OLD_IMAGES":
		d.NewImage, d.OldImage = nu, old
	}
	d.SizeBytes = itemSize(d.Keys) + itemSize(d.NewImage) + itemSize(d.OldImage)
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, seq)
	return sb.Put(k, b)
}

// ---- TTL and retention ----

// Run expires items whose TTL has passed and trims stream records older than
// 24 hours, like DynamoDB's background processes.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.Sweep()
	}
}

// Sweep runs one pass of TTL expiry and stream trimming.
func (s *Service) Sweep() {
	defer core.Recover("dynamodb sweep")
	for _, tb := range store.List[Table](s.env.Store, cTables) {
		t := tb
		if t.TTLAttribute != "" {
			if err := s.expire(&t); err != nil {
				log.Printf("dynamodb: ttl %s: %v", t.Name, err)
			}
		}
		s.trimStreams(&t)
	}
}

func (s *Service) expire(t *Table) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cur, err := s.getTable(t.Name)
	if err != nil || cur.TTLAttribute == "" {
		return nil
	}
	now := s.now().Unix()
	oldest := now - 5*365*24*3600 // DynamoDB ignores expiry times more than five years in the past
	return s.db.Update(func(tx *bolt.Tx) error {
		tb, err := tableBucket(tx, cur)
		if err != nil {
			return nil
		}
		type dead struct {
			k  []byte
			it Item
		}
		var gone []dead
		err = tb.Bucket(bItems).ForEach(func(k, v []byte) error {
			if !bytes.Contains(v, []byte(cur.TTLAttribute)) {
				return nil
			}
			it, err := decodeItem(v)
			if err != nil {
				return nil
			}
			a, ok := it[cur.TTLAttribute]
			if !ok || a.Kind != kN {
				return nil
			}
			d, err := parseDecimal(a.S)
			if err != nil {
				return nil
			}
			f, _ := strconv.ParseFloat(d.String(), 64)
			if int64(f) < now && int64(f) > oldest {
				gone = append(gone, dead{append([]byte(nil), k...), it})
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, g := range gone {
			if err := s.writeItem(tb, cur, g.k, g.it, nil, ttlIdentity); err != nil {
				return err
			}
		}
		return nil
	})
}

const streamRetention = 24 * time.Hour

func (s *Service) trimStreams(t *Table) {
	if len(t.Streams) == 0 {
		return
	}
	cutoff := float64(s.now().Add(-streamRetention).Unix())
	_ = s.db.Update(func(tx *bolt.Tx) error {
		tb := tx.Bucket(tableBucketName(t.Name))
		if tb == nil {
			return nil
		}
		for _, st := range t.Streams {
			sb := tb.Bucket(streamBucketName(st.Label))
			if sb == nil {
				continue
			}
			c := sb.Cursor()
			for k, v := c.First(); k != nil; k, v = c.First() {
				var rec struct {
					Dynamodb struct{ ApproximateCreationDateTime float64 }
				}
				if json.Unmarshal(v, &rec) == nil && rec.Dynamodb.ApproximateCreationDateTime >= cutoff {
					break
				}
				if err := sb.Delete(k); err != nil {
					return err
				}
			}
		}
		return nil
	})
	// Forget disabled streams once their records have expired.
	var keep []StreamInfo
	changed := false
	for _, st := range t.Streams {
		if st.Disabled != nil && s.now().Sub(*st.Disabled) > streamRetention {
			changed = true
			continue
		}
		keep = append(keep, st)
	}
	if changed {
		_, _ = s.alterTable(t.Name, func(cur *Table, tx *bolt.Tx) error {
			cur.Streams = keep
			tb := tx.Bucket(tableBucketName(cur.Name))
			for _, st := range t.Streams {
				if st.Disabled != nil && s.now().Sub(*st.Disabled) > streamRetention && tb != nil && tb.Bucket(streamBucketName(st.Label)) != nil {
					_ = tb.DeleteBucket(streamBucketName(st.Label))
				}
			}
			return nil
		})
	}
}

// ---- migration from version 1 ----

func (s *Service) migrate() error {
	for _, t := range store.List[Table](s.env.Store, cTables) {
		if t.Version >= storeVersion {
			continue
		}
		t := t
		if err := s.migrateTable(&t); err != nil {
			return fmt.Errorf("table %s: %w", t.Name, err)
		}
	}
	return nil
}

func (s *Service) migrateTable(t *Table) error {
	if t.PartitionKey.Type == "" {
		t.PartitionKey.Type = "S"
	}
	for i := range t.Indexes {
		if t.Indexes[i].Status == "" {
			t.Indexes[i].Status = "ACTIVE"
		}
		if t.Indexes[i].Projection == nil {
			t.Indexes[i].Projection = &Projection{Type: "ALL"}
		}
	}
	if err := t.validateSchema(); err != nil {
		return err
	}
	if t.ID == "" {
		t.ID = newTableID()
	}
	if t.TableClass == "" {
		t.TableClass = "STANDARD"
	}
	migrated := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		tb, err := ensureTableBuckets(tx, t)
		if err != nil {
			return err
		}
		old := tx.Bucket([]byte(t.Name))
		if old == nil {
			return nil
		}
		err = old.ForEach(func(k, v []byte) error {
			if v == nil {
				return nil // nested bucket
			}
			dec := json.NewDecoder(bytes.NewReader(v))
			dec.UseNumber()
			var plain map[string]any
			if err := dec.Decode(&plain); err != nil {
				log.Printf("dynamodb: migrate %s: skipping undecodable item: %v", t.Name, err)
				return nil
			}
			it, err := itemFromPlain(plain)
			if err != nil {
				log.Printf("dynamodb: migrate %s: skipping item: %v", t.Name, err)
				return nil
			}
			key, err := t.schema().keyOf(it)
			if err != nil {
				log.Printf("dynamodb: migrate %s: skipping item: %v", t.Name, err)
				return nil
			}
			prev, err := getItem(tb, key)
			if err != nil {
				return err
			}
			migrated++
			return s.writeItem(tb, t, key, prev, it, nil)
		})
		if err != nil {
			return err
		}
		return tx.DeleteBucket([]byte(t.Name))
	})
	if err != nil {
		return err
	}
	if migrated > 0 {
		log.Printf("dynamodb: migrated %d items of table %s to storage version %d", migrated, t.Name, storeVersion)
	}
	t.Version = storeVersion
	return store.Put(s.env.Store, cTables, t.Name, *t)
}
