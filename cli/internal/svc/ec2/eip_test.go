package ec2_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestElasticIPs(t *testing.T) {
	h := liveHarness(t)

	a := h.AWSJSON(t, "ec2", "allocate-address", "--domain", "vpc", "--tag-specifications", "ResourceType=elastic-ip,Tags=[{Key=Name,Value=web}]")
	alloc, ip := a["AllocationId"].(string), a["PublicIp"].(string)
	if !strings.HasPrefix(alloc, "eipalloc-") || !strings.HasPrefix(ip, "203.0.113.") || a["Domain"] != "vpc" {
		t.Fatalf("allocate: %v", a)
	}
	b := h.AWSJSON(t, "ec2", "allocate-address")
	if b["PublicIp"] == ip {
		t.Fatalf("two allocations share %s", ip)
	}
	h.AWS(t, "ec2", "create-tags", "--resources", alloc, "--tags", "Key=env,Value=qa")
	d := h.AWSJSON(t, "ec2", "describe-addresses", "--filters", "Name=tag:Name,Values=web")["Addresses"].([]any)
	if len(d) != 1 || d[0].(map[string]any)["PublicIp"] != ip || len(d[0].(map[string]any)["Tags"].([]any)) != 2 {
		t.Fatalf("describe by tag: %v", d)
	}
	if o, err := h.AWSErr(t, "ec2", "describe-addresses", "--allocation-ids", "eipalloc-nope"); err == nil || !strings.Contains(o, "InvalidAllocationID.NotFound") {
		t.Fatalf("missing: %v %s", err, o)
	}

	sg := h.AWSJSON(t, "ec2", "create-security-group", "--group-name", "eip", "--description", "eip")["GroupId"].(string)
	run := func() string {
		id := h.AWSJSON(t, "ec2", "run-instances", "--image-id", "ami-nginx", "--security-group-ids", sg)["Instances"].([]any)[0].(map[string]any)["InstanceId"].(string)
		t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "terminate-instances", "--instance-ids", id) })
		waitFor(t, "running", 2*time.Minute, func() bool { return state(t, h.Native(t, "GET", "/api/v1/ec2/instances/"+id, nil)) == "running" })
		return id
	}
	i1, i2 := run(), run()
	publicIP := func(id string) string {
		r := h.AWSJSON(t, "ec2", "describe-instances", "--instance-ids", id)["Reservations"].([]any)[0].(map[string]any)["Instances"].([]any)[0].(map[string]any)
		s, _ := r["PublicIpAddress"].(string)
		return s
	}

	assoc := h.AWSJSON(t, "ec2", "associate-address", "--allocation-id", alloc, "--instance-id", i1)["AssociationId"].(string)
	if !strings.HasPrefix(assoc, "eipassoc-") || publicIP(i1) != ip {
		t.Fatalf("associate: %s public %q", assoc, publicIP(i1))
	}
	d = h.AWSJSON(t, "ec2", "describe-addresses", "--filters", "Name=instance-id,Values="+i1)["Addresses"].([]any)
	if len(d) != 1 || d[0].(map[string]any)["AssociationId"] != assoc {
		t.Fatalf("describe by instance: %v", d)
	}
	if o, err := h.AWSErr(t, "ec2", "associate-address", "--allocation-id", alloc, "--instance-id", i2); err == nil || !strings.Contains(o, "Resource.AlreadyAssociated") {
		t.Fatalf("reassociate without flag: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "ec2", "release-address", "--allocation-id", alloc); err == nil || !strings.Contains(o, "InvalidIPAddress.InUse") {
		t.Fatalf("release in use: %v %s", err, o)
	}

	// The address survives stop and start.
	h.AWS(t, "ec2", "stop-instances", "--instance-ids", i1)
	waitFor(t, "stopped", 2*time.Minute, func() bool { return state(t, h.Native(t, "GET", "/api/v1/ec2/instances/"+i1, nil)) == "stopped" })
	if publicIP(i1) != ip {
		t.Fatalf("lost address while stopped: %q", publicIP(i1))
	}
	h.AWS(t, "ec2", "start-instances", "--instance-ids", i1)
	waitFor(t, "running", 2*time.Minute, func() bool { return state(t, h.Native(t, "GET", "/api/v1/ec2/instances/"+i1, nil)) == "running" })
	if publicIP(i1) != ip {
		t.Fatalf("lost address after start: %q", publicIP(i1))
	}

	// It follows the instance it is moved to.
	h.AWS(t, "ec2", "associate-address", "--allocation-id", alloc, "--instance-id", i2, "--allow-reassociation")
	if publicIP(i1) != "" || publicIP(i2) != ip {
		t.Fatalf("after move: i1=%q i2=%q", publicIP(i1), publicIP(i2))
	}

	// Native routes.
	var list []struct {
		AllocationID  string `json:"allocation_id"`
		AssociationID string `json:"association_id"`
		InstanceID    string `json:"instance_id"`
	}
	if err := json.Unmarshal(h.Native(t, "GET", "/api/v1/ec2/addresses", nil), &list); err != nil || len(list) != 2 {
		t.Fatalf("native list: %v %v", err, list)
	}
	h.Native(t, "POST", "/api/v1/ec2/addresses/"+alloc+"/disassociate", nil)
	if publicIP(i2) != "" {
		t.Fatalf("still associated: %q", publicIP(i2))
	}
	h.Native(t, "POST", "/api/v1/ec2/addresses/"+alloc+"/associate", map[string]any{"instance_id": i1})
	if publicIP(i1) != ip {
		t.Fatalf("native associate: %q", publicIP(i1))
	}

	// Terminating the instance frees the association but keeps the allocation.
	h.AWS(t, "ec2", "terminate-instances", "--instance-ids", i1)
	waitFor(t, "disassociated", time.Minute, func() bool {
		return len(h.AWSJSON(t, "ec2", "describe-addresses", "--allocation-ids", alloc)["Addresses"].([]any)[0].(map[string]any)) > 0 &&
			h.AWSJSON(t, "ec2", "describe-addresses", "--allocation-ids", alloc)["Addresses"].([]any)[0].(map[string]any)["AssociationId"] == nil
	})
	h.AWS(t, "ec2", "release-address", "--allocation-id", alloc)
	h.Native(t, "DELETE", "/api/v1/ec2/addresses/"+b["AllocationId"].(string), nil)
	if l := h.AWSJSON(t, "ec2", "describe-addresses")["Addresses"].([]any); len(l) != 0 {
		t.Fatalf("addresses left: %v", l)
	}
	if o, err := h.AWSErr(t, "ec2", "disassociate-address", "--association-id", assoc); err == nil || !strings.Contains(o, "InvalidAssociationID.NotFound") {
		t.Fatalf("stale association: %v %s", err, o)
	}
	audit := strings.Join(h.AuditLog(), "\n")
	for _, want := range []string{"ec2:AllocateAddress arn:aws:ec2:us-east-1:123456789012:elastic-ip/", "ec2:AssociateAddress arn:aws:ec2:us-east-1:123456789012:elastic-ip/" + alloc,
		"ec2:AssociateAddress arn:aws:ec2:us-east-1:123456789012:instance/" + i1, "ec2:ReleaseAddress arn:aws:ec2:us-east-1:123456789012:elastic-ip/" + alloc,
		"ec2:DisassociateAddress arn:aws:ec2:us-east-1:123456789012:elastic-ip/" + alloc} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit log lacks %q", want)
		}
	}
}

func state(t *testing.T, body []byte) string {
	var i struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(body, &i)
	return i.State
}
