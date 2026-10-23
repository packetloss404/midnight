# Architecture Decision Records

| ADR | Status | Title |
|---|---|---|
| [0001](0001-panel-decisions-normative.md) | accepted | Adopt Panel Locked Decisions As Normative |
| [0002](0002-name-and-prefixes.md) | accepted | Adopt Midnight Name And Prefixes For Phase 0 |
| [0003](0003-local-safe-scaffold-default.md) | superseded in practice (2026-09-04); retained as Phase 0 history | Local-Safe Scaffold Seams Are The Phase 0 Default; Live Adapters Stay Behind Opt-In |
| [0004](0004-oauth-shim-for-cli.md) | superseded for human login; retained as operator recovery compatibility | Thin OAuth shim for `midnight login` |
| [0017](0017-graphql-gateway.md) | accepted | GraphQL gateway alongside the v1alpha REST API |
| [0018](0018-scheduler-interface.md) | accepted | Deployment-execution Scheduler interface |
| [0019](0019-reference-variables.md) | accepted | Reference Variables (`${{ServiceName.VAR}}`) |
| [0020](0020-private-networking.md) | accepted | Per-project private networking and built-in env vars |
| [0021](0021-volumes.md) | accepted | Volumes — Docker Named Volumes for Single-Host Persistence |
| [0022](0022-github-app.md) | superseded in part by ADR 0038 and migration 0049 | GitHub App + auto-deploy on push |
| [0023](0023-ai-agent-core.md) | accepted | AI Agent Core |
| [0024](0024-agent-chat-backend.md) | proposed | AI agent chat backend (conversation persistence + SSE) |
| [0025](0025-agent-build-failure-diagnosis.md) | proposed | Agent build-failure auto-diagnosis |
| [0026](0026-mcp-server.md) | accepted | MCP Server (Track 3) |
| [0027](0027-agent-authz-budget.md) | accepted | Agent authorization, project scope, budget, and kill switch |
| [0028](0028-deploy-intake-transactional-outbox.md) | accepted | Deployment work uses transactional Postgres outboxes |
| [0029](0029-durable-service-environment-config.md) | accepted | Durable service/environment config and deployment snapshots |
| [0030](0030-durable-observability.md) | accepted | Durable scoped observability on the single-host profile |
| [0031](0031-tenant-identity-boundary.md) | accepted | Durable tenant identity precedes human product access |
| [0032](0032-atomic-human-product-mutations.md) | accepted | Human product mutations advance through one atomic coordinator |
| [0033](0033-project-automation-token-foundation.md) | accepted | Project automation tokens start as a closed, scoped verifier foundation |
| [0034](0034-identity-invitation-delivery.md) | accepted | Identity invitations use manual one-time delivery and atomic redemption |
| [0035](0035-content-addressed-tenant-source-uploads.md) | accepted | Human source uploads use content-addressed storage and atomic ownership |
| [0036](0036-tenant-operation-cancellation.md) | accepted | Tenant cancellation fences durable work and converges at worker safe points |
| [0037](0037-atomic-generated-deployment-routes.md) | accepted | Human deployment intake owns one atomic generated route |
| [0038](0038-canonical-github-source-links.md) | accepted | Canonical tenant-safe GitHub source links |
| [0039](0039-durable-webhook-inbox.md) | accepted | Verified webhooks use a durable at-least-once inbox |
| [0040](0040-service-managed-generated-domains.md) | accepted | Generated HTTP domains are service-managed tenant resources |
| [0041](0041-topology-layouts-and-product-actions.md) | accepted | Durable topology layouts and stable product actions |
| [0042](0042-usage-showback-and-admission.md) | Accepted | Evidence-backed usage showback and internal admission |
| [0043](0043-workspace-bound-github-app-setup.md) | Accepted | Bind GitHub App setup to workspace authority and GitHub proof |
