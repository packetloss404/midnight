# ADR-0018: Deployment-execution Scheduler interface

- Status: accepted
- Date: 2026-05-17
- Owners: platform / runtime

## Context

The deploy executor drives a single host's Docker daemon directly today:
`internal/deploy/executor.go` calls into `internal/runtime/docker.Adapter`
through the `ActivationRuntime` interface (PullVerify, Schedule, Start,
ResolveRoutingTarget, Drain). This is the right answer for the MVP, where
the control plane, worker, and runtime all live on the same host.

The backend roadmap commits to running deployments across multiple hosts.
The plumbing for that future is already half-built:

- `migrations/0001_alpha_core.up.sql` ships `hosts` and `host_heartbeats`.
- `internal/api/agent_handlers.go` exposes `/api/v1alpha/agents/register`,
  `/heartbeat`, `/desired`, and `/status`.
- `cmd/midnight-agent/` is the remote-host binary that already speaks the
  agent protocol (it ships with its own supervisor / reconciler).

What is missing is a seam between the deploy executor and the runtime that
the multi-host scheduler can drop into. Calling the Docker SDK directly
from `executor.go` makes it impossible to flip the switch without rewriting
the executor.

## Decision

Introduce `internal/scheduler.Scheduler` as the deployment-execution
boundary:

```go
type Scheduler interface {
    Schedule(ctx context.Context, intent ScheduleIntent) (DeploymentPlacement, error)
    Reconcile(ctx context.Context, deploymentID string) (ReconcileResult, error)
    Drain(ctx context.Context, deploymentID string, grace time.Duration) error
    State(ctx context.Context, deploymentID string) (DeploymentState, error)
}
```

The executor calls `Scheduler.Schedule(...)` instead of reaching into the
runtime adapter. Two implementations live behind this interface:

- **`internal/scheduler/local.LocalDockerScheduler`** — wraps the existing
  `runtime.RuntimeAdapter` (docker, local). Behavior is byte-identical to
  the pre-refactor executor; `local_test.go` is the regression contract.
  This is the default selector.
- **`internal/scheduler/multihost.MultiHostScheduler`** — picks a host from
  the existing `hosts` + `host_heartbeats` tables and writes desired-state
  rows that the existing `/api/v1alpha/agents/{id}/desired` endpoint
  returns to remote `midnight-agent` binaries. Schedule is implemented;
  Reconcile, Drain, and State return `scheduler.ErrNotImplemented` with
  TODOs pointing at the agent status callback path. No silent fallbacks.

The selector is `MIDNIGHT_SCHEDULER=local|multihost` (default `local`).
Both `cmd/midnight-server` and `cmd/midnight-worker` parse and validate the
selector at startup. The worker uses the parsed selector to construct the
scheduler instance it installs on the executor; the server validates the
selector so dev environments fail fast when typo'd.

## Interface contract

- `Schedule` MUST be idempotent on `ScheduleRequest.IdempotencyKey`. The
  local scheduler inherits idempotency from the docker adapter; the
  multi-host scheduler uses the idempotency key as the
  `agent_desired_state` etag.
- `Schedule` MUST respect ctx cancellation at named safe points. The
  executor enforces the existing "cancel before promotion" safe point
  unchanged — the scheduler does not need to know about cancellation
  semantics.
- The scheduler MUST NOT perform route promotion. The route promoter call
  stays in `internal/deploy/executor.go` because promotion is part of the
  operations FSM (one FSM step, not a scheduler primitive).
- `Schedule` MAY emit per-sub-step telemetry through the
  `ScheduleIntent.StepObserver` callback. The local scheduler emits a
  Started + Succeeded/Failed pair for every runtime call so the executor's
  pre-refactor `pull-verify`, `schedule`, `start`,
  `readiness-routing-check` telemetry order is preserved byte-for-byte.

## Migration: local → multihost

The day we flip `MIDNIGHT_SCHEDULER=multihost` we are filling in
implementation, not reshaping interfaces. The remaining work is bounded:

1. Wire concrete `HostRepository` + `DesiredStateWriter` implementations
   over pgx for `internal/scheduler/multihost`. The repository reads from
   `hosts` + `host_heartbeats`; the writer updates `agent_desired_state`
   (the existing GET `/desired` endpoint already serves the row).
2. Implement `MultiHostScheduler.Reconcile()` to project the agent's
   `agent_status_reports` row into `ReconcileResult`. The POST
   `/api/v1alpha/agents/{id}/status` endpoint already accepts the
   `AgentInstanceStatus` shape; we only need to convert it into a
   `runtime.InstanceStatus`.
3. Implement `MultiHostScheduler.Drain()` by writing a desired-state
   document with the instance marked `DRAINING` + grace, polling the
   status report until state=STOPPED.
4. Implement `MultiHostScheduler.State()` from the same status report
   projection.
5. (Optional, separate ADR) Replace the first-fit placement strategy with
   the GPU/region/RAM-tier matcher in step 6 below.

None of these steps change the executor, the deploy intake, the
operations FSM, or the wire shapes the agents speak today.

## Why reuse the existing agent endpoints

The agent protocol (`/register`, `/heartbeat`, `/desired`, `/status`) is
already locked. It is the shape `cmd/midnight-agent/` reconciles against
and the shape the version-skew gate (`agent_handlers.go`, `EnforceAgentVersion`)
defends. Introducing a separate scheduler-→-agent channel would mean:

- Two heartbeat surfaces to keep in sync.
- A second auth-and-clock-skew code path.
- Two desired-state stores to keep transactionally consistent.

Reusing the existing endpoints means the scheduler is "just another writer
to `agent_desired_state`", and agents do not need to know whether the
control plane is running single-host or multi-host. The migration is a
deployment concern, not a protocol concern.

## Stacker capability advertising

Multi-host placement uses a label-bag model matching the Railway
region/GPU/RAM tier UX. Hosts advertise capabilities in their
`hosts.labels` JSONB column when they register; placement filters hosts by
the `RequiredCapabilities` map on `ScheduleIntent`.

The default contract today is:

| Key       | Example values        | Source                        |
|-----------|-----------------------|-------------------------------|
| `runtime` | `docker`              | Hard-coded for the MVP        |
| `region`  | `us-east`, `eu-west`  | Agent config (future)         |
| `gpu`     | `nvidia-h100`, `none` | Agent capability probe        |
| `ram_tier`| `8gi`, `32gi`, `128gi`| Agent capability probe        |
| `cgroup`  | `v2`                  | Docker adapter cgroup probe   |

`MultiHostScheduler` ships with the minimum filter — `{"runtime":
"docker"}` — and picks the first healthy host in deterministic id order.
The richer matcher (GPU + region + RAM tier + cost) lands as a follow-up;
the interface does not have to change because the matcher is an
implementation detail of `pickFirstFit` behind `MultiHostScheduler.Schedule`.

## Consequences

- **Today**: zero behavior change. `MIDNIGHT_SCHEDULER` defaults to
  `local`, the LocalDockerScheduler wraps the existing runtime adapter,
  and every executor test still passes.
- **Tomorrow**: the multi-host scheduler is a drop-in; the executor,
  deploy intake, operations FSM, and route promoter are unchanged.
- **Eviction risk**: the `ActivationRuntime` interface stays in
  `internal/deploy` as a back-compat alias so the worker's existing
  wiring keeps working. New callers MUST use `Executor.Scheduler` instead.

## Links

- Related panel decisions: ADR-015 (RuntimeAdapter boundary v1), ADR-006
  (routing telemetry).
- Related work items: hosts + host_heartbeats migration, agent endpoints
  in `internal/api/agent_handlers.go`, `cmd/midnight-agent/` supervisor.
- Related code: `internal/scheduler/service_scheduler.go`,
  `internal/scheduler/local/local.go`,
  `internal/scheduler/multihost/multihost.go`.
