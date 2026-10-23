// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	midnightdomain "github.com/midnight/midnight/internal/domain"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/internal/monitors"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/internal/tcpproxy"
	templatepkg "github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/internal/timeline"
	"github.com/midnight/midnight/internal/topology"
	"github.com/midnight/midnight/internal/variableintent"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/internal/webhook"
)

// PostgresDataStackConfig is the production boot shape for the v1alpha router.
type PostgresDataStackConfig struct {
	Pool            *pg.Pool
	MigrationsFS    fs.FS
	ManifestPath    string
	CursorSecret    []byte
	AgentHMACSecret []byte
	// AutomationTokenMACKey is the independent stable key used to derive
	// verifier-only project automation-token hashes. It must not reuse cursor,
	// identity-session, agent, operator, or envelope-encryption key material.
	AutomationTokenMACKey []byte
	// InvitationMACKey is the independent stable key used to derive
	// verifier-only identity invitation hashes.
	InvitationMACKey []byte
	// VariableIntentMACKey is the independent stable key used to fingerprint
	// value-bearing tenant variable requests without persisting a guessable
	// unkeyed body digest.
	VariableIntentMACKey []byte
	// VariableKeyProvider owns the KEK used to wrap per-variable DEKs.
	// Production supplies a file/KMS-backed provider and startup fails closed
	// when it is absent.
	VariableKeyProvider secrets.KeyProvider
	// SourceArtifactRoot is the absolute shared filesystem root used by the
	// API's content-addressed upload store and the build worker materializer.
	// Empty keeps tenant `up` honestly unavailable for embedders/tests that do
	// not configure shared durable storage.
	SourceArtifactRoot string
	// SourceArtifactQuota is the strict workspace logical-storage and
	// multi-replica inflight admission policy for human source uploads. The
	// zero value selects sourceartifact.DefaultQuotaConfig for embedders.
	SourceArtifactQuota sourceartifact.QuotaConfig
	AppsDomain          string
	TCPProxyBaseDomain  string
	TCPProxyPorts       tcpproxy.PortRange
	Clock               func() time.Time
}

// NewPostgresDataStack wires the Postgres-backed stores that are available
// today. Fields without a durable adapter still fall back inside the router.
func NewPostgresDataStack(cfg PostgresDataStackConfig) (DataStack, error) {
	if cfg.Pool == nil {
		return DataStack{}, errors.New("postgres data stack: pool is required")
	}
	if cfg.MigrationsFS == nil {
		return DataStack{}, errors.New("postgres data stack: migrations FS is required")
	}
	if len(cfg.CursorSecret) < MinCursorSecretBytes {
		return DataStack{}, fmt.Errorf("postgres data stack: cursor secret yields %d bytes, want >= %d", len(cfg.CursorSecret), MinCursorSecretBytes)
	}
	if len(cfg.AutomationTokenMACKey) < automationtoken.MinMACKeyBytes {
		return DataStack{}, fmt.Errorf(
			"postgres data stack: automation token MAC key yields %d bytes, want >= %d",
			len(cfg.AutomationTokenMACKey),
			automationtoken.MinMACKeyBytes,
		)
	}
	if len(cfg.InvitationMACKey) < identity.InvitationMinMACBytes {
		return DataStack{}, fmt.Errorf(
			"postgres data stack: invitation MAC key yields %d bytes, want >= %d",
			len(cfg.InvitationMACKey),
			identity.InvitationMinMACBytes,
		)
	}
	if len(cfg.VariableIntentMACKey) < variableintent.MinMACKeyBytes {
		return DataStack{}, fmt.Errorf(
			"postgres data stack: variable intent MAC key yields %d bytes, want >= %d",
			len(cfg.VariableIntentMACKey),
			variableintent.MinMACKeyBytes,
		)
	}
	if hmac.Equal(cfg.AutomationTokenMACKey, cfg.CursorSecret) {
		return DataStack{}, errors.New("postgres data stack: automation token MAC key must be independent from the cursor secret")
	}
	if len(cfg.AgentHMACSecret) > 0 && hmac.Equal(cfg.AutomationTokenMACKey, cfg.AgentHMACSecret) {
		return DataStack{}, errors.New("postgres data stack: automation token MAC key must be independent from the agent HMAC secret")
	}
	if hmac.Equal(cfg.InvitationMACKey, cfg.CursorSecret) ||
		hmac.Equal(cfg.InvitationMACKey, cfg.AutomationTokenMACKey) ||
		(len(cfg.AgentHMACSecret) > 0 && hmac.Equal(cfg.InvitationMACKey, cfg.AgentHMACSecret)) {
		return DataStack{}, errors.New("postgres data stack: invitation MAC key must be independent from cursor, automation-token, and agent secrets")
	}
	if hmac.Equal(cfg.VariableIntentMACKey, cfg.CursorSecret) ||
		hmac.Equal(cfg.VariableIntentMACKey, cfg.AutomationTokenMACKey) ||
		hmac.Equal(cfg.VariableIntentMACKey, cfg.InvitationMACKey) ||
		(len(cfg.AgentHMACSecret) > 0 && hmac.Equal(cfg.VariableIntentMACKey, cfg.AgentHMACSecret)) {
		return DataStack{}, errors.New("postgres data stack: variable intent MAC key must be independent from cursor, automation-token, invitation, and agent secrets")
	}
	if cfg.VariableKeyProvider == nil {
		return DataStack{}, errors.New("postgres data stack: variable key provider is required")
	}
	if appsDomain := strings.TrimSpace(cfg.AppsDomain); appsDomain != "" {
		normalized, err := midnightdomain.NormalizeHostname(appsDomain)
		if err != nil {
			return DataStack{}, fmt.Errorf("postgres data stack: apps domain: %w", err)
		}
		cfg.AppsDomain = normalized
	}
	instanceDomainSettings := &PostgresInstanceDomainSettingsStore{Pool: cfg.Pool}
	if cfg.Pool.Inner() != nil {
		persistedDomains, seedErr := seedInstanceDomainSettings(context.Background(), instanceDomainSettings, cfg.AppsDomain)
		if seedErr == nil {
			cfg.AppsDomain = persistedDomains.AppsDomain
		} else if !errors.Is(seedErr, ErrInstanceDomainsNotConfigured) {
			return DataStack{}, fmt.Errorf("postgres data stack: instance domains: %w", seedErr)
		}
	}

	resources := NewPostgresResourceStore(cfg.Pool)
	identityStore := identity.NewPostgresStore(cfg.Pool)
	idempotencyStore := idempotency.NewPostgresStoreWithSensitiveResponseKeys(
		cfg.Pool,
		cfg.VariableKeyProvider,
	)
	if cfg.Clock != nil {
		idempotencyStore.Now = cfg.Clock
	}
	identitySessions, err := identity.NewManager(identity.ManagerConfig{
		Store:       identityStore,
		KeyProvider: cfg.VariableKeyProvider,
		MACKey:      deriveIdentitySessionMACKey(cfg.CursorSecret),
		Clock:       cfg.Clock,
	})
	if err != nil {
		return DataStack{}, fmt.Errorf("postgres data stack: identity sessions: %w", err)
	}
	automationTokenStore := automationtoken.NewPostgresStore(cfg.Pool)
	automationTokens, err := automationtoken.NewManager(automationtoken.ManagerConfig{
		Store:  automationTokenStore,
		MACKey: cfg.AutomationTokenMACKey,
		Clock:  cfg.Clock,
	})
	if err != nil {
		return DataStack{}, fmt.Errorf("postgres data stack: automation tokens: %w", err)
	}
	identityInvitations, err := identity.NewInvitationService(identity.InvitationServiceConfig{
		Store:  identityStore,
		MACKey: cfg.InvitationMACKey,
		Clock:  cfg.Clock,
	})
	if err != nil {
		return DataStack{}, fmt.Errorf("postgres data stack: identity invitations: %w", err)
	}
	environmentDuplicator := NewPostgresEnvironmentDuplicator(cfg.Pool, cfg.VariableKeyProvider)
	if cfg.Clock != nil {
		environmentDuplicator.Now = cfg.Clock
	}
	auditChain := audit.NewPostgresChain(cfg.Pool)
	if cfg.Clock != nil {
		auditChain.SetClock(cfg.Clock)
	}
	identityClaim := NewPostgresIdentityClaimCoordinator(
		cfg.Pool,
		identityStore,
		idempotencyStore,
		auditChain,
	)
	identityRecovery := NewPostgresIdentityRecoveryCoordinator(
		cfg.Pool,
		identityStore,
		idempotencyStore,
		auditChain,
	)
	tenantMutations := NewPostgresTenantMutationCoordinator(cfg.Pool, auditChain, cfg.Clock)
	tenantMutations.Resources = resources
	tenantMutations.Idempotency = idempotencyStore
	tenantMutations.VariableKeyProvider = cfg.VariableKeyProvider
	tenantMutations.VariableIntentMACKey = append([]byte(nil), cfg.VariableIntentMACKey...)
	serviceConfigs := serviceconfig.NewStore(cfg.Pool)
	if cfg.Clock != nil {
		serviceConfigs.Now = cfg.Clock
	}
	tenantMutations.ServiceConfigs = serviceConfigs
	domainStore := router.NewPostgresDurableRouteStore(cfg.Pool)
	if cfg.Clock != nil {
		domainStore.Now = cfg.Clock
	}
	tenantMutations.Routes = domainStore
	tenantMutations.AppsDomain = cfg.AppsDomain
	automationTokenMutations := NewPostgresAutomationTokenMutationCoordinator(
		cfg.Pool,
		identityStore,
		automationTokens,
		automationTokenStore,
		idempotencyStore,
		auditChain,
		cfg.Clock,
	)
	identityInvitationMutations := NewPostgresIdentityInvitationMutationCoordinator(
		cfg.Pool,
		identityInvitations,
		identityStore,
		idempotencyStore,
		auditChain,
		cfg.Clock,
	)
	webhookDeliveries := webhook.NewPostgresDeliveryStore(cfg.Pool)
	webhookDeliveries.AcceptanceFence = func(
		ctx context.Context,
		tx *pg.Tx,
		request webhook.InboxAcceptance,
	) error {
		if request.Provider != webhook.ProviderGitHub {
			return nil
		}
		var installationID int64
		switch request.EventType {
		case string(githubapp.EventPush):
			installationID = githubapp.PushEventInstallationID(request.Payload)
		case string(githubapp.EventPullRequest):
			installationID = githubapp.PullRequestEventInstallationID(request.Payload)
		default:
			return nil
		}
		if installationID <= 0 {
			// The durable planner will classify a malformed push payload as
			// terminal. There is no valid authority scope to serialize here.
			return nil
		}
		return githubapp.LockInstallationInTx(ctx, tx, installationID)
	}
	webhookDeliveries.Audit = auditChain
	webhookDeliveryRedrive := NewPostgresWebhookDeliveryRedriveCoordinator(
		cfg.Pool,
		webhookDeliveries,
		idempotencyStore,
		auditChain,
		cfg.Clock,
	)
	runner := &pg.MigrationRunner{
		Pool:         cfg.Pool,
		FS:           cfg.MigrationsFS,
		ManifestPath: cfg.ManifestPath,
	}

	volumeStore := volume.NewPostgresStore(cfg.Pool)
	if cfg.Clock != nil {
		volumeStore.Now = cfg.Clock
	}
	tenantMutations.Volumes = volumeStore
	deploymentStore := NewPostgresDeploymentStore(cfg.Pool)
	environmentResourceEvidence := NewPostgresEnvironmentResourceEvidenceStore(cfg.Pool)
	if cfg.Clock != nil {
		deploymentStore.Now = cfg.Clock
	}
	deployOutbox := NewPostgresDeployOutbox(cfg.Pool)
	if cfg.Clock != nil {
		deployOutbox.Now = cfg.Clock
	}
	tenantDeployIntake := NewPostgresTenantDeployIntakeCoordinator(cfg.Pool, auditChain, cfg.Clock)
	tenantDeployIntake.AppsDomain = strings.TrimSpace(cfg.AppsDomain)
	// The human deployment admission must share the exact response-encrypting
	// idempotency store and outbox objects exposed by this stack. Constructing
	// parallel adapters against the same DSN would weaken the authority check
	// and make boot-time wiring drift invisible.
	tenantDeployIntake.Idempotency = idempotencyStore
	tenantDeployIntake.Outbox = deployOutbox
	tenantDeployIntake.SourceArtifacts = sourceartifact.NewPostgresStore(cfg.Pool)
	var sourceArtifactStore TenantUpArtifactStore
	var tenantUpIntake TenantUpIntakeCoordinator
	var usageSourceArtifactLimit *int64
	if root := strings.TrimSpace(cfg.SourceArtifactRoot); root != "" {
		quotaConfig := cfg.SourceArtifactQuota
		if quotaConfig == (sourceartifact.QuotaConfig{}) {
			quotaConfig = sourceartifact.DefaultQuotaConfig
		}
		if err := quotaConfig.Validate(
			sourceartifact.DefaultLimits.MaxCompressedBytes,
			sourceartifact.DefaultUploadLease,
		); err != nil {
			return DataStack{}, fmt.Errorf("postgres data stack: source artifact quota: %w", err)
		}
		filesystemStore, storeErr := sourceartifact.NewFSStore(root)
		err = storeErr
		if err != nil {
			return DataStack{}, fmt.Errorf("postgres data stack: source artifact store: %w", err)
		}
		sourceArtifactStore = filesystemStore
		tenantDeployIntake.SourceArtifactFS = filesystemStore
		tenantDeployIntake.SourceArtifactQuota = sourceartifact.NewQuotaStore(cfg.Pool, quotaConfig)
		configuredLimit := quotaConfig.WorkspaceBytes
		usageSourceArtifactLimit = &configuredLimit
		if cfg.Clock != nil {
			tenantDeployIntake.SourceArtifactQuota.Now = cfg.Clock
		}
		tenantUpIntake = tenantDeployIntake
	}
	stagedWorkOutbox := NewPostgresStagedWorkOutbox(cfg.Pool)
	if cfg.Clock != nil {
		stagedWorkOutbox.Now = cfg.Clock
	}
	variableStore := secrets.NewVaultWithStore(
		cfg.VariableKeyProvider,
		secrets.DenyByDefault{},
		secrets.NewPostgresEnvelopeStore(cfg.Pool),
	)
	templateProvisioner := NewPostgresTemplateProvisioner(cfg.Pool, cfg.VariableKeyProvider)
	templateRunStore := templatepkg.NewPostgresRunStore(cfg.Pool)
	communityTemplateForks := templatepkg.NewPostgresCommunityForkStore(cfg.Pool)
	if cfg.Clock != nil {
		templateProvisioner.Now = cfg.Clock
		templateRunStore.Now = cfg.Clock
		communityTemplateForks.Now = cfg.Clock
	}
	observabilityStore := observability.NewPostgresStore(cfg.Pool)
	if cfg.Clock != nil {
		observabilityStore.Now = cfg.Clock
	}
	meteringStore := metering.NewPostgresStore(cfg.Pool)
	meteringStore.SourceArtifactLimitBytes = usageSourceArtifactLimit
	meteringStore.Audit = auditChain
	if cfg.Clock != nil {
		meteringStore.Now = cfg.Clock
	}
	tenantMutations.Metering = meteringStore
	deploymentActions := NewPostgresDeploymentActionStore(cfg.Pool)
	tenantMutations.DeploymentActions = deploymentActions
	tenantMutations.DeployOutbox = deployOutbox
	tenantDeploymentActions := NewPostgresTenantDeploymentActionCoordinator(
		cfg.Pool,
		idempotencyStore,
		auditChain,
		deploymentActions,
		cfg.Clock,
	)
	tenantOperationCancel := NewPostgresTenantOperationCancelCoordinator(
		cfg.Pool,
		idempotencyStore,
		auditChain,
		cfg.Clock,
	)
	githubSourceLinks := githubapp.NewSourceLinkStore(cfg.Pool)
	if cfg.Clock != nil {
		githubSourceLinks.Now = cfg.Clock
	}
	tenantGitHubSourceLinks := NewPostgresTenantGitHubSourceLinkCoordinator(
		cfg.Pool,
		idempotencyStore,
		auditChain,
		githubSourceLinks,
		cfg.Clock,
	)
	githubGrantRecovery := NewPostgresGitHubGrantRecoveryCoordinator(
		cfg.Pool,
		githubSourceLinks,
		idempotencyStore,
		auditChain,
		cfg.Clock,
	)
	githubSetupCodec, err := githubapp.NewSetupStateCodec(
		deriveGitHubSetupMACKey(cfg.CursorSecret),
		githubapp.WithSetupClock(cfg.Clock),
	)
	if err != nil {
		return DataStack{}, fmt.Errorf("postgres data stack: github setup state: %w", err)
	}
	githubSetup := NewPostgresGitHubSetupCoordinator(
		cfg.Pool,
		githubSourceLinks,
		auditChain,
		githubSetupCodec,
		cfg.Clock,
	)
	var previewLifecycle GitHubPreviewLifecycle
	if strings.TrimSpace(cfg.AppsDomain) != "" {
		postgresPreviewLifecycle := NewPostgresGitHubPreviewLifecycle(
			cfg.Pool,
			canonicalSourceLinkLookup{store: githubSourceLinks},
			cfg.VariableKeyProvider,
			domainStore,
			deploymentActions,
			cfg.AppsDomain,
		)
		if cfg.Clock != nil {
			postgresPreviewLifecycle.Now = cfg.Clock
		}
		previewLifecycle = postgresPreviewLifecycle
	}
	volumeRecovery := backup.NewPostgresVolumeRunStore(cfg.Pool)
	volumeRecoveryApply := NewPostgresVolumeRecoveryApplyCoordinator(cfg.Pool, volumeRecovery, deploymentActions)
	if cfg.Clock != nil {
		deploymentActions.Now = cfg.Clock
		volumeRecovery.Now = cfg.Clock
		volumeRecoveryApply.Now = cfg.Clock
	}
	tcpProxyStore := tcpproxy.NewPostgresStore(cfg.Pool)
	tcpProxyStore.BaseDomain = cfg.TCPProxyBaseDomain
	if cfg.TCPProxyPorts.Start > 0 || cfg.TCPProxyPorts.End > 0 {
		tcpProxyStore.Ports = cfg.TCPProxyPorts
	}
	if cfg.Clock != nil {
		tcpProxyStore.Now = cfg.Clock
	}

	return DataStack{
		OperationStore:              NewPostgresOperationStore(cfg.Pool),
		DeploymentStore:             deploymentStore,
		EnvironmentResourceEvidence: environmentResourceEvidence,
		DeploymentActions:           deploymentActions,
		CronOccurrences:             deploy.NewPostgresCronStore(cfg.Pool),
		IdempotencyStore:            idempotencyStore,
		CursorCodec:                 NewCursorCodec(cfg.CursorSecret),
		ProjectStore:                resources,
		ServiceStore:                resources,
		EnvironmentStore:            resources,
		TopologyLayouts:             topology.NewPostgresStore(cfg.Pool),
		EnvironmentDuplicator:       environmentDuplicator,
		DomainStore:                 domainStore,
		AppsDomain:                  cfg.AppsDomain,
		InstanceDomainSettings:      instanceDomainSettings,
		TCPProxyStore:               tcpProxyStore,
		TCPProxyBackendResolver:     tcpproxy.PostgresBackendResolver{Pool: cfg.Pool},
		TCPProxyBaseDomain:          cfg.TCPProxyBaseDomain,
		MutationScopeResolver:       NewMutationScopeResolver(resources, resources, resources),
		IdentitySessions:            identitySessions,
		AutomationTokens:            automationTokens,
		AutomationTokenCatalog:      automationTokens,
		AutomationTokenMutations:    automationTokenMutations,
		IdentityInvitationCatalog:   identityInvitations,
		IdentityInvitationMutations: identityInvitationMutations,
		IdentityAccess:              identityStore,
		IdentityClaim:               identityClaim,
		IdentityRecovery:            identityStore,
		IdentityRecoveryCommit:      identityRecovery,
		TenantMutations:             tenantMutations,
		TenantEnvironmentUpdates:    tenantMutations,
		TenantServiceConfigs:        tenantMutations,
		TenantServiceDeletes:        tenantMutations,
		TenantVariables:             tenantMutations,
		EnvironmentChangesets:       tenantMutations,
		TenantGeneratedDomains:      tenantMutations,
		TenantCustomDomains:         tenantMutations,
		TenantDeployIntake:          tenantDeployIntake,
		GraphQLDeployIntake:         tenantDeployIntake,
		TenantUpIntake:              tenantUpIntake,
		SourceArtifactStore:         sourceArtifactStore,
		TenantDeploymentActions:     tenantDeploymentActions,
		TenantOperationCancel:       tenantOperationCancel,
		GitHubSourceLinks:           githubSourceLinks,
		TenantGitHubSourceLinks:     tenantGitHubSourceLinks,
		GitHubGrantRecovery:         githubGrantRecovery,
		GitHubSetup:                 githubSetup,
		// A durable stack is a production-capable boundary. Embedders must
		// explicitly provide the recovery bearer before operator routes,
		// including the one-shot tenant claim, become reachable.
		ProductionAuthRequired:      true,
		VariableStore:               variableStore,
		AuditStore:                  AuditChainQueryStore{Chain: auditChain},
		TimelineStore:               timeline.NewPostgresStore(cfg.Pool),
		QueueStore:                  queue.NewPostgresStore(cfg.Pool),
		DeployIntakeOutbox:          deployOutbox,
		StagedWorkOutbox:            stagedWorkOutbox,
		PreviewLifecycle:            previewLifecycle,
		TemplateProvisioner:         templateProvisioner,
		TemplateRunReader:           templateRunStore,
		CommunityTemplateDiscoverer: templatepkg.NewGitHubCommunityDiscoverer(),
		CommunityTemplateForks:      communityTemplateForks,
		LogStore:                    observabilityStore,
		ObservabilityStore:          observabilityStore,
		MeteringStore:               meteringStore,
		MonitorStore:                monitors.NewPostgresStore(cfg.Pool),
		Migrations:                  &MigrationsRunnerAdmin{Runner: runner},
		AgentHMACSecret:             append([]byte(nil), cfg.AgentHMACSecret...),
		AuditChain:                  auditChain,
		WebhookInbox:                webhookDeliveries,
		WebhookDeliveryRedrive:      webhookDeliveryRedrive,
		VolumeStore:                 volumeStore,
		VolumeRecoveryStore:         volumeRecovery,
		VolumeRecoveryApply:         volumeRecoveryApply,
		ServiceConfigStore:          serviceConfigs,
		// Mount intent is durable on volumes.mount_path. The sidecar remains
		// nil in production so reads cannot accidentally prefer process memory.
		VolumeMountStore: nil,
		Clock:            cfg.Clock,
	}, nil
}

func deriveIdentitySessionMACKey(cursorSecret []byte) []byte {
	mac := hmac.New(sha256.New, cursorSecret)
	_, _ = mac.Write([]byte("midnight/identity-session-token/v1"))
	return mac.Sum(nil)
}

func deriveGitHubSetupMACKey(cursorSecret []byte) []byte {
	mac := hmac.New(sha256.New, cursorSecret)
	_, _ = mac.Write([]byte("midnight/github-app-setup-state/v1"))
	return mac.Sum(nil)
}

// AuditChainQueryStore adapts the append-only audit chain to the API's richer
// list filter. The chain remains the source of truth for row ordering.
type AuditChainQueryStore struct {
	Chain audit.AuditChain
}

// ListAudit implements AuditQueryStore.
func (s AuditChainQueryStore) ListAudit(ctx context.Context, f AuditFilter) ([]audit.Row, error) {
	if s.Chain == nil {
		return nil, errors.New("audit chain query store: chain is required")
	}
	query := audit.Query{SinceID: f.AfterID}
	if !auditFilterHasPredicates(f) {
		query.Limit = f.Limit
	}
	rows, err := s.Chain.Read(ctx, query)
	if err != nil {
		return nil, err
	}
	if !auditFilterHasPredicates(f) {
		return rows, nil
	}
	limit := f.Limit
	if limit <= 0 {
		limit = len(rows)
	}
	out := make([]audit.Row, 0, min(limit, len(rows)))
	for _, row := range rows {
		if !auditRowMatches(row, f) {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func auditFilterHasPredicates(f AuditFilter) bool {
	return f.ResourceKind != "" || f.ResourceID != "" || f.ActorID != "" || f.RequestID != ""
}

func auditRowMatches(row audit.Row, f AuditFilter) bool {
	if f.ResourceKind != "" && row.ResourceKind != f.ResourceKind {
		return false
	}
	if f.ResourceID != "" && row.ResourceID != f.ResourceID {
		return false
	}
	if f.ActorID != "" && row.ActorID != f.ActorID {
		return false
	}
	if f.RequestID != "" && row.RequestID != f.RequestID {
		return false
	}
	return true
}
