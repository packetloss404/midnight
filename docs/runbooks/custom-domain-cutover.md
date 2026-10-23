# Custom-domain cutover

Status: single-VM MVP runbook.

Use this runbook to move an existing service/environment from a generated
hostname to a custom hostname. It also applies after changing the installation
apps domain: existing generated domains retain their original hostname and must
be replaced deliberately.

## Preconditions

- Record the repository and installed `/api/v1alpha/compat` commit, package,
  schema maximum/count, service, environment, active deployment, and current
  public domain IDs.
- Confirm the active deployment is healthy before changing domain state.
- Identify the authoritative DNS provider. A configured Cloudflare account does
  not make Cloudflare authoritative when the zone delegates elsewhere.
- Decide where TLS terminates. If an external Caddy, load balancer, or reverse
  proxy owns the certificate, the Midnight route remains HTTP-only with TLS
  intent disabled.
- Keep ownership-verification values and credentials out of retained evidence.

## Stage DNS and ownership

1. Create the routing record shown by Midnight for the exact custom hostname.
   Use the public ingress target, not a private VM address.
2. Create the exact `_midnight-challenge.<hostname>` TXT record shown by
   Midnight. Do not document or commit its value.
3. Query public recursive DNS and the zone's authoritative name servers until
   both records return the intended values.
4. Ask Midnight to verify ownership. A DNS record alone does not make the
   domain eligible; retain the verified state and timestamp.

## Bind and attach the route

1. Inspect the custom domain, HTTP route, service-domain binding, and current
   generated binding separately. A failed recovery/API operation may have
   created the domain, route, and challenge before failing to create a binding.
2. Bind the verified custom hostname to the intended service and environment.
   If the existing generated singleton occupies the service-managed binding,
   record it, remove it through the supported domain lifecycle, and retry the
   custom binding.
3. Confirm the custom route is public and uses the intended container port.
4. Confirm the route has a ready backend for the active deployment. Binding a
   domain does not create a backend. If the route remains pending or returns a
   routing 404, redeploy the existing immutable image or run the supported
   startup/route repair so the active deployment acquires an exact route intent
   and backend.
5. Wait for Midnight's controller revision, Traefik acknowledgement, and Host
   probe to become current. These are separate from public DNS and HTTPS.

## Configure an external TLS edge

Skip this section when Midnight and Traefik own public TLS directly.

1. Add the hostname to the persistent source used by the edge controller. If a
   portal generates Caddy configuration, update the portal database or API;
   editing the generated Caddyfile will be overwritten.
2. Proxy to Midnight's private HTTP ingress, preserving the original Host and
   standard `X-Forwarded-*` headers.
3. Wait for the edge to obtain a valid certificate and expose the hostname on
   port 443. Do not enable Midnight TLS intent for this route: the edge owns the
   certificate and HTTPS evidence.

For generated domains beneath an operator-owned apps domain, prefer one
wildcard DNS record and one wildcard edge certificate. A generated hostname has
exactly one label beneath the apps domain, so `*.apps.example.com` covers every
generated service without adding a DNS or edge entry per deployment. The edge
must preserve the Host header and proxy the wildcard to Midnight ingress;
Midnight's exact route still decides whether the request reaches a workload.
Wildcard certificate issuance requires DNS-01 and a narrowly scoped provider
credential. Store that credential outside rendered configuration and ensure the
persistent edge controller receives it during validation, reload, and restart.

## Verify and finish

Record each result independently:

- public DNS resolves through the authoritative provider;
- Midnight reports the custom domain verified and bound to the intended scope;
- the route is active and ready on the intended target port;
- the active deployment has at least one healthy instance and ready backend;
- a trusted Host-header request reaches the workload through Midnight ingress;
- an external HTTPS request presents a valid certificate and reaches the app;
- the console's live link uses the custom hostname;
- no obsolete generated domain remains when the custom hostname should be the
  only public address.

An application redirect such as `/` to `/login` is valid reachability evidence
when it is the application's expected behavior. Record the initial HTTP status,
redirect target, and final status without retaining session cookies.

## Recovery

- If ownership verification is pending, leave the route ineligible and repair
  authoritative DNS before changing bindings.
- If binding creation fails after domain creation, inspect and reuse the
  existing domain/route/challenge records instead of creating duplicates.
- If the route has no backend, restore it through a deployment or supported
  route repair; do not hand-edit rendered Traefik artifacts.
- If external HTTPS fails while Midnight's Host probe succeeds, troubleshoot
  the edge certificate, public DNS, and port 443 separately from Midnight.
- If the cutover cannot complete, restore the previous supported binding and
  edge route from the recorded IDs, then verify it end to end.

See [Durable HTTP routing](../networking/durable-http-routing.md),
[Domain TLS failure](domain-tls-failure.md), and
[Live deploy proof](../runtime/live-deploy-proof.md) for the underlying evidence
model and acceptance procedure.
