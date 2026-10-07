package ec2_test

import (
	"strings"
	"testing"
)

// TestNATGateways drives NAT gateways the way the terraform-aws-modules VPC
// module does: an Elastic IP, a gateway in a public subnet, a private route to
// it, then teardown in reverse.
func TestNATGateways(t *testing.T) {
	h := liveHarness(t)
	vpcID := h.AWSJSON(t, "ec2", "create-vpc", "--cidr-block", "10.31.0.0/16")["Vpc"].(map[string]any)["VpcId"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "delete-vpc", "--vpc-id", vpcID) })
	sn := h.AWSJSON(t, "ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.31.1.0/24")["Subnet"].(map[string]any)["SubnetId"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "delete-subnet", "--subnet-id", sn) })
	eip := h.AWSJSON(t, "ec2", "allocate-address", "--domain", "vpc")
	alloc := eip["AllocationId"].(string)
	// Terraform reads every Elastic IP's reverse DNS attribute.
	attr := h.AWSJSON(t, "ec2", "describe-addresses-attribute", "--allocation-ids", alloc, "--attribute", "domain-name")["Addresses"].([]any)
	if len(attr) != 1 || attr[0].(map[string]any)["AllocationId"] != alloc || attr[0].(map[string]any)["PtrRecord"] != nil {
		t.Fatalf("addresses attribute: %v", attr)
	}

	if o, err := h.AWSErr(t, "ec2", "create-nat-gateway", "--subnet-id", sn); err == nil || !strings.Contains(o, "MissingParameter") {
		t.Fatalf("public gateway without an address: %v %s", err, o)
	}
	ng := h.AWSJSON(t, "ec2", "create-nat-gateway", "--subnet-id", sn, "--allocation-id", alloc,
		"--tag-specifications", "ResourceType=natgateway,Tags=[{Key=Name,Value=main}]")["NatGateway"].(map[string]any)
	id := ng["NatGatewayId"].(string)
	addr := ng["NatGatewayAddresses"].([]any)[0].(map[string]any)
	if !strings.HasPrefix(id, "nat-") || ng["State"] != "available" || ng["VpcId"] != vpcID || addr["PublicIp"] != eip["PublicIp"] ||
		!strings.HasPrefix(addr["PrivateIp"].(string), "10.31.1.") || !strings.HasPrefix(addr["NetworkInterfaceId"].(string), "eni-") {
		t.Fatalf("create: %v", ng)
	}
	h.AWS(t, "ec2", "wait", "nat-gateway-available", "--nat-gateway-ids", id)
	d := h.AWSJSON(t, "ec2", "describe-nat-gateways", "--filter", "Name=tag:Name,Values=main", "Name=vpc-id,Values="+vpcID)["NatGateways"].([]any)
	if len(d) != 1 || d[0].(map[string]any)["NatGatewayId"] != id {
		t.Fatalf("describe by tag: %v", d)
	}
	// The Elastic IP is held by the gateway's interface until the gateway goes.
	a := h.AWSJSON(t, "ec2", "describe-addresses", "--allocation-ids", alloc)["Addresses"].([]any)[0].(map[string]any)
	if a["NetworkInterfaceId"] != addr["NetworkInterfaceId"] || a["AssociationId"] == nil {
		t.Fatalf("address: %v", a)
	}
	if o, err := h.AWSErr(t, "ec2", "release-address", "--allocation-id", alloc); err == nil || !strings.Contains(o, "InvalidIPAddress.InUse") {
		t.Fatalf("release in use: %v %s", err, o)
	}

	rtb := h.AWSJSON(t, "ec2", "create-route-table", "--vpc-id", vpcID)["RouteTable"].(map[string]any)["RouteTableId"].(string)
	h.AWS(t, "ec2", "create-route", "--route-table-id", rtb, "--destination-cidr-block", "0.0.0.0/0", "--nat-gateway-id", id)
	routes := h.AWSJSON(t, "ec2", "describe-route-tables", "--route-table-ids", rtb)["RouteTables"].([]any)[0].(map[string]any)["Routes"].([]any)
	if r := routes[len(routes)-1].(map[string]any); r["NatGatewayId"] != id {
		t.Fatalf("routes: %v", routes)
	}
	if o, err := h.AWSErr(t, "ec2", "delete-subnet", "--subnet-id", sn); err == nil || !strings.Contains(o, "DependencyViolation") {
		t.Fatalf("delete subnet with a gateway: %v %s", err, o)
	}

	h.AWS(t, "ec2", "delete-nat-gateway", "--nat-gateway-id", id)
	h.AWS(t, "ec2", "wait", "nat-gateway-deleted", "--nat-gateway-ids", id)
	if o, err := h.AWSErr(t, "ec2", "delete-nat-gateway", "--nat-gateway-id", id); err == nil || !strings.Contains(o, "NatGatewayNotFound") {
		t.Fatalf("delete twice: %v %s", err, o)
	}
	h.AWS(t, "ec2", "delete-route", "--route-table-id", rtb, "--destination-cidr-block", "0.0.0.0/0")
	h.AWS(t, "ec2", "delete-route-table", "--route-table-id", rtb)
	h.AWS(t, "ec2", "release-address", "--allocation-id", alloc)
	h.AWS(t, "ec2", "delete-subnet", "--subnet-id", sn)
}
