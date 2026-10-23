# Readiness

How Midnight decides that a deploy candidate is ready to receive traffic, and
how it decides that waiting any longer is pointless.

## The gate

`internal/deploy/executor.go` runs one polling gate per replica between
`Start` and route promotion. Each attempt:

1. Re-resolves the candidate's routing target from the runtime adapter.
2. Re-evaluates `EvaluateActivationReadinessRouting` — runtime status,
   routing target identity, host agreement, and route reconciliation.
3. Runs the configured application probe (HTTP or TCP) against the
   container's own address.

Promotion requires all three to agree. A cancel request checked on every
attempt aborts the wait promptly.

## Which port gets probed

The platform default target port (8080) is a guess, not a choice. The
executor marks it as one with `runtime.ScheduleRequest.TargetPortInferred`,
and the Docker adapter's `resolveTargetPort` then prefers the port the image
declares for itself when it declares exactly one. That is why an unmodified
Redis image becomes ready on 6379 without anyone configuring a port.

An operator-chosen port is never second-guessed.

## Images with no port at all

A worker, a queue consumer, or a cron-shaped image declares no port and has
nothing listening. The default readiness kind is `tcp`, so before this
behavior such a service could never become ready: the probe dialed a port
nothing was bound to until the timeout elapsed.

For an immutable-image deploy, the executor asks the runtime what the image
declares (`runtime.ImagePortReporter`). When the image declares no port *and*
no human chose a port, health kind, health port, or HTTP path,
`serviceconfig.EffectiveHealthKind` resolves to `none`: the container running
and not exiting becomes the readiness signal, and the timeline says so.

This narrowing is deliberate. Any of the following keeps the configured
probe, because a service that has a port must still prove it is listening:

- the image declares a port;
- the operator set `target_port` to anything but the platform default;
- the operator set `health_port`, or a health kind other than `tcp`;
- the deploy is a source build (its port convention is the platform default,
  which the builder honors).

## Failing fast on a dead candidate

Waiting only pays off while the workload can still become ready. A container
that has already exited, or one the restart policy keeps resurrecting, never
will.

Adapters that can inspect a workload implement
`runtime.InstanceLivenessObserver`. On every failed probe attempt the
executor asks for that evidence, and a terminal answer ends the wait
immediately through `readiness.Fatal`:

| Evidence | Failure code |
|---|---|
| Container stopped and nothing is restarting it | `DEPLOY_CANDIDATE_EXITED` |
| Container restarted `restartLoopThreshold` times or more | `DEPLOY_CANDIDATE_RESTART_LOOP` |

The failure reason carries the exit code, the OOM/error detail, and a bounded
tail of the container's own last log lines, so the operation timeline and the
service card explain the crash rather than reporting a timeout.

An adapter that cannot answer reports `Observed: false`, and the wait falls
back to plain timeout behavior. "We could not look" is never read as "the
workload is dead".

Log lines are untrusted workload output. They are truncated per line and the
whole reason is capped (`maxReadinessFailureReasonBytes`).

## Timeouts

| Deploy | Default budget |
|---|---|
| Immutable image | `serviceconfig.DefaultImageHealthTimeout` (120s) |
| Source build | `serviceconfig.DefaultHealthTimeout` (300s) |

An image deploy compiles and installs nothing at rollout time, so a container
that has not answered in two minutes is far more likely broken than slow. A
source build may legitimately spend minutes on a cold cache.

Setting `health_timeout` on the service configuration overrides both. The
CLI's `midnight deploy image --port`, `--health-kind`, and `--health-path`
write the port and readiness intent to that configuration before the rollout
is enqueued, so the worker reads the values the operator just supplied.

## Progress

Readiness can legitimately take minutes. `readinessProgressReporter` emits a
throttled `waiting for readiness: <reason>` timeline event so a slow rollout
explains itself instead of leaving a silent gap.
