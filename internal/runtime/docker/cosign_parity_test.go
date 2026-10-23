package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

// Cosign verifier parity contract.
//
// This file is the centerpiece of the cosign SDK boundary doc
// (agile/handoff/governance/COSIGN_SDK_BOUNDARY.md): it captures the
// canonical input/output of CosignExecVerifier so the v1 SDK swap is
// verified-equivalent rather than a leap of faith.
//
// Today the SDK side is a build-tagged skeleton (see
// cosign_sdk_verifier_skeleton.go behind `cosign_sdk`) and the parity
// matrix exercises only the exec verifier. When the v1 PR lands, the
// SDK side flips on, the test runs the same input table against both
// implementations, and any drift is a hard test failure.

// canonicalParityCase is one (image, runner-outcome, expected
// failurecode) triple. The runner stub stands in for the full cosign
// behavior so the parity table is reproducible without network access.
type canonicalParityCase struct {
	name        string
	imageRef    string
	stub        cosignRunner
	wantCode    failurecodes.Code // empty means success
	wantArgsHas []string          // substrings the canonical args MUST contain
}

// canonicalParityCases is the agreed-upon canonical-input table. Add a
// row here when CosignExecVerifier classifies a new outcome class; the
// v1 SDK PR MUST satisfy the same table.
func canonicalParityCases() []canonicalParityCase {
	return []canonicalParityCase{
		{
			name:     "verified_ok",
			imageRef: "registry.example.com/midnight-server:v0.1.0@sha256:" + sha("server"),
			stub: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return []byte("Verified OK\n"), nil, nil
			},
			wantCode:    "",
			wantArgsHas: []string{"verify", "registry.example.com/midnight-server:v0.1.0@sha256:" + sha("server")},
		},
		{
			name:     "no_matching_signatures",
			imageRef: "registry.example.com/midnight-worker:v0.1.0",
			stub: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, []byte("Error: no matching signatures\n"), errors.New("exit 1")
			},
			wantCode: failurecodes.ImageSignatureRequired,
		},
		{
			name:     "transparency_log_failure",
			imageRef: "registry.example.com/midnight-agent:v0.1.0",
			stub: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, []byte("Error: rekor inclusion proof failed\n"), errors.New("exit 1")
			},
			wantCode: failurecodes.ImageSignatureVerifyTransparencyFailed,
		},
		{
			name:     "wrong_signing_key",
			imageRef: "registry.example.com/midnight-buildkit:v0.1.0",
			stub: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, []byte("Error: signature could not be verified against the supplied public key\n"), errors.New("exit 1")
			},
			wantCode: failurecodes.ImageSignatureInvalid,
		},
		{
			name:     "timeout",
			imageRef: "registry.example.com/midnight-registry:v0.1.0",
			stub: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, nil, context.DeadlineExceeded
			},
			wantCode: failurecodes.ImageSignatureVerifyTimeout,
		},
	}
}

// TestCosignParityExecSide drives the canonical input table against
// CosignExecVerifier. The SDK side is exercised by
// TestCosignParitySDKSide (cosign_sdk build tag); both tests share the
// same canonicalParityCases() table so adding a row here automatically
// expands the SDK contract.
func TestCosignParityExecSide(t *testing.T) {
	t.Parallel()

	stub := touchStubBinary(t)
	for _, tc := range canonicalParityCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			v := CosignExecVerifier{
				BinaryPath: stub,
				runner:     tc.stub,
			}
			args := v.buildArgs(tc.imageRef)
			for _, want := range tc.wantArgsHas {
				if !sliceContains(args, want) {
					t.Fatalf("args = %v missing required token %q", args, want)
				}
			}
			err := v.Verify(context.Background(), tc.imageRef)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("Verify(%s) = %v, want nil", tc.imageRef, err)
				}
				return
			}
			dockerErr, ok := err.(*Error)
			if !ok {
				t.Fatalf("error type = %T, want *Error", err)
			}
			if dockerErr.Code != tc.wantCode {
				t.Fatalf("Code = %s, want %s", dockerErr.Code, tc.wantCode)
			}
		})
	}
}

// TestCosignParityCanonicalArgsAreStable asserts the args vector
// CosignExecVerifier emits for each policy mode is byte-stable across
// patch releases. The v1 SDK swap MUST emit the same effective inputs
// (key path / certificate-identity / OIDC issuer) so the parity test
// can compare semantic outputs, not just exec exit codes.
func TestCosignParityCanonicalArgsAreStable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		v    CosignExecVerifier
		want []string
	}{
		{
			name: "key_mode_only",
			v: CosignExecVerifier{
				BinaryPath:    "stub",
				PublicKeyPath: "/etc/midnight/cosign.pub",
			},
			want: []string{"verify", "--key", "/etc/midnight/cosign.pub", "img:v1"},
		},
		{
			name: "keyless_full",
			v: CosignExecVerifier{
				BinaryPath:                "stub",
				CertificateIdentityRegexp: "^https://github.com/midnight/midnight/",
				CertificateOIDCIssuer:     "https://token.actions.githubusercontent.com",
				FulcioRoot:                "/etc/midnight/fulcio.pem",
			},
			want: []string{
				"verify",
				"--certificate-identity-regexp", "^https://github.com/midnight/midnight/",
				"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
				"--certificate-chain", "/etc/midnight/fulcio.pem",
				"img:v1",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.v.buildArgs("img:v1")
			if !equalArgs(got, tc.want) {
				t.Fatalf("buildArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// sha is a deterministic test helper that lets the parity table use
// human-readable image-ref shapes without committing fake long
// hex strings into git history. The actual hash math is irrelevant —
// the parity contract only cares that the same string flows in and
// out of the verifier.
func sha(label string) string {
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd" + label[:1]
}

func sliceContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
