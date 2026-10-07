package vpc

import (
	"strings"
	"testing"
)

func testView(sgs ...SecurityGroup) (*fwView, []Member) {
	v := VPC{ID: "vpc-1", CIDR: "10.5.0.0/16"}
	m := map[string]SecurityGroup{}
	for _, g := range sgs {
		m[g.ID] = g
	}
	members := []Member{
		{Kind: "ec2", ID: "i-a", VpcID: "vpc-1", IP: "10.5.0.12", ContainerID: "ca", Groups: []string{"sg-app"}},
		{Kind: "ec2", ID: "i-a2", VpcID: "vpc-1", IP: "10.5.0.9", ContainerID: "ca2", Groups: []string{"sg-app"}},
		{Kind: "rds", ID: "db", VpcID: "vpc-1", IP: "10.5.0.20", ContainerID: "cdb", Groups: []string{"sg-db"}},
		{Kind: "ec2", ID: "i-d", VpcID: "vpc-1", IP: "10.5.0.30", ContainerID: "cd"}, // default group
		{Kind: "ec2", ID: "i-x", VpcID: "vpc-2", IP: "10.6.0.5", ContainerID: "cx", Groups: []string{"sg-app"}},
	}
	return newFWView(v, "sg-def", m, members), members
}

func has(t *testing.T, text string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(text, l+"\n") {
			t.Errorf("ruleset lacks %q:\n%s", l, text)
		}
	}
}

func TestRulesetIngressBySourceGroupAndCIDR(t *testing.T) {
	f, ms := testView(
		SecurityGroup{ID: "sg-app"},
		SecurityGroup{ID: "sg-def", Ingress: []Rule{selfRule("sg-def")}},
		SecurityGroup{ID: "sg-db", Ingress: []Rule{
			{Protocol: "tcp", FromPort: 5432, ToPort: 5432, SourceGroup: "sg-app"},
			{Protocol: "tcp", FromPort: 80, ToPort: 90, CIDR: "10.5.4.0/24"},
			{Protocol: "icmp", FromPort: 8, ToPort: -1, CIDR: "0.0.0.0/0"},
			{Protocol: "udp", FromPort: 53, ToPort: 53, PrefixList: "pl-1"}, // cannot be resolved: no rule
		}},
	)
	got := f.ruleset(ms[2])
	has(t, got,
		"-A HC-SG-IN -i lo -j ACCEPT",
		"-A HC-SG-IN -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		// members of sg-app in this VPC only, sorted
		"-A HC-SG-IN -s 10.5.0.9,10.5.0.12 -p tcp -m tcp --dport 5432 -j ACCEPT",
		"-A HC-SG-IN -s 10.5.4.0/24 -p tcp -m tcp --dport 80:90 -j ACCEPT",
		"-A HC-SG-IN -p icmp -m icmp --icmp-type 8 -j ACCEPT",
		"-A HC-SG-IN -j DROP",
		"-A HC-SG-OUT -j ACCEPT", // default egress
	)
	if strings.Contains(got, "10.6.0.5") || strings.Contains(got, "udp") {
		t.Errorf("foreign VPC member or unresolvable prefix list leaked into the rules:\n%s", got)
	}
	// A member without groups is in the default group: sg-def's self rule lists it.
	d := f.ruleset(ms[3])
	has(t, d, "-A HC-SG-IN -s 10.5.0.30 -j ACCEPT")
	// Sources of the group's rules follow membership changes.
	ms2 := append(ms[:1:1], ms[2:]...)
	f2 := newFWView(f.vpc, "sg-def", f.sgs, ms2)
	if strings.Contains(f2.ruleset(ms[2]), "10.5.0.9") {
		t.Error("a removed member is still allowed")
	}
}

func TestRulesetExternalPortsAndLoopbackRule(t *testing.T) {
	f, ms := testView(SecurityGroup{ID: "sg-db", Ingress: []Rule{{Protocol: "tcp", FromPort: 8080, ToPort: 8080, CIDR: "127.0.0.1/32"}}})
	m := ms[2]
	m.ExternalTCP = []int{5432}
	got := f.ruleset(m)
	has(t, got,
		"-A HC-SG-IN -p tcp -m tcp --dport 5432 -s 10.5.0.1 -j ACCEPT",
		"-A HC-SG-IN -p tcp -m tcp --dport 5432 ! -s 10.5.0.0/16 -j ACCEPT",
		"-A HC-SG-IN -s 10.5.0.1 -p tcp -m tcp --dport 8080 -j ACCEPT",
		"-A HC-SG-IN ! -s 10.5.0.0/16 -p tcp -m tcp --dport 8080 -j ACCEPT",
	)
	if strings.Contains(got, "10.5.255.253") {
		t.Errorf("HomeCloud's container address is trusted although HomeCloud runs on the host:\n%s", got)
	}
	// HomeCloud in a container dials external ports from its reserved address.
	f.api = APIAddress(f.vpc.CIDR)
	has(t, f.ruleset(m), "-A HC-SG-IN -p tcp -m tcp --dport 5432 -s 10.5.255.253 -j ACCEPT")
}

func TestRulesetEgress(t *testing.T) {
	f, ms := testView(
		SecurityGroup{ID: "sg-app", EgressSet: true, Egress: []Rule{
			{Protocol: "tcp", FromPort: 443, ToPort: 443, CIDR: "0.0.0.0/0"},
			{Protocol: "tcp", FromPort: 5432, ToPort: 5432, SourceGroup: "sg-db"},
		}},
		SecurityGroup{ID: "sg-db"},
	)
	got := f.ruleset(ms[0])
	has(t, got,
		"-A HC-SG-OUT -o lo -j ACCEPT",
		"-A HC-SG-OUT -d 10.5.0.2,10.5.255.254,169.254.169.254 -j ACCEPT", // resolver and metadata service
		"-A HC-SG-OUT -p tcp -m tcp --dport 443 -j ACCEPT",
		"-A HC-SG-OUT -d 10.5.0.20 -p tcp -m tcp --dport 5432 -j ACCEPT",
		"-A HC-SG-OUT -j DROP",
	)
	// Any group with the default allow-all rule keeps egress open.
	f, ms = testView(SecurityGroup{ID: "sg-app", EgressSet: true}, SecurityGroup{ID: "sg-x"})
	m := ms[0]
	m.Groups = []string{"sg-app", "sg-x"}
	if got := f.ruleset(m); !strings.Contains(got, "-A HC-SG-OUT -j ACCEPT\n") || strings.Contains(got, "HC-SG-OUT -j DROP") {
		t.Errorf("egress not open:\n%s", got)
	}
}

func TestRulesetIsDeterministic(t *testing.T) {
	f, ms := testView(SecurityGroup{ID: "sg-db", Ingress: []Rule{{Protocol: "-1", FromPort: -1, ToPort: -1, SourceGroup: "sg-app"}}}, SecurityGroup{ID: "sg-app"})
	a, b := f.ruleset(ms[2]), f.ruleset(ms[2])
	if a != b || rulesetHash(a) != rulesetHash(b) {
		t.Fatal("ruleset differs between renders")
	}
}
