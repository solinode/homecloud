package kms

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

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
	r.Handle("GET /api/v1/kms/keys/{id}/policy", "kms:GetKeyPolicy", s.nativeGetPolicy, d)
	r.Handle("PUT /api/v1/kms/keys/{id}/policy", "kms:PutKeyPolicy", s.nativePutPolicy, d)
	r.Handle("GET /api/v1/kms/keys/{id}/grants", "kms:ListGrants", s.nativeListGrants, d)
	r.Handle("POST /api/v1/kms/keys/{id}/grants", "kms:CreateGrant", s.nativeCreateGrant, d)
	r.Handle("DELETE /api/v1/kms/keys/{id}/grants/{grant}", "kms:RevokeGrant", s.nativeRevokeGrant, d)
	r.Handle("GET /api/v1/kms/keys/{id}/public-key", "kms:GetPublicKey", s.nativePublicKey, d)
	r.Handle("GET /api/v1/kms/aliases", "kms:ListAliases", s.listAliases)
	r.Handle("POST /api/v1/kms/aliases", "kms:CreateAlias", s.nativeCreateAlias, d)
	r.Handle("DELETE /api/v1/kms/aliases/{name...}", "kms:DeleteAlias", s.nativeDeleteAlias, d)
	r.Handle("POST /api/v1/kms/encrypt", "kms:Encrypt", s.nativeEncrypt, d)
	r.Handle("POST /api/v1/kms/decrypt", "kms:Decrypt", s.nativeDecrypt, d)
	r.Handle("POST /api/v1/kms/generate-data-key", "kms:GenerateDataKey", s.nativeDataKey, d)
	r.Handle("POST /api/v1/kms/sign", "kms:Sign", s.nativeSign, d)
	r.Handle("POST /api/v1/kms/verify", "kms:Verify", s.nativeVerify, d)
	r.Handle("POST /api/v1/kms/generate-mac", "kms:GenerateMac", s.nativeGenerateMac, d)
	r.Handle("POST /api/v1/kms/verify-mac", "kms:VerifyMac", s.nativeVerifyMac, d)
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
	Algorithm         string            `json:"encryption_algorithm"`
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
	blob, alg, err := s.encrypt(k, pt, in.EncryptionContext, in.Algorithm)
	if err != nil {
		return nil, err
	}
	return map[string]string{"ciphertext_blob": base64.StdEncoding.EncodeToString(blob), "key_id": k.ARN, "encryption_algorithm": alg}, nil
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
	pt, k, alg, err := s.decrypt(c.Authorize, "kms:Decrypt", blob, in.EncryptionContext, in.KeyID, in.Algorithm)
	if err != nil {
		return nil, err
	}
	return map[string]string{"plaintext": base64.StdEncoding.EncodeToString(pt), "key_id": k.ARN, "encryption_algorithm": alg}, nil
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

func (s *Service) nativeGetPolicy(c *httpx.Ctx) (any, error) {
	k, err := s.keyFor(c.Authorize, "kms:GetKeyPolicy", c.Param("id"))
	if err != nil {
		return nil, err
	}
	return map[string]string{"policy_name": "default", "policy": s.policyOf(k)}, nil
}

func (s *Service) nativePutPolicy(c *httpx.Ctx) (any, error) {
	var in struct {
		Policy string `json:"policy"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.putPolicy(c.Authorize, c.Param("id"), "default", in.Policy); err != nil {
		return nil, err
	}
	return map[string]string{"policy_name": "default", "policy": in.Policy}, nil
}

func grantView(g Grant) map[string]any {
	return map[string]any{"grant_id": g.ID, "name": g.Name, "grantee_principal": g.GranteePrincipal,
		"retiring_principal": g.RetiringPrincipal, "operations": g.Operations, "constraints": g.Constraints, "created_at": g.CreatedAt}
}

func (s *Service) nativeListGrants(c *httpx.Ctx) (any, error) {
	k, err := s.keyFor(c.Authorize, "kms:ListGrants", c.Param("id"))
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, g := range k.Grants {
		out = append(out, grantView(g))
	}
	return out, nil
}

func (s *Service) nativeCreateGrant(c *httpx.Ctx) (any, error) {
	var in struct {
		Name              string          `json:"name"`
		GranteePrincipal  string          `json:"grantee_principal"`
		RetiringPrincipal string          `json:"retiring_principal"`
		Operations        []string        `json:"operations"`
		Constraints       json.RawMessage `json:"constraints"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	g, err := s.createGrant(c.Authorize, c.Param("id"), Grant{Name: in.Name, GranteePrincipal: in.GranteePrincipal,
		RetiringPrincipal: in.RetiringPrincipal, Operations: in.Operations, Constraints: in.Constraints})
	if err != nil {
		return nil, err
	}
	v := grantView(g)
	v["grant_token"] = g.Token
	return v, nil
}

func (s *Service) nativeRevokeGrant(c *httpx.Ctx) (any, error) {
	return nil, s.removeGrant(c.Authorize, "kms:RevokeGrant", c.Param("id"), c.Param("grant"), "")
}

func (s *Service) nativePublicKey(c *httpx.Ctx) (any, error) {
	k, err := s.keyFor(c.Authorize, "kms:GetPublicKey", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if !k.rsa() && !k.ecc() {
		return nil, errf("UnsupportedOperationException", "%s is not an asymmetric key", k.ARN)
	}
	if err := usable(k); err != nil {
		return nil, err
	}
	pub, err := s.publicKeyDER(k)
	if err != nil {
		return nil, err
	}
	pemBody := base64.StdEncoding.EncodeToString(pub)
	var lines []string
	for len(pemBody) > 64 {
		lines, pemBody = append(lines, pemBody[:64]), pemBody[64:]
	}
	lines = append(lines, pemBody)
	return map[string]any{"key_id": core.CanonicalARN(k.ARN), "public_key": base64.StdEncoding.EncodeToString(pub),
		"pem":      "-----BEGIN PUBLIC KEY-----\n" + strings.Join(lines, "\n") + "\n-----END PUBLIC KEY-----\n",
		"key_spec": k.KeySpec, "key_usage": k.KeyUsage,
		"encryption_algorithms": encryptionAlgorithms(k), "signing_algorithms": signingAlgorithms(k)}, nil
}

// signInput carries base64 message, signature and MAC fields.
type signInput struct {
	KeyID       string `json:"key_id"`
	Message     string `json:"message"` // base64
	MessageType string `json:"message_type"`
	Algorithm   string `json:"algorithm"` // signing or MAC algorithm
	Signature   string `json:"signature"` // base64
	Mac         string `json:"mac"`       // base64
}

func b64(name, v string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, core.BadRequest("%s must be base64", name)
	}
	return b, nil
}

func (in signInput) decode() (msg, sig, mac []byte, err error) {
	if msg, err = b64("message", in.Message); err != nil {
		return
	}
	if sig, err = b64("signature", in.Signature); err != nil {
		return
	}
	mac, err = b64("mac", in.Mac)
	return
}

// invalid reports a failed verification (as opposed to a request error).
func invalid(err error, code string) bool {
	var e *core.Error
	return errors.As(err, &e) && e.Code == code
}

func (s *Service) nativeSign(c *httpx.Ctx) (any, error) {
	var in signInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	msg, _, _, err := in.decode()
	if err != nil {
		return nil, err
	}
	sig, k, err := s.sign(c.Authorize, in.KeyID, msg, in.MessageType, in.Algorithm)
	if err != nil {
		return nil, err
	}
	return map[string]string{"key_id": core.CanonicalARN(k.ARN), "signature": base64.StdEncoding.EncodeToString(sig), "algorithm": in.Algorithm}, nil
}

func (s *Service) nativeVerify(c *httpx.Ctx) (any, error) {
	var in signInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	msg, sig, _, err := in.decode()
	if err != nil {
		return nil, err
	}
	k, err := s.verify(c.Authorize, in.KeyID, msg, in.MessageType, in.Algorithm, sig)
	if err != nil && !invalid(err, "KMSInvalidSignatureException") {
		return nil, err
	}
	return map[string]any{"key_id": core.CanonicalARN(k.ARN), "valid": err == nil, "algorithm": in.Algorithm}, nil
}

func (s *Service) nativeGenerateMac(c *httpx.Ctx) (any, error) {
	var in signInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	msg, _, _, err := in.decode()
	if err != nil {
		return nil, err
	}
	mac, k, err := s.mac(c.Authorize, "kms:GenerateMac", in.KeyID, msg, in.Algorithm)
	if err != nil {
		return nil, err
	}
	return map[string]string{"key_id": core.CanonicalARN(k.ARN), "mac": base64.StdEncoding.EncodeToString(mac), "algorithm": in.Algorithm}, nil
}

func (s *Service) nativeVerifyMac(c *httpx.Ctx) (any, error) {
	var in signInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	msg, _, mac, err := in.decode()
	if err != nil {
		return nil, err
	}
	k, err := s.verifyMac(c.Authorize, in.KeyID, msg, in.Algorithm, mac)
	if err != nil && !invalid(err, "KMSInvalidMacException") {
		return nil, err
	}
	return map[string]any{"key_id": core.CanonicalARN(k.ARN), "valid": err == nil, "algorithm": in.Algorithm}, nil
}
