package lambda

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const cAPIs = "apigw_apis"

// API is an HTTP API (API Gateway v2) whose routes invoke functions.
type API struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Routes      []Route     `json:"routes"`
	CORS        bool        `json:"cors"`
	Authorizer  *Authorizer `json:"authorizer,omitempty"`
	Endpoint    string      `json:"endpoint"`
	CreatedAt   time.Time   `json:"created_at"`

	// Set for APIs created through the AWS API (apigatewayv2). ProtocolType marks them:
	// they serve stages, integrations and resource-policy checks; native APIs serve as before.
	ProtocolType      string         `json:"protocol_type,omitempty"`
	CorsConfig        map[string]any `json:"cors_config,omitempty"`
	Version           string         `json:"version,omitempty"`
	DisableExecuteAPI bool           `json:"disable_execute_api,omitempty"`
	Integrations      []doc          `json:"integrations,omitempty"`
	Stages            []doc          `json:"stages,omitempty"`
	Deployments       []doc          `json:"deployments,omitempty"`
	Authorizers       []doc          `json:"authorizers,omitempty"`
	Tags              core.Tags      `json:"tags,omitempty"`
}

type Route struct {
	ID           string `json:"id"`
	Method       string `json:"method"` // GET, POST, ... or ANY
	Path         string `json:"path"`   // e.g. /items/{id} or /files/{proxy+}
	FunctionName string `json:"function_name"`
	// Authorization is NONE or JWT (requires a token from the API's authorizer user pool).
	Authorization string `json:"authorization,omitempty"`
	// AWS API fields: the integration a route targets (instead of FunctionName), its
	// authorizer and scopes, and other route settings kept as sent.
	IntegrationID string         `json:"integration_id,omitempty"`
	AuthorizerID  string         `json:"authorizer_id,omitempty"`
	Scopes        []string       `json:"scopes,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

// Authorizer validates Cognito user pool tokens on routes marked JWT.
type Authorizer struct {
	UserPoolID string `json:"user_pool_id"`
	Audience   string `json:"audience,omitempty"` // app client ID; empty accepts any client of the pool
}

// JWTVerifier checks a user pool token (set by the server from the Cognito service).
type JWTVerifier func(pool, token, audience string) (map[string]any, error)

func (r Route) Key() string {
	if r.Path == "$default" {
		return "$default"
	}
	return r.Method + " " + r.Path
}

// matchPath matches a request path against a route template, returning path parameters.
func matchPath(tmpl, p string) (map[string]string, bool) {
	ts := strings.Split(strings.Trim(tmpl, "/"), "/")
	ps := strings.Split(strings.Trim(p, "/"), "/")
	params := map[string]string{}
	for i, t := range ts {
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "+}") {
			params[t[1:len(t)-2]] = strings.Join(ps[i:], "/")
			return params, true
		}
		if i >= len(ps) {
			return nil, false
		}
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
			if ps[i] == "" {
				return nil, false
			}
			params[t[1:len(t)-1]] = ps[i]
			continue
		}
		if t != ps[i] {
			return nil, false
		}
	}
	return params, len(ts) == len(ps)
}

// specificity orders routes so literal segments win over parameters and greedy paths.
func specificity(r Route) int {
	score := 0
	for _, seg := range strings.Split(strings.Trim(r.Path, "/"), "/") {
		switch {
		case strings.HasSuffix(seg, "+}"):
			score += 0
		case strings.HasPrefix(seg, "{"):
			score += 1
		default:
			score += 3
		}
	}
	if r.Method != "ANY" {
		score++
	}
	return score
}

// httpEvent builds an API Gateway v2 (HTTP API) payload for a request.
func (s *Service) httpEvent(r *http.Request, rawPath, routeKey, apiID string, pathParams map[string]string) (map[string]any, error) {
	// Never hand HomeCloud credentials sent as ?access_token to function code.
	if q := r.URL.Query(); q.Has("access_token") {
		q.Del("access_token")
		r.URL.RawQuery = q.Encode()
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 6<<20))
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	for k, v := range r.Header {
		if strings.EqualFold(k, "Authorization") && apiID == "" {
			continue
		}
		headers[strings.ToLower(k)] = strings.Join(v, ",")
	}
	qs := map[string]string{}
	for k, v := range r.URL.Query() {
		qs[k] = strings.Join(v, ",")
	}
	now := time.Now()
	ev := map[string]any{
		"version": "2.0", "routeKey": routeKey, "rawPath": rawPath, "rawQueryString": r.URL.RawQuery,
		"headers": headers, "queryStringParameters": qs, "isBase64Encoded": false,
		"requestContext": map[string]any{
			"accountId": s.env.AccountID, "apiId": apiID, "domainName": r.Host, "requestId": uuid(), "routeKey": routeKey, "stage": "$default",
			"time": now.UTC().Format("02/Jan/2006:15:04:05 -0700"), "timeEpoch": now.UnixMilli(),
			"http": map[string]string{"method": r.Method, "path": rawPath, "protocol": r.Proto, "sourceIp": httpx.ClientIP(r), "userAgent": r.UserAgent()},
		},
	}
	if len(pathParams) > 0 {
		ev["pathParameters"] = pathParams
	}
	if len(r.Cookies()) > 0 {
		var cs []string
		for _, c := range r.Cookies() {
			cs = append(cs, c.String())
		}
		ev["cookies"] = cs
	}
	if len(body) > 0 {
		if utf8.Valid(body) {
			ev["body"] = string(body)
		} else {
			ev["body"] = base64.StdEncoding.EncodeToString(body)
			ev["isBase64Encoded"] = true
		}
	}
	return ev, nil
}

// respond turns a function result into an HTTP response, following API Gateway's rules.
func respond(w http.ResponseWriter, res *InvokeResult, cors bool) {
	if cors {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
	if res.FunctionError != "" {
		httpx.WriteJSON(w, http.StatusBadGateway, map[string]string{"message": "Internal Server Error"})
		return
	}
	var out struct {
		StatusCode      int               `json:"statusCode"`
		Headers         map[string]string `json:"headers"`
		Body            *string           `json:"body"`
		IsBase64Encoded bool              `json:"isBase64Encoded"`
		Cookies         []string          `json:"cookies"`
	}
	if json.Unmarshal(res.Payload, &out) != nil || out.StatusCode == 0 {
		// A bare value is returned as a 200 JSON body.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(res.Payload)
		return
	}
	for k, v := range out.Headers {
		w.Header().Set(k, v)
	}
	for _, c := range out.Cookies {
		w.Header().Add("Set-Cookie", c)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	if out.StatusCode < 100 || out.StatusCode > 599 {
		httpx.WriteJSON(w, http.StatusBadGateway, map[string]string{"message": "Internal Server Error"})
		return
	}
	w.WriteHeader(out.StatusCode)
	if out.Body != nil {
		if out.IsBase64Encoded {
			b, _ := base64.StdEncoding.DecodeString(*out.Body)
			w.Write(b)
		} else {
			w.Write([]byte(*out.Body))
		}
	}
}

func (s *Service) apigwRoutes(r *httpx.Router) {
	r.Handle("GET /api/v1/apigateway/apis", "apigateway:GET", s.listAPIs)
	r.Handle("POST /api/v1/apigateway/apis", "apigateway:POST", s.createAPI)
	res := httpx.Res("arn:aws:apigateway:{region}::/apis/{id}")
	r.Handle("GET /api/v1/apigateway/apis/{id}", "apigateway:GET", s.getAPI, res)
	r.Handle("PATCH /api/v1/apigateway/apis/{id}", "apigateway:PATCH", s.patchAPI, res)
	r.Handle("DELETE /api/v1/apigateway/apis/{id}", "apigateway:DELETE", s.deleteAPI, res)
	r.Handle("POST /api/v1/apigateway/apis/{id}/routes", "apigateway:POST", s.addRoute, res)
	r.Handle("PATCH /api/v1/apigateway/apis/{id}/routes/{route}", "apigateway:PATCH", s.updateRoute, res)
	r.Handle("DELETE /api/v1/apigateway/apis/{id}/routes/{route}", "apigateway:DELETE", s.deleteRoute, res)
	r.Handle("/apigw/{id}/{path...}", "", s.serveAPI, httpx.Public())
}

func (s *Service) apiEndpoint(id string) string {
	return s.publicBase() + "/apigw/" + id
}

func (s *Service) listAPIs(c *httpx.Ctx) (any, error) {
	return store.List[API](s.env.Store, cAPIs), nil
}

func (s *Service) getAPI(c *httpx.Ctx) (any, error) {
	a, err := store.Get[API](s.env.Store, cAPIs, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("api", c.Param("id"))
	}
	return a, nil
}

var methods = map[string]bool{"ANY": true, "GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true}

// checkRoute validates a route; the caller must be allowed to invoke its function,
// since the route makes it publicly callable.
func (s *Service) checkRoute(c *httpx.Ctx, r *Route) error {
	r.Method = strings.ToUpper(r.Method)
	if r.Method == "" {
		r.Method = "ANY"
	}
	if !methods[r.Method] {
		return core.BadRequest("unsupported method %q", r.Method)
	}
	if !strings.HasPrefix(r.Path, "/") {
		return core.BadRequest("route path must start with /")
	}
	if !s.Exists(r.FunctionName) {
		return core.NotFound("function", r.FunctionName)
	}
	if err := c.Authorize("lambda:InvokeFunction", s.env.ARN("lambda", "function:"+r.FunctionName)); err != nil {
		return err
	}
	r.Authorization = strings.ToUpper(r.Authorization)
	if r.Authorization == "" {
		r.Authorization = "NONE"
	}
	if r.Authorization != "NONE" && r.Authorization != "JWT" {
		return core.BadRequest("authorization must be NONE or JWT")
	}
	if r.ID == "" {
		r.ID = strings.ToLower(core.RandHex(7))
	}
	return nil
}

// checkAuthorizer verifies the pool and client exist and that the caller may use
// the pool: attaching it makes the API trust the pool's tokens.
func (s *Service) checkAuthorizer(c *httpx.Ctx, a *Authorizer) error {
	if a == nil || a.UserPoolID == "" {
		return nil
	}
	if err := c.Authorize("cognito-idp:DescribeUserPool", s.env.ARN("cognito-idp", "userpool/"+a.UserPoolID)); err != nil {
		return err
	}
	if s.CheckAuthorizer != nil {
		return s.CheckAuthorizer(a.UserPoolID, a.Audience)
	}
	return nil
}

func (s *Service) createAPI(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		CORS        bool        `json:"cors"`
		Routes      []Route     `json:"routes"`
		Authorizer  *Authorizer `json:"authorizer"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, core.BadRequest("name is required")
	}
	if err := s.checkAuthorizer(c, in.Authorizer); err != nil {
		return nil, err
	}
	if in.Authorizer != nil && in.Authorizer.UserPoolID == "" {
		in.Authorizer = nil
	}
	id := strings.ToLower(core.RandHex(10))
	a := API{ID: id, Name: in.Name, Description: in.Description, CORS: in.CORS, Routes: []Route{}, Endpoint: s.apiEndpoint(id), CreatedAt: core.Now(), Authorizer: in.Authorizer}
	for _, r := range in.Routes {
		if err := s.checkRoute(c, &r); err != nil {
			return nil, err
		}
		a.Routes = append(a.Routes, r)
	}
	return a, store.Put(s.env.Store, cAPIs, a.ID, a)
}

func (s *Service) patchAPI(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        *string     `json:"name"`
		Description *string     `json:"description"`
		CORS        *bool       `json:"cors"`
		Authorizer  *Authorizer `json:"authorizer"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.checkAuthorizer(c, in.Authorizer); err != nil {
		return nil, err
	}
	a, err := store.Update(s.env.Store, cAPIs, c.Param("id"), func(a *API) error {
		if in.Name != nil {
			a.Name = *in.Name
		}
		if in.Description != nil {
			a.Description = *in.Description
		}
		if in.CORS != nil {
			a.CORS = *in.CORS
		}
		if in.Authorizer != nil {
			if in.Authorizer.UserPoolID == "" {
				a.Authorizer = nil
			} else {
				a.Authorizer = in.Authorizer
			}
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("api", c.Param("id"))
	}
	return a, err
}

func (s *Service) deleteAPI(c *httpx.Ctx) (any, error) {
	if err := store.Delete(s.env.Store, cAPIs, c.Param("id")); err != nil {
		return nil, core.NotFound("api", c.Param("id"))
	}
	return nil, nil
}

func (s *Service) addRoute(c *httpx.Ctx) (any, error) {
	var r Route
	if err := c.Bind(&r); err != nil {
		return nil, err
	}
	if err := s.checkRoute(c, &r); err != nil {
		return nil, err
	}
	a, err := store.Update(s.env.Store, cAPIs, c.Param("id"), func(a *API) error {
		for _, x := range a.Routes {
			if x.Key() == r.Key() {
				return core.Conflict("route %q already exists", r.Key())
			}
		}
		a.Routes = append(a.Routes, r)
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("api", c.Param("id"))
	}
	return a, err
}

// updateRoute changes a route in place, keeping its ID.
func (s *Service) updateRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Method        *string `json:"method"`
		Path          *string `json:"path"`
		FunctionName  *string `json:"function_name"`
		Authorization *string `json:"authorization"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	cur, err := store.Get[API](s.env.Store, cAPIs, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("api", c.Param("id"))
	}
	i := slices.IndexFunc(cur.Routes, func(r Route) bool { return r.ID == c.Param("route") })
	if i < 0 {
		return nil, core.NotFound("route", c.Param("route"))
	}
	r := cur.Routes[i]
	if in.Method != nil {
		r.Method = *in.Method
	}
	if in.Path != nil {
		r.Path = *in.Path
	}
	if in.FunctionName != nil {
		r.FunctionName = *in.FunctionName
	}
	if in.Authorization != nil {
		r.Authorization = *in.Authorization
	}
	if err := s.checkRoute(c, &r); err != nil {
		return nil, err
	}
	a, err := store.Update(s.env.Store, cAPIs, c.Param("id"), func(a *API) error {
		j := -1
		for k, x := range a.Routes {
			if x.ID == r.ID {
				j = k
			} else if x.Key() == r.Key() {
				return core.Conflict("route %q already exists", r.Key())
			}
		}
		if j < 0 {
			return core.NotFound("route", r.ID)
		}
		a.Routes[j] = r
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("api", c.Param("id"))
	}
	return a, err
}

func (s *Service) deleteRoute(c *httpx.Ctx) (any, error) {
	a, err := store.Update(s.env.Store, cAPIs, c.Param("id"), func(a *API) error {
		for i, x := range a.Routes {
			if x.ID == c.Param("route") {
				a.Routes = append(a.Routes[:i], a.Routes[i+1:]...)
				return nil
			}
		}
		return core.NotFound("route", c.Param("route"))
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("api", c.Param("id"))
	}
	return a, err
}
