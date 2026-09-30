package cognito

// SRP-6a as Cognito implements it (USER_SRP_AUTH), compatible with
// amazon-cognito-identity-js, pycognito and warrant: RFC 3526 3072-bit group,
// g = 2, k = H(00 | N | 02), x = H(pad(salt) | H(poolName + username + ":" +
// password)), and a session key derived with HKDF ("Caldera Derived Key").
// HomeCloud is the server: it keeps a verifier (salt and v = g^x) per user.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

const srpNHex = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F" +
	"83655D23DCA3AD961C62F356208552BB9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B" +
	"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF6955817183995497CEA956AE515D2261898FA0510" +
	"15728E5A8AAAC42DAD33170D04507A33A85521ABDF1CBA64ECFB850458DBEF0A8AEA71575D060C7DB3970F85A6E1E4C7" +
	"ABF5AE8CDB0933D71E8C94E04A25619DCEE3D2261AD2EE6BF12FFA06D98A0864D87602733EC86A64521F2B18177B200C" +
	"BBE117577A615D6C770988C0BAD946E208E24FA074E5AB3143DB5BFCE0FD108E4B82D120A93AD2CAFFFFFFFFFFFFFFFF"

var (
	srpN = mustHex(srpNHex)
	srpG = big.NewInt(2)
	srpK = new(big.Int).SetBytes(sha256Sum(append(append([]byte{0}, srpN.Bytes()...), 2)))
)

const (
	srpChallengeTTL = 5 * time.Minute
	srpMaxPending   = 20000
	srpClockSkew    = 15 * time.Minute
	srpTimeLayout   = "Mon Jan 2 15:4:5 UTC 2006"
)

func mustHex(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("bad hex")
	}
	return n
}

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

// padHexStr mirrors the reference clients' pad_hex: even length, and a leading
// 00 when the top bit would make the number look negative.
func padHexStr(h string) string {
	if len(h)%2 == 1 {
		return "0" + h
	}
	if h != "" && strings.ContainsRune("89abcdefABCDEF", rune(h[0])) {
		return "00" + h
	}
	return h
}

func padBytes(x *big.Int) []byte {
	b, _ := hex.DecodeString(padHexStr(x.Text(16)))
	return b
}

// poolName is the part of the pool ID after the region prefix.
func srpPoolName(poolID string) string {
	if _, rest, ok := strings.Cut(poolID, "_"); ok {
		return rest
	}
	return poolID
}

// srpX is the private key x from the salt (hex) and the user's credentials.
func srpX(poolID, username, password, saltHex string) *big.Int {
	inner := sha256Sum([]byte(srpPoolName(poolID) + username + ":" + password))
	salt, _ := hex.DecodeString(padHexStr(saltHex))
	return new(big.Int).SetBytes(sha256Sum(append(salt, inner...)))
}

// newVerifier derives a fresh salt and verifier for a password.
func newVerifier(poolID, username, password string) (saltHex, verifierHex string) {
	var salt [16]byte
	_, _ = rand.Read(salt[:])
	salt[0] = salt[0]%0x70 + 0x10 // top nibble 1-7: no leading zero, never sign-padded
	saltHex = hex.EncodeToString(salt[:])
	return saltHex, new(big.Int).Exp(srpG, srpX(poolID, username, password, saltHex), srpN).Text(16)
}

func srpU(a, b *big.Int) *big.Int {
	return new(big.Int).SetBytes(sha256Sum(append(padBytes(a), padBytes(b)...)))
}

// srpServerB is B = k*v + g^b mod N.
func srpServerB(v, b *big.Int) *big.Int {
	r := new(big.Int).Mul(srpK, v)
	r.Add(r, new(big.Int).Exp(srpG, b, srpN))
	return r.Mod(r, srpN)
}

// srpServerKey is the HKDF-derived 16-byte key: S = (A * v^u)^b mod N.
func srpServerKey(a, b, v, bigB *big.Int) []byte {
	u := srpU(a, bigB)
	s := new(big.Int).Exp(v, u, srpN)
	s.Mul(s, a).Mod(s, srpN)
	s.Exp(s, b, srpN)
	return srpKeyFrom(s, u)
}

// srpKeyFrom derives the session key from the shared secret S and u.
func srpKeyFrom(s, u *big.Int) []byte {
	prk := hmac.New(sha256.New, padBytes(u))
	prk.Write(padBytes(s))
	okm := hmac.New(sha256.New, prk.Sum(nil))
	okm.Write([]byte("Caldera Derived Key\x01"))
	return okm.Sum(nil)[:16]
}

// srpSignature is PASSWORD_CLAIM_SIGNATURE.
func srpSignature(key []byte, poolID, username string, secretBlock []byte, timestamp string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(srpPoolName(poolID) + username))
	m.Write(secretBlock)
	m.Write([]byte(timestamp))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// srpPending is a PASSWORD_VERIFIER challenge waiting for its answer. It lives
// in memory only (the ephemeral b must not be persisted) and is single-use.
type srpPending struct {
	pool, client, username string
	rateKey                string
	salt                   string
	a, b, bigB, v          *big.Int
	fake                   bool // unknown user, or no verifier: fails after the answer like a wrong password
	expires                time.Time
}

func randInt(bits int) *big.Int {
	b := make([]byte, bits/8)
	_, _ = rand.Read(b)
	return new(big.Int).SetBytes(b)
}

func (s *Service) srpFakeSalt(poolID, username string) string {
	m := hmac.New(sha256.New, s.srpKey)
	m.Write([]byte(poolID + "/" + strings.ToLower(username)))
	b := m.Sum(nil)[:16]
	b[0] = b[0]%0x70 + 0x10
	return hex.EncodeToString(b)
}

// srpInitiate answers SRP_A with the PASSWORD_VERIFIER challenge parameters.
func (s *Service) srpInitiate(p Pool, cl Client, username, srpA, ip string) (map[string]string, error) {
	if username == "" || srpA == "" {
		return nil, invalid("Missing required parameter USERNAME or SRP_A")
	}
	a, ok := new(big.Int).SetString(srpA, 16)
	if !ok || len(srpA) > len(srpNHex)+2 || new(big.Int).Mod(a, srpN).Sign() == 0 {
		return nil, invalid("Invalid SRP_A")
	}
	rk := p.ID + "/srp/" + strings.ToLower(username) + "/" + ip
	if !s.attemptLimit(p.ID+"/acct/"+strings.ToLower(username), maxAccountAttempts) || !s.attempt(rk) {
		return nil, core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many failed attempts; try again later")
	}
	st := &srpPending{pool: p.ID, client: cl.ID, username: username, rateKey: rk, a: a, b: randInt(512), expires: time.Now().Add(srpChallengeTTL)}
	u, err := s.getUser(p.ID, username)
	if err == nil && u.SRPSalt != "" && u.SRPVerifier != "" {
		st.username, st.salt = u.Username, u.SRPSalt
		st.v, _ = new(big.Int).SetString(u.SRPVerifier, 16)
	}
	if st.v == nil {
		st.fake, st.salt, st.v = true, s.srpFakeSalt(p.ID, username), randInt(256)
	}
	st.bigB = srpServerB(st.v, st.b)
	block := make([]byte, 64)
	_, _ = rand.Read(block)
	secretBlock := base64.StdEncoding.EncodeToString(block)
	s.mu.Lock()
	now := time.Now()
	if len(s.srp) >= srpMaxPending {
		for k, x := range s.srp {
			if now.After(x.expires) {
				delete(s.srp, k)
			}
		}
		if len(s.srp) >= srpMaxPending {
			s.mu.Unlock()
			return nil, core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many pending sign-ins; try again later")
		}
	}
	s.srp[hashToken(secretBlock)] = st
	s.mu.Unlock()
	return map[string]string{"SALT": st.salt, "SECRET_BLOCK": secretBlock, "SRP_B": st.bigB.Text(16), "USER_ID_FOR_SRP": st.username, "USERNAME": st.username}, nil
}

func srpDeny() error {
	return core.Errf(http.StatusBadRequest, "NotAuthorizedException", "Incorrect username or password.")
}

// srpVerify checks the PASSWORD_VERIFIER answer. The challenge is consumed
// whether or not the answer is right. On success it returns the user.
func (s *Service) srpVerify(p Pool, cl Client, resp map[string]string) (User, error) {
	block := resp["PASSWORD_CLAIM_SECRET_BLOCK"]
	sig, ts, name := resp["PASSWORD_CLAIM_SIGNATURE"], resp["TIMESTAMP"], resp["USERNAME"]
	if block == "" || sig == "" || ts == "" || name == "" {
		return User{}, invalid("Missing required parameter PASSWORD_CLAIM_SECRET_BLOCK, PASSWORD_CLAIM_SIGNATURE, TIMESTAMP or USERNAME")
	}
	k := hashToken(block)
	s.mu.Lock()
	st := s.srp[k]
	delete(s.srp, k)
	s.mu.Unlock()
	if st == nil || time.Now().After(st.expires) {
		return User{}, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "Invalid session for the user, session is expired.")
	}
	if st.pool != p.ID || st.client != cl.ID || !strings.EqualFold(st.username, name) {
		return User{}, srpDeny()
	}
	raw, err := base64.StdEncoding.DecodeString(block)
	if err != nil {
		return User{}, srpDeny()
	}
	want := srpSignature(srpServerKey(st.a, st.b, st.v, st.bigB), p.ID, st.username, raw, ts)
	okSig := subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
	if !okSig || st.fake {
		return User{}, srpDeny()
	}
	t, err := time.Parse(srpTimeLayout, ts)
	if err != nil || t.Before(time.Now().Add(-srpClockSkew)) || t.After(time.Now().Add(srpClockSkew)) {
		return User{}, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "Invalid timestamp.")
	}
	u, err := s.getUser(p.ID, st.username)
	if err != nil || u.SRPSalt != st.salt { // deleted, or its password changed meanwhile
		return User{}, srpDeny()
	}
	s.succeeded(st.rateKey)
	return u, nil
}
