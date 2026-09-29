// Package elb implements application load balancers. Each load balancer is a
// managed nginx container placed in a VPC subnet; HomeCloud renders its
// configuration from listeners, rules and target groups, health-checks
// targets from inside the VPC and reloads nginx when the healthy set changes.
package elb

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

const (
	cLBs   = "elb_load_balancers"
	cTGs   = "elb_target_groups"
	lbImg  = "nginx:1.27-alpine"
	confIn = "/etc/nginx/conf.d/default.conf"
)

type Rule struct {
	ID          string `json:"id"`
	Priority    int    `json:"priority"`
	PathPrefix  string `json:"path_prefix,omitempty"` // e.g. /api/
	HostHeader  string `json:"host_header,omitempty"` // e.g. api.example.com
	TargetGroup string `json:"target_group"`
}

type Listener struct {
	ID             string `json:"id"`
	Port           int    `json:"port"`
	Protocol       string `json:"protocol"` // HTTP | HTTPS
	CertificateARN string `json:"certificate_arn,omitempty"`
	// RedirectHTTPSPort makes an HTTP listener redirect every request to the HTTPS listener on this port.
	RedirectHTTPSPort  int    `json:"redirect_https_port,omitempty"`
	PublicPort         int    `json:"public_port,omitempty"` // requested host port (0 = any)
	DefaultTargetGroup string `json:"default_target_group"`
	Rules              []Rule `json:"rules"`
}

type LoadBalancer struct {
	Name        string         `json:"name"`
	ARN         string         `json:"arn"`
	DNSName     string         `json:"dns_name"`
	Scheme      string         `json:"scheme"` // internet-facing | internal
	VpcID       string         `json:"vpc_id"`
	SubnetID    string         `json:"subnet_id"`
	PrivateIP   string         `json:"private_ip"`
	Listeners   []Listener     `json:"listeners"`
	State       string         `json:"state"`
	StateReason string         `json:"state_reason,omitempty"`
	ContainerID string         `json:"container_id,omitempty"`
	PublicPorts map[string]int `json:"public_ports"`
	PublicHost  string         `json:"public_host"`
	CreatedAt   time.Time      `json:"created_at"`
	Tags        core.Tags      `json:"tags,omitempty"`
}

type HealthCheck struct {
	Path               string `json:"path"`
	IntervalSeconds    int    `json:"interval_seconds"`
	HealthyThreshold   int    `json:"healthy_threshold"`
	UnhealthyThreshold int    `json:"unhealthy_threshold"`
}

type Target struct {
	ID       string `json:"id"` // instance id, or an IP address
	Port     int    `json:"port"`
	IP       string `json:"ip"`
	Health   string `json:"health"` // initial | healthy | unhealthy | unavailable
	Reason   string `json:"reason,omitempty"`
	streak   int
	lastGood bool
}

type TargetGroup struct {
	Name        string      `json:"name"`
	ARN         string      `json:"arn"`
	Protocol    string      `json:"protocol"`
	Port        int         `json:"port"`
	VpcID       string      `json:"vpc_id"`
	HealthCheck HealthCheck `json:"health_check"`
	Targets     []Target    `json:"targets"`
	CreatedAt   time.Time   `json:"created_at"`
}

// Resolver maps a target ID (e.g. an instance) to its private IP and VPC.
type Resolver func(id string) (ip, vpcID string, ok bool)

// Certificates supplies TLS material for HTTPS listeners (the ACM service).
type Certificates interface {
	KeyPair(arn string) (certChain, key []byte, err error)
	Exists(arn string) bool
}

type Service struct {
	env     *svc.Env
	vpc     *vpc.Service
	Resolve Resolver
	Certs   Certificates
	mu      sync.Mutex         // serialises config pushes
	health  map[string]*Target // tg/target -> live health state
	hmu     sync.Mutex
}

func New(env *svc.Env, v *vpc.Service) *Service {
	return &Service{env: env, vpc: v, health: map[string]*Target{}}
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$`)

// Rule conditions are rendered into nginx configuration, so they are whitelisted.
var (
	pathRe = regexp.MustCompile(`^/[A-Za-z0-9._~%/*-]*$`)
	hostRe = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
)

// ---- config rendering ----

func upstream(tg string) string { return "tg_" + strings.ReplaceAll(tg, "-", "_") }

func healthKey(tg, target string, port int) string { return fmt.Sprintf("%s/%s:%d", tg, target, port) }

// healthOf returns a copy of a target's health (nil if never checked).
func (s *Service) healthOf(tg, target string, port int) *Target {
	s.hmu.Lock()
	defer s.hmu.Unlock()
	h := s.health[healthKey(tg, target, port)]
	if h == nil {
		return nil
	}
	cp := *h
	return &cp
}

// forget drops health state for a target (all ports).
func (s *Service) forget(tg, target string) {
	s.hmu.Lock()
	defer s.hmu.Unlock()
	for k := range s.health {
		if strings.HasPrefix(k, tg+"/"+target+":") {
			delete(s.health, k)
		}
	}
}

func (s *Service) render(lb LoadBalancer) string {
	var b strings.Builder
	b.WriteString("# generated by HomeCloud; do not edit\n")
	b.WriteString("map $http_upgrade $connection_upgrade { default upgrade; '' close; }\n")
	b.WriteString(`log_format hc '$remote_addr "$request" $status $body_bytes_sent $request_time "$http_user_agent" upstream=$upstream_addr';` + "\n")
	used := map[string]bool{}
	for _, l := range lb.Listeners {
		if l.DefaultTargetGroup != "" {
			used[l.DefaultTargetGroup] = true
		}
		for _, r := range l.Rules {
			used[r.TargetGroup] = true
		}
	}
	names := make([]string, 0, len(used))
	for n := range used {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		tg, err := store.Get[TargetGroup](s.env.Store, cTGs, n)
		// A shared zone keeps round-robin state consistent across nginx workers.
		fmt.Fprintf(&b, "upstream %s {\n  zone %s 64k;\n", upstream(n), upstream(n))
		servers := 0
		if err == nil {
			for _, t := range tg.Targets {
				h := s.healthOf(n, t.ID, t.Port)
				ip, _, ok := s.resolve(t.ID)
				if !ok || (h != nil && h.Health != "healthy" && h.Health != "initial") {
					continue
				}
				fmt.Fprintf(&b, "  server %s:%d max_fails=2 fail_timeout=10s;\n", ip, t.Port)
				servers++
			}
		}
		if servers == 0 {
			b.WriteString("  server 127.0.0.1:9 down; # no healthy targets\n")
		}
		b.WriteString("  keepalive 16;\n}\n")
	}
	proxy := func(tg string) string {
		return fmt.Sprintf(`    proxy_pass http://%s;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_next_upstream error timeout http_502 http_503;
`, upstream(tg))
	}
	for _, l := range lb.Listeners {
		rules := append([]Rule(nil), l.Rules...)
		sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority < rules[j].Priority })
		hosts := map[string][]Rule{}
		var hostOrder []string
		for _, r := range rules {
			if _, seen := hosts[r.HostHeader]; !seen {
				hostOrder = append(hostOrder, r.HostHeader)
			}
			hosts[r.HostHeader] = append(hosts[r.HostHeader], r)
		}
		writeServer := func(serverName string, rs []Rule, def bool) {
			fmt.Fprintf(&b, "server {\n  listen %d", l.Port)
			if l.Protocol == "HTTPS" {
				b.WriteString(" ssl")
			}
			if def {
				b.WriteString(" default_server")
			}
			b.WriteString(";\n")
			if l.Protocol == "HTTPS" {
				fmt.Fprintf(&b, "  http2 on;\n  ssl_certificate /etc/nginx/certs/%s.crt;\n  ssl_certificate_key /etc/nginx/certs/%s.key;\n", l.ID, l.ID)
				b.WriteString("  ssl_protocols TLSv1.2 TLSv1.3;\n  ssl_session_cache shared:hc:10m;\n")
			}
			if l.RedirectHTTPSPort > 0 {
				port := l.RedirectHTTPSPort
				if hp := lb.PublicPorts[fmt.Sprintf("%d/tcp", port)]; hp > 0 {
					port = hp
				}
				fmt.Fprintf(&b, "  location = /__hc_health { return 200 'ok'; }\n  location / { return 301 https://$host:%d$request_uri; }\n}\n", port)
				return
			}
			if serverName != "" {
				fmt.Fprintf(&b, "  server_name %s;\n", serverName)
			}
			b.WriteString("  access_log /dev/stdout hc;\n  client_max_body_size 100m;\n")
			b.WriteString("  location = /__hc_health { return 200 'ok'; }\n")
			hasRoot := false
			seen := map[string]bool{}
			for _, r := range rs {
				p := r.PathPrefix
				if p == "" {
					p = "/"
				}
				if seen[p] { // first rule by priority wins; nginx rejects duplicates
					continue
				}
				seen[p] = true
				if p == "/" {
					hasRoot = true
				}
				fmt.Fprintf(&b, "  location ^~ %s {\n%s  }\n", p, proxy(r.TargetGroup))
			}
			if !hasRoot {
				fmt.Fprintf(&b, "  location / {\n%s  }\n", proxy(l.DefaultTargetGroup))
			}
			b.WriteString("}\n")
		}
		writeServer("", hosts[""], true)
		for _, h := range hostOrder {
			if h != "" {
				writeServer(h, append(hosts[h], hosts[""]...), false)
			}
		}
	}
	return b.String()
}

// files returns the nginx configuration plus certificates for HTTPS listeners.
func (s *Service) files(lb LoadBalancer) map[string][]byte {
	out := map[string][]byte{strings.TrimPrefix(confIn, "/"): []byte(s.render(lb))}
	for _, l := range lb.Listeners {
		if l.Protocol != "HTTPS" || s.Certs == nil {
			continue
		}
		cert, key, err := s.Certs.KeyPair(l.CertificateARN)
		if err != nil {
			log.Printf("elb: %s: certificate %s: %v", lb.Name, l.CertificateARN, err)
			continue
		}
		out["etc/nginx/certs/"+l.ID+".crt"] = cert
		out["etc/nginx/certs/"+l.ID+".key"] = key
	}
	return out
}

// UsesCertificate reports whether any listener serves the certificate.
func (s *Service) UsesCertificate(arn string) bool {
	for _, lb := range store.List[LoadBalancer](s.env.Store, cLBs) {
		if slices.ContainsFunc(lb.Listeners, func(l Listener) bool { return l.CertificateARN == arn }) {
			return true
		}
	}
	return false
}

// CertificateRenewed pushes fresh certificate material to every balancer using it.
func (s *Service) CertificateRenewed(arn string) {
	for _, lb := range store.List[LoadBalancer](s.env.Store, cLBs) {
		if slices.ContainsFunc(lb.Listeners, func(l Listener) bool { return l.CertificateARN == arn }) {
			if err := s.push(context.Background(), lb.Name); err != nil {
				log.Printf("elb: reload %s after certificate renewal: %v", lb.Name, err)
			}
		}
	}
}

// push writes the configuration into the load balancer and reloads nginx.
func (s *Service) push(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lb, err := store.Get[LoadBalancer](s.env.Store, cLBs, name)
	if err != nil || lb.ContainerID == "" {
		return err
	}
	if err := s.env.Docker.CopyIn(ctx, lb.ContainerID, "/", s.files(lb), 0o600); err != nil {
		return err
	}
	if s.env.Docker.State(lb.ContainerID) != "running" {
		return nil
	}
	res, err := s.env.Docker.Exec(ctx, lb.ContainerID, []string{"nginx", "-s", "reload"}, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("nginx reload failed: %s", strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

// provision (re)creates the load balancer container; listener ports are fixed at creation.
func (s *Service) provision(lb LoadBalancer) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	fail := func(err error) {
		log.Printf("elb: %s: %v", lb.Name, err)
		_, _ = store.Update(s.env.Store, cLBs, lb.Name, func(x *LoadBalancer) error { x.State, x.StateReason = "failed", err.Error(); return nil })
	}
	if lb.ContainerID != "" {
		_ = s.env.Docker.Remove(lb.ContainerID)
	}
	v, err := s.vpc.GetVPC(lb.VpcID)
	if err != nil {
		fail(err)
		return
	}
	var ports []runtime.Port
	if lb.Scheme == "internet-facing" {
		for _, l := range lb.Listeners {
			ports = append(ports, runtime.Port{ContainerPort: l.Port, HostPort: l.PublicPort})
		}
	}
	cid, err := s.env.Docker.Run(ctx, runtime.RunSpec{
		Name: svc.ContainerName("elb", lb.Name), Image: lbImg, Labels: runtime.Labels("elb", lb.Name, nil),
		Network: v.Network, IP: lb.PrivateIP, Aliases: []string{lb.DNSName}, Ports: ports, Restart: "unless-stopped",
		MemoryMB: 256, NanoCPUs: 1e9,
	})
	if err != nil {
		fail(err)
		return
	}
	if _, err := store.Update(s.env.Store, cLBs, lb.Name, func(x *LoadBalancer) error { x.ContainerID = cid; return nil }); err != nil {
		_ = s.env.Docker.Remove(cid) // deleted while provisioning
		return
	}
	lb.ContainerID = cid
	if err := s.env.Docker.CopyIn(ctx, cid, "/", s.files(lb), 0o600); err != nil {
		fail(err)
		return
	}
	if err := s.env.Docker.Start(cid); err != nil {
		fail(err)
		return
	}
	_, _ = store.Update(s.env.Store, cLBs, lb.Name, func(x *LoadBalancer) error {
		x.State, x.StateReason, x.PublicPorts = "active", "", s.env.Docker.PublishedPorts(cid)
		return nil
	})
	// Redirects point at published host ports, which are only known now.
	if slices.ContainsFunc(lb.Listeners, func(l Listener) bool { return l.RedirectHTTPSPort > 0 }) {
		if err := s.push(ctx, lb.Name); err != nil {
			log.Printf("elb: %s: %v", lb.Name, err)
		}
	}
}

// ---- health checks ----

// Run health-checks every target group used by a load balancer.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	due := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, lb := range store.List[LoadBalancer](s.env.Store, cLBs) {
			if lb.State != "active" || lb.ContainerID == "" {
				continue
			}
			changed := false
			seen := map[string]bool{}
			for _, l := range lb.Listeners {
				tgs := []string{l.DefaultTargetGroup}
				for _, r := range l.Rules {
					tgs = append(tgs, r.TargetGroup)
				}
				for _, name := range tgs {
					if seen[name] {
						continue
					}
					seen[name] = true
					tg, err := store.Get[TargetGroup](s.env.Store, cTGs, name)
					if err != nil || time.Now().Before(due[lb.Name+"/"+name]) {
						continue
					}
					due[lb.Name+"/"+name] = time.Now().Add(time.Duration(tg.HealthCheck.IntervalSeconds) * time.Second)
					if s.check(ctx, lb, tg) {
						// Every balancer routing to this group needs the new healthy set.
						for _, other := range s.usedBy(tg.Name) {
							if other != lb.Name {
								if err := s.push(ctx, other); err != nil {
									log.Printf("elb: reload %s: %v", other, err)
								}
							}
						}
						changed = true
					}
				}
			}
			if changed {
				if err := s.push(ctx, lb.Name); err != nil {
					log.Printf("elb: reload %s: %v", lb.Name, err)
				}
			}
		}
	}
}

// check probes each target from inside the load balancer; reports whether any health state flipped.
// Health state is only read and written under hmu.
func (s *Service) check(ctx context.Context, lb LoadBalancer, tg TargetGroup) bool {
	changed := false
	for _, t := range tg.Targets {
		key := healthKey(tg.Name, t.ID, t.Port)
		ip, _, ok := s.resolve(t.ID)
		var probeErr string
		url := ""
		if ok {
			url = fmt.Sprintf("http://%s:%d%s", ip, t.Port, tg.HealthCheck.Path)
			cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			res, err := s.env.Docker.Exec(cctx, lb.ContainerID, []string{"wget", "-q", "-T", "4", "-O", "/dev/null", url}, nil)
			cancel()
			ok = err == nil && res.ExitCode == 0
			if res != nil {
				probeErr = strings.TrimSpace(res.Stderr)
			}
		}
		s.hmu.Lock()
		h := s.health[key]
		if h == nil {
			h = &Target{Health: "initial"}
			s.health[key] = h
		}
		prev := h.Health
		if url == "" {
			h.Health, h.Reason = "unavailable", "target has no private IP (stopped or terminated)"
		} else {
			if ok != h.lastGood {
				h.streak = 0
			}
			h.lastGood = ok
			h.streak++
			switch {
			case ok && h.Health != "healthy" && (h.streak >= tg.HealthCheck.HealthyThreshold || h.Health == "initial" || h.Health == "unavailable"):
				h.Health, h.Reason = "healthy", ""
			case !ok && h.Health != "unhealthy" && (h.streak >= tg.HealthCheck.UnhealthyThreshold || h.Health == "initial" || h.Health == "unavailable"):
				h.Health, h.Reason = "unhealthy", "health check to "+url+" failed"
				if probeErr != "" {
					h.Reason += ": " + probeErr
				}
			}
		}
		if prev != h.Health {
			changed = true
		}
		s.hmu.Unlock()
	}
	return changed
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	lbRes := httpx.Res("arn:hc:elasticloadbalancing:local-1:{account}:loadbalancer/app/{name}")
	tgRes := httpx.Res("arn:hc:elasticloadbalancing:local-1:{account}:targetgroup/{name}")
	r.Handle("GET /api/v1/elb/load-balancers", "elasticloadbalancing:DescribeLoadBalancers", s.listLBs)
	r.Handle("POST /api/v1/elb/load-balancers", "elasticloadbalancing:CreateLoadBalancer", s.createLB)
	r.Handle("GET /api/v1/elb/load-balancers/{name}", "elasticloadbalancing:DescribeLoadBalancers", s.getLB, lbRes)
	r.Handle("DELETE /api/v1/elb/load-balancers/{name}", "elasticloadbalancing:DeleteLoadBalancer", s.deleteLB, lbRes)
	r.Handle("POST /api/v1/elb/load-balancers/{name}/listeners", "elasticloadbalancing:CreateListener", s.addListener, lbRes)
	r.Handle("DELETE /api/v1/elb/load-balancers/{name}/listeners/{id}", "elasticloadbalancing:DeleteListener", s.deleteListener, lbRes)
	r.Handle("POST /api/v1/elb/load-balancers/{name}/listeners/{id}/rules", "elasticloadbalancing:CreateRule", s.addRule, lbRes)
	r.Handle("DELETE /api/v1/elb/load-balancers/{name}/listeners/{id}/rules/{rule}", "elasticloadbalancing:DeleteRule", s.deleteRule, lbRes)
	r.Handle("GET /api/v1/elb/load-balancers/{name}/config", "elasticloadbalancing:DescribeLoadBalancers", s.config, lbRes)
	r.Handle("GET /api/v1/elb/target-groups", "elasticloadbalancing:DescribeTargetGroups", s.listTGs)
	r.Handle("POST /api/v1/elb/target-groups", "elasticloadbalancing:CreateTargetGroup", s.createTG)
	r.Handle("GET /api/v1/elb/target-groups/{name}", "elasticloadbalancing:DescribeTargetHealth", s.getTG, tgRes)
	r.Handle("PATCH /api/v1/elb/target-groups/{name}", "elasticloadbalancing:ModifyTargetGroup", s.modifyTG, tgRes)
	r.Handle("DELETE /api/v1/elb/target-groups/{name}", "elasticloadbalancing:DeleteTargetGroup", s.deleteTG, tgRes)
	r.Handle("POST /api/v1/elb/target-groups/{name}/targets", "elasticloadbalancing:RegisterTargets", s.register, tgRes)
	r.Handle("DELETE /api/v1/elb/target-groups/{name}/targets/{target}", "elasticloadbalancing:DeregisterTargets", s.deregister, tgRes)
}

func (s *Service) lbView(lb LoadBalancer) LoadBalancer {
	if lb.ContainerID != "" && lb.State == "active" {
		if st := s.env.Docker.State(lb.ContainerID); st != "running" {
			lb.State, lb.StateReason = "failed", "load balancer container is "+st
		}
	}
	return lb
}

func (s *Service) listLBs(c *httpx.Ctx) (any, error) {
	out := []LoadBalancer{}
	for _, lb := range store.List[LoadBalancer](s.env.Store, cLBs) {
		out = append(out, s.lbView(lb))
	}
	return out, nil
}

func (s *Service) getLB(c *httpx.Ctx) (any, error) {
	lb, err := store.Get[LoadBalancer](s.env.Store, cLBs, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("load balancer", c.Param("name"))
	}
	return s.lbView(lb), nil
}

func (s *Service) config(c *httpx.Ctx) (any, error) {
	lb, err := store.Get[LoadBalancer](s.env.Store, cLBs, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("load balancer", c.Param("name"))
	}
	return map[string]string{"nginx_conf": s.render(lb)}, nil
}

// checkRedirects verifies that every redirect points at an HTTPS listener.
func checkRedirects(ls []Listener) error {
	for _, l := range ls {
		if l.RedirectHTTPSPort > 0 && !slices.ContainsFunc(ls, func(o Listener) bool { return o.Protocol == "HTTPS" && o.Port == l.RedirectHTTPSPort }) {
			return core.BadRequest("listener %d redirects to port %d, which has no HTTPS listener", l.Port, l.RedirectHTTPSPort)
		}
	}
	return nil
}

func (s *Service) checkListener(l *Listener, vpcID string, taken []Listener) error {
	if l.Port < 1 || l.Port > 65535 {
		return core.BadRequest("listener port must be 1-65535")
	}
	for _, o := range taken {
		if o.Port == l.Port {
			return core.Conflict("a listener already uses port %d", l.Port)
		}
	}
	l.Protocol = strings.ToUpper(l.Protocol)
	if l.Protocol == "" {
		l.Protocol = "HTTP"
	}
	switch l.Protocol {
	case "HTTP":
		l.CertificateARN = ""
		if l.RedirectHTTPSPort < 0 || l.RedirectHTTPSPort > 65535 {
			return core.BadRequest("redirect_https_port must be a port number")
		}
		if l.RedirectHTTPSPort > 0 && l.DefaultTargetGroup != "" {
			return core.BadRequest("a listener either redirects to HTTPS or forwards to a target group, not both")
		}
		if l.RedirectHTTPSPort > 0 {
			l.ID = core.RandHex(12)
			l.Rules = []Rule{}
			return nil // a pure redirect listener needs no target group
		}
	case "HTTPS":
		if s.Certs == nil || !s.Certs.Exists(l.CertificateARN) {
			return core.BadRequest("HTTPS listeners need a valid certificate_arn (see ACM)")
		}
		l.RedirectHTTPSPort = 0
	default:
		return core.BadRequest("protocol must be HTTP or HTTPS")
	}
	tg, err := store.Get[TargetGroup](s.env.Store, cTGs, l.DefaultTargetGroup)
	if err != nil {
		return core.NotFound("target group", l.DefaultTargetGroup)
	}
	if tg.VpcID != vpcID {
		return core.BadRequest("target group %s is in %s, not %s", tg.Name, tg.VpcID, vpcID)
	}
	l.ID = core.RandHex(12)
	if l.Rules == nil {
		l.Rules = []Rule{}
	}
	return nil
}

func (s *Service) createLB(c *httpx.Ctx) (any, error) {
	var in struct {
		Name      string     `json:"name"`
		Scheme    string     `json:"scheme"`
		SubnetID  string     `json:"subnet_id"`
		Listeners []Listener `json:"listeners"`
		Tags      core.Tags  `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !nameRe.MatchString(in.Name) {
		return nil, core.BadRequest("load balancer names are up to 32 letters, digits and hyphens")
	}
	if store.Has(s.env.Store, cLBs, in.Name) {
		return nil, core.Conflict("load balancer %q already exists", in.Name)
	}
	if in.Scheme == "" {
		in.Scheme = "internet-facing"
	}
	if in.Scheme != "internet-facing" && in.Scheme != "internal" {
		return nil, core.BadRequest("scheme must be internet-facing or internal")
	}
	if len(in.Listeners) == 0 {
		return nil, core.BadRequest("at least one listener is required")
	}
	pl, err := s.vpc.Place(in.SubnetID, "elb:"+in.Name)
	if err != nil {
		return nil, err
	}
	var ls []Listener
	for _, l := range in.Listeners {
		if err := s.checkListener(&l, pl.VPC.ID, ls); err != nil {
			s.vpc.Release("elb:" + in.Name)
			return nil, err
		}
		ls = append(ls, l)
	}
	if err := checkRedirects(ls); err != nil {
		s.vpc.Release("elb:" + in.Name)
		return nil, err
	}
	lb := LoadBalancer{Name: in.Name, ARN: s.env.ARN("elasticloadbalancing", "loadbalancer/app/"+in.Name), DNSName: in.Name + ".elb.internal",
		Scheme: in.Scheme, VpcID: pl.VPC.ID, SubnetID: pl.Subnet.ID, PrivateIP: pl.IP, Listeners: ls, State: "provisioning",
		PublicPorts: map[string]int{}, PublicHost: s.env.Cfg.PublicHost, CreatedAt: core.Now(), Tags: in.Tags}
	if err := store.Put(s.env.Store, cLBs, lb.Name, lb); err != nil {
		return nil, err
	}
	go s.provision(lb)
	return lb, nil
}

func (s *Service) deleteLB(c *httpx.Ctx) (any, error) {
	lb, err := store.Get[LoadBalancer](s.env.Store, cLBs, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("load balancer", c.Param("name"))
	}
	if lb.ContainerID != "" {
		if err := s.env.Docker.Remove(lb.ContainerID); err != nil {
			return nil, err
		}
	}
	s.vpc.Release("elb:" + lb.Name)
	return nil, store.Delete(s.env.Store, cLBs, lb.Name)
}

// changeListeners edits listeners; port changes need a new container.
func (s *Service) changeListeners(name string, recreate bool, fn func(lb *LoadBalancer) error) (any, error) {
	before, err := store.Get[LoadBalancer](s.env.Store, cLBs, name)
	if err != nil {
		return nil, core.NotFound("load balancer", name)
	}
	lb, err := store.Update(s.env.Store, cLBs, name, fn)
	if err == store.ErrNotFound {
		return nil, core.NotFound("load balancer", name)
	}
	if err != nil {
		return nil, err
	}
	if recreate {
		lb, _ = store.Update(s.env.Store, cLBs, name, func(x *LoadBalancer) error { x.State = "provisioning"; return nil })
		go s.provision(lb)
		return lb, nil
	}
	if err := s.push(context.Background(), name); err != nil {
		// nginx rejected the configuration: restore the previous listeners.
		_ = store.Put(s.env.Store, cLBs, name, before)
		_ = s.push(context.Background(), name)
		return nil, core.Errf(http.StatusBadRequest, "InvalidConfigurationRequest", "the load balancer rejected this change: %v", err)
	}
	return lb, nil
}

func (s *Service) addListener(c *httpx.Ctx) (any, error) {
	var l Listener
	if err := c.Bind(&l); err != nil {
		return nil, err
	}
	return s.changeListeners(c.Param("name"), true, func(lb *LoadBalancer) error {
		if err := s.checkListener(&l, lb.VpcID, lb.Listeners); err != nil {
			return err
		}
		lb.Listeners = append(lb.Listeners, l)
		return checkRedirects(lb.Listeners)
	})
}

func (s *Service) deleteListener(c *httpx.Ctx) (any, error) {
	return s.changeListeners(c.Param("name"), true, func(lb *LoadBalancer) error {
		n := len(lb.Listeners)
		lb.Listeners = slices.DeleteFunc(lb.Listeners, func(l Listener) bool { return l.ID == c.Param("id") })
		if len(lb.Listeners) == n {
			return core.NotFound("listener", c.Param("id"))
		}
		if len(lb.Listeners) == 0 {
			return core.BadRequest("a load balancer needs at least one listener")
		}
		return checkRedirects(lb.Listeners) // don't strand a redirect
	})
}

func (s *Service) addRule(c *httpx.Ctx) (any, error) {
	var r Rule
	if err := c.Bind(&r); err != nil {
		return nil, err
	}
	if r.PathPrefix == "" && r.HostHeader == "" {
		return nil, core.BadRequest("a rule needs a path_prefix or host_header condition")
	}
	if r.PathPrefix != "" && !pathRe.MatchString(r.PathPrefix) {
		return nil, core.BadRequest("path_prefix must start with / and use only letters, digits and . _ ~ %% / * -")
	}
	if r.HostHeader != "" && !hostRe.MatchString(r.HostHeader) {
		return nil, core.BadRequest("host_header must be a host name, optionally starting with *.")
	}
	return s.changeListeners(c.Param("name"), false, func(lb *LoadBalancer) error {
		tg, err := store.Get[TargetGroup](s.env.Store, cTGs, r.TargetGroup)
		if err != nil {
			return core.NotFound("target group", r.TargetGroup)
		}
		if tg.VpcID != lb.VpcID {
			return core.BadRequest("target group %s is in another VPC", tg.Name)
		}
		for i := range lb.Listeners {
			if lb.Listeners[i].ID == c.Param("id") {
				for _, o := range lb.Listeners[i].Rules {
					if o.PathPrefix == r.PathPrefix && o.HostHeader == r.HostHeader {
						return core.Conflict("listener already has a rule for host %q and path %q", r.HostHeader, r.PathPrefix)
					}
					if r.Priority != 0 && o.Priority == r.Priority {
						return core.Conflict("listener already has a rule with priority %d", r.Priority)
					}
				}
				r.ID = core.RandHex(12)
				if r.Priority == 0 {
					r.Priority = len(lb.Listeners[i].Rules) + 1
				}
				lb.Listeners[i].Rules = append(lb.Listeners[i].Rules, r)
				return nil
			}
		}
		return core.NotFound("listener", c.Param("id"))
	})
}

func (s *Service) deleteRule(c *httpx.Ctx) (any, error) {
	return s.changeListeners(c.Param("name"), false, func(lb *LoadBalancer) error {
		for i := range lb.Listeners {
			if lb.Listeners[i].ID == c.Param("id") {
				n := len(lb.Listeners[i].Rules)
				lb.Listeners[i].Rules = slices.DeleteFunc(lb.Listeners[i].Rules, func(r Rule) bool { return r.ID == c.Param("rule") })
				if len(lb.Listeners[i].Rules) == n {
					return core.NotFound("rule", c.Param("rule"))
				}
				return nil
			}
		}
		return core.NotFound("listener", c.Param("id"))
	})
}

// ---- target groups ----

func (s *Service) tgView(tg TargetGroup) TargetGroup {
	for i, t := range tg.Targets {
		if ip, _, ok := s.resolve(t.ID); ok {
			tg.Targets[i].IP = ip
		} else {
			tg.Targets[i].IP = ""
		}
		if h := s.healthOf(tg.Name, t.ID, t.Port); h != nil {
			tg.Targets[i].Health, tg.Targets[i].Reason = h.Health, h.Reason
		} else {
			tg.Targets[i].Health, tg.Targets[i].Reason = "unused", "not attached to an active load balancer"
		}
	}
	return tg
}

func (s *Service) resolve(id string) (string, string, bool) {
	if s.Resolve != nil {
		if ip, v, ok := s.Resolve(id); ok {
			return ip, v, true
		}
	}
	return "", "", false
}

func (s *Service) listTGs(c *httpx.Ctx) (any, error) {
	out := []TargetGroup{}
	for _, tg := range store.List[TargetGroup](s.env.Store, cTGs) {
		out = append(out, s.tgView(tg))
	}
	return out, nil
}

func (s *Service) getTG(c *httpx.Ctx) (any, error) {
	tg, err := store.Get[TargetGroup](s.env.Store, cTGs, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("target group", c.Param("name"))
	}
	return s.tgView(tg), nil
}

func normalizeHC(h *HealthCheck) error {
	if h.Path == "" {
		h.Path = "/"
	}
	if !strings.HasPrefix(h.Path, "/") || strings.ContainsAny(h.Path, " \n") {
		return core.BadRequest("health check path must start with /")
	}
	if h.IntervalSeconds == 0 {
		h.IntervalSeconds = 15
	}
	if h.IntervalSeconds < 5 || h.IntervalSeconds > 300 {
		return core.BadRequest("interval_seconds must be 5-300")
	}
	if h.HealthyThreshold == 0 {
		h.HealthyThreshold = 2
	}
	if h.UnhealthyThreshold == 0 {
		h.UnhealthyThreshold = 2
	}
	return nil
}

func (s *Service) createTG(c *httpx.Ctx) (any, error) {
	var in TargetGroup
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !nameRe.MatchString(in.Name) {
		return nil, core.BadRequest("target group names are up to 32 letters, digits and hyphens")
	}
	if store.Has(s.env.Store, cTGs, in.Name) {
		return nil, core.Conflict("target group %q already exists", in.Name)
	}
	if in.Port < 1 || in.Port > 65535 {
		return nil, core.BadRequest("port must be 1-65535")
	}
	if in.VpcID == "" {
		in.VpcID = s.vpc.DefaultVPCID()
	}
	if _, err := s.vpc.GetVPC(in.VpcID); err != nil {
		return nil, core.NotFound("vpc", in.VpcID)
	}
	if err := normalizeHC(&in.HealthCheck); err != nil {
		return nil, err
	}
	tg := TargetGroup{Name: in.Name, ARN: s.env.ARN("elasticloadbalancing", "targetgroup/"+in.Name), Protocol: "HTTP", Port: in.Port,
		VpcID: in.VpcID, HealthCheck: in.HealthCheck, Targets: []Target{}, CreatedAt: core.Now()}
	return tg, store.Put(s.env.Store, cTGs, tg.Name, tg)
}

func (s *Service) modifyTG(c *httpx.Ctx) (any, error) {
	var in HealthCheck
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := normalizeHC(&in); err != nil {
		return nil, err
	}
	tg, err := store.Update(s.env.Store, cTGs, c.Param("name"), func(t *TargetGroup) error { t.HealthCheck = in; return nil })
	if err == store.ErrNotFound {
		return nil, core.NotFound("target group", c.Param("name"))
	}
	return s.tgView(tg), err
}

func (s *Service) usedBy(tg string) []string {
	var out []string
	for _, lb := range store.List[LoadBalancer](s.env.Store, cLBs) {
		for _, l := range lb.Listeners {
			if l.DefaultTargetGroup == tg || slices.ContainsFunc(l.Rules, func(r Rule) bool { return r.TargetGroup == tg }) {
				out = append(out, lb.Name)
				break
			}
		}
	}
	return out
}

func (s *Service) deleteTG(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if !store.Has(s.env.Store, cTGs, name) {
		return nil, core.NotFound("target group", name)
	}
	if u := s.usedBy(name); len(u) > 0 {
		return nil, core.Errf(http.StatusConflict, "ResourceInUse", "target group %s is used by %s", name, strings.Join(u, ", "))
	}
	return nil, store.Delete(s.env.Store, cTGs, name)
}

// pushUsers reloads every load balancer that routes to a target group.
func (s *Service) pushUsers(tg string) {
	for _, lb := range s.usedBy(tg) {
		if err := s.push(context.Background(), lb); err != nil {
			log.Printf("elb: reload %s: %v", lb, err)
		}
	}
}

func (s *Service) register(c *httpx.Ctx) (any, error) {
	var in struct {
		Targets []Target `json:"targets"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	tg, err := store.Update(s.env.Store, cTGs, c.Param("name"), func(tg *TargetGroup) error {
		for _, t := range in.Targets {
			ip, vpcID, ok := s.resolve(t.ID)
			if !ok {
				return core.BadRequest("target %q is not a known instance or task", t.ID)
			}
			if vpcID != tg.VpcID {
				return core.BadRequest("target %s is in %s, not the target group's VPC %s", t.ID, vpcID, tg.VpcID)
			}
			if t.Port == 0 {
				t.Port = tg.Port
			}
			t.IP = ip
			tg.Targets = slices.DeleteFunc(tg.Targets, func(x Target) bool { return x.ID == t.ID && x.Port == t.Port })
			tg.Targets = append(tg.Targets, Target{ID: t.ID, Port: t.Port, IP: ip})
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("target group", c.Param("name"))
	}
	if err != nil {
		return nil, err
	}
	s.pushUsers(tg.Name)
	return s.tgView(tg), nil
}

func (s *Service) deregister(c *httpx.Ctx) (any, error) {
	tg, err := store.Update(s.env.Store, cTGs, c.Param("name"), func(tg *TargetGroup) error {
		n := len(tg.Targets)
		tg.Targets = slices.DeleteFunc(tg.Targets, func(t Target) bool { return t.ID == c.Param("target") })
		if len(tg.Targets) == n {
			return core.NotFound("target", c.Param("target"))
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("target group", c.Param("name"))
	}
	if err != nil {
		return nil, err
	}
	s.forget(tg.Name, c.Param("target"))
	s.pushUsers(tg.Name)
	return s.tgView(tg), nil
}

// Refresh re-resolves target IPs (e.g. after instances restart) and reloads balancers.
func (s *Service) Refresh(targetID string) {
	for _, tg := range store.List[TargetGroup](s.env.Store, cTGs) {
		if slices.ContainsFunc(tg.Targets, func(t Target) bool { return t.ID == targetID }) {
			s.pushUsers(tg.Name)
		}
	}
}

// SetTarget registers (add=true) or deregisters a target programmatically, e.g. for ECS tasks.
func (s *Service) SetTarget(tgName, id string, port int, add bool) error {
	_, err := store.Update(s.env.Store, cTGs, tgName, func(tg *TargetGroup) error {
		tg.Targets = slices.DeleteFunc(tg.Targets, func(t Target) bool { return t.ID == id })
		if add {
			if port == 0 {
				port = tg.Port
			}
			tg.Targets = append(tg.Targets, Target{ID: id, Port: port})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !add {
		s.forget(tgName, id)
	}
	s.pushUsers(tgName)
	return nil
}

// TargetIDs returns the IDs of every registered target.
func (s *Service) TargetIDs() []string {
	var out []string
	for _, tg := range store.List[TargetGroup](s.env.Store, cTGs) {
		for _, t := range tg.Targets {
			out = append(out, t.ID)
		}
	}
	return out
}

// DropTarget deregisters a target (e.g. a terminated instance) from every target group.
func (s *Service) DropTarget(id string) {
	for _, tg := range store.List[TargetGroup](s.env.Store, cTGs) {
		if slices.ContainsFunc(tg.Targets, func(t Target) bool { return t.ID == id }) {
			_ = s.SetTarget(tg.Name, id, 0, false)
		}
	}
}

// TargetGroupVPC returns the VPC of a target group.
func (s *Service) TargetGroupVPC(name string) (string, bool) {
	tg, err := store.Get[TargetGroup](s.env.Store, cTGs, name)
	return tg.VpcID, err == nil
}

// EnsureTarget registers id with a target group if it is not already registered.
func (s *Service) EnsureTarget(tgName, id string) error {
	tg, err := store.Get[TargetGroup](s.env.Store, cTGs, tgName)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(tg.Targets, func(t Target) bool { return t.ID == id }) {
		return nil
	}
	return s.SetTarget(tgName, id, 0, true)
}

// PrivateIP resolves a load balancer name to its private IP (for DNS aliases).
func (s *Service) PrivateIP(name string) (string, bool) {
	lb, err := store.Get[LoadBalancer](s.env.Store, cLBs, name)
	if err != nil || lb.State != "active" {
		return "", false
	}
	return lb.PrivateIP, true
}

// Recover re-provisions load balancers whose provisioning was interrupted by a restart.
func (s *Service) Recover() {
	for _, lb := range store.List[LoadBalancer](s.env.Store, cLBs) {
		if lb.State == "provisioning" {
			go s.provision(lb)
		}
	}
}
