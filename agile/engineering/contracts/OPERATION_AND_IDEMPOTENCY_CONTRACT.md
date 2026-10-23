# Operation & Idempotency Contract — `/api/v1alpha`

This document is the panel-locked contract for the **Operation/Job/Idempotency triad** as it appears on `/api/v1alpha`. It is normative for every mutating endpoint, every list endpoint, and every long-running action surfaced by the API.

Source spec sections (line numbers refer to `D:/projects/midnight/midnight-platform-plan.md`):
- "Eight-Specialist Panel Locked Decisions" -> Distributed systems / control-plane correctness (L102-L112).
- "Eight-Specialist Panel Locked Decisions" -> API/IaC/DX (L172-L173, L178-L180).
- "API, Automation, And IaC Contract" (L611-L621): Operation/Job/Deployment/IdempotencyKey semantics.
- "Deployment State Machine" (L693-L781).
- Sprint 3 stories MIDNIGHT-S03-005, S03-006, S03-007.

---

## 1. Operation lifecycle

### 1.1 Definitions

- **Operation** is the public API-facing async promise. Returned by every mutating async action with `202 Accepted`. Records: `id`, `kind`, `state`, `target`, `actor`, `idempotency_key`, `progress`, `result`, `error`, `request_id`, `created_at`, `updated_at`, `expires_at`. Survives until `expires_at`.
- **Job** is the internal durable worker execution record. Owns leases, retries, worker ownership, attempt count, heartbeat, timeout, cancellation, and dead-letter state. Many-to-one with Operation; the Operation aggregates child Job state.
- **Deployment** is the domain resource representing a deploy attempt with an immutable snapshot. Survives after the Operation/Job rows expire.
- **IdempotencyKey** de-duplicates requests by
  `(server_actor, canonical_tenant, method, path_template, client_key)` and
  request body hash. Each attempt owns an immutable generation fence plus the
  stored response or operation id.

### 1.2 Public state machine

```
            +---------+
            | PENDING |   reservation accepted, work not yet picked
            +---------+
                 |
                 v
            +---------+
            | RUNNING |   worker holds a lease and is making progress
            +---------+
                 |
       +---------+---------+
       v         v         v
+-----------+ +--------+ +----------+
| SUCCEEDED | | FAILED | | CANCELED |
+-----------+ +--------+ +----------+
       (terminal)  (terminal)  (terminal)
```

Terminal states are absorbing. There is no transition out of `SUCCEEDED`/`FAILED`/`CANCELED`.

`CANCELED` may be reached from `RUNNING` via a cooperative cancel at a named safe point (panel: cancellation between route-add and drain-completion is rejected with `CANCEL_NOT_SAFE`; only rollback is permitted in that window). `CANCELED` may also be reached from `PENDING` if the queueing layer drops the reservation before any work runs.

### 1.3 TTL policy

- `operation.expires_at = created_at + 24h` (Phase 0 default; Phase 1+ tunable per workspace, never below 1h).
- `idempotency_key.expires_at >= operation.expires_at` (panel-locked invariant; enforced by FK trigger and a CI invariant test).
- Operation GC is **forbidden** before key GC. The reaper joins both tables and deletes Operation rows only after their associated idempotency key row has reached its TTL.
- Terminal-state operations remain queryable until `expires_at`. After expiry the Deployment/Domain/etc. domain resource remains; the Operation row does not.

---

## 2. Operation polling

```
GET /api/v1alpha/operations/{id}
```

Response 200 includes the full Operation in the body and these response headers:

| Header | Meaning |
|---|---|
| `Retry-After` | Integer seconds. Mandatory for non-terminal states. Always integer per panel decision. |
| `X-Adaptive-Backoff-Hint-Seconds` | Server-suggested next-poll delay in seconds. Adapts from `1s -> 30s` based on age + observed progress. |
| `X-Request-Id` | Always equal to body `request_id`. |

Adaptive backoff (server-side):

```
hint = clamp(min(30, max(1, age_seconds / 4)), 1, 30)
```

Clients SHOULD honor `X-Adaptive-Backoff-Hint-Seconds`. Clients MUST honor `Retry-After`.

Polling examples:

| Operation age | Hint | Behavior |
|---|---|---|
| Just created (< 4s) | 1 | Tight loop OK. |
| 30s in | ~7 | One poll every 7s. |
| 2 min in | ~30 | One poll every 30s. |
| Terminal | absent | Stop polling. |

---

## 3. Operation streaming (SSE)

Default streaming surface, panel-locked:

```
GET /api/v1alpha/operations/{id}/events
Accept: text/event-stream
Last-Event-ID: <optional reconnect anchor>
```

Frame shape:

```
id: 142
event: state
data: {"state":"RUNNING","at":"2026-04-25T12:00:30Z"}

id: 143
event: progress
data: {"phase":"BUILDING","percent":35}

id: 144
event: progress
data: {"phase":"DEPLOYING","percent":80}

id: 145
event: done
data: {"state":"SUCCEEDED","result":{"deployment_id":"dep_..."}}
```

Reconnect semantics:

- Client reconnects with `Last-Event-ID: 143`. The server replays events strictly after id 143 and continues live.
- Server retains at least the last 1000 frames OR the last 1 hour of frames per Operation, whichever is greater.
- Frames are monotonic, gap-free integers per Operation. Gaps imply a bug.

WebSocket alternate (`/v1alpha/ws`) is documented for log/exec multiplexing only; it is not the streaming default for Operations.

---

## 4. Idempotency keys

### 4.1 Scope and TTL

- Header: `Idempotency-Key: <client-generated UUID or opaque token>`.
- Required on every `POST/PATCH/DELETE`. Missing key returns `400 IDEMPOTENCY_KEY_REQUIRED`.
- Scope:
  **`(server_actor, canonical_tenant, method, path_template, client_key)`**.
  The actor and tenant are derived from verified server state, never from the
  bearer value or a caller-selected header. Sessions for one user share an
  actor; different actors, tenants, methods, or route templates are
  independent.
- TTL: **24 hours** from first reservation.
- Body hash: **SHA-256 over RFC 8785 canonical JSON** (deterministic key ordering, deterministic number formatting). The hash is stored alongside the key.
- Attempt fence: every reservation has a cryptographically random
  `generation`. Attach/complete calls compare actor, tenant, method, path,
  client key, body hash, generation, expected state, and unexpired TTL.

### 4.2 Replay header

When the server detects a replay (same key, same scope, same body hash), the response is the **byte-identical** stored response with the additional response header:

```
Idempotent-Replayed: true
```

This applies to `2xx`, `4xx`, and `5xx` results — once a result is stored, it is replayed verbatim.

### 4.3 Required relation to Operation TTL

Panel-locked invariant:

```
idempotency_key.expires_at >= operation.expires_at
```

Operation GC is forbidden before key GC. Enforced by:

- A FK with `ON DELETE RESTRICT` from `idempotency_keys.operation_id -> operations.id`.
- A CI test (`internal/idempotency/test_invariants_test.go`) that reaps and asserts no orphaned key exists with a missing operation.

---

## 5. Idempotency state matrix

The panel-locked state matrix MUST be implemented exactly. Rows = first-request state; columns = same-body / different-body.

| First-request state | Same body | Different body |
|---|---|---|
| **Pending** (key reserved, request still in-flight) | `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with `Retry-After: 1`. The original request is still authoritative. | `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with `Retry-After: 1`. The body hash does not match yet, but the in-progress lock takes precedence. After the original completes, repeat with the original body to replay; or use a fresh key. |
| **Succeeded** (terminal 2xx stored) | Replay the stored response **verbatim**, add `Idempotent-Replayed: true`. | `409 IDEMPOTENCY_KEY_REUSED`. Stored response is not exposed. `details.first_request_body_hash` and `details.replay_request_body_hash` are returned. |
| **Failed-terminal** (terminal 4xx/5xx stored) | Replay the stored response **verbatim** (including 4xx/5xx body), add `Idempotent-Replayed: true`. | `409 IDEMPOTENCY_KEY_REUSED`. |
| **Key reserved, no body store** (rare race; reservation row exists, body+response not yet flushed) | Server waits up to `idempotency_reservation_grace_ms` (default 250ms) for the body store. If still empty, returns `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with `Retry-After: 1`. | Same as same-body during grace. After grace, `409 IDEMPOTENCY_REQUEST_IN_PROGRESS`. |
| **Expired** (TTL elapsed; attempt retained as `EXPIRED` history) | Treat as a fresh request: reserve a new generation and run the action. | Same: fresh request. A late holder of the prior generation is fenced. |

CI fixtures: each cell has at least one Schemathesis stateful property test (Sprint 3 MIDNIGHT-S03-005 acceptance criterion).

### 5.1 In-flight duplicate handling

When two concurrent requests arrive with the same
`(server_actor, canonical_tenant, method, path_template, client_key)`:

1. Both acquire the same transaction-scoped advisory scope lock.
2. The winner inserts the only active generation and runs the handler.
3. The loser observes the active reservation. It returns:

```json
HTTP/1.1 409 Conflict
Retry-After: 1
X-Request-Id: req_01HZXLOSER...

{
  "code": "IDEMPOTENCY_REQUEST_IN_PROGRESS",
  "message": "An identical request is currently being processed.",
  "request_id": "req_01HZXLOSER...",
  "docs_url": "https://midnight.dev/docs/error-codes#idempotency_request_in_progress",
  "details": { "wait_ms_hint": 1000 }
}
```

The loser SHOULD retry after `Retry-After`; on retry it will hit the **Succeeded** or **Failed-terminal** row of the state matrix.

---

## 6. Optimistic concurrency

### 6.1 `If-Match` header

High-risk updates accept and require:

```
If-Match: <resource_version>
```

`resource_version` is an opaque monotonic token returned in:

- The resource's `resource_version` JSON field.
- The `ETag` response header on `GET` of the same resource.

On mismatch, the server returns:

```json
HTTP/1.1 412 Precondition Failed
X-Request-Id: req_...

{
  "code": "RESOURCE_VERSION_MISMATCH",
  "message": "Resource version supplied in If-Match no longer matches.",
  "request_id": "req_...",
  "docs_url": "https://midnight.dev/docs/error-codes#resource_version_mismatch",
  "details": { "expected": "rv_01HZXOLD", "actual": "rv_01HZXNEW" }
}
```

### 6.2 Endpoints requiring `If-Match`

- `POST /services/{id}/deploy` — `If-Match` on the service's resource_version when the spec is being mutated transactionally (e.g. via `applyChangeSet`-equivalent payload).
- `POST /deployments/{id}/rollback` — `If-Match` on the service's resource_version.
- `PATCH /variables/{id}` — `If-Match` on the variable's resource_version.
- All v1alpha `applyChangeSet`-shaped endpoints (Phase 1+) MUST require `If-Match` on each member resource.

### 6.3 `applyChangeSet` atomicity

Panel-locked: all `If-Match` checks are evaluated together. The first conflict returns `409` with the conflicting resource list and **nothing applied**. Side effects (deploys, DNS, ACME) enqueue via the transactional outbox pattern within the same transaction.

---

## 7. Cursor pagination

### 7.1 Format

- Opaque base64url string.
- Server-signed (HMAC) over `(resource_kind, last_id, last_sort_field, sig_key_id, expiry)`.
- Tampering returns `400 INVALID_CURSOR`.
- Cursors stable for at least **1 hour**. Older cursors return `400 INVALID_CURSOR` (same code; clients restart iteration without a cursor).
- Forward-only in v1alpha. Backward iteration is not in scope.

### 7.2 Limits

- `limit` query parameter. Default **50**. Hard cap **200**.
- Above 200 -> clamped to 200, response includes:
  ```
  Warning: 299 - "limit clamped to 200 (from 500)"
  ```
- No `total_count`. Ever. CI lint forbids `total_count` in OpenAPI.

### 7.3 End signal

- Last page returns `next_cursor: null`.
- All endpoints use the shape:
  ```json
  {
    "data": [ ... ],
    "page": { "next_cursor": null }
  }
  ```

### 7.4 Signing-key rotation

Cursor signing keys rotate on a 90-day cadence with a 7-day overlap. During overlap, both keys validate. After overlap expires, cursors signed with the old key return `400 INVALID_CURSOR`.

---

## 8. Rate-limit headers and budgets

Panel-locked default budgets (units = requests / minute):

| Class | Budget |
|---|---|
| user | 600 |
| service-account | 1200 |
| cli | 600 |
| mcp | 300 |
| webhook receiver | 6000 |
| Git provider | 1200 |
| agent | 12000 |

Two-tier enforcement: `min(per-tenant, per-source-IP)`. Both must be under budget for the request to proceed.

Headers (always present on responses subject to a rate-limit class):

| Header | Meaning |
|---|---|
| `RateLimit-Limit` | Window budget for the actor's class. |
| `RateLimit-Remaining` | Remaining budget in the current window. |
| `RateLimit-Reset` | Unix seconds at which the window resets. |
| `Retry-After` | On `429`, integer seconds until next allowed attempt. |

`429` response code is always `RATE_LIMITED`.

---

## 9. State summary (panel quick reference)

| Concern | Value |
|---|---|
| `Idempotency-Key` scope | `(token_id, method, path_template)` |
| `Idempotency-Key` TTL | 24 h |
| Body hash | SHA-256 over RFC 8785 canonical JSON |
| `idempotency_key.expires_at` | `>= operation.expires_at` (FK-enforced) |
| Replay marker | Response header `Idempotent-Replayed: true` |
| In-flight duplicate | `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with `Retry-After` |
| Different body, same key | `409 IDEMPOTENCY_KEY_REUSED` |
| Optimistic concurrency | `If-Match: <resource_version>` -> `412 RESOURCE_VERSION_MISMATCH` |
| Async accepted | `202 Accepted` + `Location: /api/v1alpha/operations/{op_id}` + Operation in body |
| Polling backoff | `Retry-After` integer seconds + `X-Adaptive-Backoff-Hint-Seconds` (1..30) |
| Streaming default | SSE at `/operations/{id}/events`; reconnect with `Last-Event-ID` |
| WebSocket | Documented at `/v1alpha/ws`; log/exec multiplexing only |
| Cursor format | base64url, server-signed HMAC; tamper -> `400 INVALID_CURSOR` |
| Cursor stability | >= 1 hour |
| Page limit | default 50, max 200, no `total_count` |
| Rate-limit headers | `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`; `Retry-After` integer seconds |
| Public deployment states | `QUEUED`, `BUILDING`, `DEPLOYING`, `ACTIVE`, `SUPERSEDED`, `FAILED`, `CANCELING`, `CANCELED`, `REMOVING`, `REMOVED` |
| Cooperative cancel window | Forbidden between route-add (step 6) and drain-completion (step 9); -> `CANCEL_NOT_SAFE` |
| Promotion guard | `UPDATE deployments SET state='ACTIVE' WHERE id=$id AND state='DEPLOYING' AND resource_version=$rv` |
| Single-flight per (service, env) | `pg_advisory_xact_lock(hashtext('svc:'||service_id||':'||env_id))` |
