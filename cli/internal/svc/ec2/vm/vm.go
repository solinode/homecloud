// Package vm holds what EC2 needs to run an instance as a virtual machine:
// the runner image (QEMU, passt and helper scripts, built locally), the
// cloud image catalog, the QEMU command line and the cloud-init seed. It has no
// dependency on the rest of HomeCloud so each piece can be tested on its own.
//
// A VM instance is a QEMU process inside a Docker container (the "VM
// container") attached to the instance's VPC network with its private
// address, so everything that works per container keeps working: addresses,
// DNS, the metadata service, security groups and published ports. passt gives
// the guest the container's address, gateway and routes and forwards every
// inbound port to it.
package vm

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"sort"
)

// Arch is the guest architecture, which is the Docker host's.
type Arch string

const (
	ArchAArch64 Arch = "aarch64"
	ArchX8664   Arch = "x86_64"
)

// ArchOf maps a Docker or EC2 architecture name to a guest architecture.
func ArchOf(name string) (Arch, error) {
	switch name {
	case "aarch64", "arm64":
		return ArchAArch64, nil
	case "x86_64", "amd64":
		return ArchX8664, nil
	}
	return "", fmt.Errorf("virtual machines are not supported on %q hosts (need aarch64 or x86_64)", name)
}

// Cloud image file names use Debian-style architecture names.
func (a Arch) Deb() string {
	if a == ArchAArch64 {
		return "arm64"
	}
	return "amd64"
}

// QEMU is the qemu-system binary for the architecture.
func (a Arch) QEMU() string { return "qemu-system-" + string(a) }

//go:embed runner.Dockerfile
var runnerDockerfile string

//go:embed hc-vm-run
var runScript string

//go:embed hc-vm-fetch
var fetchScript string

//go:embed hc-vm-ctl
var ctlScript string

// RunnerContext is the build context of the runner image.
func RunnerContext() map[string][]byte {
	return map[string][]byte{
		"Dockerfile":  []byte(runnerDockerfile),
		"hc-vm-run":   []byte(runScript),
		"hc-vm-fetch": []byte(fetchScript),
		"hc-vm-ctl":   []byte(ctlScript),
	}
}

// RunnerTag names the runner image by the hash of its build context, so a
// change to the Dockerfile or scripts builds a new image.
func RunnerTag() string {
	files := RunnerContext()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(files[n]))
		h.Write(files[n])
	}
	return "homecloud/vm-runner:" + hex.EncodeToString(h.Sum(nil))[:12]
}

// Paths inside the VM container.
const (
	ImagesDir = "/images"  // image cache volume (read-only)
	DiskDir   = "/vm"      // the instance's root volume
	SeedDir   = "/hc/seed" // cloud-init NoCloud files
)

// MinRootGB is the smallest root disk: it must hold the cloud image.
const MinRootGB = 4
