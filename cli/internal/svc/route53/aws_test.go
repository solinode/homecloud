package route53

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

func setup(t *testing.T) (*awstest.Harness, *Service) {
	t.Helper()
	h := awstest.New(t)
	for _, id := range []string{"vpc-aaa", "vpc-bbb"} {
		cidr := "10.88.0.0/16"
		if id == "vpc-bbb" {
			cidr = "10.89.0.0/16"
		}
		if err := store.Put(h.Env.Store, "vpc_vpcs", id, vpc.VPC{ID: id, Name: id, CIDR: cidr, Network: "hc-" + id, State: "available"}); err != nil {
			t.Fatal(err)
		}
	}
	s := New(h.Env, vpc.New(h.Env))
	s.Resolve = func(id string) (string, bool) {
		if id == "web" {
			return "10.88.0.7", true
		}
		return "", false
	}
	s.RegisterAWS()
	s.Routes(h.Router)
	return h, s
}

func batch(t *testing.T, h *awstest.Harness, zone, json string) string {
	t.Helper()
	return h.AWS(t, "route53", "change-resource-record-sets", "--hosted-zone-id", zone, "--change-batch", json)
}

func change(action, name, typ string, ttl int, values ...string) string {
	rr := ""
	for i, v := range values {
		if i > 0 {
			rr += ","
		}
		b, _ := json.Marshal(v)
		rr += `{"Value":` + string(b) + `}`
	}
	return `{"Action":"` + action + `","ResourceRecordSet":{"Name":"` + name + `","Type":"` + typ + `","TTL":` + strconv.Itoa(ttl) + `,"ResourceRecords":[` + rr + `]}}`
}

func batchOf(changes ...string) string {
	return `{"Comment":"test","Changes":[` + strings.Join(changes, ",") + `]}`
}

func TestAWSZonesAndRecords(t *testing.T) {
	h, _ := setup(t)
	z := h.AWSJSON(t, "route53", "create-hosted-zone", "--name", "example.com", "--caller-reference", "ref-1",
		"--hosted-zone-config", "Comment=demo")
	zone := z["HostedZone"].(map[string]any)
	id := strings.TrimPrefix(zone["Id"].(string), "/hostedzone/")
	if !strings.HasPrefix(zone["Id"].(string), "/hostedzone/Z") || zone["Name"] != "example.com." || zone["Config"].(map[string]any)["Comment"] != "demo" {
		t.Fatalf("zone %v", zone)
	}
	if ns := z["DelegationSet"].(map[string]any)["NameServers"].([]any); len(ns) == 0 {
		t.Fatalf("no name servers: %v", z)
	}
	if !strings.HasPrefix(z["ChangeInfo"].(map[string]any)["Id"].(string), "/change/C") {
		t.Fatalf("change %v", z["ChangeInfo"])
	}
	if o, err := h.AWSErr(t, "route53", "create-hosted-zone", "--name", "other.com", "--caller-reference", "ref-1"); err == nil || !strings.Contains(o, "HostedZoneAlreadyExists") {
		t.Fatalf("duplicate reference: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "route53", "create-hosted-zone", "--name", "example.com", "--caller-reference", "ref-2"); err == nil || !strings.Contains(o, "HostedZoneAlreadyExists") {
		t.Fatalf("duplicate name: %v %s", err, o)
	}

	got := h.AWSJSON(t, "route53", "get-hosted-zone", "--id", id)
	if got["HostedZone"].(map[string]any)["ResourceRecordSetCount"].(float64) != 2 {
		t.Fatalf("empty zone has NS and SOA: %v", got)
	}
	if l := h.AWSJSON(t, "route53", "list-hosted-zones")["HostedZones"].([]any); len(l) != 1 {
		t.Fatalf("list %v", l)
	}
	if l := h.AWSJSON(t, "route53", "list-hosted-zones-by-name", "--dns-name", "example.com")["HostedZones"].([]any); len(l) != 1 {
		t.Fatalf("by name %v", l)
	}
	if l := h.AWSJSON(t, "route53", "list-hosted-zones-by-name", "--dns-name", "zzz.com")["HostedZones"]; l != nil && len(l.([]any)) != 0 {
		t.Fatalf("by name past the end: %v", l)
	}

	// Records.
	out := batch(t, h, id, batchOf(
		change("CREATE", "www.example.com", "A", 60, "192.0.2.1", "192.0.2.2"),
		change("CREATE", "example.com", "TXT", 300, `"v=spf1 include:_spf.example.com ~all"`, `"a;b"`),
		change("CREATE", "example.com", "MX", 300, "10 mail.example.com.", "20 mail2.example.com."),
		change("CREATE", "_sip._tcp.example.com", "SRV", 300, "1 10 5060 sip.example.com."),
		change("CREATE", "*.example.com", "CNAME", 300, "www.example.com."),
		change("CREATE", "v6.example.com", "AAAA", 300, "2001:db8::1"),
		change("CREATE", "d._domainkey.example.com", "TXT", 300, `"`+strings.Repeat("k", 200)+`" "`+strings.Repeat("m", 200)+`"`),
		`{"Action":"CREATE","ResourceRecordSet":{"Name":"app.example.com","Type":"A","AliasTarget":{"HostedZoneId":"Z35SXDOTRQ7X7K","DNSName":"web.elb.internal","EvaluateTargetHealth":true}}}`,
	))
	var ch map[string]any
	_ = json.Unmarshal([]byte(out), &ch)
	cid := strings.TrimPrefix(ch["ChangeInfo"].(map[string]any)["Id"].(string), "/change/")
	if h.AWSJSON(t, "route53", "get-change", "--id", cid)["ChangeInfo"].(map[string]any)["Status"] != "INSYNC" {
		t.Fatal("change not INSYNC")
	}
	if o, err := h.AWSErr(t, "route53", "get-change", "--id", "CNOPE"); err == nil || !strings.Contains(o, "NoSuchChange") {
		t.Fatalf("missing change: %v %s", err, o)
	}
	rrs := h.AWSJSON(t, "route53", "list-resource-record-sets", "--hosted-zone-id", id)["ResourceRecordSets"].([]any)
	byKey := map[string]map[string]any{}
	for _, r := range rrs {
		m := r.(map[string]any)
		byKey[m["Name"].(string)+" "+m["Type"].(string)] = m
	}
	for _, k := range []string{"example.com. NS", "example.com. SOA", "www.example.com. A", `\052.example.com. CNAME`, "example.com. MX", "example.com. TXT", "app.example.com. A", "_sip._tcp.example.com. SRV", "v6.example.com. AAAA"} {
		if byKey[k] == nil {
			t.Errorf("missing %s in %v", k, keys(byKey))
		}
	}
	if a := byKey["app.example.com. A"]["AliasTarget"].(map[string]any); a["DNSName"] != "web.elb.internal." || a["HostedZoneId"] != "Z35SXDOTRQ7X7K" || a["EvaluateTargetHealth"] != true {
		t.Errorf("alias %v", a)
	}
	txt := byKey["example.com. TXT"]["ResourceRecords"].([]any)
	if txt[0].(map[string]any)["Value"] != `"v=spf1 include:_spf.example.com ~all"` {
		t.Errorf("txt %v", txt)
	}
	dk := byKey["d._domainkey.example.com. TXT"]["ResourceRecords"].([]any)[0].(map[string]any)["Value"].(string)
	if strings.Count(dk, `"`) != 4 || !strings.Contains(dk, strings.Repeat("k", 200)+strings.Repeat("m", 55)+`" "`) {
		t.Errorf("long TXT should be split into 255-byte strings: %s", dk)
	}
	// Start-at and pagination, as Terraform reads a record.
	one := h.AWSJSON(t, "route53", "list-resource-record-sets", "--hosted-zone-id", id, "--start-record-name", "www.example.com", "--start-record-type", "A", "--max-items", "1")
	if r := one["ResourceRecordSets"].([]any); len(r) != 1 || r[0].(map[string]any)["Name"] != "www.example.com." {
		t.Errorf("start at www: %v", one)
	}
	page := h.AWSJSON(t, "route53", "list-resource-record-sets", "--hosted-zone-id", id, "--page-size", "3")
	if len(page["ResourceRecordSets"].([]any)) != len(rrs) {
		t.Errorf("paginated CLI listing returned %d, want %d", len(page["ResourceRecordSets"].([]any)), len(rrs))
	}

	// Batch errors are atomic and use Route 53's phrases.
	if o, err := h.AWSErr(t, "route53", "change-resource-record-sets", "--hosted-zone-id", id, "--change-batch",
		batchOf(change("CREATE", "new.example.com", "A", 60, "192.0.2.9"), change("CREATE", "www.example.com", "A", 60, "192.0.2.3"))); err == nil || !strings.Contains(o, "InvalidChangeBatch") || !strings.Contains(o, "already exists") {
		t.Fatalf("create existing: %v %s", err, o)
	}
	if o := h.AWS(t, "route53", "list-resource-record-sets", "--hosted-zone-id", id, "--start-record-name", "new.example.com", "--start-record-type", "A", "--max-items", "1"); strings.Contains(o, `"new.example.com."`) {
		t.Fatalf("failed batch was partially applied: %s", o)
	}
	if o, err := h.AWSErr(t, "route53", "change-resource-record-sets", "--hosted-zone-id", id, "--change-batch",
		batchOf(change("DELETE", "gone.example.com", "A", 60, "192.0.2.9"))); err == nil || !strings.Contains(o, "not found") {
		t.Fatalf("delete missing: %v %s", err, o)
	}
	for _, bad := range []string{
		batchOf(change("CREATE", "x.other.org", "A", 60, "192.0.2.9")),
		batchOf(change("CREATE", "x.example.com", "A", 60, "nope")),
		batchOf(change("CREATE", "example.com", "CNAME", 60, "a.example.com.")),
		`{"Changes":[{"Action":"CREATE","ResourceRecordSet":{"Name":"w.example.com","Type":"A","SetIdentifier":"a","Weight":10,"TTL":60,"ResourceRecords":[{"Value":"192.0.2.1"}]}}]}`,
		`{"Changes":[{"Action":"CREATE","ResourceRecordSet":{"Name":"w.example.com","Type":"A","AliasTarget":{"HostedZoneId":"Z","DNSName":"d111.cloudfront.net","EvaluateTargetHealth":false}}}]}`,
	} {
		if o, err := h.AWSErr(t, "route53", "change-resource-record-sets", "--hosted-zone-id", id, "--change-batch", bad); err == nil || !strings.Contains(o, "InvalidChangeBatch") {
			t.Errorf("%s: %v %s", bad, err, o)
		}
	}
	if o, err := h.AWSErr(t, "route53", "change-resource-record-sets", "--hosted-zone-id", "ZNOPE", "--change-batch", batchOf(change("CREATE", "a.example.com", "A", 60, "192.0.2.1"))); err == nil || !strings.Contains(o, "NoSuchHostedZone") {
		t.Fatalf("missing zone: %v %s", err, o)
	}

	// UPSERT and DELETE.
	batch(t, h, id, batchOf(change("UPSERT", "www.example.com", "A", 120, "192.0.2.50"), change("UPSERT", "fresh.example.com", "A", 120, "192.0.2.51")))
	o := h.AWS(t, "route53", "list-resource-record-sets", "--hosted-zone-id", id, "--start-record-name", "www.example.com", "--start-record-type", "A", "--max-items", "1")
	if !strings.Contains(o, "192.0.2.50") || strings.Contains(o, "192.0.2.1\"") {
		t.Fatalf("upsert: %s", o)
	}
	batch(t, h, id, batchOf(change("DELETE", "fresh.example.com", "A", 120, "192.0.2.51")))
	if o, err := h.AWSErr(t, "route53", "delete-hosted-zone", "--id", id); err == nil || !strings.Contains(o, "HostedZoneNotEmpty") {
		t.Fatalf("delete non-empty zone: %v %s", err, o)
	}

	// Tags.
	h.AWS(t, "route53", "change-tags-for-resource", "--resource-type", "hostedzone", "--resource-id", id, "--add-tags", "Key=env,Value=qa", "Key=team,Value=x")
	h.AWS(t, "route53", "change-tags-for-resource", "--resource-type", "hostedzone", "--resource-id", id, "--remove-tag-keys", "team")
	tags := h.AWSJSON(t, "route53", "list-tags-for-resource", "--resource-type", "hostedzone", "--resource-id", id)["ResourceTagSet"].(map[string]any)
	if l := tags["Tags"].([]any); len(l) != 1 || l[0].(map[string]any)["Key"] != "env" || tags["ResourceId"] != id {
		t.Fatalf("tags %v", tags)
	}
	h.AWS(t, "route53", "update-hosted-zone-comment", "--id", id, "--comment", "changed")
	if h.AWSJSON(t, "route53", "get-hosted-zone", "--id", id)["HostedZone"].(map[string]any)["Config"].(map[string]any)["Comment"] != "changed" {
		t.Fatal("comment not updated")
	}

	// Empty the zone and delete it.
	var del []string
	for _, r := range h.AWSJSON(t, "route53", "list-resource-record-sets", "--hosted-zone-id", id)["ResourceRecordSets"].([]any) {
		m := r.(map[string]any)
		if m["Type"] == "NS" || m["Type"] == "SOA" {
			continue
		}
		v, _ := json.Marshal(m)
		del = append(del, `{"Action":"DELETE","ResourceRecordSet":`+string(v)+`}`)
	}
	batch(t, h, id, batchOf(del...))
	h.AWS(t, "route53", "delete-hosted-zone", "--id", id)
	if o, err := h.AWSErr(t, "route53", "get-hosted-zone", "--id", id); err == nil || !strings.Contains(o, "NoSuchHostedZone") {
		t.Fatalf("deleted zone: %v %s", err, o)
	}
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAWSPrivateZoneAndVPCs(t *testing.T) {
	h, s := setup(t)
	if o, err := h.AWSErr(t, "route53", "create-hosted-zone", "--name", "corp.internal", "--caller-reference", "p0", "--vpc", "VPCRegion=us-east-1,VPCId=vpc-nope"); err == nil || !strings.Contains(o, "InvalidVPCId") {
		t.Fatalf("bad vpc: %v %s", err, o)
	}
	z := h.AWSJSON(t, "route53", "create-hosted-zone", "--name", "corp.internal", "--caller-reference", "p1", "--vpc", "VPCRegion=us-east-1,VPCId=vpc-aaa")
	id := strings.TrimPrefix(z["HostedZone"].(map[string]any)["Id"].(string), "/hostedzone/")
	if z["HostedZone"].(map[string]any)["Config"].(map[string]any)["PrivateZone"] != true || z["VPC"].(map[string]any)["VPCId"] != "vpc-aaa" {
		t.Fatalf("private zone %v", z)
	}
	if z["DelegationSet"] != nil {
		t.Fatalf("private zones have no delegation set: %v", z)
	}
	got := h.AWSJSON(t, "route53", "get-hosted-zone", "--id", id)
	if v := got["VPCs"].([]any); len(v) != 1 || v[0].(map[string]any)["VPCId"] != "vpc-aaa" {
		t.Fatalf("vpcs %v", got)
	}
	if o, err := h.AWSErr(t, "route53", "disassociate-vpc-from-hosted-zone", "--hosted-zone-id", id, "--vpc", "VPCRegion=us-east-1,VPCId=vpc-aaa"); err == nil || !strings.Contains(o, "LastVPCAssociation") {
		t.Fatalf("last vpc: %v %s", err, o)
	}
	h.AWS(t, "route53", "associate-vpc-with-hosted-zone", "--hosted-zone-id", id, "--vpc", "VPCRegion=us-east-1,VPCId=vpc-bbb")
	if o, err := h.AWSErr(t, "route53", "associate-vpc-with-hosted-zone", "--hosted-zone-id", id, "--vpc", "VPCRegion=us-east-1,VPCId=vpc-bbb"); err == nil || !strings.Contains(o, "ConflictingDomainExists") {
		t.Fatalf("twice: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "route53", "associate-vpc-with-hosted-zone", "--hosted-zone-id", id, "--vpc", "VPCRegion=us-east-1,VPCId=vpc-nope"); err == nil || !strings.Contains(o, "InvalidVPCId") {
		t.Fatalf("unknown vpc: %v %s", err, o)
	}
	if l := h.AWSJSON(t, "route53", "list-hosted-zones-by-vpc", "--vpc-id", "vpc-bbb", "--vpc-region", "us-east-1")["HostedZoneSummaries"].([]any); len(l) != 1 {
		t.Fatalf("by vpc %v", l)
	}
	h.AWS(t, "route53", "disassociate-vpc-from-hosted-zone", "--hosted-zone-id", id, "--vpc", "VPCRegion=us-east-1,VPCId=vpc-aaa")
	zn, _ := s.zone(id)
	if len(zn.VpcIDs) != 1 || zn.VpcIDs[0] != "vpc-bbb" {
		t.Fatalf("zone vpcs %v", zn.VpcIDs)
	}
	// The native API sees the same zone, and public zones cannot take VPCs.
	if n := string(h.Native(t, "GET", "/api/v1/route53/zones", nil)); !strings.Contains(n, "corp.internal.") {
		t.Fatalf("native list: %s", n)
	}
	pub := h.AWSJSON(t, "route53", "create-hosted-zone", "--name", "pub.example", "--caller-reference", "p2")["HostedZone"].(map[string]any)["Id"].(string)
	if o, err := h.AWSErr(t, "route53", "associate-vpc-with-hosted-zone", "--hosted-zone-id", pub, "--vpc", "VPCRegion=us-east-1,VPCId=vpc-aaa"); err == nil || !strings.Contains(o, "PublicZoneVPCAssociation") {
		t.Fatalf("public zone: %v %s", err, o)
	}
}

func TestAWSBoto3(t *testing.T) {
	h, _ := setup(t)
	out := h.Python(t, `
r = boto3.client("route53")
z = r.create_hosted_zone(Name="boto.example.", CallerReference="b1", HostedZoneConfig={"Comment": "c"})
zid = z["HostedZone"]["Id"]
assert zid.startswith("/hostedzone/Z"), zid
ch = r.change_resource_record_sets(HostedZoneId=zid, ChangeBatch={"Changes": [
  {"Action": "CREATE", "ResourceRecordSet": {"Name": "a.boto.example.", "Type": "A", "TTL": 30, "ResourceRecords": [{"Value": "192.0.2.10"}]}},
  {"Action": "UPSERT", "ResourceRecordSet": {"Name": "boto.example.", "Type": "TXT", "TTL": 30, "ResourceRecords": [{"Value": '"v=DMARC1; p=none"'}]}},
]})
assert r.get_change(Id=ch["ChangeInfo"]["Id"])["ChangeInfo"]["Status"] == "INSYNC"
rr = r.list_resource_record_sets(HostedZoneId=zid, StartRecordName="a.boto.example.", StartRecordType="A", MaxItems="1")
assert rr["ResourceRecordSets"][0]["ResourceRecords"][0]["Value"] == "192.0.2.10", rr
try:
    r.change_resource_record_sets(HostedZoneId=zid, ChangeBatch={"Changes": [{"Action": "CREATE", "ResourceRecordSet": {"Name": "a.boto.example.", "Type": "A", "TTL": 30, "ResourceRecords": [{"Value": "192.0.2.10"}]}}]})
    raise SystemExit("expected error")
except r.exceptions.InvalidChangeBatch as e:
    assert "already exists" in str(e), e
try:
    r.delete_hosted_zone(Id=zid)
    raise SystemExit("expected HostedZoneNotEmpty")
except r.exceptions.HostedZoneNotEmpty:
    pass
try:
    r.get_hosted_zone(Id="/hostedzone/ZMISSING")
    raise SystemExit("expected NoSuchHostedZone")
except r.exceptions.NoSuchHostedZone:
    pass
r.change_tags_for_resource(ResourceType="hostedzone", ResourceId=zid.split("/")[-1], AddTags=[{"Key": "k", "Value": "v"}])
assert r.list_tags_for_resource(ResourceType="hostedzone", ResourceId=zid.split("/")[-1])["ResourceTagSet"]["Tags"] == [{"Key": "k", "Value": "v"}]
print("ok")
`)
	if !strings.Contains(out, "ok") {
		t.Fatalf("boto3: %s", out)
	}
}

func TestAWSIAM(t *testing.T) {
	h, _ := setup(t)
	id := strings.TrimPrefix(h.AWSJSON(t, "route53", "create-hosted-zone", "--name", "a.example", "--caller-reference", "i1")["HostedZone"].(map[string]any)["Id"].(string), "/hostedzone/")
	id2 := strings.TrimPrefix(h.AWSJSON(t, "route53", "create-hosted-zone", "--name", "b.example", "--caller-reference", "i2")["HostedZone"].(map[string]any)["Id"].(string), "/hostedzone/")

	// Read-only.
	ro, rs := h.User(t, "ro", "ReadOnlyAccess")
	if o, err := h.AWSAs(t, ro, rs, "", "route53", "list-hosted-zones"); err != nil {
		t.Fatalf("read-only list: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, ro, rs, "", "route53", "get-hosted-zone", "--id", id); err != nil {
		t.Fatalf("read-only get: %v %s", err, o)
	}
	for _, args := range [][]string{
		{"route53", "create-hosted-zone", "--name", "c.example", "--caller-reference", "i3"},
		{"route53", "delete-hosted-zone", "--id", id},
		{"route53", "change-resource-record-sets", "--hosted-zone-id", id, "--change-batch", batchOf(change("CREATE", "x.a.example", "A", 60, "192.0.2.1"))},
		{"route53", "change-tags-for-resource", "--resource-type", "hostedzone", "--resource-id", id, "--add-tags", "Key=a,Value=b"},
		{"route53", "associate-vpc-with-hosted-zone", "--hosted-zone-id", id, "--vpc", "VPCRegion=us-east-1,VPCId=vpc-aaa"},
	} {
		if o, err := h.AWSAs(t, ro, rs, "", args...); err == nil || !strings.Contains(o, "AccessDenied") {
			t.Fatalf("%v as read-only: %v %s", args[1], err, o)
		}
	}

	// Scoped to one zone.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "zone-a", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{"Effect": "Allow", "Action": []string{"route53:ChangeResourceRecordSets", "route53:ListResourceRecordSets", "route53:GetChange", "route53:GetHostedZone"}, "Resource": "arn:aws:route53:::hostedzone/" + id},
			map[string]any{"Effect": "Allow", "Action": "route53:GetChange", "Resource": "arn:aws:route53:::change/*"},
		}}})
	ak, as := h.User(t, "zone-a", "zone-a")
	o, err := h.AWSAs(t, ak, as, "", "route53", "change-resource-record-sets", "--hosted-zone-id", id, "--change-batch", batchOf(change("CREATE", "x.a.example", "A", 60, "192.0.2.1")))
	if err != nil {
		t.Fatalf("scoped change: %v %s", err, o)
	}
	var ch map[string]any
	_ = json.Unmarshal([]byte(o), &ch)
	if o, err := h.AWSAs(t, ak, as, "", "route53", "get-change", "--id", ch["ChangeInfo"].(map[string]any)["Id"].(string)); err != nil {
		t.Fatalf("scoped get-change: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, ak, as, "", "route53", "change-resource-record-sets", "--hosted-zone-id", id2, "--change-batch", batchOf(change("CREATE", "x.b.example", "A", 60, "192.0.2.1"))); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("other zone: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, ak, as, "", "route53", "list-hosted-zones"); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("list: %v %s", err, o)
	}
	// Denials are audited with the zone ARN.
	found := false
	for _, a := range h.AuditLog() {
		if a == "route53:ChangeResourceRecordSets arn:aws:route53:::hostedzone/"+id2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit log %v", h.AuditLog())
	}
}

// ---- resolution: what CoreDNS is given must answer over DNS ----

// zoneAnswerer serves the rendered zone files over UDP DNS, the way CoreDNS's
// file plugin would, for the record types Route 53 records use.
type zoneAnswerer struct {
	rr map[string][]zrec // "name type" -> records
}

type zrec struct {
	typ  string
	ttl  uint32
	data string
}

func parseZone(text string) *zoneAnswerer {
	z := &zoneAnswerer{rr: map[string][]zrec{}}
	for _, line := range strings.Split(text, "\n") {
		f := strings.SplitN(line, " ", 5)
		if len(f) < 5 || f[2] != "IN" || strings.HasPrefix(line, "$") {
			continue
		}
		ttl, _ := strconv.Atoi(f[1])
		k := strings.ToLower(f[0]) + " " + f[3]
		z.rr[k] = append(z.rr[k], zrec{f[3], uint32(ttl), f[4]})
	}
	return z
}

func (z *zoneAnswerer) serve(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) != 1 {
				continue
			}
			q := m.Questions[0]
			name := strings.ToLower(q.Name.String())
			resp := dnsmessage.Message{Header: dnsmessage.Header{ID: m.ID, Response: true, Authoritative: true}, Questions: m.Questions}
			typ := map[dnsmessage.Type]string{dnsmessage.TypeA: "A", dnsmessage.TypeAAAA: "AAAA", dnsmessage.TypeCNAME: "CNAME", dnsmessage.TypeTXT: "TXT", dnsmessage.TypeMX: "MX"}[q.Type]
			for _, r := range z.rr[name+" "+typ] {
				h := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: r.ttl}
				switch typ {
				case "A":
					var a [4]byte
					copy(a[:], net.ParseIP(r.data).To4())
					resp.Answers = append(resp.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.AResource{A: a}})
				case "AAAA":
					var a [16]byte
					copy(a[:], net.ParseIP(r.data).To16())
					resp.Answers = append(resp.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.AAAAResource{AAAA: a}})
				case "CNAME":
					resp.Answers = append(resp.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(r.data)}})
				case "MX":
					var pref int
					var host string
					_, _ = fmtSscan(r.data, &pref, &host)
					resp.Answers = append(resp.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.MXResource{Pref: uint16(pref), MX: dnsmessage.MustNewName(host)}})
				case "TXT":
					var parts []string
					rest := r.data
					for rest != "" {
						i := 1
						for ; i < len(rest) && rest[i] != '"'; i++ {
							if rest[i] == '\\' {
								i++
							}
						}
						s, _ := strconv.Unquote(rest[:i+1])
						parts = append(parts, s)
						rest = strings.TrimLeft(rest[i+1:], " ")
					}
					resp.Answers = append(resp.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.TXTResource{TXT: parts}})
				}
			}
			if len(resp.Answers) == 0 {
				resp.RCode = dnsmessage.RCodeNameError
			}
			if b, err := resp.Pack(); err == nil {
				_, _ = pc.WriteTo(b, addr)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func fmtSscan(s string, pref *int, host *string) (int, error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, nil
	}
	*pref, _ = strconv.Atoi(f[0])
	*host = f[1]
	return 2, nil
}

func TestAWSRecordsResolveThroughDNS(t *testing.T) {
	h, s := setup(t)
	id := strings.TrimPrefix(h.AWSJSON(t, "route53", "create-hosted-zone", "--name", "lab.example", "--caller-reference", "d1")["HostedZone"].(map[string]any)["Id"].(string), "/hostedzone/")
	batch(t, h, id, batchOf(
		change("CREATE", "www.lab.example", "A", 60, "192.0.2.10"),
		change("CREATE", "v6.lab.example", "AAAA", 60, "2001:db8::10"),
		change("CREATE", "ftp.lab.example", "CNAME", 60, "www.lab.example."),
		change("CREATE", "lab.example", "MX", 60, "10 mail.lab.example."),
		change("CREATE", "_dmarc.lab.example", "TXT", 60, `"v=DMARC1; p=reject; rua=mailto:d@lab.example"`),
		change("CREATE", "long.lab.example", "TXT", 60, `"`+strings.Repeat("a", 255)+`" "`+strings.Repeat("b", 100)+`"`),
		`{"Action":"CREATE","ResourceRecordSet":{"Name":"app.lab.example","Type":"A","AliasTarget":{"HostedZoneId":"Z35SXDOTRQ7X7K","DNSName":"web.elb.internal.","EvaluateTargetHealth":true}}}`,
		`{"Action":"CREATE","ResourceRecordSet":{"Name":"gone.lab.example","Type":"A","AliasTarget":{"HostedZoneId":"Z35SXDOTRQ7X7K","DNSName":"missing.elb.internal","EvaluateTargetHealth":true}}}`,
	))
	files := s.render()
	zf := files["zones/"+id+".db"]
	if !strings.Contains(files["Corefile"], "lab.example. {") || zf == "" {
		t.Fatalf("zone not configured for CoreDNS:\n%s", files["Corefile"])
	}
	addr := parseZone(zf).serve(t)
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", addr)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ips, err := r.LookupIP(ctx, "ip4", "www.lab.example"); err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.10" {
		t.Fatalf("A: %v %v", ips, err)
	}
	if ips, err := r.LookupIP(ctx, "ip6", "v6.lab.example"); err != nil || len(ips) != 1 || ips[0].String() != "2001:db8::10" {
		t.Fatalf("AAAA: %v %v", ips, err)
	}
	if cn, err := r.LookupCNAME(ctx, "ftp.lab.example"); err != nil || cn != "www.lab.example." {
		t.Fatalf("CNAME: %v %v", cn, err)
	}
	if mx, err := r.LookupMX(ctx, "lab.example"); err != nil || len(mx) != 1 || mx[0].Host != "mail.lab.example." || mx[0].Pref != 10 {
		t.Fatalf("MX: %v %v", mx, err)
	}
	if tx, err := r.LookupTXT(ctx, "_dmarc.lab.example"); err != nil || len(tx) != 1 || tx[0] != "v=DMARC1; p=reject; rua=mailto:d@lab.example" {
		t.Fatalf("TXT with semicolons: %v %v", tx, err)
	}
	if tx, err := r.LookupTXT(ctx, "long.lab.example"); err != nil || len(tx) != 1 || tx[0] != strings.Repeat("a", 255)+strings.Repeat("b", 100) {
		t.Fatalf("long TXT: %d %v", len(tx), err)
	}
	if ips, err := r.LookupIP(ctx, "ip4", "app.lab.example"); err != nil || len(ips) != 1 || ips[0].String() != "10.88.0.7" {
		t.Fatalf("alias to load balancer follows its address: %v %v", ips, err)
	}
	if _, err := r.LookupIP(ctx, "ip4", "gone.lab.example"); err == nil {
		t.Fatal("alias to a missing load balancer should not answer")
	}
	// A deleted record stops resolving after the next render.
	batch(t, h, id, batchOf(change("DELETE", "www.lab.example", "A", 60, "192.0.2.10")))
	addr = parseZone(s.render()["zones/"+id+".db"]).serve(t)
	if _, err := r.LookupIP(ctx, "ip4", "www.lab.example"); err == nil {
		t.Fatal("deleted record still resolves")
	}
}
