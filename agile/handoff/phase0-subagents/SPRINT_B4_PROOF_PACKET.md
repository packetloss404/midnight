# Phase 0 Beta Sprint B4 Proof Packet Handoff

Owner: Worker 6

Updated: 2026-05-01

## Purpose

Sprint B4 defines the canonical Phase 0 Beta proof packet. It turns the local release gate outputs plus redacted B1/B2/B3 operator evidence into one portable evidence index that PM-B and release reviewers can inspect without rerunning live infrastructure.

B4 does not replace the B1 live deploy proof, the B2 operator install path, or the B3 operator dashboard proof. It depends on those slices and sends work back to their owners when their artifacts are missing or incomplete.

## Required Packet Contents

| Ref name | Evidence | Source |
| --- | --- | --- |
| `gate-release-evidence` | `artifacts/phase0/release-evidence.json` | B4 local gate run. |
| `gate-release-summary` | `artifacts/phase0/release-summary.json` | B4 local gate run. |
| `drill-manifest` | `test/drills/manifest.yaml` | Existing Phase 0 gate input. |
| `prometheus-alerts` | `deploy/prometheus/alerts.yaml` | Existing alert pack. |
| `b1-live-deploy-proof` | Redacted B1 operator note under `artifacts/phase0/sprint-b1-live-deploy/<run-id>/` | B1 owner. |
| `b2-operator-install-path` | Redacted B2 operator note under `artifacts/phase0/sprint-b2-operator-install/<run-id>/` | B2 owner. |
| `b3-operator-dashboard` | Redacted B3 operator note under `artifacts/phase0/sprint-b3-operator-dashboard/<run-id>/` | B3 owner. |

## Canonical Command

Run from the repository root after the live/operator slices have produced their notes.

First seed or refresh the local gate outputs:

```sh
mkdir -p artifacts/phase0
go run ./tools/phase0-gate \
  --format=json \
  --execute-drills \
  --require-executed-drills \
  --release-evidence artifacts/phase0/release-evidence.json \
  --release-evidence-index artifacts/phase0/release-evidence-index.json \
  --verify-release-evidence-index artifacts/phase0/release-evidence-index.json \
  --verify-evidence-files \
  --release-summary artifacts/phase0/release-summary.json
```

Then build and verify the cross-slice packet index:

```sh
go run ./tools/phase0-gate \
  --format=json \
  --execute-drills \
  --require-executed-drills \
  --release-evidence artifacts/phase0/release-evidence.json \
  --release-evidence-index artifacts/phase0/release-evidence-index.json \
  --verify-release-evidence-index artifacts/phase0/release-evidence-index.json \
  --verify-evidence-files \
  --release-summary artifacts/phase0/release-summary.json \
  --evidence-ref operator-notes=artifacts/phase0/operator-notes.md \
  --evidence-ref gate-release-summary=artifacts/phase0/release-summary.json \
  --evidence-ref b1-live-deploy-proof=artifacts/phase0/sprint-b1-live-deploy/<run-id>/operator-notes.md \
  --evidence-ref b2-operator-install-path=artifacts/phase0/sprint-b2-operator-install/<run-id>/operator-notes.md \
  --evidence-ref b3-operator-dashboard=artifacts/phase0/sprint-b3-operator-dashboard/<run-id>/operator-notes.md
```

Expected result: both commands exit `0`; `releaseReady=true`; index verification passes with file checks; every extra ref is workspace-relative, present, redacted, and named for the slice that produced it.

## Local-Only Boundary

B4 may read and write release evidence under `artifacts/phase0`, read the drill manifest and alert pack, and verify indexed evidence files. It must not install packages, enable services, query Docker, query Traefik, probe DNS or public routes, read secrets, or capture dashboard screenshots.

## Return Paths

- Return to B1 when live deploy notes or checklist artifacts are missing, route proof is incomplete, or immutable image/operation evidence is absent.
- Return to B2 when install notes are missing, package version or commit is absent, or doctor/systemd/first-boot evidence is incomplete.
- Return to B3 when dashboard notes or redacted captures are missing, ambiguous, or not workspace-relative.
- Return to the gate owner when local release evidence, release summary, or index verification fails.

## Current Validation

Documentation-only validation for this handoff should include:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

The full B4 command is intentionally deferred until real B1/B2/B3 operator note paths exist.
