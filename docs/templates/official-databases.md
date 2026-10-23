# Official Database Templates

`internal/templates` defines the Phase 2/v1 official database template metadata contract. A template is publishable only when every support gate is marked passed: backup, restore, upgrade, UID/GID, smoke test, and support matrix.

Current metadata baselines cover Postgres 16, Redis 7 cache mode, Redis 7 durable mode, MySQL 8.4, and MongoDB 7 single-node. The executable built-ins are Postgres 16, Redis 7 durable, MySQL 8.4, and MongoDB 7 single-node. Redis templates must declare cache or durable mode. MongoDB intentionally rejects replica-set and sharded-cluster modes.

The marketplace/`PlanInstantiation` package remains planning-only by itself.
The canonical REST surface executes all four official runtime templates through
`POST /api/v1alpha/templates/{slug}/instances`. Its serializable transaction
creates service/config/encrypted-variable/volume/deployment/operation/outbox
truth, and its Docker worker requires an engine-native write, a real container
replacement, a read of the same marker from the attached volume, and cleanup of
secret-bearing probes before success. Runtime images are exact-digest pinned and
must be present in the worker trust allowlist.

Database templates must keep `TCPProxyDefaultEnabled` false. A database can only receive an external TCP endpoint through an explicit TCP proxy action; template selection alone never exposes Postgres, Redis, MySQL, or MongoDB to the network. Optional CIDRs restrict the peer address observed by Midnight, but Docker bridge NAT can replace the original client address with a gateway.

Generated evidence must not include credential-bearing URLs such as `postgres://user:password@host/db`. Connection URLs and credentials are stored only as encrypted variables; status and verification evidence remain metadata-only.

## Marketplace Contract

`MarketplaceCards` converts official database template metadata into user-facing cards with trust class, maintenance status, tags, engine/version, and last-updated fields. `SearchMarketplace` supports local query and tag filtering for the Phase 2 marketplace UI without contacting a remote catalog.

`DeployButtonURL` validates the deploy-on-Midnight URL shape: HTTPS dashboard base, HTTPS source repository URL without query/fragment, pinned `ref`, and workspace ID. The resulting path is `/new/template?source=<repo-url>&ref=<sha>&workspace=<id>`.

## Instantiation Planning

`PlanInstantiation` turns an official template ID plus workspace/service inputs into a local deployment plan. The plan includes the pinned image digest, mount path, internal port, a private connection URL placeholder without inline credentials, required secret names, generated credential references, concrete runtime environment references, a deterministic volume attach intent, backup command/readiness data, restore-plan guidance, private-by-default networking, support gates, and post-create checks.

Instantiation planning is intentionally offline and returns `status: planned`
with `provisioned: false`. It does not pull images, resolve secrets, create or
attach volumes, run health checks, schedule backups, expose TCP, or execute
restores. The legacy dashboard template-deploy route remains unavailable
because it does not share the canonical REST saga's atomic intake and
compensation boundary.
