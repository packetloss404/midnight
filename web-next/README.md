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

No API requests are intercepted. Invitee accounts are explicit database
fixtures with real password hashes and no initial memberships; these tests
do **not** prove account enrollment or invitation email delivery. The existing
account can accept a manually delivered credential from its project index.
Only the first administrator uses the product's one-shot account setup flow.

There is no worker in this lane. The automation test checks HTTP 202, a readable
linked operation, and exactly one queued durable deployment. Applying without
deploying and queued admission do not prove image pulls, runtime secret
delivery, Apply & Deploy completion, or serving application traffic.

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

The right-rail dock (`AgentDock`) is mounted once in `AppShell` and persists
across navigation, so an in-flight SSE stream is **not** aborted when the user
switches pages. Expanded width is 440 px; collapsed strip is 48 px.

### Local mock backend

Set `VITE_AGENT_MOCK=1` to route agent calls to the in-memory development
adapter in `src/api/agent-mock.ts`. It emits deterministic SSE chunks for local
UI work and is never enabled by default.

```bash
VITE_AGENT_MOCK=1 npm run dev
```

Normal development and production builds use `/api/v1alpha/agent/*`.
