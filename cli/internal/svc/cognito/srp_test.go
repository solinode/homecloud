package cognito

import (
	"math/big"
	"testing"
)

// Known vector computed with pycognito's AWSSRP (fixed a and b, salt, user and
// password): the reference client's key and PASSWORD_CLAIM_SIGNATURE must be
// reproduced by the server side.
const (
	vecPool = "us-east-1_TestPool1"
	vecUser = "alice"
	vecPass = "Correct#Horse1"
	vecSalt = "3a5f0c9e7b1d2468ace02468bdf13579"
	vecA    = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	vecB    = "fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321"
	vecK    = "538282c4354742d7cbbde2359fcf67f9f5b3a6b08791e5011b43b8a5b66d9ee6"
	vecX    = "fd60b8eed3b043c0f2632e611aa748850fde995fb7eb552aee8443df817a71b0"
	vecBig  = "ca84fe3a718d3454728a6da23589985b97a62d34dfd516dd110a6e2d86a5e5a70e5dc71c8b29a5dd2d882abe1f79d70dbabbba2b06d13287cb8ebeb401c6388be9ead9bd791e0d63c67dd8b48b4e112551e7744e0daebb03ba68ce0a21dfc1785c29c78a851d84e8f98fccee5693b6356074488cd89279a295de741ed7db76e54468c427776da63c76018f774cad6f01154cea8846b9d2e7c4e794feee6f824f82f9f7449748e3bafa2b6925fa50c13563abeac4fba4811108aa102a2ea304c5978f5232369dc18f9c0f8563bbb4117b7400ea0ba746fc0b95c9deef3efe0756262e52cfc1179c0ade4342119afe37f292a2f3079fe32a273abe0ede6e6212c3d6a56886601207dd70a193f69733ffa61cbf22720a30bdd88546c7c138a6a4d686f994412330c5e8989980cf8c71544032b225a91163a02f2edc405aceb085330bb832d7469a4f0ddff431db73e74c9f39cd095871d1e762443c819634d341e945c4b04e06fde74a82bb98bfa0ef6782f975426eceb4ef2d565a2553e5075f64"
	vecKey  = "a342c4981bf001ce71e8833c542fa6a8"
	vecSig  = "l7zdjXXrkROEItWvhVV6KN4ZOa/6Mj3CgW1WyYRj2M4="
	vecTS   = "Wed Sep 2 5:04:09 UTC 2026"
)

func TestSRPGroup(t *testing.T) {
	if srpN.BitLen() != 3072 || !srpN.ProbablyPrime(10) || !new(big.Int).Rsh(srpN, 1).ProbablyPrime(10) {
		t.Fatal("N is not the 3072-bit safe prime")
	}
	if srpK.Text(16) != vecK {
		t.Fatalf("k = %s", srpK.Text(16))
	}
}

func TestSRPKnownVector(t *testing.T) {
	x := srpX(vecPool, vecUser, vecPass, vecSalt)
	if x.Text(16) != vecX {
		t.Fatalf("x = %s", x.Text(16))
	}
	v := new(big.Int).Exp(srpG, x, srpN)
	a, b := mustHex(vecA), mustHex(vecB)
	bigB := srpServerB(v, b)
	if bigB.Text(16) != vecBig {
		t.Fatalf("B = %s", bigB.Text(16))
	}
	bigA := new(big.Int).Exp(srpG, a, srpN)
	key := srpServerKey(bigA, b, v, bigB)
	if got := hexOf(key); got != vecKey {
		t.Fatalf("key = %s", got)
	}
	block := make([]byte, 64)
	for i := range block {
		block[i] = byte(i)
	}
	if got := srpSignature(key, vecPool, vecUser, block, vecTS); got != vecSig {
		t.Fatalf("signature = %s", got)
	}
	if srpSignature(key, vecPool, vecUser, block, vecTS+" ") == vecSig {
		t.Fatal("signature ignores the timestamp")
	}
}

func TestSRPRoundTrip(t *testing.T) {
	salt, ver := newVerifier(vecPool, vecUser, vecPass)
	if x := srpX(vecPool, vecUser, vecPass, salt); new(big.Int).Exp(srpG, x, srpN).Text(16) != ver {
		t.Fatal("verifier does not match the password")
	}
	if other, _ := newVerifier(vecPool, vecUser, vecPass); other == salt {
		t.Fatal("salt is not random")
	}
	// Client side of the exchange, written from the spec, agrees with the server.
	v := mustHex(ver)
	a, b := mustHex(vecA), randInt(512)
	bigA := new(big.Int).Exp(srpG, a, srpN)
	bigB := srpServerB(v, b)
	u := srpU(bigA, bigB)
	x := srpX(vecPool, vecUser, vecPass, salt)
	base := new(big.Int).Sub(bigB, new(big.Int).Mul(srpK, new(big.Int).Exp(srpG, x, srpN)))
	base.Mod(base, srpN)
	s := new(big.Int).Exp(base, new(big.Int).Add(a, new(big.Int).Mul(u, x)), srpN)
	if s.Cmp(new(big.Int).Exp(new(big.Int).Mul(bigA, new(big.Int).Exp(v, u, srpN)), b, srpN)) != 0 {
		t.Fatal("client and server secrets differ")
	}
}

func hexOf(b []byte) string {
	const d = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, d[c>>4], d[c&15])
	}
	return string(out)
}
