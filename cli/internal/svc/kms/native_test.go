package kms_test

import (
	"encoding/json"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
)

func TestKMSNativePolicyGrantsSigning(t *testing.T) {
	h, _, _ := setup(t)
	k := kms.New(h.Env, h.Secrets)
	k.Routes(h.Router)
	call := func(method, path string, body any) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(h.Native(t, method, path, body), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	id := call("POST", "/api/v1/kms/keys", map[string]any{"description": "sym"})["id"].(string)
	pol := call("GET", "/api/v1/kms/keys/"+id+"/policy", nil)["policy"].(string)
	call("PUT", "/api/v1/kms/keys/"+id+"/policy", map[string]any{"policy": pol})

	g := call("POST", "/api/v1/kms/keys/"+id+"/grants", map[string]any{"grantee_principal": "arn:aws:iam::" + h.Env.AccountID + ":root", "operations": []string{"Decrypt"}})
	var grants []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/kms/keys/"+id+"/grants", nil), &grants)
	if len(grants) != 1 || grants[0]["grant_id"] != g["grant_id"] {
		t.Fatalf("grants: %v", grants)
	}
	h.Native(t, "DELETE", "/api/v1/kms/keys/"+id+"/grants/"+g["grant_id"].(string), nil)
	grants = nil
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/kms/keys/"+id+"/grants", nil), &grants)
	if len(grants) != 0 {
		t.Fatalf("revoked grants: %v", grants)
	}

	msg := "aGVsbG8="
	sk := call("POST", "/api/v1/kms/keys", map[string]any{"key_spec": "ECC_NIST_P256", "key_usage": "SIGN_VERIFY"})["id"].(string)
	sig := call("POST", "/api/v1/kms/sign", map[string]any{"key_id": sk, "message": msg, "algorithm": "ECDSA_SHA_256"})["signature"]
	if v := call("POST", "/api/v1/kms/verify", map[string]any{"key_id": sk, "message": msg, "algorithm": "ECDSA_SHA_256", "signature": sig}); v["valid"] != true {
		t.Fatalf("verify: %v", v)
	}
	if v := call("POST", "/api/v1/kms/verify", map[string]any{"key_id": sk, "message": "Ynll", "algorithm": "ECDSA_SHA_256", "signature": sig}); v["valid"] != false {
		t.Fatalf("verify tampered: %v", v)
	}
	if p := call("GET", "/api/v1/kms/keys/"+sk+"/public-key", nil); p["pem"] == "" {
		t.Fatalf("public key: %v", p)
	}

	mk := call("POST", "/api/v1/kms/keys", map[string]any{"key_spec": "HMAC_256", "key_usage": "GENERATE_VERIFY_MAC"})["id"].(string)
	mac := call("POST", "/api/v1/kms/generate-mac", map[string]any{"key_id": mk, "message": msg, "algorithm": "HMAC_SHA_256"})["mac"]
	if v := call("POST", "/api/v1/kms/verify-mac", map[string]any{"key_id": mk, "message": msg, "algorithm": "HMAC_SHA_256", "mac": mac}); v["valid"] != true {
		t.Fatalf("verify-mac: %v", v)
	}

	rk := call("POST", "/api/v1/kms/keys", map[string]any{"key_spec": "RSA_2048", "key_usage": "ENCRYPT_DECRYPT"})["id"].(string)
	enc := call("POST", "/api/v1/kms/encrypt", map[string]any{"key_id": rk, "plaintext": msg, "encryption_algorithm": "RSAES_OAEP_SHA_256"})
	if d := call("POST", "/api/v1/kms/decrypt", map[string]any{"key_id": rk, "ciphertext_blob": enc["ciphertext_blob"], "encryption_algorithm": "RSAES_OAEP_SHA_256"}); d["plaintext"] != msg {
		t.Fatalf("rsa decrypt: %v", d)
	}
}
