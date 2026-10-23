package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
)

type DurableRouteIntentCoordinator struct {
	Store router.DomainWorkflowStore
}

func (c DurableRouteIntentCoordinator) EnsureDeployRoute(ctx context.Context, payload DeployJobPayload, candidate runtime.ScheduleRequest) error {
	if payload.Route == nil || !payload.Route.Public {
		return nil
	}
	if c.Store == nil {
		return errors.New("durable route store is required")
	}
	managed, ok := c.Store.(router.ManagedServiceRouteStore)
	if !ok {
		return errors.New("managed service route binding authority is required")
	}
	_, err := managed.BindManagedServiceRouteToDeployment(
		ctx,
		candidate.DeploymentID,
		router.RouteScope{
			ProjectID: candidate.ProjectID, EnvironmentID: candidate.EnvironmentID, ServiceID: candidate.ServiceID,
		},
		router.ManagedRouteSelector{
			Host: payload.Route.Host, Name: payload.Route.Name,
			Group: payload.Route.Group, PathPrefix: payload.Route.Path,
			TLSDesired: payload.Route.TLS,
		},
		time.Now().UTC(),
	)
	return err
}

type DurableRoutePromotionStore interface {
	LoadRouteGroup(context.Context, string, string) (router.DurableRouteGroup, error)
	ListDeploymentPublicRouteStates(context.Context, string, router.RouteScope) ([]router.DurableDesiredRoute, error)
	ReplaceEligibleRouteGroupBackendsCAS(context.Context, string, string, int64, []router.DurableRouteBackendSet) ([]router.DurableRouteBackendSet, int64, error)
	RestoreEligibleRouteGroupBackendsWithOwnershipCAS(context.Context, string, string, int64, []router.DurableRouteBackendSet) ([]router.DurableRouteBackendSet, int64, error)
}

const defaultDurableRouteCompensationTimeout = 30 * time.Second

// DurableRoutePromoter replaces the legacy process-local single-route writer.
// It stages ready candidates on every exact deployment-bound generated route,
// waits for one route-group acknowledgement/probe gate, commits candidate-only
// sets as one group mutation, verifies once more, and moves canonical state
// last. Any later failure restores the complete captured route set.
type DurableRoutePromoter struct {
	Store               DurableRoutePromotionStore
	Reconciler          *router.DurableRouteReconciler
	CompensationTimeout time.Duration
}

// HasEligibleDeploymentPublicRouteBindings refreshes the exact durable route
// truth for a deployment. Executors use this narrow read immediately before
// promotion so a route attached after admission cannot be missed because of a
// stale job payload.
func (p DurableRoutePromoter) HasEligibleDeploymentPublicRouteBindings(
	ctx context.Context,
	deploymentID string,
	scope router.RouteScope,
) (bool, error) {
	if p.Store == nil {
		return false, errors.New("durable route promotion store is required")
	}
	states, err := p.Store.ListDeploymentPublicRouteStates(
		ctx,
		strings.TrimSpace(deploymentID),
		scope,
	)
	if err != nil {
		return false, err
	}
	if len(states) == 0 {
		return false, nil
	}
	seenRoutes := make(map[string]struct{}, len(states))
	for _, state := range states {
		if err := validateDeploymentPublicRouteState(
			state,
			scope,
			seenRoutes,
		); err != nil {
			return false, err
		}
	}
	return true, nil
}

// PromoteReplicas publishes the complete ready replica set on every bound
// hostname. The method remains separate to preserve the executor and legacy
// Traefik RoutePromoter interfaces.
func (p DurableRoutePromoter) PromoteReplicas(ctx context.Context, targets []runtime.RoutingTarget, metadata RoutePromotionMetadata) error {
	return p.PromoteReplicasWithCommit(ctx, targets, metadata, func(context.Context) error { return nil })
}

func (p DurableRoutePromoter) PromoteReplicasWithCommit(ctx context.Context, targets []runtime.RoutingTarget, metadata RoutePromotionMetadata, commit func(context.Context) error) error {
	if !metadata.Public {
		return nil
	}
	if len(targets) == 0 || p.Store == nil || p.Reconciler == nil {
		return fmt.Errorf("%w: replica route promotion is not configured", ErrRoutePromotionPublishFailed)
	}
	scope, deploymentID, err := validateDurablePromotionTargets(targets, metadata)
	if err != nil {
		return err
	}
	group, err := p.Store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		return fmt.Errorf("%w: load durable route group: %w", ErrRoutePromotionPublishFailed, err)
	}
	states, err := p.Store.ListDeploymentPublicRouteStates(
		ctx,
		deploymentID,
		scope,
	)
	if err != nil {
		return fmt.Errorf(
			"%w: list deployment public route set: %w",
			ErrRoutePromotionPublishFailed,
			err,
		)
	}
	if len(states) == 0 {
		return fmt.Errorf(
			"%w: public deployment %s has no exact public route binding",
			ErrRoutePromotionPublishFailed,
			deploymentID,
		)
	}
	previous, staged, committed, err := durablePromotionBackendSets(
		states,
		targets,
		scope,
		metadata,
	)
	if err != nil {
		return err
	}

	revision := group.DesiredRevision
	var nextRevision int64
	if _, nextRevision, err = p.Store.ReplaceEligibleRouteGroupBackendsCAS(
		ctx,
		scope.ProjectID,
		scope.EnvironmentID,
		revision,
		staged,
	); err != nil {
		return fmt.Errorf(
			"%w: stage durable route-group backends: %w",
			ErrRoutePromotionPublishFailed,
			err,
		)
	}
	revision = nextRevision
	if err = p.Reconciler.ReconcileGroup(ctx, scope.ProjectID, scope.EnvironmentID); err == nil {
		// A blocked route elsewhere in the environment no longer fails the
		// group, so the promoter can no longer infer from a successful
		// reconcile that its own route was published. Confirm that directly:
		// this deployment is only promoted once every route it owns carries a
		// ready backend in the reconciled group.
		err = p.verifyOwnRoutesPublished(ctx, scope, deploymentID, states)
	}
	if err != nil {
		return p.restoreDurableRouteGroup(
			ctx,
			scope,
			revision,
			previous,
			fmt.Errorf(
				"%w: staged route group did not produce acknowledgement and ingress evidence: %w",
				ErrRoutePromotionPublishFailed,
				err,
			),
		)
	}
	if _, nextRevision, err = p.Store.ReplaceEligibleRouteGroupBackendsCAS(
		ctx,
		scope.ProjectID,
		scope.EnvironmentID,
		revision,
		committed,
	); err != nil {
		return p.restoreDurableRouteGroup(
			ctx,
			scope,
			revision,
			previous,
			fmt.Errorf(
				"%w: commit durable route-group backends: %w",
				ErrRoutePromotionPublishFailed,
				err,
			),
		)
	}
	revision = nextRevision
	if err = p.Reconciler.ReconcileGroup(ctx, scope.ProjectID, scope.EnvironmentID); err != nil {
		return p.restoreDurableRouteGroup(
			ctx,
			scope,
			revision,
			previous,
			fmt.Errorf(
				"%w: committed route-group verification failed: %w",
				ErrRoutePromotionPublishFailed,
				err,
			),
		)
	}
	// The canonical CAS is intentionally last: no fallible route operation is
	// allowed after the active pointer moves. If it loses a race, restore the
	// exact captured whole-route set before reporting failure to the executor.
	if commit != nil {
		if err = commit(ctx); err != nil {
			return p.restoreDurableRouteGroup(
				ctx,
				scope,
				revision,
				previous,
				fmt.Errorf(
					"%w: canonical deployment commit failed: %w",
					ErrRoutePromotionPublishFailed,
					err,
				),
			)
		}
	}
	return nil
}

func (p DurableRoutePromoter) Promote(ctx context.Context, target runtime.RoutingTarget, metadata RoutePromotionMetadata) error {
	return p.PromoteWithCommit(ctx, target, metadata, func(context.Context) error { return nil })
}

func (p DurableRoutePromoter) PromoteWithCommit(ctx context.Context, target runtime.RoutingTarget, metadata RoutePromotionMetadata, commit func(context.Context) error) error {
	return p.PromoteReplicasWithCommit(
		ctx,
		[]runtime.RoutingTarget{target},
		metadata,
		commit,
	)
}

func validateDurablePromotionTargets(
	targets []runtime.RoutingTarget,
	metadata RoutePromotionMetadata,
) (router.RouteScope, string, error) {
	if len(targets) == 0 {
		return router.RouteScope{}, "", fmt.Errorf(
			"%w: at least one routing target is required",
			ErrRoutePromotionInvalidTarget,
		)
	}
	scope := router.RouteScope{
		ProjectID:     strings.TrimSpace(targets[0].ProjectID),
		EnvironmentID: strings.TrimSpace(targets[0].EnvironmentID),
		ServiceID:     strings.TrimSpace(metadata.ServiceID),
	}
	deploymentID := strings.TrimSpace(targets[0].DeploymentID)
	if scope.ProjectID == "" ||
		scope.EnvironmentID == "" ||
		scope.ServiceID == "" ||
		deploymentID == "" {
		return router.RouteScope{}, "", fmt.Errorf(
			"%w: deployment and exact route scope are required",
			ErrRoutePromotionInvalidTarget,
		)
	}
	seenInstances := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.DeploymentID) != deploymentID ||
			strings.TrimSpace(target.ProjectID) != scope.ProjectID ||
			strings.TrimSpace(target.EnvironmentID) != scope.EnvironmentID {
			return router.RouteScope{}, "", fmt.Errorf(
				"%w: replica targets must share one deployment and route scope",
				ErrRoutePromotionInvalidTarget,
			)
		}
		instanceID := strings.TrimSpace(target.InstanceID)
		if instanceID == "" {
			return router.RouteScope{}, "", fmt.Errorf(
				"%w: replica instance id is required",
				ErrRoutePromotionInvalidTarget,
			)
		}
		if _, duplicate := seenInstances[instanceID]; duplicate {
			return router.RouteScope{}, "", fmt.Errorf(
				"%w: duplicate replica instance %s",
				ErrRoutePromotionInvalidTarget,
				instanceID,
			)
		}
		seenInstances[instanceID] = struct{}{}
		if !target.Ready {
			return router.RouteScope{}, "", fmt.Errorf(
				"%w: replica %s is not ready",
				ErrRoutePromotionInvalidTarget,
				instanceID,
			)
		}
	}
	return scope, deploymentID, nil
}

func durablePromotionBackendSets(
	states []router.DurableDesiredRoute,
	targets []runtime.RoutingTarget,
	scope router.RouteScope,
	metadata RoutePromotionMetadata,
) (
	[]router.DurableRouteBackendSet,
	[]router.DurableRouteBackendSet,
	[]router.DurableRouteBackendSet,
	error,
) {
	deploymentID := targets[0].DeploymentID
	previous := make([]router.DurableRouteBackendSet, len(states))
	staged := make([]router.DurableRouteBackendSet, len(states))
	committed := make([]router.DurableRouteBackendSet, len(states))
	seenRoutes := make(map[string]struct{}, len(states))
	for index, state := range states {
		if err := validateDeploymentPublicRouteState(
			state,
			scope,
			seenRoutes,
		); err != nil {
			return nil, nil, nil, fmt.Errorf(
				"%w: %v",
				ErrRoutePromotionPublishFailed,
				err,
			)
		}
		priorBackends := append(
			[]router.DurableRouteBackend(nil),
			state.Backends...,
		)
		candidateBackends := make([]router.DurableRouteBackend, 0, len(targets))
		for _, target := range targets {
			backend, err := durableBackendForTarget(
				state.Route,
				scope,
				target,
				metadata,
			)
			if err != nil {
				return nil, nil, nil, err
			}
			candidateBackends = append(candidateBackends, backend)
		}
		stagedBackends := withoutDeploymentBackends(
			priorBackends,
			deploymentID,
		)
		stagedBackends = append(stagedBackends, candidateBackends...)
		previous[index] = router.DurableRouteBackendSet{
			RouteID:  state.Route.ID,
			Backends: priorBackends,
		}
		staged[index] = router.DurableRouteBackendSet{
			RouteID:  state.Route.ID,
			Backends: stagedBackends,
		}
		committed[index] = router.DurableRouteBackendSet{
			RouteID:  state.Route.ID,
			Backends: candidateBackends,
		}
	}
	return previous, staged, committed, nil
}

func validateDeploymentPublicRouteState(
	state router.DurableDesiredRoute,
	scope router.RouteScope,
	seenRoutes map[string]struct{},
) error {
	routeID := strings.TrimSpace(state.Route.ID)
	if routeID == "" ||
		state.Route.RouteScope != scope ||
		state.Domain.RouteScope != scope ||
		state.Route.DomainID != state.Domain.ID ||
		!state.Route.Public {
		return errors.New("deployment route binding returned an ineligible public route")
	}
	switch state.Domain.Kind {
	case router.DomainKindGenerated, router.DomainKindCustom:
	default:
		return errors.New("deployment route binding returned an unsupported domain kind")
	}
	if state.Domain.TLSDesired {
		if state.Domain.TLSState != router.DomainTLSReady {
			return fmt.Errorf(
				"deployment route %s requires TLS without ready certificate evidence",
				routeID,
			)
		}
	} else if state.Domain.TLSState != router.DomainTLSDisabled {
		return fmt.Errorf(
			"deployment route %s has inconsistent disabled TLS lifecycle state",
			routeID,
		)
	}
	if _, duplicate := seenRoutes[routeID]; duplicate {
		return fmt.Errorf(
			"deployment route binding returned duplicate route %s",
			routeID,
		)
	}
	seenRoutes[routeID] = struct{}{}
	return nil
}

func (p DurableRoutePromoter) restoreDurableRouteGroup(
	ctx context.Context,
	scope router.RouteScope,
	expectedRevision int64,
	previous []router.DurableRouteBackendSet,
	primary error,
) error {
	timeout := p.CompensationTimeout
	if timeout <= 0 {
		timeout = defaultDurableRouteCompensationTimeout
	}
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	_, _, restoreErr := p.Store.RestoreEligibleRouteGroupBackendsWithOwnershipCAS(
		restoreCtx,
		scope.ProjectID,
		scope.EnvironmentID,
		expectedRevision,
		previous,
	)
	if restoreErr != nil {
		restoreErr = fmt.Errorf(
			"restore exact durable route-group backend set: %w",
			restoreErr,
		)
	}
	reconcileErr := p.Reconciler.ReconcileGroup(
		restoreCtx,
		scope.ProjectID,
		scope.EnvironmentID,
	)
	if reconcileErr != nil {
		reconcileErr = fmt.Errorf(
			"reconcile restored durable route group: %w",
			reconcileErr,
		)
	}
	return errors.Join(primary, restoreErr, reconcileErr)
}

func durableBackendForTarget(routeValue router.DurableHTTPRoute, scope router.RouteScope, target runtime.RoutingTarget, metadata RoutePromotionMetadata) (router.DurableRouteBackend, error) {
	host := strings.TrimSpace(target.ContainerIP)
	if host == "" {
		return router.DurableRouteBackend{}, fmt.Errorf("%w: routing target has no scoped container IP", ErrRoutePromotionInvalidTarget)
	}
	port := target.InternalPort
	if port == 0 {
		port = metadata.InternalPort
	}
	if port == 0 {
		port = routeValue.TargetPort
	}
	serviceURL := (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, fmt.Sprintf("%d", port))}).String()
	sum := sha256.Sum256([]byte(routeValue.ID + "\x00" + target.DeploymentID + "\x00" + target.InstanceID))
	return router.DurableRouteBackend{
		ID: "bck_" + hex.EncodeToString(sum[:16]), RouteID: routeValue.ID, RouteScope: scope,
		DeploymentID: target.DeploymentID, InstanceID: target.InstanceID,
		ServiceURL: serviceURL, Ready: target.Ready,
	}, nil
}

func withoutDeploymentBackends(values []router.DurableRouteBackend, deploymentID string) []router.DurableRouteBackend {
	out := make([]router.DurableRouteBackend, 0, len(values))
	for _, value := range values {
		if value.DeploymentID != deploymentID {
			out = append(out, value)
		}
	}
	return out
}

// verifyOwnRoutesPublished reports whether every public route this deployment
// owns came out of reconciliation with a ready backend. The group-wide reconcile
// deliberately tolerates a sibling service whose route has no backend, so this
// is what preserves the promotion guarantee for the deployment being promoted.
func (p DurableRoutePromoter) verifyOwnRoutesPublished(
	ctx context.Context,
	scope router.RouteScope,
	deploymentID string,
	states []router.DurableDesiredRoute,
) error {
	if len(states) == 0 {
		return nil
	}
	group, err := p.Store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		return fmt.Errorf("%w: reload route group after reconcile: %w", ErrRoutePromotionPublishFailed, err)
	}
	ready := make(map[string]bool, len(group.Routes))
	for _, route := range group.Routes {
		for _, backend := range route.Backends {
			if backend.Ready && strings.TrimSpace(backend.DeploymentID) == deploymentID {
				ready[route.Route.ID] = true
				break
			}
		}
	}
	for _, state := range states {
		if !ready[state.Route.ID] {
			return fmt.Errorf(
				"%w: route %s was not published with a ready backend for deployment %s",
				router.ErrDurableRouteBlocked,
				state.Route.ID,
				deploymentID,
			)
		}
	}
	return nil
}
