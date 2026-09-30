package cognito

import (
	"encoding/base64"
	"math/big"

	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// SRPNHex is the SRP group modulus.
const SRPNHex = srpNHex

// SRPClient answers a PASSWORD_VERIFIER challenge as a client would (the
// reference-vector test pins the maths to pycognito). It returns the SRP_A for
// InitiateAuth and a function that turns the challenge parameters into the
// RespondToAuthChallenge responses.
func SRPClient(poolID, password string) (srpA string, answer func(cp map[string]string, timestamp string) map[string]string) {
	a := randInt(1024)
	bigA := new(big.Int).Exp(srpG, a, srpN)
	return bigA.Text(16), func(cp map[string]string, ts string) map[string]string {
		user := cp["USER_ID_FOR_SRP"]
		bigB := mustHex(cp["SRP_B"])
		u := srpU(bigA, bigB)
		x := srpX(poolID, user, password, cp["SALT"])
		base := new(big.Int).Sub(bigB, new(big.Int).Mul(srpK, new(big.Int).Exp(srpG, x, srpN)))
		base.Mod(base, srpN)
		s := new(big.Int).Exp(base, new(big.Int).Add(a, new(big.Int).Mul(u, x)), srpN)
		key := srpKeyFrom(s, u)
		block, _ := base64.StdEncoding.DecodeString(cp["SECRET_BLOCK"])
		return map[string]string{"USERNAME": user, "PASSWORD_CLAIM_SECRET_BLOCK": cp["SECRET_BLOCK"], "TIMESTAMP": ts,
			"PASSWORD_CLAIM_SIGNATURE": srpSignature(key, poolID, user, block, ts)}
	}
}

// ClearVerifier removes a user's SRP verifier, as for a user created before SRP support.
func ClearVerifier(s *Service, pool, user string) error {
	_, err := store.Update(s.env.Store, cUsers, userKey(pool, user), func(u *User) error { u.SRPSalt, u.SRPVerifier = "", ""; return nil })
	return err
}

// HasVerifier reports whether the user has an SRP verifier.
func HasVerifier(s *Service, pool, user string) bool {
	u, err := s.getUser(pool, user)
	return err == nil && u.SRPVerifier != ""
}
