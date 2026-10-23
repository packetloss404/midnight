# Operator Readiness Runbook

Operator readiness is a local aggregation contract for product completeness. It uses already-computed planner and gate outputs; it must not call live services or inspect host state.

## Statuses

| Status | Meaning |
| --- | --- |
| `ready` | Local evidence passed and the domain is ready to proceed. |
| `needs-review` | Evidence is missing, incomplete, warning-only, or intentionally skipped. |
| `blocked` | Local evidence found a blocker that must be fixed before proceeding. |

## Required Evidence

| Phase | Domain | Local evidence to provide |
| --- | --- | --- |
| 0 | `install_gate` | Generated automatically from `doctor.NewReadinessSummary`. |
| 1 | `doctor_snapshot` | Generated automatically from `doctor.NewReadinessSummary`. |
| 2 | `notifications` | Result of notification validation or delivery-preview planning, for example `notifications.BuildDeliveryPreview`. |
| 2 | `marketplace` | Result of local marketplace/template validation, for example `templates.SearchMarketplace` or official database template gates. |
| 3 | `networking` | Result of local networking planners, for example internal DNS readiness, WireGuard planning, route planning, or TCP proxy planning. |
| 3 | `runtime_adapter` | Runtime adapter conformance or acceptance report, for example `runtime/conformance.RunAcceptanceSubset`. |
| 3 | `registry_cache` | Registry cache plan status from `registry.PlanCache`. |
| 3 | `volume_attach` | Volume attach/detach plan status from `backup.PlanVolumeAttachments`. |

## Triage

1. If aggregate status is `blocked`, inspect the first blocked domain and fix that planner or gate input before rerunning readiness.
2. If status is `needs-review`, check `missing_domains` first. Missing evidence means no local planner result was supplied for a required domain.
3. If all required domains are present, inspect domain gates with `needs-review` and decide whether to accept warnings or collect stronger local evidence.
4. Do not replace missing evidence with live probes. Add or wire deterministic planner output instead.

## Local-Only Rule

The readiness model is intentionally side-effect free. Callers may supply gate evidence that was already computed by existing planners, but readiness aggregation itself must not send notifications, query marketplaces, create network interfaces, run runtime adapters against clusters, call registry APIs, mount volumes, or mutate services.

## Multi-Host Guardrails

The single-VM MVP profile is an implementation profile, not a contract boundary. Readiness evidence must keep host identity, runtime adapter, network planner, registry/cache planner, and volume attachment evidence as separate domains so future multi-host callers can submit multiple host-scoped planner results without changing the aggregate status model.

Do not encode readiness as "this VM is ready" or rely on absolute filesystem paths in release evidence. Use workspace-relative evidence refs and include host or component identity inside the evidence payload. A future multi-host caller should be able to aggregate one control-plane readiness result plus N host/runtime/network results and produce the same `ready`, `needs-review`, or `blocked` verdicts.

## MVP Evidence Packet

The MVP readiness packet is a portable index of local gate output plus redacted operator evidence from the install path, live deploy check, and dashboard/API review. It does not create new live behavior. It packages evidence that already exists and verifies that refs stay workspace-relative so the packet can move between workspaces without host-local assumptions.

Required packet refs:

| Packet ref | Required artifact | Source |
| --- | --- | --- |
| `gate-release-evidence` | `artifacts/mvp/release-evidence.json` | Local release gate run. |
| `gate-release-summary` | `artifacts/mvp/release-summary.json` | Local release gate run. |
| `drill-manifest` | `test/drills/manifest.yaml` | Existing gate input. |
| `prometheus-alerts` | `deploy/prometheus/alerts.yaml` | Existing alert pack. |
| `live-deploy-check` | `artifacts/mvp/live-deploy/<run-id>/operator-notes.md` plus redacted deploy evidence | Live deploy check. |
| `operator-install-path` | `artifacts/mvp/operator-install/<run-id>/operator-notes.md` or equivalent redacted install note | Operator install path. |
| `operator-dashboard` | `artifacts/mvp/operator-dashboard/<run-id>/operator-notes.md` plus redacted screenshots or dashboard capture notes | Dashboard/API review. |

First seed or refresh the local gate outputs:

```sh
mkdir -p artifacts/mvp
go run ./tools/phase0-gate \
  --format=json \
  --execute-drills \
  --require-executed-drills \
  --release-evidence artifacts/mvp/release-evidence.json \
  --release-evidence-index artifacts/mvp/release-evidence-index.json \
  --verify-release-evidence-index artifacts/mvp/release-evidence-index.json \
  --verify-evidence-files \
  --release-summary artifacts/mvp/release-summary.json
```

Then build and verify the packet index with the operator refs that exist for the run:

```sh
go run ./tools/phase0-gate \
  --format=json \
  --execute-drills \
  --require-executed-drills \
  --release-evidence artifacts/mvp/release-evidence.json \
  --release-evidence-index artifacts/mvp/release-evidence-index.json \
  --verify-release-evidence-index artifacts/mvp/release-evidence-index.json \
  --verify-evidence-files \
  --release-summary artifacts/mvp/release-summary.json \
  --evidence-ref operator-notes=artifacts/mvp/operator-notes.md \
  --evidence-ref gate-release-summary=artifacts/mvp/release-summary.json \
  --evidence-ref live-deploy-check=artifacts/mvp/live-deploy/<run-id>/operator-notes.md \
  --evidence-ref operator-install-path=artifacts/mvp/operator-install/<run-id>/operator-notes.md \
  --evidence-ref operator-dashboard=artifacts/mvp/operator-dashboard/<run-id>/operator-notes.md
```

The packet passes when both commands exit `0`, `releaseReady` is true in release evidence and summary, index verification passes with `--verify-evidence-files`, and every extra ref resolves to a redacted, shareable file. If a live slice is not available yet, omit that `--evidence-ref` and mark the aggregate `needs-review`; do not create placeholder files for missing evidence.

## Local-Only Boundary

The packet command may run `tools/phase0-gate`, read the drill manifest, read the alert pack, write deterministic release evidence JSON, write the release evidence index, verify workspace-relative refs, and read referenced files when `--verify-evidence-files` is set.

It must not install packages, enable systemd units, start or stop Midnight services, query Docker, query Traefik, probe DNS or public routes, read raw DSNs or private keys, inspect host-absolute route files, or capture dashboard screenshots. Those activities belong to the install path, live deploy check, and dashboard/API review. The packet accepts their redacted operator notes and artifact refs as inputs.

## Return Paths

- Back to the live deploy owner when `live-deploy-check` is missing, the operator notes do not name the public route and immutable image digest, or the checklist lacks deploy response, operation events, Docker runtime, Traefik stat, and route response evidence.
- Back to the install owner when `operator-install-path` is missing, install notes do not identify the package version and commit, or package boot evidence lacks doctor, systemd, Postgres, Traefik, Docker, and first-boot status.
- Back to the dashboard owner when `operator-dashboard` is missing, dashboard proof lacks the dashboard state being shown, or screenshots/captures are not redacted and workspace-relative.
- Back to the local gate owner when the gate cannot produce or verify release evidence, the evidence index, or the release summary.
- Back to the relevant domain owner for future live networking, runtime, registry cache, or volume attach evidence.
