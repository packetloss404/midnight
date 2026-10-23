# Variable lifecycle

Midnight stores active variable values as encrypted envelopes in PostgreSQL.
Public REST and GUI reads return metadata only; no endpoint returns plaintext.

Durable human sessions use only the exact nested collection:

```text
GET|POST /api/v1alpha/services/{service_id}/environments/{environment_id}/variables
GET|PATCH|DELETE /api/v1alpha/services/{service_id}/environments/{environment_id}/variables/{variable_id}
```

The server derives project and workspace ownership from the canonical service
and environment rows. Callers cannot provide those ownership fields. Reads
require current `variables.read`; mutations require current `variables.write`.
Missing, cross-tenant, wrong-lineage, restricted, and tombstoned resources are
concealed as 404 where applicable. Project automation tokens are denied. The
legacy global `/api/v1alpha/variables*` routes remain an installation-wide
recovery-operator compatibility surface and are not a human-session fallback.
The nested surface manages service-owned variables only; shared-variable
management remains outside the admitted human lifecycle.

Human create bodies contain only `name`, `scope`, and the write-only `value`.
New human-created names must match the portable
`[A-Za-z_][A-Za-z0-9_]*` environment/build-secret identifier subset and are
limited to 255 bytes. Metadata and item operations remain compatible with
legacy recovery-created names, including hyphenated names.
Update bodies contain a required positive int64 `expected_version`, optional
`scope`, and write-only replacement `value`. Delete bodies contain only the
required positive int64 `expected_version`. Every body is one strict JSON
object with no unknown or duplicate fields, trailing JSON, or null document and
is bounded to 64 KiB. Each mutation also requires exactly one non-blank
physical `Idempotency-Key`; repeated and proxy-coalesced values are rejected.
An exact replay revalidates the current session and tenant authority before
returning the stored status, headers, and metadata-only body.

Creating a human variable establishes version 1. Replacing or deleting one must
send the exact current version. PostgreSQL compares that version in the same
transaction that writes ciphertext or creates the tombstone, so concurrent
editors cannot silently overwrite each other. A stale mutation returns
`409 RESOURCE_CONFLICT` with metadata-only expected and actual versions.
Successful create and update responses include an ETag for the new version;
create also returns the exact nested `Location`.

Delete removes the variable from active lookup immediately and erases the
active row's ciphertext, wrapped DEK, KEK identifier, reference list, and size
metadata. The row remains as a tombstone. A new variable with the same
project/environment/service/name gets a new identity.

Each successful value version also has an internal encrypted history record.
Deployment configuration snapshots pin variable IDs, versions, and the
service slug/display-name aliases used by cross-service references. A retry
resolves those exact historical envelopes and alias bindings even after a
later rotation, delete, or service rename; it never falls forward to current
variable state or consults mutable aliases. Version-1 snapshots created before
alias pinning still resolve same-service references, but fail closed for
cross-service aliases instead of guessing from current service metadata.

Historical plaintext is available only at the build/runtime injection
boundary. Before either production worker returns decrypted bytes to its
injector, it must append a hash-chained `variable.decrypt` audit entry. That
entry contains redacted variable identity/version, scope, resolution purpose,
KEK fingerprint, length bucket, and the exact deployment/build/cron worker
attempt; it never contains the variable name, template, or plaintext. Missing
execution correlation or failed audit persistence fails the injection.
Plaintext is never added to API responses, queues, events, audit rows, or logs.

The web editor keeps unsaved plaintext only in the current browser tab. It
edits one active row or one create draft at a time, and Save stages that edit
in the tab. Operations then encrypts the staged values into one durable
environment changeset. Review and apply commit variables together with any
runtime, generated-networking, and volume changes in one serializable
transaction. The exact nested variable routes above remain supported for
callers that deliberately need a single-row mutation. The API and CLI accept
an explicitly empty value; the current GUI requires a non-empty create or
replacement value. Neither a direct mutation nor changeset apply changes or
restarts running containers.

This boundary follows the useful desired-variable and reference flow described
by [Railway's variable documentation](https://docs.railway.com/variables),
while using Midnight's own durable changeset, RBAC, replay, and audit contract.
Midnight intentionally keeps apply separate from deployment: saving, rotating,
or deleting desired variables does not queue a deploy, restart a service, or
prove that runtime state changed.

Current limits:

- Applying desired variables does not automatically create a deployment.
- Human shared-variable create/update/delete is not admitted yet.
- Retained historical envelopes currently follow KEK retention; a complete KEK
  purge workflow must rewrap every historical version before retiring the old
  KEK.
