# Railway parity Loop 3.7 handoff — custom TLS intent and renewal

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

Status: implementation and repository-owned gates are complete; the feature
slice is `a03d8ee` and the follow-up acceptance-auth fix is `06fb8ee`, both
committed and pushed on `main`. External certificate proof remains.

## Delivered

- Tenant custom-domain creation accepts an optional `tls` boolean through the
  nested REST/OpenAPI contract. Omitted TLS remains HTTP-only for compatibility;
  `tls=true` requests the worker-owned certificate lifecycle.
- Custom TLS intent is durable in the existing `domains.tls_desired` state and
  is visible in REST, CLI, and GUI projections. The CLI supports
  `midnight domain custom add --tls HOST`; the Networking panel exposes a
  clearly labeled HTTPS checkbox.
- Exact TXT ownership verification still gates custom route intent. A TLS
  custom route may become public intent after ownership proof, but durable
  route publication remains blocked until certificate-ready evidence exists.
- Custom-domain binding accepts this staged TLS state without weakening the
  generated-domain readiness gate. Deployment promotion and durable route
  rendering continue to fail closed while TLS is unavailable.
- The worker lifecycle now selects ready orders inside a 30-day renewal window.
  Renewal failures retain the existing ready domain state and retry with the
  same bounded durable backoff.
- CLI strict decoding now includes the redaction-safe `routing_dns` object,
  preventing the prior response-schema mismatch.

## Honest boundary

This is an explicit request and durable admission path, not a claim of public
HTTPS. The worker must be configured with ACME provider credentials and an
operator-owned Traefik resolver. DNS-provider mutation, certificate loading
into Traefik, delegated DNS, browser/off-host reachability, wildcard reuse,
and production CA evidence remain external release gates.

## Verification

- `go test ./...`
- `CGO_ENABLED=0 go vet ./...`
- `CGO_ENABLED=0 go test -race ./internal/api ./internal/router ./internal/deploy ./cmd/midnight`
- `go run ./tools/contract-tests`
- `MIDNIGHT_PARITY_REPEAT=1 scripts/railway-parity-loop.sh fast`
- `scripts/railway-parity-loop.sh integration`
- `scripts/railway-parity-loop.sh security`
- `scripts/railway-parity-loop.sh ui` (17 Chromium checks plus the real API
  browser flow)

The self-started MVP acceptance server now configures a deterministic local
bearer and sends it through REST, CLI, and GUI probes, so the local surface
agreement gate exercises the same explicit authentication contract as an
installed server.

The live ACME staging test remains gated behind real credentials and
`MIDNIGHT_LE_STAGING=1`; no local gate fakes public CA evidence.

## Next gate

Run the delegated-DNS Let's Encrypt staging proof on a clean VM, verify that
the operator's Traefik resolver loads the issued material, then exercise
restart/restore, public `:443`, SAN/expiry, renewal, and production promotion.
