package ec2

import (
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Elastic IPs. On a single host an Elastic IP is a stable public address
// record: an address from the configured pool (Config.ElasticIPPool, by
// default the documentation range 203.0.113.0/24) that stays with the account
// until released, follows an instance when associated and disassociated, and
// survives its stop and start. The address is what DescribeInstances and the
// instance metadata service report as the instance's public IP. Nothing is
// routed to it: an instance is still reached through its published ports on
// the HomeCloud host (see Config.PublicHost).

const (
	cAddresses         = "ec2_addresses"
	defaultElasticPool = "203.0.113.0/24"
)

// Address is an Elastic IP allocation.
type Address struct {
	AllocationID  string    `json:"allocation_id"`
	PublicIP      string    `json:"public_ip"`
	Domain        string    `json:"domain"`
	AssociationID string    `json:"association_id,omitempty"`
	InstanceID    string    `json:"instance_id,omitempty"`
	PrivateIP     string    `json:"private_ip,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Tags          core.Tags `json:"tags,omitempty"`
}

var eipMu sync.Mutex

func (s *Service) addresses() []Address {
	out := store.List[Address](s.env.Store, cAddresses)
	slices.SortFunc(out, func(a, b Address) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

func eipNotFound(id string) error {
	return core.Errf(http.StatusBadRequest, "InvalidAllocationID.NotFound", "The allocation ID '%s' does not exist", id)
}

// elasticIPFor returns the Elastic IP associated with an instance, or "".
func (s *Service) elasticIPFor(instanceID string) string {
	for _, a := range store.List[Address](s.env.Store, cAddresses) {
		if a.InstanceID == instanceID {
			return a.PublicIP
		}
	}
	return ""
}

// AllocateAddress reserves the next free address of the pool.
func (s *Service) AllocateAddress(tags core.Tags) (Address, error) {
	eipMu.Lock()
	defer eipMu.Unlock()
	pool := s.env.Cfg.ElasticIPPool
	if pool == "" {
		pool = defaultElasticPool
	}
	p, err := netip.ParsePrefix(pool)
	if err != nil || !p.Addr().Is4() {
		return Address{}, core.Errf(http.StatusInternalServerError, "InternalError", "elastic_ip_pool %q is not an IPv4 CIDR", pool)
	}
	p = p.Masked()
	used := map[string]bool{}
	for _, a := range store.List[Address](s.env.Store, cAddresses) {
		used[a.PublicIP] = true
	}
	// Skip the network and broadcast addresses of the pool.
	for ip := p.Addr().Next(); p.Contains(ip) && p.Contains(ip.Next()); ip = ip.Next() {
		if used[ip.String()] {
			continue
		}
		a := Address{AllocationID: core.NewID("eipalloc"), PublicIP: ip.String(), Domain: "vpc", CreatedAt: core.Now(), Tags: tags}
		return a, store.Put(s.env.Store, cAddresses, a.AllocationID, a)
	}
	return Address{}, core.Errf(http.StatusBadRequest, "AddressLimitExceeded", "The elastic IP pool %s is exhausted", pool)
}

func (s *Service) getAddress(id string) (Address, error) {
	a, err := store.Get[Address](s.env.Store, cAddresses, id)
	if err != nil {
		return a, eipNotFound(id)
	}
	return a, nil
}

// ReleaseAddress frees an address that is not associated.
func (s *Service) ReleaseAddress(id string) error {
	eipMu.Lock()
	defer eipMu.Unlock()
	a, err := s.getAddress(id)
	if err != nil {
		return err
	}
	if a.AssociationID != "" {
		return core.Errf(http.StatusBadRequest, "InvalidIPAddress.InUse", "The address %s is associated with %s and cannot be released", a.PublicIP, a.InstanceID)
	}
	return store.Delete(s.env.Store, cAddresses, id)
}

// AssociateAddress attaches an address to an instance. An instance has at
// most one Elastic IP: a second one replaces the first.
func (s *Service) AssociateAddress(id, instanceID string, reassociate bool) (Address, error) {
	i, err := s.get(instanceID)
	if err != nil || i.State == "terminated" || i.State == "shutting-down" {
		return Address{}, instanceNotFound(instanceID)
	}
	eipMu.Lock()
	defer eipMu.Unlock()
	a, err := s.getAddress(id)
	if err != nil {
		return a, err
	}
	if a.InstanceID == i.ID {
		return a, nil
	}
	if a.AssociationID != "" && !reassociate {
		return a, core.Errf(http.StatusBadRequest, "Resource.AlreadyAssociated", "resource %s is already associated with associate-id %s", id, a.AssociationID)
	}
	for _, o := range store.List[Address](s.env.Store, cAddresses) {
		if o.InstanceID == i.ID && o.AllocationID != id {
			o.AssociationID, o.InstanceID, o.PrivateIP = "", "", ""
			if err := store.Put(s.env.Store, cAddresses, o.AllocationID, o); err != nil {
				return a, err
			}
		}
	}
	a.AssociationID, a.InstanceID, a.PrivateIP = core.NewID("eipassoc"), i.ID, i.PrivateIP
	return a, store.Put(s.env.Store, cAddresses, a.AllocationID, a)
}

// DisassociateAddress detaches an address by association ID.
func (s *Service) DisassociateAddress(assocID string) (Address, error) {
	eipMu.Lock()
	defer eipMu.Unlock()
	a, ok := s.addressByAssociation(assocID)
	if !ok {
		return a, core.Errf(http.StatusBadRequest, "InvalidAssociationID.NotFound", "The association ID '%s' does not exist", assocID)
	}
	a.AssociationID, a.InstanceID, a.PrivateIP = "", "", ""
	return a, store.Put(s.env.Store, cAddresses, a.AllocationID, a)
}

func (s *Service) addressByAssociation(assocID string) (Address, bool) {
	for _, a := range store.List[Address](s.env.Store, cAddresses) {
		if a.AssociationID != "" && a.AssociationID == assocID {
			return a, true
		}
	}
	return Address{}, false
}

// dropAddress disassociates an instance's Elastic IP when it is terminated;
// the allocation stays.
func (s *Service) dropAddress(instanceID string) {
	eipMu.Lock()
	defer eipMu.Unlock()
	for _, a := range store.List[Address](s.env.Store, cAddresses) {
		if a.InstanceID == instanceID {
			a.AssociationID, a.InstanceID, a.PrivateIP = "", "", ""
			_ = store.Put(s.env.Store, cAddresses, a.AllocationID, a)
		}
	}
}

func (s *Service) eipARN(id string) string { return s.env.ARN("ec2", "elastic-ip/"+id) }

// ---- AWS API ----

func (s *Service) addressXML(a Address) map[string]any {
	m := map[string]any{"publicIp": a.PublicIP, "allocationId": a.AllocationID, "domain": a.Domain, "publicIpv4Pool": "amazon",
		"networkBorderGroup": core.Region, "tagSet": tagSet(a.Tags)}
	if a.InstanceID != "" {
		m["instanceId"], m["associationId"] = a.InstanceID, a.AssociationID
		m["networkInterfaceId"], m["networkInterfaceOwnerId"] = eniID(a.InstanceID), s.env.AccountID
		m["privateIpAddress"] = a.PrivateIP
	}
	return m
}

func (s *Service) awsAllocateAddress(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:AllocateAddress", q.ARN("ec2", "elastic-ip/*")); err != nil {
		return nil, err
	}
	if d := q.Param("Domain"); d != "" && d != "vpc" {
		return nil, invalid("Invalid value '%s' for domain: only vpc addresses are supported", d)
	}
	if q.Param("Address") != "" || q.Param("PublicIpv4Pool") != "" {
		return nil, invalid("bringing your own address or choosing a pool is not supported")
	}
	tags := tagSpecs(q, "elastic-ip")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	a, err := s.AllocateAddress(tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"publicIp": a.PublicIP, "allocationId": a.AllocationID, "domain": "vpc", "publicIpv4Pool": "amazon",
		"networkBorderGroup": core.Region}, nil
}

func (s *Service) awsDescribeAddresses(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeAddresses", "*"); err != nil {
		return nil, err
	}
	ids, ips, fs := q.List("AllocationId"), q.List("PublicIp"), filters(q)
	all := s.addresses()
	for _, id := range ids {
		if !slices.ContainsFunc(all, func(a Address) bool { return a.AllocationID == id }) {
			return nil, eipNotFound(id)
		}
	}
	for _, ip := range ips {
		if !slices.ContainsFunc(all, func(a Address) bool { return a.PublicIP == ip }) {
			return nil, core.Errf(http.StatusBadRequest, "InvalidAddress.NotFound", "Address '%s' not found.", ip)
		}
	}
	items := awsapi.Items{}
	for _, a := range all {
		if (len(ids) > 0 && !slices.Contains(ids, a.AllocationID)) || (len(ips) > 0 && !slices.Contains(ips, a.PublicIP)) {
			continue
		}
		f := attrs{}.set("allocation-id", a.AllocationID).set("public-ip", a.PublicIP).set("domain", a.Domain).
			set("association-id", nonEmpty(a.AssociationID)...).set("instance-id", nonEmpty(a.InstanceID)...).
			set("private-ip-address", nonEmpty(a.PrivateIP)...).set("network-border-group", core.Region).tags(a.Tags)
		if a.InstanceID != "" {
			f.set("network-interface-id", eniID(a.InstanceID)).set("network-interface-owner-id", s.env.AccountID)
		}
		if match(fs, f) {
			items = append(items, s.addressXML(a))
		}
	}
	return map[string]any{"addressesSet": items}, nil
}

func (s *Service) awsReleaseAddress(q *awsapi.Req) (any, error) {
	id := q.Param("AllocationId")
	if id == "" {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter AllocationId")
	}
	if err := q.Authorize("ec2:ReleaseAddress", s.eipARN(id)); err != nil {
		return nil, err
	}
	return nil, s.ReleaseAddress(id)
}

func (s *Service) awsAssociateAddress(q *awsapi.Req) (any, error) {
	id := q.Param("AllocationId")
	if id == "" {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter AllocationId")
	}
	if err := q.Authorize("ec2:AssociateAddress", s.eipARN(id)); err != nil {
		return nil, err
	}
	instanceID := q.Param("InstanceId")
	if eni := q.Param("NetworkInterfaceId"); eni != "" && instanceID == "" {
		if err := q.Authorize("ec2:AssociateAddress", q.ARN("ec2", "network-interface/"+eni)); err != nil {
			return nil, err
		}
		for _, i := range s.list() {
			if i.State != "terminated" && eniID(i.ID) == eni {
				instanceID = i.ID
			}
		}
		if instanceID == "" {
			return nil, core.Errf(http.StatusBadRequest, "InvalidNetworkInterfaceID.NotFound", "The networkInterface ID '%s' does not exist", eni)
		}
	}
	if instanceID == "" {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter InstanceId or NetworkInterfaceId")
	}
	if err := q.Authorize("ec2:AssociateAddress", q.ARN("ec2", "instance/"+instanceID)); err != nil {
		return nil, err
	}
	a, err := s.AssociateAddress(id, instanceID, q.Param("AllowReassociation") == "true")
	if err != nil {
		return nil, err
	}
	return map[string]any{"associationId": a.AssociationID}, nil
}

func (s *Service) awsDisassociateAddress(q *awsapi.Req) (any, error) {
	assoc := q.Param("AssociationId")
	if assoc == "" {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter AssociationId")
	}
	arn := "*"
	if a, ok := s.addressByAssociation(assoc); ok {
		arn = s.eipARN(a.AllocationID)
	}
	if err := q.Authorize("ec2:DisassociateAddress", arn); err != nil {
		return nil, err
	}
	_, err := s.DisassociateAddress(assoc)
	return nil, err
}

// ---- native routes ----

func (s *Service) addressRoutes(r *httpx.Router) {
	res := httpx.Res("arn:aws:ec2:{region}:{account}:elastic-ip/{id}")
	r.Handle("GET /api/v1/ec2/addresses", "ec2:DescribeAddresses", s.listAddresses)
	r.Handle("POST /api/v1/ec2/addresses", "ec2:AllocateAddress", s.allocateAddressRoute, httpx.Res("arn:aws:ec2:{region}:{account}:elastic-ip/*"))
	r.Handle("DELETE /api/v1/ec2/addresses/{id}", "ec2:ReleaseAddress", s.releaseAddressRoute, res)
	r.Handle("POST /api/v1/ec2/addresses/{id}/associate", "ec2:AssociateAddress", s.associateAddressRoute, res)
	r.Handle("POST /api/v1/ec2/addresses/{id}/disassociate", "ec2:DisassociateAddress", s.disassociateAddressRoute, res)
}

func (s *Service) listAddresses(c *httpx.Ctx) (any, error) { return s.addresses(), nil }

func (s *Service) allocateAddressRoute(c *httpx.Ctx) (any, error) {
	var in tagsBody
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	return s.AllocateAddress(in.Tags)
}

func (s *Service) releaseAddressRoute(c *httpx.Ctx) (any, error) {
	return nil, s.ReleaseAddress(c.Param("id"))
}

func (s *Service) associateAddressRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		InstanceID  string `json:"instance_id"`
		Reassociate bool   `json:"reassociate"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.InstanceID == "" {
		return nil, core.BadRequest("instance_id is required")
	}
	if err := c.Authorize("ec2:AssociateAddress", ec2ARN(c, "instance/"+in.InstanceID)); err != nil {
		return nil, err
	}
	return s.AssociateAddress(c.Param("id"), in.InstanceID, in.Reassociate)
}

func (s *Service) disassociateAddressRoute(c *httpx.Ctx) (any, error) {
	a, err := s.getAddress(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if a.AssociationID == "" {
		return a, nil
	}
	return s.DisassociateAddress(a.AssociationID)
}
