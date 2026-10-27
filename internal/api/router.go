package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	asyncapiv1 "github.com/midnight/midnight/api/asyncapi/v1"
	openapiv1alpha "github.com/midnight/midnight/api/openapi/v1alpha"
	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/agent/conversation"
	agentmcp "github.com/midnight/midnight/internal/agent/mcp"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/auth"
	"github.com/midnight/midnight/internal/auth/oauth"
	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/db"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/tcpproxy"
	"github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/internal/topology"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/internal/webhook"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const v1alphaPrefix = "/api/v1alpha"

// defaultCursorSecret retains the Phase 0 placeholder so existing
// unit-test entry points (NewRouter, NewRouterWithOperationStore) keep
// working without an on-disk key. Production startup loads the real
// secret via CursorSecretSource (see cursor_secret.go) before calling
// newRouterWithStores. Lane 4 will own envelope-wrapping the on-disk
// key per ADR-014.
var defaultCursorSecret = []byte("midnight-phase0-cursor-placeholder")

// defaultAgentHMACSecret is the Phase 1 dev placeholder used when no
// agent HMAC secret is supplied through DataStack.AgentHMACSecret.
// Production startup MUST replace this via the Lane 4 sealed-variable
// bootstrap path (see LANE2_STATUS.md "Agent bootstrap secret" open
// question). The placeholder is intentionally distinct from the cursor
// secret so a future audit confirms neither key is reused.
var defaultAgentHMACSecret = []byte("midnight-phase1-agent-hmac-placeholder")

// NewRouter constructs the Phase 0 v1alpha HTTP API surface.
func NewRouter() http.Handler {
	return NewRouterWithOperationStore(NewMemoryOperationStore())
}

// NewRouterWithOperationStore constructs the v1alpha HTTP API surface using
// the supplied operation store and a default in-memory resource/idempotency
// pair.
func NewRouterWithOperationStore(operationStore OperationStore) http.Handler {
	resources := NewMemoryResourceStore()
	return newRouterWithStores(operationStore, idempotency.NewMemoryStore(), NewCursorCodec(defaultCursorSecret), resources, resources)
}

func newRouterWithStores(operationStore OperationStore, idempotencyStore idempotency.Store, cursorCodec CursorCodec, projectStore ProjectStore, serviceStore ServiceStore) http.Handler {
	return newRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotencyStore,
		CursorCodec:      cursorCodec,
		ProjectStore:     projectStore,
		ServiceStore:     serviceStore,
	})
}

// DataStack bundles every persistence seam the Phase 1 router consumes.
// Lane 1 wires the Postgres-backed implementations; tests + Phase 0
// callers still use NewRouter / NewRouterWithOperationStore which build
// in-memory defaults for any nil field.
type DataStack struct {
	OperationStore   OperationStore
	IdempotencyStore idempotency.Store
	CursorCodec      CursorCodec
	ProjectStore     ProjectStore
	ServiceStore     ServiceStore
	EnvironmentStore EnvironmentStore
	// TopologyLayouts stores presentation-only, project/environment-scoped
	// service coordinates. It never participates in deploy configuration.
	TopologyLayouts topology.Store
	// EnvironmentDuplicator owns the config-bearing clone transaction. It is
	// nil in memory mode so that mode returns an honest 501 instead of
	// pretending metadata-only duplication copied encrypted state.
	EnvironmentDuplicator   EnvironmentDuplicator
	DomainStore             router.DomainWorkflowStore
	AppsDomain              string
	InstanceDomainSettings  InstanceDomainSettingsStore
	TCPProxyStore           tcpproxy.Store
	TCPProxyBackendResolver tcpproxy.BackendResolver
	TCPProxyBaseDomain      string
	// MutationScopeResolver proves project/service/environment relationships
	// before scoped mutations write state. Production wires the Postgres
	// resource stores here; isolated legacy memory fixtures may leave it nil
	// and opt in when they model a complete resource graph.
	MutationScopeResolver MutationScopeResolver
	VariableStore         *secrets.Vault
	AuditStore            AuditQueryStore
	TimelineStore         TimelineQueryStore
	LogStore              LogQueryStore
	// ObservabilityStore owns scoped historical logs, metrics, ingestion
	// counters, retention, and durable live-tail replay. Nil means the
	// production collector/query surface is honestly unavailable.
	ObservabilityStore observability.Store
	// MeteringStore owns tenant-scoped, replay-safe usage evidence and
	// transactional admission. The memory implementation is development-only.
	MeteringStore metering.Store
	// MonitorStore owns built-in metric definitions, incident state, and
	// metadata-only notification channel references.
	MonitorStore MonitorStore
	Migrations   MigrationsAdminStore
	QueueStore   queue.Store
	// DeployIntakeOutbox atomically commits REST deploy/up product truth and
	// durable work intent before a dispatcher exposes a job to workers.
	// Production Postgres wiring provides this seam; memory-mode keeps the
	// existing direct queue path unless a test explicitly supplies one.
	DeployIntakeOutbox DurableDeployIntake
	// StagedWorkOutbox atomically owns GitHub build intake and build-to-deploy
	// phase handoffs. Nil preserves the process-local compatibility path.
	StagedWorkOutbox DurableStagedWorkOutbox
	// PreviewLifecycle is the complete durable pull-request lifecycle. Nil
	// keeps pull_request deliveries acknowledged but unrouted and the read API
	// reports setup_required.
	PreviewLifecycle GitHubPreviewLifecycle
	// TemplateProvisioner owns the all-or-nothing official database-template
	// intake. TemplateRunReader surfaces its redaction-safe status.
	TemplateProvisioner TemplateProvisioner
	TemplateRunReader   templates.RunReader
	// CommunityTemplateDiscoverer admits only immutable public GitHub sources;
	// CommunityTemplateForks stores project-owned editable copies. A nil store
	// keeps memory mode honest instead of presenting ephemeral template edits.
	CommunityTemplateDiscoverer templates.CommunityDiscoverer
	CommunityTemplateForks      templates.CommunityForkStore

	// DeploymentStore is the canonical deployment-record persistence. When nil
	// (memory-mode / default NewRouter), dual-write to the `deployments` table
	// is skipped and the legacy operation-event deploy path is unaffected.
	DeploymentStore DeploymentStore
	// EnvironmentResourceEvidence batches canonical configured-source,
	// per-replica readiness, and verified public-route evidence for the product
	// environment workspace. Nil keeps non-durable embedders honest.
	EnvironmentResourceEvidence *PostgresEnvironmentResourceEvidenceStore

	// DeploymentActions is the durable intake seam for image replay and runtime
	// actions. It is intentionally nil in memory mode: these mutations need a
	// single transaction spanning operations, deployment snapshots, and the
	// staged-work outbox, so pretending that a process-local implementation is
	// equivalent would make the product promise unsafe.
	DeploymentActions DeploymentActionIntake
	// CronOccurrences exposes durable, redaction-safe execution evidence for
	// automatic cron occurrences. Nil is an honest unavailable capability.
	CronOccurrences CronOccurrenceStore

	// Phase0ProofPacketDir points dashboard proof summary APIs at recorded
	// release artifacts. Empty value reads the canonical artifacts/phase0
	// packet and never shells out or probes the live host.
	Phase0ProofPacketDir string

	// AgentRegistry exposes Lane 2's agent endpoints under
	// /api/v1alpha/agents/*. When nil, Lane 5 wires an in-memory
	// AgentRegistry seeded from AgentHMACSecret so the router stays
	// usable without a Postgres-backed host store. Lane 1 did not ship
	// a Go-level Postgres adapter for hosts (the migration is in
	// 0004_agents.up.sql but the adapter is a follow-up); when the
	// adapter lands the constructor here just consumes it.
	//
	// TODO(phase1/lane5): replace the in-memory AgentRegistry with the
	// Postgres-backed host store as soon as Lane 1's Go adapter for
	// hosts/host_heartbeats lands.
	AgentRegistry *AgentRegistry
	// AgentCredentials resolves authentication/signing keys scoped to a single
	// runtime agent. Production startup wires a file-backed static resolver;
	// the operator recovery bearer is never consulted by agent routes.
	AgentCredentials AgentCredentialResolver

	// AgentHMACSecret is retained only for the explicit shared development
	// compatibility resolver. Production callers use AgentCredentials.
	// Empty value falls back to the local test placeholder.
	AgentHMACSecret []byte

	// AuditChain is Lane 4's panel-locked audit hash chain seam. The
	// wire-up uses it for the inbound webhook receiver's audit emit
	// path and every mutating handler's success path. When nil,
	// Lane 5 builds an in-memory MemoryChain so default NewRouter()
	// consumers stay drop-in compatible. Production callers wire
	// `audit.NewPostgresChain(pool)` so the chain persists across
	// process restarts and tamper detection works against the real
	// audit_logs / audit_checkpoints tables (see
	// internal/audit/chain_pg.go).
	AuditChain audit.AuditChain
	// AuditVerifyingKeys is operator-configured trust; exported keys and
	// worker distribution directories are never imported implicitly.
	AuditVerifyingKeys audit.VerifyingKeyProvider

	// WebhookEndpoints resolves the {endpoint_id} component of an
	// inbound webhook URL into a per-endpoint signing key set. Lane 4
	// ships the in-memory MemoryEndpointResolver; Lane 1 will plug in
	// the Postgres-backed shape once the webhook_endpoints schema
	// lands. Defaults to an empty MemoryEndpointResolver so unknown
	// endpoint ids return 404 (matching the panel-locked contract).
	WebhookEndpoints webhook.EndpointResolver

	// WebhookDedupeStore is the ephemeral compatibility seam used only when no
	// durable inbox is wired. PostgreSQL production uses WebhookInbox below.
	WebhookDedupeStore webhook.DedupeStore

	// WebhookDispatcher promotes an accepted inbound delivery into
	// the deploy operation pipeline. Default NoopDispatcher keeps the
	// receiver wire-test friendly; Lane 3 / Sprint 10 promotes
	// inbound deliveries into deploy operations.
	WebhookDispatcher webhook.Dispatcher

	// WebhookInbox is the PostgreSQL persist-before-ack delivery boundary.
	// When present, the HTTP receiver only verifies and commits; a separately
	// managed worker plans and dispatches immutable targets. Nil keeps memory
	// mode explicitly ephemeral and synchronous.
	WebhookInbox webhook.DurableInbox
	// WebhookDeliveryRedrive is separately wired only when the inbox,
	// idempotency store, and audit chain share one PostgreSQL transaction
	// authority. Nil makes the operator mutation return an honest 501.
	WebhookDeliveryRedrive WebhookDeliveryRedriveCoordinator

	// GitHubApp is the optional GitHub App accessor that powers
	// /github/installations and /github/installations/{id}/repos.
	// Defaults to nil (those endpoints return 501) so router_test.go
	// keeps working without GitHub credentials.
	GitHubApp GitHubAppAccessor

	// GitHubAppConfig is the parsed AppConfig kept on the DataStack so
	// downstream components (build worker, webhook dispatcher) share
	// the same key material. Operators set MIDNIGHT_GITHUB_* env vars
	// and the bootstrap path populates this field.
	GitHubAppConfig *github.AppConfig

	// GitHubSourceLinks is canonical workspace-grant and service-link truth.
	// TenantGitHubSourceLinks is the separately admitted human authority that
	// linearizes current access, exact grants, idempotency, links, and audit.
	// Both stay nil in memory mode so the product never advertises durable
	// source linking on process-local service.spec compatibility data.
	GitHubSourceLinks       *github.SourceLinkStore
	TenantGitHubSourceLinks TenantGitHubSourceLinkCoordinator
	GitHubGrantRecovery     GitHubGrantRecoveryCoordinator
	// GitHubSetup is the human workspace-admin installation handshake. The
	// callback remains separately unauthenticated at the HTTP layer because
	// GitHub cannot forward a Midnight bearer; signed one-time state plus a
	// GitHub user OAuth proof are required before it writes any grant.
	GitHubSetup GitHubSetupCoordinator

	// BackupDestinationURI is the configured off-host backup target
	// visible to restore status. Until backup destination resources
	// land, production callers may leave it empty and the router reads
	// MIDNIGHT_BACKUP_DESTINATION_URI from process env.
	BackupDestinationURI string

	// APIBearerToken optionally protects dashboard and product/control-plane
	// routes with Authorization: Bearer <token>. Empty preserves the Phase 0
	// developer behavior where the local API is unauthenticated.
	APIBearerToken string

	// IdentitySessions enables durable, expiring human login sessions. Product
	// reads require the shared tenant authority below; product writes require
	// an explicitly admitted TenantMutations route. Human sessions marked as
	// installation administrators may also use the legacy global surface.
	IdentitySessions *identity.Manager
	// AutomationTokens verifies project-scoped service-account credentials.
	// Authentication alone grants nothing: product route registration also
	// applies the exact ci-deploy-v1 admission registry.
	AutomationTokens AutomationTokenAuthenticator
	// AutomationTokenCatalog lists metadata only after a human project-manage
	// authorization decision. It never returns raw bearer material.
	AutomationTokenCatalog AutomationTokenCatalog
	// AutomationTokenMutations coordinates human-authorized mint and revoke
	// transactions. Route registration remains explicitly human-session-only.
	AutomationTokenMutations AutomationTokenMutationCoordinator
	// IdentityInvitationCatalog exposes exact-scope metadata only. The raw
	// bearer credential exists solely in create/reissue responses.
	IdentityInvitationCatalog IdentityInvitationCatalog
	// IdentityInvitationMutations coordinates human-only create, reissue,
	// revoke, and accept transactions.
	IdentityInvitationMutations IdentityInvitationMutationCoordinator
	// IdentityAccess owns workspace/project membership and canonical scope
	// resolution. It is separate from session verification so tenant auth can
	// stay fail-closed while a store is unavailable.
	IdentityAccess identity.AccessStore
	// IdentityClaim atomically commits the one-time default-workspace claim,
	// audit evidence, and exact sensitive idempotency response. Production wires
	// this only when all participants share one PostgreSQL authority. Nil keeps
	// memory/custom AccessStore compatibility on the legacy handler path.
	IdentityClaim IdentityClaimCoordinator
	// IdentityRecovery resets one installation administrator's password and
	// TOTP enrollment under the operator recovery credential. It is the only
	// supported way back into a claimed installation whose first-run password
	// was lost, since /auth/claim is one-shot. Nil keeps the route closed.
	IdentityRecovery identity.AdministratorRecoveryStore
	// IdentityRecoveryCommit atomically commits that reset, its audit
	// evidence, and the exact sensitive idempotency response. Production wires
	// this only when all participants share one PostgreSQL authority; nil
	// keeps memory/custom recovery stores on the legacy store path.
	IdentityRecoveryCommit IdentityRecoveryCoordinator
	// TenantMutations is the route-by-route human product write coordinator.
	// A non-nil value does not enable arbitrary writes: the router explicitly
	// admits only methods whose coordinator path commits current session
	// validity, RBAC, idempotency, product state, and audit evidence together.
	TenantMutations TenantMutationCoordinator
	// TenantEnvironmentUpdates independently admits exact human PATCH metadata
	// updates. Nil keeps update, protection, deletion, and duplication closed
	// while preserving tenant environment reads and operator recovery.
	TenantEnvironmentUpdates TenantEnvironmentUpdateCoordinator
	// TenantServiceConfigs independently admits exact human GET/PUT desired
	// configuration on one service/environment lineage. Nil keeps the visible
	// product editor fail-closed without affecting operator recovery.
	TenantServiceConfigs TenantServiceConfigCoordinator
	// TenantServiceDeletes independently admits exactly one human route:
	// DELETE /services/{id}. It can never start, stop, or remove a container;
	// a service whose runtime is still live is refused rather than deleted.
	// Nil keeps service deletion closed without affecting any other surface.
	TenantServiceDeletes TenantServiceDeleteCoordinator
	// TenantVariables independently admits metadata-only reads and exact
	// create/rotate/delete mutations for service-owned variables on one
	// service/environment lineage. Shared-variable mutation and the legacy
	// global recovery surface remain separately closed.
	TenantVariables TenantVariableCoordinator
	// EnvironmentChangesets is the durable, human-session review/apply
	// boundary for one environment. Variable, runtime config, generated-domain,
	// and volume intent use their canonical stores and per-kind capabilities in
	// one serializable transaction. Apply never creates a deployment.
	EnvironmentChangesets EnvironmentChangesetCoordinator
	// TenantGeneratedDomains independently admits only HumanSession generated
	// HTTP list/create/get/delete on one service/environment lineage. Custom
	// hosts, paths, ports, TLS, and the flat recovery surface remain closed.
	TenantGeneratedDomains TenantGeneratedDomainCoordinator
	// TenantCustomDomains admits exact human-session custom HTTP hostnames.
	// Ownership TXT proof is required before the route becomes public; the
	// operator recovery domain API remains a separate authority.
	TenantCustomDomains TenantCustomDomainCoordinator
	// TenantDeployIntake admits only the exact human-session immutable-image
	// POST /services/{service_id}/deployments route. All source, route, up, and
	// cancellation mutations remain independently closed even when this
	// coordinator is installed; deployment actions have their own coordinator.
	TenantDeployIntake TenantDeployIntakeCoordinator
	// GraphQLDeployIntake is the independent trusted-operator GraphQL deploy
	// authority. Production installs it only when idempotency, audit, canonical
	// deployment state, and the worker outbox share one Postgres pool. Memory
	// mode deliberately retains the legacy direct-queue compatibility path.
	GraphQLDeployIntake GraphQLDeployIntakeCoordinator
	// TenantUpIntake and SourceArtifactStore jointly admit only the exact
	// human-session POST /services/{service_id}/up route. The coordinator
	// repeats current authorization after content-addressed staging and commits
	// the artifact reference with operation/deployment/outbox/audit/idempotency.
	TenantUpIntake      TenantUpIntakeCoordinator
	SourceArtifactStore TenantUpArtifactStore
	// TenantDeploymentActions admits only the exact human-session deployment
	// action route. It remains independent from first-deploy/up admission and
	// does not enable cancellation, source linking, routing, GraphQL, or MCP.
	TenantDeploymentActions TenantDeploymentActionCoordinator
	// TenantOperationCancel admits only the exact human-session operation
	// cancellation route. It does not enable the internal fail endpoint,
	// arbitrary operation mutation, automation cancellation, or GraphQL/MCP.
	TenantOperationCancel TenantOperationCancelCoordinator

	// ProductionAuthRequired fails closed for unsafe product/control-plane
	// methods when a production server expects API auth but no bearer token is
	// configured yet. Developer/test routers leave this false.
	ProductionAuthRequired bool
	// APIAdmissionLimits applies one shared authenticated request budget and a
	// separate concurrent SSE budget. Zero values select service defaults.
	APIAdmissionLimits APIAdmissionLimits

	// Clock overrides the wall clock used by the webhook receiver and
	// audit sink. Tests pass a fixed time.Time so signature replay
	// windows stay deterministic.
	Clock func() time.Time

	// GraphQLHandler, when non-nil, is mounted at /graphql/v2 alongside
	// the v1alpha REST surface. The graphql package builds the handler
	// (it owns the schema/resolver wiring) and passes it in here so
	// internal/api stays free of a graphql import (which would create
	// a cycle: internal/graphql already imports internal/api).
	GraphQLHandler http.Handler

	// GraphQLPlaygroundHandler, when non-nil, is mounted at /graphiql
	// behind the same protection layer as the v1alpha surface. Production
	// callers leave this nil so the playground is not exposed.
	GraphQLPlaygroundHandler http.Handler

	// OAuthStore optionally enables the OAuth-on-API shim mounted at
	// /auth/oauth/{authorize,token}. The shim's `access_token` is the
	// existing operator bearer token (see ADR on the OAuth shim). When
	// nil, Lane 5 builds an in-memory store so the routes work out of the
	// box; the dev placeholder is acceptable because the shim is single-
	// tenant. Postgres-backed storage lands when accounts land.
	OAuthStore *oauth.Store

	// VolumeStore is the (service, environment)-scoped persistent
	// volume store. The scheduler consumes it via volume.Manager to
	// materialize Docker volumes and record attachments. When nil the
	// router falls back to an in-memory store so dev/test boots stay
	// usable; production wires volume.NewPostgresStore here.
	VolumeStore volume.Store

	// VolumeRecoveryStore owns durable backup/restore runs and their atomic
	// worker-queue handoff. It never receives archive passphrases.
	VolumeRecoveryStore VolumeRecoveryStore
	// VolumeRecoveryApply atomically commits a staged replacement-volume swap
	// with the immutable redeploy/outbox handoff. Nil means apply is unavailable
	// even when backup and restore staging are configured.
	VolumeRecoveryApply VolumeRecoveryApply
	// VolumeRecoveryExecutorAvailable is explicit operator evidence that a
	// Docker recovery worker has both destination and key-file configuration.
	// The API cannot infer this from the presence of control-plane tables.
	VolumeRecoveryExecutorAvailable bool

	// VolumeMountStore caches the (volume_id, mount_path) pair the
	// REST create call supplied so subsequent GETs echo it back even
	// before a deployment has scheduled the attachment.
	VolumeMountStore VolumeMountStore

	// ServiceConfigStore is the durable per-service/environment deployment
	// intent used by the public REST surface and immutable worker snapshots.
	ServiceConfigStore ServiceConfigStore

	// AgentAuthorizer is the project-scope authorization seam
	// consumed by the agent chat backend and the MCP server. It
	// gates every read / mutate against the principal carried on
	// the request. Nil falls back to agentauth.OpenAuthorizer
	// (bearer → all projects), the single-tenant v1 default.
	AgentAuthorizer agentauth.ProjectScopeAuthorizer

	// AgentMutationGate is the operator+session brake on mutating
	// tool calls. Nil falls back to agentauth.DefaultMutationGate
	// (MIDNIGHT_AGENT_ALLOW_MUTATIONS + per-message flag).
	AgentMutationGate agentauth.MutationGate

	// AgentBudget tracks cumulative per-conversation + per-host
	// daily usage and decides whether to allow another turn. Nil
	// falls back to an in-memory MemoryBudget with the default
	// caps (200k tokens / $5 conversation / $50 host-day).
	AgentBudget agentauth.SessionBudget

	// AgentRevoker is the operator kill switch for active
	// conversations / MCP sessions. The chat-backend's stream
	// loop polls IsRevoked between turns. Nil falls back to an
	// in-memory MemorySessionRevoker.
	AgentRevoker agentauth.SessionRevoker

	// AgentAuditSink is the per-tool-call audit emit seam. Nil
	// falls back to a sink that writes through the existing
	// audit.AuditChain (so the canonical hash chain remains the
	// single source of truth).
	AgentAuditSink agentauth.AgentAuditSink

	// ConversationStore is the AI agent chat persistence seam. When
	// nil the router builds an in-memory MemoryStore so developer
	// builds get the agent chat panel without a Postgres dependency.
	ConversationStore conversation.Store

	// AgentCaller is the seam between the chat backend and the
	// Anthropic SDK / tool loop. When nil the conversation Send
	// endpoint returns a clear "caller not configured" error; the
	// REST CRUD endpoints continue to work.
	AgentCaller agent.Caller
	// AgentChatModel is the provider-resolved default model stamped onto new
	// chat conversations. Empty falls back to conversation.DefaultModel.
	AgentChatModel string

	// ConversationScopeAuthorizer optionally restricts AI agent
	// conversations to specific project / service scopes. When nil
	// every authenticated caller may see every conversation.
	ConversationScopeAuthorizer conversation.ProjectScopeAuthorizer

	// AgentPrincipalFromRequest extracts the principal id (actor) for
	// the AI agent chat handlers. Production bootstrap supplies an extractor
	// backed by the authenticated actor in request context; the nil default does
	// the same. Tests/embedders may explicitly supply their own authenticated
	// principal extractor.
	AgentPrincipalFromRequest func(r *http.Request) string

	// MCPRegistry is the canonical tool registry the MCP server bridges
	// onto the JSON-RPC / streamable-HTTP wire at /mcp/v1. When nil the
	// MCP routes are NOT mounted (the dashboard / chat surface still
	// works). Production startup builds tools.NewDefaultRegistry() and
	// passes it here.
	MCPRegistry *tools.Registry

	// MCPEnv is the agent.Env handed to tool implementations executed
	// over the MCP wire. The MCP server overlays AllowMutations per
	// session — the rest of the bag (Services / Operations / Builds /
	// Metrics / Audit / Actor) is forwarded verbatim. When the zero
	// value is supplied the MCP server still mounts but tool
	// invocations will fail with whatever error the in-memory tools
	// raise (typically "service not found") — production callers MUST
	// populate this field.
	MCPEnv agent.Env

	// MCPResources is the resources/list and resources/read provider.
	// When nil the MCP server still mounts but advertises
	// capabilities.resources=false and returns method-not-found for
	// the read endpoints.
	MCPResources agentmcp.ResourceProvider

	// MCPRateLimitPerMinute caps tools/call calls per MCP session
	// per minute. <=0 falls back to MIDNIGHT_MCP_RATE_LIMIT_PER_MINUTE
	// or the package default (60).
	MCPRateLimitPerMinute int

	// MCPEndpoint is the path the MCP server is mounted at; used by
	// /.well-known/mcp to advertise the URL to clients. Defaults to
	// "/mcp/v1".
	MCPEndpoint string

	// MCPDocsURL surfaces a docs URL on the manifest's `docs` field
	// so clients know where to read about the server.
	MCPDocsURL string
}

// NewRouterWithDataStack wires the v1alpha HTTP API surface using the
// supplied DataStack. Any nil field falls back to the in-memory
// defaults used by Phase 0 tests.
func NewRouterWithDataStack(stack DataStack) http.Handler {
	return newRouterWithDataStack(stack)
}

func newRouterWithDataStack(stack DataStack) http.Handler {
	_, handler := buildRouterWithDataStack(stack)
	return handler
}

// buildRouterWithDataStack retains the existing construction seam for tests
// that need to query net/http's matcher directly.
func buildRouterWithDataStack(stack DataStack) (*http.ServeMux, http.Handler) {
	mux, handler, _ := buildRouterWithDataStackInventory(stack)
	return mux, handler
}

// buildRouterWithDataStackInventory returns the concrete ServeMux, public
// middleware-wrapped handler, and the explicit REST inventory populated by the
// same registrar that installs production routes.
func buildRouterWithDataStackInventory(stack DataStack) (*http.ServeMux, http.Handler, *RuntimeRouteInventory) {
	if stack.OperationStore == nil {
		stack.OperationStore = NewMemoryOperationStore()
	}
	if stack.IdempotencyStore == nil {
		stack.IdempotencyStore = idempotency.NewMemoryStore()
	}
	if stack.ProjectStore == nil {
		stack.ProjectStore = NewMemoryResourceStore()
	}
	if stack.ServiceStore == nil {
		if memory, ok := stack.ProjectStore.(*MemoryResourceStore); ok {
			stack.ServiceStore = memory
		} else {
			stack.ServiceStore = NewMemoryResourceStore()
		}
	}
	if stack.EnvironmentStore == nil {
		stack.EnvironmentStore = NewMemoryEnvironmentStore(stack.Clock)
	}
	if stack.TopologyLayouts == nil && !stack.ProductionAuthRequired {
		stack.TopologyLayouts = topology.NewMemoryStore(stack.Clock)
	}
	if stack.DeploymentStore == nil {
		stack.DeploymentStore = NewMemoryDeploymentStore(stack.Clock)
	}
	if stack.MeteringStore == nil {
		stack.MeteringStore = metering.NewMemoryStore()
	}
	if stack.VariableStore == nil {
		stack.VariableStore = newDefaultVariableStore(stack.ProductionAuthRequired)
	}
	if stack.CursorCodec.secret == nil {
		stack.CursorCodec = NewCursorCodec(defaultCursorSecret)
	}
	if stack.AuditChain == nil {
		stack.AuditChain = audit.NewMemoryChain()
	}
	if stack.VolumeStore == nil {
		stack.VolumeStore = volume.NewMemoryStore()
	}
	if stack.VolumeMountStore == nil {
		stack.VolumeMountStore = NewMemoryVolumeMountStore()
	}
	clock := stack.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	emitter := newAuditEmitter(stack.AuditChain, clock)

	if stack.AgentAuthorizer == nil {
		stack.AgentAuthorizer = agentauth.NewOpenAuthorizer()
	}
	if stack.AgentMutationGate == nil {
		stack.AgentMutationGate = agentauth.DefaultMutationGate()
	}
	if stack.AgentBudget == nil {
		stack.AgentBudget = agentauth.NewMemoryBudget(agentauth.LoadBudgetConfig())
	}
	if stack.AgentRevoker == nil {
		stack.AgentRevoker = agentauth.NewMemorySessionRevoker()
	}
	if stack.AgentAuditSink == nil {
		stack.AgentAuditSink = agentauth.NewAuditChainSink(stack.AuditChain)
	}

	hub := newOperationStreamHub()
	operationStore := newPublishingOperationStore(stack.OperationStore, hub)
	dashboardAuditSource := newAuditChainDashboardSource(stack.AuditChain)
	dashboardAuditSource.keys = stack.AuditVerifyingKeys
	registry := stack.AgentRegistry
	if registry == nil {
		if stack.AgentCredentials != nil {
			registry = NewAgentRegistryWithCredentials(stack.AgentCredentials, nil)
		} else {
			secret := stack.AgentHMACSecret
			if len(secret) == 0 {
				secret = defaultAgentHMACSecret
			}
			registry = NewAgentRegistry(secret, nil)
		}
	}
	backupDestinationURI := strings.TrimSpace(stack.BackupDestinationURI)
	if backupDestinationURI == "" {
		backupDestinationURI = os.Getenv(backup.EnvBackupDestinationURI)
	}
	mux := newRuntimeRouteMux()
	admission := NewAPIAdmissionController(stack.APIAdmissionLimits, clock)
	protectedAuth := optionalBearerTokenMiddleware(
		stack.APIBearerToken,
		stack.ProductionAuthRequired,
		stack.IdentitySessions,
	)
	protected := func(next http.Handler) http.Handler {
		return protectedAuth(admission.Middleware(next))
	}
	operatorToken := operatorRecoveryToken(stack.APIBearerToken)
	protectedHandleFunc := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, protected(handler))
	}
	productAuth := productAuthenticationMiddleware(
		stack.APIBearerToken,
		stack.ProductionAuthRequired,
		stack.IdentitySessions,
		stack.AutomationTokens,
	)
	productProtected := func(next http.Handler) http.Handler {
		return productAuth(admission.Middleware(next))
	}
	productProjects := stack.ProjectStore
	productServices := stack.ServiceStore
	productEnvironments := stack.EnvironmentStore
	productTenantReadsEnabled := stack.IdentitySessions != nil &&
		stack.IdentitySessions.SharesAccessAuthority(stack.IdentityAccess) &&
		productResourceStoresShareAuthority(
			stack.ProjectStore,
			stack.ServiceStore,
			stack.EnvironmentStore,
			stack.IdentityAccess,
		)
	if productTenantReadsEnabled {
		productProjects = tenantProjectStore{base: stack.ProjectStore, access: stack.IdentityAccess}
		productServices = tenantServiceStore{base: stack.ServiceStore, access: stack.IdentityAccess}
		productEnvironments = tenantEnvironmentStore{base: stack.EnvironmentStore, access: stack.IdentityAccess}
	}
	tenantOperationAuthorizer, tenantOperationReadsEnabled := newPostgresTenantOperationAuthorizer(
		stack.OperationStore,
		stack.DeploymentStore,
		stack.IdentityAccess,
	)
	tenantOperationReadsEnabled = tenantOperationReadsEnabled && productTenantReadsEnabled
	tenantOperationAuthorityEnabled := tenantOperationReadsEnabled ||
		(productTenantReadsEnabled && stack.TenantOperationCancel != nil)
	productReadHandleFunc := func(pattern string, handler http.HandlerFunc) {
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanReadOnlyProductMiddleware(productHandler)
		productHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled, productHandler)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productProjectCollectionHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanProjectCreate := func(r *http.Request) bool {
			return r.Method == http.MethodPost &&
				r.URL.Path == v1alphaPrefix+resourceCollectionProjects
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanProjectCreate, productHandler)
		productHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled, productHandler)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productServiceCollectionHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanServiceCreate := func(r *http.Request) bool {
			return stack.TenantMutations != nil &&
				r.Method == http.MethodPost &&
				r.URL.Path == v1alphaPrefix+resourceCollectionServices
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanServiceCreate, productHandler)
		productHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled, productHandler)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productEnvironmentCollectionHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanEnvironmentCreate := func(r *http.Request) bool {
			return stack.TenantMutations != nil &&
				r.Method == http.MethodPost &&
				r.URL.Path == v1alphaPrefix+"/environments"
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanEnvironmentCreate, productHandler)
		productHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled, productHandler)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productEnvironmentItemHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanEnvironmentUpdate := func(r *http.Request) bool {
			return stack.TenantEnvironmentUpdates != nil &&
				r.Method == http.MethodPatch
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanEnvironmentUpdate, productHandler)
		productHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled, productHandler)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productServiceConfigHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanServiceConfigPut := func(r *http.Request) bool {
			return stack.TenantServiceConfigs != nil &&
				r.Method == http.MethodPut
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanServiceConfigPut, productHandler)
		productHandler = humanTenantAuthorityMiddleware(
			productTenantReadsEnabled && stack.TenantServiceConfigs != nil,
			productHandler,
		)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productServiceItemHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanServiceDelete := func(r *http.Request) bool {
			return stack.TenantServiceDeletes != nil &&
				r.Method == http.MethodDelete
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanServiceDelete, productHandler)
		productHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled, productHandler)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productTenantVariablesHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanVariableMutation := func(r *http.Request) bool {
			if stack.TenantVariables == nil {
				return false
			}
			switch r.Method {
			case http.MethodPost, http.MethodPatch, http.MethodDelete:
				return true
			default:
				return false
			}
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanVariableMutation, productHandler)
		productHandler = humanTenantAuthorityMiddleware(
			productTenantReadsEnabled && stack.TenantVariables != nil,
			productHandler,
		)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productEnvironmentChangesetsHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanChangesetMutation := func(r *http.Request) bool {
			return stack.EnvironmentChangesets != nil && r.Method != http.MethodGet
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanChangesetMutation, productHandler)
		productHandler = humanTenantAuthorityMiddleware(
			productTenantReadsEnabled && stack.EnvironmentChangesets != nil,
			productHandler,
		)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productTenantGeneratedDomainsHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanGeneratedDomainMutation := func(r *http.Request) bool {
			if stack.TenantGeneratedDomains == nil {
				return false
			}
			switch r.Method {
			case http.MethodPost, http.MethodDelete:
				return true
			default:
				return false
			}
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanGeneratedDomainMutation, productHandler)
		productHandler = humanTenantAuthorityMiddleware(
			productTenantReadsEnabled && stack.TenantGeneratedDomains != nil,
			productHandler,
		)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productTenantCustomDomainsHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanCustomDomainMutation := func(r *http.Request) bool {
			if stack.TenantCustomDomains == nil {
				return false
			}
			switch r.Method {
			case http.MethodPost, http.MethodDelete:
				return true
			default:
				return false
			}
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(allowHumanCustomDomainMutation, productHandler)
		productHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled && stack.TenantCustomDomains != nil, productHandler)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productServiceSubrouteHandleFunc := func(pattern string, handler http.HandlerFunc) {
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanServiceDeploymentReadBoundaryMiddleware(
			productHandler,
			stack.TenantDeployIntake != nil,
			stack.TenantUpIntake != nil && stack.SourceArtifactStore != nil,
		)
		productHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled, productHandler)
		mux.Handle(pattern, productProtected(productHandler))
	}
	productOperationReadHandleFunc := func(pattern string, handler http.HandlerFunc) {
		allowHumanCancel := func(r *http.Request) bool {
			if stack.TenantOperationCancel == nil ||
				r.Method != http.MethodPost {
				return false
			}
			_, action, ok := parseOperationPath(r.URL.Path)
			return ok && action == "cancel"
		}
		productHandler := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, handler)
		productHandler = humanProductMutationMiddleware(
			allowHumanCancel,
			productHandler,
		)
		productHandler = humanOperationReadAuthorizationMiddleware(
			tenantOperationAuthorizer,
			clock,
			productHandler,
		)
		productHandler = humanTenantAuthorityMiddleware(
			tenantOperationAuthorityEnabled,
			productHandler,
		)
		mux.Handle(pattern, productProtected(productHandler))
	}
	humanManagementHandleFunc := func(pattern string, handler http.HandlerFunc) {
		if stack.IdentitySessions == nil {
			mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable human sessions are not configured", nil)
			}))
			return
		}
		productHandler := humanTenantAuthorityMiddleware(productTenantReadsEnabled, handler)
		productHandler = humanProductPrincipalContextMiddleware(productHandler)
		mux.Handle(pattern, identitySessionMiddleware(stack.IdentitySessions)(productHandler))
	}
	mux.Handle(v1alphaPrefix+"/compat", CompatHandler())
	mux.HandleFunc(v1alphaPrefix+"/openapi.yaml", openAPIHandler)
	mux.HandleFunc(v1alphaPrefix+"/asyncapi.yaml", asyncAPIHandler)
	registerIdentitySessionRoutes(mux, stack.IdentitySessions, emitter)
	registerIdentityAccessRoutes(mux, stack.IdentitySessions, stack.IdentityAccess, clock, emitter)
	registerGitHubSetupRoutes(mux, stack.IdentitySessions, stack.GitHubSetup)
	protectedHandleFunc(instanceDomainsPath, instanceDomainsHandler(stack.InstanceDomainSettings, stack.AppsDomain, emitter))
	protectedHandleFunc(identityClaimPath, mutation("identity:claim", identityClaimHandler(stack.IdentityAccess, stack.IdentitySessions, stack.IdempotencyStore, stack.IdentityClaim, clock, emitter)))
	protectedHandleFunc(identityRecoveryPath, mutation("identity:recovery", identityRecoveryHandler(stack.IdentityRecovery, stack.IdentitySessions, stack.IdempotencyStore, stack.IdentityRecoveryCommit, clock, emitter)))
	// Unauthenticated on purpose: the console must know whether first-run setup
	// is required before any credential exists. See identity_claim_status.go.
	mux.HandleFunc(identityClaimStatusPath, identityClaimStatusHandler(stack.IdentityAccess))
	protectedHandleFunc(v1alphaPrefix+"/backup/runs", backupRunsHandler(backupDestinationURI, clock))
	protectedHandleFunc(v1alphaPrefix+"/backup/restore/status", backupRestoreStatusHandler(backupDestinationURI, stack.VolumeRecoveryExecutorAvailable))
	protectedHandleFunc(v1alphaPrefix+"/backup/restore/plans", backupRestorePlansHandler(backupDestinationURI))
	protectedHandleFunc(v1alphaPrefix+"/_demo/long", demoLongHandler(operationStore, stack.IdempotencyStore))
	mux.HandleFunc(v1alphaPrefix+"/_demo/health-target", demoHealthTargetHandler)
	mux.HandleFunc(v1alphaPrefix+"/_demo/schema-evidence", demoSchemaEvidenceHandler)
	mux.HandleFunc(v1alphaPrefix+"/_demo/migration-plan", demoMigrationPlanHandler)
	mux.HandleFunc(v1alphaPrefix+"/_demo/release-evidence", demoReleaseEvidenceHandler)
	mux.HandleFunc(v1alphaPrefix+"/_demo/evidence-index", demoEvidenceIndexHandler)
	mux.HandleFunc(v1alphaPrefix+"/_demo/api-data-readiness", demoAPIDataReadinessHandler(operationStore))
	protectedHandleFunc(v1alphaPrefix+"/dashboard/summary", dashboardSummaryHandler(operationStore, stack.ProjectStore, stack.ServiceStore, dashboardAuditSource))
	protectedHandleFunc(v1alphaPrefix+"/dashboard/runtime-inventory", dashboardRuntimeInventoryHandler(stack.ProjectStore, stack.ServiceStore, operationStore, registry, clock))
	protectedHandleFunc(v1alphaPrefix+"/dashboard/template-deploy", dashboardTemplateDeployHandler(stack.ProjectStore, stack.ServiceStore, stack.EnvironmentStore, operationStore, stack.QueueStore, clock))
	protectedHandleFunc(v1alphaPrefix+"/dashboard/compatibility-matrix", dashboardCompatibilityMatrixHandler)
	protectedHandleFunc(v1alphaPrefix+"/dashboard/audit-evidence", dashboardAuditEvidenceHandler(dashboardAuditSource))
	protectedHandleFunc(v1alphaPrefix+"/audit/events", dashboardAuditEventsHandler(dashboardAuditSource))
	protectedHandleFunc(v1alphaPrefix+"/audit/log-drains", dashboardAuditLogDrainsHandler(dashboardAuditSource))
	protectedHandleFunc(v1alphaPrefix+"/audit/export", dashboardAuditExportHandler(dashboardAuditSource))
	protectedHandleFunc(v1alphaPrefix+"/audit/evidence", dashboardAuditEvidenceHandler(dashboardAuditSource))
	protectedHandleFunc(v1alphaPrefix+"/dashboard/template-marketplace", dashboardTemplateMarketplaceHandler)
	if experimentalEnabled() {
		protectedHandleFunc(v1alphaPrefix+"/dashboard/notification-preview", dashboardNotificationPreviewHandler)
		protectedHandleFunc(v1alphaPrefix+"/enterprise/identity/plan", enterpriseIdentityPlanHandler)
		protectedHandleFunc(v1alphaPrefix+"/enterprise/audit/export-plan", enterpriseAuditExportPlanHandler)
		protectedHandleFunc(v1alphaPrefix+"/enterprise/controls/plan", enterpriseControlsPlanHandler)
		protectedHandleFunc(v1alphaPrefix+"/billing/metering-limits-abuse/plan", billingMeteringLimitsAbusePlanHandler)
		protectedHandleFunc(v1alphaPrefix+"/isolation/build/plan", buildIsolationEvidencePlanHandler)
		protectedHandleFunc(v1alphaPrefix+"/networking/region-routing/plan", regionRoutingPlanHandler)
		protectedHandleFunc(v1alphaPrefix+"/control-plane/ha/profile-c/readiness", profileCHAReadinessPlanHandler)
		protectedHandleFunc(v1alphaPrefix+"/release/disconnected-bundle/plan", releaseDisconnectedBundlePlanHandler)
	}
	protectedHandleFunc(v1alphaPrefix+"/operations", listOperationsHandler(operationStore, stack.CursorCodec))
	productOperationReadHandleFunc(v1alphaPrefix+"/operations/", operationsHandlerWithHub(
		operationStore,
		stack.IdempotencyStore,
		stack.QueueStore,
		hub,
		emitter,
		stack.LogStore,
		stack.CursorCodec,
		stack.TenantOperationCancel,
	))
	productReadHandleFunc(v1alphaPrefix+"/observability/logs", http.HandlerFunc(humanObservabilityProjectAuthorizationMiddleware(
		stack.IdentityAccess,
		observabilityLogsHandler(stack.ObservabilityStore, stack.CursorCodec, clock),
	).ServeHTTP))
	productReadHandleFunc(v1alphaPrefix+"/observability/logs/stream", http.HandlerFunc(humanObservabilityProjectAuthorizationMiddleware(
		stack.IdentityAccess,
		observabilityLogStreamHandler(stack.ObservabilityStore, stack.CursorCodec, clock),
	).ServeHTTP))
	productReadHandleFunc(v1alphaPrefix+"/observability/metrics", http.HandlerFunc(humanObservabilityProjectAuthorizationMiddleware(
		stack.IdentityAccess,
		observabilityMetricsHandler(stack.ObservabilityStore, clock),
	).ServeHTTP))
	productReadHandleFunc(v1alphaPrefix+"/usage/summary", usageSummaryHandler(stack.MeteringStore, clock))
	protectedHandleFunc(v1alphaPrefix+"/monitors", mutation("observability:write", monitorsHandler(stack.MonitorStore, stack.IdempotencyStore)))
	protectedHandleFunc(v1alphaPrefix+"/monitors/{monitor_id}", mutation("observability:write", monitorsHandler(stack.MonitorStore, stack.IdempotencyStore)))
	protectedHandleFunc(v1alphaPrefix+"/monitors/{monitor_id}/subscriptions", mutation("observability:write", monitorSubscriptionHandler(stack.MonitorStore, stack.IdempotencyStore)))
	protectedHandleFunc(v1alphaPrefix+"/monitor-channels", mutation("observability:write", monitorChannelsHandler(stack.MonitorStore, stack.IdempotencyStore)))
	protectedHandleFunc(v1alphaPrefix+"/monitor-incidents", monitorIncidentsHandler(stack.MonitorStore))
	protectedHandleFunc(v1alphaPrefix+"/monitor-deliveries", monitorDeliveriesHandler(stack.MonitorStore))
	protectedHandleFunc(
		v1alphaPrefix+"/monitor-deliveries/{delivery_id}/retry",
		mutation("observability:write", retryMonitorDeliveryHandler(stack.MonitorStore, stack.IdempotencyStore, clock)),
	)
	protectedHandleFunc(
		v1alphaPrefix+"/webhook-deliveries",
		webhookDeliveryCollectionHandler(stack.WebhookInbox, stack.IdentityAccess),
	)
	protectedHandleFunc(
		v1alphaPrefix+"/webhook-deliveries/{delivery_id}",
		webhookDeliveryResourceHandler(stack.WebhookInbox, stack.IdentityAccess),
	)
	protectedHandleFunc(
		v1alphaPrefix+"/webhook-deliveries/{delivery_id}/redrive",
		webhookDeliveryRedriveRoute(stack.WebhookDeliveryRedrive, stack.WebhookInbox, stack.IdentityAccess),
	)
	// mutation(...) is the uniform mutation seam (see mutation.go). It wraps a
	// route with a declared scope plus Idempotency-Key presence/format
	// enforcement so mutating requests are rejected uniformly before the
	// handler runs; the handler keeps its richer Reserve/Complete flow. It is
	// applied to the exact collection routes below, whose only mutating method
	// maps to a create/update the handler already gates on the key. The
	// trailing-slash item routes (/projects/, /services/) and the
	// deploy/up/services-github/config/operations mutation paths are left for a
	// follow-up because they multiplex sub-routes (e.g. PUT/DELETE
	// /services/{id}/github) that must not require Idempotency-Key.
	productProjectsDispatch := projectsHandler(productProjects, productEnvironments, stack.IdempotencyStore, emitter, stack.TenantMutations)
	productProjectCollectionHandleFunc(v1alphaPrefix+"/projects", mutation("projects:write", productProjectsDispatch))
	projectAuditReader, _ := stack.AuditChain.(audit.ProjectReader)
	productReadHandleFunc(v1alphaPrefix+"/projects/{project_id}/audit/events", projectAuditEventsHandler(
		projectAuditReader,
		stack.IdentityAccess,
		stack.CursorCodec,
	))
	productReadHandleFunc(v1alphaPrefix+"/projects/{project_id}", productProjectsDispatch)
	topologyHandler := topologyLayoutHandler(
		stack.TopologyLayouts,
		productProjects,
		productEnvironments,
		productServices,
		stack.IdentityAccess,
	)
	topologyProductHandler := humanProductMutationMiddleware(
		func(r *http.Request) bool { return r.Method == http.MethodPut },
		topologyHandler,
	)
	topologyProductHandler = humanTenantAuthorityMiddleware(productTenantReadsEnabled, topologyProductHandler)
	mux.Handle(
		v1alphaPrefix+"/projects/{project_id}/environments/{environment_id}/topology-layout",
		productProtected(topologyProductHandler),
	)
	humanManagementHandleFunc(
		v1alphaPrefix+"/projects/{project_id}/automation-tokens",
		mutation(
			"projects:automation-tokens:write",
			automationTokenCollectionHandler(
				stack.AutomationTokenCatalog,
				stack.IdentityAccess,
				stack.AutomationTokenMutations,
			),
		),
	)
	humanManagementHandleFunc(
		v1alphaPrefix+"/projects/{project_id}/automation-tokens/{token_id}",
		mutation(
			"projects:automation-tokens:write",
			automationTokenResourceHandler(
				stack.AutomationTokenCatalog,
				stack.IdentityAccess,
				stack.AutomationTokenMutations,
			),
		),
	)
	humanManagementHandleFunc(
		v1alphaPrefix+invitationRoute,
		mutation(
			"identity:invitations:write",
			identityInvitationCollectionHandler(
				stack.IdentityInvitationCatalog,
				stack.IdentityInvitationMutations,
				stack.CursorCodec,
			),
		),
	)
	humanManagementHandleFunc(
		v1alphaPrefix+invitationRoute+"/accept",
		mutation(
			"identity:invitations:accept",
			identityInvitationAcceptHandler(stack.IdentityInvitationMutations),
		),
	)
	humanManagementHandleFunc(
		v1alphaPrefix+invitationRoute+"/{invitation_id}/reissue",
		mutation(
			"identity:invitations:write",
			identityInvitationReissueHandler(stack.IdentityInvitationMutations),
		),
	)
	humanManagementHandleFunc(
		v1alphaPrefix+invitationRoute+"/{invitation_id}",
		mutation(
			"identity:invitations:write",
			identityInvitationResourceHandler(
				stack.IdentityInvitationCatalog,
				stack.IdentityInvitationMutations,
			),
		),
	)
	protectedHandleFunc(v1alphaPrefix+"/projects/", projectsHandler(stack.ProjectStore, stack.EnvironmentStore, stack.IdempotencyStore, emitter))
	if productTenantReadsEnabled {
		projectAccess := automationAdmissionMiddleware(stack.MutationScopeResolver, stack.DeploymentStore, projectAccessHandler(stack.ProjectStore, stack.EnvironmentStore, stack.IdentityAccess))
		mux.Handle(v1alphaPrefix+"/projects/{project_id}/access", productProtected(projectAccess))
	}
	if stack.TenantGitHubSourceLinks != nil {
		githubProjectDiscovery := tenantGitHubProjectRepositoriesHandler(stack.TenantGitHubSourceLinks)
		githubProjectHandler := humanTenantAuthorityMiddleware(
			productTenantReadsEnabled,
			githubProjectDiscovery,
		)
		mux.Handle(
			v1alphaPrefix+"/projects/{project_id}/github/repositories",
			productProtected(githubProjectHandler),
		)
		mux.Handle(
			v1alphaPrefix+"/projects/{project_id}/github/repositories/{repository_id}/branches",
			productProtected(githubProjectHandler),
		)
	}
	productDeploymentStore := stack.DeploymentStore
	if productTenantReadsEnabled && stack.DeploymentStore != nil {
		productDeploymentStore = tenantDeploymentStore{
			DeploymentStore: stack.DeploymentStore,
			access:          stack.IdentityAccess,
		}
	}
	servicesDispatch := servicesHandler(
		productServices,
		stack.IdempotencyStore,
		operationStore,
		stack.QueueStore,
		productDeploymentStore,
		stack.DeployIntakeOutbox,
		emitter,
		clock,
		stack.TenantMutations,
		stack.TenantDeployIntake,
		stack.TenantUpIntake,
		stack.SourceArtifactStore,
		stack.DomainStore,
		stack.CursorCodec,
		stack.PreviewLifecycle,
		stack.MutationScopeResolver,
	)
	githubLinks := servicesGitHubHandler(stack.ServiceStore, stack.GitHubApp, emitter)
	servicesMux := func(w http.ResponseWriter, r *http.Request) {
		// Per-service GitHub-link subroute /services/{id}/github
		if _, ok := parseServiceGitHubPath(strings.TrimPrefix(r.URL.Path, v1alphaPrefix)); ok {
			githubLinks(w, r)
			return
		}
		servicesDispatch(w, r)
	}
	productServiceCollectionHandleFunc(v1alphaPrefix+"/services", mutation("services:write", servicesDispatch))
	serviceDelete := serviceDeleteHandler(
		stack.ServiceStore,
		stack.IdempotencyStore,
		emitter,
		stack.TenantServiceDeletes,
	)
	// One registration owns both methods on the service item route: net/http
	// rejects a second pattern for the same path, and the DELETE has to sit
	// behind the mutation gate that the read-only registrar cannot provide.
	productServiceItemHandleFunc(v1alphaPrefix+"/services/{service_id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			serviceDelete(w, r)
			return
		}
		servicesDispatch(w, r)
	})
	productServiceSubrouteHandleFunc(v1alphaPrefix+"/services/", servicesMux)
	if stack.TenantGitHubSourceLinks != nil {
		githubServiceHandler := humanTenantAuthorityMiddleware(
			productTenantReadsEnabled,
			tenantGitHubServiceLinkHandler(stack.TenantGitHubSourceLinks),
		)
		mux.Handle(
			v1alphaPrefix+"/services/{service_id}/github",
			productProtected(githubServiceHandler),
		)
	}
	productServiceConfigHandleFunc(
		v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/config",
		mutation("services:write", serviceConfigHandler(
			stack.ServiceConfigStore,
			stack.IdempotencyStore,
			emitter,
			stack.MutationScopeResolver,
			stack.TenantServiceConfigs,
		)),
	)
	protectedHandleFunc(v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/cron-occurrences", cronOccurrencesHandler(stack.CronOccurrences, stack.MutationScopeResolver))
	protectedHandleFunc(v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/cron-occurrences/{occurrence_id}", cronOccurrencesHandler(stack.CronOccurrences, stack.MutationScopeResolver))
	deploymentActions := mutation(
		"services:write",
		deploymentActionHandler(
			stack.DeploymentActions,
			stack.DeploymentStore,
			stack.TenantDeploymentActions,
		),
	)
	deploymentActionProduct := automationAdmissionMiddleware(
		stack.MutationScopeResolver,
		stack.DeploymentStore,
		deploymentActions,
	)
	deploymentActionProduct = humanTenantAuthorityMiddleware(
		productTenantReadsEnabled,
		deploymentActionProduct,
	)
	mux.Handle(
		v1alphaPrefix+"/services/{service_id}/deployments/{deployment_id}/actions/{action}",
		productProtected(deploymentActionProduct),
	)
	protectedHandleFunc(v1alphaPrefix+"/variables", variablesHandler(stack.VariableStore, stack.IdempotencyStore, emitter, stack.MutationScopeResolver))
	protectedHandleFunc(v1alphaPrefix+"/variables/", variablesHandler(stack.VariableStore, stack.IdempotencyStore, emitter, stack.MutationScopeResolver))
	productTenantVariablesHandleFunc(
		v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/variables",
		mutation("variables:write", tenantVariablesHandler(stack.TenantVariables)),
	)
	productTenantVariablesHandleFunc(
		v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/variables/{variable_id}",
		mutation("variables:write", tenantVariablesHandler(stack.TenantVariables)),
	)
	productEnvironmentChangesetsHandleFunc(
		v1alphaPrefix+"/environments/{environment_id}/changeset",
		mutation("changesets:write", environmentChangesetHandler(stack.EnvironmentChangesets)),
	)
	productEnvironmentChangesetsHandleFunc(
		v1alphaPrefix+"/environments/{environment_id}/changeset/validate",
		requireScope("changesets:read", environmentChangesetHandler(stack.EnvironmentChangesets)),
	)
	for _, action := range []string{"apply", "apply-and-deploy", "discard"} {
		productEnvironmentChangesetsHandleFunc(
			v1alphaPrefix+"/environments/{environment_id}/changeset/"+action,
			mutation("changesets:write", environmentChangesetHandler(stack.EnvironmentChangesets)),
		)
	}
	productEnvironmentChangesetsHandleFunc(
		v1alphaPrefix+"/environments/{environment_id}/operation-groups/{group_id}",
		requireScope("changesets:read", environmentOperationGroupHandler(stack.EnvironmentChangesets)),
	)
	productTenantGeneratedDomainsHandleFunc(
		v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/domains",
		tenantGeneratedDomainMethodScope(tenantGeneratedDomainsHandler(stack.TenantGeneratedDomains)),
	)
	productTenantCustomDomainsHandleFunc(
		v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/custom-domains",
		tenantCustomDomainsHandler(stack.TenantCustomDomains),
	)
	productTenantCustomDomainsHandleFunc(
		v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/custom-domains/{domain_id}",
		tenantCustomDomainsHandler(stack.TenantCustomDomains),
	)
	productTenantCustomDomainsHandleFunc(
		v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/custom-domains/{domain_id}/verify",
		tenantCustomDomainsHandler(stack.TenantCustomDomains),
	)
	productTenantGeneratedDomainsHandleFunc(
		v1alphaPrefix+"/services/{service_id}/environments/{environment_id}/domains/{domain_id}",
		tenantGeneratedDomainMethodScope(tenantGeneratedDomainsHandler(stack.TenantGeneratedDomains)),
	)
	templateDepsBundle := templateDeps{
		serviceStore:          stack.ServiceStore,
		variableStore:         stack.VariableStore,
		operationStore:        operationStore,
		idem:                  stack.IdempotencyStore,
		emitter:               emitter,
		scopeResolver:         stack.MutationScopeResolver,
		provisioner:           stack.TemplateProvisioner,
		runReader:             stack.TemplateRunReader,
		communityDiscoverer:   stack.CommunityTemplateDiscoverer,
		communityForks:        stack.CommunityTemplateForks,
		identityAccess:        stack.IdentityAccess,
		serviceConfigs:        stack.ServiceConfigStore,
		environmentChangesets: stack.EnvironmentChangesets,
	}
	communityTemplateProductHandler := automationAdmissionMiddleware(
		stack.MutationScopeResolver,
		stack.DeploymentStore,
		templatesHandler(templateDepsBundle),
	)
	communityTemplateProductHandler = humanProductMutationMiddleware(
		func(r *http.Request) bool {
			return r.Method == http.MethodPost || r.Method == http.MethodPatch
		},
		communityTemplateProductHandler,
	)
	communityTemplateProductHandler = humanTenantAuthorityMiddleware(
		productTenantReadsEnabled,
		communityTemplateProductHandler,
	)
	mux.Handle(v1alphaPrefix+"/templates/community/", productProtected(communityTemplateProductHandler))
	protectedHandleFunc(v1alphaPrefix+"/templates", templatesHandler(templateDepsBundle))
	protectedHandleFunc(v1alphaPrefix+"/templates/", templatesHandler(templateDepsBundle))
	protectedHandleFunc(v1alphaPrefix+"/volumes", mutation("volumes:write", volumeHandler(stack.VolumeStore, stack.VolumeMountStore, stack.IdempotencyStore, emitter, stack.MutationScopeResolver)))
	protectedHandleFunc(v1alphaPrefix+"/volumes/", mutation("volumes:write", volumeHandler(stack.VolumeStore, stack.VolumeMountStore, stack.IdempotencyStore, emitter, stack.MutationScopeResolver)))
	protectedHandleFunc(v1alphaPrefix+"/volumes/{volume_id}/backups", mutation("volumes:write", volumeBackupsHandler(stack.VolumeRecoveryStore, stack.VolumeRecoveryExecutorAvailable, stack.IdempotencyStore, clock)))
	protectedHandleFunc(v1alphaPrefix+"/volumes/{volume_id}/restores", volumeRestoresHandler(stack.VolumeRecoveryStore))
	protectedHandleFunc(v1alphaPrefix+"/volumes/{volume_id}/backup-policies", mutation("volumes:write", volumeBackupPoliciesHandler(stack.VolumeRecoveryStore, stack.VolumeRecoveryExecutorAvailable, stack.IdempotencyStore)))
	protectedHandleFunc(v1alphaPrefix+"/volumes/{volume_id}/backup-policies/{policy_id}", mutation("volumes:write", volumeBackupPoliciesHandler(stack.VolumeRecoveryStore, stack.VolumeRecoveryExecutorAvailable, stack.IdempotencyStore)))
	protectedHandleFunc(v1alphaPrefix+"/volume-backups/{backup_id}", volumeBackupResourceHandler(stack.VolumeRecoveryStore, stack.VolumeRecoveryExecutorAvailable, stack.IdempotencyStore))
	protectedHandleFunc(v1alphaPrefix+"/volume-backups/{backup_id}/restores", mutation("volumes:write", volumeBackupResourceHandler(stack.VolumeRecoveryStore, stack.VolumeRecoveryExecutorAvailable, stack.IdempotencyStore)))
	protectedHandleFunc(v1alphaPrefix+"/volume-restores/{restore_id}", volumeRestoreResourceHandler(stack.VolumeRecoveryStore))
	protectedHandleFunc(v1alphaPrefix+"/volume-restores/{restore_id}/apply", mutation("volumes:write", volumeRestoreApplyHandler(stack.VolumeRecoveryApply)))
	protectedHandleFunc(v1alphaPrefix+"/domains", mutation("domains:write", domainsHandler(stack.DomainStore, stack.AppsDomain, stack.IdempotencyStore, emitter, stack.MutationScopeResolver)))
	protectedHandleFunc(v1alphaPrefix+"/domains/", mutation("domains:write", domainsHandler(stack.DomainStore, stack.AppsDomain, stack.IdempotencyStore, emitter, stack.MutationScopeResolver)))
	protectedHandleFunc(v1alphaPrefix+"/tcp-proxies", mutation("domains:write", tcpProxiesHandler(stack.TCPProxyStore, stack.TCPProxyBackendResolver, stack.TCPProxyBaseDomain, stack.IdempotencyStore, emitter, stack.MutationScopeResolver)))
	protectedHandleFunc(v1alphaPrefix+"/tcp-proxies/", mutation("domains:write", tcpProxiesHandler(stack.TCPProxyStore, stack.TCPProxyBackendResolver, stack.TCPProxyBaseDomain, stack.IdempotencyStore, emitter, stack.MutationScopeResolver)))
	protectedHandleFunc(v1alphaPrefix+"/config/diff", configDiffHandler(stack.ProjectStore, stack.ServiceStore))
	protectedHandleFunc(v1alphaPrefix+"/config/apply", configApplyHandler(stack.ProjectStore, stack.ServiceStore, stack.IdempotencyStore))
	productEnvironmentChangesetsHandleFunc(v1alphaPrefix+"/config/sync", configSyncHandler(stack.EnvironmentChangesets))
	if stack.EnvironmentStore != nil {
		productEnvironmentsDispatch := environmentsHandlerWithTenantMutations(
			productEnvironments,
			stack.EnvironmentDuplicator,
			stack.TenantMutations,
			stack.TenantEnvironmentUpdates,
		)
		productEnvironmentCollectionHandleFunc(
			v1alphaPrefix+"/environments",
			mutation("environments:write", productEnvironmentsDispatch),
		)
		productEnvironmentItemHandleFunc(v1alphaPrefix+"/environments/{environment_id}", productEnvironmentsDispatch)
		environmentResourceDeps := EnvironmentResourceSummaryDependencies{
			Projects:     productProjects,
			Environments: productEnvironments,
			Services:     productServices,
			Deployments:  productDeploymentStore,
			Operations:   operationStore,
		}
		if stack.EnvironmentResourceEvidence != nil {
			environmentResourceDeps.Endpoints = stack.EnvironmentResourceEvidence
			environmentResourceDeps.Replicas = stack.EnvironmentResourceEvidence
			environmentResourceDeps.Sources = stack.EnvironmentResourceEvidence
		}
		productReadHandleFunc(
			v1alphaPrefix+"/environments/{environment_id}/resources",
			EnvironmentResourceSummaryHandler(environmentResourceDeps),
		)
		protectedHandleFunc(v1alphaPrefix+"/environments/", environmentsHandler(stack.EnvironmentStore, stack.EnvironmentDuplicator))
	}
	if stack.AuditStore != nil {
		protectedHandleFunc(v1alphaPrefix+"/audit", auditListHandler(stack.AuditStore))
	}
	if stack.TimelineStore != nil {
		protectedHandleFunc(v1alphaPrefix+"/timeline", timelineListHandler(stack.TimelineStore))
	}
	if stack.Migrations != nil {
		protectedHandleFunc(v1alphaPrefix+"/migrations", migrationsHandler(stack.Migrations))
	}

	RegisterAgentHandlersWithAdmission(mux, registry, admission)

	if stack.ConversationStore == nil {
		stack.ConversationStore = conversation.NewMemoryStore(clock)
	}
	agentEnv := buildAgentEnv(stack, "agent:chat")
	conversationService, _ := conversation.NewService(conversation.Config{
		Store:        stack.ConversationStore,
		Caller:       stack.AgentCaller,
		Authorizer:   stack.ConversationScopeAuthorizer,
		Now:          clock,
		DefaultModel: stack.AgentChatModel,
		Env:          agentEnv,
	})
	if conversationService != nil {
		convDeps := AgentConversationHandlerDeps{
			Service:              conversationService,
			IdempotencyStore:     stack.IdempotencyStore,
			Now:                  clock,
			PrincipalFromRequest: stack.AgentPrincipalFromRequest,
			CursorCodec:          stack.CursorCodec,
		}
		convHandler := protected(agentConversationsHandler(convDeps))
		mux.Handle(AgentConversationsPrefix, convHandler)
		mux.Handle(AgentConversationsPrefix+"/", convHandler)
	}

	// MCP server (Track 3 of the agent surface). Mounted only when the
	// caller supplies a tool registry — production startup wires
	// tools.NewDefaultRegistry() once and reuses it for both the chat
	// backend (via AgentCaller) and the MCP wire.
	if stack.MCPRegistry != nil {
		mcpEndpoint := strings.TrimSpace(stack.MCPEndpoint)
		if mcpEndpoint == "" {
			mcpEndpoint = "/mcp/v1"
		}
		mcpEnv := stack.MCPEnv
		if mcpEnv.Services == nil && mcpEnv.Operations == nil {
			mcpEnv = buildAgentEnv(stack, "agent:mcp")
		}
		if mcpEnv.Actor == "" {
			mcpEnv.Actor = "agent:mcp"
		}
		mcpOpts := agentmcp.Options{
			Registry:           stack.MCPRegistry,
			Env:                mcpEnv,
			APIBearerToken:     operatorToken,
			RateLimitPerMinute: stack.MCPRateLimitPerMinute,
			Endpoint:           mcpEndpoint,
			DocsURL:            stack.MCPDocsURL,
			Resources:          stack.MCPResources,
			Now:                clock,
		}
		mux.Handle(mcpEndpoint, protected(agentmcp.NewServer(mcpOpts)))
		// The well-known manifest is unauthenticated by design — clients
		// must be able to discover the auth scheme before they hold a
		// token. The auth check on /mcp/v1 itself is enforced both by the
		// shared operator gate and inside agentmcp.NewServer.
		mux.Handle("/.well-known/mcp", agentmcp.NewManifestHandler(mcpOpts))
	}

	registerWebUI(mux.ServeMux, stack.InstanceDomainSettings)

	// OAuth shim. The shim is intentionally unprotected by the
	// optionalBearerTokenMiddleware because the CLI's browser arrives
	// without any bearer header; the authorize handler does its own
	// identity check (bearer header, cookie, or query param) and the
	// token handler is locked to a single-use PKCE round-trip.
	oauthStore := stack.OAuthStore
	if oauthStore == nil {
		oauthStore = oauth.NewStore(clock)
	}
	oauthCfg := oauth.HandlerConfig{Store: oauthStore, BearerToken: operatorToken}
	mux.Handle("/auth/oauth/authorize", oauth.AuthorizeHandler(oauthCfg))
	mux.Handle("/auth/oauth/token", oauth.TokenHandler(oauthCfg))

	if stack.GraphQLHandler != nil {
		// /graphql/v2 enforces its own auth headers (Authorization +
		// Project-Access-Token) inside graphql.NewHandler. The shared
		// installation gate admits either an administrator's human session or
		// the separately configured emergency recovery credential.
		mux.Handle("/graphql/v2", protected(stack.GraphQLHandler))
		mux.Handle("/graphql/v2/", protected(stack.GraphQLHandler))
	}
	if stack.GraphQLPlaygroundHandler != nil {
		// /graphiql is dev-only; gate it behind the same bearer-token
		// middleware as the dashboard so the playground is not
		// reachable on a production node that hasn't set the dev flag.
		mux.Handle("/graphiql", protected(stack.GraphQLPlaygroundHandler))
		mux.Handle("/graphiql/", protected(stack.GraphQLPlaygroundHandler))
	}

	// Top-level github installation routes.
	protectedHandleFunc(v1alphaPrefix+"/github/installations", githubLinks)
	protectedHandleFunc(v1alphaPrefix+"/github/installations/", githubLinks)
	if stack.GitHubGrantRecovery != nil {
		protectedHandleFunc(
			v1alphaPrefix+"/github/installations/{installation_id}/workspace-grant",
			githubGrantRecoveryHandler(stack.GitHubGrantRecovery),
		)
	}

	if stack.WebhookEndpoints == nil {
		stack.WebhookEndpoints = webhook.NewMemoryEndpointResolver()
	}
	if stack.WebhookDedupeStore == nil {
		stack.WebhookDedupeStore = webhook.NewMemoryDedupeStore()
	}
	if stack.WebhookDispatcher == nil {
		// Default GitHub dispatcher promotes push webhooks into deploys
		// for services with a spec.github link. Other providers fall
		// through to the no-op dispatcher.
		stack.WebhookDispatcher = newDefaultWebhookDispatcher(stack, operationStore, emitter, clock)
	}
	receiver := webhook.NewReceiver(webhook.Options{
		Resolver:   stack.WebhookEndpoints,
		Dedupe:     stack.WebhookDedupeStore,
		Inbox:      stack.WebhookInbox,
		Clock:      clock,
		Audit:      newAuditChainSink(stack.AuditChain, clock),
		Dispatcher: stack.WebhookDispatcher,
	})
	RegisterWebhookHandlers(mux, WebhookHandlerOptions{Receiver: receiver})

	return mux.ServeMux, middleware.RequestID(mux), mux.inventory
}

// newDefaultVariableStore builds the Vault used when a DataStack leaves
// VariableStore nil.
//
// In developer/test mode (productionMode == false) it seeds an in-memory KEK
// from a public dev constant. That is convenient for local work but MUST NEVER
// back a production node: the constant is public, so every sealed variable
// would be decryptable by anyone who reads the source.
//
// In production/operator mode we fail closed. The vault is built from the
// operator-managed on-disk KEK (MIDNIGHT_KEK_PATH, default /etc/midnight/kek).
// If no operator KEK is configured we panic during boot rather than silently
// falling back to the public dev constant — the node refuses to start until an
// operator provisions real key material (or wires DataStack.VariableStore
// explicitly).
func newDefaultVariableStore(productionMode bool) *secrets.Vault {
	if productionMode {
		provider, err := secrets.NewFileKeyProvider(secrets.FileKEKConfig{})
		if err != nil {
			// lint:allow-panic The router constructor cannot return an error and production must fail closed without a KEK.
			panic(fmt.Sprintf(
				"midnight: refusing to boot in production mode without an operator KEK: %v; "+
					"provision key material at %s (or set MIDNIGHT_KEK_PATH), or wire DataStack.VariableStore explicitly",
				err, secrets.DefaultKEKPath(),
			))
		}
		return secrets.NewVault(provider, secrets.AllowAll{})
	}
	// In-memory developer/test store only.
	provider, _ := secrets.NewMemoryKeyProvider([]byte("midnight-dev-variable-kek-000000"), 1)
	return secrets.NewVault(provider, secrets.AllowAll{})
}

// NewRouterWithCursorSecret constructs a router using a custom cursor signing
// secret.
func NewRouterWithCursorSecret(operationStore OperationStore, secret []byte) http.Handler {
	resources := NewMemoryResourceStore()
	return newRouterWithStores(operationStore, idempotency.NewMemoryStore(), NewCursorCodec(secret), resources, resources)
}

// newDefaultWebhookDispatcher returns the panel-locked dispatcher used
// when DataStack.WebhookDispatcher is nil. It promotes GitHub push
// webhooks into deploy operations via GitHubWebhookDispatcher; non-
// GitHub provider deliveries fall through to a no-op.
func newDefaultWebhookDispatcher(stack DataStack, operationStore OperationStore, emitter *auditEmitter, clock func() time.Time) webhook.Dispatcher {
	gh := NewGitHubWebhookDispatcher(stack.ServiceStore, operationStore, stack.QueueStore, emitter, clock, nil, stack.EnvironmentStore)
	if stack.GitHubSourceLinks != nil {
		gh.WithServiceLookup(canonicalSourceLinkLookup{store: stack.GitHubSourceLinks})
	}
	return gh.WithDeploymentStore(stack.DeploymentStore).
		WithStagedWorkOutbox(stack.StagedWorkOutbox).
		WithPreviewLifecycle(stack.PreviewLifecycle)
}

// NewDefaultGitHubWebhookDispatcherForStack constructs the same canonical
// dispatcher used by router defaults without requiring access to the router's
// request-audit emitter. Server bootstrap uses it before router construction so
// the durable inbox worker and receiver share one planner/target dispatcher.
func NewDefaultGitHubWebhookDispatcherForStack(stack DataStack) *GitHubWebhookDispatcher {
	clock := stack.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	gh := NewGitHubWebhookDispatcher(
		stack.ServiceStore,
		stack.OperationStore,
		stack.QueueStore,
		nil,
		clock,
		nil,
		stack.EnvironmentStore,
	)
	if stack.GitHubSourceLinks != nil {
		gh.WithServiceLookup(canonicalSourceLinkLookup{store: stack.GitHubSourceLinks})
	}
	return gh.WithDeploymentStore(stack.DeploymentStore).
		WithStagedWorkOutbox(stack.StagedWorkOutbox).
		WithPreviewLifecycle(stack.PreviewLifecycle)
}

func openAPIHandler(w http.ResponseWriter, r *http.Request) {
	serveYAMLContract(openapiv1alpha.MidnightYAML)(w, r)
}

func asyncAPIHandler(w http.ResponseWriter, r *http.Request) {
	serveYAMLContract(asyncapiv1.MidnightYAML)(w, r)
}

func serveYAMLContract(document []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}

		w.Header().Set("Content-Type", "application/yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(document)
	}
}

func optionalBearerTokenMiddleware(token string, productionAuthRequired bool, sessions *identity.Manager) func(http.Handler) http.Handler {
	token = strings.TrimSpace(token)
	tokenErr := auth.ValidateOperatorRecoveryToken(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A human administrator session is an independent primary authority:
			// a missing or misconfigured emergency credential must not lock a
			// valid administrator out of the installation.
			rawToken, hasBearer := singleBearerToken(r.Header)
			if hasBearer && strings.HasPrefix(rawToken, identity.SessionTokenPrefix) {
				if sessions == nil {
					w.Header().Set("WWW-Authenticate", "Bearer")
					writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session credentials are not accepted by this route", nil)
					return
				}
				principal, session, err := sessions.Authenticate(r.Context(), r.Header.Get("Authorization"))
				if err != nil {
					w.Header().Set("WWW-Authenticate", "Bearer")
					if errors.Is(err, identity.ErrSessionInvalid) {
						writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
						return
					}
					writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity service is unavailable", nil)
					return
				}
				if !principal.InstanceAdmin {
					writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "installation administrator access is required", nil)
					return
				}
				ctx := context.WithValue(r.Context(), identityPrincipalContextKey{}, principal)
				ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
				ctx = withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: principal})
				ctx = idempotency.WithActor(ctx, idempotency.UserActor(principal.UserID))
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if tokenErr != nil {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthTokenHeaderInvalid, "operator recovery token cannot use a scoped Midnight credential class", nil)
				return
			}
			if token == "" {
				if !productionAuthRequired {
					ctx := idempotency.WithActor(r.Context(), idempotency.ActorLocalDeveloper)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthRequiredMissing, "administrator sign-in or an emergency recovery credential is required in production", map[string]any{
					"reason_code":   auth.ProductionAuthMissingReason,
					"env":           "MIDNIGHT_API_BEARER_TOKEN",
					"recovery_hint": failurecodes.RecoveryHint(failurecodes.AuthRequiredMissing),
				})
				return
			}
			if bearerTokenAuthorized(r.Header, token) {
				ctx := idempotency.WithActor(r.Context(), idempotency.ActorRecoveryOperator)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthTokenHeaderInvalid, "missing or invalid bearer token", nil)
		})
	}
}

// IsInstanceAdminRequest reports whether the request carries a currently
// authenticated installation-administrator human session. The context value
// can only be installed by the server's session middleware; callers cannot
// manufacture it through headers. It is exported for sibling transports such
// as the GraphQL gateway that sit inside the same authenticated router.
func IsInstanceAdminRequest(ctx context.Context) bool {
	principal, ok := identityPrincipalFromContext(ctx)
	return ok && principal.Kind == "user" && principal.UserID != "" && principal.InstanceAdmin
}

// operatorRecoveryToken returns the configured installation-wide recovery
// credential only when it is a legacy operator secret or belongs to the admin
// class. Scoped human, service-account, agent, and MCP credentials must never
// be passed to adapters that compare or reissue the operator token directly.
func operatorRecoveryToken(token string) string {
	token = strings.TrimSpace(token)
	if err := auth.ValidateOperatorRecoveryToken(token); err != nil {
		return ""
	}
	return token
}

func bearerTokenAuthorized(header http.Header, token string) bool {
	values := header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(fields[1]), []byte(token)) == 1
}

func operationsHandler(operationStore OperationStore, idempotencyStore idempotency.Store) http.HandlerFunc {
	return operationsHandlerWithHub(operationStore, idempotencyStore, nil, nil, nil, nil, NewCursorCodec(defaultCursorSecret))
}

// operationsHandlerWithHub dispatches the /operations/{id}[...] sub-routes,
// using the supplied stream hub for live SSE fan-out when present.
func operationsHandlerWithHub(
	operationStore OperationStore,
	idempotencyStore idempotency.Store,
	queueStore queue.Store,
	hub *operationStreamHub,
	emitter *auditEmitter,
	logStore LogQueryStore,
	cursorCodec CursorCodec,
	tenantCancelCoordinators ...TenantOperationCancelCoordinator,
) http.HandlerFunc {
	var tenantCancel TenantOperationCancelCoordinator
	if len(tenantCancelCoordinators) > 0 {
		tenantCancel = tenantCancelCoordinators[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		operationID, action, ok := parseOperationPath(r.URL.Path)
		if !ok {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", nil)
			return
		}
		if action == "cancel" {
			cancelOperationHandler(
				operationStore,
				idempotencyStore,
				queueStore,
				emitter,
				tenantCancel,
			)(w, r, orailtypes.ID(operationID))
			return
		}
		if action == "fail" {
			failOperationHandler(operationStore, emitter)(w, r, orailtypes.ID(operationID))
			return
		}

		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"operation_id": operationID})
			return
		}

		if action == "events" {
			if r.URL.Query().Get("stream") == "sse" && hub != nil {
				writeOperationLiveEvents(w, r, operationStore, hub, orailtypes.ID(operationID))
				return
			}
			writeOperationEvents(w, r, operationStore, orailtypes.ID(operationID))
			return
		}
		if action == "logs" {
			writeOperationLogs(w, r, operationStore, logStore, cursorCodec, orailtypes.ID(operationID))
			return
		}

		operation, err := operationStore.Get(r.Context(), orailtypes.ID(operationID))
		if errors.Is(err, ErrOperationNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", map[string]any{"operation_id": operationID})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation lookup failed", map[string]any{"operation_id": operationID})
			return
		}
		WriteJSON(w, http.StatusOK, operation)
	}
}

type failOperationRequest struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func failOperationHandler(operationStore OperationStore, emitter *auditEmitter) func(http.ResponseWriter, *http.Request, orailtypes.ID) {
	return func(w http.ResponseWriter, r *http.Request, operationID orailtypes.ID) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"operation_id": operationID})
			return
		}

		var request failOperationRequest
		body, ok := readBoundedJSONRequestBody(w, r, "invalid fail request body")
		if !ok || !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid fail request body") {
			return
		}

		current, err := operationStore.Get(r.Context(), operationID)
		if errors.Is(err, ErrOperationNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", map[string]any{"operation_id": operationID})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation lookup failed", map[string]any{"operation_id": operationID})
			return
		}

		operation, err := operationStore.Fail(r.Context(), operationID, orailtypes.FailureCode(request.Code), request.Message)
		if errors.Is(err, ErrOperationInvalidTransition) {
			WriteInvalidOperationTransition(w, r, current.State, orailtypes.OperationFailed)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation fail failed", map[string]any{"operation_id": operationID})
			return
		}
		message := request.Message
		if message == "" {
			message = "failed"
		}
		if _, err := operationStore.AppendEvent(r.Context(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: message}); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation event append failed", map[string]any{"operation_id": operationID})
			return
		}

		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "operation",
			ResourceID:   string(operation.ID),
			Action:       "operation.fail",
			After: map[string]any{
				"state":  string(operation.State),
				"reason": message,
				"code":   string(operation.Code),
			},
		})

		writeOperationAccepted(w, operation, false)
	}
}

func cancelOperationHandler(
	operationStore OperationStore,
	idempotencyStore idempotency.Store,
	queueStore queue.Store,
	emitter *auditEmitter,
	tenantCancelCoordinators ...TenantOperationCancelCoordinator,
) func(http.ResponseWriter, *http.Request, orailtypes.ID) {
	var tenantCancel TenantOperationCancelCoordinator
	if len(tenantCancelCoordinators) > 0 {
		tenantCancel = tenantCancelCoordinators[0]
	}
	return func(w http.ResponseWriter, r *http.Request, operationID orailtypes.ID) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"operation_id": operationID})
			return
		}
		if principal, ok := productPrincipalFromContext(r.Context()); ok &&
			principal.Kind == productPrincipalHuman {
			cancelTenantOperation(w, r, operationID, tenantCancel)
			return
		}

		key := idempotency.Key(r.Header.Get("Idempotency-Key"))
		var reservation idempotency.Reservation
		var err error
		if key != "" {
			body, ok := readBoundedJSONRequestBody(w, r, "operation cancel accepts no fields")
			if !ok {
				return
			}
			var request struct{}
			if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "operation cancel accepts no fields") {
				return
			}
			fingerprint := idempotencyRequestFingerprintForRoute(
				r,
				v1alphaPrefix+"/operations/{operation_id}/cancel",
				idempotency.BodyHashSHA256(body),
			)
			reservation, err = idempotencyStore.Reserve(r.Context(), key, fingerprint, 24*time.Hour)
			if errors.Is(err, idempotency.ErrInvalidKey) {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", map[string]any{"operation_id": operationID})
				return
			}
			if errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
				writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", map[string]any{"operation_id": operationID})
				return
			}
			if errors.Is(err, idempotency.ErrKeyReused) {
				writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", map[string]any{"operation_id": operationID})
				return
			}
			if errors.Is(err, idempotency.ErrRequestInProgress) {
				writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", map[string]any{"operation_id": operationID})
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency reservation failed", map[string]any{"operation_id": operationID})
				return
			}
			if reservationHasExactStoredResponse(reservation) {
				stored := reservation.Response.Clone()
				w.Header().Set("Idempotent-Replayed", "true")
				writeStoredResponse(w, stored)
				return
			}
			if reservation.OperationID != "" {
				operation, err := operationStore.Get(r.Context(), orailtypes.ID(reservation.OperationID))
				if err != nil {
					writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation replay failed", map[string]any{"operation_id": reservation.OperationID})
					return
				}
				emitter.emit(r.Context(), audit.Row{
					ResourceKind: "operation",
					ResourceID:   string(operation.ID),
					Action:       "operation.cancel.replay",
					Attrs: map[string]any{
						"idempotency_key": string(key),
					},
				})
				writeOperationAccepted(w, operation, true)
				return
			}
			capture := newIdempotencyResponseCapture(w)
			w = capture
			defer capture.Complete(r, idempotencyStore, reservation.Reference())
		}

		current, err := operationStore.Get(r.Context(), operationID)
		if errors.Is(err, ErrOperationNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", map[string]any{"operation_id": operationID})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation lookup failed", map[string]any{"operation_id": operationID})
			return
		}

		operation, message, action, auditAfter, handled, err := cancelDeployOperation(r.Context(), operationStore, queueStore, current)
		if errors.Is(err, errDeployCancelQueueUnavailable) {
			writeRequestError(w, r, http.StatusConflict, failurecodes.DeployCancelQueueUnavailable, "deploy queued job cannot be canceled atomically", map[string]any{
				"operation_id": operationID,
				"state":        current.State,
				"required":     "queue cancellation before lease",
			})
			return
		}
		if errors.Is(err, ErrOperationInvalidTransition) {
			WriteInvalidOperationTransition(w, r, current.State, orailtypes.OperationCanceled)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deploy cancel failed", map[string]any{"operation_id": operationID})
			return
		}
		if !handled {
			operation, err = operationStore.Transition(r.Context(), operationID, orailtypes.OperationCanceled)
			if errors.Is(err, ErrOperationInvalidTransition) {
				WriteInvalidOperationTransition(w, r, current.State, orailtypes.OperationCanceled)
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation cancel failed", map[string]any{"operation_id": operationID})
				return
			}
			message = "canceled"
			action = "operation.cancel"
			auditAfter = map[string]any{
				"state": string(operation.State),
			}
		}
		if _, err := operationStore.AppendEvent(r.Context(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: message}); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation event append failed", map[string]any{"operation_id": operationID})
			return
		}
		if key != "" {
			ref := reservation.Reference()
			if _, err := idempotencyStore.AttachOperation(r.Context(), ref, string(operation.ID)); err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency operation attach failed", map[string]any{"operation_id": operationID})
				return
			}
		}

		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "operation",
			ResourceID:   string(operation.ID),
			Action:       action,
			Before: map[string]any{
				"state": string(current.State),
			},
			After: auditAfter,
		})

		writeOperationAccepted(w, operation, false)
	}
}

var errDeployCancelQueueUnavailable = errors.New("deploy cancel queue unavailable")

func cancelDeployOperation(ctx context.Context, operationStore OperationStore, queueStore queue.Store, current orailtypes.Operation) (orailtypes.Operation, string, string, map[string]any, bool, error) {
	if current.Kind != "deploy" {
		return orailtypes.Operation{}, "", "", nil, false, nil
	}
	switch current.State {
	case orailtypes.OperationQueued:
		canceler, ok := queueStore.(queue.QueuedCancelStore)
		if queueStore == nil || !ok {
			return orailtypes.Operation{}, "", "", nil, true, errDeployCancelQueueUnavailable
		}
		result, err := canceler.CancelQueued(ctx, queue.CancelQueuedRequest{
			Queue:             queue.Deploy,
			ParentOperationID: string(current.ID),
			Reason:            queue.ReasonDeployCanceledBeforeLease,
		})
		if errors.Is(err, queue.ErrJobNotFound) {
			return orailtypes.Operation{}, "", "", nil, true, errDeployCancelQueueUnavailable
		}
		if err != nil {
			return orailtypes.Operation{}, "", "", nil, true, err
		}
		if result.Canceled || (result.AlreadyTerminal && result.Job.State == queue.JobCanceled) {
			operation, err := operationStore.Transition(ctx, current.ID, orailtypes.OperationCanceled)
			if err != nil {
				return orailtypes.Operation{}, "", "", nil, true, err
			}
			message := deployCancelBeforeLeaseMessage(result.Job)
			return operation, message, "operation.cancel", map[string]any{
				"state":       string(operation.State),
				"job_id":      result.Job.ID,
				"job_state":   string(result.Job.State),
				"reason_code": queue.ReasonDeployCanceledBeforeLease,
			}, true, nil
		}
		if result.AlreadyLeased || result.Job.State == queue.JobLeased {
			message := deployCancelSafePointMessage(result.Job)
			return current, message, "operation.cancel.request", map[string]any{
				"state":            string(current.State),
				"cancel_requested": true,
				"job_id":           result.Job.ID,
				"job_state":        string(result.Job.State),
				"safe_point":       "before-traffic-promotion",
			}, true, nil
		}
		return orailtypes.Operation{}, "", "", nil, true, errDeployCancelQueueUnavailable
	case orailtypes.OperationRunning:
		message := "cancel requested: running deploy will stop only at worker safe point before traffic promotion"
		return current, message, "operation.cancel.request", map[string]any{
			"state":            string(current.State),
			"cancel_requested": true,
			"safe_point":       "before-traffic-promotion",
		}, true, nil
	default:
		return orailtypes.Operation{}, "", "", nil, false, nil
	}
}

func deployCancelBeforeLeaseMessage(job queue.Job) string {
	message := "canceled: queued deploy job canceled before lease"
	if job.ID != "" {
		message += " job_id=" + job.ID
	}
	message += " reason_code=" + queue.ReasonDeployCanceledBeforeLease
	return message
}

func deployCancelSafePointMessage(job queue.Job) string {
	message := "cancel requested: deploy job already leased; worker will stop only at safe point before traffic promotion"
	if job.ID != "" {
		message += " job_id=" + job.ID
	}
	if job.LeaseID != "" {
		message += " lease_id=" + job.LeaseID
	}
	return message
}

func listOperationsHandler(operationStore OperationStore, cursorCodec CursorCodec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}

		limit, err := parseStrictPageLimit(r.URL.Query().Get("limit"))
		if errors.Is(err, orailtypes.ErrPageLimitExceeded) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.PaginationLimitExceeded, "page limit exceeds the locked maximum", map[string]any{"max": orailtypes.MaxPageLimit})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid page limit", nil)
			return
		}

		filter, err := parseOperationListFilter(r)
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid operation list filter", nil)
			return
		}

		binding := operationListCursorBinding(r.Context(), filter, limit)
		var after *OperationListCursor
		if rawCursor := r.URL.Query().Get("cursor"); rawCursor != "" {
			position, err := cursorCodec.DecodeOperationCursor(rawCursor, binding, time.Now().UTC())
			if err != nil {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid cursor", nil)
				return
			}
			after = &position
		}

		pageStore, ok := operationStore.(OperationPageStore)
		if !ok {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation pagination is unavailable", nil)
			return
		}
		page, err := pageStore.ListPage(r.Context(), filter, after, limit)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation list failed", nil)
			return
		}
		items := make([]orailtypes.Operation, 0, len(page.Records))
		for _, record := range page.Records {
			items = append(items, record.Operation)
		}

		var nextCursor *string
		if page.HasMore {
			if len(page.Records) == 0 {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation pagination returned an invalid page", nil)
				return
			}
			last := page.Records[len(page.Records)-1]
			next, err := cursorCodec.EncodeOperationCursor(OperationListCursor{
				CreatedAt: last.CreatedAt,
				ID:        last.Operation.ID,
			}, binding, time.Now().UTC())
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "cursor encode failed", nil)
				return
			}
			nextCursor = &next
		}

		WriteJSON(w, http.StatusOK, orailtypes.CursorPageResponse[orailtypes.Operation]{
			Data: items,
			Page: orailtypes.CursorPageDTO{NextCursor: nextCursor},
		})
	}
}

func parseOperationListFilter(r *http.Request) (OperationListFilter, error) {
	filter := OperationListFilter{Kind: r.URL.Query().Get("kind")}
	if rawStatus := r.URL.Query().Get("status"); rawStatus != "" {
		status := orailtypes.OperationState(strings.ToUpper(rawStatus))
		if status == orailtypes.OperationState("PENDING") {
			status = orailtypes.OperationQueued
		}
		if err := (orailtypes.Operation{ID: "filter", State: status}).Validate(); err != nil {
			return OperationListFilter{}, err
		}
		filter.Status = &status
	}
	return filter, nil
}

func parsePageLimit(raw string) (int, bool, error) {
	if raw == "" {
		return orailtypes.NormalizePageLimit(0)
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, orailtypes.ErrInvalidCursorPage
	}
	return orailtypes.NormalizePageLimit(limit)
}

// parseStrictPageLimit normalizes the ?limit= query parameter and returns
// orailtypes.ErrPageLimitExceeded when the requested value is above the
// locked maximum so callers can surface PAGINATION_LIMIT_EXCEEDED.
func parseStrictPageLimit(raw string) (int, error) {
	if raw == "" {
		return orailtypes.NormalizePageLimitStrict(0)
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, orailtypes.ErrInvalidCursorPage
	}
	return orailtypes.NormalizePageLimitStrict(limit)
}

func demoLongHandler(operationStore OperationStore, idempotencyStore idempotency.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}

		key := idempotency.Key(r.Header.Get("Idempotency-Key"))
		if err := idempotency.ValidateKey(key); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
			return
		}

		body, ok := readBoundedJSONRequestBody(w, r, "demo long request accepts no fields")
		if !ok {
			return
		}
		fingerprint := idempotencyRequestFingerprintForRoute(
			r,
			v1alphaPrefix+"/_demo/long",
			idempotency.BodyHashSHA256(body),
		)
		reservation, err := idempotencyStore.Reserve(r.Context(), key, fingerprint, 24*time.Hour)
		if errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
			writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
			return
		}
		if errors.Is(err, idempotency.ErrKeyReused) {
			writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
			return
		}
		if errors.Is(err, idempotency.ErrRequestInProgress) {
			writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency reservation failed", nil)
			return
		}
		if reservationHasExactStoredResponse(reservation) {
			stored := reservation.Response.Clone()
			w.Header().Set("Idempotent-Replayed", "true")
			writeStoredResponse(w, stored)
			return
		}
		if reservation.OperationID != "" {
			operation, err := operationStore.Get(r.Context(), orailtypes.ID(reservation.OperationID))
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation replay failed", map[string]any{"operation_id": reservation.OperationID})
				return
			}
			writeOperationAccepted(w, operation, true)
			return
		}
		capture := newIdempotencyResponseCapture(w)
		w = capture
		defer capture.Complete(r, idempotencyStore, reservation.Reference())

		operation, err := CreateQueuedOperation(r.Context(), operationStore, "")
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation create failed", nil)
			return
		}
		ref := reservation.Reference()
		if _, err := idempotencyStore.AttachOperation(r.Context(), ref, string(operation.ID)); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency operation attach failed", map[string]any{"operation_id": operation.ID}) //nolint:errcheck
			return
		}
		writeOperationAccepted(w, operation, false)
	}
}

func demoHealthTargetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}

	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	WriteJSON(w, http.StatusOK, ReviewTargetHealth("demo-api", true, now.Add(-time.Minute), func() time.Time { return now }))
}

func demoSchemaEvidenceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}

	report, err := db.BuildSchemaEvidence(db.MigrationManifest{
		SchemaVersion: 1,
		Migrations: []db.Migration{{
			Version: 1,
			Name:    "alpha_core",
			Up:      "0001_alpha_core.up.sql",
			Down:    "0001_alpha_core.down.sql",
		}},
	}, []int{1})
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "schema evidence build failed", nil)
		return
	}
	demoSQL := `
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY);
CREATE INDEX IF NOT EXISTS idx_operations_state ON operations(state);
`
	demoDownSQL := `
DROP TABLE IF EXISTS operations;
DROP TABLE IF EXISTS schema_migrations;
`
	report.AddSourceFileEvidence("0001_alpha_core.down.sql", []byte(demoDownSQL))
	report.AddSourceFileEvidence("0001_alpha_core.up.sql", []byte(demoSQL))
	report.AddSQLSubsetEvidence(demoSQL)
	data, err := db.RenderSchemaEvidenceJSON(report)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "schema evidence render failed", nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func demoMigrationPlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}

	plan, err := db.BuildMigrationExecutionPlan(db.MigrationManifest{
		SchemaVersion: 2,
		Migrations: []db.Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "beta_core", Up: "0002_beta_core.up.sql", Down: "0002_beta_core.down.sql"},
		},
	}, []int{1})
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "migration plan build failed", nil)
		return
	}
	data, err := db.RenderMigrationExecutionPlanJSON(plan)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "migration plan render failed", nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func demoReleaseEvidenceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}

	manifest := db.MigrationManifest{
		SchemaVersion: 2,
		Migrations: []db.Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "beta_core", Up: "0002_beta_core.up.sql", Down: "0002_beta_core.down.sql", UpChecksum: "sha256:placeholder"},
		},
	}
	schema, err := db.BuildSchemaEvidence(manifest, []int{1})
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "schema evidence build failed", nil)
		return
	}
	schema.AddSQLSubsetEvidence(`
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY);
`)
	plan, err := db.BuildMigrationExecutionPlan(manifest, []int{1})
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "migration plan build failed", nil)
		return
	}
	data, err := db.RenderReleaseEvidencePacketJSON(db.BuildReleaseEvidencePacket(schema, plan))
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "release evidence render failed", nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func demoEvidenceIndexHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}

	manifest := db.MigrationManifest{
		SchemaVersion: 2,
		Migrations: []db.Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "beta_core", Up: "0002_beta_core.up.sql", Down: "0002_beta_core.down.sql", UpChecksum: "sha256:placeholder"},
		},
	}
	schema, err := db.BuildSchemaEvidence(manifest, []int{1})
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "schema evidence build failed", nil)
		return
	}
	schema.AddSQLSubsetEvidence(`
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY);
`)
	plan, err := db.BuildMigrationExecutionPlan(manifest, []int{1})
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "migration plan build failed", nil)
		return
	}
	packet := db.BuildReleaseEvidencePacket(schema, plan)
	index, err := db.BuildAPISchemaEvidenceIndex(db.APISchemaEvidenceIndexInput{
		SchemaEvidencePath:  "artifacts/phase0/schema-evidence.json",
		SchemaEvidence:      schema,
		MigrationPlanPath:   "artifacts/phase0/migration-plan.json",
		MigrationPlan:       plan,
		ReleaseEvidencePath: "artifacts/phase0/release-evidence.json",
		ReleaseEvidence:     packet,
		OpenAPISpecPath:     "api/openapi/v1alpha/midnight.yaml",
	})
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "evidence index build failed", nil)
		return
	}
	data, err := db.RenderAPISchemaEvidenceIndexJSON(index)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "evidence index render failed", nil)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func demoAPIDataReadinessHandler(operationStore OperationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}

		manifest := db.MigrationManifest{
			SchemaVersion: 2,
			Migrations: []db.Migration{
				{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
				{Version: 2, Name: "beta_core", Up: "0002_beta_core.up.sql", Down: "0002_beta_core.down.sql", UpChecksum: "sha256:placeholder"},
			},
		}
		schema, err := db.BuildSchemaEvidence(manifest, []int{1})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "schema evidence build failed", nil)
			return
		}
		schema.AddSQLSubsetEvidence(`
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY);
`)
		plan, err := db.BuildMigrationExecutionPlan(manifest, []int{1})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "migration plan build failed", nil)
			return
		}
		packet := db.BuildReleaseEvidencePacket(schema, plan)
		index, err := db.BuildAPISchemaEvidenceIndex(db.APISchemaEvidenceIndexInput{
			SchemaEvidencePath:  "artifacts/phase0/schema-evidence.json",
			SchemaEvidence:      schema,
			MigrationPlanPath:   "artifacts/phase0/migration-plan.json",
			MigrationPlan:       plan,
			ReleaseEvidencePath: "artifacts/phase0/release-evidence.json",
			ReleaseEvidence:     packet,
			OpenAPISpecPath:     "api/openapi/v1alpha/midnight.yaml",
		})
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "evidence index build failed", nil)
			return
		}

		WriteJSON(w, http.StatusOK, BuildAPIDataReadinessSummary(r.Context(), APIDataReadinessInput{
			ReleaseEvidence: packet,
			EvidenceIndex:   index,
			OperationStore:  operationStore,
		}))
	}
}

func writeOperationAccepted(w http.ResponseWriter, operation orailtypes.Operation, replayed bool) {
	w.Header().Set("Location", v1alphaPrefix+"/operations/"+string(operation.ID))
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	WriteJSON(w, http.StatusAccepted, operation)
}

// writeOperationLiveEvents replays durable events and then follows the
// per-operation pub/sub feed. Subscription happens before the durable read:
// an append racing with that read is therefore present either in the replay,
// in the subscription, or both. Sequence de-duplication makes the overlap
// harmless and Last-Event-ID makes a disconnected stream resumable.
func writeOperationLiveEvents(w http.ResponseWriter, r *http.Request, operationStore OperationStore, hub *operationStreamHub, operationID orailtypes.ID) {
	after, err := operationEventsResumeSequence(r)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid event cursor", map[string]any{"operation_id": operationID})
		return
	}
	accessGuard, guarded := operationStreamAccessGuardFromContext(r.Context())
	if guarded {
		if err := accessGuard.Check(r.Context()); err != nil {
			if errors.Is(err, ErrOperationNotFound) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", map[string]any{"operation_id": operationID})
				return
			}
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "operation authorization is unavailable", map[string]any{"operation_id": operationID})
			return
		}
	}

	subscriber := hub.Subscribe(r.Context(), operationID, 32)
	defer hub.Unsubscribe(operationID, subscriber)

	events, err := operationStore.ListEvents(r.Context(), operationID)
	if errors.Is(err, ErrOperationNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", map[string]any{"operation_id": operationID})
		return
	} else if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation events lookup failed", map[string]any{"operation_id": operationID})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	nextSequence := after
	var lastState orailtypes.OperationState
	for _, event := range events {
		if guarded {
			if err := accessGuard.Check(r.Context()); err != nil {
				return
			}
		}
		lastState = event.State
		if event.Seq <= nextSequence {
			continue
		}
		if event.Seq != nextSequence+1 {
			// A durable gap must never be papered over by emitting a later
			// event. Closing lets the client retry from the last contiguous
			// sequence while operators investigate the corrupt history.
			return
		}
		if err := writeOperationEventSSE(w, event); err != nil {
			return
		}
		nextSequence = event.Seq
		if flusher != nil {
			flusher.Flush()
		}
	}
	if flusher != nil {
		flusher.Flush()
	}
	if isTerminalOperationState(lastState) {
		return
	}

	var revalidation <-chan time.Time
	var revalidationTicker *time.Ticker
	if guarded {
		interval := accessGuard.Interval
		if interval <= 0 {
			interval = tenantOperationStreamRevalidationInterval
		}
		revalidationTicker = time.NewTicker(interval)
		defer revalidationTicker.Stop()
		revalidation = revalidationTicker.C
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-revalidation:
			if err := accessGuard.Check(r.Context()); err != nil {
				return
			}
		case event, ok := <-subscriber.Events():
			if !ok {
				return
			}
			if guarded {
				if err := accessGuard.Check(r.Context()); err != nil {
					return
				}
			}
			if event.Seq <= nextSequence {
				continue
			}
			if event.Seq != nextSequence+1 {
				// Concurrent appenders can commit in sequence order but
				// publish in the opposite order. Recover the contiguous
				// durable history before writing the newer live event.
				replay, replayErr := operationStore.ListEvents(r.Context(), operationID)
				if replayErr != nil {
					return
				}
				for _, durable := range replay {
					if guarded {
						if err := accessGuard.Check(r.Context()); err != nil {
							return
						}
					}
					lastState = durable.State
					if durable.Seq <= nextSequence {
						continue
					}
					if durable.Seq != nextSequence+1 {
						return
					}
					if err := writeOperationEventSSE(w, durable); err != nil {
						return
					}
					nextSequence = durable.Seq
					if flusher != nil {
						flusher.Flush()
					}
				}
				if isTerminalOperationState(lastState) {
					return
				}
				if event.Seq <= nextSequence {
					continue
				}
				if event.Seq != nextSequence+1 {
					return
				}
			}
			if err := writeOperationEventSSE(w, event); err != nil {
				return
			}
			nextSequence = event.Seq
			if flusher != nil {
				flusher.Flush()
			}
			if isTerminalOperationState(event.State) {
				return
			}
		}
	}
}

func writeOperationEvents(w http.ResponseWriter, r *http.Request, operationStore OperationStore, operationID orailtypes.ID) {
	after, err := operationEventsResumeSequence(r)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid event cursor", map[string]any{"operation_id": operationID})
		return
	}

	events, err := operationStore.ListEvents(r.Context(), operationID)
	if errors.Is(err, ErrOperationNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.OperationNotFound, "operation not found", map[string]any{"operation_id": operationID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation events lookup failed", map[string]any{"operation_id": operationID})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	nextSequence := after
	for _, event := range events {
		if event.Seq <= after {
			if event.Seq > nextSequence {
				nextSequence = event.Seq
			}
			continue
		}
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if event.Seq > nextSequence {
			nextSequence = event.Seq
		}
		_, _ = fmt.Fprintf(w, "id: %d\n", event.Seq)
		_, _ = w.Write([]byte("event: operation\n"))
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(data)
		_, _ = w.Write([]byte("\n\n"))
	}
	data, err := json.Marshal(operationEventsCursor{NextSequence: nextSequence})
	if err == nil {
		_, _ = w.Write([]byte("event: cursor\n"))
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(data)
		_, _ = w.Write([]byte("\n\n"))
	}
}

type operationEventsCursor struct {
	NextSequence int `json:"next_sequence"`
}

func writeOperationEventSSE(w io.Writer, event OperationEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: operation\ndata: ", event.Seq); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

// operationEventsResumeSequence gives the standard SSE Last-Event-ID header
// precedence over the query cursor used by non-EventSource clients.
func operationEventsResumeSequence(r *http.Request) (int, error) {
	if lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID")); lastEventID != "" {
		return parseOperationEventsAfter(lastEventID)
	}
	return parseOperationEventsAfter(r.URL.Query().Get("after"))
}

func parseOperationEventsAfter(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	after, err := strconv.Atoi(raw)
	if err != nil || after < 0 {
		return 0, orailtypes.ErrInvalidCursorPage
	}
	return after, nil
}

func parseOperationPath(path string) (operationID string, action string, ok bool) {
	rest := strings.TrimPrefix(path, v1alphaPrefix+"/operations/")
	if rest == "" || rest == path {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		return "", "", false
	}
	if len(parts) == 1 {
		return parts[0], "", true
	}
	if len(parts) == 2 && parts[1] == "events" {
		return parts[0], "events", true
	}
	if len(parts) == 2 && parts[1] == "logs" {
		return parts[0], "logs", true
	}
	if len(parts) == 2 && parts[1] == "cancel" {
		return parts[0], "cancel", true
	}
	if len(parts) == 2 && parts[1] == "fail" {
		return parts[0], "fail", true
	}
	return "", "", false
}

func writeRequestError(w http.ResponseWriter, r *http.Request, status int, code failurecodes.Code, message string, details map[string]any) {
	WriteError(w, status, code, message, middleware.FromContext(r.Context()), details)
}

func WriteInvalidOperationTransition(w http.ResponseWriter, r *http.Request, from, to orailtypes.OperationState) {
	writeRequestError(w, r, http.StatusConflict, failurecodes.OperationInvalidTransition, "invalid operation state transition", map[string]any{
		"from": from,
		"to":   to,
	})
}
