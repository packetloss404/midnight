# Canonical Deployments — staged rollout plan & writer audit

> **Archive — historical rollout plan.** Branch names, pending work, and review
> gates below describe the original checkpoint. Use the [backlog](../BACKLOG.md)
> and [deployment readiness](deployment-readiness.md) for current work and evidence.
> This path is retained for existing migration and source references.

Branch: `feat/canonical-deployments`. Status: foundation landed (verifiable
pieces), remaining integration steps **gated on human review + a real Postgres**.

## What's done (safe, verified)

| Commit | What | Verified by |
|---|---|---|
| `1149cd7` | `internal/deploy/contract.go` FSM guard + migration 0014 (tables, DEFERRABLE one-active invariant, typed `deployment_events`, **inert** validator function — no trigger) | `go test ./internal/deploy` + non-DB manifest/schema tests |
| `ea5d33a` | Fix: SPA catch-all no longer swallows unmatched backend paths (real PR-#18 regression) | `go test ./internal/api` |
| `ca9b946` | `PostgresDeploymentStore` — create / FSM-guarded CAS transitions / atomic `PromoteDeployment` swap / rollback / event-ledger append | compiles; integration test below |
| `f681935` | `//go:build integration` Postgres test: promote CAS + one-active invariant, rollback CAS conflict, illegal-transition rejection | runs in CI with Docker |

Migration 0014 only **adds empty tables + an unattached function**, so applying
it changes no existing code path.

## Writer audit — every deployment-state mutation site today

The current system has **no canonical row**; state is reconstructed by parsing
operation-event strings (`internal/api/deployments.go`: `deploymentResourceFromOperation`,
`parseDashboardDeployIntakeEventMessage`, `active := route != ""`). Sites that
create/mutate deploy state, and how each maps to the new store:

| # | Site | Today | New store call (dual-write) |
|---|---|---|---|
| 1 | `internal/api/deploy_intake.go` `createDeployIntake` | creates a `deploy` **operation**, enqueues a deploy job, emits a queued event | also `CreateDeployment(state=queued)` with action=`deploy_image`/`deploy_source`, image/source metadata, `operation_id`, `trigger_key` |
| 2 | `internal/api/dashboard.go` template-deploy | builds a `DeployIntakeRequest` → same intake path | covered by #1 |
| 3 | `internal/deploy/executor.go` (run by `cmd/midnight-worker`) | schedule → readiness → `Promoter.Promote` (route) → drain; emits `DeployStep` events | `MarkDeploymentBuilding`/`MarkDeploymentDeploying`, then `PromoteDeployment` on ready, `FailDeployment` on failure. **Requires threading a deployment-store handle into the worker/executor** (today it only has a `Promoter` interface) |
| 4 | `internal/build/rollback.go` (14-step plan; already INSERTs a new QUEUED deployment id) | operation-based | `CreateRollbackDeployment` (never rebuilds) + CAS `PromoteDeployment(expectedActive)` |
| 5 | `internal/graphql/resolver.go` `deploymentRedeploy`/`Rollback`/`Restart` | mutation stubs / operation intake | `CreateDeployment` with action `redeploy_*`/`restart_instance`; rollback via #4 |
| 6 | `internal/api/deployments.go` read path | parses event strings | **read flip** — read the `deployments` table (LAST, after 1–5 + backfill populate it) |

## Staged rollout (each stage is its own reviewable PR)

Ordering matters: **do not flip the read path until writers + backfill populate
the table**, or the deployments API returns empty.

1. **Dual-write on create** (#1, #2) — populate `deployments` at intake alongside
   the operation. Reads unchanged. Low blast radius.
2. **Dual-write in the worker** (#3) — thread the store into `cmd/midnight-worker`
   / `executor`; drive Mark*/Promote/Fail from the real deploy lifecycle. Validate
   against Docker/CI with the integration harness. Highest-effort stage.
3. **Redeploy/rollback/restart** (#4, #5) — route through the store with correct
   actions + CAS.
4. **Historical backfill** (idempotent, gated) — migration `0015_backfill_deployments`.
   LIMITATION: historical deploy operations carry no *structured* `environment_id`
   (it lived in event-message strings), so a faithful reconstruction isn't
   possible in SQL. The backfill is conservative: it synthesizes only TERMINAL
   rows (never 'active') for deploy operations of services whose project has
   exactly one environment (env unambiguous), idempotently and reversibly.
   Multi-env services and unrecoverable operations are left to the guarded
   read-flip fallback; the current active is established by going-forward
   dual-write. Runs BEFORE the trigger migration. Rehearse on a prod clone.
5. **Flip the read path** (#6) — `deployments.go` reads the table; operations
   become pure request-tracking.
6. **Attach the FSM trigger** (`CREATE TRIGGER trg_validate_deployment_contract`)
   — only after all writers are audited/adapted and the backfill is validated.
   DONE (migration `0016_deployment_fsm_trigger`, human-approved): binds the
   0014 validator as a row trigger so the DB rejects non-queued inserts, illegal
   transitions, and image mutation. Ordered after the backfill (0015). Apply on
   a rehearsed prod clone.

## Status (stages)

All six stages landed on `feat/canonical-deployments`: intake dual-write, worker
reconcile, redeploy/rollback/restart mirror, gated backfill migration 0015,
guarded read-flip, and the FSM trigger (0016). Live-DB apply of migrations
0014/0015/0016 remains a human/CI step, rehearsed on a prod clone.

## Why the loop paused here

Stages 1–6 change live deploy code paths and **cannot be validated without a real
Postgres** (no Docker in the dev env) — and the guardrail is to stop before any
real-DB apply / enabling the trigger. The foundation is in place and CI-tested;
the next stage (worker dual-write) wants review + a Docker/CI run. Resume by
picking up stage 1.
