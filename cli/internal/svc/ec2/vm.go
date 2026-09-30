package ec2

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2/vm"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// VM instances.
//
// An AMI with a VMBase boots an official cloud image under QEMU. The guest
// runs inside a Docker container (the "VM container", built from the runner
// image in the vm package) attached to the instance's VPC network with its
// private address, so addresses, DNS, the metadata service, security groups
// (iptables in the container's namespace) and published ports work as they do
// for container instances. Inside the container passt hands the guest the
// container's address, gateway and routes and forwards every inbound port to
// it. The root disk is a qcow2 overlay on the cached base image, kept in the
// instance's root volume (an EBS volume record at /dev/xvda, mounted at /vm).
// The guest uses hardware acceleration when the Docker host has /dev/kvm and
// is otherwise emulated.
//
// Stage 1 covers the instance lifecycle. Extra volumes, snapshots and images
// of VM disks and backup are not available for VM instances yet. The browser
// terminal (serial console), run-command and CloudWatch metrics are in
// vm_access.go.

const defaultVMImageVolume = "hc-vm-images"

type vmState struct {
	runnerMu     sync.Mutex
	runnerTag    string // built and ready
	runnerErr    error
	runnerFailed time.Time

	kvmOnce sync.Once
	kvm     bool

	fetchMu sync.Mutex // one download at a time
}

// vmPlan is what Launch works out once for every instance of a VM launch.
type vmPlan struct {
	virtualization string // kvm | emulated
	user           string
	diskGB         int
	root           VolumeSpec
	ami            string // the Docker volume of an image made from an instance
}

func errVMUnsupported(what string) error {
	return core.Errf(http.StatusBadRequest, "UnsupportedOperation", "%s VM instances is not supported yet", what)
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func (s *Service) vmImageVolume() string {
	if s.VMImageVolume != "" {
		return s.VMImageVolume
	}
	return defaultVMImageVolume
}

// planVM validates a launch of a VM image and decides how it runs.
func (s *Service) planVM(in RunInput, img Image) (vmPlan, error) {
	var p vmPlan
	if s.env.Docker == nil {
		return p, core.Errf(http.StatusServiceUnavailable, "Unsupported", "virtual machine instances need Docker")
	}
	arch, err := vm.ArchOf(hostArch(s))
	if err != nil {
		return p, core.Errf(http.StatusBadRequest, "Unsupported", "%v", err)
	}
	minGB := vm.MinRootGB
	if img.VMBase != "" {
		base, ok := vm.Bases[img.VMBase]
		if !ok {
			return p, core.Errf(http.StatusBadRequest, "InvalidAMIID.Unavailable", "the image %s has no cloud image %q", img.ID, img.VMBase)
		}
		if _, err := base.For(arch); err != nil {
			return p, core.Errf(http.StatusBadRequest, "InvalidAMIID.Unavailable", "the image %s is not available for %s hosts: %v", img.ID, arch, err)
		}
		p.user, p.diskGB = base.DefaultUser, 8
	} else { // an image made from an instance
		if img.VMArch != string(arch) {
			return p, core.Errf(http.StatusBadRequest, "InvalidAMIID.Unavailable", "the image %s was made on a %s host, this one is %s", img.ID, img.VMArch, arch)
		}
		p.user, p.diskGB, minGB = img.VMUser, max(8, img.VMDiskGB), img.VMDiskGB
	}
	if len(in.FileSystems) > 0 {
		return p, errVMUnsupported("mounting file systems in")
	}
	if err := s.vmRunnerFailure(); err != nil {
		return p, core.Errf(http.StatusServiceUnavailable, "Unsupported", "virtual machine instances are unavailable: %v", err)
	}
	p.root = VolumeSpec{Device: "/dev/xvda", MountPath: vm.DiskDir}
	del := true
	p.root.DeleteOnTermination = &del
	if r := in.RootVolume; r != nil {
		if r.SizeGB > 0 {
			p.diskGB = r.SizeGB
		}
		if r.DeleteOnTermination != nil {
			del := *r.DeleteOnTermination
			p.root.DeleteOnTermination = &del
		}
		p.root.VolumeType, p.root.Iops, p.root.Throughput, p.root.Encrypted, p.root.KMSKeyID = r.VolumeType, r.Iops, r.Throughput, r.Encrypted, r.KMSKeyID
	}
	if p.diskGB < minGB || p.diskGB > 16384 {
		return p, core.Errf(http.StatusBadRequest, "InvalidParameterValue", "the root volume of %s must be between %d and 16384 GiB", img.ID, minGB)
	}
	p.ami = img.VMDisk
	p.root.SizeGB = p.diskGB
	p.virtualization = "emulated"
	if s.vmKVM() {
		p.virtualization = "kvm"
	}
	return p, nil
}

// vmKVM reports whether the Docker host offers /dev/kvm. It is probed once by
// creating a container with the device (HC_VM_ACCEL=kvm|tcg overrides).
func (s *Service) vmKVM() bool {
	s.vm.kvmOnce.Do(func() {
		switch os.Getenv("HC_VM_ACCEL") {
		case "kvm":
			s.vm.kvm = true
			return
		case "tcg":
			log.Printf("ec2: HC_VM_ACCEL=tcg: VM instances run emulated (TCG), which is slow")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, err := s.env.Docker.RunOnce(ctx, runtime.RunSpec{
			Image: helperImage, Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{"test -r /dev/kvm && test -w /dev/kvm"},
			Devices: []string{"/dev/kvm"}, Network: "none",
		})
		if err != nil {
			log.Printf("ec2: /dev/kvm is not usable on the Docker host (%v): VM instances run emulated (TCG), which is slow", err)
			return
		}
		s.vm.kvm = true
	})
	return s.vm.kvm
}

// vmRunnerFailure is the error of a runner image build that failed recently.
func (s *Service) vmRunnerFailure() error {
	s.vm.runnerMu.Lock()
	defer s.vm.runnerMu.Unlock()
	if s.vm.runnerErr != nil && time.Since(s.vm.runnerFailed) < time.Minute {
		return s.vm.runnerErr
	}
	return nil
}

// vmRunner builds the runner image on first use (locally, tagged by content
// hash) and returns its tag. Only VM launches wait for the build; nothing else
// does. A failed build is remembered for a minute so launches fail fast
// with the reason.
func (s *Service) vmRunner(ctx context.Context) (string, error) {
	s.vm.runnerMu.Lock()
	defer s.vm.runnerMu.Unlock()
	tag := vm.RunnerTag()
	if s.vm.runnerTag == tag {
		return tag, nil
	}
	if s.vm.runnerErr != nil && time.Since(s.vm.runnerFailed) < time.Minute {
		return "", s.vm.runnerErr
	}
	bctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	log.Printf("ec2: building the VM runner image %s (first VM launch)", tag)
	if err := s.env.Docker.BuildImageFiles(bctx, tag, vm.RunnerContext()); err != nil {
		s.vm.runnerErr = fmt.Errorf("building the VM runner image failed: %w", err)
		s.vm.runnerFailed = time.Now()
		return "", s.vm.runnerErr
	}
	s.vm.runnerTag, s.vm.runnerErr = tag, nil
	return tag, nil
}

// vmEnsureBase downloads the instance's cloud image into the image cache
// volume unless it is there, verified by checksum.
func (s *Service) vmEnsureBase(ctx context.Context, runner string, base vm.Base, arch vm.Arch) error {
	f, err := base.For(arch)
	if err != nil {
		return err
	}
	s.vm.fetchMu.Lock()
	defer s.vm.fetchMu.Unlock()
	vol := s.vmImageVolume()
	if err := s.env.Docker.CreateVolume(vol, runtime.Labels("ec2", "vm-images", nil)); err != nil {
		return err
	}
	fctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	if _, err := s.env.Docker.RunOnce(fctx, runtime.RunSpec{
		Image: runner, Entrypoint: []string{"/usr/local/bin/hc-vm-fetch"}, Cmd: []string{f.URL, base.CacheName(arch), f.Sum},
		Mounts: []runtime.Mount{{Volume: vol, Target: vm.ImagesDir}},
	}); err != nil {
		return fmt.Errorf("download %s: %w", f.URL, err)
	}
	return nil
}

// vmMachine is the guest hardware of an instance.
func (s *Service) vmMachine(inst Instance) vm.Machine {
	arch, _ := vm.ArchOf(hostArch(s))
	m := vm.Machine{Name: inst.ID, Arch: arch, KVM: inst.Virtualization == "kvm",
		VCPUs: max(1, int(math.Ceil(inst.VCPUs))), MemoryMB: inst.MemoryMB}
	for _, d := range vmDataDisks(inst) {
		m.Disks = append(m.Disks, vm.Disk{ID: d.VolumeID})
	}
	return m
}

// vmRunSpec is the VM container of an instance.
func (s *Service) vmRunSpec(inst Instance, network string) runtime.RunSpec {
	mach := s.vmMachine(inst)
	base := vm.Bases[inst.VMBase]
	netEnv := os.Getenv("HC_VM_NET")
	if inst.VMNetwork == "user" { // passt failed for this instance before: stay on user-mode networking
		netEnv = "user"
	}
	dns := ""
	if v, err := s.vpc.GetVPC(inst.VpcID); err == nil {
		dns = vpc.DNSAddress(v.CIDR)
	}
	mounts := []runtime.Mount{{Volume: s.vmImageVolume(), Target: vm.ImagesDir, ReadOnly: true}}
	var disks []string
	for _, v := range inst.Volumes {
		if v.MountPath == vm.DiskDir { // the root volume
			mounts = append(mounts, runtime.Mount{Volume: volumeName(v.VolumeID), Target: v.MountPath})
			continue
		}
		mounts = append(mounts, runtime.Mount{Volume: volumeName(v.VolumeID), Target: vm.DisksDir + "/" + v.VolumeID})
	}
	for _, d := range vmDataDisks(inst) {
		size := 1
		if vol, err := store.Get[Volume](s.env.Store, cVolumes, d.VolumeID); err == nil {
			size = vol.SizeGB
		}
		disks = append(disks, fmt.Sprintf("%s:%d", d.VolumeID, size))
	}
	if inst.VMAMI != "" {
		mounts = append(mounts, runtime.Mount{Volume: inst.VMAMI, Target: "/ami", ReadOnly: true})
	}
	spec := runtime.RunSpec{
		DNS:    s.dns(inst.VpcID),
		Name:   svc.ContainerName("ec2", inst.ID),
		Image:  vm.RunnerTag(),
		Cmd:    mach.Command(),
		Labels: runtime.Labels("ec2", inst.ID, map[string]string{"homecloud.name": inst.Name, "homecloud.virtualization": inst.Virtualization}),
		// QEMU's own threads (I/O, emulation, the main loop), passt and the helpers
		// share the container's quota with the guest's vCPUs: a quota the vCPUs
		// alone exhaust starves passt, and the guest's DHCP and connections time out.
		NanoCPUs: int64(min(float64(mach.VCPUs)+1.5, s.hostCPU) * 1e9),
		MemoryMB: inst.MemoryMB + 768,
		Ports:    s.portsFor(inst),
		Mounts:   mounts,
		Network:  network,
		IP:       inst.PrivateIP,
		Aliases:  append([]string{inst.ID, inst.PrivateDNS}, nonEmpty(inst.Name)...),
		Hostname: strings.TrimSuffix(inst.PrivateDNS, ".internal"),
		Env: map[string]string{
			"HC_VM_BASE": base.CacheName(mach.Arch), "HC_VM_DISK_GB": strconv.Itoa(inst.VMDiskGB), "HC_VM_ARCH": string(mach.Arch), "HC_VM_DNS": dns,
			"HC_VM_DISKS": strings.Join(disks, " "),
			// Used only when passt is unavailable (or HC_VM_NET=user is set for the server).
			"HC_VM_NET": netEnv, "HC_VM_HOSTFWD": hostFwd(s.vpc.IngressPorts(inst.SecurityGroups, 64)),
		},
		ExtraHosts: []string{runtime.HostAlias},
		// passt sandboxes itself (user and mount namespaces, pivot_root), which
		// Docker's default seccomp profile and, on Ubuntu, its docker-default
		// AppArmor profile forbid; the container gets no extra capability. It
		// also opens a socket for each forwarded port, so it needs more than the
		// 65536 open files some hosts allow by default.
		SecurityOpt: vmSecurityOpts(),
		NoFile:      1048576,
	}
	if mach.KVM {
		spec.Devices = []string{"/dev/kvm"}
	}
	if inst.VMAMI != "" {
		spec.Env["HC_VM_AMI"] = "1"
	}
	return spec
}

// vmFwdMatches reports whether a VM container forwards the ports its groups
// allow. That only matters with user-mode networking (HC_VM_NET=user), where
// QEMU forwards a fixed list of ports given at creation; with passt every
// port reaches the guest and nothing needs rebuilding.
func (s *Service) vmFwdMatches(inst Instance) bool {
	if !inst.IsVM() || (os.Getenv("HC_VM_NET") != "user" && inst.VMNetwork != "user") {
		return true
	}
	c, err := s.env.Docker.Inspect(inst.ContainerID)
	if err != nil || c.Config == nil {
		return true
	}
	for _, e := range c.Config.Env {
		if v, ok := strings.CutPrefix(e, "HC_VM_HOSTFWD="); ok {
			return v == hostFwd(s.vpc.IngressPorts(inst.SecurityGroups, 64))
		}
	}
	return true
}

var (
	secOptsOnce sync.Once
	secOpts     []string
)

// vmSecurityOpts confines the VM container with Docker's default seccomp
// profile plus the four syscalls passt's sandbox needs (vm.SeccompProfile).
// AppArmor cannot be narrowed from here: Ubuntu's docker-default profile denies
// the mounts passt's sandbox makes, and a profile would have to be loaded on
// the host. So the container runs without AppArmor confinement, unless
// HC_VM_APPARMOR names a profile the administrator loaded on the host (a
// profile that allows mount, umount and pivot_root inside user namespaces).
func vmSecurityOpts() []string {
	secOptsOnce.Do(func() {
		prof, err := vm.SeccompProfile()
		if err != nil {
			log.Printf("ec2: building the VM seccomp profile: %v (using seccomp=unconfined)", err)
			prof = "unconfined"
		}
		aa := os.Getenv("HC_VM_APPARMOR")
		if aa == "" {
			aa = "unconfined"
		}
		secOpts = []string{"seccomp=" + prof, "apparmor=" + aa}
	})
	return secOpts
}

// hostFwd renders ports as QEMU user-mode networking forwards.
func hostFwd(ports []runtime.Port) string {
	var b strings.Builder
	for _, p := range ports {
		fmt.Fprintf(&b, ",hostfwd=%s::%d-:%d", p.Protocol, p.ContainerPort, p.ContainerPort)
	}
	return b.String()
}

// vmSeed is the cloud-init seed of an instance, as files to copy into its container.
func (s *Service) vmSeed(inst Instance) map[string][]byte {
	seed := vm.Seed{InstanceID: inst.ID, Hostname: strings.TrimSuffix(inst.PrivateDNS, ".internal"), UserData: inst.UserData}
	if inst.KeyName != "" {
		if k, err := s.keyPair(inst.KeyName); err == nil {
			seed.PublicKey = k.PublicKey
		}
	}
	out := map[string][]byte{}
	for name, b := range seed.Files() {
		out[strings.TrimPrefix(vm.SeedDir, "/")+"/"+name] = b
	}
	return out
}

func (s *Service) vmPending(id string) bool {
	i, err := store.Get[Instance](s.env.Store, cInstances, id)
	return err == nil && i.State == "pending"
}

// launchVM prepares and boots a VM instance in the background.
func (s *Service) launchVM(inst Instance, network string) {
	defer core.Recover("ec2 launch vm " + inst.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	fail := func(err error) { s.failLaunch(inst, err) }
	runner, err := s.vmRunner(ctx)
	if err != nil {
		fail(err)
		return
	}
	arch, _ := vm.ArchOf(hostArch(s))
	if inst.VMBase != "" {
		if err := s.vmEnsureBase(ctx, runner, vm.Bases[inst.VMBase], arch); err != nil {
			fail(err)
			return
		}
	}
	if !s.vmPending(inst.ID) {
		return // terminated while the image downloaded
	}
	if inst.Virtualization == "emulated" {
		log.Printf("ec2: %s runs emulated (no /dev/kvm): booting takes a while", inst.ID)
	}
	cid, err := s.env.Docker.Run(ctx, s.vmRunSpec(inst, network))
	if err != nil {
		fail(err)
		return
	}
	if err := s.env.Docker.CopyIn(ctx, cid, "/", s.vmSeed(inst), 0o644); err != nil {
		_ = s.env.Docker.Remove(cid)
		fail(fmt.Errorf("write cloud-init seed: %w", err))
		return
	}
	since := time.Now().Add(-2 * time.Second)
	if err := s.env.Docker.Start(cid); err != nil {
		_ = s.env.Docker.Remove(cid)
		fail(err)
		return
	}
	gone := false
	_, _ = store.Update(s.env.Store, cInstances, inst.ID, func(x *Instance) error {
		if x.State != "pending" {
			gone = true
			return nil
		}
		x.ContainerID = cid
		return nil
	})
	if gone { // terminated while launching
		go s.env.Docker.Remove(cid)
		return
	}
	inst.ContainerID = cid
	s.metadataRoute(ctx, cid, inst.VpcID)
	s.vpc.ProtectNow(ctx, s.member(inst, cid))
	err = s.vmWaitReady(inst, since)
	if errors.Is(err, errPasstExited) {
		// passt died while the guest booted: boot it again in user-mode networking.
		log.Printf("ec2: %s: %v; restarting the guest with user-mode networking", inst.ID, err)
		if err = s.vmFallbackToUser(inst.ID); err == nil {
			if cur, gerr := store.Get[Instance](s.env.Store, cInstances, inst.ID); gerr == nil {
				inst, cid = cur, cur.ContainerID
				err = s.vmWaitReady(inst, time.Now().Add(-2*time.Second))
			}
		}
	}
	if err != nil {
		if out, _ := s.env.Docker.Logs(cid, 30, time.Time{}); out != "" {
			err = fmt.Errorf("%w: %s", err, tail(out, 1500))
		}
		_ = s.env.Docker.Remove(cid)
		fail(err)
		return
	}
	netMode := "passt"
	if os.Getenv("HC_VM_NET") == "user" || inst.VMNetwork == "user" {
		netMode = "user"
	} else if why := s.vmPasstFailure(cid); why != "" {
		netMode = "user"
		log.Printf("ec2: %s: passt could not start, so the guest uses user-mode networking behind NAT (no private address of its own; only ports allowed at launch are forwarded): %s", inst.ID, why)
	}
	_, _ = store.Update(s.env.Store, cInstances, inst.ID, func(x *Instance) error {
		if x.State != "pending" { // terminated meanwhile
			return nil
		}
		x.VMNetwork = netMode
		x.State, x.StateReason = "running", ""
		x.PublicPorts = s.env.Docker.PublishedPorts(cid)
		return nil
	})
	s.syncPortsAsync(inst.ID) // groups may have changed while launching
}

// errPasstExited is what vmWaitReady returns when passt died under a running guest.
var errPasstExited = errors.New("passt exited")

// vmFallbackToUser records that passt does not work for an instance and
// rebuilds its container with QEMU's user-mode networking (the guest boots
// again; it sits behind NAT, and security group changes rebuild it).
func (s *Service) vmFallbackToUser(id string) error {
	if _, err := store.Update(s.env.Store, cInstances, id, func(x *Instance) error { x.VMNetwork = "user"; return nil }); err != nil {
		return err
	}
	return s.recreate(id)
}

var (
	passtWatchMu   sync.Mutex
	passtWatchLast = map[string]time.Time{}
)

// vmWatchPasst looks (at most every 20 seconds per instance) for a passt that
// died under a running guest, which the VM container's supervisor marks with
// /run/passt.dead, and restarts the guest in user-mode networking.
func (s *Service) vmWatchPasst(i Instance) {
	if !i.IsVM() || i.State != "running" || i.VMNetwork == "user" || i.ContainerID == "" || isBusy(i.ID) {
		return
	}
	passtWatchMu.Lock()
	if time.Since(passtWatchLast[i.ID]) < 20*time.Second {
		passtWatchMu.Unlock()
		return
	}
	passtWatchLast[i.ID] = time.Now()
	passtWatchMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := s.env.Docker.Exec(ctx, i.ContainerID, []string{"test", "-f", "/run/passt.dead"}, nil)
	if err != nil || res.ExitCode != 0 {
		return
	}
	log.Printf("ec2: %s: passt exited under the running guest; restarting it with user-mode networking (the guest reboots, behind NAT)", i.ID)
	go func() {
		defer core.Recover("ec2 passt fallback " + i.ID)
		if err := s.vmFallbackToUser(i.ID); err != nil {
			log.Printf("ec2: %s: restart in user-mode networking: %v", i.ID, err)
		}
	}()
}

// vmPasstFailure returns why passt did not start in the VM container (what the
// entrypoint printed before falling back to user-mode networking), or "".
func (s *Service) vmPasstFailure(cid string) string {
	out, err := s.env.Docker.Logs(cid, 0, time.Time{})
	if err != nil {
		return ""
	}
	const marker = "[homecloud] passt did not start"
	i := strings.Index(out, marker)
	if i < 0 {
		return ""
	}
	out = out[i:]
	if j := strings.Index(out, "[homecloud] starting"); j > 0 {
		out = out[:j]
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			// drop the docker log timestamp
			if _, rest, ok := strings.Cut(l, " "); ok && strings.HasSuffix(l[:strings.IndexByte(l, ' ')], "Z") {
				l = rest
			}
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "; ")
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

// vmBootTimeout is how long a guest gets to finish booting before the instance
// is reported running anyway (images without cloud-init never print the marker).
func vmBootTimeout(inst Instance) time.Duration {
	if inst.Virtualization == "kvm" {
		return 10 * time.Minute
	}
	return 30 * time.Minute
}

// vmWaitReady returns once the guest has booted (cloud-init finished, or a
// login prompt has been up for a while), the wait timed out (logged; the guest
// may simply not print the markers) or the VM container stopped, which is an
// error. It gives up quietly when the instance is terminated or stopped.
func (s *Service) vmWaitReady(inst Instance, since time.Time) error {
	deadline := time.Now().Add(vmBootTimeout(inst))
	var promptSince time.Time
	for {
		st := s.env.Docker.State(inst.ContainerID)
		if st != "running" {
			return fmt.Errorf("the virtual machine stopped while booting (container %s)", st)
		}
		if cur, err := store.Get[Instance](s.env.Store, cInstances, inst.ID); err != nil || cur.State == "terminated" || cur.State == "shutting-down" || cur.State == "stopping" || cur.State == "stopped" {
			return nil
		}
		out, _ := s.env.Docker.Logs(inst.ContainerID, 0, since)
		if strings.Contains(out, "[homecloud] passt exited unexpectedly") {
			return fmt.Errorf("%w, so the guest has no network: %s", errPasstExited, tail(out[strings.LastIndex(out, "[homecloud] passt exited unexpectedly"):], 600))
		}
		done, prompt := vm.Ready(out)
		if done {
			return nil
		}
		if prompt {
			if promptSince.IsZero() {
				promptSince = time.Now()
			} else if time.Since(promptSince) > 90*time.Second {
				return nil // a login prompt and no cloud-init final message
			}
		}
		if time.Now().After(deadline) {
			log.Printf("ec2: %s: no boot-complete marker on the console after %s; reporting it running", inst.ID, vmBootTimeout(inst))
			return nil
		}
		time.Sleep(3 * time.Second)
	}
}

// vmShutdownTimeout is how long the guest gets to power off after the ACPI request.
func vmShutdownTimeout(inst Instance) time.Duration {
	if inst.Virtualization == "kvm" {
		return 90 * time.Second
	}
	return 3 * time.Minute
}

// vmPowerOff asks the guest to shut down (ACPI power button through QMP) and
// waits for QEMU, and with it the container, to exit. It reports whether that
// happened in time.
func (s *Service) vmPowerOff(inst Instance) bool {
	if s.env.Docker.State(inst.ContainerID) != "running" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	if _, err := s.env.Docker.Exec(ctx, inst.ContainerID, []string{"/usr/local/bin/hc-vm-ctl", "system_powerdown"}, nil); err != nil {
		log.Printf("ec2: power down %s: %v", inst.ID, err)
	}
	cancel()
	deadline := time.Now().Add(vmShutdownTimeout(inst))
	for time.Now().Before(deadline) {
		if s.env.Docker.State(inst.ContainerID) != "running" {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

// vmStop shuts the guest down gracefully, then kills the container if it is
// still there; force skips the graceful part.
func (s *Service) vmStop(inst Instance, force bool) error {
	if !force && !s.vmPowerOff(inst) {
		out, _ := s.env.Docker.Logs(inst.ContainerID, 12, time.Time{})
		log.Printf("ec2: %s did not power off in %s; killing it. Console: %s", inst.ID, vmShutdownTimeout(inst), tail(out, 1500))
	}
	return s.env.Docker.Stop(inst.ContainerID, 0)
}

// vmStart boots a stopped VM (same disk) and waits for the guest to be up.
func (s *Service) vmStart(inst Instance) error {
	if base, ok := vm.Bases[inst.VMBase]; ok {
		// The root disk is an overlay on the cached cloud image: fetch it again if the cache was cleared or the host changed.
		fctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
		if arch, err := vm.ArchOf(hostArch(s)); err == nil {
			if runner, err := s.vmRunner(fctx); err != nil {
				log.Printf("ec2: start %s: %v", inst.ID, err)
			} else if err := s.vmEnsureBase(fctx, runner, base, arch); err != nil {
				log.Printf("ec2: start %s: %v", inst.ID, err)
			}
		}
		cancel()
	}
	since := time.Now().Add(-2 * time.Second)
	if err := s.env.Docker.Start(inst.ContainerID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.metadataRoute(ctx, inst.ContainerID, inst.VpcID)
	s.vpc.ProtectNow(ctx, s.member(inst, inst.ContainerID))
	return s.vmWaitReady(inst, since)
}

// vmReboot restarts the guest: a graceful shutdown, then the same container starts again.
func (s *Service) vmReboot(inst Instance) error {
	setBusy(inst.ID, true) // the container is down for a while: not "stopped"
	defer setBusy(inst.ID, false)
	if err := s.vmStop(inst, false); err != nil {
		return err
	}
	return s.vmStart(inst)
}

// recreateVM rebuilds the VM container of an instance from its current
// settings (instance type, ports).
func (s *Service) recreateVM(id string) error { return s.recreate(id) }

// rebuildVM replaces an instance's VM container with one built from the
// instance's current settings. Nothing is snapshotted: the disk lives in the
// root volume. A running guest is shut down and boots again, as after a reboot.
// The caller holds recreateMu and has marked the instance busy.
func (s *Service) rebuildVM(i Instance) error {
	v, err := s.vpc.GetVPC(i.VpcID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	if _, err := s.vmRunner(ctx); err != nil { // a new HomeCloud version may have a new runner image
		return err
	}
	running := s.env.Docker.State(i.ContainerID) == "running"
	if running {
		if err := s.vmStop(i, false); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	name := svc.ContainerName("ec2", i.ID)
	if err := s.env.Docker.C.RenameContainer(dockerRename(i.ContainerID, name+"-old", ctx)); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	undo := func(cause error) error {
		_ = s.env.Docker.C.RenameContainer(dockerRename(i.ContainerID, name, ctx))
		if running {
			_ = s.env.Docker.Start(i.ContainerID)
		}
		return cause
	}
	cid, err := s.env.Docker.Run(ctx, s.vmRunSpec(i, v.Network))
	if err != nil {
		return undo(fmt.Errorf("recreate: %w", err))
	}
	if err := s.env.Docker.CopyIn(ctx, cid, "/", s.vmSeed(i), 0o644); err != nil {
		_ = s.env.Docker.Remove(cid)
		return undo(fmt.Errorf("write cloud-init seed: %w", err))
	}
	_ = s.env.Docker.Remove(i.ContainerID)
	if running {
		if err := s.env.Docker.Start(cid); err != nil {
			log.Printf("ec2: start recreated %s: %v", i.ID, err)
		} else {
			s.metadataRoute(ctx, cid, i.VpcID)
			s.vpc.ProtectNow(ctx, s.member(i, cid))
		}
	}
	_, err = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		x.ContainerID = cid
		if running {
			x.PublicPorts = s.env.Docker.PublishedPorts(cid)
		}
		return nil
	})
	return err
}

func dockerRename(id, name string, ctx context.Context) docker.RenameContainerOptions {
	return docker.RenameContainerOptions{ID: id, Name: name, Context: ctx}
}
