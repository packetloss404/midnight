# ADR-0002: Adopt Midnight Name And Prefixes For Phase 0

- Status: accepted
- Date: 2026-04-25
- Owners: PM-A Delivery Coordination

## Context

Phase 0 requires stable public names so package, binary, API, token, and documentation work do not drift.

## Decision

- Public project name: Midnight.
- Go module path: `github.com/midnight/midnight`.
- Binary prefix: `midnight-*`; user CLI binary: `midnight`.
- API token prefix: `midn_`.
- Dashboard package manager for Phase 0: npm with `package-lock.json` v3.

Exact BuildKit and registry binary responsibilities remain tracked in `agile/handoff/PHASE0_DECISION_LOG.md` until S01/S02/S05 close that decision.

## Consequences

- CI and docs use Midnight names immediately.
- Token handling code must reject non-`midn_` token transports.
- npm is the only accepted web package manager for v0 scaffolding.
