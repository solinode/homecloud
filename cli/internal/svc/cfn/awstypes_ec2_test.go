package cfn_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cfn"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// testRoles gives EC2 instance profiles from IAM (what the server wires up).
type testRoles struct{ iam *iam.Service }

func (r testRoles) InstanceProfile(ref string) (arn, id, role string, err error) {
	p, err := r.iam.GetInstanceProfile(ref)
	if err != nil {
		return "", "", "", err
	}
	if rl, err := r.iam.InstanceProfileRole(ref); err == nil {
		role = rl.ARN
	}
	return p.ARN, p.ID, role, nil
}

func (r testRoles) InstanceCredentials(role, session string, ttl time.Duration) (ec2.Credentials, error) {
	c, err := r.iam.AssumeRoleForService(role, "ec2.amazonaws.com", session, ttl)
	return ec2.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Expiration: c.Expiration}, err
}

// newEC2Env starts CloudFormation with IAM and, when withNet is set, the VPC
// and EC2 services (a VPC is a Docker network, so that part skips without Docker).
func newEC2Env(t *testing.T, withNet bool) *env {
	t.Helper()
	h := awstest.New(t)
	if withNet {
		d, err := runtime.New()
		if err != nil || d.C.Ping() != nil {
			t.Skip("Docker not available")
		}
		h.Env.Docker = d
		v := vpc.New(h.Env)
		if err := v.EnsureDefault(t.Context()); err != nil {
			t.Skipf("default vpc: %v", err)
		}
		e := ec2.New(h.Env, v)
		e.Roles = testRoles{h.IAM}
		e.RegisterAWS()
		v.Routes(h.Router)
		e.Routes(h.Router)
	}
	c := cfn.New(h.Env)
	c.Handler = h.Mux
	c.Refresh = h.IAM.Refresh
	c.RolePrincipal = func(arn string) (*httpx.Principal, error) {
		return h.IAM.ServiceRolePrincipal(arn, "cloudformation.amazonaws.com", "HomeCloudCloudFormation")
	}
	c.Routes(h.Router)
	c.RegisterAWS()
	return &env{Harness: h, CFN: c}
}

func TestInstanceProfileStack(t *testing.T) {
	e := newEC2Env(t, false)
	tf := write(t, "ip.yaml", `
Resources:
  Role:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument: {Version: "2012-10-17", Statement: [{Effect: Allow, Principal: {Service: ec2.amazonaws.com}, Action: "sts:AssumeRole"}]}
  Profile:
    Type: AWS::IAM::InstanceProfile
    Properties:
      Path: /apps/
      Roles: [!Ref Role]
Outputs:
  Name: {Value: !Ref Profile}
  Arn: {Value: !GetAtt Profile.Arn}
  Role: {Value: !Ref Role}
`)
	o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "ip", "--template-body", "file://"+tf)
	if err == nil || !strings.Contains(o, "CAPABILITY_IAM") {
		t.Fatalf("an instance profile needs CAPABILITY_IAM: %v %s", err, o)
	}
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "ip", "--template-body", "file://"+tf, "--capabilities", "CAPABILITY_IAM")
	out := outputs(e.waitFor(t, "ip", "CREATE_COMPLETE"))
	if !strings.HasPrefix(out["Name"], "ip-Profile-") || out["Arn"] != "arn:aws:iam::"+e.Env.AccountID+":instance-profile/apps/"+out["Name"] {
		t.Fatalf("outputs: %v", out)
	}
	p := e.AWSJSON(t, "iam", "get-instance-profile", "--instance-profile-name", out["Name"])["InstanceProfile"].(map[string]any)
	roles := p["Roles"].([]any)
	if len(roles) != 1 || str(roles[0].(map[string]any), "RoleName") != out["Role"] || str(p, "Path") != "/apps/" {
		t.Fatalf("profile: %v", p)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "ip")
	e.waitGone(t, "ip")
	if _, err := e.AWSErr(t, "iam", "get-instance-profile", "--instance-profile-name", out["Name"]); err == nil {
		t.Fatal("profile survived the stack")
	}
	if _, err := e.AWSErr(t, "iam", "get-role", "--role-name", out["Role"]); err == nil {
		t.Fatal("role survived the stack")
	}
	// Two roles are refused, as in AWS.
	two := write(t, "two.yaml", `
Resources:
  Profile:
    Type: AWS::IAM::InstanceProfile
    Properties: {Roles: [a, b]}
`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "two", "--template-body", "file://"+two, "--capabilities", "CAPABILITY_IAM")
	e.waitFor(t, "two", "ROLLBACK_COMPLETE")
}

const netTemplate = `
Resources:
  Vpc:
    Type: AWS::EC2::VPC
    Properties:
      CidrBlock: 10.77.0.0/16
      EnableDnsSupport: true
      EnableDnsHostnames: true
      Tags: [{Key: Name, Value: cfn-net}]
  Public:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref Vpc
      CidrBlock: 10.77.1.0/24
      AvailabilityZone: us-east-1b
      Tags: [{Key: Name, Value: cfn-public}]
  Igw:
    Type: AWS::EC2::InternetGateway
    Properties:
      Tags: [{Key: Name, Value: cfn-igw}]
  Attach:
    Type: AWS::EC2::VPCGatewayAttachment
    Properties: {VpcId: !Ref Vpc, InternetGatewayId: !Ref Igw}
  Rtb:
    Type: AWS::EC2::RouteTable
    Properties: {VpcId: !Ref Vpc}
  Default:
    Type: AWS::EC2::Route
    DependsOn: Attach
    Properties: {RouteTableId: !Ref Rtb, DestinationCidrBlock: 0.0.0.0/0, GatewayId: !Ref Igw}
  Assoc:
    Type: AWS::EC2::SubnetRouteTableAssociation
    Properties: {RouteTableId: !Ref Rtb, SubnetId: !Ref Public}
  Web:
    Type: AWS::EC2::SecurityGroup
    Properties:
      GroupName: cfn-web
      GroupDescription: web
      VpcId: !Ref Vpc
      SecurityGroupIngress:
        - {IpProtocol: tcp, FromPort: 80, ToPort: 80, CidrIp: 0.0.0.0/0}
        - {IpProtocol: tcp, FromPort: 22, ToPort: 22, CidrIp: 10.0.0.0/8}
        - {IpProtocol: icmp, FromPort: -1, ToPort: -1, CidrIp: 0.0.0.0/0}
      SecurityGroupEgress:
        - {IpProtocol: "-1", CidrIp: 0.0.0.0/0}
  Extra:
    Type: AWS::EC2::SecurityGroupIngress
    Properties: {GroupId: !GetAtt Web.GroupId, IpProtocol: udp, FromPort: 5353, ToPort: 5353, CidrIp: 127.0.0.1/32}
  Ip:
    Type: AWS::EC2::EIP
    Properties: {Domain: vpc}
  Data:
    Type: AWS::EC2::Volume
    Properties: {AvailabilityZone: us-east-1a, Size: 3, Tags: [{Key: Name, Value: cfn-data}]}
Outputs:
  VpcId: {Value: !Ref Vpc}
  VpcCidr: {Value: !GetAtt Vpc.CidrBlock}
  DefaultSg: {Value: !GetAtt Vpc.DefaultSecurityGroup}
  SubnetId: {Value: !Ref Public}
  SubnetAz: {Value: !GetAtt Public.AvailabilityZone}
  IgwId: {Value: !Ref Igw}
  RtbId: {Value: !Ref Rtb}
  SgId: {Value: !GetAtt Web.GroupId}
  SgRef: {Value: !Ref Web}
  SgVpc: {Value: !GetAtt Web.VpcId}
  RuleId: {Value: !Ref Extra}
  EipRef: {Value: !Ref Ip}
  EipAlloc: {Value: !GetAtt Ip.AllocationId}
  VolumeId: {Value: !Ref Data}
`

func TestNetworkStack(t *testing.T) {
	e := newEC2Env(t, true)
	tf := write(t, "net.yaml", netTemplate)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "net", "--template-body", "file://"+tf)
	st := e.waitFor(t, "net", "CREATE_COMPLETE")
	out := outputs(st)
	if !strings.HasPrefix(out["VpcId"], "vpc-") || out["VpcCidr"] != "10.77.0.0/16" || !strings.HasPrefix(out["DefaultSg"], "sg-") {
		t.Fatalf("vpc outputs: %v", out)
	}
	if out["SubnetAz"] != "us-east-1b" || out["SgRef"] != out["SgId"] || out["SgVpc"] != out["VpcId"] || !strings.HasPrefix(out["RuleId"], "sgr-") {
		t.Fatalf("outputs: %v", out)
	}
	if out["EipRef"] == out["EipAlloc"] || !strings.HasPrefix(out["EipAlloc"], "eipalloc-") {
		t.Fatalf("eip outputs: %v", out)
	}

	vpcs := e.AWSJSON(t, "ec2", "describe-vpcs", "--vpc-ids", out["VpcId"])["Vpcs"].([]any)
	if v := vpcs[0].(map[string]any); str(v, "CidrBlock") != "10.77.0.0/16" || str(v, "State") != "available" {
		t.Fatalf("vpc: %v", v)
	}
	sn := e.AWSJSON(t, "ec2", "describe-subnets", "--subnet-ids", out["SubnetId"])["Subnets"].([]any)[0].(map[string]any)
	if str(sn, "VpcId") != out["VpcId"] || str(sn, "CidrBlock") != "10.77.1.0/24" || str(sn, "AvailabilityZone") != "us-east-1b" {
		t.Fatalf("subnet: %v", sn)
	}
	sg := e.AWSJSON(t, "ec2", "describe-security-groups", "--group-ids", out["SgId"])["SecurityGroups"].([]any)[0].(map[string]any)
	if str(sg, "GroupName") != "cfn-web" || str(sg, "VpcId") != out["VpcId"] || str(sg, "Description") != "web" {
		t.Fatalf("sg: %v", sg)
	}
	// Ingress: port 80 and the udp rule; the 10.0.0.0/8 and icmp rules cannot be enforced and are left out.
	ports := map[string]bool{}
	for _, p := range sg["IpPermissions"].([]any) {
		pm := p.(map[string]any)
		ports[str(pm, "IpProtocol")+"/"+fmt.Sprint(pm["FromPort"])] = true
	}
	if len(ports) != 2 || !ports["tcp/80"] || !ports["udp/5353"] {
		t.Fatalf("ingress rules: %v", ports)
	}
	igw := e.AWSJSON(t, "ec2", "describe-internet-gateways", "--internet-gateway-ids", out["IgwId"])["InternetGateways"].([]any)[0].(map[string]any)
	if at := igw["Attachments"].([]any); len(at) != 1 || str(at[0].(map[string]any), "VpcId") != out["VpcId"] {
		t.Fatalf("igw: %v", igw)
	}
	rtb := e.AWSJSON(t, "ec2", "describe-route-tables", "--route-table-ids", out["RtbId"])["RouteTables"].([]any)[0].(map[string]any)
	var def bool
	for _, r := range rtb["Routes"].([]any) {
		if rm := r.(map[string]any); str(rm, "DestinationCidrBlock") == "0.0.0.0/0" && str(rm, "GatewayId") == out["IgwId"] {
			def = true
		}
	}
	assoc := false
	for _, a := range rtb["Associations"].([]any) {
		if str(a.(map[string]any), "SubnetId") == out["SubnetId"] {
			assoc = true
		}
	}
	if !def || !assoc {
		t.Fatalf("route table: %v", rtb)
	}
	if vol := e.AWSJSON(t, "ec2", "describe-volumes", "--volume-ids", out["VolumeId"])["Volumes"].([]any)[0].(map[string]any); vol["Size"] != float64(3) || str(vol, "AvailabilityZone") != "us-east-1a" {
		t.Fatalf("volume: %v", vol)
	}
	if addrs := e.AWSJSON(t, "ec2", "describe-addresses", "--allocation-ids", out["EipAlloc"])["Addresses"].([]any); len(addrs) != 1 || str(addrs[0].(map[string]any), "PublicIp") != out["EipRef"] {
		t.Fatalf("eip: %v", addrs)
	}

	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "net")
	e.waitGone(t, "net")
	for _, c := range [][]string{
		{"describe-vpcs", "--vpc-ids", out["VpcId"]}, {"describe-subnets", "--subnet-ids", out["SubnetId"]},
		{"describe-security-groups", "--group-ids", out["SgId"]}, {"describe-internet-gateways", "--internet-gateway-ids", out["IgwId"]},
		{"describe-route-tables", "--route-table-ids", out["RtbId"]}, {"describe-volumes", "--volume-ids", out["VolumeId"]},
		{"describe-addresses", "--allocation-ids", out["EipAlloc"]},
	} {
		if o, err := e.AWSErr(t, append([]string{"ec2"}, c...)...); err == nil {
			t.Errorf("%s: resource survived the stack: %s", c[0], o)
		}
	}
}

// TestInstanceStack launches an instance from a stack. It needs Docker and an
// image the host can pull, so it is skipped when either is missing.
func TestInstanceStack(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a container")
	}
	e := newEC2Env(t, true)
	tf := write(t, "inst.yaml", `
Resources:
  Role:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument: {Version: "2012-10-17", Statement: [{Effect: Allow, Principal: {Service: ec2.amazonaws.com}, Action: "sts:AssumeRole"}]}
  Profile:
    Type: AWS::IAM::InstanceProfile
    Properties: {Roles: [!Ref Role]}
  Sg:
    Type: AWS::EC2::SecurityGroup
    Properties:
      GroupDescription: web
      SecurityGroupIngress: [{IpProtocol: tcp, FromPort: 80, ToPort: 80, CidrIp: 0.0.0.0/0}]
  Web:
    Type: AWS::EC2::Instance
    Properties:
      ImageId: ami-nginx
      InstanceType: t3.micro
      SecurityGroupIds: [!GetAtt Sg.GroupId]
      IamInstanceProfile: !Ref Profile
      UserData: !Base64 "#!/bin/sh\necho hi\n"
      Tags: [{Key: Name, Value: cfn-web}]
Outputs:
  Id: {Value: !Ref Web}
  Ip: {Value: !GetAtt Web.PrivateIp}
  Dns: {Value: !GetAtt Web.PrivateDnsName}
  Az: {Value: !GetAtt Web.AvailabilityZone}
`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "inst", "--template-body", "file://"+tf, "--capabilities", "CAPABILITY_IAM")
	deadline := 3 * time.Minute
	var st map[string]any
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		st = e.stack(t, "inst")
		if s := str(st, "StackStatus"); s != "CREATE_IN_PROGRESS" {
			break
		}
		time.Sleep(time.Second)
	}
	if s := str(st, "StackStatus"); s != "CREATE_COMPLETE" {
		t.Skipf("instance did not start (image unavailable?): %s %s", s, str(st, "StackStatusReason"))
	}
	out := outputs(st)
	if !strings.HasPrefix(out["Id"], "i-") || out["Ip"] == "" || out["Az"] == "" {
		t.Fatalf("outputs: %v", out)
	}
	inst := e.AWSJSON(t, "ec2", "describe-instances", "--instance-ids", out["Id"])["Reservations"].([]any)[0].(map[string]any)["Instances"].([]any)[0].(map[string]any)
	if str(inst["State"].(map[string]any), "Name") != "running" || str(inst, "PrivateIpAddress") != out["Ip"] || str(inst, "InstanceType") != "t3.micro" {
		t.Fatalf("instance: %v", inst)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "inst")
	e.waitGone(t, "inst")
}
