// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"errors"

	"github.com/midnight/midnight/internal/router"
)

type DurableActionRouteStore interface {
	LoadRouteGroup(context.Context, string, string) (router.DurableRouteGroup, error)
	ListDeploymentPublicRouteStates(context.Context, string, router.RouteScope) ([]router.DurableDesiredRoute, error)
	ReplaceRouteGroupBackendsCAS(context.Context, string, string, int64, []router.DurableRouteBackendSet) ([]router.DurableRouteBackendSet, int64, error)
}

type DurableActionRouteWithdrawer struct {
	Store      DurableActionRouteStore
	Reconciler *router.DurableRouteReconciler
}

func (w DurableActionRouteWithdrawer) WithdrawDeployment(ctx context.Context, action RuntimeActionRecord) error {
	if w.Store == nil {
		return nil
	}
	group, err := w.Store.LoadRouteGroup(ctx, action.ProjectID, action.EnvironmentID)
	if errors.Is(err, router.ErrDurableRouteNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	scope := router.RouteScope{
		ProjectID:     action.ProjectID,
		EnvironmentID: action.EnvironmentID,
		ServiceID:     action.ServiceID,
	}
	states, err := w.Store.ListDeploymentPublicRouteStates(
		ctx,
		action.DeploymentID,
		scope,
	)
	if err != nil {
		return err
	}
	if len(states) == 0 {
		return nil
	}
	if w.Reconciler == nil {
		return errors.New("durable action route reconciler is required")
	}
	sets := make([]router.DurableRouteBackendSet, 0, len(states))
	for _, state := range states {
		filtered := make([]router.DurableRouteBackend, 0, len(state.Backends))
		for _, backend := range state.Backends {
			if backend.DeploymentID == action.DeploymentID &&
				(action.InstanceID == "" || backend.InstanceID == action.InstanceID) {
				continue
			}
			filtered = append(filtered, backend)
		}
		sets = append(sets, router.DurableRouteBackendSet{
			RouteID:  state.Route.ID,
			Backends: filtered,
		})
	}
	if _, _, err := w.Store.ReplaceRouteGroupBackendsCAS(
		ctx,
		action.ProjectID,
		action.EnvironmentID,
		group.DesiredRevision,
		sets,
	); err != nil {
		return err
	}
	return w.Reconciler.ReconcileGroup(
		ctx,
		action.ProjectID,
		action.EnvironmentID,
	)
}
