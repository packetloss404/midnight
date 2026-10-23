# Phase 1 / Lane 4 (Security + Audit) — Status

- **Branch:** `phase1/lane4-security`
- **Worktree:** `.worktrees/phase1-lane4-security`
- **Base:** `main` @ `2a64283 feat: phase 1 — promote scaffolds toward functional implementations`
- **Status:** Implementation complete, **left on the branch for human review**. Not pushed. No PR opened.

## What landed

### A. Dependencies
- **No new third-party Go dependencies added.** The implementation uses
  stdlib `crypto/aes`, `crypto/cipher`, `crypto/rand`, `crypto/ed25519`,
  `crypto/hmac`, `crypto/sha256`, `crypto/subtle` (via `hmac.Equal`).
- The brief offered `golang.org/x/crypto` (HKDF/scrypt) and
  `github.com/google/go-github/v62`. Both deferred:
  - HKDF/scrypt are not panel-required for envelope encryption at MVP
    Beta — AES-GCM with crypto/rand-derived nonces matches the panel-
    locked spec.
  - `go-github` would be needed for the live GitHub combined-status
    call. It is gated behind a build tag; the package ships a
    `StubStatusChecker` that returns `ErrLiveGitHubDisabled` so default
    `go test ./...` runs require zero network access.

### B. Envelope encryption (§1.4)
New package `internal/secrets/`:

- `KeyProvider` interface (`internal/secrets/keyprovider.go`) — panel-
  locked boundary the eventual AWS KMS / GCP KMS / Vault adapters will
  satisfy.
- `MemoryKeyProvider` + `NewFileKeyProvider` — default file-backed
  provider reading from `/etc/midnight/kek` (or `MIDNIGHT_KEK_PATH`).
- AES-256-GCM seal/open with 96-bit `crypto/rand` nonces and the
  variable id bound into AAD (`internal/secrets/aesgcm.go`,
  `envelope.go`). Nonce-reuse is structurally impossible per call.
- `Vault` (`internal/secrets/vault.go`) — in-memory primary store of
  envelopes, redaction-safe metadata (length bucket, version,
  last_rotated_at, KEK fingerprint), Set/Get/Decrypt/Rotate/List.
  Generates a `RevealToken` typed by panel-locked injection scope so
  sealed values cannot be revealed by API surfaces.
- `SealedVariable` (`internal/secrets/sealed.go`) — write-once shape
  whose `Reveal()` requires a Vault-issued reveal token; `String()` is
  redacted; `PublicJSON()` returns metadata only.
- `CreateReference` / `ResolveReference` (`internal/secrets/reference.go`)
  enforce cross-scope authorization: `AllowsTierEdge` first
  (preview→prod permanently denied), then `CanRead`. Cycles surface
  `ErrReferenceCycle`. Secrecy propagation: a reference to a sealed
  target is itself sealed.
- `RotateKEK` (`internal/secrets/rotation.go`) re-wraps every persisted
  envelope under the new KEK; previous KEK retained per
  `MemoryKeyProvider` graceTicks; rotation report is redaction-safe and
  callers emit it as a single audit row.

Tests cover:
- AES-GCM round-trip, nonce uniqueness across 1024 calls, tamper
  rejection, AAD swap rejection (`aesgcm_test.go`).
- KEK file mode/length/missing handling, fingerprint stability per
  generation (`keyprovider_test.go`).
- Vault round-trip, `LengthBucket` bucketing, sealed no-reveal, sealed
  rotate-no-view, **encrypted-disk plaintext sweep**, **KEK rotation
  re-wraps every envelope and decrypt still works**, **reference cross-
  scope authz denies cross-tier and missing-read**, **resolution-time
  consumer-principal authorization re-check**, cycle detection
  (`vault_test.go`).

### C. Postgres-backed audit chain export + verify (§1.11)
New package `internal/audit/`:

- `Row` / `Checkpoint` schemas and JSONL record discriminator
  (`internal/audit/record.go`).
- Deterministic canonical-row encoding tolerant of JSON roundtrips
  (int↔float normalization, sorted keys, RFC-3339-Nano UTC timestamps;
  prev/row/checkpoint fields excluded by definition)
  (`internal/audit/canonical.go`).
- `ComputeRowHash` = SHA-256(prev_hash || canonical_row); panel-locked
  `GenesisHash()` = SHA-256("midnight-audit-v1") (`chain.go`).
- `AuditChain` interface + `MemoryChain` Lane 4 implementation with
  per-chain head mutex; `Tamper` + `DeleteSequence` test-only hooks
  (`store.go`).
- Ed25519 signer/verifier with PEM file format; canonical signed input
  per FEATURE_AUDIT_HASH_CHAIN.md `keys.go`).
- `Checkpointer.Sign` produces a daily checkpoint over the chain head
  (`checkpoint.go`).
- `Verify(ctx, source, keys)` walks rows, recomputes hashes, validates
  every checkpoint signature, returns a typed `VerifyReport` with
  `OutcomeOK` / `OutcomeWarn` / `OutcomeChainBreak` /
  `OutcomeSignatureBreak` (`verify.go`).
- Streaming JSONL `Exporter` with trailing manifest line (`record_kind`
  discriminator, `stream_sha256`, `signing_key_fingerprint`); `Source`
  adapters for live chain + parsed JSONL (`export.go`).
- `Destination` interface — `file://`, `memory://`, plus stub for
  `s3://` / `gcs://` / `sftp://` / `azure://` returning
  `ErrDestinationNotSupported` with a TODO to consolidate with
  `internal/backup` Destination (`export.go`).
- `DrainPlan` validator (`drain_plan.go`) the eventual Phase 2 §2.10
  Splunk HEC / Elastic drain consumes.

CLI: new files
- `cmd/midnight/audit_dispatch.go` (subcommand router, package main)
- `cmd/midnight/audit_verify.go` (the `audit verify` subcommand)
- `cmd/midnight/main.go` extended to dispatch `audit` (only `main.go`
  calls `os.Exit`, per the source-guard).

`midnight audit verify`:
- `--from-jsonl PATH` reads a JSONL export.
- `--from-db URL` is documented but stubs back to
  `EX_NOINPUT` (66) with a pointer to this status doc — Lane 1 wires
  the live Postgres source; Lane 4 ships the `--from-jsonl` path.
- `--public-key FILE` and `--public-key-dir DIR` (rotation overlap).
- `--json` renders the `VerifyReport` as JSON.
- Documented exit codes (see "P0-D017 closure proposal" below).

Tamper test (`cmd/midnight/audit_verify_test.go`): produces a chain,
mutates row 2, runs `audit verify`, asserts exit code 2 with row 2
identified in the report.

### D. Signed webhooks + GitHub App (§1.10)
New / extended packages:

- `internal/cloudevents/` — CloudEvents v1.0 envelope encode/decode
  with the panel-locked `traceparent`, `midnightrequestid`,
  `midnighttenantid` extensions and the `dev.midnight.*` namespace
  guard.
- `internal/webhook/sign.go` — outbound `Midnight-Signature: t=,v1=,
  kid=` signer (multi-`v1` rotation overlap), `ParseSignature`,
  `VerifySignature` (HMAC + ±300s replay window, constant-time),
  GitHub `X-Hub-Signature-256` parser.
- `internal/webhook/dedupe.go` — `(provider, delivery_id, event_hash)`
  unique-tuple in-memory dedupe store mirroring the panel-locked
  `webhook_deliveries` UNIQUE index.
- `internal/webhook/receive.go` — `Receiver.HandleInbound(provider)`:
  signature → replay → dedupe → CloudEvents decode → emit audit row →
  dispatch. Disabled endpoints return `410 Gone` with
  `WEBHOOK_ENDPOINT_DISABLED`.
- `internal/integrations/github/manifest.go` — GitHub App manifest
  template + validator that rejects banned permissions
  (administration / workflows / secrets / broad org) and asserts the
  panel-locked event subscription set.
- `internal/integrations/github/push_event.go` — push-event parser +
  `PushEventToDeploy` mapping; `APIClient` interface (default `Stub`
  returns `ErrLiveAPIDisabled`).
- `internal/integrations/github/status_check.go` — `StatusChecker` +
  `RequiredCheckPolicy` + `Gate.Evaluate(ctx, repo, sha)` returning a
  typed `Decision` with `DEPLOY_GATE_PENDING_CHECKS` reason code. Live
  GitHub calls are stubbed; `FixtureStatusChecker` is the test seam.
- `internal/promotion/locks.go` — `Locks.TryAcquire` /
  `Locks.Release` with `ErrLockHeld` (matches `errors.Is(
  ErrEnvironmentLocked)`) and audit emit on every transition.
- `internal/promotion/promotions.go` — append-only `Ledger` mirroring
  the `artifact_promotions` migration columns; validates required
  fields and the `gate_result` enum.
- New migrations:
  - `migrations/0002_environment_locks.up.sql` /
    `0002_environment_locks.down.sql`
  - `migrations/0003_artifact_promotions.up.sql` /
    `0003_artifact_promotions.down.sql`
  - `migrations/manifest.yaml` updated to register both versions
    (manifest had to be touched for the runner to pick the new
    migrations up; existing `0001_alpha_core` files are NOT modified).
- `internal/api/webhook_handlers.go` — `RegisterWebhookHandlers(mux,
  WebhookHandlerOptions{Receiver})` registers the four panel-locked
  inbound paths (`github`, `gitlab-generic`, `bitbucket`,
  `midnight-incoming`). A `TODO(lane-1)` at the bottom of the file
  signposts where Lane 1 wires this from `router.go`. **`router.go` is
  intentionally NOT edited**.

Tests cover:
- Sign/verify round-trip, replay window (±301s), rotation overlap with
  two valid keys (`sign_test.go`).
- Receiver: accept-and-emit-audit, replay (second delivery returns
  200 + `Idempotent-Replayed: true`, no re-dispatch), bad signature →
  `WEBHOOK_SIGNATURE_INVALID`, replay-window violation →
  `WEBHOOK_REPLAY_REJECTED`, disabled endpoint → 410, GitHub
  `X-Hub-Signature-256` accepted (`receive_test.go`).
- Manifest validates default, rejects banned permissions, requires
  panel events; render is deterministic; push-event parser surfaces
  fork→untrusted; required-check policy covers all-success / pending-
  missing / failed branches (`github_test.go`).
- Environment lock: single winner under N concurrent goroutines;
  release wrong-operation rejected; promotion ledger validation
  (`locks_test.go`).
- API mux registration: panel-locked path dispatches to the receiver;
  nil receiver does not panic (`webhook_handlers_test.go`).

## Cross-platform fix (.gitattributes)

Phase 0's repo had `core.autocrlf=true` but no `.gitattributes`. On
Windows clones the testdata yaml fixtures
(`internal/router/testdata/*.yaml`) checked out as CRLF, which broke
`TestRenderTraefikDynamicV2GoldenFiles` against the LF-only output of
the renderer. The failure pre-existed Lane 4 (verified via
`git stash; go test`).

Lane 4 added a workspace-wide `.gitattributes` pinning Go sources, YAML,
JSON, SQL, and Markdown to LF. The fix is workspace-wide and does not
modify any Lane 3 source files (`internal/router/traefik.go`).

## P0-D017 closure proposal

PHASE0_DECISION_LOG.md item P0-D017 is "deferred to the audit verifier
implementation slice". Lane 4 ships the verifier and **proposes to
close P0-D017** with the following exit-code envelope, which aligns
with the panel decision in **ADR-011 §"Decision" item 6** and
**FEATURE_AUDIT_HASH_CHAIN.md §"Verify Algorithm" step 6**:

| Code | Meaning | Rationale |
|------|---------|-----------|
| 0    | healthy | every row hash matches, every checkpoint signature verifies. |
| 1    | warnings only | non-contiguous `prev_checkpoint_id` (acceptable on partition pruning per spec). |
| 2    | chain mismatch / tamper detected | row_hash recomputation mismatch, prev_hash mismatch (delete/reorder), or checkpoint last_row_hash mismatch. |
| 3    | signature failure or signing key missing | `AUDIT_CHECKPOINT_SIGNATURE_INVALID` or `AUDIT_KEY_NOT_FOUND`. |
| 64   | usage error (sysexits EX_USAGE) | bad flags, conflicting flags, unknown subcommand. |
| 66   | missing input (sysexits EX_NOINPUT) | `--from-jsonl` path not found; `--from-db` invoked before Lane 1 wires the live Postgres source. |

The brief's defaults ("0=ok, 1=usage, 2=mismatch, 3=missing") were
overridden because **midnight-platform-plan.md panel decisions said
otherwise** (the brief explicitly carves an "unless ..." escape):
ADR-011 reserves `1` for warnings and `3` for signature/key failure.
Usage and missing-input collide if we keep `1`/`3` as panel-locked, so
Lane 4 routes those to the sysexits codes 64/66. CI invocations and
operator scripts dispatch on the panel-locked codes only; sysexits
codes are documented but secondary.

If PM-B/Security disagree, the easiest alternative is to collapse 64/66
into 1 (since "warnings only" is a soft outcome and a usage error is
also non-tamper), but that loses the ADR-locked semantic of `1`. We
recommend keeping the proposal above.

## Stretch items — explicitly deferred

- **GitLab webhook (§2.5)** — handler wiring is provider-agnostic and
  the receiver routes a `gitlab-generic` provider, but the GitLab-
  specific event parser is not implemented. Deferred to a Phase 2
  story.
- **AWS KMS / GCP KMS / Vault adapters (Phase 4)** — the `KeyProvider`
  interface is the boundary; `MemoryKeyProvider` is the only
  implementation Lane 4 ships.
- **Audit drain to Splunk HEC / Elastic (Phase 2 §2.10)** — `DrainPlan`
  validator ships; the live drain worker does not.
- **`midnight audit export` CLI subcommand** — the
  `internal/audit.Exporter` is the API; a CLI wrapper is straightforward
  but not in the brief's "minimum must land" list. Lane 1 may add
  `cmd/midnight audit export` when it lands the live Postgres source.
- **Live `--from-db` audit verify** — Lane 1 owns the Postgres
  `AuditChain` implementation; Lane 4's `--from-jsonl` path is the
  panel-locked floor.
- **Audit emit hooks on the live API mux** — Lane 1 wires the receiver
  audit sink + dispatcher dependencies from `router.go`; Lane 4 ships
  the `RegisterWebhookHandlers(mux, opts)` boundary plus a TODO
  marker.
- **Failure code registry additions** (`AUDIT_CHECKPOINT_SIGNATURE_INVALID`,
  `AUDIT_KEY_NOT_FOUND`, `WEBHOOK_ENDPOINT_DISABLED`,
  `DEPLOY_GATE_PENDING_CHECKS`, `VAR_REF_CREATE_FORBIDDEN`,
  `VARIABLE_REFERENCE_FAILED`, `VAR_REFERENCE_CYCLE`) — left to a
  follow-up because adding constants requires updating
  `docs/error-codes.md` (the lint guard enforces alphabetical order).
  The codes are referenced in code comments and test assertions; the
  registry update is mechanical. Open question for PM-B / Security
  panel: confirm code spelling before the registry add.

## Open questions for PM-A / PM-B

1. **P0-D017 closure**: confirm the exit-code envelope above matches
   panel intent. (See "P0-D017 closure proposal".)
2. **Failure-code registry**: confirm spelling for the 7 new codes
   listed under "Stretch items — explicitly deferred". The lint
   registry guard requires `docs/error-codes.md` rows in alphabetical
   order; staging the update against typos costs a re-roll.
3. **Cross-tier matrix policy seams**: the panel-locked default-deny
   matrix (Sprint 9 VAR-905) is enforced via `DenyByDefault`. Is the
   per-(actor, src→dst) allowlist the right surface, or should Phase 1
   instead expose a workspace-policy table that the resolver
   consults? Lane 4 ships the smaller allowlist; PM-B should confirm
   before Phase 4 RBAC binds to a different shape.
4. **`webhook_deliveries` migration**: Lane 4 exposes the in-memory
   dedupe and emits audit rows but **does not add a migration** for
   `webhook_deliveries` because Lane 1 owns the audit / data plane
   migrations. Lane 1: please add `webhook_deliveries(provider,
   delivery_id, event_hash) UNIQUE` plus the panel-locked attempt-
   history columns, then swap the Receiver's `MemoryDedupeStore` for
   the Postgres-backed one.
5. **`audit_logs` + `audit_checkpoints` migrations**: Lane 4 ships the
   chain math, exporter, verifier, and Ed25519 plumbing, but the
   Postgres tables (with the panel-locked monthly partitioning,
   advisory lock semantics, and role grants) are Lane 1's. The
   `AuditChain` interface in `internal/audit/chain.go` is the seam.
6. **KEK file mode on Windows**: Lane 4's `LoadKEKMaterial` accepts
   either `0400` or "POSIX-mode-not-honored" (mode == 0) so Windows
   developer hosts work. Production deploy on Linux MUST set `0400`
   per panel decision; the install runbook is Lane 1 / Lane 2's.

## How to verify this branch

```pwsh
cd D:/projects/midnight/.worktrees/phase1-lane4-security
go fmt ./...
go vet ./...
go build ./...
go test ./...
go run ./tools/lint
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

All six commands exit 0.

## Acceptance bar — evidence

| Acceptance criterion | Where it is proved |
|---|---|
| `go build ./...` passes | `go build ./...` exit 0 (above) |
| `go test ./...` passes | `go test ./...` exit 0 (above) |
| KEK rotation re-wraps + decrypts; no plaintext on disk | `internal/secrets/vault_test.go` `TestKEKRotationProducesNewWrap`, `TestVaultEncryptedDiskHasNoPlaintext` |
| Sealed: set, read returns redacted, re-reveal fails | `internal/secrets/vault_test.go` `TestSealedVariableNoReveal`, `TestSealedVariableRotateNoView` |
| Reference cross-scope authz denied | `internal/secrets/vault_test.go` `TestReferenceCrossScopeAuthzDeniedByDefault`, `TestReferenceResolutionRequiresConsumerRead` |
| Tamper test exits 2 with offending row | `cmd/midnight/audit_verify_test.go` `TestRunAuditVerifyChainBreakExit2` |
| Replay rejected; valid delivery emits audit + creates op | `internal/webhook/receive_test.go` `TestReceiverAcceptsValidDeliveryEmitsAudit`, `TestReceiverRejectsReplayedDelivery`, `TestReceiverRejectsReplayWindowViolation` |
| Concurrent env-lock: one winner, loser typed reason | `internal/promotion/locks_test.go` `TestEnvironmentLockSingleHolder`, `TestEnvironmentLockConcurrentAcquireSingleWinner` |

## Final summary
- **Branch:** `phase1/lane4-security`
- **New deps:** none (stdlib only).
- **New migrations:** `0002_environment_locks`, `0003_artifact_promotions`.
- **New commands/handlers:** `midnight audit verify` subcommand;
  `RegisterWebhookHandlers(mux, opts)` registering 4 inbound provider
  paths.
- **P0-D017 closure proposal:** see table above (0/1/2/3 panel-locked;
  64/66 sysexits for usage / missing input). Open for PM-B sign-off.
