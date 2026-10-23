# Phase 0 Gate `--watch` Mode (Design)

Status: post-Phase 1, **design only — no implementation yet**.

This document captures the planned design for a `--watch` mode on
`tools/phase0-gate`. The mode is intentionally not implemented during Phase 1;
the gate currently runs once-per-invocation and exits. The doc exists so the
shape of the future feature is locked before any code is added, and so other
lanes can reference the same spec while planning their CI integrations.

## Motivation

Operators authoring drill rows iterate on `test/drills/manifest.yaml` while
running the gate repeatedly. Each iteration today requires a fresh
`go run ./tools/phase0-gate ...` invocation, which adds startup latency and
clutters terminal output. A `--watch` mode shortens the inner loop: edit
manifest → save → see fresh validation result.

Watch mode is **never** the release-gate entry point. It is for local
authoring only. CI continues to invoke the gate exactly once per run, as
documented in `docs/governance/phase0-gate.md`.

## Trigger Inputs

The watcher MUST react only to changes that can plausibly affect the gate's
output:

| Path | Reason |
|---|---|
| `test/drills/manifest.yaml` | Source of truth for drill rows. |
| `deploy/prometheus/alerts.yaml` | Locked alert ruleset validated by every gate run. |
| `docs/error-codes.md` | Reason-code registry consulted by `--validate-references`. |
| `docs/runbooks/*.md` | Runbook catalog consulted by reference validation. |

The watcher MUST NOT react to:

- Edits inside `artifacts/phase0/**` — these are the gate's own outputs and
  would create a feedback loop.
- Edits inside `internal/**`, `cmd/**`, or any other build target — `go test`
  is the right surface for those.
- Symlink target swaps that do not change the resolved path content.

## Debounce

Filesystem editors (vim, VS Code, JetBrains IDEs) typically write a file as
"create temp → rename → fsync", producing 2-4 inotify events per save.
Without debouncing, the gate would run multiple times per save and emit
inconsistent partial reports.

The watcher MUST debounce events with a 250 ms trailing-edge window: events
within 250 ms of the last received event extend the window; the gate runs
only after the window settles. The window is fixed (not configurable) so
behavior is deterministic across machines.

## Re-run Semantics

On every settled debounce window:

1. Re-read `test/drills/manifest.yaml` and `deploy/prometheus/alerts.yaml`
   from disk.
2. Run the same code path that one-shot mode uses, with the same flags
   (`--format`, `--require-executed-drills`, etc.) inherited from the
   `--watch` invocation.
3. Emit the rendered report to stdout, prefixed by a short timestamp banner
   (`-- phase0-gate watch run @ <RFC3339> --`) so successive runs are easy to
   distinguish in terminal scrollback.
4. **Never** write release-evidence, release-summary, evidence-index, or
   junit-out artifacts in watch mode. Watch is read-only with respect to the
   filesystem outside stdout.

If the gate fails (any non-zero exit code in one-shot mode), watch mode
continues — it does NOT exit. Watch mode is a daemon-style loop; only
`SIGINT` / `SIGTERM` / `Ctrl-C` ends it.

## Exit Semantics

| Termination signal | Exit code |
|---|---|
| Clean `SIGINT` or `SIGTERM` | `0` (ExitOK). Watch mode is not a release gate; the operator's `Ctrl-C` is success. |
| Initial validation failed before watch loop began | The same exit code one-shot mode would have returned (e.g. `2`/`3`/`4`/`5`). |
| Filesystem watcher could not be installed (permissions, OS limits, missing inotify) | `2` (ExitInputError). |

These match the `ExitCode` constants documented in
`tools/phase0-gate/exitcodes.go`. No new exit codes are introduced for watch
mode.

## Flag Conflicts

`--watch` is mutually exclusive with:

- `--release-evidence`
- `--release-summary`
- `--release-evidence-index`
- `--verify-release-evidence-index`
- `--diff-evidence-left` / `--diff-evidence-right`
- `--junit-out`

If any of those are set together with `--watch`, the gate MUST exit with
`ExitInputError` and a deterministic message naming the conflict, before any
filesystem watcher is installed.

`--watch` IS compatible with:

- `--format=<text|json|markdown>`
- `--quiet`
- `--print-coverage`
- `--validate-references`
- `--execute-drills` / `--require-executed-drills`

## Implementation Notes (deferred)

When this feature lands post-Phase 1, the implementation should:

- Use the standard library plus a single small dependency (`fsnotify` is the
  current target). If `fsnotify` is not in `go.sum` at implementation time,
  fall back to a 1 Hz polling loop that stats each watched path and compares
  modification time + byte length. Polling is acceptable because the watch
  surface is small (single-digit files).
- Centralize the watched-path list in one private function so the test suite
  can assert it without duplication.
- Cover the debounce window with a deterministic test that drives the timer
  via an injected clock, not `time.Sleep`.

## Out of Scope

- Network-fed watch (HTTP long-poll, gRPC streaming). Watch mode is local-FS
  only.
- Cross-host watch via a shared filesystem. The gate is invoked from a single
  workstation at a time.
- Auto-fix of detected issues. The gate reports; it never edits manifests.
