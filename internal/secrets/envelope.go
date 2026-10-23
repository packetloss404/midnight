package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Scope marks the privacy classification of an envelope. Sealed scope
// receives strictly stronger handling: the plaintext never returns from a
// public read path; only an internal injection boundary may unseal it.
type Scope string

const (
	// ScopePlain is for non-secret values (system/public client vars).
	ScopePlain Scope = "plain"
	// ScopeSecret is the default for credentials surfaced to runtime
	// containers via env injection but readable through the audit/admin
	// reveal path.
	ScopeSecret Scope = "secret"
	// ScopeSealed is the panel-locked write-only class. No API path
	// returns plaintext after create; the only way to "rotate" a sealed
	// variable is to overwrite with a new value.
	ScopeSealed Scope = "sealed"
)

// Envelope is the persisted shape of an encrypted variable. The DEK is
// wrapped under the active KEK; ciphertext is AES-GCM(DEK, plaintext)
// with the variable id used as the AAD so envelopes cannot be swapped
// across rows.
type Envelope struct {
	KEKID         KEKID  `json:"kek_id"`
	WrappedDEK    []byte `json:"wrapped_dek"`
	Ciphertext    []byte `json:"ciphertext"`
	CanonicalForm string `json:"canonical_form"` // version tag for migration safety
}

// envelopeCanonicalV1 is the only canonical form Lane 4 emits.
const envelopeCanonicalV1 = "midnight-envelope-v1"

// ErrEnvelopeAuth is returned when the wrapped DEK fails to unwrap, when
// AES-GCM authentication fails, or when the AAD does not match the
// supplied variable id (envelope swap detection).
var ErrEnvelopeAuth = errors.New("midnight: envelope authentication failed")

// VariableID is a canonical, scope-prefixed identifier used as the AAD
// for AES-GCM so envelopes cannot be re-bound to a different variable
// row (which would otherwise trivially leak plaintext between rows).
type VariableID string

// VariableMeta is the redaction-safe view of a Variable surfaced through
// any read path. Plaintext NEVER appears in this struct; sealed variables
// further have CleartextSize set to a coarse bucket only.
//
// ValueKind is "literal" for a value that is a plain string and
// "reference" when the stored template contains at least one `${{...}}`
// expression. References lists the parsed reference targets — names
// only, never values — so dashboards can surface the dependency graph
// without unsealing the encrypted blob.
type VariableMeta struct {
	ID             VariableID          `json:"id"`
	Scope          Scope               `json:"scope"`
	Workspace      string              `json:"workspace_id"`
	Project        string              `json:"project_id"`
	Environment    string              `json:"environment_id"`
	Service        string              `json:"service_id,omitempty"`
	Name           string              `json:"name"`
	Version        int                 `json:"version"`
	LastRotatedAt  time.Time           `json:"last_rotated_at"`
	LengthBucket   string              `json:"length_bucket"`
	KEKFingerprint string              `json:"kek_fingerprint"`
	ValueKind      string              `json:"value_kind,omitempty"`
	References     []VariableReference `json:"references,omitempty"`
}

// VariableReference describes one parsed `${{...}}` reference attached
// to a variable's stored template. The fields are deliberately
// minimal — they identify the target by name only so the metadata view
// never leaks the resolved value.
type VariableReference struct {
	Kind    string `json:"kind"` // "service_var" | "shared" | "local" | "builtin"
	Service string `json:"service,omitempty"`
	Name    string `json:"name"`
}

// LengthBucket maps a plaintext byte length onto the panel-locked
// metadata buckets (Sprint 9 VAR-904: 1-16, 17-64, 65-256, 257-1024,
// 1024+) so audit and metadata surfaces can describe size without
// leaking exact size.
func LengthBucket(n int) string {
	switch {
	case n <= 0:
		return "empty"
	case n <= 16:
		return "1-16"
	case n <= 64:
		return "17-64"
	case n <= 256:
		return "65-256"
	case n <= 1024:
		return "257-1024"
	default:
		return "1024+"
	}
}

// SealForVariable encrypts plaintext for the supplied VariableID via the
// supplied KeyProvider. The variable id is bound into the AES-GCM AAD so
// the envelope cannot be silently re-bound to another variable row.
func SealForVariable(ctx context.Context, kp KeyProvider, id VariableID, plaintext []byte) (Envelope, error) {
	dek, err := generateDEK()
	if err != nil {
		return Envelope{}, err
	}
	defer zero(dek)

	kekID, wrappedDEK, err := kp.Wrap(ctx, dek)
	if err != nil {
		return Envelope{}, err
	}
	ciphertext, err := aesGCMSeal(dek, []byte(id), plaintext)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		KEKID:         kekID,
		WrappedDEK:    wrappedDEK,
		Ciphertext:    ciphertext,
		CanonicalForm: envelopeCanonicalV1,
	}, nil
}

// OpenForVariable opens an envelope produced by SealForVariable.
func OpenForVariable(ctx context.Context, kp KeyProvider, id VariableID, env Envelope) ([]byte, error) {
	if env.CanonicalForm != envelopeCanonicalV1 {
		return nil, fmt.Errorf("midnight: unknown envelope form %q", env.CanonicalForm)
	}
	dek, err := kp.Unwrap(ctx, env.KEKID, env.WrappedDEK)
	if err != nil {
		return nil, err
	}
	defer zero(dek)
	plaintext, err := aesGCMOpen(dek, []byte(id), env.Ciphertext)
	if err != nil {
		return nil, ErrEnvelopeAuth
	}
	return plaintext, nil
}

// ReWrap re-wraps the DEK inside an envelope under the currently-active
// KEK without ever re-encrypting the ciphertext. This is the inner step of
// KEK rotation.
func ReWrap(ctx context.Context, kp KeyProvider, env Envelope) (Envelope, error) {
	dek, err := kp.Unwrap(ctx, env.KEKID, env.WrappedDEK)
	if err != nil {
		return Envelope{}, err
	}
	defer zero(dek)
	newKEK, newWrapped, err := kp.Wrap(ctx, dek)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		KEKID:         newKEK,
		WrappedDEK:    newWrapped,
		Ciphertext:    env.Ciphertext,
		CanonicalForm: env.CanonicalForm,
	}, nil
}

// EnvelopeFingerprint returns a stable hash over an envelope's wire form
// (kek_id + wrapped_dek + ciphertext). Used by audit rows and tests; never
// includes plaintext.
func EnvelopeFingerprint(env Envelope) string {
	h := sha256.New()
	_, _ = h.Write([]byte(env.KEKID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(env.WrappedDEK)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(env.Ciphertext)
	return hex.EncodeToString(h.Sum(nil))
}

// zero overwrites b in place. Defensive only — Go can copy buffers around
// freely in a moving GC, so callers should still treat plaintext as
// short-lived (panel: line 1178 — secret plaintext lifetime "zeroed
// after injection").
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
