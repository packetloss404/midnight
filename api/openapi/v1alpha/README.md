# OpenAPI v1alpha

`midnight.yaml` is the canonical v1alpha API contract embedded by the server.

The server embeds this file and serves it from `GET /api/v1alpha/openapi.yaml`. Demo endpoints are all under `/_demo/*` and are deterministic, in-memory, test-only surfaces:

- `POST /_demo/long`
- `GET /_demo/health-target`
- `GET /_demo/schema-evidence`
- `GET /_demo/migration-plan`
- `GET /_demo/release-evidence`
- `GET /_demo/evidence-index`
- `GET /_demo/api-data-readiness`

Schema evidence, migration planning, release evidence, evidence indexing, and API/data readiness are dry-run evidence surfaces. They do not connect to a real DB, execute migrations, or write files from the HTTP handlers.

The audit dashboard/feed endpoints read the router's configured audit chain. A fresh local router returns empty evidence and export manifests rather than seeded audit or SIEM demo data.

Environment metadata `PATCH /environments/{id}` explicitly lists both
recovery-operator and durable `HumanSession` security. The supported human
shape is a strict object bounded to 16 KiB with positive `expected_version` and
at least one `slug`, `name`, or `tier`; it requires singleton idempotency and
current `environment.write`. The response exposes the incremented
`resource_version`. Protected-state/restriction changes, delete, duplicate,
copy, deployment, and runtime application are outside the human contract.

Service/environment configuration `GET` and `PUT` explicitly list both
recovery-operator and durable `HumanSession` security. Human reads require
current `service.read`; writes require current `service.write`, a singleton
`Idempotency-Key`, a strict body bounded to 32 KiB, and exact-version CAS.
Restricted, cross-tenant, and wrong-lineage targets are concealed rather than
exposing configuration metadata. A saved configuration is desired state for a
later deployment snapshot, not evidence that a deployment or restart occurred.

Tenant variable metadata and lifecycle use the exact nested
`/services/{service_id}/environments/{environment_id}/variables` collection
and item paths. Those five operations are `HumanSession`-only; the global
`/variables*` family remains the unchanged recovery-operator compatibility
surface. Human schemas reject unknown ownership fields, keep `value`
write-only, require portable `[A-Za-z_][A-Za-z0-9_]*` create names up to
255 bytes, require positive int64 versions for update/delete, and return only
strict redaction-safe metadata. Metadata/item operations remain compatible
with legacy recovery-created names. Reads require `variables.read`; mutations
require `variables.write`. Every mutation requires exactly one physical
`Idempotency-Key`, reauthorizes replay, and publishes the documented ETag,
Location, and replay headers where applicable. Saving variable desired state
does not deploy, restart, or mutate a running workload. Project automation
tokens are denied.
