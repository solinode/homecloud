package lambda

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The API Gateway v2 (HTTP API) AWS wire protocol (restJson1, signing name
// "apigateway"). It drives the same API records the native routes and the
// serving code in apigw.go use: an API created here is a native API with
// ProtocolType set, plus integrations, stages, deployments and authorizers.

// doc is an AWS-shaped JSON document. Integrations, stages, deployments and
// authorizers are kept whole so Terraform reads back what it wrote.
type doc = map[string]any

func dStr(d doc, k string) string { v, _ := d[k].(string); return v }

func (s *Service) registerAPIGateway() {
	awsapi.Register(&awsapi.Service{
		Name: "apigateway",
		REST: s.serveAPIGateway,
		ErrorCode: map[string]string{
			"ResourceNotFound": "NotFoundException",
			"ResourceConflict": "ConflictException",
			"Conflict":         "ConflictException",
			"AlreadyExists":    "ConflictException",
			"ValidationError":  "BadRequestException",
			"BadRequest":       "BadRequestException",
			"TooManyRequests":  "TooManyRequestsException",
			"AccessDenied":     "AccessDeniedException",
			"InternalError":    "InternalServerErrorException",
		},
	})
}

func (s *Service) gwRoutes() []awsRoute {
	r := func(method, pattern, op string, h awsHandler) awsRoute {
		return awsRoute{method: method, op: op, path: strings.Split(strings.Trim(pattern, "/"), "/"), h: h}
	}
	return []awsRoute{
		r("POST", "/v2/apis", "CreateApi", s.gwCreateAPI),
		r("GET", "/v2/apis", "GetApis", s.gwGetAPIs),
		r("GET", "/v2/apis/{api}", "GetApi", s.gwGetAPI),
		r("PATCH", "/v2/apis/{api}", "UpdateApi", s.gwUpdateAPI),
		r("DELETE", "/v2/apis/{api}", "DeleteApi", s.gwDeleteAPI),
		r("POST", "/v2/apis/{api}/routes", "CreateRoute", s.gwCreateRoute),
		r("GET", "/v2/apis/{api}/routes", "GetRoutes", s.gwGetRoutes),
		r("GET", "/v2/apis/{api}/routes/{id}", "GetRoute", s.gwGetRoute),
		r("PATCH", "/v2/apis/{api}/routes/{id}", "UpdateRoute", s.gwUpdateRoute),
		r("DELETE", "/v2/apis/{api}/routes/{id}", "DeleteRoute", s.gwDeleteRoute),
		r("POST", "/v2/apis/{api}/integrations", "CreateIntegration", s.gwSubCreate(integrationSR)),
		r("GET", "/v2/apis/{api}/integrations", "GetIntegrations", s.gwSubList(integrationSR)),
		r("GET", "/v2/apis/{api}/integrations/{id}", "GetIntegration", s.gwSubGet(integrationSR)),
		r("PATCH", "/v2/apis/{api}/integrations/{id}", "UpdateIntegration", s.gwSubUpdate(integrationSR)),
		r("DELETE", "/v2/apis/{api}/integrations/{id}", "DeleteIntegration", s.gwSubDelete(integrationSR, nil)),
		r("POST", "/v2/apis/{api}/stages", "CreateStage", s.gwCreateStage),
		r("GET", "/v2/apis/{api}/stages", "GetStages", s.gwGetStages),
		r("GET", "/v2/apis/{api}/stages/{id}", "GetStage", s.gwGetStage),
		r("PATCH", "/v2/apis/{api}/stages/{id}", "UpdateStage", s.gwUpdateStage),
		r("DELETE", "/v2/apis/{api}/stages/{id}", "DeleteStage", s.gwDeleteStage),
		r("POST", "/v2/apis/{api}/deployments", "CreateDeployment", s.gwCreateDeployment),
		r("GET", "/v2/apis/{api}/deployments", "GetDeployments", s.gwGetDeployments),
		r("GET", "/v2/apis/{api}/deployments/{id}", "GetDeployment", s.gwGetDeployment),
		r("POST", "/v2/apis/{api}/authorizers", "CreateAuthorizer", s.gwSubCreate(authorizerSR)),
		r("GET", "/v2/apis/{api}/authorizers", "GetAuthorizers", s.gwSubList(authorizerSR)),
		r("GET", "/v2/apis/{api}/authorizers/{id}", "GetAuthorizer", s.gwSubGet(authorizerSR)),
		r("PATCH", "/v2/apis/{api}/authorizers/{id}", "UpdateAuthorizer", s.gwSubUpdate(authorizerSR)),
		r("DELETE", "/v2/apis/{api}/authorizers/{id}", "DeleteAuthorizer", s.gwSubDelete(authorizerSR, nil)),
		r("POST", "/v2/tags/{arn}", "TagResource", s.gwTag),
		r("DELETE", "/v2/tags/{arn}", "UntagResource", s.gwUntag),
		r("GET", "/v2/tags/{arn}", "GetTags", s.gwGetTags),
	}
}

func (s *Service) serveAPIGateway(q *awsapi.Req) {
	path := q.R.URL.EscapedPath()
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, sg := range segs {
		if u, err := url.PathUnescape(sg); err == nil {
			segs[i] = u
		}
	}
	methodOK := false
	for _, rt := range s.gwRoutes() {
		p, ok := matchRoute(rt.path, segs)
		if !ok {
			continue
		}
		methodOK = true
		if rt.method != q.R.Method {
			continue
		}
		q.Op = rt.op
		if err := rt.h(q, p); err != nil {
			q.Fail(err)
		}
		return
	}
	if methodOK {
		q.Fail(awsapi.Errorf(http.StatusMethodNotAllowed, "MethodNotAllowedException", "method %s is not allowed on %s", q.R.Method, q.R.URL.Path))
		return
	}
	q.Fail(awsapi.Errorf(http.StatusNotFound, "UnknownOperationException", "HomeCloud implements API Gateway HTTP APIs (apigatewayv2) only; %s %s is not supported", q.R.Method, q.R.URL.Path))
}

// ---- helpers ----

func gwARN(suffix string) string { return "arn:aws:apigateway:" + core.Region + "::" + suffix }

func apiRes(id string, more ...string) string {
	return gwARN(strings.Join(append([]string{"/apis/" + id}, more...), "/"))
}

func gwBind(q *awsapi.Req) (doc, error) {
	d := doc{}
	if len(strings.TrimSpace(string(q.Body))) == 0 {
		return d, nil
	}
	if err := json.Unmarshal(q.Body, &d); err != nil {
		return nil, awsapi.Errorf(http.StatusBadRequest, "BadRequestException", "Could not parse request body into json: %v", err)
	}
	return d, nil
}

func gwItems(q *awsapi.Req, items []doc) error {
	if items == nil {
		items = []doc{}
	}
	q.WriteJSON(http.StatusOK, doc{"items": items})
	return nil
}

func gwNotFound(kind, id string) error {
	return core.Errf(http.StatusNotFound, "ResourceNotFound", "%s with identifier %s not found", kind, id)
}

func gwTime() string { return core.Now().UTC().Format("2006-01-02T15:04:05Z") }

var gwStageRe = regexp.MustCompile(`^([a-zA-Z0-9_-]+|\$default)$`)

// gwAPI loads an API after authorizing the call against resource.
func (s *Service) gwAPI(q *awsapi.Req, p map[string]string, action string, more ...string) (API, error) {
	id := p["api"]
	if err := q.Authorize(action, apiRes(id, more...)); err != nil {
		return API{}, err
	}
	a, err := store.Get[API](s.env.Store, cAPIs, id)
	if err != nil {
		return a, gwNotFound("API", id)
	}
	return a, nil
}

// gwModify mutates an API. deploy re-deploys stages with auto-deploy on.
func (s *Service) gwModify(id string, deploy bool, fn func(*API) error) (API, error) {
	a, err := store.Update(s.env.Store, cAPIs, id, func(a *API) error {
		if err := fn(a); err != nil {
			return err
		}
		if deploy {
			autoDeploy(a)
		}
		return nil
	})
	if err == store.ErrNotFound {
		return a, gwNotFound("API", id)
	}
	return a, err
}

// autoDeploy creates a deployment for every stage that has AutoDeploy on.
func autoDeploy(a *API) {
	for _, st := range a.Stages {
		if b, _ := st["autoDeploy"].(bool); b {
			deploy(a, st, "", true)
		}
	}
}

func deploy(a *API, st doc, desc string, auto bool) doc {
	d := doc{"deploymentId": strings.ToLower(core.RandHex(5)), "createdDate": gwTime(), "deploymentStatus": "DEPLOYED", "autoDeployed": auto}
	if desc != "" {
		d["description"] = desc
	}
	a.Deployments = append(a.Deployments, d)
	if st != nil {
		st["deploymentId"] = d["deploymentId"]
		st["lastUpdatedDate"] = gwTime()
		if auto {
			st["lastDeploymentStatusMessage"] = "Automatic deployment triggered by changes to the Api configuration"
		}
	}
	return d
}

func (s *Service) apiDoc(a API) doc {
	d := doc{"apiId": a.ID, "name": a.Name, "protocolType": "HTTP", "apiEndpoint": s.apiEndpoint(a.ID),
		"routeSelectionExpression": "$request.method $request.path", "apiKeySelectionExpression": "$request.header.x-api-key",
		"createdDate": a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), "disableExecuteApiEndpoint": a.DisableExecuteAPI,
		"ipAddressType": "ipv4", "tags": map[string]string(nonNilTags(a.Tags))}
	if a.Description != "" {
		d["description"] = a.Description
	}
	if a.Version != "" {
		d["version"] = a.Version
	}
	if a.CorsConfig != nil {
		d["corsConfiguration"] = a.CorsConfig
	}
	return d
}

func nonNilTags(t core.Tags) core.Tags {
	if t == nil {
		return core.Tags{}
	}
	return t
}

func (s *Service) gwRespond(q *awsapi.Req, status int, v any) error {
	q.WriteJSON(status, v)
	return nil
}

func gwNoContent(q *awsapi.Req) error {
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- APIs ----

func (s *Service) gwCreateAPI(q *awsapi.Req, p map[string]string) error {
	if err := q.Authorize("apigateway:POST", gwARN("/apis")); err != nil {
		return err
	}
	in, err := gwBind(q)
	if err != nil {
		return err
	}
	name := dStr(in, "name")
	if strings.TrimSpace(name) == "" {
		return core.BadRequest("name is required")
	}
	if pt := dStr(in, "protocolType"); pt != "HTTP" {
		return core.BadRequest("protocolType must be HTTP: HomeCloud implements HTTP APIs only")
	}
	tags, err := gwTags(in["tags"])
	if err != nil {
		return err
	}
	id := strings.ToLower(core.RandHex(10))
	a := API{ID: id, Name: name, Description: dStr(in, "description"), Routes: []Route{}, Endpoint: s.apiEndpoint(id), CreatedAt: core.Now(),
		ProtocolType: "HTTP", Version: dStr(in, "version"), Tags: tags}
	a.DisableExecuteAPI, _ = in["disableExecuteApiEndpoint"].(bool)
	if c, ok := in["corsConfiguration"].(map[string]any); ok {
		a.CorsConfig = c
	}
	// Quick create: a target makes an integration, a route (default $default) and an auto-deployed $default stage.
	if target := dStr(in, "target"); target != "" {
		integ := doc{"integrationId": strings.ToLower(core.RandHex(7)), "integrationType": "HTTP_PROXY", "integrationUri": target,
			"integrationMethod": "ANY", "payloadFormatVersion": "1.0", "timeoutInMillis": 30000, "connectionType": "INTERNET"}
		if strings.HasPrefix(target, "arn:") {
			integ["integrationType"], integ["integrationMethod"], integ["payloadFormatVersion"] = "AWS_PROXY", "POST", "2.0"
		}
		if err := checkIntegration(integ); err != nil {
			return err
		}
		a.Integrations = []doc{integ}
		key := dStr(in, "routeKey")
		if key == "" {
			key = "$default"
		}
		r, err := parseRouteInput(&a, doc{"routeKey": key, "target": "integrations/" + integ["integrationId"].(string)}, nil)
		if err != nil {
			return err
		}
		a.Routes = append(a.Routes, r)
		st := newStage(doc{"stageName": "$default", "autoDeploy": true})
		a.Stages = []doc{st}
		deploy(&a, st, "", true)
	}
	if err := store.Put(s.env.Store, cAPIs, a.ID, a); err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusCreated, s.apiDoc(a))
}

func gwTags(v any) (core.Tags, error) {
	t := core.Tags{}
	m, _ := v.(map[string]any)
	for k, x := range m {
		sv, _ := x.(string)
		t[k] = sv
	}
	if len(t) > 50 {
		return nil, core.BadRequest("a resource can have at most 50 tags")
	}
	return t, nil
}

func (s *Service) gwGetAPIs(q *awsapi.Req, p map[string]string) error {
	if err := q.Authorize("apigateway:GET", gwARN("/apis")); err != nil {
		return err
	}
	items := []doc{}
	for _, a := range store.List[API](s.env.Store, cAPIs) {
		items = append(items, s.apiDoc(a))
	}
	return gwItems(q, items)
}

func (s *Service) gwGetAPI(q *awsapi.Req, p map[string]string) error {
	a, err := s.gwAPI(q, p, "apigateway:GET")
	if err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusOK, s.apiDoc(a))
}

func (s *Service) gwUpdateAPI(q *awsapi.Req, p map[string]string) error {
	if _, err := s.gwAPI(q, p, "apigateway:PATCH"); err != nil {
		return err
	}
	in, err := gwBind(q)
	if err != nil {
		return err
	}
	a, err := s.gwModify(p["api"], true, func(a *API) error {
		if v, ok := in["name"].(string); ok {
			if strings.TrimSpace(v) == "" {
				return core.BadRequest("name must not be empty")
			}
			a.Name = v
		}
		if v, ok := in["description"].(string); ok {
			a.Description = v
		}
		if v, ok := in["version"].(string); ok {
			a.Version = v
		}
		if v, ok := in["disableExecuteApiEndpoint"].(bool); ok {
			a.DisableExecuteAPI = v
		}
		if c, ok := in["corsConfiguration"].(map[string]any); ok {
			a.CorsConfig = c
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusOK, s.apiDoc(a))
}

func (s *Service) gwDeleteAPI(q *awsapi.Req, p map[string]string) error {
	if _, err := s.gwAPI(q, p, "apigateway:DELETE"); err != nil {
		return err
	}
	if err := store.Delete(s.env.Store, cAPIs, p["api"]); err != nil {
		return gwNotFound("API", p["api"])
	}
	return gwNoContent(q)
}

// ---- routes ----

var routeKnown = []string{"routeKey", "target", "authorizationType", "authorizerId", "authorizationScopes", "routeId"}

func (r Route) doc() doc {
	d := doc{}
	for k, v := range r.Extra {
		d[k] = v
	}
	d["routeId"], d["routeKey"] = r.ID, r.Key()
	d["authorizationType"] = orDefault(r.Authorization, "NONE")
	d["apiKeyRequired"] = false
	if v, ok := r.Extra["apiKeyRequired"]; ok {
		d["apiKeyRequired"] = v
	}
	if r.IntegrationID != "" {
		d["target"] = "integrations/" + r.IntegrationID
	}
	if r.AuthorizerID != "" {
		d["authorizerId"] = r.AuthorizerID
	}
	if len(r.Scopes) > 0 {
		d["authorizationScopes"] = r.Scopes
	}
	return d
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// parseRouteInput applies AWS route fields (a create, or with cur an update) to a route.
func parseRouteInput(a *API, in doc, cur *Route) (Route, error) {
	r := Route{Extra: map[string]any{}}
	if cur != nil {
		r = *cur
		r.Extra = map[string]any{}
		for k, v := range cur.Extra {
			r.Extra[k] = v
		}
	} else {
		r.ID = strings.ToLower(core.RandHex(7))
		r.Authorization = "NONE"
	}
	if k, ok := in["routeKey"].(string); ok {
		switch {
		case k == "$default":
			r.Method, r.Path = "ANY", "$default"
		default:
			m, path, found := strings.Cut(k, " ")
			m = strings.ToUpper(m)
			if !found || !methods[m] || !strings.HasPrefix(path, "/") {
				return r, core.BadRequest("routeKey must be $default or \"METHOD /path\": %q", k)
			}
			r.Method, r.Path = m, path
		}
	} else if cur == nil {
		return r, core.BadRequest("routeKey is required")
	}
	if t, ok := in["target"].(string); ok {
		id, found := strings.CutPrefix(t, "integrations/")
		if !found || !slices.ContainsFunc(a.Integrations, func(d doc) bool { return dStr(d, "integrationId") == id }) {
			return r, core.Errf(http.StatusNotFound, "ResourceNotFound", "Integration with identifier %s not found", strings.TrimPrefix(t, "integrations/"))
		}
		r.IntegrationID, r.FunctionName = id, ""
	}
	if t, ok := in["authorizationType"].(string); ok {
		r.Authorization = strings.ToUpper(t)
	}
	if t, ok := in["authorizerId"].(string); ok {
		r.AuthorizerID = t
	}
	if v, ok := in["authorizationScopes"].([]any); ok {
		r.Scopes = nil
		for _, x := range v {
			if sv, ok := x.(string); ok {
				r.Scopes = append(r.Scopes, sv)
			}
		}
	}
	switch r.Authorization {
	case "NONE":
		r.AuthorizerID = ""
	case "JWT":
		if r.AuthorizerID == "" {
			return r, core.BadRequest("authorizerId is required for authorizationType JWT")
		}
		if !slices.ContainsFunc(a.Authorizers, func(d doc) bool { return dStr(d, "authorizerId") == r.AuthorizerID }) {
			return r, core.Errf(http.StatusNotFound, "ResourceNotFound", "Authorizer with identifier %s not found", r.AuthorizerID)
		}
	default:
		return r, core.BadRequest("authorizationType %s is not supported: use NONE or JWT", r.Authorization)
	}
	for k, v := range in {
		if !slices.Contains(routeKnown, k) {
			r.Extra[k] = v
		}
	}
	for _, x := range a.Routes {
		if x.ID != r.ID && x.Key() == r.Key() {
			return r, core.Conflict("route %q already exists", r.Key())
		}
	}
	return r, nil
}

func (s *Service) gwCreateRoute(q *awsapi.Req, p map[string]string) error {
	a, err := s.gwAPI(q, p, "apigateway:POST", "routes")
	if err != nil {
		return err
	}
	in, err := gwBind(q)
	if err != nil {
		return err
	}
	if _, err := parseRouteInput(&a, in, nil); err != nil {
		return err
	}
	var out Route
	if _, err := s.gwModify(a.ID, true, func(a *API) error {
		r, err := parseRouteInput(a, in, nil)
		out = r
		a.Routes = append(a.Routes, r)
		return err
	}); err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusCreated, out.doc())
}

func (s *Service) gwGetRoutes(q *awsapi.Req, p map[string]string) error {
	a, err := s.gwAPI(q, p, "apigateway:GET", "routes")
	if err != nil {
		return err
	}
	items := []doc{}
	for _, r := range a.Routes {
		items = append(items, r.doc())
	}
	return gwItems(q, items)
}

func findRoute(a API, id string) (int, error) {
	i := slices.IndexFunc(a.Routes, func(r Route) bool { return r.ID == id })
	if i < 0 {
		return i, gwNotFound("Route", id)
	}
	return i, nil
}

func (s *Service) gwGetRoute(q *awsapi.Req, p map[string]string) error {
	a, err := s.gwAPI(q, p, "apigateway:GET", "routes", p["id"])
	if err != nil {
		return err
	}
	i, err := findRoute(a, p["id"])
	if err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusOK, a.Routes[i].doc())
}

func (s *Service) gwUpdateRoute(q *awsapi.Req, p map[string]string) error {
	if _, err := s.gwAPI(q, p, "apigateway:PATCH", "routes", p["id"]); err != nil {
		return err
	}
	in, err := gwBind(q)
	if err != nil {
		return err
	}
	var out Route
	if _, err := s.gwModify(p["api"], true, func(a *API) error {
		i, err := findRoute(*a, p["id"])
		if err != nil {
			return err
		}
		r, err := parseRouteInput(a, in, &a.Routes[i])
		if err != nil {
			return err
		}
		a.Routes[i], out = r, r
		return nil
	}); err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusOK, out.doc())
}

func (s *Service) gwDeleteRoute(q *awsapi.Req, p map[string]string) error {
	if _, err := s.gwAPI(q, p, "apigateway:DELETE", "routes", p["id"]); err != nil {
		return err
	}
	if _, err := s.gwModify(p["api"], true, func(a *API) error {
		i, err := findRoute(*a, p["id"])
		if err != nil {
			return err
		}
		a.Routes = slices.Delete(a.Routes, i, i+1)
		return nil
	}); err != nil {
		return err
	}
	return gwNoContent(q)
}

// ---- generic sub-resource documents (integrations, stages, authorizers) ----

// subresource describes one of the AWS-shaped document lists on an API.
type subresource struct {
	kind, idKey, path string
	list              func(*API) *[]doc
	// prepare validates and completes a document (create) or its merged form (update).
	prepare func(a *API, d doc, create bool) error
}

func mergeDoc(dst, in doc, protected ...string) {
	for k, v := range in {
		if slices.Contains(protected, k) {
			continue
		}
		if v == nil {
			delete(dst, k)
			continue
		}
		dst[k] = v
	}
}

func (s *Service) gwSubCreate(sr subresource) awsHandler {
	return func(q *awsapi.Req, p map[string]string) error {
		if _, err := s.gwAPI(q, p, "apigateway:POST", sr.path); err != nil {
			return err
		}
		in, err := gwBind(q)
		if err != nil {
			return err
		}
		var out doc
		if _, err := s.gwModify(p["api"], true, func(a *API) error {
			d := doc{}
			mergeDoc(d, in, sr.idKey)
			d[sr.idKey] = strings.ToLower(core.RandHex(7))
			if err := sr.prepare(a, d, true); err != nil {
				return err
			}
			l := sr.list(a)
			*l = append(*l, d)
			out = d
			return nil
		}); err != nil {
			return err
		}
		return s.gwRespond(q, http.StatusCreated, out)
	}
}

func (s *Service) gwSubList(sr subresource) awsHandler {
	return func(q *awsapi.Req, p map[string]string) error {
		a, err := s.gwAPI(q, p, "apigateway:GET", sr.path)
		if err != nil {
			return err
		}
		return gwItems(q, *sr.list(&a))
	}
}

func (s *Service) gwSubGet(sr subresource) awsHandler {
	return func(q *awsapi.Req, p map[string]string) error {
		a, err := s.gwAPI(q, p, "apigateway:GET", sr.path, p["id"])
		if err != nil {
			return err
		}
		for _, d := range *sr.list(&a) {
			if dStr(d, sr.idKey) == p["id"] {
				return s.gwRespond(q, http.StatusOK, d)
			}
		}
		return gwNotFound(sr.kind, p["id"])
	}
}

func (s *Service) gwSubUpdate(sr subresource) awsHandler {
	return func(q *awsapi.Req, p map[string]string) error {
		if _, err := s.gwAPI(q, p, "apigateway:PATCH", sr.path, p["id"]); err != nil {
			return err
		}
		in, err := gwBind(q)
		if err != nil {
			return err
		}
		var out doc
		if _, err := s.gwModify(p["api"], true, func(a *API) error {
			for _, d := range *sr.list(a) {
				if dStr(d, sr.idKey) != p["id"] {
					continue
				}
				merged := doc{}
				mergeDoc(merged, d)
				mergeDoc(merged, in, sr.idKey)
				if err := sr.prepare(a, merged, false); err != nil {
					return err
				}
				clear(d)
				mergeDoc(d, merged)
				out = d
				return nil
			}
			return gwNotFound(sr.kind, p["id"])
		}); err != nil {
			return err
		}
		return s.gwRespond(q, http.StatusOK, out)
	}
}

func (s *Service) gwSubDelete(sr subresource, onDelete func(*API, string)) awsHandler {
	return func(q *awsapi.Req, p map[string]string) error {
		if _, err := s.gwAPI(q, p, "apigateway:DELETE", sr.path, p["id"]); err != nil {
			return err
		}
		if _, err := s.gwModify(p["api"], true, func(a *API) error {
			l := sr.list(a)
			i := slices.IndexFunc(*l, func(d doc) bool { return dStr(d, sr.idKey) == p["id"] })
			if i < 0 {
				return gwNotFound(sr.kind, p["id"])
			}
			*l = slices.Delete(*l, i, i+1)
			if onDelete != nil {
				onDelete(a, p["id"])
			}
			return nil
		}); err != nil {
			return err
		}
		return gwNoContent(q)
	}
}

// ---- integrations ----

var integrationSR = subresource{kind: "Integration", idKey: "integrationId", path: "integrations",
	list: func(a *API) *[]doc { return &a.Integrations }, prepare: func(a *API, d doc, create bool) error { return checkIntegration(d) }}

func checkIntegration(d doc) error {
	typ, uri := dStr(d, "integrationType"), dStr(d, "integrationUri")
	switch typ {
	case "AWS_PROXY":
		if uri == "" {
			return core.BadRequest("integrationUri is required for AWS_PROXY integrations")
		}
		if _, ok := lambdaTarget(uri); !ok {
			return core.BadRequest("only Lambda function integrationUri values are supported for AWS_PROXY")
		}
		if v := dStr(d, "payloadFormatVersion"); v != "1.0" && v != "2.0" {
			return core.BadRequest("payloadFormatVersion must be 1.0 or 2.0 for AWS_PROXY integrations")
		}
	case "HTTP_PROXY":
		u, err := url.Parse(uri)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return core.BadRequest("integrationUri must be an http or https URL for HTTP_PROXY integrations")
		}
		if m := dStr(d, "integrationMethod"); m == "" || (!methods[strings.ToUpper(m)]) {
			return core.BadRequest("integrationMethod is required for HTTP_PROXY integrations")
		}
		if _, ok := d["payloadFormatVersion"]; !ok {
			d["payloadFormatVersion"] = "1.0"
		}
	default:
		return core.BadRequest("integrationType %q is not supported: use AWS_PROXY or HTTP_PROXY", typ)
	}
	if _, ok := d["timeoutInMillis"]; !ok {
		d["timeoutInMillis"] = 30000
	}
	if _, ok := d["connectionType"]; !ok {
		d["connectionType"] = "INTERNET"
	}
	if _, ok := d["passthroughBehavior"]; !ok && dStr(d, "integrationType") == "AWS_PROXY" {
		d["passthroughBehavior"] = "WHEN_NO_MATCH"
	}
	d["apiGatewayManaged"] = false
	return nil
}

var lambdaURIRe = regexp.MustCompile(`^(?:arn:aws:apigateway:[^:]*:lambda:path/[0-9-]+/functions/)?(arn:aws:lambda:[^:]*:[0-9]*:function:[^/]+)(?:/invocations)?$`)

// lambdaTarget extracts the function ARN from an integration URI.
func lambdaTarget(uri string) (string, bool) {
	m := lambdaURIRe.FindStringSubmatch(uri)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// ---- stages ----

func newStage(in doc) doc {
	st := doc{}
	mergeDoc(st, in)
	now := gwTime()
	st["createdDate"], st["lastUpdatedDate"] = now, now
	if _, ok := st["autoDeploy"]; !ok {
		st["autoDeploy"] = false
	}
	if _, ok := st["defaultRouteSettings"]; !ok {
		st["defaultRouteSettings"] = doc{"detailedMetricsEnabled": false}
	}
	if _, ok := st["routeSettings"]; !ok {
		st["routeSettings"] = doc{}
	}
	if _, ok := st["tags"]; !ok {
		st["tags"] = doc{}
	}
	st["apiGatewayManaged"] = false
	return st
}

func (s *Service) gwCreateStage(q *awsapi.Req, p map[string]string) error {
	if _, err := s.gwAPI(q, p, "apigateway:POST", "stages"); err != nil {
		return err
	}
	in, err := gwBind(q)
	if err != nil {
		return err
	}
	name := dStr(in, "stageName")
	if !gwStageRe.MatchString(name) {
		return core.BadRequest("stageName must be $default or contain only letters, digits, '-' and '_'")
	}
	var out doc
	if _, err := s.gwModify(p["api"], false, func(a *API) error {
		if slices.ContainsFunc(a.Stages, func(d doc) bool { return dStr(d, "stageName") == name }) {
			return core.Conflict("Stage with name %s already exists", name)
		}
		if id := dStr(in, "deploymentId"); id != "" && !slices.ContainsFunc(a.Deployments, func(d doc) bool { return dStr(d, "deploymentId") == id }) {
			return gwNotFound("Deployment", id)
		}
		st := newStage(in)
		if b, _ := st["autoDeploy"].(bool); b && dStr(st, "deploymentId") == "" {
			deploy(a, st, "", true)
		}
		a.Stages = append(a.Stages, st)
		out = st
		return nil
	}); err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusCreated, out)
}

func (s *Service) gwGetStages(q *awsapi.Req, p map[string]string) error {
	a, err := s.gwAPI(q, p, "apigateway:GET", "stages")
	if err != nil {
		return err
	}
	return gwItems(q, a.Stages)
}

func (s *Service) gwGetStage(q *awsapi.Req, p map[string]string) error {
	a, err := s.gwAPI(q, p, "apigateway:GET", "stages", p["id"])
	if err != nil {
		return err
	}
	for _, d := range a.Stages {
		if dStr(d, "stageName") == p["id"] {
			return s.gwRespond(q, http.StatusOK, d)
		}
	}
	return gwNotFound("Stage", p["id"])
}

func (s *Service) gwUpdateStage(q *awsapi.Req, p map[string]string) error {
	if _, err := s.gwAPI(q, p, "apigateway:PATCH", "stages", p["id"]); err != nil {
		return err
	}
	in, err := gwBind(q)
	if err != nil {
		return err
	}
	var out doc
	if _, err := s.gwModify(p["api"], false, func(a *API) error {
		for _, st := range a.Stages {
			if dStr(st, "stageName") != p["id"] {
				continue
			}
			if id := dStr(in, "deploymentId"); id != "" && !slices.ContainsFunc(a.Deployments, func(d doc) bool { return dStr(d, "deploymentId") == id }) {
				return gwNotFound("Deployment", id)
			}
			wasAuto, _ := st["autoDeploy"].(bool)
			mergeDoc(st, in, "stageName", "createdDate", "apiGatewayManaged")
			st["lastUpdatedDate"] = gwTime()
			if auto, _ := st["autoDeploy"].(bool); auto && !wasAuto && dStr(in, "deploymentId") == "" {
				deploy(a, st, "", true)
			}
			out = st
			return nil
		}
		return gwNotFound("Stage", p["id"])
	}); err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusOK, out)
}

func (s *Service) gwDeleteStage(q *awsapi.Req, p map[string]string) error {
	if _, err := s.gwAPI(q, p, "apigateway:DELETE", "stages", p["id"]); err != nil {
		return err
	}
	if _, err := s.gwModify(p["api"], false, func(a *API) error {
		i := slices.IndexFunc(a.Stages, func(d doc) bool { return dStr(d, "stageName") == p["id"] })
		if i < 0 {
			return gwNotFound("Stage", p["id"])
		}
		a.Stages = slices.Delete(a.Stages, i, i+1)
		return nil
	}); err != nil {
		return err
	}
	return gwNoContent(q)
}

// ---- deployments ----

func (s *Service) gwCreateDeployment(q *awsapi.Req, p map[string]string) error {
	if _, err := s.gwAPI(q, p, "apigateway:POST", "deployments"); err != nil {
		return err
	}
	in, err := gwBind(q)
	if err != nil {
		return err
	}
	var out doc
	if _, err := s.gwModify(p["api"], false, func(a *API) error {
		var st doc
		if name := dStr(in, "stageName"); name != "" {
			i := slices.IndexFunc(a.Stages, func(d doc) bool { return dStr(d, "stageName") == name })
			if i < 0 {
				return gwNotFound("Stage", name)
			}
			st = a.Stages[i]
		}
		out = deploy(a, st, dStr(in, "description"), false)
		return nil
	}); err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusCreated, out)
}

func (s *Service) gwGetDeployments(q *awsapi.Req, p map[string]string) error {
	a, err := s.gwAPI(q, p, "apigateway:GET", "deployments")
	if err != nil {
		return err
	}
	return gwItems(q, a.Deployments)
}

func (s *Service) gwGetDeployment(q *awsapi.Req, p map[string]string) error {
	a, err := s.gwAPI(q, p, "apigateway:GET", "deployments", p["id"])
	if err != nil {
		return err
	}
	for _, d := range a.Deployments {
		if dStr(d, "deploymentId") == p["id"] {
			return s.gwRespond(q, http.StatusOK, d)
		}
	}
	return gwNotFound("Deployment", p["id"])
}

// ---- authorizers ----

var authorizerSR = subresource{kind: "Authorizer", idKey: "authorizerId", path: "authorizers",
	list: func(a *API) *[]doc { return &a.Authorizers }, prepare: func(a *API, d doc, create bool) error {
		if dStr(d, "authorizerType") != "JWT" {
			return core.BadRequest("authorizerType %q is not supported: use JWT", dStr(d, "authorizerType"))
		}
		if strings.TrimSpace(dStr(d, "name")) == "" {
			return core.BadRequest("name is required")
		}
		if _, ok := d["identitySource"].([]any); !ok {
			return core.BadRequest("identitySource is required for JWT authorizers")
		}
		if _, _, err := jwtConfig(d); err != nil {
			return err
		}
		return nil
	}}

// jwtConfig reads a JWT authorizer's issuer (a Cognito user pool: the pool ID is the
// last path segment) and audiences.
func jwtConfig(d doc) (pool string, audience []string, err error) {
	c, _ := d["jwtConfiguration"].(map[string]any)
	iss, _ := c["issuer"].(string)
	if iss == "" {
		return "", nil, core.BadRequest("jwtConfiguration.issuer is required for JWT authorizers")
	}
	if a, ok := c["audience"].([]any); ok {
		for _, x := range a {
			if sv, ok := x.(string); ok {
				audience = append(audience, sv)
			}
		}
	}
	return iss[strings.LastIndex(iss, "/")+1:], audience, nil
}

// ---- tags ----

// tagTarget resolves a tag ARN to the tag set it names.
func (s *Service) gwTagTarget(q *awsapi.Req, p map[string]string, action string, fn func(tags *core.Tags) error) error {
	arn := p["arn"]
	rest, ok := strings.CutPrefix(arn, gwARN("/apis/"))
	if !ok {
		return core.BadRequest("only HTTP APIs and their stages can be tagged: %s", arn)
	}
	// The action applies to the tags collection, as in AWS.
	if err := q.Authorize(action, gwARN("/tags/"+arn)); err != nil {
		return err
	}
	id, sub, _ := strings.Cut(rest, "/")
	stage, isStage := strings.CutPrefix(sub, "stages/")
	if sub != "" && !isStage {
		return core.BadRequest("resource cannot be tagged: %s", arn)
	}
	_, err := s.gwModify(id, false, func(a *API) error {
		if !isStage {
			return fn(&a.Tags)
		}
		for _, st := range a.Stages {
			if dStr(st, "stageName") == stage {
				t := core.Tags{}
				if m, ok := st["tags"].(map[string]any); ok {
					for k, v := range m {
						t[k], _ = v.(string)
					}
				}
				if err := fn(&t); err != nil {
					return err
				}
				m := doc{}
				for k, v := range t {
					m[k] = v
				}
				st["tags"] = m
				return nil
			}
		}
		return gwNotFound("Stage", stage)
	})
	return err
}

func (s *Service) gwTag(q *awsapi.Req, p map[string]string) error {
	in, err := gwBind(q)
	if err != nil {
		return err
	}
	add, err := gwTags(in["tags"])
	if err != nil {
		return err
	}
	if err := s.gwTagTarget(q, p, "apigateway:POST", func(t *core.Tags) error {
		if *t == nil {
			*t = core.Tags{}
		}
		for k, v := range add {
			(*t)[k] = v
		}
		if len(*t) > 50 {
			return core.BadRequest("a resource can have at most 50 tags")
		}
		return nil
	}); err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusCreated, nil)
}

func (s *Service) gwUntag(q *awsapi.Req, p map[string]string) error {
	keys := q.R.URL.Query()["tagKeys"]
	if err := s.gwTagTarget(q, p, "apigateway:DELETE", func(t *core.Tags) error {
		for _, k := range keys {
			delete(*t, k)
		}
		return nil
	}); err != nil {
		return err
	}
	return gwNoContent(q)
}

func (s *Service) gwGetTags(q *awsapi.Req, p map[string]string) error {
	var got core.Tags
	// Reads go through the same lookup without changing anything.
	if err := s.gwTagTarget(q, p, "apigateway:GET", func(t *core.Tags) error {
		got = core.Tags{}
		for k, v := range *t {
			got[k] = v
		}
		return nil
	}); err != nil {
		return err
	}
	return s.gwRespond(q, http.StatusOK, doc{"tags": map[string]string(nonNilTags(got))})
}
