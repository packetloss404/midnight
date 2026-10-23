package router

import (
	"strings"
	"testing"
)

func TestSanitizeRouteGroupFilename(t *testing.T) {
	tests := map[string]string{
		"Tenant A / Primary": "tenant-a-primary",
		"edge.api_v1":        "edge.api_v1",
		"  Prod:Blue  ":      "prod-blue",
	}

	for input, want := range tests {
		got, err := SanitizeRouteGroupFilename(input)
		if err != nil {
			t.Fatalf("SanitizeRouteGroupFilename(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("SanitizeRouteGroupFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSanitizeRouteGroupFilenameRejectsEmptyResult(t *testing.T) {
	if _, err := SanitizeRouteGroupFilename(" ../ "); err == nil {
		t.Fatal("expected empty result error")
	}
}

func TestSanitizeRouteFilenameDisambiguatesCollidingStems(t *testing.T) {
	a, err := SanitizeRouteFilename("svc a")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename(%q) returned error: %v", "svc a", err)
	}
	b, err := SanitizeRouteFilename("svc-a")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename(%q) returned error: %v", "svc-a", err)
	}

	// Both sanitize to the same "svc-a" stem; the hash suffix must keep them
	// from clobbering each other on disk.
	if a == b {
		t.Fatalf("SanitizeRouteFilename collided for %q and %q: both = %q", "svc a", "svc-a", a)
	}
	for input, got := range map[string]string{"svc a": a, "svc-a": b} {
		if !strings.HasPrefix(got, "svc-a-") {
			t.Fatalf("SanitizeRouteFilename(%q) = %q, want svc-a-<hash>", input, got)
		}
	}
}

func TestSanitizeRouteFilenameIsDeterministic(t *testing.T) {
	first, err := SanitizeRouteFilename("Tenant A / Primary")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename returned error: %v", err)
	}
	second, err := SanitizeRouteFilename("Tenant A / Primary")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename returned error: %v", err)
	}
	if first != second {
		t.Fatalf("SanitizeRouteFilename not deterministic: %q vs %q", first, second)
	}
}
