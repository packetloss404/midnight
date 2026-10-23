# Disk Pressure Runbook

Status: operator draft.

The `DiskPressure` alert fires when `midnight_disk_used_ratio` for any tracked root crosses the
critical threshold (default 0.85). The block threshold is 0.92 and the warn threshold is 0.75 per
the Storage section of the platform plan.

## Preconditions

- Operator has shell access to the affected host.
- The latest backup completed successfully (`BackupFailed` is not also firing).
- Disk pressure thresholds have not been overridden below their locked defaults.

## Triage steps

1. Identify the affected mount from the alert label (`root` is one of `volumes`, `registry`,
   `buildkit`, `logs`, `postgres`, `backups`).
2. Run `df -h <mount>` to confirm the metric. If the host metric and Prometheus disagree by more
   than 5%, suspect the agent reporter and capture a support bundle for triage.
3. Run `midnight doctor --json` and verify `OR-CHK-080` and any disk-related checks. Treat any
   `2 fail` exit code as a hard block before remediating.

## Remediation

- For `buildkit` and `registry` mounts: prune cache and unreferenced blobs per the build/registry
  runbook. Do not enable destructive registry GC if the `registry.gc` feature flag is disabled.
- For `logs`: rotate or ship the oldest cursor outside the support-bundle window (default 24h).
- For `postgres`: do not delete WAL or relation files. Escalate to a Postgres admin and capture a
  logical dump first.
- For `backups`: confirm off-host destination is configured (`BackupOffhostMissing`). Move the
  oldest backup to off-host before deleting.

## Verification

After remediation, the metric should return below 0.75 within ten minutes. If it stays at or
above 0.85, leave the host in degraded mode and escalate per the linked failure code.

## Rollback

If a deletion accidentally removed in-use data, immediately stop writers and restore from the
most recent backup using the offline restore plan generator
(`internal/backup.GenerateRestorePlan`). Each generated step has a stable idempotency key that
ensures retries do not double-apply.

## Linked failure codes

- `BACKUP_OFFHOST_MISSING`
- `SUPPORT_BUNDLE_BUDGET_EXCEEDED`
