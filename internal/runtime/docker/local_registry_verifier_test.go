package docker

import (
	"context"
	"testing"
)

func TestLocalRegistryDigestVerifierAcceptsOnlyImmutableLocalImages(t *testing.T) {
	verifier := LocalRegistryDigestVerifier{Endpoint: "http://127.0.0.1:5000/"}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := verifier.Verify(context.Background(), "127.0.0.1:5000/project/service@sha256:"+digest); err != nil {
		t.Fatalf("Verify(local digest): %v", err)
	}

	for _, image := range []string{
		"127.0.0.1:5000/project/service:latest",
		"registry.example.com/project/service@sha256:" + digest,
		"127.0.0.1:5000/project/service@sha256:short",
		"127.0.0.1:5000/project/service@sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if err := verifier.Verify(context.Background(), image); err == nil {
			t.Fatalf("Verify(%q) succeeded, want fail-closed", image)
		}
	}
}
