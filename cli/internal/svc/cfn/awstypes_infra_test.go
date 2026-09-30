package cfn_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/svc/acm"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cfn"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ecs"
	"github.com/homecloudhq/homecloud/cli/internal/svc/elb"
	"github.com/homecloudhq/homecloud/cli/internal/svc/rds"
	"github.com/homecloudhq/homecloud/cli/internal/svc/route53"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// These tests drive CloudFormation's infrastructure types (load balancing,
// ECS, RDS, Route 53, ACM) with the real AWS CLI. Route 53, ACM and task
// definitions need no containers; everything else runs real nginx, Postgres
// and task containers and is skipped without Docker.

type infraEnv struct {
	*env
	VPC     *vpc.Service
	VpcID   string
	Subnets []string
}

// newInfraEnv starts an AWS endpoint with CloudFormation and the infrastructure
// services. With docker it also starts the container-backed services on a
// default VPC (the test is skipped when Docker is unavailable).
func newInfraEnv(t *testing.T, docker bool) *infraEnv {
	t.Helper()
	h := awstest.New(t)
	ie := &infraEnv{}
	if docker {
		d, err := runtime.New()
		if err != nil {
			t.Skip("docker not available: ", err)
		}
		h.Env.Docker = d
		runtime.Account = "cfninfra" + strings.ToLower(core.RandHex(4))
	}
	v := vpc.New(h.Env)
	ie.VPC = v
	if docker {
		if err := v.EnsureDefault(context.Background()); err != nil {
			t.Skip("cannot create the default VPC: ", err)
		}
		acct := runtime.Account
		t.Cleanup(func() {
			for _, kind := range [][]string{{"ps", "-aq"}, {"volume", "ls", "-q"}} {
				out, _ := exec.Command("docker", append(kind, "--filter", "label="+core.LabelAccount+"="+acct)...).Output()
				if ids := strings.Fields(string(out)); len(ids) > 0 {
					rm := "rm"
					if kind[0] == "volume" {
						rm = "volume"
					}
					args := []string{rm, "-f"}
					if kind[0] == "volume" {
						args = []string{"volume", "rm", "-f"}
					}
					_ = exec.Command("docker", append(args, ids...)...).Run()
				}
			}
			for _, x := range v.List() {
				_ = v.DeleteVPC(x.ID)
			}
		})
		ie.VpcID = v.DefaultVPCID()
		for _, s := range v.Subnets() {
			ie.Subnets = append(ie.Subnets, s.ID)
		}
		if len(ie.Subnets) < 2 {
			t.Fatalf("subnets: %v", ie.Subnets)
		}
	}
	dns := route53.New(h.Env, v)
	dns.Routes(h.Router)
	dns.RegisterAWS()
	certs := acm.New(h.Env, h.Secrets)
	certs.Routes(h.Router)
	certs.RegisterAWS()
	var lb *elb.Service
	if docker {
		lb = elb.New(h.Env, v)
		lb.Certs = certs
		certs.InUse, certs.OnRenew, certs.UsedBy = lb.UsesCertificate, lb.CertificateRenewed, lb.CertificateUsers
		lb.Routes(h.Router)
		lb.RegisterAWS()
		db := rds.New(h.Env, v, h.Secrets)
		db.Routes(h.Router)
		db.RegisterAWS()
	}
	tasks := ecs.New(h.Env, v, lb, h.Secrets)
	tasks.Routes(h.Router)
	tasks.RegisterAWS()
	if docker {
		// The server runs this loop; it rolls deployments forward and retires old tasks.
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go tasks.Run(ctx)
		lb.Resolve = func(id string) (string, string, bool) { return tasks.PrivateIP(id) }
		dns.Resolve = func(id string) (string, bool) { return lb.PrivateIP(id) }
	}
	c := cfn.New(h.Env)
	c.Handler = h.Mux
	c.Refresh = h.IAM.Refresh
	c.RolePrincipal = func(arn string) (*httpx.Principal, error) {
		return h.IAM.ServiceRolePrincipal(arn, "cloudformation.amazonaws.com", "HomeCloudCloudFormation")
	}
	c.Routes(h.Router)
	c.RegisterAWS()
	ie.env = &env{Harness: h, CFN: c}
	return ie
}

func (e *infraEnv) deploy(t *testing.T, verb, name, tmpl string, params ...string) {
	t.Helper()
	args := []string{"cloudformation", verb + "-stack", "--stack-name", name, "--template-body", "file://" + write(t, name+".yaml", tmpl)}
	if len(params) > 0 {
		args = append(args, "--parameters")
		for _, p := range params {
			k, v, _ := strings.Cut(p, "=")
			args = append(args, "ParameterKey="+k+",ParameterValue="+v)
		}
	}
	e.AWS(t, args...)
}

// wait polls a stack for one of the statuses, with time for containers.
func (e *infraEnv) wait(t *testing.T, name string, want ...string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	last := ""
	for time.Now().Before(deadline) {
		out, err := e.AWSErr(t, "cloudformation", "describe-stacks", "--stack-name", name)
		if err == nil {
			st := e.stack(t, name)
			last = str(st, "StackStatus")
			for _, w := range want {
				if last == w {
					return st
				}
			}
			if strings.HasSuffix(last, "_COMPLETE") || strings.HasSuffix(last, "_FAILED") {
				t.Fatalf("stack %s reached %s (%s), want %v\n%v", name, last, str(st, "StackStatusReason"), want, e.eventLines(t, name))
			}
		} else if strings.Contains(out, "does not exist") && last == "" {
			t.Fatalf("stack %s vanished: %s", name, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("stack %s is %s, want %v\n%v", name, last, want, e.eventLines(t, name))
	return nil
}

func (e *infraEnv) eventLines(t *testing.T, name string) []string {
	var out []string
	for _, ev := range e.events(t, name) {
		out = append(out, fmt.Sprintf("%s %s %s", str(ev, "LogicalResourceId"), str(ev, "ResourceStatus"), str(ev, "ResourceStatusReason")))
	}
	return out
}

func (e *infraEnv) deleted(t *testing.T, name string) {
	t.Helper()
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", name)
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		out, err := e.AWSErr(t, "cloudformation", "describe-stacks", "--stack-name", name)
		if err != nil && strings.Contains(out, "does not exist") {
			return
		}
		if err == nil && str(e.stack(t, name), "StackStatus") == "DELETE_FAILED" {
			t.Fatalf("delete of %s failed: %v", name, e.eventLines(t, name))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("stack %s was not deleted: %v", name, e.eventLines(t, name))
}

// failing creates a stack that must roll back and returns its failure reason.
func (e *infraEnv) failing(t *testing.T, name, tmpl string, params ...string) string {
	t.Helper()
	e.deploy(t, "create", name, tmpl, params...)
	e.wait(t, name, "ROLLBACK_COMPLETE")
	for _, ev := range e.events(t, name) {
		if str(ev, "ResourceStatus") == "CREATE_FAILED" {
			return str(ev, "ResourceStatusReason")
		}
	}
	t.Fatalf("no CREATE_FAILED event: %v", e.eventLines(t, name))
	return ""
}

func list(m map[string]any, k string) []map[string]any {
	var out []map[string]any
	l, _ := m[k].([]any)
	for _, e := range l {
		out = append(out, e.(map[string]any))
	}
	return out
}

func recordSets(t *testing.T, e *infraEnv, zone string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, r := range list(e.AWSJSON(t, "route53", "list-resource-record-sets", "--hosted-zone-id", zone), "ResourceRecordSets") {
		out[str(r, "Name")+" "+str(r, "Type")] = r
	}
	return out
}

func recordValues(r map[string]any) []string {
	var out []string
	for _, v := range list(r, "ResourceRecords") {
		out = append(out, str(v, "Value"))
	}
	return out
}

const dnsCertTemplate = `
Parameters:
  WwwTtl: {Type: Number, Default: 60}
Resources:
  Zone:
    Type: AWS::Route53::HostedZone
    Properties:
      Name: example.test
      HostedZoneConfig: {Comment: infra test}
      HostedZoneTags: [{Key: env, Value: qa}]
  Www:
    Type: AWS::Route53::RecordSet
    Properties:
      HostedZoneId: !Ref Zone
      Name: www.example.test.
      Type: A
      TTL: !Ref WwwTtl
      ResourceRecords: ["10.1.2.3", "10.1.2.4"]
  Txt:
    Type: AWS::Route53::RecordSet
    DependsOn: Zone
    Properties:
      HostedZoneName: example.test.
      Name: txt.example.test
      Type: TXT
      TTL: "300"
      ResourceRecords: ['"v=spf1 -all"']
  Group:
    Type: AWS::Route53::RecordSetGroup
    Properties:
      HostedZoneId: !Ref Zone
      RecordSets:
        - {Name: app.example.test, Type: CNAME, TTL: 120, ResourceRecords: [www.example.test]}
        - {Name: example.test, Type: MX, TTL: 300, ResourceRecords: ["10 mail.example.test"]}
  Cert:
    Type: AWS::ACM::Certificate
    Properties:
      DomainName: example.test
      SubjectAlternativeNames: ["*.example.test"]
      ValidationMethod: DNS
      Tags: [{Key: env, Value: qa}]
Outputs:
  ZoneId: {Value: !Ref Zone}
  NameServers: {Value: !Join [",", !GetAtt Zone.NameServers]}
  ZoneAtt: {Value: !GetAtt Zone.Id}
  WwwRef: {Value: !Ref Www}
  CertArn: {Value: !Ref Cert}
`

func TestInfraRoute53AndACM(t *testing.T) {
	e := newInfraEnv(t, false)
	e.deploy(t, "create", "dns", dnsCertTemplate)
	out := outputs(e.wait(t, "dns", "CREATE_COMPLETE"))
	zone := out["ZoneId"]
	if !strings.HasPrefix(zone, "Z") || out["ZoneAtt"] != zone || out["WwwRef"] != "www.example.test." || out["NameServers"] == "" {
		t.Fatalf("outputs: %v", out)
	}
	z := e.AWSJSON(t, "route53", "get-hosted-zone", "--id", zone)["HostedZone"].(map[string]any)
	if str(z, "Name") != "example.test." || str(z["Config"].(map[string]any), "Comment") != "infra test" {
		t.Fatalf("zone: %v", z)
	}
	recs := recordSets(t, e, zone)
	www := recs["www.example.test. A"]
	if www == nil || www["TTL"].(float64) != 60 || strings.Join(recordValues(www), ",") != "10.1.2.3,10.1.2.4" {
		t.Fatalf("www record: %v", recs)
	}
	if v := recordValues(recs["txt.example.test. TXT"]); len(v) != 1 || v[0] != `"v=spf1 -all"` {
		t.Fatalf("txt record: %v", recs)
	}
	if v := recordValues(recs["app.example.test. CNAME"]); len(v) != 1 || v[0] != "www.example.test" {
		t.Fatalf("cname record: %v", recs)
	}
	if v := recordValues(recs["example.test. MX"]); len(v) != 1 || v[0] != "10 mail.example.test" {
		t.Fatalf("mx record: %v", recs)
	}
	cert := e.AWSJSON(t, "acm", "describe-certificate", "--certificate-arn", out["CertArn"])["Certificate"].(map[string]any)
	if str(cert, "DomainName") != "example.test" || str(cert, "Status") != "ISSUED" || len(cert["SubjectAlternativeNames"].([]any)) < 1 {
		t.Fatalf("certificate: %v", cert)
	}

	// Changing a record replaces it in place; the zone and certificate stay.
	e.deploy(t, "update", "dns", dnsCertTemplate, "WwwTtl=120")
	out2 := outputs(e.wait(t, "dns", "UPDATE_COMPLETE"))
	if out2["ZoneId"] != zone || out2["CertArn"] != out["CertArn"] {
		t.Fatalf("zone or certificate was replaced: %v", out2)
	}
	if www := recordSets(t, e, zone)["www.example.test. A"]; www == nil || www["TTL"].(float64) != 120 {
		t.Fatalf("www after update: %v", www)
	}

	e.deleted(t, "dns")
	if o, err := e.AWSErr(t, "route53", "get-hosted-zone", "--id", zone); err == nil {
		t.Fatalf("zone survived the stack: %s", o)
	}
	if o, err := e.AWSErr(t, "acm", "describe-certificate", "--certificate-arn", out["CertArn"]); err == nil {
		t.Fatalf("certificate survived the stack: %s", o)
	}
}

func TestInfraRoute53Unsupported(t *testing.T) {
	e := newInfraEnv(t, false)
	reason := e.failing(t, "weighted", `
Resources:
  Zone:
    Type: AWS::Route53::HostedZone
    Properties: {Name: weighted.test}
  Rec:
    Type: AWS::Route53::RecordSet
    Properties:
      HostedZoneId: !Ref Zone
      Name: w.weighted.test
      Type: A
      TTL: 60
      SetIdentifier: one
      Weight: 10
      ResourceRecords: ["10.0.0.1"]
`)
	if !strings.Contains(reason, "Property SetIdentifier is not supported by HomeCloud") {
		t.Fatalf("reason: %s", reason)
	}
	// The rollback removed the zone.
	if o := e.AWS(t, "route53", "list-hosted-zones"); strings.Contains(o, "weighted.test") {
		t.Fatalf("zone survived the rollback: %s", o)
	}
	if reason := e.failing(t, "aliasbad", `
Resources:
  Zone:
    Type: AWS::Route53::HostedZone
    Properties: {Name: aliasbad.test}
  Rec:
    Type: AWS::Route53::RecordSet
    Properties:
      HostedZoneId: !Ref Zone
      Name: a.aliasbad.test
      Type: A
      AliasTarget: {DNSName: d111111abcdef8.cloudfront.net, HostedZoneId: Z2FDTNDATAQYW2}
`); !strings.Contains(reason, "not a HomeCloud load balancer DNS name") {
		t.Fatalf("alias reason: %s", reason)
	}
	if reason := e.failing(t, "pca", `
Resources:
  Cert:
    Type: AWS::ACM::Certificate
    Properties:
      DomainName: pca.test
      CertificateAuthorityArn: arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/x
`); !strings.Contains(reason, "Property CertificateAuthorityArn is not supported by HomeCloud") {
		t.Fatalf("pca reason: %s", reason)
	}
}

const taskDefTemplate = `
Parameters:
  Image: {Type: String, Default: "nginx:alpine"}
Resources:
  Td:
    Type: AWS::ECS::TaskDefinition
    Properties:
      Family: infra-web
      Cpu: "256"
      Memory: "512"
      NetworkMode: awsvpc
      RequiresCompatibilities: [FARGATE]
      TaskRoleArn: arn:aws:iam::123456789012:role/task
      ExecutionRoleArn: arn:aws:iam::123456789012:role/exec
      Tags: [{Key: team, Value: core}]
      ContainerDefinitions:
        - Name: web
          Image: !Ref Image
          Essential: true
          PortMappings: [{ContainerPort: 80}]
          Environment: [{Name: STAGE, Value: qa}]
          Command: [nginx, -g, "daemon off;"]
          EntryPoint: [/docker-entrypoint.sh]
          LogConfiguration:
            LogDriver: awslogs
            Options: {awslogs-group: /ecs/infra-web, awslogs-stream-prefix: web, awslogs-region: us-east-1}
Outputs:
  TdArn: {Value: !Ref Td}
  TdAtt: {Value: !GetAtt Td.TaskDefinitionArn}
`

func TestInfraTaskDefinition(t *testing.T) {
	e := newInfraEnv(t, false)
	e.deploy(t, "create", "td", taskDefTemplate)
	out := outputs(e.wait(t, "td", "CREATE_COMPLETE"))
	arn := out["TdArn"]
	if !strings.HasSuffix(arn, ":task-definition/infra-web:1") || out["TdAtt"] != arn {
		t.Fatalf("outputs: %v", out)
	}
	td := e.AWSJSON(t, "ecs", "describe-task-definition", "--task-definition", arn)["taskDefinition"].(map[string]any)
	c := list(td, "containerDefinitions")[0]
	opts := c["logConfiguration"].(map[string]any)["options"].(map[string]any)
	if str(td, "family") != "infra-web" || str(td, "cpu") != "256" || str(td, "memory") != "512" || str(td, "networkMode") != "awsvpc" ||
		str(td, "taskRoleArn") != "arn:aws:iam::123456789012:role/task" || str(c, "name") != "web" || str(c, "image") != "nginx:alpine" ||
		opts["awslogs-group"] != "/ecs/infra-web" || list(c, "portMappings")[0]["containerPort"].(float64) != 80 ||
		str(list(c, "environment")[0], "name") != "STAGE" || c["entryPoint"].([]any)[0] != "/docker-entrypoint.sh" {
		t.Fatalf("task definition: %v", td)
	}

	// A new image is a new revision; the old one is deregistered.
	e.deploy(t, "update", "td", taskDefTemplate, "Image=nginx:1.27-alpine")
	out = outputs(e.wait(t, "td", "UPDATE_COMPLETE"))
	if !strings.HasSuffix(out["TdArn"], ":task-definition/infra-web:2") {
		t.Fatalf("update outputs: %v", out)
	}
	if old := e.AWSJSON(t, "ecs", "describe-task-definition", "--task-definition", arn)["taskDefinition"].(map[string]any); str(old, "status") != "INACTIVE" {
		t.Fatalf("old revision: %v", old)
	}

	e.deleted(t, "td")
	if cur := e.AWSJSON(t, "ecs", "describe-task-definition", "--task-definition", out["TdArn"])["taskDefinition"].(map[string]any); str(cur, "status") != "INACTIVE" {
		t.Fatalf("revision after delete: %v", cur)
	}

	// What one container per task cannot express is refused.
	reason := e.failing(t, "two", `
Resources:
  Td:
    Type: AWS::ECS::TaskDefinition
    Properties:
      Family: two
      ContainerDefinitions:
        - {Name: a, Image: "nginx:alpine"}
        - {Name: b, Image: "nginx:alpine"}
`)
	if !strings.Contains(reason, "single container per task") {
		t.Fatalf("two containers: %s", reason)
	}
	if reason := e.failing(t, "vol", `
Resources:
  Td:
    Type: AWS::ECS::TaskDefinition
    Properties:
      Family: vol
      Volumes: [{Name: data}]
      ContainerDefinitions: [{Name: a, Image: "nginx:alpine"}]
`); !strings.Contains(reason, "Property Volumes is not supported by HomeCloud") {
		t.Fatalf("volumes: %s", reason)
	}
}

const lbTemplate = `
Parameters:
  VpcId: {Type: String}
  SubnetId: {Type: String}
  HttpPort: {Type: Number, Default: 80}
Resources:
  Zone:
    Type: AWS::Route53::HostedZone
    Properties: {Name: lb.test}
  Cert:
    Type: AWS::ACM::Certificate
    Properties: {DomainName: lb.test}
  Tg:
    Type: AWS::ElasticLoadBalancingV2::TargetGroup
    Properties:
      Name: infra-tg
      Port: 8080
      Protocol: HTTP
      VpcId: !Ref VpcId
      TargetType: ip
      HealthCheckPath: /health
      HealthCheckIntervalSeconds: 10
      HealthyThresholdCount: 3
      UnhealthyThresholdCount: 4
      Matcher: {HttpCode: "200-299"}
      Targets: [{Id: 10.9.9.9, Port: 8080}]
      Tags: [{Key: env, Value: qa}]
  Alb:
    Type: AWS::ElasticLoadBalancingV2::LoadBalancer
    Properties:
      Name: infra-alb
      Type: application
      Scheme: internal
      Subnets: [!Ref SubnetId]
  Http:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties:
      LoadBalancerArn: !Ref Alb
      Port: !Ref HttpPort
      Protocol: HTTP
      DefaultActions: [{Type: forward, TargetGroupArn: !Ref Tg}]
  Redirect:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties:
      LoadBalancerArn: !Ref Alb
      Port: 8081
      Protocol: HTTP
      DefaultActions:
        - Type: redirect
          RedirectConfig: {Protocol: HTTPS, Port: "8443", StatusCode: HTTP_301}
  Https:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties:
      LoadBalancerArn: !GetAtt Alb.LoadBalancerArn
      Port: 8443
      Protocol: HTTPS
      Certificates: [{CertificateArn: !Ref Cert}]
      DefaultActions:
        - Type: fixed-response
          FixedResponseConfig: {StatusCode: "200", ContentType: text/plain, MessageBody: secure}
  Static:
    Type: AWS::ElasticLoadBalancingV2::ListenerRule
    Properties:
      ListenerArn: !Ref Http
      Priority: 10
      Conditions: [{Field: path-pattern, PathPatternConfig: {Values: ["/static/*"]}}]
      Actions: [{Type: fixed-response, FixedResponseConfig: {StatusCode: "200", ContentType: text/plain, MessageBody: hello}}]
  Api:
    Type: AWS::ElasticLoadBalancingV2::ListenerRule
    Properties:
      ListenerArn: !Ref Http
      Priority: 20
      Conditions:
        - {Field: host-header, HostHeaderConfig: {Values: [api.lb.test]}}
        - {Field: path-pattern, Values: ["/v1/*"]}
      Actions: [{Type: forward, TargetGroupArn: !Ref Tg}]
  Alias:
    Type: AWS::Route53::RecordSet
    Properties:
      HostedZoneId: !Ref Zone
      Name: www.lb.test
      Type: A
      AliasTarget:
        DNSName: !GetAtt Alb.DNSName
        HostedZoneId: !GetAtt Alb.CanonicalHostedZoneID
Outputs:
  TgArn: {Value: !Ref Tg}
  TgArnAtt: {Value: !GetAtt Tg.TargetGroupArn}
  TgName: {Value: !GetAtt Tg.TargetGroupName}
  AlbArn: {Value: !Ref Alb}
  AlbName: {Value: !GetAtt Alb.LoadBalancerName}
  AlbDns: {Value: !GetAtt Alb.DNSName}
  AlbZone: {Value: !GetAtt Alb.CanonicalHostedZoneID}
  HttpArn: {Value: !Ref Http}
  StaticArn: {Value: !Ref Static}
  ZoneId: {Value: !Ref Zone}
`

func TestInfraLoadBalancing(t *testing.T) {
	e := newInfraEnv(t, true)
	params := []string{"VpcId=" + e.VpcID, "SubnetId=" + e.Subnets[0]}
	e.deploy(t, "create", "lb", lbTemplate, params...)
	out := outputs(e.wait(t, "lb", "CREATE_COMPLETE"))
	if !strings.Contains(out["TgArn"], ":targetgroup/infra-tg/") || out["TgArnAtt"] != out["TgArn"] || out["TgName"] != "infra-tg" ||
		!strings.Contains(out["AlbArn"], ":loadbalancer/app/infra-alb/") || out["AlbName"] != "infra-alb" || out["AlbDns"] != "infra-alb.elb.internal" ||
		out["AlbZone"] != "Z35SXDOTRQ7X7K" || !strings.Contains(out["HttpArn"], ":listener/app/infra-alb/") || !strings.Contains(out["StaticArn"], ":listener-rule/app/infra-alb/") {
		t.Fatalf("outputs: %v", out)
	}

	tg := list(e.AWSJSON(t, "elbv2", "describe-target-groups", "--target-group-arns", out["TgArn"]), "TargetGroups")[0]
	if str(tg, "TargetType") != "ip" || tg["Port"].(float64) != 8080 || str(tg, "HealthCheckPath") != "/health" || tg["HealthCheckIntervalSeconds"].(float64) != 10 ||
		tg["HealthyThresholdCount"].(float64) != 3 || tg["UnhealthyThresholdCount"].(float64) != 4 || str(tg["Matcher"].(map[string]any), "HttpCode") != "200-299" {
		t.Fatalf("target group: %v", tg)
	}
	th := list(e.AWSJSON(t, "elbv2", "describe-target-health", "--target-group-arn", out["TgArn"]), "TargetHealthDescriptions")
	if len(th) != 1 || str(th[0]["Target"].(map[string]any), "Id") != "10.9.9.9" {
		t.Fatalf("targets: %v", th)
	}
	alb := list(e.AWSJSON(t, "elbv2", "describe-load-balancers", "--load-balancer-arns", out["AlbArn"]), "LoadBalancers")[0]
	if str(alb, "Scheme") != "internal" || str(alb, "Type") != "application" || str(alb, "DNSName") != out["AlbDns"] || str(alb["State"].(map[string]any), "Code") != "active" {
		t.Fatalf("load balancer: %v", alb)
	}
	// The placeholder listener that keeps the balancer valid is gone again.
	ports := map[float64]map[string]any{}
	for _, l := range list(e.AWSJSON(t, "elbv2", "describe-listeners", "--load-balancer-arn", out["AlbArn"]), "Listeners") {
		ports[l["Port"].(float64)] = l
	}
	if len(ports) != 3 || ports[80] == nil || ports[8081] == nil || ports[8443] == nil || str(ports[8443], "Protocol") != "HTTPS" || str(ports[80], "ListenerArn") != out["HttpArn"] {
		t.Fatalf("listeners: %v", ports)
	}
	if a := list(ports[8081], "DefaultActions")[0]; str(a, "Type") != "redirect" || str(a["RedirectConfig"].(map[string]any), "StatusCode") != "HTTP_301" {
		t.Fatalf("redirect: %v", a)
	}
	if a := list(ports[80], "DefaultActions")[0]; str(a, "Type") != "forward" || str(a, "TargetGroupArn") != out["TgArn"] {
		t.Fatalf("forward: %v", a)
	}
	prio := map[string]map[string]any{}
	for _, r := range list(e.AWSJSON(t, "elbv2", "describe-rules", "--listener-arn", out["HttpArn"]), "Rules") {
		prio[str(r, "Priority")] = r
	}
	if len(prio) != 3 || str(prio["10"], "RuleArn") != out["StaticArn"] || str(list(prio["10"], "Actions")[0], "Type") != "fixed-response" ||
		str(list(prio["20"], "Actions")[0], "TargetGroupArn") != out["TgArn"] || len(list(prio["20"], "Conditions")) != 2 {
		t.Fatalf("rules: %v", prio)
	}
	recs := recordSets(t, e, out["ZoneId"])
	if a := recs["www.lb.test. A"]; a == nil || str(a["AliasTarget"].(map[string]any), "DNSName") != "infra-alb.elb.internal." {
		t.Fatalf("alias record: %v", recs)
	}

	// A new listener port replaces the listener and the rules under it.
	e.deploy(t, "update", "lb", lbTemplate, append(params, "HttpPort=8082")...)
	out2 := outputs(e.wait(t, "lb", "UPDATE_COMPLETE"))
	if out2["HttpArn"] == out["HttpArn"] || out2["AlbArn"] != out["AlbArn"] || out2["TgArn"] != out["TgArn"] {
		t.Fatalf("update outputs: %v", out2)
	}
	ports = map[float64]map[string]any{}
	for _, l := range list(e.AWSJSON(t, "elbv2", "describe-listeners", "--load-balancer-arn", out["AlbArn"]), "Listeners") {
		ports[l["Port"].(float64)] = l
	}
	if len(ports) != 3 || ports[80] != nil || ports[8082] == nil {
		t.Fatalf("listeners after update: %v", ports)
	}
	if r := list(e.AWSJSON(t, "elbv2", "describe-rules", "--listener-arn", out2["HttpArn"]), "Rules"); len(r) != 3 {
		t.Fatalf("rules after update: %v", r)
	}

	e.deleted(t, "lb")
	if o, err := e.AWSErr(t, "elbv2", "describe-load-balancers", "--load-balancer-arns", out["AlbArn"]); err == nil {
		t.Fatalf("load balancer survived the stack: %s", o)
	}
	if o, err := e.AWSErr(t, "elbv2", "describe-target-groups", "--target-group-arns", out["TgArn"]); err == nil {
		t.Fatalf("target group survived the stack: %s", o)
	}
	if o := e.AWS(t, "route53", "list-hosted-zones"); strings.Contains(o, "lb.test") {
		t.Fatalf("zone survived the stack: %s", o)
	}
}

func TestInfraLoadBalancingUnsupported(t *testing.T) {
	e := newInfraEnv(t, true)
	params := []string{"VpcId=" + e.VpcID, "SubnetId=" + e.Subnets[0]}
	head := `
Parameters:
  VpcId: {Type: String}
  SubnetId: {Type: String}
Resources:
`
	if reason := e.failing(t, "nlb", head+`
  Alb:
    Type: AWS::ElasticLoadBalancingV2::LoadBalancer
    Properties: {Type: network, Subnets: [!Ref SubnetId]}
`, params...); !strings.Contains(reason, "Property Type=network is not supported by HomeCloud") {
		t.Fatalf("network: %s", reason)
	}
	if reason := e.failing(t, "weights", head+`
  A: {Type: "AWS::ElasticLoadBalancingV2::TargetGroup", Properties: {Port: 80, VpcId: !Ref VpcId}}
  B: {Type: "AWS::ElasticLoadBalancingV2::TargetGroup", Properties: {Port: 81, VpcId: !Ref VpcId}}
  Alb:
    Type: AWS::ElasticLoadBalancingV2::LoadBalancer
    Properties: {Subnets: [!Ref SubnetId], Scheme: internal}
  L:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties:
      LoadBalancerArn: !Ref Alb
      Port: 80
      Protocol: HTTP
      DefaultActions:
        - Type: forward
          ForwardConfig:
            TargetGroups: [{TargetGroupArn: !Ref A, Weight: 1}, {TargetGroupArn: !Ref B, Weight: 1}]
`, params...); !strings.Contains(reason, "several target groups") {
		t.Fatalf("weights: %s", reason)
	}
	// Rolling back removed everything, including the placeholder-holding balancer.
	if o := e.AWS(t, "elbv2", "describe-load-balancers"); strings.Contains(o, "LoadBalancerArn") {
		t.Fatalf("balancer survived the rollback: %s", o)
	}
	if o := e.AWS(t, "elbv2", "describe-target-groups"); strings.Contains(o, "TargetGroupArn") {
		t.Fatalf("target groups survived the rollback: %s", o)
	}
}

const serviceTemplate = `
Parameters:
  VpcId: {Type: String}
  SubnetId: {Type: String}
  Stage: {Type: String, Default: one}
Resources:
  Td:
    Type: AWS::ECS::TaskDefinition
    Properties:
      Family: infra-svc
      Cpu: "256"
      Memory: "512"
      ContainerDefinitions:
        - Name: web
          Image: nginx:alpine
          PortMappings: [{ContainerPort: 80}]
          Environment: [{Name: STAGE, Value: !Ref Stage}]
  Tg:
    Type: AWS::ElasticLoadBalancingV2::TargetGroup
    Properties: {Name: svc-tg, Port: 80, VpcId: !Ref VpcId, TargetType: ip}
  Alb:
    Type: AWS::ElasticLoadBalancingV2::LoadBalancer
    Properties: {Name: svc-alb, Scheme: internal, Subnets: [!Ref SubnetId]}
  Listener:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties:
      LoadBalancerArn: !Ref Alb
      Port: 80
      DefaultActions: [{Type: forward, TargetGroupArn: !Ref Tg}]
  Svc:
    Type: AWS::ECS::Service
    DependsOn: Listener
    Properties:
      ServiceName: infra-svc
      TaskDefinition: !Ref Td
      DesiredCount: 1
      LaunchType: FARGATE
      NetworkConfiguration:
        AwsvpcConfiguration: {Subnets: [!Ref SubnetId]}
      LoadBalancers: [{ContainerName: web, ContainerPort: 80, TargetGroupArn: !Ref Tg}]
Outputs:
  SvcArn: {Value: !Ref Svc}
  SvcName: {Value: !GetAtt Svc.Name}
  TdArn: {Value: !Ref Td}
`

func TestInfraECSService(t *testing.T) {
	e := newInfraEnv(t, true)
	params := []string{"VpcId=" + e.VpcID, "SubnetId=" + e.Subnets[0]}
	e.deploy(t, "create", "svc", serviceTemplate, params...)
	out := outputs(e.wait(t, "svc", "CREATE_COMPLETE"))
	if !strings.HasSuffix(out["SvcArn"], ":service/default/infra-svc") || out["SvcName"] != "infra-svc" || !strings.HasSuffix(out["TdArn"], ":task-definition/infra-svc:1") {
		t.Fatalf("outputs: %v", out)
	}
	s := list(e.AWSJSON(t, "ecs", "describe-services", "--cluster", "default", "--services", "infra-svc"), "services")[0]
	if str(s, "status") != "ACTIVE" || s["desiredCount"].(float64) != 1 || s["runningCount"].(float64) != 1 || str(s, "taskDefinition") != out["TdArn"] {
		t.Fatalf("service: %v", s)
	}
	if lbs := list(s, "loadBalancers"); len(lbs) != 1 || !strings.Contains(str(lbs[0], "targetGroupArn"), ":targetgroup/svc-tg/") {
		t.Fatalf("service load balancers: %v", s["loadBalancers"])
	}

	// A new task definition revision replaces the service (the old one drains first).
	e.deploy(t, "update", "svc", serviceTemplate, append(params, "Stage=two")...)
	out2 := outputs(e.wait(t, "svc", "UPDATE_COMPLETE"))
	if !strings.HasSuffix(out2["TdArn"], ":task-definition/infra-svc:2") || out2["SvcArn"] != out["SvcArn"] {
		t.Fatalf("update outputs: %v", out2)
	}
	// The rolling deployment replaces the task after the update completes.
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		s = list(e.AWSJSON(t, "ecs", "describe-services", "--cluster", "default", "--services", "infra-svc"), "services")[0]
		if str(s, "status") == "ACTIVE" && s["runningCount"].(float64) == 1 && str(s, "taskDefinition") == out2["TdArn"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service after update: %v", s)
		}
	}

	e.deleted(t, "svc")
	d := e.AWSJSON(t, "ecs", "describe-services", "--cluster", "default", "--services", "infra-svc")
	for _, s := range list(d, "services") {
		if str(s, "status") == "ACTIVE" {
			t.Fatalf("service survived the stack: %v", s)
		}
	}
}

const rdsTemplate = `
Parameters:
  SubnetA: {Type: String}
  SubnetB: {Type: String}
  Password: {Type: String, NoEcho: true}
  DbPort: {Type: Number, Default: 5432}
  MultiAz: {Type: String, Default: "false"}
Resources:
  Subnets:
    Type: AWS::RDS::DBSubnetGroup
    Properties:
      DBSubnetGroupDescription: infra subnets
      SubnetIds: [!Ref SubnetB, !Ref SubnetA]
  Params:
    Type: AWS::RDS::DBParameterGroup
    Properties: {Description: defaults, Family: postgres16}
  Db:
    Type: AWS::RDS::DBInstance
    Properties:
      DBInstanceIdentifier: infra-db
      Engine: postgres
      EngineVersion: "16"
      DBInstanceClass: db.t3.micro
      AllocatedStorage: "20"
      MasterUsername: dbadmin
      MasterUserPassword: !Ref Password
      DBName: appdb
      DBSubnetGroupName: !Ref Subnets
      DBParameterGroupName: !Ref Params
      VPCSecurityGroups: [sg-0123456789abcdef0]
      Port: !Ref DbPort
      MultiAZ: !Ref MultiAz
      BackupRetentionPeriod: 0
      Tags: [{Key: env, Value: qa}]
Outputs:
  DbId: {Value: !Ref Db}
  Address: {Value: !GetAtt Db.Endpoint.Address}
  Port: {Value: !GetAtt Db.Endpoint.Port}
  SubnetGroup: {Value: !Ref Subnets}
`

func TestInfraRDS(t *testing.T) {
	e := newInfraEnv(t, true)
	params := []string{"SubnetA=" + e.Subnets[0], "SubnetB=" + e.Subnets[1], "Password=S3cretPassw0rd"}

	// What the native model cannot express fails the stack before or after boot.
	if reason := e.failing(t, "multiaz", rdsTemplate, append(params, "MultiAz=true")...); !strings.Contains(reason, "Property MultiAZ is not supported by HomeCloud") {
		t.Fatalf("multi-az: %s", reason)
	}
	if o := e.AWS(t, "rds", "describe-db-instances"); strings.Contains(o, "infra-db") {
		t.Fatalf("instance survived the rollback: %s", o)
	}

	e.deploy(t, "create", "db", rdsTemplate, params...)
	out := outputs(e.wait(t, "db", "CREATE_COMPLETE"))
	if out["DbId"] != "infra-db" || out["Port"] != "5432" || !strings.HasPrefix(out["Address"], "infra-db.") || out["SubnetGroup"] == "" {
		t.Fatalf("outputs: %v", out)
	}
	d := list(e.AWSJSON(t, "rds", "describe-db-instances", "--db-instance-identifier", "infra-db"), "DBInstances")[0]
	if str(d, "DBInstanceStatus") != "available" || str(d, "Engine") != "postgres" || str(d, "DBName") != "appdb" || str(d, "MasterUsername") != "dbadmin" ||
		d["AllocatedStorage"].(float64) != 20 || str(d["Endpoint"].(map[string]any), "Address") != out["Address"] {
		t.Fatalf("instance: %v", d)
	}
	// The subnet group decided where the database runs (its first subnet).
	var native map[string]any
	if err := json.Unmarshal(e.Native(t, "GET", "/api/v1/rds/instances/infra-db", nil), &native); err != nil || native["subnet_id"] != e.Subnets[1] {
		t.Fatalf("placement: %v %v (want %s)", err, native["subnet_id"], e.Subnets[1])
	}

	e.deleted(t, "db")
	if o := e.AWS(t, "rds", "describe-db-instances"); strings.Contains(o, "infra-db") {
		t.Fatalf("instance survived the stack: %s", o)
	}

	// The engine listens on its own port; another Port fails the stack and rolls back.
	if reason := e.failing(t, "port", rdsTemplate, append(params, "DbPort=3307")...); !strings.Contains(reason, "Property Port=3307 is not supported by HomeCloud") {
		t.Fatalf("port: %s", reason)
	}
	if o := e.AWS(t, "rds", "describe-db-instances"); strings.Contains(o, "infra-db") {
		t.Fatalf("instance survived the port rollback: %s", o)
	}
}
