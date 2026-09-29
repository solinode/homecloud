package dynamodb

import (
	"fmt"
	"sync"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
	bolt "go.etcd.io/bbolt"
)

// Concurrent atomic counters stay exact, and an index created while writes are
// running contains every item.
func TestConcurrentWritesAndBackfill(t *testing.T) {
	s, err := New(svctest.Env(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tb := &Table{Name: "ctr", PartitionKey: KeyDef{"id", "S"}}
	if err := s.createTable(tb); err != nil {
		t.Fatal(err)
	}
	ctx, _ := newExprCtx(nil, map[string]AV{":one": NumInt(1)})
	upd, err := parseUpdate("ADD n :one", ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := Item{"id": Str("c")}
	kb, _ := tb.schema().keyFrom(Item{"id": Str("c")})
	var wg sync.WaitGroup
	errs := make(chan error, 500)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				errs <- s.itemTx("ctr", func(t *Table, b *bolt.Bucket) error {
					_, _, err := s.updateTx(b, t, key, kb, exprUpdater(upd, t.schema()), condition{})
					return err
				})
				it := Item{"id": Str(fmt.Sprintf("i%d-%d", g, i)), "g": Str("x")}
				k, _ := prepItem(tb, it)
				errs <- s.itemTx("ctr", func(t *Table, b *bolt.Bucket) error {
					_, err := s.putTx(b, t, k, it, condition{})
					return err
				})
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.alterTable("ctr", func(t *Table, tx *bolt.Tx) error {
			return s.addIndex(t, tx, Index{Name: "by_g", PartitionKey: KeyDef{"g", "S"}})
		})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	it, _ := s.getOne("ctr", kb)
	if it["n"].S != "200" {
		t.Fatalf("counter = %v", it["n"])
	}
	res, err := s.read("ctr", readReq{index: "by_g", kc: &keyCond{PK: Str("x")}, forward: true})
	if err != nil || res.count != 200 {
		t.Fatalf("index has %d items (%v)", res.count, err)
	}
}
