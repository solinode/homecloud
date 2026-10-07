package vpc

import (
	"strconv"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

func TestIngressPorts(t *testing.T) {
	s := &Service{env: svctest.Env(t)}
	put := func(id string, rules ...Rule) {
		if err := store.Put(s.env.Store, cSGs, id, SecurityGroup{ID: id, Ingress: rules}); err != nil {
			t.Fatal(err)
		}
	}
	put("sg-a",
		Rule{Protocol: "tcp", FromPort: 22, ToPort: 22, CIDR: "10.0.0.0/8"}, // any source counts
		Rule{Protocol: "udp", FromPort: 53, ToPort: 53, SourceGroup: "sg-b"},
		Rule{Protocol: "tcp", FromPort: 8000, ToPort: 8002, CIDR: "0.0.0.0/0"},
		Rule{Protocol: "icmp", FromPort: -1, ToPort: -1, CIDR: "0.0.0.0/0"}, // not a port
		Rule{Protocol: "-1", FromPort: 0, ToPort: 65535, CIDR: "0.0.0.0/0"}, // all traffic: too many ports
	)
	put("sg-b", Rule{Protocol: "tcp", FromPort: 22, ToPort: 22, CIDR: "0.0.0.0/0"}, Rule{Protocol: "tcp", FromPort: 1000, ToPort: 2000, CIDR: "0.0.0.0/0"})

	var list []string
	for _, p := range s.IngressPorts([]string{"sg-a", "sg-b", "sg-missing"}, 64) {
		list = append(list, p.Protocol+":"+strconv.Itoa(p.ContainerPort))
	}
	want := []string{"tcp:22", "udp:53", "tcp:8000", "tcp:8001", "tcp:8002"}
	if len(list) != len(want) {
		t.Fatalf("ports %v, want %v (the 1001-port range and all-traffic rule exceed the limit, 22 is listed once)", list, want)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Fatalf("ports %v, want %v", list, want)
		}
	}
	if n := len(s.IngressPorts([]string{"sg-a"}, 2)); n != 2 {
		t.Errorf("limit 2 returned %d ports", n)
	}
}
