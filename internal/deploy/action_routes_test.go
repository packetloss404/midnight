// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/router"
)

func TestDurableActionRouteWithdrawerClearsExactDeploymentAcrossGroupOnce(t *testing.T) {
	scope := router.RouteScope{
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
	}
	group := promotionGroup(router.DurableRouteBackend{
		ID: "route-1-action", RouteID: "route-1", RouteScope: scope,
		DeploymentID: "dep-action", InstanceID: "dep-action-r0",
		ServiceURL: "http://10.0.0.2:8080", Ready: true,
	})
	group.Routes[0].Backends = append(
		group.Routes[0].Backends,
		router.DurableRouteBackend{
			ID: "route-1-other", RouteID: "route-1", RouteScope: scope,
			DeploymentID: "dep-other", InstanceID: "dep-other-r0",
			ServiceURL: "http://10.0.0.9:8080", Ready: true,
		},
	)
	group.Routes = append(group.Routes, router.DurableDesiredRoute{
		Domain: router.DurableDomain{
			ID: "domain-2", RouteScope: scope, Kind: router.DomainKindCustom,
			NormalizedHostname: "app-two.example.test",
			TLSDesired:         true,
			TLSState:           router.DomainTLSReady,
		},
		Route: router.DurableHTTPRoute{
			ID: "route-2", DomainID: "domain-2", RouteScope: scope,
			Name: "app-two", PathPrefix: "/", TargetPort: 8080, Public: true,
		},
		Backends: []router.DurableRouteBackend{
			{
				ID: "route-2-action", RouteID: "route-2", RouteScope: scope,
				DeploymentID: "dep-action", InstanceID: "dep-action-r0",
				ServiceURL: "http://10.0.0.2:8080", Ready: true,
			},
			{
				ID: "route-2-other", RouteID: "route-2", RouteScope: scope,
				DeploymentID: "dep-other", InstanceID: "dep-other-r0",
				ServiceURL: "http://10.0.0.9:8080", Ready: true,
			},
		},
	})
	store := &promotionStoreFake{group: group}
	publisher := &promotionPublisherFake{}
	withdrawer := DurableActionRouteWithdrawer{
		Store:      store,
		Reconciler: promotionReadyReconciler(store, publisher),
	}
	err := withdrawer.WithdrawDeployment(
		context.Background(),
		RuntimeActionRecord{
			DeploymentID:  "dep-action",
			ProjectID:     scope.ProjectID,
			EnvironmentID: scope.EnvironmentID,
			ServiceID:     scope.ServiceID,
			Action:        RuntimeActionStop,
		},
	)
	if err != nil {
		t.Fatalf("WithdrawDeployment(): %v", err)
	}
	if len(store.history) != 1 || len(store.history[0]) != 2 {
		t.Fatalf("batch history = %#v, want one two-route replacement", store.history)
	}
	if publisher.calls != 1 {
		t.Fatalf("route-group reconciles = %d, want 1", publisher.calls)
	}
	if len(store.group.Routes) != 2 {
		t.Fatalf("route bindings changed: routes=%#v", store.group.Routes)
	}
	for _, desired := range store.group.Routes {
		if len(desired.Backends) != 1 ||
			desired.Backends[0].DeploymentID != "dep-other" {
			t.Fatalf("withdrawn route %s backends = %#v", desired.Route.ID, desired.Backends)
		}
	}
}

func TestDurableActionRouteWithdrawerFailsBeforeMutationWithoutReconciler(t *testing.T) {
	scope := router.RouteScope{
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
	}
	store := &promotionStoreFake{group: promotionGroup(router.DurableRouteBackend{
		ID: "route-1-action", RouteID: "route-1", RouteScope: scope,
		DeploymentID: "dep-action", InstanceID: "dep-action-r0",
		ServiceURL: "http://10.0.0.2:8080", Ready: true,
	})}
	err := (DurableActionRouteWithdrawer{Store: store}).WithdrawDeployment(
		context.Background(),
		RuntimeActionRecord{
			DeploymentID:  "dep-action",
			ProjectID:     scope.ProjectID,
			EnvironmentID: scope.EnvironmentID,
			ServiceID:     scope.ServiceID,
			Action:        RuntimeActionStop,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "reconciler is required") {
		t.Fatalf("WithdrawDeployment() error = %v, want reconciler requirement", err)
	}
	if len(store.history) != 0 {
		t.Fatalf("backend mutations = %#v, want none", store.history)
	}
	if got := store.group.Routes[0].Backends; len(got) != 1 ||
		got[0].DeploymentID != "dep-action" {
		t.Fatalf("route backends = %#v, want original deployment", got)
	}
}

func TestDurableActionRouteWithdrawerPublishesLastBackendRemoval(t *testing.T) {
	scope := router.RouteScope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"}
	store := &promotionStoreFake{group: promotionGroup(router.DurableRouteBackend{ID: "backend", RouteID: "route-1", RouteScope: scope, DeploymentID: "dep-action", InstanceID: "dep-action-r0", ServiceURL: "http://10.0.0.2:8080", Ready: true})}
	publisher := &promotionPublisherFake{}
	w := DurableActionRouteWithdrawer{Store: store, Reconciler: promotionReadyReconciler(store, publisher)}
	if err := w.WithdrawDeployment(context.Background(), RuntimeActionRecord{DeploymentID: "dep-action", ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID, ServiceID: scope.ServiceID}); err != nil {
		t.Fatal(err)
	}
	if len(store.group.Routes[0].Backends) != 0 || publisher.calls != 1 {
		t.Fatalf("last backend retained or withdrawal unpublished: backends=%v publications=%d", store.group.Routes[0].Backends, publisher.calls)
	}
}
