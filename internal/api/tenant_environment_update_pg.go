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
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

func tenantEnvironmentUpdateIdempotencyPath(environmentID string) string {
	return v1alphaPrefix + "/environments/" + environmentID
}

// UpdateEnvironment linearizes current human authority, an exact-version
// ordinary metadata patch, the exact response, idempotency, and audit evidence.
func (c *PostgresTenantMutationCoordinator) UpdateEnvironment(
	ctx context.Context,
	request TenantEnvironmentUpdateRequest,
) (TenantMutationResult, error) {
	if err := c.validateEnvironmentUpdate(request); err != nil {
		return TenantMutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TenantMutationResult{}, err
	}
	now := c.now().Truncate(time.Microsecond)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var txErr error
			result, txErr = c.updateEnvironmentInTx(ctx, tx, request, now)
			return txErr
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("tenant environment update serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) updateEnvironmentInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantEnvironmentUpdateRequest,
	now time.Time,
) (TenantMutationResult, error) {
	projectID, workspaceID, err := resolveTenantEnvironmentUpdateLineage(
		ctx,
		tx,
		request.EnvironmentID,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant environment update workspace membership lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant environment update project membership lock: %w", err)
	}

	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(request.Principal.UserID))
	mutationCtx = idempotency.WithTenant(
		mutationCtx,
		projectScopedIdempotencyTenant(workspaceID, projectID),
	)
	if err := revalidateTenantMutationSessionTuple(
		mutationCtx,
		tx,
		request.Principal,
		request.Session,
		now,
	); err != nil {
		return TenantMutationResult{}, err
	}
	canonicalWorkspaceID, workspaceRole, projectRole, err := lockTenantServiceProjectAccess(
		mutationCtx,
		tx,
		projectID,
		workspaceID,
		request.Principal.UserID,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if canonicalWorkspaceID != workspaceID {
		return TenantMutationResult{}, identity.ErrAccessNotFound
	}
	current, restricted, err := lockTenantEnvironmentForUpdate(
		mutationCtx,
		tx,
		request.EnvironmentID,
		projectID,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	decision := identity.EvaluateAccess(identity.AccessRequest{
		WorkspaceRole:         workspaceRole,
		ProjectRole:           projectRole,
		Capability:            identity.CapabilityEnvironmentWrite,
		EnvironmentRestricted: restricted,
	})
	if !decision.Allowed {
		if decision.Reason == "environment-restricted" {
			return TenantMutationResult{}, identity.ErrAccessNotFound
		}
		return TenantMutationResult{}, identity.ErrAuthorizationDenied
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
			return TenantMutationResult{}, errors.New("tenant environment update replay has no stored response")
		}
		return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	if current.ResourceVersion != request.Update.ExpectedVersion {
		response, responseErr := environmentUpdateConflictStoredResponse(
			request.RequestID,
			current,
			request.Update.ExpectedVersion,
			"version",
		)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if err := c.completeEnvironmentUpdateConflict(
			mutationCtx,
			tx,
			reservation,
			request,
			current,
			current,
			now,
			workspaceID,
			workspaceRole,
			projectRole,
			response,
		); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	}

	next := current
	if request.Update.Slug != nil {
		next.Slug = *request.Update.Slug
	}
	if request.Update.Name != nil {
		next.Name = *request.Update.Name
	}
	if request.Update.Tier != nil {
		next.Tier = *request.Update.Tier
	}
	var conflictingID string
	err = tx.QueryRow(mutationCtx, `
		SELECT id
		FROM environments
		WHERE project_id=$1
		  AND slug=$2
		  AND id<>$3`,
		projectID,
		next.Slug,
		request.EnvironmentID,
	).Scan(&conflictingID)
	if err == nil {
		response, responseErr := environmentUpdateConflictStoredResponse(
			request.RequestID,
			next,
			request.Update.ExpectedVersion,
			"slug",
		)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if err := c.completeEnvironmentUpdateConflict(
			mutationCtx,
			tx,
			reservation,
			request,
			current,
			next,
			now,
			workspaceID,
			workspaceRole,
			projectRole,
			response,
		); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TenantMutationResult{}, fmt.Errorf("tenant environment update slug conflict lookup: %w", err)
	}

	updated, err := c.Resources.UpdateEnvironmentMetadataInTx(
		mutationCtx,
		tx,
		next,
		request.Update.ExpectedVersion,
	)
	if err != nil {
		if errors.Is(err, ErrResourceConflict) {
			return TenantMutationResult{}, fmt.Errorf("%w: %v", errTenantEnvironmentConflictRace, err)
		}
		return TenantMutationResult{}, fmt.Errorf("tenant environment metadata update: %w", err)
	}
	response, err := environmentUpdatedStoredResponse(updated)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant environment update idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(
		mutationCtx,
		tx,
		tenantEnvironmentUpdateAuditRow(
			request,
			now,
			workspaceID,
			workspaceRole,
			projectRole,
			current,
			updated,
			"",
			"allowed",
		),
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant environment update audit append: %w", err)
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) completeEnvironmentUpdateConflict(
	ctx context.Context,
	tx *pg.Tx,
	reservation idempotency.Reservation,
	request TenantEnvironmentUpdateRequest,
	before Environment,
	after Environment,
	now time.Time,
	workspaceID string,
	workspaceRole identity.WorkspaceRole,
	projectRole identity.ProjectRole,
	response idempotency.StoredResponse,
) error {
	if _, err := c.Idempotency.CompleteFailureInTx(
		ctx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return fmt.Errorf("tenant environment update conflict idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(
		ctx,
		tx,
		tenantEnvironmentUpdateAuditRow(
			request,
			now,
			workspaceID,
			workspaceRole,
			projectRole,
			before,
			after,
			string(failurecodes.ResourceConflict),
			"denied",
		),
	); err != nil {
		return fmt.Errorf("tenant environment update conflict audit append: %w", err)
	}
	return nil
}

func resolveTenantEnvironmentUpdateLineage(
	ctx context.Context,
	tx *pg.Tx,
	environmentID string,
) (string, string, error) {
	var projectID, workspaceID string
	err := tx.QueryRow(ctx, `
		SELECT e.project_id, p.workspace_id
		FROM environments e
		JOIN projects p ON p.id=e.project_id
		JOIN workspaces w ON w.id=p.workspace_id
		WHERE e.id=$1
		  AND e.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		  AND w.deleted_at IS NULL`,
		environmentID,
	).Scan(&projectID, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", identity.ErrAccessNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("tenant environment update lineage resolve: %w", err)
	}
	return projectID, workspaceID, nil
}

func lockTenantEnvironmentForUpdate(
	ctx context.Context,
	tx *pg.Tx,
	environmentID string,
	projectID string,
) (Environment, bool, error) {
	var environment Environment
	var restricted bool
	err := tx.QueryRow(ctx, `
		SELECT id, project_id, slug, name, tier, is_protected,
		       access_restricted, resource_version, created_at, updated_at
		FROM environments
		WHERE id=$1
		  AND project_id=$2
		  AND deleted_at IS NULL
		FOR UPDATE`,
		environmentID,
		projectID,
	).Scan(
		&environment.ID,
		&environment.ProjectID,
		&environment.Slug,
		&environment.Name,
		&environment.Tier,
		&environment.IsProtected,
		&restricted,
		&environment.ResourceVersion,
		&environment.CreatedAt,
		&environment.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, false, identity.ErrAccessNotFound
	}
	if err != nil {
		return Environment{}, false, fmt.Errorf("tenant environment update row lock: %w", err)
	}
	environment.CreatedAt = environment.CreatedAt.UTC()
	environment.UpdatedAt = environment.UpdatedAt.UTC()
	return environment, restricted, nil
}

func (c *PostgresTenantMutationCoordinator) validateEnvironmentUpdate(
	request TenantEnvironmentUpdateRequest,
) error {
	if c == nil ||
		c.Pool == nil ||
		c.Resources == nil ||
		c.Resources.Pool == nil ||
		c.Resources.Pool != c.Pool ||
		c.Idempotency == nil ||
		c.Idempotency.Pool == nil ||
		c.Idempotency.Pool != c.Pool ||
		c.Audit == nil ||
		c.Audit.Pool == nil ||
		c.Audit.Pool != c.Pool {
		return errors.New("tenant environment update coordinator does not share one postgres authority")
	}
	if request.Principal.Kind != "user" ||
		request.Principal.UserID == "" ||
		request.Principal.SessionID == "" ||
		request.Session.ID != request.Principal.SessionID ||
		request.Session.UserID != request.Principal.UserID ||
		request.Session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	if request.EnvironmentID == "" || strings.Contains(request.EnvironmentID, "/") {
		return fmt.Errorf("%w: environment id is invalid", ErrResourceInvalid)
	}
	if request.Update.ExpectedVersion < 1 {
		return fmt.Errorf("%w: expected_version must be positive", ErrResourceInvalid)
	}
	if request.Update.Slug == nil && request.Update.Name == nil && request.Update.Tier == nil {
		return fmt.Errorf("%w: at least one environment metadata field is required", ErrResourceInvalid)
	}
	if request.Update.Slug != nil && !serviceSlugPattern.MatchString(*request.Update.Slug) {
		return fmt.Errorf("%w: environment slug is invalid", ErrResourceInvalid)
	}
	if request.Update.Name != nil &&
		(strings.TrimSpace(*request.Update.Name) == "" || utf8.RuneCountInString(*request.Update.Name) > 200) {
		return fmt.Errorf("%w: environment name is required and must be at most 200 characters", ErrResourceInvalid)
	}
	if request.Update.Tier != nil && !validEnvironmentTier(*request.Update.Tier) {
		return fmt.Errorf("%w: environment tier is invalid", ErrResourceInvalid)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	if request.Fingerprint.Method != http.MethodPatch ||
		request.Fingerprint.Path != tenantEnvironmentUpdateIdempotencyPath(request.EnvironmentID) {
		return fmt.Errorf("%w: environment update fingerprint route is invalid", ErrResourceInvalid)
	}
	return nil
}

func environmentUpdatedStoredResponse(environment Environment) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(environment)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant environment update response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("ETag", strongVersionETag(environment.ResourceVersion))
	return idempotency.StoredResponse{
		StatusCode: http.StatusOK,
		Header:     headers,
		Body:       body,
	}, nil
}

func environmentUpdateConflictStoredResponse(
	requestID string,
	environment Environment,
	expectedVersion int64,
	conflictKind string,
) (idempotency.StoredResponse, error) {
	message := "resource conflict: environment metadata changed"
	details := map[string]any{
		"environment_id":   string(environment.ID),
		"expected_version": expectedVersion,
		"current_version":  environment.ResourceVersion,
		"conflict":         conflictKind,
	}
	if conflictKind == "slug" {
		message = fmt.Sprintf("resource conflict: environment slug %q already exists in project", environment.Slug)
		details["slug"] = environment.Slug
	}
	status := http.StatusConflict
	code := failurecodes.ResourceConflict
	if conflictKind == "version" {
		status = http.StatusPreconditionFailed
		code = failurecodes.PreconditionFailed
	}
	body, err := marshalErrorResponse(status, code, message, requestID, details)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant environment update conflict response encode: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	return idempotency.StoredResponse{
		StatusCode: responseStatus(body, status),
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantEnvironmentUpdateAuditRow(
	request TenantEnvironmentUpdateRequest,
	now time.Time,
	workspaceID string,
	workspaceRole identity.WorkspaceRole,
	projectRole identity.ProjectRole,
	before Environment,
	after Environment,
	reasonCode string,
	policyDecision string,
) audit.Row {
	row := audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    workspaceID,
		ProjectID:      string(before.ProjectID),
		ResourceKind:   "environment",
		ResourceID:     string(before.ID),
		Action:         "environment.update",
		ReasonCode:     reasonCode,
		PolicyDecision: policyDecision,
		Attrs: map[string]any{
			"capability":       string(identity.CapabilityEnvironmentWrite),
			"workspace_role":   string(workspaceRole),
			"project_role":     string(projectRole),
			"session_id":       request.Principal.SessionID,
			"expected_version": request.Update.ExpectedVersion,
		},
		Before: environmentMetadataAuditValue(before),
	}
	if policyDecision == "allowed" {
		row.After = environmentMetadataAuditValue(after)
	}
	return row
}

func environmentMetadataAuditValue(environment Environment) map[string]any {
	return map[string]any{
		"project_id":       string(environment.ProjectID),
		"slug":             environment.Slug,
		"name":             environment.Name,
		"tier":             environment.Tier,
		"is_protected":     environment.IsProtected,
		"resource_version": environment.ResourceVersion,
	}
}

var _ TenantEnvironmentUpdateCoordinator = (*PostgresTenantMutationCoordinator)(nil)
