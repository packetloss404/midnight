# Phase 0 Decision Log

This log captures decisions that must be closed before affected Phase 0 worker subagents implement code. PM-A owns delivery follow-up. PM-B owns release-risk impact and accepted-risk governance.

Status values: `open`, `decided`, `deferred`, `superseded`.

| ID | Decision | Impacted lanes | Default recommendation | Owner | Due | Status |
|---|---|---|---|---|---|---|
| P0-D001 | Canonical product repo/source of truth and GitHub bootstrap path. | S01-S08 | Treat this workspace as the planning repo until S01 creates the product scaffold; record remote/protection details when available. | PM-A / S01 | Before S01 PR 1 | decided |
| P0-D002 | Web package manager for v0. | S01, S06, S08 | Use npm and `package-lock.json` v3 for Phase 0; revisit pnpm after v0. | PM-A / S01 | Before web scaffold | decided |
| P0-D003 | Binary names for BuildKit and registry. | S01, S02, S05, S08 | Use `midnight-buildkit`; keep registry embedded unless a standalone `midnight-registry` binary is explicitly required. | PM-A / S01 | Before packaging scaffold | decided |
| P0-D004 | Release-signing key custody path. | S01, S02, S05, S08 | Test keys for `dev` channel CI; offline-or-KMS for `alpha`/`beta`; KMS-required for `stable`/`enterprise-lts` (see `agile/handoff/governance/RELEASE_KEY_CUSTODY.md`). | PM-B / Security | Before protected signing | decided |
| P0-D005 | Canonical doctor/preflight check IDs. | S02, S07, S08 | Create one registry before implementing checks; prevent renames by CI. | PM-A / S02 | Before doctor code | decided |
| P0-D006 | Docker `daemon.json` `init` baseline. | S02, S04, S08 | Prefer per-container `HostConfig.Init=true`; do not require daemon-level `init:true` unless ADR confirms. | PM-A / S02/S04 | Before daemon merge tests | decided |
| P0-D007 | API token classes and token entropy claim. | S03, S04, S06, S08 | Align schema with public token classes or add explicit mapping; fix 26-base32 entropy claim. | PM-A / S03 | Before auth codegen | decided |
| P0-D008 | Bootstrap endpoint path. | S02, S03, S08 | Choose one `/api/v1alpha` path and update OpenAPI, sprint references, and Pact fixtures. | PM-A / S03 | Before auth handlers | decided |
| P0-D009 | OpenAPI artifact path. | S01, S03, S08 | Use one release artifact path and serve it byte-for-byte at runtime. | PM-A / S03 | Before contract CI | decided |
| P0-D010 | Environment tier spelling and resource prefix taxonomy. | S03, S06, S08 | Freeze public API spelling; map internally if needed. Expand/hide helper prefixes intentionally. | PM-A / S03 | Before DTO generation | decided |
| P0-D011 | Invalid Postgres partial index using `now()`. | S03, S05, S08 | Fix migration source before applying schema. | PM-A / S03 | Before migration PR | decided |
| P0-D012 | Agent desired-state transport. | S03, S04, S08 | Use REST polling behind an interface for Phase 0; keep proto shapes future-compatible. | PM-A / S03/S04 | Before reconciler | decided |
| P0-D013 | Traefik dynamic writer ownership. | S04, S06, S08 | S04 reports route targets/freshness; S06 owns all Traefik file writes. | PM-A / S04/S06 | Before route code | decided |
| P0-D014 | Health probe runner ownership. | S04, S06, S08 | S04 owns agent-side probes; S06 owns timeline and route-readiness semantics. | PM-A / S04/S06 | Before healthcheck implementation | decided |
| P0-D015 | Custom-domain HTTP-01 Phase 0 scope. | S06, S07, S08 | Generated apps-domain is public Phase 0 scope; custom-domain HTTP-01 is hidden/admin-only primitive unless re-scoped. | PM-B / Product | Before S06 UI/CLI | decided |
| P0-D016 | Phase 0 backup boundary. | S07, S08 | Local platform backup/restore is required; external off-host providers are non-blocking unless re-scoped. `BACKUP_OFFHOST_MISSING` warning/alert is required. | PM-B / Product/SRE | Before backup provider code | decided |
| P0-D017 | Audit verify phase, canonical row, and exit codes. | S03, S07, S08, Security | Ratify Lane 4 envelope: `0` healthy / `1` warnings / `2` chain mismatch / `3` signature failure (panel-locked per ADR-011); `64` usage (`EX_USAGE`) / `66` missing input (`EX_NOINPUT`) sysexits-aligned. See `agile/handoff/governance/AUDIT_VERIFY_EXIT_CODES.md`. | PM-B / Security/S03/S07 | Before audit verify | decided |
| P0-D018 | Upgrade gate scope in Phase 0. | S02, S03, S08 | Gate schema idempotence and reinstall/smoke in Alpha; full transaction remains MVP Beta unless re-scoped. | PM-B / Release | Before S08 gate manifest | decided |
| P0-D019 | Webhook duplicate drill scope. | S03, S06, S08 | Gate only prototype HMAC + delivery-id/event-hash dedupe; no GitHub App product claim. | PM-B / Product | Before D-08 | decided |
| P0-D020 | Volume read-only remount drill scope. | S04, S07, S08 | Use loopback/platform fixture for classifier behavior; no tenant volume product claim. | PM-B / Product/SRE | Before D-17 | decided |
| P0-D021 | Test/drill root path. | S01, S08 | Use `test/drills/` to match quality strategy unless an ADR chooses `tests/drills/`. | PM-A / S08 | Before harness PR | decided |
| P0-D022 | Phase 0 design-partner signoff counts. | PM-B, S08 | Ratify pre-locked counts: `v0.1.0-alpha` = 2 install signoffs; `v0.2.0-alpha` = 3 first-product-loop signoffs; `v0.3.0-alpha` = 5 first-product-loop signoffs + ≥1 backup/restore drill. Per-partner template, aggregation contract, reviewer matrix, and stale-after-90-days lifecycle in `agile/handoff/governance/DESIGN_PARTNER_SIGNOFF_TEMPLATE.md`. | PM-B / Product | Before first alpha tag | decided |

## Phase 0 Closure Resolution

Phase 0 is closed on local release evidence, not live infrastructure execution. PM-A and PM-B accept local adapters for Phase 0 gate completion when they are deterministic, side-effect safe by default, and covered by release evidence under `artifacts/phase0`.

- `P0-D001`, `P0-D002`, `P0-D003`, `P0-D005` through `P0-D016`, and `P0-D018` through `P0-D021` are decided by the defaults recorded in the table and implemented in the scaffold.
- `P0-D004` was deferred from the local Phase 0 gate (protected signing is not part of the local gate). It is now closed by `agile/handoff/governance/RELEASE_KEY_CUSTODY.md`: test keys for `dev` channel CI, offline-or-KMS for `alpha`/`beta`, and KMS-required for `stable`/`enterprise-lts`. The first protected `alpha` release cannot tag until that doc's "Operational checklist" is co-signed by PM-B, Security, and the Release Manager.
- `P0-D017` was deferred to the audit verifier implementation slice. Phase 1 / Lane 4 shipped the verifier (`cmd/midnight/audit_verify.go`) and proposed the exit-code envelope. The proposal is now ratified by `agile/handoff/governance/AUDIT_VERIFY_EXIT_CODES.md`: `0` healthy / `1` warnings only / `2` chain mismatch / `3` signature failure / `64` usage error / `66` missing input.
- `P0-D022` was deferred to alpha-tag governance. It is now closed by `agile/handoff/governance/DESIGN_PARTNER_SIGNOFF_TEMPLATE.md`, which ratifies the per-tag counts already in `PROGRAM_OVERVIEW.md` §3 / `RELEASE_PLAN.md` §2.0 (`v0.1.0-alpha` = 2 install signoffs; `v0.2.0-alpha` = 3 first-product-loop signoffs; `v0.3.0-alpha` = 5 first-product-loop signoffs + ≥1 backup/restore drill).
- Full live Docker, ACME, BuildKit, registry, DB persistence, and live drill execution were explicitly post-Phase 0 work; Phase 1 (`phase1/lane{1..4}-*`, `phase1/lane5-wireup`, `phase1/sprint1-finish`) is integrating those into `main`. See `agile/handoff/phase1-subagents/PHASE1_INTEGRATION_REPORT.md` and the `v0.1.0-alpha` greenlight brief at `agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md`.
