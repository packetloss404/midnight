// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/timeline"
	"github.com/midnight/midnight/internal/webhook"
)

func TestNewPostgresDataStackWiresDurableControlPlaneStores(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC) }
	keyProvider := testVariableKeyProvider(t)
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool:                  &pg.Pool{},
		MigrationsFS:          fstest.MapFS{"manifest.yaml": &fstest.MapFile{Data: []byte("schemaVersion: 1\nmigrations: []\n")}},
		ManifestPath:          "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AgentHMACSecret:       []byte("agent-secret"),
		AutomationTokenMACKey: []byte("automation-token-postgres-test-key"),
		InvitationMACKey:      []byte("invitation-postgres-test-mac-key"),
		VariableIntentMACKey:  []byte("variable-intent-postgres-test-key"),
		VariableKeyProvider:   keyProvider,
		SourceArtifactRoot:    t.TempDir(),
		Clock:                 now,
	})
	if err != nil {
		t.Fatalf("NewPostgresDataStack() error = %v", err)
	}

	if _, ok := stack.OperationStore.(*PostgresOperationStore); !ok {
		t.Fatalf("OperationStore = %T, want *PostgresOperationStore", stack.OperationStore)
	}
	deploymentStore, ok := stack.DeploymentStore.(*PostgresDeploymentStore)
	if !ok {
		t.Fatalf("DeploymentStore = %T, want *PostgresDeploymentStore", stack.DeploymentStore)
	}
	if got := deploymentStore.Now(); !got.Equal(now()) {
		t.Fatalf("DeploymentStore.Now() = %s, want %s", got, now())
	}
	idempotencyStore, ok := stack.IdempotencyStore.(*idempotency.PostgresStore)
	if !ok {
		t.Fatalf("IdempotencyStore = %T, want *idempotency.PostgresStore", stack.IdempotencyStore)
	}
	if idempotencyStore.ResponseKeys != keyProvider {
		t.Fatal("IdempotencyStore does not share the production response KEK provider")
	}
	if stack.IdentitySessions == nil {
		t.Fatal("IdentitySessions = nil, want durable session manager")
	}
	if _, ok := stack.IdentityAccess.(*identity.PostgresStore); !ok {
		t.Fatalf("IdentityAccess = %T, want *identity.PostgresStore", stack.IdentityAccess)
	}
	identityClaim, ok := stack.IdentityClaim.(*PostgresIdentityClaimCoordinator)
	if !ok {
		t.Fatalf("IdentityClaim = %T, want *PostgresIdentityClaimCoordinator", stack.IdentityClaim)
	}
	if identityClaim.Pool != stack.IdentityAccess.(*identity.PostgresStore).Pool ||
		identityClaim.Identity != stack.IdentityAccess ||
		identityClaim.Idempotency != idempotencyStore ||
		identityClaim.Audit != stack.AuditChain {
		t.Fatal("IdentityClaim does not share identity, idempotency, audit, and PostgreSQL authorities")
	}
	automationTokens, ok := stack.AutomationTokens.(*automationtoken.Manager)
	if !ok {
		t.Fatalf("AutomationTokens = %T, want *automationtoken.Manager", stack.AutomationTokens)
	}
	if stack.AutomationTokenCatalog != automationTokens {
		t.Fatalf("AutomationTokenCatalog = %T, want shared manager %p", stack.AutomationTokenCatalog, automationTokens)
	}
	automationMutations, ok := stack.AutomationTokenMutations.(*PostgresAutomationTokenMutationCoordinator)
	if !ok {
		t.Fatalf("AutomationTokenMutations = %T, want *PostgresAutomationTokenMutationCoordinator", stack.AutomationTokenMutations)
	}
	if automationMutations.Tokens != automationTokens ||
		automationMutations.Idempotency != idempotencyStore ||
		automationMutations.Access != stack.IdentityAccess ||
		automationMutations.Audit != stack.AuditChain {
		t.Fatal("AutomationTokenMutations does not share token, idempotency, identity, and audit authorities")
	}
	if automationMutations.TokenStore == nil || automationMutations.TokenStore.Pool != stack.IdentityAccess.(*identity.PostgresStore).Pool {
		t.Fatal("AutomationTokenMutations.TokenStore does not share the production PostgreSQL pool")
	}
	identityInvitations, ok := stack.IdentityInvitationCatalog.(*identity.InvitationService)
	if !ok {
		t.Fatalf("IdentityInvitationCatalog = %T, want *identity.InvitationService", stack.IdentityInvitationCatalog)
	}
	invitationMutations, ok := stack.IdentityInvitationMutations.(*PostgresIdentityInvitationMutationCoordinator)
	if !ok {
		t.Fatalf("IdentityInvitationMutations = %T, want *PostgresIdentityInvitationMutationCoordinator", stack.IdentityInvitationMutations)
	}
	if invitationMutations.Invitations != identityInvitations ||
		invitationMutations.Store != stack.IdentityAccess ||
		invitationMutations.Idempotency != idempotencyStore ||
		invitationMutations.Audit != stack.AuditChain {
		t.Fatal("IdentityInvitationMutations does not share invitation, idempotency, identity, and audit authorities")
	}
	if !stack.ProductionAuthRequired {
		t.Fatal("ProductionAuthRequired = false, durable stack must fail closed")
	}
	if _, ok := stack.QueueStore.(*queue.PostgresStore); !ok {
		t.Fatalf("QueueStore = %T, want *queue.PostgresStore", stack.QueueStore)
	}
	outbox, ok := stack.DeployIntakeOutbox.(*PostgresDeployOutbox)
	if !ok {
		t.Fatalf("DeployIntakeOutbox = %T, want *PostgresDeployOutbox", stack.DeployIntakeOutbox)
	}
	if got := outbox.Now(); !got.Equal(now()) {
		t.Fatalf("DeployIntakeOutbox.Now() = %s, want %s", got, now())
	}
	tenantDeploy, ok := stack.TenantDeployIntake.(*PostgresTenantDeployIntakeCoordinator)
	if !ok {
		t.Fatalf("TenantDeployIntake = %T, want *PostgresTenantDeployIntakeCoordinator", stack.TenantDeployIntake)
	}
	if stack.GraphQLDeployIntake != tenantDeploy {
		t.Fatalf("GraphQLDeployIntake = %T, want shared Postgres deploy coordinator %p", stack.GraphQLDeployIntake, tenantDeploy)
	}
	tenantActions, ok := stack.TenantDeploymentActions.(*PostgresTenantDeploymentActionCoordinator)
	if !ok {
		t.Fatalf(
			"TenantDeploymentActions = %T, want *PostgresTenantDeploymentActionCoordinator",
			stack.TenantDeploymentActions,
		)
	}
	if tenantActions.Pool != idempotencyStore.Pool ||
		tenantActions.Idempotency != stack.IdempotencyStore ||
		DeploymentActionIntake(tenantActions.Actions) != stack.DeploymentActions {
		t.Fatal("tenant deployment actions do not share stack authority")
	}
	tenantCancel, ok := stack.TenantOperationCancel.(*PostgresTenantOperationCancelCoordinator)
	if !ok {
		t.Fatalf(
			"TenantOperationCancel = %T, want *PostgresTenantOperationCancelCoordinator",
			stack.TenantOperationCancel,
		)
	}
	if tenantCancel.Pool != idempotencyStore.Pool ||
		tenantCancel.Idempotency != stack.IdempotencyStore ||
		tenantCancel.Audit != stack.AuditChain {
		t.Fatal("tenant operation cancellation does not share stack authority")
	}
	githubSourceLinks := stack.GitHubSourceLinks
	if githubSourceLinks == nil {
		t.Fatal("GitHubSourceLinks = nil, want *github.SourceLinkStore")
	}
	tenantGitHubSourceLinks, ok := stack.TenantGitHubSourceLinks.(*PostgresTenantGitHubSourceLinkCoordinator)
	if !ok {
		t.Fatalf(
			"TenantGitHubSourceLinks = %T, want *PostgresTenantGitHubSourceLinkCoordinator",
			stack.TenantGitHubSourceLinks,
		)
	}
	if tenantGitHubSourceLinks.Pool != idempotencyStore.Pool ||
		tenantGitHubSourceLinks.Idempotency != idempotencyStore ||
		tenantGitHubSourceLinks.Audit != stack.AuditChain ||
		tenantGitHubSourceLinks.Links != githubSourceLinks {
		t.Fatal("tenant GitHub source links do not share stack PostgreSQL, idempotency, audit, and source-link authorities")
	}
	githubGrantRecovery, ok := stack.GitHubGrantRecovery.(*PostgresGitHubGrantRecoveryCoordinator)
	if !ok {
		t.Fatalf(
			"GitHubGrantRecovery = %T, want *PostgresGitHubGrantRecoveryCoordinator",
			stack.GitHubGrantRecovery,
		)
	}
	if githubGrantRecovery.Pool != idempotencyStore.Pool ||
		githubGrantRecovery.Idempotency != idempotencyStore ||
		githubGrantRecovery.Audit != stack.AuditChain ||
		githubGrantRecovery.Links != githubSourceLinks {
		t.Fatal("GitHub grant recovery does not share stack PostgreSQL, idempotency, audit, and source-link authorities")
	}
	if got := githubSourceLinks.Now(); !got.Equal(now()) {
		t.Fatalf("GitHubSourceLinks.Now() = %s, want %s", got, now())
	}
	if got := tenantGitHubSourceLinks.now(); !got.Equal(now()) {
		t.Fatalf("TenantGitHubSourceLinks.now() = %s, want %s", got, now())
	}
	if got := githubGrantRecovery.now(); !got.Equal(now()) {
		t.Fatalf("GitHubGrantRecovery.now() = %s, want %s", got, now())
	}
	if tenantDeploy.Pool != idempotencyStore.Pool ||
		tenantDeploy.Idempotency != idempotencyStore ||
		tenantDeploy.Audit != stack.AuditChain ||
		tenantDeploy.Outbox != outbox ||
		tenantDeploy.SourceArtifacts == nil ||
		tenantDeploy.SourceArtifacts.Pool != tenantDeploy.Pool {
		t.Fatal("TenantDeployIntake does not share the stack PostgreSQL, idempotency, audit, deploy-outbox, and artifact authorities")
	}
	if stack.TenantUpIntake != tenantDeploy || stack.SourceArtifactStore == nil {
		t.Fatalf("tenant up wiring coordinator=%T artifacts=%T", stack.TenantUpIntake, stack.SourceArtifactStore)
	}
	stagedOutbox, ok := stack.StagedWorkOutbox.(*PostgresStagedWorkOutbox)
	if !ok {
		t.Fatalf("StagedWorkOutbox = %T, want *PostgresStagedWorkOutbox", stack.StagedWorkOutbox)
	}
	if got := stagedOutbox.Now(); !got.Equal(now()) {
		t.Fatalf("StagedWorkOutbox.Now() = %s, want %s", got, now())
	}
	if _, ok := stack.TimelineStore.(*timeline.PostgresStore); !ok {
		t.Fatalf("TimelineStore = %T, want *timeline.PostgresStore", stack.TimelineStore)
	}
	if stack.VariableStore == nil {
		t.Fatal("VariableStore = nil, want durable encrypted vault")
	}
	tenantVariables, ok := stack.TenantVariables.(*PostgresTenantMutationCoordinator)
	if !ok || tenantVariables.VariableKeyProvider != keyProvider ||
		string(tenantVariables.VariableIntentMACKey) != "variable-intent-postgres-test-key" {
		t.Fatalf("TenantVariables = %T, want shared encrypted tenant coordinator", stack.TenantVariables)
	}
	if _, ok := stack.MutationScopeResolver.(*StoreMutationScopeResolver); !ok {
		t.Fatalf("MutationScopeResolver = %T, want *StoreMutationScopeResolver", stack.MutationScopeResolver)
	}
	if _, ok := stack.DomainStore.(*router.PostgresDurableRouteStore); !ok {
		t.Fatalf("DomainStore = %T, want *router.PostgresDurableRouteStore", stack.DomainStore)
	}
	observabilityStore, ok := stack.ObservabilityStore.(*observability.PostgresStore)
	if !ok {
		t.Fatalf("ObservabilityStore = %T, want *observability.PostgresStore", stack.ObservabilityStore)
	}
	logStore, ok := stack.LogStore.(*observability.PostgresStore)
	if !ok || logStore != observabilityStore {
		t.Fatalf("LogStore = %T, want the durable observability store", stack.LogStore)
	}
	if stack.AuditStore == nil ||
		stack.AuditChain == nil ||
		stack.Migrations == nil ||
		stack.WebhookInbox == nil ||
		stack.WebhookDeliveryRedrive == nil {
		t.Fatalf("durable stack missing audit/migration/webhook seams: %#v", stack)
	}
	webhookRedrive, ok := stack.WebhookDeliveryRedrive.(*PostgresWebhookDeliveryRedriveCoordinator)
	if !ok {
		t.Fatalf(
			"WebhookDeliveryRedrive = %T, want *PostgresWebhookDeliveryRedriveCoordinator",
			stack.WebhookDeliveryRedrive,
		)
	}
	webhookInbox := stack.WebhookInbox.(*webhook.PostgresDeliveryStore)
	if webhookRedrive.Pool != webhookInbox.Pool ||
		webhookRedrive.Inbox != webhookInbox ||
		webhookRedrive.Idempotency != idempotencyStore ||
		webhookRedrive.Audit != stack.AuditChain ||
		webhookInbox.Audit != stack.AuditChain ||
		webhookInbox.AcceptanceFence == nil {
		t.Fatal("webhook recovery does not share the inbox, PostgreSQL, idempotency, audit, and receipt-fence authorities")
	}
	if _, ok := stack.WebhookInbox.(webhook.TerminalPayloadPurger); !ok {
		t.Fatalf("WebhookInbox = %T, want terminal payload retention", stack.WebhookInbox)
	}
	if string(stack.AgentHMACSecret) != "agent-secret" || stack.Clock == nil {
		t.Fatalf("secret/clock not carried into stack")
	}
}

func TestNewPostgresDataStackKeepsTenantUpClosedWithoutSharedArtifactRoot(t *testing.T) {
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool:                  &pg.Pool{},
		MigrationsFS:          fstest.MapFS{"manifest.yaml": &fstest.MapFile{Data: []byte("schemaVersion: 1\nmigrations: []\n")}},
		ManifestPath:          "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AutomationTokenMACKey: []byte("automation-token-postgres-test-key"),
		InvitationMACKey:      []byte("invitation-postgres-test-mac-key"),
		VariableIntentMACKey:  []byte("variable-intent-postgres-test-key"),
		VariableKeyProvider:   testVariableKeyProvider(t),
	})
	if err != nil {
		t.Fatalf("NewPostgresDataStack() error = %v", err)
	}
	if stack.TenantDeployIntake == nil {
		t.Fatal("immutable-image tenant deploy should remain installed")
	}
	if stack.TenantUpIntake != nil || stack.SourceArtifactStore != nil {
		t.Fatalf("tenant up unexpectedly installed coordinator=%T artifacts=%T", stack.TenantUpIntake, stack.SourceArtifactStore)
	}
}

func TestNewPostgresDataStackRejectsNonAbsoluteArtifactRoot(t *testing.T) {
	_, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool:                  &pg.Pool{},
		MigrationsFS:          fstest.MapFS{"manifest.yaml": &fstest.MapFile{Data: []byte("schemaVersion: 1\nmigrations: []\n")}},
		ManifestPath:          "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AutomationTokenMACKey: []byte("automation-token-postgres-test-key"),
		InvitationMACKey:      []byte("invitation-postgres-test-mac-key"),
		VariableIntentMACKey:  []byte("variable-intent-postgres-test-key"),
		VariableKeyProvider:   testVariableKeyProvider(t),
		SourceArtifactRoot:    "relative/uploads",
	})
	if err == nil || !strings.Contains(err.Error(), "source artifact") {
		t.Fatalf("error=%v, want source artifact root failure", err)
	}
}

func TestNewPostgresDataStackRequiresDurableBootInputs(t *testing.T) {
	validFS := fstest.MapFS{"manifest.yaml": &fstest.MapFile{Data: []byte("schemaVersion: 1\nmigrations: []\n")}}
	validSecret := []byte("0123456789abcdef0123456789abcdef")
	validAutomationTokenKey := []byte("automation-token-postgres-test-key")
	validInvitationKey := []byte("invitation-postgres-test-mac-key")
	validVariableIntentKey := []byte("variable-intent-postgres-test-key")
	keyProvider := testVariableKeyProvider(t)

	tests := []struct {
		name string
		cfg  PostgresDataStackConfig
	}{
		{
			name: "pool",
			cfg: PostgresDataStackConfig{
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  validVariableIntentKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "migrations fs",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				CursorSecret:          validSecret,
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  validVariableIntentKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "cursor secret",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          []byte("too-short"),
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  validVariableIntentKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "automation token MAC key",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AutomationTokenMACKey: []byte("too-short"),
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  validVariableIntentKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "automation token key reuses cursor secret",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AutomationTokenMACKey: validSecret,
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  validVariableIntentKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "automation token key reuses agent secret",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AgentHMACSecret:       validAutomationTokenKey,
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  validVariableIntentKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "invitation MAC key",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      []byte("too-short"),
				VariableIntentMACKey:  validVariableIntentKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "invitation key reuses automation token key",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      validAutomationTokenKey,
				VariableIntentMACKey:  validVariableIntentKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "variable intent MAC key",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  []byte("too-short"),
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "variable intent key reuses invitation key",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  validInvitationKey,
				VariableKeyProvider:   keyProvider,
			},
		},
		{
			name: "variable key provider",
			cfg: PostgresDataStackConfig{
				Pool:                  &pg.Pool{},
				MigrationsFS:          validFS,
				CursorSecret:          validSecret,
				AutomationTokenMACKey: validAutomationTokenKey,
				InvitationMACKey:      validInvitationKey,
				VariableIntentMACKey:  validVariableIntentKey,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewPostgresDataStack(tt.cfg); err == nil {
				t.Fatal("NewPostgresDataStack() error = nil, want boot input validation error")
			}
		})
	}
}

func testVariableKeyProvider(t *testing.T) secrets.KeyProvider {
	t.Helper()
	provider, err := secrets.NewMemoryKeyProvider([]byte("postgres-test-variable-kek-00000"), 1)
	if err != nil {
		t.Fatalf("NewMemoryKeyProvider: %v", err)
	}
	return provider
}
