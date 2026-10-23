# Terraform/OpenTofu Provider Foundations

The initial provider foundation lives in `providers/terraform/midnightprovider` without pulling in a provider SDK yet. It pins the resource model to the shared config-as-code structs in `pkg/configcode`.

Current resource contracts:

- `midnight_project`: `slug`, optional `display_name`, computed `id`.
- `midnight_service`: `project_slug`, `slug`, optional `display_name`, computed `id`.
- `midnight_environment`: `project_slug`, `slug`, optional `tier`, computed `id`.
- `midnight_variable`: non-secret desired-state metadata with optional `secret_ref` for sealed material.
- `midnight_template_instance`: deploy-on-Midnight template source/ref plus parameter bindings.

The eventual Terraform/OpenTofu plugin should translate resource state into the same `midnight/v1alpha` config document used by the API and CLI, then call the diff/apply API routes.

`providers/terraform/midnightprovider.Contract` now pins the provider boundary before the SDK is introduced:

- Endpoint environment variable: `MIDNIGHT_ENDPOINT`.
- Token environment variable: `MIDNIGHT_TOKEN`.
- Mutation idempotency header: `Idempotency-Key`.
- Default request timeout: `30s`.
- Retry budget: 3 attempts.
- Acceptance target: disposable Midnight instances only.

Provider resources remain SDK-free metadata in this slice. Acceptance tests must still run against disposable instances once the plugin boundary is implemented. Secret values are never exported into Terraform state; only secret references are modeled.
