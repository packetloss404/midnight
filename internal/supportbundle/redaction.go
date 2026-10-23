package supportbundle

import (
	"regexp"
	"sort"
	"strings"
)

// RedactionRule names a single regex-driven redaction pass applied to text payloads.
type RedactionRule string

const (
	// RedactionRuleMidnightToken matches Midnight platform tokens (`midn_*`).
	RedactionRuleMidnightToken RedactionRule = "midnight-token"
	// RedactionRuleEmail matches RFC-shaped email addresses.
	RedactionRuleEmail RedactionRule = "email"
	// RedactionRuleIPv4 matches IPv4 dotted-quad addresses.
	RedactionRuleIPv4 RedactionRule = "ipv4"
	// RedactionRuleIPv6 matches IPv6 colon-separated addresses (full form).
	RedactionRuleIPv6 RedactionRule = "ipv6"
	// RedactionRuleAWSAccessKey matches AWS access key IDs (`AKIA*` / `ASIA*`).
	RedactionRuleAWSAccessKey RedactionRule = "aws-access-key"
	// RedactionRuleAWSSecret matches AWS secret access key shapes scoped to assignment-style declarations.
	RedactionRuleAWSSecret RedactionRule = "aws-secret-key"
	// RedactionRuleGitHubToken matches modern GitHub personal/app/installation token shapes.
	RedactionRuleGitHubToken RedactionRule = "github-token"
	// RedactionRulePEMPrivateKey matches base64-shaped PEM private key blocks.
	RedactionRulePEMPrivateKey RedactionRule = "pem-private-key"
	// RedactionRuleAuthorization matches `Authorization` header values.
	RedactionRuleAuthorization RedactionRule = "authorization-header"
	// RedactionRuleURLCredential matches URL-embedded `user:pass@` credentials.
	RedactionRuleURLCredential RedactionRule = "url-credential"

	// RedactionPlaceholder is the deterministic replacement value written in the bundle.
	RedactionPlaceholder = "[REDACTED]"
)

// orderedRedactionRules is the deterministic order rules are applied in. Order matters:
// the PEM block matcher runs first because its content otherwise looks like base64 noise
// and subsequent narrow rules (token shapes, headers) can match safely afterwards.
var orderedRedactionRules = []redactionRulePattern{
	{rule: RedactionRulePEMPrivateKey, expr: regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)},
	{rule: RedactionRuleAuthorization, expr: regexp.MustCompile(`(?i)(authorization\s*:\s*)([^\r\n]+)`), keepGroup: 1},
	{rule: RedactionRuleURLCredential, expr: regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+\-.]*://)([^\s/:@]+):([^\s/@]+)@`), replacement: "${1}" + RedactionPlaceholder + "@"},
	{rule: RedactionRuleMidnightToken, expr: regexp.MustCompile(`midn_[A-Za-z0-9_\-]{8,}`)},
	{rule: RedactionRuleGitHubToken, expr: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`)},
	{rule: RedactionRuleAWSAccessKey, expr: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{rule: RedactionRuleAWSSecret, expr: regexp.MustCompile(`(?i)(aws[_-]?secret[_-]?(?:access[_-]?)?key\s*[:=]\s*)([A-Za-z0-9/+=]{40})`), keepGroup: 1},
	{rule: RedactionRuleEmail, expr: regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)},
	{rule: RedactionRuleIPv6, expr: regexp.MustCompile(`\b(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}\b`)},
	{rule: RedactionRuleIPv4, expr: regexp.MustCompile(`\b(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)(?:\.(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)){3}\b`)},
}

type redactionRulePattern struct {
	rule        RedactionRule
	expr        *regexp.Regexp
	keepGroup   int    // when non-zero, retain submatch[keepGroup] verbatim and replace the rest with placeholder
	replacement string // when set, used as the replacement template; defaults to RedactionPlaceholder
}

// RedactionResult holds the redacted text and per-rule counts produced by RedactText.
type RedactionResult struct {
	// Text is the redacted text.
	Text string
	// Findings records the per-rule hit count for the redaction passes.
	Findings []RedactionFinding
}

// RedactText applies the locked Phase 0 redaction rule set to text and returns redacted text plus per-rule findings.
//
// Path is recorded in the returned findings so the bundle manifest can attribute hits to a file.
// An empty path is allowed; callers that share results across files should pass the file path.
func RedactText(path string, text string) RedactionResult {
	result := RedactionResult{Text: text}
	counts := make(map[RedactionRule]int, len(orderedRedactionRules))
	for _, p := range orderedRedactionRules {
		var hits int
		result.Text = p.expr.ReplaceAllStringFunc(result.Text, func(match string) string {
			hits++
			if p.replacement != "" {
				return p.expr.ReplaceAllString(match, p.replacement)
			}
			if p.keepGroup > 0 {
				groups := p.expr.FindStringSubmatch(match)
				if len(groups) > p.keepGroup {
					return groups[p.keepGroup] + RedactionPlaceholder
				}
			}
			return RedactionPlaceholder
		})
		if hits > 0 {
			counts[p.rule] = hits
		}
	}
	rules := make([]RedactionRule, 0, len(counts))
	for rule := range counts {
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i] < rules[j] })
	for _, rule := range rules {
		result.Findings = append(result.Findings, RedactionFinding{Path: path, Rule: string(rule), Count: counts[rule]})
	}
	return result
}

// RedactionRules returns the deterministic ordered list of redaction rule names applied by RedactText.
//
// The order matches the in-engine application order so callers can render review summaries with stable ordering.
func RedactionRules() []RedactionRule {
	rules := make([]RedactionRule, 0, len(orderedRedactionRules))
	for _, p := range orderedRedactionRules {
		rules = append(rules, p.rule)
	}
	return rules
}

// ContainsLikelySecret returns true when text contains any redaction-rule match. It is used by
// release-gate fixtures to confirm that no plaintext leaks survive multi-pass redaction.
func ContainsLikelySecret(text string) bool {
	for _, p := range orderedRedactionRules {
		if p.expr.MatchString(text) {
			return true
		}
	}
	return false
}

// LooksLikeSecretValue returns true if a single value (without surrounding context) looks like
// any of the secret shapes the redactor recognizes. Useful for telemetry payload validation.
func LooksLikeSecretValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return ContainsLikelySecret(value)
}
