# ADR-010: Rootful Docker for Alpha; rootless BuildKit is the v1-GA blocker

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, security, trust-boundary, runtime, rootful-acknowledgement]

## Context

Midnight's MVP runtime profile is rootful Docker Engine on trusted self-hosted Linux hosts. The panel was unambiguous about what this is and what it is not:

> "Rootful Docker is accepted only for trusted self-hosted code. It is not a malicious-tenant or public multi-tenant boundary." (line 64)

> "Rootful Docker Engine on trusted self-hosted Linux hosts. Rootful Docker is not a malicious-tenant or public multi-tenant isolation boundary. Docker daemon access is host-root equivalent. Only `midnight-agent` may access the Docker Engine API." (lines 892–894)

> "Users with deploy rights can execute arbitrary code in builds and containers. Hosted/public mode is blocked until separate builder/runtime trust domains, abuse controls, stronger sandboxing, and tamper-evident audit are implemented." (lines 906–908)

> "`BUILD_ROOTFUL` warning is surfaced by `midnight doctor` whenever co-located rootful BuildKit is detected. Granting `Developer`/`Deployer` roles in Alpha requires explicit operator acknowledgement that those roles are host-root-equivalent." (line 128)

> "Co-located BuildKit in Alpha runs as a dedicated unprivileged user with a CPU/memory cgroup cap, dedicated tmpfs, and no read access to `/var/lib/midnight/{db,secrets,registry/data,kms}`. Rootless BuildKit is the v1-GA blocker." (line 84)

The trust profiles (line 932) make the staircase concrete: local/dev trusted single-node → self-hosted team trusted-code → self-hosted semi-trusted → public multi-tenant. Phase 0 is profile A (trusted-code, rootful Docker, hardened defaults). Profile C (semi-trusted users) and Profile D (public multi-tenant) are out of scope until rootless BuildKit, separate trust domains, abuse controls, and tamper-evident audit (line 1261, ADR-011) all land.

This ADR makes the **trust-boundary acknowledgement** explicit and enforceable in product UX, not just docs.

## Decision

Midnight Phase 0 Alpha runs **rootful Docker Engine** as the runtime substrate. The product surface acknowledges the host-root-equivalence in three places:

1. **`midnight doctor` surfaces `BUILD_ROOTFUL`** whenever co-located rootful BuildKit is detected (panel: line 128).
2. **Granting `Developer` or `Deployer` roles** in Alpha **requires explicit operator acknowledgement** that those roles are host-root-equivalent. The acknowledgement is captured in the audit log (line 128).
3. **Hosted/public multi-tenant mode is blocked** at install. Profile flags allow only `local` and `self-hosted-trusted` in Phase 0; profiles `semi-trusted` and `public-multi-tenant` are not reachable.

Co-located BuildKit constraints (panel: line 84):

- Runs as a **dedicated unprivileged user** with a CPU/memory cgroup cap, dedicated tmpfs, and **no read access** to `/var/lib/midnight/{db,secrets,registry/data,kms}`.
- Build cache mounts are scoped per `(tenant, service, secret-binding-set)`; rotating any bound secret invalidates the cache (line 838).
- Untrusted-build classification is explicit (line 83): any commit whose author is not a workspace member, any push from a fork, or any branch outside the trusted-branch allowlist runs with no project secrets, no shared cache read or write, and pushes to a quarantine path.

**Rootless BuildKit is the v1-GA blocker** (panel: line 84). Until rootless BuildKit ships, Midnight does not claim "production-ready for semi-trusted teams." v1 will:

- Prefer rootless BuildKit with RootlessKit network isolation where practical, with documented rootful fallback (line 828).
- Emit `BUILD_COLOCATED_RISK` (or equivalent) when co-located BuildKit is detected on a production-tier host.
- Document the separate-build-host pattern as the recommended layout for hosted/semi-trusted deployments.

## Trust Boundary Statement

The following is normative for documentation, marketing, and `midnight doctor`:

> Midnight Alpha is a single-node trusted-code platform. Anyone with `Developer` or `Deployer` rights can run arbitrary code on the host, because Docker daemon access is host-root-equivalent. Midnight protects trusted self-hosted operators from accidental cross-project exposure; it does not protect a public platform operator from malicious tenants. Public multi-tenant hosting requires stronger isolation than rootful Docker, including microVM/gVisor/Kata-style sandboxing, separate builder hosts, abuse controls, and tamper-evident audit.

This statement is shipped in `/usr/share/midnight/runbooks/trust-model.md` and surfaced verbatim in `midnight doctor --check=OR-CHK-TRUST-PROFILE`.

## Alternatives Considered

- **Ship rootless Docker / rootless BuildKit at Alpha.** Rejected: rootless Docker has incomplete support for cgroup v2 device controllers, network namespace edge cases, and `userns-remap` is operationally heavy for the 12-week Phase 0. Panel made rootless BuildKit a v1 blocker, not an Alpha requirement (line 84).
- **Run BuildKit on a separate host from runtime in Alpha.** Considered: this is the right end state for hosted/semi-trusted profiles. Out of scope for Phase 0 because the install promise is single-VM (line 191). Documented as the recommended pattern when operators move to Profile C.
- **Use gVisor or Kata for tenant containers in Alpha.** Rejected at Alpha: adds an OCI runtime that has its own kernel-compatibility matrix, performance profile, and operational story. Documented as a Phase 4 hosted-track option.
- **Don't acknowledge the trust boundary explicitly; just document it in the README.** Rejected: panel explicitly required UX-level acknowledgement (line 128). Soft warnings get ignored; explicit acknowledgement creates an audit row.
- **Allow `--profile=public-multi-tenant` with a `--i-know-what-im-doing` flag.** Rejected: the panel's guard is policy, not just defaults (lines 906–908). We do not ship the unsafe profile at all.

## Consequences

Positive:

- The trust model is explicit in product UX, not buried in docs.
- `BUILD_ROOTFUL` warning + acknowledgement-on-role-grant prevent the most common operator mistake: handing a `Developer` token to someone they wouldn't hand root to.
- v1 gets a concrete blocker (rootless BuildKit), not a vague "future hardening."
- Phase 0 ships in 12 weeks because we are not also doing rootless BuildKit, gVisor integration, and microVM packaging at the same time.

Negative:

- Phase 0 is unsuitable for hosting third-party untrusted code. We say so loudly.
- Co-located BuildKit means a build that escapes BuildKit's sandbox can read host paths it shouldn't. Mitigation: the panel-locked cgroup cap, dedicated tmpfs, no-read access to `/var/lib/midnight/{db,secrets,registry/data,kms}`, and the build-output secret leak scanner (`BUILD_OUTPUT_SECRET_LEAK`, line 129).
- Operators who want to host fork PR builds must wait for the v1 untrusted-build classification + quarantine registry path (line 83).

Follow-up work:

- `midnight doctor --check=OR-CHK-BUILD-ROOTFUL` ships in Phase 0 (`SPRINT_07_OBSERVABILITY_SUPPORT.md`).
- Role-grant flow records explicit acknowledgement in audit (`SPRINT_09_VARS_SECRETS_AUDIT.md`).
- Trust-model runbook ships at `/usr/share/midnight/runbooks/trust-model.md` (`SPRINT_07_OBSERVABILITY_SUPPORT.md`).
- v1 epic: rootless BuildKit; covered in `EPICS.md` v1-foundations track.

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestRoleGrantRequiresAcknowledgement`** in `pkg/auth/rbac`: granting `Developer` or `Deployer` without `acknowledged_root_equivalent: true` returns `400 ACK_REQUIRED`; with the acknowledgement, an audit row is written with the actor and timestamp.
- **Test name `TestBuildKitUnprivilegedUser`** in `pkg/build/buildkit`: the systemd unit asserts `User=midnight-build`, `Group=midnight-build`, and the cgroup limits match the installer manifest.
- **Test name `TestBuildKitNoReadAccess`** in `pkg/build/buildkit`: a probe inside the BuildKit container asserts `read /var/lib/midnight/db` and `read /var/lib/midnight/secrets` fail with `EACCES`.
- **Test name `TestPublicMultiTenantProfileBlocked`** in `pkg/install/profile`: `--profile=public-multi-tenant` is rejected by the installer with a clear refusal message.
- **Runbook check `OR-CHK-BUILD-ROOTFUL`** in `midnight doctor`: detects co-located rootful BuildKit and emits the panel-locked `BUILD_ROOTFUL` warning. Output appears in support bundles.
- **Runbook check `OR-CHK-TRUST-PROFILE`** in `midnight doctor`: prints the trust-boundary statement verbatim.
- **CI gate `gate.no-public-multi-tenant`:** integration test attempts to set `--profile=public-multi-tenant`; expects the install to fail.
- **Code review checklist item `CR-TRUST-01`:** any feature that exposes elevated access (exec, shell, container manipulation) must reference this ADR and confirm it does not regress the trust model.
- **Failure drill `DRILL-BUILD-ESCAPE`** in `SPRINT_08_FAILURE_DRILLS.md`: a build attempts to read `/var/lib/midnight/secrets/`; the cgroup + filesystem confinement prevents it; alert fires.
- **Audit record `audit:rbac.role.granted`** carries the acknowledgement flag; `midnight audit verify` chains it (ADR-011).

## References

- spec: lines 64, 84, 128, 129, 838, 892–908, 932–937, 2316
- features: `FEATURE_DAEMON_JSON_BASELINE.md`, `FEATURE_AUDIT_HASH_CHAIN.md`, `FEATURE_ROLLBACK_BY_DIGEST.md`
- sprints: `SPRINT_05_BUILDKIT.md`, `SPRINT_07_OBSERVABILITY_SUPPORT.md`, `SPRINT_08_FAILURE_DRILLS.md`, `SPRINT_09_VARS_SECRETS_AUDIT.md`
- handoff: `SECURITY_REVIEW_PLAN.md`, `OPERATIONS_RUNBOOK_INDEX.md`
