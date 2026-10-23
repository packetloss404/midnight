# Container & Network Labels

The Docker `RuntimeAdapter` applies a deterministic label schema to every
Midnight-managed container and network. The schema is the contract every
adapter implementation must satisfy through ADR-015 conformance check
`label_hygiene`.

## Container labels

Every container created by `midnight-agent` carries the labels below. They
are applied as part of the validated `ContainerSpec` and re-asserted by
`internal/runtime/docker.LabelSet.AsMap()` so callers cannot accidentally
shadow a required key.

| Label | Required | Notes |
|---|---|---|
| `midnight.managed_by` | yes | Always set to the literal value `midnight-agent`. Reconcile filters on this label to ignore foreign workloads. |
| `midnight.project_id` | yes | The owning project id (panel-locked `<prefix>_<26-char ULID>` shape). |
| `midnight.environment_id` | yes | The owning environment id. |
| `midnight.service_id` | yes | The owning service id. |
| `midnight.deployment_id` | yes | The owning deployment id. |
| `midnight.instance_id` | yes | The deployment-instance id. |
| `midnight.agent_id` | optional | The host agent id when set in `Config.AgentID`. |
| `midnight.host_id` | optional | The host row id from the `hosts` table. |
| `midnight.image_digest` | optional | The verified image digest after Lane 4 cosign verification lands. |
| `midnight.idempotency_key` | optional | Carries the schedule idempotency key so retries do not duplicate. |

## Container name format

`or-{svc_id_short}-{dep_seq}-{inst_seq}` — RFC 1123 valid, ≤63 characters.
Both sequences are stable hashes of the corresponding
Midnight ids; the first eight ULID characters of the service id are used as
`svc_id_short`.

The `or-` prefix (Open-Rails legacy, retained for migration evidence) lets
the reconciler's `ContainerList` filter combine label selection with name
prefix selection. The `or-net-{prj}-{env}` prefix follows the same logic for
per-(project, environment) bridge networks.

Stale containers carrying `midnight.managed_by=midnight-agent` and a lower
`deployment_seq` are reconciled (removed); stale containers without the
managed-by label produce `CONTAINER_NAME_CONFLICT` so operators can investigate.

## Network labels (forward-looking)

Bridge networks created by the `NetworkManager` carry the same
`midnight.project_id` / `midnight.environment_id` labels so the periodic
invariant check can assert tenancy. The current implementation ships the
labelling contract; network-manager behavior is a separate runtime slice.

## Failure-code mapping

- `CGROUP_V2_REQUIRED` — host runs cgroup v1; agent refuses to start.
- `DOCKER_ENGINE_UNSUPPORTED` — Docker Engine API < v1.43; agent refuses.
- `DOCKER_DAEMON_BASELINE_CONFLICT` — `daemon.json` baseline drifted.
- `CONTAINER_NAME_CONFLICT` — stale name not safe to GC.
- `CONTAINER_LABEL_MISSING` — schedule request lacked a required label.
