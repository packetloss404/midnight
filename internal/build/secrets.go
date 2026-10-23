package build

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// BuildOutputSecretFinding describes a single secret-shaped match in build
// output. The matched bytes themselves are never included; only a redacted
// fingerprint and the column/line where the match started.
type BuildOutputSecretFinding struct {
	Prefix   string `json:"prefix"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Redacted string `json:"redacted"`
}

var buildOutputSecretPrefixes = []string{"midn_", "AKIA", "xoxb-", "xoxp-", "xoxa-", "ghp_", "ghs_", "gho_", "ghu_", "ghr_"}

var (
	awsAccessKeyRE = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	githubTokenRE  = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,255}`)
	slackTokenRE   = regexp.MustCompile(`xox[bpao]-[A-Za-z0-9-]{10,}`)
	jwtRE          = regexp.MustCompile(`eyJ[A-Za-z0-9_=-]+\.eyJ[A-Za-z0-9_=-]+\.[A-Za-z0-9_=.+/-]+`)
	pemBlockRE     = regexp.MustCompile(`-----BEGIN [A-Z ]+PRIVATE KEY-----`)
)

// EntropySecretScanOptions controls the high-entropy substring detector. A
// zero MinTokenLength disables the detector. MinShannonEntropy defaults to
// 4.5 bits per byte when unset; that catches typical 32-byte hex-or-base64
// secrets without flagging normal English prose.
type EntropySecretScanOptions struct {
	MinTokenLength    int
	MinShannonEntropy float64
}

// DefaultEntropySecretScanOptions returns the Phase 1 defaults: 32-character
// minimum length and 4.5 bits per byte minimum entropy.
func DefaultEntropySecretScanOptions() EntropySecretScanOptions {
	return EntropySecretScanOptions{MinTokenLength: 32, MinShannonEntropy: 4.5}
}

// ScanBuildOutputSecrets is the legacy prefix-only scanner. It remains for
// callers that have not yet adopted the richer Phase 1 pattern set.
func ScanBuildOutputSecrets(output string) []BuildOutputSecretFinding {
	findings := []BuildOutputSecretFinding{}
	line := 1
	column := 1

	for i := 0; i < len(output); i++ {
		for _, prefix := range buildOutputSecretPrefixes {
			if strings.HasPrefix(output[i:], prefix) {
				findings = append(findings, BuildOutputSecretFinding{
					Prefix:   prefix,
					Line:     line,
					Column:   column,
					Redacted: prefix + "<redacted>",
				})
				break
			}
		}

		if output[i] == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}

	return findings
}

// ScanBuildOutputSecretsExtended is the Phase 1 scanner. It runs the prefix
// scanner first, then the explicit pattern regexes, then the high-entropy
// detector. It deduplicates findings at the same line/column/prefix to keep
// reports terse.
func ScanBuildOutputSecretsExtended(output string, opts EntropySecretScanOptions) []BuildOutputSecretFinding {
	findings := ScanBuildOutputSecrets(output)
	findings = appendRegexFindings(findings, output, awsAccessKeyRE, "AKIA")
	findings = appendRegexFindings(findings, output, githubTokenRE, "ghX_")
	findings = appendRegexFindings(findings, output, slackTokenRE, "xox?-")
	findings = appendRegexFindings(findings, output, jwtRE, "jwt:")
	findings = appendRegexFindings(findings, output, pemBlockRE, "pem:")
	if opts.MinTokenLength > 0 {
		minEntropy := opts.MinShannonEntropy
		if minEntropy <= 0 {
			minEntropy = 4.5
		}
		findings = append(findings, scanHighEntropyTokens(output, opts.MinTokenLength, minEntropy)...)
	}
	return dedupeFindings(findings)
}

func appendRegexFindings(out []BuildOutputSecretFinding, output string, re *regexp.Regexp, label string) []BuildOutputSecretFinding {
	for _, match := range re.FindAllStringIndex(output, -1) {
		line, column := lineColumn(output, match[0])
		out = append(out, BuildOutputSecretFinding{
			Prefix:   label,
			Line:     line,
			Column:   column,
			Redacted: label + "<redacted>",
		})
	}
	return out
}

func scanHighEntropyTokens(output string, minLen int, minEntropy float64) []BuildOutputSecretFinding {
	out := []BuildOutputSecretFinding{}
	tokenStart := -1
	emit := func(start, end int) {
		if end-start < minLen {
			return
		}
		token := output[start:end]
		if !looksLikeSecretToken(token) {
			return
		}
		if shannonEntropy(token) < minEntropy {
			return
		}
		line, column := lineColumn(output, start)
		out = append(out, BuildOutputSecretFinding{
			Prefix:   "entropy",
			Line:     line,
			Column:   column,
			Redacted: "entropy<redacted>",
		})
	}
	for i := 0; i < len(output); i++ {
		if isSecretTokenChar(rune(output[i])) {
			if tokenStart < 0 {
				tokenStart = i
			}
			continue
		}
		if tokenStart >= 0 {
			emit(tokenStart, i)
			tokenStart = -1
		}
	}
	if tokenStart >= 0 {
		emit(tokenStart, len(output))
	}
	return out
}

func isSecretTokenChar(r rune) bool {
	switch r {
	case '_', '-', '+', '/', '=', '.':
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func looksLikeSecretToken(token string) bool {
	hasLower := false
	hasUpper := false
	hasDigit := false
	for _, r := range token {
		switch {
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	score := 0
	if hasLower {
		score++
	}
	if hasUpper {
		score++
	}
	if hasDigit {
		score++
	}
	return score >= 2
}

func shannonEntropy(token string) float64 {
	if len(token) == 0 {
		return 0
	}
	counts := make(map[rune]int)
	for _, r := range token {
		counts[r]++
	}
	var entropy float64
	length := float64(len(token))
	for _, count := range counts {
		p := float64(count) / length
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func lineColumn(output string, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	line := 1
	column := 1
	for i := 0; i < offset && i < len(output); i++ {
		if output[i] == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return line, column
}

func dedupeFindings(findings []BuildOutputSecretFinding) []BuildOutputSecretFinding {
	if len(findings) <= 1 {
		return findings
	}
	type key struct {
		line   int
		column int
		prefix string
	}
	seen := make(map[key]struct{}, len(findings))
	out := findings[:0]
	for _, finding := range findings {
		k := key{line: finding.Line, column: finding.Column, prefix: finding.Prefix}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, finding)
	}
	return out
}
