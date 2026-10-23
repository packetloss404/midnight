package supportbundle

import (
	"strings"
	"testing"
)

// goldenVector exercises one redaction rule with a representative input and expected output.
type goldenVector struct {
	name    string
	rule    RedactionRule
	in      string
	out     string
	mustHit bool
}

func TestRedactTextGoldenVectors(t *testing.T) {
	vectors := []goldenVector{
		{name: "midnight token", rule: RedactionRuleMidnightToken, in: "auth=midn_ABCDEF1234567890", out: "auth=" + RedactionPlaceholder, mustHit: true},
		{name: "email", rule: RedactionRuleEmail, in: "operator=ops@midnight.example", out: "operator=" + RedactionPlaceholder, mustHit: true},
		{name: "ipv4", rule: RedactionRuleIPv4, in: "remote 10.0.0.42 connected", out: "remote " + RedactionPlaceholder + " connected", mustHit: true},
		{name: "ipv6", rule: RedactionRuleIPv6, in: "remote 2001:0db8:85a3:0000:0000:8a2e:0370:7334 connected", out: "remote " + RedactionPlaceholder + " connected", mustHit: true},
		{name: "aws access key", rule: RedactionRuleAWSAccessKey, in: "id=AKIAIOSFODNN7EXAMPLE", out: "id=" + RedactionPlaceholder, mustHit: true},
		{name: "aws secret", rule: RedactionRuleAWSSecret, in: "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", out: "AWS_SECRET_ACCESS_KEY=" + RedactionPlaceholder, mustHit: true},
		{name: "github token classic", rule: RedactionRuleGitHubToken, in: "token=ghp_abcdefghijklmnopqrstuvwxyz0123456789", out: "token=" + RedactionPlaceholder, mustHit: true},
		{name: "github token oauth", rule: RedactionRuleGitHubToken, in: "token=gho_abcdefghijklmnop0123456789", out: "token=" + RedactionPlaceholder, mustHit: true},
		{name: "pem private key", rule: RedactionRulePEMPrivateKey, in: "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAxyz\n-----END RSA PRIVATE KEY-----", out: RedactionPlaceholder, mustHit: true},
		{name: "openssh private key", rule: RedactionRulePEMPrivateKey, in: "-----BEGIN OPENSSH PRIVATE KEY-----\nMIIEowIBAAKCAQEAxyz\n-----END OPENSSH PRIVATE KEY-----", out: RedactionPlaceholder, mustHit: true},
		{name: "authorization header", rule: RedactionRuleAuthorization, in: "Authorization: Bearer some-token-value", out: "Authorization: " + RedactionPlaceholder, mustHit: true},
		{name: "url credential", rule: RedactionRuleURLCredential, in: "postgres://user:hunter2@db.local/midnight", out: "postgres://" + RedactionPlaceholder + "@db.local/midnight", mustHit: true},
	}

	for _, tt := range vectors {
		t.Run(tt.name, func(t *testing.T) {
			result := RedactText("logs/sample.log", tt.in)
			if result.Text != tt.out {
				t.Fatalf("RedactText() text = %q, want %q", result.Text, tt.out)
			}
			if tt.mustHit {
				found := false
				for _, finding := range result.Findings {
					if finding.Rule == string(tt.rule) {
						found = true
						if finding.Count < 1 {
							t.Fatalf("RedactText() finding count = %d, want >= 1", finding.Count)
						}
						if finding.Path != "logs/sample.log" {
							t.Fatalf("RedactText() finding path = %q, want logs/sample.log", finding.Path)
						}
					}
				}
				if !found {
					t.Fatalf("RedactText() findings = %+v, want rule %q", result.Findings, tt.rule)
				}
			}
		})
	}
}

func TestRedactTextLeavesCleanInputUntouched(t *testing.T) {
	clean := "deploy succeeded id=run-001 status=ready"
	result := RedactText("logs/clean.log", clean)
	if result.Text != clean {
		t.Fatalf("RedactText() text = %q, want %q", result.Text, clean)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("RedactText() findings = %+v, want none", result.Findings)
	}
}

func TestRedactTextHandlesMultipleRulesInOnePayload(t *testing.T) {
	in := "operator=ops@midnight.example sent midn_TOKENVALUE12345 from 10.0.0.5"
	result := RedactText("logs/api.log", in)
	if strings.Contains(result.Text, "ops@midnight.example") || strings.Contains(result.Text, "midn_TOKENVALUE12345") || strings.Contains(result.Text, "10.0.0.5") {
		t.Fatalf("RedactText() text = %q, want all secrets removed", result.Text)
	}
	if len(result.Findings) < 3 {
		t.Fatalf("RedactText() findings = %+v, want at least three rule hits", result.Findings)
	}
}

func TestContainsLikelySecretMatchesKnownShapes(t *testing.T) {
	cases := []string{
		"midn_ABCDEFGH12345678",
		"ops@midnight.example",
		"10.0.0.1",
		"AKIAIOSFODNN7EXAMPLE",
		"-----BEGIN PRIVATE KEY-----\nbase64\n-----END PRIVATE KEY-----",
		"https://user:pw@host.example/path",
	}
	for _, in := range cases {
		if !ContainsLikelySecret(in) {
			t.Fatalf("ContainsLikelySecret(%q) = false, want true", in)
		}
	}
}

func TestContainsLikelySecretRejectsCleanText(t *testing.T) {
	if ContainsLikelySecret("phase 0 deploy succeeded") {
		t.Fatal("ContainsLikelySecret() returned true for clean text")
	}
}

func TestRedactionRulesAreOrderedAndStable(t *testing.T) {
	rules := RedactionRules()
	if len(rules) < 5 {
		t.Fatalf("RedactionRules() = %v, want at least five rules", rules)
	}
	if rules[0] != RedactionRulePEMPrivateKey {
		t.Fatalf("RedactionRules() first = %q, want pem-private-key first", rules[0])
	}
}

func TestRedactTextNoLeakage(t *testing.T) {
	// Release-gate parity: every fixture vector must scrub plaintext entirely.
	vectors := []string{
		"midn_secret_TOKEN12345",
		"ops@midnight.example",
		"10.10.10.10",
		"2001:db8:0:0:0:0:0:1",
		"AKIAEXAMPLEEXAMPLE12",
		"AWS_SECRET_ACCESS_KEY=" + strings.Repeat("a", 40),
		"ghp_" + strings.Repeat("z", 36),
		"-----BEGIN PRIVATE KEY-----\nbase64data\n-----END PRIVATE KEY-----",
		"Authorization: Bearer leak-me",
		"https://user:pw@db.local/run",
	}
	for _, plaintext := range vectors {
		result := RedactText("logs/leak.log", plaintext)
		// Strip RedactionPlaceholder before checking that the original substring is gone.
		stripped := strings.ReplaceAll(result.Text, RedactionPlaceholder, "")
		// pull out values that were intended to be redacted
		if strings.Contains(stripped, plaintext) {
			t.Fatalf("RedactText() leaked %q in %q", plaintext, result.Text)
		}
	}
}
