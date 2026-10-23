# Resume Note

This file used to contain an April 2026 live-execution plan. That snapshot is
retired: it described a planner-only repository with no Postgres or runtime
wiring, named a long-deleted feature branch, and pointed at source line numbers
that no longer contain the described code.

Use the maintained sources instead:

- [`HANDOFF.md`](../HANDOFF.md) for the current developer and operator
  checkpoint.
- [`README.md`](../README.md) for the supported product surface and local
  commands.
- [`docs/mvp-acceptance.md`](../docs/mvp-acceptance.md) for executable readiness
  gates.
- [`docs/deployment-readiness.md`](../docs/deployment-readiness.md) for the
  dated installed and public-domain evidence ledger.
- [`BACKLOG.md`](../BACKLOG.md) for unfinished work. Together with
  `HANDOFF.md` it is the source of truth for what is still open; the dated
  parity and sprint documents under `docs/` and `agile/` are the historical
  records. Still confirm each item against current code before starting it.

Resolve the exact checkpoint from Git rather than copying branch names or
commit hashes into this note:

```sh
git status --short --branch
git log -1 --oneline --decorate
```
