# ADR-006: Traefik with file provider; no Docker provider, no socket mount

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, networking, ingress, security, traefik]

## Context

The architecture review and the panel chose Traefik for v1 ingress because the Railway-like product model needs dynamic HTTP routing, ACME/TLS, host/SNI routing, WebSockets, target-port mapping, and eventual TCP proxying — all out of the box (line 33, line 402). The panel further pinned **how** Traefik is wired:

> "Midnight must use Midnight-generated dynamic file provider config by default, not the Docker provider or tenant labels, and must not mount the Docker socket." (line 65, reaffirmed line 988, line 2391)

> "Traefik must not mount `/var/run/docker.sock` in the default profile. The Traefik Docker provider is not the default because Docker API access expands the proxy compromise blast radius and tenant labels are not source of truth." (lines 987–988)

> "Dynamic Traefik config files are written via temp-file + atomic rename on the same filesystem; never truncated in place. Midnight reserves `/.well-known/acme-challenge/*` at maximum router priority on every entrypoint serving :80; user routes claiming this path are rejected at validation." (line 159)

> "Ingress defaults are pinned (table to ship in the doc, not 'configurable'): `maxRequestBodyBytes` (default 32 MiB), `idleTimeout` (180s), `readTimeout`/`writeTimeout` (60s), `webSocketIdleTimeout` (15m), trust-proxy hop count (1), forwarded header set (`X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Host`, `X-Real-IP`, `X-Request-Id`), h2c upstream opt-in for gRPC." (line 160)

The trust argument is concrete: a Traefik with the Docker provider needs read access to `/var/run/docker.sock`, which is host-root-equivalent (line 894). A compromised Traefik then becomes a compromised Docker daemon — i.e., the entire host. Tenant labels on tenant containers are also untrusted as a source of truth: a tenant with `Developer` rights could place arbitrary labels and steer the proxy. The route table in Postgres is the only authoritative source.

## Decision

Midnight uses **Traefik (OSS)** as v1 ingress, configured exclusively through the **file provider** with dynamic config files **written by Midnight** to a control-plane-owned directory.

Concretely:

- The Traefik Docker provider is **disabled**. Traefik **does not mount** `/var/run/docker.sock`.
- The Midnight control plane writes the dynamic config files (`/etc/traefik/dynamic/*.yaml`) using temp-file + atomic rename on the same filesystem; truncate-in-place is forbidden (panel: line 159).
- Midnight reserves `/.well-known/acme-challenge/*` at maximum router priority on every entrypoint serving :80 (panel: line 159).
- The pinned ingress defaults (panel: line 160) ship in the static config file shipped with the `.deb`. They are not "configurable" in the operator sense; tuning them requires an operator config override that is logged.
- Traefik OSS ACME is supported only for a single active ingress instance; HA ingress requires an external/elected certificate controller before support is claimed (panel: line 1096).
- Traefik's Prometheus endpoint is enabled by default on a control-plane-only listener with a fixed metric set (panel: line 165).

Caddy and Nginx-with-controller alternatives were considered and rejected.

## Alternatives Considered

- **Caddy.** Rejected: Caddy's auto-ACME story is excellent, but its dynamic-routing model is config-reload-driven and weaker for tenant routes that change at deploy cadence. Caddy's plugin ecosystem for DNS-01 providers is also smaller than Traefik's. The panel did not consider Caddy a v1 fit for "dynamic HTTP routing, ACME, host routing, WebSockets, target ports, and eventual TCP proxying" (line 33).
- **Nginx + a custom controller.** Rejected: requires us to write the controller, the ACME glue, the DNS-01 provider stack (panel locks DNS-01 + wildcard at Alpha-blocking, line 153, ADR-007), and the Prometheus integration. Builds Midnight into a "we wrote our own ingress" project rather than a deployment platform. The first product loop does not include reverse-proxy engineering.
- **HAProxy.** Rejected: weaker dynamic config for tenant-rate routing changes; ACME integration is via external tooling (`acme.sh` or `certbot`) and is not first-class.
- **Envoy + control plane (xDS).** Rejected at v1: xDS implementation is a multi-quarter project. Envoy is the right choice for a hypothetical Phase 4 multi-region hosted variant, not Phase 0.
- **Traefik with the Docker provider.** Rejected by panel (line 65, line 988). Mounting the Docker socket into Traefik makes Traefik host-root-equivalent and treats tenant-applied labels as authoritative routing — both unacceptable for the trust model (line 64, line 894).
- **Traefik with the Kubernetes provider.** Rejected: requires Kubernetes, which is a Phase 3 adapter (line 23).

## Consequences

Positive:

- The route table in Postgres is the only source of truth; Traefik observes a generated artifact, never tenant labels.
- Traefik has no path to the Docker daemon. Compromise of Traefik does not compromise the host runtime.
- Pinned ingress defaults make supportability tractable: a 504 has a known timeout source (`writeTimeout`, `idleTimeout`, `webSocketIdleTimeout`) rather than a per-operator-tuned mystery.
- ACME state (`acme.json`) is part of the platform backup set with byte-exact restore (panel: line 154, ADR-007).
- The Prometheus endpoint emits a fixed metric set (panel: line 165) that Grafana dashboards can rely on across versions.

Negative:

- Midnight owns the file-provider config generator. We must support atomic rename, validate generated YAML against a schema, and reject user routes claiming `/.well-known/acme-challenge/*` (panel: line 159).
- HA ingress is out of scope for v1; multi-Traefik with shared ACME requires an elected cert controller that the panel explicitly declines to ship in OSS Phase 0/1 (line 1096).
- Future TCP proxy (line 1057) sits behind Traefik's TCP entrypoint, not a separate proxy.

Follow-up work:

- Dynamic config generator with atomic-rename writer (`SPRINT_06_INGRESS_LOGS.md`).
- Validator that rejects user-route definitions claiming the ACME path or non-allowed forwarded headers.
- Pinned defaults baked into `/etc/traefik/traefik.yaml` shipped with the `.deb`.
- ACME state backup/restore tested at scale (`FEATURE_ACME_DNS01_WILDCARD.md` test plan: 1k domains, throttle ≤ 20/hr).

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestTraefikDoesNotMountDockerSock`** in `pkg/ingress/traefik`: the unit-under-test asserts the rendered systemd unit and Traefik static config never reference `/var/run/docker.sock`. A drift fixture (`docker-provider-config.yaml`) is rejected by validation.
- **Test name `TestFileProviderAtomicRename`** in `pkg/ingress/traefik`: the writer uses `os.Rename` on the same filesystem; truncate-in-place attempts fail the test.
- **Test name `TestACMEChallengePathReserved`** in `pkg/ingress/traefik`: a user-route fixture claiming `/.well-known/acme-challenge/foo` is rejected at validation; the Midnight-internal route holds the path at maximum priority.
- **Test name `TestPinnedIngressDefaults`** in `pkg/ingress/traefik`: the rendered config matches the panel-locked defaults: `maxRequestBodyBytes=32MiB`, `idleTimeout=180s`, `readTimeout=60s`, `writeTimeout=60s`, `webSocketIdleTimeout=15m`, trust-proxy hop count = 1, forwarded headers exactly the panel-locked set.
- **CI gate `gate.no-docker-provider`:** static analysis of shipped config files asserts the Docker provider stanza is absent in default profiles.
- **Runbook check `OR-CHK-INGRESS-DEFAULTS`** in `midnight doctor`: parses live Traefik config and reports drift from the panel-locked defaults.
- **Code review checklist item `CR-INGRESS-01`:** any change to ingress defaults requires an explicit operator-config override path; no silent default change.
- **Failure drill `DRILL-TRAEFIK-RELOAD`** in `SPRINT_08_FAILURE_DRILLS.md`: kill-and-restart Traefik mid-deploy; verify routes reconcile from the file provider without missing `/.well-known/acme-challenge/*`.

## References

- spec: lines 33, 65, 159, 160, 165, 402, 982–998, 1086–1101, 2391
- features: `FEATURE_ACME_DNS01_WILDCARD.md`, `FEATURE_DEPLOYMENT_STATE_MACHINE.md`
- sprints: `SPRINT_06_INGRESS_LOGS.md`, `SPRINT_08_FAILURE_DRILLS.md`, `SPRINT_10_CUSTOM_DOMAINS_GH_APP.md`
- handoff: `SECURITY_REVIEW_PLAN.md`, `OPERATIONS_RUNBOOK_INDEX.md`
