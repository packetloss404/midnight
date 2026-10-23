# Deployment Timeline

Timeline events record ingress and certificate progress. The default local router can keep them in memory for deterministic tests; `NewPostgresDataStack` wires `internal/timeline.PostgresStore` to the `timeline_events` table for durable control-plane boot.

## Durable Deploy Intake

The first durable deploy intake path is `POST /api/v1alpha/services/{id}/deployments`. An accepted request creates a queued `deploy` operation, binds the `Idempotency-Key` reservation to that operation, and enqueues a `deploy` queue job. The worker trace key is the operation id: it is returned in the API response, recorded on the idempotency reservation, persisted on `jobs.parent_operation_id`, included in the queue payload as `operation_id` for older workers, and echoed in the operation event stream when the job is enqueued.

Migration `0009_queue_parent_operation_id` adds the soft `jobs.parent_operation_id` link and backfills it from `jobs.operation_id` or payload `operation_id` when either value is present. Queue callers should set `EnqueueRequest.ParentOperationID` directly; `PostgresStore.Enqueue` also preserves the current deploy intake compatibility path by copying payload `operation_id` into the column when the explicit field is empty. The service project id remains the recommended `partition_key` for queue fairness.

Current execution boundary: `midnight-worker serve` owns durable queue boot, migrations, expired lease reclaim, and the deploy executor loop for `queue=deploy`. The executor decodes payloads with `schema_version=midnight.dev/deploy-job/v1alpha1`, uses the payload `operation_id` as the trace key, transitions the linked operation from queued to running, and then records a terminal succeeded or failed operation state. The execution path is:

1. Decode deploy payload into a Docker/runtime `ScheduleRequest`.
2. Pull and verify the candidate image before scheduling.
3. Schedule and start the candidate runtime instance.
4. Resolve the runtime `RoutingTarget`.
5. Run the readiness/routing gate.
6. Promote the target by publishing Traefik file-provider dynamic config.
7. Drain the previous active instance only after promotion succeeds.
8. Append operation events for state transitions and failures.

The executor also emits best-effort deploy step telemetry through `internal/deploy.DeployStepObserver`. Step events use the stable steps `pull-verify`, `schedule`, `start`, `readiness-routing-check`, `promote`, `drain-previous`, `succeeded`, and `failed`, with `started`, `succeeded`, or `failed` state. The worker persists those as operation events so the dashboard timeline can show live progress from `GET /api/v1alpha/operations/{id}/events` or the SSE stream without parsing logs.

The worker must keep operation events and step telemetry support-safe: event messages may describe the phase and failure reason, but must not include rendered route YAML, tenant secrets, raw provider credentials, Docker socket paths, or full filesystem paths. Queue completion is separate from operation success; a failed executor marks the queue job failed and also marks the linked operation failed with the mapped failure code.

Local/noop fallback: when the worker is wired with `internal/runtime/local.Adapter` and `deploy.NoopRoutePromoter`, it records deterministic runtime intents and never writes Traefik files. This mode is for local development, tests, and dry-run diagnostics. It does not prove live Docker scheduling, live image signature verification, Traefik reload behavior, DNS, or HTTPS.

Real routing mode requires `--runtime-backend=docker` or `MIDNIGHT_WORKER_RUNTIME_BACKEND=docker`, Docker Engine access via `--docker-host` or `MIDNIGHT_WORKER_DOCKER_HOST` / `DOCKER_HOST`, Postgres DSN configuration (`MIDNIGHT_DB_DSN`, `MIDNIGHT_DATABASE_URL`, `MIDNIGHT_DB_DSN_FILE`, or the matching `--postgres-dsn` / `--postgres-dsn-file` flags), and route publication configured with `--route-config-dir` / `MIDNIGHT_WORKER_ROUTE_CONFIG_DIR`. The route config directory must exist, be absolute, and be writable by the `midnight` service user; the shipped Traefik contract uses `/etc/midnight/traefik/dynamic`. Route metadata comes from worker flags/env: `--route-group`, `--route-public-host`, `--route-public-path`, `--route-public-tls`, `--route-cert-resolver`, and `--route-target-port` plus their `MIDNIGHT_WORKER_ROUTE_*` equivalents. Image trust must also be configured: either `--verify-signatures` with `--cosign-binary` and a key-pair or keyless cosign policy, or a repeatable `--trusted-image` static allowlist for acceptance-only immutable image refs.

Until older workers are retired, deploy intake should continue to include payload `operation_id` even when it also sets `EnqueueRequest.ParentOperationID` directly.

| Event | Meaning |
|---|---|
| `ingress.route.rendered` | A Traefik file-provider route was rendered. |
| `ingress.route.artifact.written` | A route group `traefik.dynamic.yaml` file-provider artifact was written atomically. |
| `acme.dns.pending` | DNS-01 challenge data requires publication. |
| `acme.certificate.ready` | A certificate is available to bind. |
| `ingress.route.active` | The ingress route is expected to be serving traffic. |
| `deploy.worker.running` | The deploy executor transitioned the linked operation to running. |
| `deploy.worker.failed` | The deploy executor failed before queue completion; the operation carries the mapped failure code. |
| `deploy.worker.succeeded` | The deploy executor completed promotion and any required previous-instance drain. |

Route activation decisions can use `internal/router.EvaluateRouteTargetFreshness` to distinguish fresh, stale, and unknown target observations before declaring a route active.

Route reconciliation results also include `internal/router.RouteReconciliationSummary` for status UI consumers. The summary reports the artifact hash and route counts without exposing rendered configuration content, and can be rendered as a compact UI/CLI status string.

Route publication uses a pure prewrite plan followed by an injected writer. The local writer requires an absolute publication root and writes only sanitized, relative route group artifacts beneath that root. `internal/router.ApplyRouteArtifactPublication` emits support-safe apply evidence for the write attempt: status, counts, bytes written, and diagnostic codes only.

Ingress route release evidence starts with `internal/acme.RoutePublicationEvidencePacket`, which combines prewrite decisions, publication summary, ingress diagnostics review, and heartbeat review. `internal/acme.IngressRouteReleaseEvidenceIndex` then references route publication evidence, ACME diagnostics, ingress diagnostics review, deployment heartbeat review, logs, and timeline files using normalized relative paths.

Ingress route release evidence indexes can keep optional SHA256 metadata on referenced evidence files. Metadata validation does not touch the filesystem; callers can run `internal/acme.VerifyIngressRouteReleaseEvidenceIndexFiles` when release evidence files are available locally to check referenced file existence and validate hashes for refs that include SHA256 metadata.

`internal/acme.IngressEvidenceBundle` groups route publication evidence, the evidence index, and apply evidence into one support-safe decision record with a rolled-up status.

DNS-01 instructions, DNS-provider interfaces, CAA preflight models, and
certificate readiness events remain side-effect-free contracts. When the
worker's explicit ACME configuration is enabled, `midnight-worker` constructs
the Lego issuer and domain lifecycle controller, resolves ownership TXT
records, persists certificate-order evidence, and retries pending work. The
default is disabled and staging; production requires an explicit
acknowledgement plus provider credentials. The normal unit-test path still
does not perform DNS writes, ACME account/order calls, or certificate
issuance, and installed/public HTTPS remains an external Traefik/DNS proof
gate.

Deployment job heartbeat gaps can be reviewed with `internal/timeline.ReviewDeploymentHeartbeatGap`. The pure reviewer returns JSON-friendly `ok`, `warning`, or `stuck` status values, gap and threshold seconds, deterministic issue codes, and support-safe diagnostics. Gaps greater than 180 seconds are classified as `stuck`; the default warning threshold is 90 seconds.

Queue heartbeat observability is split between the queue lease and the release evidence path. `queue.HeartbeatLease` / `PostgresStore.Heartbeat` update `jobs.heartbeat_at` while the worker holds a lease; `ReviewDeploymentHeartbeatGap` turns the last observed heartbeat into release-gate evidence and the `DeployJobStuck` alert uses `midnight_deploy_job_heartbeat_gap_seconds > 180`. A fresh dashboard timeline should therefore show both operation step progress and a non-stuck queue heartbeat review for the same operation.

The local gate includes a Docker-free deploy route promotion drill in `tools/phase0-gate`: it enqueues a deploy payload with route intent, runs `internal/deploy.Executor` against a fake runtime target, publishes through `deploy.TraefikRoutePromoter` with an in-memory route publisher, checks route apply evidence, and expects operation transitions from `RUNNING` to `SUCCEEDED`. This proves the local contract and telemetry wiring, not live Docker scheduling, Traefik reload behavior, DNS, ACME, or HTTPS.
