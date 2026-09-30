// Package ec2 implements compute instances as resource-limited containers
// placed in a VPC subnet, plus AMIs, instance types and EBS-style volumes.
package ec2

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/netip"
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
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2/vm"
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
	// Device is the EC2 device name (/dev/sdf); the volume is mounted at MountPath.
	Device     string    `json:"device,omitempty"`
	AttachTime time.Time `json:"attach_time,omitempty"`
}

// MetadataOptions configure the instance metadata service for an instance.
type MetadataOptions struct {
	HttpTokens           string `json:"http_tokens,omitempty"`   // optional | required
	HttpEndpoint         string `json:"http_endpoint,omitempty"` // enabled | disabled
	HopLimit             int    `json:"hop_limit,omitempty"`
	InstanceMetadataTags string `json:"instance_metadata_tags,omitempty"` // enabled | disabled
}

func (m MetadataOptions) withDefaults() MetadataOptions {
	if m.HttpTokens == "" {
		m.HttpTokens = "optional"
	}
	if m.HttpEndpoint == "" {
		m.HttpEndpoint = "enabled"
	}
	if m.HopLimit == 0 {
		m.HopLimit = 1
	}
	if m.InstanceMetadataTags == "" {
		m.InstanceMetadataTags = "disabled"
	}
	return m
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

	// KeyName is the key pair whose public key is in root's authorized_keys.
	KeyName string `json:"key_name,omitempty"`
	// IAMProfileARN is the instance profile whose role the metadata service
	// hands out credentials for.
	IAMProfileARN string          `json:"iam_profile_arn,omitempty"`
	IAMProfileID  string          `json:"iam_profile_id,omitempty"`
	Metadata      MetadataOptions `json:"metadata_options"`
	// EC2 attributes (recorded; DisableAPITermination and DisableAPIStop are enforced).
	DisableAPITermination bool   `json:"disable_api_termination,omitempty"`
	DisableAPIStop        bool   `json:"disable_api_stop,omitempty"`
	ShutdownBehavior      string `json:"shutdown_behavior,omitempty"`
	SourceDestCheckOff    bool   `json:"source_dest_check_off,omitempty"`
	CPUCredits            string `json:"cpu_credits,omitempty"`
	Monitoring            bool   `json:"monitoring,omitempty"`
	EBSOptimized          bool   `json:"ebs_optimized,omitempty"`
	ReservationID         string `json:"reservation_id,omitempty"`
	LaunchIndex           int    `json:"launch_index,omitempty"`
	ClientToken           string `json:"client_token,omitempty"`
	LaunchTemplateID      string `json:"launch_template_id,omitempty"`
	LaunchTemplateVersion string `json:"launch_template_version,omitempty"`
	// RootImage is the image the container was last created from when it
	// differs from the AMI (a volume attach recreates the container from a
	// snapshot of its disk).
	RootImage string `json:"root_image,omitempty"`

	// Virtual machine instances (an AMI with a VMBase): "kvm" when the guest
	// runs with hardware acceleration, "emulated" when the Docker host has no
	// /dev/kvm and QEMU emulates the CPU (slow). Empty for container instances.
	Virtualization string `json:"virtualization,omitempty"`
	VMBase         string `json:"vm_base,omitempty"`    // key of the cloud image (vm.Bases)
	VMUser         string `json:"vm_user,omitempty"`    // the image's default login user
	VMDiskGB       int    `json:"vm_disk_gb,omitempty"` // root disk size
	// VMNetwork is "passt" (the guest has the instance's address) or "user"
	// (passt could not start: the guest is behind NAT with forwarded ports).
	VMNetwork string `json:"vm_network,omitempty"`
	// VMAMI is the Docker volume holding the disk of the image (made from an
	// instance) the instance was launched from; the root disk was copied from it.
	VMAMI string `json:"vm_ami,omitempty"`
}

// IsVM reports whether the instance is a virtual machine.
func (i Instance) IsVM() bool { return i.Virtualization != "" }

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
	// EBS attributes (recorded).
	VolumeType string `json:"volume_type,omitempty"`
	Iops       int    `json:"iops,omitempty"`
	Throughput int    `json:"throughput,omitempty"`
	Encrypted  bool   `json:"encrypted,omitempty"`
	KMSKeyID   string `json:"kms_key_id,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	// Device and AttachTime describe the current attachment.
	Device              string    `json:"device,omitempty"`
	AttachTime          time.Time `json:"attach_time,omitempty"`
	DeleteOnTermination bool      `json:"delete_on_termination,omitempty"`
	AttachState         string    `json:"attach_state,omitempty"` // attaching | attached | detaching
	// VMRoot is set on the root volume of a VM instance.
	VMRoot bool `json:"vm_root,omitempty"`
}

func volumeName(id string) string { return "hc-" + id }

type Service struct {
	// DNSFor returns resolver addresses for containers in a VPC (Route 53).
	DNSFor  func(vpcID string) []string
	env     *svc.Env
	vpc     *vpc.Service
	hostCPU float64
	mu      sync.Mutex // serialises state transitions
	efsMu   sync.Mutex // serialises file system, mount target and access point changes
	// OnTerminate is called after an instance is terminated (e.g. to deregister it from target groups).
	OnTerminate func(id string)
	// TemplateInUse names what still uses a launch template (an Auto Scaling group), or "".
	TemplateInUse func(id string) string
	// Roles resolves instance profiles and issues their role's credentials (IAM).
	Roles Roles
	imds  *imds
	// IMDSContainer names the metadata service container (default homecloud-imds).
	// Tests running beside a real installation use their own.
	IMDSContainer string
	// VMImageVolume is the Docker volume caching downloaded cloud images.
	VMImageVolume string
	vm            vmState
}

// Roles is what EC2 needs from IAM for instance profiles.
type Roles interface {
	// InstanceProfile resolves a profile name or ARN to its ARN, ID and role name.
	InstanceProfile(ref string) (arn, id, role string, err error)
	// InstanceCredentials issues credentials for a role to ec2.amazonaws.com.
	InstanceCredentials(role, session string, ttl time.Duration) (Credentials, error)
}

// Credentials are temporary credentials for an instance's role.
type Credentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	Expiration                                 time.Time
}

func New(env *svc.Env, v *vpc.Service) *Service {
	s := &Service{env: env, vpc: v, hostCPU: 1}
	if env.Docker != nil { // tests of the API layer run without containers
		if info, err := env.Docker.C.Info(); err == nil && info.NCPU > 0 {
			s.hostCPU = float64(info.NCPU)
		}
	}
	v.GroupChanged = s.groupChanged
	v.RegisterMembers(s.fwMembers)
	v.InUse = func(sg string) bool {
		for _, i := range store.List[Instance](env.Store, cInstances) {
			if i.State != "terminated" && slices.Contains(i.SecurityGroups, sg) {
				return true
			}
		}
		for _, m := range store.List[MountTarget](env.Store, cMountTargets) {
			if slices.Contains(m.SecurityGroups, sg) {
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
	if i.State == "terminated" || i.State == "pending" || i.ContainerID == "" || isBusy(i.ID) {
		return i
	}
	st := s.env.Docker.State(i.ContainerID)
	want := i.State
	if st == "missing" && i.IsVM() {
		// The disk outlives the container (a restored backup has volumes but no containers).
		if ri, ok := s.vmRecoverContainer(i); ok {
			return ri
		}
	}
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
			s.releaseVolumes(i)
			s.vpc.Release(i.ID)
			s.instanceGone(i)
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
	res := httpx.Res("arn:aws:ec2:{region}:{account}:instance/{id}")
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
	r.Handle("GET /api/v1/ec2/capabilities", "ec2:DescribeInstanceTypes", s.capabilities)
	r.Handle("GET /api/v1/ec2/images", "ec2:DescribeImages", s.listImages)
	r.Handle("POST /api/v1/ec2/images", "ec2:RegisterImage", s.registerImage)
	imgRes := httpx.Res("arn:aws:ec2:{region}:{account}:image/{id}")
	volRes := httpx.Res("arn:aws:ec2:{region}:{account}:volume/{id}")
	r.Handle("GET /api/v1/ec2/images/{id}", "ec2:DescribeImages", s.getImage, imgRes)
	r.Handle("DELETE /api/v1/ec2/images/{id}", "ec2:DeregisterImage", s.deregisterImage, imgRes)

	s.efsRoutes(r)
	s.networkRoutes(r)
	s.addressRoutes(r)
	r.Handle("GET /api/v1/ec2/volumes", "ec2:DescribeVolumes", s.listVolumes)
	r.Handle("POST /api/v1/ec2/volumes", "ec2:CreateVolume", s.createVolumeRoute)
	r.Handle("GET /api/v1/ec2/volumes/{id}", "ec2:DescribeVolumes", s.getVolume, volRes)
	r.Handle("DELETE /api/v1/ec2/volumes/{id}", "ec2:DeleteVolume", s.deleteVolume, volRes)
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
	Name             string       `json:"name"`
	ImageID          string       `json:"image_id"`
	InstanceType     string       `json:"instance_type"`
	SubnetID         string       `json:"subnet_id"`
	SecurityGroupIDs []string     `json:"security_group_ids"`
	UserData         string       `json:"user_data"`
	Count            int          `json:"count"`
	Tags             core.Tags    `json:"tags"`
	Volumes          []VolumeSpec `json:"volumes"`
	FileSystems      []FSMount    `json:"file_systems"`
	// RootVolume sizes the root disk of a VM instance (default 8 GiB) and
	// whether it is deleted on termination. Container instances ignore it.
	RootVolume *VolumeSpec `json:"root_volume,omitempty"`
	// KeyName puts a key pair's public key in root's authorized_keys (the
	// default user's, for VM images).
	KeyName string `json:"key_name"`
	// IAMInstanceProfile (name or ARN) gives the instance its role's credentials
	// through the metadata service. The caller must be allowed iam:PassRole.
	IAMInstanceProfile string          `json:"iam_instance_profile"`
	Metadata           MetadataOptions `json:"metadata_options"`
	// Attrs are EC2 API attributes (RunInstances).
	Attrs InstanceAttrs `json:"-"`
	// VolumeTags are applied to volumes created at launch.
	VolumeTags core.Tags `json:"-"`
	// ExactName gives every instance of a multi-instance launch the same name
	// (EC2 API) instead of numbering them.
	ExactName bool `json:"-"`
}

// VolumeSpec is a volume to attach at launch: an existing one or a new one.
type VolumeSpec struct {
	VolumeID            string `json:"volume_id"` // attach an existing available volume
	SizeGB              int    `json:"size_gb"`   // or create a new one
	MountPath           string `json:"mount_path"`
	DeleteOnTermination *bool  `json:"delete_on_termination"`
	// EC2 block device mapping fields.
	Device     string `json:"device,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	VolumeType string `json:"volume_type,omitempty"`
	Iops       int    `json:"iops,omitempty"`
	Throughput int    `json:"throughput,omitempty"`
	Encrypted  bool   `json:"encrypted,omitempty"`
	KMSKeyID   string `json:"kms_key_id,omitempty"`
}

// InstanceAttrs are the EC2 API's instance attributes.
type InstanceAttrs struct {
	DisableAPITermination, DisableAPIStop, SourceDestCheckOff, Monitoring, EBSOptimized bool
	ShutdownBehavior, CPUCredits, ClientToken, ReservationID                            string
	LaunchTemplateID, LaunchTemplateVersion                                             string
}

// devicePath is where an EBS device is mounted: /dev/sdf -> /mnt/sdf.
func devicePath(device string) string {
	return "/mnt/" + device[strings.LastIndexByte(device, '/')+1:]
}

func (s *Service) image(id string) (Image, error) {
	for _, im := range catalog {
		if im.ID == id {
			im.Owner, im.State = "homecloud", "available"
			return im.normalized(), nil
		}
	}
	im, err := store.Get[Image](s.env.Store, cImages, id)
	if err != nil {
		return im, core.NotFound("image", id)
	}
	return im.normalized(), nil
}

// checkTags rejects user tags in the reserved hc: namespace (used by HomeCloud
// itself, e.g. hc:autoscaling:groupName decides Auto Scaling membership).
func checkTags(t core.Tags) error {
	for k := range t {
		if strings.HasPrefix(strings.ToLower(k), "hc:") {
			return core.BadRequest("tag keys starting with hc: are reserved")
		}
	}
	return nil
}

func (s *Service) run(c *httpx.Ctx) (any, error) {
	var in RunInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	for _, v := range in.Volumes {
		if v.VolumeID != "" {
			if err := c.Authorize("ec2:AttachVolume", s.env.ARN("ec2", "volume/"+v.VolumeID)); err != nil {
				return nil, err
			}
		}
	}
	for _, m := range in.FileSystems {
		if err := c.Authorize("elasticfilesystem:ClientMount", s.env.ARN("elasticfilesystem", "file-system/"+m.FileSystemID)); err != nil {
			return nil, err
		}
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
		return nil, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "unknown instance type %q", in.InstanceType)
	}
	if in.ImageID == "" {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "image_id is required")
	}
	img, err := s.image(in.ImageID)
	if err != nil {
		return nil, err
	}
	var vmPlan vmPlan
	if img.IsVM() {
		if vmPlan, err = s.planVM(in, img); err != nil {
			return nil, err
		}
	}
	var key KeyPair
	if in.KeyName != "" {
		if key, err = s.keyPair(in.KeyName); err != nil {
			return nil, err
		}
	}
	var profileARN, profileID string
	if in.IAMInstanceProfile != "" {
		if s.Roles == nil {
			return nil, core.BadRequest("instance profiles are not available")
		}
		if profileARN, profileID, _, err = s.Roles.InstanceProfile(in.IAMInstanceProfile); err != nil {
			return nil, err
		}
	}
	md := in.Metadata.withDefaults()
	if err := checkMetadata(md); err != nil {
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
		if !img.IsVM() && !strings.HasPrefix(v.MountPath, "/") { // VM disks have a device, not a mount path
			return nil, core.BadRequest("volume mount_path must be absolute")
		}
		if v.SnapshotID != "" {
			if _, err := s.snapshot(v.SnapshotID); err != nil {
				return nil, err
			}
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
			n := 0
			for _, o := range in.Volumes {
				if o.VolumeID == v.VolumeID {
					n++
				}
			}
			if n > 1 {
				return nil, core.BadRequest("volume %s is listed twice", v.VolumeID)
			}
		}
	}
	reservation := in.Attrs.ReservationID
	if reservation == "" {
		reservation = core.NewID("r")
	}

	var launched []Instance
	for n := 0; n < in.Count; n++ {
		id := core.NewID("i")
		pl, err := s.vpc.Place(in.SubnetID, id)
		if err != nil {
			return launched, err
		}
		sgs := in.SecurityGroupIDs
		if len(sgs) == 0 {
			sgs = []string{s.vpc.DefaultSecurityGroup(pl.VPC.ID)}
		}
		if err := s.vpc.CheckGroups(pl.VPC.ID, sgs); err != nil {
			s.vpc.Release(id)
			return launched, err
		}
		name := in.Name
		if name != "" && in.Count > 1 && !in.ExactName {
			name = fmt.Sprintf("%s-%d", in.Name, n+1)
		}
		inst := Instance{
			ID: id, Name: name, ARN: s.env.ARN("ec2", "instance/"+id), ImageID: img.ID, ImageRef: img.Ref,
			InstanceType: it.Name, VCPUs: it.VCPUs, MemoryMB: it.MemoryMB, State: "pending",
			VpcID: pl.VPC.ID, SubnetID: pl.Subnet.ID, AvailabilityZone: pl.Subnet.AvailabilityZone,
			PrivateIP: pl.IP, PrivateDNS: "ip-" + strings.ReplaceAll(pl.IP, ".", "-") + ".internal",
			SecurityGroups: sgs, UserData: in.UserData, KeepAlive: img.KeepAlive, Volumes: []VolumeAttachment{}, FileSystems: nzFS(in.FileSystems),
			PublicPorts: map[string]int{}, PublicHost: s.env.Cfg.PublicHost, LaunchTime: core.Now(), Tags: in.Tags,
			KeyName: key.Name, IAMProfileARN: profileARN, IAMProfileID: profileID, Metadata: md,
			DisableAPITermination: in.Attrs.DisableAPITermination, DisableAPIStop: in.Attrs.DisableAPIStop,
			ShutdownBehavior: in.Attrs.ShutdownBehavior, SourceDestCheckOff: in.Attrs.SourceDestCheckOff, CPUCredits: in.Attrs.CPUCredits,
			Monitoring: in.Attrs.Monitoring, EBSOptimized: in.Attrs.EBSOptimized, ReservationID: reservation, LaunchIndex: n,
			ClientToken: in.Attrs.ClientToken, LaunchTemplateID: in.Attrs.LaunchTemplateID, LaunchTemplateVersion: in.Attrs.LaunchTemplateVersion,
		}
		vols := in.Volumes
		if img.IsVM() {
			inst.Virtualization, inst.VMBase, inst.VMUser, inst.VMDiskGB, inst.VMAMI = vmPlan.virtualization, img.VMBase, vmPlan.user, vmPlan.diskGB, vmPlan.ami
			for n := range vols { // extra volumes: name their EBS device when the caller did not
				if vols[n].Device == "" {
					vols[n].Device = fmt.Sprintf("/dev/sd%c", 'f'+n)
				}
			}
			inst.ImageRef = "" // a VM image has no Docker image
			vols = append([]VolumeSpec{vmPlan.root}, vols...)
		}
		if inst.ShutdownBehavior == "" {
			inst.ShutdownBehavior = "stop"
		}
		if inst.CPUCredits == "" && strings.HasPrefix(it.Name, "t") {
			inst.CPUCredits = "unlimited"
		}
		// undo releases what this instance claimed if a later step fails.
		undo := func() {
			s.releaseVolumes(inst)
			s.vpc.Release(id)
		}
		for _, v := range vols {
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
				vol, err := s.createVolume(VolumeInput{Size: size, AZ: pl.Subnet.AvailabilityZone, Tags: in.VolumeTags, Type: v.VolumeType,
					Iops: v.Iops, Throughput: v.Throughput, Encrypted: v.Encrypted, KMSKeyID: v.KMSKeyID, SnapshotID: v.SnapshotID, Sync: true, VMRoot: img.IsVM() && v.MountPath == vm.DiskDir})
				if err != nil {
					undo()
					return launched, err
				}
				volID = vol.ID
			}
			now := core.Now()
			// Claim atomically: two launches must not both attach the same volume.
			if _, err := store.Update(s.env.Store, cVolumes, volID, func(x *Volume) error {
				if x.State != "available" {
					return core.Conflict("volume %s is %s", x.ID, x.State)
				}
				x.State, x.AttachedTo, x.MountPath = "in-use", id, v.MountPath
				x.Device, x.AttachTime, x.DeleteOnTermination = v.Device, now, del
				return nil
			}); err != nil {
				undo()
				return launched, err
			}
			inst.Volumes = append(inst.Volumes, VolumeAttachment{VolumeID: volID, MountPath: v.MountPath, DeleteOnTermination: del, Device: v.Device, AttachTime: now})
		}
		if err := store.Put(s.env.Store, cInstances, id, inst); err != nil {
			undo()
			return launched, err
		}
		launched = append(launched, inst)
		if inst.IsVM() {
			go s.launchVM(inst, pl.Network)
		} else {
			go s.launch(inst, pl.Network)
		}
	}
	return launched, nil
}

func checkMetadata(m MetadataOptions) error {
	if m.HttpTokens != "optional" && m.HttpTokens != "required" {
		return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "HttpTokens must be optional or required")
	}
	if m.HttpEndpoint != "enabled" && m.HttpEndpoint != "disabled" {
		return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "HttpEndpoint must be enabled or disabled")
	}
	if m.InstanceMetadataTags != "enabled" && m.InstanceMetadataTags != "disabled" {
		return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "InstanceMetadataTags must be enabled or disabled")
	}
	if m.HopLimit < 1 || m.HopLimit > 64 {
		return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "HttpPutResponseHopLimit must be between 1 and 64")
	}
	return nil
}

// runSpec is the container of an instance.
func (s *Service) runSpec(inst Instance, network string) runtime.RunSpec {
	if inst.IsVM() {
		return s.vmRunSpec(inst, network)
	}
	mounts := []runtime.Mount{}
	for _, v := range inst.Volumes {
		mounts = append(mounts, runtime.Mount{Volume: volumeName(v.VolumeID), Target: v.MountPath})
	}
	for _, m := range inst.FileSystems {
		mounts = append(mounts, runtime.Mount{Volume: fsVolume(m.FileSystemID), Target: m.MountPath, ReadOnly: m.ReadOnly})
	}
	image := inst.ImageRef
	if inst.RootImage != "" {
		image = inst.RootImage
	}
	spec := runtime.RunSpec{
		DNS:      s.dns(inst.VpcID),
		Name:     svc.ContainerName("ec2", inst.ID),
		Image:    image,
		Labels:   runtime.Labels("ec2", inst.ID, map[string]string{"homecloud.name": inst.Name}),
		NanoCPUs: int64(min(inst.VCPUs, s.hostCPU) * 1e9),
		MemoryMB: inst.MemoryMB,
		Ports:    s.portsFor(inst),
		Mounts:   mounts,
		Network:  network,
		IP:       inst.PrivateIP,
		Aliases:  append([]string{inst.ID, inst.PrivateDNS}, nonEmpty(inst.Name)...),
		Hostname: strings.TrimSuffix(inst.PrivateDNS, ".internal"),
		Env: map[string]string{
			"HC_INSTANCE_ID": inst.ID, "HC_INSTANCE_TYPE": inst.InstanceType, "HC_REGION": s.env.Cfg.Region, "HC_PRIVATE_IP": inst.PrivateIP,
		},
		ExtraHosts: []string{runtime.HostAlias},
	}
	// SDKs inside the instance talk to HomeCloud, not AWS, and find their
	// credentials through the instance metadata service.
	for k, v := range s.awsEnv() {
		spec.Env[k] = v
	}
	if inst.KeepAlive {
		spec.Entrypoint = []string{"/bin/sh", "-c"}
		spec.Cmd = []string{bootScript}
	}
	return spec
}

func (s *Service) awsEnv() map[string]string {
	env := map[string]string{"AWS_REGION": s.env.Cfg.Region, "AWS_DEFAULT_REGION": s.env.Cfg.Region}
	if s.env.ContainerAPI != "" {
		env["AWS_ENDPOINT_URL"] = s.env.ContainerAPI
	}
	return env
}

// bootFiles are written into a new instance's disk before it starts.
func (s *Service) bootFiles(inst Instance) (map[string][]byte, error) {
	meta, _ := json.MarshalIndent(map[string]any{
		"instance-id": inst.ID, "instance-type": inst.InstanceType, "ami-id": inst.ImageID, "local-ipv4": inst.PrivateIP,
		"local-hostname": inst.PrivateDNS, "placement": map[string]string{"availability-zone": inst.AvailabilityZone, "region": s.env.Cfg.Region},
		"vpc-id": inst.VpcID, "subnet-id": inst.SubnetID, "security-groups": inst.SecurityGroups, "tags": inst.Tags,
	}, "", "  ")
	files := map[string][]byte{"var/lib/homecloud/instance.json": meta}
	if inst.UserData != "" {
		files["var/lib/homecloud/user-data"] = []byte(inst.UserData)
	}
	var profile strings.Builder
	profile.WriteString("# Set by HomeCloud: AWS SDKs and the AWS CLI reach HomeCloud and get credentials from the instance metadata service.\n")
	env := s.awsEnv()
	for _, k := range []string{"AWS_ENDPOINT_URL", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v, ok := env[k]; ok {
			fmt.Fprintf(&profile, "export %s=%s\n", k, v)
		}
	}
	files["etc/profile.d/homecloud-aws.sh"] = []byte(profile.String())
	if inst.KeyName != "" {
		if k, err := s.keyPair(inst.KeyName); err == nil {
			files["root/.ssh/authorized_keys"] = []byte(strings.TrimSpace(k.PublicKey) + "\n")
		}
	}
	return files, nil
}

// failLaunch terminates an instance whose launch failed and releases what it claimed.
func (s *Service) failLaunch(inst Instance, err error) {
	log.Printf("ec2: launch %s failed: %v", inst.ID, err)
	already := false
	_, _ = store.Update(s.env.Store, cInstances, inst.ID, func(x *Instance) error {
		if x.State == "terminated" { // terminated while launching: already cleaned up
			already = true
			return nil
		}
		n := core.Now()
		x.State, x.StateReason, x.TerminatedAt = "terminated", "Server.LaunchFailure: "+err.Error(), &n
		return nil
	})
	if !already {
		s.releaseVolumes(inst)
		s.vpc.Release(inst.ID)
	}
}

// launch creates and boots the instance's container in the background.
func (s *Service) launch(inst Instance, network string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	fail := func(err error) { s.failLaunch(inst, err) }
	cid, err := s.env.Docker.Run(ctx, s.runSpec(inst, network))
	if err != nil {
		fail(err)
		return
	}
	files, _ := s.bootFiles(inst)
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
	s.metadataRoute(ctx, cid, inst.VpcID)
	s.vpc.ProtectNow(ctx, s.member(inst, cid))
	_, _ = store.Update(s.env.Store, cInstances, inst.ID, func(x *Instance) error {
		if x.State != "pending" { // terminated while launching
			go s.env.Docker.Remove(cid)
			return nil
		}
		x.ContainerID, x.State, x.StateReason = cid, "running", ""
		x.PublicPorts = s.env.Docker.PublishedPorts(cid)
		return nil
	})
	s.syncPortsAsync(inst.ID) // groups may have changed while launching
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
		defer core.Recover("ec2 " + during + " " + id)
		// Only settle the state if nothing (e.g. a terminate) changed it meanwhile.
		if err := fn(i); err != nil {
			_, _ = store.Update(s.env.Store, cInstances, id, func(x *Instance) error {
				if x.State == during {
					x.State, x.StateReason = i.State, err.Error()
				}
				return nil
			})
			return
		}
		_, _ = store.Update(s.env.Store, cInstances, id, func(x *Instance) error {
			if x.State != during {
				return nil
			}
			x.State, x.StateReason = after, ""
			if after == "running" {
				x.PublicPorts = s.env.Docker.PublishedPorts(x.ContainerID)
			}
			return nil
		})
		s.syncPortsAsync(id) // groups may have changed meanwhile
	}()
	return cur, nil
}

func (s *Service) start(c *httpx.Ctx) (any, error) { return s.StartInstance(c.Param("id")) }

func (s *Service) stop(c *httpx.Ctx) (any, error) { return s.StopInstance(c.Param("id"), false) }

func (s *Service) reboot(c *httpx.Ctx) (any, error) { return s.RebootInstance(c.Param("id")) }

// StartInstance starts a stopped instance.
func (s *Service) StartInstance(id string) (any, error) {
	return s.transition(id, []string{"stopped"}, func(i Instance) error {
		if i.IsVM() {
			return s.vmStart(i)
		}
		if err := s.env.Docker.Start(i.ContainerID); err != nil {
			return err
		}
		s.metadataRoute(context.Background(), i.ContainerID, i.VpcID)
		return nil
	}, "pending", "running")
}

// StopInstance stops a running instance; force skips the graceful shutdown.
func (s *Service) StopInstance(id string, force bool) (any, error) {
	if i, err := s.get(id); err == nil && i.DisableAPIStop {
		return nil, core.Errf(http.StatusBadRequest, "OperationNotPermitted", "the instance '%s' may not be stopped; modify its 'disableApiStop' attribute and try again", id)
	}
	timeout := uint(10)
	if force {
		timeout = 0
	}
	return s.transition(id, []string{"running"}, func(i Instance) error {
		if i.IsVM() {
			return s.vmStop(i, force)
		}
		return s.env.Docker.Stop(i.ContainerID, timeout)
	}, "stopping", "stopped")
}

// RebootInstance restarts a running instance.
func (s *Service) RebootInstance(id string) (any, error) {
	return s.transition(id, []string{"running"}, func(i Instance) error {
		if i.IsVM() {
			return s.vmReboot(i)
		}
		if err := s.env.Docker.Restart(i.ContainerID); err != nil {
			return err
		}
		s.metadataRoute(context.Background(), i.ContainerID, i.VpcID)
		return nil
	}, "running", "running")
}

func (s *Service) terminate(c *httpx.Ctx) (any, error) {
	if i, err := s.get(c.Param("id")); err == nil && i.DisableAPITermination {
		return nil, core.Errf(http.StatusBadRequest, "OperationNotPermitted", "the instance '%s' may not be terminated; modify its 'disableApiTermination' attribute and try again", i.ID)
	}
	return s.Terminate(c.Param("id"))
}

// CheckLaunch validates a launch's image, instance type, subnets and security
// groups without launching anything, and returns the VPC the subnets are in.
func (s *Service) CheckLaunch(in RunInput, subnets []string) (string, error) {
	typ := in.InstanceType
	if typ == "" {
		typ = "t3.micro"
	}
	if _, ok := findType(typ); !ok {
		return "", core.BadRequest("unknown instance type %q", typ)
	}
	if _, err := s.image(in.ImageID); err != nil {
		return "", err
	}
	if len(subnets) == 0 {
		subnets = []string{""}
	}
	vpcID := ""
	for _, sn := range subnets {
		v, err := s.vpc.SubnetVPC(sn)
		if err != nil {
			return "", err
		}
		if vpcID != "" && v != vpcID {
			return "", core.BadRequest("subnets must all be in the same VPC")
		}
		vpcID = v
	}
	if err := s.vpc.CheckGroups(vpcID, in.SecurityGroupIDs); err != nil {
		return "", err
	}
	return vpcID, nil
}

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
	s.instanceGone(i)
	out, err := store.Update(s.env.Store, cInstances, id, func(x *Instance) error {
		n := core.Now()
		x.State, x.TerminatedAt, x.PublicPorts, x.StateReason = "terminated", &n, map[string]int{}, "Client.UserInitiatedShutdown"
		return nil
	})
	s.vpc.FirewallChanged() // its address leaves every group it was in
	if err == nil && s.OnTerminate != nil {
		s.OnTerminate(id)
	}
	return out, err
}

func (s *Service) releaseVolumes(i Instance) {
	for _, v := range i.Volumes {
		if v.DeleteOnTermination {
			_ = s.env.Docker.RemoveVolume(volumeName(v.VolumeID))
			_ = store.Delete(s.env.Store, cVolumes, v.VolumeID)
			continue
		}
		_, _ = store.Update(s.env.Store, cVolumes, v.VolumeID, func(x *Volume) error {
			x.State, x.AttachedTo, x.MountPath, x.Device, x.AttachTime, x.DeleteOnTermination = "available", "", "", "", time.Time{}, false
			x.AttachState = ""
			return nil
		})
	}
}

// instanceGone releases what a terminated instance held besides its
// addresses and volumes: its disk snapshot image and cached role credentials.
func (s *Service) instanceGone(i Instance) {
	defer s.vpc.FirewallChanged()
	s.dropAddress(i.ID)
	if i.RootImage != "" {
		_ = s.env.Docker.C.RemoveImage(i.RootImage)
	}
	if s.imds != nil {
		s.imds.forget(i.ID)
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
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if i.State == "terminated" || i.ContainerID == "" {
		return nil, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance is %s", i.State)
	}
	if in.InstanceType != "" && in.InstanceType != i.InstanceType {
		if i, err = s.ChangeType(i.ID, in.InstanceType); err != nil {
			return nil, err
		}
	}
	return store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		if in.Name != nil {
			x.Name = *in.Name
		}
		if in.Tags != nil {
			x.Tags = in.Tags
		}
		return nil
	})
}

// ChangeType resizes a stopped instance to another instance type.
func (s *Service) ChangeType(id, typ string) (Instance, error) {
	i, err := s.get(id)
	if err != nil {
		return i, err
	}
	if typ == i.InstanceType {
		return i, nil
	}
	it, ok := findType(typ)
	if !ok {
		return i, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "unknown instance type %q", typ)
	}
	if i.State != "stopped" || i.ContainerID == "" {
		return i, core.Errf(http.StatusConflict, "IncorrectInstanceState", "the instance %s must be stopped to change its type", i.ID)
	}
	if i.IsVM() {
		// The guest's CPU and memory are QEMU arguments: rebuild the container.
		if _, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
			x.InstanceType, x.VCPUs, x.MemoryMB = it.Name, it.VCPUs, it.MemoryMB
			return nil
		}); err != nil {
			return i, err
		}
		if err := s.recreateVM(i.ID); err != nil {
			return i, fmt.Errorf("resize virtual machine: %w", err)
		}
		return s.get(i.ID)
	}
	mem := it.MemoryMB * 1024 * 1024
	if err := s.env.Docker.C.UpdateContainer(i.ContainerID, docker.UpdateContainerOptions{
		Memory: int(mem), MemorySwap: int(mem * 2), CPUPeriod: 100000, CPUQuota: int(min(it.VCPUs, s.hostCPU) * 100000),
	}); err != nil {
		return i, fmt.Errorf("resize container: %w", err)
	}
	return store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		x.InstanceType, x.VCPUs, x.MemoryMB = it.Name, it.VCPUs, it.MemoryMB
		return nil
	})
}

// publicIP is the address an instance's published ports are reachable on:
// the HomeCloud host, when it is configured as an IP address, or the
// instance's Elastic IP.
func (s *Service) publicIP(i Instance) string {
	if ip := s.elasticIPFor(i.ID); ip != "" {
		return ip
	}
	if len(i.PublicPorts) == 0 {
		return ""
	}
	if a, err := netip.ParseAddr(i.PublicHost); err == nil && a.Is4() {
		return a.String()
	}
	return ""
}

// tagsOf returns a resource's tags with its name as the Name tag.
func (s *Service) tagsOf(id, name string, tags core.Tags) core.Tags {
	out := core.Tags{}
	for k, v := range tags {
		out[k] = v
	}
	if name != "" {
		out["Name"] = name
	}
	return out
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
	if i.IsVM() {
		return s.vmRunCommand(c.R.Context(), i, in.Command, in.Timeout)
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
	return s.allImages(), nil
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
	return s.CreateImage(c.Param("id"), in.Name, in.Description, nil)
}

// CreateImage captures an instance's disk as a new AMI (docker commit).
func (s *Service) CreateImage(instanceID, name, description string, tags core.Tags) (Image, error) {
	i, err := s.get(instanceID)
	if err != nil {
		return Image{}, err
	}
	if i.ContainerID == "" || i.State == "terminated" {
		return Image{}, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance %s has no disk to capture", i.ID)
	}
	if i.IsVM() {
		return s.createVMImage(i, name, description, tags)
	}
	if name == "" {
		name = i.ID + "-image"
	}
	for _, im := range store.List[Image](s.env.Store, cImages) {
		if im.Name == name {
			return Image{}, core.Errf(http.StatusConflict, "InvalidAMIName.Duplicate", "AMI name %s is already in use by AMI %s", name, im.ID)
		}
	}
	id := core.NewID("ami")
	if _, err := s.env.Docker.C.CommitContainer(docker.CommitContainerOptions{
		Container: i.ContainerID, Repository: "homecloud/ami", Tag: id, Message: description,
		Run: &docker.Config{Labels: map[string]string{core.LabelManaged: "true"}},
	}); err != nil {
		return Image{}, fmt.Errorf("capture image: %w", err)
	}
	im := Image{ID: id, Name: name, Description: description, Ref: "homecloud/ami:" + id, Platform: "linux",
		KeepAlive: i.KeepAlive, Owner: s.env.AccountID, State: "available", CreatedAt: core.Now().Format(time.RFC3339), SourceInstance: i.ID, Tags: tags}
	return im, store.Put(s.env.Store, cImages, im.ID, im)
}

func (s *Service) deregisterImage(c *httpx.Ctx) (any, error) {
	return nil, s.DeregisterImage(c.Param("id"))
}

// DeregisterImage removes an AMI of this account (and its Docker image when
// HomeCloud captured it).
func (s *Service) DeregisterImage(id string) error {
	im, err := store.Get[Image](s.env.Store, cImages, id)
	if err != nil {
		return core.NotFound("image", id)
	}
	if im.VMDisk != "" {
		_ = s.env.Docker.RemoveVolume(im.VMDisk)
	} else if im.SourceInstance != "" {
		_ = s.env.Docker.C.RemoveImage(im.Ref)
	}
	return store.Delete(s.env.Store, cImages, im.ID)
}

// ---- volumes ----

// VolumeInput describes a new volume.
type VolumeInput struct {
	Name       string
	Size       int
	AZ         string
	Tags       core.Tags
	Type       string
	Iops       int
	Throughput int
	Encrypted  bool
	KMSKeyID   string
	// SnapshotID fills the volume from a snapshot; the volume is "creating"
	// until the copy finishes, unless Sync waits for it.
	SnapshotID string
	Sync       bool
	// VMRoot marks the root volume of a VM instance (its disk is a qcow2 overlay that snapshots and backups flatten).
	VMRoot bool
}

func (s *Service) createVolume(in VolumeInput) (Volume, error) {
	if in.Type == "" {
		in.Type = "gp3"
	}
	if in.AZ == "" {
		in.AZ = s.env.Cfg.Region + "a"
	}
	var snap Snapshot
	if in.SnapshotID != "" {
		var err error
		if snap, err = s.snapshot(in.SnapshotID); err != nil {
			return Volume{}, err
		}
		if snap.State != "completed" {
			return Volume{}, core.Errf(http.StatusBadRequest, "IncorrectState", "snapshot %s is %s", snap.ID, snap.State)
		}
		if in.Size == 0 {
			in.Size = snap.VolumeSize
		}
		if in.Size < snap.VolumeSize {
			return Volume{}, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "volume size %d GiB is smaller than snapshot %s (%d GiB)", in.Size, snap.ID, snap.VolumeSize)
		}
	}
	if in.Size == 0 {
		in.Size = 8
	}
	if in.Size < 1 || in.Size > 16384 {
		return Volume{}, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "volume size must be between 1 and 16384 GiB")
	}
	if in.Type == "gp3" {
		if in.Iops == 0 {
			in.Iops = 3000
		}
		if in.Throughput == 0 {
			in.Throughput = 125
		}
	}
	v := Volume{ID: core.NewID("vol"), Name: in.Name, SizeGB: in.Size, State: "available", AvailabilityZone: in.AZ, CreatedAt: core.Now(), Tags: in.Tags,
		VolumeType: in.Type, Iops: in.Iops, Throughput: in.Throughput, Encrypted: in.Encrypted, KMSKeyID: in.KMSKeyID, SnapshotID: in.SnapshotID}
	if v.Name == "" && in.Tags["Name"] != "" {
		v.Name = in.Tags["Name"]
	}
	v.VMRoot = in.VMRoot
	var extra map[string]string
	if in.VMRoot {
		extra = map[string]string{vm.LabelRoot: "true"}
	}
	if err := s.env.Docker.CreateVolume(volumeName(v.ID), runtime.Labels("ebs", v.ID, extra)); err != nil {
		return v, err
	}
	if in.SnapshotID == "" {
		return v, store.Put(s.env.Store, cVolumes, v.ID, v)
	}
	restore := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		return s.copyVolume(ctx, snapVolume(snap.ID), volumeName(v.ID))
	}
	if in.Sync {
		if err := restore(); err != nil {
			_ = s.env.Docker.RemoveVolume(volumeName(v.ID))
			return v, fmt.Errorf("restore snapshot %s: %w", snap.ID, err)
		}
		return v, store.Put(s.env.Store, cVolumes, v.ID, v)
	}
	v.State = "creating"
	if err := store.Put(s.env.Store, cVolumes, v.ID, v); err != nil {
		return v, err
	}
	go func() {
		defer core.Recover("restore " + v.ID)
		state := "available"
		if err := restore(); err != nil {
			log.Printf("ec2: restore %s from %s: %v", v.ID, snap.ID, err)
			state = "error"
		}
		_, _ = store.Update(s.env.Store, cVolumes, v.ID, func(x *Volume) error {
			if x.State == "creating" {
				x.State = state
			}
			return nil
		})
	}()
	return v, nil
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
		SnapshotID       string    `json:"snapshot_id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.createVolume(VolumeInput{Name: in.Name, Size: in.SizeGB, AZ: in.AvailabilityZone, Tags: in.Tags, SnapshotID: in.SnapshotID})
}

func (s *Service) deleteVolume(c *httpx.Ctx) (any, error) { return nil, s.DeleteVolume(c.Param("id")) }

// DeleteVolume removes a volume that is not attached.
func (s *Service) DeleteVolume(id string) error {
	v, err := store.Get[Volume](s.env.Store, cVolumes, id)
	if err != nil {
		return core.NotFound("volume", id)
	}
	if v.State == "in-use" {
		return core.Errf(http.StatusConflict, "VolumeInUse", "volume %s is attached to %s", v.ID, v.AttachedTo)
	}
	if err := s.env.Docker.RemoveVolume(volumeName(v.ID)); err != nil {
		return err
	}
	return store.Delete(s.env.Store, cVolumes, v.ID)
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

func (s *Service) dns(vpcID string) []string {
	if s.DNSFor == nil {
		return nil
	}
	return s.DNSFor(vpcID)
}

// Recover settles instances whose launch or state change was interrupted by a restart.
func (s *Service) Recover() {
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		switch i.State {
		case "pending", "stopping", "shutting-down":
		default:
			continue
		}
		state := "terminated"
		if i.ContainerID != "" {
			switch s.env.Docker.State(i.ContainerID) {
			case "running":
				state = "running"
			case "exited", "created":
				state = "stopped"
			}
		}
		if i.State == "shutting-down" {
			if i.ContainerID != "" {
				_ = s.env.Docker.Remove(i.ContainerID)
			}
			state = "terminated"
		}
		_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
			x.State = state
			if state == "terminated" {
				n := core.Now()
				x.TerminatedAt, x.StateReason = &n, "Server.Restart: interrupted by a HomeCloud restart"
			}
			return nil
		})
		if state == "terminated" {
			s.releaseVolumes(i)
			s.vpc.Release(i.ID)
		}
	}
}
