package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/homecloudhq/homecloud/cli/internal/client"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/server"
	"github.com/spf13/cobra"
)

func init() {
	var resetRoot, selfSigned bool
	cfg := core.DefaultConfig()
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HomeCloud server (API, web console and all services)",
		Long: `Run the HomeCloud server. On first start it creates your account, a root user
and CLI credentials in the data directory. Settings passed as flags are saved to
<data-dir>/config.json and reused on later starts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg = mergeConfig(cmd, cfg)
			if selfSigned && cfg.TLSCert == "" {
				c, k, err := server.SelfSignedCert(cfg.DataDir, cfg.PublicHost)
				if err != nil {
					return err
				}
				cfg.TLSCert, cfg.TLSKey = c, k
				saveConfig(cfg)
			}
			if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
				return fmt.Errorf("--tls-cert and --tls-key must be used together")
			}
			server.Version = Version
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return server.Run(ctx, cfg, server.Options{ResetRootPassword: resetRoot})
		},
	}
	f := serveCmd.Flags()
	f.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "where HomeCloud keeps its state")
	f.StringVar(&cfg.APIAddr, "addr", cfg.APIAddr, "address the API and console listen on (use 0.0.0.0:8080 to expose on your LAN)")
	f.StringVar(&cfg.PublicHost, "public-host", cfg.PublicHost, "host name clients use to reach published ports (e.g. your LAN IP or Tailscale name)")
	f.IntVar(&cfg.S3Port, "s3-port", cfg.S3Port, "host port for the S3-compatible endpoint")
	f.IntVar(&cfg.S3ConsolePort, "s3-console-port", cfg.S3ConsolePort, "host port for the MinIO console")
	f.IntVar(&cfg.DNSPort, "dns-port", cfg.DNSPort, "host port (UDP and TCP) serving public DNS zones")
	f.BoolVar(&resetRoot, "reset-root-password", false, "generate a new root console password and print it")
	f.StringVar(&cfg.TLSCert, "tls-cert", "", "serve HTTPS with this PEM certificate")
	f.StringVar(&cfg.TLSKey, "tls-key", "", "PEM private key for --tls-cert")
	f.BoolVar(&selfSigned, "tls-self-signed", false, "serve HTTPS with a generated self-signed certificate")
	RootCmd.AddCommand(serveCmd)

	var p client.Profile
	configureCmd := &cobra.Command{
		Use:   "configure",
		Short: "Set the endpoint and access key the CLI uses",
		RunE: func(cmd *cobra.Command, args []string) error {
			cur, _ := client.Load()
			if p.Endpoint == "" {
				p.Endpoint = prompt("Endpoint", cur.Endpoint)
			}
			if p.AccessKeyID == "" {
				p.AccessKeyID = prompt("Access key ID", cur.AccessKeyID)
			}
			if p.SecretAccessKey == "" {
				p.SecretAccessKey = prompt("Secret access key", "")
			}
			if err := client.Save(p); err != nil {
				return err
			}
			fmt.Println("saved to", client.CredentialsFile())
			return nil
		},
	}
	configureCmd.Flags().StringVar(&p.Endpoint, "endpoint", "", "API endpoint, e.g. http://homelab:8080")
	configureCmd.Flags().StringVar(&p.AccessKeyID, "access-key-id", "", "access key ID")
	configureCmd.Flags().StringVar(&p.SecretAccessKey, "secret-access-key", "", "secret access key")
	RootCmd.AddCommand(configureCmd)

	RootCmd.AddCommand(&cobra.Command{
		Use:   "whoami",
		Short: "Show the identity the CLI is using",
		RunE: func(cmd *cobra.Command, args []string) error {
			return call("GET", "/api/v1/auth/whoami", nil, nil)
		},
	})

	var data string
	apiCmd := &cobra.Command{
		Use:   "api METHOD PATH",
		Short: "Call any API route directly, e.g. `homecloud api GET /api/v1/ec2/instances`",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var body any
			if data == "-" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				data = string(b)
			}
			if data != "" {
				var v any
				if err := json.Unmarshal([]byte(data), &v); err != nil {
					return fmt.Errorf("--data must be JSON: %w", err)
				}
				body = v
			}
			var out any
			if err := api().Do(strings.ToUpper(args[0]), args[1], body, &out); err != nil {
				return err
			}
			printJSON(out)
			return nil
		},
	}
	apiCmd.Flags().StringVarP(&data, "data", "d", "", "JSON request body ('-' reads stdin)")
	RootCmd.AddCommand(apiCmd)
}

// mergeConfig loads the saved config, applies explicitly set flags and saves the result.
func mergeConfig(cmd *cobra.Command, flags core.Config) core.Config {
	path := flags.Path("config.json")
	cfg := core.DefaultConfig()
	cfg.DataDir = flags.DataDir
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			log.Printf("ignoring corrupt %s: %v", path, err)
		}
	}
	cfg.DataDir = flags.DataDir
	set := func(name string, apply func()) {
		if cmd.Flags().Changed(name) {
			apply()
		}
	}
	set("addr", func() { cfg.APIAddr = flags.APIAddr })
	set("public-host", func() { cfg.PublicHost = flags.PublicHost })
	set("s3-port", func() { cfg.S3Port = flags.S3Port })
	set("s3-console-port", func() { cfg.S3ConsolePort = flags.S3ConsolePort })
	set("dns-port", func() { cfg.DNSPort = flags.DNSPort })
	set("tls-cert", func() { cfg.TLSCert = flags.TLSCert })
	set("tls-key", func() { cfg.TLSKey = flags.TLSKey })
	saveConfig(cfg)
	return cfg
}

func saveConfig(cfg core.Config) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err == nil {
		b, _ := json.MarshalIndent(cfg, "", "  ")
		_ = os.WriteFile(cfg.Path("config.json"), b, 0o600)
	}
}

func prompt(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	var s string
	fmt.Scanln(&s)
	if s == "" {
		return def
	}
	return s
}
