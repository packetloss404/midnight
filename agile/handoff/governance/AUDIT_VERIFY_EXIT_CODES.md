---
id: GOV-AUDIT-VERIFY-EXIT-CODES
decision_ref: P0-D017
status: decided
owner: PM-B / Security
deciders: [PM-B, Security, S03 owner, S07 owner, Lane 4 implementer]
related_decisions: []
related_features: [FEATURE_AUDIT_HASH_CHAIN]
related_adrs: [ADR-011]
date: 2026-04-27
---

# `midnight audit verify` Exit Codes (P0-D017)

This document closes `PHASE0_DECISION_LOG.md` row **P0-D017 — Audit
verify phase, canonical row, and exit codes**. It ratifies Lane 4's
post-implementation proposal (see `agile/handoff/phase1-subagents/
LANE4_STATUS.md` § "P0-D017 closure proposal") as the panel-locked
envelope for the `midnight audit verify` CLI shipped in Phase 1.

## Decision

| Code | Meaning | Class | Source |
|------|---------|-------|--------|
| `0`  | healthy — every row hash matches; every checkpoint signature verifies. | panel-locked | ADR-011 §Decision item 6; FEATURE_AUDIT_HASH_CHAIN.md §Verify Algorithm step 6. |
| `1`  | warnings only — non-contiguous `prev_checkpoint_id` at a pruned partition boundary (acceptable per spec when a `final` checkpoint exists). | panel-locked | Same. |
| `2`  | chain mismatch / tamper detected — recomputed `row_hash` ≠ stored, `prev_hash` ≠ predecessor's `row_hash`, or checkpoint `last_row_hash` ≠ chain head. | panel-locked | Same. |
| `3`  | signature failure or signing key missing — any `audit_checkpoint` signature fails or the matching public key cannot be loaded. Surfaces failure codes `AUDIT_CHECKPOINT_SIGNATURE_INVALID` / `AUDIT_KEY_NOT_FOUND`. | panel-locked | Same. |
| `64` | usage error — bad flags, conflicting flags (`--from-jsonl` and `--from-db` both set), unknown subcommand. | sysexits-aligned | `EX_USAGE` per `<sysexits.h>`. |
| `66` | missing input — `--from-jsonl PATH` does not exist; `--from-db URL` invoked before Lane 1 wires the live Postgres source. | sysexits-aligned | `EX_NOINPUT` per `<sysexits.h>`. |

## Rationale

The brief's defaults ("0=ok, 1=usage, 2=mismatch, 3=missing") are
overridden because **midnight-platform-plan.md panel decisions said
otherwise** (the brief explicitly carves an "unless ..." escape):
ADR-011 §Decision item 6 reserves `1` for warnings and `3` for
signature/key failure. Usage and missing-input collide if `1`/`3` are
kept panel-locked, so we route those to the sysexits codes `64` / `66`
and document them as secondary.

The split has three properties we want to preserve:

1. **Panel-locked codes are stable across SIEM consumers.** Splunk /
   Elastic alert on `0/1/2/3`; new minor releases never widen or shift
   that envelope.
2. **Sysexits codes are panel-secondary but unambiguous.** Operators
   running the CLI by hand (or in shell scripts) get a sysexits-shaped
   answer for the cases that aren't about the audit chain itself. `64`
   and `66` are the only post-panel additions; we will not allocate
   further codes without an ADR change.
3. **Verifier never panics or returns `1` for a non-audit failure.**
   The Lane 4 implementation funnels every error through the typed
   `audit.VerifyReport` / `audit.OutcomeXxx` constants; any uncategorized
   error path is treated as `1` only if the verifier finished and produced
   a report. Pre-report errors (file open failures, key load failures
   with `os.ErrNotExist`) collapse to `66`; signing-key load failures
   that are not `os.ErrNotExist` collapse to `3` to preserve the panel
   semantics.

## SIEM consumer guidance

This is the contract we are asking Splunk / Elastic / Datadog
operators to encode:

| Code | Alert? | Suggested transformation |
|------|--------|--------------------------|
| `0`  | No alert. | Index the `VerifyReport` JSON as informational; surface in operator dashboards under "Audit chain healthy". |
| `1`  | **No alert by default**; informational only. Optionally alert if the warning persists across two consecutive `audit verify --from-db` runs (operator-tunable). | `severity=info`. The verifier already logs the partition boundary; surface only the most recent partition gap. |
| `2`  | **Alert immediately, page on-call.** | `severity=critical`. Index every reported row id; cross-reference against `audit_logs`/`audit_checkpoints` row id range. |
| `3`  | **Alert immediately, page on-call AND security on-call.** | `severity=critical`. Surface the `signing_key_fingerprint` of the offending checkpoint; trigger the key-rotation runbook (`docs/runbooks/key-rotation.md` placeholder; security owns this runbook in Phase 2). |
| `64` | No alert by default; this is operator misuse, not an audit-chain finding. Optionally page after N occurrences. | `severity=warning`. Useful as a tripwire for badly written CI invocations. |
| `66` | No alert by default for `--from-db` (Phase 1: this is the documented "Lane 1 not yet wired" outcome). **Alert** for `--from-jsonl PATH` if the path is supposed to be the canonical export — that means an exporter or operator pipeline misconfigured the path. | `severity=warning`. Can be promoted to `severity=critical` per environment if `--from-jsonl` is operator-managed. |

For Splunk users, the canonical search to drive the alert table:

```
sourcetype=midnight_audit_verify
| stats count by exit_code
| eval class = case(
    exit_code == 0, "healthy",
    exit_code == 1, "warnings",
    exit_code == 2, "tamper",
    exit_code == 3, "signature_failure",
    exit_code == 64, "usage",
    exit_code == 66, "missing_input",
    true(), "unknown")
| where class IN ("tamper","signature_failure")
```

For Elastic, equivalent KQL:

```
event.dataset:"midnight.audit.verify" AND audit.exit_code:(2 OR 3)
```

## Cross-references

- Lane 4 implementation: `cmd/midnight/audit_verify.go`. The exit-code
  constants are at the top of the file:
  - `auditVerifyExitOK = 0`
  - `auditVerifyExitWarn = 1`
  - `auditVerifyExitChainBreak = 2`
  - `auditVerifyExitSignatureBreak = 3`
  - `auditVerifyExitUsage = 64` (`EX_USAGE`)
  - `auditVerifyExitMissingInput = 66` (`EX_NOINPUT`)
- Lane 4 tamper test: `cmd/midnight/audit_verify_test.go`
  (`TestRunAuditVerifyChainBreakExit2`) produces a chain, mutates row 2,
  runs `audit verify`, and asserts exit code 2 with row 2 identified in
  the report.
- Audit-chain math + canonical-row encoder + verifier: `internal/audit/`
  (`canonical.go`, `chain.go`, `verify.go`, `keys.go`, `checkpoint.go`,
  `record.go`).
- Postgres-backed `AuditChain` (Lane 5 wireup): `internal/audit/`
  (`pg_chain.go`) — the Postgres source the `--from-db` path will
  consume once the CLI flag is upgraded out of its current `EX_NOINPUT`
  stub; tracked as a Phase 2 follow-up in Lane 5's wireup notes (see
  `agile/handoff/phase1-subagents/SPRINT1_FINISH_STATUS.md` and the
  Sprint 1 merge log on `main`).
- ADR-011: `agile/engineering/adr/ADR-011-audit-log-hash-chain-mvp.md`
  §Decision item 6, §Compliance / Verification.
- Feature dossier: `agile/features/FEATURE_AUDIT_HASH_CHAIN.md`
  §Verify Algorithm step 6, §Failure Modes, §Test Plan.
- Failure codes: `docs/error-codes.md` rows
  `AUDIT_CHAIN_BROKEN`, `AUDIT_CHECKPOINT_SIGNATURE_INVALID`,
  `AUDIT_KEY_NOT_FOUND` (added by Lane 5 wireup follow-up,
  commit `b2ab788` on `main`).

## PM-B / Security sign-off block

```yaml
audit_verify_exit_codes_signoff:
  decision_id: P0-D017
  status: decided
  ratifies: lane4_proposal
  panel_locked_codes: [0, 1, 2, 3]
  sysexits_codes: [64, 66]
  cli_implementation_ref: cmd/midnight/audit_verify.go
  test_evidence_ref: cmd/midnight/audit_verify_test.go
  signers:
    pm_b:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
    security:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
```

> Note: this sign-off block is a placeholder. Agent 4 (the Phase 0
> closure agent) cannot sign on PM-B / Security's behalf. The
> placeholder dates are filled by the named humans before
> `v0.1.0-alpha` (final) is tagged.
