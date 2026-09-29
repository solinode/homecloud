package lambda

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Routes      []Route   `json:"routes"`
	CORS        bool      `json:"cors"`
	Endpoint    string    `json:"endpoint"`
	CreatedAt   time.Time `json:"created_at"`
}

type Route struct {
	ID           string `json:"id"`
	Method       string `json:"method"` // GET, POST, ... or ANY
	Path         string `json:"path"`   // e.g. /items/{id} or /files/{proxy+}
	FunctionName string `json:"function_name"`
}

func (r Route) Key() string { return r.Method + " " + r.Path }

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

// serveURL handles a function URL: /lambda-url/{name}/{path...}.
func (s *Service) serveURL(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	f, err := store.Get[Function](s.env.Store, cFunctions, name)
	if err != nil || !f.URL.Enabled {
		return nil, core.Errf(http.StatusNotFound, "NotFound", "no function URL is configured for %q", name)
	}
	if f.URL.AuthType == "HC_IAM" {
		if s.Auth == nil {
			return nil, core.Errf(http.StatusForbidden, "AccessDenied", "IAM auth unavailable")
		}
		p, err := s.Auth.Authenticate(c.R)
		if err != nil {
			return nil, err
		}
		if !p.Can("lambda:InvokeFunctionUrl", f.ARN) {
			return nil, core.Errf(http.StatusForbidden, "AccessDenied", "%s may not invoke %s", p.ARN, f.ARN)
		}
	}
	ev, err := s.httpEvent(c.R, "/"+c.Param("path"), "$default", "", nil)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(ev)
	res, err := s.Invoke(c.R.Context(), name, payload)
	if err != nil {
		return nil, err
	}
	c.MarkWritten()
	respond(c.W, res, false)
	return nil, nil
}

func (s *Service) apigwRoutes(r *httpx.Router) {
	r.Handle("GET /api/v1/apigateway/apis", "apigateway:GET", s.listAPIs)
	r.Handle("POST /api/v1/apigateway/apis", "apigateway:POST", s.createAPI)
	r.Handle("GET /api/v1/apigateway/apis/{id}", "apigateway:GET", s.getAPI)
	r.Handle("PATCH /api/v1/apigateway/apis/{id}", "apigateway:PATCH", s.patchAPI)
	r.Handle("DELETE /api/v1/apigateway/apis/{id}", "apigateway:DELETE", s.deleteAPI)
	r.Handle("POST /api/v1/apigateway/apis/{id}/routes", "apigateway:POST", s.addRoute)
	r.Handle("DELETE /api/v1/apigateway/apis/{id}/routes/{route}", "apigateway:DELETE", s.deleteRoute)
	r.Handle("/apigw/{id}/{path...}", "", s.serveAPI, httpx.Public())
}

func (s *Service) apiEndpoint(id string) string {
	_, port, _ := strings.Cut(s.env.Cfg.APIAddr, ":")
	return fmt.Sprintf("http://%s:%s/apigw/%s", s.env.Cfg.PublicHost, port, id)
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

func (s *Service) checkRoute(r *Route) error {
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
	r.ID = strings.ToLower(core.RandHex(7))
	return nil
}

func (s *Service) createAPI(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string  `json:"name"`
		Description string  `json:"description"`
		CORS        bool    `json:"cors"`
		Routes      []Route `json:"routes"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, core.BadRequest("name is required")
	}
	id := strings.ToLower(core.RandHex(10))
	a := API{ID: id, Name: in.Name, Description: in.Description, CORS: in.CORS, Routes: []Route{}, Endpoint: s.apiEndpoint(id), CreatedAt: core.Now()}
	for _, r := range in.Routes {
		if err := s.checkRoute(&r); err != nil {
			return nil, err
		}
		a.Routes = append(a.Routes, r)
	}
	return a, store.Put(s.env.Store, cAPIs, a.ID, a)
}

func (s *Service) patchAPI(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		CORS        *bool   `json:"cors"`
	}
	if err := c.Bind(&in); err != nil {
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
	if err := s.checkRoute(&r); err != nil {
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

// serveAPI dispatches a public request to the best-matching route's function.
func (s *Service) serveAPI(c *httpx.Ctx) (any, error) {
	a, err := store.Get[API](s.env.Store, cAPIs, c.Param("id"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "NotFound", "no API %q", c.Param("id"))
	}
	if a.CORS && c.R.Method == http.MethodOptions {
		h := c.W.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "*")
		return c.JSON(http.StatusNoContent, nil)
	}
	p := "/" + c.Param("path")
	routes := append([]Route(nil), a.Routes...)
	sort.SliceStable(routes, func(i, j int) bool { return specificity(routes[i]) > specificity(routes[j]) })
	for _, r := range routes {
		if r.Method != "ANY" && r.Method != c.R.Method {
			continue
		}
		params, ok := matchPath(r.Path, p)
		if !ok {
			continue
		}
		ev, err := s.httpEvent(c.R, p, r.Key(), a.ID, params)
		if err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(ev)
		res, err := s.Invoke(c.R.Context(), r.FunctionName, payload)
		if err != nil {
			return nil, err
		}
		c.MarkWritten()
		respond(c.W, res, a.CORS)
		return nil, nil
	}
	return nil, core.Errf(http.StatusNotFound, "NotFound", "no route matches %s %s", c.R.Method, p)
}
