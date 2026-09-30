package core

import (
	"net"
	"testing"
)

func TestBlockedIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "169.254.169.254", "::ffff:169.254.169.254", "::ffff:127.0.0.1", "0.0.0.0", "0.1.2.3",
		"100.100.100.200", "192.0.0.192", "fd00:ec2::254", "64:ff9b::7f00:1", "2002:7f00:1::", "fe80::1", "224.0.0.1"} {
		if !blockedIP(net.ParseIP(s)) {
			t.Errorf("%s is reachable", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111", "100.64.0.1"} {
		if blockedIP(net.ParseIP(s)) {
			t.Errorf("%s is blocked", s)
		}
	}
	// Every address of this host is off limits (Docker bridge gateways, public IP).
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !blockedIP(n.IP) {
			t.Errorf("host address %s is reachable", n.IP)
		}
	}
	DenyPrivateTargets = true
	defer func() { DenyPrivateTargets = false }()
	for _, s := range []string{"10.0.0.5", "172.17.0.1", "192.168.1.1", "fd12::1"} {
		if !blockedIP(net.ParseIP(s)) {
			t.Errorf("%s reachable with HOMECLOUD_DENY_PRIVATE_TARGETS", s)
		}
	}
}
