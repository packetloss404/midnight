# Style Guide

## Go

- Go code is formatted with `gofmt` and should remain dependency-light until
  the owning worker lane lands implementation.
- Package comments are required for new packages. The canonical form is a
  single-sentence comment that begins with `Package <name>`. The lint guard
  `LINT_SOURCE_GUARD_FAILED` enforces this on every non-main, non-test file
  under `internal/`, `pkg/`, and `tools/`. The exempt allowlist is documented
  in `tools/lint/source_guards.go`.
- Public API types belong in `pkg/orailtypes` or generated clients, not in
  arbitrary internal packages.
- Do not introduce package cycles.
- Use `internal/clock` for production time access outside tests.
- `panic(` is forbidden outside `cmd/<binary>/main.go` entry points. Tests
  may panic intentionally to validate recovery semantics.
- `os.Exit` is forbidden outside `cmd/<binary>/main.go` and the `tools/`
  subtree. Long-lived daemons return errors instead.

## Lint Integration

`make lint` and `task lint` run the same chain in this order:

1. `test -z "$(gofmt -l cmd internal pkg tools)"`
2. `go vet ./...`
3. `go run ./tools/lint`

Run `make format` or `task format` when formatting changes are needed.

`go run ./tools/lint` runs every guard described above plus the scaffold,
workflow, manual workflow shape, error-code registry, ADR index drift,
Phase 0 gate documentation accuracy, and console product-copy checks. On failure it prints one diagnostic per
violation and a final `lint reason: LINT_*` line whose code is documented in
[`docs/error-codes.md`](../error-codes.md). Release automation routes by that
reason code, so do not strip the prefix when forwarding lint output to chat
or alert channels.

### Stable Exit Codes

`tools/lint` returns one of four stable exit codes. Local automation and
pre-commit route on these directly; do not renumber them.

| Exit | Constant | Meaning |
|---|---|---|
| `0` | `lint.ExitOK` | Every guard passed, or `--adr-index-write` ran and the index was already current. |
| `2` | `lint.ExitInvocation` | The lint binary itself could not parse arguments or load on-disk inputs (corresponds to `LINT_INVOCATION_FAILED`). |
| `3` | `lint.ExitGuardFailed` | One or more guards rejected the tree. The final stderr line carries the per-category `LINT_*` reason code. |
| `4` | `lint.ExitDriftWritten` | `--adr-index-write` regenerated `docs/adr/README.md` from per-ADR front-matter. Local and manual workflow checks use this to detect drift without ever modifying the working tree. |

### Failure-Code Literals

Every `SCREAMING_SNAKE_CASE` string literal of length `>=3` containing an
underscore in any source file under `cmd/`, `internal/`, or `tools/` must
either be registered in [`docs/error-codes.md`](../error-codes.md), listed in
[`tools/lint/non_failure_code_literals.txt`](../../tools/lint/non_failure_code_literals.txt),
or annotated on the same line with `// not-a-failure-code`. The deep registry
guard (`LINT_REGISTRY_GUARD_FAILED`) catches the "added a code, forgot the
registry row" failure mode before it reaches an operator alert. Prefer the
inline `// not-a-failure-code` annotation when the literal is owned by a
single source line; use the allowlist file only for tokens defined by an
external standard or shared across files.

### Product Copy

Strings a person reads in the Midnight console must be written in the reader's
language, not the control plane's. The guard `LINT_PRODUCT_COPY_GUARD_FAILED`
scans JSX text and string literals under `web-next/src/` and rejects these
words in user-visible positions:

| Avoid in the console | Write instead |
|---|---|
| `canonical deployment` | current release, or drop the qualifier |
| `durable changeset` | saved draft, or changes |
| `admitted` | connected, allowed, or available |
| `operation` | activity, rollout, build, deploy, or restore |
| `persisted replica evidence` | last reported health |
| `apply changeset` | apply configuration |
| `intake` | accepted, or say what actually happened |

The full rationale is in
[`docs/railway-ux-9-of-10-target-2026-09-01.md`](../railway-ux-9-of-10-target-2026-09-01.md).

Two rules bound the rewrite. Never invent a capability that does not exist,
and never soften a real failure into vagueness: an error must still say what
broke and what the reader can do. When a control is unavailable, say what the
reader can do about it rather than naming the internal boundary that forbids
it.

The guard reads a literal when it sits in a rendered position (`label`,
`title`, `detail`, `description`, `hint`, `placeholder`, `message`, `reason`,
`aria-label`, and similar) or when it reads as prose. It ignores identifiers,
header names, URLs, query keys, comments, `console.*` arguments, and error
`cause` values — so a user-facing message can be reworded while the exact
developer detail survives in a `console.error` or an error `cause`. Files under
`web-next/src/pages/docs/` are excluded by path: the in-app documentation
teaches the control-plane model, so those terms are its subject.

A term that genuinely belongs on screen — an API operation id under
`Technical details`, for example — is recorded in
[`tools/lint/product_copy_allowlist.txt`](../../tools/lint/product_copy_allowlist.txt)
as `<path> | <exact snippet>`. Entries are exact, and the guard fails on an
entry that no longer matches anything, so a reworded string retires its own
exemption instead of leaving a permanent hole.

### ADR Index Regeneration

Run `go run ./tools/lint --adr-index-write` to regenerate
`docs/adr/README.md` from the per-ADR front-matter. The tool exits with
`lint.ExitDriftWritten = 4` when the file was rewritten and `lint.ExitOK = 0`
when the index already matched, so local and manual workflow checks can detect
drift without ever leaving a dirty tree behind.

## Pre-commit

`pre-commit install` wires the same chain plus repository-pinned hooks for
trailing whitespace, end-of-file fixers, and YAML well-formedness. Local
bypass with `git commit --no-verify` is permitted; reviewers should ask for
the relevant local command output when the hook is bypassed.

## ADRs

A change that introduces or modifies architecture must include a new ADR.
Copy `docs/adr/0000-template.md`, increment the four-digit number, fill in
the front-matter, and update `docs/adr/README.md`. The lint check
`LINT_ADR_INDEX_DRIFT` re-derives the index from per-ADR front-matter and
fails on drift, so the index update is mechanical.
