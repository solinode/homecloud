package vpc

import (
	"net/netip"
	"testing"
)

func TestLastAddr(t *testing.T) {
	cases := map[string]string{"10.88.0.0/20": "10.88.15.255", "10.0.0.0/16": "10.0.255.255", "192.168.1.16/28": "192.168.1.31"}
	for cidr, want := range cases {
		if got := lastAddr(netip.MustParsePrefix(cidr)).String(); got != want {
			t.Errorf("%s: got %s want %s", cidr, got, want)
		}
	}
}

func TestNormalizeRule(t *testing.T) {
	r, err := normalizeRule(Rule{FromPort: 80})
	if err != nil || r.ToPort != 80 || r.Protocol != "tcp" || r.CIDR != "0.0.0.0/0" {
		t.Fatalf("defaults: %+v %v", r, err)
	}
	for _, bad := range []Rule{{FromPort: 0}, {FromPort: 10, ToPort: 5}, {FromPort: 1, ToPort: 100}, {FromPort: 22, Protocol: "icmp"}, {FromPort: 22, CIDR: "nope"}} {
		if _, err := normalizeRule(bad); err == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
}
