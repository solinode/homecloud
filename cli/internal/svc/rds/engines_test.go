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

// A Redis-family cache runs with a password (native API) or without (ElastiCache without an AuthToken).
func TestCacheAuth(t *testing.T) {
	for _, name := range []string{"redis", "valkey"} {
		e, _ := findEngine(name)
		if got := e.cmd("s3cret"); !reflect.DeepEqual(got, []string{name + "-server", "--requirepass", "s3cret", "--appendonly", "yes"}) {
			t.Errorf("%s with password: %q", name, got)
		}
		if got := e.cmd(""); !reflect.DeepEqual(got, []string{name + "-server", "--appendonly", "yes"}) {
			t.Errorf("%s without password: %q", name, got)
		}
		if got := e.probe("", "s3cret", ""); got[2] != name+"-cli --no-auth-warning -a 's3cret' ping | grep -q PONG" {
			t.Errorf("%s probe with password: %q", name, got)
		}
		if got := e.probe("", "", ""); got[2] != name+"-cli ping | grep -q PONG" {
			t.Errorf("%s probe without password: %q", name, got)
		}
		if got := e.query("", "", "", "GET k"); !reflect.DeepEqual(got, []string{name + "-cli", "GET", "k"}) {
			t.Errorf("%s query without password: %q", name, got)
		}
	}
}

func TestQuote(t *testing.T) {
	if got := q(`it's`); got != `'it'\''s'` {
		t.Errorf("got %s", got)
	}
}
