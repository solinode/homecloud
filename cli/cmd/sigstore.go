package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// Release signatures. The release workflow signs checksums.txt with Sigstore
// (cosign keyless: a short-lived certificate for the workflow's GitHub OIDC
// identity, logged in the Rekor transparency log) and publishes the result as
// checksums.txt.sigstore.json. Verifying that bundle against the workflow's
// identity proves checksums.txt, and so every archive it lists, came from this
// repository's release workflow for that tag.

const (
	signatureAsset = "checksums.txt.sigstore.json"
	oidcIssuer     = "https://token.actions.githubusercontent.com"
	// firstSignedRelease is the first release published with a signature.
	// Older ones install only with --allow-unsigned; newer ones never without one.
	firstSignedRelease = "v0.5.0"
)

// signerIdentity is the certificate identity (SAN) of the release workflow run for tag.
func signerIdentity(tag string) string {
	return "https://github.com/" + releaseRepo + "/.github/workflows/release.yml@refs/tags/" + tag
}

// trustedMaterial returns Sigstore's public-good trust roots (Fulcio CAs, Rekor
// and CT log keys, timestamp authorities), fetched and verified through
// Sigstore's TUF repository starting from the root embedded in sigstore-go,
// and cached like cosign does. Tests replace it.
var trustedMaterial = func() (root.TrustedMaterial, error) {
	opts := tuf.DefaultOptions()
	if err := os.MkdirAll(filepath.Dir(opts.CachePath), 0o700); err != nil {
		opts = opts.WithDisableLocalCache()
	}
	tr, err := root.FetchTrustedRootWithOptions(opts)
	if err != nil && !opts.DisableLocalCache {
		// An unwritable cache (e.g. another user's home): fetch without it.
		tr, err = root.FetchTrustedRootWithOptions(tuf.DefaultOptions().WithDisableLocalCache())
	}
	if err != nil {
		return nil, fmt.Errorf("fetch the Sigstore trust root: %w", err)
	}
	return tr, nil
}

// verifierOptions are what cosign verify-blob checks by default: an embedded
// certificate transparency proof, a transparency log entry and a trusted
// signing time. Tests drop the first (their test CA issues no SCTs).
var verifierOptions = []verify.VerifierOption{
	verify.WithSignedCertificateTimestamps(1),
	verify.WithTransparencyLog(1),
	verify.WithObserverTimestamps(1),
}

// loadBundle parses a Sigstore bundle (JSON).
var loadBundle = func(b []byte) (verify.SignedEntity, error) {
	var bn bundle.Bundle
	if err := bn.UnmarshalJSON(b); err != nil {
		return nil, err
	}
	return &bn, nil
}

// verifyChecksumsSignature checks that bundleJSON is a valid Sigstore
// signature over checksums by the release workflow of tag.
func verifyChecksumsSignature(tag string, checksums, bundleJSON []byte) error {
	entity, err := loadBundle(bundleJSON)
	if err != nil {
		return fmt.Errorf("read %s: %w", signatureAsset, err)
	}
	tm, err := trustedMaterial()
	if err != nil {
		return err
	}
	v, err := verify.NewVerifier(tm, verifierOptions...)
	if err != nil {
		return err
	}
	id, err := verify.NewShortCertificateIdentity(oidcIssuer, "", signerIdentity(tag), "")
	if err != nil {
		return err
	}
	if _, err := v.Verify(entity, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(checksums)), verify.WithCertificateIdentity(id))); err != nil {
		return fmt.Errorf("signature of %s for %s does not verify (expected signer %s): %w", "checksums.txt", tag, signerIdentity(tag), err)
	}
	return nil
}
