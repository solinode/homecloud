package ec2

import "github.com/homecloudhq/homecloud/cli/internal/httpx"

// Capabilities tells the console what this host can run, so the launch wizard
// can warn before someone picks a VM image on a host without KVM.
type Capabilities struct {
	// VM is "kvm" when virtual machine instances run hardware-accelerated,
	// "emulated" when the Docker host has no usable /dev/kvm (QEMU emulates the
	// CPU, which is slow), and "unavailable" when VMs cannot run at all.
	VM string `json:"vm"`
}

func (s *Service) capabilities(c *httpx.Ctx) (any, error) {
	if s.env.Docker == nil {
		return Capabilities{VM: "unavailable"}, nil
	}
	if s.vmRunnerFailure() != nil {
		return Capabilities{VM: "unavailable"}, nil
	}
	if s.vmKVM() {
		return Capabilities{VM: "kvm"}, nil
	}
	return Capabilities{VM: "emulated"}, nil
}
