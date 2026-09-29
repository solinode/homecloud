package kms

import (
	"crypto/rand"
	"encoding/base64"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

func (s *Service) Routes(r *httpx.Router) {
	r.Handle("GET /api/v1/kms/keys", "kms:ListKeys", s.listKeys)
	r.Handle("POST /api/v1/kms/keys", "kms:CreateKey", s.nativeCreateKey)
	// {id} may be a key ID, key ARN or alias; handlers authorize the resolved key's ARN.
	d := httpx.Deferred()
	r.Handle("GET /api/v1/kms/keys/{id}", "kms:DescribeKey", s.nativeDescribe, d)
	r.Handle("PATCH /api/v1/kms/keys/{id}", "kms:UpdateKeyDescription", s.nativeUpdate, d)
	r.Handle("POST /api/v1/kms/keys/{id}/enable", "kms:EnableKey", s.nativeSetState(true), d)
	r.Handle("POST /api/v1/kms/keys/{id}/disable", "kms:DisableKey", s.nativeSetState(false), d)
	r.Handle("POST /api/v1/kms/keys/{id}/rotate", "kms:RotateKeyOnDemand", s.nativeRotate, d)
	r.Handle("POST /api/v1/kms/keys/{id}/schedule-deletion", "kms:ScheduleKeyDeletion", s.nativeScheduleDeletion, d)
	r.Handle("POST /api/v1/kms/keys/{id}/cancel-deletion", "kms:CancelKeyDeletion", s.nativeCancelDeletion, d)
	r.Handle("GET /api/v1/kms/aliases", "kms:ListAliases", s.listAliases)
	r.Handle("POST /api/v1/kms/aliases", "kms:CreateAlias", s.nativeCreateAlias, d)
	r.Handle("DELETE /api/v1/kms/aliases/{name...}", "kms:DeleteAlias", s.nativeDeleteAlias, d)
	r.Handle("POST /api/v1/kms/encrypt", "kms:Encrypt", s.nativeEncrypt, d)
	r.Handle("POST /api/v1/kms/decrypt", "kms:Decrypt", s.nativeDecrypt, d)
	r.Handle("POST /api/v1/kms/generate-data-key", "kms:GenerateDataKey", s.nativeDataKey, d)
	r.Handle("POST /api/v1/kms/generate-random", "kms:GenerateRandom", s.nativeRandom)
}

func (s *Service) keyView(k Key) map[string]any {
	k.ARN = core.CanonicalARN(k.ARN)
	return k.view(s.aliasesOf(k.ID))
}

func (s *Service) listKeys(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, k := range store.List[Key](s.env.Store, cKeys) {
		out = append(out, s.keyView(k))
	}
	return out, nil
}

func (s *Service) nativeCreateKey(c *httpx.Ctx) (any, error) {
	var in struct {
		Description string    `json:"description"`
		Alias       string    `json:"alias"`
		Rotation    bool      `json:"rotation_enabled"`
		KeySpec     string    `json:"key_spec"`
		KeyUsage    string    `json:"key_usage"`
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
			return nil, errf("AlreadyExistsException", "alias %q already exists", in.Alias)
		}
	}
	k, err := s.createCustomerKey(c.Authorize, CreateKeyInput{Description: in.Description, KeySpec: in.KeySpec, KeyUsage: in.KeyUsage, Tags: in.Tags})
	if err != nil {
		return nil, err
	}
	if in.Rotation {
		if k, err = s.setRotation(c.Authorize, k.ID, true, 0); err != nil {
			return nil, err
		}
	}
	if in.Alias != "" {
		if _, err := s.createAlias(c.Authorize, in.Alias, k.ID); err != nil {
			return nil, err
		}
	}
	return s.keyView(k), nil
}

func (s *Service) nativeDescribe(c *httpx.Ctx) (any, error) {
	k, err := s.keyFor(c.Authorize, "kms:DescribeKey", c.Param("id"))
	if err != nil {
		return nil, err
	}
	return s.keyView(k), nil
}

func (s *Service) nativeUpdate(c *httpx.Ctx) (any, error) {
	var in struct {
		Description *string `json:"description"`
		Rotation    *bool   `json:"rotation_enabled"`
		Period      int     `json:"rotation_period_days"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(c.Authorize, "kms:DescribeKey", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if in.Description != nil {
		if k, err = s.updateDescription(c.Authorize, k.ID, *in.Description); err != nil {
			return nil, err
		}
	}
	if in.Rotation != nil && (*in.Rotation != k.RotationEnabled || in.Period != 0) {
		if k, err = s.setRotation(c.Authorize, k.ID, *in.Rotation, in.Period); err != nil {
			return nil, err
		}
	}
	return s.keyView(k), nil
}

func (s *Service) nativeSetState(enabled bool) httpx.Handler {
	return func(c *httpx.Ctx) (any, error) {
		k, err := s.setEnabled(c.Authorize, c.Param("id"), enabled)
		if err != nil {
			return nil, err
		}
		return s.keyView(k), nil
	}
}

func (s *Service) nativeRotate(c *httpx.Ctx) (any, error) {
	k, err := s.rotateOnDemand(c.Authorize, c.Param("id"))
	if err != nil {
		return nil, err
	}
	return s.keyView(k), nil
}

func (s *Service) nativeScheduleDeletion(c *httpx.Ctx) (any, error) {
	var in struct {
		PendingWindowDays int `json:"pending_window_days"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.scheduleDeletion(c.Authorize, c.Param("id"), in.PendingWindowDays)
	if err != nil {
		return nil, err
	}
	return s.keyView(k), nil
}

func (s *Service) nativeCancelDeletion(c *httpx.Ctx) (any, error) {
	k, err := s.cancelDeletion(c.Authorize, c.Param("id"))
	if err != nil {
		return nil, err
	}
	return s.keyView(k), nil
}

func (s *Service) listAliases(c *httpx.Ctx) (any, error) {
	return store.List[alias](s.env.Store, cAliases), nil
}

func (s *Service) nativeCreateAlias(c *httpx.Ctx) (any, error) {
	var in struct {
		Name  string `json:"name"`
		KeyID string `json:"key_id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.createAlias(c.Authorize, in.Name, in.KeyID)
}

func (s *Service) nativeDeleteAlias(c *httpx.Ctx) (any, error) {
	return nil, s.deleteAlias(c.Authorize, c.Param("name"))
}

type cryptoInput struct {
	KeyID             string            `json:"key_id"`
	Plaintext         string            `json:"plaintext"`       // base64
	CiphertextBlob    string            `json:"ciphertext_blob"` // base64
	EncryptionContext map[string]string `json:"encryption_context"`
	NumberOfBytes     int               `json:"number_of_bytes"`
	KeySpec           string            `json:"key_spec"`
}

func (s *Service) nativeEncrypt(c *httpx.Ctx) (any, error) {
	var in cryptoInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	pt, err := base64.StdEncoding.DecodeString(in.Plaintext)
	if err != nil {
		return nil, core.BadRequest("plaintext must be base64")
	}
	k, err := s.keyFor(c.Authorize, "kms:Encrypt", in.KeyID)
	if err != nil {
		return nil, err
	}
	if err := checkPlaintext(pt); err != nil {
		return nil, err
	}
	blob, _, err := s.encrypt(k, pt, in.EncryptionContext, "")
	if err != nil {
		return nil, err
	}
	return map[string]string{"ciphertext_blob": base64.StdEncoding.EncodeToString(blob), "key_id": k.ARN}, nil
}

func (s *Service) nativeDecrypt(c *httpx.Ctx) (any, error) {
	var in cryptoInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(in.CiphertextBlob)
	if err != nil {
		return nil, invalidCiphertext()
	}
	pt, k, _, err := s.decrypt(c.Authorize, "kms:Decrypt", blob, in.EncryptionContext, in.KeyID, "")
	if err != nil {
		return nil, err
	}
	return map[string]string{"plaintext": base64.StdEncoding.EncodeToString(pt), "key_id": k.ARN}, nil
}

func (s *Service) nativeDataKey(c *httpx.Ctx) (any, error) {
	var in cryptoInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.NumberOfBytes == 0 && in.KeySpec == "" {
		in.NumberOfBytes = 32
	}
	dk, blob, k, err := s.dataKey(c.Authorize, "kms:GenerateDataKey", in.KeyID, in.NumberOfBytes, in.KeySpec, in.EncryptionContext)
	if err != nil {
		return nil, err
	}
	return map[string]string{"plaintext": base64.StdEncoding.EncodeToString(dk), "ciphertext_blob": base64.StdEncoding.EncodeToString(blob), "key_id": k.ARN}, nil
}

func (s *Service) nativeRandom(c *httpx.Ctx) (any, error) {
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
