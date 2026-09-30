package kms_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

// importParams runs aws kms get-parameters-for-import and returns the token and wrapping public key.
func importParams(t *testing.T, h *awstest.Harness, id, alg, spec string) (token string, pub *rsa.PublicKey, out map[string]any) {
	t.Helper()
	out = h.AWSJSON(t, "kms", "get-parameters-for-import", "--key-id", id, "--wrapping-algorithm", alg, "--wrapping-key-spec", spec)
	der, err := base64.StdEncoding.DecodeString(out["PublicKey"].(string))
	if err != nil {
		t.Fatal(err)
	}
	p, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	return out["ImportToken"].(string), p.(*rsa.PublicKey), out
}

// wrapFile writes material wrapped with RSAES_OAEP_SHA_256 to a file.
func wrapFile(t *testing.T, pub *rsa.PublicKey, material []byte) string {
	t.Helper()
	w, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, material, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "wrapped.bin")
	if err := os.WriteFile(f, w, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	b, _ := base64.StdEncoding.DecodeString(token)
	f := filepath.Join(t.TempDir(), "token.bin")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestKMSImportedKeyMaterialCLI(t *testing.T) {
	h, _, clk := setup(t)
	m := meta(t, h, "kms", "create-key", "--origin", "EXTERNAL", "--description", "byok")
	id := m["KeyId"].(string)
	if m["KeyState"] != "PendingImport" || m["Origin"] != "EXTERNAL" || m["Enabled"] != false {
		t.Fatalf("new EXTERNAL key: %v", m)
	}
	// Nothing works before the material arrives.
	expectErr(t, h, "KMSInvalidStateException", "kms", "encrypt", "--key-id", id, "--plaintext", "eA==")
	expectErr(t, h, "KMSInvalidStateException", "kms", "enable-key", "--key-id", id)
	expectErr(t, h, "UnsupportedOperationException", "kms", "enable-key-rotation", "--key-id", id)
	// AWS_KMS keys take no material.
	plain := meta(t, h, "kms", "create-key")["KeyId"].(string)
	expectErr(t, h, "UnsupportedOperationException", "kms", "get-parameters-for-import", "--key-id", plain,
		"--wrapping-algorithm", "RSAES_OAEP_SHA_256", "--wrapping-key-spec", "RSA_2048")
	expectErr(t, h, "UnsupportedOperationException", "kms", "delete-imported-key-material", "--key-id", plain)
	expectErr(t, h, "ValidationException", "kms", "get-parameters-for-import", "--key-id", id,
		"--wrapping-algorithm", "RSAES_OAEP_SHA_256", "--wrapping-key-spec", "RSA_1024")

	token, pub, out := importParams(t, h, id, "RSAES_OAEP_SHA_256", "RSA_2048")
	if s, _ := out["ParametersValidTo"].(string); s == "" {
		t.Fatalf("token lifetime: %v", out["ParametersValidTo"])
	}
	material := make([]byte, 32)
	_, _ = rand.Read(material)
	validTo := time.Now().Add(2 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")

	// Wrong length, missing ValidTo, and a token of another key are rejected.
	short := wrapFile(t, pub, material[:16])
	expectErr(t, h, "IncorrectKeyMaterialException", "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token),
		"--encrypted-key-material", "fileb://"+short, "--expiration-model", "KEY_MATERIAL_EXPIRES", "--valid-to", validTo)
	wrapped := wrapFile(t, pub, material)
	expectErr(t, h, "ValidationException", "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token),
		"--encrypted-key-material", "fileb://"+wrapped, "--expiration-model", "KEY_MATERIAL_EXPIRES")
	other := meta(t, h, "kms", "create-key", "--origin", "EXTERNAL")["KeyId"].(string)
	expectErr(t, h, "InvalidImportTokenException", "kms", "import-key-material", "--key-id", other, "--import-token", "fileb://"+tokenFile(t, token),
		"--encrypted-key-material", "fileb://"+wrapped, "--expiration-model", "KEY_MATERIAL_DOES_NOT_EXPIRE")
	expectErr(t, h, "InvalidCiphertextException", "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token),
		"--encrypted-key-material", "fileb://"+tokenFile(t, token), "--expiration-model", "KEY_MATERIAL_EXPIRES", "--valid-to", validTo)

	h.AWS(t, "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token),
		"--encrypted-key-material", "fileb://"+wrapped, "--expiration-model", "KEY_MATERIAL_EXPIRES", "--valid-to", validTo)
	m = meta(t, h, "kms", "describe-key", "--key-id", id)
	if m["KeyState"] != "Enabled" || m["ExpirationModel"] != "KEY_MATERIAL_EXPIRES" || m["ValidTo"] == nil || m["Origin"] != "EXTERNAL" {
		t.Fatalf("imported key: %v", m)
	}

	// Imported keys work like AWS_KMS keys.
	enc := h.AWSJSON(t, "kms", "encrypt", "--key-id", id, "--plaintext", base64.StdEncoding.EncodeToString([]byte("secret")),
		"--encryption-context", "a=b")
	blob := enc["CiphertextBlob"].(string)
	dec := h.AWSJSON(t, "kms", "decrypt", "--ciphertext-blob", blob, "--encryption-context", "a=b")
	if p, _ := base64.StdEncoding.DecodeString(dec["Plaintext"].(string)); string(p) != "secret" {
		t.Fatalf("decrypt: %v", dec)
	}
	if dk := h.AWSJSON(t, "kms", "generate-data-key", "--key-id", id, "--key-spec", "AES_256"); dk["CiphertextBlob"] == nil {
		t.Fatalf("data key: %v", dk)
	}

	// Different material is rejected, the same material is accepted (with a new token).
	token2, pub2, _ := importParams(t, h, id, "RSAES_OAEP_SHA_256", "RSA_2048")
	diff := make([]byte, 32)
	_, _ = rand.Read(diff)
	expectErr(t, h, "IncorrectKeyMaterialException", "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token2),
		"--encrypted-key-material", "fileb://"+wrapFile(t, pub2, diff), "--expiration-model", "KEY_MATERIAL_DOES_NOT_EXPIRE")

	// Deleting the material returns the key to PendingImport; the ciphertext survives a re-import of the same material.
	h.AWS(t, "kms", "delete-imported-key-material", "--key-id", id)
	m = meta(t, h, "kms", "describe-key", "--key-id", id)
	if m["KeyState"] != "PendingImport" || m["Enabled"] != false || m["ValidTo"] != nil {
		t.Fatalf("after delete: %v", m)
	}
	expectErr(t, h, "KMSInvalidStateException", "kms", "decrypt", "--ciphertext-blob", blob, "--encryption-context", "a=b")
	expectErr(t, h, "KMSInvalidStateException", "kms", "generate-data-key", "--key-id", id, "--key-spec", "AES_256")
	expectErr(t, h, "IncorrectKeyMaterialException", "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token2),
		"--encrypted-key-material", "fileb://"+wrapFile(t, pub2, diff), "--expiration-model", "KEY_MATERIAL_DOES_NOT_EXPIRE")
	h.AWS(t, "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token2),
		"--encrypted-key-material", "fileb://"+wrapFile(t, pub2, material), "--expiration-model", "KEY_MATERIAL_DOES_NOT_EXPIRE")
	dec = h.AWSJSON(t, "kms", "decrypt", "--ciphertext-blob", blob, "--encryption-context", "a=b")
	if p, _ := base64.StdEncoding.DecodeString(dec["Plaintext"].(string)); string(p) != "secret" {
		t.Fatalf("decrypt after re-import: %v", dec)
	}
	if m = meta(t, h, "kms", "describe-key", "--key-id", id); m["ExpirationModel"] != "KEY_MATERIAL_DOES_NOT_EXPIRE" || m["ValidTo"] != nil {
		t.Fatalf("re-imported: %v", m)
	}

	// Material that expires stops working at ValidTo.
	h.AWS(t, "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token2),
		"--encrypted-key-material", "fileb://"+wrapFile(t, pub2, material), "--expiration-model", "KEY_MATERIAL_EXPIRES", "--valid-to", validTo)
	clk.Add(time.Hour)
	h.AWS(t, "kms", "decrypt", "--ciphertext-blob", blob, "--encryption-context", "a=b")
	clk.Add(2 * time.Hour)
	expectErr(t, h, "KMSInvalidStateException", "kms", "decrypt", "--ciphertext-blob", blob, "--encryption-context", "a=b")
	if m = meta(t, h, "kms", "describe-key", "--key-id", id); m["KeyState"] != "PendingImport" {
		t.Fatalf("after expiry: %v", m)
	}

	// Import tokens expire after 24 hours.
	token3, pub3, _ := importParams(t, h, id, "RSAES_OAEP_SHA_256", "RSA_3072")
	clk.Add(25 * time.Hour)
	expectErr(t, h, "ExpiredImportTokenException", "kms", "import-key-material", "--key-id", id, "--import-token", "fileb://"+tokenFile(t, token3),
		"--encrypted-key-material", "fileb://"+wrapFile(t, pub3, material), "--expiration-model", "KEY_MATERIAL_DOES_NOT_EXPIRE")
}

func TestKMSImportWrappingBoto3(t *testing.T) {
	py := os.Getenv("HC_TEST_PYTHON")
	if py == "" {
		py = "python3"
	}
	if exec.Command(py, "-c", "import cryptography, boto3").Run() != nil {
		t.Skip("python with cryptography and boto3 not available (set HC_TEST_PYTHON)")
	}
	h, _, _ := setup(t)
	out := h.Python(t, `
import os, datetime
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa, ec
from cryptography.hazmat.primitives.keywrap import aes_key_wrap_with_padding

kms = boto3.client("kms")
def code(f):
    try:
        f()
        return "OK"
    except botocore.exceptions.ClientError as e:
        return e.response["Error"]["Code"]

def oaep(h):
    return padding.OAEP(mgf=padding.MGF1(algorithm=h), algorithm=h, label=None)

def wrap(pub, alg, material):
    if alg == "RSAES_OAEP_SHA_256":
        return pub.encrypt(material, oaep(hashes.SHA256()))
    if alg == "RSAES_OAEP_SHA_1":
        return pub.encrypt(material, oaep(hashes.SHA1()))
    h = hashes.SHA256() if alg.endswith("SHA_256") else hashes.SHA1()
    aes = os.urandom(32)
    return pub.encrypt(aes, oaep(h)) + aes_key_wrap_with_padding(aes, material)

def imp(key_id, alg, spec, material, **kw):
    p = kms.get_parameters_for_import(KeyId=key_id, WrappingAlgorithm=alg, WrappingKeySpec=spec)
    pub = serialization.load_der_public_key(p["PublicKey"])
    kms.import_key_material(KeyId=key_id, ImportToken=p["ImportToken"], EncryptedKeyMaterial=wrap(pub, alg, material),
        ExpirationModel="KEY_MATERIAL_DOES_NOT_EXPIRE", **kw)

for alg in ["RSAES_OAEP_SHA_1", "RSAES_OAEP_SHA_256", "RSA_AES_KEY_WRAP_SHA_1", "RSA_AES_KEY_WRAP_SHA_256"]:
    for spec in ["RSA_2048", "RSA_3072"]:
        k = kms.create_key(Origin="EXTERNAL")["KeyMetadata"]
        assert k["KeyState"] == "PendingImport" and k["Origin"] == "EXTERNAL", k
        imp(k["KeyId"], alg, spec, os.urandom(32))
        c = kms.encrypt(KeyId=k["KeyId"], Plaintext=b"hi")
        assert kms.decrypt(CiphertextBlob=c["CiphertextBlob"])["Plaintext"] == b"hi"
        print(alg, spec, "ok")

# A token made for another algorithm cannot import.
k = kms.create_key(Origin="EXTERNAL")["KeyMetadata"]["KeyId"]
p = kms.get_parameters_for_import(KeyId=k, WrappingAlgorithm="RSAES_OAEP_SHA_1", WrappingKeySpec="RSA_2048")
pub = serialization.load_der_public_key(p["PublicKey"])
print("wrongalg", code(lambda: kms.import_key_material(KeyId=k, ImportToken=p["ImportToken"], EncryptedKeyMaterial=wrap(pub, "RSAES_OAEP_SHA_256", os.urandom(32)), ExpirationModel="KEY_MATERIAL_DOES_NOT_EXPIRE")))
print("noexpiry", code(lambda: kms.import_key_material(KeyId=k, ImportToken=p["ImportToken"], EncryptedKeyMaterial=wrap(pub, "RSAES_OAEP_SHA_1", os.urandom(32)))))
print("pastvalid", code(lambda: kms.import_key_material(KeyId=k, ImportToken=p["ImportToken"], EncryptedKeyMaterial=wrap(pub, "RSAES_OAEP_SHA_1", os.urandom(32)),
    ExpirationModel="KEY_MATERIAL_EXPIRES", ValidTo=datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=1))))

# HMAC keys: material length depends on the spec.
hk = kms.create_key(Origin="EXTERNAL", KeySpec="HMAC_256", KeyUsage="GENERATE_VERIFY_MAC")["KeyMetadata"]["KeyId"]
print("hmacshort", code(lambda: imp(hk, "RSAES_OAEP_SHA_256", "RSA_2048", os.urandom(16))))
imp(hk, "RSAES_OAEP_SHA_256", "RSA_2048", os.urandom(32))
mac = kms.generate_mac(KeyId=hk, Message=b"m", MacAlgorithm="HMAC_SHA_256")["Mac"]
assert kms.verify_mac(KeyId=hk, Message=b"m", Mac=mac, MacAlgorithm="HMAC_SHA_256")["MacValid"]

# RSA keys: import a private key (PKCS#8) with RSA_AES_KEY_WRAP and verify against the public key.
rk = kms.create_key(Origin="EXTERNAL", KeySpec="RSA_2048", KeyUsage="ENCRYPT_DECRYPT")["KeyMetadata"]["KeyId"]
priv = rsa.generate_private_key(public_exponent=65537, key_size=2048)
der = priv.private_bytes(serialization.Encoding.DER, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
imp(rk, "RSA_AES_KEY_WRAP_SHA_256", "RSA_2048", der)
ct = priv.public_key().encrypt(b"asym", oaep(hashes.SHA256()))
assert kms.decrypt(KeyId=rk, CiphertextBlob=ct, EncryptionAlgorithm="RSAES_OAEP_SHA_256")["Plaintext"] == b"asym"
ek = kms.create_key(Origin="EXTERNAL", KeySpec="ECC_NIST_P256", KeyUsage="SIGN_VERIFY")["KeyMetadata"]["KeyId"]
wrongder = priv.private_bytes(serialization.Encoding.DER, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
print("wrongcurve", code(lambda: imp(ek, "RSA_AES_KEY_WRAP_SHA_256", "RSA_2048", wrongder)))
`)
	for _, want := range []string{"wrongalg InvalidCiphertextException", "noexpiry ValidationException", "pastvalid ValidationException",
		"hmacshort IncorrectKeyMaterialException", "wrongcurve IncorrectKeyMaterialException", "RSA_AES_KEY_WRAP_SHA_256 RSA_3072 ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestKMSMultiRegion(t *testing.T) {
	h, _, _ := setup(t)
	m := meta(t, h, "kms", "create-key", "--multi-region")
	id := m["KeyId"].(string)
	cfg, _ := m["MultiRegionConfiguration"].(map[string]any)
	if !strings.HasPrefix(id, "mrk-") || m["MultiRegion"] != true || cfg["MultiRegionKeyType"] != "PRIMARY" ||
		!strings.HasSuffix(cfg["PrimaryKey"].(map[string]any)["Arn"].(string), ":key/"+id) {
		t.Fatalf("multi-Region key: %v", m)
	}
	if m = meta(t, h, "kms", "describe-key", "--key-id", id); m["MultiRegion"] != true || m["KeyState"] != "Enabled" {
		t.Fatalf("describe: %v", m)
	}
	blob := h.AWSJSON(t, "kms", "encrypt", "--key-id", id, "--plaintext", "eA==")["CiphertextBlob"].(string)
	if h.AWSJSON(t, "kms", "decrypt", "--ciphertext-blob", blob)["Plaintext"] != "eA==" {
		t.Fatal("multi-Region round trip")
	}
	// A single-region key is not multi-Region.
	single := meta(t, h, "kms", "create-key")
	if single["MultiRegion"] != false || single["MultiRegionConfiguration"] != nil || strings.HasPrefix(single["KeyId"].(string), "mrk-") {
		t.Fatalf("single key: %v", single)
	}
	// HomeCloud is one region: replicas and primary moves are honestly unsupported.
	expectErr(t, h, "UnsupportedOperationException", "kms", "replicate-key", "--key-id", id, "--replica-region", "eu-west-1")
	expectErr(t, h, "AlreadyExistsException", "kms", "replicate-key", "--key-id", id, "--replica-region", "us-east-1")
	expectErr(t, h, "UnsupportedOperationException", "kms", "replicate-key", "--key-id", single["KeyId"].(string), "--replica-region", "eu-west-1")
	expectErr(t, h, "UnsupportedOperationException", "kms", "update-primary-region", "--key-id", id, "--primary-region", "eu-west-1")
	expectErr(t, h, "UnsupportedOperationException", "kms", "update-primary-region", "--key-id", id, "--primary-region", "us-east-1")
	expectErr(t, h, "NotFoundException", "kms", "replicate-key", "--key-id", "mrk-00000000000000000000000000000000", "--replica-region", "eu-west-1")
	// A multi-Region key can have imported material too.
	ext := meta(t, h, "kms", "create-key", "--multi-region", "--origin", "EXTERNAL")
	if ext["KeyState"] != "PendingImport" || ext["MultiRegion"] != true {
		t.Fatalf("external multi-Region: %v", ext)
	}
}

func TestKMSImportIAM(t *testing.T) {
	h, _, _ := setup(t)
	id := meta(t, h, "kms", "create-key", "--origin", "EXTERNAL", "--multi-region")["KeyId"].(string)
	arn := "arn:aws:kms:us-east-1:" + h.Env.AccountID + ":key/" + id
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "kms-describe", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"kms:DescribeKey"}, "Resource": arn}}}})
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "kms-import", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"kms:GetParametersForImport", "kms:ImportKeyMaterial", "kms:DeleteImportedKeyMaterial", "kms:ReplicateKey"}, "Resource": arn}}}})
	deny := func(akid, secret string, args ...string) {
		t.Helper()
		if out, err := h.AWSAs(t, akid, secret, "", args...); err == nil || !strings.Contains(out, "AccessDeniedException") {
			t.Fatalf("%v: want AccessDenied: %v %s", args, err, out)
		}
	}
	akid, secret := h.User(t, "describer", "kms-describe")
	deny(akid, secret, "kms", "get-parameters-for-import", "--key-id", id, "--wrapping-algorithm", "RSAES_OAEP_SHA_256", "--wrapping-key-spec", "RSA_2048")
	deny(akid, secret, "kms", "import-key-material", "--key-id", id, "--import-token", "eA==", "--encrypted-key-material", "eA==", "--expiration-model", "KEY_MATERIAL_DOES_NOT_EXPIRE")
	deny(akid, secret, "kms", "delete-imported-key-material", "--key-id", id)
	deny(akid, secret, "kms", "replicate-key", "--key-id", id, "--replica-region", "eu-west-1")
	deny(akid, secret, "kms", "update-primary-region", "--key-id", id, "--primary-region", "eu-west-1")

	akid2, secret2 := h.User(t, "importer", "kms-import")
	out, err := h.AWSAs(t, akid2, secret2, "", "kms", "get-parameters-for-import", "--key-id", id, "--wrapping-algorithm", "RSAES_OAEP_SHA_256", "--wrapping-key-spec", "RSA_2048")
	if err != nil {
		t.Fatalf("importer get-parameters-for-import: %v %s", err, out)
	}
	if out, err := h.AWSAs(t, akid2, secret2, "", "kms", "delete-imported-key-material", "--key-id", id); err != nil {
		t.Fatalf("importer delete-imported-key-material: %v %s", err, out)
	}
	// Allowed by IAM, then refused because HomeCloud has one region.
	if out, err := h.AWSAs(t, akid2, secret2, "", "kms", "replicate-key", "--key-id", id, "--replica-region", "eu-west-1"); err == nil || !strings.Contains(out, "UnsupportedOperationException") {
		t.Fatalf("importer replicate-key: %v %s", err, out)
	}
}
