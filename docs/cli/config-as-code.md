# Config as code

Midnight has two explicit config-as-code documents. The topology document
creates missing projects and services. The environment sync document stages
reviewable desired-state changes; it never deploys or applies them implicitly.

## Topology document

```json
{
  "api_version": "midnight/v1alpha",
  "projects": [{"slug": "alpha", "display_name": "Alpha"}],
  "services": [{"project_slug": "alpha", "slug": "web"}]
}
```

`midnight config validate --file midnight.json` validates the document locally.

`midnight config diff --file midnight.json` renders an offline create-only plan against an empty state. Add `--endpoint http://127.0.0.1:8080` to call `POST /api/v1alpha/config/diff` against a running server.

`midnight config apply --file midnight.json --endpoint http://127.0.0.1:8080 --idempotency-key apply-alpha` calls `POST /api/v1alpha/config/apply`. This first slice creates missing projects and services only; existing resources with differing `display_name` values are reported as updates by diff and rejected by apply.

## Environment sync document

Use canonical IDs and the current changeset revision. A revision of `0` means
the environment has no active draft. This small example stages one variable:

```json
{
  "api_version": "midnight/v1alpha",
  "environment_id": "env_01H...",
  "expected_revision": 0,
  "note": "Production API configuration",
  "changes": [
    {
      "kind": "variable",
      "operation": "add",
      "service_id": "svc_01H...",
      "name": "LOG_LEVEL",
      "scope": "plain",
      "value": "info"
    }
  ]
}
```

Stage it with a caller-owned replay key:

```sh
midnight config sync \
  --file production-sync.json \
  --endpoint https://midnight.example.com \
  --idempotency-key sync-production-20260830
```

The command validates the envelope locally, sends the document to
`POST /api/v1alpha/config/sync`, and prints the redaction-safe durable draft.
Review and transition that exact revision separately:

```sh
midnight changeset get --environment-id env_01H...
midnight changeset validate --environment-id env_01H... --revision 1
midnight changeset apply --environment-id env_01H... --revision 1 \
  --idempotency-key apply-production-20260830
```

Use `midnight changeset discard` with the same revision and a new idempotency
key to abandon the draft. Runtime, generated networking, and volume changes
use the schemas in the installation's `/api/v1alpha/openapi.yaml`. Edit and
remove operations require `target_id` plus the resource's current
`expected_version`. Variable values are write-only and encrypted before the
draft is persisted. Dashboard editors can continue collecting one tab-local
mixed-resource review, but Operations will not replace an active durable
draft; apply or discard that revision before saving another one.
