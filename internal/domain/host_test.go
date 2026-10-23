package domain

import (
	"strings"
	"testing"
)

func TestDNSLabelWithCollisionSuffixSanitizesSeedAndSuffix(t *testing.T) {
	label := DNSLabelWithCollisionSuffix(" My_Service/API 2026! ", "COLLIDE#02")

	if label != "my-service-api-2026-collide-02" {
		t.Fatalf("label = %q", label)
	}
	assertDNSLabel(t, label)
}

func TestDNSLabelWithCollisionSuffixCapsLabelAndPreservesSuffixBudget(t *testing.T) {
	label := DNSLabelWithCollisionSuffix(strings.Repeat("a", 100), "retry-12")

	if len(label) != MaxDNSLabelOctets {
		t.Fatalf("label length = %d, want %d: %q", len(label), MaxDNSLabelOctets, label)
	}
	if !strings.HasSuffix(label, "-retry-12") {
		t.Fatalf("label = %q, want collision suffix preserved", label)
	}
	assertDNSLabel(t, label)
}

func TestDNSLabelWithCollisionSuffixFallsBackForEmptyOrInvalidSeed(t *testing.T) {
	if got := DNSLabelWithCollisionSuffix(" ::: ", ""); got != "app" {
		t.Fatalf("label = %q, want app", got)
	}
	if got := DNSLabelWithCollisionSuffix(" ::: ", "02"); got != "app-02" {
		t.Fatalf("label = %q, want app-02", got)
	}
}

func TestDNSLabelWithCollisionSuffixHandlesOversizedSuffix(t *testing.T) {
	label := DNSLabelWithCollisionSuffix("api", strings.Repeat("z", 100))

	if len(label) != MaxDNSLabelOctets {
		t.Fatalf("label length = %d, want %d", len(label), MaxDNSLabelOctets)
	}
	if !strings.HasPrefix(label, "a-") {
		t.Fatalf("label = %q, want room for base label and suffix separator", label)
	}
	assertDNSLabel(t, label)
}

func TestDNSLabelSanitizesAndCapsSeed(t *testing.T) {
	if got := DNSLabel(" My_Service/API 2026! "); got != "my-service-api-2026" {
		t.Fatalf("DNSLabel sanitize = %q, want my-service-api-2026", got)
	}

	capped := DNSLabel(strings.Repeat("a", 100))
	if len(capped) != MaxDNSLabelOctets {
		t.Fatalf("DNSLabel cap length = %d, want %d", len(capped), MaxDNSLabelOctets)
	}
	assertDNSLabel(t, capped)

	// Non-ASCII input must not leak raw multibyte UTF-8 into the label.
	nonASCII := DNSLabel("café-über-café")
	assertDNSLabel(t, nonASCII)
	if nonASCII != "caf-ber-caf" {
		t.Fatalf("DNSLabel non-ascii = %q, want caf-ber-caf", nonASCII)
	}

	if got := DNSLabel(" ::: "); got != "app" {
		t.Fatalf("DNSLabel empty seed = %q, want app", got)
	}
}

func assertDNSLabel(t *testing.T, label string) {
	t.Helper()

	if label == "" {
		t.Fatal("label is empty")
	}
	if len(label) > MaxDNSLabelOctets {
		t.Fatalf("label length = %d, max %d", len(label), MaxDNSLabelOctets)
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		t.Fatalf("label %q must not start or end with hyphen", label)
	}
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		t.Fatalf("label %q contains non-DNS-safe rune %q", label, r)
	}
}
