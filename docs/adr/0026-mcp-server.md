# ADR-0026: MCP Server (Track 3)

- Status: accepted
- Date: 2026-05-17
- Owners: agent-core / MCP track

## Context

Midnight already has two consumers of the agent core (the in-product
chat backend in ADR-0024 and the build-failure diagnosis worker in
ADR-0025). Track 3 of the agent surface is the **external MCP server**
that lets third-party clients — Claude Code, Claude Desktop, custom
agents — drive the Midnight control plane through the same tool
surface the in-product chat uses.

Requirements from the platform plan:

- Speak the Model Context Protocol over a transport external clients
  can already reach (HTTP, not stdio — operator-self-hosted Midnight
  runs as a long-lived service behind a load balancer).
- Bridge the canonical `*tools.Registry` defined in ADR-0023 *without
  duplicating tool definitions*. The 11 platform tools are owned by
  `internal/agent/tools`; the MCP wire is just another consumer.
- Honor the same mutation-gate semantics the chat backend enforces
  (per-session opt-in; mutating tools are filtered out of `tools/list`
  when the gate is closed so the model never sees them).
- Per-session rate limit so a misbehaving client can't exhaust the
  control plane.
- Discoverable via a well-known manifest so clients can configure
  themselves with a single URL.

A prior attempt landed with a parallel `tools.Registry` shape that
diverged from the canonical one shipped on `main`. This ADR captures
the *clean* design against the actual registry.

## Decision

### Transport: streamable-HTTP

The MCP server is mounted on a single HTTP path (`/mcp/v1`) and speaks
JSON-RPC 2.0. Each POST carries one frame (or a batch); the response
is either `application/json` (single frame) or — when a future
progress-reporting tool lands — `text/event-stream` for SSE. The
current 11-tool surface returns quickly enough that the JSON path is
the default.

Stdio mode (for local clients like Claude Desktop talking to a Midnight
binary on the same machine) is left to a follow-up. The shared wire
core can be reused — only the transport adapter is missing.

### Bridge, don't duplicate

`internal/agent/mcp` consumes `*tools.Registry` through a narrow
internal `registryView` interface declared inside the package. The
interface satisfies `*tools.Registry` directly; we never add a
parallel registry type. `tools/list` walks `Registry.Names()`, calls
`Registry.Get(name)`, and emits one MCP tool record per result.
`tools/call` unmarshals `params.arguments` into a `json.RawMessage`
and calls `Registry.Execute(ctx, name, raw, env)`. The return is
JSON-encoded and wrapped in a single `text` content block per the MCP
result envelope.

The Anthropic SDK's tool-shape stays inside `internal/agent/tools`.
The MCP wire shape (which has a slightly different annotation surface
— `destructiveHint`, `readOnlyHint`) stays inside `internal/agent/mcp`.

### JSON-RPC error mapping

| Failure | Code | Notes |
|---|---|---|
| Method not registered | `-32601` | MCP `tools/call` for unknown name; also unknown JSON-RPC method |
| Mutating tool refused | `-32030` | `MIDNIGHT_MUTATION_DISALLOWED` — sentinel string is also in the error message so non-Go clients can match it |
| Arguments JSON malformed | `-32602` | Surfaces `tools/call` param errors |
| Tool execute failure | (none) | Result envelope `isError=true` with the error message in a text block, per MCP spec — JSON-RPC layer reports success |
| Rate limit hit | `-32029` | Per-session token bucket; reset window encoded in error data |
| Generic internal failure | `-32603` | Tool result not JSON-encodable, etc. |
| Parse error | `-32700` | Malformed JSON body |
| Invalid request | `-32600` | Missing `jsonrpc: "2.0"`, or `tools/list` before `initialize` |
| Bearer missing/invalid | HTTP 401 | Runs before JSON-RPC parse so hostile callers can't tickle the parser |

### Mutation gate

Two doors must both say yes for a mutating tool to run:

1. **Session flag.** Set by the `X-Midnight-Allow-Mutations: true`
   header on the request that carries `initialize` *or* by
   `initialize.params._meta.allow_mutations`. Both are accepted; the
   header is the documented default for clients (matches the
   `claude mcp add ... --header` pattern). The flag is per-connection
   and persists for the lifetime of the session.
2. **Registry check.** `tools.Registry.Execute` re-asserts
   `env.AllowMutations` against `t.Mutates` and refuses with
   `agent.ErrMutationsDisabled` if the session somehow forged its
   way past the wire layer.

`tools/list` filters by `t.Mutates && !sess.AllowMutations` so the
model never even sees the mutating tools when the gate is closed.

### Session state

Each connection is identified by a sortable id (we use `rs/xid` —
12-byte, monotonically increasing, base32). MCP clients send the id
back on every subsequent request in the `Mcp-Session-Id` header; the
server echoes the id on the response so clients picking up a fresh
session can capture it. Sessions die when the in-memory map is GC'd
(process restart); v1 has no Postgres-backed session store.

### Rate limit

A simple per-session token bucket: `capacity` tokens, refilled to
`capacity` every minute. One `tools/call` consumes one token; the
discovery surface (`tools/list`, `resources/list`, the manifest) does
not. Default `capacity = 60`; operators override via
`MIDNIGHT_MCP_RATE_LIMIT_PER_MINUTE` or the
`Options.RateLimitPerMinute` constructor field.

### Manifest

`/.well-known/mcp` serves a JSON document with the server name,
version, supported protocol versions, transport URLs, auth scheme,
the per-server mutation-gate metadata (key + refusal error code),
the rate-limit cap, and a docs URL. The endpoint is unauthenticated
on purpose — discovery must work before the client has a token.

## Consequences

- Adding a new tool only requires registering it on
  `tools.NewDefaultRegistry()`. The MCP server picks it up
  automatically. This is the same pull-on-startup contract the chat
  backend honors.
- A bug in the registry's mutation gate would surface on the MCP
  surface immediately. The defense-in-depth check inside the MCP
  package matches the registry's check, so we'd refuse twice — but
  both have to be present.
- A future stdio transport reuses everything except the HTTP
  envelope; the JSON-RPC dispatcher in `server.go` is transport-
  agnostic.
- The hand-rolled JSON-RPC is small (one dispatch function, ~50 SLOC)
  and keeps us off the `github.com/modelcontextprotocol/go-sdk`
  dependency until we need its richer features (progress
  notifications, completions). The SDK can be reintroduced behind
  the same `Options` surface if those land.

## Non-goals

- No Postgres-backed session store. v1 sessions are in-memory.
- No subscription / `resources/updated` notifications. The resources
  capability is read-only.
- No completions, sampling, or roots — these are MCP features Midnight
  doesn't need yet.
- No OAuth / token-refresh handshake; the existing operator bearer is
  the single credential.
