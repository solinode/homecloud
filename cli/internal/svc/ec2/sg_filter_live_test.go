package ec2_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/dockertest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// filterEnv is EC2 and VPC on real Docker with security group enforcement
// running, and a fresh VPC to launch into; it skips without Docker.
type filterEnv struct {
	t      *testing.T
	h      *awstest.Harness
	vpc    *vpc.Service
	vpcID  string
	subnet string
	defSG  string
	ec2    *ec2.Service
}

func newFilterEnv(t *testing.T) *filterEnv {
	t.Helper()
	h := awstest.New(t)
	h.Env.Docker = dockertest.Start(t)
	v := vpc.New(h.Env)
	e := ec2.New(h.Env, v)
	e.RegisterAWS()
	v.Routes(h.Router)
	e.Routes(h.Router)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { v.RunFirewall(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	// A /16 nobody else on this Docker host uses.
	var second int
	var vpcID string
	for try := 0; ; try++ {
		second = 130 + rand.Intn(120)
		out, err := h.AWSErr(t, "ec2", "create-vpc", "--cidr-block", fmt.Sprintf("10.%d.0.0/16", second))
		if err == nil {
			var r struct{ Vpc struct{ VpcId string } }
			if json.Unmarshal([]byte(out), &r) != nil || r.Vpc.VpcId == "" {
				t.Fatalf("create-vpc: %s", out)
			}
			vpcID = r.Vpc.VpcId
			break
		}
		if try > 20 || !strings.Contains(out, "overlaps") {
			t.Fatalf("create-vpc: %v %s", err, out)
		}
	}
	t.Cleanup(func() { _ = v.DeleteVPC(vpcID) })
	sn := h.AWSJSON(t, "ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", fmt.Sprintf("10.%d.1.0/24", second))["Subnet"].(map[string]any)["SubnetId"].(string)
	return &filterEnv{t: t, h: h, vpc: v, ec2: e, vpcID: vpcID, subnet: sn, defSG: v.DefaultSecurityGroup(vpcID)}
}

func (f *filterEnv) group(t *testing.T, name string) string {
	t.Helper()
	return f.h.AWSJSON(t, "ec2", "create-security-group", "--group-name", name, "--description", name, "--vpc-id", f.vpcID)["GroupId"].(string)
}

type node struct {
	id, ip string
}

// launch starts an nginx instance (listening on 80) in the test VPC.
func (f *filterEnv) launch(t *testing.T, sgs ...string) node {
	t.Helper()
	args := []string{"ec2", "run-instances", "--image-id", "ami-nginx", "--subnet-id", f.subnet}
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

func (f *filterEnv) container(n node) string {
	var i struct {
		State       string `json:"state"`
		ContainerID string `json:"container_id"`
	}
	if json.Unmarshal(f.h.Native(f.t, "GET", "/api/v1/ec2/instances/"+n.id, nil), &i) != nil || i.State != "running" {
		return ""
	}
	return i.ContainerID
}

// reach reports whether from can open a TCP connection to to:port right now.
func (f *filterEnv) reach(from node, to node, port int) bool {
	cid := f.container(from)
	if cid == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := f.h.Env.Docker.Exec(ctx, cid, []string{"wget", "-q", "-T", "2", "-t", "1", "-O", "/dev/null", fmt.Sprintf("http://%s:%d/", to.ip, port)}, nil)
	return err == nil && res.ExitCode == 0
}

// eventually waits for the reachability from to be want (rules are applied asynchronously).
func (f *filterEnv) eventually(t *testing.T, want bool, from, to node, what string) {
	t.Helper()
	end := time.Now().Add(90 * time.Second)
	for time.Now().Before(end) {
		if f.reach(from, to, 80) == want {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s: %s -> %s:80 reachable = %v, want %v", what, from.id, to.id, !want, want)
}

// steady checks reachability stays as expected for a while.
func (f *filterEnv) steady(t *testing.T, want bool, from, to node, what string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if got := f.reach(from, to, 80); got != want {
			t.Fatalf("%s: %s -> %s:80 reachable = %v, want %v", what, from.id, to.id, got, want)
		}
	}
}

func TestSecurityGroupsFilterTrafficInsideVPC(t *testing.T) {
	f := newFilterEnv(t)
	h := f.h
	sgA, sgB, sgC := f.group(t, "app"), f.group(t, "db"), f.group(t, "other")
	a, b, c := f.launch(t, sgA), f.launch(t, sgB), f.launch(t, sgC)

	// Nothing allowed yet: default deny inside the VPC.
	f.eventually(t, false, a, b, "no rules")
	f.steady(t, false, c, b, "no rules")

	// B allows tcp/80 from sg-A only.
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sgB, "--ip-permissions", fromGroup(sgA))
	f.eventually(t, true, a, b, "allow from sg-A (replies flow back)")
	f.steady(t, false, c, b, "C is not in sg-A")
	f.steady(t, false, b, a, "A has no ingress rules")
	// Only the allowed port.
	if f.reach(a, b, 8080) {
		t.Fatal("A reached B on a port no rule allows")
	}

	// A CIDR rule (C's address) opens B to C as well; revoking closes it.
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sgB, "--protocol", "tcp", "--port", "80", "--cidr", c.ip+"/32")
	f.eventually(t, true, c, b, "CIDR rule")
	h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", sgB, "--protocol", "tcp", "--port", "80", "--cidr", c.ip+"/32")
	f.eventually(t, false, c, b, "CIDR rule revoked")
	f.steady(t, true, a, b, "sg-A rule still there")

	// Moving C into sg-A (membership change) lets it in.
	h.AWS(t, "ec2", "modify-instance-attribute", "--instance-id", c.id, "--groups", sgA)
	f.eventually(t, true, c, b, "C joined sg-A")
	h.AWS(t, "ec2", "modify-instance-attribute", "--instance-id", c.id, "--groups", sgC)
	f.eventually(t, false, c, b, "C left sg-A")

	// A new member of sg-A is allowed without touching B's rules.
	d := f.launch(t, sgA)
	f.eventually(t, true, d, b, "new member of sg-A")

	// Rules survive a container restart (fresh network namespace).
	h.AWS(t, "ec2", "reboot-instances", "--instance-ids", b.id)
	waitFor(t, "B back", 2*time.Minute, func() bool { return f.container(b) != "" })
	f.eventually(t, true, a, b, "after restart, sg-A allowed")
	f.eventually(t, false, c, b, "after restart, C still denied")

	// Terminating a member removes its address from the group's sources.
	h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", sgB, "--ip-permissions", fromGroup(sgA))
	f.eventually(t, false, a, b, "sg-A rule revoked")
	f.eventually(t, false, d, b, "sg-A rule revoked (second member)")
}

func TestDefaultSecurityGroupAllowsItsMembers(t *testing.T) {
	f := newFilterEnv(t)
	e1, e2 := f.launch(t), f.launch(t) // no groups: the VPC's default group
	other := f.group(t, "other")
	g := f.launch(t, other)
	f.eventually(t, false, g, e1, "outsider to default group")
	f.eventually(t, true, e2, e1, "default group allows itself")
	f.steady(t, true, e1, e2, "default group allows itself")
	f.steady(t, false, g, e2, "outsider to default group")

	// The self rule is an ordinary rule: revoking it closes the group.
	f.h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", f.defSG, "--ip-permissions",
		fmt.Sprintf(`IpProtocol=-1,UserIdGroupPairs=[{GroupId=%s}]`, f.defSG))
	f.eventually(t, false, e2, e1, "self rule revoked")
}

func TestSecurityGroupEgressRulesAreEnforced(t *testing.T) {
	f := newFilterEnv(t)
	open := f.group(t, "open")
	f.h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", open, "--protocol", "tcp", "--port", "80", "--cidr", "0.0.0.0/0")
	egr := f.group(t, "egress")
	target, blocked, x := f.launch(t, open), f.launch(t, open), f.launch(t, egr)
	f.h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", open, "--protocol", "tcp", "--port", "80", "--cidr", f.cidr(t)) // in-VPC clients too

	// Default egress allows everything.
	f.eventually(t, true, x, target, "default egress")
	// Replace it with a rule for one destination address only.
	f.h.AWS(t, "ec2", "revoke-security-group-egress", "--group-id", egr, "--ip-permissions", "IpProtocol=-1,IpRanges=[{CidrIp=0.0.0.0/0}]")
	f.h.AWS(t, "ec2", "authorize-security-group-egress", "--group-id", egr, "--protocol", "tcp", "--port", "80", "--cidr", target.ip+"/32")
	f.eventually(t, false, x, blocked, "egress limited to one destination")
	f.steady(t, true, x, target, "egress allows its destination")
}

func (f *filterEnv) cidr(t *testing.T) string {
	t.Helper()
	out := f.h.AWSJSON(t, "ec2", "describe-vpcs", "--vpc-ids", f.vpcID)["Vpcs"].([]any)[0].(map[string]any)["CidrBlock"].(string)
	return strings.TrimSpace(out)
}

// fromGroup is an ip-permissions argument for tcp/80 from members of a group.
func fromGroup(sg string) string {
	return fmt.Sprintf("IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId=%s}]", sg)
}
