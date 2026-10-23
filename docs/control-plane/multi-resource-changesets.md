# Multi-resource changesets and config sync

Status: implemented contract for `MID-103` and reviewed Apply & Deploy extension
`MID-106` (2026-09-03)

## One mutation authority

Midnight has one review and commit boundary for environment configuration:
the durable environment changeset. Browser forms, config-as-code files, and
community templates are inputs to that boundary. They do not call resource
stores independently and they never report a deployment.

Every draft item names one canonical resource kind and operation:

| Kind | Operations | Canonical apply authority |
|---|---|---|
| `variable` | `add`, `edit`, `remove` | encrypted variable store with exact version CAS |
| `runtime` | `add`, `edit` | service-environment configuration store with exact version CAS |
| `networking` | `add`, `remove` | generated-domain and durable-route store with exact version CAS |
| `storage` | `add`, `edit`, `remove` | volume store with exact resource-version CAS and attachment fencing |

The draft retains only redaction-safe runtime, networking, and storage
configuration. Variable values remain envelope-encrypted with the changeset
item ID as AAD. Apply revalidates session generation, membership, environment
restriction, service lineage, every kind-specific capability, every target
version, and every uniqueness constraint in the same serializable transaction
that commits all resource mutations, changeset state, idempotency response,
and audit evidence.

Required write capabilities are `variables.write`, `service.write`,
`domains.write`, and `volume.write`; storage removal additionally requires
`volume.delete`. Reading or validating a draft similarly checks the
kind-specific read surface. A caller that lacks any required capability cannot
read, replace, apply, or discard the mixed draft.

Apply-only changes desired state and does not start, restart, or promote a
workload. The reviewed Apply & Deploy mutation commits that same desired-state
transition, advances the environment revision, snapshots every affected
service, and records the server-derived deployment intake in the same
serializable transaction. Build-input changes select `rebuild`; runtime-only
changes select immutable-image `redeploy`; unaffected services select `none`.
Workers remain the authority for build, runtime, health, and promotion results.

## Config-as-code synchronization

`POST /api/v1alpha/config/sync` accepts one strict, bounded environment sync
document and replaces that environment's active changeset using the same
revision and idempotency contract as `PUT .../changeset`. It returns the
durable changeset review; it does not apply it. The CLI prints the revision and
requires a separate explicit transition command. The current CLI exposes
apply-only; the console and API expose the primary Apply & Deploy transition,
with CLI parity tracked as `MID-107`.

The file format uses stable project, environment, service, and resource IDs so
the server never guesses a tenant scope from a display name. A generated
export may use slugs for human display, but synchronization is ID-bound.

## Community templates

A valid project-owned local community fork may be compiled into a changeset
for one existing environment. Template service slugs must resolve to existing
services in that project for this release. The compiler maps environment
values to variable additions, image/runtime intent to service configuration,
and declared volumes to storage additions. Generated values are created only
when the server seals the draft. Community template application never inherits
official trust and never invokes the official database provisioning saga.

Service creation remains an explicit product operation. After a compiled
template draft is reviewed, Apply & Deploy may enqueue affected existing
services through the same environment authority. This keeps template review
atomic without weakening service-creation or deployment authorization.

## Recovery and replay

Draft replace, config sync, apply, Apply & Deploy, and discard use strong
`If-Match` plus one `Idempotency-Key`. Exact terminal replays return the stored
response after fresh authorization. Stale revisions return the committed
precondition failure. A process restart cannot lose the active draft, its
validation evidence, immutable source provenance, configuration revision, or
operation group.

Apply & Deploy can return a `partial` group when configuration committed but a
safe deployment intake could not be created for every affected service. That
state is durable and itemized; it is never rewritten as complete or rolled
back implicitly. Group retry and dependency ordering remain future work.
