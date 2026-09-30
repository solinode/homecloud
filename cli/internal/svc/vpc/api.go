package vpc

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Store collections, for services that tag VPC resources generically.
const (
	CollectionVPCs           = cVPCs
	CollectionSubnets        = cSubnets
	CollectionSecurityGroups = cSGs
)

// Subnets returns every subnet.
func (s *Service) Subnets() []Subnet { return store.List[Subnet](s.env.Store, cSubnets) }

// GetSubnet returns a subnet.
func (s *Service) GetSubnet(id string) (Subnet, error) {
	sn, err := store.Get[Subnet](s.env.Store, cSubnets, id)
	if err != nil {
		return sn, core.NotFound("subnet", id)
	}
	return sn, nil
}

// SubnetUsage returns the addresses in use and still free in a subnet.
func (s *Service) SubnetUsage(sn Subnet) (used, free int) {
	v := s.subnetView(sn)
	return v.UsedIPs, v.AvailableIPs
}

// SecurityGroups returns every security group.
func (s *Service) SecurityGroups() []SecurityGroup {
	return store.List[SecurityGroup](s.env.Store, cSGs)
}

// GetSecurityGroup returns a security group.
func (s *Service) GetSecurityGroup(id string) (SecurityGroup, error) {
	g, err := store.Get[SecurityGroup](s.env.Store, cSGs, id)
	if err != nil {
		return g, core.NotFound("security group", id)
	}
	return g, nil
}

// UpdateVPC changes a VPC record.
func (s *Service) UpdateVPC(id string, fn func(*VPC) error) (VPC, error) {
	v, err := store.Update(s.env.Store, cVPCs, id, fn)
	if err == store.ErrNotFound {
		return v, core.NotFound("vpc", id)
	}
	return v, err
}

// UpdateSubnet changes a subnet record.
func (s *Service) UpdateSubnet(id string, fn func(*Subnet) error) (Subnet, error) {
	sn, err := store.Update(s.env.Store, cSubnets, id, fn)
	if err == store.ErrNotFound {
		return sn, core.NotFound("subnet", id)
	}
	return sn, err
}

// UpdateSecurityGroup changes a security group (its rules or tags).
func (s *Service) UpdateSecurityGroup(id string, fn func(*SecurityGroup) error) (SecurityGroup, error) {
	g, err := store.Update(s.env.Store, cSGs, id, fn)
	if err == store.ErrNotFound {
		return g, core.NotFound("security group", id)
	}
	s.groupChanged(id, err)
	return g, err
}

// groupChanged tells the hook a group's rules changed (when the change succeeded).
func (s *Service) groupChanged(id string, err error) {
	if err == nil && s.GroupChanged != nil {
		s.GroupChanged(id)
	}
}

// EgressRules returns a group's outbound rules, including AWS's default
// allow-all rule for groups whose egress was never changed.
func EgressRules(g SecurityGroup) []Rule {
	if g.EgressSet {
		return g.Egress
	}
	return []Rule{DefaultEgress(g.ID)}
}

// DefaultEgress is the allow-all outbound rule every new group starts with.
func DefaultEgress(groupID string) Rule {
	return Rule{ID: "sgr-" + strings.TrimPrefix(groupID, "sg-"), Protocol: "-1", FromPort: -1, ToPort: -1, CIDR: "0.0.0.0/0"}
}

// Publishable reports whether an ingress rule can be enforced by publishing
// host ports: TCP or UDP, from anywhere or from this host only, and at most
// maxPublishedRange ports. Other rules are recorded but have no effect beyond
// the VPC, where all traffic is allowed.
func Publishable(r Rule) bool {
	return (r.Protocol == "tcp" || r.Protocol == "udp") && (r.CIDR == "0.0.0.0/0" || r.CIDR == "127.0.0.1/32") &&
		r.FromPort >= 1 && r.ToPort <= 65535 && r.FromPort <= r.ToPort && r.ToPort-r.FromPort < maxPublishedRange
}

// NormalizeAWSRule validates a rule from the EC2 API, which may use any
// protocol and source. Exactly one source must be set.
func (s *Service) NormalizeAWSRule(r Rule) (Rule, error) {
	p := strings.ToLower(strings.TrimSpace(r.Protocol))
	switch p {
	case "6":
		p = "tcp"
	case "17":
		p = "udp"
	case "1":
		p = "icmp"
	case "58":
		p = "icmpv6"
	case "all", "":
		p = "-1"
	}
	switch p {
	case "tcp", "udp":
		if r.FromPort < 0 || r.ToPort > 65535 || r.FromPort > r.ToPort {
			return r, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "invalid port range %d-%d", r.FromPort, r.ToPort)
		}
	case "icmp", "icmpv6":
	case "-1":
		r.FromPort, r.ToPort = -1, -1
	default:
		if n, err := strconv.Atoi(p); err != nil || n < 0 || n > 255 {
			return r, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "invalid IP protocol %q", r.Protocol)
		}
		r.FromPort, r.ToPort = -1, -1
	}
	r.Protocol = p
	n := 0
	for _, v := range []string{r.CIDR, r.CIDRv6, r.SourceGroup, r.PrefixList} {
		if v != "" {
			n++
		}
	}
	if n != 1 {
		return r, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "a rule needs exactly one source (CIDR, IPv6 CIDR, security group or prefix list)")
	}
	if r.CIDR != "" {
		pf, err := netip.ParsePrefix(r.CIDR)
		if err != nil || !pf.Addr().Is4() {
			return r, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "CIDR block %s is malformed", r.CIDR)
		}
	}
	if r.CIDRv6 != "" {
		if pf, err := netip.ParsePrefix(r.CIDRv6); err != nil || !pf.Addr().Is6() {
			return r, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "CIDR block %s is malformed", r.CIDRv6)
		}
	}
	if r.SourceGroup != "" && !store.Has(s.env.Store, cSGs, r.SourceGroup) {
		return r, core.NotFound("security group", r.SourceGroup)
	}
	if r.ID == "" {
		r.ID = core.NewID("sgr")
	}
	return r, nil
}

// SameRule reports whether two rules have the same protocol, ports and source.
func SameRule(a, b Rule) bool {
	return a.Protocol == b.Protocol && a.FromPort == b.FromPort && a.ToPort == b.ToPort && a.CIDR == b.CIDR &&
		a.CIDRv6 == b.CIDRv6 && a.SourceGroup == b.SourceGroup && a.PrefixList == b.PrefixList
}

// ---- internet access ----

// SetInternetAccess turns a VPC's internet access on or off. Docker cannot
// change this on a live network, so the network is recreated and every
// container on it is reconnected with its address and aliases.
func (s *Service) SetInternetAccess(ctx context.Context, id string, on bool) error {
	v, err := store.Get[VPC](s.env.Store, cVPCs, id)
	if err != nil {
		return core.NotFound("vpc", id)
	}
	if v.InternetAccess == on {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	type member struct {
		id, ip  string
		aliases []string
	}
	var members []member
	cs, err := s.env.Docker.C.ListContainers(docker.ListContainersOptions{All: true, Filters: map[string][]string{"network": {v.Network}}})
	if err != nil {
		return err
	}
	for _, c := range cs {
		ci, err := s.env.Docker.Inspect(c.ID)
		if err != nil || ci.NetworkSettings == nil {
			continue
		}
		ep, ok := ci.NetworkSettings.Networks[v.Network]
		if !ok {
			continue
		}
		ip := ep.IPAddress
		if ip == "" && ci.Config != nil { // stopped: the address HomeCloud allocated to its resource
			for _, a := range store.List[allocation](s.env.Store, cIPs) {
				if a.Owner != "" && a.Owner == ci.Config.Labels[core.LabelResource] {
					ip = a.IP
				}
			}
		}
		var aliases []string
		for _, a := range ep.Aliases {
			if !strings.HasPrefix(ci.ID, a) { // Docker adds the short container ID itself
				aliases = append(aliases, a)
			}
		}
		members = append(members, member{id: c.ID, ip: ip, aliases: aliases})
	}
	for _, m := range members {
		if err := s.env.Docker.C.DisconnectNetwork(v.Network, docker.NetworkConnectionOptions{Container: m.id, Force: true}); err != nil {
			log.Printf("vpc %s: disconnect %s: %v", id, m.id[:12], err)
		}
	}
	reconnect := func() {
		for _, m := range members {
			if err := s.env.Docker.ConnectIP(v.Network, m.id, m.ip, m.aliases...); err != nil {
				log.Printf("vpc %s: reconnect %s: %v", id, m.id[:12], err)
			}
		}
	}
	if err := s.env.Docker.RemoveNetwork(v.Network); err != nil {
		reconnect()
		return core.Errf(http.StatusConflict, "DependencyViolation", "recreate network: %v", err)
	}
	p := netip.MustParsePrefix(v.CIDR)
	if _, err := s.env.Docker.CreateNetwork(v.Network, p.String(), p.Addr().Next().String(), !on, runtime.Labels("vpc", v.ID, nil)); err != nil {
		// Put the old network back so the VPC keeps working.
		_, _ = s.env.Docker.CreateNetwork(v.Network, p.String(), p.Addr().Next().String(), !v.InternetAccess, runtime.Labels("vpc", v.ID, nil))
		reconnect()
		return fmt.Errorf("recreate network: %w", err)
	}
	reconnect()
	v, err = store.Update(s.env.Store, cVPCs, id, func(x *VPC) error { x.InternetAccess = on; return nil })
	if err != nil {
		return err
	}
	if s.NetworkChanged != nil {
		go s.NetworkChanged(v)
	}
	return nil
}
