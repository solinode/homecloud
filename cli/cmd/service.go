package cmd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"text/template"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/spf13/cobra"
)

const (
	launchdLabel = "dev.homecloud.server"
	systemdUnit  = "homecloud.service"
)

var systemdTmpl = template.Must(template.New("unit").Parse(`[Unit]
Description=HomeCloud (self-hosted AWS-compatible cloud)
Documentation=https://github.com/homecloudhq/homecloud
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=simple
ExecStart={{.Exec}}
Environment=HOMECLOUD_DATA_DIR={{.DataDir}}
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
{{- if .User}}
User={{.User}}
{{- end}}

[Install]
WantedBy={{.WantedBy}}
`))

var launchdTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"x": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label | x}}</string>
  <key>ProgramArguments</key>
  <array>{{range .Args}}
    <string>{{. | x}}</string>{{end}}
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOMECLOUD_DATA_DIR</key><string>{{.DataDir | x}}</string>
    <key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>{{.Log | x}}</string>
  <key>StandardErrorPath</key><string>{{.Log | x}}</string>
</dict>
</plist>
`))

type serviceSpec struct {
	Exec, DataDir, User, WantedBy, Label, Log string
	Args                                      []string
}

func init() {
	var dryRun, system bool
	var dataDir string
	svcCmd := &cobra.Command{
		Use:   "service",
		Short: "Run HomeCloud as a background service (launchd on macOS, systemd on Linux)",
	}
	install := &cobra.Command{
		Use:   "install",
		Short: "Install and start HomeCloud as a service that starts at boot or login",
		Long: `Installs a service that runs "homecloud serve" in the background and restarts it
if it exits: a launchd agent on macOS (starts at login), a systemd unit on Linux
(user unit, or a system unit with --system when run as root). Server flags such as
--addr are read from <data-dir>/config.json, which "homecloud serve" saves.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			dataDir, err := serviceDataDir(cmd.Flags().Changed("data-dir"), dataDir, os.Geteuid(),
				os.Getenv("SUDO_USER"), os.Getenv("HOMECLOUD_DATA_DIR"), func(u string) (string, error) {
					usr, err := user.Lookup(u)
					if err != nil {
						return "", err
					}
					return usr.HomeDir, nil
				})
			if err != nil {
				return err
			}
			dir, err := filepath.Abs(dataDir)
			if err != nil {
				return err
			}
			switch goruntime.GOOS {
			case "darwin":
				return installLaunchd(exe, dir, dryRun)
			case "linux":
				return installSystemd(exe, dir, system, dryRun)
			default:
				fmt.Printf("Automatic service installation isn't supported on %s. Run HomeCloud at startup with:\n\n  %q serve --data-dir %q\n\n", goruntime.GOOS, exe, dir)
				if goruntime.GOOS == "windows" {
					fmt.Printf("For example, with Task Scheduler:\n\n  schtasks /Create /TN HomeCloud /SC ONLOGON /RL HIGHEST /TR \"\\\"%s\\\" serve --data-dir \\\"%s\\\"\"\n", exe, dir)
				}
				return nil
			}
		},
	}
	install.Flags().BoolVar(&dryRun, "dry-run", false, "print the service definition instead of installing it")
	install.Flags().BoolVar(&system, "system", false, "Linux: install a system unit (requires root) instead of a user unit")
	install.Flags().StringVar(&dataDir, "data-dir", core.DefaultDataDir(), "data directory the service uses")

	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the HomeCloud service (data is kept)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch goruntime.GOOS {
			case "darwin":
				p := launchdPath()
				_ = run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel))
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					return err
				}
				fmt.Println("removed", p)
			case "linux":
				for _, scope := range []bool{false, true} {
					p := systemdPath(scope)
					if _, err := os.Stat(p); err != nil {
						continue
					}
					_ = run(systemctl(scope, "disable", "--now", systemdUnit)...)
					if err := os.Remove(p); err != nil {
						return err
					}
					_ = run(systemctl(scope, "daemon-reload")...)
					fmt.Println("removed", p)
				}
			default:
				fmt.Println("no service is installed on", goruntime.GOOS)
			}
			return nil
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether the HomeCloud service is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch goruntime.GOOS {
			case "darwin":
				return runTTY("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel))
			case "linux":
				_, err := os.Stat(systemdPath(true))
				return runTTY(systemctl(err == nil, "status", "--no-pager", systemdUnit)...)
			}
			return fmt.Errorf("services aren't managed on %s", goruntime.GOOS)
		},
	}
	svcCmd.AddCommand(install, uninstall, status)
	RootCmd.AddCommand(svcCmd)
}

// serviceDataDir picks the data directory a service uses. Run through sudo,
// the default must come from the invoking user's home (whose data it is), not
// root's; when that user cannot be resolved the command refuses rather than
// silently creating a second installation under /root.
func serviceDataDir(explicit bool, flagValue string, euid int, sudoUser, envDir string, homeOf func(string) (string, error)) (string, error) {
	if explicit || envDir != "" || euid != 0 || sudoUser == "" || sudoUser == "root" {
		return flagValue, nil
	}
	home, err := homeOf(sudoUser)
	if err != nil || home == "" {
		return "", fmt.Errorf("running under sudo as %s, whose home directory can't be found: pass --data-dir explicitly", sudoUser)
	}
	return filepath.Join(home, ".homecloud"), nil
}

func launchdPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func installLaunchd(exe, dataDir string, dryRun bool) error {
	spec := serviceSpec{Label: launchdLabel, DataDir: dataDir, Log: filepath.Join(dataDir, "server.log"),
		Args: []string{exe, "serve", "--data-dir", dataDir}}
	var b bytes.Buffer
	if err := launchdTmpl.Execute(&b, spec); err != nil {
		return err
	}
	p := launchdPath()
	if dryRun {
		fmt.Printf("# %s\n%s", p, b.String())
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = run("launchctl", "bootout", domain+"/"+launchdLabel) // replace an older definition
	if err := run("launchctl", "bootstrap", domain, p); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	fmt.Printf("installed %s\nHomeCloud starts at login; logs: %s\n", p, spec.Log)
	return nil
}

func systemdPath(system bool) string {
	if system {
		return filepath.Join("/etc/systemd/system", systemdUnit)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", systemdUnit)
}

func systemctl(system bool, args ...string) []string {
	if system {
		return append([]string{"systemctl"}, args...)
	}
	return append([]string{"systemctl", "--user"}, args...)
}

func installSystemd(exe, dataDir string, system, dryRun bool) error {
	if system && os.Geteuid() != 0 && !dryRun {
		return fmt.Errorf("--system needs root (sudo homecloud service install --system)")
	}
	spec := serviceSpec{Exec: fmt.Sprintf("%s serve --data-dir %s", quoteArg(exe), quoteArg(dataDir)), DataDir: dataDir, WantedBy: "default.target"}
	if system {
		spec.WantedBy = "multi-user.target"
		if u := os.Getenv("SUDO_USER"); u != "" {
			spec.User = u // run as the invoking user, whose data dir it is
		}
	}
	var b bytes.Buffer
	if err := systemdTmpl.Execute(&b, spec); err != nil {
		return err
	}
	if !system {
		// User units can't depend on docker.service (a system unit).
		out := strings.ReplaceAll(b.String(), "After=network-online.target docker.service\n", "After=network-online.target\n")
		out = strings.ReplaceAll(out, "Requires=docker.service\n", "")
		b.Reset()
		b.WriteString(out)
	}
	p := systemdPath(system)
	if dryRun {
		fmt.Printf("# %s\n%s", p, b.String())
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		return err
	}
	if err := run(systemctl(system, "daemon-reload")...); err != nil {
		return err
	}
	if err := run(systemctl(system, "enable", "--now", systemdUnit)...); err != nil {
		return err
	}
	fmt.Printf("installed %s\n", p)
	if !system {
		fmt.Println("To keep HomeCloud running after you log out: sudo loginctl enable-linger $USER")
	}
	fmt.Printf("Logs: journalctl %s-u homecloud -f\n", map[bool]string{true: "", false: "--user "}[system])
	return nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t\"'\\") {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
	}
	return s
}

func run(args ...string) error {
	c := exec.Command(args[0], args[1:]...)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

func runTTY(args ...string) error {
	c := exec.Command(args[0], args[1:]...)
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
	return c.Run()
}
