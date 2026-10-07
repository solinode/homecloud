package ec2

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// The instance metadata service (IMDSv1 and IMDSv2) at 169.254.169.254.
//
// One helper container, homecloud-imds (nginx), is attached to every VPC
// network at the VPC's metadata address (vpc.MetadataAddress, which the
// allocator never hands out) and also owns 169.254.169.254 on its loopback.
// Each instance gets a host route "169.254.169.254 via <metadata address>",
// added from outside by a short-lived container sharing the instance's network
// namespace with NET_ADMIN (the instance itself gets no extra capability).
// The helper forwards requests to HomeCloud's /_imds/ endpoint with the
// caller's address and a per-server secret; HomeCloud identifies the instance
// by its private address (VPC ranges never overlap) and answers from its
// record, issuing the instance profile role's credentials through IAM.

const (
	imdsContainer = "homecloud-imds"
	imdsImage     = "nginx:alpine"
	imdsIP        = "169.254.169.254"
	// IMDSPath is where HomeCloud serves the metadata service to the helper.
	IMDSPath = "/_imds/"

	credTTL     = 6 * time.Hour
	credRefresh = time.Hour // hand out fresh credentials when less than this is left
)

type imds struct {
	s      *Service
	key    string
	mu     sync.Mutex
	tokens map[string]imdsToken
	creds  map[string]cachedCred // by instance ID
}

type imdsToken struct {
	instance string
	expires  time.Time
}

type cachedCred struct {
	role    string
	cred    Credentials
	updated time.Time
}

func (m *imds) forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.creds, id)
	for t, v := range m.tokens {
		if v.instance == id {
			delete(m.tokens, t)
		}
	}
}

func (s *Service) imdsName() string {
	if s.IMDSContainer != "" {
		return s.IMDSContainer
	}
	return imdsContainer
}

func (s *Service) initIMDS() {
	if s.imds == nil {
		s.imds = &imds{s: s, key: core.RandHex(40), tokens: map[string]imdsToken{}, creds: map[string]cachedCred{}}
	}
}

// IMDSHandler serves the metadata service to the helper container.
func (s *Service) IMDSHandler() http.Handler {
	s.initIMDS()
	return s.imds
}

// RunIMDS keeps the metadata helper running and attached to every VPC, and
// routes the metadata address in instances that are already running.
func (s *Service) RunIMDS(ctx context.Context) {
	s.initIMDS()
	if err := s.ensureIMDS(ctx); err != nil {
		log.Printf("ec2: instance metadata service: %v", err)
	}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.State == "running" && i.ContainerID != "" {
			s.metadataRoute(ctx, i.ContainerID, i.VpcID)
		}
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.ensureIMDS(ctx); err != nil {
				log.Printf("ec2: instance metadata service: %v", err)
			}
			s.imds.prune()
		}
	}
}

// VPCCreated attaches the metadata helper to a new VPC.
func (s *Service) VPCCreated(v vpc.VPC) {
	_ = s.env.Docker.ConnectIP(v.Network, s.imdsName(), vpc.MetadataAddress(v.CIDR))
}

// NetworkChanged re-routes the metadata address in a VPC's running instances
// after its network was recreated.
func (s *Service) NetworkChanged(v vpc.VPC) {
	_ = s.env.Docker.ConnectIP(v.Network, s.imdsName(), vpc.MetadataAddress(v.CIDR))
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.VpcID == v.ID && i.State == "running" && i.ContainerID != "" {
			s.metadataRoute(context.Background(), i.ContainerID, i.VpcID)
		}
	}
}

func (m *imds) prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for t, v := range m.tokens {
		if now.After(v.expires) {
			delete(m.tokens, t)
		}
	}
}

func (s *Service) imdsConfig() string {
	api := s.env.ContainerAPI
	if api == "" {
		api = "http://" + strings.Split(runtime.HostAlias, ":")[0] + ":8080"
	}
	if s.env.Docker.Self() != "" {
		// HomeCloud runs in a container: name its current address on the default
		// bridge, where the helper runs (it changes when HomeCloud restarts, and
		// the helper's configuration is refreshed).
		if ip, err := s.env.Docker.SelfIP(""); err == nil {
			api = strings.Replace(api, runtime.HostAliasName, ip, 1)
		}
	}
	return fmt.Sprintf(`worker_processes 1;
error_log /dev/stderr warn;
events { worker_connections 512; }
http {
  access_log off;
  server {
    listen 80;
    location / {
      proxy_pass %s%s;
      proxy_set_header X-HC-IMDS-Key "%s";
      proxy_set_header X-HC-IMDS-Client $remote_addr;
      proxy_connect_timeout 5s;
      proxy_read_timeout 30s;
    }
  }
}
`, strings.TrimSuffix(api, "/"), IMDSPath, s.imds.key)
}

// ensureIMDS starts the helper (or refreshes its configuration) and attaches
// it to every VPC network.
func (s *Service) ensureIMDS(ctx context.Context) error {
	conf := []byte(s.imdsConfig())
	c, err := s.env.Docker.Inspect(s.imdsName())
	if err == nil && c.State.Running {
		// Reload only when the configuration (the secret) changed.
		res, err := s.env.Docker.Exec(ctx, c.ID, []string{"cat", "/etc/nginx/nginx.conf"}, nil)
		if err != nil || res.Stdout != string(conf) {
			if err := s.env.Docker.CopyIn(ctx, c.ID, "/etc/nginx", map[string][]byte{"nginx.conf": conf}, 0o644); err != nil {
				return err
			}
			if _, err := s.env.Docker.Exec(ctx, c.ID, []string{"nginx", "-s", "reload"}, nil); err != nil {
				return err
			}
		}
	} else {
		if err == nil {
			_ = s.env.Docker.Remove(c.ID)
		}
		id, err := s.env.Docker.Run(ctx, runtime.RunSpec{
			Name: s.imdsName(), Image: imdsImage,
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{"ip addr add " + imdsIP + "/32 dev lo 2>/dev/null; exec nginx -g 'daemon off;'"},
			Labels:     runtime.Labels("ec2", "server", map[string]string{"homecloud.name": "instance metadata service"}),
			MemoryMB:   64, Restart: "unless-stopped", CapAdd: []string{"NET_ADMIN"},
			ExtraHosts: []string{runtime.HostAlias},
		})
		if err != nil {
			return err
		}
		if err := s.env.Docker.CopyIn(ctx, id, "/etc/nginx", map[string][]byte{"nginx.conf": conf}, 0o644); err != nil {
			_ = s.env.Docker.Remove(id)
			return err
		}
		if err := s.env.Docker.Start(id); err != nil {
			_ = s.env.Docker.Remove(id)
			return err
		}
	}
	for _, v := range s.vpc.List() {
		if err := s.env.Docker.ConnectIP(v.Network, s.imdsName(), vpc.MetadataAddress(v.CIDR)); err != nil {
			log.Printf("ec2: attach metadata service to %s: %v", v.ID, err)
		}
	}
	return nil
}

// metadataRoute routes 169.254.169.254 inside a running instance's network
// namespace to the VPC's metadata address.
func (s *Service) metadataRoute(ctx context.Context, cid, vpcID string) {
	v, err := s.vpc.GetVPC(vpcID)
	if err != nil {
		return
	}
	if err := s.env.Docker.EnsureImage(ctx, helperImage); err != nil {
		log.Printf("ec2: metadata route: %v", err)
		return
	}
	c, err := s.env.Docker.C.CreateContainer(docker.CreateContainerOptions{Context: ctx,
		Config:     &docker.Config{Image: helperImage, Cmd: []string{"ip", "route", "replace", imdsIP + "/32", "via", vpc.MetadataAddress(v.CIDR)}, Labels: runtime.Labels("ec2", "helper", nil)},
		HostConfig: &docker.HostConfig{NetworkMode: "container:" + cid, CapAdd: []string{"NET_ADMIN"}},
	})
	if err != nil {
		log.Printf("ec2: metadata route: %v", err)
		return
	}
	defer func() { _ = s.env.Docker.Remove(c.ID) }()
	if err := s.env.Docker.C.StartContainerWithContext(c.ID, nil, ctx); err != nil {
		log.Printf("ec2: metadata route: %v", err)
		return
	}
	if code, err := s.env.Docker.C.WaitContainerWithContext(c.ID, ctx); err != nil || code != 0 {
		out, _ := s.env.Docker.Logs(c.ID, 20, time.Time{})
		log.Printf("ec2: metadata route in %s: exit %d %v %s", cid[:12], code, err, out)
	}
}

// ---- the metadata service ----

func (m *imds) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-HC-IMDS-Key")), []byte(m.key)) != 1 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	inst, ok := m.s.instanceByIP(r.Header.Get("X-HC-IMDS-Client"))
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	md := inst.Metadata.withDefaults()
	if md.HttpEndpoint == "disabled" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	path := "/" + strings.TrimPrefix(r.URL.Path, IMDSPath)
	w.Header().Set("Server", "EC2ws")
	if path == "/latest/api/token" {
		m.token(w, r, inst)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if tok := r.Header.Get("X-aws-ec2-metadata-token"); tok != "" {
		m.mu.Lock()
		t, ok := m.tokens[tok]
		m.mu.Unlock()
		if !ok || t.instance != inst.ID || time.Now().After(t.expires) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	} else if md.HttpTokens == "required" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	m.serve(w, path, inst)
}

func (m *imds) token(w http.ResponseWriter, r *http.Request, inst Instance) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "PUT")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	// As in EC2, token requests that went through a proxy are refused.
	if r.Header.Get("X-Forwarded-For") != "" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	ttl, err := strconv.Atoi(r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds"))
	if err != nil || ttl < 1 || ttl > 21600 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	tok := base64.RawURLEncoding.EncodeToString([]byte(core.RandHex(48)))
	m.mu.Lock()
	m.tokens[tok] = imdsToken{instance: inst.ID, expires: time.Now().Add(time.Duration(ttl) * time.Second)}
	m.mu.Unlock()
	w.Header().Set("X-aws-ec2-metadata-token-ttl-seconds", strconv.Itoa(ttl))
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(tok))
}

func (s *Service) instanceByIP(ip string) (Instance, bool) {
	if ip == "" {
		return Instance{}, false
	}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.PrivateIP == ip && i.State != "terminated" {
			return i, true
		}
	}
	return Instance{}, false
}

func (m *imds) serve(w http.ResponseWriter, path string, inst Instance) {
	if path == "/" {
		writeText(w, "latest")
		return
	}
	if !strings.HasPrefix(path, "/latest") {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(path, "/latest"), "/")
	switch {
	case rest == "":
		items := []string{"dynamic", "meta-data"}
		if inst.UserData != "" {
			items = append(items, "user-data")
		}
		writeText(w, strings.Join(items, "\n"))
		return
	case rest == "user-data":
		if inst.UserData == "" {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(inst.UserData))
		return
	}
	var tree map[string]string
	switch {
	case rest == "dynamic" || strings.HasPrefix(rest, "dynamic/"):
		tree = m.dynamic(inst)
	case rest == "meta-data" || strings.HasPrefix(rest, "meta-data/"):
		tree = m.metaData(inst)
		if strings.HasPrefix(rest, "meta-data/iam/security-credentials/") && strings.TrimSuffix(strings.TrimPrefix(rest, "meta-data/iam/security-credentials/"), "/") != "" {
			m.credentials(w, rest, inst)
			return
		}
	default:
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if v, ok := tree[rest]; ok {
		if strings.HasPrefix(v, "{") {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}
		_, _ = w.Write([]byte(v))
		return
	}
	// A directory: list its children, subdirectories with a trailing slash.
	dir := strings.TrimSuffix(rest, "/") + "/"
	seen := map[string]bool{}
	var items []string
	for k := range tree {
		if !strings.HasPrefix(k, dir) {
			continue
		}
		child := strings.TrimPrefix(k, dir)
		if n := strings.IndexByte(child, '/'); n >= 0 {
			child = child[:n+1]
		}
		if !seen[child] {
			seen[child] = true
			items = append(items, child)
		}
	}
	if len(items) == 0 {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	sort.Strings(items)
	if dir == "meta-data/public-keys/" && inst.KeyName != "" {
		items = []string{"0=" + inst.KeyName}
	}
	writeText(w, strings.Join(items, "\n"))
}

func writeText(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(s))
}

func (m *imds) mac(inst Instance) string {
	if c, err := m.s.env.Docker.Inspect(inst.ContainerID); err == nil && c.NetworkSettings != nil {
		for _, n := range c.NetworkSettings.Networks {
			if n.IPAddress == inst.PrivateIP && n.MacAddress != "" {
				return n.MacAddress
			}
		}
	}
	if a, err := netip.ParseAddr(inst.PrivateIP); err == nil && a.Is4() {
		b := a.As4()
		return fmt.Sprintf("02:42:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3])
	}
	return "02:42:00:00:00:00"
}

func (m *imds) metaData(inst Instance) map[string]string {
	s := m.s
	mac := m.mac(inst)
	t := map[string]string{
		"meta-data/ami-id":                         inst.ImageID,
		"meta-data/ami-launch-index":               strconv.Itoa(inst.LaunchIndex),
		"meta-data/ami-manifest-path":              "(unknown)",
		"meta-data/block-device-mapping/ami":       "/dev/xvda",
		"meta-data/block-device-mapping/root":      "/dev/xvda",
		"meta-data/hostname":                       inst.PrivateDNS,
		"meta-data/instance-action":                "none",
		"meta-data/instance-id":                    inst.ID,
		"meta-data/instance-life-cycle":            "on-demand",
		"meta-data/instance-type":                  inst.InstanceType,
		"meta-data/local-hostname":                 inst.PrivateDNS,
		"meta-data/local-ipv4":                     inst.PrivateIP,
		"meta-data/mac":                            mac,
		"meta-data/placement/availability-zone":    inst.AvailabilityZone,
		"meta-data/placement/availability-zone-id": azID(inst.AvailabilityZone),
		"meta-data/placement/region":               s.env.Cfg.Region,
		"meta-data/profile":                        "default-hvm",
		"meta-data/reservation-id":                 inst.ReservationID,
		"meta-data/services/domain":                "amazonaws.com",
		"meta-data/services/partition":             "aws",
	}
	names := []string{}
	for _, g := range inst.SecurityGroups {
		if sg, err := s.vpc.GetSecurityGroup(g); err == nil {
			names = append(names, sg.Name)
		}
	}
	t["meta-data/security-groups"] = strings.Join(names, "\n")
	for n, a := range inst.Volumes {
		if a.Device != "" {
			t["meta-data/block-device-mapping/ebs"+strconv.Itoa(n+1)] = strings.TrimPrefix(a.Device, "/dev/")
		}
	}
	macp := "meta-data/network/interfaces/macs/" + mac + "/"
	t[macp+"device-number"] = "0"
	t[macp+"interface-id"] = eniID(inst.ID)
	t[macp+"local-hostname"] = inst.PrivateDNS
	t[macp+"local-ipv4s"] = inst.PrivateIP
	t[macp+"mac"] = mac
	t[macp+"owner-id"] = s.env.AccountID
	t[macp+"security-group-ids"] = strings.Join(inst.SecurityGroups, "\n")
	t[macp+"security-groups"] = strings.Join(names, "\n")
	t[macp+"subnet-id"] = inst.SubnetID
	t[macp+"vpc-id"] = inst.VpcID
	if sn, err := s.vpc.GetSubnet(inst.SubnetID); err == nil {
		t[macp+"subnet-ipv4-cidr-block"] = sn.CIDR
	}
	if v, err := s.vpc.GetVPC(inst.VpcID); err == nil {
		t[macp+"vpc-ipv4-cidr-block"] = v.CIDR
		t[macp+"vpc-ipv4-cidr-blocks"] = v.CIDR
	}
	if ip := s.publicIP(inst); ip != "" {
		t["meta-data/public-ipv4"] = ip
		t[macp+"public-ipv4s"] = ip
		if inst.PublicHost != "" {
			t["meta-data/public-hostname"] = inst.PublicHost
		}
	}
	if inst.KeyName != "" {
		if k, err := s.keyPair(inst.KeyName); err == nil {
			t["meta-data/public-keys/0/openssh-key"] = k.PublicKey
		}
	}
	if inst.IAMProfileARN != "" {
		info, _ := json.MarshalIndent(map[string]string{"Code": "Success", "LastUpdated": time.Now().UTC().Format(time.RFC3339),
			"InstanceProfileArn": inst.IAMProfileARN, "InstanceProfileId": inst.IAMProfileID}, "", "  ")
		t["meta-data/iam/info"] = string(info)
		if role := m.roleName(inst); role != "" {
			t["meta-data/iam/security-credentials/"+role] = "{}" // served by credentials()
		}
	}
	if inst.Metadata.withDefaults().InstanceMetadataTags == "enabled" {
		for k, v := range s.tagsOf(inst.ID, inst.Name, inst.Tags) {
			t["meta-data/tags/instance/"+k] = v
		}
	}
	return t
}

func (m *imds) dynamic(inst Instance) map[string]string {
	arch := hostArch(m.s)
	doc, _ := json.MarshalIndent(map[string]any{
		"accountId": m.s.env.AccountID, "architecture": arch, "availabilityZone": inst.AvailabilityZone,
		"billingProducts": nil, "devpayProductCodes": nil, "marketplaceProductCodes": nil,
		"imageId": inst.ImageID, "instanceId": inst.ID, "instanceType": inst.InstanceType, "kernelId": nil,
		"pendingTime": inst.LaunchTime.UTC().Format(time.RFC3339), "privateIp": inst.PrivateIP, "ramdiskId": nil,
		"region": m.s.env.Cfg.Region, "version": "2017-09-30",
	}, "", "  ")
	return map[string]string{"dynamic/instance-identity/document": string(doc)}
}

func (m *imds) roleName(inst Instance) string {
	if inst.IAMProfileARN == "" || m.s.Roles == nil {
		return ""
	}
	_, _, role, err := m.s.Roles.InstanceProfile(inst.IAMProfileARN)
	if err != nil || role == "" {
		return ""
	}
	return role[strings.LastIndexByte(role, '/')+1:]
}

// credentials serves iam/security-credentials/<role>: temporary credentials
// for the instance profile's role, renewed an hour before they expire.
func (m *imds) credentials(w http.ResponseWriter, rest string, inst Instance) {
	name := strings.TrimSuffix(strings.TrimPrefix(rest, "meta-data/iam/security-credentials/"), "/")
	if inst.IAMProfileARN == "" || m.s.Roles == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	_, _, roleARN, err := m.s.Roles.InstanceProfile(inst.IAMProfileARN)
	if err != nil || roleARN == "" || roleARN[strings.LastIndexByte(roleARN, '/')+1:] != name {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	m.mu.Lock()
	c, ok := m.creds[inst.ID]
	m.mu.Unlock()
	if !ok || c.role != roleARN || time.Until(c.cred.Expiration) < credRefresh {
		cred, err := m.s.Roles.InstanceCredentials(roleARN, inst.ID, credTTL)
		if err != nil {
			b, _ := json.MarshalIndent(map[string]string{"Code": "AssumeRoleUnauthorizedAccess", "Message": err.Error(),
				"LastUpdated": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
			return
		}
		c = cachedCred{role: roleARN, cred: cred, updated: time.Now()}
		m.mu.Lock()
		m.creds[inst.ID] = c
		m.mu.Unlock()
	}
	b, _ := json.MarshalIndent(map[string]string{
		"Code": "Success", "LastUpdated": c.updated.UTC().Format(time.RFC3339), "Type": "AWS-HMAC",
		"AccessKeyId": c.cred.AccessKeyID, "SecretAccessKey": c.cred.SecretAccessKey, "Token": c.cred.SessionToken,
		"Expiration": c.cred.Expiration.UTC().Format(time.RFC3339),
	}, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// azID maps a zone name to an AWS-style zone ID (us-east-1a -> use1-az1).
func azID(az string) string {
	if az == "" {
		return ""
	}
	letter := az[len(az)-1]
	region := az[:len(az)-1]
	parts := strings.Split(region, "-")
	short := ""
	if len(parts) == 3 {
		short = parts[0] + string(parts[1][0]) + parts[2]
	} else {
		short = strings.ReplaceAll(region, "-", "")
	}
	return fmt.Sprintf("%s-az%d", short, int(letter-'a')+1)
}

func eniID(instanceID string) string { return "eni-" + strings.TrimPrefix(instanceID, "i-") }

var (
	archOnce sync.Once
	arch     string
)

// hostArch is the EC2 architecture of the Docker host (x86_64 or arm64).
func hostArch(s *Service) string {
	archOnce.Do(func() {
		arch = "x86_64"
		if info, err := s.env.Docker.C.Info(); err == nil && slices.Contains([]string{"aarch64", "arm64"}, info.Architecture) {
			arch = "arm64"
		}
	})
	return arch
}
