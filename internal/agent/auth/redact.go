// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/midnight/midnight/internal/redaction"
	"github.com/midnight/midnight/internal/secrets"
)

// RedactedPlaceholder is the constant the redactor substitutes for
// matched plaintext values. Kept stable so dashboards / audit
// readers can recognize redacted spans.
const RedactedPlaceholder = "<redacted>"

// SecretRedactor rewrites a free-form string by replacing every
// plaintext that matches a known secret with RedactedPlaceholder.
// It is safe for concurrent use; AddPlaintexts may be called at
// any point to extend the match set.
//
// Algorithm:
//
//  1. Maintain a deduped, length-sorted (longest-first) list of
//     plaintexts. Sorting by length ensures a longer plaintext
//     containing a shorter plaintext as a substring is replaced
//     atomically (otherwise the inner match would consume the
//     outer one's bytes).
//  2. For each plaintext, do a plain strings.ReplaceAll. The
//     replacement is idempotent because the placeholder cannot
//     equal any allowed plaintext (it includes "<" and ">" which
//     are not valid token characters under any of the canonical
//     credential formats Midnight tracks).
//  3. After the per-secret pass, run the pattern-based
//     redaction.DefaultRedactor over the result to catch
//     well-known shapes (Midnight tokens, emails, AWS keys, …)
//     that may have leaked from sources Midnight does not own.
//
// Known limitations (documented in ADR-0027):
//   - Plaintext known only to an upstream provider is never
//     redacted unless the operator surfaces it through
//     AddPlaintexts or the vault adapter.
//   - Single-character secrets and the empty string are skipped
//     to avoid pathological N-pass rewrites.
//   - Plaintext that the agent has never observed (and that the
//     vault doesn't know about) leaks through. The chat-backend
//     SHOULD reject tool inputs that name an unknown variable.
//   - Tokens encoded in base64/URL-safe forms are NOT decoded —
//     only the literal plaintext shape is matched. Provider
//     tokens that are themselves stored encoded should also be
//     stored in the vault under a wrapped-base64 form so the
//     literal match catches them.
type SecretRedactor struct {
	mu          sync.RWMutex
	plaintexts  []string
	patternPass *redaction.Redactor
}

// NewSecretRedactor constructs an empty redactor. Add plaintexts
// via AddPlaintexts; the well-known pattern pass runs by default.
func NewSecretRedactor() *SecretRedactor {
	return &SecretRedactor{patternPass: redaction.DefaultRedactor()}
}

// AddPlaintexts extends the secret match set. Empty or
// single-character strings are skipped per the limitation above.
// Idempotent: duplicate values are deduplicated. After this call
// the match set is re-sorted longest-first so substring overlaps
// resolve atomically.
func (r *SecretRedactor) AddPlaintexts(values ...string) {
	if r == nil || len(values) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]struct{}, len(r.plaintexts)+len(values))
	for _, existing := range r.plaintexts {
		seen[existing] = struct{}{}
	}
	for _, v := range values {
		if len(v) < 2 {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		r.plaintexts = append(r.plaintexts, v)
	}
	sort.SliceStable(r.plaintexts, func(i, j int) bool {
		// longest-first so a longer plaintext containing a
		// shorter one as a substring is replaced atomically
		return len(r.plaintexts[i]) > len(r.plaintexts[j])
	})
}

// Plaintexts returns a copy of the registered plaintext set.
// Test-only — production callers should never need to enumerate.
func (r *SecretRedactor) Plaintexts() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.plaintexts))
	copy(out, r.plaintexts)
	return out
}

// Redact applies the per-secret pass + the pattern pass. Safe on
// unknown input — returns the input unchanged when no rules
// match. Idempotent.
func (r *SecretRedactor) Redact(input string) string {
	if r == nil || input == "" {
		return input
	}
	r.mu.RLock()
	plaintexts := r.plaintexts
	pattern := r.patternPass
	r.mu.RUnlock()
	out := input
	for _, secret := range plaintexts {
		if secret == "" {
			continue
		}
		if !strings.Contains(out, secret) {
			continue
		}
		out = strings.ReplaceAll(out, secret, RedactedPlaceholder)
	}
	if pattern != nil {
		out = pattern.RedactString(out)
	}
	return out
}

// RedactMap walks a JSON-shaped map and redacts every string
// value. Schema is preserved (no keys are dropped).
func (r *SecretRedactor) RedactMap(m map[string]any) map[string]any {
	if r == nil || m == nil {
		return m
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = r.redactValue(v)
	}
	return out
}

func (r *SecretRedactor) redactValue(v any) any {
	switch typed := v.(type) {
	case string:
		return r.Redact(typed)
	case map[string]any:
		return r.RedactMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = r.redactValue(item)
		}
		return out
	default:
		return v
	}
}

// VaultPlaintextSource is the seam the redactor uses to learn
// which plaintext values the secrets.Vault knows about. The
// canonical secrets.Vault does NOT expose plaintexts in bulk (by
// design — sealed vars are write-only); the operator wires a
// narrower adapter that:
//
//   - on first ingest, walks every non-sealed variable and
//     unseals it under ScopeRestore (or a custom resolved scope
//     the operator approves);
//   - listens to a "variable changed" event and appends the new
//     plaintext on each rotation.
//
// In the v1 default this is left unset and the redactor falls
// back to the pattern-pass alone. Operators opting in to
// plaintext-aware redaction pass a VaultPlaintextSource
// explicitly.
type VaultPlaintextSource interface {
	// Plaintexts returns the snapshot of known plaintexts the
	// redactor should match. Implementations MUST NOT include
	// sealed-variable values.
	Plaintexts(ctx context.Context) ([]string, error)
}

// AttachVault registers a VaultPlaintextSource. The redactor
// pulls plaintexts at attach time (synchronously) and caches them.
// Callers reseed by re-calling AttachVault after rotation.
func (r *SecretRedactor) AttachVault(ctx context.Context, src VaultPlaintextSource) error {
	if r == nil || src == nil {
		return nil
	}
	values, err := src.Plaintexts(ctx)
	if err != nil {
		return err
	}
	r.AddPlaintexts(values...)
	return nil
}

// NewSecretRedactorFromVault constructs a redactor preseeded from
// a VaultPlaintextSource. Equivalent to NewSecretRedactor +
// AttachVault for callers that want a single constructor.
func NewSecretRedactorFromVault(ctx context.Context, src VaultPlaintextSource) (*SecretRedactor, error) {
	r := NewSecretRedactor()
	if err := r.AttachVault(ctx, src); err != nil {
		return nil, err
	}
	return r, nil
}

// VaultPlaintextSourceFunc adapts a plain function to the
// VaultPlaintextSource interface.
type VaultPlaintextSourceFunc func(ctx context.Context) ([]string, error)

// Plaintexts implements VaultPlaintextSource.
func (f VaultPlaintextSourceFunc) Plaintexts(ctx context.Context) ([]string, error) {
	return f(ctx)
}

// EmptyVaultPlaintextSource always returns no plaintexts. Used as
// the production default when plaintext-aware redaction is
// disabled.
type EmptyVaultPlaintextSource struct{}

// Plaintexts returns (nil, nil).
func (EmptyVaultPlaintextSource) Plaintexts(ctx context.Context) ([]string, error) {
	return nil, nil
}

// PlaintextResolver is the helper signature secrets.Vault consumers
// implement to decrypt a single VariableMeta. The redactor uses it
// to walk the metadata list returned by the vault and accumulate
// plaintexts for every non-sealed variable.
type PlaintextResolver func(ctx context.Context, meta secrets.VariableMeta) ([]byte, error)

// CollectVaultPlaintexts returns the plaintexts of every
// non-sealed variable in the supplied metadata list, using the
// supplied resolver. Errors from individual resolves are surfaced
// to the caller — partial snapshots are not returned, so a single
// decrypt failure aborts the seed.
func CollectVaultPlaintexts(ctx context.Context, metas []secrets.VariableMeta, resolve PlaintextResolver) ([]string, error) {
	if resolve == nil {
		return nil, nil
	}
	out := make([]string, 0, len(metas))
	for _, meta := range metas {
		if meta.Scope == secrets.ScopeSealed {
			continue
		}
		plaintext, err := resolve(ctx, meta)
		if err != nil {
			return nil, err
		}
		if len(plaintext) == 0 {
			continue
		}
		out = append(out, string(plaintext))
	}
	return out, nil
}

// redactString is a tiny helper used by AuditChainSink to redact a
// single value when a redactor is configured. Nil-safe.
func redactString(r *SecretRedactor, s string) string {
	if r == nil {
		return s
	}
	return r.Redact(s)
}

// redactMap is the analogous helper for map values.
func redactMap(r *SecretRedactor, m map[string]any) map[string]any {
	if r == nil {
		return m
	}
	return r.RedactMap(m)
}
