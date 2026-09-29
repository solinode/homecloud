package cognito

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// sign produces an RS256 JWT.
func sign(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + b64.EncodeToString(sig), nil
}

var (
	errMalformed = errors.New("malformed token")
	errSignature = errors.New("invalid token signature")
	errExpired   = errors.New("token has expired")
)

// Verify checks an RS256 JWT against a public key and returns its claims.
func Verify(token string, pub *rsa.PublicKey) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &hdr) != nil || hdr.Alg != "RS256" {
		return nil, errMalformed
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, errMalformed
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		return nil, errSignature
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, errMalformed
	}
	var claims map[string]any
	if err := json.Unmarshal(pb, &claims); err != nil {
		return nil, errMalformed
	}
	if exp, ok := claims["exp"].(float64); !ok || time.Now().Unix() >= int64(exp) {
		return nil, errExpired
	}
	return claims, nil
}

// kidOf reads the key ID from a token header without verifying it.
func kidOf(token string) string {
	h, _, _ := strings.Cut(token, ".")
	b, err := b64.DecodeString(h)
	if err != nil {
		return ""
	}
	var hdr struct {
		Kid string `json:"kid"`
	}
	_ = json.Unmarshal(b, &hdr)
	return hdr.Kid
}

func jwk(pub *rsa.PublicKey, kid string) map[string]string {
	return map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
		"n": b64.EncodeToString(pub.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(pub.E)).Bytes())}
}
