// Package secrets owns KEK/DEK envelope encryption and sealed-variable
// primitives.
//
// Phase 0 / Phase 1 scope (Lane 4):
//
//   - KEK source: a single 32-byte key material file at
//     /etc/midnight/kek (override via MIDNIGHT_KEK_PATH or supplied by the
//     KeyProvider interface).
//   - DEKs are 256-bit AES-GCM keys generated per Variable; ciphertext is
//     stored as (kek_id, nonce, ciphertext, tag).
//   - Nonces are 96-bit, generated from crypto/rand, never reused per key.
//   - The Vault is the in-memory primary store of variable Envelopes plus
//     redaction-safe metadata (length bucket, version, last_rotated_at).
//   - SealedVariable wraps a plaintext byte slice that can be set once,
//     decrypted into ephemeral memory by callers holding a Reveal token,
//     and never re-revealed by API surfaces.
//   - KEK rotation rewraps every DEK in a single transaction; the previous
//     KEK is retained until the configured grace expires; the rotation
//     emits a single audit-row description for the caller to persist.
//
// The package deliberately avoids any direct database dependency. Lane 1
// (data) wraps the Vault with a Postgres-backed implementation later; Lane
// 4 ships the substrate plus the in-memory implementation that the rest of
// the lane consumes from tests, the CLI, and webhook handlers.
package secrets
