package store

import (
	"path/filepath"
	"testing"
)

type doc struct {
	N int `json:"n"`
}

func TestStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Put(s, "c", "b", doc{2}); err != nil {
		t.Fatal(err)
	}
	_ = Put(s, "c", "a", doc{1})
	if _, err := Update(s, "c", "a", func(d *doc) error { d.N = 10; return nil }); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := List[doc](s2, "c")
	if len(got) != 2 || got[0].N != 10 || got[1].N != 2 {
		t.Fatalf("reloaded %v", got)
	}
	if err := Delete(s2, "c", "a"); err != nil || Has(s2, "c", "a") {
		t.Fatal("delete failed")
	}
	if _, err := Get[doc](s2, "c", "zzz"); err != ErrNotFound {
		t.Fatal("expected ErrNotFound")
	}
}
