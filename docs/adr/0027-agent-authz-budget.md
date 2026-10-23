# ADR-0027: Agent authorization, project scope, budget, and kill switch

- Status: accepted
- Date: 2026-05-17
- Deciders: Track A1A92 (agent authorization, project scoping, and cost/safety guardrails)
- Related: ADR-0011 (audit hash chain), ADR-0017 (panel decisions)

## Context

The Midnight agent (in-product chat + MCP) exposes mutating tool calls
to a self-hosted operator. The blast radius of an agent-driven mistake
is unbounded if every mutating tool is reachable on every turn for every
project. Four risks have to be contained without coupling the chat
backend, the agent core (Anthropic SDK), the diagnosis pipeline, and
the MCP server into a single monolith:

1. **Authorization** — an agent must only see / mutate resources its
   caller can. v1 is single-tenant (one operator), but per-project ACLs
   are imminent.
2. **Mutation gating** — operators need a hard switch that turns every
   mutating tool off (read-only mode), AND a per-message flag the user
   toggles in the chat UI.
3. **Cost / token caps** — the agent can burn tokens unboundedly. Each
   conversation has a token + cost cap; each host has a daily cost
   ceiling.
4. **Kill switch** — an operator must be able to stop any active
   conversation mid-stream.

Everything above also has to be audit-logged through the existing
tamper-evident audit chain, with plaintext secrets redacted.

## Decision

A new internal package, `internal/agent/auth/`, owns the four interfaces
plus the audit emit + redaction helpers. The chat backend and the MCP
server consume the interfaces; both tracks own their own wire surfaces.

### The four interfaces

```go
type ProjectScopeAuthorizer interface {
    AllowedProjects(ctx, principal) (ProjectScope, error)
    CanRead(ctx, principal, kind, id string) (bool, error)
    CanMutate(ctx, principal, kind, id string) (bool, error)
}

type MutationGate interface {
    AllowMutations(ctx, session SessionContext) bool
}

type SessionBudget interface {
    Allow(ctx, session) (Decision, error)
    Record(ctx, session, usage Usage) error
    Limits(ctx) BudgetLimit
    Usage(ctx, session) (BudgetUsage, error)
}

type SessionRevoker interface {
    Revoke(ctx, sessionID, actorID, reason string) error
    IsRevoked(ctx, sessionID string) (bool, error)
    RevokedAt(ctx, sessionID string) (RevokedRecord, error)
}

type AgentAuditSink interface {
    Record(ctx, row AgentAuditRow) error
}
```

The chat backend's `internal/api/agent.go` consumes them via the
`DataStack` fields `AgentAuthorizer`, `AgentMutationGate`, `AgentBudget`,
`AgentRevoker`, `AgentAuditSink`. v1 defaults wire the open variants so
the existing single-tenant deploy stays drop-in compatible.

### Default values (env-overridable)

| Cap                             | Default     | Env override                                  |
| ------------------------------- | ----------- | --------------------------------------------- |
| Conversation tokens             | 200 000     | `MIDNIGHT_AGENT_CONVERSATION_TOKEN_CAP`       |
| Conversation cost (USD cents)   | 500 ($5)    | `MIDNIGHT_AGENT_CONVERSATION_COST_CENTS`      |
| Host daily cost (USD cents)     | 5 000 ($50) | `MIDNIGHT_AGENT_HOST_DAILY_COST_CENTS`        |
| Operator mutations allowed      | `true`      | `MIDNIGHT_AGENT_ALLOW_MUTATIONS=0` disables   |

The mutation gate is a `CompositeGate{Operator, Session}` — the
operator-level env var AND the per-message flag must both say yes for a
mutation to proceed.

### Project scope — v1 single-tenant

`OpenAuthorizer` is the v1 default: any operator-bearer principal sees
every project. `DenyAllAuthorizer` is the test scaffold.
`PostgresACLAuthorizer` is a feature-flagged skeleton — until the ACL
table schema lands (post-teams), every call returns
`ErrAuthorizerUnavailable` and callers fall back to `OpenAuthorizer`.
The interface is structured so per-project ACLs slot in without a wire
change.

The diagnosis pipeline runs as a `SystemPrincipal` which bypasses scope
but still emits audit rows.

### Redaction

`SecretRedactor` rewrites a free-form string by replacing every
plaintext that matches a known secret with `<redacted>`. The algorithm
is:

1. Maintain a deduped, **longest-first**-sorted list of plaintexts.
   Sorting by length ensures a longer plaintext that contains a shorter
   one as a substring is replaced atomically (the inner match would
   otherwise consume the outer one's bytes).
2. For each plaintext, do a plain `strings.ReplaceAll`. The
   replacement is idempotent because the placeholder includes
   characters (`<` `>`) that cannot appear in any of the canonical
   credential formats Midnight tracks.
3. Run `redaction.DefaultRedactor` over the result to catch
   well-known shapes (Midnight tokens, emails, IPv4, IPv6, AWS keys)
   the operator never registered.

**Known limitations** (intentional, surfaced in tests):

* Plaintext known only to an upstream provider is never redacted
  unless the operator surfaces it through `AddPlaintexts` or a
  `VaultPlaintextSource`.
* Single-character secrets and the empty string are skipped to avoid
  pathological N-pass rewrites.
* Plaintext the agent has never observed leaks through. The
  chat-backend SHOULD reject tool inputs that name an unknown
  variable.
* Tokens encoded in base64/hex/URL-safe forms are NOT decoded — only
  the literal plaintext shape is matched. The redaction test suite
  pins this limitation (`TestSecretRedactor_HexEncodedLeakIsKnownLimitation`).
* The redactor is purely synchronous — a vault rotation after the
  redactor is constructed requires a fresh `AttachVault` call. The
  bootstrap path is expected to re-seed on every variable change.

### Kill-switch semantics & latency

`SessionRevoker.Revoke` writes to the `agent_revoked_sessions` table
(or the in-memory map) — INSERT-only so the original revoker / reason
/ timestamp survive a re-revoke. The chat-backend's stream loop calls
`IsRevoked` **between agent turns** (i.e. at the model's stream-stop
event). The contract latency bound is therefore one turn:

* Typical turn duration: 1–30 s for short tool calls, up to 60 s for
  long completions.
* Contractual upper bound: `KillSwitchLatencyBound = 60 s`.
* For synchronous revoke (e.g. emergency stop) the operator also calls
  the chat-backend's `disconnect` endpoint, which closes the SSE socket
  without waiting for the next turn boundary.

Re-revoke is idempotent. The CompositeRevoker fan-out propagates the
revoke to every inner store (used during MCP↔chat migrations).

### Audit emit

`AgentAuditRow` is the per-tool-call payload. `AuditChainSink` wraps it
into an `audit.Row` and Appends through the existing tamper-evident
chain. **Note on the `actor_type` constraint:** the
`audit_logs_actor_type_chk` constraint added in migration 0001
enumerates `('user','service_account','system','mcp_client')` only.
Until a follow-up migration relaxes the constraint, the sink writes
`actor_type='system'` for agent rows and stores the canonical agent
actor kind in `attrs.actor_kind='agent'` plus the conversation /
session id in `attrs.agent_actor_id`. The track contract calls this out
as part of the coordination plan; both forms of filtering work.

The `audit_logs.project_id` / `environment_id` columns are populated
from the resource the tool acted on so per-project audit queries work
without inspecting `attrs`.

### Postgres migration

`migrations/0010_agent_revoked_sessions.up.sql` creates three tables:

* `agent_revoked_sessions(session_id PK, actor_id, reason, revoked_at)`
* `agent_session_budgets(session_id PK, tokens_used, cost_cents_used, updated_at)`
* `agent_host_budget((host_id, bucket_day) PK, cost_cents_used, updated_at)`

The host-day bucket rolls at UTC midnight. The migration is **slot 10**
in this worktree because the worktree branched before migrations 10
(variable_references), 11 (service_github_index) and the chat-backend
track's expected `0012_agent_conversations`. On merge into main this
file renumbers to the next free slot (target: 13); the schema is
independent (no FK to conversations) so the renumber is mechanical.

## Consequences

* The chat backend can wire all four gates in <50 LoC; no per-handler
  duplication of the operator+session brake.
* MCP gets the same audit and revoke story for free.
* Switching to per-project ACLs is a single
  `DataStack.AgentAuthorizer` swap — no wire change.
* The default caps mean an operator who never sets env vars gets a
  sensible $5 / 200k-token / $50-per-day safety net.

## ACL roadmap

When teams / users land:

1. Add a `project_acl` table — `(project_id, principal_id,
   read_allowed, mutate_allowed)`.
2. Flip `PostgresACLAuthorizer.Enabled = true` and provide the
   `LookupACL` hook against `project_acl`.
3. Swap `DataStack.AgentAuthorizer` from `OpenAuthorizer` to
   `PostgresACLAuthorizer`.

The interfaces do not change.

## Test coverage

* `authorizer_test.go` — Open / Deny / Postgres skeleton scopes.
* `mutation_gate_test.go` — env parsing, session flag, composite both-required.
* `budget_test.go` — defaults, token cap exhaustion, conversation cost cap,
  host-daily cap with `RetryAt`, host-day rollover, empty-session reject.
* `revoker_test.go` — set/get, idempotent re-revoke, composite fan-out,
  latency bound constant.
* `redact_test.go` — unknown input pass-through, longest-match-wins,
  idempotency, dedupe, attacker-submitted leak attempts, hex-encoded
  limitation, vault-seeded plaintexts.
* `audit_test.go` — row shape, `actor_kind=agent` attr, MCP id fallback,
  redactor integration, multi-sink fan-out.
