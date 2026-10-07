package ec2

import (
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Network ACLs (EC2 API) are records: HomeCloud filters traffic with security
// groups only, so ACL entries are stored and reported, not enforced. Every VPC
// has a default ACL (acl-<vpc suffix>) allowing all traffic, which every
// subnet uses until it is associated with another ACL of its VPC.

const cNetworkACLs = "ec2_network_acls"

type ACLEntry struct {
	RuleNumber int    `json:"rule_number"`
	Protocol   string `json:"protocol"`
	Action     string `json:"action"`
	Egress     bool   `json:"egress"`
	CIDR       string `json:"cidr,omitempty"`
	IPv6CIDR   string `json:"ipv6_cidr,omitempty"`
	FromPort   *int   `json:"from_port,omitempty"`
	ToPort     *int   `json:"to_port,omitempty"`
	ICMPType   *int   `json:"icmp_type,omitempty"`
	ICMPCode   *int   `json:"icmp_code,omitempty"`
}

type NetworkACL struct {
	ID      string     `json:"id"`
	VpcID   string     `json:"vpc_id"`
	Default bool       `json:"default,omitempty"`
	Entries []ACLEntry `json:"entries"`
	// Associations maps subnet IDs to association IDs (custom ACLs only;
	// subnets not listed anywhere use their VPC's default ACL).
	Associations map[string]string `json:"associations,omitempty"`
	Tags         core.Tags         `json:"tags,omitempty"`
}

var aclMu sync.Mutex

func aclNotFound(id string) error {
	return core.Errf(http.StatusBadRequest, "InvalidNetworkAclID.NotFound", "The networkAcl ID '%s' does not exist", id)
}

func defaultACLID(vpcID string) string { return "acl-" + strings.TrimPrefix(vpcID, "vpc-") }

func defaultACLEntries() []ACLEntry {
	var out []ACLEntry
	for _, egress := range []bool{false, true} {
		out = append(out, ACLEntry{RuleNumber: 100, Protocol: "-1", Action: "allow", Egress: egress, CIDR: "0.0.0.0/0"},
			ACLEntry{RuleNumber: 32767, Protocol: "-1", Action: "deny", Egress: egress, CIDR: "0.0.0.0/0"})
	}
	return out
}

// acls returns every live VPC's default ACL (made on first sight) and the
// custom ACLs; ACLs of deleted VPCs are dropped.
func (s *Service) acls() []NetworkACL {
	vpcs := map[string]bool{}
	for _, v := range s.vpc.List() {
		vpcs[v.ID] = true
	}
	have := map[string]bool{}
	var out []NetworkACL
	for _, a := range store.List[NetworkACL](s.env.Store, cNetworkACLs) {
		if !vpcs[a.VpcID] {
			_ = store.Delete(s.env.Store, cNetworkACLs, a.ID)
			continue
		}
		have[a.ID] = true
		out = append(out, a)
	}
	for _, v := range s.vpc.List() {
		if id := defaultACLID(v.ID); !have[id] {
			a := NetworkACL{ID: id, VpcID: v.ID, Default: true, Entries: defaultACLEntries()}
			_ = store.Put(s.env.Store, cNetworkACLs, a.ID, a)
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b NetworkACL) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (s *Service) getACL(id string) (NetworkACL, error) {
	for _, a := range s.acls() {
		if a.ID == id {
			return a, nil
		}
	}
	return NetworkACL{}, aclNotFound(id)
}

// aclAssociations lists the (subnet, association ID) pairs an ACL holds,
// including the default ACL's implicit ones.
func (s *Service) aclAssociations(a NetworkACL, all []NetworkACL) [][2]string {
	var out [][2]string
	if !a.Default {
		for sn, id := range a.Associations {
			out = append(out, [2]string{sn, id})
		}
	} else {
		taken := map[string]bool{}
		for _, o := range all {
			for sn := range o.Associations {
				taken[sn] = true
			}
		}
		for _, sn := range s.vpc.Subnets() {
			if sn.VpcID == a.VpcID && !taken[sn.ID] {
				out = append(out, [2]string{sn.ID, "aclassoc-" + strings.TrimPrefix(sn.ID, "subnet-")})
			}
		}
	}
	slices.SortFunc(out, func(x, y [2]string) int { return strings.Compare(x[0], y[0]) })
	return out
}

func (s *Service) aclXML(a NetworkACL, all []NetworkACL) map[string]any {
	entries := awsapi.Items{}
	for _, e := range a.Entries {
		m := map[string]any{"ruleNumber": e.RuleNumber, "protocol": e.Protocol, "ruleAction": e.Action, "egress": e.Egress}
		if e.CIDR != "" {
			m["cidrBlock"] = e.CIDR
		}
		if e.IPv6CIDR != "" {
			m["ipv6CidrBlock"] = e.IPv6CIDR
		}
		if e.FromPort != nil && e.ToPort != nil {
			m["portRange"] = map[string]any{"from": *e.FromPort, "to": *e.ToPort}
		}
		if e.ICMPType != nil && e.ICMPCode != nil {
			m["icmpTypeCode"] = map[string]any{"type": *e.ICMPType, "code": *e.ICMPCode}
		}
		entries = append(entries, m)
	}
	assocs := awsapi.Items{}
	for _, p := range s.aclAssociations(a, all) {
		assocs = append(assocs, map[string]any{"networkAclAssociationId": p[1], "networkAclId": a.ID, "subnetId": p[0]})
	}
	return map[string]any{"networkAclId": a.ID, "vpcId": a.VpcID, "default": a.Default, "ownerId": s.env.AccountID,
		"entrySet": entries, "associationSet": assocs, "tagSet": tagSet(a.Tags)}
}

func (s *Service) awsDescribeNetworkAcls(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeNetworkAcls", "*"); err != nil {
		return nil, err
	}
	ids, fs := q.List("NetworkAclId"), filters(q)
	aclMu.Lock()
	all := s.acls()
	aclMu.Unlock()
	for _, id := range ids {
		if !slices.ContainsFunc(all, func(a NetworkACL) bool { return a.ID == id }) {
			return nil, aclNotFound(id)
		}
	}
	want := idSet(ids)
	items := awsapi.Items{}
	for _, a := range all {
		at := attrs{}.set("network-acl-id", a.ID).set("vpc-id", a.VpcID).set("default", strconv.FormatBool(a.Default)).
			set("owner-id", s.env.AccountID).set("association.subnet-id").set("association.association-id").set("association.network-acl-id", a.ID).tags(a.Tags)
		for _, p := range s.aclAssociations(a, all) {
			at.set("association.subnet-id", p[0]).set("association.association-id", p[1])
		}
		for _, e := range a.Entries {
			at.set("entry.rule-number", strconv.Itoa(e.RuleNumber)).set("entry.rule-action", e.Action).set("entry.protocol", e.Protocol).
				set("entry.egress", strconv.FormatBool(e.Egress))
			if e.CIDR != "" {
				at.set("entry.cidr", e.CIDR)
			}
		}
		if (len(ids) == 0 || want[a.ID]) && match(fs, at) {
			items = append(items, s.aclXML(a, all))
		}
	}
	return map[string]any{"networkAclSet": items}, nil
}

func (s *Service) awsCreateNetworkAcl(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:CreateNetworkAcl", q.ARN("ec2", "network-acl/*")); err != nil {
		return nil, err
	}
	vpcID := q.Param("VpcId")
	if _, err := s.vpc.GetVPC(vpcID); err != nil {
		return nil, vpcNotFound(vpcID)
	}
	tags := tagSpecs(q, "network-acl")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	// A new ACL denies everything until entries are added, as in AWS.
	var entries []ACLEntry
	for _, egress := range []bool{false, true} {
		entries = append(entries, ACLEntry{RuleNumber: 32767, Protocol: "-1", Action: "deny", Egress: egress, CIDR: "0.0.0.0/0"})
	}
	a := NetworkACL{ID: core.NewID("acl"), VpcID: vpcID, Entries: entries, Tags: tags}
	aclMu.Lock()
	defer aclMu.Unlock()
	if err := store.Put(s.env.Store, cNetworkACLs, a.ID, a); err != nil {
		return nil, err
	}
	return map[string]any{"networkAcl": s.aclXML(a, s.acls()), "clientToken": q.Param("ClientToken")}, nil
}

func (s *Service) awsDeleteNetworkAcl(q *awsapi.Req) (any, error) {
	id := q.Param("NetworkAclId")
	if err := q.Authorize("ec2:DeleteNetworkAcl", q.ARN("ec2", "network-acl/"+id)); err != nil {
		return nil, err
	}
	aclMu.Lock()
	defer aclMu.Unlock()
	a, err := s.getACL(id)
	if err != nil {
		return nil, err
	}
	if a.Default {
		return nil, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "cannot delete default network ACL %s", id)
	}
	if len(a.Associations) > 0 {
		return nil, core.Errf(http.StatusBadRequest, "DependencyViolation", "The networkAcl '%s' has dependencies and cannot be deleted.", id)
	}
	return nil, store.Delete(s.env.Store, cNetworkACLs, id)
}

func (s *Service) awsReplaceNetworkAclAssociation(q *awsapi.Req) (any, error) {
	assoc, id := q.Param("AssociationId"), q.Param("NetworkAclId")
	if err := q.Authorize("ec2:ReplaceNetworkAclAssociation", q.ARN("ec2", "network-acl/"+id)); err != nil {
		return nil, err
	}
	aclMu.Lock()
	defer aclMu.Unlock()
	all := s.acls()
	target, err := s.getACL(id)
	if err != nil {
		return nil, err
	}
	subnet := ""
	var from *NetworkACL
	for i := range all {
		for _, p := range s.aclAssociations(all[i], all) {
			if p[1] == assoc {
				subnet, from = p[0], &all[i]
			}
		}
	}
	if from == nil {
		return nil, core.Errf(http.StatusBadRequest, "InvalidAssociationID.NotFound", "The association ID '%s' does not exist", assoc)
	}
	if from.VpcID != target.VpcID {
		return nil, invalid("network ACL %s and subnet %s belong to different networks", id, subnet)
	}
	if !from.Default {
		delete(from.Associations, subnet)
		if err := store.Put(s.env.Store, cNetworkACLs, from.ID, *from); err != nil {
			return nil, err
		}
	}
	newID := "aclassoc-" + strings.TrimPrefix(subnet, "subnet-")
	if !target.Default {
		newID = core.NewID("aclassoc")
		if target.Associations == nil {
			target.Associations = map[string]string{}
		}
		target.Associations[subnet] = newID
		if err := store.Put(s.env.Store, cNetworkACLs, target.ID, target); err != nil {
			return nil, err
		}
	}
	return map[string]any{"newAssociationId": newID}, nil
}

// aclEntryArg reads an entry from Create/ReplaceNetworkAclEntry.
func aclEntryArg(q *awsapi.Req) (ACLEntry, error) {
	e := ACLEntry{Protocol: strings.ToLower(q.Param("Protocol")), Action: strings.ToLower(q.Param("RuleAction")),
		Egress: q.Param("Egress") == "true", CIDR: q.Param("CidrBlock"), IPv6CIDR: q.Param("Ipv6CidrBlock")}
	n, err := strconv.Atoi(q.Param("RuleNumber"))
	if err != nil || n < 1 || n > 32766 {
		return e, invalid("Invalid value '%s' for RuleNumber: must be 1-32766", q.Param("RuleNumber"))
	}
	e.RuleNumber = n
	switch e.Protocol {
	case "all":
		e.Protocol = "-1"
	case "tcp":
		e.Protocol = "6"
	case "udp":
		e.Protocol = "17"
	case "icmp":
		e.Protocol = "1"
	}
	if p, err := strconv.Atoi(e.Protocol); err != nil || p < -1 || p > 255 {
		return e, invalid("Invalid value '%s' for Protocol", q.Param("Protocol"))
	}
	if e.Action != "allow" && e.Action != "deny" {
		return e, invalid("Invalid value '%s' for RuleAction: must be allow or deny", q.Param("RuleAction"))
	}
	if (e.CIDR == "") == (e.IPv6CIDR == "") {
		return e, invalid("Exactly one of CidrBlock and Ipv6CidrBlock is required")
	}
	for _, c := range []string{e.CIDR, e.IPv6CIDR} {
		if c != "" {
			if _, err := netip.ParsePrefix(c); err != nil {
				return e, invalid("Invalid CIDR block '%s'", c)
			}
		}
	}
	num := func(name string) *int {
		if v, err := strconv.Atoi(q.Param(name)); err == nil {
			return &v
		}
		return nil
	}
	if e.Protocol == "6" || e.Protocol == "17" {
		e.FromPort, e.ToPort = num("PortRange.From"), num("PortRange.To")
		if e.FromPort == nil || e.ToPort == nil {
			return e, core.Errf(http.StatusBadRequest, "MissingParameter", "TCP and UDP entries need a PortRange")
		}
	}
	if e.Protocol == "1" || e.Protocol == "58" {
		e.ICMPType, e.ICMPCode = num("Icmp.Type"), num("Icmp.Code")
	}
	return e, nil
}

func (s *Service) aclEntryOp(replace bool) ec2Op {
	action := "ec2:CreateNetworkAclEntry"
	if replace {
		action = "ec2:ReplaceNetworkAclEntry"
	}
	return func(q *awsapi.Req) (any, error) {
		id := q.Param("NetworkAclId")
		if err := q.Authorize(action, q.ARN("ec2", "network-acl/"+id)); err != nil {
			return nil, err
		}
		e, err := aclEntryArg(q)
		if err != nil {
			return nil, err
		}
		aclMu.Lock()
		defer aclMu.Unlock()
		a, err := s.getACL(id)
		if err != nil {
			return nil, err
		}
		n := slices.IndexFunc(a.Entries, func(o ACLEntry) bool { return o.RuleNumber == e.RuleNumber && o.Egress == e.Egress })
		switch {
		case replace && n < 0:
			return nil, core.Errf(http.StatusBadRequest, "InvalidNetworkAclEntry.NotFound", "The network acl entry identified by %d does not exist", e.RuleNumber)
		case replace:
			a.Entries[n] = e
		case n >= 0:
			return nil, core.Errf(http.StatusBadRequest, "NetworkAclEntryAlreadyExists", "The network acl entry identified by %d already exists.", e.RuleNumber)
		default:
			a.Entries = append(a.Entries, e)
		}
		slices.SortStableFunc(a.Entries, func(x, y ACLEntry) int { return x.RuleNumber - y.RuleNumber })
		return nil, store.Put(s.env.Store, cNetworkACLs, a.ID, a)
	}
}

func (s *Service) awsDeleteNetworkAclEntry(q *awsapi.Req) (any, error) {
	id := q.Param("NetworkAclId")
	if err := q.Authorize("ec2:DeleteNetworkAclEntry", q.ARN("ec2", "network-acl/"+id)); err != nil {
		return nil, err
	}
	rule, _ := strconv.Atoi(q.Param("RuleNumber"))
	egress := q.Param("Egress") == "true"
	aclMu.Lock()
	defer aclMu.Unlock()
	a, err := s.getACL(id)
	if err != nil {
		return nil, err
	}
	n := slices.IndexFunc(a.Entries, func(o ACLEntry) bool { return o.RuleNumber == rule && o.Egress == egress })
	if n < 0 || rule == 32767 {
		return nil, core.Errf(http.StatusBadRequest, "InvalidNetworkAclEntry.NotFound", "The network acl entry identified by %d does not exist", rule)
	}
	a.Entries = slices.Delete(a.Entries, n, n+1)
	return nil, store.Put(s.env.Store, cNetworkACLs, a.ID, a)
}

// setACLTags changes an ACL's tags (CreateTags/DeleteTags).
func (s *Service) setACLTags(id string, fn func(t *core.Tags)) error {
	aclMu.Lock()
	defer aclMu.Unlock()
	a, err := s.getACL(id)
	if err != nil {
		return err
	}
	fn(&a.Tags)
	return store.Put(s.env.Store, cNetworkACLs, a.ID, a)
}
