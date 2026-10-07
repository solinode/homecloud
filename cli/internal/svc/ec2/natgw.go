package ec2

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// NAT gateways (EC2 API) are records, like Elastic IPs: HomeCloud gives a VPC
// internet access as a whole (an attached internet gateway with a default
// route, see network.go), so private subnets reach the internet whether or
// not their route goes through a NAT gateway. A NAT gateway takes a private
// address in its subnet and, when public, holds its Elastic IP (associated
// until the gateway is deleted). It is available at once; a deleted gateway is
// reported as deleted for an hour, as in AWS.

const (
	cNatGateways  = "ec2_nat_gateways"
	natDeletedTTL = time.Hour
)

type NatGateway struct {
	ID               string    `json:"id"`
	SubnetID         string    `json:"subnet_id"`
	VpcID            string    `json:"vpc_id"`
	ConnectivityType string    `json:"connectivity_type"`
	AllocationID     string    `json:"allocation_id,omitempty"`
	AssociationID    string    `json:"association_id,omitempty"`
	PublicIP         string    `json:"public_ip,omitempty"`
	PrivateIP        string    `json:"private_ip"`
	ENI              string    `json:"eni"`
	State            string    `json:"state"`
	ClientToken      string    `json:"client_token,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	DeletedAt        time.Time `json:"deleted_at,omitzero"`
	Tags             core.Tags `json:"tags,omitempty"`
}

var natMu sync.Mutex

func natNotFound(id string) error {
	return core.Errf(http.StatusBadRequest, "NatGatewayNotFound", "The Nat Gateway %s was not found", id)
}

func natOwner(id string) string { return "natgw:" + id }

// natGateways lists the gateways, dropping deleted ones older than an hour.
func (s *Service) natGateways() []NatGateway {
	var out []NatGateway
	for _, g := range store.List[NatGateway](s.env.Store, cNatGateways) {
		if g.State == "deleted" && time.Since(g.DeletedAt) > natDeletedTTL {
			_ = store.Delete(s.env.Store, cNatGateways, g.ID)
			continue
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b NatGateway) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// CreateNatGateway makes a gateway in a subnet; a public one needs an
// unassociated Elastic IP allocation.
func (s *Service) CreateNatGateway(subnetID, connectivity, allocationID, token string, tags core.Tags) (NatGateway, error) {
	if connectivity == "" {
		connectivity = "public"
	}
	if connectivity != "public" && connectivity != "private" {
		return NatGateway{}, invalid("Invalid value '%s' for ConnectivityType", connectivity)
	}
	if connectivity == "public" && allocationID == "" {
		return NatGateway{}, core.Errf(http.StatusBadRequest, "MissingParameter", "A public NAT gateway requires an AllocationId")
	}
	if connectivity == "private" && allocationID != "" {
		return NatGateway{}, invalid("A private NAT gateway cannot have an Elastic IP allocation")
	}
	sn, err := s.vpc.GetSubnet(subnetID)
	if err != nil {
		return NatGateway{}, subnetNotFound(subnetID)
	}
	natMu.Lock()
	defer natMu.Unlock()
	if token != "" {
		for _, g := range s.natGateways() {
			if g.ClientToken == token {
				return g, nil
			}
		}
	}
	g := NatGateway{ID: core.NewID("nat"), SubnetID: sn.ID, VpcID: sn.VpcID, ConnectivityType: connectivity,
		ENI: core.NewID("eni"), State: "available", ClientToken: token, CreatedAt: core.Now(), Tags: tags}
	pl, err := s.vpc.Place(sn.ID, natOwner(g.ID))
	if err != nil {
		return NatGateway{}, core.Errf(http.StatusBadRequest, "InsufficientFreeAddressesInSubnet", "%v", err)
	}
	g.PrivateIP = pl.IP
	if allocationID != "" {
		eipMu.Lock()
		a, err := s.getAddress(allocationID)
		if err == nil && a.AssociationID != "" {
			err = core.Errf(http.StatusBadRequest, "Resource.AlreadyAssociated", "Elastic IP address [%s] is already associated", allocationID)
		}
		if err == nil {
			a.AssociationID, a.NatGatewayID, a.PrivateIP, a.ENI = core.NewID("eipassoc"), g.ID, g.PrivateIP, g.ENI
			err = store.Put(s.env.Store, cAddresses, a.AllocationID, a)
		}
		eipMu.Unlock()
		if err != nil {
			s.vpc.Release(natOwner(g.ID))
			return NatGateway{}, err
		}
		g.AllocationID, g.AssociationID, g.PublicIP = a.AllocationID, a.AssociationID, a.PublicIP
	}
	if err := store.Put(s.env.Store, cNatGateways, g.ID, g); err != nil {
		return NatGateway{}, err
	}
	return g, nil
}

// DeleteNatGateway frees the gateway's address and Elastic IP; the record
// stays, in state deleted, for an hour.
func (s *Service) DeleteNatGateway(id string) (NatGateway, error) {
	natMu.Lock()
	defer natMu.Unlock()
	g, err := store.Get[NatGateway](s.env.Store, cNatGateways, id)
	if err != nil || g.State == "deleted" {
		return g, natNotFound(id)
	}
	s.vpc.Release(natOwner(g.ID))
	if g.AllocationID != "" {
		eipMu.Lock()
		if a, err := s.getAddress(g.AllocationID); err == nil && a.NatGatewayID == g.ID {
			a.AssociationID, a.NatGatewayID, a.PrivateIP, a.ENI = "", "", "", ""
			_ = store.Put(s.env.Store, cAddresses, a.AllocationID, a)
		}
		eipMu.Unlock()
	}
	g.State, g.DeletedAt = "deleted", core.Now()
	return g, store.Put(s.env.Store, cNatGateways, g.ID, g)
}

// natGatewayIn reports a live NAT gateway in a subnet (it blocks DeleteSubnet).
func (s *Service) natGatewayIn(subnetID string) string {
	for _, g := range s.natGateways() {
		if g.SubnetID == subnetID && g.State != "deleted" {
			return g.ID
		}
	}
	return ""
}

// ---- AWS API ----

func (s *Service) natXML(g NatGateway) map[string]any {
	addr := map[string]any{"networkInterfaceId": g.ENI, "privateIp": g.PrivateIP, "isPrimary": true,
		"status": "succeeded"}
	if g.AllocationID != "" {
		addr["allocationId"], addr["publicIp"], addr["associationId"] = g.AllocationID, g.PublicIP, g.AssociationID
	}
	if g.State == "deleted" {
		addr["status"] = "disassociated"
		delete(addr, "associationId")
	}
	m := map[string]any{"natGatewayId": g.ID, "subnetId": g.SubnetID, "vpcId": g.VpcID, "state": g.State,
		"connectivityType": g.ConnectivityType, "availabilityMode": "zonal", "createTime": g.CreatedAt.UTC().Format(time.RFC3339),
		"natGatewayAddressSet": awsapi.Items{addr}, "tagSet": tagSet(g.Tags)}
	if !g.DeletedAt.IsZero() {
		m["deleteTime"] = g.DeletedAt.UTC().Format(time.RFC3339)
	}
	return m
}

func (s *Service) awsCreateNatGateway(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:CreateNatGateway", q.ARN("ec2", "natgateway/*")); err != nil {
		return nil, err
	}
	subnet := q.Param("SubnetId")
	if subnet == "" {
		if q.Param("AvailabilityMode") == "regional" || q.Param("VpcId") != "" {
			return nil, core.Errf(http.StatusBadRequest, "UnsupportedOperation", "regional NAT gateways are not supported; create a zonal NAT gateway in a subnet")
		}
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter SubnetId")
	}
	if len(q.List("SecondaryAllocationId")) > 0 || q.Param("SecondaryPrivateIpAddressCount") != "" ||
		len(q.List("SecondaryPrivateIpAddress")) > 0 || q.Param("PrivateIpAddress") != "" {
		return nil, core.Errf(http.StatusBadRequest, "UnsupportedOperation", "secondary and chosen NAT gateway addresses are not supported")
	}
	tags := tagSpecs(q, "natgateway")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	g, err := s.CreateNatGateway(subnet, q.Param("ConnectivityType"), q.Param("AllocationId"), q.Param("ClientToken"), tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"natGateway": s.natXML(g), "clientToken": g.ClientToken}, nil
}

func (s *Service) awsDescribeNatGateways(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeNatGateways", "*"); err != nil {
		return nil, err
	}
	ids, fs := q.List("NatGatewayId"), filters(q)
	all := s.natGateways()
	for _, id := range ids {
		if !slices.ContainsFunc(all, func(g NatGateway) bool { return g.ID == id }) {
			return nil, natNotFound(id)
		}
	}
	want := idSet(ids)
	items := awsapi.Items{}
	for _, g := range all {
		a := attrs{}.set("nat-gateway-id", g.ID).set("state", g.State).set("subnet-id", g.SubnetID).set("vpc-id", g.VpcID).tags(g.Tags)
		if (len(ids) == 0 || want[g.ID]) && match(fs, a) {
			items = append(items, s.natXML(g))
		}
	}
	return map[string]any{"natGatewaySet": items}, nil
}

func (s *Service) awsDeleteNatGateway(q *awsapi.Req) (any, error) {
	id := q.Param("NatGatewayId")
	if !strings.HasPrefix(id, "nat-") {
		return nil, natNotFound(id)
	}
	if err := q.Authorize("ec2:DeleteNatGateway", q.ARN("ec2", "natgateway/"+id)); err != nil {
		return nil, err
	}
	if _, err := s.DeleteNatGateway(id); err != nil {
		return nil, err
	}
	return map[string]any{"natGatewayId": id}, nil
}
