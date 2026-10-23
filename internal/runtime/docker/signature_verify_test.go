package docker

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestStaticSignatureVerifierAllowsKnownImage(t *testing.T) {
	t.Parallel()

	v := StaticSignatureVerifier{Signed: map[string]struct{}{
		"registry.example.com/midnight-server:v0.1.0": {},
	}}
	if err := v.Verify(context.Background(), "registry.example.com/midnight-server:v0.1.0"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestStaticSignatureVerifierRefusesUnknownImage(t *testing.T) {
	t.Parallel()

	v := StaticSignatureVerifier{Signed: map[string]struct{}{
		"good-image:v1": {},
	}}
	err := v.Verify(context.Background(), "unsigned-image:v1")
	if err == nil {
		t.Fatal("expected error for unsigned image")
	}
	dockerErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if dockerErr.Code != failurecodes.ImageSignatureRequired {
		t.Fatalf("Code = %s, want IMAGE_SIGNATURE_REQUIRED", dockerErr.Code)
	}
}

func TestAnySignatureVerifierComposesLocalDigestsWithExactOfficialImage(t *testing.T) {
	t.Parallel()
	const official = "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20"
	v := AnySignatureVerifier{Verifiers: []SignatureVerifier{
		LocalRegistryDigestVerifier{Endpoint: "registry:5000"},
		StaticSignatureVerifier{Signed: map[string]struct{}{official: {}}},
	}}
	local := "registry:5000/project/api@sha256:" + strings.Repeat("a", 64)
	for _, image := range []string{local, official} {
		if err := v.Verify(context.Background(), image); err != nil {
			t.Fatalf("Verify(%q): %v", image, err)
		}
	}
	for _, image := range []string{
		"registry:5000/project/api:latest",
		"postgres:16.14",
		"docker.io/library/redis@sha256:" + strings.Repeat("b", 64),
		"postgres:16.14@sha256:" + strings.Repeat("c", 64),
	} {
		if err := v.Verify(context.Background(), image); err == nil {
			t.Fatalf("Verify(%q) succeeded outside composed trust policy", image)
		} else if CodeOf(err) != failurecodes.ImageSignatureRequired {
			t.Fatalf("Verify(%q) code=%s, want %s", image, CodeOf(err), failurecodes.ImageSignatureRequired)
		}
	}
}

func TestPullVerifyUsesComposedTrustPolicyBeforeDockerPull(t *testing.T) {
	ctx := context.Background()
	adapter, engine := newTestAdapter(t)
	const official = "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20"
	local := "registry:5000/project/api@sha256:" + strings.Repeat("a", 64)
	adapter.SetSignatureVerifier(AnySignatureVerifier{Verifiers: []SignatureVerifier{
		LocalRegistryDigestVerifier{Endpoint: "registry:5000"},
		StaticSignatureVerifier{Signed: map[string]struct{}{official: {}}},
	}})
	for _, image := range []string{official, local} {
		result, err := adapter.PullVerify(ctx, image)
		if err != nil || !result.Verified || !result.Trusted {
			t.Fatalf("PullVerify(%q)=%+v, %v", image, result, err)
		}
	}
	rejected := "docker.io/library/redis@sha256:" + strings.Repeat("b", 64)
	if _, err := adapter.PullVerify(ctx, rejected); err == nil {
		t.Fatalf("PullVerify(%q) unexpectedly succeeded", rejected)
	}
	if !reflect.DeepEqual(engine.pulled, []string{official, local}) {
		t.Fatalf("pulled=%v, want only accepted images", engine.pulled)
	}
}

func TestAdapterDoesNotEnforceWhenNoVerifierSet(t *testing.T) {
	t.Parallel()

	a := &Adapter{}
	if err := a.VerifyImageSignature(context.Background(), "any-image:v1"); err != nil {
		t.Fatalf("VerifyImageSignature with nil verifier should not error, got %v", err)
	}
}

func TestAdapterEnforcesWhenVerifierSet(t *testing.T) {
	t.Parallel()

	a := &Adapter{}
	a.EnableSignatureEnforcement(StaticSignatureVerifier{Signed: map[string]struct{}{"good:v1": {}}})
	if err := a.VerifyImageSignature(context.Background(), "good:v1"); err != nil {
		t.Fatalf("VerifyImageSignature(good): %v", err)
	}
	err := a.VerifyImageSignature(context.Background(), "bad:v1")
	if err == nil {
		t.Fatal("expected error for unsigned image")
	}
}

func TestPullVerifyRequiresTrustPolicyBeforePull(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)

	result, err := a.PullVerify(ctx, "unsigned:v1")
	if err == nil {
		t.Fatal("expected missing trust policy to fail")
	}
	if CodeOf(err) != failurecodes.ImageSignatureRequired {
		t.Fatalf("CodeOf(err) = %s, want IMAGE_SIGNATURE_REQUIRED", CodeOf(err))
	}
	if result.Verified || result.Trusted {
		t.Fatalf("result = %#v, want unverified and untrusted", result)
	}
	if len(fake.pulled) != 0 {
		t.Fatalf("ImagePull called without trust policy: %#v", fake.pulled)
	}
}

func TestPullVerifyRefusesUnsignedImageBeforePull(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	a.SetSignatureVerifier(StaticSignatureVerifier{Signed: map[string]struct{}{"good:v1": {}}})

	result, err := a.PullVerify(ctx, "bad:v1")
	if err == nil {
		t.Fatal("expected unsigned candidate to fail")
	}
	if CodeOf(err) != failurecodes.ImageSignatureRequired {
		t.Fatalf("CodeOf(err) = %s, want IMAGE_SIGNATURE_REQUIRED", CodeOf(err))
	}
	if result.Verified || result.Trusted {
		t.Fatalf("result = %#v, want unverified and untrusted", result)
	}
	if len(fake.pulled) != 0 {
		t.Fatalf("ImagePull called for unsigned candidate: %#v", fake.pulled)
	}
}

func TestPullVerifyPullsSignedImageWhenVerifierSet(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	a.SetSignatureVerifier(StaticSignatureVerifier{Signed: map[string]struct{}{"good:v1": {}}})

	if !a.Capabilities().SignatureEnforce {
		t.Fatal("SignatureEnforce = false, want true when verifier is configured")
	}
	result, err := a.PullVerify(ctx, " good:v1 ")
	if err != nil {
		t.Fatalf("PullVerify signed candidate: %v", err)
	}
	if !result.Verified || !result.Trusted {
		t.Fatalf("result = %#v, want verified and trusted", result)
	}
	if len(fake.pulled) != 1 || fake.pulled[0] != "good:v1" {
		t.Fatalf("pulled = %#v, want trimmed signed image", fake.pulled)
	}
}
