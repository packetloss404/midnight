# ADR-0035: Human source uploads use content-addressed storage and atomic ownership

- Status: accepted
- Date: 2026-07-28

## Context

`midnight up` originally let the API extract a client archive into a temporary
directory and enqueue a `local://` path. That compatibility path can create a
canonical deployment, but it is not a safe human-tenant boundary: authorization
can change while a large body is being read, API-side extraction expands the
attack surface, shared checkout paths are vulnerable to premature cleanup, and
database state can outlive an untracked filesystem artifact.

An idempotency lookup cannot safely happen before consuming the archive. Doing
so would let a reused key conceal changed source bytes. Conversely, committing
database ownership before the blob is durable can publish work that the build
worker can never materialize.

## Decision

The exact human-session route
`POST /api/v1alpha/services/{service_id}/up` uses a separate, explicitly wired
capability. Project automation and recovery-operator credentials retain the
legacy staged-extraction path until that compatibility surface is migrated
independently.

The human path:

1. validates singleton authentication, idempotency, environment, optional
   message, and content-type headers;
2. authorizes the current session and target before reading the body;
3. streams the bounded gzip archive into private staging while hashing the
   exact compressed bytes;
4. rejects traversal, absolute paths, links, devices, FIFOs, duplicate paths,
   malformed streams, and compressed, entry-count, or expanded-size limit
   violations;
5. fsyncs and atomically publishes the validated bytes at an immutable
   digest-derived path;
6. revalidates the current session, grant, project, service, and environment in
   the accepting `SERIALIZABLE` transaction; and
7. commits the idempotent response, operation, deployment, initial event,
   outbox intent, artifact metadata, ownership reference, and redacted audit
   evidence in that same transaction.

The request fingerprint binds the concrete route, service, environment,
optional message, archive digest, and compressed length. A replay therefore
still consumes, validates, and hashes the complete body before it can return
the stored response.

Migration 0046 adds durable, replica-safe admission and logical-retention
accounting for this human-session path. The client must advertise one
canonical `X-Midnight-Archive-Digest` and an exact positive `Content-Length`.
After current-authority preflight, the API reserves the declared bytes before
reading the body. Publication then reauthorizes and holds the quota-global,
workspace, and artifact-lifecycle locks while atomically publishing the
verified blob and recording the conservative crash marker. Accepted or
idempotently replayed intake transfers that marker into a durable
workspace-and-digest charge before consuming the reservation.

The default policy is 5 GiB of retained logical source archives per workspace,
200 MiB and four concurrent uploads per workspace, and 1 GiB and 32 concurrent
uploads installation-wide. Reservations use a ten-minute upload lease and a
26-hour conservative orphan charge. Explicit configuration is parsed strictly
by both the API and build worker; unsafe disagreements with the upload or
janitor retention contract fail startup.

The API never extracts a human upload. The build worker rehashes the immutable
blob and materializes a unique checkout for each attempt. Checkout release
removes only that attempt directory. Durable artifact ownership is released
only after the queue lease reaches a persisted terminal state; reconciliation
heals a crash between queue terminalization and reference release.

Migration 0045 stores immutable `source_artifacts` metadata and one
operation/deployment-scoped `source_artifact_refs` owner. A composite foreign
key requires the named operation and complete project/service/environment tuple
to be the exact lineage persisted on the named deployment; individually valid
IDs from different lineages cannot be mixed. The optional 256-byte printable
message is stored only on that ownership row. Raw messages and storage paths
are not included in source URIs, worker events, or audit payloads.

Filesystem publication precedes the database transaction. Final binding takes
the same PostgreSQL lifecycle advisory lock used by garbage collection and
rehashes the canonical blob while that lock is held. A collector may win
before binding, in which case admission fails closed; once admission commits,
accepted work cannot point at a blob the collector already removed. A crash or
rejected transaction can leave an unreferenced blob for later collection.

The build worker owns the automatic production janitor. At startup and on a
bounded ticker it runs separate-timeout content-addressed and legacy passes.
The PostgreSQL pass uses a nonblocking lifecycle lock, releases references only
from durable terminal queue truth, rotates bounded discovery cursors, caps both
candidate count and cumulative hash bytes, and applies a two-pass quarantine
grace before deletion. It restores quarantined blobs if database mutation
rolls back or an active reference is rebound. A separate legacy pass
age-cleans owned abandoned staging and checkout directories. Corrupt or
unexpected filesystem entries fail closed, and janitor failures never stop the
build worker.

The retained-byte policy is a logical workspace quota, not a kernel-enforced
filesystem quota or a global host-disk watermark. One digest is charged once
per workspace even when multiple deployments own it; the same physical digest
used by two workspaces is charged to both. Random staging files and the
retained project-automation/recovery-operator compatibility path are not
covered by this logical quota. After a successful content-addressed janitor
pass, proof reconciliation releases a reservation or ledger charge only when
neither the canonical nor quarantined locator exists. Time alone never proves
that bytes disappeared.

Quota rows deliberately use restrictive ownership foreign keys. Active or
crash-ambiguous reservations temporarily block deletion of their scoped
project, service, environment, session, or workspace; retained ledger charges
block hard workspace deletion even after operation/deployment lineage
cascades. Cleanup and proof reconciliation must retire those accounting rows
before the corresponding hard delete can complete. This favors conservative
storage ownership over silently orphaning bytes during administrative purge.

## Consequences

- A `202` response means the build is durably queued; it does not mean the
  deployment is healthy or publicly reachable.
- Local source deployment remains private by default and does not create a
  domain.
- Human-session uploads have a hard 50 MiB compressed maximum.
  `MIDNIGHT_UP_BODY_LIMIT_BYTES` may lower that ceiling, but cannot raise it.
  The retained legacy automation/operator extraction route continues to use
  its separately configurable body-limit semantics until it is migrated.
- Human-session uploads require an exact positive `Content-Length` and one
  canonical `X-Midnight-Archive-Digest: sha256:<lowercase-hex>` header.
  Retained logical quota exhaustion returns `413`; temporary workspace or
  installation-wide upload backpressure returns `429` with `Retry-After`.
- API and build-worker processes must share the configured
  `MIDNIGHT_UP_EXTRACTION_ROOT`. For human uploads this is the durable
  content-addressed blob, staging, and isolated-checkout root; for the retained
  compatibility path it is also the legacy extraction root. The packaged
  installation sets it to `/var/lib/midnight/uploads`; human `up` remains
  unavailable when a durable shared root is not configured.
- Content-addressed duplicates share one immutable blob while retaining
  separate operation/deployment ownership rows. Logical charging de-duplicates
  within a workspace, not across workspace security boundaries.
- Project automation and recovery-operator uploads still use the documented
  legacy extraction path and must not be described as content-addressed until
  their own migration lands.
- The logical quota does not cap random staging bytes, the legacy compatibility
  path, or total host storage. Operators still need filesystem capacity
  monitoring and an appropriately isolated upload volume.
- Automatic cleanup is intentionally delayed and bounded. Operators can tune
  the worker's source-artifact GC interval, pass timeout, retention windows,
  item count, and verification-byte budget; unsafe values fail worker startup.
