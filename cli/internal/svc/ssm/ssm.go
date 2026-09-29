// Package ssm implements Systems Manager Parameter Store: hierarchical
// configuration parameters (String, StringList, SecureString) with version
// history. SecureString values are encrypted with KMS.
package ssm

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
)

const (
	cParams    = "ssm_parameters"
	maxHistory = 100
	maxValue   = 8192
	defaultKey = "alias/hc/ssm"
)

type version struct {
	Version      int       `json:"version"`
	Value        string    `json:"value"` // ciphertext blob for SecureString
	LastModified time.Time `json:"last_modified"`
	ModifiedBy   string    `json:"modified_by"`
	Labels       []string  `json:"labels,omitempty"`
}

type Parameter struct {
	Name        string    `json:"name"`
	ARN         string    `json:"arn"`
	Type        string    `json:"type"` // String | StringList | SecureString
	KeyID       string    `json:"key_id,omitempty"`
	Description string    `json:"description"`
	DataType    string    `json:"data_type"`
	History     []version `json:"history"`
	Tags        core.Tags `json:"tags,omitempty"`
}

func (p Parameter) current() version { return p.History[len(p.History)-1] }

type Service struct {
	env *svc.Env
	kms *kms.Service
}

func New(env *svc.Env, k *kms.Service) *Service { return &Service{env: env, kms: k} }

var nameRe = regexp.MustCompile(`^/?[a-zA-Z0-9_.\-/]+$`)

func arnName(name string) string { return strings.TrimPrefix(name, "/") }

func (s *Service) Routes(r *httpx.Router) {
	// Parameter names contain slashes, so they travel in the ?name= query parameter.
	r.Handle("GET /api/v1/ssm/parameters", "ssm:DescribeParameters", s.list)
	r.Handle("GET /api/v1/ssm/parameter", "ssm:GetParameter", s.get)
	r.Handle("PUT /api/v1/ssm/parameter", "ssm:PutParameter", s.put)
	r.Handle("DELETE /api/v1/ssm/parameter", "ssm:DeleteParameter", s.delete)
	r.Handle("GET /api/v1/ssm/parameter/history", "ssm:GetParameterHistory", s.history)
	r.Handle("POST /api/v1/ssm/parameter/labels", "ssm:LabelParameterVersion", s.label)
	r.Handle("GET /api/v1/ssm/parameters-by-path", "ssm:GetParametersByPath", s.byPath)
}

func (s *Service) arn(name string) string { return s.env.ARN("ssm", "parameter/"+arnName(name)) }

func (s *Service) load(c *httpx.Ctx, action string) (Parameter, error) {
	name := c.Query("name")
	sel := ""
	if n, v, ok := strings.Cut(name, ":"); ok {
		name, sel = n, v
	}
	p, err := store.Get[Parameter](s.env.Store, cParams, name)
	if err != nil {
		return p, core.Errf(http.StatusNotFound, "ParameterNotFound", "parameter %q does not exist", name)
	}
	if err := c.Authorize(action, p.ARN); err != nil {
		return p, err
	}
	if sel != "" {
		// name:3 selects a version, name:label selects a labelled version.
		idx := slices.IndexFunc(p.History, func(v version) bool {
			return sel == strconv.Itoa(v.Version) || slices.Contains(v.Labels, sel)
		})
		if idx < 0 {
			return p, core.Errf(http.StatusNotFound, "ParameterVersionNotFound", "no version or label %q for %q", sel, name)
		}
		p.History = p.History[:idx+1]
	}
	return p, nil
}

func (s *Service) render(p Parameter, decrypt bool) (map[string]any, error) {
	cur := p.current()
	val := cur.Value
	if p.Type == "SecureString" {
		if decrypt {
			pt, _, err := s.kms.Decrypt(cur.Value, map[string]string{"PARAMETER_ARN": p.ARN})
			if err != nil {
				return nil, err
			}
			val = string(pt)
		}
	}
	return map[string]any{"name": p.Name, "arn": p.ARN, "type": p.Type, "value": val, "version": cur.Version,
		"last_modified": cur.LastModified, "data_type": p.DataType, "labels": cur.Labels}, nil
}

func (s *Service) summary(p Parameter) map[string]any {
	cur := p.current()
	return map[string]any{"name": p.Name, "arn": p.ARN, "type": p.Type, "key_id": p.KeyID, "description": p.Description,
		"version": cur.Version, "last_modified": cur.LastModified, "last_modified_by": cur.ModifiedBy, "data_type": p.DataType, "tags": p.Tags}
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	prefix := c.Query("prefix")
	out := []map[string]any{}
	for _, p := range store.List[Parameter](s.env.Store, cParams) {
		if prefix == "" || strings.HasPrefix(p.Name, prefix) {
			out = append(out, s.summary(p))
		}
	}
	return out, nil
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	p, err := s.load(c, "ssm:GetParameter")
	if err != nil {
		return nil, err
	}
	if p.Type == "SecureString" && c.Query("with_decryption") == "true" {
		if err := c.Authorize("kms:Decrypt", p.KeyID); err != nil {
			return nil, err
		}
	}
	return s.render(p, c.Query("with_decryption") == "true")
}

func (s *Service) put(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string    `json:"name"`
		Value       string    `json:"value"`
		Type        string    `json:"type"`
		KeyID       string    `json:"key_id"`
		Description *string   `json:"description"`
		Overwrite   bool      `json:"overwrite"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Name) > 1011 || !nameRe.MatchString(in.Name) || strings.Contains(in.Name, "//") || (strings.Contains(in.Name, "/") && !strings.HasPrefix(in.Name, "/")) {
		return nil, core.BadRequest("parameter names use letters, digits and _.-/; hierarchical names must start with /")
	}
	if in.Value == "" || len(in.Value) > maxValue {
		return nil, core.BadRequest("value must be 1-%d characters", maxValue)
	}
	if err := c.Authorize("ssm:PutParameter", s.arn(in.Name)); err != nil {
		return nil, err
	}
	old, exists := store.Get[Parameter](s.env.Store, cParams, in.Name)
	if exists == nil && !in.Overwrite {
		return nil, core.Errf(http.StatusConflict, "ParameterAlreadyExists", "parameter %q exists; set overwrite to update it", in.Name)
	}
	p := old
	if exists != nil {
		p = Parameter{Name: in.Name, ARN: s.arn(in.Name), Type: "String", DataType: "text", Tags: in.Tags}
	}
	if in.Type != "" {
		if in.Type != "String" && in.Type != "StringList" && in.Type != "SecureString" {
			return nil, core.BadRequest("type must be String, StringList or SecureString")
		}
		p.Type = in.Type
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	value := in.Value
	if p.Type == "SecureString" {
		key := in.KeyID
		if key == "" {
			key = p.KeyID
		}
		if key == "" {
			var err error
			if key, err = s.kms.ManagedKey(defaultKey); err != nil {
				return nil, err
			}
		}
		blob, arn, err := s.kms.Encrypt(key, []byte(in.Value), map[string]string{"PARAMETER_ARN": p.ARN})
		if err != nil {
			return nil, err
		}
		value, p.KeyID = blob, arn
	} else {
		p.KeyID = ""
	}
	v := version{Version: 1, Value: value, LastModified: core.Now(), ModifiedBy: c.P.ARN}
	if len(p.History) > 0 {
		v.Version = p.current().Version + 1
	}
	p.History = append(p.History, v)
	if len(p.History) > maxHistory {
		p.History = p.History[len(p.History)-maxHistory:]
	}
	if err := store.Put(s.env.Store, cParams, p.Name, p); err != nil {
		return nil, err
	}
	return map[string]any{"version": v.Version, "tier": "Standard"}, nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	p, err := s.load(c, "ssm:DeleteParameter")
	if err != nil {
		return nil, err
	}
	return nil, store.Delete(s.env.Store, cParams, p.Name)
}

func (s *Service) history(c *httpx.Ctx) (any, error) {
	p, err := s.load(c, "ssm:GetParameterHistory")
	if err != nil {
		return nil, err
	}
	decrypt := c.Query("with_decryption") == "true"
	out := []map[string]any{}
	for _, v := range p.History {
		val := v.Value
		if p.Type == "SecureString" {
			val = "****"
			if decrypt {
				pt, _, err := s.kms.Decrypt(v.Value, map[string]string{"PARAMETER_ARN": p.ARN})
				if err != nil {
					return nil, err
				}
				val = string(pt)
			}
		}
		out = append(out, map[string]any{"version": v.Version, "value": val, "last_modified": v.LastModified, "modified_by": v.ModifiedBy, "labels": v.Labels})
	}
	return out, nil
}

func (s *Service) label(c *httpx.Ctx) (any, error) {
	var in struct {
		Name    string   `json:"name"`
		Version int      `json:"version"`
		Labels  []string `json:"labels"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	p, err := store.Update(s.env.Store, cParams, in.Name, func(p *Parameter) error {
		found := false
		for i := range p.History {
			for _, l := range in.Labels {
				// a label points at one version at a time
				p.History[i].Labels = slices.DeleteFunc(p.History[i].Labels, func(x string) bool { return x == l })
			}
			if p.History[i].Version == in.Version {
				p.History[i].Labels = append(p.History[i].Labels, in.Labels...)
				found = true
			}
		}
		if !found {
			return core.Errf(http.StatusNotFound, "ParameterVersionNotFound", "version %d not found", in.Version)
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.Errf(http.StatusNotFound, "ParameterNotFound", "parameter %q does not exist", in.Name)
	}
	if err != nil {
		return nil, err
	}
	return s.summary(p), nil
}

func (s *Service) byPath(c *httpx.Ctx) (any, error) {
	path := c.Query("path")
	if !strings.HasPrefix(path, "/") {
		return nil, core.BadRequest("path must start with /")
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	recursive := c.Query("recursive") == "true"
	decrypt := c.Query("with_decryption") == "true"
	out := []map[string]any{}
	for _, p := range store.List[Parameter](s.env.Store, cParams) {
		rest, ok := strings.CutPrefix(p.Name, path)
		if !ok || (!recursive && strings.Contains(rest, "/")) {
			continue
		}
		if !c.P.Can("ssm:GetParametersByPath", p.ARN) {
			continue
		}
		v, err := s.render(p, decrypt)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
