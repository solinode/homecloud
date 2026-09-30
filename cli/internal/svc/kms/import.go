package kms

import (
	"crypto/aes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/binary"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Imported key material (Origin EXTERNAL). A key created with
// Origin=EXTERNAL starts in state PendingImport without material. The caller
// fetches a wrapping public key and import token with GetParametersForImport,
// wraps their material with it and calls ImportKeyMaterial. The material is
// stored encrypted under the master key, like generated material. Once
// deleted or expired, only the same material can be imported again.

const (
	cTokens     = "kms_import_tokens"
	tokenTTL    = 24 * time.Hour
	expires     = "KEY_MATERIAL_EXPIRES"
	notExpires  = "KEY_MATERIAL_DOES_NOT_EXPIRE"
	wrapAESRSA1 = "RSA_AES_KEY_WRAP_SHA_1"
	wrapAESRSA2 = "RSA_AES_KEY_WRAP_SHA_256"
)

var wrappingAlgorithms = []string{"RSAES_OAEP_SHA_1", "RSAES_OAEP_SHA_256", wrapAESRSA1, wrapAESRSA2}

// importToken is the server side of an import token: the wrapping private
// key (encrypted by the master key), bound to one key and one algorithm.
type importToken struct {
	ID        string    `json:"id"`
	KeyID     string    `json:"key_id"`
	Algorithm string    `json:"algorithm"`
	Spec      string    `json:"spec"`
	Private   string    `json:"private"`
	Expires   time.Time `json:"expires"`
}

// dropMaterial erases a key's imported material (the digest stays).
func dropMaterial(k *Key) {
	if len(k.Versions) > 0 {
		k.Versions = append([]keyVersion(nil), k.Versions...)
		k.Versions[0].Material = ""
	}
	k.ValidTo, k.ExpirationModel = nil, ""
	if k.State != statePending {
		k.State = stateImport
	}
}

func (s *Service) dropTokens(keyID string) {
	for _, t := range store.List[importToken](s.env.Store, cTokens) {
		if t.KeyID == keyID {
			_ = store.Delete(s.env.Store, cTokens, t.ID)
		}
	}
}

// importable authorizes action and returns an EXTERNAL key that can take material.
func (s *Service) importable(az Authz, action, ref string) (Key, error) {
	k, err := s.keyFor(az, action, ref)
	if err != nil {
		return k, err
	}
	if !k.external() {
		return k, errf("UnsupportedOperationException", "%s origin is AWS_KMS, which is not compatible with this operation.", k.ARN)
	}
	return k, notPending(&k)
}

// GetParametersForImportOut is a wrapping public key and its import token.
type importParams struct {
	Token     []byte
	PublicKey []byte
	ValidTo   time.Time
	KeyARN    string
}

func (s *Service) getParametersForImport(az Authz, ref, alg, spec string) (importParams, error) {
	k, err := s.importable(az, "kms:GetParametersForImport", ref)
	if err != nil {
		return importParams{}, err
	}
	if !contains(wrappingAlgorithms, alg) {
		return importParams{}, core.BadRequest("WrappingAlgorithm must be one of %s", strings.Join(wrappingAlgorithms, ", "))
	}
	if rsaBits[spec] == 0 {
		return importParams{}, core.BadRequest("WrappingKeySpec must be RSA_2048, RSA_3072 or RSA_4096")
	}
	der, err := genMaterial(spec)
	if err != nil {
		return importParams{}, err
	}
	pk, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return importParams{}, err
	}
	pub, err := marshalPublic(pk)
	if err != nil {
		return importParams{}, err
	}
	now := s.now()
	for _, t := range store.List[importToken](s.env.Store, cTokens) { // purge expired tokens
		if !now.Before(t.Expires) {
			_ = store.Delete(s.env.Store, cTokens, t.ID)
		}
	}
	t := importToken{ID: core.RandHex(64), KeyID: k.ID, Algorithm: alg, Spec: spec, Private: s.master.Encrypt(der), Expires: now.Add(tokenTTL)}
	if err := store.Put(s.env.Store, cTokens, t.ID, t); err != nil {
		return importParams{}, err
	}
	return importParams{Token: []byte(t.ID), PublicKey: pub, ValidTo: t.Expires, KeyARN: k.ARN}, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Service) importKeyMaterial(az Authz, ref string, token, wrapped []byte, validTo *time.Time, model string) (Key, error) {
	k, err := s.importable(az, "kms:ImportKeyMaterial", ref)
	if err != nil {
		return k, err
	}
	switch model {
	case "":
		model = expires
	case expires, notExpires:
	default:
		return k, core.BadRequest("ExpirationModel must be %s or %s", expires, notExpires)
	}
	if model == expires && validTo == nil {
		return k, core.BadRequest("ValidTo is required when ExpirationModel is %s", expires)
	}
	if model == notExpires && validTo != nil {
		return k, core.BadRequest("ValidTo must not be set when ExpirationModel is %s", notExpires)
	}
	if validTo != nil && !validTo.After(s.now()) {
		return k, core.BadRequest("ValidTo must be in the future")
	}
	if len(wrapped) == 0 {
		return k, core.BadRequest("EncryptedKeyMaterial is required")
	}
	t, err := store.Get[importToken](s.env.Store, cTokens, string(token))
	if err != nil || t.KeyID != k.ID {
		return k, errf("InvalidImportTokenException", "The import token is invalid or was issued for a different key.")
	}
	if !s.now().Before(t.Expires) {
		_ = store.Delete(s.env.Store, cTokens, t.ID)
		return k, errf("ExpiredImportTokenException", "The import token has expired. Call GetParametersForImport again.")
	}
	der, err := s.master.Decrypt(t.Private)
	if err != nil {
		return k, err
	}
	pk, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return k, err
	}
	mat, err := unwrapMaterial(pk.(*rsa.PrivateKey), t.Algorithm, wrapped)
	if err != nil {
		return k, err
	}
	if err := checkMaterial(k, mat); err != nil {
		return k, err
	}
	sum := sha256.Sum256(mat)
	enc := s.master.Encrypt(mat)
	var vt *time.Time
	if validTo != nil {
		v := validTo.UTC().Truncate(time.Second)
		vt = &v
	}
	return store.Update(s.env.Store, cKeys, k.ID, func(x *Key) error {
		if err := notPending(x); err != nil {
			return err
		}
		if x.MaterialDigest != "" {
			prev, err := s.master.Decrypt(x.MaterialDigest)
			if err != nil {
				return err
			}
			if subtle.ConstantTimeCompare(prev, sum[:]) != 1 {
				return errf("IncorrectKeyMaterialException", "The key material differs from the material previously imported into this key. Import the same key material.")
			}
		}
		x.Versions = append([]keyVersion(nil), x.Versions...)
		x.Versions[0].Material = enc
		x.MaterialDigest = s.master.Encrypt(sum[:])
		x.ExpirationModel, x.ValidTo = model, vt
		if x.State == stateImport {
			x.State = stateEnabled
		}
		return nil
	})
}

func (s *Service) deleteImportedKeyMaterial(az Authz, ref string) (Key, error) {
	if _, err := s.importable(az, "kms:DeleteImportedKeyMaterial", ref); err != nil {
		return Key{}, err
	}
	return s.mutate(az, "kms:DeleteImportedKeyMaterial", ref, func(k *Key) error {
		if err := notPending(k); err != nil {
			return err
		}
		dropMaterial(k)
		return nil
	})
}

// unwrapMaterial decrypts wrapped key material: RSAES_OAEP directly, or for
// RSA_AES_KEY_WRAP an RSA-OAEP wrapped AES-256 key followed by the material
// wrapped with it (AES-KWP, RFC 5649).
func unwrapMaterial(priv *rsa.PrivateKey, alg string, blob []byte) ([]byte, error) {
	bad := errf("InvalidCiphertextException", "The encrypted key material could not be decrypted with the wrapping key of this import token and algorithm.")
	_, h := hashFor(alg)
	if strings.HasPrefix(alg, "RSAES_OAEP") {
		m, err := rsa.DecryptOAEP(h(), rand.Reader, priv, blob, nil)
		if err != nil {
			return nil, bad
		}
		return m, nil
	}
	n := priv.Size()
	if len(blob) <= n {
		return nil, bad
	}
	kek, err := rsa.DecryptOAEP(h(), rand.Reader, priv, blob[:n], nil)
	if err != nil || len(kek) != 32 {
		return nil, bad
	}
	m, err := kwpUnwrap(kek, blob[n:])
	if err != nil {
		return nil, bad
	}
	return m, nil
}

// kwpUnwrap is AES key unwrap with padding (RFC 5649).
func kwpUnwrap(kek, c []byte) ([]byte, error) {
	if len(c)%8 != 0 || len(c) < 16 {
		return nil, errf("InvalidCiphertextException", "bad wrapped key length")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(c)/8 - 1
	var a, p []byte
	if n == 1 {
		buf := make([]byte, 16)
		block.Decrypt(buf, c)
		a, p = buf[:8], buf[8:]
	} else {
		a = append([]byte(nil), c[:8]...)
		r := append([]byte(nil), c[8:]...)
		for j := 5; j >= 0; j-- {
			for i := n; i >= 1; i-- {
				var b [16]byte
				binary.BigEndian.PutUint64(b[:8], binary.BigEndian.Uint64(a)^uint64(n*j+i))
				copy(b[8:], r[(i-1)*8:i*8])
				block.Decrypt(b[:], b[:])
				copy(a, b[:8])
				copy(r[(i-1)*8:i*8], b[8:])
			}
		}
		p = r
	}
	mli := int(binary.BigEndian.Uint32(a[4:]))
	if a[0] != 0xA6 || a[1] != 0x59 || a[2] != 0x59 || a[3] != 0xA6 || mli > len(p) || mli <= len(p)-8 {
		return nil, errf("InvalidCiphertextException", "wrapped key integrity check failed")
	}
	for _, x := range p[mli:] {
		if x != 0 {
			return nil, errf("InvalidCiphertextException", "wrapped key padding is invalid")
		}
	}
	return p[:mli], nil
}

// checkMaterial validates material for the key's spec: raw bytes for
// SYMMETRIC_DEFAULT and HMAC keys, a PKCS#8 private key for RSA and ECC.
func checkMaterial(k Key, m []byte) error {
	bad := func(format string, a ...any) error {
		return errf("IncorrectKeyMaterialException", format, a...)
	}
	switch {
	case k.symmetric():
		if len(m) != 32 {
			return bad("The key material must be 256 bits (32 bytes) for SYMMETRIC_DEFAULT; got %d bytes.", len(m))
		}
	case k.hmac():
		if len(m) != hmacSizes[k.KeySpec] {
			return bad("The key material must be %d bytes for %s; got %d bytes.", hmacSizes[k.KeySpec], k.KeySpec, len(m))
		}
	default:
		pk, err := x509.ParsePKCS8PrivateKey(m)
		if err != nil {
			return bad("The key material for %s must be a PKCS#8 DER private key.", k.KeySpec)
		}
		ok := false
		switch p := pk.(type) {
		case *rsa.PrivateKey:
			ok = k.rsa() && p.N.BitLen() == rsaBits[k.KeySpec]
		case *ecdsa.PrivateKey:
			ok = k.ecc() && p.Curve == curves[k.KeySpec]
		}
		if !ok {
			return bad("The private key does not match key spec %s.", k.KeySpec)
		}
	}
	return nil
}
