package ec2

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// The VPC part of the EC2 API: VPCs, subnets, security groups, internet
// gateways, route tables and (read-only, allow-all) network ACLs.

func (s *Service) vpcOps(ops map[string]ec2Op) {
	for k, v := range map[string]ec2Op{
		"CreateVpc":                     s.awsCreateVpc,
		"DescribeVpcs":                  s.awsDescribeVpcs,
		"DeleteVpc":                     s.awsDeleteVpc,
		"ModifyVpcAttribute":            s.awsModifyVpcAttribute,
		"DescribeVpcAttribute":          s.awsDescribeVpcAttribute,
		"CreateSubnet":                  s.awsCreateSubnet,
		"DescribeSubnets":               s.awsDescribeSubnets,
		"DeleteSubnet":                  s.awsDeleteSubnet,
		"ModifySubnetAttribute":         s.awsModifySubnetAttribute,
		"CreateSecurityGroup":           s.awsCreateSecurityGroup,
		"DescribeSecurityGroups":        s.awsDescribeSecurityGroups,
		"DeleteSecurityGroup":           s.awsDeleteSecurityGroup,
		"AuthorizeSecurityGroupIngress": s.ruleOp(false, true),
		"AuthorizeSecurityGroupEgress":  s.ruleOp(true, true),
		"RevokeSecurityGroupIngress":    s.ruleOp(false, false),
		"RevokeSecurityGroupEgress":     s.ruleOp(true, false),
		"DescribeSecurityGroupRules":    s.awsDescribeSecurityGroupRules,
		"CreateInternetGateway":         s.awsCreateInternetGateway,
		"AttachInternetGateway":         s.awsAttachInternetGateway,
		"DetachInternetGateway":         s.awsDetachInternetGateway,
		"DeleteInternetGateway":         s.awsDeleteInternetGateway,
		"DescribeInternetGateways":      s.awsDescribeInternetGateways,
		"CreateRouteTable":              s.awsCreateRouteTable,
		"DescribeRouteTables":           s.awsDescribeRouteTables,
		"DeleteRouteTable":              s.awsDeleteRouteTable,
		"CreateRoute":                   s.routeOp(false),
		"ReplaceRoute":                  s.routeOp(true),
		"DeleteRoute":                   s.awsDeleteRoute,
		"AssociateRouteTable":           s.awsAssociateRouteTable,
		"DisassociateRouteTable":        s.awsDisassociateRouteTable,
		"ReplaceRouteTableAssociation":  s.awsReplaceRouteTableAssociation,
		"DescribeNetworkAcls":           s.awsDescribeNetworkAcls,
		"CreateNetworkAcl":              s.awsCreateNetworkAcl,
		"DeleteNetworkAcl":              s.awsDeleteNetworkAcl,
		"ReplaceNetworkAclAssociation":  s.awsReplaceNetworkAclAssociation,
		"CreateNetworkAclEntry":         s.aclEntryOp(false),
		"ReplaceNetworkAclEntry":        s.aclEntryOp(true),
		"DeleteNetworkAclEntry":         s.awsDeleteNetworkAclEntry,
	} {
		ops[k] = v
	}
}

// ---- VPCs ----

func (s *Service) vpcXML(v vpc.VPC) map[string]any {
	return map[string]any{
		"vpcId": v.ID, "state": "available", "cidrBlock": v.CIDR, "dhcpOptionsId": "default", "instanceTenancy": "default",
		"isDefault": v.Default, "ownerId": s.env.AccountID, "tagSet": tagSet(s.tagsOf(v.ID, v.Name, v.Tags)),
		"cidrBlockAssociationSet": awsapi.Items{map[string]any{"associationId": "vpc-cidr-assoc-" + strings.TrimPrefix(v.ID, "vpc-"),
			"cidrBlock": v.CIDR, "cidrBlockState": map[string]any{"state": "associated"}}},
		"ipv6CidrBlockAssociationSet": awsapi.Items{},
	}
}

func (s *Service) vpcArg(q *awsapi.Req, action string) (vpc.VPC, error) {
	id := q.Param("VpcId")
	if err := q.Authorize(action, q.ARN("ec2", "vpc/"+id)); err != nil {
		return vpc.VPC{}, err
	}
	v, err := s.vpc.GetVPC(id)
	if err != nil {
		return v, vpcNotFound(id)
	}
	return v, nil
}

func (s *Service) awsCreateVpc(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:CreateVpc", q.ARN("ec2", "vpc/*")); err != nil {
		return nil, err
	}
	cidr := q.Param("CidrBlock")
	if cidr == "" {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter CidrBlock")
	}
	tags := tagSpecs(q, "vpc")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	name := tags["Name"]
	delete(tags, "Name")
	v, err := s.vpc.CreateVPC(name, cidr, false, tags)
	if err != nil {
		if ce, ok := err.(*core.Error); ok && ce.Code == "ResourceConflict" {
			return nil, core.Errf(http.StatusBadRequest, "InvalidVpc.Range", "%s", ce.Message)
		}
		return nil, err
	}
	netMu.Lock()
	s.ensureVPCNet(v)
	netMu.Unlock()
	return map[string]any{"vpc": s.vpcXML(v)}, nil
}

func (s *Service) awsDescribeVpcs(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeVpcs", "*"); err != nil {
		return nil, err
	}
	s.ensureAllVPCNet()
	ids, fs := q.List("VpcId"), filters(q)
	for _, id := range ids {
		if _, err := s.vpc.GetVPC(id); err != nil {
			return nil, vpcNotFound(id)
		}
	}
	want := idSet(ids)
	vpcs := s.vpc.List()
	slices.SortFunc(vpcs, func(a, b vpc.VPC) int { return a.CreatedAt.Compare(b.CreatedAt) })
	items := awsapi.Items{}
	for _, v := range vpcs {
		a := attrs{}.set("vpc-id", v.ID).set("cidr", v.CIDR).set("cidr-block-association.cidr-block", v.CIDR).set("state", "available").
			set("is-default", strconv.FormatBool(v.Default)).set("isDefault", strconv.FormatBool(v.Default)).set("owner-id", s.env.AccountID).
			set("dhcp-options-id", "default").tags(s.tagsOf(v.ID, v.Name, v.Tags))
		if (len(ids) == 0 || want[v.ID]) && match(fs, a) {
			items = append(items, s.vpcXML(v))
		}
	}
	return map[string]any{"vpcSet": items}, nil
}

func (s *Service) awsDeleteVpc(q *awsapi.Req) (any, error) {
	v, err := s.vpcArg(q, "ec2:DeleteVpc")
	if err != nil {
		return nil, err
	}
	if v.Default {
		return nil, core.Errf(http.StatusBadRequest, "OperationNotPermitted", "HomeCloud's default VPC cannot be deleted")
	}
	for _, g := range store.List[InternetGateway](s.env.Store, cIGWs) {
		if g.VpcID == v.ID && !g.Auto {
			return nil, core.Errf(http.StatusBadRequest, "DependencyViolation", "The vpc '%s' has dependencies and cannot be deleted (internet gateway %s is attached).", v.ID, g.ID)
		}
	}
	return nil, s.vpc.DeleteVPC(v.ID)
}

func (s *Service) awsModifyVpcAttribute(q *awsapi.Req) (any, error) {
	v, err := s.vpcArg(q, "ec2:ModifyVpcAttribute")
	if err != nil {
		return nil, err
	}
	_, err = s.vpc.UpdateVPC(v.ID, func(x *vpc.VPC) error {
		if b, ok := boolParam(q, "EnableDnsHostnames"); ok {
			x.DNSHostnames = b
		}
		if b, ok := boolParam(q, "EnableDnsSupport"); ok {
			x.DNSSupportDisabled = !b
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsDescribeVpcAttribute(q *awsapi.Req) (any, error) {
	v, err := s.vpcArg(q, "ec2:DescribeVpcAttribute")
	if err != nil {
		return nil, err
	}
	out := map[string]any{"vpcId": v.ID}
	switch a := q.Param("Attribute"); a {
	case "enableDnsSupport":
		out[a] = value(!v.DNSSupportDisabled)
	case "enableDnsHostnames":
		out[a] = value(v.DNSHostnames)
	case "enableNetworkAddressUsageMetrics":
		out[a] = value(false)
	default:
		return nil, invalid("Value (%s) for parameter attribute is invalid. Unknown attribute.", a)
	}
	return out, nil
}

// ---- subnets ----

func (s *Service) subnetXML(sn vpc.Subnet) map[string]any {
	_, free := s.vpc.SubnetUsage(sn)
	return map[string]any{
		"subnetId": sn.ID, "subnetArn": s.env.ARN("ec2", "subnet/"+sn.ID), "state": "available", "vpcId": sn.VpcID, "cidrBlock": sn.CIDR,
		"availableIpAddressCount": free, "availabilityZone": sn.AvailabilityZone, "availabilityZoneId": azID(sn.AvailabilityZone),
		"defaultForAz": sn.Default, "mapPublicIpOnLaunch": sn.MapPublicIP, "mapCustomerOwnedIpOnLaunch": false,
		"assignIpv6AddressOnCreation": false, "enableDns64": false, "ipv6Native": false, "ownerId": s.env.AccountID,
		"ipv6CidrBlockAssociationSet": awsapi.Items{}, "tagSet": tagSet(s.tagsOf(sn.ID, sn.Name, sn.Tags)),
		"privateDnsNameOptionsOnLaunch": map[string]any{"hostnameType": "ip-name", "enableResourceNameDnsARecord": false, "enableResourceNameDnsAAAARecord": false},
	}
}

func (s *Service) awsCreateSubnet(q *awsapi.Req) (any, error) {
	v, err := s.vpcArg(q, "ec2:CreateSubnet")
	if err != nil {
		return nil, err
	}
	az := q.Param("AvailabilityZone")
	if id := q.Param("AvailabilityZoneId"); az == "" && id != "" {
		for _, l := range zoneLetters {
			if azID(s.region()+l) == id {
				az = s.region() + l
			}
		}
	}
	if az != "" && !slices.ContainsFunc(zoneLetters, func(l string) bool { return az == s.region()+l }) {
		return nil, invalid("Value (%s) for parameter availabilityZone is invalid. Subnets can currently only be created in the following availability zones: %sa, %sb, %sc.", az, s.region(), s.region(), s.region())
	}
	tags := tagSpecs(q, "subnet")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	name := tags["Name"]
	delete(tags, "Name")
	sn, err := s.vpc.CreateSubnet(v.ID, name, q.Param("CidrBlock"), az, tags)
	if err != nil {
		if ce, ok := err.(*core.Error); ok && ce.Code == "ResourceConflict" {
			return nil, core.Errf(http.StatusBadRequest, "InvalidSubnet.Conflict", "%s", ce.Message)
		}
		if ce, ok := err.(*core.Error); ok && ce.Code == "ValidationError" {
			return nil, core.Errf(http.StatusBadRequest, "InvalidSubnet.Range", "%s", ce.Message)
		}
		return nil, err
	}
	return map[string]any{"subnet": s.subnetXML(sn)}, nil
}

func (s *Service) awsDescribeSubnets(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeSubnets", "*"); err != nil {
		return nil, err
	}
	ids, fs := q.List("SubnetId"), filters(q)
	for _, id := range ids {
		if _, err := s.vpc.GetSubnet(id); err != nil {
			return nil, subnetNotFound(id)
		}
	}
	want := idSet(ids)
	subs := s.vpc.Subnets()
	slices.SortFunc(subs, func(a, b vpc.Subnet) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.AvailabilityZone, b.AvailabilityZone)
	})
	items := awsapi.Items{}
	for _, sn := range subs {
		def := strconv.FormatBool(sn.Default)
		a := attrs{}.set("subnet-id", sn.ID).set("vpc-id", sn.VpcID).set("vpcId", sn.VpcID).set("cidr-block", sn.CIDR).set("cidr", sn.CIDR).
			set("cidrBlock", sn.CIDR).set("availability-zone", sn.AvailabilityZone).set("availabilityZone", sn.AvailabilityZone).
			set("availability-zone-id", azID(sn.AvailabilityZone)).set("default-for-az", def).set("defaultForAz", def).set("state", "available").
			set("map-public-ip-on-launch", strconv.FormatBool(sn.MapPublicIP)).set("owner-id", s.env.AccountID).
			set("subnet-arn", s.env.ARN("ec2", "subnet/"+sn.ID)).tags(s.tagsOf(sn.ID, sn.Name, sn.Tags))
		if (len(ids) == 0 || want[sn.ID]) && match(fs, a) {
			items = append(items, s.subnetXML(sn))
		}
	}
	return map[string]any{"subnetSet": items}, nil
}

func (s *Service) awsDeleteSubnet(q *awsapi.Req) (any, error) {
	id := q.Param("SubnetId")
	if err := q.Authorize("ec2:DeleteSubnet", q.ARN("ec2", "subnet/"+id)); err != nil {
		return nil, err
	}
	if _, err := s.vpc.GetSubnet(id); err != nil {
		return nil, subnetNotFound(id)
	}
	if nat := s.natGatewayIn(id); nat != "" {
		return nil, core.Errf(http.StatusBadRequest, "DependencyViolation", "The subnet '%s' has dependencies and cannot be deleted (NAT gateway %s).", id, nat)
	}
	if err := s.vpc.DeleteSubnet(id); err != nil {
		return nil, err
	}
	aclMu.Lock()
	for _, a := range store.List[NetworkACL](s.env.Store, cNetworkACLs) {
		if _, ok := a.Associations[id]; ok {
			delete(a.Associations, id)
			_ = store.Put(s.env.Store, cNetworkACLs, a.ID, a)
		}
	}
	aclMu.Unlock()
	netMu.Lock()
	defer netMu.Unlock()
	for _, t := range store.List[RouteTable](s.env.Store, cRouteTables) {
		if slices.ContainsFunc(t.Associations, func(a RouteAssociation) bool { return a.SubnetID == id }) {
			_, _ = store.Update(s.env.Store, cRouteTables, t.ID, func(x *RouteTable) error {
				x.Associations = slices.DeleteFunc(x.Associations, func(a RouteAssociation) bool { return a.SubnetID == id })
				return nil
			})
		}
	}
	return nil, nil
}

func (s *Service) awsModifySubnetAttribute(q *awsapi.Req) (any, error) {
	id := q.Param("SubnetId")
	if err := q.Authorize("ec2:ModifySubnetAttribute", q.ARN("ec2", "subnet/"+id)); err != nil {
		return nil, err
	}
	_, err := s.vpc.UpdateSubnet(id, func(x *vpc.Subnet) error {
		if b, ok := boolParam(q, "MapPublicIpOnLaunch"); ok {
			x.MapPublicIP = b
		}
		return nil
	})
	if err != nil {
		return nil, subnetNotFound(id)
	}
	return nil, nil
}

// ---- security groups ----

func (s *Service) groupArg(q *awsapi.Req, action string) (vpc.SecurityGroup, error) {
	id := q.Param("GroupId")
	if id == "" && q.Param("GroupName") != "" {
		def := s.vpc.DefaultVPCID()
		for _, g := range s.vpc.SecurityGroups() {
			if g.VpcID == def && g.Name == q.Param("GroupName") {
				id = g.ID
			}
		}
		if id == "" {
			return vpc.SecurityGroup{}, core.Errf(http.StatusBadRequest, "InvalidGroup.NotFound", "The security group '%s' does not exist in default VPC '%s'", q.Param("GroupName"), def)
		}
	}
	if err := q.Authorize(action, q.ARN("ec2", "security-group/"+id)); err != nil {
		return vpc.SecurityGroup{}, err
	}
	g, err := s.vpc.GetSecurityGroup(id)
	if err != nil {
		return g, core.Errf(http.StatusBadRequest, "InvalidGroup.NotFound", "The security group '%s' does not exist", id)
	}
	return g, nil
}

// permissions groups rules with the same protocol and ports into one
// IpPermission, as EC2 reports them.
func (s *Service) permissions(rules []vpc.Rule) awsapi.Items {
	type key struct {
		proto    string
		from, to int
	}
	var order []key
	perms := map[key]map[string]awsapi.Items{}
	for _, r := range rules {
		k := key{r.Protocol, r.FromPort, r.ToPort}
		if perms[k] == nil {
			order = append(order, k)
			perms[k] = map[string]awsapi.Items{"ipRanges": {}, "ipv6Ranges": {}, "groups": {}, "prefixListIds": {}}
		}
		p := perms[k]
		desc := any(nil)
		if r.Description != "" {
			desc = r.Description
		}
		switch {
		case r.CIDR != "":
			p["ipRanges"] = append(p["ipRanges"], map[string]any{"cidrIp": r.CIDR, "description": desc})
		case r.CIDRv6 != "":
			p["ipv6Ranges"] = append(p["ipv6Ranges"], map[string]any{"cidrIpv6": r.CIDRv6, "description": desc})
		case r.SourceGroup != "":
			p["groups"] = append(p["groups"], map[string]any{"groupId": r.SourceGroup, "userId": s.env.AccountID, "description": desc})
		case r.PrefixList != "":
			p["prefixListIds"] = append(p["prefixListIds"], map[string]any{"prefixListId": r.PrefixList, "description": desc})
		}
	}
	out := awsapi.Items{}
	for _, k := range order {
		m := map[string]any{"ipProtocol": k.proto}
		for n, v := range perms[k] {
			m[n] = v
		}
		if k.proto != "-1" {
			m["fromPort"], m["toPort"] = k.from, k.to
		}
		out = append(out, m)
	}
	return out
}

func (s *Service) groupXML(g vpc.SecurityGroup) map[string]any {
	return map[string]any{
		"ownerId": s.env.AccountID, "groupId": g.ID, "groupName": g.Name, "groupDescription": g.Description, "vpcId": g.VpcID,
		"securityGroupArn": s.env.ARN("ec2", "security-group/"+g.ID), "tagSet": tagSet(g.Tags),
		"ipPermissions": s.permissions(g.Ingress), "ipPermissionsEgress": s.permissions(vpc.EgressRules(g)),
	}
}

func (s *Service) awsCreateSecurityGroup(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:CreateSecurityGroup", q.ARN("ec2", "security-group/*")); err != nil {
		return nil, err
	}
	vpcID := q.Param("VpcId")
	if vpcID != "" {
		if _, err := s.vpc.GetVPC(vpcID); err != nil {
			return nil, vpcNotFound(vpcID)
		}
	}
	if q.Param("GroupDescription") == "" {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter GroupDescription")
	}
	tags := tagSpecs(q, "security-group")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	g, err := s.vpc.CreateSecurityGroup(vpcID, q.Param("GroupName"), q.Param("GroupDescription"), tags, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"return": true, "groupId": g.ID, "securityGroupArn": s.env.ARN("ec2", "security-group/"+g.ID), "tagSet": tagSet(g.Tags)}, nil
}

func (s *Service) awsDescribeSecurityGroups(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeSecurityGroups", "*"); err != nil {
		return nil, err
	}
	ids, names, fs := q.List("GroupId"), q.List("GroupName"), filters(q)
	for _, id := range ids {
		if _, err := s.vpc.GetSecurityGroup(id); err != nil {
			return nil, core.Errf(http.StatusBadRequest, "InvalidGroup.NotFound", "The security group '%s' does not exist", id)
		}
	}
	def := s.vpc.DefaultVPCID()
	for _, n := range names {
		if !slices.ContainsFunc(s.vpc.SecurityGroups(), func(g vpc.SecurityGroup) bool { return g.Name == n && g.VpcID == def }) {
			return nil, core.Errf(http.StatusBadRequest, "InvalidGroup.NotFound", "The security group '%s' does not exist in default VPC '%s'", n, def)
		}
	}
	want := idSet(ids)
	groups := s.vpc.SecurityGroups()
	slices.SortFunc(groups, func(a, b vpc.SecurityGroup) int { return a.CreatedAt.Compare(b.CreatedAt) })
	items := awsapi.Items{}
	for _, g := range groups {
		if len(ids) > 0 && !want[g.ID] {
			continue
		}
		if len(names) > 0 && (g.VpcID != def || !slices.Contains(names, g.Name)) {
			continue
		}
		a := attrs{}.set("group-id", g.ID).set("group-name", g.Name).set("vpc-id", g.VpcID).set("description", g.Description).
			set("owner-id", s.env.AccountID).tags(g.Tags)
		for _, r := range g.Ingress {
			a.set("ip-permission.protocol", r.Protocol).set("ip-permission.cidr", r.CIDR).set("ip-permission.group-id", r.SourceGroup).
				set("ip-permission.from-port", strconv.Itoa(r.FromPort)).set("ip-permission.to-port", strconv.Itoa(r.ToPort))
		}
		if match(fs, a) {
			items = append(items, s.groupXML(g))
		}
	}
	return map[string]any{"securityGroupInfo": items}, nil
}

func (s *Service) awsDeleteSecurityGroup(q *awsapi.Req) (any, error) {
	g, err := s.groupArg(q, "ec2:DeleteSecurityGroup")
	if err != nil {
		return nil, err
	}
	return map[string]any{"return": true, "groupId": g.ID}, s.vpc.DeleteSecurityGroup(g.ID)
}

// reqRules reads IpPermissions (or the legacy top-level parameters) as rules.
func reqRules(q *awsapi.Req) []vpc.Rule {
	var out []vpc.Rule
	perm := func(m map[string]string) {
		base := vpc.Rule{Protocol: m["IpProtocol"], FromPort: -1, ToPort: -1}
		if v, err := strconv.Atoi(m["FromPort"]); err == nil {
			base.FromPort = v
		}
		if v, err := strconv.Atoi(m["ToPort"]); err == nil {
			base.ToPort = v
		}
		for _, r := range nested(m, "IpRanges") {
			x := base
			x.CIDR, x.Description = r["CidrIp"], r["Description"]
			out = append(out, x)
		}
		for _, r := range nested(m, "Ipv6Ranges") {
			x := base
			x.CIDRv6, x.Description = r["CidrIpv6"], r["Description"]
			out = append(out, x)
		}
		// The EC2 Query protocol names the group pairs "Groups"; "UserIdGroupPairs" is accepted too.
		for _, r := range append(nested(m, "Groups"), nested(m, "UserIdGroupPairs")...) {
			x := base
			x.SourceGroup, x.Description = r["GroupId"], r["Description"]
			out = append(out, x)
		}
		for _, r := range nested(m, "PrefixListIds") {
			x := base
			x.PrefixList, x.Description = r["PrefixListId"], r["Description"]
			out = append(out, x)
		}
	}
	for _, m := range q.Structs("IpPermissions") {
		perm(m)
	}
	if q.Param("IpProtocol") != "" {
		m := map[string]string{"IpProtocol": q.Param("IpProtocol"), "FromPort": q.Param("FromPort"), "ToPort": q.Param("ToPort")}
		if c := q.Param("CidrIp"); c != "" {
			m["IpRanges.1.CidrIp"] = c
		}
		if g := q.Param("SourceSecurityGroupId"); g != "" {
			m["UserIdGroupPairs.1.GroupId"] = g
		}
		perm(m)
	}
	return out
}

func (s *Service) ruleXML(g vpc.SecurityGroup, r vpc.Rule, egress bool) map[string]any {
	m := map[string]any{"securityGroupRuleId": r.ID, "groupId": g.ID, "groupOwnerId": s.env.AccountID, "isEgress": egress,
		"ipProtocol": r.Protocol, "fromPort": r.FromPort, "toPort": r.ToPort, "tagSet": tagSet(r.Tags),
		"securityGroupRuleArn": s.env.ARN("ec2", "security-group-rule/"+r.ID)}
	if r.Description != "" {
		m["description"] = r.Description
	}
	switch {
	case r.CIDR != "":
		m["cidrIpv4"] = r.CIDR
	case r.CIDRv6 != "":
		m["cidrIpv6"] = r.CIDRv6
	case r.SourceGroup != "":
		m["referencedGroupInfo"] = map[string]any{"groupId": r.SourceGroup, "userId": s.env.AccountID}
	case r.PrefixList != "":
		m["prefixListId"] = r.PrefixList
	}
	return m
}

// ruleOp authorizes or revokes ingress or egress rules.
func (s *Service) ruleOp(egress, add bool) ec2Op {
	action := map[[2]bool]string{{false, true}: "ec2:AuthorizeSecurityGroupIngress", {true, true}: "ec2:AuthorizeSecurityGroupEgress",
		{false, false}: "ec2:RevokeSecurityGroupIngress", {true, false}: "ec2:RevokeSecurityGroupEgress"}[[2]bool{egress, add}]
	return func(q *awsapi.Req) (any, error) {
		g, err := s.groupArg(q, action)
		if err != nil {
			return nil, err
		}
		var rules []vpc.Rule
		for _, r := range reqRules(q) {
			nr, err := s.vpc.NormalizeAWSRule(r)
			if err != nil {
				return nil, err
			}
			rules = append(rules, nr)
		}
		ruleIDs := q.List("SecurityGroupRuleId")
		if len(rules) == 0 && (add || len(ruleIDs) == 0) {
			return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "No IpPermissions were given")
		}
		if tags := tagSpecs(q, "security-group-rule"); add && len(tags) > 0 {
			if err := checkTags(tags); err != nil {
				return nil, err
			}
			for i := range rules {
				rules[i].Tags = maps.Clone(tags)
			}
		}
		var added []vpc.Rule
		g, err = s.vpc.UpdateSecurityGroup(g.ID, func(x *vpc.SecurityGroup) error {
			cur := x.Ingress
			if egress {
				cur = vpc.EgressRules(*x)
			}
			cur = slices.Clone(cur)
			if add {
				for _, r := range rules {
					if slices.ContainsFunc(cur, func(o vpc.Rule) bool { return vpc.SameRule(o, r) }) {
						return core.Errf(http.StatusBadRequest, "InvalidPermission.Duplicate", "the specified rule \"peer: %s, %s, from port: %d, to port: %d, ALLOW\" already exists", ruleSource(r), r.Protocol, r.FromPort, r.ToPort)
					}
					cur = append(cur, r)
					added = append(added, r)
				}
			} else {
				cur = slices.DeleteFunc(cur, func(o vpc.Rule) bool {
					return slices.Contains(ruleIDs, o.ID) || slices.ContainsFunc(rules, func(r vpc.Rule) bool { return vpc.SameRule(o, r) })
				})
			}
			if egress {
				x.Egress, x.EgressSet = cur, true
			} else {
				x.Ingress = cur
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		out := map[string]any{"return": true}
		if add {
			items := awsapi.Items{}
			for _, r := range added {
				items = append(items, s.ruleXML(g, r, egress))
			}
			out["securityGroupRuleSet"] = items
		}
		return out, nil
	}
}

func ruleSource(r vpc.Rule) string {
	for _, v := range []string{r.CIDR, r.CIDRv6, r.SourceGroup, r.PrefixList} {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Service) awsDescribeSecurityGroupRules(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeSecurityGroupRules", "*"); err != nil {
		return nil, err
	}
	ids, fs := q.List("SecurityGroupRuleId"), filters(q)
	items := awsapi.Items{}
	for _, g := range s.vpc.SecurityGroups() {
		emit := func(rules []vpc.Rule, egress bool) {
			for _, r := range rules {
				a := attrs{}.set("group-id", g.ID).set("security-group-rule-id", r.ID).tags(r.Tags)
				if (len(ids) == 0 || slices.Contains(ids, r.ID)) && match(fs, a) {
					items = append(items, s.ruleXML(g, r, egress))
				}
			}
		}
		emit(g.Ingress, false)
		emit(vpc.EgressRules(g), true)
	}
	return map[string]any{"securityGroupRuleSet": items}, nil
}

// ---- internet gateways ----

func (s *Service) igwXML(g InternetGateway) map[string]any {
	att := awsapi.Items{}
	if g.VpcID != "" {
		att = append(att, map[string]any{"vpcId": g.VpcID, "state": "available"})
	}
	return map[string]any{"internetGatewayId": g.ID, "ownerId": s.env.AccountID, "attachmentSet": att, "tagSet": tagSet(g.Tags)}
}

func (s *Service) awsCreateInternetGateway(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:CreateInternetGateway", q.ARN("ec2", "internet-gateway/*")); err != nil {
		return nil, err
	}
	tags := tagSpecs(q, "internet-gateway")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	g, err := s.CreateInternetGateway(tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"internetGateway": s.igwXML(g)}, nil
}

func (s *Service) awsAttachInternetGateway(q *awsapi.Req) (any, error) {
	id := q.Param("InternetGatewayId")
	if err := q.Authorize("ec2:AttachInternetGateway", q.ARN("ec2", "internet-gateway/"+id)); err != nil {
		return nil, err
	}
	return nil, s.AttachInternetGateway(id, q.Param("VpcId"))
}

func (s *Service) awsDetachInternetGateway(q *awsapi.Req) (any, error) {
	id := q.Param("InternetGatewayId")
	if err := q.Authorize("ec2:DetachInternetGateway", q.ARN("ec2", "internet-gateway/"+id)); err != nil {
		return nil, err
	}
	return nil, s.DetachInternetGateway(id, q.Param("VpcId"))
}

func (s *Service) awsDeleteInternetGateway(q *awsapi.Req) (any, error) {
	id := q.Param("InternetGatewayId")
	if err := q.Authorize("ec2:DeleteInternetGateway", q.ARN("ec2", "internet-gateway/"+id)); err != nil {
		return nil, err
	}
	return nil, s.DeleteInternetGateway(id)
}

func (s *Service) awsDescribeInternetGateways(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeInternetGateways", "*"); err != nil {
		return nil, err
	}
	s.ensureAllVPCNet()
	ids, fs := q.List("InternetGatewayId"), filters(q)
	for _, id := range ids {
		if !store.Has(s.env.Store, cIGWs, id) {
			return nil, igwNotFound(id)
		}
	}
	want := idSet(ids)
	items := awsapi.Items{}
	for _, g := range store.List[InternetGateway](s.env.Store, cIGWs) {
		a := attrs{}.set("internet-gateway-id", g.ID).set("owner-id", s.env.AccountID).tags(g.Tags)
		if g.VpcID != "" {
			a.set("attachment.vpc-id", g.VpcID).set("attachment.state", "available")
		} else {
			a.set("attachment.vpc-id").set("attachment.state")
		}
		if (len(ids) == 0 || want[g.ID]) && match(fs, a) {
			items = append(items, s.igwXML(g))
		}
	}
	return map[string]any{"internetGatewaySet": items}, nil
}

// ---- route tables ----

func (s *Service) routeTableXML(t RouteTable) map[string]any {
	routes := awsapi.Items{}
	if v, err := s.vpc.GetVPC(t.VpcID); err == nil {
		routes = append(routes, map[string]any{"destinationCidrBlock": v.CIDR, "gatewayId": "local", "state": "active", "origin": "CreateRouteTable"})
	}
	for _, r := range t.Routes {
		m := map[string]any{"destinationCidrBlock": r.Destination, "state": "active", "origin": r.Origin}
		if r.GatewayID != "" {
			m["gatewayId"] = r.GatewayID
			if strings.HasPrefix(r.GatewayID, "igw-") {
				if g, err := store.Get[InternetGateway](s.env.Store, cIGWs, r.GatewayID); err != nil || g.VpcID != t.VpcID {
					m["state"] = "blackhole"
				}
			}
		}
		if r.Target != "" {
			m[r.TargetKind] = r.Target
		}
		routes = append(routes, m)
	}
	assocs := awsapi.Items{}
	for _, a := range t.Associations {
		m := map[string]any{"routeTableAssociationId": a.ID, "routeTableId": t.ID, "main": a.Main,
			"associationState": map[string]any{"state": "associated"}}
		if a.SubnetID != "" {
			m["subnetId"] = a.SubnetID
		}
		assocs = append(assocs, m)
	}
	return map[string]any{"routeTableId": t.ID, "vpcId": t.VpcID, "ownerId": s.env.AccountID, "routeSet": routes,
		"associationSet": assocs, "propagatingVgwSet": awsapi.Items{}, "tagSet": tagSet(t.Tags)}
}

func (s *Service) awsCreateRouteTable(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:CreateRouteTable", q.ARN("ec2", "route-table/*")); err != nil {
		return nil, err
	}
	tags := tagSpecs(q, "route-table")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	s.ensureAllVPCNet()
	t, err := s.CreateRouteTable(q.Param("VpcId"), tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"routeTable": s.routeTableXML(t)}, nil
}

func (s *Service) awsDescribeRouteTables(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeRouteTables", "*"); err != nil {
		return nil, err
	}
	s.ensureAllVPCNet()
	ids, fs := q.List("RouteTableId"), filters(q)
	for _, id := range ids {
		if !store.Has(s.env.Store, cRouteTables, id) {
			return nil, rtbNotFound(id)
		}
	}
	want := idSet(ids)
	items := awsapi.Items{}
	for _, t := range store.List[RouteTable](s.env.Store, cRouteTables) {
		a := attrs{}.set("route-table-id", t.ID).set("vpc-id", t.VpcID).set("owner-id", s.env.AccountID).
			set("association.main", strconv.FormatBool(t.main())).set("association.subnet-id").set("association.route-table-association-id").
			set("route.gateway-id", "local").set("route.destination-cidr-block").tags(t.Tags)
		for _, as := range t.Associations {
			a.set("association.route-table-association-id", as.ID)
			if as.SubnetID != "" {
				a.set("association.subnet-id", as.SubnetID)
			}
		}
		for _, r := range t.Routes {
			a.set("route.destination-cidr-block", r.Destination)
			if r.GatewayID != "" {
				a.set("route.gateway-id", r.GatewayID)
			}
		}
		if (len(ids) == 0 || want[t.ID]) && match(fs, a) {
			items = append(items, s.routeTableXML(t))
		}
	}
	return map[string]any{"routeTableSet": items}, nil
}

func (s *Service) rtbArg(q *awsapi.Req, action string) (string, error) {
	id := q.Param("RouteTableId")
	return id, q.Authorize(action, q.ARN("ec2", "route-table/"+id))
}

func (s *Service) awsDeleteRouteTable(q *awsapi.Req) (any, error) {
	id, err := s.rtbArg(q, "ec2:DeleteRouteTable")
	if err != nil {
		return nil, err
	}
	return nil, s.DeleteRouteTable(id)
}

var routeTargets = []struct{ param, xml string }{
	{"NatGatewayId", "natGatewayId"}, {"NetworkInterfaceId", "networkInterfaceId"}, {"InstanceId", "instanceId"},
	{"VpcPeeringConnectionId", "vpcPeeringConnectionId"}, {"TransitGatewayId", "transitGatewayId"}, {"VpcEndpointId", "gatewayId"},
	{"EgressOnlyInternetGatewayId", "egressOnlyInternetGatewayId"}, {"CarrierGatewayId", "carrierGatewayId"}, {"LocalGatewayId", "localGatewayId"},
}

func (s *Service) routeOp(replace bool) ec2Op {
	action := "ec2:CreateRoute"
	if replace {
		action = "ec2:ReplaceRoute"
	}
	return func(q *awsapi.Req) (any, error) {
		id, err := s.rtbArg(q, action)
		if err != nil {
			return nil, err
		}
		dest := q.Param("DestinationCidrBlock")
		if dest == "" {
			if q.Param("DestinationIpv6CidrBlock") != "" || q.Param("DestinationPrefixListId") != "" {
				return nil, invalid("HomeCloud supports IPv4 destination CIDR blocks only")
			}
			return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter destinationCidrBlock")
		}
		r := Route{Destination: dest, GatewayID: q.Param("GatewayId")}
		for _, t := range routeTargets {
			if v := q.Param(t.param); v != "" {
				r.Target, r.TargetKind = v, t.xml
			}
		}
		return nil, s.CreateRoute(id, r, replace)
	}
}

func (s *Service) awsDeleteRoute(q *awsapi.Req) (any, error) {
	id, err := s.rtbArg(q, "ec2:DeleteRoute")
	if err != nil {
		return nil, err
	}
	return nil, s.DeleteRoute(id, q.Param("DestinationCidrBlock"))
}

func (s *Service) awsAssociateRouteTable(q *awsapi.Req) (any, error) {
	id, err := s.rtbArg(q, "ec2:AssociateRouteTable")
	if err != nil {
		return nil, err
	}
	if q.Param("GatewayId") != "" {
		return nil, invalid("gateway route table associations are not supported")
	}
	assoc, err := s.AssociateRouteTable(id, q.Param("SubnetId"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"associationId": assoc, "associationState": map[string]any{"state": "associated"}}, nil
}

func (s *Service) awsDisassociateRouteTable(q *awsapi.Req) (any, error) {
	assoc := q.Param("AssociationId")
	if err := q.Authorize("ec2:DisassociateRouteTable", q.ARN("ec2", "route-table/*")); err != nil {
		return nil, err
	}
	return nil, s.DisassociateRouteTable(assoc)
}

func (s *Service) awsReplaceRouteTableAssociation(q *awsapi.Req) (any, error) {
	id, err := s.rtbArg(q, "ec2:ReplaceRouteTableAssociation")
	if err != nil {
		return nil, err
	}
	assoc, err := s.ReplaceRouteTableAssociation(q.Param("AssociationId"), id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"newAssociationId": assoc, "associationState": map[string]any{"state": "associated"}}, nil
}
