// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const leakTokenLiteral = "ghs_supersecrettoken1234567890"

func TestSecretRedactsFormatting(t *testing.T) {
	s := NewSecret(leakTokenLiteral)

	// Every fmt verb must render the placeholder, never the live value.
	for _, verb := range []string{"%v", "%s", "%+v", "%#v", "%q"} {
		got := fmt.Sprintf(verb, s)
		if strings.Contains(got, leakTokenLiteral) {
			t.Fatalf("verb %s leaked token: %q", verb, got)
		}
		if !strings.Contains(got, redactedValue) {
			t.Fatalf("verb %s did not redact: %q", verb, got)
		}
	}

	// Value() is the only path to the live token.
	if s.Value() != leakTokenLiteral {
		t.Fatalf("Value() = %q", s.Value())
	}
	if s.IsZero() {
		t.Fatalf("non-empty secret reported IsZero")
	}
	if !NewSecret("").IsZero() {
		t.Fatalf("empty secret not IsZero")
	}
}

func TestSecretRedactsJSONMarshal(t *testing.T) {
	s := NewSecret(leakTokenLiteral)
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), leakTokenLiteral) {
		t.Fatalf("marshal leaked token: %s", out)
	}

	// Empty secret marshals to null.
	out, err = json.Marshal(NewSecret(""))
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(out) != "null" {
		t.Fatalf("empty marshal = %s", out)
	}
}

func TestInstallationTokenNeverLeaks(t *testing.T) {
	tok := InstallationToken{Token: NewSecret(leakTokenLiteral), ExpiresAt: time.Unix(0, 0).UTC()}

	// The historical leak vectors: %v and json.Marshal on the whole struct.
	if got := fmt.Sprintf("%v", tok); strings.Contains(got, leakTokenLiteral) {
		t.Fatalf("%%v leaked token: %q", got)
	}
	if got := fmt.Sprintf("%+v", tok); strings.Contains(got, leakTokenLiteral) {
		t.Fatalf("%%+v leaked token: %q", got)
	}
	out, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), leakTokenLiteral) {
		t.Fatalf("marshal leaked token: %s", out)
	}
}

func TestScrubSecretRemovesTokenFromText(t *testing.T) {
	tok := NewSecret(leakTokenLiteral)
	body := `{"message":"bad ` + leakTokenLiteral + ` credential"}`
	scrubbed := scrubSecret(body, tok)
	if strings.Contains(scrubbed, leakTokenLiteral) {
		t.Fatalf("scrub left token behind: %q", scrubbed)
	}
	if !strings.Contains(scrubbed, redactedValue) {
		t.Fatalf("scrub did not insert placeholder: %q", scrubbed)
	}
	// Blank secret is a no-op.
	if got := scrubSecret(body, NewSecret("")); got != body {
		t.Fatalf("blank scrub changed text: %q", got)
	}
}
