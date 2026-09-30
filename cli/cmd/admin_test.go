package cmd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

func TestSetRootPasswordOffline(t *testing.T) {
	env := svctest.Env(t)
	if _, err := iam.New(env).Bootstrap(); err != nil { // creates state.json's root user
		t.Fatal(err)
	}
	dir := env.Cfg.DataDir
	before, _ := os.ReadFile(filepath.Join(dir, "state.json"))

	if err := setRootPassword(dir, "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := setRootPassword(dir, "a-chosen-passphrase"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if string(before) == string(after) {
		t.Fatal("state did not change")
	}
	if strings.Contains(string(after), "a-chosen-passphrase") {
		t.Fatal("the password is stored in clear text")
	}
	if _, err := store.Open(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSetRootPasswordRefusals(t *testing.T) {
	// No installation: refuse instead of creating one.
	empty := t.TempDir()
	if err := setRootPassword(empty, "a-chosen-passphrase"); err == nil {
		t.Fatal("no installation, no error")
	}
	if _, err := os.Stat(filepath.Join(empty, "state.json")); err == nil {
		t.Fatal("created a state file")
	}
	// A running server owns the state.
	env := svctest.Env(t)
	if _, err := iam.New(env).Bootstrap(); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.WriteFile(filepath.Join(env.Cfg.DataDir, "config.json"), []byte(`{"api_addr":"`+l.Addr().String()+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setRootPassword(env.Cfg.DataDir, "a-chosen-passphrase"); err == nil || !strings.Contains(err.Error(), "stop it") {
		t.Fatalf("running server: %v", err)
	}
}

func TestReadPasswordFromPipe(t *testing.T) {
	r, w, _ := os.Pipe()
	_, _ = w.WriteString("s3cret-passphrase\r\nignored\n")
	w.Close()
	pw, err := readNewPassword(r, os.Stderr)
	if err != nil || pw != "s3cret-passphrase" {
		t.Fatalf("%q %v", pw, err)
	}
	r2, w2, _ := os.Pipe()
	w2.Close()
	if _, err := readNewPassword(r2, os.Stderr); err == nil {
		t.Fatal("empty stdin accepted")
	}
}
