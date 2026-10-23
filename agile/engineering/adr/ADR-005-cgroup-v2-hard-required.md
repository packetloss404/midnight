# ADR-005: cgroup v2 is hard-required at install

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, linux, runtime, install, preflight]

## Context

Midnight's runtime contract — PID limits, memory hard limits, swap policy, CPU quota/shares, OOM behavior, freezer for graceful drain, IO weight — depends on Linux control groups. cgroup v1 has split, per-controller hierarchies that fight each other (the freezer controller and the systemd unified hierarchy are mutually awkward; memory accounting differs from v2 in subtle ways) and is deprecated in modern distros. The panel previously had this as "preferred"; on review they tightened it:

> "`cgroup v2` is hard-required (not 'preferred'). Preflight refuses install on v1 with explicit `systemd.unified_cgroup_hierarchy=1` remediation." (line 88)

> "Required `daemon.json` baseline, written or merged by the installer: `live-restore: true`, `userland-proxy: false`, `iptables: true`, `no-new-privileges: true`, `storage-driver: overlay2`, explicit `default-address-pools` with conflict checking, `log-driver: json-file` with size/file caps. Installer refuses to proceed on conflict." (line 89)

> "cgroup v2 is hard-required, not 'preferred.'" (line 2400, panel resolution)

Tier 1 OS targets are Ubuntu 24.04 LTS and Debian 12 (line 34); both default to cgroup v2. The hard floor is therefore consistent with the supported OS list. The remediation path for hosts running v1 is one kernel command-line flag plus a reboot — well within an operator's control.

cgroup v2 is also a precondition for several panel-locked behaviors:

- `HostConfig.Init = true` (tini) and explicit ulimits depend on v2-style accounting for accurate memory pressure (line 93).
- The agent's resource-pressure metrics — `_disk_pressure`, `_memory_pressure`, conntrack pressure (line 162) — assume the v2 controller layout.
- Restart-policy `CRASH_LOOP_BACKOFF` (line 928) reads OOM kill events from the v2 cgroup hierarchy.
- Multi-host scheduling reservations (line 926: "Scheduler must reserve CPU, memory, disk, inode, port, and log capacity before placement") use v2 quota semantics.

## Decision

The Midnight installer **refuses to proceed** on a host whose kernel does not have cgroup v2 enabled as the unified hierarchy. The preflight check produces an actionable remediation:

> add `systemd.unified_cgroup_hierarchy=1` to the kernel command line and reboot

(panel-locked remediation string, line 88; acceptance criterion 4 in `FEATURE_DAEMON_JSON_BASELINE.md`).

Detection is `stat -fc %T /sys/fs/cgroup` returning `cgroup2fs`, equivalently `findmnt /sys/fs/cgroup -no FSTYPE` returning `cgroup2`. Hybrid mode (v2 + v1 controllers attached) is treated as v1 for refusal purposes; the panel's intent is the unified hierarchy.

`midnight doctor` reports cgroup version live; `OR-CHK-CGROUP-V2` exits non-zero if the host slips off v2 between install and runtime.

There is no `--force-cgroup-v1` escape hatch. Operators on v1 must remediate.

## Alternatives Considered

- **Soft warning, allow v1.** Rejected: the panel's resolution at line 2400 reclassifies this from "preferred" to "hard-required" specifically because soft warnings get ignored, and v1 produces silent correctness gaps in OOM handling, freezer-based drain, and PID accounting.
- **Auto-remediate by writing `GRUB_CMDLINE_LINUX` and rebooting.** Rejected: rebooting an operator's VM during `apt install midnight-server` is unacceptable. We surface the remediation string and require operator action.
- **Detect v1 and fall back to v1-compatible defaults.** Rejected: every cgroup-v2-specific behavior in the architecture (memory pressure, freezer drain, OOM kill metric, PID limit accounting) would need a parallel v1 path. Doubling the test matrix for OS versions that are end-of-life in our supported window is wasted budget.
- **Require cgroup v2 *and* a specific kernel version.** Considered. Rejected at Phase 0: the supported OS list (Ubuntu 24.04 / Debian 12) already fixes a kernel floor; an additional explicit kernel-version check would be a drop-in for v1 if we ever expand to RHEL-family targets, but adds nothing for Tier 1.

## Consequences

Positive:

- One cgroup model in the codebase. Memory pressure, OOM events, freezer-based drain, PID limits, and IO weight all use v2 semantics.
- The restart-policy `CRASH_LOOP_BACKOFF` (line 928) and `OOM_KILLED` reason code (line 1461) read from a single source.
- Remediation is a single GRUB flag and a reboot — operator-friendly even on a fresh VPS install.

Negative:

- Hosts on older Ubuntu 20.04, RHEL 8, or custom kernels with v1 default fail preflight. We document this clearly in the install guide; Tier 1 OS list (Ubuntu 24.04 / Debian 12, line 34) defaults to v2 and avoids the issue entirely.
- WSL2 and some niche kernels hard-disable v2 unified hierarchy; Midnight does not support those.

Follow-up work:

- `midnight preflight` reports cgroup version, refuses on v1 with the exact panel-locked remediation string (`SPRINT_02_INSTALLER.md`).
- `midnight doctor` includes `OR-CHK-CGROUP-V2` for runtime drift detection (`SPRINT_07_OBSERVABILITY_SUPPORT.md`).
- Documentation page `/docs/install/preflight.md` mirrors the failure-message text.

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestPreflightRefusesCgroupV1`** in `pkg/install/preflight`: simulated v1 environment causes preflight to exit non-zero with the panel-locked remediation string.
- **Test name `TestPreflightAcceptsCgroupV2`** in `pkg/install/preflight`: v2 environment passes; the test fixture asserts `findmnt /sys/fs/cgroup -no FSTYPE` returns `cgroup2`.
- **Runbook check `OR-CHK-CGROUP-V2`** in `midnight doctor`: returns exit code 0 on v2; exit code 2 with `cgroup-v1-detected` reason on v1; surfaced in support bundle.
- **CI gate `gate.preflight-cgroup`:** the integration test environment in CI runs Ubuntu 24.04 (cgroup v2 default); a `make preflight-test` target asserts preflight passes.
- **Runbook artifact `runbooks/install-cgroup-v1.md`** ships in `/usr/share/midnight/runbooks/` with the remediation procedure (panel: line 199 mandates manual runbooks ship as markdown).
- **Failure drill `DRILL-PREFLIGHT-CGROUP-V1`** in `SPRINT_08_FAILURE_DRILLS.md`: simulate a v1 host; verify install refuses and produces the documented diagnostic.
- **Code review checklist item `CR-LINUX-01`:** any new resource-management code documents whether it relies on v2-only semantics.

## References

- spec: lines 88, 89, 93, 162, 928, 1461, 2400
- features: `FEATURE_DAEMON_JSON_BASELINE.md`
- sprints: `SPRINT_02_INSTALLER.md`, `SPRINT_07_OBSERVABILITY_SUPPORT.md`, `SPRINT_08_FAILURE_DRILLS.md`
- handoff: `OPERATIONS_RUNBOOK_INDEX.md`, `TEAM_ONBOARDING.md`
