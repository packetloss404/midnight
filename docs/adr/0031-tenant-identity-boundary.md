# ADR-0031: Durable tenant identity precedes human product access

- Status: accepted
- Date: 2026-07-28

## Context

Midnight historically protected one globally scoped control plane with an
operator bearer. Railway-style accounts, workspaces, roles, and revocable
sessions cannot be layered on top by merely accepting another token: global
collection and lookup methods would expose resources across tenants.

Existing installs also contain projects with no workspace owner and legacy
workspace roles. An upgrade must preserve access without silently granting a
preexisting record the migration identity.

## Decision

Migration 0038 establishes the durable identity boundary:

- every project has a required explicit `workspace_id`;
- upgraded projects move to the reserved `wrk_default` workspace under a
  disabled migration identity;
- reserved identity/workspace collisions abort the migration;
- the one-shot operator-recovery claim requires password setup and a verified
  first TOTP code before creating the first human administrator;
- human bearer tokens are random, stored only as HMAC verifiers, expire
  absolutely and when idle, and are revocable;
- credential, MFA, forced-rotation, disable, and delete transitions increment
  `session_generation` through a database trigger;
- durable HMAC-pseudonymized account and network budgets bound password
  verification, and active sessions are capped per user;
- workspace roles are `admin`, `member`, `deployer`, and the migration-safe
  `viewer` extension; direct project roles are `owner`, `editor`, and
  `viewer`;
- environment restriction is a deny overlay for non-admin humans, with a
  separate Git-automation exception;
- membership authorization, role hierarchy checks, last-admin/last-owner
  invariants, cascading cleanup, and writes share a serializable store
  boundary;
- claim, login, revocation, and membership decisions enter the existing audit
  hash chain without password, TOTP, or bearer material.

The operator bearer remains a clearly named recovery credential. Human
sessions advance onto product routes only through tenant-scoped stores and the
route-by-route atomic coordinator in ADR-0032. The `Project-Access-Token`
alias is removed because Midnight does not yet mint a project-scoped
credential.

## Consequences

Project creation now requires `workspace_id` in REST, CLI, and the canonical
schema. Operator-only compatibility surfaces may explicitly select
`wrk_default`; authenticated human creation will never receive an implicit
tenant.

The current membership write routes update existing accepted memberships.
They are not invitation, email delivery, provisioning, or acceptance flows.

The OAuth shim in ADR-0004 remains an operator-token compatibility flow until
the CLI session gate replaces it. It must not be described as human login.

## Release gate

Tenant-scoped project/service/environment reads and atomic project creation
have passed the two-workspace concealment, replay, revocation, and membership
race gates. All other human product writes remain blocked until the same
coordinator proves the full login→project→deployment→revocation journey
through REST and CLI/GUI. GraphQL and MCP remain operator-only until they
delegate to that layer and pass equivalent cross-tenant tests.

## Deliberate boundaries

- No invitation or account-recovery lifecycle is claimed.
- No project/service token is claimed.
- No environment-specific membership role is claimed; restriction is an
  overlay on workspace/project grants.
- No deploy-capable human GUI, CLI, GraphQL, or MCP session is claimed by this
  decision. The current human write capability is REST project creation only.

Later implementation note (2026-07-29): ADR-0032 through ADR-0040
subsequently admit project/service creation, project automation tokens,
invitations, digest-pinned image and content-addressed local-source deployment,
redeploy/rollback/restart/stop/remove, deployment-operation cancellation, and
canonical GitHub source linking, ordinary environment metadata, exact
service/environment config and variables, and the generated HTTP domain
singleton across REST, CLI, and GUI. This historical decision still does not
admit custom-domain ownership, TLS, tenant failure marking, GraphQL, or MCP.
