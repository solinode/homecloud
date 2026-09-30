package ec2_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"golang.org/x/crypto/ssh"
)

// The VM instance test boots a real Ubuntu cloud image under QEMU in a Docker
// container. It needs Docker, downloads the image once (about 600 MB, into the
// Docker volume hc-test-vm-images, reused by later runs) and is slow when the
// Docker host has no /dev/kvm (the guest is emulated). Run it with
//
//	HC_TEST_VM=1 go test -timeout 60m -run TestVMInstance ./internal/svc/ec2
//
// (DOCKER_HOST=unix://$HOME/.orbstack/run/docker.sock on macOS with OrbStack).

const vmTestBudget = 12 * time.Minute // per wait for a guest to boot or a rebuild; emulated boots take up to about 5 minutes

// The Ubuntu image is the default; HC_TEST_VM_IMAGE=debian boots Debian instead (faster to boot emulated).
var vmTestImage, vmTestUser = func() (string, string) {
	if os.Getenv("HC_TEST_VM_IMAGE") == "debian" {
		return "ami-debian-12-vm", "debian"
	}
	return "ami-ubuntu-24-04-vm", "ubuntu"
}()

type vmInstance struct {
	State          string         `json:"state"`
	ContainerID    string         `json:"container_id"`
	PrivateIP      string         `json:"private_ip"`
	Virtualization string         `json:"virtualization"`
	VMUser         string         `json:"vm_user"`
	VMNetwork      string         `json:"vm_network"`
	PublicPorts    map[string]int `json:"public_ports"`
	StateReason    string         `json:"state_reason"`
}

func (f *filterEnv) vmGet(id string) vmInstance {
	var i vmInstance
	_ = json.Unmarshal(f.h.Native(f.t, "GET", "/api/v1/ec2/instances/"+id, nil), &i)
	return i
}

func (f *filterEnv) vmWaitState(t *testing.T, id, state string) vmInstance {
	t.Helper()
	var i vmInstance
	waitFor(t, id+" "+state, vmTestBudget, func() bool {
		i = f.vmGet(id)
		if i.State == "terminated" && state != "terminated" {
			t.Fatalf("%s terminated: %s", id, i.StateReason)
		}
		return i.State == state
	})
	return i
}

// startIMDS runs the instance metadata service for this test: its own helper
// container (so a real installation's is left alone) and an HTTP endpoint the
// containers reach through host.docker.internal.
func (f *filterEnv) startIMDS(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(ec2.IMDSPath, f.ec2.IMDSHandler())
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	f.h.Env.ContainerAPI = fmt.Sprintf("http://host.docker.internal:%d", ln.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.ec2.RunIMDS(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = srv.Close()
		_ = f.h.Env.Docker.Remove(f.ec2.IMDSContainer)
	})
	// The helper attaches to the VPC, then instances can route to it.
	v, err := f.vpc.GetVPC(f.vpcID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "metadata service", 5*time.Minute, func() bool {
		c, err := f.h.Env.Docker.Inspect(f.ec2.IMDSContainer)
		if err != nil || !c.State.Running {
			return false
		}
		n, ok := c.NetworkSettings.Networks[v.Network]
		return ok && n.IPAddress != ""
	})
}

// sshDial connects to the guest through its published SSH port on the Docker host.
func sshDial(t *testing.T, port int, signer ssh.Signer, user string) *ssh.Client {
	t.Helper()
	var c *ssh.Client
	waitFor(t, "ssh on published port", vmTestBudget, func() bool {
		var err error
		c, err = ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &ssh.ClientConfig{
			User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 15 * time.Second,
		})
		return err == nil
	})
	t.Cleanup(func() { c.Close() })
	return c
}

func sshRun(t *testing.T, c *ssh.Client, cmd string) string {
	t.Helper()
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out, err := s.CombinedOutput(cmd)
	if err != nil {
		t.Fatalf("ssh %q: %v\n%s", cmd, err, out)
	}
	return strings.TrimSpace(string(out))
}

// banner reports whether from (a container instance) reads an SSH banner from ip:22.
func (f *filterEnv) banner(t *testing.T, from node, ip string) bool {
	cid := f.container(from)
	if cid == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := f.h.Env.Docker.Exec(ctx, cid, []string{"/bin/sh", "-c", "echo | nc -w 4 " + ip + " 22"}, nil)
	return err == nil && strings.Contains(res.Stdout, "SSH-2.0")
}

func (f *filterEnv) bannerEventually(t *testing.T, want bool, from node, ip, what string) {
	t.Helper()
	end := time.Now().Add(vmTestBudget)
	for time.Now().Before(end) {
		if f.banner(t, from, ip) == want {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s: SSH banner from %s to %s reachable = %v, want %v", what, from.id, ip, !want, want)
}

func TestVMInstanceLifecycle(t *testing.T) {
	if os.Getenv("HC_TEST_VM") != "1" {
		t.Skip("set HC_TEST_VM=1 to boot a real VM (slow; downloads a cloud image)")
	}
	f := newFilterEnv(t)
	h := f.h
	f.ec2.VMImageVolume = "hc-test-vm-images"
	// Our own metadata service container, named before anything can attach the
	// default one (a real installation's) to the test VPC.
	f.ec2.IMDSContainer = fmt.Sprintf("hc-test-imds-%d", time.Now().UnixNano()%1e9)
	// Published ports need a VPC with an internet gateway (otherwise its network is internal).
	f.vpc.NetworkChanged = f.ec2.NetworkChanged
	igw := h.AWSJSON(t, "ec2", "create-internet-gateway")["InternetGateway"].(map[string]any)["InternetGatewayId"].(string)
	h.AWS(t, "ec2", "attach-internet-gateway", "--internet-gateway-id", igw, "--vpc-id", f.vpcID)
	rtb := h.AWSJSON(t, "ec2", "describe-route-tables", "--filters", "Name=vpc-id,Values="+f.vpcID)["RouteTables"].([]any)[0].(map[string]any)["RouteTableId"].(string)
	h.AWS(t, "ec2", "create-route", "--route-table-id", rtb, "--destination-cidr-block", "0.0.0.0/0", "--gateway-id", igw)
	t.Cleanup(func() {
		_, _ = h.AWSErr(t, "ec2", "detach-internet-gateway", "--internet-gateway-id", igw, "--vpc-id", f.vpcID)
		_, _ = h.AWSErr(t, "ec2", "delete-internet-gateway", "--internet-gateway-id", igw)
	})
	f.startIMDS(t)

	// A key pair, and a security group that lets SSH in from anywhere (published on the host).
	key := h.AWSJSON(t, "ec2", "create-key-pair", "--key-name", "vmtest", "--key-type", "ed25519")
	signer, err := ssh.ParsePrivateKey([]byte(key["KeyMaterial"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	sg := f.group(t, "vm-ssh")
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "22", "--cidr", "0.0.0.0/0")

	// A container instance in the VPC to probe the guest from.
	helper := f.launchImage(t, "ami-alpine-3-20", f.group(t, "probe"))

	userData := "#!/bin/sh\necho hc-userdata-ran > /var/tmp/userdata.txt\n"
	run := h.AWSJSON(t, "ec2", "run-instances", "--image-id", vmTestImage, "--instance-type", "t3.micro", "--key-name", "vmtest",
		"--subnet-id", f.subnet, "--security-group-ids", sg, "--user-data", userData,
		"--block-device-mappings", `[{"DeviceName":"/dev/xvda","Ebs":{"VolumeSize":10,"DeleteOnTermination":true}}]`,
		"--tag-specifications", `ResourceType=instance,Tags=[{Key=Name,Value=vm-test}]`)
	inst := run["Instances"].([]any)[0].(map[string]any)
	id, ip := inst["InstanceId"].(string), inst["PrivateIpAddress"].(string)
	if inst["Hypervisor"] != "kvm" || inst["VirtualizationType"] != "hvm" {
		t.Errorf("run-instances reports hypervisor %v, virtualization %v", inst["Hypervisor"], inst["VirtualizationType"])
	}
	terminated := false
	t.Cleanup(func() {
		if !terminated {
			_, _ = h.AWSErr(t, "ec2", "terminate-instances", "--instance-ids", id)
		}
	})
	i := f.vmWaitState(t, id, "running")
	t.Logf("%s is running (%s, guest %s) after boot", id, i.Virtualization, ip)
	if i.Virtualization != "kvm" && i.Virtualization != "emulated" || i.VMUser != vmTestUser {
		t.Fatalf("instance reports virtualization %q, user %q", i.Virtualization, i.VMUser)
	}
	if os.Getenv("HC_VM_NET") != "user" && i.VMNetwork != "passt" {
		logs, _ := h.Env.Docker.Logs(i.ContainerID, 40, time.Time{})
		t.Fatalf("passt did not run (vm_network=%q): the guest has no private address of its own. Container log:\n%s", i.VMNetwork, logs)
	}
	if i.PublicPorts["22/tcp"] == 0 {
		ci, _ := h.Env.Docker.Inspect(i.ContainerID)
		if ci != nil {
			t.Logf("port bindings %v, network ports %v", ci.HostConfig.PortBindings, ci.NetworkSettings.Ports)
		}
		t.Fatalf("ssh not published: %v", i.PublicPorts)
	}
	// The root volume is an EBS volume attached at /dev/xvda with the requested size.
	desc := h.AWSJSON(t, "ec2", "describe-volumes", "--filters", "Name=attachment.instance-id,Values="+id)["Volumes"].([]any)
	if len(desc) != 1 || desc[0].(map[string]any)["Size"].(float64) != 10 {
		t.Fatalf("root volume: %v", desc)
	}
	volID := desc[0].(map[string]any)["VolumeId"].(string)

	// Log in through the published port and inspect the guest.
	c := sshDial(t, i.PublicPorts["22/tcp"], signer, vmTestUser)
	if got, want := sshRun(t, c, "hostname"), "ip-"+strings.ReplaceAll(ip, ".", "-"); got != want {
		t.Errorf("hostname %q, want %q", got, want)
	}
	// cloud-init may still be running user data when it finishes the boot marker; it is done by now.
	waitFor(t, "user data file", 2*time.Minute, func() bool {
		s, err := c.NewSession()
		if err != nil {
			return false
		}
		defer s.Close()
		out, err := s.CombinedOutput("cat /var/tmp/userdata.txt")
		return err == nil && strings.TrimSpace(string(out)) == "hc-userdata-ran"
	})
	if got := sshRun(t, c, "curl -s -m 10 http://169.254.169.254/latest/meta-data/instance-id"); got != id {
		t.Errorf("instance-id from the metadata service inside the guest = %q, want %q", got, id)
	}
	// (With HC_VM_NET=user, the fallback, the guest sits behind NAT at 10.0.2.15.)
	if got := sshRun(t, c, "ip -4 -o addr show scope global"); os.Getenv("HC_VM_NET") != "user" && !strings.Contains(got, ip+"/") {
		t.Errorf("guest addresses %q lack the instance's private address %s", got, ip)
	}
	if got := sshRun(t, c, "uname -m"); got != "aarch64" && got != "x86_64" {
		t.Errorf("uname -m = %q", got)
	}
	if got, _ := strconv.Atoi(sshRun(t, c, "df -BG --output=size / | tail -1 | tr -d ' G'")); got < 9 {
		t.Errorf("root file system is %dG, want the 10 GiB volume grown into", got)
	}
	if got := sshRun(t, c, "getent hosts example.com >/dev/null && echo dns-ok"); got != "dns-ok" {
		t.Errorf("DNS inside the guest: %q", got)
	}
	sshRun(t, c, "echo persisted > /home/"+vmTestUser+"/keep.txt && sync")

	// The serial console is the instance's console output.
	// (The AWS CLI decodes the base64 output itself.)
	if out := h.AWSJSON(t, "ec2", "get-console-output", "--instance-id", id)["Output"].(string); !strings.Contains(out, "Cloud-init v.") || !strings.Contains(out, "finished at") {
		t.Errorf("console output lacks cloud-init lines: %.300s", out)
	}
	f.vmAccessChecks(t, id) // run-command, guest metrics and the serial terminal
	if msg, err := h.AWSErr(t, "ec2", "create-image", "--instance-id", id, "--name", "vm-img"); err == nil || !strings.Contains(msg, "not supported yet") {
		t.Errorf("create-image of a VM: %v %s", err, msg)
	}
	c.Close()
	t.Logf("guest checks passed")

	// Stop and start keep the disk.
	h.AWS(t, "ec2", "stop-instances", "--instance-ids", id)
	f.vmWaitState(t, id, "stopped")
	if h.Env.Docker.State(i.ContainerID) == "running" {
		t.Error("container still running after stop")
	}
	h.AWS(t, "ec2", "start-instances", "--instance-ids", id)
	i = f.vmWaitState(t, id, "running")
	c = sshDial(t, i.PublicPorts["22/tcp"], signer, vmTestUser)
	if got := sshRun(t, c, "cat /home/"+vmTestUser+"/keep.txt && cat /var/tmp/userdata.txt"); got != "persisted\nhc-userdata-ran" {
		t.Errorf("after stop/start: %q", got)
	}
	if got := sshRun(t, c, "curl -s -m 10 http://169.254.169.254/latest/meta-data/instance-id"); got != id {
		t.Errorf("after start, instance-id from metadata = %q", got)
	}
	c.Close()
	t.Logf("stop/start kept the disk")

	// Security groups apply to the guest. With the world-open rule the probe reaches SSH.
	f.bannerEventually(t, true, helper, ip, "world-open rule")
	// Revoking it unpublishes the port; the container is rebuilt, so the guest reboots.
	h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "22", "--cidr", "0.0.0.0/0")
	waitFor(t, "port unpublished", vmTestBudget, func() bool { g := f.vmGet(id); return g.State == "running" && len(g.PublicPorts) == 0 })
	// A rule for the probe's address only lets it in, once the guest is back up.
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "22", "--cidr", helper.ip+"/32")
	f.bannerEventually(t, true, helper, ip, "rule for the probe")
	// Revoking that rule closes SSH for the running guest (nothing to unpublish, no reboot).
	h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "22", "--cidr", helper.ip+"/32")
	f.bannerEventually(t, false, helper, ip, "rule revoked")
	for n := 0; n < 3; n++ {
		if f.banner(t, helper, ip) {
			t.Fatal("SSH reachable without a rule")
		}
	}
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "22", "--cidr", helper.ip+"/32")
	f.bannerEventually(t, true, helper, ip, "rule for the probe again")
	// A loopback-only rule publishes the port again (another rebuild); the disk survived.
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "22", "--cidr", "127.0.0.1/32")
	waitFor(t, "loopback port published", vmTestBudget, func() bool { return f.vmGet(id).PublicPorts["22/tcp"] > 0 })
	c = sshDial(t, f.vmGet(id).PublicPorts["22/tcp"], signer, vmTestUser)
	if got := sshRun(t, c, "cat /home/"+vmTestUser+"/keep.txt"); got != "persisted" {
		t.Errorf("after the rebuild: %q", got)
	}
	c.Close()
	t.Logf("security group rules and the rebuild verified")

	// Terminate removes the container and the root volume.
	cid := f.vmGet(id).ContainerID
	h.AWS(t, "ec2", "terminate-instances", "--instance-ids", id)
	terminated = true
	f.vmWaitState(t, id, "terminated")
	if st := h.Env.Docker.State(cid); st != "missing" {
		t.Errorf("container is %s after terminate", st)
	}
	if _, err := h.Env.Docker.C.InspectVolume("hc-" + volID); err == nil {
		t.Errorf("root volume %s still exists after terminate", volID)
	} else if !errors.Is(err, docker.ErrNoSuchVolume) {
		t.Errorf("inspect volume: %v", err)
	}
	if out := h.AWS(t, "ec2", "describe-volumes", "--filters", "Name=volume-id,Values="+volID); strings.Contains(out, volID) {
		t.Errorf("volume record %s remains: %s", volID, out)
	}
}

// launchImage starts a container instance of a catalog image in the test VPC.
func (f *filterEnv) launchImage(t *testing.T, image string, sgs ...string) node {
	t.Helper()
	args := []string{"ec2", "run-instances", "--image-id", image, "--subnet-id", f.subnet}
	if len(sgs) > 0 {
		args = append(args, "--security-group-ids")
		args = append(args, sgs...)
	}
	i := f.h.AWSJSON(t, args...)["Instances"].([]any)[0].(map[string]any)
	n := node{id: i["InstanceId"].(string), ip: i["PrivateIpAddress"].(string)}
	t.Cleanup(func() { _, _ = f.h.AWSErr(t, "ec2", "terminate-instances", "--instance-ids", n.id) })
	waitFor(t, n.id+" running", 3*time.Minute, func() bool { return f.container(n) != "" })
	return n
}

// The VM AMIs are in the catalog and marked as virtual machines.
func TestVMImagesInCatalog(t *testing.T) {
	h := liveHarness(t)
	out := h.AWSJSON(t, "ec2", "describe-images", "--image-ids", vmTestImage, "ami-ubuntu-24-04")["Images"].([]any)
	if len(out) != 2 {
		t.Fatalf("images: %v", out)
	}
	for _, im := range out {
		m := im.(map[string]any)
		isVM := m["ImageId"] == vmTestImage
		want := "xen"
		if isVM {
			want = "kvm"
			if !strings.HasSuffix(m["Description"].(string), "with its own kernel and cloud-init") && !strings.Contains(m["Description"].(string), "virtual machine") {
				t.Errorf("VM image description %q does not say it is a virtual machine", m["Description"])
			}
			if !strings.Contains(m["Name"].(string), "/vm/") {
				t.Errorf("VM image name %q is not distinct from the container image's", m["Name"])
			}
		}
		if m["Hypervisor"] != want || m["VirtualizationType"] != "hvm" {
			t.Errorf("%v: hypervisor %v, virtualization %v", m["ImageId"], m["Hypervisor"], m["VirtualizationType"])
		}
	}
	var imgs []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Virtualization string `json:"virtualization"`
	}
	if err := json.Unmarshal(h.Native(t, "GET", "/api/v1/ec2/images", nil), &imgs); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, im := range imgs {
		seen[im.ID] = im.Virtualization
		if im.Virtualization == "vm" && !strings.HasSuffix(im.Name, "(VM)") {
			t.Errorf("VM image %s named %q", im.ID, im.Name)
		}
	}
	if seen[vmTestImage] != "vm" || seen["ami-debian-12-vm"] != "vm" || seen["ami-ubuntu-24-04"] != "container" || seen["ami-nginx"] != "container" {
		t.Errorf("virtualization by image: %v", seen)
	}
}
