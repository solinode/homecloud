package ec2_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// liveHarness starts EC2 and VPC on real Docker, or skips.
func liveHarness(t *testing.T) *awstest.Harness {
	t.Helper()
	d, err := runtime.New()
	if err != nil || d.C.Ping() != nil {
		t.Skip("Docker not available")
	}
	h := awstest.New(t)
	h.Env.Docker = d
	v := vpc.New(h.Env)
	if err := v.EnsureDefault(t.Context()); err != nil {
		t.Skipf("default vpc: %v", err)
	}
	e := ec2.New(h.Env, v)
	e.RegisterAWS()
	v.Routes(h.Router)
	e.Routes(h.Router)
	return h
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func reachable(port int) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// TestSecurityGroupChangesApplyToRunningInstance authorizes and revokes a
// rule on a running web server and checks its port opens and closes without
// the user restarting it.
func TestSecurityGroupChangesApplyToRunningInstance(t *testing.T) {
	h := liveHarness(t)
	sg := h.AWSJSON(t, "ec2", "create-security-group", "--group-name", "web", "--description", "web")["GroupId"].(string)
	inst := h.AWSJSON(t, "ec2", "run-instances", "--image-id", "ami-nginx", "--security-group-ids", sg)["Instances"].([]any)[0].(map[string]any)["InstanceId"].(string)
	t.Cleanup(func() { _, _ = h.AWSErr(t, "ec2", "terminate-instances", "--instance-ids", inst) })

	ports := func() map[string]int {
		var i struct {
			State       string         `json:"state"`
			PublicPorts map[string]int `json:"public_ports"`
		}
		_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/ec2/instances/"+inst, nil), &i)
		if i.State != "running" {
			return nil
		}
		return i.PublicPorts
	}
	waitFor(t, "running", 2*time.Minute, func() bool { return ports() != nil })
	if p := ports(); len(p) != 0 {
		t.Fatalf("no rules yet, but ports published: %v", p)
	}

	// Open port 80 through the AWS API.
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "80", "--cidr", "0.0.0.0/0")
	waitFor(t, "port 80 published", 2*time.Minute, func() bool { return ports()["80/tcp"] > 0 })
	waitFor(t, "nginx reachable", time.Minute, func() bool { return reachable(ports()["80/tcp"]) })
	old := ports()["80/tcp"]

	// An unenforceable rule (other CIDR) changes nothing; the host port stays.
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "8080", "--cidr", "10.0.0.0/8")
	time.Sleep(2 * time.Second)
	if got := ports()["80/tcp"]; got != old {
		t.Fatalf("host port changed from %d to %d for an unenforceable rule", old, got)
	}

	// Revoke it: the port closes.
	h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", sg, "--protocol", "tcp", "--port", "80", "--cidr", "0.0.0.0/0")
	waitFor(t, "port 80 closed", 2*time.Minute, func() bool { return len(ports()) == 0 })
	if reachable(old) {
		t.Fatalf("port %d still reachable after revoke", old)
	}

	// Swapping the instance's groups applies too.
	sg2 := h.AWSJSON(t, "ec2", "create-security-group", "--group-name", "web2", "--description", "web2")["GroupId"].(string)
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sg2, "--protocol", "tcp", "--port", "80", "--cidr", "0.0.0.0/0")
	h.AWS(t, "ec2", "modify-instance-attribute", "--instance-id", inst, "--groups", sg2)
	waitFor(t, "port 80 via new group", 2*time.Minute, func() bool { return ports()["80/tcp"] > 0 })
	waitFor(t, "nginx reachable again", time.Minute, func() bool { return reachable(ports()["80/tcp"]) })

	// Moving back to the empty group via the network interface closes it.
	eni := "eni-" + strings.TrimPrefix(inst, "i-")
	h.AWS(t, "ec2", "modify-network-interface-attribute", "--network-interface-id", eni, "--groups", sg)
	waitFor(t, "port 80 closed via eni", 2*time.Minute, func() bool { return len(ports()) == 0 })
	h.AWS(t, "ec2", "modify-network-interface-attribute", "--network-interface-id", eni, "--groups", sg2)
	waitFor(t, "port 80 open via eni", 2*time.Minute, func() bool { return ports()["80/tcp"] > 0 })

	// And through the native API.
	rules := h.Native(t, "GET", "/api/v1/vpc/security-groups/"+sg2, nil)
	var g struct {
		Ingress []struct {
			ID string `json:"id"`
		} `json:"ingress"`
	}
	if err := json.Unmarshal(rules, &g); err != nil || len(g.Ingress) != 1 {
		t.Fatalf("group %s (%v)", rules, err)
	}
	h.Native(t, "DELETE", "/api/v1/vpc/security-groups/"+sg2+"/ingress/"+g.Ingress[0].ID, nil)
	waitFor(t, "port 80 closed natively", 2*time.Minute, func() bool { return len(ports()) == 0 })
	h.Native(t, "POST", "/api/v1/vpc/security-groups/"+sg2+"/ingress", map[string]any{"from_port": 80, "to_port": 80, "protocol": "tcp"})
	waitFor(t, "port 80 reopened natively", 2*time.Minute, func() bool { return ports()["80/tcp"] > 0 })
	if !strings.Contains(h.AWS(t, "ec2", "describe-instances", "--instance-ids", inst), "running") {
		t.Fatal("instance no longer running")
	}
}
