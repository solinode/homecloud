package lambda

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// publicBase is the API's base URL as clients reach it.
func (s *Service) publicBase() string {
	_, port, _ := strings.Cut(s.env.Cfg.APIAddr, ":")
	scheme := "http"
	if s.env.Cfg.TLSCert != "" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%s", scheme, s.env.Cfg.PublicHost, port)
}

// ---- function URLs ----

type urlInput struct {
	AuthType   string
	Cors       *URLCors
	InvokeMode string
}

func normalizeAuth(a string) (string, error) {
	switch a {
	case "NONE":
		return "NONE", nil
	case "AWS_IAM", "HC_IAM":
		return "HC_IAM", nil
	}
	return "", core.BadRequest("AuthType must be NONE or AWS_IAM")
}

// putURL creates (or, with update, changes) a function's URL. qual may name an alias.
func (s *Service) putURL(name, qual string, in urlInput, update bool) (Function, error) {
	cur, err := s.getLatest(name)
	if err != nil {
		return cur, err
	}
	if qual == latest {
		qual = ""
	}
	if qual != "" {
		if isVersionNumber(qual) {
			return cur, core.BadRequest("function URLs can be attached to $LATEST or an alias, not a version")
		}
		if _, err := s.getAlias(name, qual); err != nil {
			return cur, err
		}
	}
	if in.InvokeMode != "" && in.InvokeMode != "BUFFERED" && in.InvokeMode != "RESPONSE_STREAM" {
		return cur, core.BadRequest("InvokeMode must be BUFFERED or RESPONSE_STREAM")
	}
	var auth string
	if in.AuthType != "" {
		if auth, err = normalizeAuth(in.AuthType); err != nil {
			return cur, err
		}
	}
	return s.modify(name, func(f *Function) error {
		now := core.Now()
		switch {
		case update && (!f.URL.Enabled || f.URL.Qualifier != qual):
			return core.Errf(http.StatusNotFound, "ResourceNotFound", "The resource you requested does not exist.")
		case !update && f.URL.Enabled:
			return core.Conflict("Failed to create function url config for [functionArn = %s]. Error message:  FunctionUrlConfig exists for this Lambda function", f.qualifiedARN(qual))
		case !update:
			if auth == "" {
				return core.BadRequest("AuthType is required")
			}
			f.URL = FunctionURL{Enabled: true, Qualifier: qual, CreatedAt: &now, InvokeMode: "BUFFERED"}
		}
		if auth != "" {
			f.URL.AuthType = auth
		}
		if in.Cors != nil {
			f.URL.Cors = in.Cors
		}
		if in.InvokeMode != "" {
			f.URL.InvokeMode = in.InvokeMode
		}
		f.URL.URL = s.urlFor(name)
		f.URL.LastModified = &now
		return nil
	})
}

// setURL is the native API's switch: enable (or reconfigure) or disable a function's URL.
func (s *Service) setURL(name string, enabled bool, auth string) (Function, error) {
	if !enabled {
		return s.modify(name, func(f *Function) error { f.URL = FunctionURL{AuthType: "NONE"}; return nil })
	}
	if auth == "" {
		auth = "NONE"
	}
	f, err := s.getLatest(name)
	if err != nil {
		return f, err
	}
	return s.putURL(name, f.URL.Qualifier, urlInput{AuthType: auth}, f.URL.Enabled)
}

// corsHeaders applies a function URL's CORS configuration to a response.
func corsHeaders(w http.ResponseWriter, r *http.Request, c *URLCors) {
	if c == nil {
		return
	}
	origin := r.Header.Get("Origin")
	h := w.Header()
	switch {
	case slices.Contains(c.AllowOrigins, "*"):
		h.Set("Access-Control-Allow-Origin", "*")
	case origin != "" && slices.Contains(c.AllowOrigins, origin):
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
	default:
		return
	}
	if c.AllowCredentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	if len(c.ExposeHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(c.ExposeHeaders, ", "))
	}
	if r.Method == http.MethodOptions {
		if len(c.AllowMethods) > 0 {
			h.Set("Access-Control-Allow-Methods", strings.Join(c.AllowMethods, ", "))
		}
		if len(c.AllowHeaders) > 0 {
			h.Set("Access-Control-Allow-Headers", strings.Join(c.AllowHeaders, ", "))
		}
		if c.MaxAge > 0 {
			h.Set("Access-Control-Max-Age", strconv.Itoa(c.MaxAge))
		}
	}
}

// invokeURL runs a function for a function URL request (already authorized).
func (s *Service) invokeURL(w http.ResponseWriter, r *http.Request, f Function, path string) error {
	corsHeaders(w, r, f.URL.Cors)
	if f.URL.Cors != nil && r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		w.WriteHeader(http.StatusOK)
		return nil
	}
	ev, err := s.httpEvent(r, path, "$default", "", nil)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(ev)
	res, err := s.InvokeWith(r.Context(), f.Name, payload, InvokeOptions{Qualifier: f.URL.Qualifier})
	if err != nil {
		return err
	}
	respond(w, res, false)
	return nil
}

// serveURL handles a function URL: /lambda-url/{name}/{path...}. Requests
// signed with SigV4 arrive through the AWS API instead (awsServeURL).
func (s *Service) serveURL(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	f, err := s.getLatest(name)
	if err != nil || !f.URL.Enabled {
		return nil, core.Errf(http.StatusNotFound, "NotFound", "no function URL is configured for %q", name)
	}
	if c.R.URL.Query().Has("access_token") {
		return nil, core.BadRequest("function URLs do not accept access_token; send an Authorization header")
	}
	if f.URL.AuthType == "HC_IAM" {
		if s.Auth == nil {
			return nil, core.Errf(http.StatusForbidden, "AccessDenied", "IAM auth unavailable")
		}
		p, err := s.Auth.Authenticate(c.R)
		if err != nil {
			return nil, err
		}
		if !p.Can("lambda:InvokeFunctionUrl", f.ARN) && !s.policyAllows(name, f.URL.Qualifier, p, "lambda:InvokeFunctionUrl") {
			return nil, core.Errf(http.StatusForbidden, "AccessDenied", "%s may not invoke %s", p.ARN, f.ARN)
		}
	}
	c.MarkWritten()
	if err := s.invokeURL(c.W, c.R, f, "/"+c.Param("path")); err != nil {
		httpx.WriteError(c.W, err)
	}
	return nil, nil
}

// ---- code download links ----

// signedLink returns a time-limited download URL for a stored package.
func (s *Service) signedLink(kind, name, version string) string {
	exp := time.Now().Add(10 * time.Minute).Unix()
	msg := kind + "\n" + name + "\n" + version + "\n" + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, s.urlKey)
	mac.Write([]byte(msg))
	return s.publicBase() + "/lambda-code/" + base64.RawURLEncoding.EncodeToString([]byte(msg)) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) codeURL(f Function) string { return s.signedLink("fn", f.Name, f.version()) }

func (s *Service) layerURL(lv LayerVersion) string {
	return s.signedLink("layer", lv.Name, strconv.Itoa(lv.Version))
}

// serveCode serves a package named by a signed link (GetFunction Code.Location).
func (s *Service) serveCode(c *httpx.Ctx) (any, error) {
	denied := core.Errf(http.StatusForbidden, "AccessDenied", "the link is invalid or has expired")
	enc, sig, ok := strings.Cut(c.Param("token"), ".")
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if !ok || err != nil {
		return nil, denied
	}
	mac := hmac.New(sha256.New, s.urlKey)
	mac.Write(raw)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return nil, denied
	}
	parts := strings.Split(string(raw), "\n")
	if len(parts) != 4 {
		return nil, denied
	}
	if exp, _ := strconv.ParseInt(parts[3], 10, 64); time.Now().Unix() > exp {
		return nil, denied
	}
	var file string
	switch parts[0] {
	case "fn":
		file = s.codeFile(Function{Name: parts[1], Version: parts[2]})
	case "layer":
		v, _ := strconv.Atoi(parts[2])
		file = s.layerFile(LayerVersion{Name: parts[1], Version: v})
	default:
		return nil, denied
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, core.NotFound("package", parts[1])
	}
	c.W.Header().Set("Content-Type", "application/zip")
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+parts[1]+`.zip"`)
	c.MarkWritten()
	c.W.Write(b)
	return nil, nil
}
