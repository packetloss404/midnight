// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	serveridempotency "github.com/midnight/midnight/internal/idempotency"
)

const mutationJournalDirEnv = "MIDNIGHT_CLI_MUTATION_JOURNAL_DIR" // not-a-failure-code

type mutationIntentEnvelope struct {
	Version   int             `json:"version"`
	Operation string          `json:"operation"`
	Method    string          `json:"method"`
	Path      string          `json:"path"`
	Body      json.RawMessage `json:"body"`
}

// mutationAttempt owns a persisted key for a mutation that users may
// intentionally perform more than once (for example, creating a new backup).
// Definite responses clear the record. Transport and response-read failures
// retain it so the next identical invocation safely replays the same attempt.
type mutationAttempt struct {
	Key        string
	recordPath string
}

// resolveMutationIdempotencyKey derives an opaque, exact-intent key for
// desired-state mutations. The credential is used only as an HMAC key; neither
// it nor a reversible encoding of the request body is placed on the wire.
func resolveMutationIdempotencyKey(
	explicit, token, operation, method, path string,
	body any,
) (string, error) {
	if explicit != "" {
		if strings.TrimSpace(explicit) != explicit {
			return "", serveridempotency.ErrInvalidKey
		}
		if err := serveridempotency.ValidateKey(serveridempotency.Key(explicit)); err != nil {
			return "", err
		}
		return explicit, nil
	}
	return deterministicMutationIdempotencyKey(token, operation, method, path, body)
}

func deterministicMutationIdempotencyKey(
	token, operation, method, path string,
	body any,
) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("a credential is required to derive a stable mutation key")
	}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode mutation intent: %w", err)
	}
	envelope, err := json.Marshal(mutationIntentEnvelope{
		Version:   1,
		Operation: operation,
		Method:    strings.ToUpper(strings.TrimSpace(method)),
		Path:      path,
		Body:      encodedBody,
	})
	if err != nil {
		return "", fmt.Errorf("encode mutation envelope: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("midnight-cli-mutation-intent-v1\x00"))
	_, _ = mac.Write(envelope)
	digest := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	key := "midnight-cli-" + mutationKeyPrefix(operation) + "-" + digest
	if err := serveridempotency.ValidateKey(serveridempotency.Key(key)); err != nil {
		return "", fmt.Errorf("derive mutation key: %w", err)
	}
	return key, nil
}

// beginPendingMutationAttempt returns a persisted random key for repeatable
// actions. An explicit key bypasses the journal and remains caller-owned.
func beginPendingMutationAttempt(
	explicit, token, operation, method, path string,
	body any,
) (mutationAttempt, error) {
	if explicit != "" {
		key, err := resolveMutationIdempotencyKey(explicit, token, operation, method, path, body)
		return mutationAttempt{Key: key}, err
	}
	fingerprint, err := deterministicMutationIdempotencyKey(token, operation+"-pending", method, path, body)
	if err != nil {
		return mutationAttempt{}, err
	}
	dir, err := mutationJournalDir()
	if err != nil {
		return mutationAttempt{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return mutationAttempt{}, fmt.Errorf("create mutation journal: %w", err)
	}
	digest := sha256.Sum256([]byte(fingerprint))
	recordPath := filepath.Join(
		dir,
		mutationKeyPrefix(operation)+"-"+base64.RawURLEncoding.EncodeToString(digest[:])+".key",
	)
	if key, err := readPendingMutationKey(recordPath); err == nil {
		return mutationAttempt{Key: key, recordPath: recordPath}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return mutationAttempt{}, err
	}

	key, err := randomMutationIdempotencyKey(operation)
	if err != nil {
		return mutationAttempt{}, err
	}
	created, err := installPendingMutationKey(dir, recordPath, key)
	if err != nil {
		return mutationAttempt{}, err
	}
	if !created {
		key, err = readPendingMutationKey(recordPath)
		if err != nil {
			return mutationAttempt{}, err
		}
	}
	return mutationAttempt{Key: key, recordPath: recordPath}, nil
}

// Finish clears a pending-attempt record only after a definite server outcome.
// An uncertain transport outcome deliberately retains the key for replay.
func (attempt mutationAttempt) Finish(requestErr error) error {
	if attempt.recordPath == "" || mutationOutcomeUncertain(requestErr) {
		return nil
	}
	if err := os.Remove(attempt.recordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear completed mutation attempt: %w", err)
	}
	return nil
}

func mutationOutcomeUncertain(err error) bool {
	if err == nil {
		return false
	}
	var statusFailure *cliAPIStatusFailure
	if !errors.As(err, &statusFailure) {
		return true
	}
	// The server has acknowledged the key but has not reached a terminal
	// result. Retaining the journal record prevents a later retry from starting
	// a second logical action while the first is still running.
	return statusFailure.Code == "IDEMPOTENCY_REQUEST_IN_PROGRESS"
}

func writeMutationFailure(w io.Writer, command string, err error, key string) {
	fmt.Fprintf(w, "midnight %s: %s\n", command, err)
	if mutationOutcomeUncertain(err) && strings.TrimSpace(key) != "" {
		fmt.Fprintf(
			w,
			"midnight %s: request outcome is unknown; retry the exact command with --idempotency-key %s\n",
			command,
			key,
		)
	}
}

func mutationJournalDir() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv(mutationJournalDirEnv)); explicit != "" {
		return explicit, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "midnight", "pending-mutations"), nil
}

func readPendingMutationKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if err := serveridempotency.ValidateKey(serveridempotency.Key(key)); err != nil {
		return "", fmt.Errorf("mutation journal record %s is invalid: %w", path, err)
	}
	return key, nil
}

func installPendingMutationKey(dir, recordPath, key string) (bool, error) {
	temporary, err := os.CreateTemp(dir, ".pending-*.key")
	if err != nil {
		return false, fmt.Errorf("create mutation journal record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("secure mutation journal record: %w", err)
	}
	if _, err := io.WriteString(temporary, key+"\n"); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("write mutation journal record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("sync mutation journal record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close mutation journal record: %w", err)
	}
	if err := os.Link(temporaryPath, recordPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("publish mutation journal record: %w", err)
	}
	return true, nil
}

func randomMutationIdempotencyKey(operation string) (string, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate mutation key: %w", err)
	}
	return "midnight-cli-" + mutationKeyPrefix(operation) + "-" + base64.RawURLEncoding.EncodeToString(random), nil
}

func mutationKeyPrefix(operation string) string {
	var out strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(operation) {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r)
		if valid {
			out.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen && out.Len() > 0 {
			out.WriteByte('-')
			lastHyphen = true
		}
		if out.Len() >= 40 {
			break
		}
	}
	value := strings.Trim(out.String(), "-")
	if value == "" {
		return "mutation"
	}
	return value
}
