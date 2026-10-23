# INTERFACES_CONTROL_PLANE — Go Interface Signatures (Server + Workers)

- **Skeleton ID:** SKEL-IFACE-CTRLPLANE
- **Owner:** Senior Engineer 4 of 4 (skeleton specifications)
- **Status:** Locked by panel decisions; ready for engineering kickoff

This file lists the public Go interface signatures the server and worker teams
implement. **Signatures and doc-comments only — no method bodies.** Each
interface comment quotes the panel decision pinning it. Engineering adds
unexported helpers freely; changing a signature listed here requires an ADR.

Conventions:

- All `context.Context` is the first parameter. Cancellation must propagate.
- Errors are typed: package-local sentinel errors plus a wrapping
  `failurecodes.Coded` that carries the SCREAMING_SNAKE_CASE registry code.
- Repositories take and return DTO structs from `pkg/orailtypes`, never raw
  sqlc rows.
- All write methods accept an `If-Match`-style `resourceVersion` where the
  panel locks optimistic concurrency (line 104).

---

## Resource ID Type

```go
package orailtypes

// ID is the panel-locked resource id format: <prefix>_<26-char Crockford
// base32 ULID-or-UUIDv7>. Total length ≤ 32 (panel line 169).
//
// Reserved prefixes: prj env svc dep op job dom vol var tok usr sa tpl evt
// idem plan inst.
//
// Clients must NOT parse the prefix for type checks; use Kind().
type ID string

func (ID) Kind() Kind                     // returns the canonical kind, never the prefix string
func ParseID(s string) (ID, error)        // returns INVALID_ID on malformed input
```

---

## Repositories

```go
package db

// ProjectRepository is the persistence boundary for projects.
//
// Panel decision pinning:
//   line 100-113 — atomic single-flight per (service, environment) and
//   optimistic concurrency on resource_version. Project mutations cascade
//   into environment + service writes via the same transactional outbox
//   (line 108: applyChangeSet is atomic at the database level).
//
// Implementations MUST take an explicit *sql.Tx for any method that may be
// part of a multi-table change set.
type ProjectRepository interface {
    Create(ctx context.Context, tx Tx, p orailtypes.Project) (orailtypes.Project, error)
    Get(ctx context.Context, id orailtypes.ID) (orailtypes.Project, error)
    List(ctx context.Context, q ProjectQuery) (page Page[orailtypes.Project], err error)
    Update(ctx context.Context, tx Tx, p orailtypes.Project, ifMatch ResourceVersion) (orailtypes.Project, error)
    Delete(ctx context.Context, tx Tx, id orailtypes.ID, ifMatch ResourceVersion) error
}

// ServiceRepository persists services and their service_environment_state rows.
//
// Panel decision pinning:
//   line 104 — every deployment-mutation path acquires
//   pg_advisory_xact_lock(hashtext('svc:'||service_id||':'||env_id)) OR uses
//   optimistic concurrency on a service_environment_state row. The repository
//   exposes both seams; the caller chooses based on whether they hold a tx.
type ServiceRepository interface {
    Create(ctx context.Context, tx Tx, s orailtypes.Service) (orailtypes.Service, error)
    Get(ctx context.Context, id orailtypes.ID) (orailtypes.Service, error)
    GetWithEnvState(ctx context.Context, svcID, envID orailtypes.ID) (orailtypes.Service, orailtypes.ServiceEnvState, error)
    List(ctx context.Context, q ServiceQuery) (Page[orailtypes.Service], error)
    UpdateEnvState(ctx context.Context, tx Tx, svcID, envID orailtypes.ID, fn func(*orailtypes.ServiceEnvState) error, ifMatch ResourceVersion) error
    AcquireSingleFlightLock(ctx context.Context, tx Tx, svcID, envID orailtypes.ID) error
    Delete(ctx context.Context, tx Tx, id orailtypes.ID, ifMatch ResourceVersion) error
}

// DeploymentRepository persists deployments, deployment_instances, and
// deployment_snapshots; reads through the public-state projection.
//
// Panel decision pinning:
//   line 105 — Deployment state transition matrix is normative; terminal
//   states are absorbing except ACTIVE→SUPERSEDED and ACTIVE→REMOVING.
//   line 106 — only the Deployment reconciler is permitted to write
//   DEPLOYING → ACTIVE. The repository exposes a typed Transition method
//   that callers use; the reconciler is the only registered caller for the
//   ACTIVE transition guard (enforced at the Reconciler interface, below).
//   line 112 — deployment snapshot is fully self-contained; sensitive
//   variables are stored as content-addressed references with reference
//   counting (deployment_image_pins is consulted by registry GC).
type DeploymentRepository interface {
    Create(ctx context.Context, tx Tx, d orailtypes.Deployment) (orailtypes.Deployment, error)
    Get(ctx context.Context, id orailtypes.ID) (orailtypes.Deployment, error)
    GetWithSnapshot(ctx context.Context, id orailtypes.ID) (orailtypes.Deployment, orailtypes.DeploymentSnapshot, error)
    ListByService(ctx context.Context, svcID orailtypes.ID, q DeploymentQuery) (Page[orailtypes.Deployment], error)
    Transition(ctx context.Context, tx Tx, id orailtypes.ID, from, to orailtypes.DeploymentState, ifMatch ResourceVersion, reason orailtypes.ReasonCode) (orailtypes.Deployment, error)
    AppendInstance(ctx context.Context, tx Tx, in orailtypes.DeploymentInstance) error
    UpdateInstance(ctx context.Context, tx Tx, in orailtypes.DeploymentInstance) error
    NextDeploymentSeq(ctx context.Context, tx Tx, svcID, envID orailtypes.ID) (int64, error)
    InsertSnapshot(ctx context.Context, tx Tx, snap orailtypes.DeploymentSnapshot) error
    PinDigest(ctx context.Context, tx Tx, digest string, holderID orailtypes.ID) error
    UnpinDigest(ctx context.Context, tx Tx, digest string, holderID orailtypes.ID) error
}

// JobRepository is the persistence shape for the Postgres job queue. The
// Queue interface (below) wraps this with lease/heartbeat semantics.
//
// Panel decision pinning:
//   line 102 — Postgres job queue contract is normative; columns include
//   lease_owner, lease_expires_at, heartbeat_at, attempt, available_at,
//   priority, queue, partition_key. Dequeue uses FOR UPDATE SKIP LOCKED.
//   line 103 — high-write tables (events, audit_logs, jobs,
//   host_heartbeats, deployment_instances) are monthly range-partitioned.
type JobRepository interface {
    InsertOutbox(ctx context.Context, tx Tx, j orailtypes.Job) (orailtypes.ID, error)
    Get(ctx context.Context, id orailtypes.ID) (orailtypes.Job, error)
    DeadLetter(ctx context.Context, id orailtypes.ID, reason orailtypes.ReasonCode) error
    Replay(ctx context.Context, id orailtypes.ID) error
    QueueStats(ctx context.Context) ([]orailtypes.QueueStat, error)
    PruneCompleted(ctx context.Context, olderThan time.Time) (int64, error)
}

// OperationRepository persists the operations parent rows that aggregate
// child job state.
//
// Panel decision pinning:
//   line 173 — mutating async endpoints return 202 + Location:
//   /api/v1/operations/{op_id} and the full Operation in the body. The
//   repository must therefore support consistent reads for the polling
//   endpoint with bounded staleness.
type OperationRepository interface {
    Create(ctx context.Context, tx Tx, op orailtypes.Operation) (orailtypes.Operation, error)
    Get(ctx context.Context, id orailtypes.ID) (orailtypes.Operation, error)
    UpdateState(ctx context.Context, tx Tx, id orailtypes.ID, state orailtypes.OperationState, result json.RawMessage, ifMatch ResourceVersion) (orailtypes.Operation, error)
    ListChildren(ctx context.Context, opID orailtypes.ID) ([]orailtypes.Job, error)
}

// IdempotencyRepository stores the (token_id, method, path_template) ->
// {body_hash, response, state} mapping used by the idempotency middleware.
//
// Panel decision pinning:
//   line 107 — idempotency state matrix on failure paths is explicit:
//   {first request still pending, succeeded, failed-terminal, key reserved
//   before body store, key TTL expired} × {same body, different body}.
//   line 172 — Idempotency-Key TTL is 24h; body hash is SHA-256 over
//   RFC 8785 canonical JSON; replay returns identical body and
//   Idempotent-Replayed: true.
type IdempotencyRepository interface {
    Reserve(ctx context.Context, key IdempotencyKey, ttl time.Duration) (IdempotencyState, error)
    StoreResponse(ctx context.Context, key IdempotencyKey, bodyHash [32]byte, resp orailtypes.IdempotentResponse) error
    Lookup(ctx context.Context, key IdempotencyKey) (orailtypes.IdempotentResponse, IdempotencyState, error)
    ExpireBefore(ctx context.Context, t time.Time) (int64, error)
}

// AuditRepository is INSERT-only for clients of the audit chain. The
// AuditChain interface above wraps it for hash-chained writes; raw INSERTs
// are not permitted.
//
// Panel decision pinning:
//   line 132 — Postgres role separation: midnight_app has INSERT only;
//   midnight_audit_ro has SELECT only. The Go interface mirrors this:
//   no Update or Delete method exists, even for tests.
type AuditRepository interface {
    AppendChained(ctx context.Context, tx Tx, row orailtypes.AuditRow) (orailtypes.AuditRow, error)
    Read(ctx context.Context, q AuditQuery) (Page[orailtypes.AuditRow], error)
    LatestCheckpoint(ctx context.Context) (orailtypes.AuditCheckpoint, error)
    InsertCheckpoint(ctx context.Context, cp orailtypes.AuditCheckpoint) error
    StreamForExport(ctx context.Context, q AuditQuery, w io.Writer) error
}

// RouteRepository persists desired routes for Traefik file-provider output.
//
// Panel decision pinning:
//   line 159 — dynamic Traefik config files are written via temp-file +
//   atomic rename on the same filesystem; never truncated in place.
//   Midnight reserves /.well-known/acme-challenge/* at maximum router
//   priority on every entrypoint serving :80; user routes claiming this
//   path are rejected at validation.
//   line 110 — route membership requires
//   instance.last_observed_at > now() - route_freshness_ttl (default 30s).
type RouteRepository interface {
    Upsert(ctx context.Context, tx Tx, r orailtypes.Route) (orailtypes.Route, error)
    Get(ctx context.Context, id orailtypes.ID) (orailtypes.Route, error)
    ListForDeployment(ctx context.Context, depID orailtypes.ID) ([]orailtypes.Route, error)
    Delete(ctx context.Context, tx Tx, id orailtypes.ID) error
    SetTargetReadyAt(ctx context.Context, tx Tx, id orailtypes.ID, t time.Time) error
    SetMembership(ctx context.Context, tx Tx, depID orailtypes.ID, members []orailtypes.RouteMember) error
}

// HostRepository persists agent registrations and heartbeats.
//
// Panel decision pinning:
//   line 97 — agent reports clock offset in heartbeat; control plane
//   refuses agent registration with AGENT_CLOCK_SKEW if
//   |agent_clock - server_clock| > 30s.
//   line 110 — LOST instance handling and route hysteresis depend on
//   accurate last-observed-at heartbeats.
type HostRepository interface {
    Register(ctx context.Context, h orailtypes.Host) (orailtypes.Host, error)
    Heartbeat(ctx context.Context, hb orailtypes.HostHeartbeat) error
    Get(ctx context.Context, id orailtypes.ID) (orailtypes.Host, error)
    List(ctx context.Context, q HostQuery) (Page[orailtypes.Host], error)
    MarkLost(ctx context.Context, id orailtypes.ID, reason orailtypes.ReasonCode) error
}
```

---

## Queue

```go
package queue

// Queue is the per-class job queue contract. There is one Queue
// instance per class (build, deploy, reconcile, webhook, cleanup) with
// its own bounded concurrency.
//
// Panel decision pinning (line 102):
//   "Postgres job queue contract is normative: columns include
//    lease_owner, lease_expires_at, heartbeat_at, attempt, available_at,
//    priority, queue, partition_key. Dequeue uses SELECT … FOR UPDATE
//    SKIP LOCKED ordered by (priority, available_at) filtered by queue
//    and lease availability. Heartbeat cadence = lease_ttl/3; expired
//    leases are reclaimed automatically. Separate per-class queues with
//    independent worker pools and bounded concurrency: build, deploy,
//    reconcile, webhook, cleanup. Per-tenant fairness via weighted-fair
//    dequeue."
//
// Implementations MUST honor the SQL fragments listed in
// FEATURE_POSTGRES_JOB_QUEUE.md; deviations require an ADR.
type Queue interface {
    // Enqueue inserts a job in the SAME transaction as the caller's domain
    // mutation. The implementation MUST require a non-nil tx; transactional-
    // outbox semantics are non-optional (panel line 108).
    Enqueue(ctx context.Context, tx Tx, j Job) (jobID orailtypes.ID, err error)

    // Dequeue leases one ready job, applying weighted-fair partition_key
    // demotion. Returns ErrNoWork if nothing is ready. The lease has TTL
    // = lease_ttl; the worker MUST Heartbeat at lease_ttl/3 cadence.
    Dequeue(ctx context.Context, workerID string) (LeasedJob, error)

    // Heartbeat extends the lease by lease_ttl. Returns ErrLeaseLost if
    // the row is no longer leased to this worker (panel: "0 rows
    // returned ⇒ lease lost; worker MUST abort and not commit further
    // side effects").
    Heartbeat(ctx context.Context, workerID string, jobID orailtypes.ID) error

    // Complete marks the job done and stores the result for operation
    // roll-up. Idempotent on retry.
    Complete(ctx context.Context, workerID string, jobID orailtypes.ID, result Result) error

    // Fail marks the job failed with a reason code. The reaper decides
    // whether to retry (attempt += 1, exponential backoff) or move to
    // dead_letter (attempt >= max_attempts).
    Fail(ctx context.Context, workerID string, jobID orailtypes.ID, code orailtypes.ReasonCode, retriable bool) error

    // Cancel sets state='canceled' cooperatively. Workers observe at next
    // heartbeat. Cancellation between route-add and drain-completion
    // returns CANCEL_NOT_SAFE at the deployment-FSM layer (panel line
    // 109); the queue does NOT enforce that — it surfaces the request.
    Cancel(ctx context.Context, jobID orailtypes.ID) error

    // Reclaim is the reaper sweep: any leased row past lease_expires_at
    // returns to pending (or moves to dead_letter if attempt >=
    // max_attempts). Returns the count reclaimed.
    Reclaim(ctx context.Context) (int, error)
}
```

---

## Deployment Reconciler

```go
package reconciler

// DeploymentReconciler is the named promotion authority. It is the ONLY
// writer authorized to advance DEPLOYING → ACTIVE.
//
// Panel decision pinning (line 106):
//   "Promotion authority is named: a 'Deployment reconciler' worker class
//    joins deployment_instances.ready_count >= minReady and
//    routes.target_ready_at IS NOT NULL for the candidate, then performs
//    guarded UPDATE deployments SET state='ACTIVE' WHERE id=$id AND
//    state='DEPLOYING' AND resource_version=$rv."
//
// Constructed once in cmd/midnight-server. Constructor refuses to register
// itself in the queue dispatcher under any other class. Engineering MUST
// route DEPLOYING→ACTIVE writes through this interface ONLY.
type DeploymentReconciler interface {
    // Reconcile performs one reconcile pass for the given deployment id.
    // Idempotent. May return nil even when no transition occurred; that
    // is the steady-state outcome.
    Reconcile(ctx context.Context, depID orailtypes.ID) (Outcome, error)

    // PromoteIfReady is the guarded write of state ACTIVE. Implementations
    // MUST evaluate the readiness + route guards inside the same locked
    // transaction (panel: pg_advisory_xact_lock on (service, environment)).
    // Calls from any source other than the reconciler worker pool MUST
    // panic at boot via a runtime registry check.
    PromoteIfReady(ctx context.Context, tx Tx, depID orailtypes.ID, rv ResourceVersion) (Promoted bool, err error)

    // ObserveInstance updates the readiness aggregate from agent reports.
    // Does NOT transition deployment state on its own.
    ObserveInstance(ctx context.Context, in orailtypes.DeploymentInstance) error

    // HandleSupersession is invoked by the queue when a higher
    // deployment_seq commits ACTIVE for the same (service, env). The
    // reconciler then transitions the older row ACTIVE → SUPERSEDED.
    HandleSupersession(ctx context.Context, supersededID orailtypes.ID, supersedingSeq int64) error
}
```

---

## SecretStore

```go
package secrets

// SecretStore implements envelope encryption with KEK rotation.
//
// Panel decision pinning (line 118):
//   "KEK at /etc/midnight/secrets.key has mode 0400 midnight:midnight.
//    Rotation generates KEK_n+1, rewraps all DEKs in a background
//    operations job with progress, retains KEK_n for
//    secret_kek_grace_days (default 30). KEK loss is irrecoverable and
//    documented explicitly. Operators MUST back the KEK up separately
//    from Postgres backups."
//
// Plaintext NEVER returns from Decrypt for sealed variables (panel
// line 120: "no API path returns plaintext after create" — a sealed
// variable type wraps the byte slice and disallows access from
// non-deploy contexts).
type SecretStore interface {
    // Encrypt wraps plaintext with the active KEK's currently-rotated
    // DEK; returns the envelope blob plus the DEK id.
    Encrypt(ctx context.Context, plaintext []byte, scope Scope) (Envelope, error)

    // Decrypt unseals an envelope blob. For Sealed scope, returns a
    // SealedHandle that callers cannot dereference outside an authorized
    // deploy context (panel line 120).
    Decrypt(ctx context.Context, env Envelope, scope Scope) ([]byte, error)

    // RotateKEK generates KEK_{n+1}, atomically updates the active key
    // pointer, and enqueues a background operations job that rewraps
    // every DEK. Progress is reported on the parent operation. Old KEK
    // is retained for secret_kek_grace_days.
    RotateKEK(ctx context.Context) (orailtypes.ID, error)

    // Fingerprint returns the current KEK fingerprint for midnight
    // doctor and the install manifest (panel line 15 of
    // FEATURE_AUDIT_HASH_CHAIN.md).
    Fingerprint(ctx context.Context) (Fingerprint, error)
}
```

---

## AuditChain

```go
package audit

// AuditChain implements the per-row hash-chained audit log with daily
// Ed25519 checkpoints.
//
// Panel decision pinning (line 121):
//   "Audit log uses per-row hash chaining at MVP: prev_hash and
//    row_hash = H(prev_hash || canonical_row), rooted in a daily
//    audit_checkpoint row signed with the Midnight release key or
//    operator-configured Ed25519 key. midnight audit verify CLI ships
//    in MVP. JSONL export includes prev_hash, row_hash, checkpoint_id."
//
// The chain is per-instance, not per-tenant. Append acquires an advisory
// lock on the chain head, computes row_hash from the previous row, and
// inserts atomically. Concurrent appends serialize.
type AuditChain interface {
    // Append computes prev_hash + row_hash and inserts a chained row.
    // The caller's tx is the same tx that mutated the domain state.
    // Panel: variable mutations are audited without secret plaintext
    // (line 1178 of main spec); callers pass a redacted before/after.
    Append(ctx context.Context, tx Tx, row orailtypes.AuditRow) (orailtypes.AuditRow, error)

    // Verify scans a contiguous range of rows (or the entire chain) and
    // recomputes hashes. Returns the first break with row id and
    // surrounding context. Used by midnight audit verify and by the
    // periodic verifier job. Drives exit code 2 on chain break.
    Verify(ctx context.Context, q VerifyQuery) (VerifyReport, error)

    // Checkpoint is invoked by the daily worker at UTC midnight. Signs
    // (checkpoint_id, last_row_hash, last_row_id, started_at, ended_at)
    // with the Ed25519 signing key and inserts the audit_checkpoint row.
    Checkpoint(ctx context.Context, signer Ed25519Signer) (orailtypes.AuditCheckpoint, error)
}
```

---

## WebhookDispatcher

```go
package webhook

// WebhookDispatcher signs and delivers outbound webhook events with HMAC,
// replay-window, and overlap key rotation.
//
// Panel decision pinning (line 125):
//   "Outbound webhook signing: Midnight-Signature: t=<unix>,v1=<hex>,
//    kid=<key_id>, replay window 300s, two active signing keys per
//    endpoint with 30-day rotation. Canonical signing input =
//    f'{timestamp}.{raw_body}'. Multi-v1 accepted during rotation
//    overlap."
//
// Outbound HTTP MUST use the SafeHTTPClient (panel line 124) so webhook
// targets cannot resolve to RFC1918 / cloud metadata addresses.
type WebhookDispatcher interface {
    // Send delivers one event with at-least-once semantics. Returns once
    // the event has been durably enqueued; actual transmission happens
    // in the webhook worker class. The dispatcher does NOT block on
    // remote response.
    Send(ctx context.Context, tx Tx, ev orailtypes.WebhookEvent) (orailtypes.ID, error)

    // SignBody returns the canonical signing material and the
    // Midnight-Signature header value for one or two active keys.
    SignBody(ctx context.Context, endpointID orailtypes.ID, body []byte, ts time.Time) (header string, err error)

    // RotateKey adds a new active key for endpointID; the previous key
    // remains valid for the 30-day overlap window.
    RotateKey(ctx context.Context, endpointID orailtypes.ID) (kid string, err error)

    // Receipt records a per-attempt receipt (status, latency, body
    // truncated). Used by the per-endpoint token-bucket throttle (panel
    // line 111 reconcile-storm guard).
    Receipt(ctx context.Context, attemptID orailtypes.ID, r AttemptReceipt) error
}
```

---

## DnsProvider

```go
package dnsprovider

// DnsProvider is the typed contract every DNS plugin implements for
// ACME DNS-01 challenges and CAA preflight assistance.
//
// Panel decision pinning (line 153):
//   "A typed DnsProvider interface (presentTxt, cleanupTxt, getZone,
//    propagationCheck) ships in Alpha with at least Cloudflare,
//    Route53, and 'manual' providers; credentials live in the platform
//    secret store."
//
// All methods MUST be context-cancellable, idempotent, and side-effect-
// free on error besides whatever the upstream provider records. See
// FEATURE_ACME_DNS01_WILDCARD.md for the full type definitions.
type DnsProvider interface {
    Name() string
    GetZone(ctx context.Context, fqdn string) (Zone, error)
    PresentTxt(ctx context.Context, zone Zone, name, value string, ttl time.Duration) error
    CleanupTxt(ctx context.Context, zone Zone, name, value string) error
    PropagationCheck(ctx context.Context, name, value string, resolvers []string) (PropagationStatus, error)
}
```

---

## ImageRegistry

```go
package registry

// ImageRegistry abstracts the embedded distribution registry plus any
// configured external registries. The agent + scheduler call PullVerify
// before pull/start.
//
// Panel decision pinning:
//   line 80 — "The runtime agent verifies image signatures (cosign or
//   equivalent) before pull/start in the default profile. Mismatch
//   produces IMAGE_SIGNATURE_INVALID."
//   line 79 — "Production environments default to mirroring active and
//   last-N rollback digests into the embedded registry as a safety net.
//   External-registry digests are probed daily; missing digests for
//   protected deployments emit EXTERNAL_DIGEST_MISSING."
//   line 144 — "Embedded registry retains the last 10 image tags per
//   service plus all digests in active rollback windows and protected
//   snapshots. Reference-count + tag-retention sweep runs daily; full
//   destructive blob GC is opt-in with quiesce/lock/dry-run/post-GC
//   verify."
type ImageRegistry interface {
    // PullVerify resolves the digest (preferring the embedded mirror),
    // verifies the cosign signature, and surfaces IMAGE_SIGNATURE_INVALID
    // on mismatch. Returns the local content-addressed reference for
    // the runtime to consume.
    PullVerify(ctx context.Context, ref ImageRef, policy VerifyPolicy) (LocalRef, error)

    // Mirror copies a digest from an external registry into the embedded
    // registry. Idempotent on retry. Used by the protected-deployment
    // safety-net job.
    Mirror(ctx context.Context, ref ImageRef) (LocalRef, error)

    // Probe checks an external registry digest is still resolvable; emits
    // EXTERNAL_DIGEST_MISSING for protected deployments.
    Probe(ctx context.Context, ref ImageRef) (ProbeResult, error)

    // GC runs the daily reference-count + tag-retention sweep. Mode
    // controls dry-run vs apply. Refuses to reclaim digests with
    // pin_count > 0 (panel line 112).
    GC(ctx context.Context, mode GCMode) (GCReport, error)

    // Pin / Unpin manage the deployment_image_pins reference-count rows.
    Pin(ctx context.Context, tx Tx, digest string, holderID orailtypes.ID) error
    Unpin(ctx context.Context, tx Tx, digest string, holderID orailtypes.ID) error
}
```

---

## RuntimeAdapter

```go
package runtime

// RuntimeAdapter abstracts the container runtime so the agent can target
// Docker Engine in v0–v1 and containerd / Kubernetes later.
//
// Panel decision pinning:
//   line 90 — "Docker Engine API floor: v1.43 minimum (Engine ≥ 24.0);
//   the agent refuses to start with DOCKER_ENGINE_UNSUPPORTED outside
//   the supported range."
//   lines 91–93 — capability allowlist, AppArmor, HostConfig.Init=true,
//   ulimits. The adapter MUST validate every ContainerSpec against
//   these defaults at submit time; the host-side validator lives in
//   cmd/midnight-agent and is documented in INTERFACES_AGENT.md.
//
// The server-side RuntimeAdapter is mostly used by the scheduler for
// host-arch matching against multi-arch manifest lists; actual run is
// delegated to the agent.
type RuntimeAdapter interface {
    // CapabilityReport returns the supported feature set on the target
    // host (engine version, cgroup version, apparmor, selinux, userns).
    CapabilityReport(ctx context.Context, hostID orailtypes.ID) (Capabilities, error)

    // ResolvePlatform picks the manifest-list entry for the host's
    // arch/variant/os; returns IMAGE_ARCH_UNAVAILABLE if no match.
    ResolvePlatform(ctx context.Context, ref ImageRef, host Capabilities) (Platform, error)

    // Apply schedules a ContainerSpec for execution on a host via the
    // agent control stream. Returns the agent-assigned instance id.
    Apply(ctx context.Context, hostID orailtypes.ID, spec ContainerSpec) (orailtypes.ID, error)

    // Stop initiates SIGTERM-then-SIGKILL drain on an instance. Drain
    // window from the deployment policy (default Railway-style overlap).
    Stop(ctx context.Context, hostID orailtypes.ID, instanceID orailtypes.ID, drain time.Duration) error

    // Inspect returns the observed runtime state. Used by the reconciler
    // to converge desired vs observed.
    Inspect(ctx context.Context, hostID orailtypes.ID, instanceID orailtypes.ID) (Observed, error)
}
```

---

## Builder

```go
package build

// Builder produces an OCI image from a source bundle plus a resolved
// build plan. BuildKit is the v0–v1 implementation.
//
// Panel decision pinning:
//   line 78 — "Deployment snapshots store: source SHA, builder
//   type/version/digest, resolved Railpack/Nixpacks build plan JSON,
//   resolved base image digests for every FROM, and the produced image
//   digest. Rollback replays the recorded plan; it does not re-invoke
//   Railpack."
//   line 82 — "Build defaults: 30-minute timeout (configurable, max 4h);
//   per-host build concurrency min(4, cores/2); per-project concurrency 2."
//   line 83 — "Untrusted-build classification is explicit … no project
//   secrets, no shared cache read or write, push to a quarantine path."
//   line 129 — "publicClientVars + buildVars are the only classes that
//   populate process.env during build. A built-in static scan of build
//   output for known secret prefixes fails the build with
//   BUILD_OUTPUT_SECRET_LEAK."
type Builder interface {
    // Plan produces a deterministic build plan from the source.
    // For Dockerfile sources this resolves base-image digests; for
    // Railpack/Nixpacks sources this captures the resolved plan JSON.
    Plan(ctx context.Context, src SourceRef, planner Planner) (Plan, error)

    // Build executes the plan and returns the produced image digest +
    // SBOM + SLSA in-toto statement.
    Build(ctx context.Context, plan Plan, opts BuildOptions) (Artifact, error)

    // ScanOutput runs the build-output secret-leak static scan
    // (panel line 129); failure produces BUILD_OUTPUT_SECRET_LEAK.
    ScanOutput(ctx context.Context, art Artifact) (ScanReport, error)

    // Cancel cooperatively cancels a running build.
    Cancel(ctx context.Context, buildID orailtypes.ID) error
}
```

---

## SafeHTTPClient

```go
package ssrf

// SafeHTTPClient is the single shared outbound HTTP client used by
// webhook delivery, git/registry/AI providers, and ACME.
//
// Panel decision pinning (line 124):
//   "SSRF-safe outbound HTTP client: a single shared client used by
//    webhook delivery, git/registry/AI providers, and ACME, denying
//    RFC1918, CGNAT, link-local, loopback, multicast, and cloud
//    metadata endpoints (169.254.169.254, fd00:ec2::254). Hostname
//    resolved once; connection pinned to resolved IP to prevent DNS
//    rebinding. Test fixtures are required."
//
// Implementations MUST refuse to issue a request whose Host header
// disagrees with the pinned IP's reverse PTR check policy.
type SafeHTTPClient interface {
    Do(ctx context.Context, req *http.Request) (*http.Response, error)

    // Allow lists hosts that are explicitly permitted to skip the
    // RFC1918 deny (e.g. an in-cluster ACME staging server). Used
    // sparingly; configured by ops, not by application code.
    Allow(host string)

    // Deny adds an extra denylist entry on top of the panel-locked set.
    Deny(cidr string) error
}
```

---

## RedactionPipeline

```go
package redaction

// RedactionPipeline runs the multi-pass secret redaction used by the
// AI gateway, the log pipeline, audit before/after sanitization, and
// asciicast scrubbing.
//
// Panel decision pinning (line 126):
//   "AI redaction is a multi-pass pipeline with named passes:
//   (1) byte-substring match against the in-memory secret bloom filter
//       with verifying compare;
//   (2) regex pack for AWS/GCP/Azure tokens, JWT, '-----BEGIN', Slack/
//       GitHub token shapes, base64 entropy >32;
//   (3) KEY=val capture against the secret-name table;
//   (4) URL credentials ://user:pass@;
//   (5) Authorization headers.
//   Release gate: secret_leakage_rate == 0 on the fixture set."
//
// Each pass is independently testable. The pipeline composes them; the
// composition order is panel-locked.
type RedactionPipeline interface {
    // Redact runs every pass over the input bytes. Returns the redacted
    // output plus a per-pass report (counts only — the matched values
    // never leave the function).
    Redact(ctx context.Context, in []byte, scope RedactionScope) (Redacted, Report, error)

    // RedactStream is the streaming variant for asciicast and log
    // pipelines. The caller writes; the pipeline buffers per-line and
    // emits redacted lines downstream.
    RedactStream(ctx context.Context, src io.Reader, dst io.Writer, scope RedactionScope) (Report, error)

    // RegisterSecret adds a secret to the in-memory bloom filter (pass 1).
    // Called by SecretStore.Encrypt for every new secret.
    RegisterSecret(ctx context.Context, secret []byte) error

    // UnregisterSecret removes a secret from the filter on rotation.
    // Old secrets are retained until secret_kek_grace_days.
    UnregisterSecret(ctx context.Context, secret []byte) error
}
```

---

## Cross-Cutting Helper Interfaces (referenced by the above)

```go
package db

// Tx is a thin wrapper over *sql.Tx that carries a logical request id and
// the actor identity for audit attribution.
type Tx interface {
    Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
    Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
    QueryRow(ctx context.Context, q string, args ...any) *sql.Row
    RequestID() string
    Actor() orailtypes.Actor
}

// ResourceVersion is an opaque optimistic-concurrency token (panel line 104).
type ResourceVersion string

// Page is the cursor-paginated result envelope (panel line 170).
type Page[T any] struct {
    Items      []T
    NextCursor string  // empty == end
}
```

```go
package idempotency

type IdempotencyKey struct {
    TokenID      orailtypes.ID
    Method       string
    PathTemplate string
    Key          string
}

type IdempotencyState int
const (
    StateNew IdempotencyState = iota
    StateInFlight
    StateSucceeded
    StateFailedTerminal
    StateExpired
)
```

---

## Doc-Comment Style

Every interface above includes a 5–10 line doc comment quoting the panel
decision that pins it. Engineering MUST preserve this comment verbatim when
moving the interface to its package; it is the primary mechanism by which the
panel decisions remain visible at the code seams. Any future ADR that changes
the contract MUST update this comment in the same PR.
