# Durable environment changesets

Status: implemented v1alpha multi-resource Apply & Deploy authority
(2026-09-03)

## Decision

Midnight uses one active, tenant-scoped changeset per environment as the review
boundary between browser edits and desired-state mutation. A browser edit is
only an input buffer. `PUT /environments/{environment_id}/changeset` encrypts
the proposed values and persists the complete draft with a revision; a
successful response permits the browser to erase its plaintext buffer.

The changeset lifecycle is:

1. `GET .../changeset` returns the active draft, validation, capabilities, and
   redaction-safe change descriptions.
2. `PUT .../changeset` creates (`expected_revision: 0`) or replaces a draft by
   exact revision compare-and-swap.
3. `POST .../changeset/validate` rechecks current targets and versions without
   mutating desired state.
4. `POST .../changeset/apply-and-deploy` commits every supported mutation,
   advances the environment configuration revision, snapshots affected
   services, and records their server-selected deployment intake in one
   serializable PostgreSQL transaction.
5. `GET .../operation-groups/{group_id}` reports aggregate and per-service
   deployment progress for that applied revision.
6. `POST .../changeset/apply` is the Advanced apply-only action. It commits
   desired state and advances the applied revision without deployment intake.
7. `POST .../changeset/discard` terminates the draft without changing desired
   state.

Every state mutation requires one human session, current environment authority,
`If-Match`, and `Idempotency-Key`. The read-only validation POST uses the
revision in its body and does not require an idempotency key; operation-group
reads are ordinary tenant-scoped reads. Session validity, membership,
environment restriction, idempotency, state, exact response, and audit
evidence share the same transaction authority. Missing, foreign, and
policy-concealed scopes are indistinguishable.

## Secret handling

Before envelope/KMS work, the coordinator proves the current session's exact
environment authority and every structurally valid service target. The
serializable mutation transaction repeats that authorization with a fresh
clock after encryption, so revocation, expiry, and membership changes during
sealing fail closed. A terminal exact replay is still authorization-gated but
returns its committed response without resealing.

Variable values are encrypted before persistence with a fresh envelope whose
AAD is the changeset-item ID. Request bytes are keyed-fingerprinted for
idempotency and cleared before KMS work; decoded value references and prepared
envelope buffers are released or cleared as soon as their boundary completes.
Go strings are immutable, so this is lifetime minimization rather than a claim
that every allocator copy can be overwritten. Responses, validation,
idempotency responses, and audit rows contain only `value set`, `value
replaced`, or `value removed`.
Apply decrypts inside the control plane and re-encrypts under the canonical
variable ID. Apply and discard erase the transient changeset envelope in the
same terminal transaction. Historical rows retain identity, operation, scope,
revision, and timestamps, not proposed plaintext or reusable ciphertext.

## Supported resource kinds

The durable item model supports `variable`, `runtime`, `networking`, and
`storage`. Runtime items update the canonical service/environment config;
networking items add or remove the service's generated public-domain intent;
storage items create, grow, or remove canonical volume intent. Every edit or
removal carries the target's exact version. Storage removal also proves that
no active attachment exists.

The console stages runtime settings, generated-domain changes, new volume
intent, and community-template drafts through this authority. Config sync is
another producer of the same document. While a durable draft exists, these
single-editor surfaces require applying or discarding it before replacing it.
That prevents a client which cannot recover write-only variable values from
silently deleting encrypted draft entries.

All four kinds are committed in the same serializable transaction. A stale or
invalid item prevents every item from applying. Apply-only changes desired
state and creates no deployment, restart, workload, or external reachability
claim. Apply & Deploy uses the same desired-state commit and additionally
records the exact deployment intake described below.

## Changes, Activity, and first-deploy evidence

The project workspace presents two durable sources as separate concepts:

- the Changes bar and review sheet come only from the durable changeset API;
- Activity comes only from canonical deployment, operation, and
  operation-group records.

Both survive refresh because neither is browser state. The console never uses
a percentage or terminal claim inferred from elapsed time. The first-deploy
resume flow likewise derives only from saved service, source, deployment, and
active-workload evidence. Its incomplete markers are facts about missing
evidence, not predicted progress.

## Atomicity boundary

Apply & Deploy never asks the browser to decide whether a service needs a
build. Validation returns one server-derived `deployment_action` per affected
service:

- `rebuild` only when normalized build inputs changed and a canonical source
  can be pinned into a fresh immutable snapshot;
- `redeploy` when the active immutable image can be replayed with the newly
  applied runtime configuration;
- `none` when the service requires no rollout.

The transaction advances the environment's applied revision, records each
service's applied revision, creates the operation group, and commits every
successful canonical deployment, operation, snapshot, outbox row, and group
item together. Deployment workers remain the authority for build, runtime,
health, and traffic-promotion outcomes. When a deployment reaches the active
state, its service revision advances to the corresponding deployed revision.
The group read derives `accepted`, `partial`, `running`, `succeeded`, or
`failed` from those durable rows.

If configuration can commit but a service has no safe deployment candidate,
the response is `202` with a truthful `partial` group and item-level failure;
the applied revision is not hidden or rolled back. Intake order is stable by
service identifier. Group retry and dependency-DAG ordering are outside this
release.

The console exposes Apply & Deploy as the primary action and apply-only under
Advanced. The generated client exposes both API operations. The CLI currently
supports changeset `get`, `validate`, `apply`, and `discard`; a direct
`apply-and-deploy` and operation-group wrapper is tracked as `MID-107`.

Item-level collaborative amendment and a configurable retention policy for
redaction-only terminal history are intentionally outside this release.
