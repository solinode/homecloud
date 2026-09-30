package runtime

import (
	"testing"

	docker "github.com/fsouza/go-dockerclient"
)

func TestBindingsAre(t *testing.T) {
	loop := []docker.PortBinding{{HostIP: "127.0.0.1", HostPort: "9500"}}
	all := []docker.PortBinding{{HostIP: "", HostPort: "9500"}}
	all2 := []docker.PortBinding{{HostIP: "0.0.0.0", HostPort: "9500"}}
	if !bindingsAre(loop, "127.0.0.1") || bindingsAre(loop, "0.0.0.0") {
		t.Error("loopback binding misjudged")
	}
	// Containers created before the bind address was configurable listen on every address.
	if bindingsAre(all, "127.0.0.1") || !bindingsAre(all, "0.0.0.0") || !bindingsAre(all2, "0.0.0.0") {
		t.Error("wildcard binding misjudged")
	}
	if bindingsAre(nil, "127.0.0.1") {
		t.Error("unpublished port counted as bound")
	}
}
