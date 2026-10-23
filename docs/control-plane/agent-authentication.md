# Runtime agent authentication

Runtime agents do not use the installation-wide operator recovery bearer.
Registration, heartbeat, desired-state polling, and status reporting use a
credential scoped to the exact agent ID in the request path and
`X-Midnight-Agent-ID` header.

## Production configuration

The server requires `MIDNIGHT_AGENT_CREDENTIALS_FILE` (or
`--agent-credentials`) when it boots a production Postgres stack. Startup fails
closed when the file is absent, unreadable, empty, or contains weak credential
material. The JSON format is:

```json
{
  "version": 1,
  "agents": {
    "agent-a": ["replace-with-at-least-32-random-bytes"]
  }
}
```

The agent receives only its own value through
`MIDNIGHT_AGENT_CREDENTIAL_FILE` (preferred), `MIDNIGHT_AGENT_CREDENTIAL`,
`--credential-file`, or `--credential`. It sends that value as a bearer over
the configured TLS channel and uses the same value to sign heartbeat and status
bodies. Status signatures cover the complete observation plus a monotonic
sequence and timestamp; the server rejects tampering, replay, and reports
outside the 30-second clock window.

To rotate a credential without downtime, add the new value beside the old
value in that agent's array, reload/restart the server, update and restart the
agent, then remove the old value and reload/restart the server. Removing the
agent entry revokes all access for that identity. A credential listed for one
agent cannot authenticate another agent ID.

Agent request admission has two bounded stages. Before credential verification,
requests share a budget keyed only by the direct peer IP so invented agent IDs
cannot create unbounded limiter state. After verification, the normal request
budget is keyed by the authenticated agent ID and direct peer IP, so co-located
agents do not consume each other's allowance. Forwarded client-IP headers are
ignored until the server has an explicit trusted-proxy configuration.

Registration, heartbeat, and status bodies are limited to 1 MiB and decoded as
one strict JSON document. Unknown fields, duplicate object keys (including
nested duplicates), and trailing JSON values are rejected.

## Local development

Memory storage automatically uses a development credential. A Postgres-backed
local stack must opt in with `MIDNIGHT_AGENT_AUTH_DEVELOPMENT=1` (or
`--agent-auth-development`) and may provide the shared development value in
`MIDNIGHT_AGENT_HMAC_SECRET`. The checked-in Compose stack opts in explicitly.
This compatibility mode is not suitable for a deployed or multi-host system.

`MIDNIGHT_AGENT_HMAC_SECRET` and `--hmac-secret` remain agent-side compatibility
inputs for local development only. `MIDNIGHT_API_BEARER_TOKEN` is no longer read
by `midnight-agent`.

## Migration

For each existing agent, generate an independent random credential, install it
in a root-readable agent credential file, add the corresponding agent ID and
value to the server keyring, and restart the server before restarting agents.
Do not copy the operator recovery bearer into the keyring. Existing deployments
that intentionally retain a shared `MIDNIGHT_AGENT_HMAC_SECRET` must enable the
development switch temporarily; production startup otherwise refuses the
legacy configuration.
