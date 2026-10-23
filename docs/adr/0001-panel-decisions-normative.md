# ADR-0001: Adopt Panel Locked Decisions As Normative

- Status: accepted
- Date: 2026-04-25
- Owners: PM-A Delivery Coordination, technical lead

## Context

The Midnight planning bundle is governed by the Eight-Specialist Panel Locked Decisions in `midnight-platform-plan.md`.

## Decision

The panel decisions are normative for Phase 0 implementation. Stories, tests, runbooks, and release gates must cite the relevant decision when they touch a panel-locked surface.

The resource ID format is normative: `<prefix>_<26-char Crockford base32 ULID>` for time-sortability. Reserved prefixes are additive and documented in the API/schema contracts.

## Consequences

- A conflicting implementation requires an ADR before code lands.
- Release gates must test panel-locked behavior, not only document it.
- PM-A tracks dependency closure; PM-B tracks release-risk impact.
