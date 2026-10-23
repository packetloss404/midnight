# GitLab Preview Environment Slice

This document describes the local contract used by a future GitLab webhook handler before it enqueues deployment work.

## Integration Configuration

`internal/gitlab.IntegrationConfig` validates the native GitLab integration shape before any API route persists it. The foundation supports GitLab SaaS (`https://gitlab.com`) and HTTPS self-managed GitLab base URLs, requires a workspace ID, either a project ID or project path, a sealed webhook secret reference, and an explicit enabled-event list.

`PlanIntegration` returns the deterministic provider plan: provider name, normalized base URL, project reference, webhook path `/api/v1alpha/integrations/gitlab/webhook`, delivery ID header `X-Gitlab-Event-UUID`, and enabled event set. It performs no outbound GitLab calls.

## Webhook Intake

- GitLab merge request hooks are identified by `X-Gitlab-Event: Merge Request Hook`.
- The shared webhook secret is validated from `X-Gitlab-Token` using constant-time comparison.
- Delivery UUIDs should use `X-Gitlab-Event-UUID` when the API layer wires idempotency storage.

## Merge Request Lifecycle

`internal/gitlab.PlanMergeRequestPreview` maps GitLab merge request actions to preview actions:

| GitLab action | Midnight preview action |
| --- | --- |
| `open`, `reopen` | `create` |
| `update` | `update` |
| `close`, `merge` | `archive` |
| other | `noop` |

Preview environments are named `preview/mr-<iid>`. When a preview base domain is configured, the deterministic URL shape is `https://mr-<iid>--<service>.<base-domain>`.

## Trust Defaults

- Same-project merge requests use `project-preview-secrets`, empty data, and the default preview quota profile.
- Fork merge requests use no project/shared secrets, empty data, and the reduced fork quota profile.
- Closed or merged merge requests always drop secret access while the retention TTL controls archive deletion timing.

The package is intentionally not wired to identity, audit, Terraform/config-as-code, drains, database templates, or AI diagnosis workstreams.
