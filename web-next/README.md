# Midnight web console

Vite, React, and TypeScript application for Midnight's browser console. It
contains authentication, the project portfolio and topology workspace,
service operations and configuration, installation settings, usage evidence,
the in-app documentation, and the optional agent panel.

## Scripts

```
npm run dev         # vite dev server (proxies /api → http://localhost:8080)
npm run build       # production build
npm run typecheck   # tsc --noEmit
npm run test        # vitest
npm run test:e2e    # Playwright browser tests
npm run test:gate0  # typecheck, unit tests, build, and browser tests
```

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
