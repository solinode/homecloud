package secrets

import (
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Routes serves the native API. {name} may be a name or ARN; handlers
// authorize against the secret's ARN (which carries AWS's random suffix).
func (s *Service) Routes(r *httpx.Router) {
	d := httpx.Deferred()
	r.Handle("GET /api/v1/secrets", "secretsmanager:ListSecrets", s.list)
	r.Handle("POST /api/v1/secrets", "secretsmanager:CreateSecret", s.nativeCreate, d)
	r.Handle("GET /api/v1/secrets/{name}", "secretsmanager:DescribeSecret", s.nativeDescribe, d)
	r.Handle("PATCH /api/v1/secrets/{name}", "secretsmanager:UpdateSecret", s.nativeUpdate, d)
	r.Handle("GET /api/v1/secrets/{name}/value", "secretsmanager:GetSecretValue", s.nativeGetValue, d)
	r.Handle("PUT /api/v1/secrets/{name}/value", "secretsmanager:PutSecretValue", s.nativePutValue, d)
	r.Handle("DELETE /api/v1/secrets/{name}", "secretsmanager:DeleteSecret", s.nativeDelete, d)
	r.Handle("POST /api/v1/secrets/{name}/restore", "secretsmanager:RestoreSecret", s.nativeRestore, d)
	r.Handle("POST /api/v1/secrets/random-password", "secretsmanager:GetRandomPassword", s.randomPassword)
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	s.purgeDeleted()
	out := []Secret{}
	for _, sec := range store.List[Secret](s.env.Store, cSecrets) {
		sec.ARN = core.CanonicalARN(sec.ARN)
		out = append(out, sec.view())
	}
	return out, nil
}

func (s *Service) nativeCreate(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string    `json:"name"`
		Description string    `json:"description"`
		Value       string    `json:"value"`
		KMSKeyID    string    `json:"kms_key_id"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	sec, _, err := s.create(c.Authorize, CreateInput{Name: in.Name, Description: in.Description, KMSKeyID: in.KMSKeyID,
		Value: []byte(in.Value), HasValue: true, Tags: in.Tags})
	if err != nil {
		return nil, err
	}
	return sec.view(), nil
}

func (s *Service) nativeDescribe(c *httpx.Ctx) (any, error) {
	sec, err := s.describe(c.Authorize, c.Param("name"))
	if err != nil {
		return nil, err
	}
	return sec.view(), nil
}

func (s *Service) nativeUpdate(c *httpx.Ctx) (any, error) {
	var in struct {
		Description *string   `json:"description"`
		KMSKeyID    *string   `json:"kms_key_id"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	sec, _, err := s.update(c.Authorize, c.Param("name"), UpdateInput{Description: in.Description, KMSKeyID: in.KMSKeyID})
	if err != nil {
		return nil, err
	}
	if in.Tags != nil { // the native API replaces the tag set
		if err := c.Authorize("secretsmanager:TagResource", sec.ARN); err != nil {
			return nil, err
		}
		sec, err = store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
			if err := validTags(in.Tags); err != nil {
				return err
			}
			x.Tags = in.Tags
			return nil
		})
		if err != nil {
			return nil, err
		}
		sec.ARN = core.CanonicalARN(sec.ARN)
	}
	return sec.view(), nil
}

func (s *Service) nativeGetValue(c *httpx.Ctx) (any, error) {
	sec, v, p, err := s.getValue(c.Authorize, c.Param("name"), c.Query("version_id"), c.Query("version_stage"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": sec.Name, "value": string(p), "binary": v.Binary, "version_id": v.ID, "stages": v.Stages, "created_at": v.CreatedAt}, nil
}

func (s *Service) nativePutValue(c *httpx.Ctx) (any, error) {
	var in struct {
		Value string `json:"value"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	sec, _, err := s.putValue(c.Authorize, c.Param("name"), []byte(in.Value), false, "", nil)
	if err != nil {
		return nil, err
	}
	return sec.view(), nil
}

func (s *Service) nativeDelete(c *httpx.Ctx) (any, error) {
	var days *int64
	if c.Query("recovery_days") != "" {
		n := int64(c.QueryInt("recovery_days", 0))
		days = &n
	} else if c.Query("force") != "true" {
		n := int64(7)
		days = &n
	}
	sec, err := s.remove(c.Authorize, c.Param("name"), days, c.Query("force") == "true", 1)
	if err != nil {
		return nil, err
	}
	if c.Query("force") == "true" {
		return nil, nil
	}
	return sec.view(), nil
}

func (s *Service) nativeRestore(c *httpx.Ctx) (any, error) {
	sec, err := s.restore(c.Authorize, c.Param("name"))
	if err != nil {
		return nil, err
	}
	return sec.view(), nil
}

func (s *Service) randomPassword(c *httpx.Ctx) (any, error) {
	n := c.QueryInt("length", 32)
	if n < 8 || n > 4096 {
		return nil, core.BadRequest("length must be between 8 and 4096")
	}
	p, err := RandomPassword(PasswordOptions{PasswordLength: int64(n), ExcludePunctuation: c.Query("punctuation") != "true"})
	if err != nil {
		return nil, err
	}
	return map[string]string{"password": p}, nil
}
