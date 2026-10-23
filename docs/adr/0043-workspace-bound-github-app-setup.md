# ADR-0043: Bind GitHub App setup to workspace authority and GitHub proof

- Status: Accepted
- Date: 2026-08-30

## Context

ADR 0038 made source links tenant-safe, but assigning an App installation still
required an installation recovery credential. GitHub warns that an App setup
URL's `installation_id` parameter can be spoofed. A product setup flow therefore
cannot treat callback query values, GitHub account names, or browser-supplied
workspace identity as authority.

The callback cannot carry the initiating Midnight bearer token. GitHub does,
however, support state on the installation URL, user OAuth during installation,
an endpoint that proves the authorized user can access an installation, and App
endpoints that resolve canonical installation and repository metadata.

## Decision

Allow only a current workspace administrator human session to initiate setup.
Issue opaque HMAC-SHA-256 state with a random 256-bit nonce, issued time, and
ten-minute expiry. Persist only the nonce digest plus its workspace, user, and
session bindings, so no internal tenant identity crosses the browser/GitHub
boundary. The state is consumed once; expiry is capped at one hour in both the
codec and database constraint.

At callback, reject unknown or repeated query fields and never accept owner,
account, repository, or workspace values. Before external calls, verify that
the signed state exists, remains unused, and still names an active administrator
session. Exchange GitHub's one-time code for an in-memory user access token and
use it to prove access to the exact installation. Then use App authentication to
resolve the canonical installation and exhaustively paginate its repository
grant set.

Under one serializable transaction and locked one-time state, recheck session
authority, bind the installation, replace its repository grants, unlink sources
whose grant disappeared, consume state, and append audit evidence. Never persist
OAuth codes, user access tokens, signed state, client secrets, or App private
keys. Keep manual recovery assignment as a break-glass compatibility surface.

## Consequences

- A callback installation ID is only a lookup coordinate; GitHub user proof and
  App resolution establish its identity and membership.
- Revoking the initiating Midnight session or administrator membership before
  commit prevents setup.
- Replays stop at the durable state fence before contacting GitHub.
- Repository selections above one API page are synchronized without silent
  truncation, with an explicit 10,000-repository safety bound.
- App setup requires a matching HTTPS setup/OAuth callback URL and GitHub's
  request-user-authorization-on-install option.
- Credential recovery can still repair an installation when browser OAuth is
  unavailable, but it is not the ordinary tenant workflow.
