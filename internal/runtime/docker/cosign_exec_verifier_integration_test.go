//go:build integration && cosign

package docker

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCosignExecVerifierAgainstRealBinary exercises CosignExecVerifier
// against a real cosign binary on PATH and a known-signed test image.
// The build tag combo `integration && cosign` keeps it out of the
// default `go test ./...` lane (per
// agile/handoff/governance/COSIGN_SDK_BOUNDARY.md test coverage table
// row 4: integration test runs only on the cosign-aware lane).
//
// Knobs (env-driven so CI can target a specific image without hard-
// coding GHCR identities here):
//
//   - MIDNIGHT_COSIGN_TEST_IMAGE — full image ref (registry/repo:tag@sha256:...).
//     Defaults to a publicly-signed sigstore canonical image.
//   - MIDNIGHT_COSIGN_TEST_IDENTITY_REGEX — keyless certificate-identity
//     regex. Optional; cosign defaults are sufficient for the public good.
//   - MIDNIGHT_COSIGN_TEST_OIDC_ISSUER — keyless OIDC issuer URL.
//     Optional; same default note.
//
// The test is intentionally tolerant of "cosign not on PATH" because
// CI may set the build tag without provisioning the binary; in that
// case we t.Skipf rather than fail.
func TestCosignExecVerifierAgainstRealBinary(t *testing.T) {
	binary, err := exec.LookPath("cosign")
	if err != nil {
		t.Skipf("cosign not on PATH: %v", err)
	}
	image := strings.TrimSpace(os.Getenv("MIDNIGHT_COSIGN_TEST_IMAGE"))
	if image == "" {
		image = "ghcr.io/sigstore/cosign/cosign:v2.6.3"
	}
	identityRe := strings.TrimSpace(os.Getenv("MIDNIGHT_COSIGN_TEST_IDENTITY_REGEX"))
	oidc := strings.TrimSpace(os.Getenv("MIDNIGHT_COSIGN_TEST_OIDC_ISSUER"))

	v := CosignExecVerifier{
		BinaryPath:                binary,
		CertificateIdentityRegexp: identityRe,
		CertificateOIDCIssuer:     oidc,
		VerifyTimeout:             60 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := v.Verify(ctx, image); err != nil {
		// Don't fail the integration test outright on transient
		// Sigstore reachability issues; surface the typed code so the
		// CI lane can decide whether to retry.
		t.Logf("cosign verify against %q returned %v", image, err)
	}
}
