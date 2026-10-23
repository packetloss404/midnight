package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// CosignExecVerifier is the agent-side SignatureVerifier that fork-execs
// the pinned cosign binary to verify an image's Sigstore signature.
//
// Per `agile/handoff/governance/COSIGN_SDK_BOUNDARY.md` the Go SDK import
// (`github.com/sigstore/cosign/v2/...`) is binding-deferred until v1.0.0;
// alpha + beta agents fork-exec instead. This verifier is the
// implementation of that decision. The Go SDK swap is a v1 follow-up;
// the parity test in `cosign_parity_test.go` enforces equivalence at
// swap time so the v1 PR is a clean replacement of this struct.
//
// The verifier never panics, never reads stdin from the child, always
// runs under a context.WithTimeout budget, and always classifies the
// outcome through one of the panel-locked failurecodes.Code values:
//
//   - nil — image carries a valid signature against the configured policy.
//   - IMAGE_SIGNATURE_VERIFY_BINARY_MISSING — cosign binary not at
//     BinaryPath (or empty BinaryPath).
//   - IMAGE_SIGNATURE_VERIFY_TIMEOUT — exec exceeded VerifyTimeout.
//   - IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED — Rekor / Fulcio
//     transparency-log failure (split out from IMAGE_SIGNATURE_INVALID
//     for SIEM routing).
//   - IMAGE_SIGNATURE_REQUIRED — cosign reported "no matching signatures".
//   - IMAGE_SIGNATURE_INVALID — any other non-zero exit (signature
//     verifies against a non-trusted identity, Rekor unreachable, etc.).
type CosignExecVerifier struct {
	// BinaryPath is the absolute path to the pinned cosign binary.
	// When empty, every Verify call returns
	// IMAGE_SIGNATURE_VERIFY_BINARY_MISSING. The path is validated
	// lazily so tests can swap the binary between calls.
	BinaryPath string
	// PublicKeyPath, when non-empty, switches verification to
	// `cosign verify --key <path>` (key-pair mode). Mutually exclusive
	// with the keyless flags below — when both are set, the public key
	// wins (matches cosign's CLI semantics).
	PublicKeyPath string
	// CertificateIdentityRegexp, when non-empty, is passed as
	// --certificate-identity-regexp for Sigstore keyless verification.
	CertificateIdentityRegexp string
	// CertificateOIDCIssuer, when non-empty, is passed as
	// --certificate-oidc-issuer for Sigstore keyless verification.
	CertificateOIDCIssuer string
	// FulcioRoot, when non-empty, is passed as --certificate-chain
	// (the Fulcio root certificate file). Useful for private-PKI
	// deployments; ignored in the public-good Sigstore flow.
	FulcioRoot string
	// VerifyTimeout caps the exec wall-clock time per Verify call.
	// Zero falls back to DefaultCosignVerifyTimeout.
	VerifyTimeout time.Duration
	// Now is a clock seam for tests. nil falls back to time.Now.
	Now func() time.Time
	// runner is a test seam for the cosign exec. nil falls back to
	// runCosign which uses os/exec under context.WithTimeout. Tests
	// stub this to inject canned outcomes without writing a binary on
	// disk.
	runner cosignRunner
}

// DefaultCosignVerifyTimeout is the default per-call cosign verify
// budget when CosignExecVerifier.VerifyTimeout is zero. Operators
// override via the agent-config flag cosign_verify_timeout_seconds.
const DefaultCosignVerifyTimeout = 30 * time.Second

// cosignRunner is the tiny exec abstraction that lets unit tests inject
// canned outcomes without writing a binary on disk. Production wiring
// uses the real runCosign which fork-execs the pinned cosign binary.
type cosignRunner func(ctx context.Context, binaryPath string, args []string) (stdout []byte, stderr []byte, err error)

// Verify implements SignatureVerifier.
//
// The contract: imageRef is the canonical pull reference (registry/repo:
// tag@sha256:...). Verify returns nil on success or a *Error wrapping
// one of the panel-locked failurecodes.Code values listed in the type
// doc above. The error never carries the raw cosign stderr verbatim —
// stderr is included only as a Detail field and is treated as
// operator-facing diagnostic, not as a programmatic signal.
func (v CosignExecVerifier) Verify(ctx context.Context, imageRef string) error {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return newError(failurecodes.ContainerSpecInvalid, "image reference is required for signature verification", "")
	}
	if strings.TrimSpace(v.BinaryPath) == "" {
		return newError(
			failurecodes.ImageSignatureVerifyBinaryMissing,
			"cosign binary path is empty",
			"set cosign_binary_path in the agent config or disable verify_signatures",
		)
	}
	if _, statErr := os.Stat(v.BinaryPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return newError(
				failurecodes.ImageSignatureVerifyBinaryMissing,
				fmt.Sprintf("cosign binary not found at %q", v.BinaryPath),
				statErr.Error(),
			)
		}
		return newError(
			failurecodes.ImageSignatureVerifyBinaryMissing,
			fmt.Sprintf("cosign binary at %q is not accessible", v.BinaryPath),
			statErr.Error(),
		)
	}
	timeout := v.VerifyTimeout
	if timeout <= 0 {
		timeout = DefaultCosignVerifyTimeout
	}

	args := v.buildArgs(imageRef)
	runner := v.runner
	if runner == nil {
		runner = runCosign
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, err := runner(execCtx, v.BinaryPath, args)
	if err == nil {
		return nil
	}

	// Context-deadline classifications come first — both the parent
	// ctx being cancelled and our own timeout collapse to TIMEOUT so
	// the caller sees one canonical code.
	if errors.Is(err, context.DeadlineExceeded) || (execCtx.Err() != nil && errors.Is(execCtx.Err(), context.DeadlineExceeded)) {
		return newError(
			failurecodes.ImageSignatureVerifyTimeout,
			fmt.Sprintf("cosign verify exceeded %s budget", timeout),
			truncate(string(stderr), 512),
		)
	}
	if errors.Is(err, context.Canceled) {
		// Caller cancelled — surface as a wrapped error rather than a
		// fresh code; the cancellation is not a signature failure.
		return fmt.Errorf("cosign verify cancelled: %w", err)
	}

	combined := strings.ToLower(string(stdout) + "\n" + string(stderr))
	switch {
	case isTransparencyFailure(combined):
		return newError(
			failurecodes.ImageSignatureVerifyTransparencyFailed,
			fmt.Sprintf("cosign reported a Sigstore transparency-log failure for %q", imageRef),
			truncate(string(stderr), 512),
		)
	case isNoMatchingSignatures(combined):
		return newError(
			failurecodes.ImageSignatureRequired,
			fmt.Sprintf("cosign found no matching signatures for %q", imageRef),
			truncate(string(stderr), 512),
		)
	default:
		return newError(
			failurecodes.ImageSignatureInvalid,
			fmt.Sprintf("cosign verify rejected %q", imageRef),
			truncate(string(stderr), 512),
		)
	}
}

// buildArgs returns the canonical `cosign verify` argument vector for
// the configured policy. This is captured in
// `cosign_parity_test.go::CanonicalCosignArgs` so the v1 SDK swap can
// assert the SDK call carries the same effective inputs.
func (v CosignExecVerifier) buildArgs(imageRef string) []string {
	args := []string{"verify"}
	if strings.TrimSpace(v.PublicKeyPath) != "" {
		args = append(args, "--key", v.PublicKeyPath)
	} else {
		// Sigstore keyless flags are appended only when set; cosign
		// requires at least one of them in keyless mode but defaults
		// to a sensible identity-regex match if both are blank, so we
		// don't synthesize them here. Operators who need a stricter
		// identity pin set both at agent-config time.
		if strings.TrimSpace(v.CertificateIdentityRegexp) != "" {
			args = append(args, "--certificate-identity-regexp", v.CertificateIdentityRegexp)
		}
		if strings.TrimSpace(v.CertificateOIDCIssuer) != "" {
			args = append(args, "--certificate-oidc-issuer", v.CertificateOIDCIssuer)
		}
		if strings.TrimSpace(v.FulcioRoot) != "" {
			args = append(args, "--certificate-chain", v.FulcioRoot)
		}
	}
	args = append(args, imageRef)
	return args
}

// runCosign is the production cosignRunner. It fork-execs the cosign
// binary under the supplied context, captures stdout + stderr, and
// returns the exec error verbatim so the caller can branch on the
// (typed) context.DeadlineExceeded vs the generic *exec.ExitError.
func runCosign(ctx context.Context, binaryPath string, args []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		// Surface the context error so the caller's classification on
		// context.DeadlineExceeded works regardless of the exec
		// platform's native cancellation shape.
		return stdout.Bytes(), stderr.Bytes(), ctx.Err()
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

// isTransparencyFailure reports whether the combined cosign output
// indicates a Rekor / Fulcio transparency-log failure rather than a
// generic signature-mismatch outcome. Cosign's stderr phrasing is
// stable enough across v2.x patch releases to drive a substring match;
// the v1 SDK swap will replace these checks with typed errors.
func isTransparencyFailure(loweredCombined string) bool {
	for _, marker := range []string{
		"rekor",
		"transparency log",
		"transparency-log",
		"ct log",
		"ctlog",
		"certificate identity",
		"fulcio",
	} {
		if strings.Contains(loweredCombined, marker) {
			return true
		}
	}
	return false
}

// isNoMatchingSignatures reports whether cosign's stderr indicates
// "no matching signatures" — distinct from "signature was present
// but failed to verify".
func isNoMatchingSignatures(loweredCombined string) bool {
	for _, marker := range []string{
		"no matching signatures",
		"no signatures found",
		"no signatures associated",
		"signature not found",
	} {
		if strings.Contains(loweredCombined, marker) {
			return true
		}
	}
	return false
}

// truncate caps a diagnostic string at n bytes so verbose cosign stderr
// does not blow out the returned *Error.Detail field. Truncation is
// byte-safe; since the consumer only reads it as a diagnostic the
// occasional split-rune is acceptable.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
