# Phase 0 Gate

`tools/phase0-gate` is the S08-owned release gate skeleton. It currently validates that the Phase 0 locked alert set is present and well formed.

Run with:

```sh
go run ./tools/phase0-gate
```

Release evidence can be written alongside any report format:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence artifacts/phase0/release-evidence.json
```

Canonical Phase 0 release gate command:

```sh
mkdir -p artifacts/phase0
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence artifacts/phase0/release-evidence.json --release-evidence-index artifacts/phase0/release-evidence-index.json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files --release-summary artifacts/phase0/release-summary.json
```

B4 canonical proof-packet flow after the Sprint B1/B2/B3 operator notes exist starts with the local gate command above. That seeds `release-evidence.json`, `release-evidence-index.json`, and `release-summary.json`. Then rerun with the cross-slice evidence refs:

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

B4 is a packaging and verification slice, not a live execution slice. The second command refreshes the local gate evidence, writes the packet index, and verifies that every indexed file is workspace-relative and present. The B1, B2, and B3 refs must point to redacted operator-owned artifacts from their respective runbooks; do not create placeholder notes to make `--verify-evidence-files` pass.

`--release-evidence` requires an explicit `.json` path whose parent directory already exists. The file is deterministic: it uses a timestamp placeholder with a recorded source, and includes the gate report, mode flags, evidence packet, and required drill execution summary when `--require-executed-drills` is enabled. In required-drill mode the top-level `releaseReady` value is true only when alert validation and all required executed drills pass.

The gate report also includes `liveDeployProof`, the Phase 0 Beta Sprint B1 offline contract for live deploy proof. This contract is validated without installing, deploying, probing DNS, or touching the network. It records the exact artifact names, workspace-relative paths, required fields, and checks a real live deploy run must archive under `artifacts/phase0/sprint-b1-live-deploy/<run-id>/`.

The gate report also includes `operatorInstallPath`, the Phase 0 Beta Sprint B2 offline contract for the operator install path. This contract is validated without installing packages, starting systemd units, probing Traefik, or reading host files. It records the exact artifact names, workspace-relative paths, required fields, and checks a real operator install must archive under `artifacts/phase0/sprint-b2-operator-install/<run-id>/`, including `/etc/midnight/midnight.env`, the five Midnight systemd units, the Traefik dynamic publication root, post-install and post-enable doctor JSON, and the B1 live deploy proof handoff.

Release evidence and release summaries include `canonicalProofPacket`, the Phase 0 Beta Sprint B4 local contract. It records a deterministic command string, runner OS, commit SHA hint from `--last-green`, operator notes ref from `--evidence-ref operator-notes=...`, release evidence/index/summary refs, and the release evidence index verification status. Missing optional refs are encoded as `not-provided`; the gate does not create operator notes or probe remote systems.

Sprint B5 introduced the first host-scoped Phase 3 drill metadata slice. The gate reports host drill metadata as `phase3HostDrillMetadata`, sourced from `test/drills/manifest.yaml`. The slice reuses existing manifest lists under `metadata` instead of adding a new manifest schema.

The first B5 metadata carrier is the existing `D-08` conntrack pressure drill row. It stays a Phase 0 placeholder drill, but its `metadata` block must include `tags`, `notes`, and `subcases` entries that identify the Phase 3 drill, host scope, readiness domain, planner-only execution, and portable evidence ref. Valid Phase 3 readiness domains are `networking`, `runtime_adapter`, `registry_cache`, and `volume_attach`.

B5 is still local-only. The gate validates metadata shape and portable refs; it does not query Docker, query Traefik, create WireGuard interfaces, flush conntrack, probe routes, or prove live host execution. Future Phase 3 rows such as scheduler reschedule, WireGuard peer churn, key rotation overlap, PMTU regression, and IPAM quarantine can use the same manifest fields once the manifest/gate owner expands the accepted drill ID range or maps them onto existing rows intentionally.

Sprint B6 is the candidate-catalog follow-up to B5. It reconciles the merged B5 carrier on `D-08` with older planning notes that mentioned `D-19`, then catalogs future host-drill candidates without claiming execution. `D-19` remains the QA gate release-readiness placeholder unless the manifest/gate owner deliberately remaps it in a later slice. The B6 gate surface uses `phase0.beta.b6.phase3-host-drill-candidate-catalog` and adds `b6-phase3-host-drill-candidate-catalog-contract` to the evidence packet.

B6 keeps the same four Phase 3 readiness domains:

| Domain | Candidate catalog use |
| --- | --- |
| `networking` | Route promotion, WireGuard peer churn, key rotation overlap, PMTU regression, IPAM quarantine, and conntrack flush correctness. |
| `runtime_adapter` | Scheduler reschedule, adapter conformance, host heartbeat, and placement planning. |
| `registry_cache` | Registry/cache planner status, host-local cache recovery, digest pinning, and cache promotion. |
| `volume_attach` | Volume attach/detach planning, source/destination host transfer, and volume recovery state. |

B6 is local-only and must not execute live host drills. It must not query Docker or Traefik, create WireGuard interfaces, flush conntrack, probe routes, mount volumes, query registries, install services, or create placeholder host artifacts. Use the local gate command below to validate that the current `D-08` B5 metadata contract is still present:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

B6 return paths are: back to B4 for proof-packet portability issues; back to B5/manifest-gate ownership for missing `D-08` `phase3.*` metadata or future ID-range work; back to Phase 3 networking owners for executable networking drills; back to Phase 3 runtime/scheduler owners for host reschedule and adapter evidence; and back to Phase 3 registry/storage owners for registry cache and volume attach/detach evidence.

Sprint B7 is the local-only future drill ID reservation follow-up to B6. It reserves `D-20` through `D-23` as an index for later Phase 3 executable host drills, but those IDs stay outside `test/drills/manifest.yaml` and outside current harness assertions. B7 does not add live execution, does not expand the accepted executable manifest range, and does not require the gate to assert those IDs yet.

| Reserved ID | Future drill intent | Readiness domain | Return owner |
| --- | --- | --- | --- |
| `D-20` | Route promotion / dataplane reachability. | `networking` | Phase 3 networking owners |
| `D-21` | Host reschedule / runtime adapter. | `runtime_adapter` | Phase 3 runtime/scheduler owners |
| `D-22` | Registry cache recovery / digest availability. | `registry_cache` | Phase 3 registry/storage owners |
| `D-23` | Volume attach transfer / recovery. | `volume_attach` | Phase 3 registry/storage owners |

B7 validation uses the current local gate command:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

This command validates the current manifest and gate contracts only. It is not a `D-20` through `D-23` execution check. Return B7 to B4 for future proof-packet portability issues, to B5/B6 manifest-gate ownership when reserved IDs need executable manifest rows or harness assertions, to Phase 3 networking owners for executable dataplane semantics, to Phase 3 runtime/scheduler owners for host reschedule semantics, and to Phase 3 registry/storage owners for registry cache and volume transfer semantics.

Sprint B8 is the local-only promotion-readiness follow-up to B7. It defines the criteria that must be satisfied before reserved IDs `D-20` through `D-23` can enter the executable manifest, but it does not add those IDs to `test/drills/manifest.yaml`, does not expand the accepted ID range, does not add harness assertions, and does not execute live host drills.

Before promotion, each reserved ID needs an owner-approved drill spec with host-scoped data, deterministic pass/fail assertions, runner-safe fixture or rig semantics, redacted workspace-relative evidence refs, and a clear rollback path when evidence is missing or unsafe:

| Reserved ID | Promotion-readiness requirement | Return owner |
| --- | --- | --- |
| `D-20` | Route promotion / dataplane reachability spec with source and destination host identity plus route state evidence. | Phase 3 networking owners |
| `D-21` | Host reschedule / runtime adapter spec with workload identity, source and destination host identity, and adapter capability evidence. | Phase 3 runtime/scheduler owners |
| `D-22` | Registry cache recovery / digest availability spec with host identity, cache role, digest set, and recovery assertion. | Phase 3 registry/storage owners |
| `D-23` | Volume attach transfer / recovery spec with workload identity, volume identity, source and destination host identity, and attach/detach assertion. | Phase 3 registry/storage owners |

B8 validation uses the current local gate command:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

This command validates the current manifest and gate contracts only. It is not a `D-20` through `D-23` execution check and it does not certify those IDs for manifest promotion. Return B8 to B4 for proof-packet portability issues, to B5/B6/B7 manifest-gate ownership for accepted ID-range or harness assertion work, to Phase 3 networking owners for executable dataplane semantics, to Phase 3 runtime/scheduler owners for host reschedule semantics, and to Phase 3 registry/storage owners for registry cache and volume transfer semantics.

Sprint B9 is the local-only release evidence archive readiness follow-up to B8. When `test/drills/phase0-release-evidence-archive.yaml` is present, the gate validates the B9 archive contract and adds `b9-phase0-release-evidence-archive-contract` to the evidence packet. It keeps the B4 proof-packet/index contract, writes phase0-prefixed archive files only when the operator asks for them, and leaves generated outputs under ignored `artifacts/phase0` paths. B9 does not run live host probes, does not require archive files to exist during contract validation, does not change `test/drills/manifest.yaml`, does not expand the accepted ID range, and does not promote `D-20` through `D-23`.

B9 canonical archive command:

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

Expected B9 files are `artifacts/phase0/phase0-release-evidence.json`, `artifacts/phase0/phase0-release-evidence-index.json`, and `artifacts/phase0/phase0-release-summary.json`. Expected archive refs in the generated index are `gate-release-evidence`, `drill-manifest`, and `prometheus-alerts`, all workspace-relative. The command passes only when `releaseReady=true` and index verification passes with `--verify-evidence-files`; the generated artifacts are local archive evidence and must not be committed.

B9 return paths are: back to B4 for proof-packet portability issues; back to the local gate owner for command, output, `releaseReady`, or index verification failures; back to B1/B2/B3 only when future operator refs are included and incomplete; back to B5/B6/B7/B8 manifest-gate ownership for any request that depends on reserved IDs, ID-range changes, executable rows, or harness assertions; and back to Phase 3 domain owners for live host evidence.

A concise deterministic release summary can be written with:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-summary artifacts/phase0/release-summary.json
```

`--release-summary` requires an explicit `.json` path whose parent directory already exists. The summary includes `releaseReady`, gate counts, executed drill counts, evidence index verification status, and evidence packet item statuses. It is intended for release gate automation that needs a stable compact JSON result instead of the full evidence packet.

An archive manifest for release evidence can be written with:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence artifacts/phase0/release-evidence.json --release-evidence-index artifacts/phase0/release-evidence-index.json --evidence-ref operator-notes=docs/release/operator-notes.json
```

`--release-evidence-index` requires an explicit `.json` path whose parent directory already exists. The deterministic index references the gate release evidence file when `--release-evidence` is set, plus the drill manifest and Prometheus alert rules. Every path recorded in the archive index, including `gate-release-evidence`, must be workspace-relative and must not escape the workspace; this keeps evidence portable across disposable VMs and future multi-host drill rigs. Extra refs can be added with repeated `--evidence-ref name=path`; names must be unique and path-like separators are not allowed in names.

An existing or just-written archive index can be verified with:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --release-evidence artifacts/phase0/release-evidence.json --release-evidence-index artifacts/phase0/release-evidence-index.json --verify-release-evidence-index artifacts/phase0/release-evidence-index.json
```

`--verify-release-evidence-index` reads the archive index JSON, validates that it contains the required `gate-release-evidence`, `drill-manifest`, and `prometheus-alerts` refs, and rejects unsafe ref paths. Archive entries may include an optional lowercase or uppercase hex `sha256` field. By default verification is metadata-only: it validates any `sha256` field shape but does not check referenced path existence or file hashes.

To require every referenced relative file to exist from the current working directory, add `--verify-evidence-files`:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills --verify-release-evidence-index artifacts/phase0/release-evidence-index.json --verify-evidence-files
```

When `--verify-evidence-files` is set, every referenced relative file must exist from the current working directory. Entries that include `sha256` are also read and compared against the file's SHA256 digest.

Verification status is included in the gate report and release evidence packet. When `--require-executed-drills` is set, failed index verification, including missing referenced files or SHA256 mismatches in `--verify-evidence-files` mode, fails the command.
