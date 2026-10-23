# Lint Tools

Lint fixtures and custom checks live here.

`go run ./tools/lint` verifies Phase 0 scaffold files, npm lockfile policy,
GitHub workflow policy, stable workflow names, duplicate workflow job IDs,
pre-commit repository revision pinning, release governance blockers, release
evidence commands and generated JSON path policy, release-check workflow
automation and artifact upload retention, executed drill gate automation,
manual workflow shape, and the Phase 1 source/registry/ADR policy guards documented in
[`docs/contributing/style.md`](../../docs/contributing/style.md):

- `forbid panic()` outside `cmd/<binary>/main.go`.
- `forbid os.Exit(...)` outside `cmd/<binary>/main.go` and `tools/`.
- Require `// Package <name>` doc comment on every non-main package in
  `internal/`, `pkg/`, and `tools/` (with a documented allowlist for
  feature-lane-owned packages).
- Failure-code registry consistency: every `failurecodes.Code` constant must
  have a sorted row in `docs/error-codes.md`.
- ADR index drift: `docs/adr/README.md` must equal the index re-derived
  from per-ADR front-matter.

On failure the tool prints one diagnostic per violation followed by a
`lint reason: LINT_*` final line. Reason codes are documented in
[`docs/error-codes.md`](../../docs/error-codes.md):

- `LINT_SCAFFOLD_GUARD_FAILED`
- `LINT_SOURCE_GUARD_FAILED`
- `LINT_REGISTRY_GUARD_FAILED`
- `LINT_ADR_INDEX_DRIFT`
- `LINT_GATE_DOC_DRIFT`
- `LINT_INVOCATION_FAILED`

Stable exit codes: `0` (ok), `2` (invocation error), `3` (guard failure),
`4` (drift written by `--adr-index-write`). See
[`docs/contributing/style.md`](../../docs/contributing/style.md) for the full
table.

Pass `--adr-index-write` to regenerate `docs/adr/README.md` from per-ADR
front-matter. The deep failure-code-literal scanner reads
[`non_failure_code_literals.txt`](non_failure_code_literals.txt) for tokens
that look like failure codes but are not (env-var names, capability names,
TLS cipher suite identifiers, internal issue codes). Prefer per-line
`// not-a-failure-code` annotations when the literal lives on a single line.
