package sns

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SignatureVersion 1 is SHA1withRSA, as in AWS
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Messages to HTTP(S), email-json, SQS and Lambda subscribers are signed as
// Amazon SNS signs them (SignatureVersion 1: SHA1withRSA, 2: SHA256withRSA),
// with a key whose self-signed certificate is served at SigningCertURL.

type signer struct {
	key     *rsa.PrivateKey
	certPEM []byte
}

// signingKey loads (or creates) the SNS signing key and certificate.
func (s *Service) signingKey() *signer {
	s.signOnce.Do(func() {
		dir := s.env.Cfg.Path("sns")
		keyFile, certFile := filepath.Join(dir, "signing-key.pem"), filepath.Join(dir, "signing-cert.pem")
		if kb, err := os.ReadFile(keyFile); err == nil {
			if cb, err := os.ReadFile(certFile); err == nil {
				if blk, _ := pem.Decode(kb); blk != nil {
					if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
						s.sign = &signer{key: k, certPEM: cb}
						return
					}
				}
			}
		}
		sg, err := newSigner()
		if err != nil {
			log.Printf("sns: create signing key: %v", err)
			return
		}
		s.sign = sg
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(sg.key)}), 0o600)
		_ = os.WriteFile(certFile, sg.certPEM, 0o644)
	})
	return s.sign
}

func newSigner() (*signer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "sns.homecloud.local", Organization: []string{"HomeCloud"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &signer{key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

// stringToSign builds the canonical string AWS signs for an SNS message.
func stringToSign(n *notification) string {
	var keys []string
	if n.Type == "Notification" {
		keys = []string{"Message", "MessageId", "Subject", "Timestamp", "TopicArn", "Type"}
	} else {
		keys = []string{"Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"}
	}
	vals := map[string]string{"Message": n.Message, "MessageId": n.MessageId, "Subject": n.Subject, "Timestamp": n.Timestamp,
		"TopicArn": n.TopicArn, "Type": n.Type, "SubscribeURL": n.SubscribeURL, "Token": n.Token}
	out := ""
	for _, k := range keys {
		if k == "Subject" && n.Subject == "" {
			continue
		}
		out += k + "\n" + vals[k] + "\n"
	}
	return out
}

// signNotification fills Signature for n's SignatureVersion.
func (s *Service) signNotification(n *notification) {
	sg := s.signingKey()
	if sg == nil {
		return
	}
	msg := []byte(stringToSign(n))
	var (
		sig []byte
		err error
	)
	if n.SignatureVersion == "2" {
		h := sha256.Sum256(msg)
		sig, err = rsa.SignPKCS1v15(rand.Reader, sg.key, crypto.SHA256, h[:])
	} else {
		h := sha1.Sum(msg) //nolint:gosec
		sig, err = rsa.SignPKCS1v15(rand.Reader, sg.key, crypto.SHA1, h[:])
	}
	if err == nil {
		n.Signature = base64.StdEncoding.EncodeToString(sig)
	}
}

// VerifyNotification checks a notification's signature against certPEM
// (as SNS message validators do); exported for tests.
func VerifyNotification(n map[string]string, certPEM []byte) error {
	blk, _ := pem.Decode(certPEM)
	if blk == nil {
		return errors.New("bad certificate")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return err
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return errors.New("not an RSA key")
	}
	sig, err := base64.StdEncoding.DecodeString(n["Signature"])
	if err != nil {
		return err
	}
	msg := []byte(stringToSign(&notification{Type: n["Type"], MessageId: n["MessageId"], Token: n["Token"], TopicArn: n["TopicArn"],
		Subject: n["Subject"], Message: n["Message"], SubscribeURL: n["SubscribeURL"], Timestamp: n["Timestamp"]}))
	if n["SignatureVersion"] == "2" {
		h := sha256.Sum256(msg)
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig)
	}
	h := sha1.Sum(msg) //nolint:gosec
	return rsa.VerifyPKCS1v15(pub, crypto.SHA1, h[:], sig)
}
