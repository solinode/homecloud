package runtime

import (
	"slices"
	"testing"
)

// On the host (no Self), containers keep host.docker.internal as the Docker
// host and HomeCloud dials published loopback ports.
func TestNotContainerized(t *testing.T) {
	var none *Docker
	if none.Self() != "" {
		t.Fatal("nil Docker has a self")
	}
	d := &Docker{}
	hosts := []string{HostAlias, "db:10.0.0.5"}
	if got := d.hostAlias("hc-vpc-1", hosts); !slices.Equal(got, hosts) {
		t.Fatalf("hostAlias rewrote %v to %v", hosts, got)
	}
	if got := d.DialAddr("homecloud-s3", 9000, "127.0.0.1:9500"); got != "127.0.0.1:9500" {
		t.Fatalf("DialAddr: %s", got)
	}
	if _, err := d.Reach("homecloud-s3", 9000); err == nil {
		t.Fatal("Reach succeeded outside a container")
	}
	if err := d.ConnectSelf("hc-vpc-1", "10.0.255.253"); err != nil {
		t.Fatal(err)
	}
}

func TestContainerIDPattern(t *testing.T) {
	line := "1612 1590 254:1 /docker/containers/69b6a92428daf6a7cb28654361d8240de2326289c20f193e0f97f0232541bce7/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw"
	m := containerIDPattern.FindStringSubmatch(line)
	if m == nil || m[1] != "69b6a92428daf6a7cb28654361d8240de2326289c20f193e0f97f0232541bce7" {
		t.Fatalf("got %v", m)
	}
}
