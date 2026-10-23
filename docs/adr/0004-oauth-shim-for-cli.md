# ADR-0004: Thin OAuth shim for `midnight login`

- Status: superseded for human login; retained as operator recovery compatibility
- Date: 2026-05-17
- Owners: CLI Lane

## Context

The `midnight` developer CLI ships a Railway-shaped `midnight login`
command. Railway's flow is a standard OAuth 2.0 PKCE round-trip:

1. The CLI opens the operator's browser to an `authorize` URL.
2. The browser comes back to a local loopback listener with an
   authorization code.
3. The CLI exchanges code + PKCE verifier for an access token at the
   `token` endpoint.

Midnight is a self-hosted, single-tenant system. There is no user store
yet — the only identity that exists today is the operator-level bearer
token Midnight reads from the `MIDNIGHT_API_BEARER_TOKEN` env var. We
cannot ship a real multi-user OAuth surface until accounts land, but we
also do not want the CLI to grow a Midnight-specific bespoke login
protocol that we would later have to migrate away from.

## Decision

Ship a **thin OAuth shim** around the existing operator bearer token. The
shim implements the wire shape of an OAuth 2.0 PKCE server but cuts every
multi-user concern:

- `GET /auth/oauth/authorize` validates the client id (only
  `midnight-cli`), validates a PKCE `code_challenge` (S256 only), and
  renders a tiny static HTML consent page when the request rides an
  operator bearer (via `Authorization` header or a password form body;
  URL parameters and browser cookies are deliberately rejected). On consent, the
  endpoint mints a single-use authorization code, expires it in 5
  minutes, and redirects to the caller's `redirect_uri` with `code` +
  `state`.
- `POST /auth/oauth/token` consumes the code, verifies the PKCE
  `code_verifier` against the stored `code_challenge`, confirms the
  `redirect_uri` round-trips, and returns the **existing operator bearer
  token** as the `access_token`. `token_type` is `Bearer`, `expires_in`
  is `0` (the operator token has no expiry).
- The authorization-code store is in-memory. Crashing the API process
  loses any in-flight codes; the operator just re-runs `midnight login`.

The CLI side runs a loopback listener on `127.0.0.1:0`, generates a fresh
PKCE verifier + state, sends the operator to the authorize URL, receives
the callback, exchanges the code, and writes the access token to
`~/.midnight/config.json` keyed by API base URL.

## Consequences

- The CLI's login UX is identical to Railway's from the operator's
  perspective: browser opens, authorize page appears, terminal reports
  "logged in".
- Operators must still bootstrap the operator bearer token out of band
  (`MIDNIGHT_TOKEN`; the first human administrator is then created with
  `midnight admin create-user`). The shim does not
  bypass that step; it only gates how the token reaches the developer's
  laptop.
- The shim is **single-tenant**. Every successful login returns the same
  bearer token. Once accounts land we replace the bearer-token swap with
  a real user-token mint, behind the same wire surface.
- Codes are held in memory. Production-grade durability lands when the
  authorization-code store moves to Postgres. The wire shape does not
  change.
- We do not implement device-authorization (`urn:ietf:params:oauth:grant-type:device_code`).
  Operators who cannot run a loopback listener (e.g. a remote SSH
  session) use `midnight admin authorize-cli --browserless` and paste the
  authorize URL into a local browser; the callback URL is still a loopback
  bound on the remote machine, which works for ssh -L port-forwards.

## Supersession

Durable human accounts and revocable `midn_user_` sessions now exist.
`midnight login` therefore uses the canonical
`POST /api/v1alpha/auth/sessions` endpoint and reports the verified human
identity. This OAuth shim remains mounted for compatibility under the
explicitly operator-scoped `midnight admin authorize-cli` command. Its result
is stored as an operator recovery credential, never labeled as human login,
never sent to `/api/v1alpha/auth/me`, and never removed by human logout.

## Links

- Related panel decisions: ADR-0001 (decisions are normative)
- Related work items: CLI parity epic
- Related implementation PRs: this PR
