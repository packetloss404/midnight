# ADR-0038: Canonical tenant-safe GitHub source links

- Status: accepted
- Date: 2026-07-29

## Context

Midnight's original GitHub integration stored a mutable `spec.github` object on
each service and exposed App-wide installation discovery to the product UI.
That shape could not prove that a human still belonged to the service
workspace, that the installation belonged exclusively to that workspace, or
that the App still had access to the exact immutable repository identity.
Revocation and webhook intake also used different authorities, which left a
race where a revoked link could still become durable build work.

GitHub documents installation and repository identifiers as the stable
authorization coordinates. Repository names are presentation data and may
change. GitHub also warns that setup-URL query parameters are untrusted, so an
installation callback alone is not sufficient evidence for a workspace grant.

## Decision

Migration 0049 introduces three canonical relations:

1. `github_workspace_installation_grants` assigns an installation to exactly
   one Midnight workspace;
2. `github_workspace_repository_grants` records exact repository IDs that the
   App verified under that installation; and
3. `service_github_links` binds one service to an exact installation,
   repository ID, canonical repository name, and branch.

Legacy `service.spec.github` values are copied to
`github_source_link_quarantine` and removed from active service specs. They are
not silently promoted because they cannot prove current workspace ownership or
repository access.

An installation-wide recovery operator explicitly assigns an installation and
the complete allowed repository-ID set with:

```text
PUT /api/v1alpha/github/installations/{installation_id}/workspace-grant
```

The server asks GitHub for the installation and every repository before it
writes the grant. The request cannot supply account names, repository names,
clone URLs, or privacy metadata. Replacing the set immediately revokes omitted
repository grants and their service links. Revoking the installation removes
all of its links in the same serializable transaction. A globally unique
installation constraint prevents assignment to two workspaces.

Human product routes expose only repositories granted to the current project
workspace:

```text
GET    /api/v1alpha/projects/{project_id}/github/repositories
GET    /api/v1alpha/projects/{project_id}/github/repositories/{repository_id}/branches
GET    /api/v1alpha/services/{service_id}/github
PUT    /api/v1alpha/services/{service_id}/github
DELETE /api/v1alpha/services/{service_id}/github
```

Link mutation accepts exactly `installation_id`, `repository_id`, and
`branch`. It requires a current human session plus both `service.write` and
`deployment.trigger.git`, verifies the exact repository and branch live after
local authorization, then reauthorizes and commits link, idempotency response,
and audit evidence in one serializable transaction. Linking does not start a
deployment.

Signed push intake keys lookup by installation ID, repository ID, canonical
repository name, branch, and commit SHA. Fork or otherwise untrusted pushes are
skipped. Before staged work becomes durable, the intake transaction locks the
installation authority and revalidates the installation grant, repository
grant, source link, and link resource version. Revocation and intake therefore
have one ordering boundary.

Build jobs persist only non-secret source coordinates. Immediately before
checkout, the build worker mints an installation token restricted to the exact
repository ID with only `contents:read` and keeps it in process memory. The job
carries the exact link resource version; the worker revalidates current
workspace/project/service/link authority before token mint, before variable
resolution, and immediately before build solve. Revocation fences pending work
at those safe points; work already beyond the final check is not synchronously
interrupted.

`wait_for_ci` and required checks remain rejected by the canonical store until
there is a production-wired check gate. The historical optional in-memory
shape is not a capability claim.

## Consequences

- A workspace administrator can deliberately recover/install GitHub access,
  while ordinary users never enumerate App-global installations.
- Repository renames cannot redirect an existing numeric-ID grant; a live
  verification updates trusted presentation metadata.
- Cross-workspace installation reuse fails closed, and revocation wins or
  precedes webhook work under a shared database lock.
- GUI and CLI source selectors use project-scoped repository and branch
  discovery and have no manual installation-ID fallback.
- Migration 0049 invalidates legacy links by design. Operators must create a
  verified workspace grant and relink affected services.
- ADR 0039 and migration 0050 supersede the synchronous delivery boundary with
  a persist-before-ack, at-least-once inbox. Exactly-once delivery and an
  installed private-repository/process-death run are still not claimed.
- A live private-repository checkout and installed GitHub delivery remain
  external acceptance evidence.
