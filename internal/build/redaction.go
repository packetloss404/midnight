// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"regexp"
	"sort"
	"strings"
)

// redactedValue is the placeholder substituted for any secret material before
// it is persisted or logged. Shared across the build package (redaction of
// build output and git checkout stderr).
const redactedValue = "<redacted>"

var (
	// sensitiveAssignmentRE matches `NAME=value` pairs whose NAME looks like a
	// credential (TOKEN, SECRET, PASSWORD, ...). The value (group 2) is
	// replaced wholesale so a secret echoed by a build step never lands in a
	// persisted event. The value matcher accepts a single/double-quoted string
	// (so a value containing spaces is redacted in full) or an unquoted run.
	sensitiveAssignmentRE = regexp.MustCompile(`(?i)\b([A-Z0-9_.+-]*(?:TOKEN|SECRET|PASSWORD|PASSWD|PASS|API_KEY|APIKEY|ACCESS_KEY|PRIVATE_KEY|CREDENTIAL)[A-Z0-9_.+-]*=)("[^"]*"|'[^']*'|[^ \t\r\n]+)`)

	// buildArgFlagRE matches build-arg values in both the `--build-arg NAME=value`
	// (Docker/buildx) form and the `--opt build-arg:NAME=value` form this repo's
	// buildctl invocation actually emits. Build-arg values are treated as
	// potentially sensitive regardless of the arg name.
	buildArgFlagRE = regexp.MustCompile(`(?i)(--build-arg[ =]|--opt[ =]build-arg:)([A-Za-z_][A-Za-z0-9_.+-]*=)("[^"]*"|'[^']*'|[^ \t\r\n]+)`)

	// secretFlagRE matches `--secret <spec>` (Docker/buildx) and the
	// `--opt secret:<spec>` form buildctl uses, so the env=/src= components of a
	// BuildKit secret mount are redacted while the id= key is preserved.
	secretFlagRE = regexp.MustCompile(`(?i)(--secret[ =]|--opt[ =]secret:)([^ \t\r\n]+)`)
)

// Redactor scrubs secret material out of free-form build output before it is
// persisted. It combines two strategies:
//
//   - known-value replacement: exact secret strings supplied at construction
//     are replaced longest-first (so a value that is a substring of another
//     does not partially unmask it), and
//   - pattern redaction: credential-shaped assignments and --build-arg /
//     --secret flag values are redacted even when the raw secret value is not
//     known to the caller.
//
// The zero value is usable and applies pattern redaction only.
type Redactor struct {
	values []string
}

// NewRedactor builds a Redactor over the supplied secret values. Empty and
// duplicate values are dropped; leading/trailing whitespace is preserved
// because it can be part of a real secret. Values are sorted longest-first.
func NewRedactor(values []string) Redactor {
	seen := make(map[string]struct{}, len(values))
	redactor := Redactor{}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		redactor.values = append(redactor.values, value)
	}
	sort.Slice(redactor.values, func(left, right int) bool {
		return len(redactor.values[left]) > len(redactor.values[right])
	})
	return redactor
}

// RedactText returns value with all known secret values and credential-shaped
// tokens replaced by the redaction placeholder.
func (r Redactor) RedactText(value string) string {
	if value == "" {
		return ""
	}
	redacted := value
	for _, secret := range r.values {
		redacted = strings.ReplaceAll(redacted, secret, redactedValue)
	}
	redacted = buildArgFlagRE.ReplaceAllString(redacted, "${1}${2}"+redactedValue)
	redacted = secretFlagRE.ReplaceAllStringFunc(redacted, redactSecretFlagMatch)
	redacted = sensitiveAssignmentRE.ReplaceAllString(redacted, "${1}"+redactedValue)
	return redacted
}

// redactSecretFlagMatch redacts the env= and src= components of a `--secret`
// flag specification while preserving the id= key so the log still identifies
// which secret mount was involved.
func redactSecretFlagMatch(match string) string {
	sub := secretFlagRE.FindStringSubmatch(match)
	if sub == nil {
		return match
	}
	prefix, spec := sub[1], sub[2]
	parts := strings.Split(spec, ",")
	for index, part := range parts {
		key, _, ok := strings.Cut(part, "=")
		if ok && (key == "env" || key == "src") {
			parts[index] = key + "=" + redactedValue
		}
	}
	return prefix + strings.Join(parts, ",")
}
