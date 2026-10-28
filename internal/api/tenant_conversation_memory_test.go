// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"testing"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/identity"
)

func TestTenantConversationMemoryEnvironmentPreservesTrustedCallers(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "memory-chat", DisplayName: "Memory chat"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web", DisplayName: "Web"})
	if err != nil {
		t.Fatal(err)
	}
	store := conversation.NewMemoryStore(nil)
	conv, err := store.CreateConversation(ctx, conversation.Conversation{ProjectID: string(project.ID), ServiceID: string(service.ID), ActorID: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	policy := NewTenantConversationPolicy(DataStack{ConversationStore: store, ProjectStore: resources, ServiceStore: resources}).(conversation.ScopePolicy)
	services := &agentServicesAdapter{projects: resources, services: resources}
	env := agent.Env{Services: services, ProjectScope: conv.ProjectID, Actor: "agent:chat", AllowMutations: true}
	humanContext := func(admin bool) context.Context {
		principal := identity.Principal{Kind: "user", UserID: "usr_memory_chat", SessionID: "ses_memory_chat", InstanceAdmin: admin}
		ctx := withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: principal})
		ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
		return context.WithValue(ctx, identitySessionContextKey{}, identity.Session{ID: principal.SessionID, UserID: principal.UserID})
	}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		allowed bool
	}{
		{"operator", withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalOperator}), true},
		{"developer", withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalDeveloper}), true},
		{"installation administrator", humanContext(true), true},
		{"ordinary human without durable authority", humanContext(false), false},
		{"incomplete human identity", withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman}), false},
		{"automation", withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalAutomation}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound, err := policy.BindEnvironment(tc.ctx, conv, "operator", env)
			if !tc.allowed {
				if !errors.Is(err, conversation.ErrUnauthorized) {
					t.Fatalf("untrusted memory caller was admitted: %v", err)
				}
				return
			}
			if err != nil || bound.Services != services || bound.ProjectScope != conv.ProjectID || bound.AllowMutations != env.AllowMutations {
				t.Fatalf("trusted memory tool environment changed: env=%+v err=%v", bound, err)
			}
			actual, err := bound.Services.GetService(tc.ctx, string(service.ID))
			if err != nil || actual.ID != string(service.ID) {
				t.Fatalf("trusted memory service read failed: %+v %v", actual, err)
			}
		})
	}
}
