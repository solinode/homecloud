package ec2

import (
	"errors"
	"os"
	"os/exec"
)

// Guests never run on a Windows host (vmHostUnsupported).

func lockDir(string) (func(), bool, error) {
	return nil, false, errors.New("host-run guests need macOS")
}

func vmHostRunning(string) bool { return false }

func detach(*exec.Cmd) {}

func killStaleQEMU(string) {}

func privateDir(dir string) error { return os.MkdirAll(dir, 0o700) }
