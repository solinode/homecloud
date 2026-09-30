// Package vpc implements virtual private clouds on Docker bridge networks.
//
// A VPC is one bridge network spanning the VPC CIDR. Subnets are ranges of it,
// and resources launched into a subnet get a static IP from that range, so
// everything inside a VPC can reach everything else by private IP or DNS name.
// Security group ingress rules decide which ports are published on the host,
// which is how resources become reachable from outside the VPC.
package vpc

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	cVPCs    = "vpc_vpcs"
	cSubnets = "vpc_subnets"
	cSGs     = "vpc_security_groups"
	cIPs     = "vpc_ip_allocations"
)

type VPC struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	CIDR           string `json:"cidr"`
	Network        string `json:"network"` // docker network name
	Default        bool   `json:"default"`
	InternetAccess bool   `json:"internet_access"`
	// DNSHostnames and DNSSupportDisabled are the EC2 VPC attributes
	// enableDnsHostnames / enableDnsSupport (recorded; DNS always works).
	DNSHostnames       bool      `json:"dns_hostnames,omitempty"`
	DNSSupportDisabled bool      `json:"dns_support_disabled,omitempty"`
	State              string    `json:"state"`
	CreatedAt          time.Time `json:"created_at"`
	Tags               core.Tags `json:"tags,omitempty"`
}

type Subnet struct {
	ID               string `json:"id"`
	VpcID            string `json:"vpc_id"`
	Name             string `json:"name"`
	CIDR             string `json:"cidr"`
	AvailabilityZone string `json:"availability_zone"`
	Default          bool   `json:"default"`
	// MapPublicIP is the EC2 MapPublicIpOnLaunch attribute (recorded).
	MapPublicIP bool      `json:"map_public_ip,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Tags        core.Tags `json:"tags,omitempty"`
}

// Rule is a security group rule. Rules created through the native API are
// always enforceable (tcp/udp from 0.0.0.0/0 or 127.0.0.1/32); rules created
// through the EC2 API may use any protocol and source and are recorded, but
// only enforceable ones publish ports (see Publishable).
type Rule struct {
	ID          string `json:"id"`
	Protocol    string `json:"protocol"` // tcp | udp (EC2 API: also icmp, icmpv6, -1 or a protocol number)
	FromPort    int    `json:"from_port"`
	ToPort      int    `json:"to_port"`
	CIDR        string `json:"cidr,omitempty"`
	Description string `json:"description,omitempty"`
	// Other sources (EC2 API only): an IPv6 range, a security group or a prefix list.
	CIDRv6      string    `json:"cidr_ipv6,omitempty"`
	SourceGroup string    `json:"source_group,omitempty"`
	PrefixList  string    `json:"prefix_list,omitempty"`
	Tags        core.Tags `json:"tags,omitempty"`
}

type SecurityGroup struct {
	ID          string `json:"id"`
	VpcID       string `json:"vpc_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Ingress     []Rule `json:"ingress"`
	// Until EgressSet, a group has AWS's default rule allowing all outbound
	// traffic; once egress rules were changed they are enforced (fwrules.go).
	Egress    []Rule `json:"egress,omitempty"`
	EgressSet bool   `json:"egress_set,omitempty"`
	// SelfRule marks a default group that got AWS's rule allowing all
	// inbound traffic from members of the group itself (it can be revoked).
	SelfRule  bool      `json:"self_rule,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Tags      core.Tags `json:"tags,omitempty"`
}

type allocation struct {
	IP       string `json:"ip"`
	SubnetID string `json:"subnet_id"`
	Owner    string `json:"owner"`
}

type Service struct {
	env *svc.Env
	mu  sync.Mutex // serialises IP allocation
	// InUse reports whether a security group is referenced by a resource; set by EC2.
	InUse func(sgID string) bool
	// GroupChanged is called after a security group's rules were changed, so
	// running instances can be brought in line; set by EC2.
	GroupChanged func(sgID string)
	// AfterCreate is called with every new VPC.
	AfterCreate func(v VPC)
	// BeforeDelete is called before a VPC's network is removed (e.g. to drop
	// its route tables and detach its internet gateway).
	BeforeDelete func(v VPC)
	// NetworkChanged is called after a VPC's network was recreated (its
	// internet access changed); containers were reconnected with their addresses.
	NetworkChanged func(v VPC)
	fw             firewall
}

func New(env *svc.Env) *Service { return &Service{env: env} }

const maxPublishedRange = 32

// ---- lifecycle ----

// EnsureDefault creates the default VPC, its subnets and security group on first run,
// and recreates the Docker network if it went missing.
func (s *Service) EnsureDefault(ctx context.Context) error {
	s.seedSelfRules()
	for _, v := range store.List[VPC](s.env.Store, cVPCs) {
		if err := s.ensureNetwork(v); err != nil {
			return fmt.Errorf("vpc %s: %w", v.ID, err)
		}
	}
	for _, v := range store.List[VPC](s.env.Store, cVPCs) {
		if v.Default {
			return nil
		}
	}
	taken, err := s.env.Docker.NetworkSubnets()
	if err != nil {
		return err
	}
	var cidr netip.Prefix
	for i := 88; i < 120; i++ {
		p := netip.MustParsePrefix(fmt.Sprintf("10.%d.0.0/16", i))
		if s.overlapsAny(p, taken) == "" {
			cidr = p
			break
		}
	}
	if !cidr.IsValid() {
		return fmt.Errorf("no free 10.x.0.0/16 range for the default VPC")
	}
	v, err := s.createVPC("default", cidr, true, true)
	if err != nil {
		return err
	}
	base := cidr.Addr().As4()
	for i, az := range []string{"a", "b", "c"} {
		sub := netip.PrefixFrom(netip.AddrFrom4([4]byte{base[0], base[1], byte(i * 16), 0}), 20)
		sn := Subnet{ID: core.NewID("subnet"), VpcID: v.ID, Name: "default-" + s.env.Cfg.Region + az, CIDR: sub.String(),
			AvailabilityZone: s.env.Cfg.Region + az, Default: true, CreatedAt: core.Now()}
		if err := store.Put(s.env.Store, cSubnets, sn.ID, sn); err != nil {
			return err
		}
	}
	return nil
}

// selfRule is the rule of a default group that allows all inbound traffic
// from resources in the group itself.
func selfRule(groupID string) Rule {
	return Rule{ID: core.NewID("sgr"), Protocol: "-1", FromPort: -1, ToPort: -1, SourceGroup: groupID}
}

// seedSelfRules gives default groups that predate security group filtering
// AWS's self-referencing inbound rule (once; revoking it sticks).
func (s *Service) seedSelfRules() {
	for _, g := range store.List[SecurityGroup](s.env.Store, cSGs) {
		if g.Name != "default" || g.SelfRule {
			continue
		}
		_, _ = store.Update(s.env.Store, cSGs, g.ID, func(x *SecurityGroup) error {
			x.SelfRule = true
			x.Ingress = append(x.Ingress, selfRule(x.ID))
			return nil
		})
	}
}

func (s *Service) overlapsAny(p netip.Prefix, taken map[string]string) string {
	for c, name := range taken {
		if q, err := netip.ParsePrefix(c); err == nil && q.Addr().Is4() && q.Overlaps(p) {
			return name
		}
	}
	return ""
}

func (s *Service) ensureNetwork(v VPC) error {
	if _, err := s.env.Docker.C.NetworkInfo(v.Network); err == nil {
		return nil
	}
	p := netip.MustParsePrefix(v.CIDR)
	_, err := s.env.Docker.CreateNetwork(v.Network, p.String(), p.Addr().Next().String(), !v.InternetAccess,
		runtime.Labels("vpc", v.ID, nil))
	return err
}

func (s *Service) createVPC(name string, cidr netip.Prefix, internet, def bool) (VPC, error) {
	id := core.NewID("vpc")
	v := VPC{ID: id, Name: name, CIDR: cidr.String(), Network: "hc-" + id, Default: def, InternetAccess: internet, State: "available", CreatedAt: core.Now()}
	if err := s.ensureNetwork(v); err != nil {
		return v, fmt.Errorf("create network: %w", err)
	}
	if err := store.Put(s.env.Store, cVPCs, id, v); err != nil {
		return v, err
	}
	sg := SecurityGroup{ID: core.NewID("sg"), VpcID: id, Name: "default", Description: "default VPC security group", CreatedAt: core.Now()}
	sg.Ingress, sg.SelfRule = []Rule{selfRule(sg.ID)}, true
	if s.AfterCreate != nil {
		go s.AfterCreate(v)
	}
	return v, store.Put(s.env.Store, cSGs, sg.ID, sg)
}

// ---- API used by other services ----

type Placement struct {
	VPC     VPC
	Subnet  Subnet
	IP      string
	Network string
}

// Place resolves a subnet (the default VPC's first subnet when empty) and allocates an IP for owner.
func (s *Service) Place(subnetID, owner string) (*Placement, error) {
	var sn Subnet
	var err error
	if subnetID == "" {
		sn, err = s.defaultSubnet()
	} else {
		sn, err = store.Get[Subnet](s.env.Store, cSubnets, subnetID)
		if err != nil {
			err = core.NotFound("subnet", subnetID)
		}
	}
	if err != nil {
		return nil, err
	}
	v, err := store.Get[VPC](s.env.Store, cVPCs, sn.VpcID)
	if err != nil {
		return nil, core.NotFound("vpc", sn.VpcID)
	}
	ip, err := s.allocate(sn, owner)
	if err != nil {
		return nil, err
	}
	return &Placement{VPC: v, Subnet: sn, IP: ip, Network: v.Network}, nil
}

func (s *Service) defaultSubnet() (Subnet, error) {
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		if sn.Default && strings.HasSuffix(sn.AvailabilityZone, "a") {
			return sn, nil
		}
	}
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		return sn, nil
	}
	return Subnet{}, core.Errf(http.StatusConflict, "NoDefaultSubnet", "no subnet available; create a VPC and subnet first")
}

func (s *Service) allocate(sn Subnet, owner string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := netip.MustParsePrefix(sn.CIDR)
	used := map[string]bool{}
	if v, err := store.Get[VPC](s.env.Store, cVPCs, sn.VpcID); err == nil {
		used[MetadataAddress(v.CIDR)] = true // the instance metadata service's next hop
	}
	for _, a := range store.List[allocation](s.env.Store, cIPs) {
		used[a.IP] = true
		if a.Owner == owner {
			return a.IP, nil
		}
	}
	// AWS reserves the first four addresses and the last one of every subnet.
	addr := p.Addr()
	for i := 0; i < 4; i++ {
		addr = addr.Next()
	}
	last := lastAddr(p)
	for ; p.Contains(addr) && addr != last; addr = addr.Next() {
		if !used[addr.String()] {
			a := allocation{IP: addr.String(), SubnetID: sn.ID, Owner: owner}
			return a.IP, store.Put(s.env.Store, cIPs, a.IP, a)
		}
	}
	return "", core.Errf(http.StatusConflict, "InsufficientFreeAddressesInSubnet", "subnet %s has no free addresses", sn.ID)
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) | host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func (s *Service) Release(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range store.List[allocation](s.env.Store, cIPs) {
		if a.Owner == owner {
			_ = store.Delete(s.env.Store, cIPs, a.IP)
		}
	}
}

// DefaultSecurityGroup returns the "default" group of a VPC.
func (s *Service) DefaultSecurityGroup(vpcID string) string {
	for _, g := range store.List[SecurityGroup](s.env.Store, cSGs) {
		if g.VpcID == vpcID && g.Name == "default" {
			return g.ID
		}
	}
	return ""
}

// SubnetVPC returns the VPC of a subnet (the default subnet's VPC when id is empty).
func (s *Service) SubnetVPC(id string) (string, error) {
	if id == "" {
		sn, err := s.defaultSubnet()
		return sn.VpcID, err
	}
	sn, err := store.Get[Subnet](s.env.Store, cSubnets, id)
	if err != nil {
		return "", core.NotFound("subnet", id)
	}
	return sn.VpcID, nil
}

// CheckGroups verifies that every group exists and belongs to vpcID.
func (s *Service) CheckGroups(vpcID string, ids []string) error {
	for _, id := range ids {
		g, err := store.Get[SecurityGroup](s.env.Store, cSGs, id)
		if err != nil {
			return core.NotFound("security group", id)
		}
		if g.VpcID != vpcID {
			return core.BadRequest("security group %s belongs to %s, not %s", id, g.VpcID, vpcID)
		}
	}
	return nil
}

// PublishedPorts turns the ingress rules of the given groups into port bindings;
// Docker assigns each one a free host port.
func (s *Service) PublishedPorts(ids []string) []runtime.Port {
	seen := map[string]bool{}
	var out []runtime.Port
	for _, id := range ids {
		g, err := store.Get[SecurityGroup](s.env.Store, cSGs, id)
		if err != nil {
			continue
		}
		for _, r := range g.Ingress {
			if !Publishable(r) {
				continue
			}
			for p := r.FromPort; p <= r.ToPort; p++ {
				k := fmt.Sprintf("%d/%s", p, r.Protocol)
				if seen[k] {
					continue
				}
				seen[k] = true
				port := runtime.Port{ContainerPort: p, Protocol: r.Protocol}
				if r.CIDR == "127.0.0.1/32" {
					port.HostIP = "127.0.0.1"
				}
				out = append(out, port)
			}
		}
	}
	return out
}

func (s *Service) GetVPC(id string) (VPC, error) { return store.Get[VPC](s.env.Store, cVPCs, id) }

// List returns every VPC.
func (s *Service) List() []VPC { return store.List[VPC](s.env.Store, cVPCs) }

// DNSAddress is the VPC's resolver address: the base of its CIDR plus two, as in AWS.
// The allocator never hands it out (the first four addresses of a subnet are reserved).
func DNSAddress(cidr string) string { return reserved(cidr, 2) }

// MetadataAddress is where the instance metadata service (169.254.169.254)
// is reached in a VPC: the second-to-last address of the VPC CIDR, which the
// allocator never hands out.
func MetadataAddress(cidr string) string {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return ""
	}
	return lastAddr(p).Prev().String()
}

// S3Address is where the shared S3 endpoint (s3.internal) sits in a VPC: base plus three.
func S3Address(cidr string) string { return reserved(cidr, 3) }

func reserved(cidr string, n int) string {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return ""
	}
	a := p.Masked().Addr()
	for i := 0; i < n; i++ {
		a = a.Next()
	}
	return a.String()
}

// DefaultVPCID returns the ID of the default VPC.
func (s *Service) DefaultVPCID() string {
	for _, v := range store.List[VPC](s.env.Store, cVPCs) {
		if v.Default {
			return v.ID
		}
	}
	return ""
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	r.Handle("GET /api/v1/vpc/vpcs", "ec2:DescribeVpcs", s.listVPCs)
	r.Handle("POST /api/v1/vpc/vpcs", "ec2:CreateVpc", s.createVPCRoute)
	vpcRes := httpx.Res("arn:aws:ec2:{region}:{account}:vpc/{id}")
	subRes := httpx.Res("arn:aws:ec2:{region}:{account}:subnet/{id}")
	sgRes := httpx.Res("arn:aws:ec2:{region}:{account}:security-group/{id}")
	r.Handle("GET /api/v1/vpc/vpcs/{id}", "ec2:DescribeVpcs", s.getVPC, vpcRes)
	r.Handle("DELETE /api/v1/vpc/vpcs/{id}", "ec2:DeleteVpc", s.deleteVPC, httpx.Res("arn:aws:ec2:{region}:{account}:vpc/{id}"))
	r.Handle("GET /api/v1/vpc/subnets", "ec2:DescribeSubnets", s.listSubnets)
	r.Handle("POST /api/v1/vpc/subnets", "ec2:CreateSubnet", s.createSubnet)
	r.Handle("DELETE /api/v1/vpc/subnets/{id}", "ec2:DeleteSubnet", s.deleteSubnet, subRes)
	r.Handle("GET /api/v1/vpc/security-groups", "ec2:DescribeSecurityGroups", s.listSGs)
	r.Handle("POST /api/v1/vpc/security-groups", "ec2:CreateSecurityGroup", s.createSG)
	r.Handle("GET /api/v1/vpc/security-groups/{id}", "ec2:DescribeSecurityGroups", s.getSG, sgRes)
	r.Handle("DELETE /api/v1/vpc/security-groups/{id}", "ec2:DeleteSecurityGroup", s.deleteSG, sgRes)
	r.Handle("POST /api/v1/vpc/security-groups/{id}/ingress", "ec2:AuthorizeSecurityGroupIngress", s.addRule, sgRes)
	r.Handle("DELETE /api/v1/vpc/security-groups/{id}/ingress/{rule}", "ec2:RevokeSecurityGroupIngress", s.removeRule, sgRes)
}

type subnetView struct {
	Subnet
	AvailableIPs int `json:"available_ips"`
	UsedIPs      int `json:"used_ips"`
}

func (s *Service) subnetView(sn Subnet) subnetView {
	p := netip.MustParsePrefix(sn.CIDR)
	used := 0
	for _, a := range store.List[allocation](s.env.Store, cIPs) {
		if a.SubnetID == sn.ID {
			used++
		}
	}
	return subnetView{Subnet: sn, UsedIPs: used, AvailableIPs: (1 << (32 - p.Bits())) - 5 - used}
}

func (s *Service) vpcView(v VPC) map[string]any {
	subnets := []subnetView{}
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		if sn.VpcID == v.ID {
			subnets = append(subnets, s.subnetView(sn))
		}
	}
	return map[string]any{"id": v.ID, "name": v.Name, "cidr": v.CIDR, "network": v.Network, "default": v.Default,
		"internet_access": v.InternetAccess, "state": v.State, "created_at": v.CreatedAt, "tags": v.Tags, "subnets": subnets,
		"arn": s.env.ARN("ec2", "vpc/"+v.ID)}
}

func (s *Service) listVPCs(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, v := range store.List[VPC](s.env.Store, cVPCs) {
		out = append(out, s.vpcView(v))
	}
	return out, nil
}

func (s *Service) getVPC(c *httpx.Ctx) (any, error) {
	v, err := store.Get[VPC](s.env.Store, cVPCs, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("vpc", c.Param("id"))
	}
	return s.vpcView(v), nil
}

func (s *Service) createVPCRoute(c *httpx.Ctx) (any, error) {
	in := struct {
		Name           string `json:"name"`
		CIDR           string `json:"cidr"`
		InternetAccess *bool  `json:"internet_access"`
	}{}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	internet := true
	if in.InternetAccess != nil {
		internet = *in.InternetAccess
	}
	v, err := s.CreateVPC(in.Name, in.CIDR, internet, nil)
	if err != nil {
		return nil, err
	}
	return s.vpcView(v), nil
}

// CreateVPC validates a CIDR block and creates a VPC with its network and
// default security group.
func (s *Service) CreateVPC(name, cidr string, internet bool, tags core.Tags) (VPC, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() {
		return VPC{}, core.BadRequest("cidr %q is not a valid IPv4 CIDR block", cidr)
	}
	p = p.Masked()
	if p.Bits() < 16 || p.Bits() > 28 {
		return VPC{}, core.BadRequest("VPC CIDR must be between /16 and /28")
	}
	taken, err := s.env.Docker.NetworkSubnets()
	if err != nil {
		return VPC{}, err
	}
	if n := s.overlapsAny(p, taken); n != "" {
		return VPC{}, core.Conflict("cidr %s overlaps existing network %s", p, n)
	}
	v, err := s.createVPC(name, p, internet, false)
	if err != nil || len(tags) == 0 {
		return v, err
	}
	return store.Update(s.env.Store, cVPCs, v.ID, func(x *VPC) error { x.Tags = tags; return nil })
}

func (s *Service) deleteVPC(c *httpx.Ctx) (any, error) { return nil, s.DeleteVPC(c.Param("id")) }

// DeleteVPC removes a VPC with its subnets and security groups. It fails
// while resources hold addresses in it.
func (s *Service) DeleteVPC(id string) error {
	v, err := store.Get[VPC](s.env.Store, cVPCs, id)
	if err != nil {
		return core.NotFound("vpc", id)
	}
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		if sn.VpcID == id && s.subnetView(sn).UsedIPs > 0 {
			return core.Errf(http.StatusConflict, "DependencyViolation", "vpc %s has resources in subnet %s; terminate them first", id, sn.ID)
		}
	}
	// HomeCloud's own endpoints (DNS, s3.internal, instance metadata) are
	// attached to every VPC; detach them first.
	s.detachInfra(v.Network)
	if err := s.env.Docker.RemoveNetwork(v.Network); err != nil {
		return core.Errf(http.StatusConflict, "DependencyViolation", "remove network: %v", err)
	}
	if s.BeforeDelete != nil {
		s.BeforeDelete(v)
	}
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		if sn.VpcID == id {
			_ = store.Delete(s.env.Store, cSubnets, sn.ID)
		}
	}
	for _, g := range store.List[SecurityGroup](s.env.Store, cSGs) {
		if g.VpcID == id {
			_ = store.Delete(s.env.Store, cSGs, g.ID)
		}
	}
	return store.Delete(s.env.Store, cVPCs, id)
}

// infra reports whether a container is one of HomeCloud's shared endpoints
// (homecloud-dns, homecloud-s3, homecloud-imds, ...) rather than a resource.
func infra(c *docker.Container) bool {
	return strings.HasPrefix(strings.TrimPrefix(c.Name, "/"), "homecloud-") ||
		(c.Config != nil && c.Config.Labels[core.LabelResource] == "server")
}

func (s *Service) detachInfra(network string) {
	info, err := s.env.Docker.C.NetworkInfo(network)
	if err != nil {
		return
	}
	for cid := range info.Containers {
		if c, err := s.env.Docker.Inspect(cid); err == nil && infra(c) {
			_ = s.env.Docker.C.DisconnectNetwork(network, docker.NetworkConnectionOptions{Container: cid, Force: true})
		}
	}
}

func (s *Service) listSubnets(c *httpx.Ctx) (any, error) {
	out := []subnetView{}
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		if vid := c.Query("vpc_id"); vid == "" || sn.VpcID == vid {
			out = append(out, s.subnetView(sn))
		}
	}
	return out, nil
}

func (s *Service) createSubnet(c *httpx.Ctx) (any, error) {
	var in struct {
		VpcID            string `json:"vpc_id"`
		Name             string `json:"name"`
		CIDR             string `json:"cidr"`
		AvailabilityZone string `json:"availability_zone"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	sn, err := s.CreateSubnet(in.VpcID, in.Name, in.CIDR, in.AvailabilityZone, nil)
	if err != nil {
		return nil, err
	}
	return s.subnetView(sn), nil
}

// CreateSubnet validates and records a subnet of a VPC.
func (s *Service) CreateSubnet(vpcID, name, cidr, az string, tags core.Tags) (Subnet, error) {
	v, err := store.Get[VPC](s.env.Store, cVPCs, vpcID)
	if err != nil {
		return Subnet{}, core.NotFound("vpc", vpcID)
	}
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() {
		return Subnet{}, core.BadRequest("cidr %q is not a valid IPv4 CIDR block", cidr)
	}
	p = p.Masked()
	vp := netip.MustParsePrefix(v.CIDR)
	if !vp.Contains(p.Addr()) || p.Bits() < vp.Bits() || p.Bits() > 28 {
		return Subnet{}, core.BadRequest("subnet %s must be inside %s and no smaller than /28", p, vp)
	}
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		if sn.VpcID == v.ID && netip.MustParsePrefix(sn.CIDR).Overlaps(p) {
			return Subnet{}, core.Conflict("cidr %s conflicts with subnet %s (%s)", p, sn.ID, sn.CIDR)
		}
	}
	if az == "" {
		az = s.env.Cfg.Region + "a"
	}
	sn := Subnet{ID: core.NewID("subnet"), VpcID: v.ID, Name: name, CIDR: p.String(), AvailabilityZone: az, CreatedAt: core.Now(), Tags: tags}
	return sn, store.Put(s.env.Store, cSubnets, sn.ID, sn)
}

func (s *Service) deleteSubnet(c *httpx.Ctx) (any, error) { return nil, s.DeleteSubnet(c.Param("id")) }

// DeleteSubnet removes a subnet that no resource uses.
func (s *Service) DeleteSubnet(id string) error {
	sn, err := store.Get[Subnet](s.env.Store, cSubnets, id)
	if err != nil {
		return core.NotFound("subnet", id)
	}
	if s.subnetView(sn).UsedIPs > 0 {
		return core.Errf(http.StatusConflict, "DependencyViolation", "subnet %s still has resources", sn.ID)
	}
	return store.Delete(s.env.Store, cSubnets, sn.ID)
}

func (s *Service) listSGs(c *httpx.Ctx) (any, error) {
	out := []SecurityGroup{}
	for _, g := range store.List[SecurityGroup](s.env.Store, cSGs) {
		if vid := c.Query("vpc_id"); vid == "" || g.VpcID == vid {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *Service) getSG(c *httpx.Ctx) (any, error) {
	g, err := store.Get[SecurityGroup](s.env.Store, cSGs, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("security group", c.Param("id"))
	}
	return g, nil
}

func (s *Service) createSG(c *httpx.Ctx) (any, error) {
	var in struct {
		VpcID       string `json:"vpc_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Ingress     []Rule `json:"ingress"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	rules := []Rule{}
	for _, r := range in.Ingress {
		nr, err := normalizeRule(r)
		if err != nil {
			return nil, err
		}
		rules = append(rules, nr)
	}
	return s.CreateSecurityGroup(in.VpcID, in.Name, in.Description, nil, rules)
}

// CreateSecurityGroup creates a group in a VPC (the default VPC when vpcID is
// empty) with already-normalized ingress rules.
func (s *Service) CreateSecurityGroup(vpcID, name, description string, tags core.Tags, ingress []Rule) (SecurityGroup, error) {
	if vpcID == "" {
		vpcID = s.DefaultVPCID()
	}
	if !store.Has(s.env.Store, cVPCs, vpcID) {
		return SecurityGroup{}, core.NotFound("vpc", vpcID)
	}
	if strings.TrimSpace(name) == "" || strings.HasPrefix(name, "sg-") || len(name) > 255 {
		return SecurityGroup{}, core.BadRequest("name is required, may not start with sg- and must be at most 255 characters")
	}
	for _, g := range store.List[SecurityGroup](s.env.Store, cSGs) {
		if g.VpcID == vpcID && g.Name == name {
			return SecurityGroup{}, core.Errf(http.StatusConflict, "InvalidGroup.Duplicate", "security group %q already exists in %s", name, vpcID)
		}
	}
	if ingress == nil {
		ingress = []Rule{}
	}
	g := SecurityGroup{ID: core.NewID("sg"), VpcID: vpcID, Name: name, Description: description, Ingress: ingress, CreatedAt: core.Now(), Tags: tags}
	return g, store.Put(s.env.Store, cSGs, g.ID, g)
}

func normalizeRule(r Rule) (Rule, error) {
	r.Protocol = strings.ToLower(r.Protocol)
	if r.Protocol == "" {
		r.Protocol = "tcp"
	}
	if r.Protocol != "tcp" && r.Protocol != "udp" {
		return r, core.BadRequest("protocol must be tcp or udp")
	}
	if r.ToPort == 0 {
		r.ToPort = r.FromPort
	}
	if r.FromPort < 1 || r.ToPort > 65535 || r.FromPort > r.ToPort {
		return r, core.BadRequest("invalid port range %d-%d", r.FromPort, r.ToPort)
	}
	if r.ToPort-r.FromPort >= maxPublishedRange {
		return r, core.BadRequest("port ranges are limited to %d ports", maxPublishedRange)
	}
	if r.CIDR == "" {
		r.CIDR = "0.0.0.0/0"
	}
	// Rules become published host ports, which can be bound to every interface
	// or to loopback only; other source ranges cannot be enforced.
	switch r.CIDR {
	case "0.0.0.0/0", "127.0.0.1/32":
	default:
		if _, err := netip.ParsePrefix(r.CIDR); err != nil {
			return r, core.BadRequest("invalid cidr %q", r.CIDR)
		}
		return r, core.BadRequest("cidr must be 0.0.0.0/0 (reachable from anywhere) or 127.0.0.1/32 (this host only); HomeCloud cannot filter other source ranges")
	}
	r.ID = core.NewID("sgr")
	return r, nil
}

func (s *Service) addRule(c *httpx.Ctx) (any, error) {
	var in Rule
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	r, err := normalizeRule(in)
	if err != nil {
		return nil, err
	}
	g, err := store.Update(s.env.Store, cSGs, c.Param("id"), func(g *SecurityGroup) error {
		g.Ingress = append(g.Ingress, r)
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("security group", c.Param("id"))
	}
	s.groupChanged(g.ID, err)
	return g, err
}

func (s *Service) removeRule(c *httpx.Ctx) (any, error) {
	g, err := store.Update(s.env.Store, cSGs, c.Param("id"), func(g *SecurityGroup) error {
		n := len(g.Ingress)
		g.Ingress = slices.DeleteFunc(g.Ingress, func(r Rule) bool { return r.ID == c.Param("rule") })
		if len(g.Ingress) == n {
			return core.NotFound("rule", c.Param("rule"))
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("security group", c.Param("id"))
	}
	s.groupChanged(g.ID, err)
	return g, err
}

func (s *Service) deleteSG(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteSecurityGroup(c.Param("id"))
}

// DeleteSecurityGroup removes a group that no resource or other group's rule uses.
func (s *Service) DeleteSecurityGroup(id string) error {
	g, err := store.Get[SecurityGroup](s.env.Store, cSGs, id)
	if err != nil {
		return core.NotFound("security group", id)
	}
	if g.Name == "default" {
		return core.Errf(http.StatusConflict, "CannotDelete", "the default security group cannot be deleted")
	}
	if s.InUse != nil && s.InUse(g.ID) {
		return core.Errf(http.StatusConflict, "DependencyViolation", "security group %s is in use", g.ID)
	}
	for _, o := range store.List[SecurityGroup](s.env.Store, cSGs) {
		if o.ID == g.ID {
			continue
		}
		for _, r := range append(slices.Clone(o.Ingress), o.Egress...) {
			if r.SourceGroup == g.ID {
				return core.Errf(http.StatusConflict, "DependencyViolation", "security group %s is referenced by a rule of %s", g.ID, o.ID)
			}
		}
	}
	return store.Delete(s.env.Store, cSGs, g.ID)
}
