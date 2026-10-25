# Deleting a service

Status: implemented v1alpha `DELETE /services/{id}` (2026-09-04)

## Why this exists

Before this route, nothing in Midnight could be deleted. The console's Danger
zone was a paragraph pointing at a CLI command that did not exist, and a failed
template deploy left orphan services in a project permanently. That is not a
missing nicety: it is the first thing a new user needs after their first
mistake.

## Decision

`DELETE /api/v1alpha/services/{id}` removes one service and the live intent it
owns, in one serializable transaction, and refuses rather than stranding
anything.

The route **never stops, drains, or removes a container.** Runtime teardown has
one owner already — the deployment action path at
`POST /services/{service_id}/deployments/{deployment_id}/actions/{action}` —
and the delete does not duplicate it. Instead, a service whose runtime is still
live is refused. The operator stops it, then deletes it. This is the explicit
choice between "drain on delete" and "refuse to delete a running service", and
the API, the console, and the error registry all state it the same way.

## What counts as still running

The delete is refused with `SERVICE_DELETE_RUNNING` (409) when the service has

- any deployment in `queued`, `building`, or `deploying` — a deploy is in
  flight; or
- an `active` deployment whose `runtime_desired_state` is still `running` — a
  container is serving traffic; or
- a `deployment_runtime_actions` row in `queued` or `running` — a stop,
  restart, or remove is mid-flight.

An `active` deployment whose `runtime_desired_state` is `stopped` is **not**
running. The runtime action path already stopped its containers; the row
survives only as the record of what ran last, and the service can be deleted.
This is why "stop, then delete" terminates: `active` can only ever transition to
`superseded`, so waiting for the deployment to leave `active` would make a
successfully deployed service undeletable forever.

The delete is refused with `SERVICE_DELETE_BLOCKED` (409) when another surface
still holds part of the service:

- a volume with a `pending` or `active` attachment,
- an open pull-request preview (`closed_at IS NULL`),
- an unfinished source upload reservation (unpublished, lease not expired).

The last two are also the only two `ON DELETE RESTRICT` foreign keys into
`services`, so refusing them keeps the API contract and the schema agreeing.

## What the delete removes

All of it inside the one transaction, after every refusal check has passed:

| Removed | How |
| --- | --- |
| Variables | Tombstoned the same way `DELETE .../variables/{id}` does: envelope material erased immediately, metadata row retained. Immutable historical versions a past deployment pinned are left alone. |
| Volumes | Soft-deleted with the panel-locked grace window, so the existing reaper removes the backing directory instead of leaving it on disk with no owner. Attachments are cleared. |
| Published domains and HTTP routes | `http_routes` (cascading its backends, route intents, and domain bindings), then `domains` (cascading DNS challenges and certificate orders). `retired_product_domain_ids` has no foreign key and is cleared explicitly. |
| TCP proxy listeners | `tcp_proxy_endpoints`. |
| Configuration | `service_environment_configs`, `service_configuration_revisions`, `service_environment_state`. |
| Repository link | `service_github_links`, which ends auto-deploy. |
| Unapplied edits | `environment_changeset_items` naming the service, which would otherwise keep appearing in the review panel with nothing behind them. |
| Monitors | `monitor_definitions`. |
| Scheduling intent | `cron_schedule_cursors` and `environment_locks`, which carry a `service_id` but no foreign key and so would survive on their own. |

The service row itself is soft-deleted (`deleted_at`), which is the convention
every lineage query in the codebase already assumes, and which the workspace
count triggers from migration 0069 already treat as "not a live service".

## What the delete keeps

Deployments, their events, their logs, and the audit chain. They are evidence,
not live intent. Keeping them also means the delete never writes a `deployments`
row, so it cannot interact with the deployment FSM trigger attached in
migration 0016.

## Preconditions and replay

- **RBAC.** `service.delete`, evaluated inside the transaction after the
  workspace- and project-membership advisory locks and a session revalidation.
  The capability is deliberately narrower than `service.write`: a workspace
  member and a project editor can both edit a service and neither can delete
  one. Workspace admins and project owners can.
- **Resource version.** `If-Match` must carry the strong validator that
  `GET /services/{id}` now publishes as an `ETag` and echoes as
  `resource_version` in the body. A stale validator is `412`; a missing one is
  `428`. A console that has not refreshed cannot delete something that changed.
- **Idempotency.** Exactly one `Idempotency-Key`. The fingerprint binds the key
  to the concrete service path and to the `expected_version`, so one key cannot
  replay across two services in a project, and reusing a key with a different
  precondition is a conflict rather than a silent replay.
- **Refusals are committed outcomes.** A `SERVICE_DELETE_RUNNING`,
  `SERVICE_DELETE_BLOCKED`, or `412` result is stored as the exact idempotency
  response and written to the audit chain as a `denied` `service.delete` row in
  the same transaction. Retrying with the same key replays the identical
  refusal.

## Response

`200` with a receipt rather than a bare `204`, so a caller can tell the user
what actually happened:

```json
{
  "service_id": "svc_...",
  "project_id": "prj_...",
  "slug": "api",
  "deleted_at": "2026-09-04T12:00:00Z",
  "retained_deployments": 4,
  "removed": {
    "variables": 3, "volumes": 1, "published_domains": 1, "routes": 1,
    "tcp_proxies": 0, "environment_configs": 2, "source_links": 1,
    "pending_changes": 0, "monitors": 0
  }
}
```

## Two paths, one implementation

A human session goes through `PostgresTenantMutationCoordinator.DeleteService`,
which commits authorization, replay, the removal, and audit evidence together.
Any other principal (the installation recovery bearer) goes through
`PostgresResourceStore.DeleteServiceCAS`. Both call the same
`deleteServiceInTx`, so the two surfaces cannot drift on which rows survive.
The recovery path is unavailable when tenant reads are enabled, matching how
conditional environment deletion already behaves.

## Schema change

Migration `0071_service_deletion` replaces the total
`UNIQUE (project_id, slug)` constraint on `services` with a partial unique
index over live rows. Without it a deleted slug could never be reused, so the
retry of a failed template run would collide with the orphan it was meant to
replace. It also adds the partial index the live-deployment precondition asks
for on every attempt. The down migration renames colliding deleted slugs rather
than deleting rows, so reverting never cascades a service subtree away.

## Console

The service Settings panel carries a real Danger zone. It lists exactly what
will be removed and exactly what is kept, requires the operator to type the
service slug, is disabled for a role without `service.delete`, and reports the
server's typed refusal in the server's own terms — "This service is still
running. Stop its deployment from the Deployments tab, then delete it." — rather
than flattening a 409 into "the resource changed while you were working".

## Not covered yet

Deleting a **project** or an **environment** over the human product surface, and
deleting a **volume** independently of its service. `project.delete` exists as a
capability and `DELETE /environments/{id}` exists as an operator recovery route,
but neither has an atomic tenant coordinator, so both stay closed. The project
settings page says so plainly instead of showing a control that cannot work.
