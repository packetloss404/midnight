// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestSensitiveIdempotencyResponsesMigrationContract(t *testing.T) {
	upBytes, err := fs.ReadFile(FS, "0042_sensitive_idempotency_responses.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	required := []string{
		"ADD COLUMN response_storage TEXT NOT NULL DEFAULT 'plaintext-v1'",
		"ADD COLUMN response_kek_id TEXT",
		"ADD COLUMN response_wrapped_dek BYTEA",
		"ADD COLUMN response_ciphertext BYTEA",
		"ADD COLUMN response_envelope_form TEXT",
		"ADD COLUMN response_aad_sha256 TEXT",
		"response_storage IN ('plaintext-v1', 'envelope-v1')",
		"response_storage = 'envelope-v1'",
		"state IN ('SUCCEEDED', 'FAILED')",
		"response_status IS NULL",
		"response_body IS NULL",
		"response_headers IS NULL",
		"response_envelope_form IS NOT NULL",
		"response_envelope_form = 'midnight-envelope-v1'",
		"response_aad_sha256 IS NOT NULL",
		"response_aad_sha256 ~ '^[0-9a-f]{64}$'",
	}
	for _, fragment := range required {
		if !strings.Contains(up, fragment) {
			t.Errorf("up migration missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"raw_response",
		"raw_token",
		"plaintext_response",
		"response_secret",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("migration contains unsafe plaintext field %q", forbidden)
		}
	}

	downBytes, err := fs.ReadFile(FS, "0042_sensitive_idempotency_responses.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "WHERE response_storage = 'envelope-v1'") ||
		!strings.Contains(down, "cannot roll back sensitive idempotency responses while encrypted replay history exists") {
		t.Fatal("down migration does not refuse encrypted replay loss")
	}

	manifestBytes, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 42\n    name: sensitive_idempotency_responses") {
		t.Fatal("migration 0042 is not registered")
	}
}
