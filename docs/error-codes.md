# Error Codes

This registry is additive-only. Renames or removals require a versioned API
decision per the panel-locked supply-chain stance.

The registry is sorted alphabetically by `Code`. Every typed
`failurecodes.Code` constant in
[`internal/failurecodes/codes.go`](../internal/failurecodes/codes.go) must
appear here; the lint tool's `LINT_REGISTRY_GUARD_FAILED` check enforces this
on every PR. Codes that are not Go-typed yet (release pipeline, lint, KMS)
also appear here so operators always have one place to read a recovery hint.

## Code Format

- `SCREAMING_SNAKE_CASE`.
- ASCII letters, digits, and underscores only.
- Codes group by subsystem prefix (`ACME_`, `AGENT_`, `AUTH_`, `BACKUP_`,
  `CONTAINER_`, `IDEMPOTENCY_`, `INGRESS_`, `LINT_`, `OPERATION_`,
  `RELEASE_`, `RESOURCE_`, `SCHEDULER_`, `SUPPORT_`, `WEBHOOK_`, etc.) so
  triage can route on the prefix alone.
- A new code requires a registry row with `Area`, `Notes`, and
  `Recovery hint` columns.

## Registry

| Code | Area | Notes | Recovery hint |
|---|---|---|---|
| `ACME_CAA_FORBIDDEN` | Ingress | CAA preflight refused submission; remove or update CAA records. | Update CAA records to authorize the configured ACME issuer, then retry. |
| `ACME_CERTIFICATE_EXPIRED` | Ingress | Certificate is past its NotAfter timestamp. | Trigger immediate renewal; check ACME provider status if it fails. |
| `ACME_CERTIFICATE_EXPIRING_CRITICAL` | Ingress | Certificate expires within 7 days; renew immediately. | Run `midnight cert renew` for the affected domain now. |
| `ACME_CERTIFICATE_EXPIRING_WARN` | Ingress | Certificate expires within 30 days; schedule renewal. | Confirm renewal schedule is active; investigate if budget is exhausted. |
| `ACME_DNS_MISMATCH` | Ingress | DNS-01 validation observed a TXT mismatch. | Verify the configured DNS provider credentials and propagation settings. |
| `ACME_HTTP01_UNREACHABLE` | Ingress | HTTP-01 reachability gate failed; verify port 80 or fall back to DNS-01. | Confirm port 80 is reachable from the public internet; otherwise switch to DNS-01. |
| `ACME_ORDER_FAILED_PERMANENT` | Ingress | ACME order transitioned to a terminal failure; recreate order with corrected inputs. | Inspect the order error, correct the underlying input, and recreate the order. |
| `ACME_ORDER_FAILED_RETRYABLE` | Ingress | ACME order failed but is safe to retry after the recommended cool-down. | Wait for the recommended cool-down, then retry the order. |
| `ACME_RATE_BUDGET_EXHAUSTED` | Ingress | Per-registered-domain weekly issuance budget exceeded; falls back to staging. | Wait for the weekly budget window to roll over, or use staging until production budget is available. |
| `ACME_RATE_LIMITED` | Ingress | Upstream CA rate-limited issuance; back off and retry. | Back off for the duration suggested by the CA, then retry the order. |
| `ACME_RESTORE_BUDGET_EXHAUSTED` | Ingress | Restore reissue queue exceeds the panel-locked hourly throttle; planner refuses additional slots. | Allow the throttle to drain, then resume the restore queue. |
| `AGENT_CLOCK_SKEW` | Runtime/Ingress | Agent or ACME clock skew exceeds the allowed bound. | Resync the agent host clock with NTP, then restart `midnight-agent`. |
| `AGENT_HEARTBEAT_EXPIRED` | Runtime | Heartbeat timestamp fell outside the accepted clock-skew window. | Resync host clock with NTP and confirm agent heartbeat cadence. |
| `AGENT_HEARTBEAT_REPLAY` | Runtime | Heartbeat sequence already accepted within the replay window. | Investigate duplicated agent identity; rotate the agent credential if compromised. |
| `AGENT_HEARTBEAT_TAMPERED` | Runtime | Heartbeat HMAC failed to verify; payload likely tampered or wrong credential. | Verify the agent-scoped credential matches the control-plane keyring; rotate if mismatch persists. |
| `AGENT_NOT_CONFIGURED` | Agent | The AI agent has no provider API key (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `MINIMAX_API_KEY`), so chat sends return HTTP 503. | Set the provider key for the selected `MIDNIGHT_AGENT_PROVIDER` and restart `midnight-server`. |
| `AGENT_RUNTIME_UNAVAILABLE` | Runtime | Host's container runtime (Docker daemon) is unreachable; agent enters backoff. | Confirm Docker is installed and `midnight-agent` can reach the Engine API socket; the agent retries with backoff. |
| `AGENT_STATUS_REPORT_EXPIRED` | Runtime | Status observation fell outside the accepted clock-skew window. | Resync host clock with NTP and confirm status-report cadence. |
| `AGENT_STATUS_REPORT_REPLAY` | Runtime | Status sequence was already accepted. | Investigate duplicated agent identity or a stuck sender sequence; rotate the credential if compromised. |
| `AGENT_STATUS_REPORT_TAMPERED` | Runtime | Status-report HMAC failed to verify. | Verify the agent-scoped credential matches the control-plane keyring; rotate if mismatch persists. |
| `ALERT_METADATA_INVALID` | Backup/ops | Alert metadata failed the strict schema validator (severity, runbook_link, first_action, alert_id, summary). | Add the missing alert metadata fields and rerun the alert pack lint. |
| `AUDIT_CHAIN_BROKEN` | API/Audit | Audit chain verification detected tamper, edit, or delete. | Halt protected operations; engage security review per `SECURITY.md`. |
| `AUDIT_CHECKPOINT_SIGNATURE_INVALID` | API/Audit | Daily checkpoint signature failed Ed25519 verification (per ADR-011). | Halt protected operations; rotate the signing key bundle and re-run `midnight audit verify`. |
| `AUDIT_KEY_NOT_FOUND` | API/Audit | The verifying key fingerprint referenced by a checkpoint is not in the configured keyring. | Add the missing public key (PEM) to the verifier keyring or replace it via key rotation overlap. |
| `AUTHORIZATION_DENIED` | API/Identity | An authenticated human session lacks the workspace or project capability required by the route. | Ask a workspace or project administrator for the capability required by this action. |
| `AUTH_CREDENTIALS_INVALID` | API/Identity | Local-account login failed without disclosing whether the account or password was incorrect. | Verify the account credentials and multi-factor code, then retry. |
| `AUTH_OPERATOR_RECOVERY_REQUIRED` | API/Identity | A route that accepts only the installation-wide operator recovery credential received a human session instead. `POST /api/v1alpha/auth/password-reset` (`midnight admin reset-password`) is the only such route today: administrator credential recovery is deliberately unreachable from any signed-in account, so one administrator cannot rewrite another's password and second factor. | Rerun the command on the Midnight host with `MIDNIGHT_API_TOKEN` set to `$MIDNIGHT_API_BEARER_TOKEN`. |
| `AUTH_RATE_LIMITED` | API/Identity | The HMAC-pseudonymized account or network login-attempt budget is exhausted. | Wait for the `Retry-After` interval before attempting to sign in again. |
| `AUTH_REQUIRED_MISSING` | API | Production auth is expected, but no operator recovery bearer is configured for a protected route. | Configure `MIDNIGHT_API_BEARER_TOKEN` or pass `--api-bearer-token` before issuing protected requests. |
| `AUTH_SESSION_INVALID` | API/Identity | The human session is expired, idle-expired, revoked, disabled, generation-invalidated, or otherwise unusable. | Sign in again; the session may have expired or been revoked. |
| `AUTH_TOKEN_HEADER_INVALID` | API | Non-Bearer or unsupported token transport. | Resend the request with `Authorization: Bearer <midn_â€¦>`. |
| `BACKUP_CREDENTIALS_MISSING` | Backup/ops | Backup credential resolver found neither the env-var path nor a sealed-variable reference; the off-host destination cannot be reached. | Set `MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID`/`MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY`, or configure a sealed credential variable and point `MIDNIGHT_BACKUP_CREDENTIAL_REF` at its `var_<ulid>` id. |
| `BACKUP_CREDENTIALS_UNAUTHORIZED` | Backup/ops | Sealed credential reference exists but the consumer principal lacks read scope on the referenced variable, or the reference target is missing. | Grant the backup principal read scope on the referenced sealed credential, or rotate `MIDNIGHT_BACKUP_CREDENTIAL_REF` to a variable in the consumer's allowed scope. |
| `BACKUP_DESTINATION_WRITE_FAILED` | Backup/ops | A configured backup destination rejected a backup manifest or restore-plan write. | Verify the configured backup destination exists, is writable by the Midnight service account, and has enough capacity; then retry the backup or restore-plan write. |
| `BACKUP_OFFHOST_MISSING` | Backup/ops | Required warning/alert for Alpha when no off-host destination is configured. | Configure an off-host backup destination in `midnightctl backup config`. |
| `BACKUP_STALE` | Backup/ops | Last successful backup is older than the configurable threshold (default 26h). | Trigger a backup now; investigate the scheduled-backup runbook if it keeps lapsing. |
| `BACKUP_VERIFY_CHECKSUM_MISMATCH` | Backup/ops | One or more backup artifacts failed SHA-256 verification against the source-of-truth manifest. | Re-export the failing artifact; if it still mismatches, follow the `backup-restore` runbook. |
| `BOOTSTRAP_TOKEN_CONSUMED` | Install | Reserved, and not the home for administrator recovery. The bootstrap-token first-login flow was removed; the first administrator is created through the one-shot `POST /auth/claim` (`midnight admin create-user`), which reports `RESOURCE_CONFLICT` when the installation is already claimed, and a lost password afterwards is recovered through `POST /auth/password-reset` (`midnight admin reset-password`) under the operator recovery credential. Recovery stores no one-shot token, so the `users.bootstrap_token_hash` column from migration 0007 remains unused and is left in place for a later drop. | Create the first administrator with `midnight admin create-user`; if the installation is already claimed, sign in with `midnight login`, or recover a lost password with `midnight admin reset-password`. |
| `CAPABILITY_UNAVAILABLE` | API | A stable operation exists, but its required adapter or operator configuration is unavailable on this server. | Configure or restore the capability named in the problem details, then retry the operation. |
| `CAP_NOT_PERMITTED` | Runtime | Container spec requested a capability outside the operator allowlist. | Remove the capability or add it to the operator allowlist with an ADR explaining why. |
| `CGROUP_V2_REQUIRED` | Runtime | Host runs cgroup v1; ADR-005 hard-requires cgroup v2 unified hierarchy. | Add `systemd.unified_cgroup_hierarchy=1` to the kernel command line and reboot. |
| `CONTAINER_LABEL_MISSING` | Runtime | Container spec lacked one or more required midnight ownership labels. | Add the missing `midnight.project`, `midnight.service`, `midnight.deployment`, `midnight.instance` labels. |
| `CONTAINER_NAME_CONFLICT` | Runtime | Stale Docker container collides with the deterministic name allocator and is not safe to garbage-collect. | Inspect the colliding container; if it is operator-managed, remove it manually before retrying. |
| `CONTAINER_SPEC_INVALID` | Runtime | Container spec failed validator hardening checks. | Inspect the validator detail field and fix the offending spec field. |
| `CRON_INVOCATION_FAILED` | Jobs | Cron invocation exited non-zero or timed out. | Inspect invocation logs, apply the retry policy, and fix the command or image before the next run. |
| `CRON_SCHEDULE_INVALID` | Jobs | Cron service schedule failed expression validation. | Fix the cron expression and rerun configuration validation. |
| `CRON_TIMEZONE_INVALID` | Jobs | Cron service referenced an unknown IANA timezone. | Use a valid IANA timezone or omit the field to use the service default. |
| `CRON_WINDOW_MISSED` | Jobs | Cron window skipped because `concurrencyPolicy=forbid` and a prior invocation was still running. | Inspect the prior invocation; reduce runtime, change concurrency policy, or widen the schedule. |
| `CURSOR_INVALID` | API | Pagination cursor failed to decode or referenced an unknown sequence. | Drop the cursor and re-list from the start. |
| `DAEMON_JSON_CONFLICT` | Install | Operator-managed Docker daemon.json conflicts with the Midnight baseline. | Reconcile operator-managed Docker daemon.json keys with the Midnight baseline before retrying. |
| `DEPLOY_CANCELED_BEFORE_LEASE` | Queue/Deploy | Queued deploy job was canceled before any worker acquired a lease. | No worker action is required; submit a new deploy if the cancellation was accidental. |
| `DEPLOY_CANCELED_BEFORE_PROMOTION` | Worker/Deploy | Running deploy was stopped at the worker safe point after readiness checks and before traffic promotion. | The deploy was canceled before traffic promotion; submit a new deploy when ready. |
| `DEPLOY_CANCEL_QUEUE_UNAVAILABLE` | API/Queue | Queued deploy cancellation could not atomically cancel the linked queue job before lease. | Wait for the queue store to be wired, then cancel again so the queued job can be stopped before lease. |
| `DEPLOY_CANCEL_UNSAFE` | API/Queue | Tenant cancellation found neither pending durable work to withdraw nor a leased worker that can observe a safe point. | Refresh the operation. If it is still running without pending work or a leased worker, inspect queue/outbox recovery before retrying cancellation. |
| `DEPLOY_CANDIDATE_EXITED` | Worker/Deploy | Deploy candidate container stopped before it became ready; the failure reason carries the exit code and the container's last log lines. | Read the container logs in the failure reason, fix the image start command or its configuration, and deploy again. |
| `DEPLOY_CANDIDATE_RESTART_LOOP` | Worker/Deploy | Deploy candidate container exits on every start and the runtime restart policy keeps restarting it, so readiness can never settle. | The container starts and exits repeatedly; fix the crash shown in the failure reason, then deploy again. |
| `DEPLOY_GATE_PENDING_CHECKS` | Deploy gate | Deploy promotion blocked by required GitHub combined-status checks that have not finished. | Wait for the required checks to settle, or update the required-check policy; the gate retries automatically. |
| `DEPLOY_JOB_ENQUEUE_FAILED` | API/Queue | Deploy intake could not enqueue the deploy job after creating the operation. | Inspect the deploy queue store and database connectivity, then retry the deploy with a fresh idempotency key. |
| `DEPLOY_SOURCE_REF_UNSUPPORTED` | API/Deploy | Source-ref deploy intake is rejected until source-build-registry emits immutable digest candidates. | Deploy an immutable digest-pinned image until source-build-registry emits candidate images. |
| `DEPLOY_SUPERSEDED` | Runtime | Single-writer arbitration rolled back this deployment because a newer deployment for the same (service, environment) won. | Another deployment for the same (service, environment) won the supersede race; this attempt was rolled back cleanly. |
| `DISK_PRESSURE_CRITICAL` | Install | Disk usage is at or above the doctor critical alert threshold. | Free disk space or expand the volume; rerun `midnight doctor` to confirm headroom. |
| `DNS_PROPAGATION_TIMEOUT` | Ingress | DNS-01 propagation did not settle within the configured window. | Inspect DNS provider records; extend wait window or fix records, then retry. |
| `DOCKER_DAEMON_BASELINE_CONFLICT` | Runtime | Live Docker daemon configuration violates the locked baseline (overlay2, live-restore, no-new-privileges, json-file log driver, init=true). | Reconcile `/etc/docker/daemon.json` (overlay2, live-restore, no-new-privileges, json-file log driver) with the Midnight baseline before retrying. |
| `DOCKER_ENGINE_UNSUPPORTED` | Install/Runtime | Docker Engine API below v1.43 or unsupported engine. | Upgrade Docker Engine to >= 24.0 (API v1.43) and rerun preflight. |
| `EVIDENCE_INDEX_NOT_READY` | API | API-data-readiness reports the evidence index has not been built or signed yet. | Wait for the next gate run to publish the evidence index, or trigger a manual gate run. |
| `GRAPHQL_DEPLOY_MUST_BE_SOLE_MUTATION` | API/GraphQL | A durable deploy document selected another top-level mutation field alongside the deploy. | Send the durable deploy as the only top-level mutation field, then retry. |
| `GRAPHQL_VALIDATION_FAILED` | API/GraphQL | A GraphQL document failed schema validation before execution. | Correct the reported GraphQL syntax, field, argument, or type error and retry the request. |
| `HOST_PORT_NOT_PERMITTED` | Runtime | Container spec attempted to bind a privileged host port without explicit allow. | Bind to an unprivileged port (>=1024) or explicitly allow the port in operator policy. |
| `IDEMPOTENCY_KEY_INVALID` | API | Idempotency-Key header malformed or out of allowed range. | Issue a new Idempotency-Key matching the documented format and retry. |
| `IDEMPOTENCY_KEY_REQUIRED` | API | Mutating endpoint received no `Idempotency-Key` header. | Add an `Idempotency-Key` header matching the documented format and retry. |
| `IDEMPOTENCY_KEY_REUSED` | API | Same key, different body. | Use a fresh Idempotency-Key for the new request payload. |
| `IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY` | API | Body hash mismatch on terminal-state replay attempt. | Reuse the original request body or issue a new Idempotency-Key. |
| `IDEMPOTENCY_REQUEST_IN_PROGRESS` | API | Concurrent duplicate request. | Wait for the in-flight request to settle, then poll its operation. |
| `IMAGE_SIGNATURE_INVALID` | Runtime/Registry | Image signature verification failed. | Re-pull the image from a trusted registry; rerun cosign verify against the published key bundle. |
| `IMAGE_SIGNATURE_REQUIRED` | Runtime | Agent runtime is configured with `verify_signatures=true` and the requested image is unsigned. | Sign the image with cosign and push the .sig artifact, or set verify_signatures=false in the agent config (not recommended). |
| `IMAGE_SIGNATURE_VERIFY_BINARY_MISSING` | Runtime | Agent has `verify_signatures=true` but the cosign binary is not present at the configured `cosign_binary_path`; agent refuses to silently downgrade enforcement. | Install the pinned cosign binary (see versions.yaml) and set `cosign_binary_path` in the agent config, or disable verify_signatures. |
| `IMAGE_SIGNATURE_VERIFY_TIMEOUT` | Runtime | `cosign verify` fork-exec exceeded the configured timeout budget (default 30s; tunable via `cosign_verify_timeout_seconds`). | Raise `cosign_verify_timeout_seconds` in the agent config or investigate slow registry/Rekor responses. |
| `IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED` | Runtime | Cosign reported a Sigstore Rekor / Fulcio transparency-log failure (missing CT entry, transparency-log signature invalid, or certificate identity mismatch). | Halt deployment; engage security to inspect the Sigstore Rekor / Fulcio transparency entry for this image. |
| `INGRESS_PORT80_UNREACHABLE` | Ingress | External prober could not reach :80; ACME issuance gated. | Open port 80 to the public internet or switch to DNS-01 challenges. |
| `INGRESS_PORT_BOUND` | Install | A required ingress port (80 or 443) is already bound by another process. | Stop the conflicting process or reconfigure ingress to a free port; rerun preflight. |
| `INGRESS_RELOAD_THRASH` | Ingress | Traefik reload churn exceeded the thrash budget. | Pause route updates briefly to let the reload budget recover. |
| `INTERNAL_ERROR` | API | Generic non-user-actionable fallback. | File an issue with the request id; operators consult the support bundle. |
| `INVITATION_ACCOUNT_EXISTS` | API/Identity | The invited account already exists; onboarding did not change its password. | Sign in normally and accept the invitation. |
| `LEASE_EXPIRED` | Queue | Reaper reclaimed an expired lease and requeued the job after exponential backoff. | Let the backoff elapse, then inspect worker health if the job expires repeatedly. |
| `LEASE_EXPIRED_MAX_ATTEMPTS` | Queue | Reaper exhausted the retry budget for an expired lease and moved the job to terminal failure/dead-letter. | Inspect worker logs and the job payload, fix the underlying worker failure, then enqueue a new operation. |
| `LINT_ADR_INDEX_DRIFT` | Tooling | `docs/adr/README.md` drifted from per-ADR front-matter. | Regenerate the ADR index from front-matter and recommit. |
| `LINT_GATE_DOC_DRIFT` | Tooling | `docs/governance/phase0-gate.md` documents a flag that `tools/phase0-gate/main.go` does not register. | Update the operator reference to match the implemented flag set, or add the flag in the same PR. |
| `LINT_INVOCATION_FAILED` | Tooling | `tools/lint` could not load files or parse Go sources. | Re-run `make lint` from a clean worktree; ensure files are committed. |
| `LINT_PRODUCT_COPY_GUARD_FAILED` | Tooling | A user-visible `web-next` string uses implementation vocabulary (operation, durable, evidence, changeset, canonical, idempotency, admitted, intake). | Reword the string in the reader's language per `docs/railway-ux-9-of-10-target-2026-09-01.md`, or record a deliberate exception in `tools/lint/product_copy_allowlist.txt`. |
| `LINT_REGISTRY_GUARD_FAILED` | Tooling | Failure-code registry diverged from `docs/error-codes.md`. | Add or update the row in `docs/error-codes.md` and recommit. |
| `LINT_SCAFFOLD_GUARD_FAILED` | Tooling | Repository scaffold or governance file is missing or non-conforming. | Inspect the lint output, fix the scaffold or governance file, and rerun `make lint`. |
| `LINT_SOURCE_GUARD_FAILED` | Tooling | Go source policy guard rejected `panic(`, `os.Exit`, or missing package doc. | Move the offending call to a `cmd/<binary>/main.go` entry point or add the canonical package doc, then rerun. |
| `LOG_QUERY_INVALID` | Logs | Log query filter or cursor failed validation. | Drop the cursor and re-issue the query with valid filter values. |
| `LOG_STORE_UNAVAILABLE` | Logs | Operation log request could not reach a backed app/platform log store. | Configure MIDNIGHT_LOG_DIR to a readable structured log directory or inspect the server log-store error. |
| `MEMORY_HEADROOM_LOW` | Install | Available memory headroom is below the doctor warning threshold. | Free memory or right-size the host; rerun `midnight doctor` to confirm headroom. |
| `METHOD_NOT_ALLOWED` | API | The resource exists but does not support the requested HTTP method. | Retry using one of the methods listed in the `Allow` response header. |
| `MFA_INVALID_CODE` | Install | Supplied TOTP code did not verify against the stored secret within the allowed skew. | Resync the authenticator app's clock and re-enter the 6-digit code. |
| `MFA_REQUIRED` | Install | Actor must complete TOTP enrollment before the requested operation is permitted. | Complete TOTP enrollment via `midnight admin enroll-mfa` before retrying. |
| `MIDNIGHT_MUTATION_DISALLOWED` | Agent/MCP | An MCP session attempted to invoke a mutating tool without mutation authority. | Re-run the session with explicit mutation permission only if the requested state change is intended. |
| `MIGRATION_CHECKSUM_PLACEHOLDER` | API | Release evidence references a migration whose checksum is still a placeholder; not safe to ship. | Compute the canonical checksum, replace the placeholder, and re-emit release evidence. |
| `MIGRATION_GAP` | API | Release evidence detected a migration sequence gap. | Reconcile the migration sequence so every version is contiguous; re-emit release evidence. |
| `MIGRATION_PENDING` | API | Release evidence reports a pending migration that has not yet applied. | Apply the pending migration in a maintenance window; re-emit release evidence afterwards. |
| `MIGRATION_STATUS_BLOCKED` | API | Release evidence reports a migration that the gate refuses to ship. | Inspect the blocked migration's diagnostics and address the underlying issue before retrying release. |
| `NOTIFICATION_DELIVERY_FAILED` | Notifications | Native Slack, SMTP, or future notification delivery failed after planning. | Inspect the notification channel configuration, rotate credentials if needed, and retry delivery. |
| `NOT_IMPLEMENTED` | API | Endpoint or method not implemented in the current product path. | Wait for the implementation to land; track with the linked work item. |
| `OPENAPI_INVALID` | API | API-data-readiness reports the published OpenAPI document failed validation. | Re-emit the OpenAPI document and re-run the gate; consult the linter output for specifics. |
| `OPENAPI_UNAVAILABLE` | API | API-data-readiness cannot reach the OpenAPI artifact. | Re-publish `api/openapi/v1alpha/midnight.yaml`; verify it is reachable from the readiness probe. |
| `OPERATION_INVALID_TRANSITION` | API | Operation state machine refused the requested transition. | Refetch the operation, then issue a transition allowed from the current state. |
| `OPERATION_NOT_FOUND` | API | Operation id is unknown or has been pruned. | Verify the operation id and re-list operations to confirm it still exists. |
| `OPERATION_STORE_UNHEALTHY` | API | API-data-readiness reports the operation store is unreachable or unhealthy. | Inspect operation-store diagnostics and restore connectivity before re-running the gate. |
| `PAGINATION_LIMIT_EXCEEDED` | API | Requested page size exceeds the locked maximum. | Lower the page size to the documented maximum and re-issue the request. |
| `PHASE0_B9_RELEASE_EVIDENCE_ARCHIVE_INVALID` | Release evidence | Release evidence archive contract is missing required local-only archive metadata or contains invalid refs. | Fix `test/drills/phase0-release-evidence-archive.yaml`, keep refs workspace-relative, and rerun the release gate. |
| `PRECONDITION_FAILED` | API | `If-Match` was weak, malformed, mismatched, or no longer identifies the current resource version. | GET the latest resource representation and retry with its current strong ETag. |
| `PRECONDITION_REQUIRED` | API | A conditional replace, update, or delete omitted the required `If-Match` header. | GET the resource, then retry the mutation with its strong ETag in `If-Match`. |
| `PREDEPLOY_FAILED` | Jobs | Predeploy job exited non-zero and halted rollout. | Inspect the failed job logs, fix the command or image, and retry the deployment. |
| `PREDEPLOY_TIMEOUT` | Jobs | Predeploy job exceeded its configured timeout. | Increase the timeout or make the predeploy command bounded, then retry the deployment. |
| `PRIVATE_NETWORK_UNAVAILABLE` | Runtime | The project/environment network boundary could not be guaranteed. | Restore Docker network provisioning; Midnight will not fall back to the default bridge. |
| `RECONCILE_STEP_FAILED` | Runtime | Reconcile step exhausted its retry budget. | Inspect step diagnostics, fix the underlying cause, and trigger another reconcile. |
| `REDACTION_RULE_INVALID` | Ingress | A redaction rule registration was rejected. | Review the rejected rule and reformat per the redaction rule schema. |
| `REGISTRY_GC_LOCKED` | Registry | Another GC run holds the global registry GC advisory lock. | Another GC run holds the global advisory lock; wait for it to drain before retrying. |
| `REGISTRY_GC_VERIFY_FAILED` | Registry | Post-GC verify pass detected a digest the planner promised to retain. | Halt destructive GC and engage release ops; the post-GC verify probe found a digest that should have been retained. |
| `RELEASE_EVIDENCE_NOT_READY` | API | API-data-readiness reports release evidence has not been built or verified yet. | Run the release-evidence pipeline and re-attempt the gate. |
| `RELEASE_FINGERPRINT_MISMATCH` | Tooling/Backup/ops | On-disk (`docs/release/keys/midnight-release-fingerprint.txt`) and remote (project-website mirror) fingerprint copies disagree, OR `dscverify` reports a fingerprint other than the published one. | Stop and engage Security per `SECURITY.md`. Cross-check `docs/release/keys/midnight-release-fingerprint.txt` against the project-website mirror; do not trust any signed .deb until the discrepancy is resolved. |
| `RELEASE_KMS_UNAVAILABLE` | Tooling/Backup/ops | Release pipeline could not reach the signing KMS or HSM. | Halt the release, follow `docs/runbooks/key-rotation.md` to fall back, and retry once the KMS is reachable. |
| `RELEASE_REVOKED_KEY` | Tooling/Backup/ops | Verifier finds the signing fingerprint listed in a `REVOKED-<fingerprint>.txt` file under `docs/release/keys/`; even a valid signature MUST NOT be trusted. | Do not deploy the release. Wait for the next protected release tag against a fresh fingerprint per `RELEASE_KEY_CUSTODY.md` Step 8. |
| `REQUEST_RATE_LIMITED` | API | An authenticated principal and direct client network tuple exhausted its request budget or stream concurrency cap. | Wait for the `Retry-After` interval before issuing more API requests or opening another stream. |
| `RESOURCE_CONFLICT` | API | Unique constraint conflict on resource creation. | Pick a unique slug/name or address the conflicting record before retrying. |
| `RESOURCE_NOT_FOUND` | API | Resource lookup missed (project, service, etc.). | Verify the resource id; list parent resources to confirm scope. |
| `RESOURCE_VALIDATION_FAILED` | API | Resource creation payload failed validation. | Inspect the structured detail field and correct the offending fields. |
| `RESTORE_ACME_THROTTLED` | Ingress | Progress signal emitted while restore reissue throttles to <=20 certs/hour. | Allow the throttle window to advance; resume after the next slot opens. |
| `RESTORE_PLAN_SERIALIZATION_FAILED` | Backup/ops | Restore plan could not be marshalled or unmarshalled against the locked schemaVersion=2 contract; persisted schemaVersion=1 plans remain readable for upgrade compatibility. | Inspect the restore plan structure, reconcile against the schemaVersion=2 contract, and reissue. |
| `ROLLBACK_IMAGE_UNAVAILABLE` | Registry | Rollback target's recorded image digest cannot be retrieved from any configured registry. | Confirm the recorded digest exists in the embedded mirror or the source registry; if it is gone, the rollback target is lost. |
| `ROUTE_PUBLICATION_FAILED` | Ingress | Route publisher failed to write or validate the dynamic routing configuration. | Inspect the route publisher and Traefik dynamic config directory, then retry route publication after fixing the write or validation error. |
| `ROUTE_RESERVED_PATH` | Ingress | User route shadowed `/.well-known/acme-challenge/*`. | Remove the conflicting user route; reserved paths are owned by the ACME challenge router. |
| `RUNTIME_UNSUPPORTED_OPERATION` | Runtime | Runtime adapter does not support the requested operation. | Use a runtime adapter that supports the requested operation or disable the feature until adapter support lands. |
| `SAFE_EXECUTION_PLAN_INVALID` | Install | Safe execution plan failed validator (id/order/redaction policy). | Inspect the validator output, fix the offending step, and re-emit the plan. |
| `SCHEDULER_QUOTA_EXCEEDED` | Runtime | Per-project per-class scheduler quota was exhausted. | Wait for in-flight jobs to drain, raise the project quota, or shed lower-priority jobs. |
| `SCHEMA_SQL_INVALID` | API | Release evidence reports a schema SQL fixture failed validation. | Fix the offending SQL fixture and re-emit release evidence. |
| `SCHEMA_SQL_UNSUPPORTED` | API | Release evidence rejected a schema SQL fixture that uses unsupported syntax. | Rewrite the fixture using the supported subset and re-emit release evidence. |
| `SCHEMA_STATUS_BLOCKED` | API | Release evidence reports the database schema is in a state the gate refuses to ship. | Inspect the schema-status diagnostics and address the underlying issue before retrying release. |
| `SELF_HEAL_COOLDOWN_ACTIVE` | Runtime | Self-heal restart attempt suppressed by minimum cooldown. | Wait for the cooldown to expire; if instance is unhealthy, escalate via the runbook. |
| `SERVICE_DELETE_BLOCKED` | API | DELETE /services/{id} refused because a dependent record still references the service: a volume with a pending or active attachment, an open pull-request preview, or an unfinished source upload. | Remove the dependent record named in the problem details - an attached volume, an open pull-request preview, or an unfinished source upload - then retry the delete. |
| `SERVICE_DELETE_RUNNING` | API | DELETE /services/{id} refused because the service still has running or in-flight deployment intent (a deployment in `queued`, `building`, or `deploying`, an active deployment whose runtime is still `running`, or a queued/running runtime action). Deleting would strand containers and published routes. | Stop the service's current deployment (or wait for the in-flight one to finish), then delete the service. |
| `SNAPSHOT_IMMUTABLE` | Registry | API caller attempted to mutate a deployment snapshot row. | Snapshots are immutable; create a new deployment instead of mutating the existing snapshot. |
| `SOURCE_ARTIFACT_QUOTA_EXCEEDED` | API/Build | The authenticated workspace's retained logical source-archive budget is exhausted. | Wait for retained source ownership to expire or raise the workspace source-artifact quota. |
| `SOURCE_ARTIFACT_UPLOAD_BACKPRESSURE` | API/Build | The workspace or installation-wide concurrent source-upload byte/count budget is temporarily exhausted. | Wait for an in-flight source upload to finish, then retry after the advertised delay. |
| `STATIC_HANDLER_UNAVAILABLE` | Jobs | Static handler service is unavailable for static-site traffic. | Restart the static handler service and inspect host service logs if it remains unavailable. |
| `STATIC_SITE_BUILD_INVALID` | Jobs | Static-site build output or index configuration is invalid. | Fix the static-site output or index configuration, then rerun the build. |
| `STATIC_SITE_DIST_MISSING` | Jobs | Static-site build did not produce the configured `dist` directory. | Update the build to produce the configured dist directory, then redeploy. |
| `SUPPORT_BUNDLE_BUDGET_EXCEEDED` | Backup/ops | Support bundle exceeded its per-category or total archive byte budget; breakdown is included in the error. | Trim the requested categories, raise the budget knob, or generate per-category bundles instead. |
| `SUPPORT_BUNDLE_REDACTION_FAILED` | Backup/ops | Support bundle redaction could not complete (invalid input shape or duplicate path). | Inspect the redactor diagnostics, fix the offending input, and rerun support-bundle redaction. |
| `SUPPORT_BUNDLE_VERIFY_FAILED` | Backup/ops | Embedded archive manifest SHA-256 disagrees with one or more entry contents; archive may be tampered or truncated. | Regenerate the support bundle and verify its embedded manifest before sharing. |
| `TELEMETRY_OPT_IN_REQUIRED` | Backup/ops | Telemetry payload was rejected because the locked default-off opt-in flag was not set. | Run `midnight feature enable telemetry` (with operator consent) before retrying telemetry uploads. |
| `TELEMETRY_PAYLOAD_PII_DETECTED` | Backup/ops | Telemetry payload field values contained recognized PII or secret shapes (email, IPv4, IPv6, AWS keys, midn_ tokens). | Inspect the rejected payload, remove the offending value, and retry with a sanitized payload. |
| `TIMELINE_QUERY_INVALID` | Ingress | Timeline query filter or cursor failed validation. | Drop the cursor and re-issue the query with valid filter values. |
| `TIME_SYNC_DRIFT` | Install | Host clock drift exceeds the doctor warn/critical thresholds. | Resync host clock with NTP/chrony; rerun `midnight doctor` to confirm. |
| `UNINSTALL_DANGEROUS_PATH` | Install | Uninstall planner refused to plan deletion of a host-OS critical path. | Restrict the uninstall scope to Midnight-managed paths; never target host-OS critical paths. |
| `VARIABLE_REFERENCE_CYCLE` | Variables | Variable reference expansion detected a cycle. | Break the reported reference cycle, then retry variable resolution. |
| `VARIABLE_REFERENCE_FAILED` | Audit | Variable reference resolution failed at runtime (target sealed, missing, or unreadable for the consumer principal). | Inspect the structured detail field; fix the reference target or the consumer's read scope. |
| `VARIABLE_REFERENCE_UNRESOLVED` | Variables | A variable reference names a target that cannot be resolved in the deployment scope. | Create or rename the referenced variable, confirm its scope, and retry the deployment. |
| `VAR_REFERENCE_CYCLE` | Audit | Variable reference graph contains a cycle. | Break the cycle in the offending workspace's reference graph and re-apply the variable set. |
| `VAR_REF_CREATE_FORBIDDEN` | Audit | Cross-tier variable reference rejected by the panel-locked default-deny matrix (previewâ†’prod is permanently denied). | Use a per-environment variable instead, or add an explicit (actor, srcâ†’dst) allowlist row when policy allows it. |
| `VERSION_SKEW_REJECTED` | Runtime | Agent / worker / engine reported a version outside the supported compatibility window in `versions.yaml`. | Upgrade or pin the offending component to a version inside the documented compatibility window in versions.yaml. |
| `VOLUME_ATTACHED` | API | DELETE /volumes/{id} rejected because an attachment in `pending` or `active` state still references the volume. | Detach the volume from every active deployment before deleting it; redeploy the service without the volume attachment first. |
| `VOLUME_QUOTA_UNSUPPORTED` | Runtime | Host filesystem cannot enforce per-project quotas (xfs without prjquota, ext4 without quota mount). | Mount `/var/lib/midnight` on xfs (with prjquota) or ext4 (with quota) and remount. |
| `VOLUME_REPLICAS_UNSUPPORTED` | Deploy gate | Service requested replicas > 1 with a volume attachment; the local Docker driver is single-writer. | Reduce service replicas to 1 before attaching a volume, or remove the volume attachment to scale beyond one replica. |
| `VOLUME_RESIZE_DOWN_UNSUPPORTED` | API | PATCH /volumes/{id} body would shrink the declared size. | Volumes can only be resized UP; create a smaller volume and migrate data manually if you need a smaller backing store. |
| `WEBHOOK_BODY_TOO_LARGE` | API/Webhooks | Signed inbound webhook body exceeds the 25 MiB intake limit. | Verify the sender payload and configuration, then redeliver a smaller payload. |
| `WEBHOOK_DELIVERY_IDENTITY_MISMATCH` | API/Webhooks | One provider/endpoint/delivery identity was presented with different immutable envelope metadata. | Investigate the sender identity conflict; retain the first accepted envelope and redeliver only its exact body and metadata. |
| `WEBHOOK_DELIVERY_ID_INVALID` | API/Webhooks | The GitHub delivery identifier header is missing, malformed, or duplicated. | Configure GitHub to send exactly one valid `X-GitHub-Delivery` header, then redeliver. |
| `WEBHOOK_ENDPOINT_DISABLED` | Deploy gate | Inbound webhook endpoint is administratively disabled (50 cumulative failures or 5 consecutive in 5 min). | Repair the receiver, then re-enable the endpoint via `PATCH /api/v1alpha/webhook-endpoints/{id}` or the dashboard. |
| `WEBHOOK_ENDPOINT_PROVIDER_MISMATCH` | API/Webhooks | The inbound webhook endpoint is registered for a different provider. | Use the configured provider's matching endpoint path and credentials. |
| `WEBHOOK_EVENT_TYPE_INVALID` | API/Webhooks | The GitHub event type header is missing, malformed, or duplicated. | Configure GitHub to send exactly one valid `X-GitHub-Event` header, then redeliver. |
| `WEBHOOK_GITHUB_PAYLOAD_INVALID` | API/Webhooks | A signed GitHub body cannot be parsed as the declared event contract. | Inspect the declared GitHub event and sender configuration; correct the malformed payload before redelivery. |
| `WEBHOOK_INBOX_UNAVAILABLE` | API/Webhooks | A verified payload could not commit to the durable inbox and acceptance audit. | Restore PostgreSQL inbox and audit availability, then allow the provider to redeliver. |
| `WEBHOOK_METHOD_NOT_ALLOWED` | API/Webhooks | Inbound webhook delivery used a method other than POST. | Resend the webhook delivery as an HTTP POST request. |
| `WEBHOOK_PAYLOAD_CORRUPT` | API/Webhooks | A retained verified delivery body no longer matches its stored SHA-256 digest. | Do not redrive; restore the retained payload from a verified backup or submit a new provider delivery. |
| `WEBHOOK_PAYLOAD_INVALID` | API/Webhooks | The planner found a permanent signed-payload invariant failure. | Inspect the signed event contract, correct the permanent payload error, and submit a new provider delivery. |
| `WEBHOOK_PLAN_STORE_FAILED` | API/Webhooks | Immutable per-service targets could not be committed under the delivery lease. | Restore durable inbox writes and allow the delivery planner to retry. |
| `WEBHOOK_REPLAY_REJECTED` | API | Webhook timestamp outside the locked replay window. | Verify clock sync and resign the webhook with a fresh timestamp. |
| `WEBHOOK_ROUTE_ATTEMPTS_EXHAUSTED` | API/Webhooks | The bounded delivery-planning attempt cycle ended after failure or a final lost lease. | Fix the planner dependency, then use the operator redrive route to start a new bounded attempt cycle. |
| `WEBHOOK_ROUTE_FAILED` | API/Webhooks | Temporal source routing failed transiently before immutable targets were stored. | Restore the source-link or database dependency and allow the bounded planner retry to run. |
| `WEBHOOK_SIGNATURE_INVALID` | API | Outbound or inbound HMAC signature verification failed. | Verify the signing secret matches the active key id; rotate if mismatch persists. |
| `WEBHOOK_TARGETS_MISSING` | API/Webhooks | A delivery marked as routed has no immutable target rows. | Inspect planner and inbox consistency; submit a new delivery only after the missing-target invariant is repaired. |
| `WEBHOOK_TARGET_ATTEMPTS_EXHAUSTED` | API/Webhooks | A target exhausted its bounded dispatch budget, including a final lost worker lease. | Fix the downstream dependency, then redrive the dead-letter delivery to start a new target attempt cycle. |
| `WEBHOOK_TARGET_AUTHORITY_CHANGED` | API/Webhooks | Current installation, repository-grant, or service-link authority no longer matches the immutable target. | Confirm the current installation, repository grant, and service link; relink and submit a new delivery if deployment is still intended. |
| `WEBHOOK_TARGET_DEAD_LETTER` | API/Webhooks | The parent delivery contains at least one dead-letter target. | Inspect the dead-letter target reason, correct it, and use the operator redrive route. |
| `WEBHOOK_TARGET_DISPATCH_FAILED` | API/Webhooks | Canonical staged deployment intake failed transiently for one immutable target. | Restore staged-intake dependencies and allow the bounded target retry to continue. |
| `WEBHOOK_TARGET_INVALID` | API/Webhooks | The dispatcher found a permanent immutable-target invariant failure. | Repair the permanent target invariant before submitting a new provider delivery. |
| `WEBHOOK_TARGET_PAYLOAD_CORRUPT` | API/Webhooks | A retained opaque target payload no longer matches its stored SHA-256 digest. | Do not redrive the corrupt target; restore verified retained data or submit a new provider delivery. |
| `WEBHOOK_TARGET_PAYLOAD_INVALID` | API/Webhooks | The retained target payload cannot be decoded by the target dispatcher. | Do not retry the malformed immutable target; correct the planner and submit a new provider delivery. |
| `WEBHOOK_TARGET_SNAPSHOT_MISMATCH` | API/Webhooks | The opaque target payload disagrees with its queryable immutable identity snapshot. | Inspect planner/dispatcher version skew and submit a new delivery only after the immutable snapshot contract is repaired. |
| `WILDCARD_RENEWAL_FAILED` | Ingress | Wildcard certificate renewal failed; investigate provider error. | Inspect ACME provider error detail and re-attempt renewal once the cause is corrected. |
