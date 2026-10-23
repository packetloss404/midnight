package secrets

import (
	"context"
	"errors"
	"sync"
)

// ErrSealedRevealed is returned when a caller attempts to obtain
// plaintext from a sealed variable through any path other than the
// authorized injection token.
var ErrSealedRevealed = errors.New("midnight: sealed variable cannot be revealed")

// ErrSealedAlreadySet is returned when a sealed variable's plaintext is
// supplied a second time without a rotation. Sealed variables forbid
// view-then-edit; the only way to "edit" is to rotate (which calls
// Rotate / Set on a new value).
var ErrSealedAlreadySet = errors.New("midnight: sealed variable already set")

// RevealToken is an opaque marker that proves the caller is in an
// authorized injection context (build/runtime injection, sealed rotation,
// or restore). It is never serialized and is only constructed by the
// SecretStore implementation that produced the SealedVariable.
type RevealToken struct {
	scope string // "runtime_inject" | "build_inject" | "sealed_rotation" | "restore"
	vault *Vault
}

// String returns a debug-only representation; never includes plaintext.
func (r RevealToken) String() string {
	if r.scope == "" {
		return "RevealToken(empty)"
	}
	return "RevealToken(" + r.scope + ")"
}

// SealedVariable is the panel-locked write-only-after-create variable
// type. Read paths return only metadata; the plaintext is reachable only
// via Reveal with a valid RevealToken.
type SealedVariable struct {
	mu       sync.Mutex
	meta     VariableMeta
	envelope Envelope
	store    *Vault // back-reference for unseal
	set      bool
}

// Meta returns the redaction-safe metadata view. Plaintext is never
// included; sealed variables further surface only LengthBucket, never the
// exact byte length.
func (s *SealedVariable) Meta() VariableMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// String returns a redacted representation suitable for logging.
func (s *SealedVariable) String() string {
	m := s.Meta()
	return "SealedVariable(id=" + string(m.ID) + ",bucket=" + m.LengthBucket + ")"
}

// Reveal returns plaintext to the caller. Requires a RevealToken that was
// minted by the same Vault that created the SealedVariable; tokens minted
// by a different Vault are rejected so tests cannot accidentally cross
// boundaries.
func (s *SealedVariable) Reveal(ctx context.Context, token RevealToken) ([]byte, error) {
	if token.scope == "" {
		return nil, ErrSealedRevealed
	}
	s.mu.Lock()
	env := s.envelope
	store := s.store
	id := s.meta.ID
	s.mu.Unlock()
	if store == nil || token.vault != store {
		return nil, ErrSealedRevealed
	}
	return OpenForVariable(ctx, store.kp, id, env)
}

// PublicJSON returns the metadata-only JSON-shape an API GET handler
// returns for a sealed variable. Plaintext NEVER appears.
func (s *SealedVariable) PublicJSON() VariableMeta {
	return s.Meta()
}
