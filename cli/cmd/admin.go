package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
	"github.com/spf13/cobra"
)

func init() {
	adminCmd := &cobra.Command{
		Use:   "admin",
		Short: "Maintenance commands that work on the data directory while the server is stopped",
	}
	dataDir := core.DefaultDataDir()
	setPw := &cobra.Command{
		Use:   "set-root-password",
		Short: "Set the root console password to a value you choose",
		Long: `Sets the root user's console password. The password is read from the terminal
(asked twice, not echoed) or, when input is piped, from the first line of stdin;
it is never accepted as a command-line argument, where it would end up in shell
history and process listings.

The server must be stopped: it keeps state in memory and would overwrite the
change. To generate a random password instead, use "homecloud serve --reset-root-password".

  printf '%s\n' "$PASSWORD" | homecloud admin set-root-password`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pw, err := readNewPassword(os.Stdin, os.Stderr)
			if err != nil {
				return err
			}
			if err := setRootPassword(dataDir, pw); err != nil {
				return err
			}
			fmt.Println("root console password updated")
			return nil
		},
	}
	setPw.Flags().StringVar(&dataDir, "data-dir", dataDir, "the server's data directory")
	adminCmd.AddCommand(setPw)
	RootCmd.AddCommand(adminCmd)
}

// setRootPassword sets the root password in dataDir's state. It refuses while a
// server is running there, and never creates a new installation.
func setRootPassword(dataDir, pw string) error {
	statePath := core.Config{DataDir: dataDir}.Path("state.json")
	if _, err := os.Stat(statePath); err != nil {
		return fmt.Errorf("no HomeCloud installation in %s (pass --data-dir): %w", dataDir, err)
	}
	cfg := core.DefaultConfig()
	cfg.DataDir = dataDir
	if b, err := os.ReadFile(cfg.Path("config.json")); err == nil {
		_ = json.Unmarshal(b, &cfg)
		cfg.DataDir = dataDir
	}
	if serverListening(cfg.APIAddr) {
		return fmt.Errorf("a server is running at %s: stop it first (the change would be overwritten), or use `homecloud serve --reset-root-password`", cfg.APIAddr)
	}
	st, err := store.Open(statePath)
	if err != nil {
		return err
	}
	account, created, err := iam.LoadAccount(st)
	if err != nil {
		return err
	}
	if created {
		return errors.New("this data directory has no account yet: start the server once first")
	}
	return iam.New(&svc.Env{Cfg: cfg, Store: st, AccountID: account}).SetRootPassword(pw)
}

// serverListening reports whether something accepts connections at the API address.
func serverListening(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// readNewPassword reads a password from in: twice from a terminal without
// echo, or one line when input is piped.
func readNewPassword(in *os.File, out io.Writer) (string, error) {
	if fi, err := in.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		read := func(prompt string) (string, error) {
			fmt.Fprint(out, prompt)
			off := exec.Command("stty", "-echo")
			off.Stdin = in
			_ = off.Run()
			defer func() {
				on := exec.Command("stty", "echo")
				on.Stdin = in
				_ = on.Run()
				fmt.Fprintln(out)
			}()
			return readLine(in)
		}
		a, err := read("New root password: ")
		if err != nil {
			return "", err
		}
		b, err := read("Repeat it: ")
		if err != nil {
			return "", err
		}
		if a != b {
			return "", errors.New("the passwords differ")
		}
		return a, nil
	}
	return readLine(in)
}

func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", errors.New("no password given on stdin")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
