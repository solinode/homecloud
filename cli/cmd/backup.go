package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/client"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/system"
	"github.com/spf13/cobra"
)

func init() {
	var output string
	var noVolumes bool
	backupCmd := &cobra.Command{
		Use:   "backup",
		Short: "Save a backup of this HomeCloud (state, keys, data and Docker volumes)",
		Long: `Downloads a backup from the running server: the data directory (state, the
master key, function code, logs, ...) and every HomeCloud Docker volume
(databases, S3 objects, registry images, EBS volumes, file systems). Containers
are paused briefly while their volume is copied.

Instance root disks (container file systems) are not included: keep data that
matters on volumes, as with EBS. The backup holds the master key and
credentials, so store it somewhere safe.

Restore with "homecloud restore FILE" (with the server stopped).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client.New()
			if err != nil {
				return err
			}
			c.HTTP.Timeout = 0 // large volumes take a while
			if output == "" {
				output = fmt.Sprintf("homecloud-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
			}
			path := "/api/v1/system/backup"
			if noVolumes {
				path += "?volumes=false"
			}
			resp, err := c.Request(http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			tmp := output + ".partial"
			f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			n, err := io.Copy(f, resp.Body)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(tmp)
				return fmt.Errorf("backup failed after %d bytes: %w (see the server log)", n, err)
			}
			if err := os.Rename(tmp, output); err != nil {
				return err
			}
			fmt.Printf("saved %s (%.1f MB)\n", output, float64(n)/(1<<20))
			return nil
		},
	}
	backupCmd.Flags().StringVarP(&output, "output", "o", "", "file to write (default homecloud-backup-<time>.tar.gz)")
	backupCmd.Flags().BoolVar(&noVolumes, "no-volumes", false, "only the data directory, without Docker volumes")
	RootCmd.AddCommand(backupCmd)

	var dataDir string
	var force, skipVolumes bool
	restoreCmd := &cobra.Command{
		Use:   "restore FILE",
		Short: "Restore a backup into a data directory (the server must be stopped)",
		Long: `Unpacks a backup made with "homecloud backup" into the data directory and
recreates its Docker volumes. Stop the server first; start it afterwards with
"homecloud serve" and it picks up the restored state.

With --force, an existing data directory is moved aside to
<data-dir>.before-restore-<time> and existing volumes are replaced.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := filepath.Abs(dataDir)
			if err != nil {
				return err
			}
			if running(dir) {
				return fmt.Errorf("a HomeCloud server is running for %s; stop it before restoring", dir)
			}
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			d, err := runtime.New()
			if err != nil {
				return err
			}
			m, err := system.Restore(context.Background(), d, f, system.RestoreOptions{DataDir: dir, Force: force, SkipVolumes: skipVolumes,
				Logf: func(format string, a ...any) { fmt.Printf(format+"\n", a...) }})
			if err != nil {
				return err
			}
			fmt.Printf("restored account %s (backup of %s, HomeCloud %s, %d volumes) into %s\nstart it with: homecloud serve --data-dir %s\n",
				m.AccountID, m.CreatedAt.Format(time.RFC3339), m.Version, len(m.Volumes), dir, dir)
			return nil
		},
	}
	restoreCmd.Flags().StringVar(&dataDir, "data-dir", core.DefaultDataDir(), "data directory to restore into")
	restoreCmd.Flags().BoolVar(&force, "force", false, "move an existing data directory aside and replace existing volumes")
	restoreCmd.Flags().BoolVar(&skipVolumes, "no-volumes", false, "restore only the data directory")
	RootCmd.AddCommand(restoreCmd)
}

// running reports whether a server answers at the address saved in dataDir's config.
func running(dataDir string) bool {
	b, err := os.ReadFile(filepath.Join(dataDir, "config.json"))
	if err != nil {
		return false
	}
	var cfg core.Config
	if json.Unmarshal(b, &cfg) != nil || cfg.APIAddr == "" {
		return false
	}
	// Only liveness matters here, so a self-signed certificate is fine.
	hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	for _, scheme := range []string{"http", "https"} {
		if resp, err := hc.Get(scheme + "://" + cfg.APIAddr + "/api/v1/health"); err == nil {
			resp.Body.Close()
			return true
		}
	}
	return false
}
