# External Railway-Parity Runner Evidence

External parity modes execute operator-supplied runners against infrastructure
that the repository cannot provision by itself. Exit status is not accepted as
proof. The runner must test a clean committed worktree and emit a
`midnight.parity.runner.v1` manifest plus retained, hash-bound artifacts.

## Runner Environment

The loop exports:

- `MIDNIGHT_PARITY_EVIDENCE_MANIFEST`: manifest output path.
- `MIDNIGHT_PARITY_EVIDENCE_ROOT`: private temporary evidence directory.
- `MIDNIGHT_PARITY_EVIDENCE_ARTIFACT_DIR`: directory for proof artifacts.
- `MIDNIGHT_PARITY_EVIDENCE_MODE`: requested mode.
- `MIDNIGHT_PARITY_COMMIT_SHA`: exact clean commit under test.
- `MIDNIGHT_PARITY_RUN_STARTED_AT`: UTC RFC3339 invocation time.
- `MIDNIGHT_PARITY_RUN_NONCE`: unique nonce that must be copied into the
  manifest.

Artifact paths in the manifest are relative to
`MIDNIGHT_PARITY_EVIDENCE_ROOT`, must name non-empty regular files, and may not
escape that directory. The validator recalculates each SHA-256 digest, scans
manifest and artifact content for credential-shaped material, rejects any
undeclared file/symlink/special file, enforces fresh timestamps and a unique
instance ID, and retains the complete bundle under
`.midnight/parity-evidence/external/<mode>/`. Each required assertion uses a
different structured JSON artifact; one shared log or self-attested string
cannot satisfy the contract.

The loop checks for a clean committed worktree before launch, immediately
after the runner returns, and again before retention. A runner that changes
`HEAD` or the worktree invalidates the run.

The runner must remove the provisioned instance and temporary external
resources before returning. `cleanup_passed` and the required `cleanup`
assertion describe that completed proof; they are not permission for the loop
to delete external resources itself.

## Manifest Shape

```json
{
  "schema_version": "midnight.parity.runner.v1",
  "mode": "fault",
  "commit_sha": "<MIDNIGHT_PARITY_COMMIT_SHA>",
  "run_nonce": "<MIDNIGHT_PARITY_RUN_NONCE>",
  "fresh_instance_id": "vm-20260728-001",
  "started_at": "2026-07-28T10:00:00Z",
  "completed_at": "2026-07-28T10:14:00Z",
  "assertions": [
    {
      "name": "process_death_matrix",
      "passed": true,
      "artifact": "artifacts/process-death.json"
    }
  ],
  "components": {
    "midnight": "2b3cb4b",
    "docker": "28.3.2"
  },
  "redaction_passed": true,
  "cleanup_passed": true,
  "artifacts": [
    {
      "path": "artifacts/process-death.json",
      "sha256": "<lowercase SHA-256>",
      "media_type": "application/json",
      "size_bytes": 4812
    }
  ]
}
```

Every assertion must reference a declared artifact. Assertion names are unique,
all assertions must pass, component names/versions must be non-empty, and a
previously retained `fresh_instance_id` cannot be reused for the same mode.
The example shows one assertion record; a real manifest must contain the full
required set for its mode.

Each assertion artifact has this shape:

```json
{
  "schema_version": "midnight.parity.assertion.v1",
  "mode": "fault",
  "assertion": "process_death_matrix",
  "commit_sha": "<MIDNIGHT_PARITY_COMMIT_SHA>",
  "run_nonce": "<MIDNIGHT_PARITY_RUN_NONCE>",
  "fresh_instance_id": "vm-20260728-001",
  "observations": {
    "fault_points_total": 12,
    "fault_points_passed": 12,
    "max_recovery_seconds": 18.4
  },
  "evidence": [
    "12 named process-death boundaries recovered within the convergence budget"
  ]
}
```

The identity fields must match the manifest and current invocation.
`observations` is machine-validated for the assertion, while `evidence`
contains concise reviewer-facing findings. Counts must be positive where a
test is claimed, paired tested/passed counts must match, probe statuses must be
2xx/3xx, verification booleans must be true, and residual/credential/duplicate
side-effect counts must be zero.

## Required Assertions

| Mode | Required assertion names |
|---|---|
| `dataplane` | `fresh_install`, `source_build`, `image_deploy`, `official_databases`, `runtime_replacement`, `redaction`, `cleanup` |
| `fault` | `fresh_install`, `process_death_matrix`, `durable_recovery`, `duplicate_suppression`, `redaction`, `cleanup` |
| `observability` | `fresh_install`, `build_logs`, `runtime_logs`, `http_logs`, `metrics`, `notifications`, `redaction`, `cleanup` |
| `vm` | `fresh_install`, `package_install`, `source_deploy`, `public_https`, `restart_recovery`, `upgrade_rollback`, `backup_restore`, `uninstall_cleanup`, `redaction`, `cleanup` |
| optional external `restore` extension | `fresh_install`, `off_host_backup`, `source_destroyed`, `restore`, `application_probe`, `redaction`, `cleanup` |

## Required Observation Keys

| Assertion | Required `observations` keys |
|---|---|
| `fresh_install` | `installation_id`, `provisioned_at`, `empty_state_verified` |
| `source_build` | `source_fixture`, `image_digest`, `probe_status` |
| `image_deploy` | `image_reference`, `image_digest`, `probe_status` |
| `official_databases` | `engines_tested`, `engines_passed`, `persistence_verified` |
| `runtime_replacement` | `replacements_tested`, `replacements_passed`, `persistence_verified` |
| `process_death_matrix` | `fault_points_total`, `fault_points_passed`, `max_recovery_seconds` |
| `durable_recovery` | `operations_recovered`, `terminal_state_verified` |
| `duplicate_suppression` | `duplicates_injected`, `duplicate_side_effects` |
| `build_logs`, `runtime_logs`, `http_logs` | `records_captured`, `correlation_verified` |
| `metrics` | `samples_captured`, `aggregation_verified` |
| `notifications` | `deliveries_attempted`, `deliveries_confirmed` |
| `package_install` | `package_version`, `services_healthy` |
| `source_deploy` | `source_fixture`, `deployment_id`, `probe_status` |
| `public_https` | `hostname`, `status_code`, `certificate_verified` |
| `restart_recovery` | `services_restarted`, `application_probe_status` |
| `upgrade_rollback` | `upgrade_version`, `rollback_version`, `application_probe_status` |
| `backup_restore` | `backup_id`, `restore_id`, `application_probe_status` |
| `uninstall_cleanup` | `resources_created`, `residual_resources` |
| `off_host_backup` | `backup_id`, `artifact_count`, `checksums_verified` |
| `source_destroyed` | `source_absent`, `destruction_verified` |
| `restore` | `restore_id`, `restored_artifact_count`, `checksums_verified` |
| `application_probe` | `probe_url`, `status_code`, `content_verified` |
| `redaction` | `files_scanned`, `credential_findings` |
| `cleanup` | `resources_created`, `residual_resources` |

Runners may add narrower assertions and artifacts. The required set is the
minimum evidence boundary, not a complete test plan.
