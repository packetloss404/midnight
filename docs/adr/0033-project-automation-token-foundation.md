# ADR-0033: Project automation tokens start as a closed, scoped verifier foundation

- Status: accepted
- Date: 2026-07-28

## Context

The CLI recognizes `MIDNIGHT_TOKEN`, the public token vocabulary includes
`midn_sa_`, and the baseline schema contains an `api_tokens` table. Those
shapes do not constitute project authorization. `api_tokens` has no tenant or
project scope, and treating one of its rows as a product principal would grant
installation-wide access.

The existing `service_accounts` table supplies a stable machine-actor identity,
while migration 0038 supplies durable workspace ownership for projects.
Automation credentials need to bind both relationships without weakening
human-session authorization or the atomic mutation contract in ADR-0032.

Token creation also returns secret material once. The current idempotency store
persists exact response bodies in plaintext, so it cannot safely replay a token
creation response.

## Decision

Migration 0040 adds `project_automation_tokens` and leaves legacy `api_tokens`
untouched and non-authenticating. Every token has:

- one stable service account and one project in the same workspace, enforced
  with composite foreign keys;
- an HMAC-SHA-256 verifier, a display-only prefix, and no recoverable raw
  secret;
- one immutable `ci-deploy-v1` profile;
- required absolute expiry; and
- retained revocation, creator, last-use, and resource-version metadata.

The internal automation-token manager mints 256-bit
`midn_sa_<base64url-secret>` credentials and returns the raw value once. It
accepts only the exact canonical token shape, derives a domain-separated HMAC
verifier, and authenticates only an active, unexpired token whose project and
service account are still active. Storage failures remain errors; they never
fall back to an operator, human, developer, or legacy token path.

`ci-deploy-v1` is metadata in this foundation, not route authorization. Its
future admission policy is deliberately narrow: resolve its own project,
services, and environments; trigger CLI deployments; and read status and logs
for those deployments. It does not grant membership, token management,
variable reads or writes, resource deletion, domains, volumes, GraphQL, MCP,
or operator administration.

## Dependency gates

This decision does **not** register HTTP routes or make automation tokens
usable for deployment. The following gates remain closed:

1. human dashboard and CLI sessions must authorize token management;
2. a typed principal resolver and exact method/route capability registry must
   bind every admitted request to its canonical project;
3. operations, events, and logs need durable project-scope resolution;
4. token creation needs encrypted, authorization-revalidated idempotency replay
   before it can satisfy Midnight's mutation contract;
5. creation, revocation, and deploy use must join audit evidence and product
   changes in the serializable tenant mutation coordinator; and
6. GraphQL and MCP may advertise automation-token support only after they use
   the same scoped principal and service layer.

Until all applicable gates pass, CLI and GUI capability claims must say that
project automation tokens are unavailable.

## Consequences

- Legacy `api_tokens` rows remain inert even if their `token_class` is `sa`.
- Deleting a project or service account with token history is restricted;
  normal lifecycle uses soft deletion and retained revocation evidence.
- A service account may own replacement credentials without changing its
  stable audit actor identity.
- Arbitrary capability lists, workspace/user personal tokens, alias headers,
  OIDC federation, scheduled rotation, and GraphQL/MCP use remain deferred.
- Migration downgrade refuses to discard token history.

## Verification gate

The foundation requires tests proving:

- mismatched workspace/project/service-account relationships fail;
- only HMAC verifier bytes and a safe prefix persist;
- malformed, expired, revoked, soft-deleted-scope, and legacy tokens fail
  closed;
- list and revoke return metadata without verifier or raw material;
- revocation is idempotent and preserves its first attribution; and
- migration downgrade refuses while token history exists.
