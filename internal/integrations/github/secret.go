// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// redactedValue is the placeholder rendered in place of any secret value.
const redactedValue = "<redacted>"

// Secret wraps a sensitive string (installation tokens, private keys, minted
// JWTs) so it cannot leak through the usual accidental channels: %v/%s/%+v
// formatting, json.Marshal, or a struct dump in a log line. The real value is
// only reachable through the explicit Value() accessor, so every read site is
// grep-able and intentional.
//
// This closes the leak where InstallationToken.Token was a bare string with a
// live `json:"token"` tag — a single `fmt.Sprintf("%v", tok)` or
// `json.Marshal(tok)` used to spill the live token into logs/errors.
type Secret struct {
	value string
}

// NewSecret wraps value. A zero-length value yields an IsZero() Secret.
func NewSecret(value string) Secret {
	return Secret{value: value}
}

// Value returns the underlying secret. This is the ONLY way to read the real
// value; call it only where the token is actually used (clone/fetch/status).
func (s Secret) Value() string {
	return s.value
}

// IsZero reports whether the Secret carries no value.
func (s Secret) IsZero() bool {
	return s.value == ""
}

// String renders the redacted placeholder (or "" when empty) so %s and callers
// treating the Secret as a Stringer never see the real value.
func (s Secret) String() string {
	if s.value == "" {
		return ""
	}
	return redactedValue
}

// GoString redacts under the %#v verb.
func (s Secret) GoString() string {
	return s.String()
}

// Format redacts under every fmt verb (%v, %s, %+v, %q, ...).
func (s Secret) Format(state fmt.State, verb rune) {
	_, _ = state.Write([]byte(s.String()))
}

// MarshalJSON redacts so a Secret embedded in any struct marshals to the
// placeholder (or null when empty) instead of the live value.
func (s Secret) MarshalJSON() ([]byte, error) {
	if s.value == "" {
		return []byte("null"), nil
	}
	return json.Marshal(redactedValue)
}

// redactor removes one or more secret substrings from arbitrary text (e.g. a
// GitHub API response body) before it reaches an error or a log line. Longer
// secrets are replaced first so a shorter secret that is a substring of a
// longer one does not leave a partial value behind.
type redactor struct {
	values []string
}

// newRedactor builds a redactor from the supplied secret values, ignoring
// blanks and duplicates.
func newRedactor(values ...string) redactor {
	seen := make(map[string]struct{}, len(values))
	r := redactor{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		r.values = append(r.values, value)
	}
	sort.Slice(r.values, func(left, right int) bool {
		return len(r.values[left]) > len(r.values[right])
	})
	return r
}

// redact replaces every known secret value in text with the placeholder.
func (r redactor) redact(text string) string {
	if text == "" {
		return ""
	}
	redacted := text
	for _, secret := range r.values {
		redacted = strings.ReplaceAll(redacted, secret, redactedValue)
	}
	return redacted
}

// scrubSecret is a convenience for the common single-token case: it strips the
// token from text before it is embedded in an error or log line.
func scrubSecret(text string, secrets ...Secret) string {
	values := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if !s.IsZero() {
			values = append(values, s.Value())
		}
	}
	if len(values) == 0 {
		return text
	}
	return newRedactor(values...).redact(text)
}
