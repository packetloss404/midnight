# ADR-0024: AI agent chat backend (conversation persistence + SSE)

- Status: proposed
- Date: 2026-05-17
- Owners: chat-backend track

## Context

Midnight is adding an in-product Claude chat panel that lets operators
ask the agent to explain failures, edit configuration, or run safe
remediation tool calls. Three concurrent tracks split the work:

1. **Chat backend** (this ADR): conversation + message persistence,
   REST endpoints, SSE streaming.
2. **Agent core**: Anthropic SDK integration, tool loop, MCP server,
   auto-diagnosis hooks.
3. **Frontend**: the `web-next/` panel that consumes REST + SSE.

This ADR records the contracts the chat backend owns and exposes to
the other two tracks.

## Decision

### Storage

Two new tables, owned by `migrations/0010_agent_conversations.up.sql`.

- `agent_conversations`: ULID-prefixed `conv_*` rows holding
  `(id, project_id?, service_id?, actor_id, title, model, created_at,
   updated_at, metadata jsonb)`. `metadata` is intentionally schemaless
  so additions (system preamble, panel pinning, tool budget caps) ride
  through without migrations.
- `agent_messages`: ULID-prefixed `msg_*` append-only rows holding
  `(id, conversation_id, seq, role, content jsonb, usage jsonb?,
   created_at)`. `seq` is monotonic per-conversation and the
  `(conversation_id, seq)` unique constraint is the canonical
  ordering. `content` is an array of content blocks with the
  discriminator union `text | tool_use | tool_result`.

A `pg_advisory_xact_lock` keyed on
`hashtext('midnight_agent_conv:<conv_id>')` serialises appends to a
single conversation so concurrent writers cannot interleave seq
values. Cross-conversation appends are independent.

### Message content-block JSON shape

Every `content` value is an array of blocks. Each block has a `type`
discriminator and the fields applicable to that type:

```jsonc
[
  { "type": "text",        "text": "what does the deploy log say?" },
  { "type": "tool_use",    "tool_use_id": "tu_01", "tool_name": "fetch_logs",
                            "tool_input": {"service":"svc_demo","lines":50} },
  { "type": "tool_result", "tool_use_id": "tu_01",
                            "tool_output": {"text":"..."},
                            "tool_error": "" }
]
```

Unused fields are omitted on the wire (`omitempty` in Go). The
discriminator is closed: new block types require an ADR amendment.

### REST surface

Every route under `/api/v1alpha/agent/conversations` requires the
bearer token configured at `DataStack.APIBearerToken`. Production derives
conversation ownership and cursor scope from the authenticated request
context; caller-supplied actor headers are ignored. Explicit test/embedder
stacks may provide a custom `AgentPrincipalFromRequest` extractor.

| Method | Path                                               | Notes                                                                                    |
| ------ | -------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| POST   | `/agent/conversations`                             | Create a conversation. Supports `Idempotency-Key`.                                       |
| GET    | `/agent/conversations`                             | List, filtered by `project_id` / `service_id` / `actor_id`. Cursor-paginated.            |
| GET    | `/agent/conversations/{id}`                        | Full record.                                                                             |
| DELETE | `/agent/conversations/{id}`                        | Idempotent; missing rows return 204.                                                     |
| GET    | `/agent/conversations/{id}/messages`               | Paginated history. `order=desc` for newest first.                                        |
| POST   | `/agent/conversations/{id}/messages`               | Append user message; returns `user_message`, `assistant_message`, `stop_reason`, `stream_url`. |
| GET    | `/agent/conversations/{id}/stream`                 | SSE token-level deltas; heartbeat every 15s.                                             |

### SSE endpoint contract

```
GET /api/v1alpha/agent/conversations/{id}/stream
Accept: text/event-stream

HTTP/1.1 200 OK
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no

event: delta
data: {"type":"delta","delta":"Hello"}

event: tool_use
data: {"type":"tool_use","block":{"type":"tool_use","tool_use_id":"tu_1","tool_name":"...","tool_input":{}}}

event: tool_result
data: {"type":"tool_result","block":{"type":"tool_result","tool_use_id":"tu_1","tool_output":{}}}

event: done
data: {"type":"done","message":{...},"usage":{...},"stop_reason":"end_turn"}

:heartbeat
```

Event types are `delta`, `tool_use`, `tool_result`, `done`, `error`.
Heartbeats are `:heartbeat` SSE comments emitted every 15s so proxies
do not idle-out the stream. The stream closes after `done` or `error`.

### GraphQL contract

`schemas/graphql/schema.graphql` and `internal/graphql/schema.graphql`
introduce `Conversation`, `Message`, `MessageContent`, `Usage`,
`StreamEvent`, and the four operations:

- `query conversation(id)`
- `query conversations(input)`
- `mutation conversationStart(input)`
- `mutation conversationSend(input)` — returns the assistant message
  synchronously; **clients still subscribe to the REST SSE endpoint
  for live token deltas**. Subscriptions are out of scope for the
  alpha API.
- `mutation conversationDelete(id)`

The gateway track wires resolver implementations onto
`internal/graphql.ConversationResolverDelegates`.

### Caller seam (`internal/agent/agent.go`)

The chat backend NEVER imports the Anthropic SDK. Every model call
flows through `agent.Caller`:

```go
type Caller interface {
    Send(ctx context.Context, req CallRequest) (CallResult, error)
    Stream(ctx context.Context, req CallRequest, out chan<- StreamEvent) error
}
```

`CallRequest.Conversation` is the full history including the latest
user message. `Stream` MUST close `out` before returning and MUST emit
a terminal `done` or `error` event.

### `ProjectScopeAuthorizer`

```go
type ProjectScopeAuthorizer interface {
    Authorize(ctx context.Context, scope Scope) error
}
```

When `DataStack.ConversationScopeAuthorizer` is `nil` (dev mode),
every authenticated caller may see every conversation. Production
wiring MUST install an authorizer that maps the principal to its
project memberships and RBAC role bindings before shipping
multi-tenant chat. The signature is:

```go
type Scope struct {
    ConversationID string
    ProjectID      string
    ServiceID      string
    ActorID        string
    Action         string // "create" | "read" | "list" | "delete" | "send"
}
```

## Consequences

- The chat backend is unblocked even when the agent-core and frontend
  tracks have not landed. Dev mode runs against the in-memory store
  with a stub `Caller`.
- Adding a new content-block type or `StreamEvent` type is an ADR
  amendment plus a schema bump; clients can rely on the closed enum.
- Long-running tool loops do not block the REST request: the
  assistant message is persisted on `done` and any pending SSE
  subscribers receive the same events. Re-connecting clients can
  replay history through `GET .../messages`.

## Numbering

The original task brief reserved migration `0012` and
`schema_version: 12` for this slice. At the time this ADR was
written, the chat-backend worktree's base manifest only had
versions `1..9`; the concurrent tracks owning slots 10 and 11 had
not landed. To keep the manifest a strict prefix sequence (the
existing invariant in `internal/db/migrations.go`), this ADR ships
migration `0010_agent_conversations` and bumps `schema_version` to
10. The integrator may renumber to 12 at merge time when slots 10
and 11 land alongside this work; the on-disk SQL is otherwise
unchanged.

## Links

- Related panel decisions:
  - ADR-0001 (panel decisions normative)
  - ADR-0002 (name and prefixes — additions `conv_`, `msg_`)
- Related implementation files:
  - `migrations/0010_agent_conversations.up.sql`
  - `internal/agent/agent.go`
  - `internal/agent/conversation/`
  - `internal/api/agent.go`
  - `schemas/graphql/schema.graphql`
