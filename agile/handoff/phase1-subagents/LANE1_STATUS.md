# Phase 1 / Lane 1 (DATA) — Status

- **Branch:** `phase1/lane1-data` (worktree at `.worktrees/phase1-lane1-data`)
- **Author:** Lane 1 subagent
- **Base commit:** `2a64283` (`main`, "feat: phase 1 — promote scaffolds toward functional implementations")
- **Pushes:** none. Branch is local only.
- **Commits:** none. Changes are left uncommitted on the branch for human review.

## Acceptance bar — checks run locally

| Check | Result |
|---|---|
| `go fmt ./...` | clean (no formatting changes left in tree) |
| `go vet ./...` | green |
| `go build ./...` | green |
| `go test ./...` | green for every Lane-1-owned package; pre-existing failure in `internal/router` (Traefik renderer, Lane 3 territory) carried over from `main` |
| `go test -tags=integration ./internal/db/...` | green against a real Postgres 16 testcontainer (Docker required) |
| `go run ./tools/lint` | green (`tools/versions.txt` updated to `go=1.25.0+` to match new go.mod directive) |
| `go run ./tools/phase0-gate` | green |

## What landed

### New packages

- `internal/db/pg/` — Postgres seam.
  - `dsn.go` — `LoadDSN` reading `MIDNIGHT_DB_DSN` (env) with file fallback (`MIDNIGHT_DB_DSN_FILE` → `/etc/midnight/db.dsn`). Records `Source` so operators can confirm the source without leaking the DSN.
  - `pool.go` — `Pool` wrapper (`Open` / `Close` / `Ping` / `Exec` / `Query` / `QueryRow` / `Acquire` / `WithTx` / `WithTxOpts`).
  - `tx.go` — `Tx` wrapper plus a `Conn` interface that both Pool and Tx satisfy, so every store helper composes with both modes.
  - `migrate.go` — `MigrationRunner` that reads `migrations/*.up.sql` ordered by filename, computes real per-file `sha256:<hex>` digests, and applies each migration inside its own advisory-locked transaction (`pg_advisory_xact_lock(MigrationAdvisoryLockKey)`). Records applied versions + `up_sha256` in the `schema_migrations` bookkeeping table. `Up()` is idempotent; re-running with the same files is a no-op. Mismatch detection: if a file's recomputed SHA-256 doesn't equal the recorded one, the runner refuses to proceed.
  - `integration_test.go` (build tag `integration`) — testcontainers-go v0.35.0 spinning up `postgres:16-alpine`, exercising every store, with explicit assertions on:
    - migration manifest SHA-256 matches the actual file bytes;
    - operation/event durability across pool restart;
    - idempotency state matrix (in-progress / replay-2xx / different-body 409);
    - audit chain insert + verify + tamper detection (direct `UPDATE audit_logs` from a fresh pgx connection breaks the chain);
    - resource CRUD (project / environment / service);
    - timeline pagination + restart durability;
    - queue `FOR UPDATE SKIP LOCKED` (two workers see different rows; foreign-worker heartbeat returns `ErrLeaseLost`).
- `migrations/embed.go` — `embed.FS` exposing manifest + every `.up.sql` / `.down.sql` so binaries (and the integration harness) consume the same migration set.

### New stores (all behind existing interfaces — no public-API changes)

- `internal/api/operation_store_pg.go` — `PostgresOperationStore` (matches `OperationStore`).
- `internal/api/resource_store_pg.go` — `PostgresResourceStore` (matches `ProjectStore` + `ServiceStore`, plus a new `EnvironmentStore` that other lanes can adopt).
- `internal/idempotency/postgres_store.go` — `PostgresStore` (matches `idempotency.Store`); panel-locked state matrix enforced server-side via `INSERT ... ON CONFLICT DO NOTHING` + `RowsAffected()` to distinguish winner from loser.
- `internal/audit/chain.go` + `internal/audit/store.go` — chain math + in-memory and Postgres implementations of `audit.Store`. Hashes `prev_hash || canonical_row` per ADR-011; timestamps truncate to microseconds for round-trip stability through `timestamptz`.
- `internal/timeline/postgres_store.go` — `PostgresStore` exposing `Append` / `List` / `DeploymentIDs` / `Query` with the same shape as `MemoryStore`. `Query` pushes filters into SQL and supports the existing `(deployment_id, after_seq)` cursor.
- `internal/queue/postgres_store.go` — `PostgresStore` implementing the `Store` interface. Dequeue is the panel-locked `WITH cte AS (… FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE … RETURNING` shape with the runtime `lease_expires_at IS NULL OR lease_expires_at < $now` predicate. Heartbeat / Complete / Fail use `lease_owner = $worker_id AND state = 'leased'` so a foreign worker returns `ErrLeaseLost`. `Reclaim` is the reaper sweep (panel section 3).

### Cursor signing (§0.2)

- `internal/api/cursor_secret.go` introduces a `CursorSecretSource` interface with a default `FileCursorSecretSource` (path = `/etc/midnight/cursor.key`, override via `MIDNIGHT_CURSOR_KEY_PATH`). Length budget: ≤ 4 KiB on disk, ≥ 32 bytes after `TrimRight`.
- The Phase 0 placeholder is preserved as `defaultCursorSecret` for backward-compat with existing test entry points; the comment now points at this file and at Lane 4 for envelope wrapping. A startup that wires `NewRouterWithDataStack(...)` provides its own loaded secret.
- `internal/api/cursor_secret_test.go` covers: valid load, env override precedence, missing file (`ErrCursorSecretUnavailable`), short key (`ErrCursorSecretWeak`), empty-after-trim, decode-with-different-secret (signature mismatch), tampered-signature, garbage-payload.

### Migrations (§0.3)

- `migrations/0001_alpha_core.up.sql` extended (still includes `schema_migrations`, `operations`, `jobs`, `idempotency_keys`, `api_tokens`, `hosts`, `host_heartbeats` so existing tests pass) and now adds `operation_events`, `projects`, `environments`, `services`, `timeline_events`, `audit_logs`, `audit_checkpoints`, plus the panel-locked job columns (`partition_key`, `priority`, `available_at`, `lease_owner`, `lease_expires_at`, `heartbeat_at`, `attempt`, `max_attempts`, `payload`) and idempotency columns (`actor`, `method`, `path_template`, `body_hash`, `state`, `operation_id`, `response_status`, `response_body`, `response_headers`, `expires_at`).
- Hot-path indexes: `jobs_dequeue_idx`, `jobs_reaper_idx`, `jobs_partition_key_idx`, `audit_logs_subject_idx`, `audit_logs_actor_idx`, `audit_logs_request_idx` — all use `state = '<literal>'` (lowercase) so the partial index predicate stays IMMUTABLE; the dequeue SQL still applies the `lease_expires_at` runtime predicate.
- `migrations/0001_alpha_core.down.sql` rewritten to drop the new tables in dependency order (audit → timeline → environments/services → projects → jobs/idempotency/operations → schema_migrations).
- `migrations/manifest.yaml` unchanged in shape; checksums are computed at runtime now (file bytes never travel through manifest YAML).

### Wired handlers (§0.4 / Lane 1 scope F)

`internal/api/router.go` was extended with `DataStack` + `NewRouterWithDataStack(...)`, the constructor a Postgres-aware caller uses. The original `NewRouter()` / `NewRouterWithOperationStore()` entry points still default to the in-memory stores, so router_test.go is unchanged.

Now live (handler maps to a Postgres-backed store via DataStack):

- operations get / list / cancel / events stream — already wired to `OperationStore`; Postgres twin satisfies the interface so wiring `DataStack.OperationStore = pg.NewPostgresOperationStore(...)` is the only switch needed.
- projects create / get / list — same pattern via `ProjectStore`.
- services create / get / list — same pattern via `ServiceStore`.
- environments list / get — new handler at `/api/v1alpha/environments` and `/environments/{id}` (gated by `DataStack.EnvironmentStore != nil`).
- audit list — new handler at `/api/v1alpha/audit` with `resource_kind` / `resource_id` / `actor_id` / `request_id` filters and `after_id` cursor.
- timeline list — new handler at `/api/v1alpha/timeline` with `deployment_id` / `text` filters and `<deployment_id>:<seq>` cursor.
- migrations list / run / down — new handler at `/api/v1alpha/migrations` (GET lists, POST `{"action":"up"}` or `{"action":"down"}` runs).
- idempotency replay — already implicit in every mutating handler via the existing `reserveIdempotencyOrAbort` helper; the Postgres store satisfies `idempotency.Store` so the replay path is end-to-end persistent.

### Tooling

- `Makefile`: new `db-integration` target (`go test -tags=integration -count=1 -timeout=10m ./internal/db/...`).
- `Taskfile.yml`: matching `db-integration` task.
- `tools/versions.txt`: bumped from `go=1.23+` to `go=1.25.0+` so the lint scaffold-guard matches the new `go.mod` directive (pgx v5.7.6 + testcontainers v0.35.0 transitively bring in `golang.org/x/sys` v0.42.0 which requires Go 1.25.0).

## What is deferred (and why)

### Lane 1 SCOPE — STRETCH (§G stretch goals)

| Item | Status | Reason |
|---|---|---|
| Hosts / host_heartbeats Postgres stores | Schema present, no Go store | Lane 2 owns the agent-side write path; the table already exists for them to consume. |
| `audit verify` CLI scaffold | Not added | Lane 4 owns `cmd/midnight/audit_verify.go` per the panel boundary. The `audit.Verify` math is in this lane's `chain.go` so the CLI just needs a thin shell. |
| Postgres-backed `release_evidence` and `events` partition handling | Not added | Out of MVP scope; Phase-0 release-evidence uses synthetic in-memory generation. Partition handling per SCHEMA_NOTES.md is a daily-pruning cleanup-class job that belongs to a later sprint. |

### Other §0.x and §1.x items intentionally NOT touched (other-lane territory)

| Item | Owner | Why deferred |
|---|---|---|
| Live Docker runtime (1.1) | Lane 2 | `internal/runtime/docker/`, `internal/runtime/local/`, `cmd/midnight-agent/` are out of scope. |
| BuildKit + registry GC (1.2 / 1.3) | Lane 3 | `internal/build/buildkit/`, `cmd/midnight-buildkit/`, `cmd/midnight-registry/` are out of scope. |
| Variables / secrets envelope encryption (1.4) | Lane 4 | `internal/secrets/` is Lane 4. The cursor-secret seam already documents the TODO referencing Lane 4. |
| Traefik file provider + ACME (1.5) | Lane 3 | `internal/router/traefik.go`, `internal/acme/` are out of scope. **Note:** Pre-existing failure in `internal/router/traefik_v2_test.go::TestRenderTraefikDynamicV2GoldenFiles` exists on `main`; not a Lane 1 regression. |
| GitHub App + signed webhooks (1.10) | Lane 4 | `internal/integrations/github/`, `internal/webhook/sign.go` are Lane 4. |
| Audit JSONL export + `audit_export.go` (1.11) | Lane 4 | `internal/audit/export.go` is Lane 4. The persistence + chain math + verify math are this lane's deliverable. |
| Agent ↔ control-plane network (1.16) | Lane 2 | Out of scope. |

### NotImplemented handlers that remain NotImplemented (per task allowance)

The user's task explicitly allows the following to stay deferred and listed here:

- billing endpoints (`internal/api/billing.go` family) — Phase 4.
- dashboard endpoints — Phase 2.
- enterprise identity / audit / controls — Phase 4.
- isolation / build plan — Phase 4.
- release / disconnected-bundle — Phase 4.
- routing-HA readiness — Phase 4.
- config-code import / export — Phase 2.

These were not added to the new router seam; they remain on whatever scaffold path `internal/api/router.go` uses today. The DataStack constructor leaves room for adding them without further interface changes.

## Open questions for PM-A / PM-B

1. **`go.mod` Go directive.** pgx v5.7.6 + testcontainers v0.35.0 transitively require Go 1.25.0 (via `golang.org/x/sys` v0.42.0). The original `go.mod` was at 1.23. Confirm OK to bump to 1.25.0 — alternative is to pin pgx to v5.6.0 and testcontainers to v0.30.0 (older but works on 1.22). I chose to bump rather than pin to ancient versions because (a) the lint scaffold guard already cross-checks `tools/versions.txt`, (b) modern toolchains are available everywhere we run CI, and (c) the panel-locked SCHEMA_NOTES does not pin a Go version.

2. **`MIGRATION_CHECKSUM_PLACEHOLDER` issue code semantics.** The current `BuildSchemaEvidence` path flags any non-empty `UpChecksum` as a placeholder (because in Phase 0 the only legal value was the literal placeholder). With real SHA-256 hashes flowing through, the existing semantics still work because the migration manifest YAML does not carry checksums (real hashes are in `schema_migrations.up_sha256`, computed by the runner). The synthetic `0002_beta_core.up.sql` references in `internal/api/router.go` demo handlers (lines ~558, ~597, ~651) still use `sha256:placeholder` literally because that file does not exist. I left those as-is. Confirm whether you want me to fold the real-vs-placeholder distinction into `BuildSchemaEvidence` so the demo can be updated too — it is a low-risk follow-up but would require updating the corresponding `TestRouterServesReleaseEvidenceDemo` expectations.

3. **`idempotency_keys.operation_id` foreign key.** SCHEMA_PHASE_0_ALPHA.sql declares this as `DEFERRABLE INITIALLY DEFERRED`. The Lane 1 migration declares it as a plain `REFERENCES operations(id) ON DELETE SET NULL`. The two semantics differ when the response_operation_id is set after the row insert (the panel-locked schema lets a single tx insert the idempotency row first, then the operation, then update the FK). Confirm whether the simpler ON DELETE SET NULL semantic is acceptable for Phase 1; if not, I'll change the migration to add the FK as a separate `ALTER TABLE` step with the deferred clause.

4. **Cursor secret rotation.** §0.2 said "rotation supported." This lane wires the load-once seam, but rotation is a service-restart event for now (Lane 4's envelope encryption brings KEK rotation, which is the natural rotation surface). Confirm rotation is OK to defer to Lane 4.

## Final summary

- **Branch:** `phase1/lane1-data`.
- **New dependencies (in `go.mod`):**
  - `github.com/jackc/pgx/v5` v5.7.6 (direct).
  - `github.com/testcontainers/testcontainers-go` v0.35.0 (direct, integration-test only).
  - `github.com/testcontainers/testcontainers-go/modules/postgres` v0.35.0 (direct).
  - Plus their transitive set (puddle, pgpassfile, x/text, otel, docker, moby/sys, opencontainers, gopsutil, …) — all standard.
  - Go directive bumped to 1.25.0 (see open question 1).
- **Migration files added/changed:**
  - `migrations/0001_alpha_core.up.sql` — extended with the resource graph, audit chain, operation events, panel-locked job/idempotency columns, hot-path indexes.
  - `migrations/0001_alpha_core.down.sql` — rewritten to drop new tables in dependency order.
  - `migrations/embed.go` — new (embed.FS).
  - `migrations/manifest.yaml` — unchanged (checksums are runtime).
- **Handlers now live (Postgres-backed when DataStack is wired):** operations get/list/cancel/stream, project create/get/list, service create/get/list, environments list/get, audit list, timeline list, migrations list/run/down, idempotency replay (implicit via every mutating handler).
- **Handlers still NotImplemented (deferred):** billing, dashboard, enterprise, isolation/build plan, release/disconnected-bundle, routing-HA readiness, config-code import/export. (Other-phase scope.)
