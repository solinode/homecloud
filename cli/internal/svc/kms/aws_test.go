package kms_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func setup(t *testing.T) (*awstest.Harness, *kms.Service, *clock) {
	h := awstest.New(t)
	clk := &clock{t: time.Now()}
	k := kms.New(h.Env, h.Secrets)
	k.Now = clk.Now
	k.RegisterAWS()
	return h, k, clk
}

func expectErr(t *testing.T, h *awstest.Harness, code string, args ...string) {
	t.Helper()
	out, err := h.AWSErr(t, args...)
	if err == nil || !strings.Contains(out, code) {
		t.Fatalf("aws %s: want %s, got err=%v\n%s", strings.Join(args, " "), code, err, out)
	}
}

func meta(t *testing.T, h *awstest.Harness, args ...string) map[string]any {
	t.Helper()
	return h.AWSJSON(t, args...)["KeyMetadata"].(map[string]any)
}

// Terraform reads the rotation status of every key KMS made, asymmetric ones
// included: AWS reports rotation off for them rather than failing.
func TestKMSRotationStatusOfAsymmetricKey(t *testing.T) {
	h, _, _ := setup(t)
	id := meta(t, h, "kms", "create-key", "--key-usage", "SIGN_VERIFY", "--key-spec", "ECC_NIST_P256")["KeyId"].(string)
	if rs := h.AWSJSON(t, "kms", "get-key-rotation-status", "--key-id", id); rs["KeyRotationEnabled"] != false {
		t.Fatalf("rotation status: %v", rs)
	}
	expectErr(t, h, "UnsupportedOperationException", "kms", "enable-key-rotation", "--key-id", id)
}

func TestKMSKeyLifecycleCLI(t *testing.T) {
	h, k, clk := setup(t)
	m := meta(t, h, "kms", "create-key", "--description", "app key", "--tags", "TagKey=env,TagValue=dev")
	id, arn := m["KeyId"].(string), m["Arn"].(string)
	if m["KeySpec"] != "SYMMETRIC_DEFAULT" || m["KeyUsage"] != "ENCRYPT_DECRYPT" || m["Enabled"] != true || m["KeyManager"] != "CUSTOMER" ||
		arn != "arn:aws:kms:us-east-1:"+h.Env.AccountID+":key/"+id {
		t.Fatalf("create-key: %v", m)
	}
	h.AWS(t, "kms", "create-alias", "--alias-name", "alias/app", "--target-key-id", id)
	expectErr(t, h, "AlreadyExistsException", "kms", "create-alias", "--alias-name", "alias/app", "--target-key-id", id)
	expectErr(t, h, "NotAuthorizedException", "kms", "create-alias", "--alias-name", "alias/aws/mine", "--target-key-id", id)
	for _, ref := range []string{id, arn, "alias/app", "arn:aws:kms:us-east-1:" + h.Env.AccountID + ":alias/app"} {
		if got := meta(t, h, "kms", "describe-key", "--key-id", ref); got["KeyId"] != id {
			t.Fatalf("describe-key %s: %v", ref, got)
		}
	}
	expectErr(t, h, "NotFoundException", "kms", "describe-key", "--key-id", "alias/none")
	expectErr(t, h, "NotFoundException", "kms", "describe-key", "--key-id", "1234abcd-12ab-34cd-56ef-1234567890ab")

	// AWS managed aliases create their key on first use.
	aws := meta(t, h, "kms", "describe-key", "--key-id", "alias/aws/ssm")
	if aws["KeyManager"] != "AWS" {
		t.Fatalf("alias/aws/ssm: %v", aws)
	}
	expectErr(t, h, "UnsupportedOperationException", "kms", "disable-key", "--key-id", "alias/aws/ssm")

	// Policies and tags.
	pol := h.AWSJSON(t, "kms", "get-key-policy", "--key-id", id, "--policy-name", "default")
	if !strings.Contains(pol["Policy"].(string), "Enable IAM User Permissions") {
		t.Fatalf("default policy: %v", pol)
	}
	newPol := `{"Version":"2012-10-17","Statement":[{"Sid":"x","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + h.Env.AccountID + `:root"},"Action":"kms:*","Resource":"*"}]}`
	h.AWS(t, "kms", "put-key-policy", "--key-id", id, "--policy-name", "default", "--policy", newPol)
	if pol := h.AWSJSON(t, "kms", "get-key-policy", "--key-id", id); pol["Policy"] != newPol {
		t.Fatalf("put-key-policy: %v", pol)
	}
	expectErr(t, h, "MalformedPolicyDocumentException", "kms", "put-key-policy", "--key-id", id, "--policy-name", "default", "--policy", "{}")
	if lp := h.AWSJSON(t, "kms", "list-key-policies", "--key-id", id); lp["PolicyNames"].([]any)[0] != "default" {
		t.Fatalf("list-key-policies: %v", lp)
	}
	h.AWS(t, "kms", "tag-resource", "--key-id", id, "--tags", "TagKey=team,TagValue=core")
	h.AWS(t, "kms", "untag-resource", "--key-id", id, "--tag-keys", "env")
	if tags := h.AWSJSON(t, "kms", "list-resource-tags", "--key-id", id)["Tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["TagKey"] != "team" {
		t.Fatalf("tags: %v", tags)
	}
	expectErr(t, h, "TagException", "kms", "tag-resource", "--key-id", id, "--tags", "TagKey=aws:x,TagValue=1")

	// Rotation settings.
	h.AWS(t, "kms", "enable-key-rotation", "--key-id", id, "--rotation-period-in-days", "90")
	rs := h.AWSJSON(t, "kms", "get-key-rotation-status", "--key-id", id)
	if rs["KeyRotationEnabled"] != true || rs["RotationPeriodInDays"] != float64(90) || rs["NextRotationDate"] == nil {
		t.Fatalf("rotation status: %v", rs)
	}
	h.AWS(t, "kms", "rotate-key-on-demand", "--key-id", id)
	clk.Add(91 * 24 * time.Hour)
	k.Maintain()
	rot := h.AWSJSON(t, "kms", "list-key-rotations", "--key-id", id)["Rotations"].([]any)
	if len(rot) != 2 || rot[0].(map[string]any)["RotationType"] != "ON_DEMAND" || rot[1].(map[string]any)["RotationType"] != "AUTOMATIC" {
		t.Fatalf("list-key-rotations: %v", rot)
	}
	h.AWS(t, "kms", "disable-key-rotation", "--key-id", id)

	// Enable / disable.
	h.AWS(t, "kms", "disable-key", "--key-id", "alias/app")
	if m := meta(t, h, "kms", "describe-key", "--key-id", id); m["Enabled"] != false || m["KeyState"] != "Disabled" {
		t.Fatalf("disabled: %v", m)
	}
	h.AWS(t, "kms", "enable-key", "--key-id", id)
	h.AWS(t, "kms", "update-key-description", "--key-id", id, "--description", "renamed")

	// Aliases: update, list with pagination, delete.
	m2 := meta(t, h, "kms", "create-key")
	h.AWS(t, "kms", "update-alias", "--alias-name", "alias/app", "--target-key-id", m2["KeyId"].(string))
	if got := meta(t, h, "kms", "describe-key", "--key-id", "alias/app"); got["KeyId"] != m2["KeyId"] {
		t.Fatalf("update-alias: %v", got)
	}
	signing := meta(t, h, "kms", "create-key", "--key-spec", "ECC_NIST_P256", "--key-usage", "SIGN_VERIFY")
	expectErr(t, h, "ValidationException", "kms", "update-alias", "--alias-name", "alias/app", "--target-key-id", signing["KeyId"].(string))
	for _, a := range []string{"alias/b1", "alias/b2", "alias/b3"} {
		h.AWS(t, "kms", "create-alias", "--alias-name", a, "--target-key-id", id)
	}
	if al := h.AWSJSON(t, "kms", "list-aliases", "--key-id", id)["Aliases"].([]any); len(al) != 3 {
		t.Fatalf("list-aliases --key-id: %v", al)
	}
	if al := h.AWSJSON(t, "kms", "list-aliases", "--page-size", "2")["Aliases"].([]any); len(al) != 5 { // 3 + alias/app + alias/aws/ssm
		t.Fatalf("list-aliases paginated: %d %v", len(al), al)
	}
	h.AWS(t, "kms", "delete-alias", "--alias-name", "alias/b3")
	expectErr(t, h, "NotFoundException", "kms", "delete-alias", "--alias-name", "alias/b3")
	if keys := h.AWSJSON(t, "kms", "list-keys", "--page-size", "1")["Keys"].([]any); len(keys) != 4 {
		t.Fatalf("list-keys: %v", keys)
	}

	// Grants.
	g := h.AWSJSON(t, "kms", "create-grant", "--key-id", id, "--grantee-principal", "arn:aws:iam::"+h.Env.AccountID+":role/app",
		"--operations", "Decrypt", "Encrypt", "--name", "g1")
	if g["GrantId"] == "" || g["GrantToken"] == "" {
		t.Fatalf("create-grant: %v", g)
	}
	if gs := h.AWSJSON(t, "kms", "list-grants", "--key-id", id)["Grants"].([]any); len(gs) != 1 || gs[0].(map[string]any)["Name"] != "g1" {
		t.Fatalf("list-grants: %v", gs)
	}
	h.AWS(t, "kms", "revoke-grant", "--key-id", id, "--grant-id", g["GrantId"].(string))
	if gs := h.AWSJSON(t, "kms", "list-grants", "--key-id", id)["Grants"].([]any); len(gs) != 0 {
		t.Fatalf("grant not revoked: %v", gs)
	}

	// Scheduled deletion: pending keys can't be used, cancel returns them disabled,
	// and they are deleted (with their aliases) once the window passes.
	expectErr(t, h, "ValidationException", "kms", "schedule-key-deletion", "--key-id", id, "--pending-window-in-days", "5")
	sd := h.AWSJSON(t, "kms", "schedule-key-deletion", "--key-id", id, "--pending-window-in-days", "7")
	if sd["KeyState"] != "PendingDeletion" || sd["PendingWindowInDays"] != float64(7) || sd["DeletionDate"] == nil {
		t.Fatalf("schedule-key-deletion: %v", sd)
	}
	expectErr(t, h, "KMSInvalidStateException", "kms", "schedule-key-deletion", "--key-id", id)
	expectErr(t, h, "KMSInvalidStateException", "kms", "enable-key", "--key-id", id)
	expectErr(t, h, "KMSInvalidStateException", "kms", "generate-data-key", "--key-id", id, "--key-spec", "AES_256")
	h.AWS(t, "kms", "cancel-key-deletion", "--key-id", id)
	if m := meta(t, h, "kms", "describe-key", "--key-id", id); m["KeyState"] != "Disabled" {
		t.Fatalf("after cancel: %v", m)
	}
	h.AWS(t, "kms", "schedule-key-deletion", "--key-id", id, "--pending-window-in-days", "7")
	clk.Add(6 * 24 * time.Hour)
	k.Maintain()
	meta(t, h, "kms", "describe-key", "--key-id", id)
	clk.Add(25 * time.Hour)
	k.Maintain()
	expectErr(t, h, "NotFoundException", "kms", "describe-key", "--key-id", id)
	expectErr(t, h, "NotFoundException", "kms", "describe-key", "--key-id", "alias/b1")
}

func TestKMSCryptoBoto3(t *testing.T) {
	h, _, _ := setup(t)
	out := h.Python(t, `
kms = boto3.client("kms")
def code(f):
    try:
        f()
        return "OK"
    except botocore.exceptions.ClientError as e:
        return e.response["Error"]["Code"]

k1 = kms.create_key(Description="one")["KeyMetadata"]["KeyId"]
k2 = kms.create_key(Description="two")["KeyMetadata"]["Arn"]
kms.create_alias(AliasName="alias/one", TargetKeyId=k1)
ctx = {"tenant": "a", "purpose": "test"}

c = kms.encrypt(KeyId="alias/one", Plaintext=b"hello world", EncryptionContext=ctx)
assert c["KeyId"].endswith(k1) and c["EncryptionAlgorithm"] == "SYMMETRIC_DEFAULT", c
# Ciphertexts are self-describing: decrypt needs no KeyId.
d = kms.decrypt(CiphertextBlob=c["CiphertextBlob"], EncryptionContext=ctx)
assert d["Plaintext"] == b"hello world" and d["KeyId"].endswith(k1), d
print("wrongctx", code(lambda: kms.decrypt(CiphertextBlob=c["CiphertextBlob"], EncryptionContext={"tenant": "b", "purpose": "test"})))
print("noctx", code(lambda: kms.decrypt(CiphertextBlob=c["CiphertextBlob"])))
print("wrongkey", code(lambda: kms.decrypt(CiphertextBlob=c["CiphertextBlob"], EncryptionContext=ctx, KeyId=k2)))
print("garbage", code(lambda: kms.decrypt(CiphertextBlob=b"not a ciphertext at all, sorry")))
print("big", code(lambda: kms.encrypt(KeyId=k1, Plaintext=b"x" * 4097)))
assert kms.decrypt(CiphertextBlob=c["CiphertextBlob"], EncryptionContext=ctx, KeyId="alias/one")["Plaintext"] == b"hello world"

# Rotation: old ciphertexts still decrypt.
kms.rotate_key_on_demand(KeyId=k1)
assert kms.decrypt(CiphertextBlob=c["CiphertextBlob"], EncryptionContext=ctx)["Plaintext"] == b"hello world"

# Re-encrypt to another key.
r = kms.re_encrypt(CiphertextBlob=c["CiphertextBlob"], SourceEncryptionContext=ctx, DestinationKeyId=k2, DestinationEncryptionContext={"x": "y"})
assert r["SourceKeyId"].endswith(k1) and r["KeyId"] == k2, r
assert kms.decrypt(CiphertextBlob=r["CiphertextBlob"], EncryptionContext={"x": "y"})["Plaintext"] == b"hello world"

# Envelope encryption: data keys.
dk = kms.generate_data_key(KeyId=k1, KeySpec="AES_256", EncryptionContext=ctx)
assert len(dk["Plaintext"]) == 32
assert kms.decrypt(CiphertextBlob=dk["CiphertextBlob"], EncryptionContext=ctx)["Plaintext"] == dk["Plaintext"]
assert len(kms.generate_data_key(KeyId=k1, KeySpec="AES_128")["Plaintext"]) == 16
assert len(kms.generate_data_key(KeyId=k1, NumberOfBytes=64)["Plaintext"]) == 64
wp = kms.generate_data_key_without_plaintext(KeyId=k1, KeySpec="AES_256")
assert "Plaintext" not in wp and len(kms.decrypt(CiphertextBlob=wp["CiphertextBlob"])["Plaintext"]) == 32
print("nospec", code(lambda: kms.generate_data_key(KeyId=k1)))
print("both", code(lambda: kms.generate_data_key(KeyId=k1, KeySpec="AES_256", NumberOfBytes=32)))
print("dry", code(lambda: kms.generate_data_key(KeyId=k1, KeySpec="AES_256", DryRun=True)))

pair = kms.generate_data_key_pair(KeyId=k1, KeyPairSpec="ECC_NIST_P256")
assert kms.decrypt(CiphertextBlob=pair["PrivateKeyCiphertextBlob"])["Plaintext"] == pair["PrivateKeyPlaintext"]
assert len(pair["PublicKey"]) > 50
assert "PrivateKeyPlaintext" not in kms.generate_data_key_pair_without_plaintext(KeyId=k1, KeyPairSpec="RSA_2048")
assert len(kms.generate_random(NumberOfBytes=48)["Plaintext"]) == 48

# Disabled keys.
kms.disable_key(KeyId=k1)
print("disabled-enc", code(lambda: kms.encrypt(KeyId=k1, Plaintext=b"x")))
print("disabled-dec", code(lambda: kms.decrypt(CiphertextBlob=c["CiphertextBlob"], EncryptionContext=ctx)))
kms.enable_key(KeyId=k1)

# HMAC keys.
hk = kms.create_key(KeySpec="HMAC_256", KeyUsage="GENERATE_VERIFY_MAC")["KeyMetadata"]
assert hk["MacAlgorithms"] == ["HMAC_SHA_256"], hk
mac = kms.generate_mac(KeyId=hk["KeyId"], Message=b"msg", MacAlgorithm="HMAC_SHA_256")["Mac"]
assert kms.verify_mac(KeyId=hk["KeyId"], Message=b"msg", Mac=mac, MacAlgorithm="HMAC_SHA_256")["MacValid"]
print("badmac", code(lambda: kms.verify_mac(KeyId=hk["KeyId"], Message=b"other", Mac=mac, MacAlgorithm="HMAC_SHA_256")))
print("macalg", code(lambda: kms.generate_mac(KeyId=hk["KeyId"], Message=b"msg", MacAlgorithm="HMAC_SHA_512")))
print("hmacenc", code(lambda: kms.encrypt(KeyId=hk["KeyId"], Plaintext=b"x")))
print("badspec", code(lambda: kms.create_key(KeySpec="HMAC_256")))

# Asymmetric keys.
for spec, alg in [("ECC_NIST_P256", "ECDSA_SHA_256"), ("RSA_2048", "RSASSA_PSS_SHA_256"), ("RSA_2048", "RSASSA_PKCS1_V1_5_SHA_256")]:
    sk = kms.create_key(KeySpec=spec, KeyUsage="SIGN_VERIFY")["KeyMetadata"]
    assert alg in sk["SigningAlgorithms"], sk
    sig = kms.sign(KeyId=sk["KeyId"], Message=b"payload", SigningAlgorithm=alg)["Signature"]
    assert kms.verify(KeyId=sk["KeyId"], Message=b"payload", Signature=sig, SigningAlgorithm=alg)["SignatureValid"]
    import hashlib
    dg = hashlib.sha256(b"payload").digest()
    sig2 = kms.sign(KeyId=sk["KeyId"], Message=dg, MessageType="DIGEST", SigningAlgorithm=alg)["Signature"]
    assert kms.verify(KeyId=sk["KeyId"], Message=b"payload", Signature=sig2, SigningAlgorithm=alg)["SignatureValid"]
    print("badsig", code(lambda: kms.verify(KeyId=sk["KeyId"], Message=b"tampered", Signature=sig, SigningAlgorithm=alg)))
    pk = kms.get_public_key(KeyId=sk["KeyId"])
    assert pk["KeyUsage"] == "SIGN_VERIFY" and len(pk["PublicKey"]) > 50
    print("signenc", code(lambda: kms.encrypt(KeyId=sk["KeyId"], Plaintext=b"x")))
rk = kms.create_key(KeySpec="RSA_2048", KeyUsage="ENCRYPT_DECRYPT")["KeyMetadata"]
rc = kms.encrypt(KeyId=rk["KeyId"], Plaintext=b"secret", EncryptionAlgorithm="RSAES_OAEP_SHA_256")
assert kms.decrypt(KeyId=rk["KeyId"], CiphertextBlob=rc["CiphertextBlob"], EncryptionAlgorithm="RSAES_OAEP_SHA_256")["Plaintext"] == b"secret"
print("rsanoalg", code(lambda: kms.encrypt(KeyId=rk["KeyId"], Plaintext=b"x")))
print("rsanokey", code(lambda: kms.decrypt(CiphertextBlob=rc["CiphertextBlob"], EncryptionAlgorithm="RSAES_OAEP_SHA_256")))
print("rotateasym", code(lambda: kms.enable_key_rotation(KeyId=rk["KeyId"])))

# Paginators.
keys = [k["KeyId"] for p in kms.get_paginator("list_keys").paginate(PaginationConfig={"PageSize": 2}) for k in p["Keys"]]
assert len(keys) == len(set(keys)) == 7, keys
print("done")
`)
	for _, want := range []string{"wrongctx InvalidCiphertextException", "noctx InvalidCiphertextException", "wrongkey IncorrectKeyException",
		"garbage InvalidCiphertextException", "big ValidationException", "nospec ValidationException", "both ValidationException",
		"dry DryRunOperationException", "disabled-enc DisabledException", "disabled-dec DisabledException", "badmac KMSInvalidMacException",
		"macalg InvalidKeyUsageException", "hmacenc InvalidKeyUsageException", "badspec ValidationException", "badsig KMSInvalidSignatureException",
		"signenc InvalidKeyUsageException", "rsanoalg InvalidKeyUsageException", "rsanokey InvalidCiphertextException",
		"rotateasym UnsupportedOperationException", "done"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// TestKMSPublicKeyInterop checks GetPublicKey output with Go's crypto: signatures
// from Sign verify locally, and data encrypted locally decrypts in KMS.
func TestKMSPublicKeyInterop(t *testing.T) {
	h, _, _ := setup(t)
	b64 := func(s string) []byte {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	msg := base64.StdEncoding.EncodeToString([]byte("payload"))
	digest := sha256.Sum256([]byte("payload"))

	ec := meta(t, h, "kms", "create-key", "--key-spec", "ECC_NIST_P256", "--key-usage", "SIGN_VERIFY")["KeyId"].(string)
	pub, err := x509.ParsePKIXPublicKey(b64(h.AWSJSON(t, "kms", "get-public-key", "--key-id", ec)["PublicKey"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	sig := b64(h.AWSJSON(t, "kms", "sign", "--key-id", ec, "--message", msg, "--signing-algorithm", "ECDSA_SHA_256")["Signature"].(string))
	if !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), digest[:], sig) {
		t.Fatal("ECDSA signature does not verify locally")
	}

	rk := meta(t, h, "kms", "create-key", "--key-spec", "RSA_2048", "--key-usage", "SIGN_VERIFY")["KeyId"].(string)
	rpub, err := x509.ParsePKIXPublicKey(b64(h.AWSJSON(t, "kms", "get-public-key", "--key-id", rk)["PublicKey"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	sig = b64(h.AWSJSON(t, "kms", "sign", "--key-id", rk, "--message", msg, "--signing-algorithm", "RSASSA_PSS_SHA_256")["Signature"].(string))
	if err := rsa.VerifyPSS(rpub.(*rsa.PublicKey), crypto.SHA256, digest[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}); err != nil {
		t.Fatalf("PSS signature: %v", err)
	}

	ek := meta(t, h, "kms", "create-key", "--key-spec", "RSA_3072", "--key-usage", "ENCRYPT_DECRYPT")["KeyId"].(string)
	epub, err := x509.ParsePKIXPublicKey(b64(h.AWSJSON(t, "kms", "get-public-key", "--key-id", ek)["PublicKey"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, epub.(*rsa.PublicKey), []byte("local"), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := h.AWSJSON(t, "kms", "decrypt", "--key-id", ek, "--ciphertext-blob", base64.StdEncoding.EncodeToString(ct), "--encryption-algorithm", "RSAES_OAEP_SHA_256")
	if string(b64(d["Plaintext"].(string))) != "local" {
		t.Fatalf("decrypt: %v", d)
	}
}

func TestKMSIAM(t *testing.T) {
	h, _, _ := setup(t)
	id := meta(t, h, "kms", "create-key")["KeyId"].(string)
	blob := h.AWSJSON(t, "kms", "encrypt", "--key-id", id, "--plaintext", base64.StdEncoding.EncodeToString([]byte("x")))["CiphertextBlob"].(string)
	akid, secret := h.User(t, "ro", "ReadOnlyAccess")
	if out, err := h.AWSAs(t, akid, secret, "", "kms", "describe-key", "--key-id", id); err != nil {
		t.Fatalf("read-only describe: %v %s", err, out)
	}
	for _, args := range [][]string{
		{"kms", "decrypt", "--ciphertext-blob", blob},
		{"kms", "encrypt", "--key-id", id, "--plaintext", "eA=="},
		{"kms", "schedule-key-deletion", "--key-id", id},
		{"kms", "schedule-key-deletion", "--key-id", "alias/unknown"}, // missing keys are authorized first
	} {
		if out, err := h.AWSAs(t, akid, secret, "", args...); err == nil || !strings.Contains(out, "AccessDeniedException") {
			t.Fatalf("%v as read-only: %v %s", args, err, out)
		}
	}
	// A key-scoped policy.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "one-key", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"kms:Encrypt", "kms:Decrypt"}, "Resource": "arn:aws:kms:us-east-1:" + h.Env.AccountID + ":key/" + id}}}})
	other := meta(t, h, "kms", "create-key")["KeyId"].(string)
	akid2, secret2 := h.User(t, "scoped", "one-key")
	if out, err := h.AWSAs(t, akid2, secret2, "", "kms", "decrypt", "--ciphertext-blob", blob); err != nil {
		t.Fatalf("scoped decrypt: %v %s", err, out)
	}
	if out, err := h.AWSAs(t, akid2, secret2, "", "kms", "encrypt", "--key-id", other, "--plaintext", "eA=="); err == nil || !strings.Contains(out, "AccessDeniedException") {
		t.Fatalf("scoped encrypt with other key: %v %s", err, out)
	}
}

func TestKMSNativeAPI(t *testing.T) {
	h, _, _ := setup(t)
	k := kms.New(h.Env, h.Secrets)
	k.Routes(h.Router)
	b := string(h.Native(t, "POST", "/api/v1/kms/keys", map[string]any{"description": "n", "alias": "alias/native", "rotation_enabled": true}))
	if !strings.Contains(b, `"alias/native"`) || !strings.Contains(strings.ReplaceAll(b, " ", ""), `"rotation_enabled":true`) {
		t.Fatalf("native create: %s", b)
	}
	enc := string(h.Native(t, "POST", "/api/v1/kms/encrypt", map[string]any{"key_id": "alias/native", "plaintext": "aGk="}))
	blob := enc[strings.Index(enc, `"ciphertext_blob": "`)+20:]
	blob = blob[:strings.Index(blob, `"`)]
	// Native ciphertexts decrypt over the AWS API and vice versa.
	if d := h.AWSJSON(t, "kms", "decrypt", "--ciphertext-blob", blob); d["Plaintext"] != "aGk=" {
		t.Fatalf("aws decrypt of native blob: %v", d)
	}
	if d := string(h.Native(t, "POST", "/api/v1/kms/decrypt", map[string]any{"ciphertext_blob": blob})); !strings.Contains(d, "aGk=") {
		t.Fatalf("native decrypt: %s", d)
	}
	h.Native(t, "POST", "/api/v1/kms/keys/alias%2Fnative/schedule-deletion", nil)
	if m := meta(t, h, "kms", "describe-key", "--key-id", "alias/native"); m["KeyState"] != "PendingDeletion" || m["PendingDeletionWindowInDays"] != float64(30) {
		t.Fatalf("native schedule-deletion: %v", m)
	}
}
