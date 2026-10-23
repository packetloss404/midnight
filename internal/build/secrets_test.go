package build

import (
	"strings"
	"testing"
)

func TestScanBuildOutputSecretsReturnsRedactedFindings(t *testing.T) {
	output := "step 1 ok\nleaked midn_abc123 and AKIAZZZZ\nslack xoxb-token\n"

	findings := ScanBuildOutputSecrets(output)
	if len(findings) != 3 {
		t.Fatalf("len(findings) = %d, want 3: %#v", len(findings), findings)
	}

	wantPrefixes := []string{"midn_", "AKIA", "xoxb-"}
	for i, want := range wantPrefixes {
		if findings[i].Prefix != want {
			t.Fatalf("finding[%d].Prefix = %q, want %q", i, findings[i].Prefix, want)
		}
		if !strings.Contains(findings[i].Redacted, "<redacted>") {
			t.Fatalf("finding[%d].Redacted = %q, want redacted value", i, findings[i].Redacted)
		}
	}

	for _, leaked := range []string{"abc123", "ZZZZ", "token"} {
		for _, finding := range findings {
			if strings.Contains(finding.Redacted, leaked) {
				t.Fatalf("Redacted finding %q leaked %q", finding.Redacted, leaked)
			}
		}
	}
}

func TestScanBuildOutputSecretsEmptyWhenNoPrefixes(t *testing.T) {
	if findings := ScanBuildOutputSecrets("ordinary build output"); len(findings) != 0 {
		t.Fatalf("findings = %#v, want none", findings)
	}
}
