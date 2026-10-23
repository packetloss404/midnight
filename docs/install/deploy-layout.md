# `deploy/` Package Layout

The `deploy/` tree under the repository root collects every shipped operating-system asset that the Midnight installer relies on. Files are intentionally grouped by integration surface so packagers, operators, and reviewers can audit each contract in isolation.

## Layout

```
deploy/
  apparmor/      AppArmor profile stub for `midnight-default`.
  debian/        nfpm/.deb metadata, baseline daemon JSON, environment file.
  journald/      journald drop-in (rate limit + storage caps).
  logrotate/     logrotate rule shipped at /etc/logrotate.d/midnight.
  prometheus/    Prometheus scrape stub for the platform exporters.
  systemd/       Six panel-locked systemd units.
  ufw/           UFW after.rules sentinel block.
```

## File-by-file purpose

### `deploy/systemd/`

Six units honor the package unit contract (`Restart=on-failure`, `RestartSec=5`, `StartLimitBurst=5`, `StartLimitIntervalSec=300`, `OOMScoreAdjust=-900` for server/agent, `After=docker.service postgresql.service network-online.target`).

| File | Purpose |
| --- | --- |
| `midnight-server.service` | Control-plane API server. |
| `midnight-worker.service` | Operation worker. |
| `midnight-agent.service` | Per-host agent that talks to the local Docker engine. |
| `midnight-buildkit.service` | Co-located BuildKit. Runs as a dedicated unprivileged user with cgroup CPU/memory caps and `PrivateTmp=true`, `ProtectSystem=strict`. |
| `midnight-registry.service` | Local OCI registry on a loopback bind. |
| `README.md` | Describes drop-in directories under `/etc/systemd/system/midnight-*.service.d/` for operator override. |

### `deploy/debian/`

Holds the `nfpm` packaging metadata, maintainer scripts, the baseline `daemon.json`, and the default environment file. The shipped daemon baseline matches `deploy/debian/docker-daemon-baseline.json`: BuildKit enabled, Docker `json-file` logging with size/file caps, `live-restore`, `no-new-privileges`, and the `overlay2` storage driver. The installer adds missing baseline keys, preserves unknown operator keys, and refuses conflicting values for baseline-owned keys.

| File | Purpose |
| --- | --- |
| `docker-daemon-baseline.json` | The merged Docker baseline emitted by the installer. |
| `daemon-json-merge.md` | Documents the merge rules used by `internal/preflight/daemon_json.go`. |
| `midnight.env` | EnvironmentFile defaults consumed by the systemd units. |
| `registry.yml` | Local OCI registry stub configuration. |
| `README.md`, `README.Debian` | Operator-facing notes shipped with the `.deb`. |

### `deploy/journald/`

`midnight.conf` ships a drop-in with `RateLimitBurst`, `SystemMaxUse`, and `SystemKeepFree` defaults. Tenant logs deliberately route through Docker `json-file` (not journald) per the panel decision; this file caps the platform-side journal usage only.

### `deploy/logrotate/`

`midnight` ships a logrotate drop-in with the panel-locked defaults: `100M`, `14 days`, `7 rotations`, `copytruncate`. The companion README documents how to override per-file caps without breaking installer reconcile.

### `deploy/ufw/`

`midnight` provides an `/etc/ufw/after.rules` integration block bracketed with sentinel comments. The installer appends `midnight-*` rules after the sentinel idempotently so UFW rules apply to tenant traffic without trampling operator additions.

### `deploy/apparmor/`

`midnight-default` is a stub AppArmor profile consumed by the systemd units. The package ships the profile in the `.deb` and loads it at unit start; it does not modify `daemon.json`.

### `deploy/prometheus/`

A minimal scrape config consumed by the support bundle wiring; not enabled by default.

## Cross-references

- The single-server operator install path lives in [`operator-install-path.md`](operator-install-path.md).
- The installer preflight that reads/merges these assets lives under `internal/preflight/`.
- The doctor checks that re-validate them live under `internal/doctor/`; the CLI still requires explicit fixtures or hints for several host facts.
- The dry-run uninstall planner that knows what to keep (data, KEK, daemon.json marker) lives under `internal/uninstall/`.
- The packaged runbooks for the operator-facing remediation flows live under `docs/runbooks/`.
