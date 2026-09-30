package route53

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The Amazon Route 53 API (restXml, /2013-04-01/): hosted zones, resource
// record sets and their changes, VPC associations and tags. Records are served
// by HomeCloud's DNS (CoreDNS), so what you create here resolves.
//
// Differences from AWS: changes are applied atomically and are INSYNC at once
// (CoreDNS reloads its zone files within a few seconds); routing policies
// (weighted, latency, failover, geolocation), health checks, DNSSEC and traffic
// policies are not supported; alias records target HomeCloud load balancers
// (their DNS name, "<name>.elb.internal").

const (
	r53NS     = "https://route53.amazonaws.com/doc/2013-04-01/"
	cChanges  = "route53_changes"
	elbSuffix = ".elb.internal."
	elbZoneID = "Z35SXDOTRQ7X7K" // CanonicalHostedZoneId of a HomeCloud load balancer
)

// Change is the record of a submitted change batch (GetChange).
type changeInfo struct {
	ID          string    `json:"id"`
	SubmittedAt time.Time `json:"submitted_at"`
	Comment     string    `json:"comment,omitempty"`
}

// RegisterAWS serves Route 53 over the AWS protocol.
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "route53", REST: s.awsREST, RESTError: awsError,
		ErrorCode: map[string]string{
			"ResourceNotFound": "NoSuchHostedZone", "BadRequest": "InvalidInput", "ValidationError": "InvalidInput",
			"Conflict": "PriorRequestNotComplete", "AccessDenied": "AccessDenied", "InternalError": "ServiceUnavailable",
		},
	})
}

func awsError(q *awsapi.Req, e *awsapi.Error) {
	typ := "Sender"
	if e.Status >= 500 {
		typ = "Receiver"
	}
	var b strings.Builder
	b.WriteString(xml.Header)
	// Batch errors use the same shape (AWS's own InvalidChangeBatch document is
	// not understood by every SDK's error parser).
	fmt.Fprintf(&b, `<ErrorResponse xmlns="%s"><Error><Type>%s</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		r53NS, typ, esc(e.Code), esc(e.Message), q.RequestID)
	q.W.Header().Set("Content-Type", "text/xml")
	q.W.WriteHeader(e.Status)
	_, _ = q.W.Write([]byte(b.String()))
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func invalid(format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, "InvalidInput", format, a...)
}

func hostedZoneARN(id string) string {
	return fmt.Sprintf("arn:%s:route53:::hostedzone/%s", core.Partition, id)
}
func changeARN(id string) string {
	return fmt.Sprintf("arn:%s:route53:::change/%s", core.Partition, id)
}

// ---- routing ----

func (s *Service) awsREST(q *awsapi.Req) {
	out, status, err := s.route(q)
	if err != nil {
		q.Fail(err)
		return
	}
	if out == nil {
		q.W.WriteHeader(status)
		return
	}
	b, err := xml.Marshal(out)
	if err != nil {
		q.Fail(err)
		return
	}
	q.W.Header().Set("Content-Type", "text/xml")
	q.W.WriteHeader(status)
	_, _ = q.W.Write(append([]byte(xml.Header), b...))
}

func (s *Service) route(q *awsapi.Req) (any, int, error) {
	path := strings.Trim(q.R.URL.Path, "/")
	rest, ok := strings.CutPrefix(path, "2013-04-01")
	if !ok {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "InvalidInput", "unsupported API version in %q", q.R.URL.Path)
	}
	seg := strings.Split(strings.Trim(rest, "/"), "/")
	m, qs := q.R.Method, q.R.URL.Query()
	unsupported := func() (any, int, error) {
		return nil, 0, awsapi.Errorf(http.StatusNotImplemented, "NotImplemented", "HomeCloud does not implement Route 53 %s %s", m, q.R.URL.Path)
	}
	switch seg[0] {
	case "hostedzone":
		switch {
		case len(seg) == 1 && m == http.MethodPost:
			return s.awsCreateZone(q)
		case len(seg) == 1 && m == http.MethodGet:
			return s.awsListZones(q, qs)
		case len(seg) == 2 && m == http.MethodGet:
			return s.awsGetZone(q, seg[1])
		case len(seg) == 2 && m == http.MethodDelete:
			return s.awsDeleteZone(q, seg[1])
		case len(seg) == 2 && m == http.MethodPost:
			return s.awsUpdateComment(q, seg[1])
		case len(seg) == 3 && seg[2] == "rrset" && m == http.MethodPost:
			return s.awsChangeRecords(q, seg[1])
		case len(seg) == 3 && seg[2] == "rrset" && m == http.MethodGet:
			return s.awsListRecords(q, seg[1], qs)
		case len(seg) == 3 && seg[2] == "associatevpc" && m == http.MethodPost:
			return s.awsAssociate(q, seg[1], true)
		case len(seg) == 3 && seg[2] == "disassociatevpc" && m == http.MethodPost:
			return s.awsAssociate(q, seg[1], false)
		case len(seg) == 3 && seg[2] == "dnssec" && m == http.MethodGet:
			return s.awsDNSSEC(q, seg[1])
		}
	case "hostedzonesbyname":
		if len(seg) == 1 && m == http.MethodGet {
			return s.awsListZonesByName(q, qs)
		}
	case "hostedzonesbyvpc":
		if len(seg) == 1 && m == http.MethodGet {
			return s.awsListZonesByVPC(q, qs)
		}
	case "hostedzonecount":
		if len(seg) == 1 && m == http.MethodGet {
			if err := q.Authorize("route53:GetHostedZoneCount", "*"); err != nil {
				return nil, 0, err
			}
			n := len(store.List[Zone](s.env.Store, cZones))
			return struct {
				XMLName         xml.Name `xml:"GetHostedZoneCountResponse"`
				Xmlns           string   `xml:"xmlns,attr"`
				HostedZoneCount int      `xml:"HostedZoneCount"`
			}{Xmlns: r53NS, HostedZoneCount: n}, http.StatusOK, nil
		}
	case "change":
		if len(seg) == 2 && m == http.MethodGet {
			return s.awsGetChange(q, seg[1])
		}
	case "tags":
		if len(seg) == 3 && seg[1] == "hostedzone" {
			switch m {
			case http.MethodGet:
				return s.awsListTags(q, seg[2])
			case http.MethodPost:
				return s.awsChangeTags(q, seg[2])
			}
		}
	}
	return unsupported()
}

// ---- shapes ----

type xConfig struct {
	Comment     string `xml:"Comment,omitempty"`
	PrivateZone bool   `xml:"PrivateZone"`
}

type xZone struct {
	ID                     string  `xml:"Id"`
	Name                   string  `xml:"Name"`
	CallerReference        string  `xml:"CallerReference"`
	Config                 xConfig `xml:"Config"`
	ResourceRecordSetCount int     `xml:"ResourceRecordSetCount"`
}

type xChangeInfo struct {
	ID          string `xml:"Id"`
	Status      string `xml:"Status"`
	SubmittedAt string `xml:"SubmittedAt"`
	Comment     string `xml:"Comment,omitempty"`
}

type xVPC struct {
	VPCRegion string `xml:"VPCRegion,omitempty"`
	VPCId     string `xml:"VPCId,omitempty"`
}

type xDelegation struct {
	NameServers struct {
		NameServer []string `xml:"NameServer"`
	} `xml:"NameServers"`
}

func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func zoneID(ref string) string { return strings.TrimPrefix(ref, "/hostedzone/") }

func (s *Service) xzone(z Zone) xZone {
	return xZone{ID: "/hostedzone/" + z.ID, Name: z.Name, CallerReference: z.CallerRef,
		Config: xConfig{Comment: z.Comment, PrivateZone: z.Private}, ResourceRecordSetCount: len(s.allRecords(z))}
}

func (s *Service) delegation(z Zone) *xDelegation {
	if z.Private {
		return nil
	}
	d := &xDelegation{}
	d.NameServers.NameServer = []string{"ns." + z.Name}
	return d
}

func (s *Service) newChange(comment string) (xChangeInfo, error) {
	c := changeInfo{ID: "C" + strings.ToUpper(core.RandHex(13)), SubmittedAt: core.Now(), Comment: comment}
	if err := store.Put(s.env.Store, cChanges, c.ID, c); err != nil {
		return xChangeInfo{}, err
	}
	return xChangeInfo{ID: "/change/" + c.ID, Status: "INSYNC", SubmittedAt: isoTime(c.SubmittedAt), Comment: c.Comment}, nil
}

func (s *Service) awsZone(id string) (Zone, error) {
	z, err := s.zone(zoneID(id))
	if err != nil {
		return z, awsapi.Errorf(http.StatusNotFound, "NoSuchHostedZone", "No hosted zone found with ID: %s", zoneID(id))
	}
	return z, nil
}

// vpcsOf lists the VPCs a private zone answers in (all when none are recorded).
func (s *Service) vpcsOf(z Zone) []string {
	if !z.Private {
		return nil
	}
	if len(z.VpcIDs) > 0 {
		return z.VpcIDs
	}
	var ids []string
	for _, v := range s.vpc.List() {
		ids = append(ids, v.ID)
	}
	return ids
}

func (s *Service) xvpcs(z Zone) []xVPC {
	var out []xVPC
	for _, id := range s.vpcsOf(z) {
		out = append(out, xVPC{VPCRegion: core.Region, VPCId: id})
	}
	return out
}

// ---- hosted zones ----

func (s *Service) awsCreateZone(q *awsapi.Req) (any, int, error) {
	if err := q.Authorize("route53:CreateHostedZone", "*"); err != nil {
		return nil, 0, err
	}
	var in struct {
		Name            string  `xml:"Name"`
		CallerReference string  `xml:"CallerReference"`
		VPC             *xVPC   `xml:"VPC"`
		Config          xConfig `xml:"HostedZoneConfig"`
	}
	if err := xml.Unmarshal(q.Body, &in); err != nil {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed: %v", err)
	}
	if in.Name == "" || in.CallerReference == "" {
		return nil, 0, invalid("Name and CallerReference are required")
	}
	private := in.VPC != nil && in.VPC.VPCId != ""
	if in.Config.PrivateZone && !private {
		return nil, 0, invalid("A private hosted zone needs a VPC")
	}
	for _, z := range store.List[Zone](s.env.Store, cZones) {
		if z.CallerRef == in.CallerReference {
			return nil, 0, awsapi.Errorf(http.StatusBadRequest, "HostedZoneAlreadyExists", "A hosted zone has already been created with the specified caller reference.")
		}
	}
	zi := zoneInput{Name: in.Name, Private: private, Comment: in.Config.Comment, CallerRef: in.CallerReference}
	if private {
		zi.VpcIDs = []string{in.VPC.VPCId}
		if _, err := s.vpc.GetVPC(in.VPC.VPCId); err != nil {
			return nil, 0, awsapi.Errorf(http.StatusBadRequest, "InvalidVPCId", "The VPC ID %q is not valid.", in.VPC.VPCId)
		}
	}
	z, err := s.createZone(zi)
	if err != nil {
		return nil, 0, err
	}
	ci, err := s.newChange("")
	if err != nil {
		return nil, 0, err
	}
	q.W.Header().Set("Location", "https://route53.amazonaws.com/2013-04-01/hostedzone/"+z.ID)
	out := struct {
		XMLName       xml.Name     `xml:"CreateHostedZoneResponse"`
		Xmlns         string       `xml:"xmlns,attr"`
		HostedZone    xZone        `xml:"HostedZone"`
		ChangeInfo    xChangeInfo  `xml:"ChangeInfo"`
		DelegationSet *xDelegation `xml:"DelegationSet"`
		VPC           *xVPC        `xml:"VPC"`
	}{Xmlns: r53NS, HostedZone: s.xzone(z), ChangeInfo: ci, DelegationSet: s.delegation(z)}
	if private {
		out.VPC = &xVPC{VPCRegion: core.Region, VPCId: in.VPC.VPCId}
	}
	return out, http.StatusCreated, nil
}

func (s *Service) awsGetZone(q *awsapi.Req, id string) (any, int, error) {
	id = zoneID(id)
	if err := q.Authorize("route53:GetHostedZone", hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	z, err := s.awsZone(id)
	if err != nil {
		return nil, 0, err
	}
	out := struct {
		XMLName       xml.Name     `xml:"GetHostedZoneResponse"`
		Xmlns         string       `xml:"xmlns,attr"`
		HostedZone    xZone        `xml:"HostedZone"`
		DelegationSet *xDelegation `xml:"DelegationSet"`
		VPCs          *struct {
			VPC []xVPC `xml:"VPC"`
		} `xml:"VPCs"`
	}{Xmlns: r53NS, HostedZone: s.xzone(z), DelegationSet: s.delegation(z)}
	if z.Private {
		out.VPCs = &struct {
			VPC []xVPC `xml:"VPC"`
		}{VPC: s.xvpcs(z)}
	}
	return out, http.StatusOK, nil
}

func (s *Service) awsUpdateComment(q *awsapi.Req, id string) (any, int, error) {
	id = zoneID(id)
	if err := q.Authorize("route53:UpdateHostedZoneComment", hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	var in struct {
		Comment string `xml:"Comment"`
	}
	if err := xml.Unmarshal(q.Body, &in); err != nil {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed: %v", err)
	}
	z, err := store.Update(s.env.Store, cZones, id, func(z *Zone) error { z.Comment = in.Comment; return nil })
	if errors.Is(err, store.ErrNotFound) {
		return nil, 0, awsapi.Errorf(http.StatusNotFound, "NoSuchHostedZone", "No hosted zone found with ID: %s", id)
	}
	if err != nil {
		return nil, 0, err
	}
	return struct {
		XMLName    xml.Name `xml:"UpdateHostedZoneCommentResponse"`
		Xmlns      string   `xml:"xmlns,attr"`
		HostedZone xZone    `xml:"HostedZone"`
	}{Xmlns: r53NS, HostedZone: s.xzone(z)}, http.StatusOK, nil
}

func (s *Service) awsDeleteZone(q *awsapi.Req, id string) (any, int, error) {
	id = zoneID(id)
	if err := q.Authorize("route53:DeleteHostedZone", hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	z, err := s.awsZone(id)
	if err != nil {
		return nil, 0, err
	}
	if len(z.Records) > 0 {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "HostedZoneNotEmpty", "The specified hosted zone contains non-required resource record sets and so cannot be deleted.")
	}
	if err := s.deleteZone(id, false); err != nil {
		return nil, 0, err
	}
	ci, err := s.newChange("")
	if err != nil {
		return nil, 0, err
	}
	return struct {
		XMLName    xml.Name    `xml:"DeleteHostedZoneResponse"`
		Xmlns      string      `xml:"xmlns,attr"`
		ChangeInfo xChangeInfo `xml:"ChangeInfo"`
	}{Xmlns: r53NS, ChangeInfo: ci}, http.StatusOK, nil
}

func atoi(v string, def int) int {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return def
}

type xZoneList struct {
	HostedZone []xZone `xml:"HostedZone"`
}

func (s *Service) awsListZones(q *awsapi.Req, qs url.Values) (any, int, error) {
	if err := q.Authorize("route53:ListHostedZones", "*"); err != nil {
		return nil, 0, err
	}
	zones := store.List[Zone](s.env.Store, cZones)
	sort.Slice(zones, func(i, j int) bool { return zones[i].ID < zones[j].ID })
	marker := zoneID(qs.Get("marker"))
	max := min(atoi(qs.Get("maxitems"), 100), 100)
	var page []Zone
	truncated, next := false, ""
	for _, z := range zones {
		if marker != "" && z.ID < marker {
			continue
		}
		if len(page) == max {
			truncated, next = true, z.ID
			break
		}
		page = append(page, z)
	}
	out := struct {
		XMLName     xml.Name  `xml:"ListHostedZonesResponse"`
		Xmlns       string    `xml:"xmlns,attr"`
		HostedZones xZoneList `xml:"HostedZones"`
		Marker      string    `xml:"Marker,omitempty"`
		IsTruncated bool      `xml:"IsTruncated"`
		NextMarker  string    `xml:"NextMarker,omitempty"`
		MaxItems    int       `xml:"MaxItems"`
	}{Xmlns: r53NS, Marker: qs.Get("marker"), IsTruncated: truncated, NextMarker: next, MaxItems: max}
	for _, z := range page {
		out.HostedZones.HostedZone = append(out.HostedZones.HostedZone, s.xzone(z))
	}
	return out, http.StatusOK, nil
}

// nameKey orders domain names like Route 53: by reversed labels.
func nameKey(name string) string {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(escapeName(name)), "."), ".")
	slices.Reverse(labels)
	return strings.Join(labels, ".")
}

func (s *Service) awsListZonesByName(q *awsapi.Req, qs url.Values) (any, int, error) {
	if err := q.Authorize("route53:ListHostedZonesByName", "*"); err != nil {
		return nil, 0, err
	}
	zones := store.List[Zone](s.env.Store, cZones)
	sort.Slice(zones, func(i, j int) bool {
		a, b := nameKey(zones[i].Name), nameKey(zones[j].Name)
		if a != b {
			return a < b
		}
		return zones[i].ID < zones[j].ID
	})
	dns := strings.ToLower(qs.Get("dnsname"))
	if dns != "" && !strings.HasSuffix(dns, ".") {
		dns += "."
	}
	startID := zoneID(qs.Get("hostedzoneid"))
	max := min(atoi(qs.Get("maxitems"), 100), 100)
	var page []Zone
	truncated := false
	var nextName, nextID string
	for _, z := range zones {
		if dns != "" {
			k := nameKey(z.Name)
			if k < nameKey(dns) || (k == nameKey(dns) && startID != "" && z.ID < startID) {
				continue
			}
		}
		if len(page) == max {
			truncated, nextName, nextID = true, z.Name, z.ID
			break
		}
		page = append(page, z)
	}
	out := struct {
		XMLName          xml.Name  `xml:"ListHostedZonesByNameResponse"`
		Xmlns            string    `xml:"xmlns,attr"`
		HostedZones      xZoneList `xml:"HostedZones"`
		DNSName          string    `xml:"DNSName,omitempty"`
		HostedZoneId     string    `xml:"HostedZoneId,omitempty"`
		IsTruncated      bool      `xml:"IsTruncated"`
		NextDNSName      string    `xml:"NextDNSName,omitempty"`
		NextHostedZoneId string    `xml:"NextHostedZoneId,omitempty"`
		MaxItems         int       `xml:"MaxItems"`
	}{Xmlns: r53NS, DNSName: qs.Get("dnsname"), HostedZoneId: qs.Get("hostedzoneid"), IsTruncated: truncated, NextDNSName: nextName, NextHostedZoneId: nextID, MaxItems: max}
	for _, z := range page {
		out.HostedZones.HostedZone = append(out.HostedZones.HostedZone, s.xzone(z))
	}
	return out, http.StatusOK, nil
}

func (s *Service) awsListZonesByVPC(q *awsapi.Req, qs url.Values) (any, int, error) {
	if err := q.Authorize("route53:ListHostedZonesByVPC", "*"); err != nil {
		return nil, 0, err
	}
	vpcID := qs.Get("vpcid")
	if vpcID == "" {
		return nil, 0, invalid("vpcid is required")
	}
	type summary struct {
		HostedZoneId string `xml:"HostedZoneId"`
		Name         string `xml:"Name"`
		Owner        struct {
			OwningAccount string `xml:"OwningAccount"`
		} `xml:"Owner"`
	}
	var items struct {
		S []summary `xml:"HostedZoneSummary"`
	}
	zones := store.List[Zone](s.env.Store, cZones)
	sort.Slice(zones, func(i, j int) bool { return zones[i].ID < zones[j].ID })
	for _, z := range zones {
		if z.Private && slices.Contains(s.vpcsOf(z), vpcID) {
			x := summary{HostedZoneId: z.ID, Name: z.Name}
			x.Owner.OwningAccount = q.Account
			items.S = append(items.S, x)
		}
	}
	return struct {
		XMLName           xml.Name `xml:"ListHostedZonesByVPCResponse"`
		Xmlns             string   `xml:"xmlns,attr"`
		HostedZoneSummary any      `xml:"HostedZoneSummaries"`
		MaxItems          int      `xml:"MaxItems"`
	}{Xmlns: r53NS, HostedZoneSummary: items, MaxItems: 100}, http.StatusOK, nil
}

func (s *Service) awsDNSSEC(q *awsapi.Req, id string) (any, int, error) {
	id = zoneID(id)
	if err := q.Authorize("route53:GetDNSSEC", hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	if _, err := s.awsZone(id); err != nil {
		return nil, 0, err
	}
	return struct {
		XMLName xml.Name `xml:"GetDNSSECResponse"`
		Xmlns   string   `xml:"xmlns,attr"`
		Status  struct {
			ServeSignature string `xml:"ServeSignature"`
		} `xml:"Status"`
		KeySigningKeys struct{} `xml:"KeySigningKeys"`
	}{Xmlns: r53NS, Status: struct {
		ServeSignature string `xml:"ServeSignature"`
	}{"NOT_SIGNING"}}, http.StatusOK, nil
}

// ---- changes ----

func (s *Service) awsGetChange(q *awsapi.Req, id string) (any, int, error) {
	id = strings.TrimPrefix(id, "/change/")
	if err := q.Authorize("route53:GetChange", changeARN(id)); err != nil {
		return nil, 0, err
	}
	c, err := store.Get[changeInfo](s.env.Store, cChanges, id)
	if err != nil {
		return nil, 0, awsapi.Errorf(http.StatusNotFound, "NoSuchChange", "A change with the specified change ID does not exist.")
	}
	return struct {
		XMLName    xml.Name    `xml:"GetChangeResponse"`
		Xmlns      string      `xml:"xmlns,attr"`
		ChangeInfo xChangeInfo `xml:"ChangeInfo"`
	}{Xmlns: r53NS, ChangeInfo: xChangeInfo{ID: "/change/" + c.ID, Status: "INSYNC", SubmittedAt: isoTime(c.SubmittedAt), Comment: c.Comment}}, http.StatusOK, nil
}

// ---- record sets ----

type xAlias struct {
	HostedZoneId         string `xml:"HostedZoneId"`
	DNSName              string `xml:"DNSName"`
	EvaluateTargetHealth bool   `xml:"EvaluateTargetHealth"`
}

type xRR struct {
	Name            string    `xml:"Name"`
	Type            string    `xml:"Type"`
	SetIdentifier   string    `xml:"SetIdentifier,omitempty"`
	Weight          *int      `xml:"Weight,omitempty"`
	Region          string    `xml:"Region,omitempty"`
	Failover        string    `xml:"Failover,omitempty"`
	MultiValue      *bool     `xml:"MultiValueAnswer,omitempty"`
	HealthCheckID   string    `xml:"HealthCheckId,omitempty"`
	GeoLocation     *struct{} `xml:"GeoLocation,omitempty"`
	TTL             *int      `xml:"TTL,omitempty"`
	ResourceRecords *struct {
		ResourceRecord []struct {
			Value string `xml:"Value"`
		} `xml:"ResourceRecord"`
	} `xml:"ResourceRecords,omitempty"`
	AliasTarget *xAlias `xml:"AliasTarget,omitempty"`
}

// escapeName renders a wildcard label the way Route 53 returns it.
func escapeName(n string) string {
	if strings.HasPrefix(n, "*.") || n == "*" {
		return `\052` + n[1:]
	}
	return n
}

func unescapeName(n string) string {
	return strings.Replace(strings.ToLower(n), `\052`, "*", 1)
}

// unquoteTXT joins the quoted character strings of a TXT value.
func unquoteTXT(v string) (string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, `"`) {
		return v, nil
	}
	var b strings.Builder
	for len(v) > 0 {
		v = strings.TrimLeft(v, " ")
		if v == "" {
			break
		}
		if v[0] != '"' {
			return "", fmt.Errorf("TXT value %q is not a quoted string", v)
		}
		i := 1
		for ; i < len(v); i++ {
			if v[i] == '\\' {
				i++
				continue
			}
			if v[i] == '"' {
				break
			}
		}
		if i >= len(v) {
			return "", fmt.Errorf("unterminated quote in TXT value")
		}
		u, err := strconv.Unquote(v[:i+1])
		if err != nil {
			// DNS escapes such as \DDD or \; are not Go escapes: keep the text.
			u = strings.ReplaceAll(v[1:i], `\"`, `"`)
		}
		b.WriteString(u)
		v = v[i+1:]
	}
	return b.String(), nil
}

// quoteTXT renders a TXT value as quoted strings of at most 255 bytes.
func quoteTXT(v string) string { return txtRData(v) }

// recordFromXML converts a record set of a change into the native form.
func recordFromXML(x xRR) (Record, error) {
	if x.SetIdentifier != "" || x.Weight != nil || x.Region != "" || x.Failover != "" || x.MultiValue != nil || x.HealthCheckID != "" || x.GeoLocation != nil {
		return Record{}, errors.New("routing policies (weighted, latency, failover, geolocation, multivalue) and health checks are not supported")
	}
	// Route 53 names are absolute with or without the trailing dot.
	name := unescapeName(strings.TrimSpace(x.Name))
	if name != "" && !strings.HasSuffix(name, ".") {
		name += "."
	}
	r := Record{Name: name, Type: strings.ToUpper(x.Type), TTL: 300}
	if r.Type == "SOA" || (r.Type != "" && !types[r.Type]) {
		return Record{}, fmt.Errorf("record type %q is not supported", x.Type)
	}
	if x.TTL != nil {
		r.TTL = *x.TTL
	}
	if x.AliasTarget != nil {
		if r.Type != "A" {
			return Record{}, errors.New("alias records must be type A")
		}
		dns := strings.ToLower(strings.TrimPrefix(x.AliasTarget.DNSName, "dualstack."))
		if !strings.HasSuffix(dns, ".") {
			dns += "."
		}
		lb, ok := strings.CutSuffix(dns, elbSuffix)
		if !ok || lb == "" || strings.Contains(lb, ".") {
			return Record{}, fmt.Errorf("alias target %q is not a HomeCloud load balancer DNS name", x.AliasTarget.DNSName)
		}
		r.Alias, r.AliasEvaluateHealth = lb, x.AliasTarget.EvaluateTargetHealth
		return r, nil
	}
	if x.ResourceRecords != nil {
		for _, v := range x.ResourceRecords.ResourceRecord {
			val := v.Value
			if r.Type == "TXT" {
				var err error
				if val, err = unquoteTXT(val); err != nil {
					return Record{}, err
				}
			}
			r.Values = append(r.Values, val)
		}
	}
	return r, nil
}

func recordToXML(z Zone, r Record) xRR {
	x := xRR{Name: escapeName(fqdn(r.Name, z.Name)), Type: r.Type}
	if r.Alias != "" {
		x.AliasTarget = &xAlias{HostedZoneId: elbZoneID, DNSName: r.Alias + elbSuffix, EvaluateTargetHealth: r.AliasEvaluateHealth}
		return x
	}
	ttl := r.TTL
	x.TTL = &ttl
	x.ResourceRecords = &struct {
		ResourceRecord []struct {
			Value string `xml:"Value"`
		} `xml:"ResourceRecord"`
	}{}
	for _, v := range r.Values {
		if r.Type == "TXT" {
			v = quoteTXT(v)
		}
		x.ResourceRecords.ResourceRecord = append(x.ResourceRecords.ResourceRecord, struct {
			Value string `xml:"Value"`
		}{v})
	}
	return x
}

// allRecords lists the zone's record sets as Route 53 shows them: the stored
// records plus the zone's default NS and SOA at the apex.
func (s *Service) allRecords(z Zone) []Record {
	out := slices.Clone(z.Records)
	hasNS := slices.ContainsFunc(out, func(r Record) bool { return r.Type == "NS" && fqdn(r.Name, z.Name) == z.Name })
	if !hasNS {
		out = append(out, Record{Name: "@", Type: "NS", TTL: 172800, Values: []string{"ns." + z.Name}})
	}
	out = append(out, Record{Name: "@", Type: "SOA", TTL: 900, Values: []string{fmt.Sprintf("ns.%s hostmaster.%s %d 7200 900 1209600 86400", z.Name, z.Name, z.Serial)}})
	sort.Slice(out, func(i, j int) bool {
		a, b := nameKey(fqdn(out[i].Name, z.Name)), nameKey(fqdn(out[j].Name, z.Name))
		if a != b {
			return a < b
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func (s *Service) awsChangeRecords(q *awsapi.Req, id string) (any, int, error) {
	id = zoneID(id)
	if err := q.Authorize("route53:ChangeResourceRecordSets", hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	var in struct {
		Comment string `xml:"ChangeBatch>Comment"`
		Changes []struct {
			Action string `xml:"Action"`
			RRS    xRR    `xml:"ResourceRecordSet"`
		} `xml:"ChangeBatch>Changes>Change"`
	}
	if err := xml.Unmarshal(q.Body, &in); err != nil {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed: %v", err)
	}
	z, err := s.awsZone(id)
	if err != nil {
		return nil, 0, err
	}
	if len(in.Changes) == 0 {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the ChangeBatch has no Changes")
	}
	changes := make([]Change, 0, len(in.Changes))
	for _, c := range in.Changes {
		r, err := recordFromXML(c.RRS)
		if err != nil {
			return nil, 0, awsapi.Errorf(http.StatusBadRequest, "InvalidChangeBatch", "%v", err)
		}
		changes = append(changes, Change{Action: c.Action, Record: r})
	}
	// Deleting the default apex NS is not allowed.
	for _, c := range changes {
		if c.Action == "DELETE" && c.Record.Type == "NS" && fqdn(c.Record.Name, z.Name) == z.Name && !slices.ContainsFunc(z.Records, func(r Record) bool { return r.Type == "NS" && fqdn(r.Name, z.Name) == z.Name }) {
			return nil, 0, awsapi.Errorf(http.StatusBadRequest, "InvalidChangeBatch", "Tried to delete the zone's default NS record set")
		}
	}
	if _, err := s.applyChanges(id, changes); err != nil {
		var ce *core.Error
		if errors.As(err, &ce) && ce.Code == "ValidationError" {
			return nil, 0, awsapi.Errorf(http.StatusBadRequest, "InvalidChangeBatch", "%s", ce.Message)
		}
		if errors.As(err, &ce) && ce.Code == "InvalidChangeBatch" {
			msg := ce.Message
			// Terraform and the SDKs recognise these phrases.
			if strings.HasSuffix(msg, "already exists") {
				msg = "Tried to create resource record set " + strings.TrimSuffix(msg, " already exists") + " but it already exists"
			} else if strings.HasSuffix(msg, "does not exist") {
				msg = "Tried to delete resource record set " + strings.TrimSuffix(msg, " does not exist") + " but it was not found"
			}
			return nil, 0, awsapi.Errorf(http.StatusBadRequest, "InvalidChangeBatch", "%s", msg)
		}
		return nil, 0, err
	}
	ci, err := s.newChange(in.Comment)
	if err != nil {
		return nil, 0, err
	}
	return struct {
		XMLName    xml.Name    `xml:"ChangeResourceRecordSetsResponse"`
		Xmlns      string      `xml:"xmlns,attr"`
		ChangeInfo xChangeInfo `xml:"ChangeInfo"`
	}{Xmlns: r53NS, ChangeInfo: ci}, http.StatusOK, nil
}

func (s *Service) awsListRecords(q *awsapi.Req, id string, qs url.Values) (any, int, error) {
	id = zoneID(id)
	if err := q.Authorize("route53:ListResourceRecordSets", hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	z, err := s.awsZone(id)
	if err != nil {
		return nil, 0, err
	}
	recs := s.allRecords(z)
	startName, startType := qs.Get("name"), strings.ToUpper(qs.Get("type"))
	if startName != "" {
		startName = fqdn(unescapeName(startName), z.Name)
	}
	max := min(atoi(qs.Get("maxitems"), 300), 300)
	var sets struct {
		RR []xRR `xml:"ResourceRecordSet"`
	}
	truncated := false
	var nextName, nextType string
	for _, r := range recs {
		n := fqdn(r.Name, z.Name)
		if startName != "" {
			a, b := nameKey(n), nameKey(startName)
			if a < b || (a == b && startType != "" && r.Type < startType) {
				continue
			}
		}
		if len(sets.RR) == max {
			truncated, nextName, nextType = true, escapeName(n), r.Type
			break
		}
		sets.RR = append(sets.RR, recordToXML(z, r))
	}
	return struct {
		XMLName            xml.Name `xml:"ListResourceRecordSetsResponse"`
		Xmlns              string   `xml:"xmlns,attr"`
		ResourceRecordSets any      `xml:"ResourceRecordSets"`
		IsTruncated        bool     `xml:"IsTruncated"`
		NextRecordName     string   `xml:"NextRecordName,omitempty"`
		NextRecordType     string   `xml:"NextRecordType,omitempty"`
		MaxItems           int      `xml:"MaxItems"`
	}{Xmlns: r53NS, ResourceRecordSets: sets, IsTruncated: truncated, NextRecordName: nextName, NextRecordType: nextType, MaxItems: max}, http.StatusOK, nil
}

// ---- VPC associations ----

func (s *Service) awsAssociate(q *awsapi.Req, id string, add bool) (any, int, error) {
	id = zoneID(id)
	action, root := "route53:AssociateVPCWithHostedZone", "AssociateVPCWithHostedZoneResponse"
	if !add {
		action, root = "route53:DisassociateVPCFromHostedZone", "DisassociateVPCFromHostedZoneResponse"
	}
	if err := q.Authorize(action, hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	var in struct {
		Comment string `xml:"Comment"`
		VPC     xVPC   `xml:"VPC"`
	}
	if err := xml.Unmarshal(q.Body, &in); err != nil {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed: %v", err)
	}
	if in.VPC.VPCId == "" {
		return nil, 0, invalid("VPC is required")
	}
	if _, err := s.vpc.GetVPC(in.VPC.VPCId); err != nil {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "InvalidVPCId", "The VPC ID %q is not valid.", in.VPC.VPCId)
	}
	_, err := store.Update(s.env.Store, cZones, id, func(z *Zone) error {
		if !z.Private {
			return awsapi.Errorf(http.StatusBadRequest, "PublicZoneVPCAssociation", "You're trying to associate a VPC with a public hosted zone.")
		}
		cur := s.vpcsOf(*z)
		has := slices.Contains(cur, in.VPC.VPCId)
		switch {
		case add && has:
			return awsapi.Errorf(http.StatusBadRequest, "ConflictingDomainExists", "The VPC %s is already associated with the hosted zone.", in.VPC.VPCId)
		case add:
			z.VpcIDs = append(slices.Clone(cur), in.VPC.VPCId)
		case !has:
			return awsapi.Errorf(http.StatusBadRequest, "VPCAssociationNotFound", "The VPC %s is not associated with the hosted zone.", in.VPC.VPCId)
		case len(cur) == 1:
			return awsapi.Errorf(http.StatusBadRequest, "LastVPCAssociation", "The VPC cannot be disassociated because it is the last VPC associated with the private hosted zone.")
		default:
			z.VpcIDs = slices.DeleteFunc(slices.Clone(cur), func(v string) bool { return v == in.VPC.VPCId })
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, 0, awsapi.Errorf(http.StatusNotFound, "NoSuchHostedZone", "No hosted zone found with ID: %s", id)
	}
	if err != nil {
		return nil, 0, err
	}
	s.bump(id)
	ci, err := s.newChange(in.Comment)
	if err != nil {
		return nil, 0, err
	}
	return struct {
		XMLName    xml.Name
		Xmlns      string      `xml:"xmlns,attr"`
		ChangeInfo xChangeInfo `xml:"ChangeInfo"`
	}{XMLName: xml.Name{Local: root}, Xmlns: r53NS, ChangeInfo: ci}, http.StatusOK, nil
}

// ---- tags ----

type xTag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

func (s *Service) awsListTags(q *awsapi.Req, id string) (any, int, error) {
	id = zoneID(id)
	if err := q.Authorize("route53:ListTagsForResource", hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	z, err := s.awsZone(id)
	if err != nil {
		return nil, 0, err
	}
	var tags struct {
		Tag []xTag `xml:"Tag"`
	}
	keys := make([]string, 0, len(z.Tags))
	for k := range z.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tags.Tag = append(tags.Tag, xTag{k, z.Tags[k]})
	}
	return struct {
		XMLName        xml.Name `xml:"ListTagsForResourceResponse"`
		Xmlns          string   `xml:"xmlns,attr"`
		ResourceTagSet struct {
			ResourceType string `xml:"ResourceType"`
			ResourceId   string `xml:"ResourceId"`
			Tags         any    `xml:"Tags"`
		} `xml:"ResourceTagSet"`
	}{Xmlns: r53NS, ResourceTagSet: struct {
		ResourceType string `xml:"ResourceType"`
		ResourceId   string `xml:"ResourceId"`
		Tags         any    `xml:"Tags"`
	}{"hostedzone", id, tags}}, http.StatusOK, nil
}

func (s *Service) awsChangeTags(q *awsapi.Req, id string) (any, int, error) {
	id = zoneID(id)
	if err := q.Authorize("route53:ChangeTagsForResource", hostedZoneARN(id)); err != nil {
		return nil, 0, err
	}
	var in struct {
		Add    []xTag   `xml:"AddTags>Tag"`
		Remove []string `xml:"RemoveTagKeys>Key"`
	}
	if err := xml.Unmarshal(q.Body, &in); err != nil {
		return nil, 0, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed: %v", err)
	}
	_, err := store.Update(s.env.Store, cZones, id, func(z *Zone) error {
		if z.Tags == nil {
			z.Tags = core.Tags{}
		}
		for _, t := range in.Add {
			z.Tags[t.Key] = t.Value
		}
		for _, k := range in.Remove {
			delete(z.Tags, k)
		}
		if len(z.Tags) > 50 {
			return invalid("a hosted zone can have at most 50 tags")
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, 0, awsapi.Errorf(http.StatusNotFound, "NoSuchHostedZone", "No hosted zone found with ID: %s", id)
	}
	if err != nil {
		return nil, 0, err
	}
	return struct {
		XMLName xml.Name `xml:"ChangeTagsForResourceResponse"`
		Xmlns   string   `xml:"xmlns,attr"`
	}{Xmlns: r53NS}, http.StatusOK, nil
}
