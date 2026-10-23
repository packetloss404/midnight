# Cloudflare DNS provider integration

Status: repository implementation complete, 2026-08-30. Applying it to a real
zone remains an operator gate. Cloudflare is the first provider adapter because
the repository already supports Cloudflare separately for ACME DNS-01
issuance.

## What the connector does

Midnight treats DNS as a provider-backed desired-state integration, not as a
local zone-file editor. Cloudflare is authoritative for the zone and Midnight
reconciles only records backed by its durable ownership ledger. The adapter
uses the provider-neutral `internal/dns` seam and exposes
idempotent operations for:

- listing records,
- creating or updating A, AAAA, CNAME, and TXT records, and
- deleting records only when their durable Midnight ownership is proven.

The worker's first product consumers are:

1. custom-host ownership TXT records;
2. routing records returned as `routing_dns` by the custom-domain API.

The ACME DNS-01 writer remains a separate provider boundary for now; sharing
credentials does not merge its challenge lifecycle with general DNS ownership.

The reconciler detects conflicting A/AAAA/CNAME records, preserves unmanaged
records, retries transient provider failures, and defaults to a dry-run plan.
Cloudflare updates use `PATCH`, preserving provider-specific proxy settings,
comments, and tags outside Midnight's desired-state contract. Unmanaged NS
delegations also block every record type at the delegated name.
Every mutation records a redaction-safe audit intent before the provider call
and a result afterward. A provider response or a rendered Traefik file does not
by itself prove that public DNS or HTTPS is reachable.

Migration 63 stores provider record ids, tenant lineage, type/name/TTL, and a
SHA-256 content digest. It stores neither plaintext record content nor provider
credentials. The digest is also a drift fence: if a previously owned record is
changed outside Midnight, update and deletion both stop in conflict instead of
overwriting the external change. Project/environment deletion is restricted
while provider ownership remains so relational cascades cannot discard the
only safe deletion proof.

## Cloudflare credentials

Use a Cloudflare API token scoped to one zone. The minimum write path needs
DNS Write permission; listing records requires DNS Read or DNS Write. Midnight
requires the configured zone ID and does not perform zone discovery. Cloudflare
documents the record [create](https://developers.cloudflare.com/api/resources/dns/subresources/records/methods/create/), [list](https://developers.cloudflare.com/api/resources/dns/subresources/records/methods/list/), [update](https://developers.cloudflare.com/api/typescript/resources/dns/subresources/records/methods/edit), and [delete](https://developers.cloudflare.com/api/resources/dns/subresources/records/methods/delete/) operations.

Keep the token outside Git, the database, audit payloads, and evidence
bundles. On the installed VM it belongs in the worker-only
`/etc/midnight/cloudflare.env` file with mode `0600`, containing
`CLOUDFLARE_DNS_API_TOKEN=...`. The packaged worker unit reads this optional
file; other Midnight units do not.

The existing ACME implementation and general connector reuse the
`CLOUDFLARE_DNS_API_TOKEN` name, although operators may run them in separate
processes with separately scoped tokens.

## Worker configuration

Set the non-secret values in `/etc/midnight/midnight.env`:

```ini
MIDNIGHT_WORKER_DNS_ENABLED=true
MIDNIGHT_WORKER_DNS_PROVIDER=cloudflare
MIDNIGHT_WORKER_DNS_ZONE_ID=<opaque-cloudflare-zone-id>
MIDNIGHT_WORKER_DNS_ZONE_NAME=example.com
MIDNIGHT_WORKER_DNS_ROUTE_TARGET=apps.example.com
MIDNIGHT_WORKER_DNS_TTL=300
MIDNIGHT_WORKER_DNS_APPLY=false
```

`MIDNIGHT_WORKER_DNS_ROUTE_TARGET` produces CNAME records for non-apex
hostnames. Set `MIDNIGHT_WORKER_DNS_IPV4` and/or
`MIDNIGHT_WORKER_DNS_IPV6` for apex A/AAAA records. Do not configure IPv6
until the address is externally reachable. The controller rejects domain and
ownership-TXT names outside `MIDNIGHT_WORKER_DNS_ZONE_NAME`.

Treat `MIDNIGHT_WORKER_DNS_ZONE_NAME` as an authority grant to this Midnight
instance: every service-managed domain beneath it is eligible for controller
planning. Use a dedicated delegated subzone, not an unrelated shared corporate
zone, unless every Midnight project is intentionally allowed to request names
there.

Restart the worker with `MIDNIGHT_WORKER_DNS_APPLY=false` first and inspect
its one-time startup plan in the audit rows/errors. Only after conflicts and
targets are understood should an operator set
`MIDNIGHT_WORKER_DNS_APPLY=true` and restart. Apply is periodic and idempotent;
deleting a service-managed domain causes the retained ownership rows to drive
provider deletion on a later pass.

## Public route sequence

Cloudflare cannot make the current private VM address (`10.80.13.20`) public.
Before the live proof:

1. Configure the NKC firewall with a public address and NAT TCP `80` and `443`
   to `10.80.13.20`.
2. Confirm the VM firewall and Traefik listen on both ports and that outbound
   DNS/HTTPS works.
3. Add the real domain to Cloudflare and set `MIDNIGHT_APPS_DOMAIN` to the
   chosen generated-host base domain on both the server and worker.
4. Create a service-managed generated domain or add a custom hostname through
   the tenant API/CLI/GUI. Review the controller's dry-run plan for the returned
   TXT ownership record and routing record; do not use the flat
   operator-recovery domain route as a tenant fallback.
5. Run the public HTTP proof from an external vantage point. A subdomain will
   normally use CNAME or A/AAAA as appropriate; an apex record requires the
   operator's A/ALIAS/ANAME strategy. Do not publish an AAAA record until IPv6
   reachability is real.
6. Enable the worker's Cloudflare ACME DNS-01 staging configuration, verify the
   certificate and external `:443` response, then perform the separate,
   explicitly acknowledged production-CA step.

The connector is a DNS control-plane capability; it does not replace route
ownership, Traefik acknowledgement, trusted ingress probes, public firewall
reachability, or certificate readiness. Each remains a separate evidence
gate.

## Evidence and remaining operator gate

Routine tests use an `httptest` Cloudflare API double for authentication,
pagination, CRUD, retries, conflicts, and error redaction. The reconciliation
and ownership store also have race and disposable-PostgreSQL restart coverage.
No routine test requires a Cloudflare account or token.

Run the live lane only with a disposable zone and a narrowly scoped token that
is never copied into source, fixtures, logs, audit payloads, or chat. After a
real-zone apply, re-run installed public HTTP, ACME staging,
restart/renewal, and external HTTPS proofs before describing the capability as
publicly executed. REST/dashboard plan controls and unifying the separate ACME
challenge writer are follow-up ergonomics, not substitutes for that proof.
