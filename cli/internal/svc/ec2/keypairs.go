package ec2

import (
	"crypto/ed25519"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"golang.org/x/crypto/ssh"
)

// Key pairs: HomeCloud keeps the public key and writes it to root's
// ~/.ssh/authorized_keys in instances launched with the key. Images that run
// an SSH server accept the private key; the others are reached through the
// terminal (EC2 Instance Connect equivalent) or `homecloud ec2 exec`.

const cKeyPairs = "ec2_key_pairs"

type KeyPair struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"` // rsa | ed25519
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"` // OpenSSH authorized_keys format
	CreatedAt   time.Time `json:"created_at"`
	Tags        core.Tags `json:"tags,omitempty"`
}

var keyNameRE = regexp.MustCompile(`^[\x20-\x7e]{1,255}$`)

// keyPair finds a key pair by name or ID.
func (s *Service) keyPair(ref string) (KeyPair, error) {
	if strings.HasPrefix(ref, "key-") {
		if k, err := store.Get[KeyPair](s.env.Store, cKeyPairs, ref); err == nil {
			return k, nil
		}
	}
	for _, k := range store.List[KeyPair](s.env.Store, cKeyPairs) {
		if k.Name == ref {
			return k, nil
		}
	}
	return KeyPair{}, core.Errf(http.StatusBadRequest, "InvalidKeyPair.NotFound", "The key pair '%s' does not exist", ref)
}

func (s *Service) checkKeyName(name string) error {
	if !keyNameRE.MatchString(name) {
		return core.Errf(http.StatusBadRequest, "InvalidParameterValue", "key pair names are 1 to 255 ASCII characters")
	}
	for _, k := range store.List[KeyPair](s.env.Store, cKeyPairs) {
		if k.Name == name {
			return core.Errf(http.StatusBadRequest, "InvalidKeyPair.Duplicate", "The keypair '%s' already exists.", name)
		}
	}
	return nil
}

// CreateKeyPair generates a key pair and returns it with its private key
// (PEM for RSA, OpenSSH format for ed25519), which HomeCloud does not keep.
func (s *Service) CreateKeyPair(name, keyType string, tags core.Tags) (KeyPair, string, error) {
	if err := s.checkKeyName(name); err != nil {
		return KeyPair{}, "", err
	}
	k := KeyPair{ID: core.NewID("key"), Name: name, Type: keyType, CreatedAt: core.Now(), Tags: tags}
	var private string
	switch keyType {
	case "", "rsa":
		k.Type = "rsa"
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return k, "", err
		}
		private = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
		pub, err := ssh.NewPublicKey(&key.PublicKey)
		if err != nil {
			return k, "", err
		}
		k.PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + name
		der, _ := x509.MarshalPKCS8PrivateKey(key)
		sum := sha1.Sum(der) // AWS: SHA-1 of the DER private key for keys it creates
		k.Fingerprint = colonHex(sum[:])
	case "ed25519":
		pubKey, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return k, "", err
		}
		block, err := ssh.MarshalPrivateKey(key, name)
		if err != nil {
			return k, "", err
		}
		private = string(pem.EncodeToMemory(block))
		pub, err := ssh.NewPublicKey(pubKey)
		if err != nil {
			return k, "", err
		}
		k.PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + name
		k.Fingerprint = sha256Fingerprint(pub)
	default:
		return k, "", core.Errf(http.StatusBadRequest, "InvalidParameterValue", "key type must be rsa or ed25519")
	}
	return k, private, store.Put(s.env.Store, cKeyPairs, k.ID, k)
}

// ImportKeyPair records a public key (OpenSSH, or PEM/DER as AWS accepts).
func (s *Service) ImportKeyPair(name string, material []byte, tags core.Tags) (KeyPair, error) {
	if err := s.checkKeyName(name); err != nil {
		return KeyPair{}, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(material)
	if err != nil {
		// PEM or DER encoded public keys.
		der := material
		if b, _ := pem.Decode(material); b != nil {
			der = b.Bytes
		}
		pk, perr := x509.ParsePKIXPublicKey(der)
		if perr != nil {
			return KeyPair{}, core.Errf(http.StatusBadRequest, "InvalidKey.Format", "Key is not in valid OpenSSH public key format")
		}
		if pub, err = ssh.NewPublicKey(pk); err != nil {
			return KeyPair{}, core.Errf(http.StatusBadRequest, "InvalidKey.Format", "unsupported key: %v", err)
		}
	}
	k := KeyPair{ID: core.NewID("key"), Name: name, CreatedAt: core.Now(), Tags: tags,
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))}
	switch pub.Type() {
	case ssh.KeyAlgoRSA:
		k.Type = "rsa"
		// AWS: MD5 of the DER public key for imported RSA keys.
		if cpk, ok := pub.(ssh.CryptoPublicKey); ok {
			if der, err := x509.MarshalPKIXPublicKey(cpk.CryptoPublicKey()); err == nil {
				sum := md5.Sum(der)
				k.Fingerprint = colonHex(sum[:])
			}
		}
	case ssh.KeyAlgoED25519:
		k.Type = "ed25519"
		k.Fingerprint = sha256Fingerprint(pub)
	default:
		return KeyPair{}, core.Errf(http.StatusBadRequest, "InvalidKey.Format", "only RSA and ED25519 keys are supported, not %s", pub.Type())
	}
	// Keep the key's comment, as authorized_keys lines usually carry one.
	if f := strings.Fields(string(material)); len(f) > 2 && strings.HasPrefix(f[0], "ssh-") {
		k.PublicKey += " " + strings.Join(f[2:], " ")
	}
	return k, store.Put(s.env.Store, cKeyPairs, k.ID, k)
}

// DeleteKeyPair removes a key pair by name or ID; a missing key is not an
// error (as in EC2).
func (s *Service) DeleteKeyPair(ref string) error {
	k, err := s.keyPair(ref)
	if err != nil {
		return nil
	}
	return store.Delete(s.env.Store, cKeyPairs, k.ID)
}

func colonHex(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	return strings.Join(parts, ":")
}

func sha256Fingerprint(pub ssh.PublicKey) string {
	sum := sha256.Sum256(pub.Marshal())
	return base64.StdEncoding.EncodeToString(sum[:])
}
