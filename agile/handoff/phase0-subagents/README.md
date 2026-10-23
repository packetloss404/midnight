# Phase 0 Subagent Control Room

This folder is the Phase 0 execution control room. It defines eight worker subagents and two project-manager subagents for completing Design Partner Alpha.

The worker handoff files are lane contracts, not completion claims. Each worker owns implementation in its lane, exports contract evidence to downstream lanes, and reports blockers through the PM subagent model below.

Current execution status is tracked in `STATUS.md`.

## Sprint Beta Handoffs

| Handoff | Purpose |
|---|---|
| `SPRINT_B4_PROOF_PACKET.md` | Canonical local proof-packet contract for B1/B2/B3 evidence refs. |
| `SPRINT_B5_HOST_DRILL_METADATA.md` | First host-scoped Phase 3 metadata slice in the existing manifest envelope. |
| `SPRINT_B6_HOST_DRILL_CATALOG.md` | Candidate catalog follow-up to B5; reconciles the implemented `D-08` carrier with older `D-19` planning language and defines local-only return paths. |
| `SPRINT_B7_HOST_DRILL_RESERVATIONS.md` | Local-only future drill ID reservation/index slice after B6; reserves `D-20` through `D-23` outside the executable manifest with no live execution or harness assertion requirement yet. |
| `SPRINT_B8_DRILL_PROMOTION_READINESS.md` | Local-only promotion-readiness criteria after B7; defines what must exist before `D-20` through `D-23` can enter the executable manifest, with no ID range change or live execution yet. |
| `SPRINT_B9_RELEASE_EVIDENCE_ARCHIVE.md` | Local-only release evidence archive readiness after B8; defines the phase0-prefixed canonical archive command and keeps ignored artifacts, live probes, ID-range changes, and `D-20` through `D-23` promotion out of scope. |

## Project Manager Subagents

| PM subagent | Role | Primary accountability |
|---|---|---|
| **PM-A Delivery Coordination** | Execution manager | Integrated plan, lane dependencies, DoR/DoD enforcement, daily blocker handling, sprint readiness, and cross-lane contract closure. |
| **PM-B Release/Risk Governance** | Release-risk manager | Phase 0 release gates, scope boundaries, risk register, accepted-risk workflow, design-partner signoffs, and final exit evidence. |

PM-A and PM-B do not override architecture, security, or product scope decisions. Architecture changes require technical-lead approval. Security-sensitive changes require the security lead. Phase-scope changes require product/PM3 plus release and security signoff where relevant.

## Worker Subagents

| Worker | Lane | PM-A responsibility | PM-B gate focus |
|---|---|---|---|
| **S01** | Foundations | Repo scaffold, CI, governance, names, branch protection, signing scaffold. | Required checks, protected refs, signing evidence, supply-chain baseline. |
| **S02** | Installer/Ops | `.deb`, systemd, preflight, doctor, local install, KEK/bootstrap paths. | Install timing, package contents, doctor/preflight gates, local/public profile evidence. |
| **S03** | API/Data | `/api/v1alpha`, schema, migrations, idempotency, operations, queue, OpenAPI, error registry. | Contract freeze, oasdiff, Schemathesis/Pact, queue and idempotency release evidence. |
| **S04** | Runtime/Agent | Host registration, Docker Engine API, heartbeat, reconciler, existing-image deploy. | Runtime drills: agent loss, Docker restart/socket EOF, clock skew, route observations. |
| **S05** | Build/Registry | BuildKit, embedded registry, provenance/signing, image pins, rollback by digest. | Build drills, rollback never rebuilds, registry GC safety, BuildKit corruption evidence. |
| **S06** | Ingress/Logs/DX | Traefik file provider, generated-domain TLS, DNS-01 wildcard, logs, timeline, failure DX. | DNS/ACME gates, route/log/timeline evidence, ACME restore, failure-code surfacing. |
| **S07** | Support/Backup/Obs | Alerts, dashboard, support bundle, platform backup/restore, audit verify, telemetry, feature flags. | Zero-leak support bundle, backup/restore smoke, telemetry default-off, runbook/alert evidence. |
| **S08** | QA/Release Gate | Drill harness, install timing, phase0 gate workflow, exit report. | Primary gate executor reporting to PM-B; no silent skipped drills. |

## Critical Path

1. **Decision closure:** PM-A and PM-B close the pre-coding decisions in `agile/handoff/PHASE0_DECISION_LOG.md` before affected lanes implement.
2. **S01 first:** repo scaffold, Go module, CI, governance, required checks, and signing scaffolding unblock all lanes.
3. **S02 + S03 next:** installer/runtime substrate and API/data contracts unblock runtime and release-gate work.
4. **S04 + S06 first deploy path:** existing OCI image deploy, generated HTTPS, logs, timeline, and reason-code UX prove the Phase 0 first product loop.
5. **S05 after existing-image deploy:** BuildKit adds source-to-image flow only after runtime deploy works.
6. **S07 supportability closeout:** alert, runbook, backup/restore, telemetry, and support-bundle gates use synthetic fixtures early and real S04-S06 outputs later.
7. **S08 gates last, starts early:** harness and gate manifest start immediately after S01; full pass requires all lanes.

## Alpha Mapping

| Alpha tag | Owning lanes | Gate theme |
|---|---|---|
| `v0.1.0-alpha` | S01, S02, S03 | Install, bootstrap, doctor, API skeleton, schema, queue, audit seed. |
| `v0.2.0-alpha` | S04, S06 | First deploy from existing OCI image, generated HTTPS, logs/timeline, rollback. |
| `v0.3.0-alpha` | S05, S07, S08 | Build, registry, backup/restore, supportability, full Alpha drill gate. |

## Operating Rules

- A worker cannot invent a replacement contract for a blocked dependency; PM-A escalates the dependency owner.
- A release gate cannot be marked green without an evidence artifact that S08 can reference.
- Scope that belongs to MVP Beta can only enter Phase 0 through the decision log and explicit product/release/security approval.
- Every public failure path must use a registered reason code and a recovery hint.
- Every lane must keep downstream S08 drill evidence in mind from the first implementation PR, not at release closeout.

## Cadence

- Daily PM-A blocker review with all lane leads while S01-S04/S06 are on the critical path.
- Twice-weekly contract sync for API/schema/agent/ingress/build/support handoffs.
- Weekly PM-B release-risk review covering scope conflicts, risk register, release evidence, and design-partner readiness.
- Sprint planning admits only Ready stories under `agile/handoff/DEFINITION_OF_READY_AND_DONE.md`.
- Release candidates require PM-B, S08, release/SRE, security, and product signoff.
