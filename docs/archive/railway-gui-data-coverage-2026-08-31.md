# Railway-derived data coverage in the Midnight GUI

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

Date: 2026-08-31

Status: implementation and verification record, reconciled through the
reviewed configuration journey on 2026-09-03

Sources:

- `docs/archive/midnight-railway-ui-comparison-2026-07-27.md`
- `docs/railway-ux-9-of-10-target-2026-09-01.md`
- `docs/archive/railway-parity-review-2026-07-27.md`
- `docs/archive/railway-parity-loop3.5b-handoff-2026-07-29.md` through
  `docs/archive/railway-parity-loop3.7-handoff-2026-07-29.md`
- `docs/archive/railway-parity-reboot-handoff-2026-07-28.md`
- `docs/railway-parity-runner-evidence.md`
- `docs/archive/railway-parity-vm-validation-handoff-2026-07-29.md`
- `BACKLOG.md` (canonical current capability state)

## Coverage rule

“Represented” means that a durable Midnight fact has a discoverable GUI
surface, an explicit unavailable state, or an explicit later-profile boundary.
It does not mean that Midnight claims every Railway capability. Midnight must
never turn desired configuration, a generated identity, or an implementation
plan into observed runtime or public-reachability evidence.

The environment resource summary is the canonical operational read model for
the Grid and service Overview. It separates:

- the current traffic-bearing deployment from the newest rollout;
- configured source identity from the exact deployed revision;
- desired runtime state from observed runtime state;
- requested replica count from persisted ready/running observations;
- deterministic private DNS identity from runtime readiness;
- intended public routing from a route with matching controller, Traefik, and
  trusted-probe evidence.

## Operational data coverage

| Railway-derived concept | Midnight durable truth | GUI and UX representation | Coverage |
| --- | --- | --- | --- |
| Project and active environment | Project/environment resource rows and current RBAC scope | Project header, environment selector, Grid scope, project navigation | Complete |
| Services/resources | Tenant-filtered service rows | Deterministic Grid by default; Map remains an optional relationship view | Complete |
| Connected source | Canonical GitHub service link or canonical deployment source | Card Source fact; Overview Connected source block; Configure → Source | Complete |
| Repository branch/ref and monorepo filters | Canonical source link plus release source fields | Card Source; Overview Branch/ref, watch paths, root directory, config path, CI gate, Dockerfile, and builder | Complete |
| Exact deployed revision | Canonical deployment SHA | Card Current revision and Rollout revision; Overview current-release Revision and latest matching source revision; deployment detail | Complete |
| Root directory and builder | Canonical deployment source/build fields | Overview Connected source block; deployment detail | Complete |
| Current release | Canonical active deployment pointer | Card Current release; Overview Current release | Complete |
| Newest rollout | Newest canonical deployment independent of active pointer | Card Latest rollout; Overview Latest rollout | Complete |
| Build/deploy progress | Canonical deployment, operation, and operation-group state | Card status/next action; resumable first-deploy guidance; deployment timeline; Activity indicator | Complete |
| Failure code/reason | Canonical operation/deployment failure evidence | Card alert; Overview alert; deployment detail and logs next action | Complete |
| Desired/observed runtime | Canonical deployment runtime state and generation | Card Runtime; Overview Current release; Configure → Runtime | Complete |
| Replica readiness | `deployment_instances` desired/ready/running observations | Card Health/replicas; Overview Health; durable metrics | Complete |
| Public HTTP endpoint | Active backend plus current route-group, artifact, Traefik acknowledgement, and ingress probe | Card Published endpoint/Open live; Overview Reachability; command palette Open public URL | Complete |
| Private address | Deterministic service DNS identity | Card Private address; Overview Reachability; command palette Copy private address. UI explicitly does not call the identity reachable | Complete |
| Logs | Tenant-scoped operation records and durable scoped observability records | Service Logs for ordinary project sessions; installation-wide Observability remains recovery-authorized | Complete at service scope, subject to collector health shown in the UI |
| Metrics | Tenant-authorized durable Docker metric samples | Service Metrics for ordinary project sessions. Fleet runtime inventory and project-wide Observability remain recovery-authorized | Complete at service scope, subject to collector health shown in the UI |
| Variables/secrets | Metadata-only tenant variable records and encrypted versioned changesets | Configure → Variables, Changes bar, and redaction-safe review sheet; plaintext secret diffs are never rendered | Complete |
| Runtime settings | Service/environment config, durable changeset, configuration revisions, and server-derived deployment impact | Searchable Configure → Runtime, including service kind, replicas, health/start/restart behavior, cron, and TCP where admitted; review predicts rebuild/redeploy/no-op | Complete |
| Networking settings | Tenant domain/config routes and durable changeset | Configure → Networking, including generated/custom host evidence and TCP configuration where admitted | Complete |
| Custom-domain ownership and routing DNS | Durable TXT challenge plus provider-neutral CNAME or apex A/AAAA guidance | Configure → Networking separates ownership proof, DNS guidance, route state, and public evidence | Complete; provider mutation and external DNS proof remain operator gates |
| TLS/ACME intent and certificate state | Durable TLS desire, certificate-order references, and route publication evidence | Configure → Networking shows HTTPS intent and certificate/route state without claiming browser reachability | Complete for product state; delegated DNS, Traefik loading, and public HTTPS remain operator gates |
| Persistent volume intent | Durable environment changeset | Searchable Add resource → Volume; Configure → Storage; Changes review and Apply & Deploy | Complete for desired state |
| Applied volume/backup/restore records | Canonical volume and recovery stores | Recovery-authorized Storage panel | Degraded for ordinary project sessions: the GUI names the boundary and sends no installation-wide storage read |
| Deployment actions | Canonical tenant action coordinators | Deployment detail and command palette for deploy/restart; full action set remains in deployment detail | Complete |
| Environments | Environment rows, restrictions, versions, and duplication/sync contracts | Environment selector and Project settings | Complete for admitted lifecycle; no PR-environment inference |
| Pull-request previews | Durable preview lifecycle | Deployment list preview capability | Complete for admitted GitHub preview lifecycle |
| Templates and databases | Official template catalog and project-owned community forks | New project, Add resource, and Project settings | Complete for self-managed single-node templates; no managed-HA claim |
| Cron occurrences | Durable scheduled occurrence records | Configure → Runtime → Cron history when the tenant route is admitted | Explicitly gated when the exact history authority is unavailable |
| Monitors/incidents/delivery | Durable monitor, incident, channel, and delivery records | Project Observability | Recovery-authorized capability; installed external delivery remains operator evidence |
| Usage and limits | Reconciled workspace metering and resource gauges | Global Usage & limits | Complete; payment/Stripe is intentionally absent |
| Audit history | Tenant-scoped project audit records | Project settings activity/audit surface | Complete |
| GitHub App setup and repository grants | Workspace-bound installation/repository grants | Settings and source picker | Complete; live private-repository proof remains an operator gate |

## Creation and command coverage

The visible Create action and command palette share the same product-action
vocabulary. The palette deep-links to GitHub, container image, database,
template, and volume flows. For a selected service it exposes Overview,
Deployments, Logs, Metrics, Source, Variables, Networking, Storage, Runtime,
Generate public domain, Open public URL, Copy private address, Deploy latest,
and Restart. Disabled commands remain visible with a concrete state or
authority reason.

Configuration and creation registries are searchable and share the same action
vocabulary with the command palette. Proposed configuration is isolated in the
Changes bar/review sheet; builds, deploys, restarts, and restores appear as
Activity. Apply & Deploy is the primary reviewed transition, while apply-only
is available under Advanced. Service cards and Overview distinguish the
traffic-serving release from a newer rollout and expose provenance only when
the user asks for detail.

## Deliberate non-parity

These Railway concepts are represented as boundaries, not working Midnight
features:

- multi-host scheduling, multi-region placement, global edge traffic, and
  distributed private networking are later profiles (`MID-201`);
- ZFS-backed snapshots and cross-host volume movement are later profiles
  (`MID-202`);
- managed database HA, engine upgrade orchestration, and managed failover are
  not implied by Midnight's self-managed templates;
- authoritative public DNS, Internet reachability, ACME production issuance,
  private-GitHub delivery, clean-host installation, and off-host recovery
  remain external evidence gates in `BACKLOG.md`;
- billing/payment integration is intentionally absent; Usage & limits reports
  evidence without pretending that commerce exists.

## Remaining GUI security seam

Applied volume, backup, and restore collection reads still use the
installation recovery boundary. The project Storage surface therefore permits
safe changeset staging but does not issue those broad reads. Closing that seam
requires tenant-scoped volume and recovery routes with exact project,
environment, service, and current-role revalidation; it must not be solved by
making the existing installation routes visible to human sessions.
