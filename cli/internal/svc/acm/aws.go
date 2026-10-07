package acm

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The AWS Certificate Manager API (awsJson 1.1).
//
// Certificates are issued by HomeCloud's private CA (trust it with
// `GET /api/v1/acm/ca?format=pem`), not by a public CA, and they are issued
// immediately: RequestCertificate returns a certificate that is already ISSUED,
// and DescribeCertificate reports the DNS validation CNAME record (so Terraform
// can create it in Route 53) with ValidationStatus SUCCESS without checking it.
// Certificates are ECDSA P-256; the KeyAlgorithm request field is ignored.
// ExportCertificate works for these private certificates, returning the key
// encrypted with the passphrase (legacy PEM encryption, readable by OpenSSL).

// RegisterAWS serves ACM over the AWS protocol.
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "acm", JSONPrefix: "CertificateManager", JSONVersion: "1.1",
		Ops: map[string]awsapi.Op{
			"RequestCertificate":        s.awsRequest,
			"ImportCertificate":         s.awsImport,
			"DescribeCertificate":       s.awsDescribe,
			"ListCertificates":          s.awsList,
			"GetCertificate":            s.awsGet,
			"ExportCertificate":         s.awsExport,
			"DeleteCertificate":         s.awsDelete,
			"RenewCertificate":          s.awsRenew,
			"AddTagsToCertificate":      s.awsAddTags,
			"ListTagsForCertificate":    s.awsListTags,
			"RemoveTagsFromCertificate": s.awsRemoveTags,
			"UpdateCertificateOptions":  s.awsUpdateOptions,
			"GetAccountConfiguration":   s.awsAccountConfig,
			"PutAccountConfiguration":   func(q *awsapi.Req) (any, error) { return nil, q.Authorize("acm:PutAccountConfiguration", "*") },
			"ResendValidationEmail":     func(q *awsapi.Req) (any, error) { return nil, q.Authorize("acm:ResendValidationEmail", "*") },
		},
		ErrorCode: map[string]string{"BadRequest": "ValidationException", "ValidationError": "ValidationException"},
	})
}

var arnRe = regexp.MustCompile(`^arn:[a-z-]+:acm:[a-z0-9-]+:\d{12}:certificate/[0-9a-fA-F-]{36}$`)

func acmErr(status int, code, format string, a ...any) error {
	return awsapi.Errorf(status, code, format, a...)
}

// cert authorizes the call and loads the certificate an ARN names.
func (s *Service) cert(q *awsapi.Req, action, arn string) (Certificate, error) {
	if !arnRe.MatchString(arn) {
		return Certificate{}, acmErr(http.StatusBadRequest, "InvalidArnException", "The ARN %q is not valid.", arn)
	}
	if err := q.Authorize(action, arn); err != nil {
		return Certificate{}, err
	}
	c, err := s.byARN(arn)
	if err != nil {
		return c, acmErr(http.StatusBadRequest, "ResourceNotFoundException", "Could not find certificate %s.", arn)
	}
	return c, nil
}

type awsTag struct {
	Key   string
	Value string `json:",omitempty"`
}

func tagList(t core.Tags) []awsTag {
	out := make([]awsTag, 0, len(t))
	for k, v := range t {
		out = append(out, awsTag{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func tagMap(l []awsTag) core.Tags {
	if len(l) == 0 {
		return nil
	}
	t := core.Tags{}
	for _, x := range l {
		t[x.Key] = x.Value
	}
	return t
}

func checkTags(l []awsTag) error {
	for _, t := range l {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 {
			return acmErr(http.StatusBadRequest, "InvalidTagException", "Tag keys are 1-128 and values at most 256 characters.")
		}
		if strings.HasPrefix(strings.ToLower(t.Key), "aws:") {
			return acmErr(http.StatusBadRequest, "InvalidTagException", "Tag keys may not start with aws:.")
		}
	}
	return nil
}

// ---- shapes ----

type dvoRecord struct {
	Name  string
	Type  string
	Value string
}

type dvo struct {
	DomainName       string
	ValidationDomain string
	ValidationStatus string
	ResourceRecord   *dvoRecord `json:",omitempty"`
	ValidationMethod string     `json:",omitempty"`
}

type usage struct {
	Name string
	OID  string `json:",omitempty"`
}

type certDetail struct {
	CertificateArn          string
	DomainName              string
	SubjectAlternativeNames []string
	DomainValidationOptions []dvo `json:",omitempty"`
	Serial                  string
	Subject                 string
	Issuer                  string
	CreatedAt               float64
	IssuedAt                float64 `json:",omitempty"`
	ImportedAt              float64 `json:",omitempty"`
	Status                  string
	NotBefore               float64
	NotAfter                float64
	KeyAlgorithm            string
	SignatureAlgorithm      string
	InUseBy                 []string
	Type                    string
	KeyUsages               []usage
	ExtendedKeyUsages       []usage
	RenewalEligibility      string
	Options                 map[string]string
}

// awsType is the certificate Type clients see. Certificates requested through
// ACM are AMAZON_ISSUED in AWS (PRIVATE means issued by ACM Private CA, which
// Terraform treats as needing no validation); ours emulate that flow.
func awsType(t string) string {
	if t == "PRIVATE" {
		return "AMAZON_ISSUED"
	}
	return t
}

func leafOf(c Certificate) *x509.Certificate {
	blk, _ := pem.Decode([]byte(c.CertPEM))
	if blk == nil {
		return nil
	}
	leaf, _ := x509.ParseCertificate(blk.Bytes)
	return leaf
}

func keyAlgorithm(leaf *x509.Certificate) string {
	if leaf == nil {
		return "EC_prime256v1"
	}
	switch k := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA_%d", k.N.BitLen())
	case *ecdsa.PublicKey:
		return "EC_" + map[string]string{"P-256": "prime256v1", "P-384": "secp384r1", "P-521": "secp521r1"}[k.Curve.Params().Name]
	}
	return "EC_prime256v1"
}

func sigAlgorithm(leaf *x509.Certificate) string {
	if leaf == nil {
		return "SHA256WITHECDSA"
	}
	return strings.NewReplacer("-", "WITH", "ECDSAWITH", "", "RSAWITH", "").Replace(strings.ToUpper(leaf.SignatureAlgorithm.String()))
}

// serialColons formats a serial as ACM does ("0a:1b:...").
func serialColons(hexSerial string) string {
	if len(hexSerial)%2 == 1 {
		hexSerial = "0" + hexSerial
	}
	var parts []string
	for i := 0; i < len(hexSerial); i += 2 {
		parts = append(parts, hexSerial[i:i+2])
	}
	return strings.Join(parts, ":")
}

// validationRecord is the DNS record that would prove control of a domain: the
// same for a name and its wildcard, as in AWS.
func validationRecord(arn, domain string) dvoRecord {
	base := strings.TrimPrefix(domain, "*.")
	sum := sha256.Sum256([]byte(arn + "|" + base))
	token := hex.EncodeToString(sum[:16])
	return dvoRecord{Name: "_" + token + "." + base + ".", Type: "CNAME", Value: "_" + hex.EncodeToString(sum[16:]) + ".acm-validations.aws."}
}

func (s *Service) detail(c Certificate) certDetail {
	c = c.view()
	leaf := leafOf(c)
	d := certDetail{CertificateArn: c.ARN, DomainName: c.DomainName, SubjectAlternativeNames: c.SANs, Serial: serialColons(c.Serial),
		Subject: "CN=" + c.DomainName, Issuer: "CN=" + c.Issuer, CreatedAt: awsapi.Epoch(c.CreatedAt), Status: c.Status,
		NotBefore: awsapi.Epoch(c.NotBefore), NotAfter: awsapi.Epoch(c.NotAfter), KeyAlgorithm: keyAlgorithm(leaf), SignatureAlgorithm: sigAlgorithm(leaf),
		InUseBy: []string{}, Type: awsType(c.Type), RenewalEligibility: "INELIGIBLE", Options: map[string]string{"CertificateTransparencyLoggingPreference": ctLogging(c)},
		KeyUsages: []usage{{Name: "DIGITAL_SIGNATURE"}}, ExtendedKeyUsages: []usage{{Name: "TLS_WEB_SERVER_AUTHENTICATION", OID: "1.3.6.1.5.5.7.3.1"}}}
	if d.SubjectAlternativeNames == nil {
		d.SubjectAlternativeNames = []string{}
	}
	if !c.IssuedAt.IsZero() {
		if c.Type == "IMPORTED" {
			d.ImportedAt = awsapi.Epoch(c.IssuedAt)
		} else {
			d.IssuedAt = awsapi.Epoch(c.IssuedAt)
		}
	}
	if c.Type == "PRIVATE" {
		d.RenewalEligibility = "ELIGIBLE"
		method := c.ValidationMethod
		if method == "" {
			method = "DNS"
		}
		seen := map[string]bool{}
		for _, n := range append([]string{c.DomainName}, c.SANs...) {
			if seen[n] {
				continue
			}
			seen[n] = true
			o := dvo{DomainName: n, ValidationDomain: strings.TrimPrefix(n, "*."), ValidationStatus: "SUCCESS", ValidationMethod: method}
			if method == "DNS" {
				r := validationRecord(c.ARN, n)
				o.ResourceRecord = &r
			}
			d.DomainValidationOptions = append(d.DomainValidationOptions, o)
		}
	}
	if s.UsedBy != nil {
		if by := s.UsedBy(c.ARN); len(by) > 0 {
			d.InUseBy = by
		}
	}
	return d
}

// ---- operations ----

func (s *Service) awsRequest(q *awsapi.Req) (any, error) {
	var in struct {
		DomainName              string
		SubjectAlternativeNames []string
		ValidationMethod        string
		IdempotencyToken        string
		Tags                    []awsTag
		CertificateAuthorityArn string
		KeyAlgorithm            string
		Options                 struct{ CertificateTransparencyLoggingPreference string }
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("acm:RequestCertificate", "*"); err != nil {
		return nil, err
	}
	if in.DomainName == "" {
		return nil, acmErr(http.StatusBadRequest, "ValidationException", "DomainName is required")
	}
	if len(in.SubjectAlternativeNames) > 100 {
		return nil, acmErr(http.StatusBadRequest, "ValidationException", "at most 100 subject alternative names")
	}
	switch in.ValidationMethod {
	case "", "DNS", "EMAIL", "HTTP":
	default:
		return nil, acmErr(http.StatusBadRequest, "ValidationException", "ValidationMethod must be DNS, EMAIL or HTTP")
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	if in.ValidationMethod == "" {
		in.ValidationMethod = "DNS"
	}
	ct := in.Options.CertificateTransparencyLoggingPreference
	if err := checkCTLogging(ct); err != nil {
		return nil, err
	}
	c, err := s.requestCert(requestInput{DomainName: in.DomainName, SANs: in.SubjectAlternativeNames, Tags: tagMap(in.Tags),
		ValidationMethod: in.ValidationMethod, IdempotencyToken: in.IdempotencyToken, CTLogging: ct})
	if err != nil {
		return nil, err
	}
	return map[string]string{"CertificateArn": c.ARN}, nil
}

func (s *Service) awsImport(q *awsapi.Req) (any, error) {
	var in struct {
		CertificateArn   string
		Certificate      []byte
		PrivateKey       []byte
		CertificateChain []byte
		Tags             []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	res := "*"
	if in.CertificateArn != "" {
		if !arnRe.MatchString(in.CertificateArn) {
			return nil, acmErr(http.StatusBadRequest, "InvalidArnException", "The ARN %q is not valid.", in.CertificateArn)
		}
		res = in.CertificateArn
	}
	if err := q.Authorize("acm:ImportCertificate", res); err != nil {
		return nil, err
	}
	if len(in.Certificate) == 0 || len(in.PrivateKey) == 0 {
		return nil, acmErr(http.StatusBadRequest, "ValidationException", "Certificate and PrivateKey are required")
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	c, err := s.importPEM(in.CertificateArn, string(in.Certificate), string(in.PrivateKey), string(in.CertificateChain), tagMap(in.Tags))
	if err != nil {
		var ce *core.Error
		if in.CertificateArn != "" && errors.As(err, &ce) && ce.Status == http.StatusNotFound {
			return nil, acmErr(http.StatusBadRequest, "ResourceNotFoundException", "Could not find certificate %s.", in.CertificateArn)
		}
		return nil, err
	}
	return map[string]string{"CertificateArn": c.ARN}, nil
}

func (s *Service) awsDescribe(q *awsapi.Req) (any, error) {
	var in struct{ CertificateArn string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:DescribeCertificate", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Certificate": s.detail(c)}, nil
}

func (s *Service) awsList(q *awsapi.Req) (any, error) {
	var in struct {
		CertificateStatuses []string
		Includes            struct {
			KeyTypes []string `json:"keyTypes"`
		}
		MaxItems  int
		NextToken string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("acm:ListCertificates", "*"); err != nil {
		return nil, err
	}
	certs := store.List[Certificate](s.env.Store, cCerts)
	sort.Slice(certs, func(i, j int) bool {
		if !certs[i].CreatedAt.Equal(certs[j].CreatedAt) {
			return certs[i].CreatedAt.Before(certs[j].CreatedAt)
		}
		return certs[i].ARN < certs[j].ARN
	})
	start, _ := strconv.Atoi(in.NextToken)
	max := in.MaxItems
	if max <= 0 || max > 1000 {
		max = 1000
	}
	type summary struct {
		CertificateArn                       string
		DomainName                           string
		SubjectAlternativeNameSummaries      []string
		HasAdditionalSubjectAlternativeNames bool
		Status                               string
		Type                                 string
		KeyAlgorithm                         string
		KeyUsages                            []string
		ExtendedKeyUsages                    []string
		InUse                                bool
		CreatedAt                            float64
		IssuedAt                             float64 `json:",omitempty"`
		ImportedAt                           float64 `json:",omitempty"`
		NotBefore                            float64
		NotAfter                             float64
		RenewalEligibility                   string
	}
	out := []summary{}
	idx := 0
	next := ""
	for _, c := range certs {
		d := s.detail(c)
		if len(in.CertificateStatuses) > 0 && !contains(in.CertificateStatuses, d.Status) {
			continue
		}
		if len(in.Includes.KeyTypes) > 0 && !contains(in.Includes.KeyTypes, d.KeyAlgorithm) {
			continue
		}
		if idx < start {
			idx++
			continue
		}
		if len(out) == max {
			next = strconv.Itoa(idx)
			break
		}
		idx++
		out = append(out, summary{CertificateArn: d.CertificateArn, DomainName: d.DomainName, SubjectAlternativeNameSummaries: append([]string{d.DomainName}, d.SubjectAlternativeNames...),
			Status: d.Status, Type: d.Type, KeyAlgorithm: d.KeyAlgorithm, KeyUsages: []string{"DIGITAL_SIGNATURE"}, ExtendedKeyUsages: []string{"TLS_WEB_SERVER_AUTHENTICATION"},
			InUse: len(d.InUseBy) > 0, CreatedAt: d.CreatedAt, IssuedAt: d.IssuedAt, ImportedAt: d.ImportedAt, NotBefore: d.NotBefore, NotAfter: d.NotAfter, RenewalEligibility: d.RenewalEligibility})
	}
	res := map[string]any{"CertificateSummaryList": out}
	if next != "" {
		res["NextToken"] = next
	}
	return res, nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Service) awsGet(q *awsapi.Req) (any, error) {
	var in struct{ CertificateArn string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:GetCertificate", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	out := map[string]string{"Certificate": c.CertPEM}
	if c.ChainPEM != "" {
		out["CertificateChain"] = c.ChainPEM
	}
	return out, nil
}

func (s *Service) awsExport(q *awsapi.Req) (any, error) {
	var in struct {
		CertificateArn string
		Passphrase     []byte
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:ExportCertificate", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	if c.Type != "PRIVATE" {
		return nil, acmErr(http.StatusBadRequest, "ValidationException", "Only certificates issued by the private CA can be exported; imported certificates cannot.")
	}
	if len(in.Passphrase) < 4 {
		return nil, acmErr(http.StatusBadRequest, "ValidationException", "Passphrase must be at least 4 characters")
	}
	_, key, err := s.KeyPair(c.ARN)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(key)
	if blk == nil {
		return nil, core.Errf(http.StatusInternalServerError, "InternalError", "stored key is not PEM")
	}
	enc, err := x509.EncryptPEMBlock(rand.Reader, blk.Type, blk.Bytes, in.Passphrase, x509.PEMCipherAES256) //nolint:staticcheck // OpenSSL-readable
	if err != nil {
		return nil, err
	}
	return map[string]string{"Certificate": c.CertPEM, "CertificateChain": c.ChainPEM, "PrivateKey": string(pem.EncodeToMemory(enc))}, nil
}

func (s *Service) awsDelete(q *awsapi.Req) (any, error) {
	var in struct{ CertificateArn string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:DeleteCertificate", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	if s.InUse != nil && s.InUse(c.ARN) {
		return nil, acmErr(http.StatusBadRequest, "ResourceInUseException", "Certificate %s is in use.", c.ARN)
	}
	return nil, s.deleteCert(c)
}

func (s *Service) awsRenew(q *awsapi.Req) (any, error) {
	var in struct{ CertificateArn string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:RenewCertificate", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	if c.Type != "PRIVATE" {
		return nil, acmErr(http.StatusBadRequest, "ValidationException", "Only certificates issued by the private CA can be renewed.")
	}
	_, err = s.renewCert(c)
	return nil, err
}

func (s *Service) awsUpdateOptions(q *awsapi.Req) (any, error) {
	var in struct {
		CertificateArn string
		Options        struct{ CertificateTransparencyLoggingPreference string }
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:UpdateCertificateOptions", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	ct := in.Options.CertificateTransparencyLoggingPreference
	if err := checkCTLogging(ct); err != nil || ct == "" {
		return nil, err
	}
	_, err = store.Update(s.env.Store, cCerts, c.ID, func(x *Certificate) error { x.CTLogging = ct; return nil })
	return nil, err
}

func checkCTLogging(v string) error {
	if v != "" && v != "ENABLED" && v != "DISABLED" {
		return acmErr(http.StatusBadRequest, "ValidationException", "CertificateTransparencyLoggingPreference must be ENABLED or DISABLED")
	}
	return nil
}

// ctLogging reports a certificate's transparency logging preference (ENABLED unless set).
func ctLogging(c Certificate) string {
	if c.CTLogging == "" {
		return "ENABLED"
	}
	return c.CTLogging
}

func (s *Service) awsAccountConfig(q *awsapi.Req) (any, error) {
	if err := q.Authorize("acm:GetAccountConfiguration", "*"); err != nil {
		return nil, err
	}
	return map[string]any{"ExpiryEvents": map[string]int{"DaysBeforeExpiry": 45}}, nil
}

func (s *Service) awsAddTags(q *awsapi.Req) (any, error) {
	var in struct {
		CertificateArn string
		Tags           []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:AddTagsToCertificate", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	_, err = store.Update(s.env.Store, cCerts, c.ID, func(x *Certificate) error {
		if x.Tags == nil {
			x.Tags = core.Tags{}
		}
		for _, t := range in.Tags {
			x.Tags[t.Key] = t.Value
		}
		if len(x.Tags) > 50 {
			return acmErr(http.StatusBadRequest, "TooManyTagsException", "A certificate can have at most 50 tags.")
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsListTags(q *awsapi.Req) (any, error) {
	var in struct{ CertificateArn string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:ListTagsForCertificate", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Tags": tagList(c.Tags)}, nil
}

func (s *Service) awsRemoveTags(q *awsapi.Req) (any, error) {
	var in struct {
		CertificateArn string
		Tags           []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	c, err := s.cert(q, "acm:RemoveTagsFromCertificate", in.CertificateArn)
	if err != nil {
		return nil, err
	}
	_, err = store.Update(s.env.Store, cCerts, c.ID, func(x *Certificate) error {
		for _, t := range in.Tags {
			if v, ok := x.Tags[t.Key]; ok && (t.Value == "" || t.Value == v) {
				delete(x.Tags, t.Key)
			}
		}
		return nil
	})
	return nil, err
}
