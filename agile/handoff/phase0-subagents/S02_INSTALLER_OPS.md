# Phase 0 Worker S02: Installer/Ops Handoff

- **Worker:** S02 Installer/Ops
- **Primary source:** `agile/sprints/SPRINT_02_INSTALLER.md`
- **Supporting sources:** `agile/features/FEATURE_DAEMON_JSON_BASELINE.md`, `agile/engineering/adr/ADR-005-cgroup-v2-hard-required.md`, `agile/engineering/adr/ADR-010-rootful-docker-alpha-rootless-v1.md`
- **Status:** Phase 0 planning handoff complete. No implementation code exists in this workspace yet.

## Scope

S02 owns the Linux install and operations substrate for Phase 0 Alpha:

- Signed Debian package skeleton for Ubuntu 24.04 LTS and Debian 12, including package metadata, maintainer scripts, packaged runbooks, logrotate, AppArmor stub, journald drop-in, and default path creation.
- APT repository layout and bootstrap flow using the modern `signed-by=/usr/share/keyrings/midnight-archive-keyring.gpg` pattern. `apt-key` is not allowed.
- systemd unit contract for `midnight-server`, `midnight-worker`, `midnight-agent`, `midnight-buildkit`, and `midnight-registry`.
- Installer preflight hard gates for cgroup v2, Docker Engine API version, Docker `daemon.json` baseline, time sync, ports `:80`/`:443`, firewall backend, UFW integration, and external reachability.
- `midnight doctor` framework with stable check IDs, JSON output, exit-code contract, safe fixes, and the first install/runtime checks.
- `midnight uninstall` artifact-by-artifact behavior that preserves data by default.
- `midnight install --profile=local` with `*.midnight.local` self-signed TLS and no external DNS/ACME dependency.
- First-start bootstrap artifacts owned by install/runtime packaging: `/var/lib/midnight/bootstrap-token`, `/etc/midnight/secrets.key`, and Postgres role/bootstrap baselines where required by Sprint 2.

S02 does not own the control-plane API implementation, deployment state machine, hosted/public multi-tenant mode, rootless BuildKit, dashboard UX, or app deployment flows beyond the local-mode install hooks required to support later workers.

## Owned Future Implementation Paths

| Path | Ownership |
|---|---|
| `deploy/debian/` | `nfpm` package config, Debian control metadata, maintainer scripts, APT repo config/templates, archive keyring package, logrotate drop-in, journald drop-in, AppArmor profile stub, packaged runbooks, reproducible build scripts, docs under `docs/install/*.md`. |
| `deploy/systemd/` | Five platform units, default environment files, override directory creation, `systemd-analyze verify` fixtures, BuildKit cgroup caps, filesystem confinement, OOM score settings, dependency DAG. |
| `installer/preflight/` | Host probe library and CLI surface for install/upgrade gates: cgroup v2, Docker API/version, daemon baseline merge, address-pool overlap, time sync, ports, UFW/nft/iptables backend, reachability prober, local-profile bypass rules, JSON schema. |
| `installer/doctor/` | Stable check registry, runner, JSON output schema, exit-code mapping, `--check=<id>`, `--fix=safe` / `--fix=all`, remediation markdown links, check-ID compatibility CI, support-bundle friendly output. |
| `installer/uninstall/` | Dry-run/apply planner, unit stop/remove plan, data-preservation rules, optional repo purge, Docker network cleanup, iptables chain flush, daemon.json marker behavior. |

If the repo layout from S01 lands as Go packages rather than these exact top-level paths, keep the path names as ownership labels and map them to the canonical Go packages without changing the boundary.

## Dependencies From S01

- `MIDNIGHT-S01-001` must create the canonical repo layout and reserved binaries: `midnight-server`, `midnight-worker`, `midnight-agent`, `midnight`, `midnight-buildkit`, `midnight-registry`. S02 packaging and systemd units should not invent alternate names.
- `MIDNIGHT-S01-007` must provision GPG signing material for `.deb`, `Release.gpg`, and `InRelease`. S02 can build staging packages with test keys, but production APT acceptance depends on this key pipeline.
- S01 supply-chain gates control release artifact provenance. S02 package and repo publishing must run only from protected branches once CI exists.

## Dependencies With S03

- S02 produces `/var/lib/midnight/bootstrap-token` via `MIDNIGHT-S02-010`; S03 consumes it in `MIDNIGHT-S03-013` for `POST /api/v1alpha/auth/bootstrap`, mandatory password+TOTP enrollment, and bootstrap auto-disable.
- S02 produces Postgres roles and `pg_hba.conf` baseline via `MIDNIGHT-S02-012`; S03 consumes the roles in `MIDNIGHT-S03-001` when applying grants for Phase 0 core tables.
- S02 local-mode can install host/runtime prerequisites, but the full "fresh VM to deployed app" demo still needs the S03+ control-plane and deploy stub surfaces.
- S03 API/admin surfaces may later expose the install manifest and host baseline; S02 must write `/var/lib/midnight/install-manifest.json` with stable fields for that future consumer.

## Acceptance Checklist

- [ ] `make deb` produces reproducible `amd64` and `arm64` packages under `dist/`.
- [ ] Package contents include binaries/stubs, systemd units, `/usr/share/midnight/runbooks/*.md`, `/etc/logrotate.d/midnight`, AppArmor stub, journald drop-in, and default config templates.
- [ ] Fresh install creates `/var/lib/midnight/{db,secrets,registry/data,kms,acme,logs}` with locked ownership/modes where specified.
- [ ] APT staging repo serves `noble` and `bookworm` with signed `Release`, `Release.gpg`, `InRelease`, `Packages`, and `Packages.gz`.
- [ ] Bootstrap instructions use `signed-by` keyring only; `apt-key` is absent.
- [ ] All five systemd units verify and include the locked restart, start-limit, OOM, and dependency contract.
- [ ] `midnight-buildkit` runs as a dedicated unprivileged user with CPU/memory caps, `PrivateTmp=true`, `ProtectSystem=strict`, dedicated tmpfs, and no read access to platform secrets/data paths.
- [ ] `midnight preflight --json` refuses cgroup v1 with the exact ADR-005 remediation string.
- [ ] Preflight structurally merges `/etc/docker/daemon.json`, refuses conflicts, preserves non-conflicting operator keys, validates live Docker state after reload, and records the install manifest.
- [ ] Preflight enforces time sync, Docker Engine API >= v1.43, port bindability/reachability, firewall backend compatibility, and idempotent UFW `DOCKER-USER` integration.
- [ ] `midnight doctor` ships stable IDs, JSON schema, exit-code table, per-check execution, safe fixes, and compatibility tests preventing ID renames.
- [ ] Doctor includes at least Docker availability, cgroup v2, daemon baseline, time sync, ports 80/443, UFW state, agent registration, KEK file mode, and rootful BuildKit warning checks.
- [ ] `midnight uninstall --dry-run` and `--apply` preserve data, optionally purge repo/keyring, remove units, handle Docker networks safely, flush Midnight chains, and leave daemon.json edits marked for manual cleanup.
- [ ] `midnight install --profile=local` skips ACME/reachability where appropriate, sets up `*.midnight.local`, emits/imports a self-signed root, and supports the under-15-minute local demo target.
- [ ] Bootstrap token is `0600 root:root`, one-shot, token-format checked, audited on read, and forward-compatible with S03.
- [ ] KEK is `0400 midnight:midnight`, 32 random bytes, checked by doctor, and documented as irrecoverable if lost.
- [ ] Postgres role separation and `pg_hba.conf` baseline are available for S03 migration grants, or explicitly deferred with S03 approval.

## Risks

- The sprint is over capacity: S02 is 52 SP against an estimated 48 SP. If trimming is required, `MIDNIGHT-S02-012` is already identified as deferrable to early Sprint 3, but S03 must agree because it consumes the roles.
- `daemon.json` merge behavior is high-risk because silent overwrites are forbidden. Property tests and refusal-by-default conflict messages should be implemented before any maintainer-script write path.
- External reachability prober may not exist when installer work starts. Provide a local/staging stub early and keep public ACME readiness gated until signed responses are available.
- Reproducible `.deb` builds can drift with `nfpm` version changes. Pin the tool and add a two-build digest comparison in CI.
- UFW and iptables backend behavior differs across Ubuntu 24.04 and Debian 12. Build a VM matrix early instead of relying on local host probes.
- KEK loss messaging is a release risk. The installer, runbook, and doctor output must state irrecoverability plainly.
- Rootful Docker and co-located BuildKit are acceptable only for trusted self-hosted Phase 0. Any UI/API/profile work that suggests public multi-tenant safety is a release blocker.

## Phase 0 Boundary And Mismatches

- Phase 0 Alpha allows `local` and trusted self-hosted profiles only. `semi-trusted` and `public-multi-tenant` profiles must be blocked; there is no `--i-know-what-im-doing` bypass.
- Rootless BuildKit is a v1-GA blocker, not S02/Phase 0 scope. S02 must implement rootful/co-located warning and containment, not rootless support.
- Check ID naming is inconsistent across sources and must be resolved before code lands:
  - Sprint 2 uses `OR-CHK-PRE-001 CGROUPV2_REQUIRED`.
  - `FEATURE_DAEMON_JSON_BASELINE.md` mentions `OR-CHK-CGROUPV2`, `OR-CHK-CGROUPV2` failure modes, and `OR-CHK-DOCKER-DAEMON-JSON`.
  - ADR-005 uses `OR-CHK-CGROUP-V2`.
  - Sprint 2 uses `OR-CHK-DOC-008`; ADR-010 uses `OR-CHK-BUILD-ROOTFUL`.
- BuildKit unit user naming is inconsistent: Sprint 2 says `midnight-buildkit`, ADR-010 compliance text says `midnight-build`. Pick one before writing units; recommendation is `midnight-buildkit` to match the service name.
- `FEATURE_DAEMON_JSON_BASELINE.md` includes `init: true` in the required JSON but also has an open question proposing per-container `HostConfig.Init` instead. Treat this as unresolved before locking merge tests.
- The feature doc says `DAEMON_JSON_CONFLICT` is a new/open registry addition. Coordinate with the S03 error-code registry owner before treating it as stable.

## Exact First Implementation Tasks

1. Create the install/ops package skeleton: `deploy/debian/`, `deploy/systemd/`, `installer/preflight/`, `installer/doctor/`, and `installer/uninstall/`, mapped to the S01 Go module layout once it lands.
2. Add a canonical check-ID registry document for preflight and doctor, resolving the cgroup/daemon/rootful ID mismatches above before implementation.
3. Add `nfpm` config and a `make deb` target that packages placeholder binaries, units, runbooks, logrotate, journald, AppArmor stub, and maintainer scripts for `amd64` and `arm64`.
4. Write the five systemd unit files and run `systemd-analyze verify` fixtures. Lock the restart/start-limit/OOM/dependency contract before package scripts depend on them.
5. Implement `daemon.json` baseline merge as a pure library first, with tests for empty, partial, conflicting, operator-extended, bad JSON, and address-pool-overlap cases.
6. Implement cgroup v2 and Docker Engine API preflight checks with fake-host fixtures. The cgroup v1 failure must include `add 'systemd.unified_cgroup_hierarchy=1' to the kernel command line and reboot`.
7. Implement preflight JSON output schema and exit behavior, then wire time sync, ports, firewall backend, UFW sentinel block, and reachability prober stubs.
8. Implement doctor registry/runner/JSON schema/exit codes and the initial checks. Add a CI guard that fails on check ID rename/removal.
9. Implement maintainer-script path creation for `/var/lib/midnight/*`, `/etc/midnight`, bootstrap token, KEK, install manifest, and override directories with exact modes.
10. Implement uninstall as a dry-run planner first; only then wire `--apply`, repo purge, unit removal, network cleanup, iptables flush, and daemon.json marker behavior.
11. Implement `--profile=local` as an installer profile that skips ACME/external reachability, configures `*.midnight.local`, and emits the self-signed root trust instructions.
12. Add VM integration targets for Ubuntu 24.04 and Debian 12 covering clean install, cgroup v1 refusal, daemon conflict refusal, UFW idempotency, local-mode install, uninstall/reinstall, and doctor JSON snapshots.
