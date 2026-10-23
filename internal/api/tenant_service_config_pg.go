// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
)

// GetServiceConfig returns either the explicit desired configuration or the
// observable version-zero default after revalidating current human authority
// on the exact service/environment lineage.
func (c *PostgresTenantMutationCoordinator) GetServiceConfig(
	ctx context.Context,
	request TenantServiceConfigReadRequest,
) (ServiceConfigView, error) {
	if err := c.validateServiceConfigAuthority(
		request.Principal,
		request.Session,
		request.ServiceID,
		request.EnvironmentID,
		false,
	); err != nil {
		return ServiceConfigView{}, err
	}
	if err := ctx.Err(); err != nil {
		return ServiceConfigView{}, err
	}

	now := c.now().Truncate(time.Microsecond)
	var view ServiceConfigView
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			mutationCtx, authorization, err := authorizeTenantDeployCapabilityInTx(
				ctx,
				tx,
				request.Principal,
				request.Session,
				request.ServiceID,
				request.EnvironmentID,
				now,
				identity.CapabilityServiceRead,
			)
			if err != nil {
				return err
			}
			cfg, err := c.ServiceConfigs.GetInTx(
				mutationCtx,
				tx,
				authorization.ProjectID,
				request.EnvironmentID,
				request.ServiceID,
			)
			explicit := true
			if errors.Is(err, serviceconfig.ErrNotFound) {
				cfg = serviceconfig.Default(
					authorization.ProjectID,
					request.ServiceID,
					request.EnvironmentID,
				)
				explicit = false
			} else if err != nil {
				return fmt.Errorf("tenant service config lookup: %w", err)
			}
			view = serviceConfigView(cfg, explicit)
			return nil
		})
		if err == nil {
			return view, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return ServiceConfigView{}, err
		}
	}
	return ServiceConfigView{}, errors.New("tenant service config read serializable retries exhausted")
}

// PutServiceConfig commits the exact CAS result, replay response, and audit
// evidence in the same serializable transaction as current authorization.
func (c *PostgresTenantMutationCoordinator) PutServiceConfig(
	ctx context.Context,
	request TenantServiceConfigPutRequest,
) (TenantMutationResult, error) {
	if err := c.validateServiceConfigAuthority(
		request.Principal,
		request.Session,
		request.Config.ServiceID,
		request.Config.EnvironmentID,
		true,
	); err != nil {
		return TenantMutationResult{}, err
	}
	if request.ExpectedVersion < 0 {
		return TenantMutationResult{}, fmt.Errorf("%w: expected_version must be non-negative", serviceconfig.ErrInvalid)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return TenantMutationResult{}, err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return TenantMutationResult{}, err
	}
	canonicalPath := tenantServiceConfigIdempotencyPath(
		request.Config.ServiceID,
		request.Config.EnvironmentID,
	)
	if request.Fingerprint.Method != http.MethodPut ||
		request.Fingerprint.Path != canonicalPath {
		return TenantMutationResult{}, fmt.Errorf(
			"%w: tenant service config fingerprint must use concrete PUT %s",
			idempotency.ErrInvalidFingerprint,
			canonicalPath,
		)
	}
	if err := ctx.Err(); err != nil {
		return TenantMutationResult{}, err
	}

	now := c.now().Truncate(time.Microsecond)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.putServiceConfigInTx(ctx, tx, request, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("tenant service config mutation serializable retries exhausted")
}

// tenantServiceConfigIdempotencyPath binds a caller key to the exact
// service/environment pair. Neither identifier appears in the request body,
// so a route template would permit cross-resource replay inside one project.
func tenantServiceConfigIdempotencyPath(serviceID string, environmentID string) string {
	return v1alphaPrefix + "/services/" + serviceID +
		"/environments/" + environmentID + "/config"
}

func (c *PostgresTenantMutationCoordinator) putServiceConfigInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantServiceConfigPutRequest,
	now time.Time,
) (TenantMutationResult, error) {
	mutationCtx, authorization, err := authorizeTenantDeployCapabilityInTx(
		ctx,
		tx,
		request.Principal,
		request.Session,
		request.Config.ServiceID,
		request.Config.EnvironmentID,
		now,
		identity.CapabilityServiceWrite,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}

	cfg := request.Config
	cfg.ProjectID = authorization.ProjectID
	cfg = serviceconfig.Normalize(cfg)
	if err := serviceconfig.Validate(cfg); err != nil {
		return TenantMutationResult{}, err
	}

	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantMutationResult{}, errors.New("tenant service config replay has no stored response")
		}
		return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	saved, err := c.ServiceConfigs.PutInTx(
		mutationCtx,
		tx,
		cfg,
		request.ExpectedVersion,
	)
	if errors.Is(err, serviceconfig.ErrConflict) {
		response, responseErr := serviceConfigConflictStoredResponse(
			request.RequestID,
			request.ExpectedVersion,
		)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if _, completeErr := c.Idempotency.CompleteFailureInTx(
			mutationCtx,
			tx,
			reservation.Reference(),
			response,
		); completeErr != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant service config conflict idempotency complete: %w", completeErr)
		}
		if _, auditErr := c.Audit.AppendInTx(
			mutationCtx,
			tx,
			tenantServiceConfigAuditRow(
				request,
				now,
				authorization,
				0,
				string(failurecodes.PreconditionFailed),
				"denied",
			),
		); auditErr != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant service config conflict audit append: %w", auditErr)
		}
		return TenantMutationResult{Response: response}, nil
	}
	if err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service config save: %w", err)
	}

	response, err := serviceConfigSuccessStoredResponse(saved)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service config idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(
		mutationCtx,
		tx,
		tenantServiceConfigAuditRow(
			request,
			now,
			authorization,
			saved.Version,
			"",
			"allowed",
		),
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service config audit append: %w", err)
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) validateServiceConfigAuthority(
	principal identity.Principal,
	session identity.Session,
	serviceID string,
	environmentID string,
	write bool,
) error {
	if c == nil ||
		c.Pool == nil ||
		c.ServiceConfigs == nil ||
		c.ServiceConfigs.Pool == nil ||
		c.ServiceConfigs.Pool != c.Pool {
		return errors.New("tenant service config coordinator does not share one postgres config authority")
	}
	if write &&
		(c.ServiceConfigs.Now == nil ||
			c.Idempotency == nil ||
			c.Idempotency.Pool == nil ||
			c.Idempotency.Pool != c.Pool ||
			c.Audit == nil ||
			c.Audit.Pool == nil ||
			c.Audit.Pool != c.Pool) {
		return errors.New("tenant service config mutation does not share idempotency and audit authority")
	}
	if principal.Kind != "user" ||
		principal.UserID == "" ||
		principal.SessionID == "" ||
		session.ID != principal.SessionID ||
		session.UserID != principal.UserID ||
		session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	if strings.TrimSpace(serviceID) == "" || strings.TrimSpace(environmentID) == "" {
		return identity.ErrAccessNotFound
	}
	return nil
}

func serviceConfigSuccessStoredResponse(cfg serviceconfig.Config) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(serviceConfigView(cfg, true))
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant service config response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("ETag", strongVersionETag(cfg.Version))
	return idempotency.StoredResponse{
		StatusCode: http.StatusOK,
		Header:     headers,
		Body:       body,
	}, nil
}

func serviceConfigConflictStoredResponse(
	requestID string,
	expectedVersion int64,
) (idempotency.StoredResponse, error) {
	body, err := marshalErrorResponse(http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "service config version conflict", requestID, map[string]any{"expected_version": expectedVersion})
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant service config conflict response encode: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	return idempotency.StoredResponse{
		StatusCode: http.StatusPreconditionFailed,
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantServiceConfigAuditRow(
	request TenantServiceConfigPutRequest,
	now time.Time,
	authorization tenantDeployAuthorization,
	version int64,
	reasonCode string,
	policyDecision string,
) audit.Row {
	cfg := serviceconfig.Normalize(request.Config)
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
		ResourceKind:   "service_environment_config",
		ResourceID:     request.Config.ServiceID + ":" + request.Config.EnvironmentID,
		Action:         "service.config.put",
		ReasonCode:     reasonCode,
		PolicyDecision: policyDecision,
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityServiceWrite),
			"workspace_role": string(authorization.WorkspaceRole),
			"project_role":   string(authorization.ProjectRole),
			"session_id":     request.Principal.SessionID,
		},
	}
	if policyDecision == "allowed" {
		row.After = map[string]any{
			"version":               version,
			"builder":               cfg.Builder,
			"target_port":           cfg.TargetPort,
			"health_kind":           cfg.HealthKind,
			"restart_policy":        cfg.RestartPolicy,
			"replicas":              cfg.Replicas,
			"runtime_kind":          cfg.RuntimeKind,
			"cron_schedule":         cfg.CronSchedule,
			"has_build_command":     cfg.BuildCommand != "",
			"has_start_command":     cfg.StartCommand != "",
			"has_predeploy_command": cfg.PredeployCommand != "",
		}
	}
	return row
}
