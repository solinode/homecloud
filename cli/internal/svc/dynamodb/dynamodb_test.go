package dynamodb

import (
	"bytes"
	"math"
	"sort"
	"testing"
)

func TestNumberKeyOrdering(t *testing.T) {
	nums := []float64{-1e9, -3.5, -1, 0, 0.25, 1, 2, 10, 1e12}
	var enc [][]byte
	for _, n := range nums {
		b, err := encodeKey(KeyDef{Name: "n", Type: "N"}, n)
		if err != nil {
			t.Fatal(err)
		}
		enc = append(enc, b)
	}
	if !sort.SliceIsSorted(enc, func(i, j int) bool { return bytes.Compare(enc[i], enc[j]) < 0 }) {
		t.Fatal("number encoding does not preserve order")
	}
}

func TestConditions(t *testing.T) {
	it := Item{"n": 5.0, "s": "hello", "tags": []any{"a", "b"}}
	cases := []struct {
		c    Condition
		want bool
	}{
		{Condition{Attr: "n", Op: "gt", Value: 4.0}, true},
		{Condition{Attr: "n", Op: "between", Value: 1.0, Value2: 5.0}, true},
		{Condition{Attr: "s", Op: "begins_with", Value: "he"}, true},
		{Condition{Attr: "tags", Op: "contains", Value: "b"}, true},
		{Condition{Attr: "missing", Op: "not_exists"}, true},
		{Condition{Attr: "missing", Op: "eq", Value: 1.0}, false},
		{Condition{Attr: "n", Op: "eq", Value: "5"}, false},
	}
	for i, c := range cases {
		if got := c.c.eval(it); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestNegativeZeroIsZero(t *testing.T) {
	a, _ := encodeKey(KeyDef{Name: "n", Type: "N"}, 0.0)
	b, _ := encodeKey(KeyDef{Name: "n", Type: "N"}, math.Copysign(0, -1))
	if !bytes.Equal(a, b) {
		t.Fatal("-0 and 0 encode differently")
	}
}

func TestPageResumesAfterDeletedStartKey(t *testing.T) {
	tb := Table{PartitionKey: KeyDef{Name: "p", Type: "S"}, SortKey: &KeyDef{Name: "n", Type: "N"}}
	items := []Item{{"p": "a", "n": 1.0}, {"p": "a", "n": 3.0}} // item n=2 was deleted
	s := &Service{}
	out := s.page(tb, items, pageInput{StartKey: Item{"p": "a", "n": 2.0}}, 2, tb.SortKey, true)
	got := out["items"].([]Item)
	if len(got) != 1 || got[0]["n"] != 3.0 {
		t.Fatalf("resumed at %v", got)
	}
}
