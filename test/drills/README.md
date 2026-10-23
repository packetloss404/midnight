# Failure Drills

S08 owns the Phase 0 drill harness. The canonical path is `test/drills/`.

`manifest.yaml` lists the initial placeholder drills for alerting, backup failure handling, and support bundle collection.

## Multi-Host Compatibility

Phase 0 drills may run on a single VM, but drill evidence must not assume that the control plane, runtime host, registry, ingress, or backup destination share one filesystem or one process namespace. Evidence refs should be workspace-relative paths, host identity should be recorded as data instead of inferred from the runner, and drill diagnostics should name the impacted host or component when the underlying planner exposes it.

The Phase 3 multi-host drill expansion should add explicit host-scoped assertions for scheduler reschedule, WireGuard peer churn, key rotation overlap, PMTU regression, IPAM quarantine, and conntrack flush. Those drills can reuse the current manifest envelope (`id`, `reason-code`, `event`, `diagnostic`, `runbook`) without changing Phase 0 evidence consumers.

Sprint B6 carries the first small candidate catalog in `metadata.tags`, `metadata.notes`, and `metadata.subcases` on existing drill rows. Candidate rows remain local-only and planner-only; evidence refs are workspace-relative paths under `artifacts/phase3/drills/...` and do not claim live host execution. The current catalog covers `networking`, `runtime_adapter`, `registry_cache`, and `volume_attach`.

Sprint B7 keeps future dedicated Phase 3 host-drill IDs out of the canonical Phase 0 manifest. `phase3-reservations.yaml` is a local-only reservation index for D-20 through D-23, mapping each future-rig, planner-only reservation back to its B6 carrier row in `manifest.yaml`.

Sprint B8 adds `phase3-promotion-readiness.yaml` as a local-only promotion checklist for reserved D-20 through D-23. Each row must remain outside `manifest.yaml` until it has an accepted ID range, executable rig, harness assertion, live evidence reference, and rollback boundary with workspace-relative evidence under `artifacts/phase3/promotion/...`.

Sprint B9 adds `phase0-release-evidence-archive.yaml` as a local-only archive contract for the canonical Phase 0 gate evidence files. It names the required `artifacts/phase0/...` evidence outputs plus operator notes, runner OS, commit refs, and the gate command record without creating tracked archive artifacts or promoting D-20 through D-23.

## Next Release Evidence Slice

The next useful slice is a real run of the canonical Phase 0 gate command that writes:

- `artifacts/phase0/phase0-release-evidence.json`
- `artifacts/phase0/phase0-release-evidence-index.json`
- `artifacts/phase0/phase0-release-summary.json`

Archive the exact command, commit SHA, runner OS, and any operator notes as extra workspace-relative evidence refs. The index should be verified with `--verify-evidence-files` before sign-off.
