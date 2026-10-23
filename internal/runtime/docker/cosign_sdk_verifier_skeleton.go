//go:build cosign_sdk

package docker

import (
	"context"
	"errors"
)

// CosignSDKVerifier is the v1 follow-up implementation that will swap
// the fork-exec posture for an in-process Sigstore verification path
// using `github.com/sigstore/sigstore/pkg/signature` and the Rekor v1
// client. Today this file is a build-tag-gated SKELETON that compiles
// to a "not implemented" stub; the v1 PR fills in the body.
//
// This file deliberately does NOT import any sigstore module so the
// default `go test ./...` lane and the `cosign_sdk`-tagged compile
// stay supply-chain neutral until the boundary doc is flipped:
// agile/handoff/governance/COSIGN_SDK_BOUNDARY.md.
//
// When the v1 swap happens, the parity test (cosign_parity_test.go)
// will pin the canonical input/output of this verifier to match
// CosignExecVerifier so the swap is a verified-equivalent replacement.
type CosignSDKVerifier struct {
	// PublicKeyPath / CertificateIdentityRegexp / CertificateOIDCIssuer
	// / FulcioRoot mirror CosignExecVerifier so the parity test can
	// drive both implementations from the same canonicalParityCases
	// table without re-mapping flags.
	PublicKeyPath             string
	CertificateIdentityRegexp string
	CertificateOIDCIssuer     string
	FulcioRoot                string
}

// Verify is the v1 SDK entry point. The skeleton returns a sentinel
// error that the parity test recognizes as "SDK side not yet wired";
// the v1 PR replaces this body with the real Sigstore verification.
func (CosignSDKVerifier) Verify(ctx context.Context, imageRef string) error {
	return errCosignSDKNotImplemented
}

// errCosignSDKNotImplemented is the sentinel returned by the SDK
// skeleton until the v1 PR lands. The parity test inspects this to
// SkipNow on the SDK side without reporting a false failure.
var errCosignSDKNotImplemented = errors.New("cosign SDK verifier: not implemented (skeleton; flipped on at v1 per COSIGN_SDK_BOUNDARY.md)")

// IsCosignSDKNotImplemented reports whether the supplied error is the
// "SDK side not yet wired" sentinel. The parity test uses it to skip
// SDK assertions; production callers will never see this code path
// because they wire CosignExecVerifier.
func IsCosignSDKNotImplemented(err error) bool {
	return errors.Is(err, errCosignSDKNotImplemented)
}
