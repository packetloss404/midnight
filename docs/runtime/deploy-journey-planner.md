# Deploy Journey Planner

`internal/deploy.BuildPlan` is a pure deployment journey planner. It does not call Docker, create containers, update DNS, bind TCP ports, or write audit/timeline rows. Runtime side effects belong to the deploy executor and injected runtime/promoter dependencies.

The planner composes the current pure contracts into one deterministic plan:

- optional predeploy command intent
- cron and static markers when supplied by the caller
- stateless scheduler placement across ready hosts
- runtime `BuildArtifact -> ScheduleRequest` boundary projection
- optional TCP proxy product contract
- audit and timeline hints that callers can persist later

Blocked steps keep the plan inspectable and set `ready_for_dispatch=false`. Runtime requests are only produced for accepted placements.

## Worker Execution Contract

The live deploy-worker path starts with the durable deploy job payload and ends with operation events plus queue completion:

```
deploy job payload
  -> Docker/runtime ScheduleRequest
  -> pull-verify
  -> schedule/start
  -> runtime RoutingTarget
  -> readiness/routing gate
  -> Traefik file-provider publish
  -> operation events + previous-instance drain
```

`internal/deploy.Executor` owns that ordering. It decodes `midnight.dev/deploy-job/v1alpha1`, builds the `runtime.ScheduleRequest`, transitions the linked operation to `running`, and maps each failure into a terminal failed operation before failing the queue lease. Successful execution promotes the candidate before draining the previous active instance.

The executor boundary is intentionally injected:

- `ActivationRuntime` supplies pull verification, scheduling, start, routing target resolution, and drain. The real routing profile must supply a Docker-backed implementation.
- `RoutePromoter` publishes the candidate target after the readiness/routing gate. `cmd/midnight-worker` wires a Traefik file-provider promoter when a route config directory is configured; the package-level `deploy.TraefikRoutePromoter` is the reusable pure/promoter boundary for the same target-to-route conversion.
- `OperationUpdater` appends support-safe operation transitions and failures.
- `DeployStepObserver` receives support-safe step telemetry for `pull-verify`, `schedule`, `start`, `readiness-routing-check`, `promote`, `drain-previous`, and terminal success/failure. `cmd/midnight-worker` persists those events onto the operation event stream so dashboard timelines can render progress without reading worker logs.

Local/noop fallback remains available for deterministic tests and dry runs: the worker default runtime backend is `local`, and an empty route config directory wires `deploy.NoopRoutePromoter`. That fallback records runtime intents in memory and never publishes Traefik config. It must not be used as proof that Docker, Traefik reload, DNS, ACME, or HTTPS are working.

For real routing, start the worker with `--runtime-backend=docker` or `MIDNIGHT_WORKER_RUNTIME_BACKEND=docker`, Docker Engine access via `--docker-host` or `MIDNIGHT_WORKER_DOCKER_HOST` / `DOCKER_HOST`, a Postgres DSN (`MIDNIGHT_DB_DSN`, `MIDNIGHT_DATABASE_URL`, `MIDNIGHT_DB_DSN_FILE`, or `--postgres-dsn` / `--postgres-dsn-file`), and `--route-config-dir` / `MIDNIGHT_WORKER_ROUTE_CONFIG_DIR` pointing at an absolute Traefik dynamic publication root. The default Traefik root is `/etc/midnight/traefik/dynamic`; the directory is installer-owned and must already exist before the publisher is constructed. Route flags/env include `--route-public-host`, `--route-public-path`, `--route-public-tls`, `--route-cert-resolver`, `--route-group`, and `--route-target-port`. Runtime labels may carry `midnight.internal_port`, `midnight.target_port`, or `midnight.port`; `--route-target-port` / `MIDNIGHT_WORKER_ROUTE_TARGET_PORT` is the worker fallback. Deploys also need a worker image-trust policy: production uses `--verify-signatures` with `--cosign-binary` plus either `--cosign-public-key` or keyless certificate identity/issuer fields; tightly controlled acceptance runs can use repeatable `--trusted-image` immutable refs to inject the static verifier.

The Docker runtime reports readiness from Docker inspect state. A running container with no healthcheck is treated as ready for the current single-VM profile; `healthy` maps to `READY`, `starting` maps to `STARTING`, and `unhealthy` maps to `UNHEALTHY` with the Docker health log summarized into a support-safe reason. Route promotion still waits for the executor readiness/routing gate, so Docker health readiness is input evidence, not a direct permission to publish.

Queue lease heartbeats are separate from step telemetry. A deploy worker heartbeat keeps `jobs.heartbeat_at` current while the operation timeline records phase progress; release evidence should review both the heartbeat gap and the step timeline before treating a route promotion as healthy.

## Candidate Activation Gate

`internal/deploy.PlanCandidateActivation` is the pure activation-gate slice that follows signature-before-pull. It models the runtime order:

1. `pull-verify`
2. `schedule`
3. `start`
4. `readiness-routing-check`
5. `promote`
6. `drain-previous`

`promote` is the point of no return. In the real worker path, promotion means the `RoutePromoter` has published the candidate target to Traefik's file-provider directory. If pull verification, schedule, start, readiness, routing, or promotion itself is blocked, `drain-previous` is skipped and the plan marks the previous active version as protected. Stop/remove cleanup for the previous version must remain downstream of `drain-previous`; it is not part of the activation gate.

### Readiness/Routing Check Contract

Deploy worker execution should keep the readiness/routing decision separate from route publication. The activation planner stays pure; execution composes runtime evidence, route reconciliation evidence, and promoter output around it. The optional `ActivationReadinessRoutingChecker` receives explicit `deployment_id`, `instance_id`, `host_id`, `routing_target_id`, and route group fields so multi-host aggregation does not infer identity from process-local state.

The pure `EvaluateActivationReadinessRouting` helper defines "ready to promote":

- the runtime status belongs to the candidate instance and is `READY`
- the routing target belongs to the same candidate instance
- the routing target carries the expected host and routing target identifiers when those identifiers are known
- the routing target reports `Ready=true`
- when router reconciliation evidence is supplied, its summary is `ready` with no pending, stale, unknown, or mismatched routes

Failures return a stable reason code such as `runtime-not-ready`, `routing-target-missing`, `routing-target-host-mismatch`, `routing-target-mismatch`, `routing-target-not-ready`, `route-pending`, or `check-error`. The activation plan retains the structured check and also copies the code plus reason into the `readiness-routing-check` step reason so deploy worker errors can flow back without requiring live Traefik/Docker integration in tests.

The readiness/routing gate must pass before any Traefik artifact is written. A candidate is safe to promote only when the runtime status belongs to the candidate, the routing target belongs to the same candidate and host when known, the target is marked ready, and any supplied route reconciliation summary is `ready` with no pending, stale, unknown, or mismatched routes.

The route promotion drill in `tools/phase0-gate` exercises this execution contract without Docker: it builds a deploy payload with route intent, passes it through a fake queue/job boundary, runs the executor against a fake ready runtime target, publishes through `TraefikRoutePromoter` with an in-memory publisher, verifies route apply evidence and support-safe step telemetry, and checks the operation transition sequence. It is a local contract check only; live Docker healthchecks, image pulls, filesystem publication, Traefik reloads, DNS, and HTTPS remain separate verification.

Docker routing target readiness is derived from Docker Engine evidence only; the worker does not perform a live network probe. `ResolveRoutingTarget` marks a target ready when inspect reports the container is running and the Docker health status is `healthy`, no healthcheck is present, or existing Docker status text explicitly reports a ready/healthy state. `starting` and `unhealthy` health states block readiness. The shared `RoutingTarget` shape only carries `Ready`; detailed Docker health reason text is retained on the adapter's `InstanceStatus` when the adapter observes it.

The live single-VM verification checklist for those separate concerns lives in [`live-deploy-proof.md`](live-deploy-proof.md). It defines the live deploy runbook, evidence expectations, failure boundaries, and non-goals for proving one Docker-backed deploy through public routing.
