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
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	CIDR           string    `json:"cidr"`
	Network        string    `json:"network"` // docker network name
	Default        bool      `json:"default"`
	InternetAccess bool      `json:"internet_access"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	Tags           core.Tags `json:"tags,omitempty"`
}

type Subnet struct {
	ID               string    `json:"id"`
	VpcID            string    `json:"vpc_id"`
	Name             string    `json:"name"`
	CIDR             string    `json:"cidr"`
	AvailabilityZone string    `json:"availability_zone"`
	Default          bool      `json:"default"`
	CreatedAt        time.Time `json:"created_at"`
}

type Rule struct {
	ID          string `json:"id"`
	Protocol    string `json:"protocol"` // tcp | udp
	FromPort    int    `json:"from_port"`
	ToPort      int    `json:"to_port"`
	CIDR        string `json:"cidr"`
	Description string `json:"description,omitempty"`
}

type SecurityGroup struct {
	ID          string    `json:"id"`
	VpcID       string    `json:"vpc_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Ingress     []Rule    `json:"ingress"`
	CreatedAt   time.Time `json:"created_at"`
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
	// AfterCreate is called with the Docker network of every new VPC.
	AfterCreate func(network string)
}

func New(env *svc.Env) *Service { return &Service{env: env} }

const maxPublishedRange = 32

// ---- lifecycle ----

// EnsureDefault creates the default VPC, its subnets and security group on first run,
// and recreates the Docker network if it went missing.
func (s *Service) EnsureDefault(ctx context.Context) error {
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
	sg := SecurityGroup{ID: core.NewID("sg"), VpcID: id, Name: "default", Description: "default VPC security group", Ingress: []Rule{}, CreatedAt: core.Now()}
	if s.AfterCreate != nil {
		go s.AfterCreate(v.Network)
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
			for p := r.FromPort; p <= r.ToPort; p++ {
				k := fmt.Sprintf("%d/%s", p, r.Protocol)
				if seen[k] {
					continue
				}
				seen[k] = true
				out = append(out, runtime.Port{ContainerPort: p, Protocol: r.Protocol})
			}
		}
	}
	return out
}

func (s *Service) GetVPC(id string) (VPC, error) { return store.Get[VPC](s.env.Store, cVPCs, id) }

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
	r.Handle("GET /api/v1/vpc/vpcs/{id}", "ec2:DescribeVpcs", s.getVPC)
	r.Handle("DELETE /api/v1/vpc/vpcs/{id}", "ec2:DeleteVpc", s.deleteVPC, httpx.Res("arn:hc:ec2:local-1:{account}:vpc/{id}"))
	r.Handle("GET /api/v1/vpc/subnets", "ec2:DescribeSubnets", s.listSubnets)
	r.Handle("POST /api/v1/vpc/subnets", "ec2:CreateSubnet", s.createSubnet)
	r.Handle("DELETE /api/v1/vpc/subnets/{id}", "ec2:DeleteSubnet", s.deleteSubnet)
	r.Handle("GET /api/v1/vpc/security-groups", "ec2:DescribeSecurityGroups", s.listSGs)
	r.Handle("POST /api/v1/vpc/security-groups", "ec2:CreateSecurityGroup", s.createSG)
	r.Handle("GET /api/v1/vpc/security-groups/{id}", "ec2:DescribeSecurityGroups", s.getSG)
	r.Handle("DELETE /api/v1/vpc/security-groups/{id}", "ec2:DeleteSecurityGroup", s.deleteSG)
	r.Handle("POST /api/v1/vpc/security-groups/{id}/ingress", "ec2:AuthorizeSecurityGroupIngress", s.addRule)
	r.Handle("DELETE /api/v1/vpc/security-groups/{id}/ingress/{rule}", "ec2:RevokeSecurityGroupIngress", s.removeRule)
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
	p, err := netip.ParsePrefix(in.CIDR)
	if err != nil || !p.Addr().Is4() {
		return nil, core.BadRequest("cidr %q is not a valid IPv4 CIDR block", in.CIDR)
	}
	p = p.Masked()
	if p.Bits() < 16 || p.Bits() > 28 {
		return nil, core.BadRequest("VPC CIDR must be between /16 and /28")
	}
	taken, err := s.env.Docker.NetworkSubnets()
	if err != nil {
		return nil, err
	}
	if n := s.overlapsAny(p, taken); n != "" {
		return nil, core.Conflict("cidr %s overlaps existing network %s", p, n)
	}
	internet := true
	if in.InternetAccess != nil {
		internet = *in.InternetAccess
	}
	v, err := s.createVPC(in.Name, p, internet, false)
	if err != nil {
		return nil, err
	}
	return s.vpcView(v), nil
}

func (s *Service) deleteVPC(c *httpx.Ctx) (any, error) {
	id := c.Param("id")
	v, err := store.Get[VPC](s.env.Store, cVPCs, id)
	if err != nil {
		return nil, core.NotFound("vpc", id)
	}
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		if sn.VpcID == id && s.subnetView(sn).UsedIPs > 0 {
			return nil, core.Errf(http.StatusConflict, "DependencyViolation", "vpc %s has resources in subnet %s; terminate them first", id, sn.ID)
		}
	}
	// Shared service endpoints (e.g. s3.internal) are attached to every VPC; detach them first.
	if info, err := s.env.Docker.C.NetworkInfo(v.Network); err == nil {
		for cid := range info.Containers {
			if c, err := s.env.Docker.Inspect(cid); err == nil && c.Config != nil && c.Config.Labels[core.LabelService] == "s3" {
				_ = s.env.Docker.C.DisconnectNetwork(v.Network, docker.NetworkConnectionOptions{Container: cid, Force: true})
			}
		}
	}
	if err := s.env.Docker.RemoveNetwork(v.Network); err != nil {
		return nil, core.Errf(http.StatusConflict, "DependencyViolation", "remove network: %v", err)
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
	return nil, store.Delete(s.env.Store, cVPCs, id)
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
	v, err := store.Get[VPC](s.env.Store, cVPCs, in.VpcID)
	if err != nil {
		return nil, core.NotFound("vpc", in.VpcID)
	}
	p, err := netip.ParsePrefix(in.CIDR)
	if err != nil || !p.Addr().Is4() {
		return nil, core.BadRequest("cidr %q is not a valid IPv4 CIDR block", in.CIDR)
	}
	p = p.Masked()
	vp := netip.MustParsePrefix(v.CIDR)
	if !vp.Contains(p.Addr()) || p.Bits() < vp.Bits() || p.Bits() > 28 {
		return nil, core.BadRequest("subnet %s must be inside %s and no smaller than /28", p, vp)
	}
	for _, sn := range store.List[Subnet](s.env.Store, cSubnets) {
		if sn.VpcID == v.ID && netip.MustParsePrefix(sn.CIDR).Overlaps(p) {
			return nil, core.Conflict("cidr %s conflicts with subnet %s (%s)", p, sn.ID, sn.CIDR)
		}
	}
	if in.AvailabilityZone == "" {
		in.AvailabilityZone = s.env.Cfg.Region + "a"
	}
	sn := Subnet{ID: core.NewID("subnet"), VpcID: v.ID, Name: in.Name, CIDR: p.String(), AvailabilityZone: in.AvailabilityZone, CreatedAt: core.Now()}
	return s.subnetView(sn), store.Put(s.env.Store, cSubnets, sn.ID, sn)
}

func (s *Service) deleteSubnet(c *httpx.Ctx) (any, error) {
	sn, err := store.Get[Subnet](s.env.Store, cSubnets, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("subnet", c.Param("id"))
	}
	if s.subnetView(sn).UsedIPs > 0 {
		return nil, core.Errf(http.StatusConflict, "DependencyViolation", "subnet %s still has resources", sn.ID)
	}
	return nil, store.Delete(s.env.Store, cSubnets, sn.ID)
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
	if in.VpcID == "" {
		for _, v := range store.List[VPC](s.env.Store, cVPCs) {
			if v.Default {
				in.VpcID = v.ID
			}
		}
	}
	if !store.Has(s.env.Store, cVPCs, in.VpcID) {
		return nil, core.NotFound("vpc", in.VpcID)
	}
	if strings.TrimSpace(in.Name) == "" || strings.HasPrefix(in.Name, "sg-") || len(in.Name) > 255 {
		return nil, core.BadRequest("name is required, may not start with sg- and must be at most 255 characters")
	}
	for _, g := range store.List[SecurityGroup](s.env.Store, cSGs) {
		if g.VpcID == in.VpcID && g.Name == in.Name {
			return nil, core.Conflict("security group %q already exists in %s", in.Name, in.VpcID)
		}
	}
	g := SecurityGroup{ID: core.NewID("sg"), VpcID: in.VpcID, Name: in.Name, Description: in.Description, Ingress: []Rule{}, CreatedAt: core.Now()}
	for _, r := range in.Ingress {
		nr, err := normalizeRule(r)
		if err != nil {
			return nil, err
		}
		g.Ingress = append(g.Ingress, nr)
	}
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
	if _, err := netip.ParsePrefix(r.CIDR); err != nil {
		return r, core.BadRequest("invalid cidr %q", r.CIDR)
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
	return g, err
}

func (s *Service) deleteSG(c *httpx.Ctx) (any, error) {
	g, err := store.Get[SecurityGroup](s.env.Store, cSGs, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("security group", c.Param("id"))
	}
	if g.Name == "default" {
		return nil, core.Errf(http.StatusConflict, "CannotDelete", "the default security group cannot be deleted")
	}
	if s.InUse != nil && s.InUse(g.ID) {
		return nil, core.Errf(http.StatusConflict, "DependencyViolation", "security group %s is in use", g.ID)
	}
	return nil, store.Delete(s.env.Store, cSGs, g.ID)
}
