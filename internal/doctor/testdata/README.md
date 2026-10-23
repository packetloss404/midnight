# `internal/doctor/testdata/`

Fixture trees consumed by the disposable-VM CI harness via the
`midnight-doctor --hostsource=fixture:<dir>` flag added in the
Phase 1 Blocker 1 (VM CI harness) work. Each subdirectory is a
self-contained host-fact fixture matching the canonical filenames
documented in `cmd/midnight-doctor/main.go`:

| File | Doctor check it feeds |
|---|---|
| `mountinfo` | `OR-CHK-DOC-001` (cgroup v2 unified hierarchy detection) |
| `daemon.json` | `OR-CHK-DOC-002` (`/etc/docker/daemon.json` baseline merge) |
| `docker-version.txt` | `OR-CHK-DOC-003` (Docker Engine API ≥ v1.43) |
| `ss-ltn.txt` | `OR-CHK-DOC-004` (ingress ports 80/443 free) |
| `hints.json` | `OR-CHK-DOC-005..009` (firewall, MAC, time-sync, hostname, distro packages) |

`hints.json` shape mirrors `internal/preflight.HostHints` and is
parsed by `cmd/midnight-doctor`'s `resolveHostSource` helper. Adding
new fixtures follows the same naming convention; the harness CLI
flag does not need to change.

## `cgroup-v1/`

Negative-path fixture for 0.1-EC3. The `mountinfo` file describes
a host whose `/sys/fs/cgroup` is the v1 tmpfs root with named
controllers mounted underneath (no `cgroup2` filesystem). Used by
the disposable-VM CI workflow's negative-path job to assert
`OR-CHK-DOC-001` returns `status=fail` with the panel-locked
remediation `add 'systemd.unified_cgroup_hierarchy=1' to the kernel
command line and reboot`.
