// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDeterministicMutationIdempotencyKeyTracksExactIntent(t *testing.T) {
	t.Parallel()

	body := map[string]any{"name": "orders", "size": 10}
	first, err := deterministicMutationIdempotencyKey("secret-token", "volume-create", http.MethodPost, "/volumes", body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deterministicMutationIdempotencyKey(
		"secret-token",
		"volume-create",
		http.MethodPost,
		"/volumes",
		map[string]any{"size": 10, "name": "orders"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same intent keys differ: %q != %q", first, second)
	}
	for name, changed := range map[string]string{
		"body":  mustMutationKey(t, "secret-token", "volume-create", http.MethodPost, "/volumes", map[string]any{"name": "other", "size": 10}),
		"path":  mustMutationKey(t, "secret-token", "volume-create", http.MethodPost, "/other", body),
		"token": mustMutationKey(t, "other-token", "volume-create", http.MethodPost, "/volumes", body),
	} {
		if changed == first {
			t.Errorf("%s change reused key %q", name, changed)
		}
	}
	if strings.Contains(first, "secret-token") || strings.Contains(first, "orders") {
		t.Fatalf("key exposed intent material: %q", first)
	}
}

func TestPendingMutationAttemptReusesUnknownOutcomeAndRotatesAfterSuccess(t *testing.T) {
	t.Setenv(mutationJournalDirEnv, t.TempDir())
	body := map[string]int{"retention_days": 14}

	first, err := beginPendingMutationAttempt("", "token", "volume-backup-create", http.MethodPost, "/volumes/1/backups", body)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Finish(io.ErrUnexpectedEOF); err != nil {
		t.Fatal(err)
	}
	retry, err := beginPendingMutationAttempt("", "token", "volume-backup-create", http.MethodPost, "/volumes/1/backups", body)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Key != first.Key {
		t.Fatalf("unknown-outcome retry key = %q, want %q", retry.Key, first.Key)
	}
	if err := retry.Finish(nil); err != nil {
		t.Fatal(err)
	}
	next, err := beginPendingMutationAttempt("", "token", "volume-backup-create", http.MethodPost, "/volumes/1/backups", body)
	if err != nil {
		t.Fatal(err)
	}
	if next.Key == first.Key {
		t.Fatalf("completed repeat reused prior attempt key %q", next.Key)
	}
}

func TestPendingMutationAttemptClearsDefiniteFailure(t *testing.T) {
	t.Setenv(mutationJournalDirEnv, t.TempDir())
	body := map[string]string{"project_id": "prj_1"}
	first, err := beginPendingMutationAttempt("", "token", "monitor-delivery-retry", http.MethodPost, "/deliveries/1/retry", body)
	if err != nil {
		t.Fatal(err)
	}
	definite := &cliAPIStatusFailure{Method: http.MethodPost, Path: "/deliveries/1/retry", Status: "409 Conflict"}
	if err := first.Finish(definite); err != nil {
		t.Fatal(err)
	}
	next, err := beginPendingMutationAttempt("", "token", "monitor-delivery-retry", http.MethodPost, "/deliveries/1/retry", body)
	if err != nil {
		t.Fatal(err)
	}
	if next.Key == first.Key {
		t.Fatalf("definite response retained attempt key %q", next.Key)
	}
}

func TestPendingMutationAttemptRetainsInProgressResponse(t *testing.T) {
	t.Setenv(mutationJournalDirEnv, t.TempDir())
	body := map[string]string{"project_id": "prj_1"}
	first, err := beginPendingMutationAttempt("", "token", "monitor-delivery-retry", http.MethodPost, "/deliveries/1/retry", body)
	if err != nil {
		t.Fatal(err)
	}
	inProgress := &cliAPIStatusFailure{
		Method: http.MethodPost,
		Path:   "/deliveries/1/retry",
		Status: "409 Conflict",
		Code:   "IDEMPOTENCY_REQUEST_IN_PROGRESS",
	}
	if err := first.Finish(inProgress); err != nil {
		t.Fatal(err)
	}
	retry, err := beginPendingMutationAttempt("", "token", "monitor-delivery-retry", http.MethodPost, "/deliveries/1/retry", body)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Key != first.Key {
		t.Fatalf("in-progress retry key = %q, want %q", retry.Key, first.Key)
	}
}

func TestWriteMutationFailureIncludesUnknownOutcomeRecoveryKey(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	writeMutationFailure(&output, "volume create", io.ErrUnexpectedEOF, "retry-key-1")
	if !strings.Contains(output.String(), "outcome is unknown") || !strings.Contains(output.String(), "--idempotency-key retry-key-1") {
		t.Fatalf("output = %q", output.String())
	}
	output.Reset()
	writeMutationFailure(&output, "volume create", &cliAPIStatusFailure{Status: "400 Bad Request"}, "retry-key-1")
	if strings.Contains(output.String(), "outcome is unknown") {
		t.Fatalf("definite failure received uncertainty guidance: %q", output.String())
	}
}

func TestResolveMutationIdempotencyKeyRejectsWhitespace(t *testing.T) {
	t.Parallel()

	_, err := resolveMutationIdempotencyKey(" key ", "token", "test", http.MethodPost, "/test", nil)
	if err == nil {
		t.Fatal("expected invalid explicit key")
	}
}

func mustMutationKey(t *testing.T, token, operation, method, path string, body any) string {
	t.Helper()
	key, err := deterministicMutationIdempotencyKey(token, operation, method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
