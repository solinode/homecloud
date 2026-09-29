// Package ssm implements Systems Manager Parameter Store: hierarchical
// configuration parameters (String, StringList, SecureString) with version
// history and labels. SecureString values are encrypted with KMS.
package ssm

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	Type         string    `json:"type,omitempty"`
	KeyID        string    `json:"key_id,omitempty"`
	LastModified time.Time `json:"last_modified"`
	ModifiedBy   string    `json:"modified_by"`
	Labels       []string  `json:"labels,omitempty"`
}

type Parameter struct {
	Name        string    `json:"name"`
	ARN         string    `json:"arn"`
	Type        string    `json:"type"` // type of the current version
	KeyID       string    `json:"key_id,omitempty"`
	Description string    `json:"description"`
	DataType    string    `json:"data_type"`
	History     []version `json:"history"`
	Tags        core.Tags `json:"tags,omitempty"`
}

func (p Parameter) current() version { return p.History[len(p.History)-1] }

// typeOf and keyOf fall back to the parameter's fields for versions stored before
// they were recorded per version.
func (p Parameter) typeOf(v version) string {
	if v.Type != "" {
		return v.Type
	}
	return p.Type
}

func (p Parameter) keyOf(v version) string {
	if v.Type != "" {
		return v.KeyID
	}
	return p.KeyID
}

type Service struct {
	env *svc.Env
	kms *kms.Service
	mu  sync.Mutex // serialises creation of new parameters
}

func New(env *svc.Env, k *kms.Service) *Service { return &Service{env: env, kms: k} }

var nameRe = regexp.MustCompile(`^/?[a-zA-Z0-9_.\-/]+$`)

func arnName(name string) string { return strings.TrimPrefix(name, "/") }

func (s *Service) Routes(r *httpx.Router) {
	// Parameter names contain slashes, so they travel in the ?name= query
	// parameter, and handlers authorize against the parameter's ARN.
	d := httpx.Deferred()
	r.Handle("GET /api/v1/ssm/parameters", "ssm:DescribeParameters", s.list)
	r.Handle("GET /api/v1/ssm/parameter", "ssm:GetParameter", s.get, d)
	r.Handle("PUT /api/v1/ssm/parameter", "ssm:PutParameter", s.put, d)
	r.Handle("DELETE /api/v1/ssm/parameter", "ssm:DeleteParameter", s.delete, d)
	r.Handle("GET /api/v1/ssm/parameter/history", "ssm:GetParameterHistory", s.history, d)
	r.Handle("POST /api/v1/ssm/parameter/labels", "ssm:LabelParameterVersion", s.label, d)
	r.Handle("GET /api/v1/ssm/parameters-by-path", "ssm:GetParametersByPath", s.byPath, d)
}

func (s *Service) arn(name string) string { return s.env.ARN("ssm", "parameter/"+arnName(name)) }

func notFound(name string) error {
	return core.Errf(http.StatusNotFound, "ParameterNotFound", "parameter %q does not exist", name)
}

// load reads the parameter named by ?name= (optionally name:version or
// name:label), authorizes action on it and returns it with the selected version.
func (s *Service) load(c *httpx.Ctx, action string) (Parameter, version, error) {
	name := c.Query("name")
	sel := ""
	if n, v, ok := strings.Cut(name, ":"); ok {
		name, sel = n, v
	}
	if err := c.Authorize(action, s.arn(name)); err != nil {
		return Parameter{}, version{}, err
	}
	p, err := store.Get[Parameter](s.env.Store, cParams, name)
	if err != nil {
		return p, version{}, notFound(name)
	}
	if sel == "" {
		return p, p.current(), nil
	}
	idx := slices.IndexFunc(p.History, func(v version) bool {
		return sel == strconv.Itoa(v.Version) || slices.Contains(v.Labels, sel)
	})
	if idx < 0 {
		return p, version{}, core.Errf(http.StatusNotFound, "ParameterVersionNotFound", "no version or label %q for %q", sel, name)
	}
	return p, p.History[idx], nil
}

// value returns a version's value, decrypting SecureStrings when asked and allowed.
func (s *Service) value(c *httpx.Ctx, p Parameter, v version, decrypt bool) (string, error) {
	if p.typeOf(v) != "SecureString" {
		return v.Value, nil
	}
	if !decrypt {
		return v.Value, nil
	}
	if err := c.Authorize("kms:Decrypt", p.keyOf(v)); err != nil {
		return "", err
	}
	pt, _, err := s.kms.Decrypt(v.Value, map[string]string{"PARAMETER_ARN": p.ARN})
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func (s *Service) render(c *httpx.Ctx, p Parameter, v version, decrypt bool) (map[string]any, error) {
	val, err := s.value(c, p, v, decrypt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": p.Name, "arn": p.ARN, "type": p.typeOf(v), "value": val, "version": v.Version,
		"last_modified": v.LastModified, "data_type": p.DataType, "labels": v.Labels}, nil
}

func (s *Service) summary(p Parameter) map[string]any {
	cur := p.current()
	return map[string]any{"name": p.Name, "arn": p.ARN, "type": p.typeOf(cur), "key_id": p.keyOf(cur), "description": p.Description,
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
	p, v, err := s.load(c, "ssm:GetParameter")
	if err != nil {
		return nil, err
	}
	return s.render(c, p, v, c.Query("with_decryption") == "true")
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
	if in.Type != "" && in.Type != "String" && in.Type != "StringList" && in.Type != "SecureString" {
		return nil, core.BadRequest("type must be String, StringList or SecureString")
	}
	arn := s.arn(in.Name)
	if err := c.Authorize("ssm:PutParameter", arn); err != nil {
		return nil, err
	}
	// newVersion builds the next version for a parameter (encrypting when needed).
	newVersion := func(p *Parameter) error {
		typ := in.Type
		if typ == "" {
			typ = p.Type
		}
		v := version{Value: in.Value, Type: typ, LastModified: core.Now(), ModifiedBy: c.P.ARN}
		if typ == "SecureString" {
			key := in.KeyID
			if key == "" && len(p.History) > 0 {
				key = p.keyOf(p.current())
			}
			if key == "" {
				var err error
				if key, err = s.kms.ManagedKey(defaultKey); err != nil {
					return err
				}
			}
			blob, keyARN, err := s.kms.Encrypt(key, []byte(in.Value), map[string]string{"PARAMETER_ARN": arn})
			if err != nil {
				return err
			}
			if err := c.Authorize("kms:Encrypt", keyARN); err != nil {
				return err
			}
			v.Value, v.KeyID = blob, keyARN
		}
		v.Version = 1
		if len(p.History) > 0 {
			v.Version = p.current().Version + 1
		}
		p.History = append(p.History, v)
		if len(p.History) > maxHistory {
			p.History = p.History[len(p.History)-maxHistory:]
		}
		p.Type, p.KeyID = v.Type, v.KeyID
		if in.Description != nil {
			p.Description = *in.Description
		}
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if store.Has(s.env.Store, cParams, in.Name) {
		if !in.Overwrite {
			return nil, core.Errf(http.StatusConflict, "ParameterAlreadyExists", "parameter %q exists; set overwrite to update it", in.Name)
		}
		p, err := store.Update(s.env.Store, cParams, in.Name, func(p *Parameter) error {
			p.History = slices.Clone(p.History) // fn may run again on retry
			return newVersion(p)
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"version": p.current().Version, "tier": "Standard"}, nil
	}
	p := Parameter{Name: in.Name, ARN: arn, Type: "String", DataType: "text", Tags: in.Tags}
	if err := newVersion(&p); err != nil {
		return nil, err
	}
	if err := store.Put(s.env.Store, cParams, p.Name, p); err != nil {
		return nil, err
	}
	return map[string]any{"version": 1, "tier": "Standard"}, nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	p, _, err := s.load(c, "ssm:DeleteParameter")
	if err != nil {
		return nil, err
	}
	return nil, store.Delete(s.env.Store, cParams, p.Name)
}

func (s *Service) history(c *httpx.Ctx) (any, error) {
	p, _, err := s.load(c, "ssm:GetParameterHistory")
	if err != nil {
		return nil, err
	}
	decrypt := c.Query("with_decryption") == "true"
	out := []map[string]any{}
	for _, v := range p.History {
		val := v.Value
		if p.typeOf(v) == "SecureString" {
			val = "****"
			if decrypt {
				if val, err = s.value(c, p, v, true); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, map[string]any{"version": v.Version, "type": p.typeOf(v), "value": val, "last_modified": v.LastModified,
			"modified_by": v.ModifiedBy, "labels": v.Labels})
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
	if err := c.Authorize("ssm:LabelParameterVersion", s.arn(in.Name)); err != nil {
		return nil, err
	}
	for _, l := range in.Labels {
		if l == "" || len(l) > 100 || strings.ContainsAny(l, ":/ ") || l[0] >= '0' && l[0] <= '9' {
			return nil, core.BadRequest("label %q is invalid (no : / or spaces, and may not start with a digit)", l)
		}
	}
	p, err := store.Update(s.env.Store, cParams, in.Name, func(p *Parameter) error {
		p.History = slices.Clone(p.History)
		found := false
		for i := range p.History {
			labels := slices.Clone(p.History[i].Labels)
			for _, l := range in.Labels { // a label points at one version at a time
				labels = slices.DeleteFunc(labels, func(x string) bool { return x == l })
			}
			if p.History[i].Version == in.Version {
				labels = append(labels, in.Labels...)
				found = true
			}
			p.History[i].Labels = labels
		}
		if !found {
			return core.Errf(http.StatusNotFound, "ParameterVersionNotFound", "version %d not found", in.Version)
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(in.Name)
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
		// Parameters the caller cannot read (or decrypt) are left out, not failed.
		if c.Authorize("ssm:GetParametersByPath", p.ARN) != nil {
			continue
		}
		v, err := s.render(c, p, p.current(), decrypt)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}
