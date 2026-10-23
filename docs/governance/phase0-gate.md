# Phase 0 Gate Reference

`tools/phase0-gate` is the S08-owned release gate that PM-A and PM-B treat as
the canonical Phase 0 acceptance signal. It validates the locked Prometheus
alert set and the drill manifest, optionally executes drill adapters,
optionally writes release-evidence JSON, and (in `--require-executed-drills`
mode) returns a non-zero exit when any required drill or referenced evidence
file is missing.

This document is the operator reference for the public flag surface and exit
codes. It is kept aligned with `tools/phase0-gate/main.go` by the
`LINT_SCAFFOLD_GUARD_FAILED` lint check; if a flag is added or renamed in
`main.go`, this doc must be updated in the same PR.

## Invocation

```sh
go run ./tools/phase0-gate [flags]
```

Always run from the repository root. The tool reads `deploy/prometheus/alerts.yaml`
and `test/drills/manifest.yaml` from the working directory.

## Flags

| Flag | Default | Purpose |
|---|---|---|
| `--format=<text|markdown|json>` | `text` | Report format. JSON is the canonical machine format consumed by release evidence emission. |
| `--out=<path>` | empty | Write the rendered report to a file instead of stdout. The file is created with `0o644`. |
| `--last-green=<sha>` | `unknown` | Optional last-green release SHA included in the rendered report for human triage. |
| `--drill-adapter=<local|live>` | `local` | Select the drill execution adapter. `live` is reserved and rejected until the live harness lands. |
| `--execute-drills` | `false` | Run the in-process drill adapter against the manifest and include a non-blocking drill execution summary. Required for any release-evidence emission. |
| `--require-executed-drills` | `false` | Strict mode: requires `--execute-drills` and fails the command when any required drill is skipped, blocked, or failed, or when release-evidence-index verification does not return `pass`. This is the mode `make release-check` runs. |
| `--release-evidence=<path>` | empty | Write the deterministic release evidence JSON to `<path>`. Path must be an explicit `.json`, must not use relative traversal, and the parent directory must already exist. |
| `--release-summary=<path>` | empty | Write a concise deterministic release summary JSON. Same path policy as `--release-evidence`. |
| `--release-evidence-index=<path>` | empty | Write the deterministic release evidence archive index JSON. Same path policy as `--release-evidence`. The index pins the gate evidence file (when `--release-evidence` is set), the drill manifest, and the alert rules; extra refs may be added with `--evidence-ref`. |
| `--verify-release-evidence-index=<path>` | empty | Read and verify a release evidence archive index JSON file. Validates ref shape and SHA-256 fields if present. |
| `--verify-evidence-files` | `false` | Requires `--verify-release-evidence-index`. Promotes verification from metadata-only to file-existence and SHA-256 content checks. Required by `make release-check`. |
| `--evidence-ref=<name>=<relative/path>` | empty | Repeatable. Add an extra evidence reference to the release evidence index. `<name>` must be unique and contain no path separators; `<path>` must be a relative-safe path. |
| `--junit-out=<path>` | empty | Write a JUnit XML report. |
| `--print-coverage` | `false` | Print drill coverage and exit. |
| `--quiet` | `false` | Suppress success output; failures still print. |
| `--validate-references` | `false` | Validate drill manifest references against the error-code registry, runbooks, and event allowlist. |
| `--references=<mode>` | empty | Reference-catalog mode. `strict` requires every drill row's runbook field to resolve under `docs/runbooks/`. |
| `--diff-evidence-left=<path>` | empty | Left release-evidence JSON path for diff mode. |
| `--diff-evidence-right=<path>` | empty | Right release-evidence JSON path for diff mode. |
| `--diff-out=<path>` | empty | Write release-evidence diff JSON to a file; defaults to stdout in diff mode. |

## Exit Codes

| Exit | Meaning |
|---|---|
| `0` | Gate passed in the requested mode. In `--require-executed-drills` mode this also implies all required drills passed and the release-evidence index verification (if run) returned `pass`. |
| `2` | Input error: malformed or contradictory flags/arguments, missing repo files, unsupported format, or invalid path options. |
| `3` | Manifest invalid: drill manifest or alert rules failed deterministic validation before any drill ran. |
| `4` | Drill failure: at least one executed drill failed, was blocked/skipped under strict mode, or did not satisfy `--require-executed-drills`. |
| `5` | Release evidence failure: evidence emission, verification, or diffing failed. |

The companion lint tool `tools/lint` exits with the same `1`-on-failure
contract but additionally prints a `lint reason: LINT_*` line. The phase0
gate intentionally does not emit a parallel reason code because a release
gate failure requires reading the JSON evidence packet to triage.

## Common Failures

| Symptom | Likely cause |
|---|---|
| `--require-executed-drills requires --execute-drills` | The strict mode flag was set without execution. Add `--execute-drills`. |
| `phase0 gate failed: read deploy/prometheus/alerts.yaml` | Working directory is not the repo root. |
| `phase0 gate failed: --release-evidence path must end in .json` | Path contains a directory or non-`.json` suffix. Use the canonical `artifacts/phase0/release-evidence.json`. |
| `releaseReady: false` in JSON output | A required drill was skipped or failed, or the release evidence index verification reported anything other than `pass`. Inspect `drillExecution` and `releaseEvidenceIndexVerification` for the offending entry. |

## Canonical Release Gate Command

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

This is the exact command `make release-check` runs.
[`docs/governance/release-evidence.md`](release-evidence.md) documents the
output paths and retention.
