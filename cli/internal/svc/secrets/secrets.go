// Package secrets implements Secrets Manager: versioned secret values with
// staging labels, encrypted at rest with AES-256-GCM under a per-installation
// master key (or with a customer KMS key), rotation through Lambda functions and
// scheduled deletion with a recovery window.
//
// The same operations are served by the native API (Routes) and the AWS
// awsJson1.1 protocol (RegisterAWS); both call the methods in ops.go.
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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	cSecrets     = "secrets"
	maxVersions  = 100 // versions without a staging label beyond this are dropped, oldest first
	maxValue     = 65536
	stageCurrent = "AWSCURRENT"
	stagePrev    = "AWSPREVIOUS"
	stagePending = "AWSPENDING"
)

type Version struct {
	ID     string   `json:"id"`
	Stages []string `json:"stages"`
	// Ciphertext is the sealed value; empty for the placeholder version a
	// rotation creates before its function stores the new value.
	Ciphertext   string     `json:"ciphertext,omitempty"`
	Binary       bool       `json:"binary,omitempty"`  // the value is SecretBinary
	KMSKey       string     `json:"kms_key,omitempty"` // ARN of the customer KMS key that sealed the value
	CreatedAt    time.Time  `json:"created_at"`
	LastAccessed *time.Time `json:"last_accessed,omitempty"`
}

// RotationRules is the rotation schedule of a secret.
type RotationRules struct {
	AutomaticallyAfterDays int64  `json:"AutomaticallyAfterDays,omitempty"`
	Duration               string `json:"Duration,omitempty"`
	ScheduleExpression     string `json:"ScheduleExpression,omitempty"`
}

type Secret struct {
	Name         string     `json:"name"`
	ARN          string     `json:"arn"`
	Description  string     `json:"description"`
	ManagedBy    string     `json:"managed_by,omitempty"` // e.g. "rds" for generated database credentials
	KMSKeyID     string     `json:"kms_key_id,omitempty"` // customer key ARN; empty for the default key
	Versions     []Version  `json:"versions"`             // newest first
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastAccessed *time.Time `json:"last_accessed,omitempty"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`    // when DeleteSecret was called
	DeletionDate *time.Time `json:"deletion_date,omitempty"` // when the secret is purged
	Tags         core.Tags  `json:"tags,omitempty"`
	Policy       string     `json:"resource_policy,omitempty"`

	RotationEnabled   bool           `json:"rotation_enabled,omitempty"`
	RotationLambdaARN string         `json:"rotation_lambda_arn,omitempty"`
	RotationRules     *RotationRules `json:"rotation_rules,omitempty"`
	LastRotated       *time.Time     `json:"last_rotated,omitempty"`
	NextRotation      *time.Time     `json:"next_rotation,omitempty"`
	RotationError     string         `json:"rotation_error,omitempty"` // why the last rotation failed
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

// version returns the index of the version with the given ID, or -1.
func (s Secret) version(id string) int {
	return slices.IndexFunc(s.Versions, func(v Version) bool { return v.ID == id })
}

// staged returns the index of the version carrying stage, or -1.
func (s Secret) staged(stage string) int {
	return slices.IndexFunc(s.Versions, func(v Version) bool { return slices.Contains(v.Stages, stage) })
}

// KeyService is the part of KMS that Secrets Manager uses for secrets
// protected by a customer key (implemented by kms.Service).
type KeyService interface {
	Encrypt(ref string, plaintext []byte, ctx map[string]string) (blob, keyARN string, err error)
	Decrypt(blob string, ctx map[string]string) ([]byte, string, error)
	KeyARN(ref string) (string, error)
}

type Service struct {
	env  *svc.Env
	aead cipher.AEAD
	mu   sync.Mutex // serialises creation

	// Now is the clock (tests replace it); nil means time.Now.
	Now func() time.Time
	// KMS seals secrets that name a KmsKeyId; nil disables customer keys.
	KMS KeyService
	// Lambda runs rotation functions; nil disables rotation.
	Lambda Invoker
	cfgMu  sync.RWMutex // guards Lambda once the service is serving

	rotMu    sync.Mutex
	rotating map[string]bool
	rotWG    sync.WaitGroup
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return core.Now()
}

// managed rejects API changes to secrets that a service owns (RDS master
// credentials, the S3 root keys); they change through that service.
func managed(sec Secret) error {
	if sec.ManagedBy != "" {
		return core.Errf(http.StatusBadRequest, "InvalidRequestException", "secret %q is managed by %s and cannot be changed directly", sec.Name, sec.ManagedBy)
	}
	return nil
}

// Authz checks the caller may perform an IAM action on a resource
// (httpx.Ctx.Authorize or awsapi.Req.Authorize).
type Authz func(action, resource string) error

// readCheck adds the service-level permission some managed secrets need.
func readCheck(az Authz, sec Secret) error {
	if sec.ManagedBy == "s3" { // MinIO root keys are as powerful as s3:AdministerServiceCredentials
		return az("s3:AdministerServiceCredentials", "*")
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
	return &Service{env: env, aead: aead, rotating: map[string]bool{}}, nil
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

// Encrypt seals plain under the installation's master key.
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

// seal encrypts a version's value with the secret's key.
func (s *Service) seal(sec Secret, v *Version, value []byte) error {
	v.KMSKey = ""
	if sec.KMSKeyID == "" {
		v.Ciphertext = s.Encrypt(value)
		return nil
	}
	if s.KMS == nil {
		return core.Errf(http.StatusBadRequest, "EncryptionFailure", "KMS is not available")
	}
	blob, arn, err := s.KMS.Encrypt(sec.KMSKeyID, value, map[string]string{"SecretARN": sec.ARN, "SecretVersionId": v.ID})
	if err != nil {
		return core.Errf(http.StatusBadRequest, "EncryptionFailure", "Secrets Manager can't encrypt the secret value with KMS key %s: %s", sec.KMSKeyID, errMessage(err))
	}
	v.Ciphertext, v.KMSKey = blob, arn
	return nil
}

// open decrypts a version's value.
func (s *Service) open(sec Secret, v Version) ([]byte, error) {
	if v.Ciphertext == "" {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFound", "Secrets Manager can't find the specified secret value for VersionId: %s", v.ID)
	}
	if v.KMSKey == "" {
		return s.Decrypt(v.Ciphertext)
	}
	if s.KMS == nil {
		return nil, core.Errf(http.StatusBadRequest, "DecryptionFailure", "KMS is not available")
	}
	p, _, err := s.KMS.Decrypt(v.Ciphertext, map[string]string{"SecretARN": sec.ARN, "SecretVersionId": v.ID})
	if err != nil {
		return nil, core.Errf(http.StatusBadRequest, "DecryptionFailure", "Secrets Manager can't decrypt the protected secret text using the provided KMS key: %s", errMessage(err))
	}
	return p, nil
}

func errMessage(err error) string {
	var ce *core.Error
	if errors.As(err, &ce) {
		return ce.Message
	}
	return err.Error()
}

var nameRe = regexp.MustCompile(`^[\w/+=.@!-]{1,512}$`)

func validName(name string) error {
	if !nameRe.MatchString(name) || strings.HasPrefix(name, "arn:") {
		return core.BadRequest("secret name %q is invalid: use 1-512 letters, digits and /_+=.@-", name)
	}
	return nil
}

// newARN returns an ARN for a new secret, with the random suffix AWS adds.
func (s *Service) newARN(name string) string {
	return s.env.ARN("secretsmanager", "secret:"+name+"-"+core.NewSecret(6))
}

// find looks a secret up by name, full ARN or partial ARN (without the suffix).
func (s *Service) find(ref string) (Secret, error) {
	notFound := core.Errf(http.StatusNotFound, "ResourceNotFound", "Secrets Manager can't find the specified secret.")
	if ref == "" {
		return Secret{}, core.BadRequest("SecretId is required")
	}
	if !strings.HasPrefix(ref, "arn:") {
		return s.get(ref)
	}
	_, rest, ok := strings.Cut(ref, ":secret:")
	if !ok || rest == "" {
		return Secret{}, notFound
	}
	// Full ARN: name + "-" + 6 random characters.
	if n := len(rest); n > 7 && rest[n-7] == '-' {
		if sec, err := s.get(rest[:n-7]); err == nil && sec.ARN == core.CanonicalARN(ref) {
			return sec, nil
		}
	}
	// Partial ARN, or the ARN of a secret created before ARNs had a suffix.
	sec, err := s.get(rest)
	if err != nil {
		return sec, notFound
	}
	if arnPrefix(sec.ARN) != arnPrefix(ref) { // another account or region
		return Secret{}, notFound
	}
	return sec, nil
}

func arnPrefix(arn string) string {
	p, _, _ := strings.Cut(core.CanonicalARN(arn), ":secret:")
	return p
}

// get reads a secret by name.
func (s *Service) get(name string) (Secret, error) {
	sec, err := store.Get[Secret](s.env.Store, cSecrets, name)
	if err != nil {
		return sec, core.Errf(http.StatusNotFound, "ResourceNotFound", "Secrets Manager can't find the specified secret.")
	}
	sec.ARN = core.CanonicalARN(sec.ARN)
	return sec, nil
}

// refARN is the ARN to authorize for a reference that may not exist.
func (s *Service) refARN(ref string) string {
	if strings.HasPrefix(ref, "arn:") {
		return ref
	}
	return s.env.ARN("secretsmanager", "secret:"+ref)
}

// lookup finds the secret and authorizes action on it. For a missing secret it
// authorizes against the reference so callers without access learn nothing.
func (s *Service) lookup(az Authz, action, ref string) (Secret, error) {
	sec, err := s.find(ref)
	if err != nil {
		if aerr := az(action, s.refARN(ref)); aerr != nil {
			return sec, aerr
		}
		return sec, err
	}
	return sec, az(action, sec.ARN)
}

func deletedErr(sec Secret, what string) error {
	return core.Errf(http.StatusBadRequest, "InvalidRequestException",
		"You can't perform this operation on the secret because it was marked for deletion.%s", what)
}

// ---- internal API used by other services (no IAM checks) ----

// ARN returns the ARN of a secret, for other services that report it.
func (s *Service) ARN(name string) (string, bool) {
	sec, err := s.get(name)
	return sec.ARN, err == nil
}

// Put creates the secret or adds a new current version; used by other services.
func (s *Service) Put(name, value, description, managedBy string) (Secret, error) {
	if err := validName(name); err != nil {
		return Secret{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if store.Has(s.env.Store, cSecrets, name) {
		sec, _, err := s.addVersion(name, []byte(value), false, "", nil, func(sec Secret) error {
			if sec.DeletionDate != nil {
				return core.Errf(http.StatusBadRequest, "InvalidRequestException", "secret %q is scheduled for deletion; restore it first", name)
			}
			return nil
		})
		return sec, err
	}
	sec := Secret{Name: name, ARN: s.newARN(name), Description: description, ManagedBy: managedBy, CreatedAt: s.now(), UpdatedAt: s.now()}
	v := Version{ID: uuid(), Stages: []string{stageCurrent}, CreatedAt: s.now()}
	if err := s.seal(sec, &v, []byte(value)); err != nil {
		return sec, err
	}
	sec.Versions = []Version{v}
	return sec, store.Put(s.env.Store, cSecrets, name, sec)
}

// Value returns the plaintext of the version with the given stage (AWSCURRENT
// when empty) or ID. The secret may be named by name or ARN.
func (s *Service) Value(name, stage, versionID string) (string, Version, error) {
	sec, err := s.find(name)
	if err != nil {
		return "", Version{}, core.NotFound("secret", name)
	}
	_, v, p, err := s.value(sec, versionID, stage)
	return string(p), v, err
}

// Remove deletes a secret immediately (used when its owning resource is deleted).
func (s *Service) Remove(name string) { _ = store.Delete(s.env.Store, cSecrets, name) }

// PurgeExpired permanently deletes secrets whose recovery window has passed
// and starts scheduled rotations that are due. The server calls it hourly.
func (s *Service) PurgeExpired() {
	s.purgeDeleted()
	now := s.now()
	for _, sec := range store.List[Secret](s.env.Store, cSecrets) {
		if s.lambda() != nil && sec.DeletionDate == nil && sec.RotationEnabled && sec.NextRotation != nil && !now.Before(*sec.NextRotation) && !s.pendingRotation(sec) {
			_, _ = s.startRotation(sec.Name, "")
		}
	}
}

func (s *Service) purgeDeleted() {
	now := s.now()
	for _, sec := range store.List[Secret](s.env.Store, cSecrets) {
		if sec.DeletionDate != nil && !now.Before(*sec.DeletionDate) {
			_ = store.Delete(s.env.Store, cSecrets, sec.Name)
		}
	}
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := fmt.Sprintf("%x", b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// SetLambda replaces the rotation invoker while the service is running.
func (s *Service) SetLambda(inv Invoker) {
	s.cfgMu.Lock()
	s.Lambda = inv
	s.cfgMu.Unlock()
}

func (s *Service) lambda() Invoker {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.Lambda
}
