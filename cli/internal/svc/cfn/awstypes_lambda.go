package cfn

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// Lambda and API Gateway (HTTP API) resources.

// fnRef reads a function name, or name:qualifier, from a name or a (qualified) ARN.
func fnRef(s string) (name, qual string) {
	if i := strings.Index(s, "function:"); i >= 0 {
		s = s[i+len("function:"):]
	}
	name, qual, _ = strings.Cut(s, ":")
	return name, qual
}

func fnPath(name string) string { return "/api/v1/lambda/functions/" + esc(name) }

func unsupported(prop string) error {
	return fmt.Errorf("Property %s is not supported by HomeCloud", prop)
}

// rejectProps fails on properties HomeCloud cannot express and that would
// silently change what the function does.
func rejectProps(in map[string]any, names ...string) error {
	for _, n := range names {
		if has(in, n) {
			return unsupported(n)
		}
	}
	return nil
}

// inlineFile names the file an inline ZipFile is written to.
func inlineFile(runtime, handler, source string) (string, error) {
	module := handler
	if i := strings.LastIndex(handler, "."); i >= 0 {
		module = handler[:i]
	}
	if module == "" {
		return "", fmt.Errorf("Property validation failure: [The property {/Handler} is required]")
	}
	switch {
	case strings.HasPrefix(runtime, "nodejs"):
		ext := ".js"
		for _, l := range strings.Split(source, "\n") {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "export ") || strings.HasPrefix(l, "import ") {
				ext = ".mjs"
				break
			}
		}
		return module + ext, nil
	case strings.HasPrefix(runtime, "python"):
		return strings.ReplaceAll(module, ".", "/") + ".py", nil
	case strings.HasPrefix(runtime, "ruby"):
		return module + ".rb", nil
	}
	return "", fmt.Errorf("Property Code.ZipFile is not supported by HomeCloud for runtime %s (only Node.js, Python and Ruby)", runtime)
}

// fetchS3Zip reads a code package from S3 as the caller.
func fetchS3Zip(x *xctx, code map[string]any) (string, error) {
	if err := req(code, "S3Bucket", "S3Key"); err != nil {
		return "", err
	}
	p := "/api/v1/s3/buckets/" + esc(sv(code, "S3Bucket")) + "/object?key=" + url.QueryEscape(sv(code, "S3Key"))
	if v := sv(code, "S3ObjectVersion"); v != "" {
		p += "&version_id=" + url.QueryEscape(v)
	}
	b, err := x.Fetch(p)
	if err != nil {
		return "", fmt.Errorf("Error occurred while GetObject. %v", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func lambdaFunctionProps(x *xctx, in map[string]any) (map[string]any, error) {
	if err := rejectProps(in, "FileSystemConfigs", "CodeSigningConfigArn"); err != nil {
		return nil, err
	}
	name := sv(in, "FunctionName")
	if name == "" {
		name = x.GenName(64, false)
	}
	out := map[string]any{"name": name}
	code := mv(in, "Code")
	if code == nil {
		return nil, req(in, "Code")
	}
	image := sv(code, "ImageUri")
	if image != "" || sv(in, "PackageType") == "Image" {
		if image == "" {
			return nil, fmt.Errorf("Property validation failure: [The property {/Code/ImageUri} is required]")
		}
		out["package_type"] = "Image"
		out["image_uri"] = image
		if ic := mv(in, "ImageConfig"); ic != nil {
			cfg := map[string]any{}
			if l := lv(ic, "EntryPoint"); l != nil {
				cfg["EntryPoint"] = strs(l)
			}
			if l := lv(ic, "Command"); l != nil {
				cfg["Command"] = strs(l)
			}
			if has(ic, "WorkingDirectory") {
				cfg["WorkingDirectory"] = sv(ic, "WorkingDirectory")
			}
			out["image_config"] = cfg
		}
	} else {
		if err := req(in, "Runtime", "Handler"); err != nil {
			return nil, err
		}
		out["runtime"], out["handler"] = sv(in, "Runtime"), sv(in, "Handler")
		switch {
		case has(code, "ZipFile"):
			src := sv(code, "ZipFile")
			file, err := inlineFile(sv(in, "Runtime"), sv(in, "Handler"), src)
			if err != nil {
				return nil, err
			}
			out["code"] = map[string]any{"files": map[string]string{file: src}}
		case has(code, "S3Bucket"):
			z, err := fetchS3Zip(x, code)
			if err != nil {
				return nil, err
			}
			out["code"] = map[string]any{"zip_base64": z}
		default:
			return nil, fmt.Errorf("Property validation failure: [The property {/Code} must specify ZipFile, S3Bucket/S3Key or ImageUri]")
		}
	}
	if has(in, "Role") {
		out["role"] = sv(in, "Role")
	}
	if has(in, "Description") {
		out["description"] = sv(in, "Description")
	}
	setInt(out, "timeout_seconds", in, "Timeout")
	setInt(out, "memory_mb", in, "MemorySize")
	if env := mv(mv(in, "Environment"), "Variables"); env != nil {
		vars := map[string]string{}
		for k, v := range env {
			vars[k] = toStr(v)
		}
		out["environment"] = vars
	}
	if l := lv(in, "Layers"); len(l) > 0 {
		out["layers"] = strs(l)
	}
	if l := lv(in, "Architectures"); len(l) > 0 {
		out["architectures"] = strs(l)
	}
	if t := tagMap(in["Tags"]); t != nil {
		out["tags"] = t
	}
	if vc := mv(in, "VpcConfig"); vc != nil {
		if ids := lv(vc, "SubnetIds"); len(ids) > 0 {
			out["subnet_id"] = toStr(ids[0])
		}
		if ids := lv(vc, "SecurityGroupIds"); len(ids) > 0 {
			out["security_group_ids"] = strs(ids)
		}
	}
	if dl := mv(in, "DeadLetterConfig"); dl != nil && has(dl, "TargetArn") {
		out["dead_letter_target"] = sv(dl, "TargetArn")
	}
	return out, nil
}

// urlOf reads a function URL config's function and qualifier from TargetFunctionArn and Qualifier.
func urlTarget(in map[string]any) (name, qual string) {
	name, qual = fnRef(sv(in, "TargetFunctionArn"))
	if q := sv(in, "Qualifier"); q != "" {
		qual = q
	}
	return
}

func init() {
	awsTypes["AWS::Lambda::Function"] = awsType{
		HC:    "HC::Lambda::Function",
		Props: lambdaFunctionProps,
		Post: func(x *xctx, id string, in, attrs map[string]any) error {
			if n, ok := iv(in, "ReservedConcurrentExecutions"); ok {
				_, err := x.Call("PUT", fnPath(id)+"/concurrency", map[string]any{"reserved_concurrent_executions": n})
				return err
			}
			return nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Arn":
				return firstNonEmpty(attrStr(v, "arn"), core.ARN(v.Account, "lambda", "function:"+v.ID)), true
			case "FunctionName":
				return v.ID, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::Lambda::Permission"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "FunctionName", "Action", "Principal"); err != nil {
				return "", nil, err
			}
			name, qual := fnRef(sv(in, "FunctionName"))
			sid := x.Stack + "-" + x.Logical + "-" + randID(12)
			body := map[string]any{"statement_id": sid, "action": sv(in, "Action"), "principal": sv(in, "Principal")}
			for k, n := range map[string]string{"SourceArn": "source_arn", "SourceAccount": "source_account", "EventSourceToken": "event_source_token",
				"PrincipalOrgID": "principal_org_id", "FunctionUrlAuthType": "function_url_auth_type"} {
				if has(in, k) {
					body[n] = sv(in, k)
				}
			}
			p := fnPath(name) + "/permissions"
			if qual != "" {
				p += "?qualifier=" + url.QueryEscape(qual)
			}
			if _, err := x.Call("POST", p, body); err != nil {
				return "", nil, err
			}
			return sid, map[string]any{"function": name, "qualifier": qual}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			p := fnPath(sv(r.Attributes, "function")) + "/permissions/" + esc(r.native())
			if q := sv(r.Attributes, "qualifier"); q != "" {
				p += "?qualifier=" + url.QueryEscape(q)
			}
			_, err := x.Call("DELETE", p, nil)
			return err
		},
	}

	awsTypes["AWS::Lambda::Url"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "TargetFunctionArn", "AuthType"); err != nil {
				return "", nil, err
			}
			if m := sv(in, "InvokeMode"); m != "" && m != "BUFFERED" {
				return "", nil, unsupported("InvokeMode")
			}
			name, qual := urlTarget(in)
			if qual != "" {
				return "", nil, unsupported("Qualifier")
			}
			if cur, err := x.Call("GET", fnPath(name), nil); err == nil {
				curm, _ := cur.(map[string]any)
				if cm := mv(curm, "configuration"); bv(mv(cm, "function_url"), "enabled") {
					return "", nil, fmt.Errorf("Function URL config for %s already exists", name)
				}
			}
			body := map[string]any{"enabled": true, "auth_type": sv(in, "AuthType")}
			if c := mv(in, "Cors"); c != nil {
				cors := map[string]any{}
				for _, k := range []string{"AllowHeaders", "AllowMethods", "AllowOrigins", "ExposeHeaders"} {
					if l := lv(c, k); l != nil {
						cors[k] = strs(l)
					}
				}
				if has(c, "AllowCredentials") {
					cors["AllowCredentials"] = bv(c, "AllowCredentials")
				}
				setInt(cors, "MaxAge", c, "MaxAge")
				body["cors"] = cors
			}
			out, err := x.Call("PUT", fnPath(name)+"/url", body)
			if err != nil {
				return "", nil, err
			}
			f, _ := out.(map[string]any)
			u, _ := f["function_url"].(map[string]any)
			arn := firstNonEmpty(sv(f, "arn"), core.ARN(x.Account, "lambda", "function:"+name))
			return arn, map[string]any{"function": name, "function_url": sv(u, "url"), "arn": arn}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("PUT", fnPath(sv(r.Attributes, "function"))+"/url", map[string]any{"enabled": false})
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "FunctionUrl":
				return attrStr(v, "function_url"), true
			case "FunctionArn":
				return attrStr(v, "arn"), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::Lambda::EventSourceMapping"] = awsType{
		HC: "HC::Lambda::EventSourceMapping",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "FunctionName"); err != nil {
				return nil, err
			}
			if err := rejectProps(in, "FilterCriteria", "Topics", "Queues", "SelfManagedEventSource", "AmazonManagedKafkaConfiguration",
				"SelfManagedKafkaConfiguration", "SourceAccessConfigurations", "ParallelizationFactor", "TumblingWindowInSeconds"); err != nil {
				return nil, err
			}
			if err := req(in, "EventSourceArn"); err != nil {
				return nil, err
			}
			fn, q := fnRef(sv(in, "FunctionName"))
			if q != "" {
				fn += ":" + q
			}
			out := map[string]any{"function_name": fn, "event_source_arn": sv(in, "EventSourceArn")}
			setInt(out, "batch_size", in, "BatchSize")
			setInt(out, "batching_window_seconds", in, "MaximumBatchingWindowInSeconds")
			if has(in, "Enabled") {
				out["enabled"] = bv(in, "Enabled")
			}
			if l := lv(in, "FunctionResponseTypes"); len(l) > 0 {
				out["function_response_types"] = strs(l)
			}
			if has(in, "StartingPosition") {
				out["starting_position"] = sv(in, "StartingPosition")
			}
			if n, ok := iv(in, "MaximumRetryAttempts"); ok && n >= 0 {
				out["maximum_retry_attempts"] = n
			}
			if has(in, "BisectBatchOnFunctionError") {
				out["bisect_batch_on_function_error"] = bv(in, "BisectBatchOnFunctionError")
			}
			if of := mv(mv(in, "DestinationConfig"), "OnFailure"); of != nil {
				out["on_failure"] = sv(of, "Destination")
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Id":
				return v.ID, true
			case "EventSourceMappingArn":
				return core.ARN(v.Account, "lambda", "event-source-mapping:"+v.ID), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::Lambda::Version"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "FunctionName"); err != nil {
				return "", nil, err
			}
			if err := rejectProps(in, "ProvisionedConcurrencyConfig", "RuntimePolicy"); err != nil {
				return "", nil, err
			}
			name, _ := fnRef(sv(in, "FunctionName"))
			body := map[string]any{}
			if has(in, "Description") {
				body["description"] = sv(in, "Description")
			}
			if has(in, "CodeSha256") {
				body["code_sha256"] = sv(in, "CodeSha256")
			}
			out, err := x.Call("POST", fnPath(name)+"/versions", body)
			if err != nil {
				return "", nil, err
			}
			f, _ := out.(map[string]any)
			ver := sv(f, "version")
			arn := core.ARN(x.Account, "lambda", "function:"+name+":"+ver)
			return arn, map[string]any{"function": name, "version": ver, "arn": arn, "function_arn": core.ARN(x.Account, "lambda", "function:"+name)}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", fnPath(sv(r.Attributes, "function"))+"?qualifier="+url.QueryEscape(sv(r.Attributes, "version")), nil)
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Version":
				return attrStr(v, "version"), true
			case "FunctionArn":
				return v.ID, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::Lambda::Alias"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "FunctionName", "FunctionVersion", "Name"); err != nil {
				return "", nil, err
			}
			if err := rejectProps(in, "ProvisionedConcurrencyConfig"); err != nil {
				return "", nil, err
			}
			name, _ := fnRef(sv(in, "FunctionName"))
			ver := sv(in, "FunctionVersion")
			if i := strings.LastIndex(ver, ":"); i >= 0 {
				ver = ver[i+1:]
			}
			body := map[string]any{"name": sv(in, "Name"), "function_version": ver}
			if has(in, "Description") {
				body["description"] = sv(in, "Description")
			}
			if rc := mv(in, "RoutingConfig"); rc != nil {
				w := map[string]float64{}
				for _, e := range lv(rc, "AdditionalVersionWeights") {
					m, _ := e.(map[string]any)
					f, _ := num(m["FunctionWeight"])
					w[sv(m, "FunctionVersion")] = f
				}
				body["additional_version_weights"] = w
			}
			if _, err := x.Call("POST", fnPath(name)+"/aliases", body); err != nil {
				return "", nil, err
			}
			arn := core.ARN(x.Account, "lambda", "function:"+name+":"+sv(in, "Name"))
			return arn, map[string]any{"function": name, "alias": sv(in, "Name")}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", fnPath(sv(r.Attributes, "function"))+"/aliases/"+esc(sv(r.Attributes, "alias")), nil)
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "AliasArn" {
				return v.ID, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::Lambda::LayerVersion"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "Content"); err != nil {
				return "", nil, err
			}
			name := sv(in, "LayerName")
			if name == "" {
				name = x.GenName(64, false)
			}
			z, err := fetchS3Zip(x, mv(in, "Content"))
			if err != nil {
				return "", nil, err
			}
			body := map[string]any{"name": name, "zip_base64": z}
			for k, n := range map[string]string{"Description": "description", "LicenseInfo": "license_info"} {
				if has(in, k) {
					body[n] = sv(in, k)
				}
			}
			if l := lv(in, "CompatibleRuntimes"); len(l) > 0 {
				body["compatible_runtimes"] = strs(l)
			}
			if l := lv(in, "CompatibleArchitectures"); len(l) > 0 {
				body["compatible_architectures"] = strs(l)
			}
			out, err := x.Call("POST", "/api/v1/lambda/layers", body)
			if err != nil {
				return "", nil, err
			}
			lvm, _ := out.(map[string]any)
			n, _ := iv(lvm, "version")
			arn := firstNonEmpty(sv(lvm, "arn"), core.ARN(x.Account, "lambda", fmt.Sprintf("layer:%s:%d", name, n)))
			return arn, map[string]any{"layer": name, "version": n}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			n, _ := iv(r.Attributes, "version")
			_, err := x.Call("DELETE", fmt.Sprintf("/api/v1/lambda/layers/%s/versions/%d", esc(sv(r.Attributes, "layer")), n), nil)
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "LayerVersionArn" {
				return v.ID, true
			}
			return nil, false
		},
	}

	initAPIGatewayV2()
}

// ---- API Gateway HTTP APIs ----
//
// HomeCloud's native API model is an API with routes that invoke functions.
// Integrations, stages and deployments have no native records of their own:
// an Integration's ID carries the function it invokes (<random>.<function>),
// which a Route's Target then resolves; Stages and Deployments are accepted
// and change nothing (an API serves at /apigw/<id>/ without a stage).

const gwAPI = "/api/v1/apigateway/apis/"

// lambdaTargetOf reads the function from a Lambda proxy IntegrationUri (a
// function ARN or the API Gateway invocation ARN wrapping it).
func lambdaTargetOf(uri string) (string, error) {
	i := strings.Index(uri, ":function:")
	if i < 0 {
		return "", fmt.Errorf("Invalid integration URI: %s (HomeCloud supports Lambda functions only)", uri)
	}
	ref := uri[i+len(":function:"):]
	ref, _, _ = strings.Cut(ref, "/")
	name, qual := fnRef(ref)
	if qual != "" {
		name += ":" + qual
	}
	return name, nil
}

func gwCheckAPI(x *xctx, id string) error {
	_, err := x.Call("GET", gwAPI+esc(id), nil)
	return err
}

func gwDeleteNoop(x *xctx, r *Resource) error { return nil }

func initAPIGatewayV2() {
	awsTypes["AWS::ApiGatewayV2::Api"] = awsType{
		HC: "HC::ApiGateway::Api",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if p := sv(in, "ProtocolType"); p != "" && p != "HTTP" {
				return nil, unsupported("ProtocolType " + p)
			}
			if err := rejectProps(in, "Body", "BodyS3Location", "Target", "RouteKey", "CredentialsArn"); err != nil {
				return nil, err
			}
			name := sv(in, "Name")
			if name == "" {
				return nil, req(in, "Name")
			}
			out := map[string]any{"name": name}
			if has(in, "Description") {
				out["description"] = sv(in, "Description")
			}
			if has(in, "CorsConfiguration") {
				out["cors"] = true
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "ApiId":
				return v.ID, true
			case "ApiEndpoint":
				return attrStr(v, "endpoint"), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::ApiGatewayV2::Integration"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "ApiId", "IntegrationType"); err != nil {
				return "", nil, err
			}
			if t := sv(in, "IntegrationType"); t != "AWS_PROXY" {
				return "", nil, unsupported("IntegrationType " + t + " (only AWS_PROXY to Lambda)")
			}
			if v := sv(in, "PayloadFormatVersion"); v != "" && v != "2.0" {
				return "", nil, unsupported("PayloadFormatVersion " + v)
			}
			if err := req(in, "IntegrationUri"); err != nil {
				return "", nil, err
			}
			fn, err := lambdaTargetOf(sv(in, "IntegrationUri"))
			if err != nil {
				return "", nil, err
			}
			if err := gwCheckAPI(x, sv(in, "ApiId")); err != nil {
				return "", nil, err
			}
			if _, err := x.Call("GET", fnPath(fn), nil); err != nil {
				return "", nil, err
			}
			return strings.ToLower(randID(8)) + "." + fn, map[string]any{"api_id": sv(in, "ApiId"), "function": fn}, nil
		},
		Delete: gwDeleteNoop,
		Att: func(v *attrView, n string) (any, bool) {
			if n == "IntegrationId" {
				return v.ID, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::ApiGatewayV2::Route"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "ApiId", "RouteKey"); err != nil {
				return "", nil, err
			}
			if err := rejectProps(in, "ApiKeyRequired", "AuthorizationScopes", "ModelSelectionExpression", "OperationName", "RequestModels", "RequestParameters", "RouteResponseSelectionExpression"); err != nil {
				return "", nil, err
			}
			api := sv(in, "ApiId")
			method, p, ok := strings.Cut(sv(in, "RouteKey"), " ")
			if !ok {
				return "", nil, unsupported("RouteKey " + sv(in, "RouteKey") + " (a method and path are required; $default routes are not supported)")
			}
			_, integ, ok := strings.Cut(sv(in, "Target"), "integrations/")
			_, fn, hasFn := strings.Cut(integ, ".")
			if !ok || !hasFn || fn == "" {
				return "", nil, fmt.Errorf("Property Target must be integrations/<IntegrationId> of an AWS::ApiGatewayV2::Integration")
			}
			auth := "NONE"
			switch t := sv(in, "AuthorizationType"); t {
			case "", "NONE":
			case "JWT":
				auth = "JWT"
			default:
				return "", nil, unsupported("AuthorizationType " + t)
			}
			out, err := x.Call("POST", gwAPI+esc(api)+"/routes", map[string]any{"method": method, "path": p, "function_name": fn, "authorization": auth})
			if err != nil {
				return "", nil, err
			}
			a, _ := out.(map[string]any)
			for _, r := range lv(a, "routes") {
				rm, _ := r.(map[string]any)
				if strings.EqualFold(sv(rm, "method"), method) && sv(rm, "path") == p {
					return sv(rm, "id"), map[string]any{"api_id": api}, nil
				}
			}
			return "", nil, fmt.Errorf("the new route was not found in API %s", api)
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", gwAPI+esc(sv(r.Attributes, "api_id"))+"/routes/"+esc(r.native()), nil)
			return err
		},
	}

	// A JWT authorizer maps onto the API's Cognito user pool authorizer (one per API).
	awsTypes["AWS::ApiGatewayV2::Authorizer"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "ApiId", "AuthorizerType", "Name"); err != nil {
				return "", nil, err
			}
			if t := sv(in, "AuthorizerType"); t != "JWT" {
				return "", nil, unsupported("AuthorizerType " + t)
			}
			jc := mv(in, "JwtConfiguration")
			pool := path.Base(strings.TrimRight(sv(jc, "Issuer"), "/"))
			if pool == "" || pool == "." {
				return "", nil, fmt.Errorf("Property validation failure: [The property {/JwtConfiguration/Issuer} is required]")
			}
			az := map[string]any{"user_pool_id": pool}
			if aud := lv(jc, "Audience"); len(aud) > 0 {
				az["audience"] = toStr(aud[0])
			}
			api := sv(in, "ApiId")
			if _, err := x.Call("PATCH", gwAPI+esc(api), map[string]any{"authorizer": az}); err != nil {
				return "", nil, err
			}
			return strings.ToLower(randID(8)), map[string]any{"api_id": api}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("PATCH", gwAPI+esc(sv(r.Attributes, "api_id")), map[string]any{"authorizer": map[string]any{"user_pool_id": ""}})
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "AuthorizerId" {
				return v.ID, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::ApiGatewayV2::Stage"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "ApiId", "StageName"); err != nil {
				return "", nil, err
			}
			if err := gwCheckAPI(x, sv(in, "ApiId")); err != nil {
				return "", nil, err
			}
			return sv(in, "StageName"), map[string]any{"api_id": sv(in, "ApiId")}, nil
		},
		Delete: gwDeleteNoop,
	}

	awsTypes["AWS::ApiGatewayV2::Deployment"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "ApiId"); err != nil {
				return "", nil, err
			}
			if err := gwCheckAPI(x, sv(in, "ApiId")); err != nil {
				return "", nil, err
			}
			return strings.ToLower(randID(6)), map[string]any{"api_id": sv(in, "ApiId")}, nil
		},
		Delete: gwDeleteNoop,
		Att: func(v *attrView, n string) (any, bool) {
			if n == "DeploymentId" {
				return v.ID, true
			}
			return nil, false
		},
	}
}
