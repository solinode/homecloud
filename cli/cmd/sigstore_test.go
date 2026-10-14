package cmd

import (
	"encoding/base64"
	"strings"
	"testing"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// testSigstore replaces the public-good trust roots with an in-memory
// Sigstore (CA, transparency log, timestamp authority) for the test, so
// signatures are made and checked offline.
func testSigstore(t *testing.T) *ca.VirtualSigstore {
	t.Helper()
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	oldTM, oldOpts := trustedMaterial, verifierOptions
	trustedMaterial = func() (root.TrustedMaterial, error) { return vs, nil }
	// The test CA's certificates carry no SCTs; everything else is checked as in production.
	verifierOptions = []verify.VerifierOption{verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1)}
	t.Cleanup(func() { trustedMaterial, verifierOptions = oldTM, oldOpts })
	return vs
}

// signBundle signs artifact as identity and returns a Sigstore bundle (JSON)
// like the one cosign sign-blob --bundle writes.
func signBundle(t *testing.T, vs *ca.VirtualSigstore, identity string, artifact []byte) []byte {
	t.Helper()
	e, err := vs.Sign(identity, oidcIssuer, artifact)
	if err != nil {
		t.Fatal(err)
	}
	vc, _ := e.VerificationContent()
	cert := vc.(*bundle.Certificate).Certificate()
	sc, _ := e.SignatureContent()
	ms := sc.(*bundle.MessageSignature)
	entries, _ := e.TlogEntries()
	tle := proto.Clone(entries[0].TransparencyLogEntry()).(*protorekor.TransparencyLogEntry)
	logID, err := vs.RekorLogID()
	if err != nil {
		t.Fatal(err)
	}
	set, err := vs.RekorSignPayload(tlog.RekorPayload{
		Body: base64.StdEncoding.EncodeToString(tle.CanonicalizedBody), IntegratedTime: tle.IntegratedTime,
		LogIndex: tle.LogIndex, LogID: logID})
	if err != nil {
		t.Fatal(err)
	}
	tle.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: set}
	tle.KindVersion = &protorekor.KindVersion{Kind: "hashedrekord", Version: "0.0.1"}
	b := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle+json;version=0.1",
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_X509CertificateChain{X509CertificateChain: &protocommon.X509CertificateChain{
				Certificates: []*protocommon.X509Certificate{{RawBytes: cert.Raw}}}},
			TlogEntries: []*protorekor.TransparencyLogEntry{tle},
		},
		Content: &protobundle.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{Algorithm: protocommon.HashAlgorithm_SHA2_256, Digest: ms.Digest()},
			Signature:     ms.Signature(),
		}},
	}
	out, err := protojson.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSignerIdentity(t *testing.T) {
	want := "https://github.com/solinode/homecloud/.github/workflows/release.yml@refs/tags/v0.5.0"
	if got := signerIdentity("v0.5.0"); got != want {
		t.Fatalf("signerIdentity = %q, want %q", got, want)
	}
}

func TestVerifyChecksumsSignature(t *testing.T) {
	vs := testSigstore(t)
	sums := []byte("abc123  homecloud-linux-amd64.tar.gz\n")
	good := signBundle(t, vs, signerIdentity("v0.5.0"), sums)

	if err := verifyChecksumsSignature("v0.5.0", sums, good); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	cases := []struct {
		name   string
		tag    string
		sums   []byte
		bundle []byte
		want   string
	}{
		{"tampered checksums", "v0.5.0", []byte("evil000  homecloud-linux-amd64.tar.gz\n"), good, "does not verify"},
		{"signature of another tag", "v0.5.1", sums, good, "does not verify"},
		{"another workflow", "v0.5.0", sums, signBundle(t, vs, "https://github.com/solinode/homecloud/.github/workflows/ci.yml@refs/tags/v0.5.0", sums), "does not verify"},
		{"a fork", "v0.5.0", sums, signBundle(t, vs, "https://github.com/someone/homecloud/.github/workflows/release.yml@refs/tags/v0.5.0", sums), "does not verify"},
		{"not a bundle", "v0.5.0", sums, []byte(`{"mediaType":"nope"}`), "read checksums.txt.sigstore.json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verifyChecksumsSignature(c.tag, c.sums, c.bundle)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}

	// A signature from a CA the verifier doesn't trust.
	other, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksumsSignature("v0.5.0", sums, signBundle(t, other, signerIdentity("v0.5.0"), sums)); err == nil {
		t.Fatal("signature from an untrusted CA accepted")
	}
}
