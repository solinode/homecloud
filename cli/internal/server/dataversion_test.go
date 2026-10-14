package server

import (
	"context"
	"errors"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// Run refuses data written by a newer HomeCloud before touching anything
// (no Docker needed: the check comes first).
func TestRunRefusesNewerData(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	dir := t.TempDir()
	if err := core.StampDataVersion(dir, "0.6.0"); err != nil {
		t.Fatal(err)
	}
	Version = "0.5.0"
	cfg := core.DefaultConfig()
	cfg.DataDir = dir
	err := Run(context.Background(), cfg, Options{Logf: t.Logf})
	var newer *core.NewerDataError
	if !errors.As(err, &newer) {
		t.Fatalf("Run on newer data: %v", err)
	}
}
