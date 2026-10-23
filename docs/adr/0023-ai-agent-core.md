# ADR-0023: AI Agent Core

- Status: accepted
- Date: 2026-05-17
- Owners: agent-core track

## Context

Midnight is gaining an in-product AI agent (modelled on Railway Agent) that
sits next to the dashboard and CLI. It needs to:

- Drive the Midnight control plane through a defined tool set (read +
  cautiously mutate).
- Power three concurrent surfaces — interactive chat (Track 1),
  build-failure diagnosis (Track 2), and an external MCP server (Track 3)
  — without each surface reinventing the Anthropic plumbing.
- Stay safe by default: secrets do not leak, mutations do not happen
  without explicit operator opt-in, and the agent fails closed when its
  credentials are missing rather than silently doing nothing useful.
- Be testable without live API calls.

The work in this ADR is the **core** layer: the Anthropic SDK seam, the
tool registry, the platform system prompt, and the cost-telemetry hook.
The chat HTTP endpoint, conversation persistence, frontend, MCP server,
and auto-diagnosis worker each live in their own ADR.

## Decision

### Model choice

| Surface | Default model | Override env var |
|---|---|---|
| Chat | `claude-sonnet-4-6` | `MIDNIGHT_AGENT_CHAT_MODEL` |
| Diagnosis | `claude-opus-4-7` | `MIDNIGHT_AGENT_DIAGNOSIS_MODEL` |

Sonnet 4.6 is fast, cheap, and competent at tool-driven workflows — the
dominant chat shape. Opus 4.7 is the heavier model for the (lower-volume,
higher-stakes) auto-diagnosis path, which gets one shot at a multi-step
analysis when a deploy fails and wants the best inference available.

Both models are pinned by name only (no dated suffix), so the SDK's
forward-compatible alias resolves to the latest version of each train.
The override env vars exist so an operator can dial down to Haiku on
cost-sensitive deployments or pin a dated model for reproducibility.

### SDK version

`github.com/anthropics/anthropic-sdk-go` v1.43.0. Pinned via `go.mod`.

### Caching strategy

Prompt caching is **non-negotiable for cost control**. The agent's
per-turn input is dominated by a static prefix (platform system prompt +
tool definitions, ~3–6 KB after JSON serialization). Reading that prefix
at 1/10 cost across the typical 4–10 turn conversation is the difference
between "useful product" and "expensive curiosity".

The implementation places `cache_control: {"type": "ephemeral", "ttl": "5m"}`
breakpoints on:

1. The **platform system prompt** block (always cached).
2. The **op-supplied SystemPreamble** block when present (cached
   separately so the platform cache hits regardless of preamble churn).
3. The **last tool definition** in the rendered tool array (the SDK
   forms the cache prefix from blocks up to and including this marker;
   tools render in a stable alphabetical order so the cache key stays
   stable across calls).

The op-supplied SystemPreamble is a separate cache breakpoint so chat
sessions with stable system context get a cleaner cache shape than
sessions that rotate preamble text.

### Tool loop bounds

- Default max iterations: 10 (override via `CallRequest.MaxIterations`).
- Default deadline: 60 seconds wall-clock across the whole loop
  (override via `CallRequest.Deadline`; the caller's context still wins
  if it expires sooner).
- Default `max_tokens`: 4096 (override via `CallRequest.MaxTokens`).
- Tools execute **in parallel** within an iteration. Tool errors do not
  abort the loop — they are surfaced to Claude as `is_error: true`
  tool_results so the model can recover. Hitting the iteration cap
  returns `agent.ErrMaxIterations` to the caller.

### Cost telemetry

Every `Caller.Send` and `Caller.Stream` emits exactly one `agent.Usage`
event via the injectable `MetricsEmitter` interface:

```
{
  model: string,
  input_tokens: int64,
  output_tokens: int64,
  cache_creation_tokens: int64,
  cache_read_tokens: int64,
  cost_usd_estimated: float64,
  iteration_count: int
}
```

The default emitter writes a structured line to the stdlib logger.
Production replaces it with a Prometheus / metering-pipe emitter via
`Options.Metrics`. Cost estimation uses public list prices for the
Sonnet/Opus 4.x family; the emitter is the authoritative billing source
in production.

### Failure-closed semantics

`NewCaller` returns the typed sentinel `agent.AgentAPIKeyMissing` when
`ANTHROPIC_API_KEY` is unset and no explicit key was supplied. There is
no silent degradation, no offline mode, no canned response. The chat
backend uses this sentinel to render a clear operator message ("the
agent is not configured") in the UI.

### Tool surface

The `internal/agent/tools` package ships one file per tool. The default
registry contains 11 tools, alphabetically:

| Tool | Description |
|---|---|
| `deploy_from_source` | Kick off a new build for a service from a repo + git ref. MUTATES. |
| `get_deployment` | Fetch a single deployment by id (status, route, commit, logs head). |
| `get_logs` | Fetch deployment logs with optional substring filter and tail size. |
| `get_metrics` | Fetch CPU / memory / restart counters; friendly stub when not wired. |
| `get_service` | Fetch a service by id; variable values redacted. |
| `list_projects` | List projects visible to the caller. |
| `list_services` | List services inside a project (metadata only). |
| `redeploy` | Trigger a new deployment of a service from its current source. MUTATES. |
| `rollback` | Roll back to a prior successful deployment. MUTATES. |
| `set_variable` | Set an environment variable on a service. MUTATES. |
| `unset_variable` | Remove an environment variable from a service. MUTATES. |

Each tool's `Execute`:

- Validates its input envelope (required fields, format).
- Calls the appropriate `Env.*` interface (so tests inject fakes).
- Returns a JSON-encodable value or an error. The registry converts
  errors into `is_error: true` tool_results.

Mutating tools are gated **twice**:

1. The registry refuses to execute them when `Env.AllowMutations` is
   false (returns `agent.ErrMutationsDisabled`).
2. The Anthropic tool list strips them out entirely when
   `Env.AllowMutations` is false, so Claude doesn't even see the option
   in its first turn.

`set_variable` / `unset_variable` additionally refuse names starting
with `MIDNIGHT_` unless `Env.AllowBuiltinVar` is set — the
"--allow-builtin-override" semantics from the CLI.

### Audit

Every tool invocation (including refused mutations) lands an
`agent.AuditEntry` through `Env.Audit.Record`. The entry carries:

```
ts, actor, tool, arguments, project_id, service_id, outcome ("ok" | "error" | "refused"), error
```

Audit failures do not mask tool failures — when the tool itself failed,
the runErr wins; when only the audit emit failed, the registry returns a
wrapped error. This is the boundary where the chain-of-custody package
(`internal/audit`) will plug in for production deployments.

### Human-in-the-loop hook (future)

Mutations today are gated by a static `Env.AllowMutations` flag set by
the caller. The plan for HITL approval is:

1. Add `Env.ConfirmMutation func(ctx, tool, args) error` (returns
   `ErrMutationDeclined` or nil).
2. The registry, when a mutating tool is executed and ConfirmMutation
   is non-nil, calls it AFTER validation but BEFORE the tool's Execute.
   The result is recorded in the audit row's `outcome`.
3. The chat backend (Track 1) wires ConfirmMutation to its SSE channel
   so the UI can surface "Claude wants to set FOO on service X — allow?"
   and resolve the promise on click.
4. The MCP server (Track 3) wires ConfirmMutation to its protocol-level
   approval flow.

The hook deliberately lives at the registry boundary rather than inside
each tool: it gives one place to enforce HITL across every current and
future mutating tool, and it stays opt-in for headless callers like the
diagnosis worker (which only registers read-only tools anyway).

The scope of this ADR is to define the seam; the actual ConfirmMutation
plumbing is left to the chat backend track because the UX shape is owned
there.

## Consequences

- All Midnight tracks that want to talk to Claude depend on the
  `agent.Caller` interface — never on `github.com/anthropics/anthropic-sdk-go`
  directly. Only `internal/agent/claude` imports the SDK. Lint can
  enforce this with a `tools/lint` rule in a follow-up.
- The tool list is the **only** way the agent reads or changes platform
  state. The seam intentionally has no escape hatch (no bash tool, no
  text-editor tool, no MCP server tool from the SDK itself). Expanding
  the surface means adding a tool here, with audit + mutation gating
  built in.
- Prompt-cache hit rate is now a first-class operational metric. Cache
  invalidation events (rotating the SystemPreamble, churning the tool
  list) become observable through `cache_creation_tokens` spikes.
- Tests run without live API calls: a fake `option.HTTPClient` returns
  canned message JSON, asserting that `cache_control` is on the system +
  tools blocks, that the tool loop closes cleanly, that the iteration
  cap fires, and that `AgentAPIKeyMissing` fires when no key is set.

## Links

- Panel decisions: TBD (cross-track ratification)
- Related work items: agent track 1 (chat backend), track 2 (diagnosis),
  track 3 (MCP server)
- Related implementation PRs: this PR introduces `internal/agent/{...}`
