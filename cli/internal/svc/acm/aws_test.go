package acm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

func setup(t *testing.T) (*awstest.Harness, *Service) {
	t.Helper()
	h := awstest.New(t)
	s := New(h.Env, h.Secrets)
	s.RegisterAWS()
	s.Routes(h.Router)
	return h, s
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func request(t *testing.T, h *awstest.Harness, args ...string) string {
	t.Helper()
	return str(h.AWSJSON(t, append([]string{"acm", "request-certificate"}, args...)...), "CertificateArn")
}

// selfSigned writes a certificate and key (PEM) for the domain into dir.
func selfSigned(t *testing.T, dir, domain string) (certFile, keyFile string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain, "alt." + domain},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile = filepath.Join(dir, domain+".crt"), filepath.Join(dir, domain+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	return
}

func TestAWSRequestDescribeGet(t *testing.T) {
	h, s := setup(t)
	arn := request(t, h, "--domain-name", "app.example.com", "--subject-alternative-names", "*.example.com", "example.com",
		"--validation-method", "DNS", "--idempotency-token", "tok1", "--tags", "Key=env,Value=qa")
	if !strings.HasPrefix(arn, "arn:aws:acm:us-east-1:123456789012:certificate/") || len(arn) != len("arn:aws:acm:us-east-1:123456789012:certificate/")+36 {
		t.Fatalf("arn %q", arn)
	}
	if again := request(t, h, "--domain-name", "app.example.com", "--idempotency-token", "tok1"); again != arn {
		t.Fatalf("idempotent request returned %q, want %q", again, arn)
	}
	d := h.AWSJSON(t, "acm", "describe-certificate", "--certificate-arn", arn)["Certificate"].(map[string]any)
	if d["Status"] != "ISSUED" || d["Type"] != "AMAZON_ISSUED" || d["DomainName"] != "app.example.com" || d["KeyAlgorithm"] != "EC_prime256v1" {
		t.Fatalf("describe %v", d)
	}
	if sans := d["SubjectAlternativeNames"].([]any); len(sans) != 2 {
		t.Fatalf("sans %v", sans)
	}
	opts := d["DomainValidationOptions"].([]any)
	if len(opts) != 3 {
		t.Fatalf("validation options %v", opts)
	}
	byDomain := map[string]map[string]any{}
	for _, o := range opts {
		m := o.(map[string]any)
		byDomain[str(m, "DomainName")] = m
		if m["ValidationStatus"] != "SUCCESS" || m["ValidationMethod"] != "DNS" {
			t.Fatalf("validation %v", m)
		}
	}
	rec := byDomain["*.example.com"]["ResourceRecord"].(map[string]any)
	if rec["Type"] != "CNAME" || !strings.HasSuffix(str(rec, "Name"), ".example.com.") || !strings.HasSuffix(str(rec, "Value"), ".acm-validations.aws.") {
		t.Fatalf("record %v", rec)
	}
	if apex := byDomain["example.com"]["ResourceRecord"].(map[string]any); apex["Name"] != rec["Name"] || apex["Value"] != rec["Value"] {
		t.Fatalf("a name and its wildcard share one validation record: %v vs %v", apex, rec)
	}
	if d["InUseBy"] == nil || len(d["InUseBy"].([]any)) != 0 {
		t.Fatalf("in use %v", d["InUseBy"])
	}
	if !strings.Contains(str(d, "Issuer"), "HomeCloud Private CA") {
		t.Fatalf("issuer %v", d["Issuer"])
	}

	g := h.AWSJSON(t, "acm", "get-certificate", "--certificate-arn", arn)
	blk, _ := pem.Decode([]byte(str(g, "Certificate")))
	if blk == nil {
		t.Fatalf("certificate is not PEM: %v", g)
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(str(g, "CertificateChain"))) {
		t.Fatal("chain is not PEM")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "www.example.com", Roots: roots}); err != nil {
		t.Fatalf("leaf does not verify against the returned chain: %v", err)
	}

	// ELB (and the native API) use the same certificate.
	if !s.Exists(arn) {
		t.Fatal("Exists")
	}
	if chain, key, err := s.KeyPair(arn); err != nil || len(chain) == 0 || len(key) == 0 {
		t.Fatalf("KeyPair: %v", err)
	}
	if b := string(h.Native(t, "GET", "/api/v1/acm/certificates", nil)); !strings.Contains(b, "app.example.com") {
		t.Fatalf("native list: %s", b)
	}

	// Listing.
	imported := t.TempDir()
	cf, kf := selfSigned(t, imported, "imp.example.org")
	iarn := str(h.AWSJSON(t, "acm", "import-certificate", "--certificate", "fileb://"+cf, "--private-key", "fileb://"+kf, "--tags", "Key=src,Value=lets"), "CertificateArn")
	l := h.AWSJSON(t, "acm", "list-certificates")["CertificateSummaryList"].([]any)
	if len(l) != 2 {
		t.Fatalf("list %v", l)
	}
	if l := h.AWSJSON(t, "acm", "list-certificates", "--certificate-statuses", "EXPIRED")["CertificateSummaryList"].([]any); len(l) != 0 {
		t.Fatalf("expired filter %v", l)
	}
	first := h.AWSJSON(t, "acm", "list-certificates", "--no-paginate", "--max-items", "1")
	if len(first["CertificateSummaryList"].([]any)) != 1 || first["NextToken"] == nil {
		t.Fatalf("one page %v", first)
	}

	// Imported certificates: details, re-import in place, no export.
	id := h.AWSJSON(t, "acm", "describe-certificate", "--certificate-arn", iarn)["Certificate"].(map[string]any)
	if id["Type"] != "IMPORTED" || id["DomainName"] != "imp.example.org" || id["ImportedAt"] == nil || id["DomainValidationOptions"] != nil {
		t.Fatalf("imported %v", id)
	}
	cf2, kf2 := selfSigned(t, t.TempDir(), "imp2.example.org")
	if got := str(h.AWSJSON(t, "acm", "import-certificate", "--certificate-arn", iarn, "--certificate", "fileb://"+cf2, "--private-key", "fileb://"+kf2), "CertificateArn"); got != iarn {
		t.Fatalf("re-import changed the ARN: %s", got)
	}
	id = h.AWSJSON(t, "acm", "describe-certificate", "--certificate-arn", iarn)["Certificate"].(map[string]any)
	if id["DomainName"] != "imp2.example.org" {
		t.Fatalf("re-import: %v", id)
	}
	if tags := h.AWSJSON(t, "acm", "list-tags-for-certificate", "--certificate-arn", iarn)["Tags"].([]any); len(tags) != 1 {
		t.Fatalf("re-import keeps tags: %v", tags)
	}
	if o, err := h.AWSErr(t, "acm", "export-certificate", "--certificate-arn", iarn, "--passphrase", "fileb://"+cf); err == nil || !strings.Contains(o, "ValidationException") {
		t.Fatalf("export imported: %v %s", err, o)
	}
	other := filepath.Join(t.TempDir(), "other.key")
	_ = os.WriteFile(other, []byte("garbage"), 0o600)
	if o, err := h.AWSErr(t, "acm", "import-certificate", "--certificate", "fileb://"+cf, "--private-key", "fileb://"+other); err == nil || !strings.Contains(o, "ValidationException") {
		t.Fatalf("bad key pair: %v %s", err, o)
	}
}

func TestAWSExport(t *testing.T) {
	h, _ := setup(t)
	arn := request(t, h, "--domain-name", "export.example.com")
	pass := filepath.Join(t.TempDir(), "pass")
	_ = os.WriteFile(pass, []byte("s3cret-pass"), 0o600)
	e := h.AWSJSON(t, "acm", "export-certificate", "--certificate-arn", arn, "--passphrase", "fileb://"+pass)
	blk, _ := pem.Decode([]byte(str(e, "PrivateKey")))
	if blk == nil || !x509.IsEncryptedPEMBlock(blk) { //nolint:staticcheck
		t.Fatalf("private key should be encrypted: %v", e["PrivateKey"])
	}
	der, err := x509.DecryptPEMBlock(blk, []byte("s3cret-pass")) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParseECPrivateKey(der)
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := pem.Decode([]byte(str(e, "Certificate")))
	leaf, _ := x509.ParseCertificate(cb.Bytes)
	if !key.PublicKey.Equal(leaf.PublicKey) {
		t.Fatal("exported key does not match the certificate")
	}
	if _, err := x509.DecryptPEMBlock(blk, []byte("wrong")); err == nil { //nolint:staticcheck
		t.Fatal("wrong passphrase accepted")
	}
	short := filepath.Join(t.TempDir(), "short")
	_ = os.WriteFile(short, []byte("x"), 0o600)
	if o, err := h.AWSErr(t, "acm", "export-certificate", "--certificate-arn", arn, "--passphrase", "fileb://"+short); err == nil || !strings.Contains(o, "ValidationException") {
		t.Fatalf("short passphrase: %v %s", err, o)
	}
}

func TestAWSDeleteTagsRenew(t *testing.T) {
	h, s := setup(t)
	arn := request(t, h, "--domain-name", "del.example.com")
	before := h.AWSJSON(t, "acm", "get-certificate", "--certificate-arn", arn)["Certificate"]

	h.AWS(t, "acm", "add-tags-to-certificate", "--certificate-arn", arn, "--tags", "Key=a,Value=1", "Key=b,Value=2", "Key=c")
	h.AWS(t, "acm", "remove-tags-from-certificate", "--certificate-arn", arn, "--tags", "Key=b,Value=nope", "Key=c")
	tags := h.AWSJSON(t, "acm", "list-tags-for-certificate", "--certificate-arn", arn)["Tags"].([]any)
	got := map[string]string{}
	for _, x := range tags {
		m := x.(map[string]any)
		got[str(m, "Key")] = str(m, "Value")
	}
	if len(got) != 2 || got["a"] != "1" || got["b"] != "2" {
		t.Fatalf("tags %v", got)
	}
	if o, err := h.AWSErr(t, "acm", "add-tags-to-certificate", "--certificate-arn", arn, "--tags", "Key=aws:x,Value=1"); err == nil || !strings.Contains(o, "InvalidTagException") {
		t.Fatalf("reserved tag: %v %s", err, o)
	}

	h.AWS(t, "acm", "renew-certificate", "--certificate-arn", arn)
	after := h.AWSJSON(t, "acm", "get-certificate", "--certificate-arn", arn)["Certificate"]
	if before == after {
		t.Fatal("renew did not re-issue")
	}
	if len(h.AWSJSON(t, "acm", "list-tags-for-certificate", "--certificate-arn", arn)["Tags"].([]any)) != 2 {
		t.Fatal("renew dropped tags")
	}

	// In use by a load balancer listener.
	lb := "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/abc"
	s.InUse = func(a string) bool { return a == arn }
	s.UsedBy = func(a string) []string {
		if a == arn {
			return []string{lb}
		}
		return nil
	}
	h.AWS(t, "acm", "list-certificates") // (fields set before the next requests are ordered by the harness)
	if o, err := h.AWSErr(t, "acm", "delete-certificate", "--certificate-arn", arn); err == nil || !strings.Contains(o, "ResourceInUseException") {
		t.Fatalf("delete in-use: %v %s", err, o)
	}
	d := h.AWSJSON(t, "acm", "describe-certificate", "--certificate-arn", arn)["Certificate"].(map[string]any)
	if by := d["InUseBy"].([]any); len(by) != 1 || by[0] != lb {
		t.Fatalf("InUseBy %v", d["InUseBy"])
	}
	if l := h.AWSJSON(t, "acm", "list-certificates")["CertificateSummaryList"].([]any); l[0].(map[string]any)["InUse"] != true {
		t.Fatalf("summary InUse %v", l)
	}
	s.InUse, s.UsedBy = nil, nil
	h.AWS(t, "acm", "list-certificates")
	h.AWS(t, "acm", "delete-certificate", "--certificate-arn", arn)
	for _, cmd := range []string{"describe-certificate", "get-certificate", "delete-certificate", "list-tags-for-certificate"} {
		if o, err := h.AWSErr(t, "acm", cmd, "--certificate-arn", arn); err == nil || !strings.Contains(o, "ResourceNotFoundException") {
			t.Fatalf("%s after delete: %v %s", cmd, err, o)
		}
	}
	if o, err := h.AWSErr(t, "acm", "describe-certificate", "--certificate-arn", "arn:aws:acm:us-east-1:12:certificate/x"); err == nil || !strings.Contains(o, "InvalidArnException") {
		t.Fatalf("bad arn: %v %s", err, o)
	}
	for _, bad := range []string{"bad_domain!", "a..b"} {
		if o, err := h.AWSErr(t, "acm", "request-certificate", "--domain-name", bad); err == nil || !strings.Contains(o, "ValidationException") {
			t.Fatalf("domain %q: %v %s", bad, err, o)
		}
	}
	if o, err := h.AWSErr(t, "acm", "request-certificate", "--domain-name", "x.example.com", "--validation-method", "CARRIER_PIGEON"); err == nil || !strings.Contains(o, "ValidationException") {
		t.Fatalf("validation method: %v %s", err, o)
	}
}

func TestAWSBoto3(t *testing.T) {
	h, _ := setup(t)
	out := h.Python(t, `
a = boto3.client("acm")
arn = a.request_certificate(DomainName="b.example.com", ValidationMethod="DNS", SubjectAlternativeNames=["c.example.com"])["CertificateArn"]
a.get_waiter("certificate_validated").wait(CertificateArn=arn, WaiterConfig={"Delay": 1, "MaxAttempts": 5})
d = a.describe_certificate(CertificateArn=arn)["Certificate"]
assert d["Status"] == "ISSUED" and d["Type"] == "AMAZON_ISSUED", d
assert {o["DomainName"] for o in d["DomainValidationOptions"]} == {"b.example.com", "c.example.com"}
assert d["DomainValidationOptions"][0]["ResourceRecord"]["Type"] == "CNAME"
assert isinstance(d["NotAfter"], __import__("datetime").datetime)
assert a.get_certificate(CertificateArn=arn)["Certificate"].startswith("-----BEGIN CERTIFICATE-----")
a.add_tags_to_certificate(CertificateArn=arn, Tags=[{"Key": "k", "Value": "v"}])
assert a.list_tags_for_certificate(CertificateArn=arn)["Tags"] == [{"Key": "k", "Value": "v"}]
ex = a.export_certificate(CertificateArn=arn, Passphrase=b"pass1234")
assert "ENCRYPTED" in ex["PrivateKey"], ex["PrivateKey"][:80]
assert [c["CertificateArn"] for c in a.list_certificates()["CertificateSummaryList"]] == [arn]
a.delete_certificate(CertificateArn=arn)
try:
    a.describe_certificate(CertificateArn=arn)
    raise SystemExit("expected error")
except a.exceptions.ResourceNotFoundException:
    pass
print("ok")
`)
	if !strings.Contains(out, "ok") {
		t.Fatalf("boto3: %s", out)
	}
}

func TestAWSIAM(t *testing.T) {
	h, _ := setup(t)
	arn := request(t, h, "--domain-name", "one.example.com")
	arn2 := request(t, h, "--domain-name", "two.example.com")

	ro, rs := h.User(t, "ro", "ReadOnlyAccess")
	for _, args := range [][]string{{"list-certificates"}, {"describe-certificate", "--certificate-arn", arn}, {"list-tags-for-certificate", "--certificate-arn", arn}, {"get-certificate", "--certificate-arn", arn}} {
		if o, err := h.AWSAs(t, ro, rs, "", append([]string{"acm"}, args...)...); err != nil {
			t.Fatalf("read-only %s: %v %s", args[0], err, o)
		}
	}
	for _, args := range [][]string{
		{"request-certificate", "--domain-name", "x.example.com"},
		{"delete-certificate", "--certificate-arn", arn},
		{"add-tags-to-certificate", "--certificate-arn", arn, "--tags", "Key=a,Value=b"},
		{"renew-certificate", "--certificate-arn", arn},
	} {
		if o, err := h.AWSAs(t, ro, rs, "", append([]string{"acm"}, args...)...); err == nil || !strings.Contains(o, "AccessDeniedException") {
			t.Fatalf("%s as read-only: %v %s", args[0], err, o)
		}
	}
	// Exporting a private key is not read-only.
	pass := filepath.Join(t.TempDir(), "p")
	_ = os.WriteFile(pass, []byte("pass1234"), 0o600)
	if o, err := h.AWSAs(t, ro, rs, "", "acm", "export-certificate", "--certificate-arn", arn, "--passphrase", "fileb://"+pass); err == nil || !strings.Contains(o, "AccessDeniedException") {
		t.Fatalf("export as read-only: %v %s", err, o)
	}

	// Scoped to one certificate.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "one-cert", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"acm:DescribeCertificate", "acm:GetCertificate", "acm:DeleteCertificate"}, "Resource": arn}}}})
	ak, as := h.User(t, "one", "one-cert")
	if o, err := h.AWSAs(t, ak, as, "", "acm", "describe-certificate", "--certificate-arn", arn); err != nil {
		t.Fatalf("scoped describe: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, ak, as, "", "acm", "describe-certificate", "--certificate-arn", arn2); err == nil || !strings.Contains(o, "AccessDeniedException") {
		t.Fatalf("other cert: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, ak, as, "", "acm", "list-certificates"); err == nil || !strings.Contains(o, "AccessDeniedException") {
		t.Fatalf("list: %v %s", err, o)
	}
	if o, err := h.AWSAs(t, ak, as, "", "acm", "delete-certificate", "--certificate-arn", arn); err != nil {
		t.Fatalf("scoped delete: %v %s", err, o)
	}
	// A denial does not reveal whether the certificate exists, and is audited on its ARN.
	found := false
	for _, a := range h.AuditLog() {
		if a == "acm:DescribeCertificate "+arn2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit log %v", h.AuditLog())
	}
}

// Terraform's acm module requests certificates with transparency logging
// ENABLED and re-plans forever when DescribeCertificate reports another value.
func TestAWSCertificateTransparencyOption(t *testing.T) {
	h, _ := setup(t)
	ct := func(arn string) string {
		d := h.AWSJSON(t, "acm", "describe-certificate", "--certificate-arn", arn)["Certificate"].(map[string]any)
		return str(d["Options"].(map[string]any), "CertificateTransparencyLoggingPreference")
	}
	arn := request(t, h, "--domain-name", "ct.example.com")
	if got := ct(arn); got != "ENABLED" {
		t.Fatalf("default preference %q, want ENABLED", got)
	}
	off := request(t, h, "--domain-name", "off.example.com", "--options", "CertificateTransparencyLoggingPreference=DISABLED")
	if got := ct(off); got != "DISABLED" {
		t.Fatalf("requested DISABLED, got %q", got)
	}
	h.AWS(t, "acm", "update-certificate-options", "--certificate-arn", off, "--options", "CertificateTransparencyLoggingPreference=ENABLED")
	if got := ct(off); got != "ENABLED" {
		t.Fatalf("after update %q, want ENABLED", got)
	}
	if _, err := h.AWSErr(t, "acm", "update-certificate-options", "--certificate-arn", off, "--options", "CertificateTransparencyLoggingPreference=MAYBE"); err == nil {
		t.Fatal("invalid preference accepted")
	}
}
