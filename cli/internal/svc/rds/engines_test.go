package rds

import (
	"reflect"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cases := map[string][]string{
		`SET k v`:                    {"SET", "k", "v"},
		`SET greeting "hello world"`: {"SET", "greeting", "hello world"},
		`SET k 'a; rm -rf /'`:        {"SET", "k", "a; rm -rf /"},
		`  GET   k  `:                {"GET", "k"},
		`SET k ""`:                   {"SET", "k", ""},
	}
	for in, want := range cases {
		if got := splitCommand(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestQuote(t *testing.T) {
	if got := q(`it's`); got != `'it'\''s'` {
		t.Errorf("got %s", got)
	}
}
