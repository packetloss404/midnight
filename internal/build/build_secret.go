// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	// RailpackSecretsHashBuildArg is the cache-invalidation input expected by
	// Railpack's gateway frontend. It is derived from secret values but is not
	// itself a secret transport; provenance always redacts it.
	RailpackSecretsHashBuildArg = "secrets-hash"
	buildSecretEnvPrefix        = "MIDNIGHT_BUILD_SECRET_" // not-a-failure-code
)

// BuildSecret is one ephemeral BuildKit session secret. ID is safe metadata;
// Value must not be serialized or persisted. Callers should invoke
// ZeroBuildSecrets as soon as the solve completes.
type BuildSecret struct {
	ID    string
	Value []byte
}

// ValidBuildSecretID accepts the portable environment-variable subset that
// both BuildKit's env-backed secret source and Railpack can address without
// delimiter ambiguity.
func ValidBuildSecretID(id string) bool {
	if id == "" {
		return false
	}
	for index, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' {
			continue
		}
		if index > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// ValidateBuildSecrets rejects invalid and duplicate mount IDs before a
// process is started.
func ValidateBuildSecrets(secrets []BuildSecret) error {
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if !ValidBuildSecretID(secret.ID) {
			return fmt.Errorf("build secret id %q is not a portable environment name", secret.ID)
		}
		if _, exists := seen[secret.ID]; exists {
			return fmt.Errorf("build secret id %q is duplicated", secret.ID)
		}
		seen[secret.ID] = struct{}{}
	}
	return nil
}

// ValidateBuildSecretIDs validates redaction-safe secret metadata before it is
// passed to Railpack.
func ValidateBuildSecretIDs(ids []string) error {
	secrets := make([]BuildSecret, len(ids))
	for index, id := range ids {
		secrets[index].ID = id
	}
	return ValidateBuildSecrets(secrets)
}

// CopyBuildSecrets returns a deep copy so builders cannot retain or mutate a
// caller's backing plaintext buffer.
func CopyBuildSecrets(in []BuildSecret) []BuildSecret {
	if len(in) == 0 {
		return nil
	}
	out := make([]BuildSecret, len(in))
	for index, secret := range in {
		out[index] = BuildSecret{
			ID:    secret.ID,
			Value: append([]byte(nil), secret.Value...),
		}
	}
	return out
}

// ZeroBuildSecrets overwrites the mutable plaintext byte slices owned by a
// caller. Go strings and the child-process environment cannot be reliably
// zeroed; this is a best-effort reduction of the worker's retained copies.
func ZeroBuildSecrets(secrets []BuildSecret) {
	for index := range secrets {
		for offset := range secrets[index].Value {
			secrets[index].Value[offset] = 0
		}
		secrets[index].Value = nil
	}
}

// BuildSecretIDs returns the deterministic, redaction-safe ID list.
func BuildSecretIDs(secrets []BuildSecret) []string {
	sorted := sortedBuildSecrets(secrets)
	if len(sorted) == 0 {
		return nil
	}
	ids := make([]string, len(sorted))
	for index := range sorted {
		ids[index] = sorted[index].ID
	}
	return ids
}

// BuildSecretsHash returns Railpack's deterministic layer-invalidation key.
// Length-prefixing makes the input unambiguous. The digest is passed as the
// frontend's secrets-hash build arg and is redacted from durable provenance.
func BuildSecretsHash(secrets []BuildSecret) string {
	if len(secrets) == 0 {
		return ""
	}
	hash := sha256.New()
	var length [8]byte
	for _, secret := range sortedBuildSecrets(secrets) {
		binary.BigEndian.PutUint64(length[:], uint64(len(secret.ID)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(secret.ID))
		binary.BigEndian.PutUint64(length[:], uint64(len(secret.Value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(secret.Value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sortedBuildSecrets(secrets []BuildSecret) []BuildSecret {
	out := append([]BuildSecret(nil), secrets...)
	sort.Slice(out, func(left, right int) bool {
		return out[left].ID < out[right].ID
	})
	return out
}

func buildSecretEnvironment(secrets []BuildSecret) []string {
	sorted := sortedBuildSecrets(secrets)
	if len(sorted) == 0 {
		return nil
	}
	env := make([]string, len(sorted))
	for index, secret := range sorted {
		env[index] = fmt.Sprintf("%s%d=%s", buildSecretEnvPrefix, index, string(secret.Value))
	}
	return env
}

func buildSecretValues(secrets []BuildSecret) []string {
	if len(secrets) == 0 {
		return nil
	}
	values := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if len(secret.Value) > 0 {
			values = append(values, string(secret.Value))
		}
	}
	return values
}

func environmentWithOverrides(base, overrides []string) []string {
	replaced := make(map[string]struct{}, len(overrides))
	for _, item := range overrides {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			replaced[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			if _, replace := replaced[key]; replace {
				continue
			}
		}
		out = append(out, item)
	}
	return append(out, overrides...)
}

// SecretRedactingWriter removes exact secret values before bytes reach the
// underlying durable log. It retains at most max-secret-length minus one
// trailing bytes between writes, preventing a value split across arbitrary
// subprocess write boundaries from escaping.
type SecretRedactingWriter struct {
	dst       io.Writer
	secrets   [][]byte
	maxLength int
	pending   []byte
	closed    bool
	err       error
}

// NewSecretRedactingWriter constructs a streaming exact-value redactor. Empty
// values are ignored because replacing the empty string would be undefined.
func NewSecretRedactingWriter(dst io.Writer, values []string) *SecretRedactingWriter {
	writer := &SecretRedactingWriter{dst: dst}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		raw := []byte(value)
		writer.secrets = append(writer.secrets, raw)
		if len(raw) > writer.maxLength {
			writer.maxLength = len(raw)
		}
	}
	sort.Slice(writer.secrets, func(left, right int) bool {
		return len(writer.secrets[left]) > len(writer.secrets[right])
	})
	return writer
}

func (w *SecretRedactingWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("secret redacting writer is closed")
	}
	if w.err != nil {
		return 0, w.err
	}
	if len(w.secrets) == 0 {
		n, err := w.dst.Write(p)
		if err != nil {
			w.err = err
		}
		return n, err
	}
	w.pending = append(w.pending, p...)
	if err := w.drain(false); err != nil {
		w.err = err
		return 0, err
	}
	return len(p), nil
}

// Close flushes buffered suffix bytes. It deliberately does not close the
// underlying writer, whose lifecycle remains owned by BuildctlRunner.
func (w *SecretRedactingWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err == nil {
		w.err = w.drain(true)
	}
	return w.err
}

func (w *SecretRedactingWriter) drain(final bool) error {
	for len(w.pending) > 0 {
		index, secret := earliestSecret(w.pending, w.secrets)
		if index >= 0 {
			if err := writeAll(w.dst, w.pending[:index]); err != nil {
				return err
			}
			if err := writeAll(w.dst, []byte(redactedValue)); err != nil {
				return err
			}
			w.pending = w.pending[index+len(secret):]
			continue
		}
		emit := len(w.pending)
		if !final {
			emit -= w.maxLength - 1
			if emit <= 0 {
				return nil
			}
		}
		if err := writeAll(w.dst, w.pending[:emit]); err != nil {
			return err
		}
		w.pending = w.pending[emit:]
	}
	return nil
}

func earliestSecret(value []byte, secrets [][]byte) (int, []byte) {
	bestIndex := -1
	var best []byte
	for _, secret := range secrets {
		index := bytes.Index(value, secret)
		if index < 0 {
			continue
		}
		if bestIndex < 0 || index < bestIndex || (index == bestIndex && len(secret) > len(best)) {
			bestIndex = index
			best = secret
		}
	}
	return bestIndex, best
}

func writeAll(dst io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := dst.Write(value)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}
