# Contributing

Phase 0 work is coordinated through `agile/handoff/phase0-subagents/README.md`.

Contributor references:

- Local setup: `docs/contributing/dev-setup.md`
- Local verification and manual CI: `docs/contributing/ci.md`
- Style and lint expectations: `docs/contributing/style.md`
- npm dependency policy: `docs/contributing/npm.md`
- Code ownership: `docs/contributing/codeowners.md`
- Public launch checklist: `docs/contributing/public-launch.md`

Before opening a PR:

- Confirm the story meets `agile/handoff/DEFINITION_OF_READY_AND_DONE.md`.
- Cite the relevant panel decision or explicitly state that none applies.
- Run `make lint`, `make test`, and `make supplychain` where tooling is available.
- Update docs, OpenAPI, runbooks, compatibility matrices, and error-code registry when behavior changes.
- Do not introduce a new top-level directory or cross-package dependency without an ADR.

Security-sensitive changes require security-lead review.
