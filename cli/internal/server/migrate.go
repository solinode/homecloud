package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

var legacyRewrites = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`arn:hc:(iam|sts|route53):` + core.LegacyRegion + `:`), "arn:" + core.Partition + ":${1}::"},
	{regexp.MustCompile(`arn:hc:([a-z0-9-]*):` + core.LegacyRegion + `:`), "arn:" + core.Partition + ":${1}:" + core.DefaultRegion + ":"},
	{regexp.MustCompile(`arn:hc:`), "arn:" + core.Partition + ":"},
	// Availability zones ("local-1a"), default subnet names, Cognito pool IDs
	// ("local-1_ABC") and bare region values.
	{regexp.MustCompile(`"` + core.LegacyRegion + `([a-z]?)"`), `"` + core.DefaultRegion + `${1}"`},
	{regexp.MustCompile(`default-` + core.LegacyRegion + `([a-z])`), "default-" + core.DefaultRegion + "${1}"},
	{regexp.MustCompile(core.LegacyRegion + `_([A-Z0-9]+)`), core.DefaultRegion + "_${1}"},
}

// migrateLegacyRegion moves an installation created with the legacy region
// ("local-1", ARNs "arn:hc:...") to AWS-style names ("us-east-1", "arn:aws:..."),
// so ARNs HomeCloud hands out are accepted by AWS SDKs and tools. The original
// files are kept in <data>/backup-legacy-region.
func migrateLegacyRegion(cfg *core.Config, logf func(string, ...any)) error {
	if cfg.Region != core.LegacyRegion {
		return nil
	}
	files := []string{cfg.Path("state.json"), CredentialsPath(cfg.DataDir)}
	for _, dir := range []string{"sfn", "sqs"} {
		m, _ := filepath.Glob(cfg.Path(dir, "*.json"))
		files = append(files, m...)
	}
	backup := cfg.Path("backup-legacy-region")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		return err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(cfg.DataDir, f)
		dst := filepath.Join(backup, strings.ReplaceAll(rel, string(os.PathSeparator), "_"))
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return fmt.Errorf("back up %s: %w", f, err)
		}
		out := b
		for _, r := range legacyRewrites {
			out = r.re.ReplaceAll(out, []byte(r.repl))
		}
		if json.Valid(b) && !json.Valid(out) {
			return fmt.Errorf("migrating %s produced invalid JSON; nothing was changed", f)
		}
		tmp := f + ".migrate"
		if err := os.WriteFile(tmp, out, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, f); err != nil {
			return err
		}
	}
	cfg.Region = core.DefaultRegion
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfg.Path("config.json"), b, 0o600); err != nil {
		return err
	}
	logf("migrated region %s -> %s and ARNs arn:hc -> arn:%s (backup in %s)", core.LegacyRegion, core.DefaultRegion, core.Partition, backup)
	return nil
}
