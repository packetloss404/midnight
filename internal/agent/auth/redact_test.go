// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth_test

import (
	"context"
	"strings"
	"testing"

	auth "github.com/midnight/midnight/internal/agent/auth"
)

func TestSecretRedactor_PassesUnknownInputThrough(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	const in = "hello world — no secrets here"
	out := r.Redact(in)
	if out != in {
		t.Fatalf("redactor must pass unknown input through, got %q", out)
	}
}

func TestSecretRedactor_RedactsKnownPlaintext(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	r.AddPlaintexts("super_secret_db_password")
	out := r.Redact("connection string: postgres://user:super_secret_db_password@host/db")
	if strings.Contains(out, "super_secret_db_password") {
		t.Fatalf("plaintext leaked in redaction output: %s", out)
	}
	if !strings.Contains(out, auth.RedactedPlaceholder) {
		t.Fatalf("redaction placeholder missing: %s", out)
	}
}

// TestSecretRedactor_LongestMatchWins protects against the
// substring-overlap pitfall described in redact.go.
func TestSecretRedactor_LongestMatchWins(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	// "secretvalue" contains "secret" as a substring. Adding both
	// must redact the full longer match, not leave "value" behind.
	r.AddPlaintexts("secret", "secretvalue")
	out := r.Redact("the input is secretvalue and also bare secret")
	if strings.Contains(out, "secretvalue") {
		t.Fatalf("longer match should atomically replace: %s", out)
	}
	if strings.Contains(out, "value") {
		t.Fatalf("substring leak in redaction: %s", out)
	}
}

func TestSecretRedactor_Idempotent(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	r.AddPlaintexts("alpha_secret")
	once := r.Redact("xxx alpha_secret yyy")
	twice := r.Redact(once)
	if once != twice {
		t.Fatalf("redact not idempotent: once=%q twice=%q", once, twice)
	}
}

func TestSecretRedactor_DeduplicatesPlaintexts(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	r.AddPlaintexts("same_secret", "same_secret", "same_secret")
	if got := len(r.Plaintexts()); got != 1 {
		t.Fatalf("dedupe failed: %d plaintexts", got)
	}
}

func TestSecretRedactor_SkipsShortPlaintexts(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	r.AddPlaintexts("", "a")
	if got := len(r.Plaintexts()); got != 0 {
		t.Fatalf("short plaintexts must be skipped, got %d", got)
	}
}

func TestSecretRedactor_RedactMapPreservesShape(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	r.AddPlaintexts("api_key_value_42")
	in := map[string]any{
		"args": map[string]any{
			"token": "api_key_value_42",
			"safe":  "no secret",
		},
		"depth": 1,
	}
	out := r.RedactMap(in)
	args := out["args"].(map[string]any)
	if args["token"] == "api_key_value_42" {
		t.Fatalf("redactor failed to walk nested map")
	}
	if args["safe"] != "no secret" {
		t.Fatalf("redactor must not rewrite non-matching values: %v", args)
	}
	if out["depth"] != 1 {
		t.Fatalf("non-string scalars must pass through: %v", out["depth"])
	}
}

// TestSecretRedactor_PatternPassCatchesShape verifies the
// well-known pattern pass (Midnight tokens, emails, …) catches
// leaks from sources Midnight does not own.
func TestSecretRedactor_PatternPassCatchesShape(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	// midn_ prefix + 12 alphanumeric chars matches the default
	// redaction.DefaultRedactor pattern `midn_[A-Za-z0-9]{8,}`.
	out := r.Redact("token=midn_AAAAABBBBBCC email=ops@midnight.dev ip=10.0.0.1")
	if strings.Contains(out, "midn_AAAAABBBBBCC") {
		t.Fatalf("midnight token leaked: %s", out)
	}
	if strings.Contains(out, "ops@midnight.dev") {
		t.Fatalf("email leaked: %s", out)
	}
	if strings.Contains(out, "10.0.0.1") {
		t.Fatalf("ip leaked: %s", out)
	}
}

// TestSecretRedactor_AttackerSubmittedLeaks tries every shape the
// redactor is expected to catch when the agent or a tool tries to
// surface a secret via free-form text. None of the secrets may
// appear in the resulting string.
func TestSecretRedactor_AttackerSubmittedLeaks(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	r.AddPlaintexts("PG_PASS_letmein", "STRIPE_LIVE_xxyyzz")
	leaks := []string{
		"the password is PG_PASS_letmein",
		"hexencoded:50475f504153535f6c65746d65696e",
		"agent says: \"PG_PASS_letmein\\nSTRIPE_LIVE_xxyyzz\\n\"",
		"stripe key STRIPE_LIVE_xxyyzz failed",
		"echo $PG_PASS_letmein",
		"x=PG_PASS_letmein; y=STRIPE_LIVE_xxyyzz",
	}
	for _, in := range leaks {
		out := r.Redact(in)
		if strings.Contains(out, "PG_PASS_letmein") {
			t.Fatalf("leak: %q -> %q", in, out)
		}
		if strings.Contains(out, "STRIPE_LIVE_xxyyzz") {
			t.Fatalf("leak: %q -> %q", in, out)
		}
	}
}

// TestSecretRedactor_HexEncodedLeakIsKnownLimitation pins the
// limitation called out in ADR-0027: the redactor matches literal
// plaintext only. A consumer that hex-encodes a secret before
// emitting it bypasses the redactor; the test exists to keep the
// limitation visible.
func TestSecretRedactor_HexEncodedLeakIsKnownLimitation(t *testing.T) {
	t.Parallel()
	r := auth.NewSecretRedactor()
	r.AddPlaintexts("plainsecret")
	out := r.Redact("hex=706c61696e736563726574")
	if !strings.Contains(out, "706c61696e736563726574") {
		// If a future commit fixes this, that's great — surface
		// the change so ADR-0027 can be updated.
		t.Logf("hex-encoded leak is now caught — update ADR-0027 limitations")
	}
}

func TestSecretRedactor_AttachVault(t *testing.T) {
	t.Parallel()
	src := auth.VaultPlaintextSourceFunc(func(ctx context.Context) ([]string, error) {
		return []string{"DB_PASSWORD_FROM_VAULT"}, nil
	})
	r, err := auth.NewSecretRedactorFromVault(context.Background(), src)
	if err != nil {
		t.Fatalf("NewSecretRedactorFromVault: %v", err)
	}
	out := r.Redact("psql -p DB_PASSWORD_FROM_VAULT")
	if strings.Contains(out, "DB_PASSWORD_FROM_VAULT") {
		t.Fatalf("vault-seeded plaintext leaked: %s", out)
	}
}
