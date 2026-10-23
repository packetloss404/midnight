# Cloudflare wildcard DNS and TLS verification — 2026-09-25

This is a point-in-time observation of the existing public DNS and Caddy edge.
It follows the September 24 installed-host report and does not replace a fresh
Midnight installation or application deployment acceptance run.

This public copy substitutes `example.com` and the documentation IP address
`203.0.113.10` for the operator's real domain and ingress address. The original
report is retained in private operator records.

## Authoritative DNS

The operator domain was delegated to Cloudflare's authoritative name servers.

Cloudflare's authoritative zone contains an unproxied wildcard A record for
`*.apps.example.com` targeting public ingress `203.0.113.10`. A previously
unused name, `automatic-dns-check.apps.example.com`, resolved to that address
through public resolver `1.1.1.1`. Generated services therefore require no
per-service public DNS mutation.

## Edge certificate and routing

The operator-managed Caddy edge runs Caddy 2.11.3 with the Cloudflare DNS
provider module. Its persistent portal row renders an HTTPS site for
`*.apps.example.com`, obtains and renews the wildcard certificate through
Cloudflare DNS-01, and proxies private HTTP to Midnight Traefik while preserving
the original Host and forwarded headers.

The credential is restricted to the operator zone with `DNS:Edit` and `Zone:Read`.
Its value is stored in a root-owned environment file and is not retained in
repository content, portal rows, rendered Caddy configuration, or evidence.
Both the systemd Caddy service and the portal reload path receive the protected
environment so later portal applies and service restarts keep working.

Let's Encrypt issued a certificate with subject and SAN
`*.apps.example.com`. At observation time it was valid from September 25 through
December 24, 2026. The temporary ACME challenge TXT record was absent after
issuance completed.

## External and restart verification

Before restart:

- HTTP for an unused wildcard hostname returned `308` to the same HTTPS URL.
- HTTPS completed with the trusted wildcard certificate and returned `404`
  from the proxied Midnight ingress because no route owned the probe hostname.
- `https://midnight.example.com/` and `https://docvault.example.com/` continued to
  return successful responses.

Caddy and the Caddy portal were then restarted. Both services returned active,
both binaries exposed `dns.providers.cloudflare`, the unused wildcard hostname
continued to complete trusted HTTPS and reach Midnight ingress, and the console
and DocVault continued to return successful responses.

## Generated-service behavior

For a public generated route, Midnight creates a stable hostname with exactly
one service label beneath `apps.example.com`, binds it to the service and
environment, and publishes the exact Host rule to Traefik when route and backend
evidence converge. Public DNS and edge TLS are already covered by their
wildcards. Deleting or stopping the Midnight route does not change wildcard
DNS; the unused hostname instead returns routing 404.

## Evidence boundaries

This observation proves authoritative wildcard DNS, edge certificate issuance
and renewal configuration, HTTP-to-HTTPS behavior, Host forwarding to Midnight
ingress, and edge restart persistence. It does not prove a new source/image
deployment, a ready application backend on a generated hostname, automatic
registration of arbitrary customer custom domains at the edge, or
Midnight-worker DNS-provider reconciliation. Those remain separate product and
acceptance evidence.

See [Installed VPS and public-domain verification](installed-vps-verification-2026-09-24.md),
[Deployment readiness](../deployment-readiness.md), and
[Custom-domain cutover](../runbooks/custom-domain-cutover.md).
