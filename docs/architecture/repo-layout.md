# Repository Layout

This is the maintained repository map. The original
[`agile/engineering/skeletons/REPO_LAYOUT.md`](../../agile/engineering/skeletons/REPO_LAYOUT.md)
is planning history; its proposed paths are not a current directory inventory.

Top-level directories are intentionally stable because builds and tooling depend on them:

| Path | Owner |
|---|---|
| `cmd/` | Producing binaries. |
| `internal/` | Private platform implementation. |
| `pkg/` | Public Go API surface. |
| `api/` | API artifacts. |
| `proto/` | Agent protocol artifacts. |
| `schemas/` | JSON Schema artifacts. |
| `migrations/` | Database migrations. |
| `deploy/` | Packaging and host integration artifacts. |
| `test/` | Integration, contract, and drill tests. |
| `tools/` | Build-only tools. |
| `web-next/` | Active React/Vite console, embedded into the server at build time. |
| `docs/` | Maintained documentation; dated reports live in `docs/archive/`. |
| `agile/` | Archived planning bundle, with stable paths referenced by tooling and historical contracts. |
| `examples/` | Build and deployment proof applications. |
| `scripts/` | Development, release, and acceptance runners. |

The legacy `web/` console was removed in commit `48a25bb`. The public website
and reader-facing documentation are maintained separately in
`packetloss404/midnight-marketing`.

Adding a top-level directory or moving a package across `internal/` and `pkg/` requires an ADR.
