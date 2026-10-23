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
	"github.com/midnight/midnight/pkg/orailtypes"
)

var errTenantEnvironmentConflictRace = errors.New("tenant environment conflict raced transaction")

// CreateEnvironment linearizes current human-session validity, canonical
// project access, idempotency, one ordinary environment row, its exact
// response, and audit evidence.
func (c *PostgresTenantMutationCoordinator) CreateEnvironment(
	ctx context.Context,
	request TenantEnvironmentCreateRequest,
) (TenantMutationResult, error) {
	if err := c.validateEnvironmentCreate(request); err != nil {
		return TenantMutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TenantMutationResult{}, err
	}

	environment := request.Environment
	environment.ID = c.IDFactory(orailtypes.IDPrefixEnvironment)
	now := c.now().Truncate(time.Microsecond)
	environment.CreatedAt = now
	environment.UpdatedAt = now
	request.Environment = environment

	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.createEnvironmentInTx(ctx, tx, request, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("tenant environment mutation serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) createEnvironmentInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantEnvironmentCreateRequest,
	now time.Time,
) (TenantMutationResult, error) {
	projectID := string(request.Environment.ProjectID)
	workspaceID, err := resolveTenantServiceProjectWorkspace(ctx, tx, projectID)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant environment workspace membership lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant environment project membership lock: %w", err)
	}

	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(request.Principal.UserID))
	mutationCtx = idempotency.WithTenant(mutationCtx, projectScopedIdempotencyTenant(workspaceID, projectID))
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
	decision := identity.EvaluateAccess(identity.AccessRequest{
		WorkspaceRole: workspaceRole,
		ProjectRole:   projectRole,
		Capability:    identity.CapabilityEnvironmentWrite,
	})
	if !decision.Allowed {
		return TenantMutationResult{}, identity.ErrAuthorizationDenied
	}
	// Protected-environment creation is a separate policy surface. Keep the
	// first human lifecycle slice ordinary-only after current authority has
	// been revalidated, but before idempotency or product state is written.
	if request.Environment.IsProtected {
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
			return TenantMutationResult{}, errors.New("tenant environment replay has no stored response")
		}
		return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	environment := request.Environment
	var existingEnvironmentID string
	err = tx.QueryRow(mutationCtx, `
		SELECT id
		FROM environments
		WHERE project_id=$1
		  AND slug=$2`,
		projectID,
		environment.Slug,
	).Scan(&existingEnvironmentID)
	if err == nil {
		response, responseErr := environmentConflictStoredResponse(request.RequestID, environment)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if _, completeErr := c.Idempotency.CompleteFailureInTx(
			mutationCtx,
			tx,
			reservation.Reference(),
			response,
		); completeErr != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant environment conflict idempotency complete: %w", completeErr)
		}
		if _, auditErr := c.Audit.AppendInTx(mutationCtx, tx, tenantEnvironmentAuditRow(
			request,
			now,
			workspaceID,
			workspaceRole,
			projectRole,
			existingEnvironmentID,
			string(failurecodes.ResourceConflict),
			"denied",
		)); auditErr != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant environment conflict audit append: %w", auditErr)
		}
		return TenantMutationResult{Response: response}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TenantMutationResult{}, fmt.Errorf("tenant environment conflict lookup: %w", err)
	}

	environment, err = c.Resources.CreateEnvironmentInTx(mutationCtx, tx, environment)
	if err != nil {
		if errors.Is(err, ErrResourceConflict) {
			return TenantMutationResult{}, fmt.Errorf("%w: %v", errTenantEnvironmentConflictRace, err)
		}
		return TenantMutationResult{}, fmt.Errorf("tenant environment insert: %w", err)
	}
	response, err := environmentCreatedStoredResponse(environment)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant environment idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(mutationCtx, tx, tenantEnvironmentAuditRow(
		request,
		now,
		workspaceID,
		workspaceRole,
		projectRole,
		string(environment.ID),
		"",
		"allowed",
	)); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant environment audit append: %w", err)
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) validateEnvironmentCreate(request TenantEnvironmentCreateRequest) error {
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
		c.Audit.Pool != c.Pool ||
		c.IDFactory == nil {
		return errors.New("tenant environment mutation coordinator does not share one postgres authority")
	}
	if request.Principal.Kind != "user" ||
		request.Principal.UserID == "" ||
		request.Principal.SessionID == "" ||
		request.Session.ID != request.Principal.SessionID ||
		request.Session.UserID != request.Principal.UserID ||
		request.Session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	environment := request.Environment
	if strings.TrimSpace(string(environment.ProjectID)) == "" {
		return fmt.Errorf("%w: project_id is required", ErrResourceInvalid)
	}
	if !serviceSlugPattern.MatchString(environment.Slug) {
		return fmt.Errorf("%w: environment slug must start with a lowercase letter, contain only lowercase letters, numbers, or hyphens, end with a letter or number, and be at most 63 characters", ErrResourceInvalid)
	}
	if strings.TrimSpace(environment.Name) == "" || utf8.RuneCountInString(environment.Name) > 200 {
		return fmt.Errorf("%w: environment name is required and must be at most 200 characters", ErrResourceInvalid)
	}
	if !validEnvironmentTier(environment.Tier) {
		return fmt.Errorf("%w: environment tier is invalid", ErrResourceInvalid)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	return idempotency.ValidateFingerprint(request.Fingerprint)
}

func environmentCreatedStoredResponse(environment Environment) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(environment)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant environment response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Location", v1alphaPrefix+"/environments/"+string(environment.ID))
	return idempotency.StoredResponse{
		StatusCode: http.StatusCreated,
		Header:     headers,
		Body:       body,
	}, nil
}

func environmentConflictStoredResponse(requestID string, environment Environment) (idempotency.StoredResponse, error) {
	message := fmt.Sprintf(
		"%s: environment slug %q already exists in project %q",
		ErrResourceConflict,
		environment.Slug,
		environment.ProjectID,
	)
	body, err := marshalErrorResponse(http.StatusConflict, failurecodes.ResourceConflict, message, requestID, map[string]any{"slug": environment.Slug})
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant environment conflict response encode: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	return idempotency.StoredResponse{
		StatusCode: http.StatusConflict,
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantEnvironmentAuditRow(
	request TenantEnvironmentCreateRequest,
	now time.Time,
	workspaceID string,
	workspaceRole identity.WorkspaceRole,
	projectRole identity.ProjectRole,
	resourceID string,
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
		ProjectID:      string(request.Environment.ProjectID),
		ResourceKind:   "environment",
		ResourceID:     resourceID,
		Action:         "environment.create",
		ReasonCode:     reasonCode,
		PolicyDecision: policyDecision,
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityEnvironmentWrite),
			"workspace_role": string(workspaceRole),
			"project_role":   string(projectRole),
			"session_id":     request.Principal.SessionID,
		},
	}
	if policyDecision == "allowed" {
		row.After = map[string]any{
			"project_id":   string(request.Environment.ProjectID),
			"slug":         request.Environment.Slug,
			"name":         request.Environment.Name,
			"tier":         request.Environment.Tier,
			"is_protected": false,
		}
	}
	return row
}
