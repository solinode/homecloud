package runtime

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	docker "github.com/fsouza/go-dockerclient"
)

// HomeCloud in a container.
//
// When HomeCloud itself runs in a container on the Docker host it manages
// (the official image, with the host's Docker socket mounted), "this machine"
// is not where Docker publishes ports: a port published on the host's loopback
// is out of HomeCloud's reach, and host.docker.internal in a workload is the
// Docker host, not HomeCloud. So in that mode:
//
//   - HomeCloud dials the containers it runs (MinIO, the registry, function
//     environments, the DNS server) directly, on a Docker network it shares
//     with them (Reach), joining that network when it has to;
//   - workloads reach HomeCloud at its own address on their network: Run maps
//     host.docker.internal to it (SelfIP).

// containerIDPattern finds a container ID in the bind-mount sources of
// /proc/self/mountinfo (/var/lib/docker/containers/<id>/hostname and friends).
var containerIDPattern = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// inContainer reports whether this process runs in a container.
func inContainer() bool {
	if os.Getenv("HOMECLOUD_IN_CONTAINER") == "1" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// detectSelf returns the ID of the container HomeCloud runs in when that
// container is on the Docker host c talks to, and "" otherwise.
// HOMECLOUD_CONTAINER_ID names it explicitly.
func detectSelf(c *docker.Client) string {
	if v := os.Getenv("HOMECLOUD_CONTAINER_ID"); v != "" {
		if ct, err := c.InspectContainer(v); err == nil {
			return ct.ID
		}
		return ""
	}
	if !inContainer() {
		return ""
	}
	var candidates []string
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, m := range containerIDPattern.FindAllStringSubmatch(string(b), -1) {
			if !slices.Contains(candidates, m[1]) {
				candidates = append(candidates, m[1])
			}
		}
	}
	host, _ := os.Hostname()
	if host != "" {
		candidates = append(candidates, host)
	}
	for _, id := range candidates {
		ct, err := c.InspectContainer(id)
		if err != nil || ct.State.Pid == 0 || ct.Config == nil {
			continue
		}
		// A container named like our host name is not necessarily us: it must
		// also carry our host name.
		if id == host && ct.Config.Hostname != host {
			continue
		}
		if ct.HostConfig != nil && ct.HostConfig.NetworkMode == "host" {
			return "" // the host's network: HomeCloud reaches everything as if it ran there
		}
		return ct.ID
	}
	return ""
}

// Self is the ID of the container HomeCloud runs in, or "" when HomeCloud runs
// directly on the Docker host.
func (d *Docker) Self() string {
	if d == nil {
		return ""
	}
	return d.self
}

// SelfIP returns HomeCloud's own address on network ("" is the default
// bridge), attaching HomeCloud's container to it first when needed. It fails
// when HomeCloud does not run in a container.
func (d *Docker) SelfIP(network string) (string, error) {
	if d.self == "" {
		return "", fmt.Errorf("HomeCloud does not run in a container")
	}
	if network == "" {
		network = "bridge"
	}
	d.selfMu.Lock()
	defer d.selfMu.Unlock()
	if ip := d.selfAddr(network); ip != "" {
		return ip, nil
	}
	if err := d.ConnectIP(network, d.self, ""); err != nil {
		return "", fmt.Errorf("attach HomeCloud to network %s: %w", network, err)
	}
	if ip := d.selfAddr(network); ip != "" {
		return ip, nil
	}
	return "", fmt.Errorf("HomeCloud has no address on network %s", network)
}

// ConnectSelf attaches HomeCloud's container to network at ip (a reserved
// address; empty for automatic). It does nothing when HomeCloud runs directly
// on the host or is already attached.
func (d *Docker) ConnectSelf(network, ip string) error {
	if d.self == "" {
		return nil
	}
	d.selfMu.Lock()
	defer d.selfMu.Unlock()
	if d.selfAddr(network) != "" {
		return nil
	}
	err := d.ConnectIP(network, d.self, ip)
	if err != nil && ip != "" {
		err = d.ConnectIP(network, d.self, "") // the reserved address is taken
	}
	return err
}

// DisconnectSelf detaches HomeCloud's container from network, so the network
// can be removed.
func (d *Docker) DisconnectSelf(network string) {
	if d.self == "" {
		return
	}
	d.selfMu.Lock()
	defer d.selfMu.Unlock()
	_ = d.C.DisconnectNetwork(network, docker.NetworkConnectionOptions{Container: d.self, Force: true})
}

func (d *Docker) selfAddr(network string) string {
	c, err := d.C.InspectContainer(d.self)
	if err != nil || c.NetworkSettings == nil {
		return ""
	}
	if ep, ok := c.NetworkSettings.Networks[network]; ok {
		return ep.IPAddress
	}
	return ""
}

// Reach returns the address ("ip:port") at which HomeCloud reaches
// containerPort of container id when HomeCloud runs in a container: the
// target's address on a network both share, joining one of the target's
// networks (the default bridge first) when they share none.
func (d *Docker) Reach(id string, containerPort int) (string, error) {
	if d.self == "" {
		return "", fmt.Errorf("HomeCloud does not run in a container")
	}
	t, err := d.C.InspectContainer(id)
	if err != nil {
		return "", err
	}
	if t.NetworkSettings == nil || len(t.NetworkSettings.Networks) == 0 {
		return "", fmt.Errorf("container %s has no network", strings.TrimPrefix(t.Name, "/"))
	}
	port := strconv.Itoa(containerPort)
	self, err := d.C.InspectContainer(d.self)
	if err != nil {
		return "", err
	}
	// A shared VPC network first: HomeCloud gives containers there fixed
	// addresses, while an address on the default bridge changes when the
	// container restarts. To join one, the default bridge first.
	var names []string
	for name, ep := range t.NetworkSettings.Networks {
		if ep.IPAddress != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("container %s has no address", strings.TrimPrefix(t.Name, "/"))
	}
	slices.Sort(names)
	if i := slices.Index(names, "bridge"); i >= 0 {
		names = append(slices.Delete(names, i, i+1), "bridge")
	}
	for _, name := range names {
		if mine, ok := self.NetworkSettings.Networks[name]; ok && mine.IPAddress != "" {
			return net.JoinHostPort(t.NetworkSettings.Networks[name].IPAddress, port), nil
		}
	}
	if slices.Contains(names, "bridge") {
		names = append([]string{"bridge"}, names[:len(names)-1]...)
	}
	if _, err := d.SelfIP(names[0]); err != nil {
		return "", err
	}
	return net.JoinHostPort(t.NetworkSettings.Networks[names[0]].IPAddress, port), nil
}

// DialAddr is where HomeCloud dials containerPort of container id, which is
// published on the host at hostAddr ("127.0.0.1:9500"): hostAddr itself when
// HomeCloud runs on the host, the container's own address (Reach) when it
// runs in a container. When Reach fails it returns hostAddr.
func (d *Docker) DialAddr(id string, containerPort int, hostAddr string) string {
	if d.Self() == "" {
		return hostAddr
	}
	if a, err := d.Reach(id, containerPort); err == nil {
		return a
	}
	return hostAddr
}

// hostAlias rewrites host.docker.internal for a container HomeCloud creates
// while it runs in a container: the name then means HomeCloud itself, at its
// address on the container's network.
func (d *Docker) hostAlias(network string, hosts []string) []string {
	if d.self == "" || !slices.Contains(hosts, HostAlias) {
		return hosts
	}
	ip, err := d.SelfIP(network)
	if err != nil {
		return hosts
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h == HostAlias {
			h = HostAliasName + ":" + ip
		}
		out = append(out, h)
	}
	return out
}
