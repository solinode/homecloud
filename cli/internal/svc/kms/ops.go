package kms

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/json"
	"hash"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Operations shared by the native API and the AWS protocol. Each authorizes
// through az before acting.

// keyFor resolves ref and authorizes action on the key. A missing key is
// authorized against the reference first, so callers without access learn nothing.
func (s *Service) keyFor(az Authz, action, ref string) (Key, error) {
	k, err := s.resolve(ref)
	if err != nil {
		res := ref
		if !strings.HasPrefix(ref, "arn:") {
			if strings.HasPrefix(ref, "alias/") {
				res = s.aliasARN(ref)
			} else {
				res = s.env.ARN("kms", "key/"+ref)
			}
		}
		if aerr := az(action, res); aerr != nil {
			return k, aerr
		}
		return k, err
	}
	return k, az(action, k.ARN)
}

// ---- algorithms ----

func encryptionAlgorithms(k Key) []string {
	switch {
	case k.KeyUsage != usageEncrypt:
		return nil
	case k.symmetric():
		return []string{"SYMMETRIC_DEFAULT"}
	case k.rsa():
		return []string{"RSAES_OAEP_SHA_1", "RSAES_OAEP_SHA_256"}
	}
	return nil
}

func signingAlgorithms(k Key) []string {
	if k.KeyUsage != usageSign {
		return nil
	}
	switch k.KeySpec {
	case "ECC_NIST_P256":
		return []string{"ECDSA_SHA_256"}
	case "ECC_NIST_P384":
		return []string{"ECDSA_SHA_384"}
	case "ECC_NIST_P521":
		return []string{"ECDSA_SHA_512"}
	}
	if k.rsa() {
		return []string{"RSASSA_PKCS1_V1_5_SHA_256", "RSASSA_PKCS1_V1_5_SHA_384", "RSASSA_PKCS1_V1_5_SHA_512",
			"RSASSA_PSS_SHA_256", "RSASSA_PSS_SHA_384", "RSASSA_PSS_SHA_512"}
	}
	return nil
}

func macAlgorithms(k Key) []string {
	if !k.hmac() {
		return nil
	}
	return []string{"HMAC_SHA_" + strings.TrimPrefix(k.KeySpec, "HMAC_")}
}

func hashFor(alg string) (crypto.Hash, func() hash.Hash) {
	switch {
	case strings.HasSuffix(alg, "SHA_1"):
		return crypto.SHA1, sha1.New
	case strings.HasSuffix(alg, "SHA_224"):
		return crypto.SHA224, sha256.New224
	case strings.HasSuffix(alg, "SHA_384"):
		return crypto.SHA384, sha512.New384
	case strings.HasSuffix(alg, "SHA_512"):
		return crypto.SHA512, sha512.New
	}
	return crypto.SHA256, sha256.New
}

// validSpec checks a KeySpec / KeyUsage combination.
func validSpec(ks *keySpec) error {
	if ks.Spec == "" {
		ks.Spec = specSym
	}
	if ks.Usage == "" {
		ks.Usage = usageEncrypt
	}
	var usages []string
	switch {
	case ks.Spec == specSym:
		usages = []string{usageEncrypt}
	case rsaBits[ks.Spec] > 0:
		usages = []string{usageEncrypt, usageSign}
	case curves[ks.Spec] != nil:
		usages = []string{usageSign}
	case hmacSizes[ks.Spec] > 0:
		usages = []string{usageMAC}
	case ks.Spec == "ECC_SECG_P256K1" || strings.HasPrefix(ks.Spec, "SM2") || strings.HasPrefix(ks.Spec, "ML_DSA"):
		return errf("UnsupportedOperationException", "KeySpec %s is not supported by HomeCloud", ks.Spec)
	default:
		return core.BadRequest("KeySpec %q is invalid", ks.Spec)
	}
	if !slices.Contains(usages, ks.Usage) {
		return core.BadRequest("KeyUsage %s is not compatible with KeySpec %s", ks.Usage, ks.Spec)
	}
	return nil
}

// ---- key lifecycle ----

// CreateKeyInput describes a new customer key.
type CreateKeyInput struct {
	Description, KeySpec, KeyUsage, Policy, Origin string
	MultiRegion                                    bool
	Tags                                           core.Tags
}

func validTags(t core.Tags) error {
	if len(t) > 50 {
		return errf("TagException", "a key can have at most 50 tags")
	}
	for k, v := range t {
		if k == "" || len(k) > 128 || len(v) > 256 || strings.HasPrefix(strings.ToLower(k), "aws:") {
			return errf("TagException", "tag key %q is invalid (1-128 characters, not starting with aws:)", k)
		}
	}
	return nil
}

func validPolicy(p string) error {
	if len(p) > 32768 {
		return errf("MalformedPolicyDocumentException", "the key policy is larger than 32 KB")
	}
	var doc struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(p), &doc); err != nil || len(doc.Statement) == 0 {
		return errf("MalformedPolicyDocumentException", "The new key policy will not allow you to update the key policy in the future.")
	}
	return nil
}

func (s *Service) defaultPolicy() string {
	return `{"Version":"2012-10-17","Id":"key-default-1","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow",` +
		`"Principal":{"AWS":"arn:aws:iam::` + s.env.AccountID + `:root"},"Action":"kms:*","Resource":"*"}]}`
}

func (s *Service) createCustomerKey(az Authz, in CreateKeyInput) (Key, error) {
	if err := az("kms:CreateKey", "*"); err != nil {
		return Key{}, err
	}
	ks := keySpec{Spec: in.KeySpec, Usage: in.KeyUsage}
	if err := validSpec(&ks); err != nil {
		return Key{}, err
	}
	if in.Origin != "" && in.Origin != "AWS_KMS" {
		return Key{}, errf("UnsupportedOperationException", "Origin %s is not supported; only AWS_KMS", in.Origin)
	}
	if in.MultiRegion {
		return Key{}, errf("UnsupportedOperationException", "multi-Region keys are not supported")
	}
	if len(in.Description) > 8192 {
		return Key{}, core.BadRequest("Description must be at most 8192 characters")
	}
	if err := validTags(in.Tags); err != nil {
		return Key{}, err
	}
	if in.Policy != "" {
		if err := validPolicy(in.Policy); err != nil {
			return Key{}, err
		}
	}
	return s.createKey(ks, in.Description, false, in.Tags, in.Policy)
}

// mutate applies fn to a customer key after authorizing action.
func (s *Service) mutate(az Authz, action, ref string, fn func(*Key) error) (Key, error) {
	k, err := s.keyFor(az, action, ref)
	if err != nil {
		return k, err
	}
	k, err = store.Update(s.env.Store, cKeys, k.ID, func(x *Key) error {
		if x.Managed {
			return errf("UnsupportedOperationException", "%s is an AWS managed key and cannot be changed", k.ARN)
		}
		return fn(x)
	})
	k.ARN = core.CanonicalARN(k.ARN)
	return k, err
}

func notPending(k *Key) error {
	if k.State == statePending {
		return errf("KMSInvalidStateException", "%s is pending deletion.", k.ARN)
	}
	return nil
}

func (s *Service) setEnabled(az Authz, ref string, enabled bool) (Key, error) {
	action, state := "kms:DisableKey", stateDisabled
	if enabled {
		action, state = "kms:EnableKey", stateEnabled
	}
	return s.mutate(az, action, ref, func(k *Key) error {
		if err := notPending(k); err != nil {
			return err
		}
		k.State = state
		return nil
	})
}

func (s *Service) updateDescription(az Authz, ref, desc string) (Key, error) {
	if len(desc) > 8192 {
		return Key{}, core.BadRequest("Description must be at most 8192 characters")
	}
	return s.mutate(az, "kms:UpdateKeyDescription", ref, func(k *Key) error {
		if err := notPending(k); err != nil {
			return err
		}
		k.Description = desc
		return nil
	})
}

func rotatable(k *Key) error {
	if !k.symmetric() {
		return errf("UnsupportedOperationException", "%s is not a symmetric encryption key; only those keys support rotation.", k.ARN)
	}
	if k.State == stateDisabled {
		return errf("DisabledException", "%s is disabled.", k.ARN)
	}
	return notPending(k)
}

func (s *Service) setRotation(az Authz, ref string, enabled bool, period int) (Key, error) {
	action := "kms:DisableKeyRotation"
	if enabled {
		action = "kms:EnableKeyRotation"
		if period != 0 && (period < 90 || period > 2560) {
			return Key{}, core.BadRequest("RotationPeriodInDays must be between 90 and 2560")
		}
	}
	now := s.now()
	return s.mutate(az, action, ref, func(k *Key) error {
		if err := rotatable(k); err != nil {
			return err
		}
		k.RotationEnabled = enabled
		k.NextRotation = nil
		if enabled {
			if period != 0 {
				k.RotationPeriod = period
			}
			n := now.AddDate(0, 0, k.period())
			k.NextRotation = &n
		}
		return nil
	})
}

func (s *Service) rotateOnDemand(az Authz, ref string) (Key, error) {
	k, err := s.keyFor(az, "kms:RotateKeyOnDemand", ref)
	if err != nil {
		return k, err
	}
	if k.Managed {
		return k, errf("UnsupportedOperationException", "AWS managed keys rotate automatically")
	}
	if err := rotatable(&k); err != nil {
		return k, err
	}
	n := 0
	for _, v := range k.Versions {
		if v.Rotation == "ON_DEMAND" {
			n++
		}
	}
	if n >= maxOnDemand {
		return k, errf("LimitExceededException", "%s has reached the limit of %d on-demand rotations", k.ARN, maxOnDemand)
	}
	if err := s.rotate(k.ID, "ON_DEMAND"); err != nil {
		return k, err
	}
	return s.resolve(k.ID)
}

func (s *Service) scheduleDeletion(az Authz, ref string, days int) (Key, error) {
	if days == 0 {
		days = 30
	}
	if days < 7 || days > 30 {
		return Key{}, core.BadRequest("PendingWindowInDays must be between 7 and 30")
	}
	now := s.now()
	return s.mutate(az, "kms:ScheduleKeyDeletion", ref, func(k *Key) error {
		if err := notPending(k); err != nil {
			return err
		}
		d := now.AddDate(0, 0, days)
		k.State, k.DeletionDate, k.PendingWindow = statePending, &d, days
		return nil
	})
}

func (s *Service) cancelDeletion(az Authz, ref string) (Key, error) {
	return s.mutate(az, "kms:CancelKeyDeletion", ref, func(k *Key) error {
		if k.State != statePending {
			return errf("KMSInvalidStateException", "%s is not pending deletion.", k.ARN)
		}
		k.State, k.DeletionDate, k.PendingWindow = stateDisabled, nil, 0
		return nil
	})
}

// ---- aliases ----

var aliasRe = regexp.MustCompile(`^alias/[a-zA-Z0-9/_-]{1,250}$`)

func validAlias(a string) error {
	if !aliasRe.MatchString(a) {
		return errf("ValidationException", "Alias must start with the prefix \"alias/\" and contain only alphanumeric characters, /, _ and -")
	}
	if strings.HasPrefix(a, "alias/hc/") || strings.HasPrefix(a, "alias/aws/") {
		return errf("NotAuthorizedException", "Cannot create alias with prefix 'alias/aws/' or 'alias/hc/' (reserved for managed keys)")
	}
	return nil
}

// target resolves an alias target, which must be a customer key ID or ARN.
func (s *Service) target(az Authz, action, ref string) (Key, error) {
	if strings.HasPrefix(ref, "alias/") || strings.Contains(ref, ":alias/") {
		return Key{}, core.BadRequest("TargetKeyId must be a key ID or key ARN, not an alias")
	}
	k, err := s.keyFor(az, action, ref)
	if err != nil {
		return k, err
	}
	if k.Managed {
		return k, errf("NotAuthorizedException", "aliases cannot point at AWS managed keys")
	}
	return k, notPending(&k)
}

func (s *Service) createAlias(az Authz, name, targetRef string) (alias, error) {
	if err := validAlias(name); err != nil {
		return alias{}, err
	}
	if err := az("kms:CreateAlias", s.aliasARN(name)); err != nil {
		return alias{}, err
	}
	// Pointing an alias at a key is also an action on that key.
	k, err := s.target(az, "kms:CreateAlias", targetRef)
	if err != nil {
		return alias{}, err
	}
	if store.Has(s.env.Store, cAliases, name) {
		return alias{}, errf("AlreadyExistsException", "An alias with the name %s already exists", s.aliasARN(name))
	}
	now := s.now()
	a := alias{Name: name, KeyID: k.ID, CreatedAt: now, UpdatedAt: now}
	return a, store.Put(s.env.Store, cAliases, name, a)
}

func (s *Service) updateAlias(az Authz, name, targetRef string) (alias, error) {
	if strings.HasPrefix(name, "alias/aws/") || strings.HasPrefix(name, "alias/hc/") {
		return alias{}, errf("NotAuthorizedException", "managed aliases cannot be changed")
	}
	a, err := store.Get[alias](s.env.Store, cAliases, name)
	if err != nil {
		if aerr := az("kms:UpdateAlias", s.aliasARN(name)); aerr != nil {
			return a, aerr
		}
		return a, errf("NotFoundException", "Alias %s is not found.", s.aliasARN(name))
	}
	if err := az("kms:UpdateAlias", s.aliasARN(name)); err != nil {
		return a, err
	}
	old, err := s.keyFor(az, "kms:UpdateAlias", a.KeyID)
	if err != nil {
		return a, err
	}
	k, err := s.target(az, "kms:UpdateAlias", targetRef)
	if err != nil {
		return a, err
	}
	if old.KeyUsage != k.KeyUsage || old.symmetric() != k.symmetric() {
		return a, core.BadRequest("an alias can only move to a key of the same type and key usage (%s %s)", old.KeySpec, old.KeyUsage)
	}
	return store.Update(s.env.Store, cAliases, name, func(x *alias) error {
		x.KeyID, x.UpdatedAt = k.ID, s.now()
		return nil
	})
}

func (s *Service) deleteAlias(az Authz, name string) error {
	if strings.HasPrefix(name, "alias/aws/") || strings.HasPrefix(name, "alias/hc/") {
		return errf("NotAuthorizedException", "managed aliases cannot be deleted")
	}
	a, err := store.Get[alias](s.env.Store, cAliases, name)
	if err != nil {
		if aerr := az("kms:DeleteAlias", s.aliasARN(name)); aerr != nil {
			return aerr
		}
		return errf("NotFoundException", "Alias %s is not found.", s.aliasARN(name))
	}
	if err := az("kms:DeleteAlias", s.aliasARN(name)); err != nil {
		return err
	}
	if err := az("kms:DeleteAlias", s.env.ARN("kms", "key/"+a.KeyID)); err != nil {
		return err
	}
	if err := store.Delete(s.env.Store, cAliases, name); err != nil {
		return errf("NotFoundException", "Alias %s is not found.", s.aliasARN(name))
	}
	return nil
}

// ---- cryptography ----

func checkPlaintext(p []byte) error {
	if len(p) == 0 || len(p) > maxPlaintext {
		return core.BadRequest("Plaintext must be 1-%d bytes", maxPlaintext)
	}
	return nil
}

// encrypt encrypts under k with the given algorithm (SYMMETRIC_DEFAULT when empty).
func (s *Service) encrypt(k Key, pt []byte, ctx map[string]string, alg string) ([]byte, string, error) {
	if err := usable(k); err != nil {
		return nil, "", err
	}
	if alg == "" {
		alg = "SYMMETRIC_DEFAULT"
	}
	if !slices.Contains(encryptionAlgorithms(k), alg) {
		return nil, "", errf("InvalidKeyUsageException", "%s (%s, %s) does not support encryption algorithm %s", k.ARN, k.KeySpec, k.KeyUsage, alg)
	}
	if k.symmetric() {
		b, err := s.encryptSym(k, pt, ctx)
		return b, alg, err
	}
	if len(ctx) > 0 {
		return nil, "", core.BadRequest("EncryptionContext is not supported with asymmetric keys")
	}
	priv, err := s.privateKey(k)
	if err != nil {
		return nil, "", err
	}
	_, h := hashFor(alg)
	b, err := rsa.EncryptOAEP(h(), rand.Reader, &priv.(*rsa.PrivateKey).PublicKey, pt, nil)
	if err != nil {
		return nil, "", core.BadRequest("Plaintext is too long for %s with %s", k.KeySpec, alg)
	}
	return b, alg, nil
}

// decrypt opens a ciphertext. keyRef is optional for symmetric ciphertexts
// (they name their key) and required for asymmetric ones.
func (s *Service) decrypt(az Authz, action string, blob []byte, ctx map[string]string, keyRef, alg string) ([]byte, Key, string, error) {
	var k Key
	var err error
	if keyRef != "" {
		if k, err = s.keyFor(az, action, keyRef); err != nil {
			return nil, k, "", err
		}
	} else {
		id := blobKey(blob)
		if id == "" {
			return nil, k, "", invalidCiphertext()
		}
		// Authorize before decrypting, so callers without access learn nothing about the blob.
		if err := az(action, s.env.ARN("kms", "key/"+id)); err != nil {
			return nil, k, "", err
		}
		if k, err = s.resolve(id); err != nil {
			return nil, k, "", invalidCiphertext()
		}
	}
	if err := usable(k); err != nil {
		return nil, k, "", err
	}
	if alg == "" {
		alg = "SYMMETRIC_DEFAULT"
	}
	if !slices.Contains(encryptionAlgorithms(k), alg) {
		return nil, k, "", errf("InvalidKeyUsageException", "%s (%s, %s) does not support encryption algorithm %s", k.ARN, k.KeySpec, k.KeyUsage, alg)
	}
	if k.symmetric() {
		if id := blobKey(blob); id != "" && id != k.ID {
			return nil, k, "", errf("IncorrectKeyException", "The key ID in the request does not identify a KMS key that can perform this operation.")
		}
		pt, _, err := s.decryptSym(blob, ctx)
		return pt, k, alg, err
	}
	priv, err := s.privateKey(k)
	if err != nil {
		return nil, k, "", err
	}
	_, h := hashFor(alg)
	pt, err := rsa.DecryptOAEP(h(), rand.Reader, priv.(*rsa.PrivateKey), blob, nil)
	if err != nil {
		return nil, k, "", invalidCiphertext()
	}
	return pt, k, alg, nil
}

func dataKeySize(n int, spec string) (int, error) {
	switch {
	case n != 0 && spec != "":
		return 0, core.BadRequest("Specify either KeySpec or NumberOfBytes, not both")
	case spec == "AES_256":
		return 32, nil
	case spec == "AES_128":
		return 16, nil
	case spec != "":
		return 0, core.BadRequest("KeySpec must be AES_256 or AES_128")
	case n < 1 || n > 1024:
		return 0, core.BadRequest("NumberOfBytes must be 1-1024 (or specify KeySpec)")
	}
	return n, nil
}

// dataKey generates a data key and encrypts it under the key.
func (s *Service) dataKey(az Authz, action, ref string, n int, spec string, ctx map[string]string) ([]byte, []byte, Key, error) {
	size, err := dataKeySize(n, spec)
	if err != nil {
		return nil, nil, Key{}, err
	}
	k, err := s.keyFor(az, action, ref)
	if err != nil {
		return nil, nil, k, err
	}
	dk := make([]byte, size)
	if _, err := rand.Read(dk); err != nil {
		return nil, nil, k, err
	}
	blob, err := s.encryptSym(k, dk, ctx)
	return dk, blob, k, err
}

var pairSpecs = map[string]string{"RSA_2048": "RSA_2048", "RSA_3072": "RSA_3072", "RSA_4096": "RSA_4096",
	"ECC_NIST_P256": "ECC_NIST_P256", "ECC_NIST_P384": "ECC_NIST_P384", "ECC_NIST_P521": "ECC_NIST_P521"}

// dataKeyPair generates a key pair whose private key is encrypted under the key.
func (s *Service) dataKeyPair(az Authz, action, ref, spec string, ctx map[string]string) (priv, pub, blob []byte, k Key, err error) {
	if pairSpecs[spec] == "" {
		return nil, nil, nil, k, core.BadRequest("KeyPairSpec %q is not supported", spec)
	}
	if k, err = s.keyFor(az, action, ref); err != nil {
		return nil, nil, nil, k, err
	}
	if err = usable(k); err != nil {
		return nil, nil, nil, k, err
	}
	if priv, err = genMaterial(spec); err != nil {
		return nil, nil, nil, k, err
	}
	pk, _ := x509ParsePKCS8(priv)
	if pub, err = marshalPublic(pk); err != nil {
		return nil, nil, nil, k, err
	}
	blob, err = s.encryptSym(k, priv, ctx)
	return priv, pub, blob, k, err
}

func messageDigest(msg []byte, msgType, alg string) ([]byte, crypto.Hash, error) {
	h, newHash := hashFor(alg)
	switch msgType {
	case "", "RAW":
		if len(msg) == 0 || len(msg) > maxPlaintext {
			return nil, h, core.BadRequest("Message must be 1-%d bytes", maxPlaintext)
		}
		d := newHash()
		d.Write(msg)
		return d.Sum(nil), h, nil
	case "DIGEST":
		if len(msg) != h.Size() {
			return nil, h, core.BadRequest("Digest is invalid length for algorithm %s.", alg)
		}
		return msg, h, nil
	}
	return nil, h, core.BadRequest("MessageType must be RAW or DIGEST")
}

func (s *Service) signingKey(az Authz, action, ref, alg string) (Key, any, error) {
	k, err := s.keyFor(az, action, ref)
	if err != nil {
		return k, nil, err
	}
	if err := usable(k); err != nil {
		return k, nil, err
	}
	if k.KeyUsage != usageSign || !slices.Contains(signingAlgorithms(k), alg) {
		return k, nil, errf("InvalidKeyUsageException", "%s (%s, %s) does not support signing algorithm %s", k.ARN, k.KeySpec, k.KeyUsage, alg)
	}
	priv, err := s.privateKey(k)
	return k, priv, err
}

func (s *Service) sign(az Authz, ref string, msg []byte, msgType, alg string) ([]byte, Key, error) {
	k, priv, err := s.signingKey(az, "kms:Sign", ref, alg)
	if err != nil {
		return nil, k, err
	}
	digest, h, err := messageDigest(msg, msgType, alg)
	if err != nil {
		return nil, k, err
	}
	switch p := priv.(type) {
	case *ecdsa.PrivateKey:
		sig, err := ecdsa.SignASN1(rand.Reader, p, digest)
		return sig, k, err
	case *rsa.PrivateKey:
		if strings.HasPrefix(alg, "RSASSA_PSS") {
			sig, err := rsa.SignPSS(rand.Reader, p, h, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
			return sig, k, err
		}
		sig, err := rsa.SignPKCS1v15(rand.Reader, p, h, digest)
		return sig, k, err
	}
	return nil, k, errf("InvalidKeyUsageException", "%s cannot sign", k.ARN)
}

func (s *Service) verify(az Authz, ref string, msg []byte, msgType, alg string, sig []byte) (Key, error) {
	k, priv, err := s.signingKey(az, "kms:Verify", ref, alg)
	if err != nil {
		return k, err
	}
	digest, h, err := messageDigest(msg, msgType, alg)
	if err != nil {
		return k, err
	}
	ok := false
	switch p := priv.(type) {
	case *ecdsa.PrivateKey:
		ok = ecdsa.VerifyASN1(&p.PublicKey, digest, sig)
	case *rsa.PrivateKey:
		if strings.HasPrefix(alg, "RSASSA_PSS") {
			ok = rsa.VerifyPSS(&p.PublicKey, h, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
		} else {
			ok = rsa.VerifyPKCS1v15(&p.PublicKey, h, digest, sig) == nil
		}
	}
	if !ok {
		return k, errf("KMSInvalidSignatureException", "The signature verification failed.")
	}
	return k, nil
}

func (s *Service) mac(az Authz, action, ref string, msg []byte, alg string) ([]byte, Key, error) {
	k, err := s.keyFor(az, action, ref)
	if err != nil {
		return nil, k, err
	}
	if err := usable(k); err != nil {
		return nil, k, err
	}
	if !slices.Contains(macAlgorithms(k), alg) {
		return nil, k, errf("InvalidKeyUsageException", "%s (%s, %s) does not support MAC algorithm %s", k.ARN, k.KeySpec, k.KeyUsage, alg)
	}
	if len(msg) == 0 || len(msg) > maxPlaintext {
		return nil, k, core.BadRequest("Message must be 1-%d bytes", maxPlaintext)
	}
	mat, _, err := s.current(k)
	if err != nil {
		return nil, k, err
	}
	_, h := hashFor(alg)
	m := hmac.New(h, mat)
	m.Write(msg)
	return m.Sum(nil), k, nil
}

func (s *Service) verifyMac(az Authz, ref string, msg []byte, alg string, want []byte) (Key, error) {
	got, k, err := s.mac(az, "kms:VerifyMac", ref, msg, alg)
	if err != nil {
		return k, err
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return k, errf("KMSInvalidMacException", "The MAC verification failed.")
	}
	return k, nil
}

// ---- tags, policies, grants ----

func (s *Service) tag(az Authz, ref string, add core.Tags, remove []string) (Key, error) {
	action := "kms:TagResource"
	if add == nil {
		action = "kms:UntagResource"
	}
	k, err := s.keyFor(az, action, ref)
	if err != nil {
		return k, err
	}
	if k.Managed {
		return k, errf("UnsupportedOperationException", "AWS managed keys cannot be tagged")
	}
	return store.Update(s.env.Store, cKeys, k.ID, func(x *Key) error {
		if err := notPending(x); err != nil {
			return err
		}
		t := core.Tags{}
		for k, v := range x.Tags {
			t[k] = v
		}
		for k, v := range add {
			t[k] = v
		}
		for _, k := range remove {
			delete(t, k)
		}
		if err := validTags(t); err != nil {
			return err
		}
		x.Tags = t
		return nil
	})
}

func (s *Service) policyOf(k Key) string {
	if k.Policy != "" {
		return k.Policy
	}
	return s.defaultPolicy()
}

func checkPolicyName(name string) error {
	if name != "" && name != "default" {
		return errf("NotFoundException", "No such policy exists: %s", name)
	}
	return nil
}

func (s *Service) putPolicy(az Authz, ref, name, policy string) error {
	if err := checkPolicyName(name); err != nil {
		return err
	}
	if err := validPolicy(policy); err != nil {
		return err
	}
	_, err := s.mutate(az, "kms:PutKeyPolicy", ref, func(k *Key) error {
		if err := notPending(k); err != nil {
			return err
		}
		k.Policy = policy
		return nil
	})
	return err
}

var grantOps = []string{"Decrypt", "Encrypt", "GenerateDataKey", "GenerateDataKeyWithoutPlaintext", "ReEncryptFrom", "ReEncryptTo",
	"Sign", "Verify", "GetPublicKey", "CreateGrant", "RetireGrant", "DescribeKey", "GenerateDataKeyPair",
	"GenerateDataKeyPairWithoutPlaintext", "GenerateMac", "VerifyMac", "DeriveSharedSecret"}

func (s *Service) createGrant(az Authz, ref string, g Grant) (Grant, error) {
	if g.GranteePrincipal == "" {
		return g, core.BadRequest("GranteePrincipal is required")
	}
	if len(g.Operations) == 0 {
		return g, core.BadRequest("Operations is required")
	}
	for _, op := range g.Operations {
		if !slices.Contains(grantOps, op) {
			return g, core.BadRequest("grant operation %q is invalid", op)
		}
	}
	k, err := s.keyFor(az, "kms:CreateGrant", ref)
	if err != nil {
		return g, err
	}
	if err := notPending(&k); err != nil {
		return g, err
	}
	// Retrying CreateGrant with the same name and parameters returns the existing grant.
	for _, x := range k.Grants {
		if g.Name != "" && x.Name == g.Name && x.GranteePrincipal == g.GranteePrincipal && slices.Equal(x.Operations, g.Operations) {
			return x, nil
		}
	}
	g.ID, g.Token, g.CreatedAt = core.RandHex(64), core.RandHex(64)+core.RandHex(64), s.now()
	_, err = store.Update(s.env.Store, cKeys, k.ID, func(x *Key) error {
		if len(x.Grants) >= 50000 {
			return errf("LimitExceededException", "too many grants")
		}
		x.Grants = append(slices.Clone(x.Grants), g)
		return nil
	})
	return g, err
}

// removeGrant revokes (by key and grant ID) or retires (by token, or key and grant ID) a grant.
func (s *Service) removeGrant(az Authz, action, ref, grantID, token string) error {
	var k Key
	var err error
	if ref == "" && token != "" {
		for _, x := range store.List[Key](s.env.Store, cKeys) {
			if slices.ContainsFunc(x.Grants, func(g Grant) bool { return g.Token == token }) {
				k = x
			}
		}
		if k.ID == "" {
			return errf("InvalidGrantTokenException", "The grant token is invalid.")
		}
		if err := az(action, core.CanonicalARN(k.ARN)); err != nil {
			return err
		}
	} else if k, err = s.keyFor(az, action, ref); err != nil {
		return err
	}
	_, err = store.Update(s.env.Store, cKeys, k.ID, func(x *Key) error {
		i := slices.IndexFunc(x.Grants, func(g Grant) bool { return (grantID != "" && g.ID == grantID) || (token != "" && g.Token == token) })
		if i < 0 {
			return errf("NotFoundException", "Grant ID %s not found", grantID)
		}
		x.Grants = slices.Delete(slices.Clone(x.Grants), i, i+1)
		return nil
	})
	return err
}

func grantOut(k Key, g Grant) map[string]any {
	m := map[string]any{"KeyId": k.ARN, "GrantId": g.ID, "GranteePrincipal": g.GranteePrincipal, "Operations": g.Operations,
		"IssuingAccount": "arn:aws:iam::" + strings.Split(k.ARN, ":")[4] + ":root", "CreationDate": epochT(g.CreatedAt)}
	if g.Name != "" {
		m["Name"] = g.Name
	}
	if g.RetiringPrincipal != "" {
		m["RetiringPrincipal"] = g.RetiringPrincipal
	}
	if len(g.Constraints) > 0 {
		m["Constraints"] = g.Constraints
	}
	return m
}

func epochT(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }
