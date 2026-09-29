// Package svctest builds service environments for unit tests that do not need Docker.
package svctest

import (
	"path/filepath"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

func Env(t *testing.T) *svc.Env {
	t.Helper()
	cfg := core.DefaultConfig()
	cfg.DataDir = t.TempDir()
	st, err := store.Open(filepath.Join(cfg.DataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &svc.Env{Cfg: cfg, Store: st, AccountID: "123456789012"}
}
