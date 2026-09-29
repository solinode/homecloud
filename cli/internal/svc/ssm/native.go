package ssm

import (
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

func (s *Service) Routes(r *httpx.Router) {
	// Parameter names contain slashes, so they travel in the ?name= query
	// parameter, and handlers authorize against the parameter's ARN.
	d := httpx.Deferred()
	r.Handle("GET /api/v1/ssm/parameters", "ssm:DescribeParameters", s.list)
	r.Handle("GET /api/v1/ssm/parameter", "ssm:GetParameter", s.nativeGet, d)
	r.Handle("PUT /api/v1/ssm/parameter", "ssm:PutParameter", s.nativePut, d)
	r.Handle("DELETE /api/v1/ssm/parameter", "ssm:DeleteParameter", s.nativeDelete, d)
	r.Handle("GET /api/v1/ssm/parameter/history", "ssm:GetParameterHistory", s.nativeHistory, d)
	r.Handle("POST /api/v1/ssm/parameter/labels", "ssm:LabelParameterVersion", s.nativeLabel, d)
	r.Handle("POST /api/v1/ssm/parameter/unlabel", "ssm:UnlabelParameterVersion", s.nativeUnlabel, d)
	r.Handle("GET /api/v1/ssm/parameters-by-path", "ssm:GetParametersByPath", s.nativeByPath, d)
}

func (s *Service) render(p Parameter, v version, val string) map[string]any {
	return map[string]any{"name": p.Name, "arn": p.ARN, "type": p.typeOf(v), "value": val, "version": v.Version,
		"last_modified": v.LastModified, "data_type": p.dataType(), "labels": v.Labels}
}

func (s *Service) summary(p Parameter) map[string]any {
	cur := p.current()
	return map[string]any{"name": p.Name, "arn": p.ARN, "type": p.typeOf(cur), "key_id": p.keyOf(cur), "description": p.Description,
		"version": cur.Version, "last_modified": cur.LastModified, "last_modified_by": cur.ModifiedBy, "data_type": p.dataType(),
		"tier": p.tier(), "allowed_pattern": p.AllowedPattern, "tags": p.Tags}
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	prefix := c.Query("prefix")
	out := []map[string]any{}
	for _, p := range s.all() {
		if prefix == "" || strings.HasPrefix(p.Name, prefix) {
			out = append(out, s.summary(p))
		}
	}
	return out, nil
}

func (s *Service) nativeGet(c *httpx.Ctx) (any, error) {
	p, v, _, err := s.load(c.Authorize, "ssm:GetParameter", c.Query("name"))
	if err != nil {
		return nil, err
	}
	val, err := s.value(c.Authorize, p, v, c.Query("with_decryption") == "true")
	if err != nil {
		return nil, err
	}
	return s.render(p, v, val), nil
}

func (s *Service) nativePut(c *httpx.Ctx) (any, error) {
	var in struct {
		Name           string    `json:"name"`
		Value          string    `json:"value"`
		Type           string    `json:"type"`
		KeyID          string    `json:"key_id"`
		Description    *string   `json:"description"`
		Overwrite      bool      `json:"overwrite"`
		Tier           string    `json:"tier"`
		AllowedPattern string    `json:"allowed_pattern"`
		DataType       string    `json:"data_type"`
		Tags           core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	tags := in.Tags
	if in.Overwrite {
		tags = nil // the native API ignores tags on overwrite
	}
	p, err := s.put(c.Authorize, c.P.ARN, PutInput{Name: in.Name, Value: in.Value, Type: in.Type, KeyID: in.KeyID, Description: in.Description,
		Overwrite: in.Overwrite, Tier: in.Tier, AllowedPattern: in.AllowedPattern, DataType: in.DataType, Tags: tags})
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": p.current().Version, "tier": p.tier()}, nil
}

func (s *Service) nativeDelete(c *httpx.Ctx) (any, error) {
	return nil, s.remove(c.Authorize, c.Query("name"))
}

func (s *Service) nativeHistory(c *httpx.Ctx) (any, error) {
	p, _, _, err := s.load(c.Authorize, "ssm:GetParameterHistory", c.Query("name"))
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
				if val, err = s.value(c.Authorize, p, v, true); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, map[string]any{"version": v.Version, "type": p.typeOf(v), "value": val, "last_modified": v.LastModified,
			"modified_by": v.ModifiedBy, "labels": v.Labels, "description": v.description(p)})
	}
	return out, nil
}

func (s *Service) nativeLabel(c *httpx.Ctx) (any, error) {
	var in struct {
		Name    string   `json:"name"`
		Version int      `json:"version"`
		Labels  []string `json:"labels"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	for _, l := range in.Labels {
		if !validLabel(l) {
			return nil, core.BadRequest("label %q is invalid (letters, digits, . - _; not starting with a digit, aws or ssm)", l)
		}
	}
	if _, _, err := s.label(c.Authorize, in.Name, in.Version, in.Labels); err != nil {
		return nil, err
	}
	p, _, _, err := s.load(c.Authorize, "ssm:GetParameter", in.Name)
	if err != nil {
		return nil, err
	}
	return s.summary(p), nil
}

func (s *Service) nativeByPath(c *httpx.Ctx) (any, error) {
	ps, vs, vals, err := s.byPath(c.Authorize, c.Query("path"), c.Query("recursive") == "true", nil, c.Query("with_decryption") == "true", false)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for i := range ps {
		out = append(out, s.render(ps[i], vs[i], vals[i]))
	}
	return out, nil
}

func (s *Service) nativeUnlabel(c *httpx.Ctx) (any, error) {
	var in struct {
		Name    string   `json:"name"`
		Version int      `json:"version"`
		Labels  []string `json:"labels"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	removed, invalid, err := s.unlabel(c.Authorize, in.Name, in.Version, in.Labels)
	if err != nil {
		return nil, err
	}
	if removed == nil {
		removed = []string{}
	}
	if invalid == nil {
		invalid = []string{}
	}
	return map[string]any{"removed_labels": removed, "invalid_labels": invalid}, nil
}
