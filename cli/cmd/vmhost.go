package cmd

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/spf13/cobra"
)

// vm-host is started by the server, one per VM instance whose guest runs on
// this Mac (QEMU with Hypervisor.framework): it runs QEMU and joins it to the
// instance's VM container. It is a process of its own so that guests keep
// running when the server restarts.
func init() {
	RootCmd.AddCommand(&cobra.Command{
		Use:    "vm-host DIR",
		Short:  "Run the host side of a VM instance (started by the server)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := runtime.New()
			if err != nil {
				return err
			}
			// SIGTERM and SIGINT are ignored: the guest stops when its container does.
			signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
			return ec2.RunVMHost(context.Background(), d, args[0])
		},
	})
}

// vmHostCommand is ec2.VMHostCommand for the server: this binary's vm-host.
func vmHostCommand(dir string) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return exec.Command(exe, "vm-host", dir)
}
