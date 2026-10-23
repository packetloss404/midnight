# ADR-0042: Evidence-backed usage showback and internal admission

- Status: Accepted
- Date: 2026-08-13
- Amended: 2026-08-31 (migration 0069)

## Context

Midnight needs a Usage & Limits surface without inventing billable data or
coupling donations to product access. Existing durable sources cover artifact
charges, canonical resource counts, sampled runtime metrics, and scoped
Traefik access logs. Docker network totals do not distinguish public from
private traffic, and volume requested capacity is not a kernel-enforced quota.

## Decision

Store immutable usage events with a database sequence and a unique
`(source, source_instance, source_event_id)` replay identity. Resource gauges
record state transitions only; the previous ledger sequence is part of the
next transition identity, so `1 → 2 → 1` remains three distinct events while
unchanged reconciliation is a no-op.

Expose one read-only human-session API for workspace showback. Every dimension
reports evidence coverage and open gaps. Keep admission reservations internal
to trusted coordinators, revalidate accepted workspace roles in PostgreSQL,
serialize reservations and count writes by workspace, and persist admission
decisions. Single-row project/service/environment/volume inserts have database
triggers so alternate writers cannot bypass count limits. Atomic changesets
reserve a positive net logical-volume delta and commit the canonical mutation,
reservation, and resulting gauge in one serializable transaction.

Canonical source-build transitions record exact `build_started_at` and
`build_completed_at` markers. A replay-safe producer emits one immutable
duration event per completed build and explicit gaps for pre-marker or missing
terminal evidence. The deploy worker runs bounded, overlapping reconciliation
independently of reads under a single-installation advisory lock; interactive
reads retain a smaller bounded repair tail.

Use the term **policy profile**, with a single `self_hosted` profile. Resource
count limits default to informational/unlimited until an installation owner
explicitly configures policy; the release does not impose arbitrary fixed
caps. The source-artifact line reflects the upload subsystem's actual runtime
quota when that subsystem is enabled. This is installation safety policy, not pricing. Ko-fi or any future contribution link
is presentation-only: no donation/payment identifier enters the schema,
authorization, usage ledger, or admission decision.

## Consequences

- Showback is honest when evidence is absent or incomplete.
- CPU and memory are sampled estimates, not invoice-grade measurements.
- Public response bytes are Traefik transmitted bytes only; request-body bytes
  and Docker network totals are excluded.
- Source bytes and resource counts can be safely enforced today.
- Build duration is exact only for source deployments carrying both canonical
  markers; older deployments remain explicit evidence gaps.
- Logical volume count can be enforced, while requested capacity bytes remain
  informational until a host quota driver proves enforcement.
- A later payment system must consume independently verified usage and policy;
  it cannot redefine historical evidence or infer entitlement from donations.
