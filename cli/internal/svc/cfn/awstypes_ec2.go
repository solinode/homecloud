package cfn

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// EC2, VPC and instance profile resources. They call the native VPC, EC2 and
// IAM APIs as the caller, so those services' permission checks apply. What the
// native model cannot express is documented per type: tags on VPCs, subnets and
// security groups are not stored (only the Name tag, as the resource's name),
// egress rules are not recorded, and only rules that publish ports (tcp/udp from
// anywhere or from this host) are created.

func nameTag(in map[string]any) string { return tagMap(in["Tags"])["Name"] }

// ingressRule translates a CloudFormation rule if the native security group model can hold a rule:
// tcp or udp from 0.0.0.0/0 or 127.0.0.1/32. Other rules (icmp, all traffic,
// other source ranges or groups) only matter inside a VPC, where everything is
// reachable already, so they are left out.
func ingressRule(m map[string]any) (map[string]any, bool) {
	proto := strings.ToLower(sv(m, "IpProtocol"))
	if proto != "tcp" && proto != "udp" {
		return nil, false
	}
	cidr := sv(m, "CidrIp")
	if cidr != "0.0.0.0/0" && cidr != "127.0.0.1/32" {
		return nil, false
	}
	r := map[string]any{"protocol": proto, "cidr": cidr}
	setInt(r, "from_port", m, "FromPort")
	setInt(r, "to_port", m, "ToPort")
	if has(m, "Description") {
		r["description"] = sv(m, "Description")
	}
	return r, true
}

func first(v any) map[string]any {
	if l, ok := v.([]any); ok && len(l) > 0 {
		m, _ := l[0].(map[string]any)
		return m
	}
	return nil
}

// sgByName finds a security group of the default VPC (or any VPC) by name.
func sgByName(x *xctx, name string) (string, error) {
	out, err := x.Call("GET", "/api/v1/vpc/security-groups", nil)
	if err != nil {
		return "", err
	}
	l, _ := out.([]any)
	for _, e := range l {
		if g, ok := e.(map[string]any); ok && sv(g, "name") == name {
			return sv(g, "id"), nil
		}
	}
	return "", fmt.Errorf("The security group '%s' does not exist", name)
}

func idAtt(name string, more map[string]func(v *attrView) any) func(v *attrView, n string) (any, bool) {
	return func(v *attrView, n string) (any, bool) {
		if n == name {
			return v.ID, true
		}
		if f, ok := more[n]; ok {
			return f(v), true
		}
		return nil, false
	}
}

func init() {
	awsTypes["AWS::EC2::VPC"] = awsType{
		HC: "HC::EC2::VPC",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "CidrBlock"); err != nil {
				return nil, err
			}
			// Like AWS, a new VPC has no internet access until an internet
			// gateway is attached and routed to.
			return map[string]any{"name": nameTag(in), "cidr": sv(in, "CidrBlock"), "internet_access": false}, nil
		},
		Post: func(x *xctx, id string, in, attrs map[string]any) error {
			if out, err := x.Call("GET", "/api/v1/vpc/security-groups?vpc_id="+url.QueryEscape(id), nil); err == nil {
				l, _ := out.([]any)
				for _, e := range l {
					if g, ok := e.(map[string]any); ok && sv(g, "name") == "default" {
						attrs["default_security_group"] = sv(g, "id")
					}
				}
			}
			return nil
		},
		Att: idAtt("VpcId", map[string]func(v *attrView) any{
			"CidrBlock":            func(v *attrView) any { return attrStr(v, "cidr") },
			"DefaultSecurityGroup": func(v *attrView) any { return attrStr(v, "default_security_group") },
		}),
	}

	awsTypes["AWS::EC2::Subnet"] = awsType{
		HC: "HC::EC2::Subnet",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "VpcId", "CidrBlock"); err != nil {
				return nil, err
			}
			out := map[string]any{"vpc_id": sv(in, "VpcId"), "name": nameTag(in), "cidr": sv(in, "CidrBlock")}
			if has(in, "AvailabilityZone") {
				out["availability_zone"] = sv(in, "AvailabilityZone")
			}
			return out, nil
		},
		Att: idAtt("SubnetId", map[string]func(v *attrView) any{
			"AvailabilityZone": func(v *attrView) any { return attrStr(v, "availability_zone") },
			"VpcId":            func(v *attrView) any { return attrStr(v, "vpc_id") },
		}),
	}

	awsTypes["AWS::EC2::SecurityGroup"] = awsType{
		HC: "HC::EC2::SecurityGroup",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "GroupDescription"); err != nil {
				return nil, err
			}
			name := sv(in, "GroupName")
			if name == "" {
				name = x.GenName(255, false)
			}
			out := map[string]any{"name": name, "description": sv(in, "GroupDescription")}
			if has(in, "VpcId") {
				out["vpc_id"] = sv(in, "VpcId")
			}
			rules := []any{}
			for _, e := range lv(in, "SecurityGroupIngress") {
				if m, ok := e.(map[string]any); ok {
					if r, ok := ingressRule(m); ok {
						rules = append(rules, r)
					}
				}
			}
			out["ingress"] = rules
			return out, nil
		},
		Att: idAtt("GroupId", map[string]func(v *attrView) any{
			"VpcId": func(v *attrView) any { return attrStr(v, "vpc_id") },
		}),
	}

	sgRule := func(x *xctx, in map[string]any) (string, map[string]any, error) {
		gid := sv(in, "GroupId")
		if gid == "" && has(in, "GroupName") {
			var err error
			if gid, err = sgByName(x, sv(in, "GroupName")); err != nil {
				return "", nil, err
			}
		}
		if gid == "" {
			return "", nil, fmt.Errorf("Property validation failure: [The property {/GroupId} is required]")
		}
		r, ok := ingressRule(in)
		if !ok {
			// Not expressible (see ingressRule): nothing to create.
			return x.Stack + "-" + x.Logical + "-" + randID(12), map[string]any{"group": gid}, nil
		}
		out, err := x.Call("POST", "/api/v1/vpc/security-groups/"+esc(gid)+"/ingress", r)
		if err != nil {
			return "", nil, err
		}
		rule := ""
		if g, ok := out.(map[string]any); ok {
			if l := lv(g, "ingress"); len(l) > 0 {
				if m, ok := l[len(l)-1].(map[string]any); ok {
					rule = sv(m, "id")
				}
			}
		}
		return rule, map[string]any{"group": gid, "rule": rule}, nil
	}
	awsTypes["AWS::EC2::SecurityGroupIngress"] = awsType{
		Create: sgRule,
		Delete: func(x *xctx, r *Resource) error {
			g, rule := toStr(r.Attributes["group"]), toStr(r.Attributes["rule"])
			if g == "" || rule == "" || rule == "<nil>" {
				return nil
			}
			_, err := x.Call("DELETE", "/api/v1/vpc/security-groups/"+esc(g)+"/ingress/"+esc(rule), nil)
			return err
		},
	}
	// Egress rules are not recorded by the native model (all outbound traffic is allowed).
	awsTypes["AWS::EC2::SecurityGroupEgress"] = awsType{}

	awsTypes["AWS::EC2::InternetGateway"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			body := map[string]any{}
			if t := tagMap(in["Tags"]); t != nil {
				body["tags"] = t
			}
			out, err := x.Call("POST", "/api/v1/ec2/internet-gateways", body)
			if err != nil {
				return "", nil, err
			}
			attrs, _ := out.(map[string]any)
			return sv(attrs, "id"), attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/ec2/internet-gateways/"+esc(r.native()), nil)
			return err
		},
		Att: idAtt("InternetGatewayId", nil),
	}

	awsTypes["AWS::EC2::VPCGatewayAttachment"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "VpcId"); err != nil {
				return "", nil, err
			}
			if has(in, "VpnGatewayId") {
				return "", nil, fmt.Errorf("Property VpnGatewayId is not supported by HomeCloud")
			}
			if err := req(in, "InternetGatewayId"); err != nil {
				return "", nil, err
			}
			igw, vpc := sv(in, "InternetGatewayId"), sv(in, "VpcId")
			if _, err := x.Call("POST", "/api/v1/ec2/internet-gateways/"+esc(igw)+"/attach", map[string]any{"vpc_id": vpc}); err != nil {
				return "", nil, err
			}
			return igw + "|" + vpc, map[string]any{"internet_gateway_id": igw, "vpc_id": vpc}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("POST", "/api/v1/ec2/internet-gateways/"+esc(toStr(r.Attributes["internet_gateway_id"]))+"/detach",
				map[string]any{"vpc_id": toStr(r.Attributes["vpc_id"])})
			return err
		},
	}

	awsTypes["AWS::EC2::RouteTable"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "VpcId"); err != nil {
				return "", nil, err
			}
			body := map[string]any{"vpc_id": sv(in, "VpcId")}
			if t := tagMap(in["Tags"]); t != nil {
				body["tags"] = t
			}
			out, err := x.Call("POST", "/api/v1/ec2/route-tables", body)
			if err != nil {
				return "", nil, err
			}
			attrs, _ := out.(map[string]any)
			return sv(attrs, "id"), attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/ec2/route-tables/"+esc(r.native()), nil)
			return err
		},
	}

	awsTypes["AWS::EC2::Route"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "RouteTableId"); err != nil {
				return "", nil, err
			}
			if !has(in, "DestinationCidrBlock") {
				if has(in, "DestinationIpv6CidrBlock") {
					return "", nil, fmt.Errorf("Property DestinationIpv6CidrBlock is not supported by HomeCloud")
				}
				return "", nil, fmt.Errorf("Property validation failure: [The property {/DestinationCidrBlock} is required]")
			}
			dest := sv(in, "DestinationCidrBlock")
			body := map[string]any{"destination": dest}
			if has(in, "GatewayId") {
				body["gateway_id"] = sv(in, "GatewayId")
			}
			for p, kind := range map[string]string{"NatGatewayId": "natGatewayId", "NetworkInterfaceId": "networkInterfaceId", "InstanceId": "instanceId",
				"VpcPeeringConnectionId": "vpcPeeringConnectionId", "TransitGatewayId": "transitGatewayId", "VpcEndpointId": "gatewayId",
				"EgressOnlyInternetGatewayId": "egressOnlyInternetGatewayId", "CarrierGatewayId": "carrierGatewayId", "LocalGatewayId": "localGatewayId"} {
				if has(in, p) {
					body["target"], body["target_kind"] = sv(in, p), kind
				}
			}
			rtb := sv(in, "RouteTableId")
			if _, err := x.Call("POST", "/api/v1/ec2/route-tables/"+esc(rtb)+"/routes", body); err != nil {
				return "", nil, err
			}
			return rtb + "|" + dest, map[string]any{"route_table_id": rtb, "destination": dest}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/ec2/route-tables/"+esc(toStr(r.Attributes["route_table_id"]))+"/routes?destination="+url.QueryEscape(toStr(r.Attributes["destination"])), nil)
			return err
		},
	}

	awsTypes["AWS::EC2::SubnetRouteTableAssociation"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "RouteTableId", "SubnetId"); err != nil {
				return "", nil, err
			}
			rtb := sv(in, "RouteTableId")
			out, err := x.Call("POST", "/api/v1/ec2/route-tables/"+esc(rtb)+"/associations", map[string]any{"subnet_id": sv(in, "SubnetId")})
			if err != nil {
				return "", nil, err
			}
			m, _ := out.(map[string]any)
			return sv(m, "association_id"), map[string]any{"route_table_id": rtb}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/ec2/route-tables/"+esc(toStr(r.Attributes["route_table_id"]))+"/associations/"+esc(r.native()), nil)
			return err
		},
	}

	awsTypes["AWS::EC2::EIP"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			body := map[string]any{}
			if t := tagMap(in["Tags"]); t != nil {
				body["tags"] = t
			}
			out, err := x.Call("POST", "/api/v1/ec2/addresses", body)
			if err != nil {
				return "", nil, err
			}
			attrs, _ := out.(map[string]any)
			id := sv(attrs, "allocation_id")
			if has(in, "InstanceId") {
				if _, err := x.Call("POST", "/api/v1/ec2/addresses/"+esc(id)+"/associate", map[string]any{"instance_id": sv(in, "InstanceId")}); err != nil {
					return id, attrs, err
				}
			}
			return id, attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/ec2/addresses/"+esc(r.native()), nil)
			return err
		},
		Ref: func(v *attrView) string { return attrStr(v, "public_ip") },
		Att: idAtt("AllocationId", map[string]func(v *attrView) any{
			"PublicIp": func(v *attrView) any { return attrStr(v, "public_ip") },
		}),
	}

	awsTypes["AWS::EC2::EIPAssociation"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "InstanceId"); err != nil {
				return "", nil, err
			}
			alloc := sv(in, "AllocationId")
			if alloc == "" {
				return "", nil, fmt.Errorf("Property validation failure: [The property {/AllocationId} is required]")
			}
			out, err := x.Call("POST", "/api/v1/ec2/addresses/"+esc(alloc)+"/associate", map[string]any{"instance_id": sv(in, "InstanceId"), "reassociate": bv(in, "AllowReassociation")})
			if err != nil {
				return "", nil, err
			}
			m, _ := out.(map[string]any)
			id := firstNonEmpty(sv(m, "association_id"), alloc)
			return id, map[string]any{"allocation_id": alloc}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("POST", "/api/v1/ec2/addresses/"+esc(toStr(r.Attributes["allocation_id"]))+"/disassociate", nil)
			return err
		},
	}

	awsTypes["AWS::EC2::Volume"] = awsType{
		HC: "HC::EC2::Volume",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "AvailabilityZone"); err != nil {
				return nil, err
			}
			out := map[string]any{"name": nameTag(in), "availability_zone": sv(in, "AvailabilityZone")}
			setInt(out, "size_gb", in, "Size")
			if has(in, "SnapshotId") {
				out["snapshot_id"] = sv(in, "SnapshotId")
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Att: idAtt("VolumeId", nil),
	}

	awsTypes["AWS::EC2::Instance"] = awsType{
		HC: "HC::EC2::Instance",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "ImageId"); err != nil {
				return nil, err
			}
			out := map[string]any{"image_id": sv(in, "ImageId"), "name": nameTag(in)}
			if has(in, "InstanceType") {
				out["instance_type"] = sv(in, "InstanceType")
			}
			subnet := sv(in, "SubnetId")
			groups := strs(lv(in, "SecurityGroupIds"))
			if ni := first(in["NetworkInterfaces"]); ni != nil {
				subnet = firstNonEmpty(subnet, sv(ni, "SubnetId"))
				if len(groups) == 0 {
					groups = strs(lv(ni, "GroupSet"))
				}
			}
			if subnet != "" {
				out["subnet_id"] = subnet
			}
			for _, n := range strs(lv(in, "SecurityGroups")) {
				id, err := sgByName(x, n)
				if err != nil {
					return nil, err
				}
				groups = append(groups, id)
			}
			if len(groups) > 0 {
				out["security_group_ids"] = groups
			}
			if has(in, "KeyName") {
				out["key_name"] = sv(in, "KeyName")
			}
			if has(in, "IamInstanceProfile") {
				out["iam_instance_profile"] = sv(in, "IamInstanceProfile")
			}
			if ud := sv(in, "UserData"); ud != "" {
				if b, err := base64.StdEncoding.DecodeString(ud); err == nil {
					ud = string(b)
				}
				out["user_data"] = ud
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "PrivateIp":
				return attrStr(v, "private_ip"), true
			case "PrivateDnsName":
				return attrStr(v, "private_dns"), true
			case "PublicDnsName":
				return attrStr(v, "public_host"), true
			case "PublicIp":
				// The host address the instance's published ports are on.
				if len(mv(v.Attrs, "public_ports")) == 0 {
					return "", true
				}
				return attrStr(v, "public_host"), true
			case "AvailabilityZone":
				return attrStr(v, "availability_zone"), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::IAM::InstanceProfile"] = awsType{
		IAM: true, Named: "InstanceProfileName",
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "Roles"); err != nil {
				return "", nil, err
			}
			roles := strs(lv(in, "Roles"))
			if len(roles) != 1 {
				return "", nil, fmt.Errorf("An instance profile holds exactly one role in HomeCloud (as in AWS), got %d", len(roles))
			}
			name := sv(in, "InstanceProfileName")
			if name == "" {
				name = x.GenName(128, false)
			}
			body := map[string]any{"name": name}
			if has(in, "Path") {
				body["path"] = sv(in, "Path")
			}
			if _, err := x.Call("POST", "/api/v1/iam/instance-profiles", body); err != nil {
				return "", nil, err
			}
			// Add the role separately, so a failure can remove the empty profile again.
			out, err := x.Call("POST", "/api/v1/iam/instance-profiles/"+esc(name)+"/roles", map[string]any{"role": lastSeg(roles[0])})
			if err != nil {
				_, _ = x.Call("DELETE", "/api/v1/iam/instance-profiles/"+esc(name), nil)
				return "", nil, err
			}
			attrs, _ := out.(map[string]any)
			return name, attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/iam/instance-profiles/"+esc(r.native())+"?force=true", nil)
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "Arn" {
				return firstNonEmpty(attrStr(v, "arn"), iamARN(v, "instance-profile")), true
			}
			return nil, false
		},
	}
}
