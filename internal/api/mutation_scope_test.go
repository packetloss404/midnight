// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"testing"
)

func TestMutationScopeResolverAcceptsMatchingGraphAndInfersProject(t *testing.T) {
	resolver, projectA, _, serviceA, _, environmentA, _ := mutationScopeFixture(t)

	got, err := resolver.ResolveMutationScope(context.Background(), MutationScopeRequest{
		ServiceID:          string(serviceA.ID),
		EnvironmentID:      string(environmentA.ID),
		RequireProject:     true,
		RequireService:     true,
		RequireEnvironment: true,
	})
	if err != nil {
		t.Fatalf("ResolveMutationScope() error = %v", err)
	}
	if got.ProjectID != string(projectA.ID) {
		t.Fatalf("ProjectID = %q, want %q", got.ProjectID, projectA.ID)
	}
	if got.Project == nil || got.Service == nil || got.Environment == nil {
		t.Fatalf("resolved graph is incomplete: %#v", got)
	}
}

func TestMutationScopeResolverRejectsCrossProjectRelationships(t *testing.T) {
	resolver, projectA, projectB, serviceA, _, environmentA, environmentB := mutationScopeFixture(t)
	tests := []struct {
		name string
		req  MutationScopeRequest
	}{
		{
			name: "service does not belong to explicit project",
			req: MutationScopeRequest{
				ProjectID: string(projectB.ID),
				ServiceID: string(serviceA.ID),
			},
		},
		{
			name: "environment does not belong to explicit project",
			req: MutationScopeRequest{
				ProjectID:     string(projectA.ID),
				EnvironmentID: string(environmentB.ID),
			},
		},
		{
			name: "service and environment disagree",
			req: MutationScopeRequest{
				ServiceID:     string(serviceA.ID),
				EnvironmentID: string(environmentB.ID),
			},
		},
		{
			name: "matching graph control",
			req: MutationScopeRequest{
				ProjectID:     string(projectA.ID),
				ServiceID:     string(serviceA.ID),
				EnvironmentID: string(environmentA.ID),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolver.ResolveMutationScope(context.Background(), tt.req)
			if tt.name == "matching graph control" {
				if err != nil {
					t.Fatalf("ResolveMutationScope() error = %v", err)
				}
				return
			}
			var scopeErr *MutationScopeError
			if !errors.As(err, &scopeErr) || scopeErr.Kind != MutationScopeMismatch {
				t.Fatalf("error = %#v, want MutationScopeMismatch", err)
			}
		})
	}
}

func TestMutationScopeResolverFailsClosedForMissingDependency(t *testing.T) {
	resolver := NewMutationScopeResolver(NewMemoryResourceStore(), nil, nil)
	_, err := resolver.ResolveMutationScope(context.Background(), MutationScopeRequest{
		ServiceID:      "svc_missing",
		RequireService: true,
	})
	var scopeErr *MutationScopeError
	if !errors.As(err, &scopeErr) || scopeErr.Kind != MutationScopeDependencyUnavailable {
		t.Fatalf("error = %#v, want MutationScopeDependencyUnavailable", err)
	}
}

func TestMutationScopeResolverReturnsTypedNotFound(t *testing.T) {
	resources := NewMemoryResourceStore()
	resolver := NewMutationScopeResolver(resources, resources, NewMemoryEnvironmentStore(nil))
	_, err := resolver.ResolveMutationScope(context.Background(), MutationScopeRequest{
		ServiceID:      "svc_missing",
		RequireService: true,
	})
	var scopeErr *MutationScopeError
	if !errors.As(err, &scopeErr) || scopeErr.Kind != MutationScopeNotFound {
		t.Fatalf("error = %#v, want MutationScopeNotFound", err)
	}
}

func mutationScopeFixture(t *testing.T) (*StoreMutationScopeResolver, Project, Project, Service, Service, Environment, Environment) {
	t.Helper()
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	environments := NewMemoryEnvironmentStore(nil)

	projectA, err := resources.CreateProject(ctx, Project{ID: "prj_a", WorkspaceID: DefaultWorkspaceID, Slug: "a"})
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := resources.CreateProject(ctx, Project{ID: "prj_b", WorkspaceID: DefaultWorkspaceID, Slug: "b"})
	if err != nil {
		t.Fatal(err)
	}
	serviceA, err := resources.CreateService(ctx, Service{ID: "svc_a", ProjectID: projectA.ID, Slug: "a"})
	if err != nil {
		t.Fatal(err)
	}
	serviceB, err := resources.CreateService(ctx, Service{ID: "svc_b", ProjectID: projectB.ID, Slug: "b"})
	if err != nil {
		t.Fatal(err)
	}
	environmentA, err := environments.CreateEnvironment(ctx, Environment{ID: "env_a", ProjectID: projectA.ID, Slug: "production"})
	if err != nil {
		t.Fatal(err)
	}
	environmentB, err := environments.CreateEnvironment(ctx, Environment{ID: "env_b", ProjectID: projectB.ID, Slug: "production"})
	if err != nil {
		t.Fatal(err)
	}
	return NewMutationScopeResolver(resources, resources, environments), projectA, projectB, serviceA, serviceB, environmentA, environmentB
}
