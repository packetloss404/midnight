# Webhook & Event Contract — `/api/v1alpha`

This document is the panel-locked contract for outbound webhooks, inbound webhook ingestion, the CloudEvents binding, the event catalog, delivery semantics, replay, and the dead-letter pipeline. It is normative for `/api/v1alpha` and forward-compatible with `/api/v1`.

Source spec sections (line numbers refer to `D:/projects/midnight/midnight-platform-plan.md`):
- "Eight-Specialist Panel Locked Decisions" -> Security/secrets/trust boundary (L125): outbound webhook signing.
- "Eight-Specialist Panel Locked Decisions" -> API/IaC/DX (L177): CloudEvents binding pinned.
- "API, Automation, And IaC Contract" (L672-L676): event and webhook contract.
- "Operations Automation And Remediation" -> Webhook ingestion rules (L1519-L1527).
- Sprint 10 stories WH-1007, WH-1008, WH-1009, WH-1010.

---

## 1. Outbound webhook signing

### 1.1 Signature header

Every outbound delivery includes:

```
Midnight-Signature: t=<unix>,v2=<hex>,kid=<key_id>
```

- `t` — unix epoch seconds at the moment of signing. Integer.
- `v2` — lowercase hex HMAC-SHA256 of the delivery-bound canonical input. Multiple `v2=` segments MAY appear during rotation overlap (see 1.3).
- `kid` — the public key id of the active signing key.

Canonical outbound signing input:

```python
canonical = f"{delivery_id}.{timestamp}.{raw_body}"
v2 = hmac_sha256(signing_key, canonical).hex()
```

`delivery_id` is the exact `Midnight-Delivery-Id` header and cannot contain a
full stop or control/whitespace character. The `raw_body` is the exact bytes
sent on the wire — **no whitespace stripping, no JSON canonicalization, no
header normalization** beyond what was sent. Subscribers MUST use a
constant-time comparison. The legacy v1 input, `timestamp.raw_body`, remains
accepted only by inbound Midnight-generic verification during migration.

### 1.2 Replay protection

- Replay window: **300 seconds** (subscriber MUST reject `|now - t| > 300s`).
- Subscribers SHOULD additionally dedupe by `Midnight-Delivery-Id`.
- Server clocks are mandatory: the Midnight control plane refuses to start if `chrony`/`systemd-timesyncd` is not running. Senders never sign with a stale `t`.

### 1.3 Two-key rotation overlap

Each subscriber endpoint has up to two simultaneously valid signing keys with **30-day** rotation overlap. During overlap, Midnight emits both signatures in the same header:

```
Midnight-Signature: t=1745596800,v2=<old_hex>,v2=<new_hex>,kid=<active_kid>
```

The `kid` always references the active (preferred) key. Subscribers MUST iterate both `v2` segments and accept either match. This permits subscribers to fail over without dropping deliveries.

Rotation is initiated via:

```
POST /api/v1alpha/webhook-endpoints/{id}/rotate-key
```

which returns an Operation. Keys are stored as **sealed variables** (see Sprint 9 VAR-904); plaintext is never returned by the API.

### 1.4 Required outbound headers

| Header | Purpose |
|---|---|
| `Midnight-Signature` | HMAC envelope as above. |
| `Midnight-Event-Id` | Stable id of the *event* (idempotent across redeliveries). |
| `Midnight-Delivery-Id` | Stable id of *this* delivery attempt's record (`webhook_deliveries.id`). |
| `Midnight-Event-Type` | CloudEvents `type` (e.g. `dev.midnight.deployment.failed`). |
| `Content-Type` | `application/cloudevents+json` for the structured CloudEvents binding. |
| `User-Agent` | `Midnight-Webhooks/<server_version>` |
| `Traceparent` | W3C Trace Context propagated from the originating request. |

---

## 2. Inbound webhook verification

Midnight accepts inbound webhooks at:

```
POST /api/v1alpha/webhooks/inbound/{provider}
```

Supported providers in v1alpha: `github`, `gitlab-generic`, `bitbucket`, `midnight-incoming` (provider-neutral path used by Sprint 10 WH-1008 for non-GitHub CI).

### 2.1 GitHub

- Accepts `X-Hub-Signature-256: sha256=<hex>` (HMAC-SHA256 over raw body, GitHub's published format).
- Webhook secret stored as a **sealed variable**.
- Constant-time comparison.
- Replay deduped via `X-GitHub-Delivery` against `webhook_deliveries(provider='github', delivery_id=X-GitHub-Delivery)` unique index.
- Provider deadline target: respond 2xx within the GitHub `10s` median guidance (panel-locked target p95 < 200ms for verify -> dedupe -> enqueue -> 2xx).

### 2.2 Midnight-incoming (generic CI)

- Accepts the legacy `Midnight-Signature: t=,v1=,kid=` envelope, validated against the per-endpoint sealed webhook secret. Inbound v2 adoption requires the provider delivery identity to be passed into the delivery-bound verifier.
- Replay window 300s.
- Required body fields: `provider`, `repo`, `ref`, `sha`, `service_id`, `image_digest_or_dockerfile_path`.
- Auth: signed via WH-1007 contract.

### 2.3 Verification flow

```
+------------------+
| receive request  |
+------------------+
        |
        v
+----------------------------+
| 1. parse signature header  | -> 400 WEBHOOK_SIGNATURE_INVALID
+----------------------------+
        |
        v
+----------------------------+
| 2. constant-time compare   | -> 400 WEBHOOK_SIGNATURE_INVALID
+----------------------------+
        |
        v
+----------------------------+
| 3. replay window check     | -> 400 WEBHOOK_REPLAY_REJECTED
+----------------------------+
        |
        v
+--------------------------------+
| 4. dedupe by                   |
|    (provider, delivery_id,     |
|     event_hash) UNIQUE         |
+--------------------------------+
        |  hit                     miss
        v                            v
+----------------------+   +-------------------------+
| 200 + Idempotent-    |   | enqueue async work,     |
| Replayed: true       |   | 200 to provider <200ms  |
| (no enqueue)         |   +-------------------------+
+----------------------+
```

---

## 3. CloudEvents binding (panel-locked)

Every event — whether persisted to `events`, surfaced as an Operation `state` transition, or dispatched to a webhook endpoint — uses CloudEvents v1.0 with the following pinned bindings:

```json
{
  "specversion": "1.0",
  "id": "evt_01HZX9C6F7G8H9JKMNPQRSTVWX",
  "source": "//midnight/<instance_id>/projects/<prj_id>",
  "subject": "<kind>/<id>",
  "type": "dev.midnight.<resource>.<verb>",
  "datacontenttype": "application/json",
  "dataschema": "https://midnight.dev/schemas/events/<type>/<version>.json",
  "time": "2026-04-25T12:00:00Z",
  "traceparent": "00-<trace-id>-<span-id>-01",
  "midnightrequestid": "req_01HZXACCEPTED12345678ABCDE",
  "midnighttenantid": "wsp_01HZXWORKSPACEID0000000000",
  "data": { /* per-type payload */ }
}
```

Required CloudEvents extensions (panel-locked):

- `traceparent` — W3C Trace Context. Carried into all dispatched webhooks and events.
- `midnightrequestid` — the `request_id` correlated to API audit/operation events.
- `midnighttenantid` — the workspace id (tenant boundary).

`dataschema` URLs follow the `https://midnight.dev/schemas/events/<type>/<version>.json` convention. Each schema is checked into `agile/engineering/schemas/events/<type>/<version>.json` and served from the docs site.

The HTTP delivery uses the **structured** content mode (`Content-Type: application/cloudevents+json`); the entire envelope is the body. Binary mode is allowed for AsyncAPI consumers in v1 but not the default.

---

## 4. Event catalog (v1alpha-since)

Every event ships with a `since-version` and a `dataschema` URL. The catalog is generated from the same registry that emits AsyncAPI in v1.

| `type` | Since | Description |
|---|---|---|
| `dev.midnight.deployment.queued` | v1alpha | Deployment row created and accepted by control plane. |
| `dev.midnight.deployment.building` | v1alpha | Builder accepted the build; image production started. |
| `dev.midnight.deployment.deploying` | v1alpha | Image produced; instances being scheduled and started. |
| `dev.midnight.deployment.active` | v1alpha | Promotion guard committed `state='ACTIVE'`. |
| `dev.midnight.deployment.failed` | v1alpha | Terminal failure with `reason_code` from the registry. |
| `dev.midnight.deployment.superseded` | v1alpha | A newer deployment took over (`runLatest` or manual). |
| `dev.midnight.deployment.canceled` | v1alpha | Cancellation completed cleanly. |
| `dev.midnight.domain.verified` | v1alpha | TXT + routing verification both passed. |
| `dev.midnight.domain.tls_issued` | v1alpha | ACME certificate issued. |
| `dev.midnight.domain.ownership_lapsed` | v1alpha | Periodic re-verification failed; binding entered grace. |
| `dev.midnight.backup.succeeded` | v1alpha | Backup job completed; metadata persisted. |
| `dev.midnight.backup.failed` | v1alpha | Backup job failed; reason recorded. |
| `dev.midnight.audit.checkpoint` | v1alpha | Daily signed audit checkpoint written. |
| `dev.midnight.host.heartbeat_lost` | v1alpha | Agent heartbeat missing past threshold. |
| `dev.midnight.operation.completed` | v1alpha | Operation reached `SUCCEEDED`. |
| `dev.midnight.operation.failed` | v1alpha | Operation reached `FAILED`. |

Forward-link (Phase 1+, declared here for AsyncAPI generation):

- `dev.midnight.preview.created`, `dev.midnight.preview.deleted`
- `dev.midnight.policy.denied`
- `dev.midnight.quota.exceeded`

---

## 5. Delivery semantics

### 5.1 At-least-once + dedupe

- Outbound delivery is **at-least-once**. Subscribers MUST dedupe by `Midnight-Delivery-Id`.
- The `delivery_id` is also surfaced as the unique replay identifier in the dashboard and the replay endpoint.
- Same-event multiple deliveries SHARE `Midnight-Event-Id` but have distinct `Midnight-Delivery-Id`.

### 5.2 Retry schedule

Exponential backoff with **decorrelated jitter**:

| Attempt | Nominal delay (post-failure) | Notes |
|---|---|---|
| 1 | immediate | Initial dispatch. |
| 2 | 30s +/- 10s jitter | First retry. |
| 3 | 2 min +/- 30s jitter | |
| 4 | 10 min +/- 2 min jitter | |
| 5 | 1 h +/- 10 min jitter | |
| 6 | 6 h +/- 30 min jitter | |
| 7 | 24 h +/- 1 h jitter | Final attempt. |

After attempt 7, the delivery moves to `dead_letter_events` with full diagnostics (request, response, signature `kid`, latency, error). Token-bucket per-endpoint rate-limit (panel-locked: "webhook redelivery uses token-bucket per endpoint") prevents storms after a control-plane outage.

### 5.3 Endpoint disablement

Per Sprint 10 WH-1010 acceptance:

- After **50 cumulative failures** OR **5 consecutive failures within 5 minutes**, the endpoint transitions to `disabled` with `WEBHOOK_ENDPOINT_DISABLED`.
- Disabled endpoints reject inbound deliveries with `410 Gone` and skip outbound dispatch (deliveries enter `dead_letter_events` immediately).
- Operator re-enables via dashboard or `PATCH /webhook-endpoints/{id}` after fixing the receiver.

### 5.4 Response semantics

- A subscriber returning any 2xx within the 30-second per-attempt timeout is treated as success.
- 4xx (except 408, 425, 429) is treated as **terminal** for that delivery and skips remaining retries.
- 5xx, 408, 425, 429 trigger the next retry. 429 with `Retry-After` overrides the schedule for the next attempt.

---

## 6. Replay endpoint

```
POST /api/v1alpha/webhooks/{endpoint_id}/deliveries/{id}/replay
```

- Operator-initiated manual replay.
- Re-signs the body with the **currently active key** (so `kid` may differ from the original).
- Records a new `webhook_deliveries` row with `replay_of: <original_delivery_id>`.
- Idempotent against double-clicks via `Idempotency-Key`.
- Audit event emitted.

---

## 7. Sample payload — `dev.midnight.deployment.failed`

End-to-end HTTP request Midnight sends to a subscriber endpoint:

```http
POST /webhooks/midnight HTTP/1.1
Host: example.com
Content-Type: application/cloudevents+json
User-Agent: Midnight-Webhooks/0.1.0-alpha
Midnight-Signature: t=1745596800,v2=4d2f8c9b1a7e6f3d2c1b0a9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c,kid=whk_01HZXKEY01PQR2STU3VWX4YZ56
Midnight-Event-Id: evt_01HZX9C6F7G8H9JKMNPQRSTVWX
Midnight-Delivery-Id: whd_01HZXDELIVERYABCDE0123456789
Midnight-Event-Type: dev.midnight.deployment.failed
Traceparent: 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01

{
  "specversion": "1.0",
  "id": "evt_01HZX9C6F7G8H9JKMNPQRSTVWX",
  "source": "urn:midnight:midnight-prod-001:project:prj_01HZX5MZZZZZZZZZZ0123456BB",
  "subject": "deployment/dep_01HZX9C6F7G8H9JKMNPQRSTVWX",
  "type": "dev.midnight.deployment.failed",
  "datacontenttype": "application/json",
  "dataschema": "https://midnight.dev/schemas/events/dev.midnight.deployment.failed/1.json",
  "time": "2026-04-25T12:03:14Z",
  "traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
  "midnightrequestid": "req_01HZXACCEPTED12345678ABCDE",
  "midnighttenantid": "wsp_01HZXWORKSPACEID0000000000",
  "data": {
    "deployment_id": "dep_01HZX9C6F7G8H9JKMNPQRSTVWX",
    "service_id": "svc_01HZX5N0AAEC9P3WQYRSTVWXYZ",
    "environment_id": "env_01HZX5N0AAEC9P3WQ0123456AA",
    "project_id": "prj_01HZX5MZZZZZZZZZZ0123456BB",
    "workspace_id": "wsp_01HZXWORKSPACEID0000000000",
    "sequence": 142,
    "previous_state": "DEPLOYING",
    "state": "FAILED",
    "reason_code": "HEALTHCHECK_FAILED",
    "details": {
      "sub_reason": "http_status",
      "endpoint": "/health",
      "last_status": 503,
      "attempts": 3,
      "startup_timeout_seconds": 180
    },
    "snapshot": {
      "source_sha": "9f1c4b2e3d77a8e0bcf2",
      "image_digest": "sha256:fa5e7b...",
      "builder": { "type": "dockerfile", "version": "1.6.0" }
    },
    "operation": {
      "id": "op_01HZXOPSAVERAGE0PERATION001",
      "kind": "service.deploy",
      "started_at": "2026-04-25T12:00:00Z",
      "ended_at": "2026-04-25T12:03:14Z"
    },
    "request_id": "req_01HZXACCEPTED12345678ABCDE"
  }
}
```

A subscriber MUST:

1. Compute `f"{Midnight-Delivery-Id}.{1745596800}.{<raw_body>}"` and HMAC-SHA256 it with each known key.
2. Compare in constant time against each `v2=` segment.
3. Reject if `|now - 1745596800| > 300`.
4. Dedupe on `Midnight-Delivery-Id == "whd_01HZXDELIVERYABCDE0123456789"`.
5. Respond 2xx within the timeout, then process asynchronously.

---

## 8. Quick reference

| Concern | Value |
|---|---|
| Signing algorithm | HMAC-SHA256 |
| Canonical outbound input | `f"{delivery_id}.{timestamp}.{raw_body}"` |
| Legacy inbound input | `f"{timestamp}.{raw_body}"` (`v1`, migration only) |
| Replay window | 300 s |
| Key rotation overlap | 30 days, two simultaneously valid keys |
| Dead-letter trigger | 7 attempts (24h cumulative) OR 5 consecutive in 5 min OR 50 cumulative |
| Per-attempt timeout | 30 s |
| Replay endpoint | `POST /api/v1alpha/webhooks/{endpoint_id}/deliveries/{id}/replay` |
| Inbound rate-limit class | `webhook receiver 6000 req/min` (panel-locked) |
| Outbound `User-Agent` | `Midnight-Webhooks/<server_version>` |
| Required CE extensions | `traceparent`, `midnightrequestid`, `midnighttenantid` |
| Content type | `application/cloudevents+json` (structured mode) |
