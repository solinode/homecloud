package lambda_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

func gwHarness(t *testing.T, docker bool) (*awstest.Harness, *lambda.Service) {
	t.Helper()
	h := awstest.New(t)
	if docker {
		d, err := runtime.New()
		if err != nil {
			t.Skip("docker not available: ", err)
		}
		h.Env.Docker = d
		runtime.Account = "gwtest" + h.Env.AccountID
	}
	cw, err := cloudwatch.New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	v := vpc.New(h.Env)
	if docker {
		if err := v.EnsureDefault(context.Background()); err != nil {
			t.Skip("cannot create the default VPC: ", err)
		}
		t.Cleanup(func() {
			for _, x := range v.List() {
				_ = v.DeleteVPC(x.ID)
			}
		})
	}
	l := lambda.New(h.Env, cw, v)
	l.Roles = roles{h.IAM}
	l.RegisterAWS()
	l.Routes(h.Router)
	return h, l
}

// call requests the API's endpoint (rewritten to the test server) and returns status and body.
func gwCall(t *testing.T, h *awstest.Harness, endpoint, method, path string, hdr map[string]string) (int, string) {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(method, h.URL+u.Path+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func arr(m map[string]any, k string) []any { v, _ := m[k].([]any); return v }

// TestAPIGatewayControlPlane drives the HTTP API management API with the AWS
// CLI and serves requests through an HTTP_PROXY integration (no Docker).
func TestAPIGatewayControlPlane(t *testing.T) {
	h, _ := gwHarness(t, false)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "yes")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, r.Method+" "+r.URL.RequestURI())
	}))
	defer backend.Close()

	if out, err := h.AWSErr(t, "apigatewayv2", "create-api", "--name", "ws", "--protocol-type", "WEBSOCKET",
		"--route-selection-expression", "$request.body.action"); err == nil || !strings.Contains(out, "BadRequestException") {
		t.Fatalf("websocket APIs are not supported: %s", out)
	}
	api := h.AWSJSON(t, "apigatewayv2", "create-api", "--name", "orders", "--protocol-type", "HTTP", "--description", "d1",
		"--cors-configuration", "AllowOrigins=https://a.example,AllowMethods=GET", "--tags", "team=x")
	id := api["ApiId"].(string)
	if api["Name"] != "orders" || api["ProtocolType"] != "HTTP" || api["Description"] != "d1" || api["Tags"].(map[string]any)["team"] != "x" ||
		!strings.Contains(api["ApiEndpoint"].(string), "/apigw/"+id) || api["RouteSelectionExpression"] != "$request.method $request.path" {
		t.Fatalf("create-api: %v", api)
	}
	endpoint := api["ApiEndpoint"].(string)
	if got := h.AWSJSON(t, "apigatewayv2", "get-api", "--api-id", id); got["ApiId"] != id || got["CorsConfiguration"].(map[string]any)["AllowOrigins"].([]any)[0] != "https://a.example" {
		t.Fatalf("get-api: %v", got)
	}
	if got := arr(h.AWSJSON(t, "apigatewayv2", "get-apis"), "Items"); len(got) != 1 {
		t.Fatalf("get-apis: %v", got)
	}
	if up := h.AWSJSON(t, "apigatewayv2", "update-api", "--api-id", id, "--name", "orders2", "--description", "d2"); up["Name"] != "orders2" || up["Description"] != "d2" {
		t.Fatalf("update-api: %v", up)
	}
	if out, err := h.AWSErr(t, "apigatewayv2", "get-api", "--api-id", "nope"); err == nil || !strings.Contains(out, "NotFoundException") {
		t.Fatalf("missing API: %s", out)
	}

	// Without a stage there is nothing to call.
	if code, _ := gwCall(t, h, endpoint, "GET", "/things/1", nil); code != 404 {
		t.Fatalf("no stage: %d", code)
	}

	integ := h.AWSJSON(t, "apigatewayv2", "create-integration", "--api-id", id, "--integration-type", "HTTP_PROXY",
		"--integration-uri", backend.URL+"/base/{id}", "--integration-method", "ANY", "--payload-format-version", "1.0")
	iid := integ["IntegrationId"].(string)
	if integ["IntegrationType"] != "HTTP_PROXY" || integ["IntegrationUri"] != backend.URL+"/base/{id}" || integ["TimeoutInMillis"].(float64) != 30000 {
		t.Fatalf("create-integration: %v", integ)
	}
	if out, err := h.AWSErr(t, "apigatewayv2", "create-integration", "--api-id", id, "--integration-type", "MOCK"); err == nil || !strings.Contains(out, "BadRequestException") {
		t.Fatalf("MOCK integrations are not supported: %s", out)
	}
	if got := arr(h.AWSJSON(t, "apigatewayv2", "get-integrations", "--api-id", id), "Items"); len(got) != 1 {
		t.Fatalf("get-integrations: %v", got)
	}
	if up := h.AWSJSON(t, "apigatewayv2", "update-integration", "--api-id", id, "--integration-id", iid, "--description", "proxy"); up["Description"] != "proxy" || up["IntegrationType"] != "HTTP_PROXY" {
		t.Fatalf("update-integration: %v", up)
	}

	route := h.AWSJSON(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "GET /things/{id}", "--target", "integrations/"+iid)
	rid := route["RouteId"].(string)
	if route["RouteKey"] != "GET /things/{id}" || route["Target"] != "integrations/"+iid || route["AuthorizationType"] != "NONE" {
		t.Fatalf("create-route: %v", route)
	}
	if out, err := h.AWSErr(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "GET /things/{id}"); err == nil || !strings.Contains(out, "ConflictException") {
		t.Fatalf("duplicate route: %s", out)
	}
	if out, err := h.AWSErr(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "GET /x", "--target", "integrations/missing"); err == nil || !strings.Contains(out, "NotFoundException") {
		t.Fatalf("route to a missing integration: %s", out)
	}
	if out, err := h.AWSErr(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "nonsense"); err == nil || !strings.Contains(out, "BadRequestException") {
		t.Fatalf("bad route key: %s", out)
	}
	h.AWS(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "$default", "--target", "integrations/"+iid)
	if got := arr(h.AWSJSON(t, "apigatewayv2", "get-routes", "--api-id", id), "Items"); len(got) != 2 {
		t.Fatalf("get-routes: %v", got)
	}
	if up := h.AWSJSON(t, "apigatewayv2", "update-route", "--api-id", id, "--route-id", rid, "--route-key", "ANY /things/{id}", "--operation-name", "getThing"); up["RouteKey"] != "ANY /things/{id}" || up["OperationName"] != "getThing" {
		t.Fatalf("update-route: %v", up)
	}
	if got := h.AWSJSON(t, "apigatewayv2", "get-route", "--api-id", id, "--route-id", rid); got["RouteId"] != rid || got["OperationName"] != "getThing" {
		t.Fatalf("get-route: %v", got)
	}

	// Stages: $default is served at the root, named stages under their name.
	st := h.AWSJSON(t, "apigatewayv2", "create-stage", "--api-id", id, "--stage-name", "$default", "--auto-deploy")
	if st["StageName"] != "$default" || st["AutoDeploy"] != true || st["DeploymentId"] == nil {
		t.Fatalf("create-stage: %v", st)
	}
	if out, err := h.AWSErr(t, "apigatewayv2", "create-stage", "--api-id", id, "--stage-name", "$default"); err == nil || !strings.Contains(out, "ConflictException") {
		t.Fatalf("duplicate stage: %s", out)
	}
	h.AWS(t, "apigatewayv2", "create-stage", "--api-id", id, "--stage-name", "prod", "--stage-variables", "k=v")
	if got := arr(h.AWSJSON(t, "apigatewayv2", "get-stages", "--api-id", id), "Items"); len(got) != 2 {
		t.Fatalf("get-stages: %v", got)
	}
	if got := h.AWSJSON(t, "apigatewayv2", "get-stage", "--api-id", id, "--stage-name", "prod"); got["StageVariables"].(map[string]any)["k"] != "v" {
		t.Fatalf("get-stage: %v", got)
	}
	if up := h.AWSJSON(t, "apigatewayv2", "update-stage", "--api-id", id, "--stage-name", "prod", "--description", "live"); up["Description"] != "live" {
		t.Fatalf("update-stage: %v", up)
	}
	code, body := gwCall(t, h, endpoint, "GET", "/things/7?x=1", map[string]string{"X-In": "1"})
	if code != http.StatusTeapot || body != "GET /base/7?x=1" {
		t.Fatalf("$default stage through the proxy: %d %q", code, body)
	}
	if code, body = gwCall(t, h, endpoint, "GET", "/prod/things/8", nil); code != http.StatusTeapot || body != "GET /base/8" {
		t.Fatalf("named stage: %d %q", code, body)
	}
	if code, body = gwCall(t, h, endpoint, "POST", "/other", nil); code != http.StatusTeapot || !strings.HasPrefix(body, "POST ") {
		t.Fatalf("$default route: %d %q", code, body)
	}

	// Deployments.
	dep := h.AWSJSON(t, "apigatewayv2", "create-deployment", "--api-id", id, "--stage-name", "prod", "--description", "v1")
	if dep["DeploymentStatus"] != "DEPLOYED" || dep["Description"] != "v1" {
		t.Fatalf("create-deployment: %v", dep)
	}
	if got := h.AWSJSON(t, "apigatewayv2", "get-stage", "--api-id", id, "--stage-name", "prod"); got["DeploymentId"] != dep["DeploymentId"] {
		t.Fatalf("the stage points at the deployment: %v", got)
	}
	if got := arr(h.AWSJSON(t, "apigatewayv2", "get-deployments", "--api-id", id), "Items"); len(got) < 2 {
		t.Fatalf("get-deployments (auto-deploys count): %v", got)
	}
	before := h.AWSJSON(t, "apigatewayv2", "get-stage", "--api-id", id, "--stage-name", "$default")["DeploymentId"]
	h.AWS(t, "apigatewayv2", "update-route", "--api-id", id, "--route-id", rid, "--operation-name", "again")
	if after := h.AWSJSON(t, "apigatewayv2", "get-stage", "--api-id", id, "--stage-name", "$default")["DeploymentId"]; after == before {
		t.Fatalf("a change must auto-deploy the $default stage")
	}

	// JWT authorizers.
	if out, err := h.AWSErr(t, "apigatewayv2", "create-authorizer", "--api-id", id, "--name", "bad", "--authorizer-type", "REQUEST",
		"--identity-source", "$request.header.Authorization"); err == nil || !strings.Contains(out, "BadRequestException") {
		t.Fatalf("only JWT authorizers: %s", out)
	}
	az := h.AWSJSON(t, "apigatewayv2", "create-authorizer", "--api-id", id, "--name", "jwt", "--authorizer-type", "JWT",
		"--identity-source", "$request.header.Authorization", "--jwt-configuration", "Audience=cl1,Issuer=https://cognito-idp.us-east-1.amazonaws.com/us-east-1_abc")
	aid := az["AuthorizerId"].(string)
	if az["AuthorizerType"] != "JWT" || az["JwtConfiguration"].(map[string]any)["Issuer"] == nil {
		t.Fatalf("create-authorizer: %v", az)
	}
	if got := arr(h.AWSJSON(t, "apigatewayv2", "get-authorizers", "--api-id", id), "Items"); len(got) != 1 {
		t.Fatalf("get-authorizers: %v", got)
	}
	prot := h.AWSJSON(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "GET /secure", "--target", "integrations/"+iid,
		"--authorization-type", "JWT", "--authorizer-id", aid)
	if prot["AuthorizationType"] != "JWT" || prot["AuthorizerId"] != aid {
		t.Fatalf("JWT route: %v", prot)
	}
	if code, _ = gwCall(t, h, endpoint, "GET", "/secure", nil); code != 401 {
		t.Fatalf("a JWT route without a token: %d", code)
	}
	if out, err := h.AWSErr(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "GET /iam", "--authorization-type", "AWS_IAM"); err == nil || !strings.Contains(out, "BadRequestException") {
		t.Fatalf("AWS_IAM routes are not supported: %s", out)
	}
	h.AWS(t, "apigatewayv2", "delete-route", "--api-id", id, "--route-id", prot["RouteId"].(string))
	h.AWS(t, "apigatewayv2", "delete-authorizer", "--api-id", id, "--authorizer-id", aid)
	if got := arr(h.AWSJSON(t, "apigatewayv2", "get-authorizers", "--api-id", id), "Items"); len(got) != 0 {
		t.Fatalf("delete-authorizer: %v", got)
	}

	// Tags on the API and on a stage.
	arn := "arn:aws:apigateway:us-east-1::/apis/" + id
	h.AWS(t, "apigatewayv2", "tag-resource", "--resource-arn", arn, "--tags", "env=dev,team=y")
	if got := h.AWSJSON(t, "apigatewayv2", "get-tags", "--resource-arn", arn)["Tags"].(map[string]any); got["env"] != "dev" || got["team"] != "y" {
		t.Fatalf("get-tags: %v", got)
	}
	h.AWS(t, "apigatewayv2", "untag-resource", "--resource-arn", arn, "--tag-keys", "env")
	if got := h.AWSJSON(t, "apigatewayv2", "get-tags", "--resource-arn", arn)["Tags"].(map[string]any); got["env"] != nil || got["team"] != "y" {
		t.Fatalf("untag-resource: %v", got)
	}
	h.AWS(t, "apigatewayv2", "tag-resource", "--resource-arn", arn+"/stages/prod", "--tags", "s=1")
	if got := h.AWSJSON(t, "apigatewayv2", "get-stage", "--api-id", id, "--stage-name", "prod")["Tags"].(map[string]any); got["s"] != "1" {
		t.Fatalf("stage tags: %v", got)
	}

	// Cleanup in every order.
	h.AWS(t, "apigatewayv2", "delete-stage", "--api-id", id, "--stage-name", "prod")
	if out, err := h.AWSErr(t, "apigatewayv2", "get-stage", "--api-id", id, "--stage-name", "prod"); err == nil || !strings.Contains(out, "NotFoundException") {
		t.Fatalf("deleted stage: %s", out)
	}
	h.AWS(t, "apigatewayv2", "delete-route", "--api-id", id, "--route-id", rid)
	h.AWS(t, "apigatewayv2", "delete-integration", "--api-id", id, "--integration-id", iid)
	h.AWS(t, "apigatewayv2", "delete-api", "--api-id", id)
	if got := arr(h.AWSJSON(t, "apigatewayv2", "get-apis"), "Items"); len(got) != 0 {
		t.Fatalf("delete-api: %v", got)
	}

	// Quick create with a target makes the integration, route and $default stage.
	q := h.AWSJSON(t, "apigatewayv2", "create-api", "--name", "quick", "--protocol-type", "HTTP", "--target", backend.URL+"/q")
	if code, body = gwCall(t, h, q["ApiEndpoint"].(string), "GET", "/anything", nil); code != http.StatusTeapot || body != "GET /q/anything" {
		t.Fatalf("quick create: %d %q", code, body)
	}
}

// TestAPIGatewayBoto3 checks the wire format with botocore's own parsers.
func TestAPIGatewayBoto3(t *testing.T) {
	h, _ := gwHarness(t, false)
	out := h.Python(t, `
c = boto3.client("apigatewayv2")
a = c.create_api(Name="b", ProtocolType="HTTP", Tags={"a": "b"})
i = c.create_integration(ApiId=a["ApiId"], IntegrationType="AWS_PROXY", IntegrationUri="arn:aws:lambda:us-east-1:123456789012:function:f", PayloadFormatVersion="2.0")
r = c.create_route(ApiId=a["ApiId"], RouteKey="ANY /{proxy+}", Target="integrations/" + i["IntegrationId"])
s = c.create_stage(ApiId=a["ApiId"], StageName="$default", AutoDeploy=True)
assert c.get_routes(ApiId=a["ApiId"])["Items"][0]["RouteId"] == r["RouteId"]
assert c.get_integration(ApiId=a["ApiId"], IntegrationId=i["IntegrationId"])["PayloadFormatVersion"] == "2.0"
assert c.get_stage(ApiId=a["ApiId"], StageName="$default")["AutoDeploy"] is True
assert len(c.get_deployments(ApiId=a["ApiId"])["Items"]) >= 1
arn = "arn:aws:apigateway:us-east-1::/apis/" + a["ApiId"]
c.tag_resource(ResourceArn=arn, Tags={"x": "y"})
assert c.get_tags(ResourceArn=arn)["Tags"] == {"a": "b", "x": "y"}
try:
    c.get_route(ApiId=a["ApiId"], RouteId="zzz")
    raise SystemExit("expected NotFoundException")
except c.exceptions.NotFoundException:
    pass
assert c.get_apis()["Items"][0]["ApiEndpoint"].startswith("http")
c.delete_api(ApiId=a["ApiId"])
print("ok")
`)
	if !strings.Contains(out, "ok") {
		t.Fatalf("boto3: %s", out)
	}
}

// TestAPIGatewayIAM checks every operation is authorized as apigateway:<verb> on the API's ARN.
func TestAPIGatewayIAM(t *testing.T) {
	h, _ := gwHarness(t, false)
	api := h.AWSJSON(t, "apigatewayv2", "create-api", "--name", "a", "--protocol-type", "HTTP")
	id := api["ApiId"].(string)
	h.AWS(t, "iam", "create-policy", "--policy-name", "gw-read", "--policy-document", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"apigateway:GET","Resource":"*"}]}`)
	h.AWS(t, "iam", "create-policy", "--policy-name", "gw-one", "--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"apigateway:*","Resource":"arn:aws:apigateway:us-east-1::/apis/`+id+`*"}]}`)
	rd, rs := h.User(t, "gw-reader", "gw-read")
	if _, err := h.AWSAs(t, rd, rs, "", "apigatewayv2", "get-api", "--api-id", id); err != nil {
		t.Fatalf("read allowed: %v", err)
	}
	for _, args := range [][]string{
		{"create-api", "--name", "x", "--protocol-type", "HTTP"},
		{"update-api", "--api-id", id, "--name", "z"},
		{"delete-api", "--api-id", id},
		{"create-stage", "--api-id", id, "--stage-name", "s"},
		{"create-route", "--api-id", id, "--route-key", "GET /x"},
		{"tag-resource", "--resource-arn", "arn:aws:apigateway:us-east-1::/apis/" + id, "--tags", "a=b"},
	} {
		if out, err := h.AWSAs(t, rd, rs, "", append([]string{"apigatewayv2"}, args...)...); err == nil || !strings.Contains(out, "AccessDeniedException") {
			t.Fatalf("%v must be denied to a read-only user: %s", args, out)
		}
	}
	// A user scoped to one API can manage it, and only it.
	od, os := h.User(t, "gw-one", "gw-one")
	if _, err := h.AWSAs(t, od, os, "", "apigatewayv2", "create-stage", "--api-id", id, "--stage-name", "s"); err != nil {
		t.Fatalf("scoped write: %v", err)
	}
	other := h.AWSJSON(t, "apigatewayv2", "create-api", "--name", "b", "--protocol-type", "HTTP")["ApiId"].(string)
	if out, err := h.AWSAs(t, od, os, "", "apigatewayv2", "create-stage", "--api-id", other, "--stage-name", "s"); err == nil || !strings.Contains(out, "AccessDeniedException") {
		t.Fatalf("another API: %s", out)
	}
	found := false
	for _, e := range h.AuditLog() {
		if e == "apigateway:POST arn:aws:apigateway:us-east-1::/apis/"+id+"/stages" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit log: %v", h.AuditLog())
	}
	// Native and AWS APIs are one set: the native route sees the AWS-created API.
	var list []map[string]any
	if err := json.Unmarshal(h.Native(t, "GET", "/api/v1/apigateway/apis", nil), &list); err != nil || len(list) != 2 {
		t.Fatalf("native list: %v %v", err, list)
	}
}

const gwHandlerCode = `import json
def handler(event, context):
    return {"statusCode": 201, "headers": {"content-type": "application/json", "x-fn": "1"},
            "body": json.dumps({"version": event["version"], "routeKey": event.get("routeKey"), "rawPath": event.get("rawPath") or event["path"],
                                "stage": event["requestContext"]["stage"], "id": (event.get("pathParameters") or {}).get("id")})}
`

// TestAPIGatewayEndToEnd creates a function, an HTTP API, integration, route and
// $default stage with the AWS CLI, and calls the endpoint. It needs Docker.
func TestAPIGatewayEndToEnd(t *testing.T) {
	h, _ := gwHarness(t, true)
	acct := h.Env.AccountID
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "fn-role", "assume_role_policy": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}}}})
	code := zipFile(t, "app.py", gwHandlerCode)
	fn := h.AWSJSON(t, "lambda", "create-function", "--function-name", "web", "--runtime", "python3.12", "--handler", "app.handler",
		"--role", "arn:aws:iam::"+acct+":role/fn-role", "--zip-file", "fileb://"+code)
	h.AWS(t, "lambda", "wait", "function-active-v2", "--function-name", "web")
	fnArn := fn["FunctionArn"].(string)

	api := h.AWSJSON(t, "apigatewayv2", "create-api", "--name", "e2e", "--protocol-type", "HTTP")
	id := api["ApiId"].(string)
	integ := h.AWSJSON(t, "apigatewayv2", "create-integration", "--api-id", id, "--integration-type", "AWS_PROXY",
		"--integration-uri", fnArn, "--payload-format-version", "2.0")["IntegrationId"].(string)
	h.AWS(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "GET /items/{id}", "--target", "integrations/"+integ)
	h.AWS(t, "apigatewayv2", "create-stage", "--api-id", id, "--stage-name", "$default", "--auto-deploy")
	h.AWS(t, "apigatewayv2", "create-stage", "--api-id", id, "--stage-name", "v1", "--auto-deploy")
	endpoint := api["ApiEndpoint"].(string)

	// AWS refuses the call until the function's resource policy allows API Gateway.
	if status, body := gwCall(t, h, endpoint, "GET", "/items/1", nil); status != 500 || !strings.Contains(body, "Internal Server Error") {
		t.Fatalf("no permission: %d %s", status, body)
	}
	// A permission for another API does not help.
	h.AWS(t, "lambda", "add-permission", "--function-name", "web", "--statement-id", "other", "--action", "lambda:InvokeFunction",
		"--principal", "apigateway.amazonaws.com", "--source-arn", "arn:aws:execute-api:us-east-1:"+acct+":zzzzzzzzzz/*/*/*")
	if status, _ := gwCall(t, h, endpoint, "GET", "/items/1", nil); status != 500 {
		t.Fatalf("permission for another API: %d", status)
	}
	h.AWS(t, "lambda", "add-permission", "--function-name", "web", "--statement-id", "apigw", "--action", "lambda:InvokeFunction",
		"--principal", "apigateway.amazonaws.com", "--source-arn", "arn:aws:execute-api:us-east-1:"+acct+":"+id+"/*/*/items/*")

	status, body := gwCall(t, h, endpoint, "GET", "/items/42", nil)
	if status != 201 {
		t.Fatalf("invoke: %d %s", status, body)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err, body)
	}
	if got["version"] != "2.0" || got["routeKey"] != "GET /items/{id}" || got["rawPath"] != "/items/42" || got["stage"] != "$default" || got["id"] != "42" {
		t.Fatalf("event: %v", got)
	}
	if status, body = gwCall(t, h, endpoint, "GET", "/v1/items/9", nil); status != 201 || !strings.Contains(body, `"rawPath": "/v1/items/9"`) || !strings.Contains(body, `"stage": "v1"`) {
		t.Fatalf("named stage: %d %s", status, body)
	}
	if status, _ = gwCall(t, h, endpoint, "POST", "/items/1", nil); status != 404 {
		t.Fatalf("no route for POST: %d", status)
	}
	// Payload format 1.0 gets the REST-style event.
	i1 := h.AWSJSON(t, "apigatewayv2", "create-integration", "--api-id", id, "--integration-type", "AWS_PROXY",
		"--integration-uri", "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/"+fnArn+"/invocations", "--payload-format-version", "1.0")["IntegrationId"].(string)
	h.AWS(t, "apigatewayv2", "create-route", "--api-id", id, "--route-key", "GET /items/{id}/old", "--target", "integrations/"+i1)
	if status, body = gwCall(t, h, endpoint, "GET", "/items/5/old", nil); status != 201 || !strings.Contains(body, `"version": "1.0"`) {
		t.Fatalf("payload 1.0: %d %s", status, body)
	}
	// Routes made through the native API keep working without a resource policy.
	var natv struct {
		ID       string `json:"id"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(h.Native(t, "POST", "/api/v1/apigateway/apis", map[string]any{"name": "native",
		"routes": []any{map[string]any{"method": "GET", "path": "/n", "function_name": "web"}}}), &natv); err != nil {
		t.Fatal(err)
	}
	if status, body = gwCall(t, h, natv.Endpoint, "GET", "/n", nil); status != 201 || !strings.Contains(body, `"rawPath": "/n"`) {
		t.Fatalf("native API: %d %s", status, body)
	}
	// Removing the permission cuts the API off again.
	h.AWS(t, "lambda", "remove-permission", "--function-name", "web", "--statement-id", "apigw")
	if status, _ = gwCall(t, h, endpoint, "GET", "/items/42", nil); status != 500 {
		t.Fatalf("after remove-permission: %d", status)
	}
}
