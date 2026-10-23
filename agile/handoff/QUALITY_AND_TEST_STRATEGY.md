# Midnight — Quality and Test Strategy

This document defines the test pyramid, tooling, drill harness, performance and security testing policy, and release gates per phase. It exists to make every panel-locked decision in `D:/projects/midnight/midnight-platform-plan.md` verifiable, regression-resistant, and CI-enforced.

The guiding principle from the eight-specialist panel: "every promise in this document must be enforceable, not aspirational." (`spec` line 2419.)

---

## 1. Test pyramid

```
                          ┌────────────────┐
                          │  Drill (gated) │   ← Alpha-gating + v1-gating
                          └────────────────┘
                       ┌──────────────────────┐
                       │   End-to-end (E2E)   │   ← docker-compose stack
                       └──────────────────────┘
                    ┌────────────────────────────┐
                    │ Contract (Schemathesis,    │
                    │ Pact, AsyncAPI fixtures)   │
                    └────────────────────────────┘
                ┌──────────────────────────────────────┐
                │ Integration (testcontainers-go,      │
                │ Postgres, Docker engine, BuildKit)   │
                └──────────────────────────────────────┘
        ┌──────────────────────────────────────────────────────┐
        │ Unit (pure Go, ≤ 50ms per test, ≤ 30s suite)         │
        └──────────────────────────────────────────────────────┘
```

### 1.1 Unit tests

- Pure logic, no external systems. Maps, slices, structs, state-machine transitions, parsing, validation, redaction matchers.
- One assertion concept per test.
- Use `t.Parallel()` aggressively; race detector is required.
- Fakes/mocks are written by hand or with `gomock` for narrow interfaces. Do not mock concrete structs.
- Coverage target: ≥ 85% for `internal/server/state`, `internal/worker/transitions`, `internal/agent/spec`, `internal/redaction`. Other packages: ≥ 70%.

### 1.2 Integration tests

- Postgres via `testcontainers-go` with the production schema and the production migrations applied.
- Docker engine: a real engine on the CI runner; tests use ephemeral networks/volumes/containers with `midnight-itest-` prefix and a teardown hook.
- BuildKit: a real BuildKit instance, one per test class.
- Traefik: real Traefik with the Midnight-generated dynamic file provider config; tests assert `acme.json` byte-exact restore behavior using LE staging.
- Job queue: tests use the production Postgres-backed queue with `SELECT … FOR UPDATE SKIP LOCKED`. No fakes.

### 1.3 Contract tests

- **OpenAPI lint**: every endpoint has `operationId`, `security` scopes, `requestBody` and `responses` schemas, examples, and the `x-rate-limit-class` and `x-idempotency` extensions. CI fails on any missing field.
- **oasdiff**: zero breaking-change diff vs the last published minor unless an ADR explicitly accepts it. v1 graduation requires zero breaking diff for ≥ 90 days across two consecutive minors (panel `spec` line 171).
- **Schemathesis property tests**: ≥ 95% endpoint coverage for v1 graduation. Generators respect schema constraints, idempotency-key invariants, and pagination cursor stability.
- **Pact** consumer contracts for: CLI, dashboard, Terraform provider (when shipped), MCP. Provider verification runs on every server PR.
- **AsyncAPI fixture tests**: every CloudEvents type at `/schemas/events/<type>/<version>.json` has at least one positive and one negative fixture.
- **Webhook fixture tests**: signature verify, replay-window enforcement (300s), two-key rotation overlap, dead-letter on 5× 5xx.

### 1.4 End-to-end tests

- One full stack from `docker-compose -f compose.e2e.yaml up`: Postgres, server, worker, agent, BuildKit, registry, Traefik, mock GitHub App, mock ACME via Let's Encrypt staging or Pebble.
- Cover the Phase 0 critical path (`spec` lines 2337–2353): install → bootstrap → deploy OCI image → deploy Dockerfile → generated domain → logs/timeline → failure reason → rollback.
- Cover the MVP Beta journeys: custom domain TXT verification, GitHub App push deploy, signed webhook, sealed variable, audit verify, off-host backup destination, upgrade transaction with auto-rollback.
- Cover the v1 journeys when shipped: OIDC, scheduled backups + restore-test, PR previews, Terraform provider acceptance.

### 1.5 Drill tests

Drills are panel-required release gates, not optional. See § 3.

---

## 2. Tooling

| Concern | Tool | Pin location |
| --- | --- | --- |
| Test runner | `gotestsum --format pkgname --rerun-fails=2 -- -race -count=1 ./...` | `Makefile` |
| Coverage | `go test -coverprofile=…` + `go tool cover` + `gocov-cobertura` for CI | `Makefile` |
| Race detector | required on every Go test invocation | `Makefile`, CI workflow |
| Lint | `golangci-lint run --config=.golangci.yaml` | repo root |
| Static analysis | `go vet`, `staticcheck` (via golangci-lint) | `.golangci.yaml` |
| Vulnerability scan | `govulncheck ./...`, `osv-scanner`, `npm audit --omit=dev`, `semgrep --config=p/golang` | CI |
| Container/integration | `testcontainers-go` | `go.mod` |
| API property | `schemathesis run --checks all` | CI |
| Consumer contracts | `pact-go` | `go.mod` |
| Schema diff | `oasdiff` | CI |
| Performance | `k6` for HTTP, `pgbench` for queue | `test/perf/` |
| Secret scanning (PR) | `gitleaks` | pre-commit + CI |
| Dependency updates | Renovate (Go, Node, Dockerfiles, Actions, base images, template image pins). Dependabot security alerts retained. | `renovate.json` |

### 2.1 Coverage floor

| Package | Floor |
| --- | --- |
| `internal/server/state` (state machines, transitions, guards) | 85% |
| `internal/worker/transitions` | 85% |
| `internal/agent/spec` (container spec validation) | 85% |
| `internal/redaction` | 95% (release gate; see § 5.4) |
| `internal/audit` (hash chain, checkpoint signing) | 90% |
| `internal/secrets` (KEK/DEK, sealed) | 90% |
| `internal/api/idempotency` | 90% |
| Default for other Go packages | 70% |
| TypeScript dashboard | 60% (lower because E2E covers journeys) |

CI fails if a PR drops coverage by more than 0.5% in any of the listed packages.

### 2.2 Race detector

Required on every Go test target. CI rejects builds where the race detector is disabled. Race-detector-only failures are P1 bugs.

---

## 3. Failure-drill harness design

Drills are the panel's enforceable check that the platform behaves correctly under specific failure modes. They are CI-runnable, gated, and asserted-against-reason-code.

### 3.1 Harness shape

Each drill is a Go file under `test/drills/<id>_test.go` with:

```go
//go:build drill

package drills

import "testing"

func TestDrill_KillAgent(t *testing.T) {
    h := harness.New(t)
    defer h.Cleanup()

    h.Deploy("hello-web")
    h.WaitState("svc/hello-web", "ACTIVE")

    h.KillAgent()                                   // injection
    h.AssertState("svc/hello-web", "ACTIVE")        // workload survives
    h.AssertEvent("agent.lost")                     // event emitted
    h.AssertReasonCode("AGENT_RUNTIME_UNAVAILABLE") // typed code
    h.AssertAuditEntry("agent.heartbeat.missed")
    h.AssertRunbookExists("agent-recovery")
    h.RestartAgent()
    h.AssertState("svc/hello-web", "ACTIVE")        // recovery
}
```

Each drill MUST assert all five:

1. Expected state transition.
2. Reason code.
3. Audit/event trail.
4. User-facing diagnostic (`midnight status`/`logs`/`doctor` output).
5. Recovery runbook result (the runbook completes successfully when followed).

### 3.2 Required Alpha drills (release-gating)

From `spec` lines 195 (Required Alpha failure drills) and lines 1567 (Required Alpha drills):

- `kill-agent` — agent process killed mid-deploy
- `kill-server` — control-plane server killed
- `kill-worker` — worker killed mid-job
- `docker-restart` — Docker daemon restart during deploy
- `image-pull-failure` — registry returns 5xx / network blocked
- `readiness-failure` — service never passes readiness
- `disk-pressure` — `/var/lib/midnight` reaches 92%
- `clock-skew` — agent clock > 5 min ahead of server
- `journald-wedged` — journald rate-limit lockup
- `var-log-full` — `/var/log` exhausted
- `postgres-conn-exhaust` — connection pool exhaustion
- `network-partition-30s`, `-5m`, `-30m` — three durations
- `buildkit-cache-corrupt` — BuildKit local cache corrupted
- `docker-socket-eof` — Docker socket EOF mid-call
- `volume-readonly-remount` — XFS remounts ro under pressure
- `kek-loss` — `/etc/midnight/secrets.key` deleted, recovery via documented operator-supplied passphrase
- `acme-state-restore-at-scale` — ≥ 50 certs restore after `acme.json` loss; throttled to ≤ 20/hr (`spec` line 203)
- `webhook-duplicate-reorder` — when webhooks ship in MVP Beta
- `dns-verification-broken` — when domains ship

### 3.3 Required v1 drills

- `postgres-restart-during-jobs`
- `interrupted-upgrade`
- `backup-restore-replacement-volume`
- `registry-outage`
- `acme-renewal-failure`
- `host-reboot`
- `agent-server-version-skew`

### 3.4 CI gating

```yaml
# .github/workflows/ci.yaml (sketch)
jobs:
  unit:        { runs-on: ubuntu-24.04 }
  integration: { runs-on: ubuntu-24.04, services: [postgres, docker] }
  contract:    { runs-on: ubuntu-24.04 }
  e2e:         { runs-on: ubuntu-24.04-large }
  drills:
    runs-on: ubuntu-24.04-large
    if: github.ref == 'refs/heads/main' || contains(github.event.pull_request.labels.*.name, 'run-drills')
    strategy:
      matrix:
        drill: [kill-agent, kill-server, ..., acme-state-restore-at-scale]
    steps:
      - run: make drill-run DRILL=${{ matrix.drill }}
```

Drills run on every merge to `main` and on demand on PRs via the `run-drills` label. Pre-release tags require the full Alpha/v1 drill suite green within the last 24 hours.

### 3.5 Drill ownership

- Build drills: build/supply-chain owner.
- Runtime drills: Linux runtime/kernel owner.
- Control-plane/queue drills: distributed systems owner.
- Security drills (KEK loss, secret leak): security owner.
- Backup/restore drills: storage owner.
- Network/ACME drills: networking owner.
- API/idempotency drills: API/IaC owner.
- Upgrade/install drills: release/SRE owner.

The ownership map mirrors the eight-specialist panel.

---

## 4. Performance and load test policy

Performance testing is **not** a daily activity. It is a phase-gating activity with explicit baselines.

### 4.1 When

- Phase 0 release: smoke load (single-app deploys, log streaming, route reconciliation).
- MVP Beta release: queue saturation, Postgres write tiers (`events`, `audit_logs`, `jobs`, `host_heartbeats`, `deployment_instances`), Traefik route table at 5000 routes.
- v1 release: regression vs MVP Beta baselines.
- After any change to the job-queue dequeue, advisory-lock arbitration, audit hash-chain, ingress reload, or partition-pruning code.

### 4.2 What

| Workload | Tool | Target |
| --- | --- | --- |
| API throughput | `k6` against `/api/v1alpha` | p95 < 200ms at 200 rps for read paths; p95 < 1s for `POST /deployments` excluding build time |
| Queue dequeue | Custom Go bench + `pgbench` | ≥ 1000 jobs/sec dequeued by 4 workers without lock thrash |
| Audit append | `pgbench` script | ≥ 5000 audit rows/sec with hash chain intact (verified by `midnight audit verify` post-run) |
| Ingress reload | `k6` measuring 502 rate during a 1000-route reload | < 0.1% 502 rate |
| Build concurrency | drill-style bench | `min(4, cores/2)` per host respected, no OOM |

### 4.3 Against what baseline

- Baselines are recorded in `test/perf/baselines/<release>.json`.
- A perf regression is any p95 increase > 20% from the previous release baseline. Regressions block the release.
- Phase 0 baselines are established at the Alpha release tag and frozen there.

### 4.4 Where it runs

- A dedicated CI runner profile (`perf-runner`) with fixed CPU/memory and a known Postgres tuning. Not the default CI runner; perf numbers from default CI are advisory only.

---

## 5. Security testing

Security testing is enforced in CI. Findings block merges; the redaction-eval gate blocks releases.

### 5.1 Static + dependency

- `govulncheck ./...` — required check.
- `osv-scanner` over `go.sum`, `package-lock.json`, `requirements.txt` — required check.
- `npm audit --omit=dev` for the dashboard — required check.
- `semgrep` with the curated rule pack (`p/golang`, `p/owasp-top-10`, `p/secrets`) — required check.
- `gitleaks` pre-commit hook + CI — required check.

### 5.2 Container/image scanning

- Final OCI image scanned with `trivy` and `grype` on every release tag.
- SBOM generated by the platform/control plane (not tenant scripts) per `spec` line 847.
- SLSA-style provenance emitted as in-toto SLSA v1.0 statements; **no SLSA Build Level claimed** until audited (`spec` line 80).

### 5.3 Authentication and authorization tests

- Authorization tests are release gates (`spec` line 1207).
- Every endpoint has at least one negative test for: missing token, expired token, wrong scope, cross-tenant access, cross-environment-tier reference variable.
- Bootstrap-token one-shot semantics tested (read deletes file).
- Session cookie tests: `__Host-` prefix, `Secure`, `HttpOnly`, `SameSite=Lax`, 15-minute TTL, CSRF double-submit, Origin header check.
- CSP header tests: exact match with `spec` line 123.

### 5.4 Redaction eval — release gate

Redaction is the AI-context, log, support-bundle, audit-export, and webhook-payload gate. The fixture set lives at `test/fixtures/redaction/`.

| Pass | What it must catch |
| --- | --- |
| 1 | Byte-substring match against in-memory secret bloom filter (verifying compare) |
| 2 | Regex pack: AWS (`AKIA…`, `ASIA…`), GCP service-account JSON, Azure storage keys, JWT, `-----BEGIN` blocks, Slack (`xoxb-`, `xoxp-`), GitHub (`ghp_`, `ghs_`, `gho_`), base64 entropy > 32 |
| 3 | `KEY=val` capture against the secret-name table |
| 4 | URL credentials `://user:pass@` |
| 5 | `Authorization` headers |

Release gate: `secret_leakage_rate == 0` on the full fixture set. Any new secret class added to the platform requires a corresponding fixture before merge.

### 5.5 SSRF / outbound HTTP

- The single shared SSRF-safe outbound HTTP client (`spec` line 124) is the only path used by webhook delivery, git/registry/AI providers, and ACME.
- Test fixtures (required by spec) cover: RFC1918, CGNAT, link-local, loopback, multicast, cloud metadata endpoints (`169.254.169.254`, `fd00:ec2::254`), DNS rebinding (resolved-once + connection pinned to resolved IP).
- A test that creates a non-SSRF-safe `*http.Client` in production code fails CI.

### 5.6 Webhook signing/verification

- `Midnight-Signature: t=<unix>,v1=<hex>,kid=<key_id>` produced and verified.
- Replay window 300s enforced.
- Two-active-key rotation (30-day overlap) tested.
- Multi-`v1` accepted during rotation overlap.

### 5.7 AI-assistant eval gates (when AI features ship)

Required before any AI mutating action ships (`spec` lines 1660–1670):

- Deployment diagnosis accuracy (≥ baseline, set per release).
- Secret leakage rate `== 0`.
- Prompt-injection resistance (curated injection corpus).
- Tool selection accuracy (≥ baseline).
- Required-approval recall (≥ 99%, false negatives are P0).
- Unsafe-action refusal rate (≥ 99%).
- Citation/evidence correctness.
- Hallucinated API/action rate (≤ baseline).
- Remediation success rate.

Eval datasets are versioned in the `eval_datasets` table; runs in `eval_runs`. AI features are disabled by default (`spec` line 38). MCP Class 3+ tools require out-of-band human approval through the dashboard (`spec` line 127).

### 5.8 Pen-test recommendation

Pen-test cadence is documented in `SECURITY_REVIEW_PLAN.md`. Findings flow back into this doc as new fixtures or assertions.

---

## 6. Release gates per phase

A release tag is created only when **every** gate below passes within the previous 24 hours.

### 6.1 Phase 0 Alpha release gates

| Gate | Status |
| --- | --- |
| Unit + integration + contract + E2E suites green | required |
| Coverage floors met | required |
| `govulncheck`, `osv-scanner`, `npm audit`, `semgrep`, `gitleaks` clean | required |
| OpenAPI lint clean | required |
| oasdiff: no breaking changes vs previous Alpha tag (or accepted ADR) | required |
| Required Alpha drills green (§ 3.2) | required |
| Redaction eval `secret_leakage_rate == 0` | required |
| Local-mode install ≤ 15 min (no DNS) | required (`spec` line 2220) |
| Public-mode install ≤ 15 min excluding DNS/ACME | required (`spec` line 2221) |
| `midnight rollback` preserves previous active deployment until candidate ready | required |
| `midnight support-bundle` redaction preview matches manifest | required |
| Platform backup → restore smoke passes | required |
| No external telemetry/log export without explicit operator config | required |
| Default Alpha alert rules + `midnight-overview.json` dashboard ship in `.deb` | required |
| `/usr/share/midnight/runbooks/*.md` Phase 0 set ships | required (see `OPERATIONS_RUNBOOK_INDEX.md`) |
| All Phase 0 panel decisions verified by named tests | required |
| Performance baseline recorded | required |

### 6.2 MVP Beta release gates

In addition to Phase 0 gates:

| Gate | Status |
| --- | --- |
| Sealed variables: no API path returns plaintext after create | required (`spec` line 120) |
| Audit hash chain: `midnight audit verify` green across upgrade | required |
| GitHub App: webhook signature + replay protection + dead-letter | required |
| Custom domain: TXT ownership verification, CAA preflight, HTTP-01 issuance | required |
| Postgres template: manual logical backup + restore + smoke | required |
| Off-host backup destination configurable; `BACKUP_OFFHOST_MISSING` warning surfaces | required |
| Upgrade transaction (snapshot → migrate → smoke → auto-rollback) green | required |
| Skip-version upgrade refused with `UPGRADE_SKIP_FORBIDDEN` | required |
| Image signature verification on pull/start; `IMAGE_SIGNATURE_INVALID` on mismatch | required |
| KEK rotation drill green (rewraps all DEKs, retains KEK_n 30 days) | required |
| MVP Beta panel decisions verified by named tests | required |

### 6.3 v1 Foundations release gates

In addition to MVP Beta gates:

| Gate | Status |
| --- | --- |
| OpenAPI: zero `oasdiff` breaking change for ≥ 90 days across two consecutive minors | required (`spec` line 171) |
| Schemathesis: ≥ 95% endpoint coverage | required |
| Pact: green for CLI, dashboard, Terraform provider, MCP | required |
| Two consecutive SDK releases generated without manual patches | required |
| At least one external IaC consumer integration in production | required |
| OIDC/OAuth login green | required |
| Quotas + retention policy + usage/showback ledger | required |
| Plan/dry-run + drift + import/export | required |
| Scheduled backups + weekly sample restore for ≥ 1 backup per service | required |
| PR previews with trust/data/TTL rules | required |
| OTLP/log-drain with redaction policy | required |
| All v1 panel decisions verified by named tests | required |
| All v1 drills (§ 3.3) green | required |

### 6.4 v1 GA blocker

Per the panel: rootless BuildKit is the v1-GA blocker (`spec` line 84). v1 Foundations may ship without it; v1 GA may not.

---

## 7. CI matrix summary

| Check | PR | Main | Release tag |
| --- | --- | --- | --- |
| Lint, vet, fmt | required | required | required |
| Unit tests (race, count=1) | required | required | required |
| Integration tests | required | required | required |
| Contract: OpenAPI lint, oasdiff | required | required | required |
| Contract: Schemathesis | nightly + label `run-property` | required | required |
| Contract: Pact | required | required | required |
| E2E | label `run-e2e` | required | required |
| Drills | label `run-drills` | required | required |
| `govulncheck`, `osv-scanner`, `npm audit`, `semgrep` | required | required | required |
| `gitleaks` | required | required | required |
| Redaction eval | required | required | required |
| Performance | nightly | nightly | required |
| AI evals (when AI ships) | label `run-ai-eval` | required | required |

---

## 8. Test naming conventions

Tests that verify a panel decision encode the decision in the name:

- `TestRollback_PullsStoredDigest_NeverRebuilds` — `spec` line 77.
- `TestACME_OffsetGreaterThan5s_BlocksIssuance` — `spec` line 97.
- `TestAuditChain_RowHashSurvivesRestart` — `spec` line 121.
- `TestIdempotency_ConcurrentSameKey_Returns409InProgress` — `spec` line 172.
- `TestSupersede_UsesMonotonicSequence_NotWallClock` — `spec` line 104.
- `TestSealedVariable_NoPlaintextEverReturned` — `spec` line 120.
- `TestUpgrade_SkipVersion_RefusedWithCode` — `spec` line 193.
- `TestDeployment_OneRollbackTransitionPerService_AdvisoryLock` — `spec` line 104.

A reviewer reading the test name should be able to find the panel decision in the spec without context.

---

## 9. What "good" looks like in 6 months

- Every panel decision has a named test.
- Every reason code in `docs/error-codes.md` is asserted by at least one negative test.
- Every Alpha-gating drill runs on `main` daily and on every release-tag PR.
- Coverage trends up, not down.
- The redaction fixture set has never regressed.
- The OpenAPI spec has shipped two minors with zero breaking diff.
- The team can deploy a new version of Midnight to the design-partner cluster in under 30 minutes including the full pre-release gate suite.
