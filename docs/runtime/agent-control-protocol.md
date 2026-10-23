# Agent Control Protocol

The agent control-plane contract is request construction plus supervised execution boundaries. The protocol package builds HTTP requests. The supervisor loop may execute those requests through an injected executor, or record dry-run intents without network I/O.

## Agent Loop Executor

The runtime loop is split into three boundaries:

- Planner: chooses `register`, `heartbeat`, `fetch_desired`, `backoff`, or `noop` from current registration, heartbeat, desired-state, retry, and local maintenance/drain state.
- Executor: performs only injected side-effecting actions for `register`, `heartbeat`, and `fetch_desired`.
- Artifact builder: records action outcomes, next delay, and issue codes into deterministic JSON-friendly evidence.

`backoff` and `noop` never call the executor. They only carry planned delays into the loop execution artifact.

When local maintenance or drain mode is active, the planner still prioritizes registration and due heartbeats, but suppresses desired-state polling by returning `noop` with reason `agent is in maintenance mode`. This keeps liveness visible while preventing new desired work from being accepted during operator maintenance.

## Dry-Run REST Executor

The REST loop executor has a dry-run mode for validation. In dry-run mode it still constructs the HTTP request with the same request builder used for live calls, but it records only request metadata:

- action
- method
- escaped path
- agent ID header
- desired-state ETag header when present

Dry-run mode does not call the protocol executor and does not send HTTP requests. Request bodies and authorization headers are not copied into the dry-run intent record.

## Evidence Packet And Index

Each pass can emit two release-level evidence shapes:

- `AgentReleaseEvidencePacket`: combines decision review, loop execution review, heartbeat freshness, reconcile counts, and runtime-route readiness into one release status and issue rollup.
- `AgentEvidenceIndex`: lists the release evidence packet, loop execution artifact, decision artifact, and optional runtime observation evidence using workspace-relative slash-separated paths.

Evidence builders are deterministic and side-effect free. File helpers only read or write explicit `.json` paths and strictly reject unknown JSON fields, trailing values, unsafe relative paths, and missing parent directories.

## Multi-Host Compatibility

The dry-run executor may be exercised on one VM, but the protocol must remain host-scoped. `agent_id`, `hostname`, runtime, heartbeat sequence, maintenance/drain flags, and desired-state ETags are data carried by each agent, not process-global state. Future multi-host controllers must be able to register multiple agents from different hosts, compare their independent heartbeat freshness, and attach runtime-route evidence per host without changing the request envelope.

Evidence indexes must continue to use workspace-relative slash-separated paths. They should not record absolute host paths, Docker socket paths, or assumptions that the control plane and runtime adapter share a filesystem. Host-specific details belong inside evidence payload fields, where multi-host aggregation can reason about them explicitly.

## Heartbeat Freshness

Heartbeat freshness is classified from the last successful heartbeat age:

- `fresh`: age is within the configured freshness window.
- `stale`: age is outside the freshness window, negative, or exactly at the lost threshold.
- `lost`: age is greater than the lost threshold.

The lost threshold is 15 seconds. Stale heartbeats require review; lost heartbeats block release readiness.

## Docker Boundary

Docker support in this protocol package is intentionally non-integrated. The agent records deterministic Docker metadata such as the minimum Engine API version, container names, and ownership labels. It does not import Docker SDKs, create Docker clients, open Docker sockets, start containers, stop containers, or mutate local Docker state.

## Common Headers

All requests include:

- `Accept: application/json`
- `X-Midnight-Agent-ID: <agent-id>` when an agent ID is known
- `Authorization: Bearer <token>` when a token is configured
- `User-Agent: <agent-user-agent>` when configured

JSON requests also include `Content-Type: application/json`.

## Registration

- Method: `POST`
- Path: `/api/v1/agents/register`
- Body: `RegistrationRequest`

```json
{
  "agent_id": "agent-a",
  "version": "0.0.1",
  "hostname": "host-a",
  "os": "linux",
  "arch": "amd64",
  "labels": {
    "pool": "default"
  },
  "timestamp": "2026-04-25T00:00:00Z"
}
```

## Heartbeat

- Method: `POST`
- Path: `/api/v1/agents/{agent_id}/heartbeat`
- Body: `HeartbeatRequest`

The path agent ID is URL path escaped. Heartbeat sequence numbers are monotonically increasing from `1`. The `hmac` field is currently a deterministic HMAC-SHA256 over the trimmed agent ID and sequence number separated by a newline.

```json
{
  "agent_id": "agent-a",
  "sequence": 7,
  "status": "ok",
  "runtime": "docker",
  "sent_at": "2026-04-25T00:00:00Z",
  "hmac": "<hex-hmac>"
}
```

Maintenance heartbeats set `maintenance_mode: true` and use status `maintenance` unless the caller supplied a more specific status. Drain mode implies maintenance mode, sets `drain_mode: true`, and uses status `draining`. The fields are omitted from normal heartbeats to preserve the existing payload shape.

Heartbeat retry delay uses capped exponential backoff. The first retry uses the base delay, subsequent retries double until capped by the max delay. Optional jitter is deterministic when callers inject a `RandomSource`.

## Desired State

- Method: `GET`
- Path: `/api/v1/agents/{agent_id}/desired-state`
- Body: none

When the agent has a previously accepted desired-state ETag, it sends `If-None-Match: <etag>`. A future slice will define the concrete desired-state schema and response handling.
