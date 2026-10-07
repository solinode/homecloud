// Package runtime wraps the Docker Engine, which is the substrate every
// HomeCloud service runs on. Every object it creates carries core.LabelManaged
// so HomeCloud never touches containers it does not own.
package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
)

type Docker struct {
	C *docker.Client

	self   string     // the container HomeCloud runs in (self.go), "" on the host
	selfMu sync.Mutex // serializes attaching it to networks
}

func New() (*Docker, error) {
	c, err := docker.NewClientFromEnv()
	if err != nil {
		return nil, fmt.Errorf("connect to docker: %w", err)
	}
	if err := c.Ping(); err != nil {
		return nil, fmt.Errorf("docker is not reachable (is it running?): %w", err)
	}
	return &Docker{C: c, self: detectSelf(c)}, nil
}

// Account is stamped on every managed object so two installations never share a Docker host by accident.
var Account string

// Labels returns the label set for a managed object.
func Labels(service, resource string, extra map[string]string) map[string]string {
	l := map[string]string{core.LabelManaged: "true", core.LabelService: service, core.LabelResource: resource, core.LabelAccount: Account}
	for k, v := range extra {
		l[k] = v
	}
	return l
}

// EnsureImage pulls image unless it is already present locally.
func (d *Docker) EnsureImage(ctx context.Context, image string) error {
	if _, err := d.C.InspectImage(image); err == nil {
		return nil
	}
	repo, tag := image, "latest"
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		repo, tag = image[:i], image[i+1:]
	}
	if err := d.C.PullImage(docker.PullImageOptions{Repository: repo, Tag: tag, Context: ctx, InactivityTimeout: 2 * time.Minute}, docker.AuthConfiguration{}); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

type Port struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"` // 0 = let Docker pick
	Protocol      string `json:"protocol"`  // tcp|udp
	HostIP        string `json:"host_ip"`   // default 0.0.0.0
}

type Mount struct {
	Volume   string `json:"volume"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type RunSpec struct {
	Name       string
	Image      string
	Cmd        []string
	Entrypoint []string
	Env        map[string]string
	Labels     map[string]string
	NanoCPUs   int64
	MemoryMB   int64
	Ports      []Port
	Mounts     []Mount
	Network    string
	IP         string
	Aliases    []string
	Restart    string // "", "unless-stopped", "always"
	WorkingDir string
	User       string // run as this user (e.g. "0"); empty uses the image's default
	Hostname   string
	DNS        []string // upstream servers for Docker's embedded DNS
	ExtraHosts []string // "name:ip" entries for /etc/hosts ("host-gateway" is the Docker host)
	CapAdd     []string // extra Linux capabilities (e.g. NET_ADMIN)
	Start      bool
}

// HostAlias makes the Docker host reachable from containers as host.docker.internal
// (built in on Docker Desktop and OrbStack; mapped to the bridge gateway on Linux).
const HostAlias = HostAliasName + ":host-gateway"

// HostAliasName is the name workloads reach HomeCloud by.
const HostAliasName = "host.docker.internal"

// BridgeGateway returns the Docker host's address on the default bridge (Linux),
// which containers reach through host.docker.internal.
func (d *Docker) BridgeGateway() string {
	n, err := d.C.NetworkInfo("bridge")
	if err != nil {
		return ""
	}
	for _, c := range n.IPAM.Config {
		if c.Gateway != "" {
			return c.Gateway
		}
	}
	return ""
}

func (d *Docker) Run(ctx context.Context, s RunSpec) (string, error) {
	if err := d.EnsureImage(ctx, s.Image); err != nil {
		return "", err
	}
	env := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		env = append(env, k+"="+v)
	}
	exposed := map[docker.Port]struct{}{}
	bindings := map[docker.Port][]docker.PortBinding{}
	for _, p := range s.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		dp := docker.Port(fmt.Sprintf("%d/%s", p.ContainerPort, proto))
		exposed[dp] = struct{}{}
		hp := ""
		if p.HostPort > 0 {
			hp = strconv.Itoa(p.HostPort)
		}
		ip := p.HostIP
		if ip == "" {
			ip = "0.0.0.0"
		}
		bindings[dp] = append(bindings[dp], docker.PortBinding{HostIP: ip, HostPort: hp})
	}
	mounts := make([]docker.HostMount, 0, len(s.Mounts))
	for _, m := range s.Mounts {
		mounts = append(mounts, docker.HostMount{Type: "volume", Source: m.Volume, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	hc := &docker.HostConfig{
		DNS:          s.DNS,
		ExtraHosts:   d.hostAlias(s.Network, s.ExtraHosts),
		Memory:       s.MemoryMB * 1024 * 1024,
		PortBindings: bindings,
		Mounts:       mounts,
	}
	if s.NanoCPUs > 0 {
		// Quota/period rather than NanoCPUs so limits can be changed later with docker update.
		hc.CPUPeriod, hc.CPUQuota = 100000, s.NanoCPUs/10000
	}
	hc.CapAdd = s.CapAdd
	if s.Restart != "" {
		hc.RestartPolicy = docker.RestartPolicy{Name: s.Restart}
	}
	var nc *docker.NetworkingConfig
	if s.Network != "" {
		ep := &docker.EndpointConfig{Aliases: s.Aliases}
		if s.IP != "" {
			ep.IPAMConfig = &docker.EndpointIPAMConfig{IPv4Address: s.IP}
		}
		nc = &docker.NetworkingConfig{EndpointsConfig: map[string]*docker.EndpointConfig{s.Network: ep}}
		hc.NetworkMode = s.Network
	}
	c, err := d.C.CreateContainer(docker.CreateContainerOptions{
		Name:    s.Name,
		Context: ctx,
		Config: &docker.Config{
			Image:        s.Image,
			Cmd:          s.Cmd,
			Entrypoint:   s.Entrypoint,
			Env:          env,
			Labels:       s.Labels,
			ExposedPorts: exposed,
			WorkingDir:   s.WorkingDir,
			User:         s.User,
			Hostname:     s.Hostname,
		},
		HostConfig:       hc,
		NetworkingConfig: nc,
	})
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	if s.Start {
		if err := d.C.StartContainerWithContext(c.ID, nil, ctx); err != nil {
			_ = d.Remove(c.ID)
			return "", fmt.Errorf("start container: %w", err)
		}
	}
	return c.ID, nil
}

func (d *Docker) Start(id string) error {
	err := d.C.StartContainer(id, nil)
	var already *docker.ContainerAlreadyRunning
	if errors.As(err, &already) {
		return nil
	}
	return err
}

func (d *Docker) Stop(id string, timeout uint) error {
	err := d.C.StopContainer(id, timeout)
	var notRunning *docker.ContainerNotRunning
	if errors.As(err, &notRunning) {
		return nil
	}
	return err
}

func (d *Docker) Restart(id string) error { return d.C.RestartContainer(id, 10) }

// Remove force-removes a container and its anonymous volumes; missing containers are not an error.
func (d *Docker) Remove(id string) error {
	err := d.C.RemoveContainer(docker.RemoveContainerOptions{ID: id, Force: true, RemoveVolumes: true})
	var noSuch *docker.NoSuchContainer
	if errors.As(err, &noSuch) {
		return nil
	}
	return err
}

// State returns the container's Docker state ("running", "exited", ...) or "missing".
func (d *Docker) State(id string) string {
	c, err := d.C.InspectContainer(id)
	if err != nil {
		return "missing"
	}
	return c.State.Status
}

func (d *Docker) Inspect(id string) (*docker.Container, error) { return d.C.InspectContainer(id) }

// HostPort returns the host port bound to containerPort/tcp, or 0.
func (d *Docker) HostPort(id string, containerPort int) int {
	c, err := d.C.InspectContainer(id)
	if err != nil || c.NetworkSettings == nil {
		return 0
	}
	for _, b := range c.NetworkSettings.Ports[docker.Port(fmt.Sprintf("%d/tcp", containerPort))] {
		if p, err := strconv.Atoi(b.HostPort); err == nil {
			return p
		}
	}
	return 0
}

// BoundTo reports whether every given container port ("9000/tcp") of the
// container is published on hostIP only. A container created before HomeCloud
// bound ports to loopback fails this check and gets recreated. An error means
// the container couldn't be inspected, which is not a reason to recreate it.
func (d *Docker) BoundTo(id, hostIP string, ports ...string) (bool, error) {
	c, err := d.C.InspectContainer(id)
	if err != nil {
		return false, err
	}
	if c.HostConfig == nil {
		return false, nil
	}
	for _, p := range ports {
		if !bindingsAre(c.HostConfig.PortBindings[docker.Port(p)], hostIP) {
			return false, nil
		}
	}
	return true, nil
}

func bindingsAre(bs []docker.PortBinding, hostIP string) bool {
	if len(bs) == 0 {
		return false
	}
	norm := func(ip string) string {
		if ip == "" {
			return "0.0.0.0"
		}
		return ip
	}
	for _, b := range bs {
		if norm(b.HostIP) != norm(hostIP) {
			return false
		}
	}
	return true
}

// PublishedPorts maps "80/tcp" -> host port for a container.
func (d *Docker) PublishedPorts(id string) map[string]int {
	out := map[string]int{}
	c, err := d.C.InspectContainer(id)
	if err != nil || c.NetworkSettings == nil {
		return out
	}
	for p, bs := range c.NetworkSettings.Ports {
		for _, b := range bs {
			if hp, err := strconv.Atoi(b.HostPort); err == nil {
				out[string(p)] = hp
				break
			}
		}
	}
	return out
}

func (d *Docker) Logs(id string, tail int, since time.Time) (string, error) {
	var buf bytes.Buffer
	opts := docker.LogsOptions{Container: id, OutputStream: &buf, ErrorStream: &buf, Stdout: true, Stderr: true, Timestamps: true, Tail: "all"}
	if tail > 0 {
		opts.Tail = strconv.Itoa(tail)
	}
	if !since.IsZero() {
		opts.Since = since.Unix()
	}
	err := d.C.Logs(opts)
	return buf.String(), err
}

type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// Exec runs cmd inside a running container and collects its output.
func (d *Docker) Exec(ctx context.Context, id string, cmd []string, stdin io.Reader) (*ExecResult, error) {
	ex, err := d.C.CreateExec(docker.CreateExecOptions{
		Container: id, Cmd: cmd, AttachStdout: true, AttachStderr: true, AttachStdin: stdin != nil, Context: ctx,
	})
	if err != nil {
		return nil, err
	}
	var out, errb bytes.Buffer
	if err := d.C.StartExec(ex.ID, docker.StartExecOptions{OutputStream: &out, ErrorStream: &errb, InputStream: stdin, Context: ctx}); err != nil {
		return nil, err
	}
	ins, err := d.C.InspectExec(ex.ID)
	if err != nil {
		return nil, err
	}
	return &ExecResult{ExitCode: ins.ExitCode, Stdout: out.String(), Stderr: errb.String()}, nil
}

// CopyIn writes files (path -> content) into the container under dir.
func (d *Docker) CopyIn(ctx context.Context, id, dir string, files map[string][]byte, mode int64) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: time.Now()}); err != nil {
			return err
		}
		if _, err := tw.Write(content); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return d.C.UploadToContainer(id, docker.UploadToContainerOptions{InputStream: &buf, Path: dir, Context: ctx})
}

// ---- networks & volumes ----

func (d *Docker) CreateNetwork(name, cidr, gateway string, internal bool, labels map[string]string) (string, error) {
	n, err := d.C.CreateNetwork(docker.CreateNetworkOptions{
		Name:     name,
		Driver:   "bridge",
		Internal: internal,
		Labels:   labels,
		IPAM:     &docker.IPAMOptions{Driver: "default", Config: []docker.IPAMConfig{{Subnet: cidr, Gateway: gateway}}},
	})
	if err != nil {
		return "", err
	}
	return n.ID, nil
}

func (d *Docker) RemoveNetwork(id string) error {
	err := d.C.RemoveNetwork(id)
	var noSuch *docker.NoSuchNetwork
	if errors.As(err, &noSuch) {
		return nil
	}
	return err
}

// NetworkSubnets lists the IPv4 subnets of every Docker network, for overlap checks.
func (d *Docker) NetworkSubnets() (map[string]string, error) {
	ns, err := d.C.ListNetworks()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, n := range ns {
		for _, c := range n.IPAM.Config {
			if c.Subnet != "" {
				out[c.Subnet] = n.Name
			}
		}
	}
	return out, nil
}

func (d *Docker) CreateVolume(name string, labels map[string]string) error {
	_, err := d.C.CreateVolume(docker.CreateVolumeOptions{Name: name, Labels: labels})
	return err
}

func (d *Docker) RemoveVolume(name string) error {
	err := d.C.RemoveVolumeWithOptions(docker.RemoveVolumeOptions{Name: name, Force: true})
	if errors.Is(err, docker.ErrNoSuchVolume) {
		return nil
	}
	return err
}

// ManagedContainers lists every HomeCloud-owned container, keyed by container ID.
func (d *Docker) ManagedContainers() ([]docker.APIContainers, error) {
	return d.C.ListContainers(docker.ListContainersOptions{All: true, Filters: map[string][]string{"label": {core.LabelManaged + "=true"}}})
}

// ---- stats ----

type Usage struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryBytes   uint64  `json:"memory_bytes"`
	MemoryLimit   uint64  `json:"memory_limit"`
	MemoryPercent float64 `json:"memory_percent"`
	NetRxBytes    uint64  `json:"net_rx_bytes"`
	NetTxBytes    uint64  `json:"net_tx_bytes"`
	BlockRead     uint64  `json:"block_read"`
	BlockWrite    uint64  `json:"block_write"`
	Pids          uint64  `json:"pids"`
}

// Stats takes a one-shot resource sample of a running container.
func (d *Docker) Stats(ctx context.Context, id string) (*Usage, error) {
	ch := make(chan *docker.Stats, 2)
	errc := make(chan error, 1)
	go func() {
		errc <- d.C.Stats(docker.StatsOptions{ID: id, Stats: ch, Stream: false, Context: ctx, Timeout: 10 * time.Second})
	}()
	var s *docker.Stats
	for st := range ch {
		s = st
	}
	if err := <-errc; err != nil && s == nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("no stats")
	}
	u := &Usage{Pids: s.PidsStats.Current, MemoryLimit: s.MemoryStats.Limit}
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemCPUUsage) - float64(s.PreCPUStats.SystemCPUUsage)
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if sysDelta > 0 && cpuDelta > 0 {
		u.CPUPercent = cpuDelta / sysDelta * cpus * 100
	}
	mem := s.MemoryStats.Usage
	if f := s.MemoryStats.Stats.InactiveFile; f < mem {
		mem -= f
	}
	u.MemoryBytes = mem
	if s.MemoryStats.Limit > 0 {
		u.MemoryPercent = float64(mem) / float64(s.MemoryStats.Limit) * 100
	}
	for _, n := range s.Networks {
		u.NetRxBytes += n.RxBytes
		u.NetTxBytes += n.TxBytes
	}
	for _, e := range s.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			u.BlockRead += e.Value
		case "write":
			u.BlockWrite += e.Value
		}
	}
	return u, nil
}

// Connect attaches a container to a network with DNS aliases; already-connected is not an error.
func (d *Docker) Connect(network, container string, aliases ...string) error {
	return d.ConnectIP(network, container, "", aliases...)
}

// ConnectIP is Connect with a fixed IPv4 address (empty for automatic).
func (d *Docker) ConnectIP(network, container, ip string, aliases ...string) error {
	ep := &docker.EndpointConfig{Aliases: aliases}
	if ip != "" {
		ep.IPAMConfig = &docker.EndpointIPAMConfig{IPv4Address: ip}
	}
	err := d.C.ConnectNetwork(network, docker.NetworkConnectionOptions{Container: container, EndpointConfig: ep})
	if err != nil && (strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "already attached")) {
		return nil
	}
	return err
}
