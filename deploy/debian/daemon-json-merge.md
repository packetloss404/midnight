# Docker daemon.json Merge Skeleton

The installer must preserve existing `/etc/docker/daemon.json` keys and overlay
only Midnight-owned defaults from `docker-daemon-baseline.json` when a key is
absent. Existing administrator values win only for keys Midnight does not own;
conflicting values for baseline-owned keys fail closed.

Phase 0 merge contract:

- Parse the existing file as JSON object, or start with `{}` if it is absent.
- Parse `deploy/debian/docker-daemon-baseline.json` as the defaults object.
- Recursively add missing keys from defaults without replacing existing values.
- Refuse the install when an existing value for a baseline-owned key differs
  from the baseline.
- Write atomically to `/etc/docker/daemon.json` with mode `0644`.
- If the final content differs, report that Docker requires a restart.

The installer must not remove unknown keys and must fail closed on invalid JSON
or baseline conflicts. The operator can inspect the conflict through
`midnight-doctor --check OR-CHK-DOC-002 --daemon-json-fixture /etc/docker/daemon.json --json`.
