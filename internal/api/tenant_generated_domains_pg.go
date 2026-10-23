// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func (c *PostgresTenantMutationCoordinator) ListGeneratedDomains(
	ctx context.Context,
	request TenantGeneratedDomainReadRequest,
) ([]TenantGeneratedDomainView, error) {
	if err := c.validateTenantGeneratedDomainAuthority(
		request.Principal,
		request.Session,
		request.ServiceID,
		request.EnvironmentID,
		false,
	); err != nil {
		return nil, err
	}
	var out []TenantGeneratedDomainView
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			mutationCtx, authorization, err := authorizeTenantDeployCapabilityInTx(
				ctx,
				tx,
				request.Principal,
				request.Session,
				request.ServiceID,
				request.EnvironmentID,
				c.now().Truncate(time.Microsecond),
				identity.CapabilityDomainsRead,
			)
			if err != nil {
				return err
			}
			out, err = listTenantGeneratedDomainViewsInTx(
				mutationCtx,
				tx,
				authorization,
				request.EnvironmentID,
				"",
				false,
			)
			return err
		})
		if err == nil {
			if out == nil {
				out = []TenantGeneratedDomainView{}
			}
			return out, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return nil, normalizeTenantGeneratedDomainError(err)
		}
	}
	return nil, errors.New("tenant generated domain list serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) GetGeneratedDomain(
	ctx context.Context,
	request TenantGeneratedDomainReadRequest,
) (TenantGeneratedDomainView, error) {
	if err := c.validateTenantGeneratedDomainAuthority(
		request.Principal,
		request.Session,
		request.ServiceID,
		request.EnvironmentID,
		false,
	); err != nil {
		return TenantGeneratedDomainView{}, err
	}
	if strings.TrimSpace(request.DomainID) == "" {
		return TenantGeneratedDomainView{}, ErrTenantGeneratedDomainNotFound
	}
	var out TenantGeneratedDomainView
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			mutationCtx, authorization, err := authorizeTenantDeployCapabilityInTx(
				ctx,
				tx,
				request.Principal,
				request.Session,
				request.ServiceID,
				request.EnvironmentID,
				c.now().Truncate(time.Microsecond),
				identity.CapabilityDomainsRead,
			)
			if err != nil {
				return err
			}
			values, err := listTenantGeneratedDomainViewsInTx(
				mutationCtx,
				tx,
				authorization,
				request.EnvironmentID,
				request.DomainID,
				false,
			)
			if err != nil {
				return err
			}
			if len(values) != 1 {
				return ErrTenantGeneratedDomainNotFound
			}
			out = values[0]
			return nil
		})
		if err == nil {
			return out, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantGeneratedDomainView{}, normalizeTenantGeneratedDomainError(err)
		}
	}
	return TenantGeneratedDomainView{}, errors.New("tenant generated domain get serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) CreateGeneratedDomain(
	ctx context.Context,
	request TenantGeneratedDomainCreateRequest,
) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if err := c.validateTenantGeneratedDomainMutation(
		request.TenantGeneratedDomainMutationMeta,
		http.MethodPost,
		false,
	); err != nil {
		return TenantMutationResult{}, err
	}
	// Resource identity is per incarnation. Mint it once before entering the
	// SERIALIZABLE retry loop so one logical create never changes identity
	// merely because PostgreSQL asked the transaction to retry.
	proposedDomainID := string(c.IDFactory(orailtypes.IDPrefixDomain))
	now := c.now().Truncate(time.Microsecond)
	var out TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			out, err = c.createTenantGeneratedDomainInTx(
				ctx,
				tx,
				request,
				proposedDomainID,
				now,
			)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(out), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, normalizeTenantGeneratedDomainError(err)
		}
	}
	return TenantMutationResult{}, errors.New("tenant generated domain create serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) DeleteGeneratedDomain(
	ctx context.Context,
	request TenantGeneratedDomainDeleteRequest,
) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if err := c.validateTenantGeneratedDomainMutation(
		request.TenantGeneratedDomainMutationMeta,
		http.MethodDelete,
		true,
	); err != nil {
		return TenantMutationResult{}, err
	}
	if request.ExpectedVersion < 1 {
		return TenantMutationResult{}, &TenantGeneratedDomainVersionConflict{
			Expected: request.ExpectedVersion,
		}
	}
	now := c.now().Truncate(time.Microsecond)
	var out TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			out, err = c.deleteTenantGeneratedDomainInTx(ctx, tx, request, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(out), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, normalizeTenantGeneratedDomainError(err)
		}
	}
	return TenantMutationResult{}, errors.New("tenant generated domain delete serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) createTenantGeneratedDomainInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantGeneratedDomainCreateRequest,
	proposedDomainID string,
	now time.Time,
) (TenantMutationResult, error) {
	mutationCtx, authorization, reservation, err := c.authorizeAndReserveTenantGeneratedDomain(
		ctx,
		tx,
		request.TenantGeneratedDomainMutationMeta,
		http.MethodPost,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if replay, ok, err := tenantGeneratedDomainReplay(reservation); ok || err != nil {
		return replay, err
	}
	if strings.TrimSpace(c.AppsDomain) == "" {
		return TenantMutationResult{}, ErrTenantGeneratedAppsDomainMissing
	}
	if err := router.AcquireRouteEnvironmentMutationLock(
		mutationCtx,
		tx,
		authorization.ProjectID,
		request.EnvironmentID,
	); err != nil {
		return TenantMutationResult{}, err
	}

	existing, err := listTenantGeneratedDomainViewsInTx(
		mutationCtx,
		tx,
		authorization,
		request.EnvironmentID,
		"",
		true,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if len(existing) > 0 {
		converged, err := c.convergeExistingTenantGeneratedDomainInTx(
			mutationCtx,
			tx,
			authorization,
			request,
			existing[0],
			now,
		)
		if err != nil {
			return TenantMutationResult{}, err
		}
		response, err := tenantGeneratedDomainJSONResponse(
			http.StatusOK,
			converged,
			tenantGeneratedDomainItemPath(
				request.ServiceID,
				request.EnvironmentID,
				converged.ID,
			),
		)
		if err != nil {
			return TenantMutationResult{}, err
		}
		if _, err := c.Idempotency.CompleteSuccessInTx(
			mutationCtx,
			tx,
			reservation.Reference(),
			response,
		); err != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant generated domain idempotency success complete: %w", err)
		}
		if err := c.appendTenantGeneratedDomainAudit(
			mutationCtx,
			tx,
			request.TenantGeneratedDomainMutationMeta,
			authorization,
			nil,
			&converged,
			now,
			"domain.generated.create",
			"already-present",
			"allowed",
		); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	}

	activeDeploymentID, targetPort, err := c.resolveTenantGeneratedDomainTargetInTx(
		mutationCtx,
		tx,
		authorization,
		request.EnvironmentID,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	scope := router.RouteScope{
		ProjectID:     authorization.ProjectID,
		EnvironmentID: request.EnvironmentID,
		ServiceID:     request.ServiceID,
	}
	domainValue, routeValue, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{
		Scope:      scope,
		Kind:       router.DomainKindGenerated,
		AppsDomain: c.AppsDomain,
		PathPrefix: "/",
		TargetPort: targetPort,
		Public:     true,
		TLSDesired: false,
	})
	if err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant generated domain intent: %w", err)
	}
	domainValue, routeValue, err = prepareProductGeneratedDomainInTx(
		mutationCtx,
		tx,
		domainValue,
		routeValue,
		proposedDomainID,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}

	domainValue, err = c.Routes.PutDomainInTx(mutationCtx, tx, domainValue)
	if err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant generated domain persist: %w", err)
	}
	if err := c.Routes.EnsureDomainLifecycleInTx(mutationCtx, tx, domainValue); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant generated domain lifecycle: %w", err)
	}
	routeValue, err = c.Routes.PutHTTPRouteInTx(mutationCtx, tx, routeValue)
	if err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant generated route persist: %w", err)
	}
	if err := c.Routes.BindServiceDomainInTx(
		mutationCtx,
		tx,
		domainValue.ID,
		routeValue.ID,
		scope,
		now,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant generated service binding: %w", err)
	}
	if _, err := bindTenantGeneratedDomainOpenDeploymentsInTx(
		mutationCtx,
		tx,
		scope,
		now,
	); err != nil {
		return TenantMutationResult{}, err
	}

	readyBackends, err := tenantGeneratedDomainReadyBackendsInTx(
		mutationCtx,
		tx,
		activeDeploymentID,
		routeValue,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	revision, err := advanceTenantGeneratedRouteGroupInTx(
		mutationCtx,
		tx,
		scope.ProjectID,
		scope.EnvironmentID,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if err := replaceTenantGeneratedDomainBackendsInTx(
		mutationCtx,
		tx,
		routeValue,
		readyBackends,
		revision,
	); err != nil {
		return TenantMutationResult{}, err
	}

	values, err := listTenantGeneratedDomainViewsInTx(
		mutationCtx,
		tx,
		authorization,
		request.EnvironmentID,
		domainValue.ID,
		false,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if len(values) != 1 {
		return TenantMutationResult{}, errors.New("tenant generated domain was not observable after create")
	}
	created := values[0]
	response, err := tenantGeneratedDomainJSONResponse(
		http.StatusCreated,
		created,
		tenantGeneratedDomainItemPath(
			request.ServiceID,
			request.EnvironmentID,
			created.ID,
		),
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant generated domain idempotency success complete: %w", err)
	}
	if err := c.appendTenantGeneratedDomainAudit(
		mutationCtx,
		tx,
		request.TenantGeneratedDomainMutationMeta,
		authorization,
		nil,
		&created,
		now,
		"domain.generated.create",
		"",
		"allowed",
	); err != nil {
		return TenantMutationResult{}, err
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) convergeExistingTenantGeneratedDomainInTx(
	ctx context.Context,
	tx *pg.Tx,
	authorization tenantDeployAuthorization,
	request TenantGeneratedDomainCreateRequest,
	existing TenantGeneratedDomainView,
	now time.Time,
) (TenantGeneratedDomainView, error) {
	activeDeploymentID, targetPort, err := c.resolveTenantGeneratedDomainTargetInTx(
		ctx,
		tx,
		authorization,
		request.EnvironmentID,
		now,
	)
	if err != nil {
		return TenantGeneratedDomainView{}, err
	}
	var routeID, appsDomain string
	err = tx.QueryRow(ctx, `
		SELECT b.route_id, d.apps_domain
		  FROM service_domain_bindings b
		  JOIN domains d
		    ON d.id=b.domain_id
		   AND d.project_id=b.project_id
		   AND d.environment_id=b.environment_id
		   AND d.service_id=b.service_id
		 WHERE b.domain_id=$1
		   AND b.project_id=$2
		   AND b.environment_id=$3
		   AND b.service_id=$4
		 FOR UPDATE OF b, d`,
		existing.ID,
		authorization.ProjectID,
		request.EnvironmentID,
		request.ServiceID,
	).Scan(&routeID, &appsDomain)
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantGeneratedDomainView{}, ErrTenantGeneratedDomainNotFound
	}
	if err != nil {
		return TenantGeneratedDomainView{}, fmt.Errorf(
			"tenant generated existing binding: %w",
			err,
		)
	}
	scope := router.RouteScope{
		ProjectID:     authorization.ProjectID,
		EnvironmentID: request.EnvironmentID,
		ServiceID:     request.ServiceID,
	}
	domainValue, routeValue, err := router.BuildDomainRouteIntent(
		router.DomainRouteIntentInput{
			Scope:      scope,
			Kind:       router.DomainKindGenerated,
			AppsDomain: appsDomain,
			PathPrefix: "/",
			TargetPort: targetPort,
			Public:     true,
			TLSDesired: false,
		},
	)
	if err != nil {
		return TenantGeneratedDomainView{}, fmt.Errorf(
			"tenant generated existing intent: %w",
			err,
		)
	}
	if domainValue.NormalizedHostname != existing.Hostname ||
		routeValue.ID != routeID {
		return TenantGeneratedDomainView{}, ErrTenantGeneratedDomainConflict
	}
	// BuildDomainRouteIntent owns the stable hostname and route identity. The
	// domain ID, however, identifies one exact product-resource incarnation.
	domainValue.ID = existing.ID
	routeValue.DomainID = existing.ID
	routeValue, err = c.Routes.PutHTTPRouteInTx(ctx, tx, routeValue)
	if err != nil {
		return TenantGeneratedDomainView{}, fmt.Errorf(
			"tenant generated existing route converge: %w",
			err,
		)
	}
	if err := c.Routes.BindServiceDomainInTx(
		ctx,
		tx,
		existing.ID,
		routeValue.ID,
		scope,
		now,
	); err != nil {
		return TenantGeneratedDomainView{}, fmt.Errorf(
			"tenant generated existing service binding: %w",
			err,
		)
	}
	if _, err := bindTenantGeneratedDomainOpenDeploymentsInTx(
		ctx,
		tx,
		scope,
		now,
	); err != nil {
		return TenantGeneratedDomainView{}, err
	}
	readyBackends, err := tenantGeneratedDomainReadyBackendsInTx(
		ctx,
		tx,
		activeDeploymentID,
		routeValue,
		now,
	)
	if err != nil {
		return TenantGeneratedDomainView{}, err
	}
	revision, err := advanceTenantGeneratedRouteGroupInTx(
		ctx,
		tx,
		scope.ProjectID,
		scope.EnvironmentID,
		now,
	)
	if err != nil {
		return TenantGeneratedDomainView{}, err
	}
	if err := replaceTenantGeneratedDomainBackendsInTx(
		ctx,
		tx,
		routeValue,
		readyBackends,
		revision,
	); err != nil {
		return TenantGeneratedDomainView{}, err
	}
	values, err := listTenantGeneratedDomainViewsInTx(
		ctx,
		tx,
		authorization,
		request.EnvironmentID,
		existing.ID,
		false,
	)
	if err != nil {
		return TenantGeneratedDomainView{}, err
	}
	if len(values) != 1 {
		return TenantGeneratedDomainView{}, errors.New(
			"tenant generated domain was not observable after convergence",
		)
	}
	return values[0], nil
}

// prepareProductGeneratedDomainInTx assigns a fresh product-domain identity
// unless a compatible, never-retired recovery-created generated singleton
// already occupies the scope. Recovery adoption preserves that row's actual
// identity; it never rewrites a custom hostname, apps-domain lineage, or TLS
// intent.
func prepareProductGeneratedDomainInTx(
	ctx context.Context,
	tx *pg.Tx,
	domainValue router.DurableDomain,
	routeValue router.DurableHTTPRoute,
	proposedDomainID string,
) (router.DurableDomain, router.DurableHTTPRoute, error) {
	proposedDomainID = strings.TrimSpace(proposedDomainID)
	if !strings.HasPrefix(proposedDomainID, "dom_") {
		return router.DurableDomain{}, router.DurableHTTPRoute{},
			fmt.Errorf("%w: generated domain id must use the dom_ prefix", router.ErrDurableRouteInvalid)
	}

	var existingDomainID, existingHostname, existingAppsDomain string
	var existingTLS bool
	err := tx.QueryRow(ctx, `
		SELECT id, normalized_hostname, apps_domain, tls_desired
		  FROM domains
		 WHERE project_id=$1
		   AND environment_id=$2
		   AND service_id=$3
		   AND kind='generated'
		 FOR UPDATE`,
		domainValue.ProjectID,
		domainValue.EnvironmentID,
		domainValue.ServiceID,
	).Scan(
		&existingDomainID,
		&existingHostname,
		&existingAppsDomain,
		&existingTLS,
	)
	switch {
	case err == nil &&
		(existingHostname != domainValue.NormalizedHostname ||
			existingAppsDomain != domainValue.AppsDomain ||
			existingTLS):
		return router.DurableDomain{}, router.DurableHTTPRoute{},
			ErrTenantGeneratedDomainConflict
	case err == nil:
		domainValue.ID = existingDomainID
	case errors.Is(err, pgx.ErrNoRows):
		domainValue.ID = proposedDomainID
	default:
		return router.DurableDomain{}, router.DurableHTTPRoute{},
			fmt.Errorf("tenant generated domain singleton lock: %w", err)
	}
	var retired bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM retired_product_domain_ids
			 WHERE domain_id=$1
		)`,
		domainValue.ID,
	).Scan(&retired); err != nil {
		return router.DurableDomain{}, router.DurableHTTPRoute{},
			fmt.Errorf("tenant generated retired identity lookup: %w", err)
	}
	if retired {
		return router.DurableDomain{}, router.DurableHTTPRoute{},
			ErrTenantGeneratedDomainRetired
	}
	routeValue.DomainID = domainValue.ID
	return domainValue, routeValue, nil
}

func (c *PostgresTenantMutationCoordinator) deleteTenantGeneratedDomainInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantGeneratedDomainDeleteRequest,
	now time.Time,
) (TenantMutationResult, error) {
	mutationCtx, authorization, reservation, err := c.authorizeAndReserveTenantGeneratedDomain(
		ctx,
		tx,
		request.TenantGeneratedDomainMutationMeta,
		http.MethodDelete,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if replay, ok, err := tenantGeneratedDomainReplay(reservation); ok || err != nil {
		return replay, err
	}
	if err := router.AcquireRouteEnvironmentMutationLock(
		mutationCtx,
		tx,
		authorization.ProjectID,
		request.EnvironmentID,
	); err != nil {
		return TenantMutationResult{}, err
	}
	values, err := listTenantGeneratedDomainViewsInTx(
		mutationCtx,
		tx,
		authorization,
		request.EnvironmentID,
		request.DomainID,
		true,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if len(values) != 1 {
		response, err := tenantGeneratedDomainErrorResponse(
			http.StatusNotFound,
			failurecodes.ResourceNotFound,
			"generated domain not found",
			request.RequestID,
			nil,
		)
		if err != nil {
			return TenantMutationResult{}, err
		}
		if _, err := c.Idempotency.CompleteFailureInTx(
			mutationCtx,
			tx,
			reservation.Reference(),
			response,
		); err != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant generated domain idempotency not-found complete: %w", err)
		}
		if err := c.appendTenantGeneratedDomainAudit(
			mutationCtx,
			tx,
			request.TenantGeneratedDomainMutationMeta,
			authorization,
			nil,
			nil,
			now,
			"domain.generated.delete",
			string(failurecodes.ResourceNotFound),
			"denied",
		); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	}
	before := values[0]
	if before.Version != request.ExpectedVersion {
		response, err := tenantGeneratedDomainErrorResponse(
			http.StatusPreconditionFailed,
			failurecodes.PreconditionFailed,
			"generated domain version changed",
			request.RequestID,
			map[string]any{
				"expected_version": request.ExpectedVersion,
				"current_version":  before.Version,
			},
		)
		if err != nil {
			return TenantMutationResult{}, err
		}
		if _, err := c.Idempotency.CompleteFailureInTx(
			mutationCtx,
			tx,
			reservation.Reference(),
			response,
		); err != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant generated domain idempotency conflict complete: %w", err)
		}
		if err := c.appendTenantGeneratedDomainAudit(
			mutationCtx,
			tx,
			request.TenantGeneratedDomainMutationMeta,
			authorization,
			&before,
			nil,
			now,
			"domain.generated.delete",
			string(failurecodes.PreconditionFailed),
			"denied",
		); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	}
	if _, err := tx.Exec(mutationCtx, `
		INSERT INTO retired_product_domain_ids (
			domain_id,project_id,environment_id,service_id,retired_at
		) VALUES ($1,$2,$3,$4,$5)`,
		before.ID,
		before.ProjectID,
		before.EnvironmentID,
		before.ServiceID,
		now,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf(
			"tenant generated domain identity retire: %w",
			err,
		)
	}
	if _, err := advanceTenantGeneratedRouteGroupInTx(
		mutationCtx,
		tx,
		authorization.ProjectID,
		request.EnvironmentID,
		now,
	); err != nil {
		return TenantMutationResult{}, err
	}
	tag, err := tx.Exec(mutationCtx, `
		DELETE FROM domains
		 WHERE id=$1
		   AND project_id=$2
		   AND environment_id=$3
		   AND service_id=$4
		   AND kind='generated'
		   AND tls_desired=FALSE
		   AND resource_version=$5
		   AND EXISTS (
		       SELECT 1
		         FROM service_domain_bindings b
		        WHERE b.domain_id=domains.id
		          AND b.project_id=domains.project_id
		          AND b.environment_id=domains.environment_id
		          AND b.service_id=domains.service_id
		   )`,
		before.ID,
		before.ProjectID,
		before.EnvironmentID,
		before.ServiceID,
		request.ExpectedVersion,
	)
	if err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant generated domain delete: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return TenantMutationResult{}, &TenantGeneratedDomainVersionConflict{
			Expected: request.ExpectedVersion,
			Current:  before.Version,
		}
	}
	response := idempotency.StoredResponse{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant generated domain idempotency delete complete: %w", err)
	}
	if err := c.appendTenantGeneratedDomainAudit(
		mutationCtx,
		tx,
		request.TenantGeneratedDomainMutationMeta,
		authorization,
		&before,
		nil,
		now,
		"domain.generated.delete",
		"",
		"allowed",
	); err != nil {
		return TenantMutationResult{}, err
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) authorizeAndReserveTenantGeneratedDomain(
	ctx context.Context,
	tx *pg.Tx,
	request TenantGeneratedDomainMutationMeta,
	method string,
	now time.Time,
) (context.Context, tenantDeployAuthorization, idempotency.Reservation, error) {
	mutationCtx, authorization, err := authorizeTenantDeployCapabilityInTx(
		ctx,
		tx,
		request.Principal,
		request.Session,
		request.ServiceID,
		request.EnvironmentID,
		now,
		identity.CapabilityDomainsWrite,
	)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, idempotency.Reservation{}, err
	}
	path := tenantGeneratedDomainCollectionPath(
		request.ServiceID,
		request.EnvironmentID,
	)
	if request.DomainID != "" {
		path = tenantGeneratedDomainItemPath(
			request.ServiceID,
			request.EnvironmentID,
			request.DomainID,
		)
	}
	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		idempotency.RequestFingerprint{
			Method:     method,
			Path:       path,
			BodySHA256: idempotency.BodyHashSHA256(request.RawBody),
		},
		24*time.Hour,
	)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, idempotency.Reservation{}, err
	}
	return mutationCtx, authorization, reservation, nil
}

func (c *PostgresTenantMutationCoordinator) resolveTenantGeneratedDomainTargetInTx(
	ctx context.Context,
	tx *pg.Tx,
	authorization tenantDeployAuthorization,
	environmentID string,
	now time.Time,
) (string, int, error) {
	activeDeploymentID, err := lockTenantDeployActiveState(
		ctx,
		tx,
		string(authorization.Service.ID),
		environmentID,
		now,
	)
	if err != nil {
		return "", 0, err
	}
	if activeDeploymentID != "" {
		var targetPort int
		err := tx.QueryRow(ctx, `
			SELECT (effective_config->>'target_port')::integer
			  FROM deployment_config_snapshots
			 WHERE deployment_id=$1
			   AND project_id=$2
			   AND service_id=$3
			   AND environment_id=$4
			 FOR SHARE`,
			activeDeploymentID,
			authorization.ProjectID,
			authorization.Service.ID,
			environmentID,
		).Scan(&targetPort)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", 0, errors.New("active deployment has no immutable service configuration snapshot")
		}
		if err != nil {
			return "", 0, fmt.Errorf("tenant generated active target port: %w", err)
		}
		if targetPort < 1 || targetPort > 65535 {
			return "", 0, errors.New("active deployment snapshot has an invalid target port")
		}
		return activeDeploymentID, targetPort, nil
	}

	var targetPort int
	err = tx.QueryRow(ctx, `
		SELECT target_port
		  FROM service_environment_configs
		 WHERE project_id=$1
		   AND service_id=$2
		   AND environment_id=$3
		 FOR SHARE`,
		authorization.ProjectID,
		authorization.Service.ID,
		environmentID,
	).Scan(&targetPort)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", serviceconfig.DefaultTargetPort, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("tenant generated desired target port: %w", err)
	}
	if targetPort < 1 || targetPort > 65535 {
		return "", 0, errors.New("service configuration has an invalid target port")
	}
	return "", targetPort, nil
}

func tenantGeneratedDomainReadyBackendsInTx(
	ctx context.Context,
	tx *pg.Tx,
	deploymentID string,
	routeValue router.DurableHTTPRoute,
	now time.Time,
) ([]router.DurableRouteBackend, error) {
	if strings.TrimSpace(deploymentID) == "" {
		return []router.DurableRouteBackend{}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT instance_id, container_ip
		  FROM deployment_instances
		 WHERE deployment_id=$1
		   AND desired_state='running'
		   AND ready=TRUE
		   AND btrim(container_ip)<>''
		 ORDER BY replica_ordinal, instance_id
		 FOR SHARE`,
		deploymentID,
	)
	if err != nil {
		return nil, fmt.Errorf("tenant generated ready instances: %w", err)
	}
	defer rows.Close()
	out := []router.DurableRouteBackend{}
	for rows.Next() {
		var instanceID, containerIP string
		if err := rows.Scan(&instanceID, &containerIP); err != nil {
			return nil, err
		}
		serviceURL := (&url.URL{
			Scheme: "http",
			Host: net.JoinHostPort(
				strings.TrimSpace(containerIP),
				fmt.Sprintf("%d", routeValue.TargetPort),
			),
		}).String()
		sum := sha256.Sum256([]byte(
			routeValue.ID + "\x00" + deploymentID + "\x00" + instanceID,
		))
		out = append(out, router.DurableRouteBackend{
			ID:           "bck_" + hex.EncodeToString(sum[:16]),
			RouteID:      routeValue.ID,
			RouteScope:   routeValue.RouteScope,
			DeploymentID: deploymentID,
			InstanceID:   instanceID,
			ServiceURL:   serviceURL,
			Ready:        true,
			ObservedAt:   now,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func replaceTenantGeneratedDomainBackendsInTx(
	ctx context.Context,
	tx *pg.Tx,
	routeValue router.DurableHTTPRoute,
	backends []router.DurableRouteBackend,
	revision int64,
) error {
	if _, err := tx.Exec(
		ctx,
		`DELETE FROM route_backends WHERE route_id=$1`,
		routeValue.ID,
	); err != nil {
		return fmt.Errorf("tenant generated route backend clear: %w", err)
	}
	for _, backend := range backends {
		if _, err := tx.Exec(ctx, `
			INSERT INTO route_backends (
				id, route_id, project_id, environment_id, service_id,
				deployment_id, instance_id, service_url, ready,
				desired_revision, observed_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,TRUE,$9,$10)`,
			backend.ID,
			routeValue.ID,
			routeValue.ProjectID,
			routeValue.EnvironmentID,
			routeValue.ServiceID,
			backend.DeploymentID,
			backend.InstanceID,
			backend.ServiceURL,
			revision,
			backend.ObservedAt,
		); err != nil {
			return fmt.Errorf("tenant generated route backend insert: %w", err)
		}
	}
	return nil
}

func bindTenantGeneratedDomainOpenDeploymentsInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope router.RouteScope,
	now time.Time,
) (int64, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id, route_id, domain_id, project_id,
			environment_id, service_id, created_at
		)
		SELECT d.id,
		       b.route_id,
		       b.domain_id,
		       b.project_id,
		       b.environment_id,
		       b.service_id,
		       $4
		  FROM deployments d
		  JOIN service_domain_bindings b
		    ON b.project_id=d.project_id
		   AND b.environment_id=d.environment_id
		   AND b.service_id=d.service_id
		  JOIN http_routes r
		    ON r.id=b.route_id
		   AND r.domain_id=b.domain_id
		   AND r.project_id=b.project_id
		   AND r.environment_id=b.environment_id
		   AND r.service_id=b.service_id
		  JOIN domains dom
		    ON dom.id=b.domain_id
		   AND dom.project_id=b.project_id
		   AND dom.environment_id=b.environment_id
		   AND dom.service_id=b.service_id
		 WHERE d.project_id=$1
		   AND d.environment_id=$2
		   AND d.service_id=$3
		   AND d.state IN ('queued','building','deploying','active')
		   AND dom.kind='generated'
		   AND dom.tls_desired=FALSE
		   AND dom.tls_state='disabled'
		   AND r.public_enabled=TRUE
		ON CONFLICT (deployment_id, route_id) DO NOTHING`,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
		now.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf(
			"tenant generated open deployment bindings: %w",
			err,
		)
	}
	return tag.RowsAffected(), nil
}

func advanceTenantGeneratedRouteGroupInTx(
	ctx context.Context,
	tx *pg.Tx,
	projectID string,
	environmentID string,
	now time.Time,
) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, `
		UPDATE route_controller_groups
		   SET desired_revision=desired_revision+1,
		       apply_state='pending',
		       last_error_code='',
		       last_error_detail='',
		       updated_at=$1
		 WHERE project_id=$2
		   AND environment_id=$3
		RETURNING desired_revision`,
		now,
		projectID,
		environmentID,
	).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("tenant generated route group advance: %w", err)
	}
	return revision, nil
}

func listTenantGeneratedDomainViewsInTx(
	ctx context.Context,
	tx *pg.Tx,
	authorization tenantDeployAuthorization,
	environmentID string,
	domainID string,
	lock bool,
) ([]TenantGeneratedDomainView, error) {
	query := `
		SELECT d.id,
		       d.project_id,
		       d.service_id,
		       d.environment_id,
		       d.normalized_hostname,
		       r.path_prefix,
		       r.target_port,
		       d.resource_version,
		       CASE
		           WHEN NOT EXISTS (
		               SELECT 1
		                 FROM route_backends rb
		                WHERE rb.route_id=r.id
		                  AND rb.project_id=r.project_id
		                  AND rb.environment_id=r.environment_id
		                  AND rb.service_id=r.service_id
		                  AND rb.ready=TRUE
		           ) THEN 'blocked_backend'
		           ELSE COALESCE(g.apply_state, 'pending')
		       END,
		       COALESCE((
		           EXISTS (
		               SELECT 1
		                 FROM route_backends rb
		                WHERE rb.route_id=r.id
		                  AND rb.project_id=r.project_id
		                  AND rb.environment_id=r.environment_id
		                  AND rb.service_id=r.service_id
		                  AND rb.ready=TRUE
		           )
		           AND g.apply_state='ready'
		           AND g.desired_revision=g.observed_revision
		           AND g.desired_revision=g.applied_revision
		           AND EXISTS (
		               SELECT 1
		                 FROM route_publication_observations o
		                WHERE o.project_id=g.project_id
		                  AND o.environment_id=g.environment_id
		                  AND o.desired_revision=g.desired_revision
		                  AND o.artifact_hash=g.artifact_hash
		                  AND o.state='ready'
		                  AND o.traefik_acknowledged=TRUE
		                  AND o.ingress_probe_succeeded=TRUE
		           )
		       ), FALSE),
		       d.created_at,
		       d.updated_at
		  FROM service_domain_bindings b
		  JOIN domains d
		    ON d.id=b.domain_id
		   AND d.project_id=b.project_id
		   AND d.environment_id=b.environment_id
		   AND d.service_id=b.service_id
		  JOIN http_routes r
		    ON r.id=b.route_id
		   AND r.domain_id=b.domain_id
		   AND r.project_id=b.project_id
		   AND r.environment_id=b.environment_id
		   AND r.service_id=b.service_id
		  LEFT JOIN route_controller_groups g
		    ON g.project_id=b.project_id
		   AND g.environment_id=b.environment_id
		 WHERE b.project_id=$1
		   AND b.environment_id=$2
		   AND b.service_id=$3
		   AND ($4='' OR d.id=$4)
		   AND d.kind='generated'
		   AND d.tls_desired=FALSE
		   AND d.tls_state='disabled'
		   AND r.path_prefix='/'
		   AND r.public_enabled=TRUE
		 ORDER BY d.normalized_hostname, d.id`
	if lock {
		query += ` FOR UPDATE OF b, d, r`
	}
	rows, err := tx.Query(
		ctx,
		query,
		authorization.ProjectID,
		environmentID,
		authorization.Service.ID,
		strings.TrimSpace(domainID),
	)
	if err != nil {
		return nil, fmt.Errorf("tenant generated domain query: %w", err)
	}
	defer rows.Close()
	out := []TenantGeneratedDomainView{}
	for rows.Next() {
		var value TenantGeneratedDomainView
		if err := rows.Scan(
			&value.ID,
			&value.ProjectID,
			&value.ServiceID,
			&value.EnvironmentID,
			&value.Hostname,
			&value.Path,
			&value.TargetPort,
			&value.Version,
			&value.RouteApplyState,
			&value.RouteReachable,
			&value.CreatedAt,
			&value.UpdatedAt,
		); err != nil {
			return nil, err
		}
		value.WorkspaceID = authorization.WorkspaceID
		value.Kind = router.DomainKindGenerated
		value.Public = true
		value.TLS = false
		value.CreatedAt = value.CreatedAt.UTC()
		value.UpdatedAt = value.UpdatedAt.UTC()
		out = append(out, value)
	}
	return out, rows.Err()
}

func (c *PostgresTenantMutationCoordinator) validateTenantGeneratedDomainAuthority(
	principal identity.Principal,
	session identity.Session,
	serviceID string,
	environmentID string,
	write bool,
) error {
	if c == nil || c.Pool == nil || c.Routes == nil ||
		c.Routes.Pool != c.Pool {
		return ErrTenantGeneratedDomainsUnavailable
	}
	if write &&
		(c.Idempotency == nil ||
			c.Idempotency.Pool != c.Pool ||
			c.Audit == nil ||
			c.Audit.Pool != c.Pool ||
			c.IDFactory == nil) {
		return ErrTenantGeneratedDomainsUnavailable
	}
	if principal.Kind != "user" ||
		principal.UserID == "" ||
		principal.SessionID == "" ||
		session.ID != principal.SessionID ||
		session.UserID != principal.UserID ||
		session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	if strings.TrimSpace(serviceID) == "" ||
		strings.TrimSpace(environmentID) == "" {
		return identity.ErrAccessNotFound
	}
	return nil
}

func (c *PostgresTenantMutationCoordinator) validateTenantGeneratedDomainMutation(
	request TenantGeneratedDomainMutationMeta,
	method string,
	requireID bool,
) error {
	if err := c.validateTenantGeneratedDomainAuthority(
		request.Principal,
		request.Session,
		request.ServiceID,
		request.EnvironmentID,
		true,
	); err != nil {
		return err
	}
	if requireID && strings.TrimSpace(request.DomainID) == "" {
		return ErrTenantGeneratedDomainNotFound
	}
	if !requireID && strings.TrimSpace(request.DomainID) != "" {
		return idempotency.ErrInvalidFingerprint
	}
	if request.Method != method || len(request.RawBody) == 0 {
		return idempotency.ErrInvalidFingerprint
	}
	return idempotency.ValidateKey(request.Key)
}

func tenantGeneratedDomainReplay(
	reservation idempotency.Reservation,
) (TenantMutationResult, bool, error) {
	if !reservation.IsTerminal() {
		return TenantMutationResult{}, false, nil
	}
	if reservation.Response == nil {
		return TenantMutationResult{}, true, errors.New("tenant generated domain replay has no stored response")
	}
	return TenantMutationResult{
		Response: reservation.Response.Clone(),
		Replayed: true,
	}, true, nil
}

func tenantGeneratedDomainCollectionPath(serviceID, environmentID string) string {
	return v1alphaPrefix +
		"/services/" + serviceID +
		"/environments/" + environmentID +
		"/domains"
}

func tenantGeneratedDomainItemPath(
	serviceID string,
	environmentID string,
	domainID string,
) string {
	return tenantGeneratedDomainCollectionPath(serviceID, environmentID) +
		"/" + domainID
}

func tenantGeneratedDomainJSONResponse(
	status int,
	value TenantGeneratedDomainView,
	location string,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant generated domain response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if location != "" {
		headers.Set("Location", location)
	}
	return idempotency.StoredResponse{
		StatusCode: responseStatus(body, status),
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantGeneratedDomainErrorResponse(
	status int,
	code failurecodes.Code,
	message string,
	requestID string,
	details map[string]any,
) (idempotency.StoredResponse, error) {
	body, err := marshalErrorResponse(status, code, message, requestID, details)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	return idempotency.StoredResponse{
		StatusCode: status,
		Header:     headers,
		Body:       body,
	}, nil
}

func (c *PostgresTenantMutationCoordinator) appendTenantGeneratedDomainAudit(
	ctx context.Context,
	tx *pg.Tx,
	request TenantGeneratedDomainMutationMeta,
	authorization tenantDeployAuthorization,
	before *TenantGeneratedDomainView,
	after *TenantGeneratedDomainView,
	now time.Time,
	action string,
	reasonCode string,
	decision string,
) error {
	resourceID := request.DomainID
	if after != nil {
		resourceID = after.ID
	} else if before != nil {
		resourceID = before.ID
	}
	row := audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    authorization.WorkspaceID,
		ProjectID:      authorization.ProjectID,
		EnvironmentID:  request.EnvironmentID,
		ResourceKind:   "domain",
		ResourceID:     resourceID,
		Action:         action,
		ReasonCode:     reasonCode,
		PolicyDecision: decision,
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityDomainsWrite),
			"workspace_role": string(authorization.WorkspaceRole),
			"project_role":   string(authorization.ProjectRole),
			"session_id":     request.Principal.SessionID,
			"service_id":     request.ServiceID,
		},
	}
	if before != nil {
		row.Before = tenantGeneratedDomainAuditView(*before)
	}
	if after != nil && decision == "allowed" {
		row.After = tenantGeneratedDomainAuditView(*after)
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, row); err != nil {
		return fmt.Errorf("tenant generated domain audit append: %w", err)
	}
	return nil
}

func tenantGeneratedDomainAuditView(value TenantGeneratedDomainView) map[string]any {
	return map[string]any{
		"id":             value.ID,
		"hostname":       value.Hostname,
		"path":           value.Path,
		"target_port":    value.TargetPort,
		"version":        value.Version,
		"public":         value.Public,
		"tls":            value.TLS,
		"service_id":     value.ServiceID,
		"environment_id": value.EnvironmentID,
	}
}

func normalizeTenantGeneratedDomainError(err error) error {
	switch {
	case errors.Is(err, router.ErrDurableRouteNotFound):
		return ErrTenantGeneratedDomainNotFound
	case errors.Is(err, router.ErrDurableHostnameOwned),
		errors.Is(err, router.ErrDurableRouteScopeMismatch):
		return ErrTenantGeneratedDomainConflict
	default:
		return err
	}
}

var _ TenantGeneratedDomainCoordinator = (*PostgresTenantMutationCoordinator)(nil)
