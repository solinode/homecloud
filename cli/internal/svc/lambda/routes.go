package lambda

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Routes registers the native API (/api/v1/lambda), function URLs and code links.
func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:lambda:{region}:{account}:function:{name}")
	r.Handle("GET /api/v1/lambda/runtimes", "lambda:ListRuntimes", s.listRuntimes)
	r.Handle("GET /api/v1/lambda/account", "lambda:GetAccountSettings", s.accountRoute)
	r.Handle("GET /api/v1/lambda/functions", "lambda:ListFunctions", s.list)
	r.Handle("POST /api/v1/lambda/functions", "lambda:CreateFunction", s.create)
	r.Handle("GET /api/v1/lambda/functions/{name}", "lambda:GetFunction", s.get, res)
	r.Handle("PATCH /api/v1/lambda/functions/{name}", "lambda:UpdateFunctionConfiguration", s.updateConfigRoute, res)
	r.Handle("PUT /api/v1/lambda/functions/{name}/code", "lambda:UpdateFunctionCode", s.updateCodeRoute, res)
	r.Handle("GET /api/v1/lambda/functions/{name}/code", "lambda:GetFunction", s.getCode, res)
	r.Handle("DELETE /api/v1/lambda/functions/{name}", "lambda:DeleteFunction", s.delete, res)
	r.Handle("POST /api/v1/lambda/functions/{name}/invoke", "lambda:InvokeFunction", s.invoke, res)
	r.Handle("PUT /api/v1/lambda/functions/{name}/url", "lambda:CreateFunctionUrlConfig", s.putURLRoute, res)
	r.Handle("GET /api/v1/lambda/functions/{name}/versions", "lambda:ListVersionsByFunction", s.listVersionsRoute, res)
	r.Handle("POST /api/v1/lambda/functions/{name}/versions", "lambda:PublishVersion", s.publishRoute, res)
	r.Handle("GET /api/v1/lambda/functions/{name}/aliases", "lambda:ListAliases", s.listAliasesRoute, res)
	r.Handle("POST /api/v1/lambda/functions/{name}/aliases", "lambda:CreateAlias", s.createAliasRoute, res)
	r.Handle("PATCH /api/v1/lambda/functions/{name}/aliases/{alias}", "lambda:UpdateAlias", s.updateAliasRoute, res)
	r.Handle("DELETE /api/v1/lambda/functions/{name}/aliases/{alias}", "lambda:DeleteAlias", s.deleteAliasRoute, res)
	r.Handle("PUT /api/v1/lambda/functions/{name}/concurrency", "lambda:PutFunctionConcurrency", s.putConcurrencyRoute, res)
	r.Handle("DELETE /api/v1/lambda/functions/{name}/concurrency", "lambda:DeleteFunctionConcurrency", s.deleteConcurrencyRoute, res)
	r.Handle("GET /api/v1/lambda/functions/{name}/event-invoke-config", "lambda:GetFunctionEventInvokeConfig", s.getInvokeConfigRoute, res)
	r.Handle("PUT /api/v1/lambda/functions/{name}/event-invoke-config", "lambda:PutFunctionEventInvokeConfig", s.putInvokeConfigRoute, res)
	r.Handle("DELETE /api/v1/lambda/functions/{name}/event-invoke-config", "lambda:DeleteFunctionEventInvokeConfig", s.deleteInvokeConfigRoute, res)
	r.Handle("GET /api/v1/lambda/functions/{name}/policy", "lambda:GetPolicy", s.policyRoute, res)
	lres := httpx.Res("arn:aws:lambda:{region}:{account}:layer:{layer}")
	r.Handle("GET /api/v1/lambda/layers", "lambda:ListLayers", s.listLayersRoute)
	r.Handle("POST /api/v1/lambda/layers", "lambda:PublishLayerVersion", s.publishLayerRoute, httpx.Deferred())
	r.Handle("GET /api/v1/lambda/layers/{layer}/versions", "lambda:ListLayerVersions", s.listLayerVersionsRoute, lres)
	r.Handle("DELETE /api/v1/lambda/layers/{layer}/versions/{version}", "lambda:DeleteLayerVersion", s.deleteLayerVersionRoute, lres)
	r.Handle("/lambda-url/{name}/{path...}", "", s.serveURL, httpx.Public())
	r.Handle("GET /lambda-code/{token}", "", s.serveCode, httpx.Public())
	s.apigwRoutes(r)
	s.esmRoutes(r)
}

func (s *Service) listRuntimes(c *httpx.Ctx) (any, error) { return runtimes, nil }

func (s *Service) accountRoute(c *httpx.Ctx) (any, error) { return s.accountSettings(), nil }

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []Function{}
	for _, f := range store.List[Function](s.env.Store, cFunctions) {
		f, _ = s.getLatest(f.Name)
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	f, err := s.getLatest(c.Param("name"))
	if err != nil {
		return nil, err
	}
	n := s.warmCount(f.Name)
	state := "Idle"
	if n > 0 {
		state = "Warm"
	}
	lim := limitFor(f)
	return map[string]any{"configuration": f, "environment_state": state, "environments": n,
		"concurrent_executions": s.concurrent(f.Name), "concurrency_limit": lim,
		"aliases": nzAliases(s.aliases(f.Name)), "event_invoke_config": s.invokeConfigView(f.Name, "")}, nil
}

func nzAliases(a []Alias) []Alias {
	if a == nil {
		return []Alias{}
	}
	return a
}

func (s *Service) invokeConfigView(name, qual string) any {
	if c, ok := s.invokeConfig(name, qual); ok {
		return c
	}
	return nil
}

// createInput is the native API's CreateFunction request.
type createInput struct {
	Name string `json:"name"`
	configInput
	Code        codeInput `json:"code"`
	PackageType string    `json:"package_type"`
	ImageURI    string    `json:"image_uri"`
	Publish     bool      `json:"publish"`
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in createInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := c.Authorize("lambda:CreateFunction", s.fnARN(in.Name)); err != nil {
		return nil, err
	}
	code, err := in.Code.zip()
	if err != nil {
		return nil, err
	}
	return s.createFunction(c.R.Context(), c.Authorize, createSpec{Name: in.Name, configInput: in.configInput, PackageType: in.PackageType,
		Zip: code, ImageURI: in.ImageURI, Publish: in.Publish})
}

func (s *Service) updateConfigRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		configInput
		RevisionID string `json:"revision_id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.updateConfiguration(c.Authorize, c.Param("name"), in.RevisionID, in.configInput)
}

func (s *Service) updateCodeRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		codeInput
		ImageURI string `json:"image_uri"`
		Publish  bool   `json:"publish"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	code, err := in.zip()
	if err != nil {
		return nil, err
	}
	if code == nil && in.ImageURI == "" {
		return nil, core.BadRequest("provide zip_base64, files or image_uri")
	}
	return s.updateCode(c.Param("name"), codeSpec{Zip: code, ImageURI: in.ImageURI, Publish: in.Publish})
}

// getCode returns the function's files as text when they are small enough to edit in the console.
func (s *Service) getCode(c *httpx.Ctx) (any, error) {
	f, err := s.qualified(c.Param("name"), c.Query("qualifier"))
	if err != nil {
		return nil, err
	}
	if f.isImage() {
		return map[string]any{"files": map[string]string{}, "editable": false, "file_count": 0, "image_uri": f.ImageURI}, nil
	}
	b, err := os.ReadFile(s.codeFile(f))
	if err != nil {
		return nil, err
	}
	if c.Query("format") == "zip" {
		c.W.Header().Set("Content-Type", "application/zip")
		c.W.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`.zip"`)
		c.MarkWritten()
		c.W.Write(b)
		return nil, nil
	}
	files, err := unzip(b)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	editable := true
	for n, data := range files {
		if len(data) > 256<<10 || bytes.IndexByte(data, 0) >= 0 {
			editable = false
			continue
		}
		out[n] = string(data)
	}
	if len(files) > 50 {
		editable = false
	}
	sum := sha256.Sum256(b)
	return map[string]any{"files": out, "editable": editable, "file_count": len(files), "sha256_hex": hex.EncodeToString(sum[:])}, nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	return nil, s.deleteFunction(c.Param("name"), c.Query("qualifier"))
}

func (s *Service) invoke(c *httpx.Ctx) (any, error) {
	payload, err := io.ReadAll(io.LimitReader(c.R.Body, maxSyncPayload+1))
	if err != nil {
		return nil, err
	}
	name, qual := c.Param("name"), c.Query("qualifier")
	if c.Query("invocation_type") == "Event" {
		id, err := s.InvokeAsync(name, qual, payload, "")
		if err != nil {
			return nil, err
		}
		return c.JSON(http.StatusAccepted, map[string]any{"status_code": 202, "request_id": id})
	}
	var clientCtx string
	if cc := c.R.Header.Get("X-Amz-Client-Context"); cc != "" {
		clientCtx = cc
	}
	return s.InvokeWith(c.R.Context(), name, payload, InvokeOptions{Qualifier: qual, ClientContext: clientCtx})
}

func (s *Service) putURLRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Enabled  bool     `json:"enabled"`
		AuthType string   `json:"auth_type"`
		Cors     *URLCors `json:"cors"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	f, err := s.setURL(c.Param("name"), in.Enabled, in.AuthType)
	if err != nil || in.Cors == nil || !in.Enabled {
		return f, err
	}
	return s.putURL(c.Param("name"), f.URL.Qualifier, urlInput{Cors: in.Cors}, true)
}

func (s *Service) listVersionsRoute(c *httpx.Ctx) (any, error) { return s.versions(c.Param("name")) }

func (s *Service) publishRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Description string `json:"description"`
		CodeSHA256  string `json:"code_sha256"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.publishVersion(c.Param("name"), in.Description, in.CodeSHA256, "")
}

func (s *Service) listAliasesRoute(c *httpx.Ctx) (any, error) {
	if _, err := s.getLatest(c.Param("name")); err != nil {
		return nil, err
	}
	return nzAliases(s.aliases(c.Param("name"))), nil
}

func (s *Service) createAliasRoute(c *httpx.Ctx) (any, error) {
	var in aliasInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.createAlias(c.Param("name"), in)
}

func (s *Service) updateAliasRoute(c *httpx.Ctx) (any, error) {
	var in aliasInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if _, err := s.getLatest(c.Param("name")); err != nil {
		return nil, err
	}
	return s.updateAlias(c.Param("name"), c.Param("alias"), in)
}

func (s *Service) deleteAliasRoute(c *httpx.Ctx) (any, error) {
	if _, err := s.getLatest(c.Param("name")); err != nil {
		return nil, err
	}
	return nil, s.deleteAlias(c.Param("name"), c.Param("alias"))
}

func (s *Service) putConcurrencyRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Reserved *int `json:"reserved_concurrent_executions"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Reserved == nil {
		return nil, core.BadRequest("reserved_concurrent_executions is required")
	}
	return s.putConcurrency(c.Param("name"), *in.Reserved)
}

func (s *Service) deleteConcurrencyRoute(c *httpx.Ctx) (any, error) {
	return nil, s.deleteConcurrency(c.Param("name"))
}

func (s *Service) getInvokeConfigRoute(c *httpx.Ctx) (any, error) {
	name, qual := c.Param("name"), c.Query("qualifier")
	if _, _, err := s.resolve(name, qual); err != nil {
		return nil, err
	}
	cfg, ok := s.invokeConfig(name, qual)
	if !ok {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFound", "function %s has no asynchronous invocation config", name)
	}
	return cfg, nil
}

func (s *Service) putInvokeConfigRoute(c *httpx.Ctx) (any, error) {
	var in invokeConfigInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.putInvokeConfig(c.Authorize, c.Param("name"), c.Query("qualifier"), in, false)
}

func (s *Service) deleteInvokeConfigRoute(c *httpx.Ctx) (any, error) {
	return nil, s.deleteInvokeConfig(c.Param("name"), c.Query("qualifier"))
}

func (s *Service) policyRoute(c *httpx.Ctx) (any, error) {
	f, err := s.getLatest(c.Param("name"))
	if err != nil {
		return nil, err
	}
	sts := f.Policy[c.Query("qualifier")]
	if sts == nil {
		sts = []PolicyStatement{}
	}
	return map[string]any{"Version": "2012-10-17", "Id": "default", "Statement": sts}, nil
}

func (s *Service) listLayersRoute(c *httpx.Ctx) (any, error) {
	return s.layers(c.Query("runtime"), c.Query("architecture")), nil
}

func (s *Service) publishLayerRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Name string `json:"name"`
		layerInput
		codeInput
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := c.Authorize("lambda:PublishLayerVersion", s.layerARN(in.Name)); err != nil {
		return nil, err
	}
	var zipped []byte
	var err error
	if in.ZipBase64 != "" {
		if zipped, err = base64.StdEncoding.DecodeString(in.ZipBase64); err != nil {
			return nil, core.BadRequest("zip_base64 is not valid base64")
		}
	} else if zipped, err = in.zip(); err != nil {
		return nil, err
	}
	return s.publishLayer(in.Name, zipped, in.layerInput)
}

func (s *Service) listLayerVersionsRoute(c *httpx.Ctx) (any, error) {
	return s.layerVersions(c.Param("layer"), c.Query("runtime"), c.Query("architecture")), nil
}

func (s *Service) deleteLayerVersionRoute(c *httpx.Ctx) (any, error) {
	v, err := strconv.Atoi(c.Param("version"))
	if err != nil {
		return nil, core.BadRequest("invalid version %q", c.Param("version"))
	}
	return nil, s.deleteLayerVersion(c.Param("layer"), v)
}
