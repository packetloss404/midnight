# S04 Runtime/Agent Completion Handoff

- **Worker:** Phase 0 Worker S04, Runtime/Agent
- **Workspace:** `D:/projects/midnight`
- **Primary source:** `agile/sprints/SPRINT_04_HOST_AGENT.md`
- **Reviewed contracts:** `agile/engineering/skeletons/INTERFACES_AGENT.md`, `agile/engineering/adr/ADR-004-docker-engine-api-runtime.md`, `agile/features/FEATURE_DEPLOYMENT_STATE_MACHINE.md`
- **Adjacent sprint review:** Sprint 03 Control Plane API, Sprint 05 BuildKit, Sprint 06 Ingress/Logs, Sprint 08 Failure Drills
- **Status:** Implementation-ready handoff. No production code exists in this repo yet; this document pins S04's first implementation slice and cross-team contracts.

## Scope

S04 owns the Phase 0 host runtime lane: `midnight-agent`, Docker Engine API runtime operations, default container hardening, heartbeat/reconcile behavior, host-side drift detection, and the first deploy path for a pre-built OCI image digest.

Owned behaviors:

- Agent process lifecycle under systemd, including graceful shutdown and runtime-unavailable backoff.
- Host registration and session negotiation with Docker Engine API version floor `v1.43` / Engine `>= 24.0`.
- Heartbeats every 5s with clock offset, uptime, disk/inode/conntrack pressure, pending pulls, drift, and observed instance state.
- Docker Engine API client over `unix:///var/run/docker.sock`; no Docker CLI or `nerdctl` shell-outs for normal operation.
- Deterministic container naming and required Midnight label schema.
- Label-based reconciliation loop that converges server desired state to local Docker containers and reports observations back.
- Default container hardening: cap drop/add allowlist, `no-new-privileges`, `Init=true`, explicit ulimits, pids limit, AppArmor fallback, host namespace refusal.
- Per `(project_id, environment_id)` Docker bridge network creation and tenancy invariant checks.
- Image digest resolution and cosign-equivalent signature verification before `ImagePull`.
- First existing-OCI-image deploy path through `PENDING -> DEPLOYING -> ACTIVE`, with agent reporting instance observations only.
- `LOST` instance observations and freshness inputs for route membership; public deployment state remains control-plane owned.

Explicit non-ownership:

- S04 does not own BuildKit, Dockerfile builds, source upload, build cache, provenance generation, or registry GC. Those are S05.
- S04 does not own public ingress policy, ACME issuance, custom domains, or final Traefik config semantics. Those are S06, with a boundary note below.
- S04 does not own the public deployment FSM transition authority. Agent code must not mark deployments `ACTIVE`, `FAILED`, `REMOVED`, or `SUPERSEDED`.
- S04 does not own API-wide idempotency, OpenAPI generation, auth token minting, or the error-code registry storage. Those are S03 contracts consumed by S04.

## Owned Future Implementation Paths

These are the paths S04 should own when code is created. Names follow `INTERFACES_AGENT.md` and the planned repo layout.

| Path | Owner intent |
|---|---|
| `cmd/midnight-agent/main.go` | Agent entrypoint, config load, supervisor, signal handling, non-zero exits for unsupported host preflights. |
| `internal/agent/supervisor/` | Lifecycle orchestration for registration, heartbeat, reconcile, drift, log collection, and shutdown deadline. |
| `internal/agent/config/` | `/etc/midnight/agent.yaml`, env overrides, trust policy paths, reconcile/backoff settings. |
| `pkg/agent/engineclient/` | Typed Docker Engine API client, API version negotiation, Engine error mapping, mock interface. |
| `pkg/agent/runtime/` | Container create/start/stop/remove/inspect/pull planning and idempotent runtime operations. |
| `pkg/agent/specvalidator/` | ContainerSpec hardening defaults and rejection of unsafe/unknown Docker options. |
| `pkg/agent/reconciler/` | Desired vs observed diff, bounded concurrency, local desired cache, backoff, warmup handling. |
| `pkg/agent/heartbeat/` | Heartbeat payload construction, agent sequence/HMAC, observation batching. |
| `pkg/agent/hostprobe/` | cgroup v2, kernel, OS/arch, AppArmor/SELinux, subuid, Docker baseline probes. |
| `pkg/agent/network/` | Bridge naming, IPAM allocation, default bridge refusal, tenancy invariant check. |
| `pkg/agent/imageverify/` | Digest resolution, cosign/trust-policy verification cache, pre-pull enforcement. |
| `pkg/agent/drift/` | Daemon baseline drift, orphan containers, network tenancy drift, conntrack pressure. |
| `pkg/agent/logcollector/` | Docker `json-file` tailing and NDJSON emission for S06 logs. |
| `pkg/orailtypes/agent.go` | Shared IDs, instance states, reason codes, desired/observed DTOs; keep server-safe and importable by agent. |
| `proto/agent/v1/` | Agent RPC/stream schema if S03/S04 choose protobuf transport for desired state and observations. |
| `scripts/no-docker-cli.sh` | Static CI guard forbidding production Docker CLI shell-outs. |
| `docs/compat/docker.md` | Engine API compatibility matrix and supported/refused ranges. |
| `docs/runtime/labels.md` | Container and network label schema. |
| `docs/runtime/class-a-ops.md` | Warmup-mode operations allowed during control-plane recovery. |
| `docs/security/image-trust.md` | Cosign trust policy file format and examples. |
| `runbooks/host-register-failed.md` | Registration/version/cgroup/clock-skew failure triage. |
| `runbooks/network-tenancy-drift.md` | P1 drift response for cross-tenant bridge contamination. |
| `runbooks/host-unavailable.md` | `LOST` / heartbeat timeout response path. |

## Required Contracts From S03

S04 is blocked until these S03 contracts are stable enough to compile/test against.

1. **Stable API envelope**
   - Error JSON is exactly `{code, message, request_id, docs_url, details?}`.
   - Runtime codes must already exist or be accepted in the additive registry: `DOCKER_ENGINE_UNSUPPORTED`, `AGENT_RUNTIME_UNAVAILABLE`, `AGENT_CLOCK_SKEW`, `IMAGE_PULL_FAILED`, `IMAGE_SIGNATURE_INVALID`, `CONTAINER_NAME_CONFLICT`, `NETWORK_TENANCY_DRIFT`, `CAP_NOT_PERMITTED`, `HOST_UNAVAILABLE`, `IDEMPOTENCY_REQUEST_IN_PROGRESS`.

2. **Auth and token class**
   - `Authorization: Bearer midn_agent_<26-base32>` works for host-agent APIs.
   - Agent token hash storage, public prefix, revocation, expiry, and rate-limit budget are S03-owned.
   - Agent bootstrap/registration must not require the agent to store a user/admin token.

3. **Registration and host schema**
   - `hosts` table supports `host_id`, `agent_version`, `os`, `arch`, `kernel`, `cgroup_version`, `docker_engine_version`, `docker_api_version`, `apparmor_loaded`, `selinux_mode`, `subuid_present`, negotiated API version, state, and last heartbeat.
   - `POST /api/v1alpha/hosts/register` accepts the S4-01 capability report and returns `hst_<ULID>`, an agent session token, and pinned negotiated Engine API version.
   - Control plane rejects cgroup v1 with the exact remediation string for `systemd.unified_cgroup_hierarchy=1`.

4. **Heartbeat and observation tables**
   - `host_heartbeats` is monthly range-partitioned with daily pruning and 2-week default retention.
   - `deployment_instances` exists and can receive observed per-instance state without allowing the agent to mutate public deployment state.
   - S03/S04 must agree on idempotence for heartbeat retries: S4 expects HMAC over `(host_id, agent_seq)`.

5. **Desired-state transport**
   - Sprint 4 names `GET /agents/{host_id}/desired`; `INTERFACES_AGENT.md` names a gRPC/protobuf agent-control stream.
   - S03 must lock one Phase 0 transport before S04 implements reconcile. Recommendation: keep the agent reconciler behind a transport interface and start with REST polling if that is the only S03-ready surface, while preserving `proto/agent/v1` message shapes.

6. **Deployment and operation primitives**
   - Mutating deploy endpoint returns `202 Accepted` plus Operation and honors `Idempotency-Key`.
   - Deployment creation takes `pg_advisory_xact_lock` per `(service_id, environment_id)` and produces monotonic `deployment_seq`.
   - `deployment_image_pins` must exist by S4-09 or S04/S03 need a small migration before the first deploy E2E.

7. **FSM authority**
   - Control plane enforces that the agent role cannot transition `deployments.state`.
   - Only the Deployment reconciler promotes `DEPLOYING -> ACTIVE` after `ready_count >= minReady` and route readiness.

8. **Correlation and audit/event surface**
   - `X-Request-Id` is emitted and persisted into operation/event/audit records.
   - Agent registration, heartbeat failure, runtime unavailable, drift, and instance LOST events have typed audit/event records for S08 drill assertions.

## Exported Contracts To S05

S05 BuildKit/provenance work consumes the runtime contracts below.

- **Image input shape:** S04 deploys only immutable image digests for production paths. Mutable tags must be resolved before deploy or rejected once a digest is available.
- **Signature gate:** S04 verifies signature/trust policy before `ImagePull`; `IMAGE_SIGNATURE_INVALID` is terminal and non-retriable. S05 must produce signatures/attestations that satisfy `/etc/midnight/cosign-trust.yaml`.
- **Pull/deploy API:** S05's build success path can enqueue or call the same existing-image deploy path used by S4-09. No S05-specific runtime path should bypass the S04 agent.
- **Snapshot fields:** S04 requires `deployment_id`, `deployment_seq`, `image_digest`, `service_id`, `environment_id`, `project_id`, `instance_id`, desired container spec, and minReady. S05 adds `builder_kind`, build versions, base digests, provenance URI, and produced image digest to the snapshot.
- **Registry retention:** S04 writes/depends on image pins for the deployed digest; S05 registry GC must never collect pinned digests or digests inside the rollback window.
- **Failure mapping:** S04 emits `IMAGE_PULL_FAILED`, `IMAGE_SIGNATURE_INVALID`, `IMAGE_ARCH_UNAVAILABLE`, and `HOST_UNAVAILABLE`; S05 emits build/provenance codes and must not overload runtime codes.
- **E2E test handoff:** S5-07 and S5-12 must prove `build -> sign -> push -> S04 verify -> pull -> start -> observed READY`.

## Exported Contracts To S06

S06 Ingress/Logs/Timeline consumes the runtime observations below.

- **Route target inputs:** S04 reports fresh `deployment_instances` with host id, container id, container IP, mapped/internal service port, readiness status, and `last_observed_at`.
- **Freshness semantics:** Route membership requires `last_observed_at > now() - route_freshness_ttl` (default 30s). Re-add requires 3 consecutive healthy heartbeats unless S06 explicitly overrides the default.
- **Healthcheck split:** S04 owns the agent-side probe runner when S6-09 lands. Startup `ECONNREFUSED` is tolerated until startup timeout; only readiness removes route membership; liveness restarts containers without deployment demotion.
- **Network isolation:** S04 attaches service containers only to `or-net-{project}-{env}` and never to Docker's default `bridge`. S06 ingress must target those container IPs/ports and not use Docker provider labels.
- **Logs:** S04 creates containers with Docker `json-file` log config and tails into NDJSON `{ts, stream, level, msg, attrs}`. S06 owns query/API/dashboard presentation.
- **Timeline events:** S04 supplies host/start/image-pull/container-created/container-started/healthcheck/runtime-failure events with stable codes for S6-11.
- **Boundary risk:** `INTERFACES_AGENT.md` assigns Traefik dynamic file writing to `RouteApplier`, while Sprint 6 S6-01 assigns the config writer to the server/networking lane. S04 should not implement Traefik file writes until S06 confirms ownership. Preferred Phase 0 split: S04 reports route targets and freshness; S06 writes Traefik config atomically.

## Exported Contracts To S08

S08 Phase 0 exit drills require deterministic S04 behavior and observability.

- D-01 kill agent: host enters `AGENT_LOST` within `<= 3x` heartbeat interval; tenant containers keep running.
- D-02 kill server: agent uses jittered backoff and warmup handling; workloads keep serving.
- D-04 restart Docker daemon: `live-restore` is verified, containers keep running, agent reconnects to the Engine API.
- D-05 image pull failure: candidate deploy fails with `IMAGE_PULL_FAILED`; previous active remains unchanged.
- D-06 readiness failure: candidate is not promoted; runtime reports readiness attempts and failure code.
- D-10 clock skew: registration refused with `AGENT_CLOCK_SKEW` when skew exceeds 30s; ACME-related ops blocked above 5s once S06 lands.
- D-14 network partition: 10s/60s/10min paths show jitter, no reconcile storm, correct `LOST` and `HOST_UNAVAILABLE` behavior.
- D-16 Docker socket EOF: agent stays running, surfaces `AGENT_RUNTIME_UNAVAILABLE`, backs off, and recovers without process restart.
- S08 drill harness must be able to observe state, reason code, audit/event row, user diagnostic, and runbook annotation for each S04-related drill.

## Acceptance Checklist

S04 is complete when every item below is true.

- [ ] `midnight-agent` registers with capability report and receives `host_id`, session token, and negotiated Engine API version.
- [ ] Engine API versions below `v1.43` refuse with `DOCKER_ENGINE_UNSUPPORTED`; cgroup v1 refuses with `AGENT_CGROUP_V1`; clock skew above 30s refuses with `AGENT_CLOCK_SKEW`.
- [ ] Agent never shell-outs to Docker CLI for normal operation; static CI check catches `exec.Command("docker")`, `dockerd`, and `nerdctl` invocations.
- [ ] Docker Engine client package is typed, mockable, preserves Engine status codes, and pins version headers.
- [ ] Heartbeat loop sends required fields every 5s, retries idempotently, backs off with decorrelated jitter, and avoids reconnect storms.
- [ ] Desired-state reconcile is idempotent; repeated reconcile with unchanged desired set performs zero Engine writes.
- [ ] Container name format is `or-{svc_id_short}-{dep_seq}-{inst_seq}`, RFC 1123-valid, deterministic, and collision-handled.
- [ ] Required labels are present on every managed container and documented in `docs/runtime/labels.md`.
- [ ] Container hardening defaults are exactly enforced: `CapDrop: [ALL]`, 10-cap add-back allowlist, `no-new-privileges:true`, `Init=true`, explicit `nofile`/`nproc`, pids limit, no privileged, no host PID/network/IPC.
- [ ] Operator cap allowlist rejects out-of-policy requested caps with `CAP_NOT_PERMITTED` before container create.
- [ ] Per-scope bridge network is created, conflict-checked, and enforced; wrong-tenant container attachment raises `NETWORK_TENANCY_DRIFT`.
- [ ] Image signature verification runs before pull; failure produces `IMAGE_SIGNATURE_INVALID` and no `ImagePull` call.
- [ ] Runtime-unavailable path keeps the agent process alive when Docker is stopped or socket EOF occurs.
- [ ] Agent reports observed instance state only; tests prove agent role cannot transition public deployment state.
- [ ] Existing OCI image deploy demo creates and starts a hardened nginx container within 30s, with observed instance state feeding the Deployment reconciler.
- [ ] LOST freshness and hysteresis match `FEATURE_DEPLOYMENT_STATE_MACHINE.md`: TTL 30s, degraded grace default 60s unless planning changes it, re-add after 3 healthy heartbeats.
- [ ] Runbooks and docs listed above exist and are linked from error messages or doctor output.

## Risks And Decisions Needed

| Risk | Impact | Owner / required action |
|---|---|---|
| Sprint 4 is over target velocity at 54 points. | S4-09 first deploy can slip if S4-01 through S4-08 do not land early. | S04 should implement registration, Engine client, spec validator, labels, and reconcile skeleton first; treat S4-12/S4-13 as overflow if needed. |
| Desired-state transport conflict: REST polling in Sprint 4 vs gRPC/proto stream in interface skeleton. | Reconciler can be implemented against the wrong transport. | S03/S04 must freeze Phase 0 transport before coding; hide it behind `DesiredSource` interface. |
| Traefik writer ownership conflict between `INTERFACES_AGENT.md` and Sprint 6. | Duplicate writers could corrupt route config or blur ownership. | S06 must choose final owner; S04 should initially export route targets/freshness only. |
| Docker socket is host-root-equivalent. | Agent compromise is host compromise. | Keep Docker socket access only in `midnight-agent`; enforce narrow spec validator and no tenant access to socket. |
| Engine API behavior differs across 24/25/26. | Inspect/health fields may vary. | CI matrix against min/max supported Engine API; mock unknown/newer fields. |
| Cosign trust policy too strict for early external images. | First deploy dogfood can block on unsigned images. | Support `warn` for non-prod, `enforce` by default for production tier. |
| IPAM collision with existing host networks. | First deploy fails or cross-tenant routing breaks. | Doctor/preflight conflict check against existing Docker networks and daemon default pools before deployment. |
| Agent and control plane state authority confusion. | Racey or invalid deployment state transitions. | Agent can only report observations; Deployment reconciler remains sole promoter to `ACTIVE`. |
| `deployment_image_pins` not present in S03 schema. | Registry retention and rollback-by-digest are unsafe. | Add S4 migration or confirm S03 table before S4-09 starts. |

## Exact First Implementation Tasks

Do these in order; each task should be a small PR with tests.

1. **Freeze S04/S03 protocol contract**
   - Decide REST polling vs `proto/agent/v1` stream for Phase 0 desired state.
   - Write `docs/runtime/agent-control-protocol.md` with registration, heartbeat, desired, observed, and error examples.
   - Add OpenAPI/proto fixtures before production code.

2. **Scaffold the agent binary and config**
   - Create `cmd/midnight-agent/main.go`.
   - Create `internal/agent/config` with `/etc/midnight/agent.yaml` loader.
   - Add supervisor shutdown deadline of 10s.
   - Test: `TestAgentShutdownDeadline`.

3. **Implement host probes and registration DTOs**
   - Add `pkg/agent/hostprobe` for OS/arch/kernel/cgroup/AppArmor/SELinux/subuid/Docker version.
   - Add registration client using S03 error envelope.
   - Tests: `TestCgroupV2Required`, `TestClockSkewRefused`, `TestRegisterPayloadShape`.

4. **Implement typed Docker Engine client**
   - Add `pkg/agent/engineclient` using `github.com/docker/docker/client`.
   - Negotiate API version and reject `< 1.43`.
   - Map Engine errors to typed runtime errors with status codes preserved.
   - Tests: `TestEngineAPIVersionFloor`, `TestEngineAPIVersionMalformed`, `TestRuntimeUnavailable`.

5. **Add no-Docker-CLI guard**
   - Add `scripts/no-docker-cli.sh`.
   - Wire CI once CI exists.
   - Test/static gate name: `TestNoDockerCLIShellOut`.

6. **Implement ContainerSpec and validator**
   - Add `pkg/agent/specvalidator`.
   - Enforce hardening defaults and reject privileged/host namespace/device/unknown options.
   - Tests: `TestContainerSpecAllowlist`, `TestCapabilityAllowlist`, `TestHostConfigInit`, `TestNoHostNamespaces`.

7. **Implement labels and deterministic names**
   - Add `pkg/agent/runtime/names.go` and `labels.go`.
   - Create `docs/runtime/labels.md`.
   - Tests: `TestContainerNameDeterministic`, `TestContainerNameLengthBound`, `TestRequiredLabels`.

8. **Implement create/start happy path against mocked Engine**
   - Add runtime planner for pull/create/start using validated spec.
   - Assert hardening fields in the create request.
   - Tests: `TestCreateRequestHardening`, `TestCreateStartIdempotentNoop`.

9. **Implement image verification gate**
   - Add `pkg/agent/imageverify` trust-policy parser and digest-keyed verification cache.
   - Verification must run before Engine `ImagePull`.
   - Tests: `TestSignatureInvalidBlocksPull`, `TestUnsignedWarnPolicyProceeds`, `TestVerificationCacheByDigest`.

10. **Implement heartbeat loop**
    - Add `pkg/agent/heartbeat` with payload fields from S4-02.
    - Add jittered backoff helper shared by register/heartbeat/desired polling.
    - Tests: `TestHeartbeatPayloadShape`, `TestBackoffDecorrelatedJitterBounds`, `TestHeartbeatRetryIdempotence`.

11. **Implement reconcile core**
    - Add `pkg/agent/reconciler` diff planner and apply loop with max 4 in-flight Engine writes.
    - Persist desired cache under `/var/lib/midnight/agent/desired.json`.
    - Tests: `TestReconcileNoopNoEngineWrites`, `TestReconcileConverges`, property test for random desired/current mutations.

12. **Implement network manager**
    - Add `pkg/agent/network` bridge naming, IPAM conflict check, reference-counted removal, invariant sweep.
    - Tests: `TestBridgePerProjectEnvironment`, `TestDefaultBridgeNeverUsed`, `TestNetworkTenancyDrift`.

13. **Wire first OCI image deploy E2E**
    - Add demo fixture to deploy `nginx@sha256:...`.
    - Verify registration -> desired -> signature policy -> pull -> create -> start -> observed ready.
    - Demo script target: `scripts/demo/sprint4-deploy.sh`.

14. **Add runtime-unavailable and LOST handling**
    - Docker stopped/socket EOF keeps agent process alive.
    - LOST events are emitted after freshness expiry; re-add uses 3 healthy observations.
    - Tests: `TestDockerStopNoCrashLoop`, `TestLostFreshnessTTL`, `TestHysteresisReAdd`.

15. **Document and runbook completion**
    - Add `docs/compat/docker.md`, `docs/runtime/class-a-ops.md`, `docs/security/image-trust.md`.
    - Add runbooks for host registration failure, network tenancy drift, and host unavailable.

## Completion Notes

- The strongest invariant for S04 reviewers: the agent observes and reconciles local runtime state, but never owns public deployment state.
- The highest-risk code path is Docker Engine access. Keep the typed client small, heavily mocked, and integration-tested against the supported Engine range.
- The first demonstrable slice should be existing OCI digest deploy, not BuildKit. That keeps S04 independent from S05 while unblocking S05's end-to-end build deploy later.
