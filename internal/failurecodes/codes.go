// Package failurecodes defines the additive API error-code registry used by the
// stable error envelope. Codes are additive-only; renaming requires a versioned
// API decision per the panel-locked supply-chain stance.
package failurecodes

// Code is the canonical SCREAMING_SNAKE_CASE token returned in error envelopes
// and other surfaces (CLI, dashboard, audit, webhooks).
type Code string

const (
	// InternalError is the generic non-user-actionable fallback.
	InternalError Code = "INTERNAL_ERROR"
	// NotImplemented signals a method or feature has no Phase 0 surface.
	NotImplemented Code = "NOT_IMPLEMENTED"
	// CapabilityUnavailable signals that a stable operation exists but its
	// required runtime adapter or operator configuration is unavailable on this
	// server. It maps to HTTP 503 rather than implying the route is unimplemented.
	CapabilityUnavailable Code = "CAPABILITY_UNAVAILABLE"
	// MethodNotAllowed signals that a resource exists but does not support the
	// requested HTTP method. Responses using this code include Allow.
	MethodNotAllowed Code = "METHOD_NOT_ALLOWED"
	// GraphQLDeployMustBeSoleMutation rejects durable deploy documents whose
	// top-level mutation selection contains any field besides the deploy.
	GraphQLDeployMustBeSoleMutation Code = "GRAPHQL_DEPLOY_MUST_BE_SOLE_MUTATION"

	// OperationNotFound is returned when an operation lookup misses.
	OperationNotFound Code = "OPERATION_NOT_FOUND"
	// OperationInvalidTransition rejects unsafe operation state transitions.
	OperationInvalidTransition Code = "OPERATION_INVALID_TRANSITION"

	// CursorInvalid covers tampered, expired, or malformed pagination cursors.
	CursorInvalid Code = "CURSOR_INVALID"
	// PaginationLimitExceeded is returned when the requested page size is over the cap.
	PaginationLimitExceeded Code = "PAGINATION_LIMIT_EXCEEDED"

	// AuthTokenHeaderInvalid is returned for unsupported Authorization headers.
	AuthTokenHeaderInvalid Code = "AUTH_TOKEN_HEADER_INVALID"
	// AuthRequiredMissing is returned when production auth is expected but no API token is configured.
	AuthRequiredMissing Code = "AUTH_REQUIRED_MISSING"
	// AuthCredentialsInvalid is returned for a failed local-account login without disclosing which credential failed.
	AuthCredentialsInvalid Code = "AUTH_CREDENTIALS_INVALID"
	// AuthSessionInvalid is returned when a human session is expired, revoked, disabled, or otherwise unusable.
	AuthSessionInvalid Code = "AUTH_SESSION_INVALID"
	// AuthorizationDenied is returned when an authenticated principal lacks the required capability.
	AuthorizationDenied Code = "AUTHORIZATION_DENIED"
	// AuthRateLimited is returned when a public credential-verification budget is exhausted.
	AuthRateLimited Code = "AUTH_RATE_LIMITED"
	// InvitationAccountExists requires the invited account to sign in normally.
	InvitationAccountExists Code = "INVITATION_ACCOUNT_EXISTS"
	// AuthOperatorRecoveryRequired is returned when a route accepts only the
	// installation-wide operator recovery credential and the caller presented
	// a human session instead. Administrator credential recovery is deliberately
	// not reachable from any signed-in account.
	AuthOperatorRecoveryRequired Code = "AUTH_OPERATOR_RECOVERY_REQUIRED"
	// RequestRateLimited is returned when an authenticated API request or stream
	// exceeds its shared admission budget.
	RequestRateLimited Code = "REQUEST_RATE_LIMITED"

	// IdempotencyKeyInvalid is returned when an Idempotency-Key fails validation.
	IdempotencyKeyInvalid Code = "IDEMPOTENCY_KEY_INVALID"
	// IdempotencyKeyRequired is returned when a mutating request omits the key.
	IdempotencyKeyRequired Code = "IDEMPOTENCY_KEY_REQUIRED"
	// IdempotencyKeyReused is returned for a same-key, different-body retry.
	IdempotencyKeyReused Code = "IDEMPOTENCY_KEY_REUSED"
	// IdempotencyKeyReusedDifferentBody surfaces a body-hash mismatch on terminal replay.
	IdempotencyKeyReusedDifferentBody Code = "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY"
	// IdempotencyRequestInProgress is returned for concurrent in-flight duplicates.
	IdempotencyRequestInProgress Code = "IDEMPOTENCY_REQUEST_IN_PROGRESS"

	// ResourceNotFound is returned for missing API resource lookups.
	ResourceNotFound Code = "RESOURCE_NOT_FOUND"
	// ResourceValidationFailed is returned for invalid resource creation payloads.
	ResourceValidationFailed Code = "RESOURCE_VALIDATION_FAILED"
	// ResourceConflict signals duplicate-name or other unique constraint conflicts.
	ResourceConflict Code = "RESOURCE_CONFLICT"
	// PreconditionRequired signals that a conditional mutation omitted If-Match.
	PreconditionRequired Code = "PRECONDITION_REQUIRED"
	// PreconditionFailed signals that If-Match was stale, weak, malformed, or mismatched.
	PreconditionFailed Code = "PRECONDITION_FAILED"
	// SourceArtifactQuotaExceeded reports that a workspace's retained logical
	// source-archive budget is exhausted.
	SourceArtifactQuotaExceeded Code = "SOURCE_ARTIFACT_QUOTA_EXCEEDED"
	// SourceArtifactUploadBackpressure reports a temporary workspace or
	// installation-wide source-upload concurrency limit.
	SourceArtifactUploadBackpressure Code = "SOURCE_ARTIFACT_UPLOAD_BACKPRESSURE"

	// WebhookSignatureInvalid surfaces HMAC verification failures.
	WebhookSignatureInvalid Code = "WEBHOOK_SIGNATURE_INVALID"
	// WebhookMethodNotAllowed rejects webhook delivery transports other than POST.
	WebhookMethodNotAllowed Code = "WEBHOOK_METHOD_NOT_ALLOWED"
	// WebhookReplayRejected surfaces timestamp window violations.
	WebhookReplayRejected Code = "WEBHOOK_REPLAY_REJECTED"
	// WebhookDeliveryIdentityMismatch rejects one provider delivery identity
	// presented with different immutable envelope metadata.
	WebhookDeliveryIdentityMismatch Code = "WEBHOOK_DELIVERY_IDENTITY_MISMATCH"
	// WebhookInboxUnavailable reports that verified payload admission could not
	// commit to the durable inbox.
	WebhookInboxUnavailable Code = "WEBHOOK_INBOX_UNAVAILABLE"
	// WebhookGitHubPayloadInvalid marks a signed GitHub body that cannot be
	// parsed into the declared event contract.
	WebhookGitHubPayloadInvalid Code = "WEBHOOK_GITHUB_PAYLOAD_INVALID"
	// WebhookPayloadInvalid is the planner-level permanent invalid-payload
	// fallback.
	WebhookPayloadInvalid Code = "WEBHOOK_PAYLOAD_INVALID"
	// WebhookPayloadCorrupt marks a retained delivery body whose stored digest
	// no longer matches.
	WebhookPayloadCorrupt Code = "WEBHOOK_PAYLOAD_CORRUPT"
	// WebhookRouteFailed marks a transient delivery-planning failure.
	WebhookRouteFailed Code = "WEBHOOK_ROUTE_FAILED"
	// WebhookRouteAttemptsExhausted marks a crashed or repeatedly failing
	// planner after its bounded attempt budget.
	WebhookRouteAttemptsExhausted Code = "WEBHOOK_ROUTE_ATTEMPTS_EXHAUSTED"
	// WebhookPlanStoreFailed marks failure to persist immutable targets.
	WebhookPlanStoreFailed Code = "WEBHOOK_PLAN_STORE_FAILED"
	// WebhookTargetPayloadInvalid marks a target payload that cannot be decoded.
	WebhookTargetPayloadInvalid Code = "WEBHOOK_TARGET_PAYLOAD_INVALID"
	// WebhookTargetPayloadCorrupt marks a retained target payload whose digest
	// no longer matches.
	WebhookTargetPayloadCorrupt Code = "WEBHOOK_TARGET_PAYLOAD_CORRUPT"
	// WebhookTargetSnapshotMismatch marks disagreement between an opaque
	// dispatch payload and its queryable immutable identity snapshot.
	WebhookTargetSnapshotMismatch Code = "WEBHOOK_TARGET_SNAPSHOT_MISMATCH"
	// WebhookTargetAuthorityChanged records deliberate suppression after source
	// grant or link authority changed.
	WebhookTargetAuthorityChanged Code = "WEBHOOK_TARGET_AUTHORITY_CHANGED"
	// WebhookTargetInvalid is the permanent target-invariant fallback.
	WebhookTargetInvalid Code = "WEBHOOK_TARGET_INVALID"
	// WebhookTargetDispatchFailed marks a retryable downstream intake failure.
	WebhookTargetDispatchFailed Code = "WEBHOOK_TARGET_DISPATCH_FAILED"
	// WebhookTargetAttemptsExhausted marks a target whose automatic delivery
	// budget ended while a worker lease was lost.
	WebhookTargetAttemptsExhausted Code = "WEBHOOK_TARGET_ATTEMPTS_EXHAUSTED"
	// WebhookTargetDeadLetter is the parent aggregate reason when any target
	// remains dead-lettered.
	WebhookTargetDeadLetter Code = "WEBHOOK_TARGET_DEAD_LETTER"
	// WebhookTargetsMissing marks an impossible routed delivery with no target
	// rows.
	WebhookTargetsMissing Code = "WEBHOOK_TARGETS_MISSING"

	// AuditChainBroken reports detected tamper / chain breaks.
	AuditChainBroken Code = "AUDIT_CHAIN_BROKEN"
	// NotificationDeliveryFailed reports native notification delivery failures.
	NotificationDeliveryFailed Code = "NOTIFICATION_DELIVERY_FAILED"

	// DockerEngineUnsupported reports the Docker Engine API is below v1.43 or otherwise unsupported.
	DockerEngineUnsupported Code = "DOCKER_ENGINE_UNSUPPORTED"
	// DeployJobEnqueueFailed reports the control plane could not enqueue a deploy job.
	DeployJobEnqueueFailed Code = "DEPLOY_JOB_ENQUEUE_FAILED"
	// AgentClockSkew reports the agent or ACME clock skew exceeds the allowed bound.
	AgentClockSkew Code = "AGENT_CLOCK_SKEW"
	// ImageSignatureInvalid reports a cosign verification failure on image pull.
	ImageSignatureInvalid Code = "IMAGE_SIGNATURE_INVALID"
	// BackupOffhostMissing is the Alpha warning when no off-host backup destination is configured.
	BackupOffhostMissing Code = "BACKUP_OFFHOST_MISSING"

	// S02 — installer / ops.

	// DaemonJSONConflict reports operator-managed Docker daemon.json conflicts with the Midnight baseline.
	DaemonJSONConflict Code = "DAEMON_JSON_CONFLICT"
	// DiskPressureCritical reports doctor disk-pressure breach above the critical threshold.
	DiskPressureCritical Code = "DISK_PRESSURE_CRITICAL"
	// MemoryHeadroomLow reports memory headroom below the doctor warning threshold.
	MemoryHeadroomLow Code = "MEMORY_HEADROOM_LOW"
	// TimeSyncDrift reports host clock drift exceeds the doctor thresholds.
	TimeSyncDrift Code = "TIME_SYNC_DRIFT"
	// IngressPortBound reports a required ingress port (80/443) is already bound.
	IngressPortBound Code = "INGRESS_PORT_BOUND"
	// UninstallDangerousPath reports the uninstall planner refused to plan deletion of a host-OS critical path.
	UninstallDangerousPath Code = "UNINSTALL_DANGEROUS_PATH"
	// SafeExecutionPlanInvalid reports a safe-execution plan failed validator (id/order/redaction policy).
	SafeExecutionPlanInvalid Code = "SAFE_EXECUTION_PLAN_INVALID"

	// S04 — runtime / agent.

	// AgentHeartbeatReplay reports a heartbeat sequence already accepted within the replay window.
	AgentHeartbeatReplay Code = "AGENT_HEARTBEAT_REPLAY"
	// AgentHeartbeatExpired reports a heartbeat fell outside the accepted clock-skew window.
	AgentHeartbeatExpired Code = "AGENT_HEARTBEAT_EXPIRED"
	// AgentHeartbeatTampered reports a heartbeat HMAC failed to verify.
	AgentHeartbeatTampered Code = "AGENT_HEARTBEAT_TAMPERED"
	// AgentStatusReportReplay reports a status sequence already accepted.
	AgentStatusReportReplay Code = "AGENT_STATUS_REPORT_REPLAY"
	// AgentStatusReportExpired reports a status observation outside the accepted clock-skew window.
	AgentStatusReportExpired Code = "AGENT_STATUS_REPORT_EXPIRED"
	// AgentStatusReportTampered reports a status-report HMAC failed to verify.
	AgentStatusReportTampered Code = "AGENT_STATUS_REPORT_TAMPERED"
	// AgentRuntimeUnavailable reports the host's container runtime (Docker
	// daemon) is unreachable; the agent enters backoff but does not crash-loop.
	AgentRuntimeUnavailable Code = "AGENT_RUNTIME_UNAVAILABLE"
	// AgentNotConfigured reports that the AI agent has no provider API key,
	// so chat sends are refused with a 503 instead of a generic 500.
	AgentNotConfigured Code = "AGENT_NOT_CONFIGURED"
	// PrivateNetworkUnavailable reports that Midnight could not guarantee the
	// per-project/environment network boundary. Scheduling fails closed rather
	// than silently attaching the workload to Docker's default bridge.
	PrivateNetworkUnavailable Code = "PRIVATE_NETWORK_UNAVAILABLE"
	// CGroupV2Required reports the host runs cgroup v1; ADR-005 hard-requires
	// cgroup v2 unified hierarchy.
	CGroupV2Required Code = "CGROUP_V2_REQUIRED"
	// DockerDaemonBaselineConflict reports the live Docker daemon configuration
	// diverges from the FEATURE_DAEMON_JSON_BASELINE locked baseline (overlay2,
	// live-restore, no-new-privileges, json-file log driver, init=true).
	DockerDaemonBaselineConflict Code = "DOCKER_DAEMON_BASELINE_CONFLICT"
	// VersionSkewRejected reports an agent / worker / engine outside the
	// supported compatibility window from versions.yaml.
	VersionSkewRejected Code = "VERSION_SKEW_REJECTED"
	// ContainerNameConflict reports a stale Docker container collides with the
	// deterministic name allocator and is not safe to garbage-collect.
	ContainerNameConflict Code = "CONTAINER_NAME_CONFLICT"
	// ContainerSpecInvalid reports a container spec failed validator hardening checks.
	ContainerSpecInvalid Code = "CONTAINER_SPEC_INVALID"
	// CapNotPermitted reports a container spec requested a capability outside the operator allowlist.
	CapNotPermitted Code = "CAP_NOT_PERMITTED"
	// HostPortNotPermitted reports a container spec attempted to bind a privileged host port without explicit allow.
	HostPortNotPermitted Code = "HOST_PORT_NOT_PERMITTED"
	// ContainerLabelMissing reports a container spec lacked one or more required midnight labels.
	ContainerLabelMissing Code = "CONTAINER_LABEL_MISSING"
	// ReconcileStepFailed reports a reconcile step exhausted its retry budget.
	ReconcileStepFailed Code = "RECONCILE_STEP_FAILED"
	// SchedulerQuotaExceeded reports a project per-class scheduling quota was exhausted.
	SchedulerQuotaExceeded Code = "SCHEDULER_QUOTA_EXCEEDED"
	// SelfHealCooldownActive reports a restart attempt was suppressed by the self-heal cooldown.
	SelfHealCooldownActive Code = "SELF_HEAL_COOLDOWN_ACTIVE"

	// S06 — ingress / ACME / logs / DX.

	// ACMECAAForbidden is returned when CAA preflight refuses ACME submission.
	ACMECAAForbidden Code = "ACME_CAA_FORBIDDEN"
	// ACMERateBudgetExhausted is returned when the per-registered-domain weekly budget is exceeded.
	ACMERateBudgetExhausted Code = "ACME_RATE_BUDGET_EXHAUSTED"
	// ACMERestoreBudgetExhausted is returned when an ACME restore queue exceeds its hourly throttle.
	ACMERestoreBudgetExhausted Code = "ACME_RESTORE_BUDGET_EXHAUSTED"
	// ACMEHTTP01Unreachable is returned when the HTTP-01 reachability gate fails.
	ACMEHTTP01Unreachable Code = "ACME_HTTP01_UNREACHABLE"
	// ACMEDNSMismatch is returned when DNS validation observes a TXT mismatch.
	ACMEDNSMismatch Code = "ACME_DNS_MISMATCH"
	// ACMERateLimited is returned when the upstream CA rate-limits issuance.
	ACMERateLimited Code = "ACME_RATE_LIMITED"
	// ACMEOrderFailedPermanent is returned when an ACME order transitions to a terminal failure.
	ACMEOrderFailedPermanent Code = "ACME_ORDER_FAILED_PERMANENT"
	// ACMEOrderFailedRetryable is returned when an ACME order may be retried.
	ACMEOrderFailedRetryable Code = "ACME_ORDER_FAILED_RETRYABLE"
	// ACMECertificateExpired is returned when a certificate is past its NotAfter timestamp.
	ACMECertificateExpired Code = "ACME_CERTIFICATE_EXPIRED"
	// ACMECertificateExpiringCritical is returned when a certificate expires within 7 days.
	ACMECertificateExpiringCritical Code = "ACME_CERTIFICATE_EXPIRING_CRITICAL"
	// ACMECertificateExpiringWarn is returned when a certificate expires within 30 days.
	ACMECertificateExpiringWarn Code = "ACME_CERTIFICATE_EXPIRING_WARN"
	// RestoreACMEThrottled marks restore reissue progress (not failure).
	RestoreACMEThrottled Code = "RESTORE_ACME_THROTTLED"
	// DNSPropagationTimeout is returned when DNS-01 propagation does not settle in time.
	DNSPropagationTimeout Code = "DNS_PROPAGATION_TIMEOUT"
	// IngressPort80Unreachable is returned when the external prober cannot reach :80.
	IngressPort80Unreachable Code = "INGRESS_PORT80_UNREACHABLE"
	// IngressReloadThrash is returned when Traefik reload churn exceeds the thrash budget.
	IngressReloadThrash Code = "INGRESS_RELOAD_THRASH"
	// RouteReservedPath is returned when a user route shadows /.well-known/acme-challenge/*.
	RouteReservedPath Code = "ROUTE_RESERVED_PATH"
	// RoutePublicationFailed reports a failed route publication or missing route publisher.
	RoutePublicationFailed Code = "ROUTE_PUBLICATION_FAILED"
	// WildcardRenewalFailed is returned when wildcard certificate renewal fails.
	WildcardRenewalFailed Code = "WILDCARD_RENEWAL_FAILED"
	// LogQueryInvalid is returned when a log query filter or cursor fails validation.
	LogQueryInvalid Code = "LOG_QUERY_INVALID"
	// LogStoreUnavailable is returned when a real log backing store is not configured or cannot be read.
	LogStoreUnavailable Code = "LOG_STORE_UNAVAILABLE"
	// TimelineQueryInvalid is returned when a timeline query filter or cursor fails validation.
	TimelineQueryInvalid Code = "TIMELINE_QUERY_INVALID"
	// RedactionRuleInvalid is returned when a redaction rule registration is rejected.
	RedactionRuleInvalid Code = "REDACTION_RULE_INVALID"

	// S07 — support / backup.

	// SupportBundleBudgetExceeded is returned when a support bundle exceeds its byte budget.
	SupportBundleBudgetExceeded Code = "SUPPORT_BUNDLE_BUDGET_EXCEEDED"
	// BackupVerifyChecksumMismatch is returned when one or more backup artifacts fail SHA-256 verification.
	BackupVerifyChecksumMismatch Code = "BACKUP_VERIFY_CHECKSUM_MISMATCH"
	// BackupDestinationWriteFailed is returned when a configured backup destination rejects a write.
	BackupDestinationWriteFailed Code = "BACKUP_DESTINATION_WRITE_FAILED"

	// Phase 1 / Sprint 1 finish — bootstrap admin first-login MFA.

	// BootstrapTokenConsumed is returned when an admin first-login
	// step is invoked after the one-shot bootstrap-token has already
	// been consumed (0.1-EC5).
	BootstrapTokenConsumed Code = "BOOTSTRAP_TOKEN_CONSUMED"
	// MFARequired is returned when an actor must complete the TOTP
	// enrollment step before the requested operation is permitted.
	MFARequired Code = "MFA_REQUIRED"
	// MFAInvalidCode is returned when a supplied TOTP code does not
	// verify against the stored secret within the allowed skew.
	MFAInvalidCode Code = "MFA_INVALID_CODE"

	// Phase 1 / Sprint 1 finish — §1.8 off-host backup.

	// BackupStale is raised when the last successful backup is older
	// than the configurable threshold (default 26h).
	BackupStale Code = "BACKUP_STALE"

	// BackupCredentialsMissing is returned when the backup
	// credential resolver finds neither the env-var path nor a
	// sealed-variable reference configured. The runtime refuses to
	// upload to the off-host destination rather than silently
	// degrading to anonymous writes.
	BackupCredentialsMissing Code = "BACKUP_CREDENTIALS_MISSING"
	// BackupCredentialsUnauthorized is returned when the backup
	// credential resolver finds a sealed-variable reference but the
	// consumer principal does not have read scope on the referenced
	// variable, or the reference resolves to a missing/forbidden
	// target.
	BackupCredentialsUnauthorized Code = "BACKUP_CREDENTIALS_UNAUTHORIZED"

	// Phase 1 / Sprint 1 finish — §1.9 volumes.

	// VolumeQuotaUnsupported is returned when prjquota enforcement
	// cannot be guaranteed on the host filesystem (xfs without
	// `prjquota`, ext4 without `quota` mount option). The agent
	// refuses volume create rather than silently allowing unbounded
	// growth.
	VolumeQuotaUnsupported Code = "VOLUME_QUOTA_UNSUPPORTED"
	// VolumeReplicasUnsupported is returned when a deploy spec
	// declares replicas > 1 AND requests a volume attachment. The
	// local Docker driver is single-writer, mirroring Railway's
	// constraint, so the scheduler refuses to spread one volume
	// across multiple replicas rather than risk data corruption.
	VolumeReplicasUnsupported Code = "VOLUME_REPLICAS_UNSUPPORTED"
	// VolumeResizeDownUnsupported rejects PATCH /volumes/{id} bodies
	// that would shrink the declared size. Resize UP is allowed.
	VolumeResizeDownUnsupported Code = "VOLUME_RESIZE_DOWN_UNSUPPORTED"
	// VolumeAttached rejects DELETE /volumes/{id} when an attachment
	// row in the pending or active state still references the volume.
	VolumeAttached Code = "VOLUME_ATTACHED"
	// ServiceDeleteRunning rejects DELETE /services/{id} while the service
	// still has running or in-flight deployment intent. Deleting then would
	// leave containers and published routes behind with nothing left in the
	// control plane that points at them.
	ServiceDeleteRunning Code = "SERVICE_DELETE_RUNNING"
	// ServiceDeleteBlocked rejects DELETE /services/{id} when a dependent
	// record that the delete refuses to discard on the caller's behalf still
	// references the service (an attached volume, an open pull-request
	// preview, or an unfinished source upload).
	ServiceDeleteBlocked Code = "SERVICE_DELETE_BLOCKED"
	// DeploySuperseded is the typed reason code recorded on the
	// loser side of a single-writer arbitration race (one ACTIVE,
	// rest SUPERSEDED).
	DeploySuperseded Code = "DEPLOY_SUPERSEDED"
	// DeploySourceRefUnsupported rejects source-ref deploy intake until
	// source-build-registry writes immutable digest candidates.
	DeploySourceRefUnsupported Code = "DEPLOY_SOURCE_REF_UNSUPPORTED"
	// DeployCancelQueueUnavailable rejects queued deploy cancellation when
	// the queue store cannot atomically cancel the job before lease.
	DeployCancelQueueUnavailable Code = "DEPLOY_CANCEL_QUEUE_UNAVAILABLE"
	// DeployCancelUnsafe rejects tenant cancellation when no pending durable
	// work can be withdrawn and no leased worker can observe a safe point.
	DeployCancelUnsafe Code = "DEPLOY_CANCEL_UNSAFE"
	// DeployCanceledBeforePromotion records a running deploy stopped at the
	// worker safe point after readiness checks and before traffic promotion.
	DeployCanceledBeforePromotion Code = "DEPLOY_CANCELED_BEFORE_PROMOTION"
	// DeployCandidateExited records a deploy candidate whose container
	// stopped before it ever became ready. Waiting cannot fix it, so the
	// rollout fails immediately with the exit code and log tail.
	DeployCandidateExited Code = "DEPLOY_CANDIDATE_EXITED"
	// DeployCandidateRestartLoop records a deploy candidate the runtime
	// restart policy keeps resurrecting because it exits on every start.
	DeployCandidateRestartLoop Code = "DEPLOY_CANDIDATE_RESTART_LOOP"
	// PredeployFailed records a one-off pre-deploy command that reached a
	// terminal non-zero exit before the application candidate was scheduled.
	PredeployFailed Code = "PREDEPLOY_FAILED"
	// PredeployTimeout records a one-off pre-deploy command stopped after its
	// bounded execution window elapsed.
	PredeployTimeout Code = "PREDEPLOY_TIMEOUT"

	// VariableReferenceUnresolved reports a deploy operation refused to
	// schedule because at least one `${{...}}` reference in the resolved
	// variable graph pointed at a missing target. The detail field
	// identifies the offending (service, name) tuple.
	VariableReferenceUnresolved Code = "VARIABLE_REFERENCE_UNRESOLVED"
	// VariableReferenceCycle reports a deploy operation refused to
	// schedule because the resolved variable graph forms a cycle. The
	// detail field includes the visit-stack path that produced the cycle.
	VariableReferenceCycle Code = "VARIABLE_REFERENCE_CYCLE"

	// Phase 1 / Sprint 1 finish — §1.15 image-signature verification.

	// ImageSignatureRequired is returned when an agent runtime is
	// configured with verify_signatures=true and the requested image
	// is unsigned (no cosign signature present in the registry).
	ImageSignatureRequired Code = "IMAGE_SIGNATURE_REQUIRED"

	// Phase 1 / Blocker 3 — cosign fork-exec verifier
	// (agile/handoff/governance/COSIGN_SDK_BOUNDARY.md).

	// ImageSignatureVerifyBinaryMissing is returned when the agent is
	// configured with verify_signatures=true but the cosign binary is
	// not present at the configured cosign_binary_path. The agent
	// refuses to enable signature enforcement rather than silently
	// downgrading to no-verify.
	ImageSignatureVerifyBinaryMissing Code = "IMAGE_SIGNATURE_VERIFY_BINARY_MISSING"
	// ImageSignatureVerifyTimeout is returned when a `cosign verify`
	// fork-exec invocation exceeds the configured timeout budget
	// (default 30s, tunable via cosign_verify_timeout_seconds in agent
	// config).
	ImageSignatureVerifyTimeout Code = "IMAGE_SIGNATURE_VERIFY_TIMEOUT"
	// ImageSignatureVerifyTransparencyFailed is returned when cosign
	// reports a Sigstore Rekor / Fulcio transparency-log failure
	// (missing CT entry, transparency-log signature invalid, or
	// certificate identity mismatch). It is split out from
	// IMAGE_SIGNATURE_INVALID so SIEMs can page security on transparency-
	// log breaches separately from "wrong signing key" failures.
	ImageSignatureVerifyTransparencyFailed Code = "IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED"

	// Phase 1 / v0.1.0-alpha greenlight blocker (4) — first-protected-
	// release custody execution. See agile/handoff/governance/
	// RELEASE_KEY_CUSTODY.md (Steps 7 and 8) and docs/release/keys/.

	// ReleaseFingerprintMismatch is returned when the on-disk
	// fingerprint copy in docs/release/keys/midnight-release-
	// fingerprint.txt disagrees with the project-website mirror at
	// verify time. Operators run a `diff` between the two sources
	// before trusting any signed .deb (RELEASE_KEY_CUSTODY.md Step 7).
	ReleaseFingerprintMismatch Code = "RELEASE_FINGERPRINT_MISMATCH"
	// ReleaseRevokedKey is returned when the verifier finds the
	// signing fingerprint listed in a REVOKED-<fingerprint>.txt file
	// under docs/release/keys/. Even a valid signature MUST NOT be
	// trusted in this case (RELEASE_KEY_CUSTODY.md Step 8).
	ReleaseRevokedKey Code = "RELEASE_REVOKED_KEY"

	// Scaffold merge — additive codes referenced by merged packages (registry GC, support bundle, telemetry).
	AlertMetadataInvalid           Code = "ALERT_METADATA_INVALID"
	RestorePlanSerializationFailed Code = "RESTORE_PLAN_SERIALIZATION_FAILED"
	SupportBundleRedactionFailed   Code = "SUPPORT_BUNDLE_REDACTION_FAILED"
	SupportBundleVerifyFailed      Code = "SUPPORT_BUNDLE_VERIFY_FAILED"
	TelemetryOptInRequired         Code = "TELEMETRY_OPT_IN_REQUIRED"
	TelemetryPayloadPIIDetected    Code = "TELEMETRY_PAYLOAD_PII_DETECTED"
	RollbackImageUnavailable       Code = "ROLLBACK_IMAGE_UNAVAILABLE"
	RuntimeUnsupportedOperation    Code = "RUNTIME_UNSUPPORTED_OPERATION"
	SnapshotImmutable              Code = "SNAPSHOT_IMMUTABLE"
	RegistryGCVerifyFailed         Code = "REGISTRY_GC_VERIFY_FAILED"
	RegistryGCLocked               Code = "REGISTRY_GC_LOCKED"
)

var registered = map[Code]struct{}{
	InternalError:                          {},
	NotImplemented:                         {},
	CapabilityUnavailable:                  {},
	MethodNotAllowed:                       {},
	GraphQLDeployMustBeSoleMutation:        {},
	OperationNotFound:                      {},
	OperationInvalidTransition:             {},
	CursorInvalid:                          {},
	PaginationLimitExceeded:                {},
	AuthTokenHeaderInvalid:                 {},
	AuthRequiredMissing:                    {},
	AuthCredentialsInvalid:                 {},
	AuthSessionInvalid:                     {},
	AuthorizationDenied:                    {},
	AuthRateLimited:                        {},
	InvitationAccountExists:                {},
	AuthOperatorRecoveryRequired:           {},
	RequestRateLimited:                     {},
	IdempotencyKeyInvalid:                  {},
	IdempotencyKeyRequired:                 {},
	IdempotencyKeyReused:                   {},
	IdempotencyKeyReusedDifferentBody:      {},
	IdempotencyRequestInProgress:           {},
	ResourceNotFound:                       {},
	ResourceValidationFailed:               {},
	ResourceConflict:                       {},
	PreconditionRequired:                   {},
	PreconditionFailed:                     {},
	SourceArtifactQuotaExceeded:            {},
	SourceArtifactUploadBackpressure:       {},
	WebhookSignatureInvalid:                {},
	WebhookMethodNotAllowed:                {},
	WebhookReplayRejected:                  {},
	WebhookDeliveryIdentityMismatch:        {},
	WebhookInboxUnavailable:                {},
	WebhookGitHubPayloadInvalid:            {},
	WebhookPayloadInvalid:                  {},
	WebhookPayloadCorrupt:                  {},
	WebhookRouteFailed:                     {},
	WebhookRouteAttemptsExhausted:          {},
	WebhookPlanStoreFailed:                 {},
	WebhookTargetPayloadInvalid:            {},
	WebhookTargetPayloadCorrupt:            {},
	WebhookTargetSnapshotMismatch:          {},
	WebhookTargetAuthorityChanged:          {},
	WebhookTargetInvalid:                   {},
	WebhookTargetDispatchFailed:            {},
	WebhookTargetAttemptsExhausted:         {},
	WebhookTargetDeadLetter:                {},
	WebhookTargetsMissing:                  {},
	AuditChainBroken:                       {},
	NotificationDeliveryFailed:             {},
	DockerEngineUnsupported:                {},
	DeployJobEnqueueFailed:                 {},
	AgentClockSkew:                         {},
	ImageSignatureInvalid:                  {},
	BackupOffhostMissing:                   {},
	DaemonJSONConflict:                     {},
	DiskPressureCritical:                   {},
	MemoryHeadroomLow:                      {},
	TimeSyncDrift:                          {},
	IngressPortBound:                       {},
	UninstallDangerousPath:                 {},
	SafeExecutionPlanInvalid:               {},
	AgentHeartbeatReplay:                   {},
	AgentHeartbeatExpired:                  {},
	AgentHeartbeatTampered:                 {},
	AgentStatusReportReplay:                {},
	AgentStatusReportExpired:               {},
	AgentStatusReportTampered:              {},
	AgentRuntimeUnavailable:                {},
	AgentNotConfigured:                     {},
	PrivateNetworkUnavailable:              {},
	CGroupV2Required:                       {},
	DockerDaemonBaselineConflict:           {},
	VersionSkewRejected:                    {},
	ContainerNameConflict:                  {},
	ContainerSpecInvalid:                   {},
	CapNotPermitted:                        {},
	HostPortNotPermitted:                   {},
	ContainerLabelMissing:                  {},
	ReconcileStepFailed:                    {},
	SchedulerQuotaExceeded:                 {},
	SelfHealCooldownActive:                 {},
	ACMECAAForbidden:                       {},
	ACMERateBudgetExhausted:                {},
	ACMERestoreBudgetExhausted:             {},
	ACMEHTTP01Unreachable:                  {},
	ACMEDNSMismatch:                        {},
	ACMERateLimited:                        {},
	ACMEOrderFailedPermanent:               {},
	ACMEOrderFailedRetryable:               {},
	ACMECertificateExpired:                 {},
	ACMECertificateExpiringCritical:        {},
	ACMECertificateExpiringWarn:            {},
	RestoreACMEThrottled:                   {},
	DNSPropagationTimeout:                  {},
	IngressPort80Unreachable:               {},
	IngressReloadThrash:                    {},
	RouteReservedPath:                      {},
	RoutePublicationFailed:                 {},
	WildcardRenewalFailed:                  {},
	LogQueryInvalid:                        {},
	LogStoreUnavailable:                    {},
	TimelineQueryInvalid:                   {},
	RedactionRuleInvalid:                   {},
	SupportBundleBudgetExceeded:            {},
	BackupVerifyChecksumMismatch:           {},
	BackupDestinationWriteFailed:           {},
	BootstrapTokenConsumed:                 {},
	MFARequired:                            {},
	MFAInvalidCode:                         {},
	BackupStale:                            {},
	BackupCredentialsMissing:               {},
	BackupCredentialsUnauthorized:          {},
	VolumeQuotaUnsupported:                 {},
	VolumeReplicasUnsupported:              {},
	VolumeResizeDownUnsupported:            {},
	VolumeAttached:                         {},
	ServiceDeleteRunning:                   {},
	ServiceDeleteBlocked:                   {},
	DeploySuperseded:                       {},
	DeploySourceRefUnsupported:             {},
	DeployCancelQueueUnavailable:           {},
	DeployCancelUnsafe:                     {},
	DeployCanceledBeforePromotion:          {},
	DeployCandidateExited:                  {},
	DeployCandidateRestartLoop:             {},
	PredeployFailed:                        {},
	PredeployTimeout:                       {},
	VariableReferenceUnresolved:            {},
	VariableReferenceCycle:                 {},
	ImageSignatureRequired:                 {},
	ImageSignatureVerifyBinaryMissing:      {},
	ImageSignatureVerifyTimeout:            {},
	ImageSignatureVerifyTransparencyFailed: {},
	ReleaseFingerprintMismatch:             {},
	ReleaseRevokedKey:                      {},
	AlertMetadataInvalid:                   {},
	RestorePlanSerializationFailed:         {},
	SupportBundleRedactionFailed:           {},
	SupportBundleVerifyFailed:              {},
	TelemetryOptInRequired:                 {},
	TelemetryPayloadPIIDetected:            {},
	RollbackImageUnavailable:               {},
	RuntimeUnsupportedOperation:            {},
	SnapshotImmutable:                      {},
	RegistryGCVerifyFailed:                 {},
	RegistryGCLocked:                       {},
}

// IsRegistered reports whether the supplied code is registered in the local taxonomy.
func IsRegistered(code Code) bool {
	_, ok := registered[code]
	return ok
}

// RecoveryHint returns a short operator-facing hint for the supplied code,
// or the empty string for an unregistered code.
func RecoveryHint(code Code) string {
	return recoveryHints[code]
}

// DocsURL returns a stable docs anchor URL for the supplied code, falling back to InternalError when unknown.
func DocsURL(code Code) string {
	if !IsRegistered(code) {
		code = InternalError
	}
	return "https://midnight.dev/docs/errors#" + string(code)
}

// All returns every registered code in deterministic insertion order.
func All() []Code {
	out := make([]Code, 0, len(registered))
	for _, c := range orderedCodes {
		out = append(out, c)
	}
	return out
}

// recoveryHints carries the short operator-facing hints surfaced via
// RecoveryHint. The strings here mirror docs/error-codes.md.
var recoveryHints = map[Code]string{
	InternalError:                          "File an issue with the request id; operators consult the support bundle.",
	NotImplemented:                         "Wait for the owning lane to land the implementation; track with the linked story id.",
	CapabilityUnavailable:                  "Configure or restore the capability named in the problem details, then retry the operation.",
	MethodNotAllowed:                       "Retry the resource using one of the HTTP methods listed in the Allow response header.",
	GraphQLDeployMustBeSoleMutation:        "Send the durable deploy as the only top-level mutation field, then retry.",
	OperationNotFound:                      "Verify the operation id and re-list operations to confirm it still exists.",
	OperationInvalidTransition:             "Refetch the operation, then issue a transition allowed from the current state.",
	CursorInvalid:                          "Drop the cursor and re-list from the start.",
	PaginationLimitExceeded:                "Lower the page size to the documented maximum and re-issue the request.",
	AuthTokenHeaderInvalid:                 "Resend the request with `Authorization: Bearer <midn_…>`.",
	AuthRequiredMissing:                    "Configure `MIDNIGHT_API_BEARER_TOKEN` or pass `--api-bearer-token` before issuing production mutations.",
	AuthCredentialsInvalid:                 "Verify the account credentials and multi-factor code, then retry.",
	AuthSessionInvalid:                     "Sign in again; the session may have expired or been revoked.",
	AuthorizationDenied:                    "Ask a workspace or project administrator for the capability required by this action.",
	AuthRateLimited:                        "Wait for the Retry-After interval before attempting to sign in again.",
	InvitationAccountExists:                "Sign in with the existing account and accept the invitation. Its password was not changed.",
	AuthOperatorRecoveryRequired:           "Retry with the installation's `MIDNIGHT_API_BEARER_TOKEN`; a signed-in account cannot reset an administrator credential.",
	RequestRateLimited:                     "Wait for the Retry-After interval before issuing more API requests or opening another stream.",
	IdempotencyKeyInvalid:                  "Issue a new Idempotency-Key matching the documented format and retry.",
	IdempotencyKeyRequired:                 "Add an `Idempotency-Key` header matching the documented format and retry.",
	IdempotencyKeyReused:                   "Use a fresh Idempotency-Key for the new request payload.",
	IdempotencyKeyReusedDifferentBody:      "Reuse the original request body or issue a new Idempotency-Key.",
	IdempotencyRequestInProgress:           "Wait for the in-flight request to settle, then poll its operation.",
	ResourceNotFound:                       "Verify the resource id; list parent resources to confirm scope.",
	ResourceValidationFailed:               "Inspect the structured detail field and correct the offending fields.",
	ResourceConflict:                       "Pick a unique slug/name or address the conflicting record before retrying.",
	PreconditionRequired:                   "GET the resource, then retry the mutation with its strong ETag in If-Match.",
	PreconditionFailed:                     "GET the latest resource representation and retry with its current strong ETag.",
	SourceArtifactQuotaExceeded:            "Wait for retained source ownership to expire or raise the workspace source-artifact quota.",
	SourceArtifactUploadBackpressure:       "Wait for an in-flight source upload to finish, then retry after the advertised delay.",
	WebhookSignatureInvalid:                "Verify the signing secret matches the active key id; rotate if mismatch persists.",
	WebhookMethodNotAllowed:                "Resend the webhook delivery as an HTTP POST request.",
	WebhookReplayRejected:                  "Verify clock sync and resign the webhook with a fresh timestamp.",
	WebhookDeliveryIdentityMismatch:        "Investigate the sender identity conflict; retain the first accepted envelope and redeliver only its exact body and metadata.",
	WebhookInboxUnavailable:                "Restore PostgreSQL inbox and audit availability, then allow the provider to redeliver.",
	WebhookGitHubPayloadInvalid:            "Inspect the declared GitHub event and sender configuration; correct the malformed payload before redelivery.",
	WebhookPayloadInvalid:                  "Inspect the signed event contract, correct the permanent payload error, and submit a new provider delivery.",
	WebhookPayloadCorrupt:                  "Do not redrive; restore the retained payload from a verified backup or submit a new provider delivery.",
	WebhookRouteFailed:                     "Restore the source-link or database dependency and allow the bounded planner retry to run.",
	WebhookRouteAttemptsExhausted:          "Fix the planner dependency, then use the operator redrive route to start a new bounded attempt cycle.",
	WebhookPlanStoreFailed:                 "Restore durable inbox writes and allow the delivery planner to retry.",
	WebhookTargetPayloadInvalid:            "Do not retry the malformed immutable target; correct the planner and submit a new provider delivery.",
	WebhookTargetPayloadCorrupt:            "Do not redrive the corrupt target; restore verified retained data or submit a new provider delivery.",
	WebhookTargetSnapshotMismatch:          "Inspect planner/dispatcher version skew and submit a new delivery only after the immutable snapshot contract is repaired.",
	WebhookTargetAuthorityChanged:          "Confirm the current installation, repository grant, and service link; relink and submit a new delivery if deployment is still intended.",
	WebhookTargetInvalid:                   "Repair the permanent target invariant before submitting a new provider delivery.",
	WebhookTargetDispatchFailed:            "Restore staged-intake dependencies and allow the bounded target retry to continue.",
	WebhookTargetAttemptsExhausted:         "Fix the downstream dependency, then redrive the dead-letter delivery to start a new target attempt cycle.",
	WebhookTargetDeadLetter:                "Inspect the dead-letter target reason, correct it, and use the operator redrive route.",
	WebhookTargetsMissing:                  "Inspect planner and inbox consistency; submit a new delivery only after the missing-target invariant is repaired.",
	AuditChainBroken:                       "Halt protected operations; engage security review per `SECURITY.md`.",
	NotificationDeliveryFailed:             "Inspect the notification channel configuration, rotate credentials if needed, and retry delivery.",
	DockerEngineUnsupported:                "Upgrade Docker Engine to >= 24.0 (API v1.43) and rerun preflight.",
	DeployJobEnqueueFailed:                 "Inspect the deploy queue store and database connectivity, then retry the deploy with a fresh idempotency key.",
	AgentClockSkew:                         "Resync the agent host clock with NTP, then restart `midnight-agent`.",
	ImageSignatureInvalid:                  "Re-pull the image from a trusted registry; rerun cosign verify against the published key bundle.",
	BackupOffhostMissing:                   "Configure an off-host backup destination in `midnightctl backup config`.",
	DaemonJSONConflict:                     "Reconcile operator-managed Docker daemon.json keys with the Midnight baseline before retrying.",
	DiskPressureCritical:                   "Free disk space or expand the volume; rerun `midnight doctor` to confirm headroom.",
	MemoryHeadroomLow:                      "Free memory or right-size the host; rerun `midnight doctor` to confirm headroom.",
	TimeSyncDrift:                          "Resync host clock with NTP/chrony; rerun `midnight doctor` to confirm.",
	IngressPortBound:                       "Stop the conflicting process or reconfigure ingress to a free port; rerun preflight.",
	UninstallDangerousPath:                 "Restrict the uninstall scope to Midnight-managed paths; never target host-OS critical paths.",
	SafeExecutionPlanInvalid:               "Inspect the validator output, fix the offending step, and re-emit the plan.",
	AgentHeartbeatReplay:                   "Investigate duplicated agent identity; rotate agent secret if compromised.",
	AgentHeartbeatExpired:                  "Resync host clock with NTP and confirm agent heartbeat cadence.",
	AgentHeartbeatTampered:                 "Verify the agent-scoped credential matches the control plane keyring; rotate if mismatch persists.",
	AgentStatusReportReplay:                "Investigate duplicated agent identity or a stuck sender sequence; rotate the agent credential if compromised.",
	AgentStatusReportExpired:               "Resync host clock with NTP and confirm status-report cadence.",
	AgentStatusReportTampered:              "Verify the agent-scoped credential matches the control plane keyring; rotate if mismatch persists.",
	AgentRuntimeUnavailable:                "Confirm Docker is installed and `midnight-agent` can reach the Engine API socket; the agent retries with backoff.",
	AgentNotConfigured:                     "Set the provider API key for the selected MIDNIGHT_AGENT_PROVIDER (for example ANTHROPIC_API_KEY) and restart midnight-server.",
	PrivateNetworkUnavailable:              "Restore Docker network provisioning for the project/environment boundary; Midnight will not fall back to the default bridge.",
	CGroupV2Required:                       "Add `systemd.unified_cgroup_hierarchy=1` to the kernel command line and reboot.",
	DockerDaemonBaselineConflict:           "Reconcile `/etc/docker/daemon.json` (overlay2, live-restore, no-new-privileges, json-file log driver) with the Midnight baseline before retrying.",
	VersionSkewRejected:                    "Upgrade or pin the offending component to a version inside the documented compatibility window in versions.yaml.",
	ContainerNameConflict:                  "Inspect the colliding container; if it is operator-managed, remove it manually before retrying.",
	ContainerSpecInvalid:                   "Inspect the validator detail field and fix the offending spec field.",
	CapNotPermitted:                        "Remove the capability or add it to the operator allowlist with an ADR explaining why.",
	HostPortNotPermitted:                   "Bind to an unprivileged port (>=1024) or explicitly allow the port in operator policy.",
	ContainerLabelMissing:                  "Add the missing `midnight.project`, `midnight.service`, `midnight.deployment`, `midnight.instance` labels.",
	ReconcileStepFailed:                    "Inspect step diagnostics, fix the underlying cause, and trigger another reconcile.",
	SchedulerQuotaExceeded:                 "Wait for in-flight jobs to drain, raise the project quota, or shed lower-priority jobs.",
	SelfHealCooldownActive:                 "Wait for the cooldown to expire; if instance is unhealthy, escalate via the runbook.",
	ACMECAAForbidden:                       "Update CAA records to authorize the configured ACME issuer, then retry.",
	ACMERateBudgetExhausted:                "Wait for the weekly budget window to roll over, or use staging until production budget is available.",
	ACMERestoreBudgetExhausted:             "Allow the throttle to drain, then resume the restore queue.",
	ACMEHTTP01Unreachable:                  "Confirm port 80 is reachable from the public internet; otherwise switch to DNS-01.",
	ACMEDNSMismatch:                        "Verify the configured DNS provider credentials and propagation settings.",
	ACMERateLimited:                        "Back off for the duration suggested by the CA, then retry the order.",
	ACMEOrderFailedPermanent:               "Inspect the order error, correct the underlying input, and recreate the order.",
	ACMEOrderFailedRetryable:               "Wait for the recommended cool-down, then retry the order.",
	ACMECertificateExpired:                 "Trigger immediate renewal; check ACME provider status if it fails.",
	ACMECertificateExpiringCritical:        "Run `midnight cert renew` for the affected domain now.",
	ACMECertificateExpiringWarn:            "Confirm renewal schedule is active; investigate if budget is exhausted.",
	RestoreACMEThrottled:                   "Allow the throttle window to advance; resume after the next slot opens.",
	DNSPropagationTimeout:                  "Inspect DNS provider records; extend wait window or fix records, then retry.",
	IngressPort80Unreachable:               "Open port 80 to the public internet or switch to DNS-01 challenges.",
	IngressReloadThrash:                    "Pause route updates briefly to let the reload budget recover.",
	RouteReservedPath:                      "Remove the conflicting user route; reserved paths are owned by the ACME challenge router.",
	RoutePublicationFailed:                 "Inspect the route publisher and Traefik dynamic config directory, then retry route publication after fixing the write or validation error.",
	WildcardRenewalFailed:                  "Inspect ACME provider error detail and re-attempt renewal once the cause is corrected.",
	LogQueryInvalid:                        "Drop the cursor and re-issue the query with valid filter values.",
	LogStoreUnavailable:                    "Configure MIDNIGHT_LOG_DIR to a readable structured log directory or inspect the server log-store error.",
	TimelineQueryInvalid:                   "Drop the cursor and re-issue the query with valid filter values.",
	RedactionRuleInvalid:                   "Review the rejected rule and reformat per the redaction rule schema.",
	SupportBundleBudgetExceeded:            "Trim the requested categories, raise the budget knob, or generate per-category bundles instead.",
	BackupVerifyChecksumMismatch:           "Re-export the failing artifact; if it still mismatches, follow the `backup-restore` runbook.",
	BackupDestinationWriteFailed:           "Verify the configured backup destination exists, is writable by the Midnight service account, and has enough capacity; then retry the backup or restore-plan write.",
	BootstrapTokenConsumed:                 "The bootstrap token is one-shot; rotate the admin via `midnight admin rotate-credentials` instead.",
	MFARequired:                            "Complete TOTP enrollment via `midnight admin enroll-mfa` before retrying.",
	MFAInvalidCode:                         "Resync the authenticator app's clock and re-enter the 6-digit code.",
	BackupStale:                            "Trigger a backup now; investigate the scheduled-backup runbook if it keeps lapsing.",
	BackupCredentialsMissing:               "Set `MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID`/`MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY`, or configure a sealed credential variable and point `MIDNIGHT_BACKUP_CREDENTIAL_REF` at its `var_<ulid>` id.",
	BackupCredentialsUnauthorized:          "Grant the backup principal read scope on the referenced sealed credential, or rotate `MIDNIGHT_BACKUP_CREDENTIAL_REF` to a variable in the consumer's allowed scope.",
	VolumeQuotaUnsupported:                 "Mount `/var/lib/midnight` on xfs (with prjquota) or ext4 (with quota) and remount.",
	VolumeReplicasUnsupported:              "Reduce service replicas to 1 before attaching a volume, or remove the volume attachment to scale beyond one replica.",
	VolumeResizeDownUnsupported:            "Volumes can only be resized UP; create a smaller volume and migrate data manually if you need a smaller backing store.",
	VolumeAttached:                         "Detach the volume from every active deployment before deleting it; redeploy the service without the volume attachment first.",
	ServiceDeleteRunning:                   "Stop the service's current deployment (or wait for the in-flight one to finish), then delete the service.",
	ServiceDeleteBlocked:                   "Remove the dependent record named in the problem details - an attached volume, an open pull-request preview, or an unfinished source upload - then retry the delete.",
	DeploySuperseded:                       "Another deployment for the same (service, environment) won the supersede race; this attempt was rolled back cleanly.",
	DeploySourceRefUnsupported:             "Deploy an immutable digest-pinned image until source-build-registry emits candidate images.",
	DeployCancelQueueUnavailable:           "Wait for the queue store to be wired, then cancel again so the queued job can be stopped before lease.",
	DeployCancelUnsafe:                     "Refresh the operation. If it is still running without pending work or a leased worker, inspect queue/outbox recovery before retrying cancellation.",
	DeployCanceledBeforePromotion:          "The deploy was canceled before traffic promotion; submit a new deploy when ready.",
	DeployCandidateExited:                  "Read the container logs in the failure reason, fix the image start command or its configuration, and deploy again.",
	DeployCandidateRestartLoop:             "The container starts and exits repeatedly; fix the crash shown in the failure reason, then deploy again.",
	PredeployFailed:                        "Inspect the predeploy container logs, fix the command or image, and create a new deployment.",
	PredeployTimeout:                       "Make the predeploy command bounded, then create a new deployment.",
	VariableReferenceUnresolved:            "Define the missing variable or correct the `${{...}}` reference in the offending value, then re-deploy.",
	VariableReferenceCycle:                 "Break the reference cycle reported in the operation event log, then re-deploy.",
	ImageSignatureRequired:                 "Sign the image with cosign and push the .sig artifact, or set verify_signatures=false in the agent config (not recommended).",
	ImageSignatureVerifyBinaryMissing:      "Install the pinned cosign binary (see versions.yaml) and set `cosign_binary_path` in the agent config, or disable verify_signatures.",
	ImageSignatureVerifyTimeout:            "Raise `cosign_verify_timeout_seconds` in the agent config or investigate slow registry/Rekor responses.",
	ImageSignatureVerifyTransparencyFailed: "Halt deployment; engage security to inspect the Sigstore Rekor / Fulcio transparency entry for this image.",
	ReleaseFingerprintMismatch:             "Stop and engage Security per SECURITY.md. Cross-check `docs/release/keys/midnight-release-fingerprint.txt` against the project-website mirror; do not trust any signed .deb until the discrepancy is resolved.",
	ReleaseRevokedKey:                      "Do not deploy the release. The signing fingerprint is listed in a REVOKED-<fingerprint>.txt file under `docs/release/keys/`; wait for the next protected release tag against a fresh fingerprint per RELEASE_KEY_CUSTODY.md Step 8.",
	AlertMetadataInvalid:                   "Add the missing alert metadata fields and rerun the alert pack lint.",
	RestorePlanSerializationFailed:         "Inspect the restore plan structure, reconcile against the schemaVersion=2 contract, and reissue.",
	SupportBundleRedactionFailed:           "Inspect the redactor diagnostics, fix the offending input, and rerun support-bundle redaction.",
	SupportBundleVerifyFailed:              "Regenerate the support bundle and verify its embedded manifest before sharing.",
	TelemetryOptInRequired:                 "Run `midnight feature enable telemetry` (with operator consent) before retrying telemetry uploads.",
	TelemetryPayloadPIIDetected:            "Inspect the rejected payload, remove the offending value, and retry with a sanitized payload.",
	RollbackImageUnavailable:               "Confirm the recorded digest exists in the embedded mirror or the source registry; if it is gone, the rollback target is lost.",
	RuntimeUnsupportedOperation:            "Use a runtime adapter that supports the requested operation or disable the feature until adapter support lands.",
	SnapshotImmutable:                      "Snapshots are immutable; create a new deployment instead of mutating the existing snapshot.",
	RegistryGCVerifyFailed:                 "Halt destructive GC and engage release ops; the post-GC verify probe found a digest that should have been retained.",
	RegistryGCLocked:                       "Another GC run holds the global advisory lock; wait for it to drain before retrying.",
}

// orderedCodes preserves the declaration order so callers iterating with All
// receive a deterministic sequence (map iteration is randomized in Go).
var orderedCodes = []Code{
	InternalError,
	NotImplemented,
	CapabilityUnavailable,
	MethodNotAllowed,
	GraphQLDeployMustBeSoleMutation,
	OperationNotFound,
	OperationInvalidTransition,
	CursorInvalid,
	PaginationLimitExceeded,
	AuthTokenHeaderInvalid,
	AuthRequiredMissing,
	AuthCredentialsInvalid,
	AuthSessionInvalid,
	AuthorizationDenied,
	AuthRateLimited,
	InvitationAccountExists,
	AuthOperatorRecoveryRequired,
	RequestRateLimited,
	IdempotencyKeyInvalid,
	IdempotencyKeyRequired,
	IdempotencyKeyReused,
	IdempotencyKeyReusedDifferentBody,
	IdempotencyRequestInProgress,
	ResourceNotFound,
	ResourceValidationFailed,
	ResourceConflict,
	PreconditionRequired,
	PreconditionFailed,
	SourceArtifactQuotaExceeded,
	SourceArtifactUploadBackpressure,
	WebhookSignatureInvalid,
	WebhookMethodNotAllowed,
	WebhookReplayRejected,
	WebhookDeliveryIdentityMismatch,
	WebhookInboxUnavailable,
	WebhookGitHubPayloadInvalid,
	WebhookPayloadInvalid,
	WebhookPayloadCorrupt,
	WebhookRouteFailed,
	WebhookRouteAttemptsExhausted,
	WebhookPlanStoreFailed,
	WebhookTargetPayloadInvalid,
	WebhookTargetPayloadCorrupt,
	WebhookTargetSnapshotMismatch,
	WebhookTargetAuthorityChanged,
	WebhookTargetInvalid,
	WebhookTargetDispatchFailed,
	WebhookTargetAttemptsExhausted,
	WebhookTargetDeadLetter,
	WebhookTargetsMissing,
	AuditChainBroken,
	NotificationDeliveryFailed,
	DockerEngineUnsupported,
	DeployJobEnqueueFailed,
	AgentClockSkew,
	ImageSignatureInvalid,
	BackupOffhostMissing,
	DaemonJSONConflict,
	DiskPressureCritical,
	MemoryHeadroomLow,
	TimeSyncDrift,
	IngressPortBound,
	UninstallDangerousPath,
	SafeExecutionPlanInvalid,
	AgentHeartbeatReplay,
	AgentHeartbeatExpired,
	AgentHeartbeatTampered,
	AgentStatusReportReplay,
	AgentStatusReportExpired,
	AgentStatusReportTampered,
	AgentRuntimeUnavailable,
	PrivateNetworkUnavailable,
	CGroupV2Required,
	DockerDaemonBaselineConflict,
	VersionSkewRejected,
	ContainerNameConflict,
	ContainerSpecInvalid,
	CapNotPermitted,
	HostPortNotPermitted,
	ContainerLabelMissing,
	ReconcileStepFailed,
	SchedulerQuotaExceeded,
	SelfHealCooldownActive,
	ACMECAAForbidden,
	ACMERateBudgetExhausted,
	ACMERestoreBudgetExhausted,
	ACMEHTTP01Unreachable,
	ACMEDNSMismatch,
	ACMERateLimited,
	ACMEOrderFailedPermanent,
	ACMEOrderFailedRetryable,
	ACMECertificateExpired,
	ACMECertificateExpiringCritical,
	ACMECertificateExpiringWarn,
	RestoreACMEThrottled,
	DNSPropagationTimeout,
	IngressPort80Unreachable,
	IngressReloadThrash,
	RouteReservedPath,
	RoutePublicationFailed,
	WildcardRenewalFailed,
	LogQueryInvalid,
	LogStoreUnavailable,
	TimelineQueryInvalid,
	RedactionRuleInvalid,
	SupportBundleBudgetExceeded,
	BackupVerifyChecksumMismatch,
	BackupDestinationWriteFailed,
	BootstrapTokenConsumed,
	MFARequired,
	MFAInvalidCode,
	BackupStale,
	BackupCredentialsMissing,
	BackupCredentialsUnauthorized,
	VolumeQuotaUnsupported,
	VolumeReplicasUnsupported,
	VolumeResizeDownUnsupported,
	VolumeAttached,
	ServiceDeleteRunning,
	ServiceDeleteBlocked,
	DeploySuperseded,
	DeploySourceRefUnsupported,
	DeployCancelQueueUnavailable,
	DeployCancelUnsafe,
	DeployCanceledBeforePromotion,
	DeployCandidateExited,
	DeployCandidateRestartLoop,
	PredeployFailed,
	PredeployTimeout,
	VariableReferenceUnresolved,
	VariableReferenceCycle,
	ImageSignatureRequired,
	ImageSignatureVerifyBinaryMissing,
	ImageSignatureVerifyTimeout,
	ImageSignatureVerifyTransparencyFailed,
	ReleaseFingerprintMismatch,
	ReleaseRevokedKey,
	AlertMetadataInvalid,
	RestorePlanSerializationFailed,
	SupportBundleRedactionFailed,
	SupportBundleVerifyFailed,
	TelemetryOptInRequired,
	TelemetryPayloadPIIDetected,
	RollbackImageUnavailable,
	RuntimeUnsupportedOperation,
	SnapshotImmutable,
	RegistryGCVerifyFailed,
	RegistryGCLocked,
}
