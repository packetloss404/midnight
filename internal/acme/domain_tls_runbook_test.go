package acme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDomainTLSFailureRunbookCoversGeneratedDomainDNS01Triage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "runbooks", "domain-tls-failure.md"))
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	runbook := string(data)

	for _, want := range []string{
		"generated apps-domain hostname",
		"_acme-challenge.<apps-domain>",
		"CAA permission for `letsencrypt.org`",
		"curl --resolve",
		"WILDCARD_RENEWAL_FAILED",
	} {
		if !strings.Contains(runbook, want) {
			t.Fatalf("runbook missing %q", want)
		}
	}
}
