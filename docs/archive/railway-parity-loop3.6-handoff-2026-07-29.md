# Railway parity Loop 3.6 handoff — worker-owned ACME lifecycle

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

Status: implementation and repository-owned gates are complete; committed and
pushed as `77e66d7` on `main`. External certificate proof remains.

## Delivered

- `midnight-worker` now has an opt-in domain lifecycle runner. Disabled mode is
  the default and does not construct an issuer or perform network work.
- Explicit worker configuration covers ACME email, absolute state directory,
  DNS-01 provider (`cloudflare`, `route53`, or `manual`), public resolver list,
  HTTP timeout, reconcile interval, and staging/production directory choice.
- Staging is the default. Production requires
  `MIDNIGHT_WORKER_ACME_PRODUCTION_ACK=true` in addition to the provider
  credentials.
- The worker constructs the existing Lego issuer and durable
  `DomainLifecycleController`, resolves ownership TXT evidence through the
  host resolver, records certificate-order references, and retries pending
  domains on startup and at the configured interval.
- Route publication remains fail-closed while `TLSDesired` lacks durable
  ready evidence. Certificate references are paths/URLs only; private-key
  bytes never enter the database, API projection, or logs.
- Durable Traefik route publication now carries the explicit worker resolver
  (`letsencrypt-staging` or `letsencrypt-prod` by default when ACME is enabled)
  instead of emitting an unqualified `tls: {}` route. The resolver name must
  match the operator-owned static Traefik configuration.
- Ready orders inside the 30-day renewal window are reconciled again. Renewal
  failure retains the last ready domain state and uses the same bounded retry
  backoff rather than withdrawing a currently working route.

## Configuration boundary

```sh
MIDNIGHT_WORKER_ACME_ENABLED=true
MIDNIGHT_WORKER_ACME_EMAIL=ops@example.com
MIDNIGHT_WORKER_ACME_STATE_DIR=/var/lib/midnight/acme
MIDNIGHT_WORKER_ACME_DNS_PROVIDER=cloudflare
MIDNIGHT_WORKER_ACME_ENVIRONMENT=staging
```

Provider credentials remain in the provider's own environment contract. The
worker does not accept credentials in CLI/API request bodies. Manual DNS is
available for operator-controlled TXT publication; it is not an automatic
provider integration.

## Honest boundary

This loop wires issuance and durable state, not a claim of Railway-level
automatic HTTPS. The issuer persists certificate material under its configured
state directory, but operator Traefik certificate loading, delegated DNS,
off-host reachability, browser HTTPS, renewal, wildcard reuse, and production
rate-limit evidence still require the external release runner. Do not mark
the product HTTPS-complete from a worker log or a certificate file alone.

## Verification

- `go test ./cmd/midnight-worker ./internal/router ./internal/acme`
- `go test ./...`
- `CGO_ENABLED=0 go vet ./...`
- `CGO_ENABLED=0 go test -race ./internal/api ./internal/router ./internal/deploy ./cmd/midnight`
- `go run ./tools/contract-tests`
- `MIDNIGHT_PARITY_REPEAT=1 scripts/railway-parity-loop.sh fast`
- `scripts/railway-parity-loop.sh integration`
- `scripts/railway-parity-loop.sh security`
- `scripts/railway-parity-loop.sh ui`

The live ACME staging test remains deliberately gated behind
`MIDNIGHT_LE_STAGING=1` and real provider credentials; no routine gate fakes
that external proof.

## Follow-on status

Custom TLS intent, CLI/GUI parity, and renewal-safe lifecycle behavior are
implemented in the Loop 3.7 checkpoint. The remaining release gate is still
certificate installation/public DNS/HTTPS proof on an operator-controlled VM.
