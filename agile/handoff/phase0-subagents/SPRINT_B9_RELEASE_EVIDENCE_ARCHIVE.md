# Phase 0 Beta Sprint B9 Release Evidence Archive

Owner: Worker 6

Updated: 2026-05-01

## Purpose

Sprint B9 codifies the local-only archive run for canonical Phase 0 gate evidence. It follows the B4 proof-packet contract, keeps the B5-B8 host-drill guardrails intact, and writes phase0-prefixed release files that can be archived without committing ignored artifacts.

B9 does not add live host probes, does not change `test/drills/manifest.yaml`, does not expand the accepted drill ID range, and does not promote `D-20` through `D-23`.

## Canonical Command

Run from the repository root:

```sh
mkdir -p artifacts/phase0
go run ./tools/phase0-gate \
  --format=json \
  --execute-drills \
  --require-executed-drills \
  --release-evidence artifacts/phase0/phase0-release-evidence.json \
  --release-evidence-index artifacts/phase0/phase0-release-evidence-index.json \
  --verify-release-evidence-index artifacts/phase0/phase0-release-evidence-index.json \
  --verify-evidence-files \
  --release-summary artifacts/phase0/phase0-release-summary.json
```

The command writes ignored local artifacts only. Do not stage or commit files under `artifacts/phase0`.

## Expected Refs And Files

| Entry | Expected path | Purpose |
| --- | --- | --- |
| Archive ref `gate-release-evidence` | `artifacts/phase0/phase0-release-evidence.json` | Full local gate evidence packet. |
| Generated file | `artifacts/phase0/phase0-release-summary.json` | Compact deterministic release summary. |
| Generated file | `artifacts/phase0/phase0-release-evidence-index.json` | Archive manifest for the release evidence packet. |
| Archive ref `drill-manifest` | `test/drills/manifest.yaml` | Existing Phase 0 drill manifest input. |
| Archive ref `prometheus-alerts` | `deploy/prometheus/alerts.yaml` | Existing Phase 0 alert-pack input. |

All archive refs must be workspace-relative, redacted, and safe to move between workspaces. If extra operator refs are needed later, use the B4 cross-slice names and paths; do not invent absolute paths or placeholder files.

## Local-Only Boundary

B9 may run `tools/phase0-gate`, read the existing drill manifest and alert pack, write deterministic JSON under `artifacts/phase0`, and verify the archive index with `--verify-evidence-files`.

B9 must not install packages, start or stop services, query Docker or Traefik, probe DNS or public routes, mutate WireGuard, flush conntrack, mount volumes, query registries, capture dashboard screenshots, read secrets, or create evidence that implies live host execution.

## Return Paths

- Return to B4 when archive refs cannot be represented as redacted, workspace-relative proof-packet refs.
- Return to the local gate owner when the canonical command fails, `releaseReady` is false, index verification fails, or phase0-prefixed output paths are not accepted.
- Return to B1/B2/B3 only when a future archive run adds their operator refs and those redacted artifacts are missing or incomplete.
- Return to B5/B6/B7/B8 manifest-gate ownership when a requested archive depends on `D-20` through `D-23`, accepted ID-range changes, executable manifest rows, or new harness assertions.
- Return to Phase 3 domain owners for live networking, runtime, registry cache, or volume attach evidence.

## Relation To B4 And B8

B9 is the archive-readiness form of B4. B4 defines the portable proof-packet contract and cross-slice operator refs; B9 defines the local canonical archive command and phase0-prefixed filenames for release evidence.

B9 preserves the B8 boundary. Reserved IDs `D-20` through `D-23` remain outside the executable manifest, outside the accepted ID range, and outside B9 evidence claims.

## Acceptance Criteria

1. The canonical B9 command exits `0` from the repository root.
2. `artifacts/phase0/phase0-release-evidence.json`, `artifacts/phase0/phase0-release-evidence-index.json`, and `artifacts/phase0/phase0-release-summary.json` are written locally and remain uncommitted ignored artifacts.
3. `releaseReady=true` appears in the release evidence and summary.
4. Archive index verification passes with `--verify-evidence-files`.
5. All refs in the archive index are workspace-relative and do not escape the workspace.
6. No live host probes, manifest ID-range changes, or `D-20` through `D-23` promotion are introduced.
