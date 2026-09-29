package ec2

import (
	"context"
	"log"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// Internet gateways and route tables (EC2 API). They are recorded as in EC2;
// what HomeCloud enforces is a VPC's internet access: a VPC reaches the
// internet exactly when it has an attached internet gateway and one of its
// route tables sends 0.0.0.0/0 to it. Changing that recreates the VPC's
// Docker network (see vpc.SetInternetAccess).
//
// VPCs created outside the EC2 API (the default VPC, the native API) get a
// main route table, and an internet gateway with a default route when they
// have internet access, the first time the EC2 API looks at them.

const (
	cIGWs        = "ec2_internet_gateways"
	cRouteTables = "ec2_route_tables"
)

type InternetGateway struct {
	ID    string    `json:"id"`
	VpcID string    `json:"vpc_id,omitempty"`
	Tags  core.Tags `json:"tags,omitempty"`
	// Auto marks the gateway HomeCloud created to represent a VPC's
	// existing internet access; it is removed with the VPC.
	Auto bool `json:"auto,omitempty"`
}

type Route struct {
	Destination string `json:"destination"`
	GatewayID   string `json:"gateway_id,omitempty"` // igw-..., or another target recorded as is
	Target      string `json:"target,omitempty"`     // non-gateway target (nat-, eni-, pcx-, i-, ...)
	TargetKind  string `json:"target_kind,omitempty"`
	Origin      string `json:"origin"`
}

type RouteAssociation struct {
	ID       string `json:"id"`
	SubnetID string `json:"subnet_id,omitempty"`
	Main     bool   `json:"main,omitempty"`
}

type RouteTable struct {
	ID           string             `json:"id"`
	VpcID        string             `json:"vpc_id"`
	Routes       []Route            `json:"routes"`
	Associations []RouteAssociation `json:"associations"`
	Tags         core.Tags          `json:"tags,omitempty"`
}

func (t RouteTable) main() bool {
	return slices.ContainsFunc(t.Associations, func(a RouteAssociation) bool { return a.Main })
}

var netMu sync.Mutex // serialises route table / gateway changes and their effect

// ensureVPCNet gives a VPC its main route table (and, for a VPC that already
// has internet access, an internet gateway with a default route).
func (s *Service) ensureVPCNet(v vpc.VPC) {
	for _, t := range store.List[RouteTable](s.env.Store, cRouteTables) {
		if t.VpcID == v.ID && t.main() {
			return
		}
	}
	t := RouteTable{ID: core.NewID("rtb"), VpcID: v.ID, Routes: []Route{},
		Associations: []RouteAssociation{{ID: core.NewID("rtbassoc"), Main: true}}}
	if v.InternetAccess {
		g := InternetGateway{ID: core.NewID("igw"), VpcID: v.ID, Auto: true}
		if err := store.Put(s.env.Store, cIGWs, g.ID, g); err != nil {
			return
		}
		t.Routes = append(t.Routes, Route{Destination: "0.0.0.0/0", GatewayID: g.ID, Origin: "CreateRoute"})
	}
	_ = store.Put(s.env.Store, cRouteTables, t.ID, t)
}

func (s *Service) ensureAllVPCNet() {
	netMu.Lock()
	defer netMu.Unlock()
	for _, v := range s.vpc.List() {
		s.ensureVPCNet(v)
	}
}

// VPCDeleted drops a deleted VPC's route tables and detaches its gateways.
func (s *Service) VPCDeleted(v vpc.VPC) {
	netMu.Lock()
	defer netMu.Unlock()
	for _, t := range store.List[RouteTable](s.env.Store, cRouteTables) {
		if t.VpcID == v.ID {
			_ = store.Delete(s.env.Store, cRouteTables, t.ID)
		}
	}
	for _, g := range store.List[InternetGateway](s.env.Store, cIGWs) {
		if g.VpcID != v.ID {
			continue
		}
		if g.Auto {
			_ = store.Delete(s.env.Store, cIGWs, g.ID)
			continue
		}
		_, _ = store.Update(s.env.Store, cIGWs, g.ID, func(x *InternetGateway) error { x.VpcID = ""; return nil })
	}
}

// wantsInternet reports whether a VPC's gateways and routes give it internet access.
func (s *Service) wantsInternet(vpcID string) bool {
	attached := map[string]bool{}
	for _, g := range store.List[InternetGateway](s.env.Store, cIGWs) {
		if g.VpcID == vpcID {
			attached[g.ID] = true
		}
	}
	if len(attached) == 0 {
		return false
	}
	for _, t := range store.List[RouteTable](s.env.Store, cRouteTables) {
		if t.VpcID != vpcID {
			continue
		}
		for _, r := range t.Routes {
			if r.Destination == "0.0.0.0/0" && attached[r.GatewayID] {
				return true
			}
		}
	}
	return false
}

// applyInternet makes a VPC's internet access follow its gateways and routes.
func (s *Service) applyInternet(vpcID string) {
	v, err := s.vpc.GetVPC(vpcID)
	if err != nil {
		return
	}
	want := s.wantsInternet(vpcID)
	if want == v.InternetAccess {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.vpc.SetInternetAccess(ctx, vpcID, want); err != nil {
		log.Printf("ec2: set internet access of %s to %v: %v", vpcID, want, err)
	}
}

func igwNotFound(id string) error {
	return core.Errf(http.StatusBadRequest, "InvalidInternetGatewayID.NotFound", "The internetGateway ID '%s' does not exist", id)
}

func rtbNotFound(id string) error {
	return core.Errf(http.StatusBadRequest, "InvalidRouteTableID.NotFound", "The routeTable ID '%s' does not exist", id)
}

func (s *Service) CreateInternetGateway(tags core.Tags) (InternetGateway, error) {
	g := InternetGateway{ID: core.NewID("igw"), Tags: tags}
	return g, store.Put(s.env.Store, cIGWs, g.ID, g)
}

func (s *Service) AttachInternetGateway(id, vpcID string) error {
	netMu.Lock()
	defer netMu.Unlock()
	if _, err := s.vpc.GetVPC(vpcID); err != nil {
		return vpcNotFound(vpcID)
	}
	for _, g := range store.List[InternetGateway](s.env.Store, cIGWs) {
		if g.VpcID == vpcID && g.ID != id {
			return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "Network %s already has an internet gateway attached", vpcID)
		}
	}
	_, err := store.Update(s.env.Store, cIGWs, id, func(x *InternetGateway) error {
		if x.VpcID != "" {
			return core.Errf(http.StatusBadRequest, "Resource.AlreadyAssociated", "resource %s is already attached to network %s", id, x.VpcID)
		}
		x.VpcID = vpcID
		return nil
	})
	if err == store.ErrNotFound {
		return igwNotFound(id)
	}
	if err != nil {
		return err
	}
	s.applyInternet(vpcID)
	return nil
}

func (s *Service) DetachInternetGateway(id, vpcID string) error {
	netMu.Lock()
	defer netMu.Unlock()
	_, err := store.Update(s.env.Store, cIGWs, id, func(x *InternetGateway) error {
		if x.VpcID == "" || x.VpcID != vpcID {
			return core.Errf(http.StatusBadRequest, "Gateway.NotAttached", "resource %s is not attached to network %s", id, vpcID)
		}
		x.VpcID = ""
		return nil
	})
	if err == store.ErrNotFound {
		return igwNotFound(id)
	}
	if err != nil {
		return err
	}
	s.applyInternet(vpcID)
	return nil
}

func (s *Service) DeleteInternetGateway(id string) error {
	g, err := store.Get[InternetGateway](s.env.Store, cIGWs, id)
	if err != nil {
		return igwNotFound(id)
	}
	if g.VpcID != "" {
		return core.Errf(http.StatusBadRequest, "DependencyViolation", "The internetGateway '%s' has dependencies and cannot be deleted.", id)
	}
	return store.Delete(s.env.Store, cIGWs, id)
}

func (s *Service) CreateRouteTable(vpcID string, tags core.Tags) (RouteTable, error) {
	if _, err := s.vpc.GetVPC(vpcID); err != nil {
		return RouteTable{}, vpcNotFound(vpcID)
	}
	t := RouteTable{ID: core.NewID("rtb"), VpcID: vpcID, Routes: []Route{}, Associations: []RouteAssociation{}, Tags: tags}
	return t, store.Put(s.env.Store, cRouteTables, t.ID, t)
}

func (s *Service) DeleteRouteTable(id string) error {
	t, err := store.Get[RouteTable](s.env.Store, cRouteTables, id)
	if err != nil {
		return rtbNotFound(id)
	}
	if len(t.Associations) > 0 {
		return core.Errf(http.StatusBadRequest, "DependencyViolation", "The routeTable '%s' has dependencies and cannot be deleted.", id)
	}
	netMu.Lock()
	defer netMu.Unlock()
	if err := store.Delete(s.env.Store, cRouteTables, id); err != nil {
		return err
	}
	s.applyInternet(t.VpcID)
	return nil
}

// updateRoutes changes a route table's routes and applies the effect on internet access.
func (s *Service) updateRoutes(id string, fn func(t *RouteTable) error) error {
	netMu.Lock()
	defer netMu.Unlock()
	t, err := store.Update(s.env.Store, cRouteTables, id, fn)
	if err == store.ErrNotFound {
		return rtbNotFound(id)
	}
	if err != nil {
		return err
	}
	s.applyInternet(t.VpcID)
	return nil
}

func (s *Service) checkRoute(vpcID string, r Route) (Route, error) {
	p, err := netip.ParsePrefix(r.Destination)
	if err != nil {
		return r, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "Value (%s) for parameter destinationCidrBlock is invalid", r.Destination)
	}
	r.Destination = p.Masked().String()
	if r.GatewayID != "" && strings.HasPrefix(r.GatewayID, "igw-") {
		g, err := store.Get[InternetGateway](s.env.Store, cIGWs, r.GatewayID)
		if err != nil {
			return r, igwNotFound(r.GatewayID)
		}
		if g.VpcID != vpcID {
			return r, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "route table and network gateway %s belong to different networks", g.ID)
		}
	}
	if r.GatewayID == "" && r.Target == "" {
		return r, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "a route needs a target")
	}
	r.Origin = "CreateRoute"
	return r, nil
}

func (s *Service) CreateRoute(id string, r Route, replace bool) error {
	t, err := store.Get[RouteTable](s.env.Store, cRouteTables, id)
	if err != nil {
		return rtbNotFound(id)
	}
	if r, err = s.checkRoute(t.VpcID, r); err != nil {
		return err
	}
	return s.updateRoutes(id, func(x *RouteTable) error {
		n := slices.IndexFunc(x.Routes, func(o Route) bool { return o.Destination == r.Destination })
		switch {
		case replace && n < 0:
			return core.Errf(http.StatusBadRequest, "InvalidRoute.NotFound", "no route with destination-cidr-block %s in route table %s", r.Destination, id)
		case replace:
			x.Routes[n] = r
		case n >= 0:
			return core.Errf(http.StatusBadRequest, "RouteAlreadyExists", "The route identified by %s already exists.", r.Destination)
		default:
			x.Routes = append(x.Routes, r)
		}
		return nil
	})
}

func (s *Service) DeleteRoute(id, dest string) error {
	if p, err := netip.ParsePrefix(dest); err == nil {
		dest = p.Masked().String()
	}
	return s.updateRoutes(id, func(x *RouteTable) error {
		n := len(x.Routes)
		x.Routes = slices.DeleteFunc(x.Routes, func(o Route) bool { return o.Destination == dest })
		if len(x.Routes) == n {
			return core.Errf(http.StatusBadRequest, "InvalidRoute.NotFound", "no route with destination-cidr-block %s in route table %s", dest, id)
		}
		return nil
	})
}

// AssociateRouteTable associates a subnet with a route table (replacing its
// previous explicit association).
func (s *Service) AssociateRouteTable(id, subnetID string) (string, error) {
	t, err := store.Get[RouteTable](s.env.Store, cRouteTables, id)
	if err != nil {
		return "", rtbNotFound(id)
	}
	sn, err := s.vpc.GetSubnet(subnetID)
	if err != nil {
		return "", subnetNotFound(subnetID)
	}
	if sn.VpcID != t.VpcID {
		return "", core.Errf(http.StatusBadRequest, "InvalidParameterValue", "route table %s and subnet %s belong to different networks", id, subnetID)
	}
	netMu.Lock()
	defer netMu.Unlock()
	for _, o := range store.List[RouteTable](s.env.Store, cRouteTables) {
		if slices.ContainsFunc(o.Associations, func(a RouteAssociation) bool { return a.SubnetID == subnetID }) {
			return "", core.Errf(http.StatusBadRequest, "Resource.AlreadyAssociated", "the specified association for route table %s conflicts with an existing association", o.ID)
		}
	}
	assoc := core.NewID("rtbassoc")
	_, err = store.Update(s.env.Store, cRouteTables, id, func(x *RouteTable) error {
		x.Associations = append(x.Associations, RouteAssociation{ID: assoc, SubnetID: subnetID})
		return nil
	})
	return assoc, err
}

func (s *Service) findAssociation(assoc string) (RouteTable, RouteAssociation, error) {
	for _, t := range store.List[RouteTable](s.env.Store, cRouteTables) {
		for _, a := range t.Associations {
			if a.ID == assoc {
				return t, a, nil
			}
		}
	}
	return RouteTable{}, RouteAssociation{}, core.Errf(http.StatusBadRequest, "InvalidAssociationID.NotFound", "The association ID '%s' does not exist", assoc)
}

func (s *Service) DisassociateRouteTable(assoc string) error {
	t, a, err := s.findAssociation(assoc)
	if err != nil {
		return err
	}
	if a.Main {
		return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "cannot disassociate the main route table association %s", assoc)
	}
	netMu.Lock()
	defer netMu.Unlock()
	_, err = store.Update(s.env.Store, cRouteTables, t.ID, func(x *RouteTable) error {
		x.Associations = slices.DeleteFunc(x.Associations, func(o RouteAssociation) bool { return o.ID == assoc })
		return nil
	})
	return err
}

// ReplaceRouteTableAssociation moves an association (main or subnet) to another table.
func (s *Service) ReplaceRouteTableAssociation(assoc, id string) (string, error) {
	t, a, err := s.findAssociation(assoc)
	if err != nil {
		return "", err
	}
	nt, err := store.Get[RouteTable](s.env.Store, cRouteTables, id)
	if err != nil {
		return "", rtbNotFound(id)
	}
	if nt.VpcID != t.VpcID {
		return "", core.Errf(http.StatusBadRequest, "InvalidParameterValue", "route tables %s and %s belong to different networks", t.ID, id)
	}
	netMu.Lock()
	defer netMu.Unlock()
	if _, err := store.Update(s.env.Store, cRouteTables, t.ID, func(x *RouteTable) error {
		x.Associations = slices.DeleteFunc(x.Associations, func(o RouteAssociation) bool { return o.ID == assoc })
		return nil
	}); err != nil {
		return "", err
	}
	na := RouteAssociation{ID: core.NewID("rtbassoc"), SubnetID: a.SubnetID, Main: a.Main}
	_, err = store.Update(s.env.Store, cRouteTables, id, func(x *RouteTable) error {
		x.Associations = append(x.Associations, na)
		return nil
	})
	return na.ID, err
}
