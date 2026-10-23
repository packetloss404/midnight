# Midnight Agile Delivery Bundle — Archive Index

The [documentation archive index](../docs/archive/README.md) also collects later
reviews and handoffs. This bundle retains its original paths for references
from source comments, release workflows, and tooling.

> **Archive.** This directory is the April 2026 planning bundle that preceded
> the implementation. It is kept as design history and is not maintained.
> Current status lives in [`HANDOFF.md`](../HANDOFF.md), unfinished work in
> [`BACKLOG.md`](../BACKLOG.md), release gates in
> [`docs/mvp-acceptance.md`](../docs/mvp-acceptance.md), and live architecture
> decisions in [`docs/adr/`](../docs/adr/README.md). Several files this index
> originally listed were never committed or have since been removed; they are
> marked **(not in repo)** below rather than deleted from the index so the
> original bundle shape stays visible.

The bundle was produced by 2 PM executives, 6 project managers, and 4 senior
engineers working from the panel-locked spec, which now lives at
[`midnight-platform-plan.md`](../midnight-platform-plan.md) in the repository
root.

> **Parity-review pass added (2026-04-25):** 4 reviewers cross-checked Midnight's planned end-state against Railway's full public surface. Net additions: 4 ADRs (013 templates marketplace, 014 GraphQL re-eval trigger, 015 RuntimeAdapter conformance, 016 multi-host dataplane), 4 feature dossiers (Cron, Static Site, PreDeploy/Run-Once, Multi-Host Dataplane), 1 contract doc (variable reference syntax `${{ scope.NAME }}`), 3 new sprints (S14 Cron+Static+PreDeploy, S15 PR previews+notifications+marketplace UI, S20 SDK pipeline + v1 graduation), and 14 new failure reason codes. The spec has a new `Parity Review Locked Decisions` section. CLI verb parity expanded (`up`, `run`, `connect`, `add`, `redeploy`, `restart`, `open`, `whoami`, `logout`, `version`, `down`, `unlink`, `docs`, `completion`). TCP proxy phase placement corrected to late-MVP-Beta. Migration assistant gained `railway.json`/`railway.toml`. The consolidated fix list (`parity-review/PARITY_CONSOLIDATED_FIXES.md`) is **not in repo**.

## 1. Program level

| File | Audience |
|---|---|
| `PROGRAM_OVERVIEW.md` **(not in repo)** | Executive sponsors, eng managers, design partners |
| [`RELEASE_PLAN.md`](RELEASE_PLAN.md) | Release managers, design partners, OSS contributors |
| [`EPICS.md`](EPICS.md) | Eng managers, tech leads, sprint planners |
| `CAPACITY_AND_TEAMS.md` **(not in repo)** | Hiring managers, eng managers |
| [`LIVE_EXECUTION_CHECKLIST.md`](LIVE_EXECUTION_CHECKLIST.md) | Historical live-execution checklist (not listed in the original index) |

## 2. Onboarding and process

| File | Audience |
|---|---|
| [`handoff/TEAM_ONBOARDING.md`](handoff/TEAM_ONBOARDING.md) | Every engineer joining the team |
| [`handoff/DEFINITION_OF_READY_AND_DONE.md`](handoff/DEFINITION_OF_READY_AND_DONE.md) | Tech leads, scrum masters |
| [`handoff/QUALITY_AND_TEST_STRATEGY.md`](handoff/QUALITY_AND_TEST_STRATEGY.md) | QA leads, every engineer |
| [`handoff/SECURITY_REVIEW_PLAN.md`](handoff/SECURITY_REVIEW_PLAN.md) | Security lead, tech leads |
| [`handoff/OPERATIONS_RUNBOOK_INDEX.md`](handoff/OPERATIONS_RUNBOOK_INDEX.md) | SRE, support |
| [`handoff/phase0-subagents/README.md`](handoff/phase0-subagents/README.md) | PM-A, PM-B, Phase 0 worker subagents |
| [`handoff/phase0-subagents/STATUS.md`](handoff/phase0-subagents/STATUS.md) | PM-A, PM-B, release manager |
| [`handoff/PHASE0_DECISION_LOG.md`](handoff/PHASE0_DECISION_LOG.md) | PM-A, PM-B, lane leads, release manager |
| [`handoff/governance/README.md`](handoff/governance/README.md) | PM-B, Security, Release Manager, Product — Governance closures (Phase 0 → Phase 1): P0-D004 release-key custody, P0-D017 audit verifier exit codes, P0-D022 design-partner signoff counts, and the `v0.1.0-alpha` greenlight brief. |

## 3. Engineering specs (planning-era contracts)

These were binding for the planning bundle. Where they disagree with the
current code, `docs/adr/` and the generated API artifacts under `api/` win.

### Architecture Decision Records (`engineering/adr/`)

16 planning ADRs (001–012 from the panel, 013–016 from the parity review).
They are numbered separately from the live ADRs in `docs/adr/`.

- ADR-001 Go for control plane
- ADR-002 Postgres as source of truth
- ADR-003 Postgres job queue (not Temporal) through v1
- ADR-004 Docker Engine API runtime
- ADR-005 cgroup v2 hard-required
- ADR-006 Traefik file provider ingress
- ADR-007 ACME DNS-01 wildcard is Alpha-blocking
- ADR-008 Rollback pulls digest, never rebuilds
- ADR-009 REST + OpenAPI (not GraphQL) for v1alpha
- ADR-010 Rootful Docker in Alpha, rootless in v1
- ADR-011 Audit log hash chain at MVP
- ADR-012 Deployment snapshot self-contained
- ADR-013 Templates marketplace and deploy button
- ADR-014 GraphQL reassessment trigger
- ADR-015 RuntimeAdapter boundary and conformance
- ADR-016 Multi-host dataplane design

### Database (`engineering/schemas/`)

- `SCHEMA_PHASE_0_ALPHA.sql` — planning-era DDL (29 tables, 46 indexes, 5 partitioned tables, hash-chain audit, image-pin reference counts, queue contract). The shipped schema is the `migrations/` directory.
- `SCHEMA_NOTES.md` — naming conventions, ULID prefix taxonomy, partitioning, role separation, migrations
- `SCHEMA_QUEUE_CONTRACT.md` — Postgres-as-queue normative contract: dequeue SQL, lease/heartbeat lifecycle, reclaim, per-class queues, fairness, dead-letter, cancellation safe-points

### API contracts (`engineering/contracts/`)

- `OPENAPI_V1ALPHA.yaml` — planning-era OpenAPI 3.1 draft. The maintained spec is under `api/`.
- `API_ERROR_CODE_REGISTRY.md` — flat SCREAMING_SNAKE_CASE registry with stability rules
- `WEBHOOK_AND_EVENT_CONTRACT.md` — outbound HMAC signing, replay window, key rotation, CloudEvents binding, event catalog, delivery semantics
- `OPERATION_AND_IDEMPOTENCY_CONTRACT.md` — operation lifecycle, polling, SSE streaming, idempotency state matrix, optimistic concurrency, cursor pagination, rate-limit budgets

### Code skeletons (`engineering/skeletons/`)

- `REPO_LAYOUT.md` — Go monorepo structure
- `INTERFACES_CONTROL_PLANE.md` — 21 interfaces (repositories, queue, reconciler, secret store, audit chain, webhook dispatcher, DNS provider, image registry, runtime adapter, builder, safe HTTP, redaction)
- `INTERFACES_AGENT.md` — 12 agent interfaces
- `INTEGRATION_TESTING_HARNESS.md` — testcontainers, Schemathesis, Pact, failure-drill harness, asciicast verification
- `MODULE_DEPENDENCY_MATRIX.md` — package dependency rules, no-cycle invariants, platform/tenant boundary (still referenced from `README.md`)

## 4. Feature dossiers (`features/`) — not in repo

The original bundle listed eight self-contained engineer briefings
(deployment state machine, Postgres job queue, rollback by digest, ACME
DNS-01 wildcard, `daemon.json` baseline, audit hash chain, off-host backup,
upgrade transaction). The `features/` directory was never committed. The
shipped behaviour for each topic is described in `docs/` (for example
`docs/install/daemon-json.md`, `docs/adr/`, and the runbooks) and in the code.

## 5. Sprint backlog (`sprints/`) — not in repo

The original bundle planned 13 sprints (~165 stories, ~770 story points)
across Alpha (Foundations, Installer, Control Plane API, Host Agent, BuildKit,
Ingress + Logs, Observability + Support, Failure Drills) and Beta
(Vars/Secrets/Audit, Custom Domains + GitHub App, Volumes + Postgres template,
Registry + Provenance, Upgrade Transaction). The `sprints/` directory was never
committed. Do not treat this list as a queue; open work is in
[`BACKLOG.md`](../BACKLOG.md).

## Source-of-truth chain (as planned)

```
midnight-platform-plan.md
    ├── Eight-Specialist Panel Locked Decisions  (authoritative, top of file)
    ├── RELEASE_PLAN.md, EPICS.md                  (program and delivery shape)
    ├── engineering/adr/*                          (planning-era architecture)
    ├── engineering/schemas/*                      (planning-era data model)
    ├── engineering/contracts/*                    (planning-era API surface)
    ├── engineering/skeletons/*                    (planning-era module map)
    └── handoff/*                                  (process and onboarding)
```

For the current chain, read `README.md` → `HANDOFF.md` → `BACKLOG.md` →
`docs/mvp-acceptance.md` → `docs/adr/`.
