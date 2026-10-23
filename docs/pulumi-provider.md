# Pulumi Provider Foundations

The initial Pulumi provider foundation lives in `providers/pulumi/midnightprovider` without pulling in a Pulumi SDK yet. It pins the resource model to the same `pkg/configcode` document as the CLI, API, and Terraform/OpenTofu foundations.

Current resource contracts:

- `midnight:index/project:Project`: `slug`, optional `displayName`, computed `id`.
- `midnight:index/service:Service`: `projectSlug`, `slug`, optional `displayName`, computed `id`.

The eventual Pulumi provider should translate resource inputs into the `midnight/v1alpha` config document, then call the shared diff/apply API routes. This slice does not introduce provider SDK code, previews, imports, or acceptance tests.
