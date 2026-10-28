# Midnight web console

Vite, React, and TypeScript application for Midnight's browser console. It
contains authentication, the project portfolio and topology workspace,
service operations and configuration, installation settings, usage evidence,
the in-app documentation, and the optional agent panel.

## Scripts

```
npm run dev         # vite dev server (proxies /api and /graphql → http://localhost:8080)
npm run build       # production build
npm run typecheck   # tsc --noEmit
npm run test        # vitest
npm run test:e2e    # Playwright browser tests
npm run test:gate0  # typecheck, unit tests, build, and browser tests
```

The default browser suite starts a throwaway Go memory API on
`127.0.0.1:4180` and Vite on `127.0.0.1:4173`; both ports must be free. Install
Chromium with `npm run test:e2e:install` first. `MIDNIGHT_API_PROXY_TARGET`
selects the upstream API during ordinary Vite development.

`e2e/real-control-plane.e2e.ts` uses actual HTTP requests without request
interception to create projects/services, reload their state, navigate mobile
grid/map/inspector views, report unavailable image intake through real GraphQL
and retry without creating duplicate services, and verify documentation and
the workspace sign-in boundary. The memory API has no deploy queue, so these
tests assert a typed failure and zero deployment records. Memory
data disappears with the test API; durable sessions, Postgres transactions,
public HTTPS, and backup/restore still require installed-stack acceptance.

Most other browser files use the deterministic API fixture in
`e2e/support/app.ts` for repeatable interaction, accessibility, and responsive
checks. They do not prove live backend behavior. `e2e/live-api.e2e.ts` is an
additional seeded real-API flow run by `scripts/parity-ui-acceptance.sh`;
security-header checks run against the Go-served built console with
`scripts/security-headers-browser-test.sh`. Those two files skip in the default
run unless their explicit environment inputs are present. Setting
`MIDNIGHT_E2E_BASE_URL` bypasses local server startup and skips the local
mutation suite to avoid changing an operator's external installation.

Run `npm run test:e2e:postgres` on Linux/macOS for the separate production-storage
browser lane. Its serial Chromium journeys use the real Go API and PostgreSQL:

- First-administrator password/TOTP setup, project/service creation, encrypted
  variable/runtime draft save/reload/apply, secret replacement, stale-write 412,
  logout revocation, and a new MFA login.
- Invitation acceptance by an existing recipient, email mismatch and reuse
  refusals, viewer write denial, editor role change, membership removal,
  invitation reissue, and conditional revocation.
- Automation token creation, replacement by a new credential, restricted
  project admission, forbidden administrative writes, metadata-only reload,
  revocation that makes the old bearer return 401, and stale confirmation 412.
- Disposable administrator password recovery preserving the authenticator,
  invalidation of all previous sessions, old-password rejection, and required
  MFA with the replacement password.
- New-user enrollment through `/invite`: one invitation creates an ordinary
  account and its exact grant, then uses normal login. Replay and revoked
  credentials fail; an existing account must sign in and keeps its password.
- Tenant chat privacy and scoped read/write tools with viewer/editor roles,
  explicit mutation consent, and revocation. A deterministic local HTTP model
  supplies tool requests; the browser, API, tool execution, and storage are real.
- A late failure diagnosis produced by the actual diagnosis runner, provider
  HTTP adapter, conversation adapter, and PostgreSQL event store. Viewers read
  the report without a composer; foreign access and writes fail, and revocation
  hides a visible report. The failure and local model are controlled fixtures.

No browser API requests are intercepted. The original existing-recipient
journey uses clearly labeled database identities with no initial memberships.
The new enrollment, chat, and diagnosis journeys create recipients through the
public invitation-bound product API; the enrollment journey also drives the
new-user UI. No route permits unrestricted signup or replacement of an
existing password. Ordinary accounts follow the existing password-only policy;
first-administrator MFA remains required. Invitation delivery is manual and no
email service is exercised. The onboarding endpoint has no replay cache:
after a lost creation response, sign in with the new account's password.

There is no deployment worker in this lane. The automation test checks HTTP 202, a readable
linked operation, and exactly one queued durable deployment. Applying without
deploying and queued admission do not prove image pulls, runtime secret
delivery, Apply & Deploy completion, or serving application traffic.
The diagnosis producer creates a controlled failure through the actual stores
and runs the real report producer separately. It does not prove an installed
worker caused that failure or that an external model produced the report.

The harness starts its own disposable Postgres 16 container when Docker is
available, or accepts `MIDNIGHT_PG_E2E_DSN` for a fresh database on loopback named
`midnight_browser_e2e`. It refuses other database names/hosts. The API migrates
that database with random test keys and a temporary recovery credential;
the harness removes its generated keys and only the container it created.
Supplied databases are not reset or removed. Ports 4182 and 4183 must be free.
One-time credentials stay in browser/worker memory and the run-owned state file
has mode 0600. Traces, screenshots, and failure-time page snapshots are disabled
for this credential-bearing lane; diagnostics report assertions and statuses.
The lane uses production KEK file permissions and runs separately from
`test:gate0`; it requires a Linux/macOS shell, Go, Node, and Chromium.

## Agent panel

The right-rail dock (`AgentDock`) is mounted once in `AppShell`. Changing
project, service, or environment clears its selected conversation, draft, and
mutation consent and closes the local stream. This keeps an old conversation
from appearing under a new target. Closing the stream does not cancel a
server-side turn already submitted. Conversations carry explicit environment
scope, and mutation consent remains subject to current server authorization.
Opening chat from a service hides its inspector while preserving that service's
URL and context. Closing chat restores the inspector for the same service.
Expanded width is 440 px; the collapsed control is a floating button.

### Local mock backend

Set `VITE_AGENT_MOCK=1` to route agent calls to the in-memory development
adapter in `src/api/agent-mock.ts`. It emits deterministic SSE chunks for local
UI work and is never enabled by default.

```bash
VITE_AGENT_MOCK=1 npm run dev
```

Normal development and production builds use `/api/v1alpha/agent/*`.
