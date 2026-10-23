# Midnight — Team Onboarding

Read this first. One page of orientation, then a map to everything else.

Midnight is an open-source self-hosted Railway-like PaaS. Phase 0 Alpha proves one slice: install on a VM, deploy a stateless web app, route a generated HTTPS domain, see the timeline, and roll back safely. We are not building a Railway clone, a hosted multi-tenant cloud, a Kubernetes platform, or an enterprise SSO product yet. The spec at `D:/projects/midnight/midnight-platform-plan.md` and the eight-specialist panel decisions in that file are authoritative.

---

## How to use this folder

The five handoff files in `D:/projects/midnight/agile/handoff/` are a self-contained engineering kit:

| File | Purpose |
| --- | --- |
| `TEAM_ONBOARDING.md` | This file. Orientation, glossary, dev setup. |
| `DEFINITION_OF_READY_AND_DONE.md` | DoR/DoD for stories and epics. |
| `QUALITY_AND_TEST_STRATEGY.md` | Test pyramid, tooling, drill harness, release gates. |
| `SECURITY_REVIEW_PLAN.md` | Threat model, high-risk surfaces, mandatory reviewers. |
| `OPERATIONS_RUNBOOK_INDEX.md` | Runbook inventory shipped with the `.deb`. |

Source-of-truth map (where to look for what):

| Question | Authoritative source |
| --- | --- |
| What is the locked product/architecture decision on X? | `midnight-platform-plan.md` § "Eight-Specialist Panel Locked Decisions" (lines 71–204) |
| What is the Phase 0 / MVP Beta / v1 scope? | `midnight-platform-plan.md` § "Scope Layers" and "Roadmap" (lines 2122–2305) |
| What is the deployment state machine? | `midnight-platform-plan.md` § "Deployment State Machine" (lines 693–781) |
| What are the failure reason codes? | `midnight-platform-plan.md` § "Observability And Supportability" → failure classifications (lines 1442–1499) |
| What is the API contract (idempotency, pagination, IDs)? | `midnight-platform-plan.md` § "API, IaC, and DX contract" (lines 167–204) and § "API, Automation, And IaC Contract" (lines 594–691) |
| What is the install/runtime/Linux contract? | `midnight-platform-plan.md` § "Linux Runtime Contract" (lines 888–954) and § "Linux Install, Packaging, And Operations" (lines 1955–2062) |
| What are the security hard lines? | `midnight-platform-plan.md` § "Security Hard Lines" (lines 2307–2331) |
| What epic does my story belong to? | `D:/projects/midnight/agile/epics/` (one file per epic, when produced by PM-1) |
| What sprint am I in? | `D:/projects/midnight/agile/sprints/` (sprint plans, when produced by PM-3/PM-4) |
| What does feature FEAT-XYZ mean? | `D:/projects/midnight/agile/features/FEAT-XYZ.md` (feature dossiers, when produced by PM-2) |
| What is the runbook for incident X? | `OPERATIONS_RUNBOOK_INDEX.md` (this folder) → `/usr/share/midnight/runbooks/<name>.md` |

If a story implementation seems to conflict with the spec, the spec wins. If it seems to conflict with a panel-locked decision, the panel decision wins. Open an ADR (see below) to record any deviation.

---

## Glossary

These terms appear constantly. Use them precisely.

| Term | Definition |
| --- | --- |
| Operation | Public API-facing async promise returned by mutating actions. Has `op_id`, target resource, state, actor, idempotency key, expiry. Exposed at `/api/v1alpha/operations/{id}`. Polling and SSE event stream supported. (`spec` lines 611–621.) |
| Job | Internal durable worker execution record. Owns lease, retries, worker ownership, attempt count, heartbeat, timeout. Many-to-one with operation. Lives in the `jobs` Postgres table. |
| Deployment | Domain resource representing one deploy attempt with an immutable snapshot. Survives after operations/jobs expire. Has its own state machine (`QUEUED → BUILDING → DEPLOYING → ACTIVE → SUPERSEDED`/`FAILED`/`REMOVED`). |
| Snapshot | Immutable record of source SHA, builder type/version/digest, resolved Railpack/Nixpacks plan JSON, resolved base image digests for every `FROM`, produced image digest, and resolved variables (sensitive values stored by content-addressed reference). Rollback replays a snapshot. |
| Idempotency Key | Client-supplied header (`Idempotency-Key`) that de-duplicates `POST/PATCH/DELETE`. Scope = `(token_id, method, path_template)`. TTL 24h. Body hash via SHA-256 over RFC 8785 canonical JSON. Replay returns identical body + `Idempotent-Replayed: true`. |
| Bootstrap admin | First-time admin token written to `0600 root:root /var/lib/midnight/bootstrap-token` on first start of `midnight-server`. Read once via `midnight admin show-bootstrap`, which deletes the file. Mandatory password+TOTP on first login. Auto-disables once a normal admin exists. |
| KEK | Key-Encryption Key at `/etc/midnight/secrets.key`, mode `0400 midnight:midnight`. Wraps DEKs (Data Encryption Keys) per secret. Rotation generates `KEK_n+1`, rewraps all DEKs in a background `operations` job, retains `KEK_n` for 30 days. KEK loss is irrecoverable; back it up separately from Postgres. |
| DEK | Data Encryption Key. Per-secret symmetric key used to encrypt the secret value. Sealed by the current KEK. Rotated as part of secret rotation; not user-visible. |
| Sealed variable | Write-only-after-create variable class. No API path returns plaintext after create. Rotation requires a new value, not view-then-edit. UI shows last-rotated and length-bucket only. Audit/snapshot exports elide plaintext. Ships in MVP Beta. |
| Reference variable | Variable that points to another variable or service output. Cross-scope authorization enforced: the actor creating the reference must have read access on the referenced variable's scope, and the consuming service's deploy principal must have read access at every resolution. Cross-environment-tier (dev→prod, prod→preview) denied unless policy allows. |
| `runLatest` | Push-deploy default behavior: older pending deploys are canceled or marked `SUPERSEDED` when a newer deploy arrives. Arbitration uses a monotonic deployment sequence (`bigserial`), never wall-clock time. |
| Supersede | A deployment transition where an active deployment is replaced by a newer active deployment. `ACTIVE → SUPERSEDED` is one of two non-absorbing transitions out of `ACTIVE` (the other is `ACTIVE → REMOVING`). |
| Route freshness TTL | The interval (default 30s) inside which `instance.last_observed_at` must lie for the instance to remain in the route table. Below that, the instance is removed from the route but the deployment stays `ACTIVE` if other instances remain. |
| Operation reason code | Machine-readable `SCREAMING_SNAKE_CASE` string identifying a failure. Registered in `docs/error-codes.md`. Codes are additive-only. Examples: `BUILD_FAILED`, `IMAGE_SIGNATURE_INVALID`, `IDEMPOTENCY_REQUEST_IN_PROGRESS`, `RESTORE_ACME_THROTTLED`. |
| Class A–E (automation) | Automation blast-radius classification. A = deterministic self-healing, no approval. B = safe operator runbook with optional approval. C = production-impacting, requires approval. D = data-risk, requires elevated approval and backup precondition. E = forbidden automation. (`spec` lines 1505–1511.) |
| Class 0–6 (AI action) | AI assistant action classification. 0 = read-only explanation. 1 = read-only diagnosis. 2 = draft-only. 3 = low-risk mutation with confirmation. 4 = high-risk mutation with approval. 5 = destructive/data-risk. 6 = forbidden. |
| Bootstrap token | See "Bootstrap admin." |
| Apps domain | Operator-controlled platform domain under which generated project/service hostnames are issued (e.g., `*.apps.example.com`). DNS-01 wildcard cert is Alpha-blocking. |
| Untrusted build | A commit whose author is not a workspace member, any push from a fork, or any branch outside the trusted-branch allowlist. Runs with no project secrets, no shared cache, ephemeral per-build cache only, quarantine registry path. Cannot promote to non-preview environments without explicit approval. |
| `BUILD_ROOTFUL` | Warning surfaced by `midnight doctor` whenever co-located rootful BuildKit is detected. |
| `applyChangeSet` | Atomic semantic mutation: all `If-Match` checks evaluated together; first conflict returns 409 with conflicting-resource list and nothing applied. Side effects (deploys, DNS, ACME) enqueue via transactional outbox. |

---

## Local development setup

Goal: a fresh-clone-to-running-Alpha-server in under 20 minutes on Linux/macOS/WSL2.

### Prerequisites

| Tool | Pinned version (Alpha) | Rationale |
| --- | --- | --- |
| Go | 1.22.x (read `.tool-versions`) | Backend, agent, CLI. |
| Node.js | 20 LTS | Dashboard. |
| pnpm | 9.x | Dashboard package manager. |
| Docker Engine | ≥ 24.0 (Engine API ≥ v1.43) | Runtime + dev Postgres + dev Traefik. cgroup v2 required. |
| docker-compose plugin | v2 | Dev stack. |
| `gotestsum` | latest | Test runner with race detector. |
| `golangci-lint` | pinned in repo | Lint gate. |
| `govulncheck`, `osv-scanner`, `semgrep` | pinned in repo | Security gates. |
| `mkcert` | latest | Local self-signed TLS for `--profile=local`. |

### One-time setup

```bash
git clone <repo>
cd midnight
make bootstrap          # installs hooks, downloads pinned binaries (Railpack, BuildKit image), seeds .env.dev
make dev-up             # docker-compose: Postgres, Traefik dev image, embedded registry
make migrate            # runs schema migrations against local Postgres
make seed               # creates dev workspace + bootstrap admin
make build              # builds midnight, midnight-server, midnight-agent, midnight-worker
```

### Daily fast-feedback loop

```bash
# terminal 1 — server + worker (with hot reload via reflex/air)
make run-server
make run-worker

# terminal 2 — agent
make run-agent

# terminal 3 — tests on save (under 30s for unit suite)
make watch-test

# terminal 4 — dashboard
cd dashboard && pnpm dev
```

Local-profile install (the panel-locked Phase 0 exit criterion (a) — fresh-VM-to-deployed-app under 15 min, no external DNS):

```bash
sudo midnight install --profile=local
sudo midnight admin show-bootstrap   # one-shot, deletes the file on read
midnight login --token <printed-token>
midnight init && midnight link
midnight deploy ./examples/hello-web
midnight logs -f
```

The `--profile=local` flag uses `*.midnight.local` hostnames, self-signed TLS via mkcert, disposable data roots under `~/.local/share/midnight`, and relaxed public DNS/ACME checks. It is non-production by definition.

### Test layout (see `QUALITY_AND_TEST_STRATEGY.md` for detail)

```
internal/
  server/    (unit + integration tests using testcontainers-go)
  agent/
  worker/
  builder/
  ingress/
test/
  contract/  (Schemathesis, Pact)
  e2e/       (full stack against docker-compose)
  drills/    (failure drills, gated by build tag `drill`)
  fixtures/
    redaction/  (secret_leakage_rate fixture set — release gate)
```

Always run with race detector. The CI gate is `go test -race -count=1 ./...` plus `gotestsum --format pkgname --rerun-fails`.

---

## How to run failure drills locally

Failure drills are CI-runnable, gated, and panel-required Phase 0 release gates. They live in `test/drills/`.

```bash
make drill-list                              # prints all drill IDs
make drill-run DRILL=kill-agent              # single drill against local stack
make drill-run DRILL=kill-server
make drill-run DRILL=kill-worker
make drill-run DRILL=docker-restart
make drill-run DRILL=image-pull-failure
make drill-run DRILL=readiness-failure
make drill-run DRILL=disk-pressure
make drill-run DRILL=clock-skew              # panel-added; >5min
make drill-run DRILL=journald-wedged
make drill-run DRILL=var-log-full
make drill-run DRILL=postgres-conn-exhaust
make drill-run DRILL=network-partition       # 30s, 5m, 30m durations
make drill-run DRILL=buildkit-cache-corrupt
make drill-run DRILL=docker-socket-eof
make drill-run DRILL=volume-readonly-remount
make drill-run DRILL=kek-loss
make drill-run DRILL=acme-state-restore-at-scale
make drill-suite ALPHA=1                     # runs all Alpha-gating drills
```

Each drill must assert: expected state transition, reason code, audit/event trail, user-facing diagnostic, recovery runbook result. Drills that don't make all five assertions fail.

---

## Where to ask questions / how decisions are made

### Daily questions
1. Look in the spec (search by term — the file is one document, ~2400 lines).
2. Look in the relevant epic / feature / sprint file under `D:/projects/midnight/agile/`.
3. Ask in the team chat (`#midnight-eng`).
4. If the answer requires a decision that is not in the spec or panel decisions, open an ADR.

### ADR (Architecture Decision Record) process

Location: `D:/projects/midnight/docs/adr/NNNN-title.md` (next free 4-digit number).

Template:

```markdown
# ADR-NNNN: <title>

Status: proposed | accepted | superseded by ADR-XXXX | deprecated
Date: YYYY-MM-DD
Deciders: <names>
Related: panel decision <quote>, FEAT-<id>, EPIC-<id>

## Context
What is the problem? What constraints from the spec or panel apply?

## Decision
What did we decide? Concrete and binding.

## Consequences
What changes? What new failure modes? What new tests? What is now harder?

## Alternatives considered
Each with a one-line rejection reason.
```

ADR rules:

- Any deviation from the spec or a panel-locked decision REQUIRES an accepted ADR. No exceptions.
- ADRs require two reviewers: one from the affected sub-team (build/runtime/control-plane/security/ingress/storage/api/release) and one from outside it.
- Security-impacting ADRs require the security lead.
- ADRs are immutable once accepted; supersede with a new ADR rather than editing.
- The ADR index lives at `D:/projects/midnight/docs/adr/README.md`.

### Escalation

| Topic | Owner |
| --- | --- |
| Spec interpretation | Tech lead |
| Panel decision conflict | Tech lead + original panel reviewer |
| Security trade-off | Security lead |
| Schedule trade-off | Engineering manager + product |
| Cross-team blocker | Program manager |

### Vulnerability disclosure

`security@midnight.dev` (placeholder until project name is finalized). 90-day disclosure window. Public PGP key in `SECURITY.md` at the repo root. See `SECURITY_REVIEW_PLAN.md` § "Vulnerability disclosure policy reference."

---

## First-week checklist for a new engineer

- [ ] Read this file end to end.
- [ ] Read `midnight-platform-plan.md` § Eight-Specialist Panel Locked Decisions (lines 71–204).
- [ ] Read `midnight-platform-plan.md` § Scope Layers + Roadmap (lines 2122–2305).
- [ ] Run `make bootstrap && make dev-up && make migrate && make seed && make build`.
- [ ] Deploy `examples/hello-web` against `--profile=local` and roll it back.
- [ ] Run the `kill-agent` and `image-pull-failure` drills locally.
- [ ] Read the DoR/DoD doc (this folder).
- [ ] Read the Quality & Test Strategy doc (this folder).
- [ ] Pair with someone on a Phase 0 critical-path story (`spec` lines 2337–2353).
- [ ] Submit your first PR within five working days; it should add or change a test.
