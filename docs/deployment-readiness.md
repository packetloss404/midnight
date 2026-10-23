# CLI and API deployment readiness

Reconciled: 2026-09-25 against repository commit `4e802df`, installed commit
`8ad2845`, and live package, domain, application, DNS, and edge observations.

Status: fresh installation, source deployment, and public HTTPS are recorded
across the dated evidence below. The current host runs commit `8ad2845`, schema
72, and six active units. Its console and DocVault custom hostname respond over
valid external HTTPS through an operator-managed Caddy edge. Cloudflare is now
authoritative for the operator-owned domain; wildcard generated-app DNS and edge HTTPS
survived a Caddy/portal restart on September 25. Application restart/rollback,
GitHub, and complete recovery acceptance remain open.

The executable contract is [MVP acceptance](mvp-acceptance.md); the operator
procedure is [live deploy proof](runtime/live-deploy-proof.md). Passing code
checks does not establish the installed artifact or its current health.

## Evidence ledger

| Date / source | Recorded result | Boundary |
| --- | --- | --- |
| 2026-09-03, `94df089` and handoff `df8d054` | Alpha-6/schema-70 upgrade, six active units, install-readiness, and Go/API/web/package/PostgreSQL checks for the reviewed configuration journey | Earlier upgrade evidence; superseded as the repository checkpoint |
| 2026-09-05, `MID-116`, report `b9ae643` | Wiped-host install configures successfully; schema 71; both doctor profiles have zero fatal failures; six units active; API/console return 200; first administrator and TOTP created | Fresh-install evidence for that run; the report does not identify today's installed package/commit |
| 2026-09-05, `MID-117`, report `d073931` | DocVault built from source; Traefik port-80 request with generated Host header returns application HTML; backend matches active deployment; route reports ready/reachable; PostgreSQL/Redis connectivity and persistence verified | Proves source-to-HTTP application service for that run. It does not independently prove public DNS, firewall/NAT, TLS, external HTTPS, or full recovery acceptance |
| 2026-09-12, `0891e0c`, merged by `4923a4a` | Operator-only administrator password/TOTP recovery in CLI/API/console, with session revocation and atomic durable audit/idempotency | Source implementation and test coverage are committed; this review adds no installed recovery proof |
| 2026-09-21, [`f0c132c` installed verification](research/installed-vps-verification-2026-09-21.md) | Commit-bound package provenance passed; package and `/compat` identify `f0c132c`; schema maximum/count is `71|71`; six units active; no failed units; install-readiness checks pass; registry and BuildKit probes respond; packaged password-reset help is registered | Installed-package and live-listener evidence at one observation time. No new app deployment, public HTTPS, private-GitHub, off-host restore, restart/rollback, or credential reset was performed |
| 2026-09-24, [`8ad2845` installed and public-domain verification](research/installed-vps-verification-2026-09-24.md) | Package and `/compat` identify `8ad2845`; schema maximum/count is `72|72`; six units are active; an operator-owned instance namespace, its console, and a healthy DocVault deployment on a verified custom hostname return valid external HTTPS | Point-in-time proof on the existing host. Caddy, managed outside Midnight, terminates TLS and forwards HTTP to Midnight. No fresh install, process restart, rollback, private-GitHub run, administrator recovery, or off-host restore was performed |
| 2026-09-25, [Cloudflare wildcard DNS and TLS verification](research/cloudflare-wildcard-tls-verification-2026-09-25.md) | The operator domain delegates to Cloudflare; an arbitrary one-label host under its generated-app namespace resolves to public ingress, redirects from HTTP to HTTPS, presents a trusted wildcard certificate, and reaches Midnight ingress after Caddy and portal restart | Proves generated-host DNS, edge TLS, Host forwarding, and edge restart persistence. The probe intentionally had no Midnight route and returned routing 404, so it does not replace a real source/image deployment or backend-readiness proof |

The reports are retained in [BACKLOG.md](../BACKLOG.md) under their stable IDs
and in Git history (`git show b9ae643:BACKLOG.md` and
`git show d073931:BACKLOG.md`). Their assertions have the scope above; they are
not substitutes for a complete commit-bound acceptance evidence bundle.

## Current source baseline

- 72 sequential migration pairs, including durable instance domain settings in
  migration 0072.
- First-administrator CLI/console setup, authenticated agent mutations and
  streaming, diagnosis observer wiring, intake image admission, embedded
  console assets, and improved CLI flag help landed in the September 5 build-out.
- Package, BuildKit, file-permission, route publication, backend deduplication,
  and conditional service-config update fixes landed through `233d7d5`.
- Administrator recovery landed in `0891e0c` without a new migration.
- CLI registration for `admin reset-password` landed in `a580043`.
- Instance domain setup landed through `aa9ea32`; the public console-domain
  landing behavior landed through `8ad2845`.

## Supported deployment paths

All REST paths below are relative to `/api/v1alpha`.

| Entry point | Supported flow | Evidence / remaining work |
| --- | --- | --- |
| `midnight up` | Upload linked local source, queue a build, stream progress unless detached | Source-to-HTTP deployment recorded in `MID-117`; release-candidate HTTPS/recovery acceptance remains |
| `midnight deploy image` | Submit an image for a service/environment, with server-side admission and optional generated route | Implemented; retain candidate-specific image, status, restart, and rollback evidence |
| `POST /services/{service_id}/deployments` | Tenant-authorized source/image intake with idempotency and durable operation state | Implemented; deployment intake alone is not runtime success |
| `POST /environments/{environment_id}/changeset/apply-and-deploy` | Apply exact reviewed revision, snapshot services, and queue server-selected rebuild/redeploy work | Implemented and covered by the September 3 integration/installation checkpoint; direct CLI wrapper remains `MID-107` |
| `GET /environments/{environment_id}/operation-groups/{group_id}` | Read aggregate/per-service Apply & Deploy progress | Implemented; direct CLI wrapper remains `MID-107` |
| CLI deployment inspection | `deploy status`, `deploy events`, `deploy logs`, `logs`, and linked-service actions | Implemented; inspect status for the underlying reason when `up` reports failure |

The server derives deployment/route identities and resolves admitted mutable
image tags to immutable digests at intake. `midnight up` needs an authenticated
server and `.midnight/project.json` created by `midnight link`.

## Known CLI parity gap

`cmd/midnight/changeset.go` admits `get`, `validate`, `apply`, and `discard`.
The combined Apply & Deploy transition and operation-group read still require
the API or console (`MID-107`). Shared flag definitions now populate command
help; the old blanket claim that legacy flags are absent from help is obsolete.

## Evidence still required

1. Refresh the installed package, `/compat` commit, schema, configuration, and
   candidate identity whenever a later release replaces the `8ad2845`
   observation.
2. Retain source-build, registry-push, runtime-promotion, and image-deploy
   evidence for the candidate; reuse the September 5 reports only for the
   assertions and artifact identity they actually establish.
3. Complete `MID-111`: prove a source build succeeds after restarting
   `midnight-server`, with the BuildKit socket surviving the restart.
4. Repeat public DNS, ingress, route readiness, edge TLS, and external HTTPS
   evidence on the final fresh-host candidate. Preserve the separation between
   Midnight's HTTP route and an external TLS-terminating edge.
5. Finish the fresh-host checklist (`MID-003`): restart-safe state, rollback,
   audit, dashboard/CLI/API agreement, and recovery evidence. Product-level
   off-host destruction/restore/readback remains `MID-004`.
6. Exercise administrator recovery on the installed candidate before claiming
   that path is live-proven. Keep private-GitHub/status delivery (`MID-002`)
   and hosted release/channel gates (`MID-010`/`MID-105`) separately tracked.

Capture redacted evidence under `artifacts/mvp/`; never retain passwords,
bearer tokens, DSNs, private keys, rendered route configuration, or raw secrets.
