package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/spf13/cobra"
)

func testServeFlags(cfg *core.Config) *cobra.Command {
	c := &cobra.Command{Use: "serve"}
	f := c.Flags()
	f.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "")
	f.StringVar(&cfg.PublicHost, "public-host", cfg.PublicHost, "")
	f.StringVar(&cfg.PublicURL, "public-url", "", "")
	f.StringSliceVar(&cfg.TrustedProxies, "trusted-proxies", nil, "")
	f.StringVar(&cfg.ServiceBind, "s3-bind", "", "")
	f.StringVar(&cfg.DNSBind, "dns-bind", "", "")
	return c
}

func TestMergeConfigPublicSettings(t *testing.T) {
	dir := t.TempDir()
	flags := core.DefaultConfig()
	flags.DataDir = dir
	c := testServeFlags(&flags)
	if err := c.ParseFlags([]string{"--public-url", "https://cloud.example.com/", "--trusted-proxies", "10.0.0.0/8,172.16.0.0/12", "--s3-bind", "0.0.0.0"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := mergeConfig(c, flags)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://cloud.example.com" || cfg.PublicHost != "cloud.example.com" ||
		len(cfg.TrustedProxies) != 2 || cfg.ServiceBind != "0.0.0.0" || cfg.DNSBind != "" {
		t.Fatalf("merged: %+v", cfg)
	}
	// Saved settings are reused on the next start.
	var saved core.Config
	b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if err := json.Unmarshal(b, &saved); err != nil || saved.PublicURL != cfg.PublicURL || len(saved.TrustedProxies) != 2 {
		t.Fatalf("saved config: %s %v", b, err)
	}
	flags2 := core.DefaultConfig()
	flags2.DataDir = dir
	again, err := mergeConfig(testServeFlags(&flags2), flags2)
	if err != nil || again.PublicURL != cfg.PublicURL || again.ServiceBind != "0.0.0.0" {
		t.Fatalf("reload: %+v %v", again, err)
	}
}

func TestMergeConfigRejectsBadValues(t *testing.T) {
	for _, args := range [][]string{
		{"--public-url", "cloud.example.com"},
		{"--trusted-proxies", "not-a-cidr"},
		{"--s3-bind", "localhost"},
	} {
		flags := core.DefaultConfig()
		flags.DataDir = t.TempDir()
		c := testServeFlags(&flags)
		if err := c.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		if _, err := mergeConfig(c, flags); err == nil {
			t.Errorf("%v accepted", args)
		}
		if _, err := os.Stat(filepath.Join(flags.DataDir, "config.json")); err == nil {
			t.Errorf("%v: invalid config was saved", args)
		}
	}
}
