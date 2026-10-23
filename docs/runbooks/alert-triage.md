# Alert Triage Runbook

Status: operator draft.

This runbook is the default landing point for alerts that do not have a more
specific runbook. It is intentionally short and action-oriented.

## When to use

Use this runbook when one of the following alerts fires and a more specific runbook is not linked:

- `AgentLost` — host agent heartbeat is stale.
- `CertExpiringSoon` — managed certificate expires within seven days.
- `DeployJobStuck` — build/deploy queue heartbeat gap exceeds 3x lease TTL.
- `BuildKitCacheCorruption` — BuildKit cache verification reported a failure.
- `ConntrackPressure` — conntrack table usage above the warning threshold.

## Triage steps

1. Read the alert annotation to identify the affected `host_id`, `service`, or `domain` label.
2. Capture the latest support bundle: `midnight support-bundle --preview` to confirm scope, then
   `midnight support-bundle --out triage.tar.zst`.
3. Open `manifest.json` in the bundle and confirm `redaction.passes_run > 0`. Reject any bundle
   that did not run redaction before sharing externally.
4. Inspect the alert's primary metric in Prometheus over the last 30 minutes. If the metric is
   stale, suspect collector or agent failure first.
5. If the alert resolves on its own within five minutes, record the incident in the operator
   timeline and close. Otherwise escalate per the linked failure code.

## Linked failure codes

- `BACKUP_OFFHOST_MISSING` (when triggered alongside backup alerts)
- `SUPPORT_BUNDLE_BUDGET_EXCEEDED` (when bundle generation fails during triage)
- `BACKUP_VERIFY_CHECKSUM_MISMATCH` (when restore verification fails during incident response)

## Verification

The alert recovers when its primary metric returns to the normal band for at least the alert's
`for:` duration and no follow-on alerts fire within ten minutes of recovery.

## Rollback

If a remediation made the situation worse, revert the latest config change and re-run
`midnight doctor` to confirm health regression. Use the rollback step recorded in the restore
plan when the change involved a restore action.
