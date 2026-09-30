package ec2

import (
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/runtime"
)

func TestHostFwd(t *testing.T) {
	if got := hostFwd(nil); got != "" {
		t.Errorf("no ports: %q", got)
	}
	got := hostFwd([]runtime.Port{{ContainerPort: 22, Protocol: "tcp"}, {ContainerPort: 53, Protocol: "udp"}})
	if want := ",hostfwd=tcp::22-:22,hostfwd=udp::53-:53"; got != want {
		t.Errorf("hostFwd = %q, want %q", got, want)
	}
}

func TestVMImagesAreMarked(t *testing.T) {
	seen := 0
	for _, im := range catalog {
		n := im.normalized()
		if !im.IsVM() {
			if n.Virtualization != "container" || im.Ref == "" {
				t.Errorf("%s: container image %+v", im.ID, n)
			}
			continue
		}
		seen++
		if n.Virtualization != "vm" || im.Ref != "" || im.KeepAlive {
			t.Errorf("%s: VM image %+v", im.ID, n)
		}
		if len(im.Name) < 5 || im.Name[len(im.Name)-4:] != "(VM)" {
			t.Errorf("%s: name %q does not end in (VM)", im.ID, im.Name)
		}
		if im.AWSName == "" || im.OwnerID == "" {
			t.Errorf("%s: no AWS name or owner", im.ID)
		}
	}
	if seen < 2 {
		t.Errorf("only %d VM images in the catalog", seen)
	}
}

func TestErrVMUnsupported(t *testing.T) {
	if err := errVMUnsupported("attaching volumes to"); err == nil || err.Error() != "UnsupportedOperation: attaching volumes to VM instances is not supported yet" {
		t.Errorf("%v", err)
	}
}
