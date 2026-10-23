# Midnight MCP Server

> **Status (as of this commit):** the MCP server is mounted. `/mcp/v1` and `/.well-known/mcp` are live in `midnight-server` whenever `MCPRegistry` is configured (production bootstrap sets it), and `initialize` → `tools/list` → `tools/call` works end to end against the real control plane. Read tools return real data. Mutating tools are subject to the limits in the tool table below: an MCP client authenticating with the operator bearer token can drive `redeploy` and `rollback`, but **not** the variable tools, which require a signed-in administrator session that an MCP credential does not carry. `deploy_from_source` is unavailable to every caller.

Midnight ships a Model Context Protocol (MCP) server so external clients
(Claude Code, Claude Desktop, third-party agents) can drive the
Midnight control plane through the same tool surface the in-product
chat backend uses.

The server is mounted on the same HTTP listener as the rest of the
v1alpha API. There are two relevant URLs:

- `/.well-known/mcp` — JSON manifest describing transports, auth,
  capabilities, and the mutation gate. Unauthenticated.
- `/mcp/v1` — the JSON-RPC 2.0 endpoint. Bearer-authenticated.

## Adding Midnight to Claude Code

```sh
claude mcp add midnight-local \
  --transport http http://localhost:8080/mcp/v1 \
  --header "Authorization: Bearer $MIDNIGHT_API_TOKEN"
```

After this command, `claude` knows how to talk to your Midnight host.
The server exposes 11 platform tools, 4 pre-built prompts
(`diagnose-deploy`, `rollback-suggestion`, `migrate-from-railway`,
`setup-database`), and a small set of resources
(`midnight://projects`, `midnight://services/{id}/variables`, etc).

## Enabling mutating tools

By default the server hides the 5 mutating tools (`set_variable`,
`unset_variable`, `redeploy`, `rollback`, `deploy_from_source`). The
model never sees them, so it cannot try to call them.

To enable mutations for a session, add the `X-Midnight-Allow-Mutations`
header when adding the MCP server:

```sh
claude mcp add midnight-local \
  --transport http http://localhost:8080/mcp/v1 \
  --header "Authorization: Bearer $MIDNIGHT_API_TOKEN" \
  --header "X-Midnight-Allow-Mutations: true"
```

You can also flip the flag inside the `initialize` call:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "protocolVersion": "2025-06-18",
    "_meta": {"allow_mutations": true}
  }
}
```

The gate is **per-session**: once flipped, every subsequent
`tools/call` on that session may invoke any mutating tool, subject to
the registry's own audit log. Closing the connection (or the process
restarting) drops the session.

If a client calls a mutating tool with the gate closed, the server
returns:

```json
{
  "code": -32030,
  "message": "MIDNIGHT_MUTATION_DISALLOWED: tool set_variable mutates state but this session has allow_mutations=false",
  "data": {
    "code": "MIDNIGHT_MUTATION_DISALLOWED",
    "tool": "set_variable",
    "allow_mutations": false
  }
}
```

## Rate limits

Each MCP session may issue up to 60 `tools/call` requests per minute
by default. Discovery calls (`tools/list`, `resources/list`,
`prompts/list`, the manifest) do not consume the budget.

Override via the `MIDNIGHT_MCP_RATE_LIMIT_PER_MINUTE` environment
variable when starting the Midnight host. Exceeding the cap returns
JSON-RPC error code `-32029`.

## Project scoping

For per-project ACLs (a follow-up release), send the project token via
`Project-Access-Token: <token>` alongside the operator bearer. v1
single-tenant deployments accept any non-empty value here for forward
compatibility; the eventual ACL authorizer will interpret it.

## Available tools

| Tool | Read-only | Description |
|---|---|---|
| `list_projects` | yes | Enumerate all projects visible to the session. |
| `list_services` | yes | Enumerate services in a project. |
| `get_service` | yes | Service metadata + variable names (values redacted). |
| `get_deployment` | yes | Deployment status, route, commit, timing. |
| `get_logs` | yes | Recent log lines with optional substring filter. |
| `get_metrics` | yes | CPU/memory/restart counters when metrics are wired. |
| `set_variable` | **mutates** | Set a service env var, in one environment. Requires Postgres **and** a signed-in administrator session; an MCP client on the operator bearer token gets a typed refusal explaining that. Values are write-only and never echoed back. |
| `unset_variable` | **mutates** | Delete a service env var. Same session requirement as `set_variable`. |
| `redeploy` | **mutates** | Replay the service's active deployment. Goes through the durable deployment-action coordinator; unavailable in memory mode. |
| `rollback` | **mutates** | Roll a service back to a known-good deployment. Same path as `redeploy`. |
| `deploy_from_source` | **mutates** | **Unavailable to every caller.** The durable tenant deploy intake accepts pre-built images only, and GitHub-sourced builds must come from Midnight's signed-webhook source authority, which an agent principal cannot stand in for. The tool stays listed so a client is told why rather than getting a silent failure. Use `midnight up` or push the commit. |

Mutating tools are additionally gated by `MIDNIGHT_AGENT_ALLOW_MUTATIONS` and by
the session's `allow_mutations` flag; when either is off they are hidden from
`tools/list` entirely rather than failing at call time. Every call — including a
refusal — writes an audit row scoped to the resource it named. See
[docs/agent-runtime.md](agent-runtime.md) for the full limits table.

## Pre-built prompts

`prompts/list` returns four templates. Each is rendered server-side
with simple `${arg}` substitution.

- `diagnose-deploy(deployment_id)` — walk through investigating a
  failed deployment without touching state.
- `rollback-suggestion(service_id)` — find the safest rollback target
  and ask the operator to confirm.
- `migrate-from-railway(railway_project_id?)` — guide an operator
  through importing a Railway project.
- `setup-database(database, project_id, service_id_to_consume_url?)`
  — interactive Postgres/Redis provisioning walkthrough.

## Resources

`resources/list` returns four URI templates. The `resources/read`
handler resolves them against the Midnight ServicesAPI / OperationStore:

- `midnight://projects`
- `midnight://projects/{project_id}/services`
- `midnight://services/{service_id}/variables` *(values redacted)*
- `midnight://services/{service_id}/deployments/{deployment_id}/logs`

## Operator configuration

The MCP routes are mounted only when the host's `DataStack.MCPRegistry`
is non-nil. Production startup wires
`tools.NewDefaultRegistry()` once and passes the same registry to both
the chat backend and the MCP server, so the two surfaces always agree
on what tools exist.

Relevant `DataStack` fields:

| Field | Default | Notes |
|---|---|---|
| `MCPRegistry` | nil | Required to mount MCP. |
| `MCPEnv` | zero | The `agent.Env` tools see; production wires real Services/Operations/Builds seams here. |
| `MCPResources` | nil | When nil, the `resources` capability is disabled. |
| `MCPRateLimitPerMinute` | 60 | Per-session `tools/call` cap. |
| `MCPEndpoint` | `/mcp/v1` | URL path the server is mounted at. |
| `MCPDocsURL` | "" | Surfaced on the manifest's `docs` field. |

## Troubleshooting

- **401 on every call.** The bearer token doesn't match. Confirm
  `MIDNIGHT_API_BEARER_TOKEN` on the server matches the value the
  client sends as `Authorization: Bearer ...`.
- **`-32030 MIDNIGHT_MUTATION_DISALLOWED`.** The session was not
  initialized with the mutation gate open. Either re-add the MCP
  server with `--header "X-Midnight-Allow-Mutations: true"` or send
  `_meta.allow_mutations` on `initialize`.
- **`-32029 rate limit exceeded`.** Wait one minute, or raise
  `MIDNIGHT_MCP_RATE_LIMIT_PER_MINUTE`.
- **Tool returns `"isError": true` text block.** This is a tool-level
  error (e.g. service not found), not a JSON-RPC error — the model is
  expected to read it and decide what to do next.
