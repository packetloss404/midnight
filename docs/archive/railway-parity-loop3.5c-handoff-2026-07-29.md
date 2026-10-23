# Railway parity Loop 3.5c handoff — routing DNS guidance

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

Status: implementation and repository-owned parity gates complete; committed
and pushed as `6789845` on `main`.

## Delivered

- Tenant custom-domain projections now include a redaction-safe `routing_dns`
  object alongside the ownership TXT challenge.
- Subdomains receive a CNAME target under the configured
  `MIDNIGHT_APPS_DOMAIN`.
- Apex hostnames receive explicit A/ALIAS/ANAME guidance and an
  `operator_required` state; Midnight does not invent an edge IP or pretend
  that a CNAME is valid at the zone apex.
- Missing `MIDNIGHT_APPS_DOMAIN` is surfaced as `unavailable`, rather than
  returning an incomplete target.
- REST/OpenAPI, frontend validation, the Networking panel, CLI-facing docs,
  changelog, parity review, and unit coverage are updated.

## Honest capability boundary

This is DNS setup guidance, not provider automation or DNS verification.
Midnight does not mutate Cloudflare/Route53/etc., poll authoritative CNAME/A
records, prove public Internet reachability, issue certificates, or support
wildcard `_acme-challenge` delegation in this loop. The route’s existing
ownership TXT verification remains a separate evidence lane.

This follows Railway’s documented split between CNAME/TXT setup for
subdomains, apex ALIAS/CNAME-flattening choices, and automatic certificate
issuance; it does not copy Railway’s provider integrations or UI.

- https://docs.railway.com/networking/domains/working-with-domains
- https://docs.railway.com/integrations/api/manage-domains

## Verification

- `go test ./internal/api ./internal/router`
- `CGO_ENABLED=0 go vet ./...`
- `CGO_ENABLED=0 go test -race ./internal/api ./internal/router ./internal/deploy ./cmd/midnight`
- `go run ./tools/contract-tests` (161 operations; 0 missing codes; 0 missing
  protected 401 responses)
- `MIDNIGHT_PARITY_REPEAT=1 scripts/railway-parity-loop.sh fast`
- `scripts/railway-parity-loop.sh integration`
- `scripts/railway-parity-loop.sh security`
- `scripts/railway-parity-loop.sh ui` (17 Chromium checks plus the real API
  browser flow)
- `npm --prefix web-next test -- --run` (54 files, 279 tests)
- `npm --prefix web-next run build`

## Follow-on status

The worker-owned, staging-first ACME lifecycle is implemented in the following
Loop 3.6 checkpoint. The remaining gate is certificate installation into the
operator's Traefik state plus delegated DNS/public HTTPS evidence; this loop
does not claim that external proof.
