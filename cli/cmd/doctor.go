package cmd

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/client"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine can run HomeCloud and that the server is healthy",
		RunE: func(cmd *cobra.Command, args []string) error {
			problems := 0
			ok := func(msg string, a ...any) { fmt.Printf("  ✓ "+msg+"\n", a...) }
			bad := func(fix, msg string, a ...any) {
				problems++
				fmt.Printf("  ✗ "+msg+"\n", a...)
				if fix != "" {
					fmt.Printf("      → %s\n", fix)
				}
			}

			fmt.Println("Docker")
			d, err := runtime.New()
			if err != nil {
				bad("Install Docker (Linux: Docker Engine; macOS/Windows: Docker Desktop or OrbStack) and make sure it is running.", "%v", err)
			} else {
				ok("reachable")
				if info, err := d.C.Info(); err == nil {
					ok("%s, %d CPUs, %.1f GB memory", info.OperatingSystem, info.NCPU, float64(info.MemTotal)/(1<<30))
					if info.MemTotal < 2<<30 {
						bad("Give Docker at least 4 GB of memory for databases and functions.", "Docker has less than 2 GB of memory")
					}
				}
			}

			cfg := core.DefaultConfig()
			if b, err := os.ReadFile(cfg.Path("config.json")); err == nil {
				_ = json.Unmarshal(b, &cfg)
			}
			fmt.Printf("\nServer (%s, data in %s)\n", cfg.APIAddr, cfg.DataDir)
			p, cerr := client.Load()
			if cerr != nil {
				bad("Run `homecloud serve` once to create an account, or `homecloud configure` to point at a remote server.", "no CLI credentials")
			} else {
				ok("credentials for %s", p.Endpoint)
			}
			hc := &http.Client{Timeout: 3 * time.Second}
			scheme := "http"
			if cfg.TLSCert != "" {
				scheme = "https"
			}
			resp, err := hc.Get(scheme + "://" + cfg.APIAddr + "/api/v1/health")
			running := err == nil
			if running {
				var h map[string]any
				_ = json.NewDecoder(resp.Body).Decode(&h)
				resp.Body.Close()
				ok("running, version %v, up %vs", h["version"], h["uptime_seconds"])
				if cerr == nil {
					if c, err := client.New(); err == nil {
						var who map[string]any
						if err := c.Do("GET", "/api/v1/auth/whoami", nil, &who); err != nil {
							bad("Your credentials were rejected; run `homecloud configure` with a valid access key.", "authentication failed: %v", err)
						} else {
							ok("signed in as %v (account %v)", who["user_name"], who["account_id"])
						}
					}
				}
			} else {
				bad("Start it with `homecloud serve`.", "not running at %s", cfg.APIAddr)
			}

			fmt.Println("\nPorts")
			ports := map[string]int{"S3 endpoint": cfg.S3Port, "S3 console": cfg.S3ConsolePort, "container registry": cfg.ECRPort}
			if !running {
				_, port, _ := strings.Cut(cfg.APIAddr, ":")
				var n int
				fmt.Sscan(port, &n)
				ports["API and console"] = n
			}
			for name, port := range ports {
				if d != nil && ownedBy(d, port) {
					ok("%s on %d (HomeCloud)", name, port)
					continue
				}
				if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
					l.Close()
					ok("%s port %d is free", name, port)
				} else if running && name != "API and console" {
					bad("Another program uses this port; pick another with the matching `homecloud serve` flag.", "%s port %d is in use by something else", name, port)
				} else if !running {
					bad("Stop the program using it or choose another port with `homecloud serve` flags.", "%s port %d is in use", name, port)
				}
			}
			fmt.Println()
			if problems > 0 {
				return fmt.Errorf("%d problem(s) found", problems)
			}
			fmt.Println("Everything looks good.")
			return nil
		},
	})
}

// ownedBy reports whether a HomeCloud container publishes the host port.
func ownedBy(d *runtime.Docker, port int) bool {
	cs, err := d.ManagedContainers()
	if err != nil {
		return false
	}
	for _, c := range cs {
		for _, p := range c.Ports {
			if int(p.PublicPort) == port {
				return true
			}
		}
	}
	return false
}
