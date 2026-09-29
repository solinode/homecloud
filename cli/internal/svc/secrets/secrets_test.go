package secrets

import (
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

func TestVersions(t *testing.T) {
	s, err := New(svctest.Env(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("app/db", "v1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("app/db", "v2", "", ""); err != nil {
		t.Fatal(err)
	}
	cur, _, _ := s.Value("app/db", "", "")
	prev, _, _ := s.Value("app/db", "AWSPREVIOUS", "")
	if cur != "v2" || prev != "v1" {
		t.Fatalf("current=%q previous=%q", cur, prev)
	}
	ct := s.Encrypt([]byte("x"))
	if ct == s.Encrypt([]byte("x")) {
		t.Fatal("encryption is deterministic")
	}
	if _, err := s.Put("bad name!", "v", "", ""); err == nil {
		t.Fatal("invalid name accepted")
	}
}
