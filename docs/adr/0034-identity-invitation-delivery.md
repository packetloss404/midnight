# ADR-0034: Identity invitations use manual one-time delivery and atomic redemption

- Status: accepted
- Date: 2026-07-28

## Context

Midnight needs a usable workspace and direct-project invitation flow without
claiming an email-delivery system that does not exist. An invitation grants
membership authority, so raw credentials, tenant scope, retries, reissue, and
acceptance must not be implemented as loosely coupled handler operations.

Invitation URLs containing bearer material would leak through browser history,
referrers, screenshots, proxies, and access logs. Storing a recoverable raw
credential would also turn a database or backup disclosure into an immediate
membership compromise.

## Decision

Invitation management is available only to a verified human session. Every
request names one exact workspace scope and, for direct-project invitations,
one canonical project in that workspace. The durable identity store rechecks
current membership authority inside the same transaction that changes
invitation state. Project editors may manage direct-project invitations but
cannot grant or reissue an owner role.

Midnight returns a `midn_inv_*` bearer credential only from create or reissue,
or from an exact encrypted idempotent replay. Durable storage contains a
domain-separated HMAC verifier and a display-safe prefix, never the raw token.
The verifier key is independently installed at
`/etc/midnight/invitation.key`.

Delivery is explicitly manual. The API, CLI, and GUI do not send email and do
not construct a URL containing the credential. The GUI holds a returned
credential only in component memory until its one-time reveal is closed.

Create, reissue, revoke, and accept require an idempotency key. PostgreSQL
coordinates current-session validation, current scope authority, invitation or
membership state, encrypted replay completion, and audit append in one
serializable transaction. Acceptance derives the recipient email from the
authenticated durable identity; request JSON cannot choose it.

Reissue atomically revokes the old credential and inserts a new invitation that
inherits its email, role, and exact scope. Migration 0043 records a unique
`replaces_invitation_id` self-reference so an old invitation cannot branch
into multiple replacements. The down migration refuses to erase replacement
history.

## Consequences

- A copied credential must be delivered through an operator-chosen secure
  channel; Midnight currently provides no delivery receipt or email retry.
- Losing the one-time credential requires reissue. Listing returns lifecycle
  metadata only.
- Old credentials become invalid in the same transaction that creates their
  replacement.
- Invitation acceptance cannot be used to change the recipient email or cross
  a tenant boundary.
- Account recovery, verified email delivery, and acceptance by unauthenticated
  users remain separate, unimplemented capabilities.
