// Package kms implements a key management service: symmetric customer keys
// with aliases, encrypt/decrypt with encryption context, envelope data keys,
// automatic rotation (old versions still decrypt) and scheduled deletion.
// Key material is stored encrypted under the installation's master key.
package kms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
)

const (
	cKeys         = "kms_keys"
	cAliases      = "kms_aliases"
	maxPlaintext  = 4096
	blobVersion   = 1
	rotationEvery = 365 * 24 * time.Hour
)

type keyVersion struct {
	Version   int       `json:"version"`
	Material  string    `json:"material"` // AES-256 key, encrypted by the master key
	CreatedAt time.Time `json:"created_at"`
}

type Key struct {
	ID              string       `json:"id"`
	ARN             string       `json:"arn"`
	Description     string       `json:"description"`
	KeySpec         string       `json:"key_spec"`
	KeyUsage        string       `json:"key_usage"`
	State           string       `json:"state"` // Enabled | Disabled | PendingDeletion
	Managed         bool         `json:"managed"`
	RotationEnabled bool         `json:"rotation_enabled"`
	LastRotated     *time.Time   `json:"last_rotated,omitempty"`
	DeletionDate    *time.Time   `json:"deletion_date,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	Versions        []keyVersion `json:"versions"`
	Tags            core.Tags    `json:"tags,omitempty"`
}

func (k Key) view(aliases []string) map[string]any {
	return map[string]any{"id": k.ID, "arn": k.ARN, "description": k.Description, "key_spec": k.KeySpec, "key_usage": k.KeyUsage,
		"state": k.State, "managed": k.Managed, "rotation_enabled": k.RotationEnabled, "last_rotated": k.LastRotated,
		"deletion_date": k.DeletionDate, "created_at": k.CreatedAt, "key_versions": len(k.Versions), "aliases": aliases, "tags": k.Tags}
}

type alias struct {
	Name  string `json:"name"`
	KeyID string `json:"key_id"`
}

type Service struct {
	env    *svc.Env
	master *secrets.Service
}

func New(env *svc.Env, master *secrets.Service) *Service { return &Service{env: env, master: master} }

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Service) newVersion(v int) (keyVersion, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return keyVersion{}, err
	}
	return keyVersion{Version: v, Material: s.master.Encrypt(b), CreatedAt: core.Now()}, nil
}

func (s *Service) create(desc string, managed bool, tags core.Tags) (Key, error) {
	v, err := s.newVersion(1)
	if err != nil {
		return Key{}, err
	}
	id := uuid()
	k := Key{ID: id, ARN: s.env.ARN("kms", "key/"+id), Description: desc, KeySpec: "SYMMETRIC_DEFAULT", KeyUsage: "ENCRYPT_DECRYPT",
		State: "Enabled", Managed: managed, CreatedAt: core.Now(), Versions: []keyVersion{v}, Tags: tags}
	return k, store.Put(s.env.Store, cKeys, id, k)
}

// ManagedKey returns (creating on first use) the service-managed key behind an alias like "alias/hc/ssm".
func (s *Service) ManagedKey(aliasName string) (string, error) {
	if a, err := store.Get[alias](s.env.Store, cAliases, aliasName); err == nil {
		return a.KeyID, nil
	}
	k, err := s.create("Default key that protects "+strings.TrimPrefix(aliasName, "alias/hc/")+" data", true, nil)
	if err != nil {
		return "", err
	}
	return k.ID, store.Put(s.env.Store, cAliases, aliasName, alias{Name: aliasName, KeyID: k.ID})
}

// resolve accepts a key ID, key ARN, alias name or alias ARN.
func (s *Service) resolve(ref string) (Key, error) {
	id := ref
	if i := strings.Index(ref, ":key/"); i >= 0 {
		id = ref[i+5:]
	}
	if i := strings.Index(ref, ":alias/"); i >= 0 {
		ref = ref[i+1:]
	}
	if strings.HasPrefix(ref, "alias/") {
		a, err := store.Get[alias](s.env.Store, cAliases, ref)
		if err != nil {
			return Key{}, core.Errf(http.StatusNotFound, "NotFoundException", "alias %q does not exist", ref)
		}
		id = a.KeyID
	}
	k, err := store.Get[Key](s.env.Store, cKeys, id)
	if err != nil {
		return k, core.Errf(http.StatusNotFound, "NotFoundException", "key %q does not exist", ref)
	}
	return k, nil
}

func (s *Service) material(k Key, version int) ([]byte, error) {
	for _, v := range k.Versions {
		if v.Version == version {
			return s.master.Decrypt(v.Material)
		}
	}
	return nil, core.Errf(http.StatusBadRequest, "InvalidCiphertextException", "key version %d not found", version)
}

func aad(ctx map[string]string) []byte {
	if len(ctx) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + ctx[k] + "\n")
	}
	return []byte(b.String())
}

// Encrypt returns a ciphertext blob: version byte, key-id length + id, key version, nonce, sealed data.
func (s *Service) Encrypt(ref string, plaintext []byte, ctx map[string]string) (string, string, error) {
	k, err := s.resolve(ref)
	if err != nil {
		return "", "", err
	}
	if k.State != "Enabled" {
		return "", "", core.Errf(http.StatusBadRequest, "DisabledException", "key %s is %s", k.ID, k.State)
	}
	if len(plaintext) == 0 || len(plaintext) > maxPlaintext {
		return "", "", core.BadRequest("plaintext must be 1-%d bytes", maxPlaintext)
	}
	cur := k.Versions[len(k.Versions)-1]
	mat, err := s.material(k, cur.Version)
	if err != nil {
		return "", "", err
	}
	block, _ := aes.NewCipher(mat)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	out := []byte{blobVersion, byte(len(k.ID))}
	out = append(out, k.ID...)
	out = binary.BigEndian.AppendUint32(out, uint32(cur.Version))
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, aad(ctx))
	return base64.StdEncoding.EncodeToString(out), k.ARN, nil
}

func (s *Service) Decrypt(blob string, ctx map[string]string) ([]byte, string, error) {
	bad := core.Errf(http.StatusBadRequest, "InvalidCiphertextException", "the ciphertext is invalid or was encrypted with a different encryption context")
	b, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(b) < 2 || b[0] != blobVersion {
		return nil, "", bad
	}
	n := int(b[1])
	if len(b) < 2+n+4+12 {
		return nil, "", bad
	}
	id := string(b[2 : 2+n])
	version := int(binary.BigEndian.Uint32(b[2+n:]))
	rest := b[2+n+4:]
	k, err := store.Get[Key](s.env.Store, cKeys, id)
	if err != nil {
		return nil, "", bad
	}
	if k.State != "Enabled" {
		return nil, "", core.Errf(http.StatusBadRequest, "DisabledException", "key %s is %s", k.ID, k.State)
	}
	mat, err := s.material(k, version)
	if err != nil {
		return nil, "", err
	}
	block, _ := aes.NewCipher(mat)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, rest[:gcm.NonceSize()], rest[gcm.NonceSize():], aad(ctx))
	if err != nil {
		return nil, "", bad
	}
	return pt, k.ARN, nil
}

func (s *Service) aliasesOf(id string) []string {
	out := []string{}
	for _, a := range store.List[alias](s.env.Store, cAliases) {
		if a.KeyID == id {
			out = append(out, a.Name)
		}
	}
	return out
}

// Maintain rotates keys that have automatic rotation enabled and deletes keys past their deletion date.
func (s *Service) Maintain() {
	for _, k := range store.List[Key](s.env.Store, cKeys) {
		if k.State == "PendingDeletion" && k.DeletionDate != nil && time.Now().After(*k.DeletionDate) {
			_ = store.Delete(s.env.Store, cKeys, k.ID)
			for _, a := range s.aliasesOf(k.ID) {
				_ = store.Delete(s.env.Store, cAliases, a)
			}
			continue
		}
		last := k.CreatedAt
		if k.LastRotated != nil {
			last = *k.LastRotated
		}
		if k.RotationEnabled && k.State == "Enabled" && time.Since(last) > rotationEvery {
			_ = s.rotate(k.ID)
		}
	}
}

func (s *Service) rotate(id string) error {
	_, err := store.Update(s.env.Store, cKeys, id, func(k *Key) error {
		v, err := s.newVersion(len(k.Versions) + 1)
		if err != nil {
			return err
		}
		k.Versions = append(k.Versions, v)
		n := core.Now()
		k.LastRotated = &n
		return nil
	})
	return err
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:kms:local-1:{account}:key/{id}")
	r.Handle("GET /api/v1/kms/keys", "kms:ListKeys", s.listKeys)
	r.Handle("POST /api/v1/kms/keys", "kms:CreateKey", s.createKey)
	r.Handle("GET /api/v1/kms/keys/{id}", "kms:DescribeKey", s.describe, res)
	r.Handle("PATCH /api/v1/kms/keys/{id}", "kms:UpdateKeyDescription", s.update, res)
	r.Handle("POST /api/v1/kms/keys/{id}/enable", "kms:EnableKey", s.setState("Enabled"), res)
	r.Handle("POST /api/v1/kms/keys/{id}/disable", "kms:DisableKey", s.setState("Disabled"), res)
	r.Handle("POST /api/v1/kms/keys/{id}/rotate", "kms:RotateKeyOnDemand", s.rotateNow, res)
	r.Handle("POST /api/v1/kms/keys/{id}/schedule-deletion", "kms:ScheduleKeyDeletion", s.scheduleDeletion, res)
	r.Handle("POST /api/v1/kms/keys/{id}/cancel-deletion", "kms:CancelKeyDeletion", s.cancelDeletion, res)
	r.Handle("GET /api/v1/kms/aliases", "kms:ListAliases", s.listAliases)
	r.Handle("POST /api/v1/kms/aliases", "kms:CreateAlias", s.createAlias)
	r.Handle("DELETE /api/v1/kms/aliases/{name...}", "kms:DeleteAlias", s.deleteAlias)
	r.Handle("POST /api/v1/kms/encrypt", "kms:Encrypt", s.encrypt)
	r.Handle("POST /api/v1/kms/decrypt", "kms:Decrypt", s.decrypt)
	r.Handle("POST /api/v1/kms/generate-data-key", "kms:GenerateDataKey", s.dataKey)
	r.Handle("POST /api/v1/kms/generate-random", "kms:GenerateRandom", s.random)
}

func (s *Service) listKeys(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, k := range store.List[Key](s.env.Store, cKeys) {
		out = append(out, k.view(s.aliasesOf(k.ID)))
	}
	return out, nil
}

func (s *Service) createKey(c *httpx.Ctx) (any, error) {
	var in struct {
		Description string    `json:"description"`
		Alias       string    `json:"alias"`
		Rotation    bool      `json:"rotation_enabled"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Alias != "" {
		if err := validAlias(in.Alias); err != nil {
			return nil, err
		}
		if store.Has(s.env.Store, cAliases, in.Alias) {
			return nil, core.Errf(http.StatusConflict, "AlreadyExistsException", "alias %q already exists", in.Alias)
		}
	}
	k, err := s.create(in.Description, false, in.Tags)
	if err != nil {
		return nil, err
	}
	if in.Rotation {
		k, _ = store.Update(s.env.Store, cKeys, k.ID, func(x *Key) error { x.RotationEnabled = true; return nil })
	}
	if in.Alias != "" {
		if err := store.Put(s.env.Store, cAliases, in.Alias, alias{Name: in.Alias, KeyID: k.ID}); err != nil {
			return nil, err
		}
	}
	return k.view(s.aliasesOf(k.ID)), nil
}

func (s *Service) describe(c *httpx.Ctx) (any, error) {
	k, err := s.resolve(c.Param("id"))
	if err != nil {
		return nil, err
	}
	return k.view(s.aliasesOf(k.ID)), nil
}

func (s *Service) mutate(c *httpx.Ctx, fn func(*Key) error) (any, error) {
	k, err := s.resolve(c.Param("id"))
	if err != nil {
		return nil, err
	}
	k, err = store.Update(s.env.Store, cKeys, k.ID, func(x *Key) error {
		if x.Managed {
			return core.Errf(http.StatusBadRequest, "UnsupportedOperationException", "service-managed keys cannot be changed")
		}
		return fn(x)
	})
	if err != nil {
		return nil, err
	}
	return k.view(s.aliasesOf(k.ID)), nil
}

func (s *Service) update(c *httpx.Ctx) (any, error) {
	var in struct {
		Description *string `json:"description"`
		Rotation    *bool   `json:"rotation_enabled"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.mutate(c, func(k *Key) error {
		if in.Description != nil {
			k.Description = *in.Description
		}
		if in.Rotation != nil {
			k.RotationEnabled = *in.Rotation
		}
		return nil
	})
}

func (s *Service) setState(state string) httpx.Handler {
	return func(c *httpx.Ctx) (any, error) {
		return s.mutate(c, func(k *Key) error {
			if k.State == "PendingDeletion" {
				return core.Errf(http.StatusBadRequest, "KMSInvalidStateException", "key is pending deletion; cancel the deletion first")
			}
			k.State = state
			return nil
		})
	}
}

func (s *Service) rotateNow(c *httpx.Ctx) (any, error) {
	k, err := s.resolve(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if k.Managed {
		return nil, core.Errf(http.StatusBadRequest, "UnsupportedOperationException", "service-managed keys rotate automatically")
	}
	if err := s.rotate(k.ID); err != nil {
		return nil, err
	}
	k, _ = store.Get[Key](s.env.Store, cKeys, k.ID)
	return k.view(s.aliasesOf(k.ID)), nil
}

func (s *Service) scheduleDeletion(c *httpx.Ctx) (any, error) {
	var in struct {
		PendingWindowDays int `json:"pending_window_days"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.PendingWindowDays == 0 {
		in.PendingWindowDays = 30
	}
	if in.PendingWindowDays < 7 || in.PendingWindowDays > 30 {
		return nil, core.BadRequest("pending_window_days must be 7-30")
	}
	return s.mutate(c, func(k *Key) error {
		d := core.Now().Add(time.Duration(in.PendingWindowDays) * 24 * time.Hour)
		k.State, k.DeletionDate = "PendingDeletion", &d
		return nil
	})
}

func (s *Service) cancelDeletion(c *httpx.Ctx) (any, error) {
	return s.mutate(c, func(k *Key) error {
		if k.State != "PendingDeletion" {
			return core.Errf(http.StatusBadRequest, "KMSInvalidStateException", "key is not pending deletion")
		}
		k.State, k.DeletionDate = "Disabled", nil
		return nil
	})
}

var aliasRe = regexp.MustCompile(`^alias/[a-zA-Z0-9/_-]{1,250}$`)

func validAlias(a string) error {
	if !aliasRe.MatchString(a) || strings.HasPrefix(a, "alias/hc/") || strings.HasPrefix(a, "alias/aws/") {
		return core.BadRequest("alias names look like alias/my-key (the alias/hc/ and alias/aws/ prefixes are reserved)")
	}
	return nil
}

func (s *Service) listAliases(c *httpx.Ctx) (any, error) {
	return store.List[alias](s.env.Store, cAliases), nil
}

func (s *Service) createAlias(c *httpx.Ctx) (any, error) {
	var in struct {
		Name  string `json:"name"`
		KeyID string `json:"key_id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := validAlias(in.Name); err != nil {
		return nil, err
	}
	k, err := s.resolve(in.KeyID)
	if err != nil {
		return nil, err
	}
	if store.Has(s.env.Store, cAliases, in.Name) {
		return nil, core.Errf(http.StatusConflict, "AlreadyExistsException", "alias %q already exists", in.Name)
	}
	a := alias{Name: in.Name, KeyID: k.ID}
	return a, store.Put(s.env.Store, cAliases, a.Name, a)
}

func (s *Service) deleteAlias(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	if strings.HasPrefix(name, "alias/hc/") {
		return nil, core.BadRequest("service-managed aliases cannot be deleted")
	}
	if err := store.Delete(s.env.Store, cAliases, name); err != nil {
		return nil, core.Errf(http.StatusNotFound, "NotFoundException", "alias %q does not exist", name)
	}
	return nil, nil
}

type cryptoInput struct {
	KeyID             string            `json:"key_id"`
	Plaintext         string            `json:"plaintext"`       // base64
	CiphertextBlob    string            `json:"ciphertext_blob"` // base64
	EncryptionContext map[string]string `json:"encryption_context"`
	NumberOfBytes     int               `json:"number_of_bytes"`
}

func (s *Service) authorizeKey(c *httpx.Ctx, action, ref string) error {
	k, err := s.resolve(ref)
	if err != nil {
		return err
	}
	return c.Authorize(action, k.ARN)
}

func (s *Service) encrypt(c *httpx.Ctx) (any, error) {
	var in cryptoInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authorizeKey(c, "kms:Encrypt", in.KeyID); err != nil {
		return nil, err
	}
	pt, err := base64.StdEncoding.DecodeString(in.Plaintext)
	if err != nil {
		return nil, core.BadRequest("plaintext must be base64")
	}
	blob, arn, err := s.Encrypt(in.KeyID, pt, in.EncryptionContext)
	if err != nil {
		return nil, err
	}
	return map[string]string{"ciphertext_blob": blob, "key_id": arn}, nil
}

func (s *Service) decrypt(c *httpx.Ctx) (any, error) {
	var in cryptoInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	pt, arn, err := s.Decrypt(in.CiphertextBlob, in.EncryptionContext)
	if err != nil {
		return nil, err
	}
	if err := c.Authorize("kms:Decrypt", arn); err != nil {
		return nil, err
	}
	return map[string]string{"plaintext": base64.StdEncoding.EncodeToString(pt), "key_id": arn}, nil
}

func (s *Service) dataKey(c *httpx.Ctx) (any, error) {
	var in cryptoInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authorizeKey(c, "kms:GenerateDataKey", in.KeyID); err != nil {
		return nil, err
	}
	n := in.NumberOfBytes
	if n == 0 {
		n = 32
	}
	if n < 1 || n > 1024 {
		return nil, core.BadRequest("number_of_bytes must be 1-1024")
	}
	dk := make([]byte, n)
	if _, err := rand.Read(dk); err != nil {
		return nil, err
	}
	blob, arn, err := s.Encrypt(in.KeyID, dk, in.EncryptionContext)
	if err != nil {
		return nil, err
	}
	return map[string]string{"plaintext": base64.StdEncoding.EncodeToString(dk), "ciphertext_blob": blob, "key_id": arn}, nil
}

func (s *Service) random(c *httpx.Ctx) (any, error) {
	var in cryptoInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.NumberOfBytes < 1 || in.NumberOfBytes > 1024 {
		return nil, core.BadRequest("number_of_bytes must be 1-1024")
	}
	b := make([]byte, in.NumberOfBytes)
	_, _ = rand.Read(b)
	return map[string]string{"plaintext": base64.StdEncoding.EncodeToString(b)}, nil
}
