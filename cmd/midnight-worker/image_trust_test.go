package main

import (
	"context"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/runtime/docker"
	templatepkg "github.com/midnight/midnight/internal/templates"
)

// The four official database templates ship Docker Hub images pinned by exact
// digest. A stock install sets MIDNIGHT_WORKER_TRUST_LOCAL_REGISTRY and no
// allowlist, which used to reject `midnight template deploy postgres` with
// IMAGE_SIGNATURE_REQUIRED ("outside the trusted local registry"). The digests
// are compiled into this binary, so they are trusted by construction.
func TestDefaultVerifierAcceptsEveryOfficialTemplateImage(t *testing.T) {
	verifier := signatureVerifierFromConfig(workerConfig{
		LocalRegistry:      "registry:5000",
		TrustLocalRegistry: true,
	})
	if verifier == nil {
		t.Fatal("verifier = nil, want the composed local-registry + official-image policy")
	}
	references := templatepkg.OfficialImageReferences()
	if len(references) == 0 {
		t.Fatal("no official image references are exported")
	}
	for _, reference := range references {
		if err := verifier.Verify(context.Background(), reference); err != nil {
			t.Fatalf("Verify(%q) = %v, want the shipped digest to be trusted with no operator allowlist", reference, err)
		}
	}
	// The local-registry policy still works alongside it.
	if err := verifier.Verify(context.Background(), "registry:5000/project/api@sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatalf("Verify(local registry digest) = %v", err)
	}
}

func TestDefaultVerifierStillRejectsUnshippedAndUnpinnedImages(t *testing.T) {
	verifier := signatureVerifierFromConfig(workerConfig{
		LocalRegistry:      "registry:5000",
		TrustLocalRegistry: true,
	})
	for _, image := range []string{
		"postgres:16.14",
		"postgres:16.14@sha256:" + strings.Repeat("b", 64),
		"registry.example.com/other:v1",
		"registry:5000/project/api:latest",
	} {
		if err := verifier.Verify(context.Background(), image); err == nil {
			t.Fatalf("Verify(%q) = nil, want rejection", image)
		}
	}
}

// An instance that never asked for image trust must keep running unverified
// pulls; composing the official policy there would fail-close every deploy.
func TestNoTrustPolicyStaysDisabled(t *testing.T) {
	if verifier := signatureVerifierFromConfig(workerConfig{}); verifier != nil {
		t.Fatalf("verifier = %T, want nil when no trust policy is configured", verifier)
	}
}

// Cosign remains the sole policy when signature verification is enabled.
func TestCosignVerificationIsUnchanged(t *testing.T) {
	verifier := signatureVerifierFromConfig(workerConfig{
		VerifySignatures:   true,
		TrustLocalRegistry: true,
		TrustedImages:      []string{"registry.example/app@sha256:" + strings.Repeat("c", 64)},
	})
	if _, ok := verifier.(docker.CosignExecVerifier); !ok {
		t.Fatalf("verifier = %T, want docker.CosignExecVerifier", verifier)
	}
}

func TestImageTrustPolicySummaryExplainsEveryTrustedSource(t *testing.T) {
	summary := strings.Join(imageTrustPolicySummary(workerConfig{
		LocalRegistry:      "registry:5000",
		TrustLocalRegistry: true,
		TrustedImages:      []string{"registry.example/app@sha256:" + strings.Repeat("c", 64)},
	}), "\n")

	for _, want := range []string{
		"MIDNIGHT_WORKER_TRUST_LOCAL_REGISTRY", // not-a-failure-code
		"MIDNIGHT_WORKER_TRUSTED_IMAGES",       // not-a-failure-code
		"registry.example/app@sha256:",
		"compiled into this release",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary is missing %q:\n%s", want, summary)
		}
	}
	for _, image := range templatepkg.OfficialImages() {
		if !strings.Contains(summary, image.Reference) {
			t.Fatalf("summary omits shipped image %q:\n%s", image.Reference, summary)
		}
		for _, source := range image.Sources {
			if !strings.Contains(summary, source) {
				t.Fatalf("summary omits the template %q that ships %q", source, image.Reference)
			}
		}
	}
}
