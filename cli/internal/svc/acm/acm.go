// Package acm implements certificate management: a per-installation private
// certificate authority that issues TLS certificates for your domains, and
// import of certificates issued elsewhere (e.g. Let's Encrypt). Load balancer
// HTTPS listeners use these certificates.
package acm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
)

const (
	cCerts = "acm_certificates"
	cCA    = "acm_ca"
)

type Certificate struct {
	ARN        string    `json:"arn"`
	ID         string    `json:"id"`
	DomainName string    `json:"domain_name"`
	SANs       []string  `json:"subject_alternative_names"`
	Type       string    `json:"type"` // PRIVATE | IMPORTED
	Status     string    `json:"status"`
	Issuer     string    `json:"issuer"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	Serial     string    `json:"serial"`
	CertPEM    string    `json:"certificate,omitempty"`
	ChainPEM   string    `json:"certificate_chain,omitempty"`
	KeyCT      string    `json:"key_ct,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Tags       core.Tags `json:"tags,omitempty"`
}

func (c Certificate) view() Certificate {
	c.KeyCT = ""
	if time.Now().After(c.NotAfter) {
		c.Status = "EXPIRED"
	}
	return c
}

type authority struct {
	CertPEM string `json:"certificate"`
	KeyCT   string `json:"key_ct"`
}

type Service struct {
	env     *svc.Env
	secrets *secrets.Service
	mu      sync.Mutex
	// InUse reports whether a load balancer uses a certificate (set by ELB).
	InUse func(arn string) bool
	// OnRenew is told when a certificate's material changes (ELB reloads listeners).
	OnRenew func(arn string)
}

func New(env *svc.Env, sec *secrets.Service) *Service { return &Service{env: env, secrets: sec} }

// ca returns the installation's CA, creating it on first use.
func (s *Service) ca() (*x509.Certificate, *ecdsa.PrivateKey, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := store.Get[authority](s.env.Store, cCA, "root")
	if errors.Is(err, store.ErrNotFound) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, "", err
		}
		serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
		tmpl := &x509.Certificate{
			SerialNumber: serial, Subject: pkix.Name{CommonName: "HomeCloud Private CA " + s.env.AccountID, Organization: []string{"HomeCloud"}},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
			KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			return nil, nil, "", err
		}
		kb, _ := x509.MarshalECPrivateKey(key)
		a = authority{CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), KeyCT: s.secrets.Encrypt(kb)}
		if err := store.Put(s.env.Store, cCA, "root", a); err != nil {
			return nil, nil, "", err
		}
	} else if err != nil {
		return nil, nil, "", err
	}
	blk, _ := pem.Decode([]byte(a.CertPEM))
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, nil, "", err
	}
	kb, err := s.secrets.Decrypt(a.KeyCT)
	if err != nil {
		return nil, nil, "", err
	}
	key, err := x509.ParseECPrivateKey(kb)
	return cert, key, a.CertPEM, err
}

// KeyPair returns the PEM certificate chain and private key for a certificate ARN.
func (s *Service) KeyPair(arn string) (certChain, key []byte, err error) {
	c, err := s.byARN(arn)
	if err != nil {
		return nil, nil, err
	}
	k, err := s.secrets.Decrypt(c.KeyCT)
	if err != nil {
		return nil, nil, err
	}
	return []byte(c.CertPEM + c.ChainPEM), k, nil
}

func (s *Service) byARN(arn string) (Certificate, error) {
	id := arn[strings.LastIndex(arn, "/")+1:]
	c, err := store.Get[Certificate](s.env.Store, cCerts, id)
	if err != nil {
		return c, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "certificate %q does not exist", arn)
	}
	return c, nil
}

// Exists reports whether a certificate ARN exists and is usable.
func (s *Service) Exists(arn string) bool {
	c, err := s.byARN(arn)
	return err == nil && time.Now().Before(c.NotAfter)
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

var domainRe = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:acm:local-1:{account}:certificate/{id}")
	r.Handle("GET /api/v1/acm/certificates", "acm:ListCertificates", s.list)
	r.Handle("POST /api/v1/acm/certificates", "acm:RequestCertificate", s.request)
	r.Handle("POST /api/v1/acm/certificates/import", "acm:ImportCertificate", s.importCert)
	r.Handle("GET /api/v1/acm/certificates/{id}", "acm:DescribeCertificate", s.get, res)
	r.Handle("POST /api/v1/acm/certificates/{id}/renew", "acm:RenewCertificate", s.renew, res)
	r.Handle("DELETE /api/v1/acm/certificates/{id}", "acm:DeleteCertificate", s.delete, res)
	r.Handle("GET /api/v1/acm/ca", "acm-pca:GetCertificateAuthorityCertificate", s.caCert)
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []Certificate{}
	for _, x := range store.List[Certificate](s.env.Store, cCerts) {
		v := x.view()
		v.CertPEM, v.ChainPEM = "", ""
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) issue(domain string, sans []string, days int) (Certificate, error) {
	caCert, caKey, caPEM, err := s.ca()
	if err != nil {
		return Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(0, 0, days),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range append([]string{domain}, sans...) {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return Certificate{}, err
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	id := uuid()
	return Certificate{ARN: s.env.ARN("acm", "certificate/"+id), ID: id, DomainName: domain, SANs: sans, Type: "PRIVATE", Status: "ISSUED",
		Issuer: caCert.Subject.CommonName, NotBefore: tmpl.NotBefore.UTC(), NotAfter: tmpl.NotAfter.UTC(), Serial: hex.EncodeToString(serial.Bytes()),
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), ChainPEM: caPEM,
		KeyCT: s.secrets.Encrypt(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})), CreatedAt: core.Now()}, nil
}

func (s *Service) request(c *httpx.Ctx) (any, error) {
	var in struct {
		DomainName string    `json:"domain_name"`
		SANs       []string  `json:"subject_alternative_names"`
		ValidDays  int       `json:"valid_days"`
		Tags       core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	for _, d := range append([]string{in.DomainName}, in.SANs...) {
		if net.ParseIP(d) == nil && !domainRe.MatchString(d) {
			return nil, core.BadRequest("%q is not a valid domain name or IP address", d)
		}
	}
	if in.ValidDays == 0 {
		in.ValidDays = 395
	}
	if in.ValidDays < 1 || in.ValidDays > 825 {
		return nil, core.BadRequest("valid_days must be 1-825")
	}
	cert, err := s.issue(in.DomainName, in.SANs, in.ValidDays)
	if err != nil {
		return nil, err
	}
	cert.Tags = in.Tags
	return cert.view(), store.Put(s.env.Store, cCerts, cert.ID, cert)
}

func (s *Service) importCert(c *httpx.Ctx) (any, error) {
	var in struct {
		Certificate string    `json:"certificate"`
		PrivateKey  string    `json:"private_key"`
		Chain       string    `json:"certificate_chain"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair([]byte(in.Certificate+"\n"+in.Chain), []byte(in.PrivateKey))
	if err != nil {
		return nil, core.BadRequest("certificate and private key do not form a valid pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, core.BadRequest("invalid certificate: %v", err)
	}
	if time.Now().After(leaf.NotAfter) {
		return nil, core.BadRequest("certificate expired on %s", leaf.NotAfter.Format(time.DateOnly))
	}
	id := uuid()
	domain := leaf.Subject.CommonName
	if domain == "" && len(leaf.DNSNames) > 0 {
		domain = leaf.DNSNames[0]
	}
	cert := Certificate{ARN: s.env.ARN("acm", "certificate/"+id), ID: id, DomainName: domain, SANs: leafSANs(leaf), Type: "IMPORTED", Status: "ISSUED",
		Issuer: leaf.Issuer.CommonName, NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), Serial: hex.EncodeToString(leaf.SerialNumber.Bytes()),
		CertPEM: strings.TrimSpace(in.Certificate) + "\n", ChainPEM: strings.TrimSpace(in.Chain), KeyCT: s.secrets.Encrypt([]byte(in.PrivateKey)),
		CreatedAt: core.Now(), Tags: in.Tags}
	if cert.ChainPEM != "" {
		cert.ChainPEM += "\n"
	}
	return cert.view(), store.Put(s.env.Store, cCerts, id, cert)
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	cert, err := store.Get[Certificate](s.env.Store, cCerts, c.Param("id"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "certificate %q does not exist", c.Param("id"))
	}
	v := cert.view()
	sum := sha256.Sum256([]byte(cert.CertPEM))
	return map[string]any{"certificate": v, "fingerprint_sha256": hex.EncodeToString(sum[:]), "in_use": s.InUse != nil && s.InUse(cert.ARN)}, nil
}

// renew re-issues a private certificate in place, keeping its ARN.
func (s *Service) renew(c *httpx.Ctx) (any, error) {
	cert, err := store.Get[Certificate](s.env.Store, cCerts, c.Param("id"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "certificate %q does not exist", c.Param("id"))
	}
	if cert.Type != "PRIVATE" {
		return nil, core.BadRequest("imported certificates are renewed by importing a new one")
	}
	fresh, err := s.issue(cert.DomainName, cert.SANs, int(cert.NotAfter.Sub(cert.NotBefore).Hours()/24))
	if err != nil {
		return nil, err
	}
	fresh.ARN, fresh.ID, fresh.CreatedAt, fresh.Tags = cert.ARN, cert.ID, cert.CreatedAt, cert.Tags
	if err := store.Put(s.env.Store, cCerts, cert.ID, fresh); err != nil {
		return nil, err
	}
	if s.OnRenew != nil {
		go s.OnRenew(cert.ARN)
	}
	return fresh.view(), nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	cert, err := store.Get[Certificate](s.env.Store, cCerts, c.Param("id"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "certificate %q does not exist", c.Param("id"))
	}
	if s.InUse != nil && s.InUse(cert.ARN) {
		return nil, core.Errf(http.StatusConflict, "ResourceInUseException", "certificate is used by a load balancer listener")
	}
	return nil, store.Delete(s.env.Store, cCerts, cert.ID)
}

func (s *Service) caCert(c *httpx.Ctx) (any, error) {
	cert, _, p, err := s.ca()
	if err != nil {
		return nil, err
	}
	if c.Query("format") == "pem" {
		c.W.Header().Set("Content-Type", "application/x-pem-file")
		c.W.Header().Set("Content-Disposition", `attachment; filename="homecloud-ca.pem"`)
		c.MarkWritten()
		c.W.Write([]byte(p))
		return nil, nil
	}
	return map[string]any{"subject": cert.Subject.CommonName, "not_after": cert.NotAfter, "certificate": p,
		"hint": fmt.Sprintf("Trust this CA on your devices to accept certificates issued by HomeCloud (%s).", cert.Subject.CommonName)}, nil
}

// leafSANs lists a certificate's DNS and IP subject alternative names.
func leafSANs(c *x509.Certificate) []string {
	out := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}
