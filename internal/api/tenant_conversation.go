// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

type tenantConversationPolicy struct {
	pool     *pg.Pool
	access   identity.AccessStore
	store    conversation.Store
	projects ProjectStore
	clock    func() time.Time
}

func (p *tenantConversationPolicy) now() time.Time {
	if p.clock != nil {
		return p.clock().UTC()
	}
	return time.Now().UTC()
}

// NewTenantConversationPolicy fails closed for ordinary humans unless the
// conversation, resource and membership stores share one durable authority.
// Installation administrators and operator/developer callers retain their
// existing global chat authority; system diagnosis reports remain read-only.
func NewTenantConversationPolicy(stack DataStack) conversation.ProjectScopeAuthorizer {
	policy := &tenantConversationPolicy{access: stack.IdentityAccess, store: stack.ConversationStore, projects: stack.ProjectStore, clock: stack.Clock}
	conversations, convOK := stack.ConversationStore.(*conversation.PostgresStore)
	access, accessOK := stack.IdentityAccess.(*identity.PostgresStore)
	if convOK && accessOK && conversations.Pool != nil && conversations.Pool == access.Pool &&
		productResourceStoresShareAuthority(stack.ProjectStore, stack.ServiceStore, stack.EnvironmentStore, stack.IdentityAccess) &&
		stack.IdentitySessions != nil && stack.IdentitySessions.SharesAccessAuthority(stack.IdentityAccess) {
		policy.pool = conversations.Pool
	}
	return policy
}

func tenantConversationAdmissionMiddleware(stack DataStack, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := productPrincipalFromContext(r.Context())
		if ok && principal.Kind == productPrincipalAutomation {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "automation credentials cannot access human conversations", nil)
			return
		}
		if ok && principal.Kind == productPrincipalHuman && !principal.Human.InstanceAdmin {
			policy := NewTenantConversationPolicy(stack).(*tenantConversationPolicy)
			_, scoped := stack.ConversationScopeAuthorizer.(conversation.ScopePolicy)
			if policy.pool == nil || !scoped {
				writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "durable conversation authorization is unavailable", nil)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// identity rechecks the captured session on every access, including tool calls
// and ongoing streams. Request-time role/admin booleans are never cached grants.
func (p *tenantConversationPolicy) identity(ctx context.Context) (identity.Principal, identity.Session, bool, error) {
	principal, hasPrincipal := identityPrincipalFromContext(ctx)
	session, hasSession := identitySessionFromContext(ctx)
	if !hasPrincipal && !hasSession {
		if product, ok := productPrincipalFromContext(ctx); ok && (product.Kind == productPrincipalHuman || product.Kind == productPrincipalAutomation) {
			return principal, session, false, conversation.ErrUnauthorized
		}
		return principal, session, false, nil
	}
	if !hasPrincipal || !hasSession || principal.UserID == "" || session.UserID != principal.UserID || principal.SessionID != session.ID {
		return principal, session, true, conversation.ErrUnauthorized
	}
	if p.pool == nil {
		if principal.InstanceAdmin {
			return principal, session, true, nil
		}
		return principal, session, true, conversation.ErrUnauthorized
	}
	var admin bool
	err := p.pool.QueryRow(ctx, `SELECT u.is_admin FROM auth_sessions s JOIN users u ON u.id=s.user_id
WHERE s.id=$1 AND s.user_id=$2 AND s.session_generation=$3 AND u.session_generation=$3
AND s.revoked_at IS NULL AND s.expires_at>$4 AND s.idle_expires_at>$4
AND u.deleted_at IS NULL AND u.is_disabled=FALSE AND u.must_rotate_password=FALSE`,
		session.ID, principal.UserID, session.SessionGeneration, p.now()).Scan(&admin)
	if err != nil {
		return principal, session, true, conversation.ErrUnauthorized
	}
	principal.InstanceAdmin = admin
	return principal, session, true, nil
}

func (p *tenantConversationPolicy) target(ctx context.Context, projectID, serviceID, environmentID string, capability identity.Capability) (string, error) {
	principal, _, human, err := p.identity(ctx)
	if err != nil {
		return "", err
	}
	if p.pool == nil {
		if !human || principal.InstanceAdmin {
			return "", nil
		}
		return "", conversation.ErrUnauthorized
	}
	var workspaceID string
	err = p.pool.QueryRow(ctx, `SELECT p.workspace_id FROM projects p JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
WHERE p.id=$1 AND p.deleted_at IS NULL
AND ($2='' OR EXISTS(SELECT 1 FROM services s WHERE s.id=$2 AND s.project_id=p.id AND s.deleted_at IS NULL))
AND ($3='' OR EXISTS(SELECT 1 FROM environments e WHERE e.id=$3 AND e.project_id=p.id AND e.deleted_at IS NULL))`, projectID, serviceID, environmentID).Scan(&workspaceID)
	if err != nil {
		return "", conversation.ErrUnauthorized
	}
	if human && !principal.InstanceAdmin {
		_, _, err = identity.Authorize(ctx, p.access, principal.UserID, identity.AccessTarget{WorkspaceID: workspaceID, ProjectID: projectID, EnvironmentID: environmentID}, capability, false)
		if err != nil {
			return "", conversation.ErrUnauthorized
		}
	}
	return workspaceID, nil
}

func (p *tenantConversationPolicy) Authorize(ctx context.Context, scope conversation.Scope) error {
	principal, _, human, err := p.identity(ctx)
	if err != nil {
		return err
	}
	capability := identity.CapabilityProjectRead
	if scope.ConversationID != "" {
		conv, err := p.store.GetConversation(ctx, scope.ConversationID)
		if err != nil {
			return conversation.ErrNotFound
		}
		scope.ProjectID, scope.ServiceID = conv.ProjectID, conv.ServiceID
		scope.EnvironmentID, _ = conv.Metadata["midnight.environment_id"].(string)
		if conv.ActorID == "agent:diagnosis" {
			if scope.Action != "read" || !p.validDiagnosis(ctx, conv) {
				return conversation.ErrUnauthorized
			}
			capability = identity.CapabilityLogsRead
		} else if human && !principal.InstanceAdmin && conv.ActorID != string(idempotency.UserActor(principal.UserID)) {
			return conversation.ErrNotFound
		}
	}
	if scope.ProjectID == "" {
		if !human || principal.InstanceAdmin {
			return nil
		}
		return conversation.ErrUnauthorized
	}
	_, err = p.target(ctx, scope.ProjectID, scope.ServiceID, scope.EnvironmentID, capability)
	return err
}

func (p *tenantConversationPolicy) validDiagnosis(ctx context.Context, conv conversation.Conversation) bool {
	if p.pool == nil || conv.Metadata["midnight.kind"] != "diagnosis" || conv.Metadata["midnight.project_id"] != conv.ProjectID || conv.Metadata["midnight.service_id"] != conv.ServiceID {
		return false
	}
	var valid bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployments d JOIN projects p ON p.id=d.project_id
JOIN operations o ON o.id=d.operation_id AND o.state='FAILED'
JOIN operation_events e ON e.operation_id=d.operation_id
WHERE d.state='failed' AND d.id=$1 AND d.operation_id=$2 AND d.project_id=$3 AND d.service_id=$4
AND p.workspace_id=$5 AND d.environment_id=$6 AND e.message=$7)`,
		conv.Metadata["midnight.deployment_id"], conv.Metadata["midnight.operation_id"], conv.ProjectID, conv.ServiceID,
		conv.Metadata["midnight.workspace_id"], conv.Metadata["midnight.environment_id"], "deploy:diagnosis_attached diagnosis_id="+conv.ID).Scan(&valid)
	return err == nil && valid
}

func (p *tenantConversationPolicy) PrepareStart(ctx context.Context, input conversation.StartInput) (conversation.StartInput, error) {
	principal, _, human, err := p.identity(ctx)
	if err != nil {
		return input, err
	}
	if human {
		input.ActorID = string(idempotency.UserActor(principal.UserID))
	}
	input.ProjectID, input.ServiceID, input.EnvironmentID = strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.ServiceID), strings.TrimSpace(input.EnvironmentID)
	metadata := make(map[string]any, len(input.Metadata)+5)
	for key, value := range input.Metadata {
		if !strings.HasPrefix(strings.ToLower(key), "midnight.") {
			metadata[key] = value
		}
	}
	input.Metadata = metadata
	if input.ProjectID == "" {
		if input.ServiceID != "" || input.EnvironmentID != "" || (human && !principal.InstanceAdmin) {
			return input, conversation.ErrUnauthorized
		}
		return input, nil
	}
	workspace, err := p.target(ctx, input.ProjectID, input.ServiceID, input.EnvironmentID, identity.CapabilityProjectRead)
	if err != nil {
		return input, err
	}
	for key, value := range map[string]string{"midnight.kind": "chat", "midnight.workspace_id": workspace, "midnight.project_id": input.ProjectID, "midnight.service_id": input.ServiceID, "midnight.environment_id": input.EnvironmentID} {
		if value != "" {
			metadata[key] = value
		}
	}
	return input, nil
}

func (p *tenantConversationPolicy) PrepareList(ctx context.Context, filter conversation.ListFilter, actor string) (conversation.ListFilter, error) {
	principal, _, human, err := p.identity(ctx)
	if err != nil {
		return filter, err
	}
	if !human || principal.InstanceAdmin {
		return filter, nil
	}
	canonicalActor := string(idempotency.UserActor(principal.UserID))
	if filter.ProjectID == "" || (filter.ActorID != "" && filter.ActorID != canonicalActor) {
		return filter, conversation.ErrUnauthorized
	}
	if _, err := p.target(ctx, filter.ProjectID, filter.ServiceID, "", identity.CapabilityProjectRead); err != nil {
		return filter, err
	}
	filter.ActorID, filter.IncludeSystemDiagnosis, filter.ReaderUserID = canonicalActor, true, principal.UserID
	filter.SharedDiagnosisWorkspaceRoles, filter.SharedDiagnosisProjectRoles = tenantRoleNames(identity.CapabilityLogsRead)
	return filter, nil
}

func (p *tenantConversationPolicy) BindEnvironment(ctx context.Context, conv conversation.Conversation, actor string, env agent.Env) (agent.Env, error) {
	if err := p.Authorize(ctx, conversation.Scope{ConversationID: conv.ID, ActorID: actor, Action: "send"}); err != nil {
		return env, err
	}
	principal, session, human, err := p.identity(ctx)
	if err != nil {
		return env, err
	}
	if !human && conv.ProjectID == "" {
		return env, nil
	}
	if p.pool == nil {
		if !human || principal.InstanceAdmin {
			return env, nil
		}
		return env, conversation.ErrUnauthorized
	}
	bound := &tenantConversationTools{policy: p, conversation: conv, principal: principal, session: session, human: human, base: env}
	bound.environmentID, _ = conv.Metadata["midnight.environment_id"].(string)
	env.ProjectScope = conv.ProjectID
	if human {
		env.Actor = "agent:chat:" + string(idempotency.UserActor(principal.UserID))
		if !principal.InstanceAdmin {
			_, err = p.target(ctx, conv.ProjectID, conv.ServiceID, bound.environmentID, identity.CapabilityProjectManage)
			env.AllowMutations = env.AllowMutations && err == nil
		}
	}
	if env.Services != nil {
		env.Services = bound
	}
	if env.Operations != nil {
		env.Operations = bound
	}
	if env.Builds != nil {
		env.Builds = bound
	}
	env.Metrics = bound
	return env, nil
}

var _ conversation.ScopePolicy = (*tenantConversationPolicy)(nil)

func tenantConversationDenied() error {
	return fmt.Errorf("%w: resource is outside this conversation's authorized scope", conversation.ErrUnauthorized)
}
