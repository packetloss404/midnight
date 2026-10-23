# Usage, Limits, and Abuse Foundations

Status: durable showback and safe admission foundation; no commerce.

Migration 0059 adds a replay-safe, tenant-scoped usage ledger, explicit
evidence gaps, a local policy-profile catalog, and transactional reservations.
Migration 0069 adds canonical build lifecycle markers, an independent worker
reconciliation loop, and logical-volume admission for atomic changesets.
The authenticated product API exposes read-only showback at
`GET /api/v1alpha/usage/summary`. Reservations are an internal coordinator
seam: browsers and general API clients cannot reserve arbitrary quantities.

## Evidence contract

| Dimension | Producer | Coverage | Enforcement |
|---|---|---|---|
| Source artifact bytes | `source_artifact_workspace_charges` | Complete durable gauge | Existing source-upload quota plus policy profile |
| Projects, services, environments | Canonical resource rows | Complete durable gauge | Transaction-serialized insert trigger |
| Logical volumes | Canonical active volume rows | Complete durable gauge | Changeset reservation plus transaction-serialized trigger |
| CPU seconds | Docker metric samples | Partial sampled estimate | Showback only |
| Memory byte-seconds | Docker metric samples | Partial sampled estimate | Showback only |
| Public HTTP requests | Retained Traefik access records | Partial, with collector gaps | Showback only |
| Public HTTP response bytes | Retained Traefik `tx_bytes` evidence | Partial, with collector gaps | Showback only |
| Build seconds | Canonical deployment build start/completion markers | Exact per marked source build; pre-marker rows are explicit gaps | Showback only |

Runtime CPU and memory are derived only between consecutive raw samples no
more than five minutes apart. Larger intervals become durable evidence gaps;
they are never silently interpolated. The response reports `complete`,
`partial`, or `unavailable` per dimension and includes a replay sequence.

Docker network RX/TX counters are excluded because they combine public and
private traffic. They cannot support public-egress billing or enforcement.
Volume requested capacity remains excluded from enforced byte usage because
Midnight does not currently prove kernel quota assignment. Logical active
volume count is separate control-plane evidence and can be enforced.

The deploy worker reconciles resource gauges, build durations, runtime
samples, and Traefik records at startup and once per minute by default. A
session advisory lock elects one worker replica, each pass has a whole-pass
deadline and per-producer budget, and least-recently-checkpointed workspaces
run first. Overlapping producer windows and immutable replay identities make
late evidence and worker restarts safe. Interactive reads retain a much
smaller bounded reconciliation tail but are not required to keep evidence
current.

## Policy and admission

`self_hosted` is an installation policy profile, not a commercial plan. Count
limits default to informational/unlimited; this release does not impose fixed,
unchangeable caps. When an installation owner explicitly configures a count
policy, inserts take a workspace advisory transaction lock, so concurrent
writers cannot both pass a stale count. The source artifact line reflects the
runtime's existing configured upload quota when uploads are enabled. Atomic
environment changesets reserve only a positive net logical-volume delta,
apply removals before additions, and commit the resulting volume gauge with
the canonical resources in the same serializable transaction. This preserves
limit-neutral swaps. Reservations use source intent identity, idempotent
replay, expiry, and durable admit/deny decisions.

The read API requires a verified human session and an accepted workspace
membership. Cross-tenant misses are concealed as `404`. Internal reservations
revalidate admin/member authority inside PostgreSQL.

Donations, Ko-fi, payment customers, subscriptions, invoices, and payment
providers are absent from the backend. Contributions never assign a policy
profile or change an entitlement.

## Legacy experimental planner

`POST /api/v1alpha/billing/metering-limits-abuse/plan` remains an experimental,
in-memory policy simulator behind `MIDNIGHT_EXPERIMENTAL=1`. It is not the
durable product contract and performs no admission or persistence.

## Commerce boundary

This design intentionally stops at evidence and installation safety policy.
Payment integration requires separate approval, must remain downstream of
evidence correctness, and cannot use donation state.
