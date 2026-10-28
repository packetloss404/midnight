// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type tenantConversationTools struct {
	policy        *tenantConversationPolicy
	conversation  conversation.Conversation
	environmentID string
	principal     identity.Principal
	session       identity.Session
	human         bool
	base          agent.Env
}

type tenantConversationToolScopeKey struct{}

// Tool contexts retain only the verified identity tuple, not the bearer token.
// A provider cannot erase request context and regain operator authority.
func (b *tenantConversationTools) context(ctx context.Context) context.Context {
	if b.human {
		ctx = context.WithValue(ctx, identityPrincipalContextKey{}, b.principal)
		ctx = context.WithValue(ctx, identitySessionContextKey{}, b.session)
		ctx = withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: b.principal})
	}
	return context.WithValue(ctx, tenantConversationToolScopeKey{}, b)
}

func (b *tenantConversationTools) check(ctx context.Context, projectID, serviceID, environmentID string, capability identity.Capability) error {
	ctx = b.context(ctx)
	if err := b.policy.Authorize(ctx, conversation.Scope{ConversationID: b.conversation.ID, Action: "send"}); err != nil {
		return err
	}
	if (b.conversation.ProjectID != "" && projectID != b.conversation.ProjectID) ||
		(b.conversation.ServiceID != "" && serviceID != b.conversation.ServiceID) ||
		(b.environmentID != "" && environmentID != b.environmentID) {
		return tenantConversationDenied()
	}
	_, err := b.policy.target(ctx, projectID, serviceID, environmentID, capability)
	return err
}

func (b *tenantConversationTools) service(ctx context.Context, id, environmentID string, capability identity.Capability) (string, error) {
	var projectID string
	if err := b.policy.pool.QueryRow(ctx, `SELECT project_id FROM services WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&projectID); err != nil {
		return "", tenantConversationDenied()
	}
	if err := b.check(ctx, projectID, id, environmentID, capability); err != nil {
		return "", err
	}
	return projectID, nil
}

func (b *tenantConversationTools) deployment(ctx context.Context, id string, capability identity.Capability) error {
	var projectID, serviceID, environmentID string
	if err := b.policy.pool.QueryRow(ctx, `SELECT project_id,service_id,environment_id FROM deployments WHERE id=$1`, id).Scan(&projectID, &serviceID, &environmentID); err != nil {
		return tenantConversationDenied()
	}
	return b.check(ctx, projectID, serviceID, environmentID, capability)
}

func (b *tenantConversationTools) ListProjects(ctx context.Context) ([]agent.Project, error) {
	if b.conversation.ProjectID == "" {
		if err := b.policy.Authorize(b.context(ctx), conversation.Scope{ConversationID: b.conversation.ID, Action: "send"}); err != nil {
			return nil, err
		}
		return b.base.Services.ListProjects(b.context(ctx)) // installation admin's global conversation
	}
	if err := b.check(ctx, b.conversation.ProjectID, b.conversation.ServiceID, b.environmentID, identity.CapabilityProjectRead); err != nil {
		return nil, err
	}
	p, err := b.policy.projects.GetProject(b.context(ctx), orailtypes.ID(b.conversation.ProjectID))
	if err != nil {
		return nil, err
	}
	return []agent.Project{{ID: string(p.ID), Name: firstNonEmptyString(p.DisplayName, p.Slug), Slug: p.Slug, WorkspaceID: p.WorkspaceID, CreatedAt: p.CreatedAt}}, nil
}

func (b *tenantConversationTools) ListServices(ctx context.Context, projectID string) ([]agent.Service, error) {
	if projectID == "" {
		projectID = b.conversation.ProjectID
	}
	if err := b.check(ctx, projectID, b.conversation.ServiceID, b.environmentID, identity.CapabilityServiceRead); err != nil {
		return nil, err
	}
	if b.conversation.ServiceID != "" {
		service, err := b.GetService(ctx, b.conversation.ServiceID)
		if err != nil {
			return nil, err
		}
		return []agent.Service{service}, nil
	}
	return b.base.Services.ListServices(b.context(ctx), projectID)
}

func (b *tenantConversationTools) GetService(ctx context.Context, id string) (agent.Service, error) {
	if _, err := b.service(ctx, id, b.environmentID, identity.CapabilityServiceRead); err != nil {
		return agent.Service{}, err
	}
	service, err := b.base.Services.GetService(b.context(ctx), id)
	if err != nil {
		return agent.Service{}, err
	}
	allowed := make([]agent.Deployment, 0, len(service.Deployments))
	for _, deployment := range service.Deployments {
		if b.deployment(ctx, deployment.ID, identity.CapabilityServiceRead) == nil {
			allowed = append(allowed, deployment)
		}
	}
	service.Deployments = allowed
	service.LatestStatus = ""
	if len(allowed) > 0 {
		service.LatestStatus = allowed[0].Status
	}
	return service, nil
}

func (b *tenantConversationTools) variable(ctx context.Context, request agent.VariableWrite, remove bool) error {
	if !b.base.AllowMutations {
		return agent.ErrMutationsDisabled
	}
	if request.EnvironmentID == "" {
		request.EnvironmentID = b.environmentID
	}
	if request.EnvironmentID == "" {
		return ErrAgentEnvironmentAmbiguous
	}
	if _, err := b.service(ctx, request.ServiceID, request.EnvironmentID, identity.CapabilityVariablesWrite); err != nil {
		return err
	}
	if remove {
		return b.base.Services.UnsetVariable(b.context(ctx), request)
	}
	return b.base.Services.SetVariable(b.context(ctx), request)
}
func (b *tenantConversationTools) SetVariable(ctx context.Context, request agent.VariableWrite) error {
	return b.variable(ctx, request, false)
}
func (b *tenantConversationTools) UnsetVariable(ctx context.Context, request agent.VariableWrite) error {
	return b.variable(ctx, request, true)
}

func (b *tenantConversationTools) GetDeployment(ctx context.Context, id string) (agent.Deployment, error) {
	if err := b.deployment(ctx, id, identity.CapabilityServiceRead); err != nil {
		return agent.Deployment{}, err
	}
	return b.base.Operations.GetDeployment(b.context(ctx), id)
}
func (b *tenantConversationTools) GetLogs(ctx context.Context, id string, lines int, search string) ([]agent.LogLine, error) {
	if err := b.deployment(ctx, id, identity.CapabilityLogsRead); err != nil {
		return nil, err
	}
	return b.base.Operations.GetLogs(b.context(ctx), id, lines, search)
}
func (b *tenantConversationTools) Redeploy(ctx context.Context, id string) (agent.Deployment, error) {
	if !b.base.AllowMutations {
		return agent.Deployment{}, agent.ErrMutationsDisabled
	}
	if b.environmentID == "" {
		return agent.Deployment{}, ErrAgentEnvironmentAmbiguous
	}
	if _, err := b.service(ctx, id, b.environmentID, identity.CapabilityDeploymentTriggerCLI); err != nil {
		return agent.Deployment{}, err
	}
	return b.base.Operations.Redeploy(b.context(ctx), id)
}
func (b *tenantConversationTools) Rollback(ctx context.Context, id string) (agent.Deployment, error) {
	if !b.base.AllowMutations {
		return agent.Deployment{}, agent.ErrMutationsDisabled
	}
	if err := b.deployment(ctx, id, identity.CapabilityDeploymentTriggerCLI); err != nil {
		return agent.Deployment{}, err
	}
	return b.base.Operations.Rollback(b.context(ctx), id)
}

func (b *tenantConversationTools) DeployFromSource(context.Context, string, string, string) (agent.Deployment, error) {
	return agent.Deployment{}, ErrAgentSourceDeployUnavailable
}
func (b *tenantConversationTools) DeployScopedSource(ctx context.Context, request agent.SourceDeployRequest) (agent.Deployment, error) {
	if !b.base.AllowMutations {
		return agent.Deployment{}, agent.ErrMutationsDisabled
	}
	if request.EnvironmentID == "" {
		return agent.Deployment{}, ErrAgentEnvironmentAmbiguous
	}
	if _, err := b.service(ctx, request.ServiceID, request.EnvironmentID, identity.CapabilityDeploymentTriggerCLI); err != nil {
		return agent.Deployment{}, err
	}
	builds, ok := b.base.Builds.(agent.ScopedBuildIntake)
	if !ok {
		return agent.Deployment{}, ErrAgentSourceDeployUnavailable
	}
	return builds.DeployScopedSource(agent.WithSourceConversationID(b.context(ctx), b.conversation.ID), request)
}

func (b *tenantConversationTools) SourceDeploymentContext(ctx context.Context, serviceID, environmentID string) (agent.SourceDeploymentContext, error) {
	if environmentID == "" {
		return agent.SourceDeploymentContext{}, ErrAgentEnvironmentAmbiguous
	}
	if _, err := b.service(ctx, serviceID, environmentID, identity.CapabilityServiceRead); err != nil {
		return agent.SourceDeploymentContext{}, err
	}
	source, ok := b.base.Builds.(agent.ScopedSourceContext)
	if !ok {
		return agent.SourceDeploymentContext{}, ErrAgentSourceDeployUnavailable
	}
	return source.SourceDeploymentContext(agent.WithSourceConversationID(b.context(ctx), b.conversation.ID), serviceID, environmentID)
}
func (b *tenantConversationTools) GetServiceMetrics(ctx context.Context, id string) (agent.ServiceMetrics, error) {
	if _, err := b.service(ctx, id, b.environmentID, identity.CapabilityMetricsRead); err != nil {
		return agent.ServiceMetrics{}, err
	}
	if b.base.Metrics == nil {
		return agent.ServiceMetrics{}, agent.ErrMetricsUnavailable
	}
	// The current metrics seam has no environment selector. Refuse rather
	// than returning aggregates outside a conversation's bound environment.
	if b.environmentID != "" {
		return agent.ServiceMetrics{}, agent.ErrMetricsUnavailable
	}
	return b.base.Metrics.GetServiceMetrics(b.context(ctx), id)
}
