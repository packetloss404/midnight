// Package pg owns the Postgres seam for the Midnight control plane.
//
// Phase 1 / Lane 1 (DATA) introduces real persistence. This package wraps
// jackc/pgx/v5 with the conventions every other store in `internal/...`
// relies on:
//
//   - DSN loading from environment (MIDNIGHT_DB_DSN) with file fallback,
//     so local development, CI, and operator-baked installs share the same
//     code path. Lane 4 will later own the envelope-encrypted variant.
//   - A typed connection Pool wrapper that exposes Acquire / WithTx and
//     a tiny Exec/Query/QueryRow surface for callers that do not need a
//     transaction.
//   - A Tx wrapper that satisfies the same Exec/Query/QueryRow contract,
//     so stores can write a single SQL helper and use it in either mode.
//   - A migration runner (see migrate.go) that reads .up.sql files in
//     filename order, computes SHA-256 over the file bytes (replacing the
//     Phase 0 `sha256:placeholder`), records applied versions in
//     `schema_migrations`, and runs each migration inside its own
//     transaction wrapped by a Postgres advisory lock.
//
// This package is intentionally minimal: the per-store schema lives in
// the migration files; the per-store query strings live in the store
// packages (`internal/api`, `internal/audit`, `internal/idempotency`,
// `internal/queue`, `internal/timeline`). The seam here only owns
// connection lifecycle and transactional plumbing.
package pg
