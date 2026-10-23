# API Error Code Registry — `/api/v1alpha`

This is the **panel-locked, additive-only** error code registry that ships at `docs/error-codes.md` and is referenced by the OpenAPI 3.1 contract (`OPENAPI_V1ALPHA.yaml`).

## Scope and shape

Every non-2xx response from `/api/v1alpha/*` uses the panel-locked error envelope:

```json
{
  "code": "SCREAMING_SNAKE_CASE_CODE",
  "message": "Human-readable, redaction-safe summary.",
  "request_id": "req_01HZX...",
  "docs_url": "https://midnight.dev/docs/error-codes#screaming_snake_case_code",
  "details": { "...": "optional, per-code structured context" }
}
```

Codes are flat `SCREAMING_SNAKE_CASE` with no nesting. Every entry has the columns: `code`, HTTP status, retriable, terminal, description, fix hint.

## Stability rules (panel-locked)

- **Additive-only.** New codes can be added at any time.
- **Renames or removals require an API version bump** (e.g. `v1alpha` -> `v1beta`). CI enforces this with the `error-codes-additive` check; any PR that renames or removes a code fails with `ERROR_CODE_RENAMED`.
- **Code registry semver is pinned to API version.** The registry version follows the URL-path API version (`v1alpha`, `v1beta`, `v1`). Within `v1alpha`, the registry is allowed to grow but never shrink.
- **Same code, same shape across surfaces.** API, CLI, dashboard, audit events, webhooks, and operation `error.code` all use the same code spelling.
- **`retriable=Y` means a client may retry the same request with the same `Idempotency-Key`.** It does not mean the server guarantees success — it means a retry is not actively harmful.
- **`terminal=Y` means an Operation in this state cannot transition further.** For non-Operation responses (sync 4xx/5xx), `terminal` is read as "this specific request is final; further attempts produce the same code unless a precondition changes."

## How to use the table

| Column | Meaning |
|---|---|
| **code** | The literal `SCREAMING_SNAKE_CASE` token returned in the envelope. |
| **HTTP status** | The HTTP status code on the response, or `n/a` if the code is only emitted on Operations / events / audit. |
| **retriable** | `Y` if a client retry (same body, same `Idempotency-Key`) is safe to attempt. |
| **terminal** | `Y` if the code, when set on an Operation/Deployment, is a terminal state. For sync responses, `Y` means the precondition does not heal on its own. |
| **description** | One-line summary surfaced in `message` and the docs site. |
| **fix hint** | Operator/developer remediation hint. |

---

## Error code table

### Idempotency, concurrency, pagination, rate limits (panel-additions)

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `IDEMPOTENCY_KEY_REQUIRED` | 400 | N | Y | `Idempotency-Key` missing on a `POST/PATCH/DELETE`. | Resend with a UUID `Idempotency-Key` header. |
| `IDEMPOTENCY_KEY_REUSED` | 409 | N | Y | Same key, different request body. | Use a fresh UUID, or replay with the original body. |
| `IDEMPOTENCY_REQUEST_IN_PROGRESS` | 409 | Y | N | Same key currently being processed by an in-flight request. | Honor `Retry-After` (integer seconds). |
| `INVALID_CURSOR` | 400 | N | Y | Cursor signature failed verification or expired (>1h stability window). | Restart iteration without a cursor. |
| `RESOURCE_VERSION_MISMATCH` | 412 | N | N | `If-Match` did not match the resource's current `resource_version`. | Re-fetch the resource and retry with the new `resource_version`. |
| `OPERATION_SUPERSEDED` | 409 | N | Y | A newer Operation (e.g. push deploy `runLatest`) superseded this one. | Inspect the new Operation; the old one is canceled. |
| `RATE_LIMITED` | 429 | Y | N | Per-actor or per-IP rate limit hit. | Honor `Retry-After`; reduce request rate. |
| `CONFIG_FIELD_LOCKED` | 422 | N | N | Field is owned by `midnight.yaml` config-as-code; dashboard/API edits refused. | Edit the field in source code and re-deploy, or release ownership with `midnight config release`. |
| `AUTH_TOKEN_HEADER_INVALID` | 401 | N | Y | Token sent via `X-Midnight-Token` (rejected) or malformed `Authorization`. | Use `Authorization: Bearer midn_<class>_<26-base32>`. |
| `BOOTSTRAP_DISABLED` | 404 | N | Y | The one-shot bootstrap path is auto-disabled. | Use a normal admin token. |

### Build / supply-chain failure classifications

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `BUILD_FAILED` | n/a | Y | Y | Builder exited non-zero. | Inspect build logs; fix code; redeploy. |
| `BUILD_TIMEOUT` | n/a | Y | Y | Build exceeded configured timeout (default 30m, max 4h). | Reduce build size, enable better caching, or raise `build.timeout`. |
| `BUILD_OUTPUT_SECRET_LEAK` | n/a | N | Y | Static scan detected secret prefix (e.g. `midn_`, `AKIA`, `xoxb-`, JWT shape) in build output. | Remove the credential from the build output; use BuildKit secret mounts. |
| `BUILD_SECRET_LEAK_DETECTED` | n/a | N | Y | Build secret leaked into image, log, or cached layer. | Rotate the leaked secret; rebuild with `buildSecrets` mounts only. |
| `BUILDS_BLOCKED_DISK_PRESSURE` | 409 | Y | N | Host disk pressure has crossed the degrade threshold (>=85%). | Free disk; restart build after pressure drops. |
| `DEPENDENCY_INSTALL_FAILED` | n/a | Y | Y | Package manager step failed. | Check lockfile, registry reachability, and credentials. |
| `IMAGE_PUSH_FAILED` | n/a | Y | Y | Push to embedded or external registry failed. | Check registry health and credentials. |
| `IMAGE_PULL_FAILED` | n/a | Y | Y | Image pull failed at deploy time. | Verify digest exists; check registry auth. |
| `IMAGE_SIGNATURE_INVALID` | n/a | N | Y | cosign (or equivalent) signature verification failed before pull/start. | Re-sign the image with a trusted key, or update the trusted-key set. |
| `IMAGE_ARCH_UNAVAILABLE` | n/a | N | Y | Multi-arch manifest list does not contain the host's architecture. | Build for the host arch, or relax `platforms:`. |
| `EXTERNAL_DIGEST_MISSING` | n/a | N | Y | Daily probe failed to find an external-registry digest referenced by an active/protected deployment. | Restore the missing digest from backup or rebuild. |
| `DEPLOY_PROVENANCE_UNVERIFIED` | n/a | N | Y | SLSA-style provenance attestation missing or invalid for the candidate digest. | Rebuild with provenance, or relax policy with audited approval. |

### Deployment / runtime failure classifications

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `MISSING_REQUIRED_VARIABLE` | n/a | N | Y | Preflight detected a required variable with no value or unresolved reference. | Set the variable in the appropriate scope. |
| `VARIABLE_REFERENCE_FAILED` | n/a | N | Y | Reference variable could not resolve at deploy time. | Fix the reference target or its visibility. |
| `CONTAINER_EXITED` | n/a | Y | Y | Container exited before readiness. | Inspect runtime logs; fix start command. |
| `COMMAND_NOT_FOUND` | n/a | N | Y | Start command not found in image. | Update Dockerfile/start command. |
| `PORT_NOT_LISTENING` | n/a | N | Y | App did not bind the expected port within startup window. | Ensure the app listens on `$PORT` and `0.0.0.0`. |
| `APP_BOUND_LOCALHOST` | n/a | N | Y | App bound to `127.0.0.1`/`::1` only. | Bind to `0.0.0.0` (or the wildcard for IPv6). |
| `HEALTHCHECK_FAILED` | n/a | Y | Y | Readiness check failed within retry budget. | Inspect healthcheck endpoint; raise startup timeout if cold-start is slow. |
| `ROUTE_RECONCILIATION_FAILED` | n/a | Y | Y | Traefik dynamic-config reconciliation refused or failed. | Check route table and Traefik logs. |
| `CRASH_LOOP_BACKOFF` | n/a | Y | N | Restart-policy backoff engaged after repeated crashes. | Fix the underlying crash; reset by redeploying. |
| `OOM_KILLED` | n/a | Y | Y | Container hit memory limit. | Raise memory; reduce footprint. |
| `HOST_UNAVAILABLE` | n/a | Y | Y | Zero ready instances and `degraded_grace` exceeded. | Recover the host, or reschedule. |
| `CANCEL_NOT_SAFE` | 409 | N | N | Cancellation requested between route-add (step 6) and drain-completion (step 9). | Issue a rollback instead. |
| `DEPLOY_GATE_PENDING_CHECKS` | 409 | Y | N | Required external checks have not all reported success. | Wait for external CI; re-trigger the gate. |

### Domain / TLS / DNS

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `DNS_VERIFICATION_FAILED` | n/a | Y | N | TXT/CNAME/ALIAS verification failed. `details.sub_reason` is one of `missing_txt`, `wrong_txt`, `wrong_routing_record`, `dns_not_propagated`, `caa_blocks`, `port80_blocked`, `ipv6_mismatch`, `le_rate_limit`, `advertised_addresses_drift`. | Match the records exactly, then re-verify. |
| `TLS_ISSUANCE_FAILED` | n/a | Y | N | ACME issuance failed (CAA, rate limit, challenge timeout). | Inspect domain diagnostics; consider DNS-01 fallback. |
| `INGRESS_PORT80_UNREACHABLE` | n/a | N | Y | External reachability prober could not reach `:80` / `:443` for ACME HTTP-01. | Open inbound 80/443 or switch to DNS-01 with the documented `DnsProvider`. |
| `DOMAIN_OWNERSHIP_LAPSED` | n/a | Y | N | Periodic re-verification (default 30 days, jittered) failed; route enters 7-day grace. | Re-add the TXT record, or fix routing record. |
| `RESTORE_ACME_THROTTLED` | n/a | Y | N | ACME-state restore is staging issuance at <=20 certs/hour to stay under LE caps; emitted as progress, not failure. | Wait; use LE staging for the first batch; honor the priority queue. |
| `WILDCARD_REQUIRES_DNS01` | 422 | N | Y | Wildcard custom domain attempted via HTTP-01. | Configure a `DnsProvider` and use DNS-01. |

### Backups / restore / volumes

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `BACKUP_FAILED` | n/a | Y | Y | Backup job failed. | Inspect logs; verify repo passphrase and storage. |
| `RESTORE_FAILED` | n/a | Y | Y | Restore job failed. | Inspect logs; verify backup integrity. |
| `BACKUP_STALE` | n/a | Y | N | No successful backup in the last 2x scheduled period. | Trigger a manual backup; investigate scheduler. |
| `BACKUP_UNVERIFIED` | n/a | N | N | One-time restore-into-throwaway verification has not passed for this backup config. | Run the verification drill. |
| `BACKUP_OFFHOST_MISSING` | n/a | N | N | No external `s3://`/`gcs://`/`sftp://`/`rsync` destination configured. | Configure at least one off-host destination. |
| `BACKUP_SIZE_DEGRADED` | n/a | N | N | Postgres database larger than 50 GB; logical-dump path degrades. | Move to v1 physical-backup path. |
| `BACKUP_COLOCATED_RISK` | n/a | N | N | Backup repository on the same host/FS as user volumes. | Move backup repo to a separate destination. |
| `WAL_ARCHIVE_LAGGING` | n/a | Y | N | Postgres WAL archive sidecar is lagging. | Check archive destination throughput; investigate disk pressure. |
| `OWNERSHIP_CONFLICT` | 409 | N | Y | UID/GID conflict at first volume mount. | Resolve `/etc/subuid` mapping; fix template UID/GID expectation. |
| `RESIZE_SHRINK_UNSUPPORTED` | 422 | N | Y | Volume shrink requested. | Volumes can grow online; shrink is not supported. |
| `ROLLBACK_IMAGE_UNAVAILABLE` | n/a | N | Y | Recorded image digest cannot be retrieved (registry GC, external loss). | Restore digest from backup, or rebuild and redeploy. |

### Host / agent / runtime

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `DOCKER_ENGINE_UNSUPPORTED` | n/a | N | Y | Docker Engine outside the supported version range (>=24.0, API >=v1.43). | Upgrade or downgrade Docker Engine to the supported range. |
| `AGENT_RUNTIME_UNAVAILABLE` | n/a | Y | N | Agent cannot reach the Docker Engine API; it backs off rather than crash-looping. | Restore Docker Engine; agent reconnects automatically. |
| `AGENT_CLOCK_SKEW` | n/a | Y | N | `|agent_clock - server_clock| > 30s` at registration. | Enable `chrony` or `systemd-timesyncd`; resync. |
| `VOLUME_MOUNT_FAILED` | n/a | Y | Y | Volume could not be mounted on the candidate host. | Check ownership, quotas, and disk pressure. |
| `DISK_PRESSURE` | n/a | Y | N | Host disk usage past warn/degrade/block threshold. | Free disk via build cache GC, registry GC, or expired backup pruning. |
| `STORAGE_DEGRADED` | n/a | Y | N | A storage subsystem (registry, BuildKit cache, backups) is degraded. | Inspect `midnight doctor`; address per-subsystem hint. |
| `VOLUME_HOST_LOST` | n/a | N | Y | Single-writer volume's host has been lost. | Restore from backup to a replacement host. |
| `PRIVATE_DNS_FAILED` | n/a | Y | Y | Internal DNS resolution failed for a service-to-service call. | Inspect Docker bridge invariant; check service slug RFC-1035 validity. |
| `DATABASE_TEMPLATE_UNHEALTHY` | n/a | Y | Y | Database template smoke check failed. | Inspect template logs; rerun smoke check. |

### Upgrade / version skew / platform

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `VERSION_SKEW_REJECTED` | 409 | N | Y | Worker/agent version outside the server's compatibility window at registration. | Upgrade or downgrade the worker/agent to a compatible version. |
| `UPGRADE_SKIP_FORBIDDEN` | 422 | N | Y | Skip-version upgrade attempted. | Upgrade through each intermediate version per the `apt`-resolved path. |
| `PLATFORM_DEGRADED` | 503 | Y | N | Control plane is in degraded/read-only mode (e.g. Postgres outage). | Follow the offline recovery runbook; data planes continue serving traffic. |

### Webhooks (inbound + outbound)

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `WEBHOOK_SIGNATURE_INVALID` | 400 | N | Y | HMAC signature verification failed. | Re-sign with the correct key; check `kid`. |
| `WEBHOOK_REPLAY_REJECTED` | 400 | N | Y | Timestamp outside +/-300s replay window. | Re-send with a fresh timestamp. |
| `WEBHOOK_ENDPOINT_DISABLED` | 410 | N | Y | Endpoint disabled after repeated failure. | Fix the receiver and re-enable in the dashboard. |

### AI / MCP (forward-link; most are Phase 1+)

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `AI_DISABLED` | 403 | N | Y | AI features are disabled (default). | Operator must enable via workspace policy. |
| `AI_ACTION_REQUIRES_APPROVAL` | 409 | Y | N | Class 3+ action requires out-of-band human approval. | Approve in dashboard; re-invoke. |

### Audit

| code | HTTP status | retriable | terminal | description | fix hint |
|---|---|---|---|---|---|
| `AUDIT_CHAIN_BROKEN` | n/a | N | Y | `midnight audit verify` detected a row whose `row_hash != H(prev_hash || canonical_row)`. | Investigate tamper; restore from a verified checkpoint. |

---

## Categories used in `details.sub_reason`

For codes whose taxonomy is too rich for a flat code, `details.sub_reason` is the agreed-on second-level token. These tokens are also additive-only.

- `DNS_VERIFICATION_FAILED.sub_reason in {missing_txt, wrong_txt, wrong_routing_record, dns_not_propagated, caa_blocks, port80_blocked, ipv6_mismatch, le_rate_limit, advertised_addresses_drift}`.
- `BUILD_FAILED.sub_reason in {dockerfile_parse, dependency_resolution, compile_error, test_failure, push_denied}`.
- `HEALTHCHECK_FAILED.sub_reason in {connection_refused, http_status, timeout, tls_error}`.

## CI enforcement

The CI job `error-codes-additive` runs on every PR and asserts:

1. The diff between `docs/error-codes.md` on `main` and the PR contains no removed rows and no renamed `code` cells.
2. Every code referenced in OpenAPI examples, `internal/apierr` constructors, audit-event templates, and webhook payload fixtures resolves to a row in this table.
3. Every code in this table has at least one Schemathesis test asserting the response shape.

If any of those asserts fails, the job fails with `ERROR_CODE_RENAMED` (rename/removal) or `ERROR_CODE_UNREGISTERED` (use without a row).

## Out-of-scope codes (Phase 1+)

The following codes are reserved for Phase 1+ features and **must not** be emitted by `/api/v1alpha`:

- `POLICY_DENIED` (Phase 1 OPA/Conftest)
- `QUOTA_EXCEEDED` (Phase 1 quotas)
- `BYOK_KEY_UNAVAILABLE` (enterprise BYOK)
- `SCIM_PROVISIONING_FAILED` (enterprise SCIM)

When these features ship, their codes are added to this table without a version bump (additive).
