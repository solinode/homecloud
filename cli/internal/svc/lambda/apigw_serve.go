package lambda

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

func gwMessage(c *httpx.Ctx, status int, msg string) (any, error) {
	return c.JSON(status, map[string]string{"message": msg})
}

func findStage(a API, name string) doc {
	for _, st := range a.Stages {
		if dStr(st, "stageName") == name {
			return st
		}
	}
	return nil
}

// serveAPI dispatches a public request to the best-matching route's integration.
// APIs created through the AWS API (ProtocolType set) are served like AWS HTTP
// APIs: the first path segment names a stage (else the $default stage), routes
// target integrations and Lambda calls need the function's resource policy.
func (s *Service) serveAPI(c *httpx.Ctx) (any, error) {
	a, err := store.Get[API](s.env.Store, cAPIs, c.Param("id"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "NotFound", "no API %q", c.Param("id"))
	}
	p := "/" + c.Param("path")
	stage, prefix := "$default", ""
	var stageDoc doc
	v2 := a.ProtocolType != ""
	if v2 {
		if a.DisableExecuteAPI {
			return gwMessage(c, http.StatusForbidden, "Forbidden")
		}
		first, rest, _ := strings.Cut(c.Param("path"), "/")
		if named := findStage(a, first); first != "" && first != "$default" && named != nil {
			stage, stageDoc, prefix, p = first, named, "/"+first, "/"+rest
		} else if d := findStage(a, "$default"); d != nil {
			stageDoc = d
		} else {
			return gwMessage(c, http.StatusNotFound, "Not Found")
		}
		if a.CorsConfig != nil {
			if s.applyCORS(c, a.CorsConfig) {
				return c.JSON(http.StatusNoContent, nil)
			}
		}
	} else if a.CORS && c.R.Method == http.MethodOptions {
		h := c.W.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "*")
		return c.JSON(http.StatusNoContent, nil)
	}
	routes := append([]Route(nil), a.Routes...)
	sort.SliceStable(routes, func(i, j int) bool { return specificity(routes[i]) > specificity(routes[j]) })
	for _, r := range routes {
		if r.Method != "ANY" && r.Method != c.R.Method {
			continue
		}
		var params map[string]string
		if r.Path != "$default" {
			var ok bool
			if params, ok = matchPath(r.Path, p); !ok {
				continue
			}
		}
		return s.dispatch(c, a, r, stage, stageDoc, prefix, p, params)
	}
	if v2 {
		return gwMessage(c, http.StatusNotFound, "Not Found")
	}
	return nil, core.Errf(http.StatusNotFound, "NotFound", "no route matches %s %s", c.R.Method, p)
}

func (s *Service) dispatch(c *httpx.Ctx, a API, r Route, stage string, stageDoc doc, prefix, p string, params map[string]string) (any, error) {
	v2 := a.ProtocolType != ""
	var integ doc
	if r.IntegrationID != "" {
		for _, d := range a.Integrations {
			if dStr(d, "integrationId") == r.IntegrationID {
				integ = d
			}
		}
		if integ == nil {
			log.Printf("apigw: route %s of API %s targets a missing integration", r.Key(), a.ID)
			return gwMessage(c, http.StatusInternalServerError, "Internal Server Error")
		}
	}
	var claims map[string]any
	if r.Authorization == "JWT" {
		var ok bool
		if claims, ok = s.checkJWT(c, a, r); !ok {
			return gwMessage(c, http.StatusUnauthorized, "Unauthorized")
		}
	}
	if integ != nil && dStr(integ, "integrationType") == "HTTP_PROXY" {
		return s.proxyHTTP(c, integ, r, p, params)
	}
	fn := r.FunctionName
	format := "2.0"
	if integ != nil {
		fn, _ = lambdaTarget(dStr(integ, "integrationUri"))
		format = dStr(integ, "payloadFormatVersion")
		name, qual := parseRef(fn)
		source := core.ARN(s.env.AccountID, "execute-api", a.ID+"/"+stage+"/"+r.Method+r.Path)
		if r.Path == "$default" {
			source = core.ARN(s.env.AccountID, "execute-api", a.ID+"/"+stage+"/$default")
		}
		if !s.apigwMayInvoke(name, qual, source) {
			log.Printf("apigw: %s may not invoke %s: the function's resource policy has no lambda:InvokeFunction statement for apigateway.amazonaws.com matching %s", a.ID, fn, source)
			return gwMessage(c, http.StatusInternalServerError, "Internal Server Error")
		}
	}
	ev, err := s.httpEvent(c.R, prefix+p, r.Key(), a.ID, params)
	if err != nil {
		return nil, err
	}
	rc := ev["requestContext"].(map[string]any)
	rc["stage"] = stage
	if claims != nil {
		rc["authorizer"] = map[string]any{"jwt": map[string]any{"claims": claims}}
	}
	if vars, ok := stageDoc["stageVariables"].(map[string]any); ok && len(vars) > 0 {
		ev["stageVariables"] = vars
	}
	if format == "1.0" {
		ev = v1Event(ev, c.R, r, prefix+p)
	}
	payload, _ := json.Marshal(ev)
	res, err := s.Invoke(c.R.Context(), fn, payload)
	if err != nil {
		if v2 {
			log.Printf("apigw: invoking %s for API %s: %v", fn, a.ID, err)
			return gwMessage(c, http.StatusInternalServerError, "Internal Server Error")
		}
		return nil, err
	}
	c.MarkWritten()
	respond(c.W, res, a.CORS)
	return nil, nil
}

// checkJWT validates the request's bearer token against the route's authorizer.
func (s *Service) checkJWT(c *httpx.Ctx, a API, r Route) (map[string]any, bool) {
	var pool string
	var audiences []string
	if r.AuthorizerID != "" {
		i := slices.IndexFunc(a.Authorizers, func(d doc) bool { return dStr(d, "authorizerId") == r.AuthorizerID })
		if i < 0 {
			return nil, false
		}
		pool, audiences, _ = jwtConfig(a.Authorizers[i])
	} else if a.Authorizer != nil {
		pool, audiences = a.Authorizer.UserPoolID, []string{a.Authorizer.Audience}
	}
	tok := strings.TrimSpace(strings.TrimPrefix(c.R.Header.Get("Authorization"), "Bearer "))
	if pool == "" || s.VerifyJWT == nil || tok == "" {
		return nil, false
	}
	if len(audiences) == 0 {
		audiences = []string{""}
	}
	for _, aud := range audiences {
		claims, err := s.VerifyJWT(pool, tok, aud)
		if err != nil {
			continue
		}
		if len(r.Scopes) > 0 && !hasScope(claims, r.Scopes) {
			return nil, false
		}
		return claims, true
	}
	return nil, false
}

func hasScope(claims map[string]any, want []string) bool {
	have := map[string]bool{}
	if sc, ok := claims["scope"].(string); ok {
		for _, x := range strings.Fields(sc) {
			have[x] = true
		}
	}
	if sc, ok := claims["scp"].([]any); ok {
		for _, x := range sc {
			if sv, ok := x.(string); ok {
				have[sv] = true
			}
		}
	}
	return slices.ContainsFunc(want, func(w string) bool { return have[w] })
}

// apigwMayInvoke reports whether the function's resource policy lets API Gateway
// invoke it for a request that matches sourceArn (an execute-api ARN), as AWS
// requires of every Lambda integration.
func (s *Service) apigwMayInvoke(name, qual, sourceArn string) bool {
	f, err := s.getLatest(name)
	if err != nil {
		return false
	}
	if qual == latest {
		qual = ""
	}
	for _, st := range f.Policy[qual] {
		if st.Effect != "Allow" || !slices.Contains([]string{"lambda:InvokeFunction", "lambda:*", "*"}, st.Action) || !principalIsService(st.Principal, "apigateway.amazonaws.com") {
			continue
		}
		ok := true
		for op, kv := range st.Condition {
			for k, v := range kv {
				switch {
				case (op == "ArnLike" || op == "ArnEquals") && strings.EqualFold(k, "aws:SourceArn"):
					ok = ok && arnGlob(v, sourceArn)
				case op == "StringEquals" && strings.EqualFold(k, "aws:SourceAccount"):
					ok = ok && v == s.env.AccountID
				default:
					ok = false // a condition we can't evaluate does not grant access
				}
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func principalIsService(p any, svc string) bool {
	switch pr := p.(type) {
	case string:
		return pr == "*"
	case map[string]string:
		return pr["Service"] == svc
	case map[string]any:
		switch v := pr["Service"].(type) {
		case string:
			return v == svc
		case []any:
			return slices.Contains(v, any(svc))
		}
	}
	return false
}

// arnGlob matches an ARN against a pattern with * and ? wildcards.
func arnGlob(pattern, s string) bool {
	re := "^" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(pattern)) + "$"
	ok, _ := regexp.MatchString(re, s)
	return ok
}

// v1Event converts an HTTP API v2 payload to the v1 (REST-style) payload format.
func v1Event(v2 map[string]any, r *http.Request, rt Route, path string) map[string]any {
	rc := v2["requestContext"].(map[string]any)
	headers, multi := map[string]string{}, map[string][]string{}
	for k, v := range r.Header {
		headers[k], multi[k] = v[0], v
	}
	var qs map[string]string
	var mqs map[string][]string
	if q := r.URL.Query(); len(q) > 0 {
		qs, mqs = map[string]string{}, map[string][]string{}
		for k, v := range q {
			qs[k], mqs[k] = v[len(v)-1], v
		}
	}
	resource := rt.Path
	if resource == "$default" {
		resource = "/{proxy+}"
	}
	ctx := map[string]any{
		"accountId": rc["accountId"], "apiId": rc["apiId"], "domainName": rc["domainName"], "requestId": rc["requestId"], "stage": rc["stage"],
		"protocol": r.Proto, "httpMethod": r.Method, "path": path, "resourcePath": resource, "requestTimeEpoch": rc["timeEpoch"],
		"identity": map[string]any{"sourceIp": httpx.ClientIP(r), "userAgent": r.UserAgent()},
	}
	if a, ok := rc["authorizer"]; ok {
		ctx["authorizer"] = a
	}
	ev := map[string]any{
		"version": "1.0", "resource": resource, "path": path, "httpMethod": r.Method, "headers": headers, "multiValueHeaders": multi,
		"queryStringParameters": qs, "multiValueQueryStringParameters": mqs, "pathParameters": v2["pathParameters"],
		"stageVariables": v2["stageVariables"], "requestContext": ctx, "body": v2["body"], "isBase64Encoded": v2["isBase64Encoded"],
	}
	return ev
}

var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Host"}

// proxyHTTP forwards the request to an HTTP_PROXY integration and relays its response.
func (s *Service) proxyHTTP(c *httpx.Ctx, integ doc, r Route, p string, params map[string]string) (any, error) {
	target := dStr(integ, "integrationUri")
	for k, v := range params {
		target = strings.NewReplacer("{"+k+"}", url.PathEscape(v), "{"+k+"+}", v).Replace(target)
	}
	if r.Path == "$default" {
		target = strings.TrimRight(target, "/") + p
	}
	q := c.R.URL.Query()
	q.Del("access_token")
	if enc := q.Encode(); enc != "" && !strings.Contains(target, "?") {
		target += "?" + enc
	}
	method := strings.ToUpper(dStr(integ, "integrationMethod"))
	if method == "ANY" || method == "" {
		method = c.R.Method
	}
	timeout := 30 * time.Second
	if ms, ok := integ["timeoutInMillis"].(float64); ok && ms > 0 && ms < 30000 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, io.LimitReader(c.R.Body, 10<<20))
	if err != nil {
		return gwMessage(c, http.StatusInternalServerError, "Internal Server Error")
	}
	req.Header = c.R.Header.Clone()
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	cli := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cli.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return gwMessage(c, http.StatusGatewayTimeout, "Gateway Timeout")
		}
		log.Printf("apigw: HTTP integration %s: %v", target, err)
		return gwMessage(c, http.StatusServiceUnavailable, "Service Unavailable")
	}
	defer resp.Body.Close()
	c.MarkWritten()
	for k, v := range resp.Header {
		if !slices.Contains(hopHeaders, k) {
			c.W.Header()[k] = v
		}
	}
	c.W.WriteHeader(resp.StatusCode)
	io.Copy(c.W, resp.Body)
	return nil, nil
}

// applyCORS sets CORS response headers from an API's corsConfiguration and
// reports whether the request was a preflight, which it fully answers.
func (s *Service) applyCORS(c *httpx.Ctx, cfg map[string]any) bool {
	list := func(k string) []string {
		var out []string
		if l, ok := cfg[k].([]any); ok {
			for _, x := range l {
				if sv, ok := x.(string); ok {
					out = append(out, sv)
				}
			}
		}
		return out
	}
	origin := c.R.Header.Get("Origin")
	origins := list("allowOrigins")
	if origin == "" || !(slices.Contains(origins, "*") || slices.Contains(origins, origin)) {
		return false
	}
	creds, _ := cfg["allowCredentials"].(bool)
	h := c.W.Header()
	if slices.Contains(origins, "*") && !creds {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
	}
	if creds {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	if ex := list("exposeHeaders"); len(ex) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(ex, ","))
	}
	if c.R.Method != http.MethodOptions || c.R.Header.Get("Access-Control-Request-Method") == "" {
		return false
	}
	if m := list("allowMethods"); len(m) > 0 {
		h.Set("Access-Control-Allow-Methods", strings.Join(m, ","))
	}
	if hd := list("allowHeaders"); len(hd) > 0 {
		h.Set("Access-Control-Allow-Headers", strings.Join(hd, ","))
	}
	if ma, ok := cfg["maxAge"].(float64); ok {
		h.Set("Access-Control-Max-Age", strconv.Itoa(int(ma)))
	}
	return true
}
