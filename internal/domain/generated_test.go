package domain

import (
	"strings"
	"testing"
)

func TestGeneratedHostnameUsesOneWildcardCoveredLabel(t *testing.T) {
	got, err := GeneratedHostname("Web.API--Production", "A1B2C3", "Apps.Example.COM.")
	if err != nil {
		t.Fatalf("GeneratedHostname: %v", err)
	}
	if got != "web-api-production-a1b2c3.apps.example.com" {
		t.Fatalf("hostname = %q", got)
	}
	left := strings.TrimSuffix(got, ".apps.example.com")
	if strings.Contains(left, ".") || len(left) > MaxDNSLabelOctets {
		t.Fatalf("generated prefix %q is not one bounded label", left)
	}
	if !IsSingleLabelBelow(got, "apps.example.com") {
		t.Fatalf("%q should be one label below apps domain", got)
	}
	if IsSingleLabelBelow("web.production.apps.example.com", "apps.example.com") {
		t.Fatal("multi-label generated hostname accepted")
	}
}

func TestNormalizeHostnameCanonicalizesIDNAAndRejectsRuleInput(t *testing.T) {
	got, err := NormalizeHostname("BÜCHER.Example.")
	if err != nil {
		t.Fatalf("NormalizeHostname: %v", err)
	}
	if got != "xn--bcher-kva.example" {
		t.Fatalf("normalized = %q", got)
	}
	for _, invalid := range []string{"example", "*.example.com", "host:443", "bad..example.com", "host`.example.com", "127.0.0.1"} {
		if _, err := NormalizeHostname(invalid); err == nil {
			t.Fatalf("NormalizeHostname(%q) accepted", invalid)
		}
	}
}
