# ADR-0017: GraphQL gateway alongside the v1alpha REST API

- Status: accepted
- Date: 2026-05-17
- Owners: API + Compatibility

## Context

Midnight's wedge against Railway is API compatibility: tools, dashboards,
and CI pipelines written against Railway's public API should be able to
repoint at a self-hosted Midnight instance by changing one URL.

Railway exposes two surfaces:

1. **REST** at `https://backboard.railway.app/` for some legacy/admin
   endpoints.
2. **GraphQL** at `https://backboard.railway.app/graphql/v2` as the
   primary surface — the Railway CLI, dashboard, and most third-party
   integrations exclusively target this endpoint.

Midnight already ships a REST API at `/api/v1alpha` with the durable
operation FSM, idempotency contract, encrypted variable vault, and
Phase 1 audit chain. Re-using all of that machinery for a second surface
is a hard requirement: we do not want two parallel implementations of
the deploy intake or two divergent idempotency stores.

## Decision

Ship a **dual API surface**:

1. **Keep** the existing `/api/v1alpha/*` REST endpoints. The Idempotency-Key
   header, durable operation FSM, and bearer-token auth stay exactly as
   they are.
2. **Add** a Railway-compatible GraphQL gateway at `/graphql/v2`. The
   schema (`schemas/graphql/schema.graphql`) mirrors Railway's public
   types — `Project`, `Service`, `ServiceInstance` (the compound
   `serviceId × environmentId` resource), `Deployment`, `Variable`,
   `Environment`, `Volume`, `CustomDomain`, `ServiceDomain` — and uses
   Railway's exact field names where they exist.

### Resolver-over-services, not resolver-over-REST

Resolvers in `internal/graphql/` MUST delegate to a shared service layer
(`internal/api.ServicesAPI`) that wraps the same stores, idempotency
store, queue store, and audit chain the REST handlers call. They do
**not** round-trip through HTTP. This means:

- One durable deploy authority. In production, REST and
  `serviceInstanceDeployV2` use the same Postgres coordinator/outbox
  primitives. GraphQL has a transport-neutral entry point because it commits
  its selected service-result bytes, rather than a REST response, beside the
  operation, canonical deployment, events, audit evidence, and outbox.
- One scoped idempotency story. Production GraphQL deploy requires an
  `idempotencyKey`; the fingerprint covers operation name, query, and variables.
  Its actor is the server-authenticated recovery operator and its tenant is the
  locked service/project/workspace lineage. A changed document or variables
  conflicts; an exact retry returns the committed result bytes.
- One audit chain. Production GraphQL deploy appends its recovery-operator
  audit row inside the same SERIALIZABLE transaction. Memory/dev stacks retain
  the older direct-queue compatibility path and make no durability claim.

Where business logic previously lived inline inside a REST handler (e.g.
the deploy intake), we extracted thin `*Service` methods on
`ServicesAPI` (`internal/api/services_export.go`). The REST handlers
were intentionally NOT rewritten — they keep their existing HTTP
marshalling shape and call into the same service methods from their
inline body where appropriate. This minimizes blast radius and lets the
two surfaces converge incrementally.

### Pagination

GraphQL lists use Relay-style cursor connections (`edges{node}` +
`pageInfo{hasNextPage,endCursor}`). REST keeps its existing cursor-page
shape. The two cursors are independent (different signing keys); a
caller never receives a GraphQL cursor for a REST endpoint or vice
versa.

### No subscriptions in v1

Railway's public GraphQL surface has no subscriptions — clients poll the
`deployment(id)` and `serviceInstance(...).latestDeployment` queries
to track progress. We match that: the Midnight schema has no
`Subscription` root type. Existing SSE / streaming live on the REST
surface (`GET /operations/{id}/events?stream=sse`).

### Auth

The GraphQL handler accepts both:

- `Authorization: Bearer <token>` (matches the REST surface)
- `Project-Access-Token: <token>` (Railway's alias header)

Both check against the same `MIDNIGHT_API_BEARER_TOKEN` configured for
the REST surface. Future ADRs will introduce PKCE-style per-project
tokens, but the v1 gateway shares the global bearer with the REST
surface so existing operators do not see a regression.

### GraphiQL playground

The handler at `/graphiql` is gated behind a dev-only flag
(`--graphql-playground` / `MIDNIGHT_GRAPHQL_PLAYGROUND=1`). It is loaded
from CDN-hosted GraphiQL — no JS asset is shipped in the binary — and
serves the same schema. Production deployments leave the flag unset so
the playground route is never registered.

### Runtime

The gateway uses `vektah/gqlparser/v2` (the parser/validator gqlgen
relies on under the hood) and a hand-rolled, type-safe dispatcher.
This keeps the binary lean (no 5k-line `generated.go`) while still
validating every query against the schema. The hand-rolled dispatcher
is contained in `internal/graphql/executor.go`, `resolver.go`, and
`expand.go`; replacing it with a full `gqlgen generate` runtime is a
mechanical swap — the schema, resolvers, and service layer stay
unchanged. `internal/graphql/gqlgen.yml` documents the intended
generated-runtime shape.

## Consequences

### Positive

- Railway-API-compatible clients work against Midnight by changing the
  endpoint URL.
- Zero duplication of business logic: GraphQL resolvers and REST
  handlers call the same `ServicesAPI` methods, exercise the same
  idempotency store, and emit the same audit rows.
- The schema is the contract. Adding a new field is a one-line schema
  change + a one-line resolver case. Adding a new resource is a new
  type + a new `gql*` projection + a new resolver method.

### Negative / risks

- The hand-rolled executor reimplements a slice of gqlgen's runtime
  (selection-set traversal, edge iteration). We deliberately accept this
  cost because the alternative is a permanently-generated `generated.go`
  file that drifts the moment any schema field changes without
  re-running `gqlgen generate`. ADR-NNNN (future) will revisit when the
  schema is stable enough to justify pinning the generated runtime.
- Two API surfaces means twice the conformance test surface. Smoke tests
  (`internal/graphql/integration_test.go`) verify the project → services
  → latest deployment fan-out; full Railway compat tests are a future
  ADR.
- Subscriptions are explicitly out of scope; clients that today rely on
  Railway's WebSocket fan-out for deploy logs must poll instead. This
  matches Railway's public API, but power users may notice the latency
  difference.

### Future work

- ADR-NNNN (future): PKCE-style per-project tokens for the
  `Project-Access-Token` header.
- ADR-NNNN (future): swap the hand-rolled executor for `gqlgen
  generate` once schema churn stabilizes.
- ADR-NNNN (future): GraphQL subscriptions over SSE / WebSocket once
  Railway publishes its subscription spec.

## Links

- Related panel decisions: API compatibility wedge.
- Related implementation PRs: this commit.
- Schema: `schemas/graphql/schema.graphql`
- Runtime: `internal/graphql/{executor,resolver,expand,handler}.go`
- Service layer extraction: `internal/api/services_export.go`
- Bootstrap: `internal/graphql/bootstrap.go`, wired in
  `cmd/midnight-server/main.go` via `attachGraphQL`.

## REST parity additions (post-merge pass)

After the gateway shipped the initial Project / Service / ServiceInstance
/ Deployment / Variable / Environment / Volume scaffolding, four
concurrent feature tracks landed on REST without wiring into GraphQL
(they branched off pre-merge main). This pass closes that gap so a
client written against `/graphql/v2` reaches the same surface as
`/api/v1alpha`. The new schema entities added are:

- `enum VariableValueKind { LITERAL REFERENCE }` and
  `enum VariableReferenceKind { ServiceVar Shared LocalVar Builtin }`
  plus the `Variable.valueKind: VariableValueKind!` and
  `Variable.references: [VariableReference!]!` fields. The
  `Variable.value` resolver still returns null for every scope.
- `type Template`, `type TemplateService`, `type TemplateEnv`,
  `type TemplateVolume`, and `enum TemplateEnvKind`, with the
  `templates` / `template(slug)` queries and the
  `templateDeploy(input: TemplateDeployInput!)` mutation. The mutation
  delegates to a new `ServicesAPI.MaterializeTemplateService` method
  that runs the same materializer the REST handler runs and emits the
  same audit row (`template.deploy`, `source: "graphql"`).
- `type Volume` extended with `sizeBytes: Int!` and `status: String!`
  (the legacy `sizeMb` / `state` fields are kept for Railway-shape
  compatibility). The `volume(id)` / `volumes(serviceId, projectId,
  environmentId)` queries are now backed by `volume.Store` instead of
  the previous always-empty stub, and the new
  `volumeCreate(input: VolumeCreateInput!)`,
  `volumeResize(id, sizeBytes)`, `volumeDelete(id)` mutations route
  through `ServicesAPI.{Create,Resize,Delete}VolumeService`. The
  resize-down rejection surfaces the typed `VOLUME_RESIZE_DOWN_UNSUPPORTED`
  extension code; delete on an attached volume surfaces
  `VOLUME_ATTACHED`.
- `type GitHubLink`, `type GitHubInstallation`, `type GitHubRepo`, plus
  `Service.github: GitHubLink`, the
  `serviceGithubLink(input: GitHubLinkInput!)` /
  `serviceGithubUnlink(serviceId: ID!)` mutations, and the
  `githubInstallations` / `githubInstallationRepos(installationId)`
  queries. Link CRUD writes into `service.spec.github` via the
  existing `ServiceSpecStore` capability; installation enumeration
  delegates to the configured `GitHubAppAccessor`.

  Historical note (2026-07-29): this GraphQL/operator compatibility shape is
  not the admitted human product source-link path. ADR 0038 and migration 0049
  make relational workspace grants, exact repository grants, and canonical
  service links authoritative; the human CLI and GUI do not use these
  App-global GraphQL operations.

The `ServicesAPI` (`internal/api/services_export.go`) gained
`MaterializeTemplateService`, `ListTemplatesService`,
`GetTemplateService`, `{List,Get,Create,Resize,Delete}VolumeService`,
`{Link,Unlink}GithubService`, `GithubLinkSpecForService`,
`ListGithubInstallationsService`, and
`ListGithubInstallationReposService`. The `BuildServicesAPI` defaults
now also install a `volume.NewMemoryStore` + `NewMemoryVolumeMountStore`
when the caller does not supply one, so the GraphQL gateway sees the
same backing store the REST router does without an explicit wire-up
on `DataStack` (which already shipped `VolumeStore` /
`VolumeMountStore` for the REST handler).

The `UpsertVariableService` method now also runs `varrefs.Parse` over
the supplied value and populates `secrets.VariableInput.ValueKind` /
`References` the same way the REST `POST /variables` handler does, so a
write via either surface produces an identical redaction-safe metadata
view.

With this pass the gateway is at feature parity with the REST surface.
Future ADRs will track the remaining gaps (deploy-source CRUD, log
drains, region routing).
