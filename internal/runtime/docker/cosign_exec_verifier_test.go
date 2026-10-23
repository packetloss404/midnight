package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// TestCosignExecVerifier covers the panel-locked outcome classes for
// CosignExecVerifier without requiring a real cosign binary on PATH.
// The default `go test ./...` lane MUST satisfy this contract per
// agile/handoff/governance/COSIGN_SDK_BOUNDARY.md.
//
// The "binary path" stub is a tiny on-disk file (we just need the
// existence check to pass). The exec result is injected through the
// internal cosignRunner seam so we never spawn a real child process
// at test time.
func TestCosignExecVerifier(t *testing.T) {
	t.Parallel()

	stubBinary := touchStubBinary(t)

	cases := []struct {
		name     string
		runner   cosignRunner
		wantCode failurecodes.Code
	}{
		{
			name: "success",
			runner: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return []byte("Verified OK\n"), nil, nil
			},
			wantCode: "",
		},
		{
			name: "no_signatures_found",
			runner: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, []byte("Error: no matching signatures\n"), errors.New("exit status 1")
			},
			wantCode: failurecodes.ImageSignatureRequired,
		},
		{
			name: "transparency_log_failure",
			runner: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, []byte("Error: signature integrated time outside Rekor inclusion window\n"), errors.New("exit status 1")
			},
			wantCode: failurecodes.ImageSignatureVerifyTransparencyFailed,
		},
		{
			name: "transparency_certificate_identity_failure",
			runner: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, []byte("Error: certificate identity does not match\n"), errors.New("exit status 1")
			},
			wantCode: failurecodes.ImageSignatureVerifyTransparencyFailed,
		},
		{
			name: "generic_signature_invalid",
			runner: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, []byte("Error: signature could not be verified against the supplied public key\n"), errors.New("exit status 1")
			},
			wantCode: failurecodes.ImageSignatureInvalid,
		},
		{
			name: "context_deadline_exceeded",
			runner: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
				return nil, nil, context.DeadlineExceeded
			},
			wantCode: failurecodes.ImageSignatureVerifyTimeout,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := CosignExecVerifier{
				BinaryPath:    stubBinary,
				VerifyTimeout: time.Second,
				runner:        tc.runner,
			}
			err := v.Verify(context.Background(), "registry.example.com/img@sha256:deadbeef")
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				return
			}
			dockerErr, ok := err.(*Error)
			if !ok {
				t.Fatalf("error type = %T, want *Error; err = %v", err, err)
			}
			if dockerErr.Code != tc.wantCode {
				t.Fatalf("Code = %s, want %s", dockerErr.Code, tc.wantCode)
			}
		})
	}
}

func TestCosignExecVerifierBinaryMissingEmptyPath(t *testing.T) {
	t.Parallel()

	v := CosignExecVerifier{}
	err := v.Verify(context.Background(), "registry.example.com/img:v1")
	dockerErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error; err = %v", err, err)
	}
	if dockerErr.Code != failurecodes.ImageSignatureVerifyBinaryMissing {
		t.Fatalf("Code = %s, want IMAGE_SIGNATURE_VERIFY_BINARY_MISSING", dockerErr.Code)
	}
}

func TestCosignExecVerifierBinaryMissingNonExistent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	v := CosignExecVerifier{BinaryPath: filepath.Join(dir, "nonexistent-cosign")}
	err := v.Verify(context.Background(), "registry.example.com/img:v1")
	dockerErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if dockerErr.Code != failurecodes.ImageSignatureVerifyBinaryMissing {
		t.Fatalf("Code = %s, want IMAGE_SIGNATURE_VERIFY_BINARY_MISSING", dockerErr.Code)
	}
}

func TestCosignExecVerifierEmptyImageRefRejected(t *testing.T) {
	t.Parallel()

	stubBinary := touchStubBinary(t)
	v := CosignExecVerifier{BinaryPath: stubBinary}
	err := v.Verify(context.Background(), "   ")
	dockerErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if dockerErr.Code != failurecodes.ContainerSpecInvalid {
		t.Fatalf("Code = %s, want CONTAINER_SPEC_INVALID", dockerErr.Code)
	}
}

func TestCosignExecVerifierBuildArgsKeyMode(t *testing.T) {
	t.Parallel()

	v := CosignExecVerifier{
		BinaryPath:                "stub",
		PublicKeyPath:             "/etc/midnight/cosign.pub",
		CertificateIdentityRegexp: "ignored-when-key-set",
	}
	args := v.buildArgs("registry.example.com/img:v1")
	want := []string{"verify", "--key", "/etc/midnight/cosign.pub", "registry.example.com/img:v1"}
	if !equalArgs(args, want) {
		t.Fatalf("buildArgs = %v, want %v", args, want)
	}
}

func TestCosignExecVerifierBuildArgsKeylessMode(t *testing.T) {
	t.Parallel()

	v := CosignExecVerifier{
		BinaryPath:                "stub",
		CertificateIdentityRegexp: "^https://github.com/midnight/midnight/",
		CertificateOIDCIssuer:     "https://token.actions.githubusercontent.com",
	}
	args := v.buildArgs("registry.example.com/img:v1")
	want := []string{
		"verify",
		"--certificate-identity-regexp", "^https://github.com/midnight/midnight/",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"registry.example.com/img:v1",
	}
	if !equalArgs(args, want) {
		t.Fatalf("buildArgs = %v, want %v", args, want)
	}
}

func TestCosignExecVerifierBuildArgsKeylessWithFulcioRoot(t *testing.T) {
	t.Parallel()

	v := CosignExecVerifier{
		BinaryPath:                "stub",
		CertificateIdentityRegexp: "^https://github.com/midnight/midnight/",
		CertificateOIDCIssuer:     "https://token.actions.githubusercontent.com",
		FulcioRoot:                "/etc/midnight/fulcio-root.pem",
	}
	args := v.buildArgs("registry.example.com/img:v1")
	want := []string{
		"verify",
		"--certificate-identity-regexp", "^https://github.com/midnight/midnight/",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--certificate-chain", "/etc/midnight/fulcio-root.pem",
		"registry.example.com/img:v1",
	}
	if !equalArgs(args, want) {
		t.Fatalf("buildArgs = %v, want %v", args, want)
	}
}

func TestAdapterSetSignatureVerifierClearsAndReinjects(t *testing.T) {
	t.Parallel()

	a := &Adapter{}
	if a.SignatureVerifier() != nil {
		t.Fatal("SignatureVerifier() must be nil on a fresh adapter")
	}
	a.SetSignatureVerifier(StaticSignatureVerifier{Signed: map[string]struct{}{"img:v1": {}}})
	if a.SignatureVerifier() == nil {
		t.Fatal("SignatureVerifier() must be non-nil after SetSignatureVerifier")
	}
	a.SetSignatureVerifier(nil)
	if a.SignatureVerifier() != nil {
		t.Fatal("SetSignatureVerifier(nil) must clear the verifier")
	}
}

func TestAdapterSetSignatureVerifierInjectsCosignExecVerifier(t *testing.T) {
	t.Parallel()

	stubBinary := touchStubBinary(t)
	a := &Adapter{}
	v := CosignExecVerifier{
		BinaryPath:    stubBinary,
		VerifyTimeout: time.Second,
		runner: func(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
			return []byte("Verified OK\n"), nil, nil
		},
	}
	a.SetSignatureVerifier(v)
	if err := a.VerifyImageSignature(context.Background(), "registry.example.com/img:v1"); err != nil {
		t.Fatalf("VerifyImageSignature: %v", err)
	}
}

// touchStubBinary creates a tiny on-disk file that os.Stat resolves to
// existence. We never exec it because the runner seam in the test is
// stubbed; this satisfies CosignExecVerifier.Verify's existence check
// only. The file is removed automatically via t.TempDir cleanup.
func touchStubBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "cosign-stub"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("# fake cosign stub; never exec'd\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
