// Package route53 implements DNS hosted zones on a managed CoreDNS server.
// The server holds the resolver address (CIDR base + 2) in every VPC, and new
// instances, tasks and functions use it as the upstream of Docker's embedded
// DNS, so private zones resolve inside VPCs while container names keep
// working. Public zones are also served on a host port for LAN clients.
package route53

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

const (
	cZones        = "route53_zones"
	containerName = "homecloud-dns"
	image         = "coredns/coredns:1.11.3"
)

type Record struct {
	Name   string   `json:"name"` // relative to the zone ("www", "@") or fully qualified
	Type   string   `json:"type"`
	TTL    int      `json:"ttl"`
	Values []string `json:"values,omitempty"`
	// Alias points at a HomeCloud resource (instance, task, database, load balancer)
	// and follows its private IP.
	Alias string `json:"alias,omitempty"`
	// AliasEvaluateHealth is the alias's EvaluateTargetHealth flag, stored and reported as given.
	AliasEvaluateHealth bool `json:"alias_evaluate_health,omitempty"`
}

type Zone struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"` // fully qualified, with trailing dot
	Private   bool      `json:"private"`
	VpcIDs    []string  `json:"vpc_ids,omitempty"`
	Comment   string    `json:"comment,omitempty"`
	CallerRef string    `json:"caller_reference,omitempty"`
	Tags      core.Tags `json:"tags,omitempty"`
	Records   []Record  `json:"records"`
	Serial    uint32    `json:"serial"`
	CreatedAt time.Time `json:"created_at"`
}

// Resolver maps a resource ID to its private IP.
type Resolver func(id string) (string, bool)

type Service struct {
	env     *svc.Env
	vpc     *vpc.Service
	Resolve Resolver
	mu      sync.Mutex
	last    string // last rendered configuration
	ready   bool
}

func New(env *svc.Env, v *vpc.Service) *Service { return &Service{env: env, vpc: v} }

// DNSFor returns the resolver address to give containers in a VPC, if the DNS server runs.
func (s *Service) DNSFor(vpcID string) []string {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if !ready {
		return nil
	}
	v, err := s.vpc.GetVPC(vpcID)
	if err != nil {
		return nil
	}
	return []string{vpc.DNSAddress(v.CIDR)}
}

// ---- rendering ----

func fqdn(name, zone string) string {
	switch {
	case name == "" || name == "@":
		return zone
	case strings.HasSuffix(name, "."):
		return name
	case strings.HasSuffix(name+".", zone):
		return name + "."
	}
	return name + "." + zone
}

func (s *Service) zoneFile(z Zone, nsIP string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "$ORIGIN %s\n$TTL 300\n", z.Name)
	fmt.Fprintf(&b, "@ 3600 IN SOA ns.%s hostmaster.%s %d 7200 3600 1209600 60\n", z.Name, z.Name, z.Serial)
	fmt.Fprintf(&b, "@ 3600 IN NS ns.%s\n", z.Name)
	hasNS := false
	for _, r := range z.Records {
		if fqdn(r.Name, z.Name) == "ns."+z.Name {
			hasNS = true
		}
	}
	if !hasNS && nsIP != "" {
		fmt.Fprintf(&b, "ns.%s 3600 IN A %s\n", z.Name, nsIP)
	}
	for _, r := range z.Records {
		name := fqdn(r.Name, z.Name)
		values := r.Values
		if r.Alias != "" {
			ip, ok := "", false
			if s.Resolve != nil {
				ip, ok = s.Resolve(r.Alias)
			}
			if !ok {
				continue // target not running: no answer rather than a stale one
			}
			values = []string{ip}
		}
		for _, v := range values {
			if r.Type == "TXT" {
				v = txtRData(v)
			}
			fmt.Fprintf(&b, "%s %d IN %s %s\n", name, r.TTL, r.Type, v)
		}
	}
	return b.String()
}

// txtRData renders a TXT value as zone-file character strings of at most 255
// bytes each, so long values (DKIM keys) are valid.
func txtRData(v string) string {
	if len(v) <= 255 {
		return strconv.Quote(v)
	}
	var parts []string
	for len(v) > 255 {
		parts = append(parts, strconv.Quote(v[:255]))
		v = v[255:]
	}
	return strings.Join(append(parts, strconv.Quote(v)), " ")
}

// nsAddress is the address for a zone's generated ns record: the VPC resolver,
// or for public zones the host's address when it is an IP (LAN clients can't
// reach VPC addresses).
func (s *Service) nsAddress(z Zone, vpcNS string) string {
	if !z.Private {
		if a, err := netip.ParseAddr(s.env.Cfg.PublicHost); err == nil && !a.IsLoopback() {
			return a.String()
		}
	}
	return vpcNS
}

func (s *Service) render() map[string]string {
	vpcs := s.vpc.List()
	files := map[string]string{}
	var cf strings.Builder
	cf.WriteString(". {\n  errors\n  reload 5s\n  forward . 127.0.0.11\n  cache 30\n}\n")
	zones := store.List[Zone](s.env.Store, cZones)
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	nsIP := ""
	if len(vpcs) > 0 {
		nsIP = vpc.DNSAddress(vpcs[0].CIDR)
	}
	for _, z := range zones {
		file := "zones/" + z.ID + ".db"
		files[file] = s.zoneFile(z, s.nsAddress(z, nsIP))
		fmt.Fprintf(&cf, "%s {\n  errors\n  reload 5s\n", z.Name)
		if z.Private {
			// Private zones answer only clients inside their VPCs.
			cf.WriteString("  acl {\n")
			for _, v := range vpcs {
				// Queries through the published host port arrive from the VPC
				// gateway (base+1), which no resource uses: treat them as outside.
				fmt.Fprintf(&cf, "    block net %s/32\n", gateway(v.CIDR))
			}
			for _, v := range vpcs {
				if len(z.VpcIDs) == 0 || slices.Contains(z.VpcIDs, v.ID) {
					fmt.Fprintf(&cf, "    allow net %s\n", v.CIDR)
				}
			}
			cf.WriteString("    block\n  }\n")
		}
		fmt.Fprintf(&cf, "  file /etc/coredns/%s {\n    reload 5s\n  }\n}\n", file)
	}
	files["Corefile"] = cf.String()
	return files
}

// ---- lifecycle ----

// Run starts the DNS server and keeps its configuration and VPC attachments current.
func (s *Service) Run(ctx context.Context) {
	for attempt := 0; ; attempt++ {
		err := s.start(ctx)
		if err == nil {
			break
		}
		if attempt%10 == 0 {
			log.Printf("route53: %v (retrying)", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		s.sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) start(ctx context.Context) error {
	d := s.env.Docker
	vpcs := s.vpc.List()
	if len(vpcs) == 0 {
		return fmt.Errorf("no VPCs")
	}
	bind := s.env.Cfg.DNSBindAddr()
	bound, berr := true, error(nil)
	if d.State(containerName) != "missing" {
		if bound, berr = d.BoundTo(containerName, bind, "53/udp", "53/tcp"); berr != nil {
			log.Printf("route53: inspect the DNS server: %v (keeping the container)", berr)
			bound = true
		}
	}
	if !bound {
		log.Printf("route53: recreating the DNS server to publish its port on %s only", bind)
		if err := d.Remove(containerName); err != nil {
			return fmt.Errorf("recreate CoreDNS: %w", err)
		}
	}
	if d.State(containerName) == "missing" {
		if err := checkPortFree(bind, s.env.Cfg.DNSPort); err != nil {
			return err
		}
		first := vpcs[0]
		files := s.render()
		cid, err := d.Run(ctx, runtime.RunSpec{
			Name: containerName, Image: image, Cmd: []string{"-conf", "/etc/coredns/Corefile"},
			Labels: runtime.Labels("route53", "server", nil), Restart: "unless-stopped", MemoryMB: 128,
			Network: first.Network, IP: vpc.DNSAddress(first.CIDR), Aliases: []string{"dns.internal"},
			Ports: []runtime.Port{{ContainerPort: 53, HostPort: s.env.Cfg.DNSPort, Protocol: "udp", HostIP: bind}, {ContainerPort: 53, HostPort: s.env.Cfg.DNSPort, Protocol: "tcp", HostIP: bind}},
		})
		if err != nil {
			return fmt.Errorf("start CoreDNS: %w", err)
		}
		if err := s.copy(ctx, cid, files); err != nil {
			return err
		}
		if err := d.Start(cid); err != nil {
			return err
		}
	} else if err := d.Start(containerName); err != nil {
		return err
	}
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	log.Printf("route53: DNS ready (public zones on udp/tcp port %d)", s.env.Cfg.DNSPort)
	return nil
}

// checkPortFree fails with an actionable message when another process already
// holds the DNS port (on Ubuntu, port 53 is typically systemd-resolved). A
// permission error is not a conflict: Docker's daemon runs as root and binds
// low ports even when HomeCloud does not.
func checkPortFree(bind string, port int) error {
	addr := net.JoinHostPort(bind, strconv.Itoa(port))
	var inUse error
	if l, err := net.Listen("tcp", addr); err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			inUse = err
		}
	} else {
		l.Close()
	}
	if inUse == nil {
		if pc, err := net.ListenPacket("udp", addr); err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				inUse = err
			}
		} else {
			pc.Close()
		}
	}
	if inUse == nil {
		return nil
	}
	return fmt.Errorf("DNS port %s is already in use by another process: %s", addr, portBusyHint(port))
}

func portBusyHint(port int) string {
	if port == 53 {
		return "on Ubuntu and Debian that is usually systemd-resolved: set DNSStubListener=no in its resolved.conf and restart it, or publish on one address with --dns-bind <public IP> (the stub only listens on 127.0.0.53), or use another port with --dns-port"
	}
	return "choose another port with --dns-port (8053 is the default)"
}

func (s *Service) copy(ctx context.Context, cid string, files map[string]string) error {
	m := map[string][]byte{}
	for k, v := range files {
		m["etc/coredns/"+k] = []byte(v)
	}
	return s.env.Docker.CopyIn(ctx, cid, "/", m, 0o644)
}

func (s *Service) sync(ctx context.Context) {
	if s.env.Docker == nil {
		return // no container runtime (tests)
	}
	for _, v := range s.vpc.List() {
		_ = s.env.Docker.ConnectIP(v.Network, containerName, vpc.DNSAddress(v.CIDR), "dns.internal")
	}
	files := s.render()
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var all strings.Builder
	for _, k := range keys {
		all.WriteString(k + "\n" + files[k])
	}
	s.mu.Lock()
	changed := all.String() != s.last
	s.mu.Unlock()
	if !changed {
		return
	}
	if err := s.copy(ctx, containerName, files); err != nil {
		log.Printf("route53: update config: %v", err)
		return
	}
	s.mu.Lock()
	s.last = all.String()
	s.mu.Unlock()
}

func (s *Service) bump(id string) {
	_, _ = store.Update(s.env.Store, cZones, id, func(z *Zone) error {
		n := uint32(time.Now().Unix())
		if n <= z.Serial {
			n = z.Serial + 1
		}
		z.Serial = n
		return nil
	})
	go s.sync(context.Background())
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:route53:::hostedzone/{id}")
	r.Handle("GET /api/v1/route53/zones", "route53:ListHostedZones", s.list)
	r.Handle("POST /api/v1/route53/zones", "route53:CreateHostedZone", s.create)
	r.Handle("GET /api/v1/route53/zones/{id}", "route53:GetHostedZone", s.get, res)
	r.Handle("DELETE /api/v1/route53/zones/{id}", "route53:DeleteHostedZone", s.delete, res)
	r.Handle("POST /api/v1/route53/zones/{id}/changes", "route53:ChangeResourceRecordSets", s.change, res)
	r.Handle("GET /api/v1/route53/zones/{id}/zone-file", "route53:GetHostedZone", s.exportZone, res)
	r.Handle("POST /api/v1/route53/test-dns", "route53:TestDNSAnswer", s.test)
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, z := range store.List[Zone](s.env.Store, cZones) {
		out = append(out, map[string]any{"id": z.ID, "name": z.Name, "private": z.Private, "vpc_ids": z.VpcIDs, "comment": z.Comment,
			"record_count": len(z.Records), "created_at": z.CreatedAt})
	}
	return out, nil
}

var labelRe = regexp.MustCompile(`^([a-zA-Z0-9_]([a-zA-Z0-9_-]{0,61}[a-zA-Z0-9_])?\.)+$`)

type zoneInput struct {
	Name      string    `json:"name"`
	Private   bool      `json:"private"`
	VpcIDs    []string  `json:"vpc_ids"`
	Comment   string    `json:"comment"`
	CallerRef string    `json:"caller_reference"`
	Tags      core.Tags `json:"tags"`
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in zoneInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.createZone(in)
}

// createZone is shared by the native and AWS APIs.
func (s *Service) createZone(in zoneInput) (Zone, error) {
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	if !labelRe.MatchString(name) || len(name) > 254 {
		return Zone{}, core.BadRequest("%q is not a valid domain name", in.Name)
	}
	for _, z := range store.List[Zone](s.env.Store, cZones) {
		if z.Name == name {
			return Zone{}, core.Errf(http.StatusConflict, "HostedZoneAlreadyExists", "zone %s already exists (%s)", name, z.ID)
		}
	}
	for _, v := range in.VpcIDs {
		if _, err := s.vpc.GetVPC(v); err != nil {
			return Zone{}, core.NotFound("vpc", v)
		}
	}
	z := Zone{ID: "Z" + strings.ToUpper(core.RandHex(13)), Name: name, Private: in.Private, VpcIDs: in.VpcIDs, Comment: in.Comment, CallerRef: in.CallerRef, Tags: in.Tags,
		Records: []Record{}, Serial: uint32(time.Now().Unix()), CreatedAt: core.Now()}
	if err := store.Put(s.env.Store, cZones, z.ID, z); err != nil {
		return Zone{}, err
	}
	go s.sync(context.Background())
	return z, nil
}

func (s *Service) zone(id string) (Zone, error) {
	z, err := store.Get[Zone](s.env.Store, cZones, id)
	if err != nil {
		return z, core.Errf(http.StatusNotFound, "NoSuchHostedZone", "hosted zone %q does not exist", id)
	}
	return z, nil
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	z, err := s.zone(c.Param("id"))
	if err != nil {
		return nil, err
	}
	nameservers := []string{}
	for _, v := range s.vpc.List() {
		if z.Private && len(z.VpcIDs) > 0 && !slices.Contains(z.VpcIDs, v.ID) {
			continue // the zone doesn't answer in this VPC
		}
		nameservers = append(nameservers, vpc.DNSAddress(v.CIDR)+" ("+v.Name+" VPC)")
	}
	if !z.Private {
		if ip := net.ParseIP(s.env.Cfg.DNSBindAddr()); ip != nil && ip.IsLoopback() {
			nameservers = append(nameservers, fmt.Sprintf("%s:%d (this host only; publish it with --dns-bind)", ip, s.env.Cfg.DNSPort))
		} else {
			nameservers = append(nameservers, fmt.Sprintf("%s:%d (LAN)", s.env.Cfg.PublicHost, s.env.Cfg.DNSPort))
		}
	}
	return map[string]any{"zone": z, "name_servers": nameservers}, nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	return nil, s.deleteZone(c.Param("id"), c.Query("force") == "true")
}

func (s *Service) deleteZone(id string, force bool) error {
	z, err := s.zone(id)
	if err != nil {
		return err
	}
	if len(z.Records) > 0 && !force {
		return core.Errf(http.StatusConflict, "HostedZoneNotEmpty", "zone has %d records; delete them first or pass force=true", len(z.Records))
	}
	if err := store.Delete(s.env.Store, cZones, z.ID); err != nil {
		return err
	}
	go s.sync(context.Background())
	return nil
}

var types = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "TXT": true, "MX": true, "SRV": true, "NS": true, "CAA": true, "PTR": true}

func validate(z Zone, r *Record) error {
	r.Type = strings.ToUpper(r.Type)
	if !types[r.Type] {
		return core.BadRequest("unsupported record type %q", r.Type)
	}
	r.Name = strings.ToLower(strings.TrimSpace(r.Name))
	if r.Name == "" {
		r.Name = "@"
	}
	full := fqdn(r.Name, z.Name)
	if !strings.HasSuffix(full, z.Name) || (r.Name != "@" && !labelRe.MatchString(strings.TrimPrefix(full, "*."))) {
		return core.BadRequest("record name %q is not inside zone %s", r.Name, z.Name)
	}
	if r.TTL == 0 {
		r.TTL = 300
	}
	if r.TTL < 0 || r.TTL > 604800 {
		return core.BadRequest("ttl must be 0-604800")
	}
	if r.Alias != "" {
		if r.Type != "A" || len(r.Values) > 0 {
			return core.BadRequest("alias records are type A and have no values")
		}
		return nil
	}
	if len(r.Values) == 0 {
		return core.BadRequest("a record needs at least one value")
	}
	if r.Type == "CNAME" && (len(r.Values) > 1 || full == z.Name) {
		return core.BadRequest("a CNAME has exactly one value and cannot be at the zone apex")
	}
	for _, v := range r.Values {
		if strings.ContainsAny(v, "\n\r") || (r.Type != "TXT" && strings.Contains(v, ";")) {
			return core.BadRequest("record values may not contain newlines or semicolons")
		}
		switch r.Type {
		case "A":
			if ip := net.ParseIP(v); ip == nil || ip.To4() == nil {
				return core.BadRequest("%q is not an IPv4 address", v)
			}
		case "AAAA":
			if ip := net.ParseIP(v); ip == nil || ip.To4() != nil {
				return core.BadRequest("%q is not an IPv6 address", v)
			}
		case "CNAME", "NS", "PTR":
			if !labelRe.MatchString(strings.TrimSuffix(v, ".") + ".") {
				return core.BadRequest("%q is not a domain name", v)
			}
		case "TXT":
			if len(v) > 4000 {
				return core.BadRequest("TXT values are at most 4000 characters")
			}
		}
	}
	return nil
}

// Change is one record change of a batch.
type Change struct {
	Action string `json:"action"` // CREATE | UPSERT | DELETE
	Record Record `json:"record"`
}

func (s *Service) change(c *httpx.Ctx) (any, error) {
	var in struct {
		Changes []Change `json:"changes"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	z, err := s.applyChanges(c.Param("id"), in.Changes)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "INSYNC", "records": len(z.Records)}, nil
}

// applyChanges applies a batch atomically: all changes take effect or none.
func (s *Service) applyChanges(id string, changes []Change) (Zone, error) {
	if len(changes) == 0 {
		return Zone{}, core.BadRequest("no changes")
	}
	z, err := store.Update(s.env.Store, cZones, id, func(z *Zone) error {
		for _, ch := range changes {
			r := ch.Record
			if err := validate(*z, &r); err != nil {
				return err
			}
			idx := slices.IndexFunc(z.Records, func(x Record) bool { return fqdn(x.Name, z.Name) == fqdn(r.Name, z.Name) && x.Type == r.Type })
			switch strings.ToUpper(ch.Action) {
			case "CREATE":
				if idx >= 0 {
					return core.Errf(http.StatusBadRequest, "InvalidChangeBatch", "%s %s already exists", r.Type, fqdn(r.Name, z.Name))
				}
				z.Records = append(z.Records, r)
			case "UPSERT":
				if idx >= 0 {
					z.Records[idx] = r
				} else {
					z.Records = append(z.Records, r)
				}
			case "DELETE":
				if idx < 0 {
					return core.Errf(http.StatusBadRequest, "InvalidChangeBatch", "%s %s does not exist", r.Type, fqdn(r.Name, z.Name))
				}
				z.Records = append(z.Records[:idx], z.Records[idx+1:]...)
			default:
				return core.BadRequest("action must be CREATE, UPSERT or DELETE")
			}
		}
		// CNAMEs may not share a name with other records.
		names := map[string][]string{}
		for _, r := range z.Records {
			n := fqdn(r.Name, z.Name)
			names[n] = append(names[n], r.Type)
		}
		for n, ts := range names {
			if slices.Contains(ts, "CNAME") && len(ts) > 1 {
				return core.Errf(http.StatusBadRequest, "InvalidChangeBatch", "%s has a CNAME and other records", n)
			}
		}
		return nil
	})
	if err == store.ErrNotFound {
		return Zone{}, core.Errf(http.StatusNotFound, "NoSuchHostedZone", "hosted zone %q does not exist", id)
	}
	if err != nil {
		return Zone{}, err
	}
	s.bump(id)
	return z, nil
}

func (s *Service) exportZone(c *httpx.Ctx) (any, error) {
	z, err := s.zone(c.Param("id"))
	if err != nil {
		return nil, err
	}
	ns := ""
	if vs := s.vpc.List(); len(vs) > 0 {
		ns = vpc.DNSAddress(vs[0].CIDR)
	}
	return map[string]string{"zone_file": s.zoneFile(z, s.nsAddress(z, ns))}, nil
}

// test queries the DNS server through its host port, like `dig`.
func (s *Service) test(c *httpx.Ctx) (any, error) {
	var in struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	server := s.env.Docker.DialAddr(containerName, 53, fmt.Sprintf("127.0.0.1:%d", s.env.Cfg.DNSPort))
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, server)
	}}
	ctx, cancel := context.WithTimeout(c.R.Context(), 3*time.Second)
	defer cancel()
	var answers []string
	var err error
	switch strings.ToUpper(in.Type) {
	case "", "A", "AAAA":
		family := "ip4"
		if strings.EqualFold(in.Type, "AAAA") {
			family = "ip6"
		}
		var ips []net.IP
		ips, err = r.LookupIP(ctx, family, in.Name)
		for _, ip := range ips {
			answers = append(answers, ip.String())
		}
	case "CNAME":
		var cn string
		cn, err = r.LookupCNAME(ctx, in.Name)
		answers = []string{cn}
	case "TXT":
		answers, err = r.LookupTXT(ctx, in.Name)
	case "MX":
		var mx []*net.MX
		mx, err = r.LookupMX(ctx, in.Name)
		for _, m := range mx {
			answers = append(answers, fmt.Sprintf("%d %s", m.Pref, m.Host))
		}
	default:
		return nil, core.BadRequest("test supports A, AAAA, CNAME, TXT and MX")
	}
	if err != nil {
		return map[string]any{"name": in.Name, "type": in.Type, "answers": []string{}, "error": err.Error()}, nil
	}
	return map[string]any{"name": in.Name, "type": in.Type, "answers": answers, "note": "queried the public view on the host port; private zones answer only inside their VPCs"}, nil
}

func gateway(cidr string) string {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "0.0.0.0"
	}
	return p.Masked().Addr().Next().String()
}
