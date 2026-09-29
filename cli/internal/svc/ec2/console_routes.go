package ec2

import (
	"slices"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Native (console) routes for key pairs, snapshots, internet gateways and
// route tables. They call the same functions as the EC2 API handlers.
func (s *Service) networkRoutes(r *httpx.Router) {
	key := httpx.Res("arn:aws:ec2:{region}:{account}:key-pair/{name}")
	r.Handle("GET /api/v1/ec2/key-pairs", "ec2:DescribeKeyPairs", s.listKeys)
	r.Handle("POST /api/v1/ec2/key-pairs", "ec2:CreateKeyPair", s.createKey, httpx.Deferred())
	r.Handle("POST /api/v1/ec2/key-pairs/import", "ec2:ImportKeyPair", s.importKey, httpx.Deferred())
	r.Handle("DELETE /api/v1/ec2/key-pairs/{name}", "ec2:DeleteKeyPair", s.deleteKey, key)

	snap := httpx.Res("arn:aws:ec2:{region}::snapshot/{id}")
	r.Handle("GET /api/v1/ec2/snapshots", "ec2:DescribeSnapshots", s.listSnapshots)
	r.Handle("POST /api/v1/ec2/snapshots", "ec2:CreateSnapshot", s.createSnapshotRoute, httpx.Deferred())
	r.Handle("DELETE /api/v1/ec2/snapshots/{id}", "ec2:DeleteSnapshot", s.deleteSnapshotRoute, snap)

	igw := httpx.Res("arn:aws:ec2:{region}:{account}:internet-gateway/{id}")
	r.Handle("GET /api/v1/ec2/internet-gateways", "ec2:DescribeInternetGateways", s.listIGWs)
	r.Handle("POST /api/v1/ec2/internet-gateways", "ec2:CreateInternetGateway", s.createIGW, httpx.Res("arn:aws:ec2:{region}:{account}:internet-gateway/*"))
	r.Handle("POST /api/v1/ec2/internet-gateways/{id}/attach", "ec2:AttachInternetGateway", s.attachIGW, igw)
	r.Handle("POST /api/v1/ec2/internet-gateways/{id}/detach", "ec2:DetachInternetGateway", s.detachIGW, igw)
	r.Handle("DELETE /api/v1/ec2/internet-gateways/{id}", "ec2:DeleteInternetGateway", s.deleteIGW, igw)

	rtb := httpx.Res("arn:aws:ec2:{region}:{account}:route-table/{id}")
	r.Handle("GET /api/v1/ec2/route-tables", "ec2:DescribeRouteTables", s.listRTBs)
	r.Handle("POST /api/v1/ec2/route-tables", "ec2:CreateRouteTable", s.createRTB, httpx.Res("arn:aws:ec2:{region}:{account}:route-table/*"))
	r.Handle("DELETE /api/v1/ec2/route-tables/{id}", "ec2:DeleteRouteTable", s.deleteRTB, rtb)
	r.Handle("POST /api/v1/ec2/route-tables/{id}/routes", "ec2:CreateRoute", s.addRoute, rtb)
	r.Handle("DELETE /api/v1/ec2/route-tables/{id}/routes", "ec2:DeleteRoute", s.removeRoute, rtb)
	r.Handle("POST /api/v1/ec2/route-tables/{id}/associations", "ec2:AssociateRouteTable", s.associateRTB, rtb)
	r.Handle("DELETE /api/v1/ec2/route-tables/{id}/associations/{assoc}", "ec2:DisassociateRouteTable", s.disassociateRTB, rtb)
}

func (s *Service) listKeys(c *httpx.Ctx) (any, error) {
	keys := store.List[KeyPair](s.env.Store, cKeyPairs)
	slices.SortFunc(keys, func(a, b KeyPair) int { return strings.Compare(a.Name, b.Name) })
	return keys, nil
}

type keyBody struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	PublicKey string    `json:"public_key"`
	Tags      core.Tags `json:"tags"`
}

func (s *Service) createKey(c *httpx.Ctx) (any, error) {
	var in keyBody
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := c.Authorize("ec2:CreateKeyPair", "arn:aws:ec2:"+core.Region+":"+c.Account+":key-pair/"+in.Name); err != nil {
		return nil, err
	}
	k, private, err := s.CreateKeyPair(in.Name, in.Type, in.Tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"key_pair": k, "private_key": private}, nil
}

func (s *Service) importKey(c *httpx.Ctx) (any, error) {
	var in keyBody
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := c.Authorize("ec2:ImportKeyPair", "arn:aws:ec2:"+core.Region+":"+c.Account+":key-pair/"+in.Name); err != nil {
		return nil, err
	}
	return s.ImportKeyPair(in.Name, []byte(in.PublicKey), in.Tags)
}

func (s *Service) deleteKey(c *httpx.Ctx) (any, error) { return nil, s.DeleteKeyPair(c.Param("name")) }

func (s *Service) listSnapshots(c *httpx.Ctx) (any, error) {
	out := store.List[Snapshot](s.env.Store, cSnapshots)
	slices.SortFunc(out, func(a, b Snapshot) int { return b.StartTime.Compare(a.StartTime) })
	return out, nil
}

func (s *Service) createSnapshotRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		VolumeID    string    `json:"volume_id"`
		Description string    `json:"description"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := c.Authorize("ec2:CreateSnapshot", "arn:aws:ec2:"+core.Region+"::snapshot/*"); err != nil {
		return nil, err
	}
	if err := c.Authorize("ec2:CreateSnapshot", "arn:aws:ec2:"+core.Region+":"+c.Account+":volume/"+in.VolumeID); err != nil {
		return nil, err
	}
	return s.CreateSnapshot(in.VolumeID, in.Description, in.Tags)
}

func (s *Service) deleteSnapshotRoute(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteSnapshot(c.Param("id"))
}

func (s *Service) listIGWs(c *httpx.Ctx) (any, error) {
	s.ensureAllVPCNet()
	out := store.List[InternetGateway](s.env.Store, cIGWs)
	slices.SortFunc(out, func(a, b InternetGateway) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

type tagsBody struct {
	Tags  core.Tags `json:"tags"`
	VpcID string    `json:"vpc_id"`
}

func (s *Service) createIGW(c *httpx.Ctx) (any, error) {
	var in tagsBody
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.CreateInternetGateway(in.Tags)
}

func (s *Service) attachIGW(c *httpx.Ctx) (any, error) {
	var in tagsBody
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return nil, s.AttachInternetGateway(c.Param("id"), in.VpcID)
}

func (s *Service) detachIGW(c *httpx.Ctx) (any, error) {
	var in tagsBody
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return nil, s.DetachInternetGateway(c.Param("id"), in.VpcID)
}

func (s *Service) deleteIGW(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteInternetGateway(c.Param("id"))
}

func (s *Service) listRTBs(c *httpx.Ctx) (any, error) {
	s.ensureAllVPCNet()
	out := store.List[RouteTable](s.env.Store, cRouteTables)
	slices.SortFunc(out, func(a, b RouteTable) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (s *Service) createRTB(c *httpx.Ctx) (any, error) {
	var in tagsBody
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.CreateRouteTable(in.VpcID, in.Tags)
}

func (s *Service) deleteRTB(c *httpx.Ctx) (any, error) { return nil, s.DeleteRouteTable(c.Param("id")) }

func (s *Service) addRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Route
		Replace bool `json:"replace"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return nil, s.CreateRoute(c.Param("id"), in.Route, in.Replace)
}

func (s *Service) removeRoute(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteRoute(c.Param("id"), c.Query("destination"))
}

func (s *Service) associateRTB(c *httpx.Ctx) (any, error) {
	var in struct {
		SubnetID string `json:"subnet_id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	id, err := s.AssociateRouteTable(c.Param("id"), in.SubnetID)
	return map[string]string{"association_id": id}, err
}

func (s *Service) disassociateRTB(c *httpx.Ctx) (any, error) {
	return nil, s.DisassociateRouteTable(c.Param("assoc"))
}
