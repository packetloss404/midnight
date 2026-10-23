# Auth Foundations

Auth code starts with a small policy-as-code contract in `internal/auth`.

## Production API Default

The MVP production baseline is bearer-token auth for the operator API. Package
and Postgres-backed server starts must set `MIDNIGHT_API_BEARER_TOKEN` or pass
`--api-bearer-token`; when production auth is expected but missing, protected
product/control-plane reads and writes fail closed with
`AUTH_REQUIRED_MISSING`. Only deliberately public compatibility, OpenAPI, and
health-style endpoints remain reachable. Local memory-mode developer routers
keep the unauthenticated Phase 0 behavior unless the router is explicitly
marked production-auth-required.

The operator bearer is a recovery credential, not a human or project-scoped
token. Arbitrary legacy operator secrets and the canonical `midn_admin_*`
class remain compatible. Server configuration and request middleware reject
recognized scoped classes (`midn_user_*`, `midn_sa_*`, `midn_agent_*`, and
`midn_mcp_*`) before comparing them as recovery credentials, so a copied
session, automation, agent, or MCP token cannot become installation-wide
authority. `Project-Access-Token` is not accepted or advertised.

## Durable Human Sessions

Migration 0038 adds hashed, expiring, idle-expiring, revocable human sessions.
Raw `midn_user_*` tokens are returned once; PostgreSQL stores only an
HMAC-SHA256 token verifier and a non-secret recognition prefix. Authentication
checks session expiry, idle expiry, revocation, user disable/delete state, and
the user's session generation atomically.

The default workspace claim is a one-shot operator-recovery operation. It
requires password setup plus a valid first TOTP code, encrypts the TOTP secret,
and enrolls the first administrator in one transaction. Its idempotency
fingerprint excludes password and TOTP material. Fixed migration IDs are
reserved; an existing collision aborts migration rather than inheriting
privilege.

Login verification is bounded by durable, HMAC-pseudonymized account and
network buckets, returns `Retry-After` when exhausted, and caps each user's
active sessions. Known and unknown accounts share the same public credential
failure.

Human sessions currently reach:

- `/auth/me` and `/auth/sessions`;
- accepted-workspace listing;
- existing workspace and direct-project membership administration;
- tenant-filtered project, service, and environment list/item reads; and
- `POST /projects`, as the first human product mutation admitted through the
  atomic tenant mutation coordinator; and
- `/projects/{project_id}/access`, which reports RBAC permissions without
  claiming that every corresponding runtime feature is installed.

Cross-tenant items are concealed as not found. Restricted environments are
concealed from non-admin readers. PostgreSQL collection reads apply canonical
membership and environment-restriction predicates in SQL; deterministic
in-memory fixtures use a post-filtering adapter. Product-session reads are
enabled only when session authentication and access grants are verifiably
bound to the same PostgreSQL or explicit in-memory identity authority.

Human product mutations remain deny-by-default. Migration 0039 scopes
idempotency attempts by actor, tenant, method, route, client key, and immutable
generation. The project-create coordinator revalidates the session and
membership before replay, then commits authorization, project, protected
production environment, creator ownership, exact terminal response, and audit
evidence in one serializable transaction. Dedicated coordinators now admit
service creation, digest-pinned image deployment, content-addressed local
source upload, redeploy/rollback/restart/stop/remove, and canonical GitHub
source linking with the same
current-authority, replay, product/work, exact-response, and audit boundary.
The exact operation-cancel route additionally fences pending outbox/queue work
or records a durable request for a leased build/deploy worker, then converges
the canonical deployment and operation at a safe point before traffic
promotion.
Canonical deployment and exact operation progress reads are tenant scoped.
GraphQL, MCP, dashboard-admin, audit, backup, routes,
tenant failure marking, variables, domains, volumes, templates, monitors, agents, and
every other product mutation remain unavailable to human sessions until each
route has the same boundary. Midnight claims only this enumerated
deploy-capable human surface, not general operator equivalence.

## Workspace And Team Model

Workspaces are the durable tenant boundary. Projects carry a required
`workspace_id`; no database or API default silently assigns authenticated
human creations to a tenant. Upgraded single-operator installs receive the
explicit `wrk_default` migration workspace until it is claimed.

The canonical workspace roles are `admin`, `member`, `deployer`, and the
Midnight migration extension `viewer`. Direct project roles are `owner`,
`editor`, and `viewer`. Environment access restriction is a hard overlay:
non-admin human access is denied except for separately authenticated Git
automation.

Membership write authorization, last-workspace-admin checks,
last-project-owner checks, and the write itself share a serializable store
boundary. Removing a workspace member is rejected if its cascading direct
project-membership cleanup would leave any project without another active
owner. The current write endpoints update existing accepted memberships only;
they do not implement invitation, email delivery, user provisioning, or
acceptance.

The older policy-document foundation below remains available for policy
authoring experiments. It is not the authorization implementation used by the
durable human-session routes.

Teams belong to one workspace and users can match a policy binding through
direct user identity or through team membership supplied in the access
request.

`internal/workspace` provides a live-DB-free in-memory workspace/team/RBAC store for product-completeness flows, tests, and local fixtures. It validates workspace/team identifiers, rejects team bindings outside the policy workspace, resolves user team membership before authorization, and exposes deterministic read-only snapshots for sample product surfaces that do not need `internal/api` ownership.

Public multi-tenant resource planning uses `internal/tenancy.Scope` for the workspace/project/environment boundary and validates canonical `midnight.*` labels before accepting resource ownership claims.

## Policy Document

Policies are JSON documents with explicit bindings:

```json
{
  "version": 1,
  "bindings": [
    {
      "subject": {"kind": "team", "id": "team_platform"},
      "role": "developer",
      "scope": {"kind": "workspace", "id": "wrk_acme"}
    }
  ]
}
```

Supported roles are `owner`, `admin`, `developer`, and `viewer`. Only `owner` can perform `policy.write`.

`PolicyDocument.Evaluate` returns an auditable decision envelope with `effect`, `allowed`, `reason`, `rule_id`, `policy_version`, subject, action, and scope. The existing `Allows` helper is a boolean wrapper over the same decision path. This is the local boundary for later OPA/Rego embedding: Rego may replace the evaluator, but it must preserve the decision envelope for audit logging and authorization tests.

## Built-In Policy Library

Policy metadata lives in `internal/policy`. The built-in library pins stable policy IDs for deployment resource limits, fork-preview secret isolation, cross-scope variable reads, and AI Class-2 draft-only behavior. The package validates unique IDs, enforcement modes, and target input categories before a future authoring/testing UI or OPA bundle loader consumes the library.

`internal/policy.RunTests` is the local policy authoring harness. A test case supplies a policy document, access request, expected allow/deny result, and optional reason. The harness returns per-case decisions plus pass/fail summary so dashboard or CLI authoring flows can prove policy behavior before switching a rule to enforce mode.

## OIDC Foundation

OIDC provider configuration validation currently enforces absolute HTTPS issuer and callback URLs, a non-empty client ID, and the callback path `/api/v1alpha/auth/oidc/callback`. Discovery, login handlers, token exchange, and session persistence are future integration work.

## Enterprise SSO Foundation

Enterprise identity foundations live in `internal/enterprise/identity` as validation and planning contracts only. The package validates OIDC and SAML provider shapes, SAML ACS URL targeting `/api/v1alpha/auth/saml/callback`, HTTPS-only IdP URLs, single-block SAML signing certificate PEM inputs, SCIM base URLs, SCIM token secret references, and group-to-role mappings.

`BuildPlan` returns deterministic callback paths and audit action names for review. It does not fetch OIDC discovery documents, call SAML metadata endpoints, validate assertions, provision SCIM users, or persist identity configuration.
