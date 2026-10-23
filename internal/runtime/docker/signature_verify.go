package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime"
)

// SignatureVerifier is the seam the agent wires when verify_signatures
// is set to true on its config. The interface is intentionally narrow:
// agents can swap the implementation between StaticSignatureVerifier
// (default-test posture, stdlib-only) and CosignExecVerifier (alpha +
// beta production posture, fork-execs the pinned cosign binary).
//
// Per `agile/handoff/governance/COSIGN_SDK_BOUNDARY.md` the Go SDK
// import (`github.com/sigstore/cosign/v2/...`) is binding-deferred
// until v1.0.0. The v1 follow-up adds a CosignSDKVerifier behind a
// `cosign_sdk` build tag; the parity test in
// `cosign_parity_test.go` enforces equivalence so the swap is a clean
// in-place replacement of CosignExecVerifier.
//
// Implementations return:
//
//   - nil — image carries a valid signature against the configured
//     trust policy.
//   - IMAGE_SIGNATURE_INVALID — signature present but did not verify
//     against any trusted public key.
//   - IMAGE_SIGNATURE_REQUIRED — image carries no signature at all
//     and verify_signatures is true.
//   - IMAGE_SIGNATURE_VERIFY_BINARY_MISSING — exec verifier could not
//     locate its cosign binary.
//   - IMAGE_SIGNATURE_VERIFY_TIMEOUT — exec verifier exceeded its
//     per-call timeout budget.
//   - IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED — Sigstore Rekor /
//     Fulcio transparency-log failure (split out from
//     IMAGE_SIGNATURE_INVALID for SIEM routing).
type SignatureVerifier interface {
	Verify(ctx context.Context, imageRef string) error
}

// AnySignatureVerifier accepts an image when at least one complete trust
// policy accepts it. It is used by the stock single-node worker to compose
// immutable local-registry trust with a tiny explicit allowlist of official
// digest-pinned images. It never turns a rejection into a fallback pull.
type AnySignatureVerifier struct {
	Verifiers []SignatureVerifier
}

func (v AnySignatureVerifier) Verify(ctx context.Context, imageRef string) error {
	if len(v.Verifiers) == 0 {
		return &Error{Code: failurecodes.ImageSignatureRequired, Message: "no image trust policy is configured"}
	}
	var rejected error
	for _, verifier := range v.Verifiers {
		if verifier == nil {
			continue
		}
		if err := verifier.Verify(ctx, imageRef); err == nil {
			return nil
		} else {
			rejected = errors.Join(rejected, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if rejected == nil {
		return &Error{Code: failurecodes.ImageSignatureRequired, Message: "no image trust policy is configured"}
	}
	return fmt.Errorf("image rejected by every configured trust policy: %w", rejected)
}

// StaticSignatureVerifier is a deterministic verifier driven by an
// in-memory allowlist of "image references that are signed". It is
// the Phase 1 default until the cosign dep is plumbed.
type StaticSignatureVerifier struct {
	// Signed is the set of full image references (registry/repo:
	// tag@sha256:...) that the verifier should report as signed.
	Signed map[string]struct{}
}

// LocalRegistryDigestVerifier trusts immutable images produced into
// Midnight's operator-controlled local registry. It does not make a signature
// claim: it is the explicit single-node trust policy used when the build
// worker and registry share the host. Tagged images and images from any other
// registry remain fail-closed.
type LocalRegistryDigestVerifier struct {
	Endpoint string
}

// Verify implements SignatureVerifier.
func (v LocalRegistryDigestVerifier) Verify(_ context.Context, imageRef string) error {
	endpoint := normalizeRegistryEndpoint(v.Endpoint)
	imageRef = strings.TrimSpace(imageRef)
	if endpoint == "" {
		return &Error{Code: failurecodes.ImageSignatureRequired, Message: "local registry trust policy has no endpoint"}
	}
	prefix := endpoint + "/"
	if !strings.HasPrefix(imageRef, prefix) {
		return &Error{Code: failurecodes.ImageSignatureRequired, Message: fmt.Sprintf("image %q is outside the trusted local registry", imageRef)}
	}
	digestIndex := strings.LastIndex(imageRef, "@sha256:")
	if digestIndex < len(prefix) {
		return &Error{Code: failurecodes.ImageSignatureRequired, Message: "trusted local registry images must use an immutable sha256 digest"}
	}
	digest := imageRef[digestIndex+len("@sha256:"):]
	if len(digest) != 64 {
		return &Error{Code: failurecodes.ImageSignatureRequired, Message: "trusted local registry image has an invalid sha256 digest"}
	}
	for _, char := range digest {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return &Error{Code: failurecodes.ImageSignatureRequired, Message: "trusted local registry image has an invalid sha256 digest"}
		}
	}
	return nil
}

func normalizeRegistryEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return strings.TrimRight(endpoint, "/")
}

// Verify implements SignatureVerifier.
func (v StaticSignatureVerifier) Verify(_ context.Context, imageRef string) error {
	if v.Signed == nil {
		return &Error{Code: failurecodes.ImageSignatureRequired, Message: "no trust policy configured: every pull is unsigned"}
	}
	if _, ok := v.Signed[imageRef]; !ok {
		return &Error{Code: failurecodes.ImageSignatureRequired, Message: fmt.Sprintf("image %q is not in the static trust allowlist", imageRef)}
	}
	return nil
}

// EnableSignatureEnforcement wires verifier into the adapter so a
// future PullVerify() call refuses to pull when the supplied image
// fails the verifier check. Phase 1 routes this through a small
// helper to keep the surface area tiny — Lane 2's existing
// PullVerify shape is preserved; we add the verifier as an optional
// sidecar via this exported method.
//
// The agent calls EnableSignatureEnforcement(verifier) at boot when
// `verify_signatures: true` is set in the agent config; if not
// called, PullVerify fails closed before ImagePull with
// IMAGE_SIGNATURE_REQUIRED.
//
// EnableSignatureEnforcement preserves the existing opt-in shape; new
// callers should prefer SetSignatureVerifier which accepts nil to
// clear the verifier (used by integration-test teardown).
func (a *Adapter) EnableSignatureEnforcement(verifier SignatureVerifier) {
	if verifier == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.verifier = verifier
}

// SetSignatureVerifier injects (or clears) the SignatureVerifier on the
// adapter. Unlike EnableSignatureEnforcement, passing nil clears the
// verifier so test harnesses can run with-and-without enforcement on
// the same Adapter instance. Concrete callers:
//
//   - cmd/midnight-agent at boot, with a CosignExecVerifier configured
//     from agent_config.cosign_binary_path / cosign_verify_timeout_seconds /
//     verify_signatures.
//   - tests, with StaticSignatureVerifier or a runner-stub
//     CosignExecVerifier (see cosign_exec_verifier_test.go).
//
// SetSignatureVerifier is the panel-aligned injection seam called out
// in agile/handoff/governance/COSIGN_SDK_BOUNDARY.md; the v1 SDK swap
// replaces the injected concrete type without changing this signature.
func (a *Adapter) SetSignatureVerifier(verifier SignatureVerifier) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.verifier = verifier
}

// SignatureVerifier returns the currently installed verifier (nil if
// none). Useful for tests that want to assert injection took effect
// and for the agent's reproducible-cosign-version assertion at boot.
func (a *Adapter) SignatureVerifier() SignatureVerifier {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.verifier
}

// VerifyImageSignature exposes the configured verifier to callers
// that want to gate before calling Schedule. Returns the typed
// failurecodes.Code as a *docker.Error so the agent can map it
// directly to the wire envelope.
func (a *Adapter) VerifyImageSignature(ctx context.Context, image string) error {
	a.mu.Lock()
	verifier := a.verifier
	a.mu.Unlock()
	if verifier == nil {
		return nil
	}
	if err := verifier.Verify(ctx, strings.TrimSpace(image)); err != nil {
		return err
	}
	return nil
}

// PullVerifyWithEnforcement is retained for callers that adopted the strict
// helper before PullVerify itself learned to honor the injected verifier.
// It now delegates to PullVerify so verification runs exactly once.
func (a *Adapter) PullVerifyWithEnforcement(ctx context.Context, image string) (runtime.PullVerifyResult, error) {
	return a.PullVerify(ctx, image)
}
