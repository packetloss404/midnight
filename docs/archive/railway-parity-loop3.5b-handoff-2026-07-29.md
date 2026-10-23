# Railway parity Loop 3.5b handoff — tenant custom HTTP hostnames

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

Status: implementation and repository-owned parity gates complete. Checkpoint
commit `e961147` is pushed to `main`.

## Delivered

- Migration 0053 adds bounded custom-domain challenge evidence and a safe
  downgrade refusal for live tenant custom bindings.
- Human-session nested REST routes:
  `custom-domains` list/create/get/delete plus `/{domain_id}/verify`.
- Create accepts only a hostname, normalizes it server-side, mints a fresh
  opaque domain ID and random TXT challenge, fixes `/`, the canonical target
  port, and TLS off, and leaves the route private.
- Verify reauthorizes before and after DNS I/O, bounds TXT record count/bytes,
  handles exact-value ownership only, CAS-checks the current version/challenge,
  and atomically enables the route, binds open deployments, and refreshes ready
  backends. Completed idempotency keys are checked before resolver I/O.
- Custom routes are excluded from publication, promotion, and deploy
  inheritance until the current challenge is verified. Recovery custom rows are
  not silently adopted into the tenant product surface.
- CLI: `midnight domain custom list|add|verify|delete`.
- GUI: expandable service Networking custom-host flow with one-click copyable
  TXT instructions and separate ownership/route/TLS/public-reachability
  wording.
- OpenAPI, migration contract tests, README, API docs, changelog, and parity
  review are updated.

## Honest capability boundary

This loop proves only that the configured resolver observed the exact TXT value
at the dedicated name and that the local route evidence may converge. It does
not configure CNAME/ALIAS records, prove public DNS or off-host reachability,
issue TLS certificates, support wildcard ownership, or automate DNS providers.

## Verification already run

- `go test ./...` and `CGO_ENABLED=0 go test ./...`
- custom handler strict-body, route, idempotency, and typed-error tests
- full `CGO_ENABLED=0 go test -tags=integration ./internal/api/...`
- `scripts/railway-parity-loop.sh integration` (all declared Docker-backed
  PostgreSQL/API/deploy/TCP integration packages)
- `CGO_ENABLED=0 go vet ./...`
- focused `CGO_ENABLED=0 go test -race ./internal/api ./internal/router
  ./internal/deploy ./cmd/midnight`
- PostgreSQL integration rollback gate:
  `CGO_ENABLED=0 go test -tags=integration -run
  TestPostgresIdentityClaimSessionRestartAndTenantConcealment ./internal/api`
- `go run ./tools/contract-tests` (161 operations; 0 missing codes; 0 missing
  protected 401 responses)
- `MIDNIGHT_PARITY_REPEAT=2 scripts/railway-parity-loop.sh fast`
- `scripts/railway-parity-loop.sh security` (including live Docker isolation)
- `scripts/railway-parity-loop.sh ui` (17 Chromium checks plus the real API
  browser flow; evidence retained under `.midnight/parity-evidence/ui/`)
- frontend focused domain tests and `npm run build`

## Next gate sequence

1. Loop 3.5c adds redaction-safe routing-DNS guidance for subdomain CNAME and
   apex A/ALIAS/ANAME setup; see
   `docs/archive/railway-parity-loop3.5c-handoff-2026-07-29.md`.
2. Continue with TLS/ACME as a separate gate; do not widen this loop’s claims
   while certificate and public-HTTPS dependencies are absent.
