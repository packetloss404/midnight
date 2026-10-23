# Phase 1 Lane Merge — Reconciliation Notes

This file records the non-obvious conflict-resolution choices made
when integrating the four Phase 1 lane branches into `main`. It is
written by the merge agent and is meant for human review during the
post-integration walk-through.

## Lane 1 (DATA) — `phase1/lane1-data`

- **Branch state at merge time:** lane 1's worktree shipped its
  changes uncommitted (per LANE1_STATUS.md "Commits: none. Changes
  are left uncommitted on the branch for human review."). The merge
  agent committed every staged + untracked file from the worktree
  in a single squash-style commit on the branch with the message
  `feat(phase1/lane1-data): Postgres persistence + cursor signing +
  DataStack router`, drawn directly from the LANE1_STATUS.md
  acceptance bar.
- **Merge:** clean three-way merge into `main`; no conflicts.
- **Follow-up chore commit on `main`:** `chore(lint): add .worktrees
  to scaffold deny list and .gitignore` — the lint scaffold guard
  walks every top-level directory on disk and requires a CODEOWNERS
  rule. The Phase 1 integration plan keeps three sibling worktrees
  at `.worktrees/phase1-laneN-*` for the duration of the merge
  series, so `.worktrees/` is now a top-level directory the lint
  needs to know about. Adding it to `codeownersTopLevelDenyList`
  matches the existing handling of `.claude/` (agent harness state,
  same category). `.worktrees/` was also added to `.gitignore`.

## Lane 4 (SECURITY + AUDIT) — `phase1/lane4-security`

- **Conflict files:**
  - `internal/audit/chain.go` (add/add)
  - `internal/audit/chain_test.go` (add/add)
  - `internal/audit/store.go` (add/add)
- **Resolution:** **take Lane 4's content for all three.**
- **Why:** Lane 1 wrote against an earlier draft of the audit
  package (a single `Store` interface with `Append`/`List` and an
  in-process `MemoryStore` plus a Postgres-backed `PostgresStore`).
  Lane 4's content is the panel-locked design — explicitly
  documented in LANE4_STATUS.md §C and Open Question 5: "the
  `AuditChain` interface in `internal/audit/chain.go` is the seam"
  Lane 1 is meant to implement against. Lane 4 also splits the
  package into `record.go` (Row/Checkpoint/JSONLRecord),
  `canonical.go` (RFC-8785-style canonical encoder),
  `verify.go` (`VerifyReport` with OutcomeOK / OutcomeWarn /
  OutcomeChainBreak / OutcomeSignatureBreak), `keys.go` (Ed25519
  PEM signer/verifier), `checkpoint.go`, `export.go` (streaming
  JSONL exporter + Destination interface), and `drain_plan.go`.
  Lane 1's narrower draft cannot coexist with Lane 4's canonical
  design without breaking the panel contract.
- **Adapter work to keep Lane 1's API surface alive:**
  - `internal/api/handlers_phase1.go`: introduced an
    API-package-local `AuditFilter` struct with the resource /
    actor / request / after_id filter fields, and renamed
    `AuditQueryStore.List` to `ListAudit(ctx, AuditFilter)` to
    avoid colliding with the upstream `audit.Query` shape. The
    `auditRowView` JSON tag `id` is preserved (it now reads from
    `audit.Row.Sequence`). `Before`/`After` payload fields are
    serialized from Lane 4's `map[string]any` back to
    `json.RawMessage` for wire compatibility via a small
    `marshalAuditPayload` helper.
  - `internal/db/pg/integration_test.go`: dropped
    `TestPostgresAuditStoreChainPersistsAcrossRestart` (it was
    written against Lane 1's removed `audit.PostgresStore`). The
    test was integration-tagged and never in the default
    `go test ./...` path, so dropping it is non-disruptive. A
    Postgres-backed `audit.AuditChain` is the natural follow-up;
    when it lands, the test should be reinstated against the new
    interface.
- **`.gitattributes` post-merge step:** Lane 4 added a
  workspace-wide `.gitattributes` pinning Go / YAML / JSON / SQL /
  MD files to LF. The committed `.gitattributes` only governs
  *future* checkouts, so the existing `internal/router/testdata/`
  YAML fixtures still had CRLF endings on disk from the original
  Phase 0 checkout (the same CRLF/LF mismatch that was failing
  `TestRenderTraefikDynamicV2GoldenFiles` before Lane 4 landed).
  After the Lane 4 merge commit, the testdata fixtures were
  re-checked-out via `git rm -r --cached internal/router/testdata/`
  followed by `git checkout HEAD -- internal/router/testdata/` so
  the .gitattributes LF rule applied. The
  `TestRenderTraefikDynamicV2GoldenFiles` golden test is now green
  on Windows.
- **`go.mod` / `go.sum`:** Lane 4 added no new third-party Go
  dependencies (stdlib crypto only). The Go directive remains at
  1.25.0 (set by Lane 1).
- **`migrations/manifest.yaml`:** Lane 4 added two new entries
  (`0002_environment_locks` and `0003_artifact_promotions`); the
  manifest now lists three migrations in version order.
  `migrations/0001_alpha_core.up.sql` was untouched by Lane 4 (per
  LANE4_STATUS.md "0001_alpha_core untouched"), so the merge
  preserved Lane 1's extended schema.

## Lane 2 (RUNTIME) — `phase1/lane2-runtime`

- **Conflict files:**
  - `go.mod` (content)
  - `go.sum` (content)
  - `migrations/manifest.yaml` (content)
- **Resolution:**
  - `go.mod` / `go.sum`: kept every direct dependency from both
    branches and ran `go mod tidy` to reconcile indirects. Final
    direct require set:
    - `github.com/docker/docker v28.5.2+incompatible`
    - `github.com/docker/go-connections v0.7.0`
    - `github.com/jackc/pgx/v5 v5.7.6`
    - `github.com/testcontainers/testcontainers-go v0.35.0`
    - `github.com/testcontainers/testcontainers-go/modules/postgres v0.35.0`
    - `golang.org/x/net v0.53.0`
    - Go directive: `1.25.0` (unchanged; both lanes converged here).
  - `migrations/manifest.yaml`: Lane 2 had numbered its agents
    schema as version 2 (`0002_agents.{up,down}.sql`), but Lane 4
    had already taken versions 2 (`environment_locks`) and 3
    (`artifact_promotions`). The agents migration was **renumbered
    to version 4** and the files **renamed via `git mv`** from
    `0002_agents.{up,down}.sql` to `0004_agents.{up,down}.sql`.
    Final manifest lists all four migrations in version order
    with `schema_version: 4`. `migrations/embed.go` is
    filename-pattern based, so the rename is transparent to the
    migration runner.
- **`tools/versions.txt`:** auto-merged with no conflict; Lane 2's
  value (`go=1.25.0+`) matched the post-Lane-1 line.

## Lane 3 (BUILD/INGRESS) — `phase1/lane3-build-ingress`

- **Conflict files:**
  - `go.mod` (content)
  - `go.sum` (content)
  - `versions.yaml` (content)
- **Resolution:**
  - `versions.yaml`: kept Lane 3's `distribution: v3.0.0`,
    `traefik: v3.1.5`, `lego: v4.17.4` AND Lane 2's
    `docker_engine_api_min: "1.43"` / `docker_engine_min: "24.0"`.
    All pins now coexist under the existing `buildkit:` line; the
    selection-rationale header that Lane 3 added is preserved.
  - `go.mod` / `go.sum`: combined every direct dependency from all
    branches and ran `go mod tidy`. Final direct require set:
    - `github.com/docker/docker v28.5.2+incompatible`
    - `github.com/docker/go-connections v0.7.0`
    - `github.com/go-acme/lego/v4 v4.17.4`
    - `github.com/jackc/pgx/v5 v5.7.6`
    - `github.com/testcontainers/testcontainers-go v0.35.0`
    - `github.com/testcontainers/testcontainers-go/modules/postgres v0.35.0`
    - `golang.org/x/net v0.53.0`
    - Go directive: `1.25.0`. Lane 3's branch was at `go 1.23` but
      the integration plan specifies the highest of all four lanes,
      which is Lane 1's `1.25.0` bump (driven by pgx + testcontainers
      transitive `golang.org/x/sys v0.42.0` requirement).
- **`.gitignore`**: clean three-way auto-merge — Lane 3's binary-name
  patterns (`/midnight`, `/midnight.exe`, `/midnight-*`,
  `/midnight-*.exe`) coexist with the
  `chore(lint): add .worktrees to scaffold deny list and .gitignore`
  commit's `.worktrees/` entry.
- **`internal/router/traefik_v2_test.go`**: the per-test newline
  normalization Lane 3 added is preserved. The structural CRLF fix
  (workspace-wide `.gitattributes` from Lane 4) and the testdata
  re-checkout that applied it were already in place from the Lane 4
  merge; Lane 3's small tweak doesn't conflict.
