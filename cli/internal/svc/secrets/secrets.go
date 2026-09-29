// Package secrets implements Secrets Manager: versioned secret values encrypted
// at rest with AES-256-GCM under a per-installation master key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	cSecrets     = "secrets"
	maxVersions  = 10
	stageCurrent = "AWSCURRENT"
	stagePrev    = "AWSPREVIOUS"
)

type Version struct {
	ID         string    `json:"id"`
	Stages     []string  `json:"stages"`
	Ciphertext string    `json:"ciphertext,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Secret struct {
	Name         string     `json:"name"`
	ARN          string     `json:"arn"`
	Description  string     `json:"description"`
	ManagedBy    string     `json:"managed_by,omitempty"` // e.g. "rds" for generated database credentials
	Versions     []Version  `json:"versions"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastAccessed *time.Time `json:"last_accessed,omitempty"`
	DeletionDate *time.Time `json:"deletion_date,omitempty"`
	Tags         core.Tags  `json:"tags,omitempty"`
}

func (s Secret) view() Secret {
	vs := make([]Version, len(s.Versions))
	for i, v := range s.Versions {
		v.Ciphertext = ""
		vs[i] = v
	}
	s.Versions = vs
	return s
}

type Service struct {
	env  *svc.Env
	aead cipher.AEAD
	mu   sync.Mutex // serialises creation
}

// managed rejects API changes to secrets that a service owns (RDS master
// credentials, the S3 root keys); they change through that service.
func managed(sec Secret) error {
	if sec.ManagedBy != "" {
		return core.Errf(http.StatusConflict, "ManagedSecret", "secret %q is managed by %s and cannot be changed directly", sec.Name, sec.ManagedBy)
	}
	return nil
}

// readCheck adds the service-level permission some managed secrets need.
func readCheck(c *httpx.Ctx, sec Secret) error {
	if sec.ManagedBy == "s3" { // MinIO root keys are as powerful as s3:AdministerServiceCredentials
		return c.Authorize("s3:AdministerServiceCredentials", "*")
	}
	return nil
}

func New(env *svc.Env) (*Service, error) {
	key, err := loadKey(env.Cfg.Path("master.key"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{env: env, aead: aead}, nil
}

func loadKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("master key %s is corrupt", path)
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, os.WriteFile(path, b, 0o600)
}

func (s *Service) Encrypt(plain []byte) string {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, nil))
}

func (s *Service) Decrypt(ct string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(ct)
	if err != nil || len(b) < s.aead.NonceSize() {
		return nil, errors.New("corrupt ciphertext")
	}
	n := s.aead.NonceSize()
	return s.aead.Open(nil, b[:n], b[n:], nil)
}

var nameRe = regexp.MustCompile(`^[\w/+=.@!-]{1,512}$`)

// Put creates the secret or adds a new current version; used by other services.
func (s *Service) Put(name, value, description, managedBy string) (Secret, error) {
	if !nameRe.MatchString(name) {
		return Secret{}, core.BadRequest("secret name %q is invalid", name)
	}
	v := Version{ID: core.RandHex(32), Stages: []string{stageCurrent}, Ciphertext: s.Encrypt([]byte(value)), CreatedAt: core.Now()}
	if store.Has(s.env.Store, cSecrets, name) {
		return store.Update(s.env.Store, cSecrets, name, func(sec *Secret) error {
			if sec.DeletionDate != nil {
				return core.Errf(http.StatusBadRequest, "InvalidRequest", "secret %q is scheduled for deletion; restore it first", name)
			}
			for i := range sec.Versions {
				switch {
				case contains(sec.Versions[i].Stages, stageCurrent):
					sec.Versions[i].Stages = []string{stagePrev}
				case contains(sec.Versions[i].Stages, stagePrev):
					sec.Versions[i].Stages = []string{}
				}
			}
			sec.Versions = append([]Version{v}, sec.Versions...)
			if len(sec.Versions) > maxVersions {
				sec.Versions = sec.Versions[:maxVersions]
			}
			sec.UpdatedAt = core.Now()
			return nil
		})
	}
	sec := Secret{Name: name, ARN: s.env.ARN("secretsmanager", "secret:"+name), Description: description, ManagedBy: managedBy,
		Versions: []Version{v}, CreatedAt: core.Now(), UpdatedAt: core.Now()}
	return sec, store.Put(s.env.Store, cSecrets, name, sec)
}

// Value returns the plaintext of the version with the given stage (AWSCURRENT when empty) or ID.
func (s *Service) Value(name, stage, versionID string) (string, Version, error) {
	sec, err := store.Get[Secret](s.env.Store, cSecrets, name)
	if err != nil {
		return "", Version{}, core.NotFound("secret", name)
	}
	if sec.DeletionDate != nil {
		return "", Version{}, core.Errf(http.StatusBadRequest, "InvalidRequest", "secret %q is scheduled for deletion", name)
	}
	if stage == "" && versionID == "" {
		stage = stageCurrent
	}
	for _, v := range sec.Versions {
		if (versionID != "" && v.ID == versionID) || (versionID == "" && contains(v.Stages, stage)) {
			p, err := s.Decrypt(v.Ciphertext)
			if err != nil {
				return "", v, err
			}
			_, _ = store.Update(s.env.Store, cSecrets, name, func(sec *Secret) error { n := core.Now(); sec.LastAccessed = &n; return nil })
			v.Ciphertext = ""
			return string(p), v, nil
		}
	}
	return "", Version{}, core.NotFound("secret version", name)
}

// Remove deletes a secret immediately (used when its owning resource is deleted).
func (s *Service) Remove(name string) { _ = store.Delete(s.env.Store, cSecrets, name) }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// PurgeExpired permanently deletes secrets whose recovery window has passed.
func (s *Service) PurgeExpired() {
	for _, sec := range store.List[Secret](s.env.Store, cSecrets) {
		if sec.DeletionDate != nil && time.Now().After(*sec.DeletionDate) {
			_ = store.Delete(s.env.Store, cSecrets, sec.Name)
		}
	}
}

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:secretsmanager:local-1:{account}:secret:{name}")
	r.Handle("GET /api/v1/secrets", "secretsmanager:ListSecrets", s.list)
	r.Handle("POST /api/v1/secrets", "secretsmanager:CreateSecret", s.create)
	r.Handle("GET /api/v1/secrets/{name}", "secretsmanager:DescribeSecret", s.describe, res)
	r.Handle("PATCH /api/v1/secrets/{name}", "secretsmanager:UpdateSecret", s.update, res)
	r.Handle("GET /api/v1/secrets/{name}/value", "secretsmanager:GetSecretValue", s.getValue, res)
	r.Handle("PUT /api/v1/secrets/{name}/value", "secretsmanager:PutSecretValue", s.putValue, res)
	r.Handle("DELETE /api/v1/secrets/{name}", "secretsmanager:DeleteSecret", s.delete, res)
	r.Handle("POST /api/v1/secrets/{name}/restore", "secretsmanager:RestoreSecret", s.restore, res)
	r.Handle("POST /api/v1/secrets/random-password", "secretsmanager:GetRandomPassword", s.randomPassword)
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	s.PurgeExpired()
	out := []Secret{}
	for _, sec := range store.List[Secret](s.env.Store, cSecrets) {
		out = append(out, sec.view())
	}
	return out, nil
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string    `json:"name"`
		Description string    `json:"description"`
		Value       string    `json:"value"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if store.Has(s.env.Store, cSecrets, in.Name) {
		return nil, core.Conflict("secret %q already exists", in.Name)
	}
	sec, err := s.Put(in.Name, in.Value, in.Description, "")
	if err != nil {
		return nil, err
	}
	if in.Tags != nil {
		sec, err = store.Update(s.env.Store, cSecrets, in.Name, func(x *Secret) error { x.Tags = in.Tags; return nil })
	}
	return sec.view(), err
}

func (s *Service) describe(c *httpx.Ctx) (any, error) {
	sec, err := store.Get[Secret](s.env.Store, cSecrets, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("secret", c.Param("name"))
	}
	return sec.view(), nil
}

func (s *Service) update(c *httpx.Ctx) (any, error) {
	var in struct {
		Description *string   `json:"description"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := store.Update(s.env.Store, cSecrets, c.Param("name"), func(x *Secret) error {
		if err := managed(*x); err != nil {
			return err
		}
		if in.Description != nil {
			x.Description = *in.Description
		}
		if in.Tags != nil {
			x.Tags = in.Tags
		}
		x.UpdatedAt = core.Now()
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("secret", c.Param("name"))
	}
	return sec.view(), err
}

func (s *Service) getValue(c *httpx.Ctx) (any, error) {
	if sec, err := store.Get[Secret](s.env.Store, cSecrets, c.Param("name")); err == nil {
		if err := readCheck(c, sec); err != nil {
			return nil, err
		}
	}
	val, v, err := s.Value(c.Param("name"), c.Query("version_stage"), c.Query("version_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": c.Param("name"), "value": val, "version_id": v.ID, "stages": v.Stages, "created_at": v.CreatedAt}, nil
}

func (s *Service) putValue(c *httpx.Ctx) (any, error) {
	var in struct {
		Value string `json:"value"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	cur, err := store.Get[Secret](s.env.Store, cSecrets, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("secret", c.Param("name"))
	}
	if err := managed(cur); err != nil {
		return nil, err
	}
	sec, err := s.Put(c.Param("name"), in.Value, "", "")
	if err != nil {
		return nil, err
	}
	return sec.view(), nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	cur, err := store.Get[Secret](s.env.Store, cSecrets, name)
	if err != nil {
		return nil, core.NotFound("secret", name)
	}
	if err := managed(cur); err != nil {
		return nil, err
	}
	if c.Query("force") == "true" {
		return nil, store.Delete(s.env.Store, cSecrets, name)
	}
	days := c.QueryInt("recovery_days", 7)
	if days < 1 || days > 30 {
		return nil, core.BadRequest("recovery_days must be between 1 and 30")
	}
	sec, err := store.Update(s.env.Store, cSecrets, name, func(x *Secret) error {
		d := core.Now().Add(time.Duration(days) * 24 * time.Hour)
		x.DeletionDate = &d
		return nil
	})
	return sec.view(), err
}

func (s *Service) restore(c *httpx.Ctx) (any, error) {
	sec, err := store.Update(s.env.Store, cSecrets, c.Param("name"), func(x *Secret) error { x.DeletionDate = nil; return nil })
	if err == store.ErrNotFound {
		return nil, core.NotFound("secret", c.Param("name"))
	}
	return sec.view(), err
}

func (s *Service) randomPassword(c *httpx.Ctx) (any, error) {
	n := c.QueryInt("length", 32)
	if n < 8 || n > 4096 {
		return nil, core.BadRequest("length must be between 8 and 4096")
	}
	return map[string]string{"password": core.NewSecret(n)}, nil
}
