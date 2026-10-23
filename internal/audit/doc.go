// Package audit owns append-only audit records, the per-row hash chain,
// daily Ed25519 checkpoints, JSONL export, and the verifier consumed by
// `midnight audit verify`.
//
// Panel decision pinning (line 121):
//
//	"Audit log uses per-row hash chaining at MVP: prev_hash and
//	 row_hash = H(prev_hash || canonical_row), rooted in a daily
//	 audit_checkpoint row signed with the Midnight release key or
//	 operator-configured Ed25519 key. midnight audit verify CLI ships in
//	 MVP. JSONL export includes prev_hash, row_hash, checkpoint_id."
//
// The package implements:
//
//   - Row schema + canonical-row encoding (RFC 8785 JCS-equivalent for
//     a fixed field set; v1 only).
//   - row_hash = SHA-256(prev_hash || canonical_row) with the panel-locked
//     genesis SHA-256("midnight-audit-v1").
//   - Append helper that acquires a per-chain mutex, reads the previous
//     row hash, computes the new row hash, and inserts atomically.
//   - Ed25519 signer/verifier for daily checkpoints.
//   - Streaming JSONL export with a record_kind discriminator and a
//     trailing manifest line including counts and a stream hash.
//   - Verify(ctx, source) that walks the chain row-by-row, recomputes
//     hashes, validates checkpoint signatures, and returns a typed
//     VerifyReport. The CLI subcommand consumes VerifyReport directly.
//
// Lane 1 is expected to provide a Postgres-backed implementation of the
// AuditChain interface declared here. Lane 4 ships an in-memory
// implementation suitable for tests and the CLI's `--from-db` path against
// any caller-supplied AuditChain. The chain is per-instance, not per
// tenant (panel-locked).
package audit
