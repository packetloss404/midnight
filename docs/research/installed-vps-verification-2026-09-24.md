# Installed VPS and public-domain verification — 2026-09-24

This is a point-in-time observation of the existing Midnight installation and
its public domain cutover. It records redacted product and edge evidence; it is
not a fresh-host acceptance run.

This public copy substitutes `example.com` hostnames for the operator's real
domain and omits host-specific identifiers. The original report is retained in
private operator records.

## Installed identity

| Evidence | Observed value |
| --- | --- |
| Repository and `/api/v1alpha/compat` commit | `8ad2845dd9e175a795c68d69c9ff49c996ebc36f` |
| Package | `midnight 0.1.0~alpha-6` |
| Server / API | `0.1.0-alpha` / `v1alpha` |
| Build date | `2026-09-24T13:12:38-05:00` |
| Schema maximum / count | `72|72` |
| Units | server, worker, agent, BuildKit daemon, BuildKit worker, and registry active |

The exact build identity came from the installed compatibility endpoint, not
from the local checkout alone.

## Instance namespace

The installation's durable instance settings, shown with example hostnames, were:

| Role | Hostname |
| --- | --- |
| Root domain | `example.com` |
| Console | `midnight.example.com` |
| Generated applications | `apps.example.com` |

At this observation, registrar name servers were authoritative for the zone;
Cloudflare was not yet authoritative. Public DNS included the console host,
the generated-app wildcard, the DocVault hostname, and the redacted DocVault
ownership challenge.

`https://midnight.example.com/` served the branded “This domain runs on
Midnight” page. The authenticated console remained available at
`https://midnight.example.com/login` and on deep links.

## DocVault custom hostname

- `docvault.example.com` was ownership-verified and bound to the existing
  DocVault service/environment on target port 8000.
- Midnight reported the route active and ready, with one healthy deployment
  instance.
- The earlier generated service hostnames were removed. The custom hostname was
  the only public binding and the console's live link.
- An external request to `https://docvault.example.com/` received a valid HTTPS
  response and the application's expected redirect to `/login`, whose final
  response was successful.

## TLS and ingress topology

An operator-managed Caddy edge obtained the Let's Encrypt certificate and
terminated public TLS. It forwarded the original Host over private HTTP to
Midnight's Traefik ingress. Midnight's custom route therefore correctly had
`tls_desired=false`: its evidence covers the HTTP route and ready backend,
while the edge owns certificate and external HTTPS evidence.

The edge configuration is generated from persistent portal state. The DocVault
site was added through that owner rather than by editing the generated Caddyfile.

## Cutover observations

- Saving a new instance apps domain did not rename an existing generated
  service domain. Generated hostname identity and apps-domain lineage are
  durable, so the old binding had to be replaced.
- The recovery-compatible custom-domain operation could persist the domain,
  route, and verification challenge before a later binding conflict. Retrying
  safely required inspecting the partial state rather than creating duplicates.
- The existing generated binding occupied the service-managed public binding.
  After the custom hostname was verified, removing the generated binding let
  the custom binding complete.
- A newly bound custom route had no ready backend until the existing image was
  redeployed and the route intent/controller state converged. DNS, ownership,
  binding, route readiness, and external HTTPS were verified separately.

These observations are captured as an operator procedure in
[Custom-domain cutover](../runbooks/custom-domain-cutover.md).

## Evidence boundaries

This observation does not establish:

- a fresh installation of commit `8ad2845`;
- restart-safe application state or BuildKit socket survival;
- rollback, administrator recovery, or audit acceptance;
- private-GitHub checkout and status delivery;
- product-level off-host destruction, restore, and application readback;
- automatic DNS-provider integration for this zone;
- Midnight-managed certificate issuance or wildcard generated-app HTTPS.

Those items remain tracked in [deployment readiness](../deployment-readiness.md)
and the [backlog](../../BACKLOG.md).
