# Midnight — Operations Runbook Index

This document is the index of the runbook markdown files that ship with the Midnight `.deb` at `/usr/share/midnight/runbooks/`. Each runbook is version-pinned to the package release. `midnight runbook list` and `midnight runbook describe <id>` ship in Phase 0; `midnight runbook run` (typed/automated execution) arrives in Phase 1 (`spec` line 199).

The runbook set is panel-required for the Phase 0 and MVP Beta releases. Runbooks have a fixed shape and an ownership/approval contract derived from automation Class A–E (`spec` lines 1505–1511):

- **Class A** — deterministic self-healing, low blast radius, no approval. Examples: restart a crashed stateless replica with backoff.
- **Class B** — safe operator runbook with optional approval per environment. Examples: restart staging service.
- **Class C** — production-impacting, requires approval. Examples: production rollback.
- **Class D** — data-risk, requires elevated approval and backup precondition. Examples: restore from backup.
- **Class E** — forbidden automation. Examples: delete user volumes automatically.

Every runbook below names its blast-radius class. A runbook's class is binding: an operator may not execute a Class C runbook without the documented approval, and the platform's Phase 1 `midnight runbook run` enforces this.

Runbooks live in markdown, are version-pinned to the package, and are walkthrough-tested by an operator who did not write them before they ship.

---

## 1. Phase 0 required runbooks

The following runbooks MUST ship in Phase 0 (`spec` lines 2139, 2216):

| Filename | Purpose | Preconditions | Expected duration | Class | Approvals |
| --- | --- | --- | --- | --- | --- |
| `failed-deploy-triage.md` | Triage a deploy that has reached `FAILED` state. Read the reason code, read the timeline, identify whether to retry, fix, or rollback. | Operator has read access to the project. Audit chain healthy. | 5–15 min | A (read-only) | None |
| `rollback.md` | Roll a service back to its previous active deployment. Pulls stored image digest; never rebuilds. Fails with `ROLLBACK_IMAGE_UNAVAILABLE` if digest is unretrievable. | Service has at least one prior `ACTIVE` deployment within the rollback window. Image digest still pullable from registry. | 1–5 min | C (production) / B (non-production) | Production: required reviewer + prevent self-approval (`spec` line 1238) |
| `restart-service.md` | Restart all instances of a service in place (no new deploy). Useful for transient runtime glitches. | Service is `ACTIVE`. No deploy currently in `DEPLOYING`. | 1–3 min | B / C in production | Production: required reviewer |
| `domain-tls-failure.md` | Diagnose and recover a domain stuck in `DNS_VERIFICATION_FAILED`, `TLS_ISSUANCE_FAILED`, `INGRESS_PORT80_UNREACHABLE`, `DOMAIN_OWNERSHIP_LAPSED`, or CAA-blocked. | Operator can edit DNS for the affected zone (or has CAA-edit access). | 15–60 min (excluding external DNS propagation) | B | Project admin |
| `disk-pressure.md` | Recover from `DISK_PRESSURE` (warn 75%, degrade 85%, block 92%). Order: expired build temp dirs → old BuildKit cache → unreferenced registry blobs after grace → logs past retention → expired backups by policy. **Never deletes user volumes.** | Operator on host or workspace owner. | 10–30 min | B | Project admin in production |
| `backup-restore.md` | Restore a tenant database service from a database-native backup into a replacement volume. Old volume retained until explicit deletion. | Backup is `verified`. Restore-test smoke recently green. KEK present. | 30 min – several hours (depends on size) | D (data risk) | Two-person approval; backup freshness checked |
| `regenerate-traefik-config.md` | Reconcile `routes` table → Traefik dynamic file provider. Used after agent loss, file corruption, or out-of-band edits. Atomic temp-file + rename; never truncate in place. | Routes table consistent in Postgres. | 1–5 min | B | None for non-production |
| `collect-support-bundle.md` | Run `midnight support-bundle` with redaction preview. Defaults: 24h window, 200 MB cap, `--include=logs,metrics,events,config`. Never includes secrets, tokens, sealed values, auth headers, cookies, private keys, raw env dumps. External upload is opt-in. | Operator has admin token. | 2–10 min | A | None for collection; opt-in for external upload |

---

## 2. MVP Beta additions (required)

The following are added at MVP Beta. The Phase 0 set continues to ship.

| Filename | Purpose | Preconditions | Expected duration | Class | Approvals |
| --- | --- | --- | --- | --- | --- |
| `certificate-renewal.md` | Force renewal or repair an in-progress ACME renewal. Includes LE rate-limit diagnostics, CAA recheck, and DNS-01 fallback for HTTP-01 hosts behind blocked port 80. | Domain is verified. ACME state file present and intact, or staged for restore. | 15–60 min | B | None for non-production; project admin in production |
| `host-drain.md` | Drain a host: stop accepting new placements, gracefully terminate stateless instances, refuse to reschedule volume-backed services without explicit approval. | Operator has host-admin role. Replacement capacity exists for stateless services. | 10–60 min | C | Required reviewer for production |
| `retry-webhook.md` | Re-deliver a webhook from `webhook_deliveries` history. Verify signature/replay invariants. Handle dead-letter → manual replay path. | Delivery record exists. Endpoint not disabled. | 1–5 min | B | None for non-production |
| `restore-platform.md` | Full platform restore order (`spec` lines 2050–2062): install compatible OS+Docker+Midnight → stop writers → restore `/etc/midnight` + `secrets.key` → restore Postgres → restore ACME state (or mark for reissue) → restore registry → start in maintenance mode → verify schema, key decryption, registry → restore tenant volumes → smoke → re-enable workers and ingress. ACME-state restore at scale throttled to ≤ 20 certs/hour with priority queue (TLS-expired first); LE staging for the first batch. | Backups present and verified. KEK + operator passphrase available. New host meets compatibility matrix. | 1–8 hours | D (data + production) | Two-person approval; CTO/operator-of-record signoff |
| `KEK-loss-incident.md` | Incident-response runbook for `/etc/midnight/secrets.key` loss. Documents what is recoverable (deployment metadata, non-secret config, registry, audit chain) and what is irrecoverable (encrypted secrets without a separate passphrased KEK backup). Triggers documented drill drill `kek-loss`. | Confirmed KEK loss; Postgres and operator passphrase status known. | Hours – days (depends on whether passphrase backup exists) | D | Two-person approval; security lead notified |
| `ACME-rate-limit-recovery.md` | Recover when LE per-registered-domain or per-account rate limit is hit. Switch to staging for verification, throttle to 40 issuances/registered-domain/week (panel decision, `spec` line 154), use DNS-01 fallback if HTTP-01 is rate-limited, prioritize TLS-expired then expiring-soon then healthy. | Operator has DNS provider credentials in the secret store if DNS-01 fallback is used. | 1–48 hours (LE rate-limit windows are weekly) | C | Project admin |

---

## 3. Future runbook additions (v1 Foundations and beyond)

These are not required for Phase 0 or MVP Beta but are planned. Track via the relevant epic when scheduled.

- `redeploy-from-snapshot.md` (v1)
- `database-major-upgrade.md` (v1, dump-and-restore only; in-place `pg_upgrade` out of scope per `spec` line 139)
- `volume-online-grow.md` (v1)
- `agent-version-skew.md` (v1)
- `multi-host-peer-add.md` (Phase 3)
- `multi-host-peer-remove.md` (Phase 3)
- `wireguard-key-rotation.md` (Phase 3)
- `oidc-mapping-debug.md` (v1)
- `mcp-incident-response.md` (when AI features ship)

---

## 4. Runbook template

Every runbook in `/usr/share/midnight/runbooks/` follows this shape. New runbooks copy this template; reviewers reject runbooks that omit a section. The template lives in the repo at `docs/runbooks/_template.md` and is the source of `midnight runbook describe <id>` output.

```markdown
---
id: <kebab-case-id>            # matches filename without `.md`
title: <human title>
class: A | B | C | D | E         # automation class from spec lines 1505-1511
panel_decision_refs:             # spec line numbers this runbook implements
  - L77
  - L203
related_reason_codes:            # failure reason codes this runbook resolves
  - ROLLBACK_IMAGE_UNAVAILABLE
  - IMAGE_SIGNATURE_INVALID
expected_duration_min: 1
expected_duration_max: 10
blast_radius: project | environment | service | host | platform
required_role: viewer | developer | deployer | project_admin | workspace_owner | host_admin
production_approvers: 0 | 1 | 2
prevent_self_approval: true | false
backup_freshness_required: true | false
schema_version: 1
package_version_pin: <version>
---

## Purpose

<One paragraph: what this runbook does, when to use it, when NOT to use it.>

## Preconditions

- [ ] <Each precondition is a checkbox. Operator MUST verify all.>
- [ ] Audit chain verifies (`midnight audit verify`).
- [ ] No active deployment in `DEPLOYING` for the affected service unless this runbook is the deploy.
- [ ] Backup freshness for data-touching steps: last successful backup within configured RPO.

## Inputs

| Name | Type | Description | Example |
| --- | --- | --- | --- |
| `service_id` | string | Target service ID (`svc_…`) | `svc_01HXY…` |
| ... | ... | ... | ... |

## Steps

1. <Numbered, idempotent. Each step has a verification check.>
   - Command: `midnight …`
   - Expected output: …
   - On failure: stop, run "Rollback" section.
2. …

## Verification

- [ ] <Each invariant the runbook is supposed to leave true.>
- [ ] Service state is `ACTIVE`.
- [ ] No new failure events emitted in the last 5 minutes.
- [ ] `midnight doctor` reports green for affected components.

## Rollback

<Explicit reverse-of-steps OR "Not applicable; this runbook itself is the rollback.">

If any step fails or verification fails:
1. <How to leave the system in a safe state.>
2. <When to escalate.>

## Audit and evidence

This runbook emits the following audit events:
- `runbook.executed` with id, actor, inputs, step results, approvals, final verification.
- Operation IDs for each platform action invoked.

Operator must capture:
- Runbook execution ID (printed on start).
- Any non-default inputs.
- Any approvals that were granted.

## Related

- Spec sections: <links into midnight-platform-plan.md by line number>
- Reason codes: <list>
- Other runbooks: <list>
- Drill: `test/drills/<id>_test.go` (if a drill exercises this runbook)
```

### 4.1 Walkthrough requirement

Before a runbook ships:

- The author writes the runbook.
- A second operator (not the author) walks through it on a test cluster, capturing every command and verification result.
- The walkthrough output is appended to the runbook's PR for review.
- Reviewer rejects runbooks where the walkthrough was skipped.

This satisfies the panel's "every promise must be enforceable" principle for ops procedures.

---

## 5. Runbook governance

| Concern | Owner |
| --- | --- |
| Runbook authorship | Sub-team that owns the surface (build/runtime/control-plane/security/ingress/storage/api/release) |
| Runbook review | Release/SRE lead + the surface owner |
| Walkthrough operator | Any team member who did not author the runbook |
| Runbook versioning | Pinned to `.deb` package version; published in `midnight runbook describe` output |
| Runbook deletion | Requires ADR; old runbooks remain in older `.deb` versions |
| Runbook update | Standard PR; if approval class changes, security lead must approve |
| Audit of runbook execution | `runbook_executions` table records actor, inputs, step results, approvals, logs, final verification (`spec` line 1518) |

---

## 6. Where this index sits in the source tree

- This file: `D:/projects/midnight/agile/handoff/OPERATIONS_RUNBOOK_INDEX.md`
- Runbook source markdown: `docs/runbooks/<id>.md`
- Runbook template: `docs/runbooks/_template.md`
- Shipped runbook path on installed host: `/usr/share/midnight/runbooks/<id>.md`
- CLI surface: `midnight runbook list`, `midnight runbook describe <id>` (Phase 0); `midnight runbook run <id>` (Phase 1)
- Related drills: `test/drills/<drill-id>_test.go`
- Related dashboard surface: `runbooks` table + UI in v1+

---

## 7. Quick reference: which runbook for which reason code

The `failed-deploy-triage.md` runbook is the entry point. Its decision tree maps reason codes to follow-up runbooks:

| Reason code | Primary follow-up runbook |
| --- | --- |
| `BUILD_FAILED`, `BUILD_TIMEOUT`, `DEPENDENCY_INSTALL_FAILED` | (no runbook; user fixes source) |
| `IMAGE_PULL_FAILED`, `EXTERNAL_DIGEST_MISSING` | `failed-deploy-triage.md` → registry diagnostics |
| `IMAGE_SIGNATURE_INVALID` | `failed-deploy-triage.md` → escalate to security lead |
| `ROLLBACK_IMAGE_UNAVAILABLE` | `failed-deploy-triage.md` → manual redeploy from source |
| `MISSING_REQUIRED_VARIABLE`, `VARIABLE_REFERENCE_FAILED` | (no runbook; user/operator fixes config) |
| `CONTAINER_EXITED`, `OOM_KILLED`, `CRASH_LOOP_BACKOFF` | `restart-service.md` then user investigation |
| `PORT_NOT_LISTENING`, `APP_BOUND_LOCALHOST` | (no runbook; user fixes app) |
| `HEALTHCHECK_FAILED` | `failed-deploy-triage.md` |
| `ROUTE_RECONCILIATION_FAILED` | `regenerate-traefik-config.md` |
| `TLS_ISSUANCE_FAILED`, `DNS_VERIFICATION_FAILED`, `INGRESS_PORT80_UNREACHABLE`, `DOMAIN_OWNERSHIP_LAPSED` | `domain-tls-failure.md`, `certificate-renewal.md` |
| `RESTORE_ACME_THROTTLED` | `ACME-rate-limit-recovery.md` |
| `VOLUME_MOUNT_FAILED`, `OWNERSHIP_CONFLICT` | (no runbook in Phase 0; operator follows storage docs) |
| `DISK_PRESSURE`, `BUILDS_BLOCKED_DISK_PRESSURE` | `disk-pressure.md` |
| `HOST_UNAVAILABLE`, `VOLUME_HOST_LOST`, `AGENT_RUNTIME_UNAVAILABLE` | `host-drain.md` (MVP Beta) → `restore-platform.md` if data-loss |
| `BACKUP_FAILED`, `RESTORE_FAILED`, `BACKUP_STALE`, `BACKUP_UNVERIFIED`, `BACKUP_OFFHOST_MISSING`, `BACKUP_SIZE_DEGRADED` | `backup-restore.md` (Phase 0) + escalate per code |
| `WAL_ARCHIVE_LAGGING` | (v1) `database-major-upgrade.md` precondition |
| `DOCKER_ENGINE_UNSUPPORTED`, `AGENT_CLOCK_SKEW` | `failed-deploy-triage.md` → installer/preflight diagnostics |
| `VERSION_SKEW_REJECTED`, `UPGRADE_SKIP_FORBIDDEN` | (operator follows upgrade docs; refer to compatibility matrix) |
| `PLATFORM_DEGRADED` | `restore-platform.md` (MVP Beta) |
| `IDEMPOTENCY_REQUEST_IN_PROGRESS`, `IDEMPOTENCY_KEY_REUSED`, `RESOURCE_VERSION_MISMATCH`, `OPERATION_SUPERSEDED`, `CANCEL_NOT_SAFE` | (no runbook; client retry pattern) |
| `RATE_LIMITED` | (operator tunes rate-limit class or client backs off) |
| `DEPLOY_PROVENANCE_UNVERIFIED` | `failed-deploy-triage.md` → escalate to security lead |
| `BUILD_OUTPUT_SECRET_LEAK`, `BUILD_SECRET_LEAK_DETECTED` | `failed-deploy-triage.md` → escalate to security lead immediately; suspect token rotation |

This table is updated whenever the error-code registry (`docs/error-codes.md`) gains a new entry. CI lints that every reason code with a class > A has at least one runbook listed here or an explicit "no runbook" notation.
