// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth_test

import (
	"context"
	"errors"
	"testing"

	auth "github.com/midnight/midnight/internal/agent/auth"
)

func TestProjectScope_Allows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		scope auth.ProjectScope
		id    string
		want  bool
	}{
		{"all allows any", auth.ProjectScope{All: true}, "proj_a", true},
		{"all allows empty (instance-wide)", auth.ProjectScope{All: true}, "", true},
		{"empty allows none", auth.ProjectScope{}, "proj_a", false},
		{"empty rejects empty id", auth.ProjectScope{}, "", false},
		{"explicit list matches", auth.ProjectScope{IDs: []string{"proj_a", "proj_b"}}, "proj_b", true},
		{"explicit list rejects miss", auth.ProjectScope{IDs: []string{"proj_a"}}, "proj_b", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.scope.Allows(tc.id)
			if got != tc.want {
				t.Fatalf("Allows(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func TestProjectScope_Equal(t *testing.T) {
	t.Parallel()
	left := auth.ProjectScope{IDs: []string{"b", "a"}}
	right := auth.ProjectScope{IDs: []string{"a", "b"}}
	if !left.Equal(right) {
		t.Fatalf("expected scopes to be equal after canonicalization")
	}
	all := auth.ProjectScope{All: true}
	if all.Equal(right) {
		t.Fatalf("all-scope must not equal explicit list")
	}
}

func TestOpenAuthorizer_AllowsEveryProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := auth.NewOpenAuthorizer()
	scope, err := a.AllowedProjects(ctx, auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"})
	if err != nil {
		t.Fatalf("AllowedProjects: %v", err)
	}
	if !scope.All {
		t.Fatalf("operator scope expected All=true, got %+v", scope)
	}
	ok, err := a.CanRead(ctx, auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"}, auth.ResourceKindService, "svc_x")
	if err != nil || !ok {
		t.Fatalf("CanRead(operator) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = a.CanMutate(ctx, auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"}, auth.ResourceKindService, "svc_x")
	if err != nil || !ok {
		t.Fatalf("CanMutate(operator) = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestOpenAuthorizer_RejectsEmptyPrincipal(t *testing.T) {
	t.Parallel()
	a := auth.NewOpenAuthorizer()
	ctx := context.Background()
	scope, err := a.AllowedProjects(ctx, auth.Principal{})
	if err != nil {
		t.Fatalf("AllowedProjects: %v", err)
	}
	if scope.All {
		t.Fatalf("empty principal must not get All scope, got %+v", scope)
	}
}

func TestDenyAllAuthorizer_RejectsEverything(t *testing.T) {
	t.Parallel()
	a := auth.NewDenyAllAuthorizer()
	ctx := context.Background()
	scope, _ := a.AllowedProjects(ctx, auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"})
	if scope.All || len(scope.IDs) != 0 {
		t.Fatalf("deny scope must be empty, got %+v", scope)
	}
	ok, _ := a.CanRead(ctx, auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"}, "service", "svc_x")
	if ok {
		t.Fatalf("CanRead must reject")
	}
	ok, _ = a.CanMutate(ctx, auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"}, "service", "svc_x")
	if ok {
		t.Fatalf("CanMutate must reject")
	}
}

func TestPostgresACLAuthorizer_DisabledReturnsUnavailable(t *testing.T) {
	t.Parallel()
	a := auth.NewPostgresACLAuthorizer()
	_, err := a.AllowedProjects(context.Background(), auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"})
	if !errors.Is(err, auth.ErrAuthorizerUnavailable) {
		t.Fatalf("disabled authorizer should return ErrAuthorizerUnavailable, got %v", err)
	}
}

func TestPostgresACLAuthorizer_EnabledHonorsScope(t *testing.T) {
	t.Parallel()
	a := auth.NewPostgresACLAuthorizer()
	a.Enabled = true
	principal := auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"}
	a.SetScope(principal, auth.ProjectScope{IDs: []string{"proj_a"}})
	ctx := context.Background()
	ok, err := a.CanRead(ctx, principal, auth.ResourceKindProject, "proj_a")
	if err != nil || !ok {
		t.Fatalf("CanRead(proj_a) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, err = a.CanRead(ctx, principal, auth.ResourceKindProject, "proj_b")
	if err != nil || ok {
		t.Fatalf("CanRead(proj_b) = (%v, %v), want (false, nil)", ok, err)
	}
	// Mutation requires explicit AllowMutation.
	ok, _ = a.CanMutate(ctx, principal, auth.ResourceKindProject, "proj_a")
	if ok {
		t.Fatalf("CanMutate without explicit grant must fail closed")
	}
	a.AllowMutation(principal, auth.ResourceKindProject, "proj_a")
	ok, err = a.CanMutate(ctx, principal, auth.ResourceKindProject, "proj_a")
	if err != nil || !ok {
		t.Fatalf("CanMutate(granted) = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestPostgresACLAuthorizer_LookupHookWins(t *testing.T) {
	t.Parallel()
	a := auth.NewPostgresACLAuthorizer()
	a.Enabled = true
	called := false
	a.LookupACL = func(ctx context.Context, principal auth.Principal) (auth.ProjectScope, error) {
		called = true
		return auth.ProjectScope{All: true}, nil
	}
	scope, err := a.AllowedProjects(context.Background(), auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"})
	if err != nil {
		t.Fatalf("AllowedProjects: %v", err)
	}
	if !scope.All {
		t.Fatalf("hook returned scope must be honored, got %+v", scope)
	}
	if !called {
		t.Fatalf("hook must be invoked")
	}
}

func TestSystemPrincipal_Default(t *testing.T) {
	t.Parallel()
	p := auth.SystemPrincipal("")
	if p.Kind != auth.PrincipalKindSystem || p.ID != "system" {
		t.Fatalf("SystemPrincipal default: got %+v", p)
	}
	p2 := auth.SystemPrincipal("diagnosis")
	if p2.ID != "diagnosis" {
		t.Fatalf("SystemPrincipal id override: got %+v", p2)
	}
}
