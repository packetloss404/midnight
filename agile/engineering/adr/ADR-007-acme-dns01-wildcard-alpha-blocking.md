# ADR-007: DNS-01 + wildcard for the apps-domain is Alpha-blocking

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, alpha-blocking, networking, acme, tls, panel-reclassification]

## Context

Midnight issues a generated HTTPS hostname per service under the operator's platform `apps-domain` (e.g., `<service>.<environment>.<project>.<apps-domain>`). The platform `apps-domain` is one **registered domain** at Let's Encrypt's policy boundary. Let's Encrypt's main rate limit is **50 certificates per registered domain per week**. With HTTP-01-only issuance, every generated hostname consumes one cert against that single registered domain. The math collapses fast: a single design partner with 5 services × 3 environments = 15 certificates per week from that workspace alone. Tens of services across a handful of workspaces will throttle within the first week of operation.

The panel reviewed this and reclassified DNS-01 + wildcard from "later" to **Alpha-blocking**:

> "DNS-01 + wildcard certificates for the platform `apps-domain` are Alpha-blocking, not deferred. HTTP-01-only generated domains hit Let's Encrypt 50-cert/registered-domain/week within tens of services. A typed `DnsProvider` interface (`presentTxt`, `cleanupTxt`, `getZone`, `propagationCheck`) ships in Alpha with at least Cloudflare, Route53, and `manual` providers; credentials live in the platform secret store." (line 153)

> "ACME state (`acme.json`) is part of the platform backup set; restore restores it byte-exact with `0600` ownership before Traefik starts. Cold-start without prior ACME state batches issuance at ≤ N requests/min with jittered retry, hard-caps at 40 issuances/registered-domain/week to leave headroom under the 50/week LE limit, and auto-falls back to LE staging if the budget is exceeded." (line 154)

> "CAA preflight: lifecycle resolves CAA on the registered domain and parents before submitting to ACME. Refusal surfaces remediation, never burns a failed-validation budget." (line 156)

> "Wildcard custom domains (`*.example.com`) require DNS-01, ownership TXT at `_midnight-verify.<apex>`, more-specific subdomain wins over wildcard at routing, and is documented explicitly." (line 158)

> "DNS-01 + wildcard for the platform `apps-domain` is Alpha-blocking, not deferred." (line 2397, panel resolution)

The panel further pinned: clock-skew gate (`AGENT_CLOCK_SKEW`, line 97), reachability prober for HTTP-01 fallback (`INGRESS_PORT80_UNREACHABLE`, line 155), restore-at-scale throttling at ≤ 20 certs/hour with priority queue (line 203), and that ACME challenges always run to completion or revoke (line 109).

## Decision

A typed `DnsProvider` interface ships in **Phase 0 Alpha** with **at least three implementations**: `cloudflare`, `route53`, and `manual`. The platform `apps-domain` issues a single wildcard certificate (`*.<apps-domain>`) by default via DNS-01. Per-service generated hostnames reuse this wildcard. HTTP-01 per-host issuance remains supported but is not the default for the apps-domain.

The interface, locked by panel (line 153), has exactly four methods:

- `presentTxt(zone, name, value)` — idempotent; returns provider-record-id for cleanup correlation.
- `cleanupTxt(zone, recordID)` — idempotent.
- `getZone(fqdn)` — resolves the authoritative zone; never auto-creates.
- `propagationCheck(fqdn, expected)` — polls **public resolvers** (not the provider's API) until quorum sees the value.

Provider credentials live in the platform secret store (envelope-encrypted, audited on access). They are never written to disk in plaintext (panel: line 153).

Operational guards:

- **Cold-start throttle:** ≤ 20 certs/hour with priority queue (TLS-expired first, expiring-soon next, healthy last). LE staging used for the first batch on restore. Progress surfaces as `RESTORE_ACME_THROTTLED`, never as failure (panel: line 203).
- **Hard cap:** 40 issuances per registered domain per week, leaving 10/week headroom under LE's 50/week limit (panel: line 154).
- **CAA preflight:** resolve CAA on leaf, registered domain, and parents before submitting (panel: line 156); refusal is `CAA_BLOCKS_ISSUANCE` (open registry add).
- **Reachability prober:** for hosts intended to use HTTP-01, run an Midnight-hosted prober on :80 / :443 before declaring ACME ready; unreachability gates ACME with `INGRESS_PORT80_UNREACHABLE` and DNS-01 fallback guidance (panel: line 155).
- **Clock skew:** agent refuses ACME ops if clock offset > 5s; control plane refuses agent registration if > 30s with `AGENT_CLOCK_SKEW` (panel: line 97).
- **Backup:** `acme.json` is part of the platform backup set; restore is byte-exact with `0600` before Traefik starts (panel: line 154).
- **Reserved path:** `/.well-known/acme-challenge/*` is held at maximum router priority on every entrypoint serving :80; user route definitions claiming this path are rejected (panel: line 159, ADR-006).
- **Custom wildcards:** `*.example.com` requires DNS-01 + ownership TXT at `_midnight-verify.<apex>`; more-specific subdomain wins over wildcard at routing (panel: line 158).

## Alternatives Considered

- **HTTP-01 only at Alpha; DNS-01 deferred.** Rejected by panel reclassification (line 2397). HTTP-01 per generated subdomain hits LE's 50/week per-registered-domain ceiling within tens of services. Alpha design partners would experience throttling within the first week.
- **DNS-01 with only one provider (e.g., Cloudflare).** Rejected: panel locked **at least three** providers (Cloudflare, Route53, `manual`) for Alpha (line 153). The `manual` provider is required so operators on niche DNS hosts can self-publish TXT records without us shipping a provider for every host on the planet.
- **Wildcards via a third-party hosted ACME proxy.** Rejected: introduces a hosted dependency for an OSS install, breaks the offline-friendly trust story, and creates a new unaudited path for credential exposure.
- **Custom CA / private PKI for the apps-domain.** Rejected at Phase 0: requires every browser/client trust the operator's CA — incompatible with a generated-public-domain UX. Documented as a Phase 4 enterprise-track option.
- **Buy commercial wildcard certs out-of-band.** Rejected: defeats the "deploy an app, get HTTPS" promise. Operators should not need a billing relationship with a CA to ship Phase 0.
- **Use Caddy's auto-DNS-01.** Rejected as ingress (ADR-006); not selected for the proxy. Adopting Caddy's DNS-01 logic without Caddy means re-implementing it.

## Consequences

Positive:

- The 50/week per-registered-domain ceiling no longer scales with service count; one wildcard cert covers the entire apps-domain.
- CAA preflight prevents wasted ACME submissions that would burn LE's failed-validation budget (panel: line 156).
- Cold-start throttle (≤ 20/hr) makes restore-at-scale a typed progress state (`RESTORE_ACME_THROTTLED`), not a hidden outage.
- The `manual` provider gives operators a path even on hosts where no API integration exists — required for a true OSS install story.
- Clock-skew gating catches the most common silent ACME failure mode before it produces a failed challenge.

Negative:

- DNS-01 requires DNS-provider credentials in the secret store. Panel handles this (line 153: "credentials live in the platform secret store") and the SSRF-safe outbound HTTP client gates outbound calls (line 124).
- Provider-specific drift (Cloudflare's API rate limits, Route53's eventual consistency) requires per-provider integration tests against sandbox/staging zones.
- A wildcard cert blast radius is wider than per-host certs: a stolen wildcard cert + private key compromises every generated subdomain. We mitigate via the platform KMS-sealed secret store and `acme.json` `0600` ownership.
- HA ingress is still out of scope (panel: line 1096); a single active Traefik holds the wildcard.

Follow-up work:

- `DnsProvider` interface and three implementations land in `SPRINT_06_INGRESS_LOGS.md` and `SPRINT_10_CUSTOM_DOMAINS_GH_APP.md`.
- CAA preflight, hard-cap enforcement, and cold-start throttle land in `FEATURE_ACME_DNS01_WILDCARD.md`.
- New error codes (`DNS_CREDENTIALS_INVALID`, `DNS_PROVIDER_RATE_LIMITED`, `DNS_PROPAGATION_TIMEOUT`, `CAA_BLOCKS_ISSUANCE`) are added to the locked failure-code registry (panel resolution open question, see `FEATURE_ACME_DNS01_WILDCARD.md`).

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestDnsProviderInterfaceContract`** in `pkg/ingress/dns`: each of `cloudflare`, `route53`, `manual` implements the four locked methods; `PresentTxt` and `CleanupTxt` are idempotent under double-call.
- **Test name `TestPropagationChecksPublicResolvers`** in `pkg/ingress/dns`: a fixture asserts `propagationCheck` queries public resolvers (`1.1.1.1`, `8.8.8.8`, `9.9.9.9`), not the provider's own API.
- **Test name `TestCAAPreflightBlocksLE`** in `pkg/ingress/acme`: a fixture with CAA disallowing Let's Encrypt fails fast with `CAA_BLOCKS_ISSUANCE`; no ACME submission happens.
- **Test name `TestColdStartThrottle`** in `pkg/ingress/acme`: 1000 simulated domains, restore path produces ≤ 20 issuances per simulated hour with priority order (expired → expiring-soon → healthy); status surfaces as `RESTORE_ACME_THROTTLED`.
- **Test name `TestACMEHardCap40Per7d`** in `pkg/ingress/acme`: sliding-window arithmetic correct; 41st issuance attempt within 7 days falls back to LE staging and emits an alert.
- **Test name `TestACMEPathReservedAtPriority`** in `pkg/ingress/traefik`: user-route claiming `/.well-known/acme-challenge/*` is rejected at validation (links to ADR-006).
- **CI gate `gate.acme-state-byte-exact-restore`:** integration test backs up `acme.json`, restores it, verifies SHA-256 byte-equality and `0600 traefik:traefik` ownership; Traefik starts without re-issuance.
- **Runbook check `OR-CHK-ACME-CLOCK-SKEW`** in `midnight doctor`: reports clock offset; > 5s blocks ACME with `AGENT_CLOCK_SKEW`.
- **Failure drill `DRILL-ACME-RESTORE-AT-SCALE`** in `SPRINT_08_FAILURE_DRILLS.md`: 1000 domains, restore throttles to ≤ 20/hr; LE staging used first; no `TLS_ISSUANCE_FAILED` produced (panel: line 203).
- **Failure drill `DRILL-DNS-CREDS-REVOKED`**: provider credentials revoked mid-flow; graceful failure, alert, no retry storm.

## References

- spec: lines 97, 109, 124, 153–166, 203, 1086–1101, 2397
- features: `FEATURE_ACME_DNS01_WILDCARD.md`, `FEATURE_OFFHOST_BACKUP.md`, `FEATURE_DAEMON_JSON_BASELINE.md`
- sprints: `SPRINT_06_INGRESS_LOGS.md`, `SPRINT_08_FAILURE_DRILLS.md`, `SPRINT_10_CUSTOM_DOMAINS_GH_APP.md`
- handoff: `OPERATIONS_RUNBOOK_INDEX.md`, `SECURITY_REVIEW_PLAN.md`
