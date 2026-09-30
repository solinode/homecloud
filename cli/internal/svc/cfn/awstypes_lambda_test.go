package cfn_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cfn"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/s3"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sns"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ssm"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// lambdaRoles lets the Lambda service validate execution roles.
type lambdaRoles struct{ e *env }

func (r lambdaRoles) LambdaRole(ref string) (string, error) {
	role, err := r.e.IAM.ServiceRole(ref, "lambda.amazonaws.com")
	return role.ARN, err
}

func (r lambdaRoles) LambdaCredentials(ref, session string, ttl time.Duration) (lambda.Credentials, error) {
	c, err := r.e.IAM.AssumeRoleForService(ref, "lambda.amazonaws.com", session, ttl)
	return lambda.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Expiration: c.Expiration}, err
}

// newLambdaEnv is newEnv plus Lambda and API Gateway (and S3 when asked for).
func newLambdaEnv(t *testing.T, withS3 bool) *env {
	t.Helper()
	e := &env{}
	h := awstest.New(t)
	e.Harness = h
	q := sqs.New(h.Env)
	q.Routes(h.Router)
	q.RegisterAWS()
	sn := sns.New(h.Env, q, nil)
	sn.Routes(h.Router)
	sn.RegisterAWS()
	k := kms.New(h.Env, h.Secrets)
	k.Routes(h.Router)
	k.RegisterAWS()
	p := ssm.New(h.Env, k)
	p.Routes(h.Router)
	p.RegisterAWS()
	if withS3 {
		addr := startMinIO(t)
		b := s3.New(h.Env, h.Secrets)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := b.UseMinIO(ctx, addr, minioUser, minioPass); err != nil {
			t.Fatal(err)
		}
		b.Routes(h.Router)
		b.RegisterAWS()
	}
	cw, err := cloudwatch.New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	l := lambda.New(h.Env, cw, vpc.New(h.Env))
	l.Roles = lambdaRoles{e}
	l.Queues = q
	l.RegisterAWS()
	l.Routes(h.Router)
	c := cfn.New(h.Env)
	c.Handler = h.Mux
	c.Refresh = h.IAM.Refresh
	c.RolePrincipal = func(arn string) (*httpx.Principal, error) {
		return h.IAM.ServiceRolePrincipal(arn, "cloudformation.amazonaws.com", "HomeCloudCloudFormation")
	}
	c.Routes(h.Router)
	c.RegisterAWS()
	e.CFN = c
	return e
}

const lambdaTemplate = `
AWSTemplateFormatVersion: "2010-09-09"
Resources:
  Role:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: "2012-10-17"
        Statement:
          - Effect: Allow
            Principal: {Service: lambda.amazonaws.com}
            Action: sts:AssumeRole
  Queue:
    Type: AWS::SQS::Queue
    Properties: {QueueName: fn-queue}
  Fn:
    Type: AWS::Lambda::Function
    Properties:
      Runtime: python3.12
      Handler: app.handler
      Role: !GetAtt Role.Arn
      Description: from a stack
      Timeout: 7
      MemorySize: 256
      Environment:
        Variables: {STAGE: prod, N: 3}
      Tags:
        - {Key: team, Value: platform}
      Code:
        ZipFile: |
          def handler(event, context):
              return {"statusCode": 200, "body": "hello"}
  Ver:
    Type: AWS::Lambda::Version
    Properties: {FunctionName: !Ref Fn, Description: first}
  Live:
    Type: AWS::Lambda::Alias
    Properties: {FunctionName: !Ref Fn, FunctionVersion: !GetAtt Ver.Version, Name: live}
  Perm:
    Type: AWS::Lambda::Permission
    Properties:
      FunctionName: !GetAtt Fn.Arn
      Action: lambda:InvokeFunction
      Principal: apigateway.amazonaws.com
      SourceAccount: "123456789012"
  Url:
    Type: AWS::Lambda::Url
    Properties:
      TargetFunctionArn: !GetAtt Fn.Arn
      AuthType: NONE
      Cors: {AllowOrigins: ["*"], AllowMethods: [GET]}
  Map:
    Type: AWS::Lambda::EventSourceMapping
    Properties:
      FunctionName: !Ref Fn
      EventSourceArn: !GetAtt Queue.Arn
      BatchSize: 5
      Enabled: true
  Api:
    Type: AWS::ApiGatewayV2::Api
    Properties: {Name: stack-api, ProtocolType: HTTP, Description: an api}
  Integ:
    Type: AWS::ApiGatewayV2::Integration
    Properties:
      ApiId: !Ref Api
      IntegrationType: AWS_PROXY
      IntegrationUri: !GetAtt Fn.Arn
      PayloadFormatVersion: "2.0"
  RouteHello:
    Type: AWS::ApiGatewayV2::Route
    Properties:
      ApiId: !Ref Api
      RouteKey: GET /hello
      Target: !Sub integrations/${Integ}
  Stage:
    Type: AWS::ApiGatewayV2::Stage
    Properties: {ApiId: !Ref Api, StageName: prod, AutoDeploy: true}
Outputs:
  FnName: {Value: !Ref Fn}
  FnArn: {Value: !GetAtt Fn.Arn}
  VerArn: {Value: !Ref Ver}
  VerNum: {Value: !GetAtt Ver.Version}
  AliasArn: {Value: !Ref Live}
  UrlValue: {Value: !GetAtt Url.FunctionUrl}
  MapId: {Value: !Ref Map}
  ApiId: {Value: !Ref Api}
  Endpoint: {Value: !GetAtt Api.ApiEndpoint}
`

func TestLambdaStack(t *testing.T) {
	e := newLambdaEnv(t, false)
	acct := e.Env.AccountID
	body := strings.Replace(lambdaTemplate, `"123456789012"`, `"`+acct+`"`, 1)
	tf := write(t, "lambda.yaml", body)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "fnstack", "--template-body", "file://"+tf, "--capabilities", "CAPABILITY_IAM")
	out := outputs(e.waitFor(t, "fnstack", "CREATE_COMPLETE"))

	fnName := out["FnName"]
	if !strings.HasPrefix(fnName, "fnstack-Fn-") {
		t.Fatalf("generated function name: %q", fnName)
	}
	fnArn := "arn:aws:lambda:us-east-1:" + acct + ":function:" + fnName
	if out["FnArn"] != fnArn {
		t.Fatalf("GetAtt Arn = %q, want %q", out["FnArn"], fnArn)
	}
	if out["VerArn"] != fnArn+":1" || out["VerNum"] != "1" || out["AliasArn"] != fnArn+":live" {
		t.Fatalf("version/alias outputs: %v", out)
	}

	gf := e.AWSJSON(t, "lambda", "get-function", "--function-name", fnName)
	cfg := gf["Configuration"].(map[string]any)
	if cfg["Runtime"] != "python3.12" || cfg["Handler"] != "app.handler" || cfg["Description"] != "from a stack" ||
		cfg["Timeout"].(float64) != 7 || cfg["MemorySize"].(float64) != 256 || cfg["FunctionArn"] != fnArn ||
		!strings.HasSuffix(cfg["Role"].(string), ":role/"+lastPart(cfg["Role"].(string))) {
		t.Fatalf("configuration: %v", cfg)
	}
	vars := cfg["Environment"].(map[string]any)["Variables"].(map[string]any)
	if vars["STAGE"] != "prod" || vars["N"] != "3" {
		t.Fatalf("environment: %v", vars)
	}
	if gf["Tags"].(map[string]any)["team"] != "platform" {
		t.Fatalf("tags: %v", gf["Tags"])
	}
	// The inline source was written to the file the handler names.
	code := e.Native(t, "GET", "/api/v1/lambda/functions/"+fnName+"/code", nil)
	if !strings.Contains(string(code), `"app.py"`) || !strings.Contains(string(code), "statusCode") {
		t.Fatalf("code: %s", code)
	}

	al := e.AWSJSON(t, "lambda", "get-alias", "--function-name", fnName, "--name", "live")
	if al["FunctionVersion"] != "1" || al["AliasArn"] != fnArn+":live" {
		t.Fatalf("alias: %v", al)
	}

	pol := e.AWS(t, "lambda", "get-policy", "--function-name", fnName)
	if !strings.Contains(pol, "apigateway.amazonaws.com") || !strings.Contains(pol, "lambda:InvokeFunction") || !strings.Contains(pol, "fnstack-Perm-") {
		t.Fatalf("policy: %s", pol)
	}

	uc := e.AWSJSON(t, "lambda", "get-function-url-config", "--function-name", fnName)
	if uc["FunctionUrl"] != out["UrlValue"] || uc["AuthType"] != "NONE" || uc["FunctionUrl"] == "" ||
		!strings.Contains(e.AWS(t, "lambda", "get-function-url-config", "--function-name", fnName), "GET") {
		t.Fatalf("url config: %v (output %q)", uc, out["UrlValue"])
	}

	maps := e.AWSJSON(t, "lambda", "list-event-source-mappings", "--function-name", fnName)["EventSourceMappings"].([]any)
	if len(maps) != 1 {
		t.Fatalf("mappings: %v", maps)
	}
	m := maps[0].(map[string]any)
	if m["UUID"] != out["MapId"] || m["BatchSize"].(float64) != 5 || !strings.HasSuffix(m["EventSourceArn"].(string), ":fn-queue") {
		t.Fatalf("mapping: %v", m)
	}

	api := e.AWSJSON(t, "apigatewayv2", "get-api", "--api-id", out["ApiId"])
	if api["Name"] != "stack-api" || api["ProtocolType"] != "HTTP" || api["ApiEndpoint"] == "" || out["Endpoint"] == "" || !strings.Contains(out["Endpoint"], out["ApiId"]) {
		t.Fatalf("api: %v endpoint %q", api, out["Endpoint"])
	}
	routes := e.AWSJSON(t, "apigatewayv2", "get-routes", "--api-id", out["ApiId"])["Items"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["RouteKey"] != "GET /hello" {
		t.Fatalf("routes: %v", routes)
	}

	// Code and configuration change in place: the function keeps its name, ARN, URL, mapping and version.
	v2 := strings.NewReplacer("Description: from a stack", "Description: second", "Timeout: 7", "Timeout: 9", "STAGE: prod", "STAGE: dev",
		`"body": "hello"`, `"body": "hello again"`).Replace(body)
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "fnstack", "--template-body", "file://"+write(t, "lambda2.yaml", v2), "--capabilities", "CAPABILITY_IAM")
	up := outputs(e.waitFor(t, "fnstack", "UPDATE_COMPLETE"))
	if up["FnName"] != fnName || up["FnArn"] != fnArn || up["MapId"] != out["MapId"] || up["UrlValue"] != out["UrlValue"] {
		t.Fatalf("outputs changed: %v -> %v", out, up)
	}
	cfg = e.AWSJSON(t, "lambda", "get-function", "--function-name", fnName)["Configuration"].(map[string]any)
	if cfg["Description"] != "second" || cfg["Timeout"].(float64) != 9 || cfg["MemorySize"].(float64) != 256 ||
		cfg["Environment"].(map[string]any)["Variables"].(map[string]any)["STAGE"] != "dev" {
		t.Fatalf("configuration after the update: %v", cfg)
	}
	if code := e.Native(t, "GET", "/api/v1/lambda/functions/"+fnName+"/code", nil); !strings.Contains(string(code), "hello again") {
		t.Fatalf("code after the update: %s", code)
	}
	for _, ev := range e.events(t, "fnstack") {
		if strings.HasPrefix(str(ev, "ResourceStatus"), "DELETE_") {
			t.Fatalf("the update deleted %s", str(ev, "LogicalResourceId"))
		}
	}

	// Deleting the stack removes everything it made.
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "fnstack")
	e.waitGone(t, "fnstack")
	for _, args := range [][]string{
		{"lambda", "get-function", "--function-name", fnName},
		{"apigatewayv2", "get-api", "--api-id", out["ApiId"]},
		{"lambda", "get-event-source-mapping", "--uuid", out["MapId"]},
	} {
		if o, err := e.AWSErr(t, args...); err == nil {
			t.Fatalf("%v survived the stack: %s", args, o)
		}
	}
}

func lastPart(s string) string { return s[strings.LastIndex(s, "/")+1:] }

func TestLambdaStackFromS3AndLayer(t *testing.T) {
	e := newLambdaEnv(t, true)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("index.mjs")
	w.Write([]byte("export const handler = async () => ({statusCode: 200});\n"))
	zw.Close()
	zp := filepath.Join(t.TempDir(), "code.zip")
	if err := os.WriteFile(zp, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	e.AWS(t, "s3api", "create-bucket", "--bucket", "code-bucket")
	e.AWS(t, "s3api", "put-object", "--bucket", "code-bucket", "--key", "app/code.zip", "--body", zp)
	tf := write(t, "s3.json", `{"Resources":{
	  "Layer":{"Type":"AWS::Lambda::LayerVersion","Properties":{"LayerName":"stack-layer","Content":{"S3Bucket":"code-bucket","S3Key":"app/code.zip"},"CompatibleRuntimes":["nodejs20.x"]}},
	  "Fn":{"Type":"AWS::Lambda::Function","Properties":{"FunctionName":"s3-fn","Runtime":"nodejs20.x","Handler":"index.handler",
	    "Code":{"S3Bucket":"code-bucket","S3Key":"app/code.zip"},"Layers":[{"Ref":"Layer"}]}}},
	 "Outputs":{"Layer":{"Value":{"Ref":"Layer"}}}}`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "s3fn", "--template-body", "file://"+tf)
	out := outputs(e.waitFor(t, "s3fn", "CREATE_COMPLETE"))
	if !strings.HasSuffix(out["Layer"], ":layer:stack-layer:1") {
		t.Fatalf("layer ref: %v", out)
	}
	cfg := e.AWSJSON(t, "lambda", "get-function-configuration", "--function-name", "s3-fn")
	if cfg["Runtime"] != "nodejs20.x" || cfg["CodeSize"].(float64) != float64(buf.Len()) {
		t.Fatalf("s3 code function: %v", cfg)
	}
	if ls, _ := cfg["Layers"].([]any); len(ls) != 1 || ls[0].(map[string]any)["Arn"] != out["Layer"] {
		t.Fatalf("layers: %v", cfg["Layers"])
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "s3fn")
	e.waitGone(t, "s3fn")
	if o, err := e.AWSErr(t, "lambda", "get-function", "--function-name", "s3-fn"); err == nil {
		t.Fatalf("function survived: %s", o)
	}
	if vs := e.AWSJSON(t, "lambda", "list-layer-versions", "--layer-name", "stack-layer")["LayerVersions"].([]any); len(vs) != 0 {
		t.Fatalf("layer version survived: %v", vs)
	}
}

func TestLambdaStackAuthorizationAndErrors(t *testing.T) {
	e := newLambdaEnv(t, false)
	pol := func(name, doc string) {
		var d any
		if err := json.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatal(err)
		}
		e.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": name, "document": d})
	}
	pol("cfn-only", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudformation:*","Resource":"*"}]}`)
	akid, secret := e.User(t, "deployer", "cfn-only")
	e.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "fn-role", "assume_role_policy": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}}}})
	roleARN := "arn:aws:iam::" + e.Env.AccountID + ":role/fn-role"
	tf := write(t, "denied.json", `{"Resources":{"Fn":{"Type":"AWS::Lambda::Function","Properties":{"FunctionName":"forbidden-fn","Runtime":"python3.12",
	  "Handler":"index.handler","Role":"`+roleARN+`","Code":{"ZipFile":"def handler(e, c):\n    return 1\n"}}}}}`)

	// A caller with no Lambda permissions cannot create a function through a stack.
	if o, err := e.AWSAs(t, akid, secret, "", "cloudformation", "create-stack", "--stack-name", "denied", "--template-body", "file://"+tf); err != nil {
		t.Fatalf("create-stack: %v %s", err, o)
	}
	deadline := time.Now().Add(30 * time.Second)
	var st map[string]any
	for time.Now().Before(deadline) {
		o, _ := e.AWSAs(t, akid, secret, "", "cloudformation", "describe-stacks", "--stack-name", "denied")
		var m map[string]any
		_ = json.Unmarshal([]byte(o), &m)
		if l, ok := m["Stacks"].([]any); ok {
			st = l[0].(map[string]any)
			if s := str(st, "StackStatus"); strings.HasSuffix(s, "_COMPLETE") || strings.HasSuffix(s, "_FAILED") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if str(st, "StackStatus") != "ROLLBACK_COMPLETE" {
		t.Fatalf("unauthorized function create: %v", st)
	}
	if o, _ := e.AWSAs(t, akid, secret, "", "cloudformation", "describe-stack-events", "--stack-name", "denied"); !strings.Contains(o, "lambda:CreateFunction") {
		t.Fatalf("events must name the denied action: %s", o)
	}
	if o, err := e.AWSErr(t, "lambda", "get-function", "--function-name", "forbidden-fn"); err == nil {
		t.Fatalf("forbidden function exists: %s", o)
	}

	// Properties HomeCloud cannot express are refused, not ignored.
	bad := write(t, "bad.json", `{"Resources":{"Fn":{"Type":"AWS::Lambda::Function","Properties":{"FunctionName":"efs-fn","Runtime":"python3.12",
	  "Handler":"index.handler","Role":"`+roleARN+`","Code":{"ZipFile":"x = 1"},"FileSystemConfigs":[{"Arn":"arn:aws:elasticfilesystem:us-east-1:1:access-point/fsap-1","LocalMountPath":"/mnt/x"}]}}}}`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "unsupported", "--template-body", "file://"+bad)
	e.waitFor(t, "unsupported", "ROLLBACK_COMPLETE")
	if o := e.AWS(t, "cloudformation", "describe-stack-events", "--stack-name", "unsupported"); !strings.Contains(o, "Property FileSystemConfigs is not supported by HomeCloud") {
		t.Fatalf("events: %s", o)
	}
}
