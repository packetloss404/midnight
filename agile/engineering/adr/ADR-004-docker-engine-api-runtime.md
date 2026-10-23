# ADR-004: Docker Engine API as the runtime, not the Docker CLI or direct containerd

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, runtime, host-agent, security, version-floor]

## Context

Midnight's host agent must create, start, stop, inspect, and observe containers; attach networks; mount volumes; enforce CPU/memory/PID/log limits; apply restart policy; and stream logs. The panel had three credible runtime substrates: shell out to the Docker CLI, talk directly to containerd, or use the Docker Engine API. The Linux/AI review pass and the eight-specialist panel both pinned the Docker Engine API as the v1 contract:

> "Runtime v1 uses Docker Engine through the Docker Engine API. The agent must not shell out to the Docker CLI for normal operation. containerd is a later adapter." (line 32, reaffirmed at line 403, line 852, line 2373)

> "Docker Engine API floor: v1.43 minimum (Engine ≥ 24.0); the agent refuses to start with `DOCKER_ENGINE_UNSUPPORTED` outside the supported range. The compatibility matrix is a published artifact, not a promise — `/docs/compat/docker.md` and CI-gated." (line 90)

The panel also pinned the supporting host contract: `cgroup v2` is hard-required (line 88, ADR-005), the `daemon.json` baseline is enforced by the installer (line 89), `HostConfig.Init = true` (tini) is the default for platform-managed containers (line 93), and the agent rejects unsafe specs even if the control plane sends them (line 899). All of these are properties of the Docker Engine API request, not of containerd primitives or CLI flags.

CLI shell-out has additional defects: each `docker run` is a process fork plus argv parsing, no streaming reconciliation, no typed errors, and sub-process output parsing is brittle. Direct containerd skips Docker's network and storage drivers, forcing Midnight to reimplement bridge networking, port publication, IPAM, and volume management — exactly what the panel chose Docker for in MVP (line 33: "Use Traefik for public ingress … and `Use Docker Engine through the Docker Engine API`").

## Decision

The host agent **only** talks to Docker via the **Docker Engine API** (the HTTP/Unix-socket API, accessed through the official Go client `github.com/docker/docker/client`). The agent must not shell out to `docker` or `nerdctl` for normal operation. The Docker Engine API floor is **v1.43 minimum (Engine ≥ 24.0)**.

The agent enforces:

- A narrowed container spec allowlist; unknown Docker options are rejected by default (line 899).
- `HostConfig.Init = true` (tini) for platform-managed containers (line 93).
- `no-new-privileges=true`, dropped capabilities by default with the panel-locked add-back set (`CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, SETGID, SETUID, NET_BIND_SERVICE, SETPCAP, SYS_CHROOT`) (lines 91, 911–921).
- Default ulimits `nofile=65536:1048576`, `nproc=<pids-limit>` (line 93).
- `AppArmor` profile `midnight-default` from the `.deb`, falling back to `docker-default` (line 92).
- Refusal to start with `DOCKER_ENGINE_UNSUPPORTED` outside the supported range (line 90).

Direct containerd is a later runtime adapter, not a v1 path (lines 32, 403, 852).

## Alternatives Considered

- **Shell out to the Docker CLI.** Rejected: the panel explicitly forbids it ("The agent must not shell out to the Docker CLI for normal operation," line 32). CLI shell-out has no streaming reconciliation, brittle output parsing, no typed errors, and a different Docker version compatibility surface. It also runs a different binary than the agent's compiled-in client, doubling the version-floor problem.
- **Direct containerd CRI.** Rejected at v1: containerd has no Docker-compatible bridge networking, port publication, named volumes, or `daemon.json` baseline. We would have to re-implement all four to ship Phase 0 in 12 weeks. Containerd is documented as "a later runtime adapter" (lines 32, 403). When the multi-host/Kubernetes adapter ships in Phase 3, containerd or CRI-O become viable; not before.
- **Podman.** Rejected: ecosystem maturity for the Engine API client equivalence, the `daemon.json` baseline (line 89), and BuildKit integration (ADR-008-adjacent) is weaker. Podman's rootless story is interesting for ADR-010's v1 evolution but does not change the v1 decision today.
- **Direct OCI runtime (runc, crun) with a self-built supervisor.** Rejected: turns Midnight into a container runtime project. The first product loop is install, deploy, HTTPS, and keep running; it is not runtime engineering.
- **Kubernetes from day one.** Rejected by the senior panel and architecture review (line 23: "Make Kubernetes a later runtime adapter, not the default installation requirement").

## Consequences

Positive:

- The Engine API gives us typed requests/responses, streaming events, and an explicit version handshake the agent can refuse cleanly (`DOCKER_ENGINE_UNSUPPORTED`).
- We inherit Docker's bridge networking, port publication, named volumes, and `daemon.json` enforcement (line 89), all of which the panel relies on for the rest of the architecture.
- Compat matrix is a single artifact (`/docs/compat/docker.md`) rather than a CLI-flag patchwork (line 90).
- Reconciliation can be event-driven: `GET /events` streams Docker state changes; agent reconciles labels and container state against Postgres desired state (line 942).

Negative:

- The Docker daemon is host-root-equivalent (line 64, line 894). Only `midnight-agent` may access the Engine API socket. This is acknowledged and documented; rootful trust boundary is ADR-010.
- We are coupled to Docker's release cadence. The compatibility matrix (line 194) caps server N's tolerance window; agents/workers/Engines outside the window are refused with `VERSION_SKEW_REJECTED` (line 193).
- A future Kubernetes adapter must abstract the Engine API behind a `RuntimeAdapter` interface. We design for it (line 882: "BuildArtifact -> ScheduleRequest -> InstanceStatus -> RoutingTarget") but do not ship the abstraction until Phase 3.

Follow-up work:

- Engine API version handshake at agent boot; refuse with `DOCKER_ENGINE_UNSUPPORTED` outside the window (acceptance criterion in `FEATURE_DAEMON_JSON_BASELINE.md`).
- Compatibility matrix artifact `/docs/compat/docker.md` published at release; CI gates ensure it tracks the agent's compiled client version.
- `RuntimeAdapter` interface sketched in code (no second implementation in Phase 0) so containerd/CRI work in Phase 3 does not require a refactor.

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestNoDockerCLIShellOut`** in `pkg/agent/runtime`: a static-analysis test asserts no production package imports `os/exec` to invoke `docker`, `dockerd`, or `nerdctl`. New invocations require an ADR amendment.
- **Test name `TestEngineAPIVersionFloor`** in `pkg/agent/runtime`: agent rejects an Engine reporting API version < v1.43 with `DOCKER_ENGINE_UNSUPPORTED`; matching version proceeds; newer version inside the window proceeds; newer version outside the window proceeds with a documented warning (panel: line 90).
- **Test name `TestContainerSpecAllowlist`** in `pkg/agent/runtime`: a tenant-supplied spec containing `Privileged=true`, `HostNetwork`, `PidMode=host`, or arbitrary device mounts is rejected (line 896, line 1249).
- **Test name `TestHostConfigInit`** in `pkg/agent/runtime`: every platform-managed container spec sets `HostConfig.Init = true` (panel: line 93).
- **Test name `TestCapabilityAllowlist`** in `pkg/agent/runtime`: default cap set matches the panel-locked list; templates declare additional caps via the operator-controlled allowlist (panel: line 91).
- **CI gate `gate.compat-matrix-published`:** release pipeline asserts `/docs/compat/docker.md` exists and references the same Engine API version as the compiled agent.
- **Runbook check `OR-CHK-DOCKER-ENGINE`** in `midnight doctor`: reads `docker system info`, asserts `cgroup v2`, `live-restore`, `userland-proxy=false`, `iptables=true`, `no-new-privileges=true`, `storage-driver=overlay2` per the daemon.json baseline (panel: line 89).
- **Code review checklist item `CR-RUNTIME-01`:** any new Docker interaction must use the typed Engine API client; no `exec.Command("docker", …)` is permitted.

## References

- spec: lines 32, 88–98, 403, 852–908, 942–954, 1249, 2373
- features: `FEATURE_DAEMON_JSON_BASELINE.md`, `FEATURE_DEPLOYMENT_STATE_MACHINE.md`
- sprints: `SPRINT_02_INSTALLER.md`, `SPRINT_04_HOST_AGENT.md`, `SPRINT_05_BUILDKIT.md`
- handoff: `OPERATIONS_RUNBOOK_INDEX.md`, `SECURITY_REVIEW_PLAN.md`
