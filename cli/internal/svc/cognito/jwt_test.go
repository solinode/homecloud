package cognito

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"
)

func TestJWT(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	tok, err := sign(k, "kid1", map[string]any{"sub": "u1", "exp": time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(tok, &k.PublicKey)
	if err != nil || c["sub"] != "u1" {
		t.Fatalf("verify: %v %v", c, err)
	}
	if kidOf(tok) != "kid1" {
		t.Error("kid")
	}
	parts := strings.Split(tok, ".")
	forged, _ := sign(k, "kid1", map[string]any{"sub": "admin", "exp": time.Now().Add(time.Minute).Unix()})
	tampered := parts[0] + "." + strings.Split(forged, ".")[1] + "." + parts[2]
	if _, err := Verify(tampered, &k.PublicKey); err != errSignature {
		t.Errorf("tampered payload accepted: %v", err)
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, err := Verify(tok, &other.PublicKey); err != errSignature {
		t.Error("wrong key accepted")
	}
	old, _ := sign(k, "kid1", map[string]any{"exp": time.Now().Add(-time.Second).Unix()})
	if _, err := Verify(old, &k.PublicKey); err != errExpired {
		t.Error("expired token accepted")
	}
}

func TestPasswordPolicy(t *testing.T) {
	p := PasswordPolicy{MinLength: 8, RequireNumbers: true, RequireUppercase: true}
	if p.check("Password1") != nil {
		t.Error("valid password rejected")
	}
	if p.check("password1") == nil || p.check("Pass1") == nil {
		t.Error("weak password accepted")
	}
}
