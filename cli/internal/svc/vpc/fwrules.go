package vpc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Security group enforcement inside the VPC.
//
// Every resource container that has security groups (an EC2 instance, an ECS
// task, an RDS or ElastiCache node, a load balancer) is a Member. For each
// one HomeCloud renders an iptables ruleset from the ingress and egress rules
// of its groups and loads it into the container's own network namespace (see
// fw.go). Ingress is default-deny: a packet reaches the resource only when a
// rule of one of its groups matches its protocol, port and source, where the
// source is a CIDR or "any resource that has group sg-x", which is resolved
// to those resources' current addresses. Replies to allowed connections
// (either direction) always pass (connection tracking), as in AWS.

// Member is a resource container that has security groups.
type Member struct {
	Kind        string // ec2, ecs, rds, elb
	ID          string // the resource's ID, for logs
	VpcID       string
	IP          string
	ContainerID string
	Groups      []string // empty means the VPC's default group, as in AWS
	// ExternalTCP are container ports published on the host whatever the
	// groups say (a publicly accessible database, a load balancer listener):
	// traffic from outside the VPC to them is accepted, groups filter the
	// traffic from inside it.
	ExternalTCP []int
}

const (
	chainIn  = "HC-SG-IN"
	chainOut = "HC-SG-OUT"
	// imdsAddr is the instance metadata address, reachable whatever the egress rules say.
	imdsAddr = "169.254.169.254"
)

// fwView is a VPC's members and groups, from which each member's ruleset is rendered.
type fwView struct {
	vpc       VPC
	gateway   string
	api       string // HomeCloud's own address when it runs in a container (APIAddress)
	defaultSG string
	sgs       map[string]SecurityGroup
	ips       map[string][]string // group -> addresses of its members
}

func newFWView(v VPC, defaultSG string, sgs map[string]SecurityGroup, members []Member) *fwView {
	f := &fwView{vpc: v, defaultSG: defaultSG, sgs: sgs, ips: map[string][]string{}}
	if p, err := netip.ParsePrefix(v.CIDR); err == nil {
		f.gateway = p.Masked().Addr().Next().String()
	}
	for _, m := range members {
		if m.VpcID != v.ID || m.IP == "" {
			continue
		}
		for _, g := range f.groups(m) {
			if !slices.Contains(f.ips[g], m.IP) {
				f.ips[g] = append(f.ips[g], m.IP)
			}
		}
	}
	for g := range f.ips {
		slices.SortFunc(f.ips[g], cmpAddr)
	}
	return f
}

func cmpAddr(a, b string) int {
	pa, ea := netip.ParseAddr(a)
	pb, eb := netip.ParseAddr(b)
	if ea != nil || eb != nil {
		return strings.Compare(a, b)
	}
	return pa.Compare(pb)
}

// groups is a member's effective security groups.
func (f *fwView) groups(m Member) []string {
	if len(m.Groups) == 0 && f.defaultSG != "" {
		return []string{f.defaultSG}
	}
	return m.Groups
}

// egressOpen reports whether a member's groups allow all outbound traffic
// (the AWS default), in which case egress is not filtered at all.
func (f *fwView) egressOpen(m Member) bool {
	for _, id := range f.groups(m) {
		g, ok := f.sgs[id]
		if !ok {
			continue
		}
		for _, r := range EgressRules(g) {
			if r.Protocol == "-1" && r.CIDR == "0.0.0.0/0" {
				return true
			}
		}
	}
	return false
}

// ruleset renders the iptables-restore input for one member.
func (f *fwView) ruleset(m Member) string {
	var b strings.Builder
	b.WriteString("*filter\n:" + chainIn + " - [0:0]\n:" + chainOut + " - [0:0]\n-F " + chainIn + "\n-F " + chainOut + "\n")
	seen := map[string]bool{}
	add := func(chain, line string) {
		l := "-A " + chain + " " + line
		if !seen[l] {
			seen[l] = true
			b.WriteString(l + "\n")
		}
	}
	// ingress
	add(chainIn, "-i lo -j ACCEPT")
	add(chainIn, "-m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT")
	for _, port := range m.ExternalTCP {
		add(chainIn, fmt.Sprintf("-p tcp -m tcp --dport %d -s %s -j ACCEPT", port, f.gateway))
		if f.api != "" { // HomeCloud dials it directly (a function's emulator)
			add(chainIn, fmt.Sprintf("-p tcp -m tcp --dport %d -s %s -j ACCEPT", port, f.api))
		}
		add(chainIn, fmt.Sprintf("-p tcp -m tcp --dport %d ! -s %s -j ACCEPT", port, f.vpc.CIDR))
	}
	for _, id := range f.groups(m) {
		g, ok := f.sgs[id]
		if !ok {
			continue
		}
		for _, r := range g.Ingress {
			f.rule(r, true, func(l string) { add(chainIn, l) })
		}
	}
	add(chainIn, "-j DROP")
	// egress
	if f.egressOpen(m) {
		add(chainOut, "-j ACCEPT")
	} else {
		add(chainOut, "-o lo -j ACCEPT")
		add(chainOut, "-m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT")
		add(chainOut, "-d "+strings.Join([]string{DNSAddress(f.vpc.CIDR), MetadataAddress(f.vpc.CIDR), imdsAddr}, ",")+" -j ACCEPT")
		for _, id := range f.groups(m) {
			g, ok := f.sgs[id]
			if !ok {
				continue
			}
			for _, r := range EgressRules(g) {
				f.rule(r, false, func(l string) { add(chainOut, l) })
			}
		}
		add(chainOut, "-j DROP")
	}
	b.WriteString("COMMIT\n")
	return b.String()
}

// rule renders one security group rule as ACCEPT lines (none when it cannot
// match: an IPv6 or prefix list source, or a group without members).
func (f *fwView) rule(r Rule, ingress bool, emit func(line string)) {
	proto, ok := protoMatch(r)
	if !ok {
		return
	}
	flag := "-s"
	if !ingress {
		flag = "-d"
	}
	var addrs []string // "" = any address
	switch {
	case r.CIDR == "0.0.0.0/0":
		addrs = []string{""}
	case r.CIDR == "127.0.0.1/32" && ingress:
		// From this host only: the published host port. Host connections come
		// from the network's gateway, or from outside the VPC's range.
		addrs = []string{flag + " " + f.gateway, "! " + flag + " " + f.vpc.CIDR}
	case r.CIDR != "":
		addrs = []string{flag + " " + r.CIDR}
	case r.SourceGroup != "":
		ips := f.ips[r.SourceGroup]
		for len(ips) > 0 {
			n := min(len(ips), 32)
			addrs = append(addrs, flag+" "+strings.Join(ips[:n], ","))
			ips = ips[n:]
		}
	}
	chainProto := strings.TrimSpace(proto)
	for _, a := range addrs {
		parts := []string{}
		if a != "" {
			parts = append(parts, a)
		}
		if chainProto != "" {
			parts = append(parts, chainProto)
		}
		parts = append(parts, "-j ACCEPT")
		emit(strings.Join(parts, " "))
	}
}

// protoMatch renders the protocol and port part of a rule ("" matches everything).
func protoMatch(r Rule) (string, bool) {
	switch r.Protocol {
	case "-1", "":
		return "", true
	case "tcp", "udp":
		if r.FromPort <= 0 && r.ToPort >= 65535 || r.FromPort < 0 {
			return "-p " + r.Protocol, true
		}
		if r.FromPort == r.ToPort {
			return fmt.Sprintf("-p %s -m %s --dport %d", r.Protocol, r.Protocol, r.FromPort), true
		}
		return fmt.Sprintf("-p %s -m %s --dport %d:%d", r.Protocol, r.Protocol, r.FromPort, r.ToPort), true
	case "icmp":
		switch {
		case r.FromPort < 0:
			return "-p icmp", true
		case r.ToPort < 0:
			return fmt.Sprintf("-p icmp -m icmp --icmp-type %d", r.FromPort), true
		}
		return fmt.Sprintf("-p icmp -m icmp --icmp-type %d/%d", r.FromPort, r.ToPort), true
	case "icmpv6":
		return "", false // the VPC networks are IPv4 only
	}
	if len(r.Protocol) > 0 && strings.Trim(r.Protocol, "0123456789") == "" {
		return "-p " + r.Protocol, true
	}
	return "", false
}

func rulesetHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}
