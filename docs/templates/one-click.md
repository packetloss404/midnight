# One-Click Database Templates

This page describes only the built-in, `official` database templates. Public
GitHub templates use the separate [community discovery and local-fork
flow](community.md); a community draft cannot enter this verified database
provisioning saga.

Midnight can provision official PostgreSQL 16, Redis 7 durable, MySQL 8.4, and
MongoDB 7 single-node services through the same deployment worker used by image
deploys. These are self-managed services: Midnight owns the container,
encrypted variables, private network, persistent volume, readiness gate, and
deployment evidence. It does not claim managed HA, replicas, automatic
failover, major upgrades, or a database administration UI.

## Official database runtime boundary

`POST /api/v1alpha/templates/{slug}/instances` requires an
`Idempotency-Key` and a destination `project_id`. `environment_id` is optional;
the existing production environment is selected, or one is created in the same
transaction.

```http
POST /api/v1alpha/templates/postgres/instances
Idempotency-Key: <unique-key>
Content-Type: application/json

{"project_id":"prj_demo","name":"orders-db"}
```

Replace `postgres` with `redis`, `mysql`, or `mongodb` for another official
engine. The serializable control-plane transaction creates all or none of:

- one database service using its exact-digest official image;
- an engine-specific TCP readiness configuration;
- generated encrypted credentials and sealed connection URLs;
- an engine-specific persistent data volume;
- the canonical deployment and operation event ledgers;
- a deploy-intake outbox intent; and
- a durable template-provision run.

The response is `202 Accepted` and always starts at `state: queued`. It is not
database-success evidence.

```json
{
  "operation_id": "op_...",
  "provision_run_id": "tpl_...",
  "template_slug": "postgres",
  "state": "queued",
  "environment_id": "env_...",
  "deployment_id": "dep_...",
  "created_services": [{"id":"svc_...","slug":"orders-db-..."}],
  "created_volumes": [{"kind":"volume","id":"vol_...","name":"data"}],
  "status_url": "/api/v1alpha/templates/postgres/instances/tpl_..."
}
```

The worker then uses the ordinary volume manager and isolated
project/environment network. After generic TCP readiness, it runs an
engine-native client from a sibling container on the private network, creates
and reads a deterministic marker, replaces the database container, waits for
readiness again, and requires a second sibling probe to read the same marker.
Probe results are stored as metadata-only evidence; the secret-bearing probe
containers are removed before the run may become `succeeded`.

```http
GET /api/v1alpha/templates/postgres/instances/{run_id}
```

A successful run has all three values true:

```json
{
  "state": "succeeded",
  "connectivity_verified": true,
  "persistence_verified": true,
  "cleanup_completed": true
}
```

Failed reasons are bounded and redaction-safe. Passwords, connection strings,
query output, and container environment values are never status fields.

## CLI and GUI

```sh
midnight template list
midnight template deploy --project-id prj_demo --name orders-db postgres
midnight template deploy --project-id prj_demo --name cache redis
midnight template status --slug redis tpl_...
```

The New Project → Database GUI lists all provisionable official database
templates. It polls the same durable status resource and labels connectivity,
container-replacement persistence, and probe cleanup separately.

## Networking and credentials

The database is private by default. Selecting the template does not allocate a
public endpoint. External access requires a separate explicit TCP proxy; an
optional source CIDR allowlist can restrict peers.

Each engine receives its conventional host, port, database, credential, and
connection URL variables. All values are encrypted at rest. Password-bearing
values and credential-bearing URLs use sealed scope and are injected only at
the runtime boundary.

Off-host backup execution and restore orchestration are separate capabilities;
container-replacement persistence proof is not a backup claim.
