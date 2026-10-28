# Agent runtime environment

> **Status (as of this commit):** chat, the tool registry, MCP (`/mcp/v1`), per-tool audit, and spend caps are wired into `midnight-server`. Tenant chat uses canonical project, service and environment authorization. Opt-in failure diagnosis runs in `midnight-worker`, persists a scoped report, and attaches its ID through a durable operation event. The deployment detail panel reads that event and displays the report. Local tests use a controlled upstream model; real paid-provider execution still requires [external acceptance](remaining-e2e-acceptance.md). The agent can submit [bounded inline source files](agent-source-deployment.md) through the durable source pipeline.

Chat and MCP are wired at `midnight-server serve` bootstrap. The chat panel
requires a selected provider key, a supported model/endpoint, and an authorized
human session. The defaults target a single trusted installation.

Ordinary tenant chats require an explicit authorized project and remain private
to their creator. Service and environment selections must belong to that project.
Viewers may use capability-authorized read tools. Editors and owners may request
mutations with explicit per-message consent, the global mutation brake and the
same transactional tenant coordinators used by the product APIs. Sessions,
membership and environment restrictions are checked again for every access and
tool dispatch; streams are revalidated while open. Tool targets cannot escape
the conversation scope. Automation credentials cannot use human chat.

Canonical worker diagnosis reports are shared read-only with project members
who currently have log-read permission. A report must match an actual failed
deployment and its durable attachment event. Human clients cannot forge reserved
system metadata or send/delete these reports. Installation administrators and
operator recovery retain their broader trusted authority; recovery credentials
still cannot write tenant variables or submit agent source builds. Use an
isolated QA installation for live paid-provider acceptance.

The worker resolves diagnosis context from the failed operation's unique
canonical deployment, reads bounded redacted build/runtime log tails, and sends
no tool registry or mutation capability. It saves the report before appending
its operation attachment event. Replayed events with an existing attachment
avoid another provider call. Simultaneous workers before the first attachment
can still generate duplicate reports; this is not an exactly-once guarantee.

When the selected provider key is unset the server still boots; only the
chat-send path returns a clear "caller is not configured" error. Every other
surface (REST CRUD, audit, deploys, etc.) stays available.

## Environment variables

| Variable | Default | Purpose |
| -------- | ------- | ------- |
| `MIDNIGHT_AGENT_PROVIDER` | `anthropic` | Chat provider. Supported values: `anthropic` (or `claude`), `openai`, `minimax`. |
| `ANTHROPIC_API_KEY` | _unset_ | Required when `MIDNIGHT_AGENT_PROVIDER=anthropic`. Passed through to the Anthropic SDK. Unset = chat panel returns `caller is not configured`; a warning fires once at boot. |
| `MIDNIGHT_AGENT_CHAT_MODEL` | `claude-opus-5` | Anthropic model id used for the in-product chat panel. |
| `MIDNIGHT_AGENT_THINKING` | `adaptive` | Extended-thinking mode. `adaptive` (also `on`/`true`) lets the model decide when and how deeply to reason; `off` (also `disabled`/`0`/`false`) turns it off. Silently ignored on models that predate adaptive thinking (Haiku 4.5 and older), which run without it rather than erroring. |
| `MIDNIGHT_AGENT_EFFORT` | `high` | Thinking-depth / token-spend knob (`output_config.effort`): `low`, `medium`, `high`, `xhigh`, `max`. `xhigh` is clamped to `high` on models that predate it; the whole parameter is dropped on models that do not accept it. An unrecognized value logs once and falls back to `high`. |
| `MIDNIGHT_AGENT_MAX_TOKENS` | _unset_ | Overrides the generation cap. When unset, non-streaming calls use 16,000 and streaming calls 64,000 tokens. Always clamped to the target model's output ceiling. |
| `OPENAI_API_KEY` | _unset_ | Required when `MIDNIGHT_AGENT_PROVIDER=openai`. Used by the OpenAI Responses API caller. |
| `MIDNIGHT_OPENAI_CHAT_MODEL` | `gpt-5.1` | OpenAI model id for the chat panel. |
| `MIDNIGHT_OPENAI_BASE_URL` | `https://api.openai.com/v1` | Optional OpenAI-compatible base URL override for the Responses API caller. |
| `MINIMAX_API_KEY` | _unset_ | Required when `MIDNIGHT_AGENT_PROVIDER=minimax`. Used with MiniMax's Anthropic-compatible endpoint. |
| `MIDNIGHT_MINIMAX_CHAT_MODEL` | `MiniMax-M2.7` | MiniMax model id for the chat panel. |
| `MIDNIGHT_MINIMAX_BASE_URL` | `https://api.minimax.io/anthropic` | Optional MiniMax base URL override. |
| `MIDNIGHT_AGENT_DIAGNOSIS_ENABLED` | `false` | Turns the build-failure diagnosis worker on. Off by default: a failed build should not start spending money on a host nobody configured for it. When on, `midnight-worker` builds the diagnosis caller and conversation store at startup; if the provider key is missing it says so once on stderr and stays inert. |
| `MIDNIGHT_AGENT_DIAGNOSIS_MODEL` | provider-dependent | Explicit diagnosis-model override. Otherwise Anthropic uses its diagnosis default, while OpenAI/MiniMax use their configured chat model or provider default. The worker uses the selected `MIDNIGHT_AGENT_PROVIDER`, and stamps the same model on the persisted diagnosis conversation. |
| `MIDNIGHT_AGENT_DIAGNOSIS_RATE` | `10/5m` | Per-host token-bucket rate for diagnosis runs, as `<count>/<duration>` (e.g. `10/5m`). Malformed values fall back to the defaults. |
| `MIDNIGHT_AGENT_ALLOW_MUTATIONS` | `true` | Operator-level brake on mutating tool calls. Set to `false` (or `0` / `no` / `off`) to globally disable every state-changing tool. The per-message toggle from the chat UI is ANDed with this, and the brake is re-evaluated on every mutating dispatch, not only at boot. |
| `MIDNIGHT_AGENT_CONVERSATION_TOKEN_CAP` | `200000` | Per-conversation total token cap. Once exceeded, further turns on that conversation are refused with HTTP 429 before the model is called. |
| `MIDNIGHT_AGENT_CONVERSATION_COST_CENTS` | `500` ($5.00) | Per-conversation cost cap in USD cents. Same refusal behaviour as the token cap. |
| `MIDNIGHT_AGENT_HOST_DAILY_COST_CENTS` | `5000` ($50.00) | Per-host daily cost cap in USD cents, across every conversation on the host. Rolls over at UTC midnight; the 429 carries `Retry-After` and a `retry_at` field. |

### Turning the agent on

The minimum to get a working chat panel with tools:

```sh
ANTHROPIC_API_KEY=sk-ant-...
```

Everything else has a working default. To also enable build-failure diagnosis on the worker:

```sh
ANTHROPIC_API_KEY=sk-ant-...
MIDNIGHT_AGENT_DIAGNOSIS_ENABLED=true
```

To run the agent read-only (recommended while you are still building trust in it):

```sh
ANTHROPIC_API_KEY=sk-ant-...
MIDNIGHT_AGENT_ALLOW_MUTATIONS=false
```

## What the agent cannot do

These are enforced limits, not missing polish. Each one returns a typed error
the model relays in plain language rather than reporting a change that did not
happen.

| Tool | Status | Why |
| ---- | ------ | --- |
| `list_projects`, `list_services`, `get_service`, `get_deployment`, `get_logs`, `get_metrics` | Working | Bound to the control plane's read stores. |
| `redeploy`, `rollback` | Authorized tenant human sessions | Uses `TenantDeploymentActions` with live RBAC, resource-version fencing, idempotency and audit. The trusted recovery path uses the existing operator coordinator. Requires durable runtime wiring. |
| `set_variable`, `unset_variable` | Authorized tenant human sessions with explicit mutation consent | The tenant variable coordinator checks live `variables.write` inside its transaction. Recovery tokens have no human session and are refused. Values are write-only in results and audit; a user-supplied value still exists in the chat input. Requires Postgres. |
| `deploy_from_source` | Bounded inline UTF-8 files in a scoped human chat | Uses the same source artifact, quota, acceptance/outbox and worker pipeline as `midnight up`. Requires explicit mutation consent and current resource versions. Arbitrary Git URLs, GitHub App impersonation and local filesystem paths are unsupported. See [source deployment](agent-source-deployment.md). |

Variable writes are scoped to a `(service, environment)` pair. The model may name
the environment with `environment_id`; when it does not, the environment is
resolved only if the service has exactly one. It is never guessed from several.



## Wiring summary

At process startup:

1. A shared `tools.Registry` is built once via `tools.NewDefaultRegistry()`
   and reused across every provider call.
2. `MIDNIGHT_AGENT_PROVIDER` selects the model caller:
   `claude.NewCaller` for Anthropic, `openai.NewCaller` for OpenAI
   Responses, and `claude.NewCaller` with MiniMax's Anthropic-compatible
   base URL for MiniMax. A missing key is warned about -- never fatal.
3. The conversation store is the Postgres-backed
   `conversation.PostgresStore` when the server runs in `--storage=postgres`
   mode, and the in-memory `conversation.MemoryStore` otherwise.
4. Authorization defaults: `agentauth.OpenAuthorizer` (bearer →
   every project, the single-tenant v1 default),
   `agentauth.NewEnvMutationGate()` for the operator brake,
   `agentauth.NewMemoryBudget(LoadBudgetConfig())` for the budget tracker,
   and `agentauth.NewMemorySessionRevoker()` for the kill switch. The
   budget and the kill switch default to **on**: `conversation.NewService`
   constructs them when the caller supplies none, because a spend cap
   that only exists when someone remembers to pass it is the kind of
   safety feature that quietly ends up unwired. A stack that has a
   durable budget passes it explicitly and that instance wins.
5. Every tool call — success, failure, or a refusal by the mutation gate —
   writes a row onto the tamper-evident audit chain through
   `agentauth.AgentAuditSink`, scoped to the project / service /
   deployment / environment the call named, so the chain is queryable by
   resource and not only by actor.
6. The chat handler extracts the calling principal from the authenticated
   actor bound to request context by the API middleware. The
   `X-Midnight-Actor-Id` header is not trusted by production bootstrap and
   cannot select conversation ownership or cursor scope. A custom extractor
   remains available to explicit `DataStack` embedders.

## Safety layer: what is wired

`internal/agent/auth` was written ahead of its consumers, and most of it sat
unread. It is now wired rather than deleted — nothing in it was removed,
because every piece has a consumer:

| Component | Status |
| --------- | ------ |
| `AgentAuditSink` / `AuditChainSink` | Wired. Every tool call, failure, and gate refusal writes a row on the tamper-evident chain, scoped to the resource named by the call. |
| `EnvMutationGate` / `SessionMutationGate` / `CompositeGate` | Wired. The operator brake is ANDed with the chat UI's per-message toggle, and re-evaluated on every mutating dispatch so flipping the brake takes effect at the next tool call rather than the next restart. |
| `SessionBudget` (`MemoryBudget`) | Wired into `conversation.Service.Send`. Checked before each turn, recorded after each turn. Defaults to on. |
| `SessionRevoker` (`MemorySessionRevoker`) | Wired into `conversation.Service.Send`. Checked before each turn; a revoked conversation is refused with 403 naming who stopped it and why. Defaults to on. |
| `PostgresBudget` / `PostgresSessionRevoker` | Kept, not yet the default. They implement the interfaces the service now consumes, so making the caps survive a process restart is a matter of passing one into `conversation.Config`. The in-memory default resets consumption on restart. |
| `OpenAuthorizer` / `DenyAllAuthorizer` / `PostgresACLAuthorizer` | Already wired at both bootstrap sites via `DataStack.AgentAuthorizer`. |
| `SecretRedactor` | Retained as the vault-plaintext backstop. It is not the mechanism that keeps variable values off the audit chain — see below. |

### Secrets and the audit chain

`set_variable` exists to carry a plaintext secret, and the tool registry hands
raw tool input to the audit sink. Tool input is therefore redacted by field name
before it reaches the chain: `value`, `token`, `password`, `api_key`,
`credential`, `private_key` and friends are replaced with a truncated SHA-256
digest, so two rows can still be compared for "did this change" without the
value being recoverable. Input that cannot be parsed as JSON is dropped rather
than passed through. Nothing on the write path returns a value either: the tool
result reports `{service_id, environment_id, name, ok}` and never the value, so
a secret does not end up sitting in the chat transcript.


## Operator playbook

- **First boot, Anthropic default.** Set `ANTHROPIC_API_KEY` in your
  systemd unit's `EnvironmentFile=` and the chat panel works out of the box.
- **Use OpenAI.** Set `MIDNIGHT_AGENT_PROVIDER=openai` and
  `OPENAI_API_KEY`. Optionally set `MIDNIGHT_OPENAI_CHAT_MODEL`.
- **Use MiniMax.** Set `MIDNIGHT_AGENT_PROVIDER=minimax` and
  `MINIMAX_API_KEY`. Optionally set `MIDNIGHT_MINIMAX_CHAT_MODEL` or
  `MIDNIGHT_MINIMAX_BASE_URL`.
- **Lower spend, sharper output.** Set
  the provider-specific chat-model variable and pair it with lower `*_TOKEN_CAP` /
  `*_COST_CENTS` values to keep the bill bounded.
- **Lock down mutations during an incident.** Set
  `MIDNIGHT_AGENT_ALLOW_MUTATIONS=false` and SIGHUP / restart the
  process. Every mutating tool call refuses with
  `agent: mutations disabled in this context` until the flag is
  reverted.
- **Rotate a budget cap.** Update the `*_TOKEN_CAP` /
  `*_COST_CENTS` env vars and restart. The in-memory budget tracker
  rebuilds from the new limits on next boot; existing
  conversation consumption is reset by the process restart, which
  is the operator-intended behavior for a manual cap rotation.
- **A conversation that hits a cap** gets HTTP 429 with the exhausted
  limit and the consumed amount in the problem document, so the user is
  told which cap and by how much rather than just "no". Only the
  host-daily cap rolls over, so only that one carries `Retry-After`.

## Streaming and model controls

`Caller.Stream` uses the Anthropic streaming API. Text reaches the console
token-by-token as it is generated; a `tool_call` frame is emitted the moment a
tool's arguments finish arriving. Across the tool loop the streaming path
preserves everything the non-streaming path had: parallel tool results are
returned in a **single** user message (splitting them teaches the model to stop
calling tools in parallel), both `cache_control` breakpoints stay on the system
prompt and the last tool definition, usage accumulates across iterations, and
thinking blocks are echoed back verbatim within the turn that carries a
`tool_use`.

Reasoning is not forwarded to the console as assistant text. It stays in the
accumulated message: the stream contract has no thinking frame, and putting
reasoning into the transcript would record it as something the model said to
the user.

Thinking blocks are not persisted in the conversation transcript, so they are
not replayed on a later turn. That is the documented Anthropic behaviour for
prior turns and costs nothing; the within-turn round trip that the API does
care about is preserved.

The OpenAI Responses caller (`MIDNIGHT_AGENT_PROVIDER=openai`) still emits one
delta per completed block rather than per token. It does not use an SDK — it
speaks the Responses API over hand-rolled HTTP — so token streaming there means
writing and testing an SSE decoder for a second wire protocol, which is a
larger change than the Anthropic switch and is not required by anything
shipping today. Its `StreamEvent` output is contract-identical, so the console
behaves correctly either way; it simply arrives in fewer, larger pieces.
