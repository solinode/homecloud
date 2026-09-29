package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// SelfSignedCert creates (once) a certificate for localhost and the given
// public host in dataDir/tls and returns the cert and key paths.
func SelfSignedCert(dataDir, publicHost string) (string, string, error) {
	dir := filepath.Join(dataDir, "tls")
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if _, err := os.Stat(certPath); err == nil {
		return certPath, keyPath, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "HomeCloud", Organization: []string{"HomeCloud"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if ip := net.ParseIP(publicHost); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	} else if publicHost != "" && publicHost != "localhost" {
		tmpl.DNSNames = append(tmpl.DNSNames, publicHost)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", err
	}
	return certPath, keyPath, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
}
