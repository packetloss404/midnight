# Phase 0 Subagent Handoff - S06 Ingress, Logs, and DX

Worker: S06 - Ingress/Logs/DX
Primary source: `agile/sprints/SPRINT_06_INGRESS_LOGS.md`
Reviewed references:
- `agile/features/FEATURE_ACME_DNS01_WILDCARD.md`
- `agile/engineering/adr/ADR-006-traefik-file-provider-ingress.md`
- `agile/engineering/adr/ADR-007-acme-dns01-wildcard-alpha-blocking.md`
- `agile/engineering/contracts/API_ERROR_CODE_REGISTRY.md`
- S03/S04/S05/S07/S08 sprint dependency surfaces

## Scope

S06 owns the Phase 0 public-facing deploy comprehension path: generated HTTPS routing through Traefik, platform `apps-domain` wildcard TLS, app log capture, deployment timeline, typed failure classification, and the dashboard/CLI surfaces that explain what happened.

In scope:
- Traefik static config and file-provider dynamic route generation.
- Atomic dynamic config writer under `/etc/traefik/dynamic/midnight/`.
- Generated-domain routing for `<service>.<env>.<project>.<apps-domain>`.
- ACME DNS-01 wildcard issuance for `*.<apps-domain>` using Cloudflare, Route53, and manual DNS providers.
- ACME state backup/restore contract and restore reissue throttling signals.
- HTTP-01 primitives needed by generated-domain or explicitly approved Phase 0 custom-domain work: reserved challenge router, reachability prober gate, CAA preflight, budget guard.
- App log collection from Docker `json-file`, server query API, CLI `midnight logs`, and dashboard log viewer.
- Immutable deployment timeline endpoint and compact CLI/dashboard rendering.
- Failure-code registry expansion/guard for ingress, ACME, logs, build/deploy, and host runtime reason codes.
- Default Phase 0 dashboard and DX surfacing for route/TLS/log/timeline state.

Out of scope unless the Phase 0 scope owner explicitly reaffirms it:
- Full custom-domain product workflow beyond primitives: ownership UX, customer domain lifecycle, wildcard custom domains, BYO certificate, `redirectWww`, and long-lived custom-domain diagnostics. Sprint 10 is the stronger owner for the broader custom-domain/GitHub App lane.
- HA/multi-active Traefik ACME. ADR-006 and ADR-007 allow Traefik OSS ACME only for single-active ingress.
- Kubernetes, Docker provider, tenant-applied Traefik labels, and any Traefik Docker socket mount.
- Remote log drains and external telemetry export beyond S07's opt-in aggregate telemetry.

## Custom-Domain Boundary Flag

There is a real Phase 0 boundary conflict:

- `SPRINT_06_INGRESS_LOGS.md` includes S6-03 "HTTP-01 challenge for custom domains" and demos adding a custom domain.
- `midnight-platform-plan.md` also says Phase 0's narrow slice should prove generated domains first and notes "After Phase 0: add custom domain full flow if generated domains are reliable"; Sprint 10 is dedicated to custom domains and GitHub App work.

Recommended resolution for Phase 0:
- S06 must ship the reusable ingress/TLS primitives: ACME path reservation, route validation, HTTP-01 reachability gate, CAA preflight, failure codes, and domain diagnostics fields.
- S06 should only expose generated-domain TLS by default.
- Custom-domain HTTP-01 should be either a hidden/admin-only smoke path or explicitly reclassified by product as Phase 0 Alpha-blocking before UI/CLI investment.
- Wildcard custom domains (`*.example.com`) are not S06 Phase 0 UI scope; they require DNS-01 plus `_midnight-verify.<apex>` and belong with Sprint 10 unless re-scoped.

## Owned Future Implementation Paths

Proposed path ownership for implementation:

| Area | Future paths |
|---|---|
| Traefik config | `pkg/ingress/traefik`, `internal/ingress/routes`, `configs/traefik/traefik.yaml`, `packaging/deb/etc/traefik/` |
| Atomic writer | `pkg/ingress/fileprovider`, `pkg/fs/atomicfile` if shared |
| Route validation | `internal/api/v1alpha/routes`, `internal/domain/routes`, OpenAPI route schemas |
| ACME lifecycle | `pkg/ingress/acme`, `internal/workers/acme`, `internal/api/v1alpha/domains` |
| DNS providers | `pkg/ingress/dnsprovider`, `pkg/ingress/dnsprovider/cloudflare`, `pkg/ingress/dnsprovider/route53`, `pkg/ingress/dnsprovider/manual` |
| External prober | `internal/prober`, `internal/api/v1alpha/preflight`, hosted prober contract docs |
| Logs | `internal/agent/logs`, `internal/api/v1alpha/logs`, `pkg/logs/ndjson`, CLI `midnight logs` |
| Timeline | `internal/timeline`, `internal/api/v1alpha/deployments_timeline`, dashboard timeline component |
| Failure codes | `pkg/errors/codes`, `docs/error-codes.md`, CI guard for unregistered codes |
| Dashboard/DX | dashboard route/TLS/log/timeline panels, `midnight status`, `midnight doctor --json` checks |
| Packaging | `/etc/logrotate.d/midnight`, `/etc/prometheus/rules/midnight-alerts.yml`, `/usr/share/midnight/dashboards/midnight-overview.json` |
| Runbooks/docs | `docs/networking/dns-01.md`, `docs/networking/prober.md`, `docs/control-plane/deployment-timeline.md`, `runbooks/domain-tls-failure.md`, `runbooks/regenerate-traefik-config.md` |

## Dependencies From S03

S06 consumes these S03 contracts:
- `/api/v1alpha` error envelope: `{code, message, request_id, docs_url, details}`.
- Additive-only error-code registry and OpenAPI publication workflow.
- `Idempotency-Key` middleware for route/domain mutation endpoints.
- `X-Request-Id` and W3C trace propagation into logs, operations, audit, and webhooks.
- Postgres base schema tables: `domains`, `routes`, `deployments`, `deployment_instances`, `deployment_snapshots`, `events`, `audit_logs`, `jobs`, `operations`, `failure_classifications`.
- Job queue semantics: `SELECT ... FOR UPDATE SKIP LOCKED`, leases, heartbeats, per-class queues.
- Secret/variable store for DNS provider credentials and ACME-related sealed material.
- Redaction pipeline for timeline details, app logs, build logs, support bundles, and dashboard output.
- SSRF-safe outbound HTTP/DNS client for DNS provider APIs, external prober calls, CAA checks, and ACME-adjacent outbound traffic.

Blocking gaps to verify with S03:
- Exact sealed-secret API for provider credentials.
- Exact event/audit insertion helpers and redaction contract.
- Whether `docs/error-codes.md` is generated from `API_ERROR_CODE_REGISTRY.md` or manually synchronized.

## Dependencies From S04

S06 consumes these S04 host-agent/runtime contracts:
- Docker Engine API client only; no Docker CLI shellouts for log capture or runtime inspection.
- Stable container labels: `midnight.deployment_seq`, `midnight.image_digest`, `midnight.host_id`, `midnight.managed_by=midnight-agent`.
- Container IDs and instance reports needed to tail Docker `json-file` logs.
- `deployment_instances` state and `last_observed_at` for route membership.
- LOST instance route freshness: remove stale instances after `route_freshness_ttl` default 30s; re-add after 3 healthy heartbeats.
- Host heartbeat clock offset for ACME gating: agent refuses ACME ops if offset >5s; registration refused with `AGENT_CLOCK_SKEW` if >30s.
- `AGENT_RUNTIME_UNAVAILABLE`, Docker socket EOF recovery, and host health signals that S06 must surface in timeline/failure DX.
- Doctor framework and machine-readable check IDs for ingress, ACME, log source, and dashboard install checks.

Blocking gaps to verify with S04:
- Whether S04 or S06 owns the three-probe runner implementation. S06 owns timeline semantics and route readiness integration; S04 likely owns agent-side probe execution.
- Agent streaming protocol for log lines and resume cursor (`Last-Event-ID`).
- Route membership data shape consumed by the Traefik renderer.

## Dependencies From S05

S06 consumes these S05 build/supply-chain contracts:
- Deployment snapshot fields: source SHA, builder kind/version/digest, resolved build plan, base image digests, produced image digest, provenance URI.
- Build and deploy failure events for `BUILD_FAILED`, `BUILD_TIMEOUT`, `BUILD_OUTPUT_SECRET_LEAK`, `BUILDKIT_VERSION_MISMATCH`, `BUILDKIT_CACHE_CORRUPTED`, `ROLLBACK_IMAGE_UNAVAILABLE`, `EXTERNAL_DIGEST_MISSING`, and related codes.
- Build log redaction and sanitized BuildKit excerpts for timeline detail.
- Rollback-by-digest creates a new deployment row and timeline linked to the source deployment.
- BuildKit cache corruption metric/event for S07 alerting and S08 drills.
- Registry digest retention/pin status for rollback timeline and "why failed" messaging.

Blocking gaps to verify with S05:
- Exact event names and payloads for build phases.
- Whether build logs are stored in the same log service as runtime logs or exposed as timeline-only artifacts.
- Provenance/signature verification event payloads for dashboard display.

## Exported Diagnostics To S07

S07 supportability depends on S06 exporting these redaction-safe artifacts:

| Diagnostic | Consumer | Notes |
|---|---|---|
| Traefik static config snapshot | Support bundle/config | Must prove no Docker provider and no socket mount. |
| Traefik dynamic route files | Support bundle/config | Include rendered YAML with tenant secrets absent; note file hash and generation sequence. |
| Route reconciliation events | Events/support bundle/dashboard | Include env_id, route_id, deployment_id, target instance IDs, outcome, error code. |
| Domain diagnostics | Support bundle/events/dashboard | Include ownership state, routing state, challenge type, CAA result, propagation status, last error, expiry; never include ACME private keys or DNS credentials. |
| ACME budget state | Metrics/support bundle | Registered-domain weekly count, staging fallback, restore queue position. |
| ACME restore progress | Restore SSE/support bundle | Emit `RESTORE_ACME_THROTTLED` as progress, not failure. |
| App logs | Support bundle/logs | NDJSON `{ts, stream, level, msg, attrs}` with redaction, selectors, caps, truncation markers. |
| Deployment timeline | Support bundle/events/dashboard | Immutable ordered events with `event_seq`, `phase`, `code`, `severity`, `summary`, redacted detail, correlation. |
| Failure-code histogram | Telemetry/metrics | Opt-in aggregate only; no raw logs or tenant names. |
| Metrics | Prometheus/dashboard | `traefik_config_reloads_total`, ACME cert/expiry metrics, route reconciliation counters, log tail lag/drop counters. |
| Doctor checks | Doctor/support bundle | Ingress config, ACME readiness, logrotate installed, dashboard installed, clock skew, port reachability. |

S07 packaging/runbooks that need S06 inputs:
- `domain-tls-failure.md`
- `regenerate-traefik-config.md`
- `failed-deploy-triage.md`
- `collect-support-bundle.md`
- `midnight-overview.json`
- `midnight-alerts.rules.yml`

## Exported Diagnostics To S08

S08 drills require deterministic S06 signals:

| Drill | S06 obligation |
|---|---|
| D-05 image pull failure | Timeline shows failure, previous active route unchanged, code `IMAGE_PULL_FAILED`. |
| D-06 readiness fails | Timeline shows startup/readiness attempts, route not promoted, code `HEALTHCHECK_FAILED`, `PORT_NOT_LISTENING`, or `APP_BOUND_LOCALHOST`. |
| D-09 DNS verification break | Domain diagnostics show TXT mismatch; events include `domain.verification.failed`; codes `DOMAIN_OWNERSHIP_LAPSED` and/or `DNS_VERIFICATION_FAILED`. |
| D-10 clock skew >5min | ACME ops blocked; doctor and timeline surface `AGENT_CLOCK_SKEW`. |
| D-11 journald wedged | Tenant logs continue from Docker `json-file`; platform degraded signal is separate from app log availability. |
| D-12 `/var/log` full | Logrotate/default log caps observable; logs-root disk pressure emits `DISK_PRESSURE`. |
| D-14 network partition | Route freshness/hysteresis events visible; timeline shows host offline/reconnected without false deploy failure for short partitions. |
| D-15 BuildKit cache corruption | Timeline includes build cache reset/retry; alert metric available to S07; no secret leakage in log excerpt. |
| D-16 Docker socket EOF | Timeline/doctor show `AGENT_RUNTIME_UNAVAILABLE`; recovery does not require agent crash-loop. |
| D-19 ACME-state restore at scale | Restore operation reports `RESTORE_ACME_THROTTLED`, queue priority, <=20 certs/hour, LE staging first batch. |
| Public-mode timing | Generated domain route and wildcard TLS path works through Pebble/CoreDNS fixture; ACME/DNS propagation time excluded from timer. |

## Acceptance Checklist

Traefik and routing:
- [ ] Static config uses only `providers.file.directory`; no `providers.docker`.
- [ ] Traefik unit does not mount `/var/run/docker.sock`.
- [ ] Dynamic route files are written by temp-file, fsync, same-filesystem atomic rename.
- [ ] One file per route group under `/etc/traefik/dynamic/midnight/`.
- [ ] `acme-challenge-pin` router exists in every generated file at priority `2147483647` on entrypoint `web`.
- [ ] User routes shadowing `/.well-known/acme-challenge/*` are rejected with `ROUTE_RESERVED_PATH`.
- [ ] Route renderer is byte-stable for unchanged inputs and debounces reload churn.
- [ ] `traefik_config_reloads_total` is observed and reload thrash surfaces `INGRESS_RELOAD_THRASH`.

ACME and DNS:
- [ ] `DnsProvider` interface ships with Cloudflare, Route53, and manual providers.
- [ ] DNS provider credentials are stored only in the secret store.
- [ ] Wildcard `*.<apps-domain>` issues via DNS-01 and is reused by generated domains.
- [ ] Propagation checks query public resolvers `1.1.1.1`, `8.8.8.8`, and `9.9.9.9`.
- [ ] CAA preflight blocks forbidden domains before ACME submission.
- [ ] External reachability prober gates HTTP-01 and emits `INGRESS_PORT80_UNREACHABLE` with DNS-01 fallback guidance.
- [ ] ACME issuance hard-cap is 40/registered-domain/week; overflow uses staging and emits `ACME_RATE_BUDGET_EXHAUSTED`.
- [ ] `acme.json` is included in backup and restored byte-exact with `0600` before Traefik starts.
- [ ] Restore reissue path throttles at <=20 certs/hour and reports `RESTORE_ACME_THROTTLED` as progress.

Logs:
- [ ] Agent sets Docker `LogConfig` to `json-file` with `max-size=100m`, `max-file=7`.
- [ ] `.deb` ships `/etc/logrotate.d/midnight` with 100M, 14 days, 7 rotations, copytruncate.
- [ ] Agent tails Docker logs per container, normalizes timestamps, redacts, and resumes by cursor.
- [ ] Server log query is paginated and returns NDJSON `{ts, stream, level, msg, attrs}`.
- [ ] `midnight logs <service> --since 1h --tail 200 --follow --json` matches the normative CLI shape.

Timeline and failure classification:
- [ ] `GET /api/v1alpha/deployments/{id}/timeline` returns immutable ordered events with monotonic `event_seq`.
- [ ] Timeline covers source, builder, image/digest, host, start, healthcheck, route, TLS, and failure phases.
- [ ] Every failure uses a registry code and redaction-safe structured detail.
- [ ] `midnight status <deployment>` renders the compact "why failed" answer in under 1s.
- [ ] Dashboard shows route, TLS, logs, timeline, and failure code without exposing secrets.
- [ ] CI blocks unregistered failure codes.

Observability and packaging:
- [ ] Default Prometheus rules include the eight locked Alpha alerts.
- [ ] `midnight-overview.json` includes deployments, build queue, heartbeat freshness, disk/inode, ACME issuance, cert expiry, ingress reloads, conntrack, and build duration.
- [ ] Traefik Prometheus endpoint is bound to a control-plane-only listener.
- [ ] Doctor checks expose ingress config, ACME readiness, logrotate, dashboard install, and feature kill-switch state.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Custom-domain scope leaks into Sprint 10 territory | S06 misses generated-domain/Phase 0 commitments | Keep S06 default UX generated-domain only; require explicit re-scope for custom-domain UI. |
| ACME provider integrations are flaky in CI | Blocks Alpha confidence | Use Pebble/fixtures in PR CI; run Cloudflare/Route53 live jobs nightly. |
| Wildcard cert private key leaks into support bundle | Critical security issue | Support bundle hard-blocks ACME private keys; fixture injects keys and fails on leak. |
| Traefik reload thrash during deploy churn | Route instability | Debounce writer, advisory lock per env_id, metric/alert for reloads >1/min for >5 min. |
| S04 healthcheck semantics conflict with route freshness | Incorrect route membership | Joint S04/S06 contract test: slow startup, readiness flap, LOST/re-add hysteresis. |
| Docker log rotation loses cursor continuity | Missing app logs | Cursor by container id + timestamp + line offset; test rotation without restart. |
| Error-code registry diverges between docs and constants | Broken API/DX consistency | Generate constants from registry or add CI static check before first emission. |
| `acme.json` restore ordering is wrong | Traefik reissues unnecessarily or fails TLS | Restore test verifies byte equality, mode/owner, and Traefik start before workers resume. |
| CAA code naming mismatch | Confusing API taxonomy | Prefer existing `ACME_CAA_FORBIDDEN`; do not introduce `CAA_BLOCKS_ISSUANCE` unless registry owner approves additive alias. |

## Exact First Implementation Tasks

1. Create the ingress contract package.
   - Add `pkg/ingress/traefik` with route input structs, generated dynamic YAML structs, and static config validation.
   - Add tests `TestTraefikDoesNotMountDockerSock`, `TestPinnedIngressDefaults`, and `TestACMEChallengePathReserved`.

2. Implement the atomic file-provider writer.
   - Add writer that creates `<target>.tmp.<rand>`, writes, fsyncs file and directory, renames on same filesystem, and never truncates live files.
   - Add crash/partial-write test and byte-stability fixture.

3. Add reserved ACME path validation to route mutation flow.
   - Reject path/regex rules that shadow `/.well-known/acme-challenge/*`.
   - Return `ROUTE_RESERVED_PATH` through the S03 error envelope.
   - Add OpenAPI examples and Schemathesis coverage.

4. Land the DNS provider interface and manual provider first.
   - Add `pkg/ingress/dnsprovider` with `GetZone`, `PresentTxt`, `CleanupTxt`, and `PropagationCheck`.
   - Implement manual provider with operator-facing TXT instructions and public resolver polling.
   - Add Cloudflare/Route53 provider skeletons behind interface tests.

5. Build ACME preflight gates before issuance.
   - Implement CAA preflight, :80/:443 prober client, clock-skew gate, and 40/week budget storage.
   - Emit `ACME_CAA_FORBIDDEN`, `INGRESS_PORT80_UNREACHABLE`, `ACME_RATE_BUDGET_EXHAUSTED`, and `DNS_PROPAGATION_TIMEOUT`.

6. Wire generated-domain wildcard issuance.
   - Create first-install/first-generated-domain reconciler for `*.<apps-domain>`.
   - Store challenge attempts and diagnostics on domain state.
   - Ensure generated routes reuse wildcard cert and do not trigger per-host HTTP-01 issuance.

7. Define timeline event schema and storage.
   - Add immutable `deployment_timeline_events` if current `events` table cannot provide monotonic per-deployment `event_seq`.
   - Implement `GET /api/v1alpha/deployments/{id}/timeline`.
   - Backfill event emitters for source, builder, image, host, start, healthcheck, route, TLS, failure.

8. Implement app log ingestion and query.
   - Agent tails Docker `json-file`, applies S03 redaction, and streams to control plane with resume cursor.
   - Server exposes paginated log query and CLI `midnight logs`.
   - Add rotation test with no container restart.

9. Seed/align failure-code constants.
   - Add `pkg/errors/codes` constants from the registry.
   - Add CI check that emitted codes appear in `docs/error-codes.md` or the canonical registry source.
   - Add missing S06 codes additively: `ACME_RATE_BUDGET_EXHAUSTED`, `ACME_HTTP01_UNREACHABLE`, `ACME_RATE_LIMITED`, `ACME_DNS_MISMATCH`, `ROUTE_RESERVED_PATH`, `DNS_PROPAGATION_TIMEOUT`, `WILDCARD_RENEWAL_FAILED`, `INGRESS_RELOAD_THRASH` if approved.

10. Package operator-facing defaults.
    - Add logrotate drop-in, Prometheus rules, Grafana dashboard, runbook stubs, and doctor checks.
    - Verify the support-bundle selectors include S06 diagnostics and exclude private keys/secrets.

## Open Decisions

- Whether S6-03 custom-domain HTTP-01 is a Phase 0 public feature or only a primitive/smoke path for Sprint 10.
- Whether `CAA_BLOCKS_ISSUANCE` should be added as an alias or replaced everywhere by the existing mandated `ACME_CAA_FORBIDDEN`.
- Whether timeline events should live in the existing S03 `events` table or a dedicated per-deployment timeline table.
- Which worker owns agent-side three-probe execution: S04 runtime agent or S06 health/timeline lane.
- Hosted prober deployment ownership and environment URL for `https://prober.midnight.dev/check`.

