# ADR-0030: Durable scoped observability on the single-host profile

- Status: accepted
- Date: 2026-07-27

## Context

Operation events and an optional file reader could make parts of the dashboard
look like logs, but they did not provide a production evidence path. Runtime
stdout/stderr, resource samples, and Traefik requests were not collected.
Historical metrics were placeholders, cursors could describe host storage, and
redacting only after a search would turn the search endpoint into a
secret-presence oracle.

The single-host profile should not require Loki, Prometheus, or ClickHouse to
make a first deployment diagnosable.

## Decision

Migration 0028 adds bounded PostgreSQL storage for:

- build, deploy/platform, Docker runtime, and Traefik HTTP records;
- per-source ingestion windows and explicit accepted/drop counts;
- collector cursors and health heartbeats;
- Docker CPU, memory, network, block-I/O, writable-layer, and restart samples;
- deployment markers.

Every record has project scope. Environment, service, deployment, operation,
and replica scope are included where available. API validation resolves the
entire graph before persistence, and composite foreign keys prevent a new
record from mixing resources from different projects.

Records are normalized, bounded, and redacted before the database insert and
therefore before searchable text exists. The default per-source limit is 6,000
records per minute. Excess records increment a durable drop counter surfaced
by REST, CLI, and the log viewer. Default retention is 30 days and cleanup runs
at server boot and daily for records, samples, ingestion windows, inactive
collector heartbeats, and abandoned collector checkpoints.

The deploy worker discovers every Midnight-labeled Docker container on startup
and periodically thereafter. It reads multiplexed stdout/stderr with a durable
timestamp checkpoint, relies on a source/sequence uniqueness key for inclusive
Docker replay, and samples the Docker stats and inspect APIs. The build worker
and deploy-step observer mirror their already-persisted scoped events into the
same store.

Traefik writes JSON access logs to
`/var/log/midnight/traefik/access.jsonl`. The server tails completed lines,
persists an inode/offset cursor, survives truncation and rotation, and resolves
host plus longest path prefix against a ready durable route before accepting a
tenant record. Requests for unmatched hosts or Traefik's own dashboard are not
tenant data and are skipped. Malformed JSON leaves the cursor in place and
marks the collector degraded.

Authenticated REST endpoints provide scoped history, filtered search, metrics,
and SSE live tail. Clients receive only authenticated encrypted cursor tokens.
SSE IDs use those tokens, and `Last-Event-ID` replays committed database rows
after disconnect or API restart. Metric aggregation uses PostgreSQL
`date_bin`, supports summed and per-replica modes, and is bounded to 720
points.

The production GUI and `midnight observability` CLI use these endpoints.
Collector health is aggregated by kind so collector IDs and raw driver errors
remain private. Missing and stale Docker or Traefik heartbeats are explicit
gaps in REST, CLI, dashboard, and SSE responses; unavailable collectors and
missing samples are never displayed as zero or synthetic data. Human-session
queries authorize the requested project before durable scope validation and
conceal cross-project misses. The dashboard uses operation events only for its
lifecycle phase stepper and loads its terminal from the durable operation-log
adapter. The Railway-shaped `midnight logs -f` command resolves the latest
canonical deployment and follows the same durable SSE stream rather than
substituting operation lifecycle events for application output.

## Evidence

Integration tests run disposable PostgreSQL, real Midnight-labeled Docker
workloads, and Traefik v3.1.5 with a real proxied request. They prove:

- pre-persistence redaction, rate drops, pagination, retention-shaped queries,
  process-restart durability, and database-side metric aggregation;
- real Docker stdout/stderr and stats plus restart-safe deduplication;
- real Traefik JSON output, durable route scope resolution, file checkpoint
  replay, and restart-safe deduplication;
- authenticated opaque SSE IDs, topology-safe collector-health frames, durable
  `Last-Event-ID` replay, and tenant-concealed observability reads;
- dashboard coverage proving operation events cannot populate the durable log
  terminal.

With `MIDNIGHT_PARITY_OBSERVABILITY_RUNNER` configured,
`scripts/railway-parity-loop.sh observability` reruns both the built-in
PostgreSQL/Docker/Traefik evidence and the deliberately separate real
notification-delivery origin. The exact built-in integration command above
also runs independently when that external runner is unavailable.

## Deliberate boundaries

- PostgreSQL is the only built-in backend for this profile. Larger
  installations can add another `observability.Store` implementation later.
- There is no local write-ahead spool for build/platform records during a
  PostgreSQL outage. Docker and Traefik retain their source data and resume
  from checkpoints; control-plane mirroring remains best-effort until a
  transactional telemetry outbox lands.
- Metrics expose cumulative network and block-I/O counters. Rate conversion is
  a presentation follow-up.
- Typed runtime lifecycle events, built-in monitor evaluation, notification
  delivery, and long-term rollups are separate Gate 4 dependencies and are not
  claimed by this decision.
