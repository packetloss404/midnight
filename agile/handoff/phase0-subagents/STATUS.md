# Phase 0 Subagent Execution Status

Updated: 2026-04-30

## Current State (Historical Phase 0 Scaffold Gate)

Phase 0 is complete for the historical local scaffold gate. The current product phase is Phase 0 Alpha; `v0.1.0-alpha` governance approvals and design-partner signoffs were received per product owner / user confirmation on 2026-04-30. Phase 1 engineering lanes have since integrated live Postgres, Docker, BuildKit/registry, ingress/ACME, secrets/audit, backup, volumes, release-verification work, and durable boot/package wiring. The local scaffold history below remains useful context, but the current greenlight source of truth is `agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md`.

## Governance Status Update (2026-04-30)

- Phase 0 Alpha approvals/signoffs received per product owner / user confirmation on 2026-04-30.
- Design-partner signoff floor for `v0.1.0-alpha` recorded as met per that confirmation.
- This status update does not add new technical CI evidence; existing verification caveats remain in the referenced governance docs.

## Verification Evidence

The integrated scaffold passed the local verification set:

```text
gofmt -w cmd internal pkg test tools
go test ./...
go vet ./...
go build ./...
go run ./tools/lint
go run ./tools/phase0-gate
go run ./tools/phase0-gate --format=markdown
go run ./tools/phase0-gate --format=json
go run ./tools/phase0-gate --format=json --execute-drills --out <temp-file>
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence <temp-file>
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence <temp-file> --release-evidence-index <temp-file>
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence artifacts/phase0/release-evidence.json --release-evidence-index artifacts/phase0/release-evidence-index.json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files --release-summary artifacts/phase0/release-summary.json
npm audit --omit=dev --package-lock-only --prefix web
```

Result: all commands passed; scaffold/workflow/target/status-check/version/governance/action-pin/pre-commit/release-evidence/release-check/artifact-path/doc guard passed; phase0 gate reported 19 manifest drill rows and 8 validated alerts in text, markdown, and JSON formats; drill execution reported 19 real passing local assertions and 0 skipped placeholders; required executed-drill evidence was marked `pass`; release evidence, release evidence index, and release summary JSON were written under `artifacts/phase0`; release evidence index verification passed with referenced file checks enabled; npm reported 0 vulnerabilities.

## Worker Status

| Worker | Status | Notes |
|---|---|---|
| S01 Foundations | complete | Product tree, Go module, seed binaries, package docs, governance files, release-key custody placeholder, release evidence doc, npm lockfile, workflows, release-check workflow with Phase 0 artifact upload, ADR seed, CODEOWNERS, pre-commit policy, task shims, required Makefile/Taskfile targets, duplicate job-ID guard, `phase0-drills`, `release-check`, `clean-artifacts` automation, `artifacts/phase0` evidence path policy, and scaffold/npm/workflow/branch-protection/target/CODEOWNERS/status-check/version/governance/action-pin/name/pre-commit/release-scope/release-evidence/release-check/artifact-path/doc policy guard under `tools/lint` are present. Protected signing and remote branch protection remain post-Phase 0. |
| S02 Installer/Ops | complete | Historical local gate: systemd unit files, Debian package inputs, Docker daemon baseline, journald/logrotate/UFW/AppArmor package artifacts, preflight/doctor check registries/runners/catalog JSON, explicit HostSource adapters, HostSource-backed cgroup v2/Docker API/daemon baseline checks, injected command-output parsers/runner seams, safe command execution plan/evidence model, injected command runner evidence collector, command evidence batch JSON, redaction review helper, opt-in OS command runner bridge, preflight/install release evidence packet JSON read/write, daemon path-provider helpers, dry-run remediation plan JSON for selected preflight failures, preflight assessment summary JSON, install readiness summary JSON, doctor readiness summary JSON, resource pressure classifiers, file-backed fixture adapters/bundle loader, report JSON/exit-code/combine helpers, install manifest read/write/package-content verifier, cgroup v2 parser, Docker API version helper, daemon.json merge/validate library, doctor snapshot, and uninstall dry-run plan types are present. Current Alpha package: Debian defaults, post-install cursor key/registry config/bootstrap setup, conditional migration apply, and systemd `serve` entrypoints are integrated. |
| S03 API/Data | complete | Historical local gate: `midnight-server serve` exposes `/api/v1alpha/compat`, `/api/v1alpha/openapi.yaml`, in-memory operations, filtered/paginated `GET /api/v1alpha/operations`, operation event history endpoint with sequence filtering/cursor event, API target health review/demo endpoint, schema evidence/migration plan/release evidence/evidence-index/API-data-readiness demo endpoints, cancel/fail endpoints for queued/running operations, idempotent `_demo/long`, and operations/SSE routes with error envelopes. Current Alpha package: Postgres DataStack, migration runner, cursor-secret loading, idempotency persistence, and package boot wiring are integrated; demo-only endpoints remain clearly marked as readiness/demo surfaces. |
| S04 Runtime/Agent | complete | Historical local gate: agent config loader, supervisor one-shot runner/diagnostics, supervisor loop planner/decision summary/diagnostic artifact/artifact review/loop execution artifact/injected loop executor/execution review/release evidence packet JSON read/write, agent evidence index JSON read/write, dry-run REST loop action executor, heartbeat freshness review, host probe structs, Docker API floor constant, deterministic names/labels, container spec validator, healthcheck taxonomy/defaults, bridge/IPAM conflict planner, runtime observation DTOs, desired-state diff planner, reconcile step planner with idempotency keys, registration/heartbeat/fetch desired client methods and DTOs/responses, HMAC heartbeat helper, desired-state cache/source/response, REST request builders, injected HTTP executor, and backoff helper are present. Current Alpha package: live Docker runtime and agent control-plane HMAC/HTTP2 integration are integrated as engineering work for later Alpha tags. |
| S05 Build/Registry | complete | Historical local gate: `versions.yaml`, BuildKit version contract, pure build worker executor, build worker config validator, build operation plan/state machine, build plan validation, `.dockerignore` parser/context planner/secret-file refusal, build output secret scanner, build/registry/provenance/cache-integrity review summaries, build release evidence packet JSON read/write, build/registry evidence index JSON read/write with optional SHA-256 refs and file verification, build/registry release evidence bundle helper, OCI-ish image reference parser, immutable release tag policy, registry retention candidate planner/protection reasons, GC summary reason counts, GC manifest JSON, confirm-apply safety gate, Dockerfile FROM digest parser, untrusted classification, progress events, provenance statement and artifact refs, deployment snapshot structs, rollback selector, image pin model/transitions, protected digest probe result, rollback no-build contract, secret fingerprint helper, retention/GC dry-run/apply plan/report byte accounting, and S05 docs are present. Current Alpha package: BuildKit CLI/supervisor and registry wrapper/GC/pin store are integrated, with package-managed service entrypoints. |
| S06 Ingress/Logs/DX | complete | Historical local gate: in-memory domain diagnostics store/support view, CAA preflight result model, ACME order state model, certificate expiry classifier, domain diagnostics merge/action helper, ingress diagnostics review, route publication evidence packet, ingress/route release evidence index JSON read/write with optional SHA-256 refs and file verification, ingress evidence bundle helper, injected route publication writer seam with apply evidence JSON read/write, root-scoped local publication writer, deployment heartbeat gap review, route artifact validator/support-safe diagnostics, route artifact prewrite planner/publication summary, route reconciliation planner/summary/compact string, Traefik route render types, deterministic YAML-ish renderer, ACME pinned challenge router, ingress defaults, route freshness evaluator, DNS/manual provider helpers, ACME restore throttle and weekly budget planners, route artifact hash, ACME budget constants, atomic route group writer, route filename sanitizer, in-memory timeline/log stores, log redaction placeholder and level inference, NDJSON log entry/cursor/filter/query/encoder types, timeline events, and sequence validator are present. Current Alpha package: Traefik file-provider and lego ACME integration are integrated as engineering work for later Alpha tags. |
| S07 Support/Backup/Obs | complete | Telemetry payload builder/validator/schema-ish field checks, in-memory feature flag store/doctor map, support bundle selector/manifest/preview/redaction summary/truncation/archive plan/category budget/archive manifest JSON read/write/archive review summary types, expanded hard-block path classifier, backup/support release evidence packet JSON read/write, backup/support evidence index JSON read/write with optional SHA-256 refs, operator review decisions, alert packaging evidence helper, backup destination/local URI parser/export manifest/readiness review/restore readiness/failure review/restore checksum shortcut/platform artifact list/restore step/smoke result model/passphrase validation/verification summary, locked alert metadata/rules, telemetry schema update, and runbook scaffolds are present. |
| S08 QA/Release Gate | complete | `test/drills/manifest.yaml` has D-01 through D-19 with required reason-code/event/diagnostic/runbook fields and D-14 subcases; `tools/phase0-gate` validates locked alerts plus drill manifest and reports counts in text/markdown/JSON with evidence packet read/write, `--out`, `--last-green`, `--execute-drills`, `--require-executed-drills`, `--release-evidence`, `--release-evidence-index`, `--verify-release-evidence-index`, `--verify-evidence-files`, `--release-summary`, and `--evidence-ref`; local adapters exist for D-01 through D-19 with 19 passing local assertions; executed-drill evidence is `pass` in required mode; release evidence JSON includes `releaseReady`; release evidence archive index JSON is generated and verified with optional SHA-256/file checks; deterministic release summary JSON is generated; evaluator, assertion result model, and JUnit XML emitter exist. Full live drill execution harness is post-Phase 0. |

## Post-Phase 0 PM-A Actions

1. Keep DB-backed schema/API parity current with the Postgres DataStack as new endpoints leave demo mode.
2. Continue hardening live Docker, BuildKit, registry, ACME, and route execution behind explicit release gates.
3. Replace local drill adapters with live drill execution where safe.

## Post-Phase 0 PM-B Actions

1. Execute any day-of-tag release-key custody steps and remote branch protection checks before any protected signed release.
2. Archive named design-partner signoff artifacts under the release-evidence path when available.
3. Preserve audit verifier exit-code scope as decided in `agile/handoff/governance/AUDIT_VERIFY_EXIT_CODES.md`.

## Phase 1 Engineering Lane Integration Status (for `v0.1.0-alpha` greenlight)

Updated: 2026-04-27

This section is appended after Phase 0 closure. The Phase 0 sections above are historical context for the local-scaffold gate; the rows below plus the greenlight brief describe the current Alpha package.

### Branches landed on `main`

| Branch | Merge commit | Sub-slice scope |
|---|---|---|
| `phase1/lane1-data` | `edaf996` | DATA — Postgres persistence (`internal/db/pg/`), cursor signing (`internal/api/cursor_secret.go`), DataStack router. |
| `phase1/lane4-security` | `7197fa2` | SECURITY + AUDIT — envelope encryption (`internal/secrets/`), audit chain math + verifier (`internal/audit/`, `cmd/midnight/audit_verify.go`), signed webhooks (`internal/webhook/`), GitHub-app scaffolding (`internal/integrations/github/`), env locks + artifact promotion ledger. |
| `phase1/lane2-runtime` | `52858d6` | RUNTIME — live Docker adapter (`internal/runtime/docker/`), `daemon.json` baseline + cgroup v2 hard-fail, agent ↔ control-plane HTTPS+HMAC over HTTP/2 (`pkg/agentapi`, `cmd/midnight-agent/internal/control/`). |
| `phase1/lane3-build-ingress` | `01d2ac7` | BUILD/INGRESS — BuildKit shell-out CLI (`cmd/midnight-buildkit/`), embedded-registry CLI + JSON pin store (`cmd/midnight-registry/`, `internal/registry/`), Traefik publication directory + ACME via lego (`internal/router/`, `internal/acme/`, `deploy/traefik/`). |
| `phase1/lane5-wireup` | `4eb18f5` | LANE 5 WIREUP — Postgres-backed `audit.AuditChain` + `0006_audit_chain_columns`, `RegisterAgentHandlers` + `RegisterWebhookHandlers` wired from the DataStack router, `0005_webhook_deliveries` migration + Postgres dedupe store, audit emit on every mutating handler. |
| `phase1/sprint1-finish` | `3bd9798` | SPRINT 1 FINISH — A: live `midnight doctor` + .deb packaging + bootstrap admin first-login MFA + `0007_bootstrap_admin`. B: live S3-compatible off-host backup destination (`internal/backup/s3_destination.go`). C: volumes + supersede arbitration + `0008_volumes`. D: signed releases pipeline + cosign-keyless OCI signatures + image-sig verify on the agent + `tools/release-verify` + `docs/release-verify.md`. |

### Lanes integrated

All four lane branches plus Lane 5 wireup plus the four Sprint 1 sub-slices are integrated on `main`. Tip of `main` after all merges: `3bd9798 Merge branch 'phase1/sprint1-finish'`. Default `go test ./...`, `go run ./tools/lint`, and `go run ./tools/phase0-gate` all exit 0 on the tip. The Sprint 1 reconciliation evidence is in `agile/handoff/phase1-subagents/MERGE_RECONCILIATION.md` and `agile/handoff/phase1-subagents/SPRINT1_FINISH_STATUS.md`.

### Governance closures

The three Phase 0 deferred decisions are now closed on the `phase1/governance` branch (off `main` at `3bd9798`):

| Decision | New status | Closure document |
|---|---|---|
| P0-D004 (release-key custody) | `decided` | `agile/handoff/governance/RELEASE_KEY_CUSTODY.md` |
| P0-D017 (audit verifier exit codes) | `decided` | `agile/handoff/governance/AUDIT_VERIFY_EXIT_CODES.md` |
| P0-D022 (design-partner signoff counts) | `decided` | `agile/handoff/governance/DESIGN_PARTNER_SIGNOFF_TEMPLATE.md` |

The consolidated PM-B sign-off package for `v0.1.0-alpha` is in `agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md`.

Status update (2026-04-30): Phase 0 Alpha approvals and design-partner
signoffs were received per product owner / user confirmation. This note
does not alter the technical evidence table below.

### Sprint-1 exit-criteria status

| ID | Status |
|---|---|
| 0.1-EC1 (Ubuntu 24.04 disposable-VM doctor < 10 min) | Pass (CI harness shipped on `phase1/blocker1-vm-ci`) |
| 0.1-EC2 (Debian 12 disposable-VM doctor < 10 min) | Pass (CI harness shipped on `phase1/blocker1-vm-ci`) |
| 0.1-EC3 (cgroup v1 host refused with explicit remediation) | Pass |
| 0.1-EC4 (`daemon.json` conflict refused with merged-diff output) | Pass |
| 0.1-EC5 (bootstrap-token one-shot) | Pass |
| 0.1-EC6 (Operation envelope + Idempotency-Key 409) | Pass (default lane; full Schemathesis sweep is a Phase 2 hardening task) |
| 0.1-EC7 (audit verify on 100-row fixture) | Pass |
| 0.1-EC8 (no external network at install) | Pass (by construction; disposable-VM harness will produce the network-capture evidence) |

### Greenlight Approval Status

The four greenlight conditions PM-B / Security / Release Manager / Product had to close (or accept-with-tracking) before `v0.1.0-alpha` is signed are documented in `agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md` Section 4: disposable-VM CI harness for 0.1-EC1/EC2; backup credential boundary; cosign Go SDK approval; first-protected-release custody execution. Per product owner / user confirmation on 2026-04-30, governance approvals/signoffs have been received. The recommended go/no-go conditions are in Section 5 of the same brief, and technical verification caveats remain as documented.

### Phase 1 Blocker 1 close (2026-04-27)

The disposable-VM CI harness for 0.1-EC1 / 0.1-EC2 has been merged into the current integration branch and is present as `.github/workflows/doctor-vm-ec1.yml` and `.github/workflows/doctor-vm-ec2.yml`. The workflows drive `tools/vm-harness` against `midnight doctor --json` on a clean ubuntu-24.04 runner (EC1) and a `debian:12-slim` container (EC2, strategy i). The harness asserts exit 0, < 10 min wall-clock, and every OR-CHK-DOC-001..009 entry `passed=true`. Negative-path matrix entries cover 0.1-EC3 (cgroup v1 refusal via the `--hostsource=fixture:` seam in `cmd/midnight-doctor`) and 0.1-EC4 (`/etc/docker/daemon.json` conflict). Operator runbook at `docs/runbooks/disposable-vm-doctor.md`. Default `go test ./...` stays green; the harness binary is built from `tools/vm-harness/` with parser unit tests, no Docker / VM required for the default lane. Remaining manual step: confirm both workflows pass on a real PR before flipping EC1 / EC2 from "shipped" to "verified green". The strategy upgrade path to true VMs is tracked in the `phase1-blocker1-vm-ci-followup` placeholder for `v0.2.0-alpha`.

### Pointers

- `agile/handoff/governance/README.md` — index of the four closure documents.
- `agile/handoff/phase1-subagents/PHASE1_INTEGRATION_REPORT.md` — single readable consolidation of Lane 1 / Lane 2 / Lane 3 / Lane 4 / Lane 5 wireup / Sprint 1 finish status.
- `agile/handoff/phase1-subagents/MERGE_RECONCILIATION.md` — non-obvious conflict resolutions during the lane merges.
