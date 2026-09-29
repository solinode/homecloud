// Package kms implements a key management service: symmetric encryption
// keys (self-describing ciphertexts, encryption context, envelope data keys,
// automatic and on-demand rotation where old versions still decrypt),
// asymmetric RSA/ECC keys for encryption and signing, HMAC keys, aliases, key
// policies, tags, grants and scheduled deletion. Key material is stored
// encrypted under the installation's master key.
//
// The same operations are served by the native API (native.go) and the AWS
// awsJson1.1 protocol (aws.go).
package kms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
)

const (
	cKeys         = "kms_keys"
	cAliases      = "kms_aliases"
	maxPlaintext  = 4096
	blobVersion   = 2 // 1: context encoded as k=v lines (still decrypted); 2: canonical JSON
	defaultPeriod = 365
	maxOnDemand   = 10

	stateEnabled  = "Enabled"
	stateDisabled = "Disabled"
	statePending  = "PendingDeletion"

	usageEncrypt = "ENCRYPT_DECRYPT"
	usageSign    = "SIGN_VERIFY"
	usageMAC     = "GENERATE_VERIFY_MAC"
	specSym      = "SYMMETRIC_DEFAULT"
)

type keyVersion struct {
	Version   int       `json:"version"`
	Material  string    `json:"material"` // key material, encrypted by the master key
	CreatedAt time.Time `json:"created_at"`
	Rotation  string    `json:"rotation,omitempty"` // AUTOMATIC | ON_DEMAND for rotated-in versions
}

// Grant is a KMS grant. Grants are stored and listed; access is decided by
// IAM policies.
type Grant struct {
	ID                string          `json:"id"`
	Token             string          `json:"token"`
	Name              string          `json:"name,omitempty"`
	GranteePrincipal  string          `json:"grantee_principal"`
	RetiringPrincipal string          `json:"retiring_principal,omitempty"`
	Operations        []string        `json:"operations"`
	Constraints       json.RawMessage `json:"constraints,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
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
	RotationPeriod  int          `json:"rotation_period_days,omitempty"`
	NextRotation    *time.Time   `json:"next_rotation,omitempty"`
	LastRotated     *time.Time   `json:"last_rotated,omitempty"`
	DeletionDate    *time.Time   `json:"deletion_date,omitempty"`
	PendingWindow   int          `json:"pending_window_days,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	Versions        []keyVersion `json:"versions"`
	Tags            core.Tags    `json:"tags,omitempty"`
	Policy          string       `json:"policy,omitempty"`
	Grants          []Grant      `json:"grants,omitempty"`
}

func (k Key) symmetric() bool { return k.KeySpec == specSym || k.KeySpec == "" }
func (k Key) rsa() bool       { return strings.HasPrefix(k.KeySpec, "RSA_") }
func (k Key) ecc() bool       { return strings.HasPrefix(k.KeySpec, "ECC_") }
func (k Key) hmac() bool      { return strings.HasPrefix(k.KeySpec, "HMAC_") }

func (k Key) view(aliases []string) map[string]any {
	return map[string]any{"id": k.ID, "arn": k.ARN, "description": k.Description, "key_spec": k.KeySpec, "key_usage": k.KeyUsage,
		"state": k.State, "managed": k.Managed, "rotation_enabled": k.RotationEnabled, "rotation_period_days": k.period(),
		"next_rotation": k.NextRotation, "last_rotated": k.LastRotated, "deletion_date": k.DeletionDate, "created_at": k.CreatedAt,
		"key_versions": len(k.Versions), "aliases": aliases, "tags": k.Tags}
}

func (k Key) period() int {
	if k.RotationPeriod == 0 {
		return defaultPeriod
	}
	return k.RotationPeriod
}

type alias struct {
	Name      string    `json:"name"`
	KeyID     string    `json:"key_id"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Service struct {
	env    *svc.Env
	master *secrets.Service
	// Now is the clock (tests replace it); nil means time.Now.
	Now func() time.Time
}

func New(env *svc.Env, master *secrets.Service) *Service { return &Service{env: env, master: master} }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return core.Now()
}

// Authz checks the caller may perform an IAM action on a resource
// (httpx.Ctx.Authorize or awsapi.Req.Authorize).
type Authz func(action, resource string) error

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func errf(code, format string, a ...any) *core.Error {
	return core.Errf(http.StatusBadRequest, code, format, a...)
}

// ---- key material ----

var hmacSizes = map[string]int{"HMAC_224": 28, "HMAC_256": 32, "HMAC_384": 48, "HMAC_512": 64}

var curves = map[string]elliptic.Curve{"ECC_NIST_P256": elliptic.P256(), "ECC_NIST_P384": elliptic.P384(), "ECC_NIST_P521": elliptic.P521()}

var rsaBits = map[string]int{"RSA_2048": 2048, "RSA_3072": 3072, "RSA_4096": 4096}

// genMaterial creates raw key material for a key spec: AES/HMAC bytes or a PKCS#8 private key.
func genMaterial(spec string) ([]byte, error) {
	switch {
	case spec == specSym:
		b := make([]byte, 32)
		_, err := rand.Read(b)
		return b, err
	case hmacSizes[spec] > 0:
		b := make([]byte, hmacSizes[spec])
		_, err := rand.Read(b)
		return b, err
	case rsaBits[spec] > 0:
		k, err := rsa.GenerateKey(rand.Reader, rsaBits[spec])
		if err != nil {
			return nil, err
		}
		return x509.MarshalPKCS8PrivateKey(k)
	case curves[spec] != nil:
		k, err := ecdsa.GenerateKey(curves[spec], rand.Reader)
		if err != nil {
			return nil, err
		}
		return x509.MarshalPKCS8PrivateKey(k)
	}
	return nil, errf("UnsupportedOperationException", "KeySpec %s is not supported", spec)
}

func (s *Service) newVersion(spec string, v int) (keyVersion, error) {
	b, err := genMaterial(spec)
	if err != nil {
		return keyVersion{}, err
	}
	return keyVersion{Version: v, Material: s.master.Encrypt(b), CreatedAt: s.now()}, nil
}

func (s *Service) material(k Key, version int) ([]byte, error) {
	for _, v := range k.Versions {
		if v.Version == version {
			return s.master.Decrypt(v.Material)
		}
	}
	return nil, errf("InvalidCiphertextException", "key version %d not found", version)
}

func (s *Service) current(k Key) ([]byte, int, error) {
	cur := k.Versions[len(k.Versions)-1]
	m, err := s.material(k, cur.Version)
	return m, cur.Version, err
}

func (s *Service) privateKey(k Key) (any, error) {
	m, _, err := s.current(k)
	if err != nil {
		return nil, err
	}
	return x509.ParsePKCS8PrivateKey(m)
}

func (s *Service) publicKeyDER(k Key) ([]byte, error) {
	pk, err := s.privateKey(k)
	if err != nil {
		return nil, err
	}
	return marshalPublic(pk)
}

func x509ParsePKCS8(b []byte) (any, error) { return x509.ParsePKCS8PrivateKey(b) }

// marshalPublic returns the DER SubjectPublicKeyInfo of a private key's public half.
func marshalPublic(pk any) ([]byte, error) {
	switch p := pk.(type) {
	case *rsa.PrivateKey:
		return x509.MarshalPKIXPublicKey(&p.PublicKey)
	case *ecdsa.PrivateKey:
		return x509.MarshalPKIXPublicKey(&p.PublicKey)
	}
	return nil, fmt.Errorf("unexpected private key type %T", pk)
}

// ---- keys and aliases ----

func (s *Service) create(desc string, managed bool, tags core.Tags) (Key, error) {
	return s.createKey(keySpec{Spec: specSym, Usage: usageEncrypt}, desc, managed, tags, "")
}

type keySpec struct{ Spec, Usage string }

func (s *Service) createKey(ks keySpec, desc string, managed bool, tags core.Tags, policy string) (Key, error) {
	v, err := s.newVersion(ks.Spec, 1)
	if err != nil {
		return Key{}, err
	}
	id := uuid()
	k := Key{ID: id, ARN: s.env.ARN("kms", "key/"+id), Description: desc, KeySpec: ks.Spec, KeyUsage: ks.Usage,
		State: stateEnabled, Managed: managed, CreatedAt: s.now(), Versions: []keyVersion{v}, Tags: tags, Policy: policy}
	if managed { // AWS managed keys rotate every year
		n := k.CreatedAt.AddDate(0, 0, defaultPeriod)
		k.RotationEnabled, k.NextRotation = true, &n
	}
	return k, store.Put(s.env.Store, cKeys, id, k)
}

// ManagedKey returns (creating on first use) the service-managed key behind an
// alias like "alias/aws/ssm". An alias/aws/X key reuses the key of the older
// alias/hc/X alias, so existing ciphertexts keep their key.
func (s *Service) ManagedKey(aliasName string) (string, error) {
	if a, err := store.Get[alias](s.env.Store, cAliases, aliasName); err == nil {
		return a.KeyID, nil
	}
	svcName := strings.TrimPrefix(strings.TrimPrefix(aliasName, "alias/hc/"), "alias/aws/")
	if strings.HasPrefix(aliasName, "alias/aws/") {
		if a, err := store.Get[alias](s.env.Store, cAliases, "alias/hc/"+svcName); err == nil {
			now := s.now()
			return a.KeyID, store.Put(s.env.Store, cAliases, aliasName, alias{Name: aliasName, KeyID: a.KeyID, CreatedAt: now, UpdatedAt: now})
		}
	}
	k, err := s.create("Default key that protects my "+svcName+" data when no other key is defined", true, nil)
	if err != nil {
		return "", err
	}
	now := s.now()
	return k.ID, store.Put(s.env.Store, cAliases, aliasName, alias{Name: aliasName, KeyID: k.ID, CreatedAt: now, UpdatedAt: now})
}

var awsAliasRe = regexp.MustCompile(`^alias/aws/[a-z0-9-]{1,64}$`)

// resolve accepts a key ID, key ARN, alias name or alias ARN. AWS managed
// aliases (alias/aws/<service>) create their key on first use, as in AWS.
func (s *Service) resolve(ref string) (Key, error) {
	if ref == "" {
		return Key{}, core.BadRequest("KeyId is required")
	}
	id := ref
	if strings.HasPrefix(ref, "arn:") {
		switch {
		case strings.Contains(ref, ":key/"):
			id = ref[strings.Index(ref, ":key/")+5:]
		case strings.Contains(ref, ":alias/"):
			id = ref[strings.Index(ref, ":alias/")+1:]
		default:
			return Key{}, errf("NotFoundException", "Invalid arn %s", ref)
		}
		if acct := strings.Split(ref, ":"); len(acct) > 4 && acct[4] != s.env.AccountID {
			return Key{}, errf("NotFoundException", "Key '%s' does not exist", ref)
		}
	}
	if strings.HasPrefix(id, "alias/") {
		a, err := store.Get[alias](s.env.Store, cAliases, id)
		if err != nil {
			if !awsAliasRe.MatchString(id) {
				return Key{}, errf("NotFoundException", "Alias %s is not found.", s.aliasARN(id))
			}
			kid, err := s.ManagedKey(id)
			if err != nil {
				return Key{}, err
			}
			a = alias{KeyID: kid}
		}
		id = a.KeyID
	}
	k, err := store.Get[Key](s.env.Store, cKeys, id)
	if err != nil {
		return k, errf("NotFoundException", "Key '%s' does not exist", s.env.ARN("kms", "key/"+id))
	}
	k.ARN = core.CanonicalARN(k.ARN)
	return k, nil
}

func (s *Service) aliasARN(name string) string { return s.env.ARN("kms", name) }

// KeyARN resolves a symmetric encryption key reference to its ARN (for
// services that seal data with a customer key, such as Secrets Manager).
func (s *Service) KeyARN(ref string) (string, error) {
	k, err := s.resolve(ref)
	if err != nil {
		return "", err
	}
	if !k.symmetric() || k.KeyUsage != usageEncrypt {
		return "", errf("InvalidKeyUsageException", "key %s is not a symmetric encryption key", k.ARN)
	}
	return k.ARN, nil
}

// usable checks a key can be used for cryptographic operations.
func usable(k Key) error {
	switch k.State {
	case stateEnabled:
		return nil
	case stateDisabled:
		return errf("DisabledException", "%s is disabled.", k.ARN)
	}
	return errf("KMSInvalidStateException", "%s is pending deletion.", k.ARN)
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

// ---- symmetric encryption ----

// aad encodes the encryption context unambiguously (JSON of sorted pairs).
func aad(ctx map[string]string) []byte {
	if len(ctx) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, len(keys))
	for i, k := range keys {
		pairs[i] = [2]string{k, ctx[k]}
	}
	b, _ := json.Marshal(pairs)
	return b
}

// aadV1 is the original, ambiguous encoding, kept to decrypt old ciphertexts.
func aadV1(ctx map[string]string) []byte {
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

// Encrypt seals plaintext under a symmetric key and returns a self-describing
// ciphertext blob (base64): version byte, key-id length + id, key version,
// nonce, sealed data. Used by other services; it enforces no size limit.
func (s *Service) Encrypt(ref string, plaintext []byte, ctx map[string]string) (string, string, error) {
	k, err := s.resolve(ref)
	if err != nil {
		return "", "", err
	}
	b, err := s.encryptSym(k, plaintext, ctx)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(b), k.ARN, nil
}

func (s *Service) encryptSym(k Key, plaintext []byte, ctx map[string]string) ([]byte, error) {
	if err := usable(k); err != nil {
		return nil, err
	}
	if !k.symmetric() || k.KeyUsage != usageEncrypt {
		return nil, errf("InvalidKeyUsageException", "%s key usage is %s and key spec is %s; it can't encrypt with SYMMETRIC_DEFAULT.", k.ARN, k.KeyUsage, k.KeySpec)
	}
	mat, ver, err := s.current(k)
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(mat)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := []byte{blobVersion, byte(len(k.ID))}
	out = append(out, k.ID...)
	out = binary.BigEndian.AppendUint32(out, uint32(ver))
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, aad(ctx)), nil
}

func invalidCiphertext() *core.Error {
	return errf("InvalidCiphertextException", "The ciphertext is invalid or was encrypted under a different key or encryption context.")
}

// parseBlob splits a symmetric ciphertext blob.
func parseBlob(b []byte) (id string, version int, rest []byte, v1 bool, ok bool) {
	if len(b) < 2 || (b[0] != 1 && b[0] != 2) {
		return "", 0, nil, false, false
	}
	n := int(b[1])
	if len(b) < 2+n+4+12+16 {
		return "", 0, nil, false, false
	}
	return string(b[2 : 2+n]), int(binary.BigEndian.Uint32(b[2+n:])), b[2+n+4:], b[0] == 1, true
}

// Decrypt opens a symmetric ciphertext blob (base64). Used by other services.
func (s *Service) Decrypt(blob string, ctx map[string]string) ([]byte, string, error) {
	b, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, "", invalidCiphertext()
	}
	return s.decryptSym(b, ctx)
}

func (s *Service) decryptSym(b []byte, ctx map[string]string) ([]byte, string, error) {
	id, version, rest, v1, ok := parseBlob(b)
	if !ok {
		return nil, "", invalidCiphertext()
	}
	k, err := store.Get[Key](s.env.Store, cKeys, id)
	if err != nil {
		return nil, "", invalidCiphertext()
	}
	k.ARN = core.CanonicalARN(k.ARN)
	if err := usable(k); err != nil {
		return nil, "", err
	}
	mat, err := s.material(k, version)
	if err != nil {
		return nil, "", invalidCiphertext()
	}
	encode := aad
	if v1 {
		encode = aadV1
	}
	block, _ := aes.NewCipher(mat)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, rest[:gcm.NonceSize()], rest[gcm.NonceSize():], encode(ctx))
	if err != nil {
		return nil, "", invalidCiphertext()
	}
	return pt, k.ARN, nil
}

// blobKey reads the key ID from a ciphertext blob header, without decrypting.
func blobKey(b []byte) string {
	id, _, _, _, ok := parseBlob(b)
	if !ok {
		return ""
	}
	return id
}

// ---- maintenance ----

// Maintain rotates keys whose rotation is due and deletes keys whose deletion
// window has passed. The server calls it hourly.
func (s *Service) Maintain() {
	now := s.now()
	for _, k := range store.List[Key](s.env.Store, cKeys) {
		if k.State == statePending {
			if k.DeletionDate != nil && !now.Before(*k.DeletionDate) {
				s.deleteKey(k.ID)
			}
			continue
		}
		if !k.RotationEnabled || k.State != stateEnabled || !k.symmetric() {
			continue
		}
		due := k.NextRotation
		if due == nil { // keys from before rotation dates were stored
			last := k.CreatedAt
			if k.LastRotated != nil {
				last = *k.LastRotated
			}
			d := last.AddDate(0, 0, k.period())
			due = &d
		}
		if !now.Before(*due) {
			_ = s.rotate(k.ID, "AUTOMATIC")
		}
	}
}

func (s *Service) deleteKey(id string) {
	_ = store.Delete(s.env.Store, cKeys, id)
	for _, a := range s.aliasesOf(id) {
		_ = store.Delete(s.env.Store, cAliases, a)
	}
}

func (s *Service) rotate(id, kind string) error {
	now := s.now()
	_, err := store.Update(s.env.Store, cKeys, id, func(k *Key) error {
		v, err := s.newVersion(specSym, len(k.Versions)+1)
		if err != nil {
			return err
		}
		v.Rotation = kind
		k.Versions = append(slices.Clone(k.Versions), v)
		k.LastRotated = &now
		if k.RotationEnabled {
			n := now.AddDate(0, 0, k.period())
			k.NextRotation = &n
		}
		return nil
	})
	return err
}
