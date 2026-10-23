# ADR-0029: Durable service/environment config and deployment snapshots

- Status: accepted
- Date: 2026-07-27

## Context

Deploy queue payloads carried identity, source, and image handoff data but there
was no durable per-environment service policy for build selection, commands,
ports, readiness, restart behavior, rollout overlap, or drain grace. Trusting a
stale or forged queue copy of those fields would let retries drift and would
make a deployment impossible to reproduce after config or variable rotation.

## Decision

Migration 0023 adds `service_environment_configs`, keyed by service and
environment with composite project-scope foreign keys and optimistic `version`
updates.

The build or deploy worker that first handles a canonical deployment atomically
creates one `deployment_config_snapshots` row. A retry always reuses that row,
even after later config changes. The snapshot contains:

- the normalized, non-secret effective config and its version;
- every variable ID, scope service ID, name, and version in the deployment's
  project/environment;
- a SHA-256 checksum over deterministic JSON.

It never contains plaintext, ciphertext, wrapped keys, or resolved environment
values. Worker queue payloads remain value-free and do not become runtime policy
authority.

Normal services default to a TCP probe on the durable target port. Configured
HTTP probes require an exact status 200; redirects and other 2xx responses do
not pass. `health_kind=none` is an explicit opt-out for background workers and
other no-port processes. The default timeout is 300 seconds.

The deploy worker applies start command, target port, restart policy/retries,
desired replica count, overlap policy, and drain grace to the scheduling and
activation flow. The Docker adapter maps those fields to container config. The
build worker uses the immutable root directory, builder selection, and
Dockerfile path.

## Fail-closed boundaries

- A configured pre-deploy command fails with `NOT_IMPLEMENTED`; executing it as
  part of the application container would rerun migrations on restart and is
  not equivalent to a bounded one-off job.
- A configured custom build command fails before checkout/build. It is not
  silently translated to an unverified Railpack environment convention.
- This original change did not add a public REST/OpenAPI CRUD surface. The
  capability could not be advertised as user-configurable until a complete
  authenticated, scope-checked API and GUI flow landed.
- `replicas` is preserved and forwarded as desired intent; one-container local
  execution remains a Gate 7 gap.

## Consequences

Deployment retries are reproducible across worker restarts and config/variable
edits. A merely running container can no longer be promoted by the production
worker unless it proves a TCP listener, returns HTTP 200 on its configured
path, or was explicitly declared a no-port worker.

Images used with a custom start command must provide `/bin/sh`. A future typed
argv mode can remove that image assumption without changing the snapshot
contract.

## Later tenant product admission

Loop 3.2 (2026-07-29) admits the exact
`/services/{service_id}/environments/{environment_id}/config` GET/PUT route to
durable human sessions while preserving recovery-operator compatibility.
Human GET requires current `service.read`; PUT requires current `service.write`,
an exact-version compare-and-swap, exactly one stable `Idempotency-Key`, and one
strict non-null JSON object bounded to 32 KiB. Both methods resolve the
canonical service/environment project and apply the restricted-environment
deny overlay. Missing, cross-tenant, wrong-lineage, and restricted targets are
concealed from non-admin humans.

The PostgreSQL mutation coordinator revalidates the current session and grant
before replay and commits the config row, exact success or terminal
version-conflict response, idempotency state, and audit evidence atomically.
The CLI and GUI use this route without an operator fallback. Project automation
credentials, GraphQL, and MCP do not inherit the authority. A successful PUT is
still only saved desired state for a later immutable deployment snapshot; it is
not a deployment, restart, runtime apply, or proof that the configured runtime
can execute every saved value.
