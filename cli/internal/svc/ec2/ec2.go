// Package ec2 implements compute instances as resource-limited containers
// placed in a VPC subnet, plus AMIs, instance types and EBS-style volumes.
package ec2

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

const (
	cInstances = "ec2_instances"
	cImages    = "ec2_images"
	cVolumes   = "ec2_volumes"

	terminatedTTL = time.Hour
)

type VolumeAttachment struct {
	VolumeID            string `json:"volume_id"`
	MountPath           string `json:"mount_path"`
	DeleteOnTermination bool   `json:"delete_on_termination"`
}

type Instance struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	ARN              string             `json:"arn"`
	ImageID          string             `json:"image_id"`
	ImageRef         string             `json:"image_ref"`
	InstanceType     string             `json:"instance_type"`
	VCPUs            float64            `json:"vcpus"`
	MemoryMB         int64              `json:"memory_mb"`
	State            string             `json:"state"`
	StateReason      string             `json:"state_reason,omitempty"`
	ContainerID      string             `json:"container_id,omitempty"`
	VpcID            string             `json:"vpc_id"`
	SubnetID         string             `json:"subnet_id"`
	AvailabilityZone string             `json:"availability_zone"`
	PrivateIP        string             `json:"private_ip"`
	PrivateDNS       string             `json:"private_dns"`
	SecurityGroups   []string           `json:"security_groups"`
	Volumes          []VolumeAttachment `json:"volumes"`
	FileSystems      []FSMount          `json:"file_systems"`
	UserData         string             `json:"user_data,omitempty"`
	KeepAlive        bool               `json:"keep_alive"`
	PublicPorts      map[string]int     `json:"public_ports"` // "80/tcp" -> host port
	PublicHost       string             `json:"public_host"`
	LaunchTime       time.Time          `json:"launch_time"`
	TerminatedAt     *time.Time         `json:"terminated_at,omitempty"`
	Tags             core.Tags          `json:"tags,omitempty"`
}

type Volume struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	SizeGB           int       `json:"size_gb"` // advisory: Docker volumes are not size-capped
	State            string    `json:"state"`   // available | in-use
	AttachedTo       string    `json:"attached_to,omitempty"`
	MountPath        string    `json:"mount_path,omitempty"`
	AvailabilityZone string    `json:"availability_zone"`
	CreatedAt        time.Time `json:"created_at"`
	Tags             core.Tags `json:"tags,omitempty"`
}

func volumeName(id string) string { return "hc-" + id }

type Service struct {
	env     *svc.Env
	vpc     *vpc.Service
	hostCPU float64
	mu      sync.Mutex // serialises state transitions
}

func New(env *svc.Env, v *vpc.Service) *Service {
	s := &Service{env: env, vpc: v, hostCPU: 1}
	if info, err := env.Docker.C.Info(); err == nil && info.NCPU > 0 {
		s.hostCPU = float64(info.NCPU)
	}
	v.InUse = func(sg string) bool {
		for _, i := range store.List[Instance](env.Store, cInstances) {
			if i.State != "terminated" && slices.Contains(i.SecurityGroups, sg) {
				return true
			}
		}
		return false
	}
	return s
}

// ---- reconciliation ----

// sync reconciles a stored instance with its container's actual state.
func (s *Service) sync(i Instance) Instance {
	if i.State == "terminated" || i.State == "pending" || i.ContainerID == "" {
		return i
	}
	st := s.env.Docker.State(i.ContainerID)
	want := i.State
	switch st {
	case "running":
		want = "running"
	case "exited", "created", "dead":
		want = "stopped"
	case "missing":
		want = "terminated"
	}
	if i.State == "running" {
		i.PublicPorts = s.env.Docker.PublishedPorts(i.ContainerID)
	}
	if want != i.State && !(i.State == "stopping" || i.State == "shutting-down") {
		reason := ""
		if want == "stopped" {
			reason = "Client.InstanceInitiatedShutdown: the instance's main process exited"
		} else if want == "terminated" {
			reason = "Server.InternalError: the backing container disappeared"
		}
		i, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
			x.State, x.StateReason = want, reason
			if want == "terminated" {
				n := core.Now()
				x.TerminatedAt = &n
			}
			return nil
		})
		if want == "terminated" {
			s.vpc.Release(i.ID)
		}
	}
	return i
}

func (s *Service) list() []Instance {
	out := []Instance{}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.State == "terminated" && i.TerminatedAt != nil && time.Since(*i.TerminatedAt) > terminatedTTL {
			_ = store.Delete(s.env.Store, cInstances, i.ID)
			continue
		}
		out = append(out, s.sync(i))
	}
	slices.SortFunc(out, func(a, b Instance) int { return b.LaunchTime.Compare(a.LaunchTime) })
	return out
}

func (s *Service) get(id string) (Instance, error) {
	i, err := store.Get[Instance](s.env.Store, cInstances, id)
	if err != nil {
		return i, core.NotFound("instance", id)
	}
	return s.sync(i), nil
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:ec2:local-1:{account}:instance/{id}")
	r.Handle("GET /api/v1/ec2/instances", "ec2:DescribeInstances", s.listRoute)
	r.Handle("POST /api/v1/ec2/instances", "ec2:RunInstances", s.run)
	r.Handle("GET /api/v1/ec2/instances/{id}", "ec2:DescribeInstances", s.getRoute, res)
	r.Handle("PATCH /api/v1/ec2/instances/{id}", "ec2:ModifyInstanceAttribute", s.modify, res)
	r.Handle("POST /api/v1/ec2/instances/{id}/start", "ec2:StartInstances", s.start, res)
	r.Handle("POST /api/v1/ec2/instances/{id}/stop", "ec2:StopInstances", s.stop, res)
	r.Handle("POST /api/v1/ec2/instances/{id}/reboot", "ec2:RebootInstances", s.reboot, res)
	r.Handle("DELETE /api/v1/ec2/instances/{id}", "ec2:TerminateInstances", s.terminate, res)
	r.Handle("GET /api/v1/ec2/instances/{id}/console-output", "ec2:GetConsoleOutput", s.consoleOutput, res)
	r.Handle("POST /api/v1/ec2/instances/{id}/commands", "ssm:SendCommand", s.runCommand, res)
	r.Handle("POST /api/v1/ec2/instances/{id}/image", "ec2:CreateImage", s.createImage, res)
	r.Handle("GET /api/v1/ec2/instances/{id}/terminal", "ec2-instance-connect:SendSSHPublicKey", s.terminal, res)

	r.Handle("GET /api/v1/ec2/instance-types", "ec2:DescribeInstanceTypes", s.listTypes)
	r.Handle("GET /api/v1/ec2/images", "ec2:DescribeImages", s.listImages)
	r.Handle("POST /api/v1/ec2/images", "ec2:RegisterImage", s.registerImage)
	r.Handle("GET /api/v1/ec2/images/{id}", "ec2:DescribeImages", s.getImage)
	r.Handle("DELETE /api/v1/ec2/images/{id}", "ec2:DeregisterImage", s.deregisterImage)

	s.efsRoutes(r)
	r.Handle("GET /api/v1/ec2/volumes", "ec2:DescribeVolumes", s.listVolumes)
	r.Handle("POST /api/v1/ec2/volumes", "ec2:CreateVolume", s.createVolumeRoute)
	r.Handle("GET /api/v1/ec2/volumes/{id}", "ec2:DescribeVolumes", s.getVolume)
	r.Handle("DELETE /api/v1/ec2/volumes/{id}", "ec2:DeleteVolume", s.deleteVolume)
}

func (s *Service) listRoute(c *httpx.Ctx) (any, error) {
	out := s.list()
	if st := c.Query("state"); st != "" {
		out = slices.DeleteFunc(out, func(i Instance) bool { return i.State != st })
	}
	return out, nil
}

func (s *Service) getRoute(c *httpx.Ctx) (any, error) { return s.get(c.Param("id")) }

func (s *Service) listTypes(c *httpx.Ctx) (any, error) { return instanceTypes, nil }

// RunInput describes instances to launch (the RunInstances request body).
type RunInput struct {
	Name             string    `json:"name"`
	ImageID          string    `json:"image_id"`
	InstanceType     string    `json:"instance_type"`
	SubnetID         string    `json:"subnet_id"`
	SecurityGroupIDs []string  `json:"security_group_ids"`
	UserData         string    `json:"user_data"`
	Count            int       `json:"count"`
	Tags             core.Tags `json:"tags"`
	Volumes          []struct {
		VolumeID            string `json:"volume_id"` // attach an existing available volume
		SizeGB              int    `json:"size_gb"`   // or create a new one
		MountPath           string `json:"mount_path"`
		DeleteOnTermination *bool  `json:"delete_on_termination"`
	} `json:"volumes"`
	FileSystems []FSMount `json:"file_systems"`
}

func (s *Service) image(id string) (Image, error) {
	for _, im := range catalog {
		if im.ID == id {
			im.Owner, im.State = "homecloud", "available"
			return im, nil
		}
	}
	im, err := store.Get[Image](s.env.Store, cImages, id)
	if err != nil {
		return im, core.NotFound("image", id)
	}
	return im, nil
}

func (s *Service) run(c *httpx.Ctx) (any, error) {
	var in RunInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.Launch(in)
}

// Launch validates and starts instances; the containers boot in the background.
func (s *Service) Launch(in RunInput) ([]Instance, error) {
	if in.Count == 0 {
		in.Count = 1
	}
	if in.Count < 1 || in.Count > 20 {
		return nil, core.BadRequest("count must be between 1 and 20")
	}
	if in.InstanceType == "" {
		in.InstanceType = "t3.micro"
	}
	it, ok := findType(in.InstanceType)
	if !ok {
		return nil, core.BadRequest("unknown instance type %q", in.InstanceType)
	}
	if in.ImageID == "" {
		return nil, core.BadRequest("image_id is required")
	}
	img, err := s.image(in.ImageID)
	if err != nil {
		return nil, err
	}
	for _, m := range in.FileSystems {
		if !strings.HasPrefix(m.MountPath, "/") {
			return nil, core.BadRequest("file system mount_path must be absolute")
		}
		fs, err := store.Get[FileSystem](s.env.Store, cFileSystems, m.FileSystemID)
		if err != nil {
			return nil, core.NotFound("file system", m.FileSystemID)
		}
		if fs.ReadOnly != m.ReadOnly && fs.ReadOnly {
			return nil, core.BadRequest("file system %s is read-only", fs.ID)
		}
	}
	for _, v := range in.Volumes {
		if !strings.HasPrefix(v.MountPath, "/") {
			return nil, core.BadRequest("volume mount_path must be absolute")
		}
		if v.VolumeID != "" {
			vol, err := store.Get[Volume](s.env.Store, cVolumes, v.VolumeID)
			if err != nil {
				return nil, core.NotFound("volume", v.VolumeID)
			}
			if vol.State != "available" {
				return nil, core.Conflict("volume %s is %s", vol.ID, vol.State)
			}
			if in.Count > 1 {
				return nil, core.BadRequest("an existing volume can only be attached when count is 1")
			}
		}
	}

	var launched []Instance
	for n := 0; n < in.Count; n++ {
		id := core.NewID("i")
		pl, err := s.vpc.Place(in.SubnetID, id)
		if err != nil {
			return nil, err
		}
		sgs := in.SecurityGroupIDs
		if len(sgs) == 0 {
			sgs = []string{s.vpc.DefaultSecurityGroup(pl.VPC.ID)}
		}
		if err := s.vpc.CheckGroups(pl.VPC.ID, sgs); err != nil {
			s.vpc.Release(id)
			return nil, err
		}
		name := in.Name
		if name != "" && in.Count > 1 {
			name = fmt.Sprintf("%s-%d", in.Name, n+1)
		}
		inst := Instance{
			ID: id, Name: name, ARN: s.env.ARN("ec2", "instance/"+id), ImageID: img.ID, ImageRef: img.Ref,
			InstanceType: it.Name, VCPUs: it.VCPUs, MemoryMB: it.MemoryMB, State: "pending",
			VpcID: pl.VPC.ID, SubnetID: pl.Subnet.ID, AvailabilityZone: pl.Subnet.AvailabilityZone,
			PrivateIP: pl.IP, PrivateDNS: "ip-" + strings.ReplaceAll(pl.IP, ".", "-") + ".internal",
			SecurityGroups: sgs, UserData: in.UserData, KeepAlive: img.KeepAlive, Volumes: []VolumeAttachment{}, FileSystems: nzFS(in.FileSystems),
			PublicPorts: map[string]int{}, PublicHost: s.env.Cfg.PublicHost, LaunchTime: core.Now(), Tags: in.Tags,
		}
		for _, v := range in.Volumes {
			del := v.VolumeID == ""
			if v.DeleteOnTermination != nil {
				del = *v.DeleteOnTermination
			}
			volID := v.VolumeID
			if volID == "" {
				size := v.SizeGB
				if size == 0 {
					size = 8
				}
				vol, err := s.createVolume("", size, pl.Subnet.AvailabilityZone, nil)
				if err != nil {
					return nil, err
				}
				volID = vol.ID
			}
			_, _ = store.Update(s.env.Store, cVolumes, volID, func(x *Volume) error {
				x.State, x.AttachedTo, x.MountPath = "in-use", id, v.MountPath
				return nil
			})
			inst.Volumes = append(inst.Volumes, VolumeAttachment{VolumeID: volID, MountPath: v.MountPath, DeleteOnTermination: del})
		}
		if err := store.Put(s.env.Store, cInstances, id, inst); err != nil {
			return nil, err
		}
		launched = append(launched, inst)
		go s.launch(inst, pl.Network)
	}
	return launched, nil
}

// launch creates and boots the instance's container in the background.
func (s *Service) launch(inst Instance, network string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	fail := func(err error) {
		log.Printf("ec2: launch %s failed: %v", inst.ID, err)
		_, _ = store.Update(s.env.Store, cInstances, inst.ID, func(x *Instance) error {
			n := core.Now()
			x.State, x.StateReason, x.TerminatedAt = "terminated", "Server.LaunchFailure: "+err.Error(), &n
			return nil
		})
		s.releaseVolumes(inst)
		s.vpc.Release(inst.ID)
	}
	mounts := []runtime.Mount{}
	for _, v := range inst.Volumes {
		mounts = append(mounts, runtime.Mount{Volume: volumeName(v.VolumeID), Target: v.MountPath})
	}
	for _, m := range inst.FileSystems {
		mounts = append(mounts, runtime.Mount{Volume: fsVolume(m.FileSystemID), Target: m.MountPath, ReadOnly: m.ReadOnly})
	}
	meta, _ := json.MarshalIndent(map[string]any{
		"instance-id": inst.ID, "instance-type": inst.InstanceType, "ami-id": inst.ImageID, "local-ipv4": inst.PrivateIP,
		"local-hostname": inst.PrivateDNS, "placement": map[string]string{"availability-zone": inst.AvailabilityZone, "region": s.env.Cfg.Region},
		"vpc-id": inst.VpcID, "subnet-id": inst.SubnetID, "security-groups": inst.SecurityGroups, "tags": inst.Tags,
	}, "", "  ")
	spec := runtime.RunSpec{
		Name:     svc.ContainerName("ec2", inst.ID),
		Image:    inst.ImageRef,
		Labels:   runtime.Labels("ec2", inst.ID, map[string]string{"homecloud.name": inst.Name}),
		NanoCPUs: int64(min(inst.VCPUs, s.hostCPU) * 1e9),
		MemoryMB: inst.MemoryMB,
		Ports:    s.vpc.PublishedPorts(inst.SecurityGroups),
		Mounts:   mounts,
		Network:  network,
		IP:       inst.PrivateIP,
		Aliases:  append([]string{inst.ID, inst.PrivateDNS}, nonEmpty(inst.Name)...),
		Hostname: strings.TrimSuffix(inst.PrivateDNS, ".internal"),
		Env: map[string]string{
			"HC_INSTANCE_ID": inst.ID, "HC_INSTANCE_TYPE": inst.InstanceType, "HC_REGION": s.env.Cfg.Region, "HC_PRIVATE_IP": inst.PrivateIP,
		},
	}
	if inst.KeepAlive {
		spec.Entrypoint = []string{"/bin/sh", "-c"}
		spec.Cmd = []string{bootScript}
	}
	cid, err := s.env.Docker.Run(ctx, spec)
	if err != nil {
		fail(err)
		return
	}
	files := map[string][]byte{"var/lib/homecloud/instance.json": meta}
	if inst.UserData != "" {
		files["var/lib/homecloud/user-data"] = []byte(inst.UserData)
	}
	if err := s.env.Docker.CopyIn(ctx, cid, "/", files, 0o644); err != nil {
		_ = s.env.Docker.Remove(cid)
		fail(fmt.Errorf("write instance metadata: %w", err))
		return
	}
	if err := s.env.Docker.Start(cid); err != nil {
		_ = s.env.Docker.Remove(cid)
		fail(err)
		return
	}
	_, _ = store.Update(s.env.Store, cInstances, inst.ID, func(x *Instance) error {
		if x.State != "pending" { // terminated while launching
			go s.env.Docker.Remove(cid)
			return nil
		}
		x.ContainerID, x.State, x.StateReason = cid, "running", ""
		x.PublicPorts = s.env.Docker.PublishedPorts(cid)
		return nil
	})
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func (s *Service) transition(id string, from []string, fn func(i Instance) error, during, after string) (any, error) {
	s.mu.Lock()
	i, err := s.get(id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !slices.Contains(from, i.State) {
		s.mu.Unlock()
		return nil, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance %s is %s", id, i.State)
	}
	if i.ContainerID == "" {
		s.mu.Unlock()
		return nil, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance %s has no backing container", id)
	}
	cur, err := store.Update(s.env.Store, cInstances, id, func(x *Instance) error { x.State = during; return nil })
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Docker can take a while (stop waits for the process); finish in the background
	// and let clients poll, as with EC2's pending/stopping states.
	go func() {
		if err := fn(i); err != nil {
			_, _ = store.Update(s.env.Store, cInstances, id, func(x *Instance) error { x.State, x.StateReason = i.State, err.Error(); return nil })
			return
		}
		_, _ = store.Update(s.env.Store, cInstances, id, func(x *Instance) error {
			x.State, x.StateReason = after, ""
			if after == "running" {
				x.PublicPorts = s.env.Docker.PublishedPorts(x.ContainerID)
			}
			return nil
		})
	}()
	return cur, nil
}

func (s *Service) start(c *httpx.Ctx) (any, error) {
	return s.transition(c.Param("id"), []string{"stopped"}, func(i Instance) error { return s.env.Docker.Start(i.ContainerID) }, "pending", "running")
}

func (s *Service) stop(c *httpx.Ctx) (any, error) {
	return s.transition(c.Param("id"), []string{"running"}, func(i Instance) error { return s.env.Docker.Stop(i.ContainerID, 10) }, "stopping", "stopped")
}

func (s *Service) reboot(c *httpx.Ctx) (any, error) {
	return s.transition(c.Param("id"), []string{"running"}, func(i Instance) error { return s.env.Docker.Restart(i.ContainerID) }, "running", "running")
}

func (s *Service) terminate(c *httpx.Ctx) (any, error) { return s.Terminate(c.Param("id")) }

// Terminate removes an instance and its container.
func (s *Service) Terminate(id string) (Instance, error) {
	i, err := s.get(id)
	if err != nil {
		return i, err
	}
	if i.State == "terminated" {
		return i, nil
	}
	_, _ = store.Update(s.env.Store, cInstances, id, func(x *Instance) error { x.State = "shutting-down"; return nil })
	if i.ContainerID != "" {
		if err := s.env.Docker.Remove(i.ContainerID); err != nil {
			return i, err
		}
	}
	s.releaseVolumes(i)
	s.vpc.Release(id)
	return store.Update(s.env.Store, cInstances, id, func(x *Instance) error {
		n := core.Now()
		x.State, x.TerminatedAt, x.PublicPorts, x.StateReason = "terminated", &n, map[string]int{}, "Client.UserInitiatedShutdown"
		return nil
	})
}

func (s *Service) releaseVolumes(i Instance) {
	for _, v := range i.Volumes {
		if v.DeleteOnTermination {
			_ = s.env.Docker.RemoveVolume(volumeName(v.VolumeID))
			_ = store.Delete(s.env.Store, cVolumes, v.VolumeID)
			continue
		}
		_, _ = store.Update(s.env.Store, cVolumes, v.VolumeID, func(x *Volume) error {
			x.State, x.AttachedTo, x.MountPath = "available", "", ""
			return nil
		})
	}
}

func (s *Service) modify(c *httpx.Ctx) (any, error) {
	var in struct {
		Name         *string   `json:"name"`
		InstanceType string    `json:"instance_type"`
		Tags         core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if i.State == "terminated" || i.ContainerID == "" {
		return nil, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance is %s", i.State)
	}
	var it InstanceType
	if in.InstanceType != "" && in.InstanceType != i.InstanceType {
		var ok bool
		if it, ok = findType(in.InstanceType); !ok {
			return nil, core.BadRequest("unknown instance type %q", in.InstanceType)
		}
		if i.State != "stopped" {
			return nil, core.Errf(http.StatusConflict, "IncorrectInstanceState", "the instance must be stopped to change its type")
		}
		mem := it.MemoryMB * 1024 * 1024
		if err := s.env.Docker.C.UpdateContainer(i.ContainerID, docker.UpdateContainerOptions{
			Memory: int(mem), MemorySwap: int(mem * 2), CPUPeriod: 100000, CPUQuota: int(min(it.VCPUs, s.hostCPU) * 100000),
		}); err != nil {
			return nil, fmt.Errorf("resize container: %w", err)
		}
	}
	return store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		if in.Name != nil {
			x.Name = *in.Name
		}
		if it.Name != "" {
			x.InstanceType, x.VCPUs, x.MemoryMB = it.Name, it.VCPUs, it.MemoryMB
		}
		if in.Tags != nil {
			x.Tags = in.Tags
		}
		return nil
	})
}

func (s *Service) consoleOutput(c *httpx.Ctx) (any, error) {
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if i.ContainerID == "" || i.State == "terminated" {
		return map[string]string{"instance_id": i.ID, "output": ""}, nil
	}
	out, err := s.env.Docker.Logs(i.ContainerID, c.QueryInt("tail", 500), time.Time{})
	if err != nil {
		return nil, err
	}
	return map[string]any{"instance_id": i.ID, "output": out, "timestamp": core.Now()}, nil
}

func (s *Service) runCommand(c *httpx.Ctx) (any, error) {
	var in struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout_seconds"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Command) == "" {
		return nil, core.BadRequest("command is required")
	}
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if i.State != "running" {
		return nil, core.Errf(http.StatusConflict, "InvalidInstanceState", "instance %s is %s", i.ID, i.State)
	}
	if in.Timeout <= 0 || in.Timeout > 600 {
		in.Timeout = 60
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), time.Duration(in.Timeout)*time.Second)
	defer cancel()
	start := time.Now()
	res, err := s.env.Docker.Exec(ctx, i.ContainerID, []string{"/bin/sh", "-c", in.Command}, nil)
	if err != nil {
		return nil, err
	}
	status := "Success"
	if res.ExitCode != 0 {
		status = "Failed"
	}
	return map[string]any{"command_id": core.NewID("cmd"), "instance_id": i.ID, "status": status, "exit_code": res.ExitCode,
		"stdout": res.Stdout, "stderr": res.Stderr, "duration_ms": time.Since(start).Milliseconds()}, nil
}

// ---- images ----

func (s *Service) listImages(c *httpx.Ctx) (any, error) {
	out := []Image{}
	for _, im := range catalog {
		im.Owner, im.State = "homecloud", "available"
		out = append(out, im)
	}
	return append(out, store.List[Image](s.env.Store, cImages)...), nil
}

func (s *Service) getImage(c *httpx.Ctx) (any, error) { return s.image(c.Param("id")) }

func (s *Service) getVolume(c *httpx.Ctx) (any, error) {
	v, err := store.Get[Volume](s.env.Store, cVolumes, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("volume", c.Param("id"))
	}
	return v, nil
}

func (s *Service) registerImage(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Ref         string `json:"ref"`
		KeepAlive   bool   `json:"keep_alive"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Name == "" || in.Ref == "" {
		return nil, core.BadRequest("name and ref (a Docker image reference) are required")
	}
	im := Image{ID: core.NewID("ami"), Name: in.Name, Description: in.Description, Ref: in.Ref, Platform: "linux",
		KeepAlive: in.KeepAlive, Owner: s.env.AccountID, State: "available", CreatedAt: core.Now().Format(time.RFC3339)}
	return im, store.Put(s.env.Store, cImages, im.ID, im)
}

func (s *Service) createImage(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if i.ContainerID == "" || i.State == "terminated" {
		return nil, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance %s has no disk to capture", i.ID)
	}
	if in.Name == "" {
		in.Name = i.ID + "-image"
	}
	id := core.NewID("ami")
	if _, err := s.env.Docker.C.CommitContainer(docker.CommitContainerOptions{
		Container: i.ContainerID, Repository: "homecloud/ami", Tag: id, Message: in.Description,
		Run: &docker.Config{Labels: map[string]string{core.LabelManaged: "true"}},
	}); err != nil {
		return nil, fmt.Errorf("capture image: %w", err)
	}
	im := Image{ID: id, Name: in.Name, Description: in.Description, Ref: "homecloud/ami:" + id, Platform: "linux",
		KeepAlive: i.KeepAlive, Owner: s.env.AccountID, State: "available", CreatedAt: core.Now().Format(time.RFC3339), SourceInstance: i.ID}
	return im, store.Put(s.env.Store, cImages, im.ID, im)
}

func (s *Service) deregisterImage(c *httpx.Ctx) (any, error) {
	im, err := store.Get[Image](s.env.Store, cImages, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("image", c.Param("id"))
	}
	if im.SourceInstance != "" {
		_ = s.env.Docker.C.RemoveImage(im.Ref)
	}
	return nil, store.Delete(s.env.Store, cImages, im.ID)
}

// ---- volumes ----

func (s *Service) createVolume(name string, size int, az string, tags core.Tags) (Volume, error) {
	v := Volume{ID: core.NewID("vol"), Name: name, SizeGB: size, State: "available", AvailabilityZone: az, CreatedAt: core.Now(), Tags: tags}
	if err := s.env.Docker.CreateVolume(volumeName(v.ID), runtime.Labels("ebs", v.ID, nil)); err != nil {
		return v, err
	}
	return v, store.Put(s.env.Store, cVolumes, v.ID, v)
}

func (s *Service) listVolumes(c *httpx.Ctx) (any, error) {
	return store.List[Volume](s.env.Store, cVolumes), nil
}

func (s *Service) createVolumeRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Name             string    `json:"name"`
		SizeGB           int       `json:"size_gb"`
		AvailabilityZone string    `json:"availability_zone"`
		Tags             core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.SizeGB <= 0 {
		in.SizeGB = 8
	}
	if in.AvailabilityZone == "" {
		in.AvailabilityZone = s.env.Cfg.Region + "a"
	}
	return s.createVolume(in.Name, in.SizeGB, in.AvailabilityZone, in.Tags)
}

func (s *Service) deleteVolume(c *httpx.Ctx) (any, error) {
	v, err := store.Get[Volume](s.env.Store, cVolumes, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("volume", c.Param("id"))
	}
	if v.State == "in-use" {
		return nil, core.Errf(http.StatusConflict, "VolumeInUse", "volume %s is attached to %s", v.ID, v.AttachedTo)
	}
	if err := s.env.Docker.RemoveVolume(volumeName(v.ID)); err != nil {
		return nil, err
	}
	return nil, store.Delete(s.env.Store, cVolumes, v.ID)
}

// Names returns instance display names by ID, for other services.
func (s *Service) Names() map[string]string {
	out := map[string]string{}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		out[i.ID] = i.Name
	}
	return out
}

// PrivateIP resolves a running instance to its private IP and VPC (for load balancer targets).
func (s *Service) PrivateIP(id string) (string, string, bool) {
	i, err := store.Get[Instance](s.env.Store, cInstances, id)
	if err != nil || i.State != "running" {
		return "", "", false
	}
	return i.PrivateIP, i.VpcID, true
}

// Instances returns every instance with its state reconciled against Docker.
func (s *Service) Instances() []Instance { return s.list() }
