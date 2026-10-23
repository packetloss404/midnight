// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

const maxManagedAutomationTokenLifetime = 365 * 24 * time.Hour

// PostgresAutomationTokenMutationCoordinator is the product write boundary
// for project credentials. It commits current-session authorization,
// service-account identity, verifier-only token state, encrypted replay, and
// audit evidence in one serializable transaction.
type PostgresAutomationTokenMutationCoordinator struct {
	Pool        *pg.Pool
	Access      identity.AccessStore
	Tokens      *automationtoken.Manager
	TokenStore  *automationtoken.PostgresStore
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain
	Now         func() time.Time
}

func NewPostgresAutomationTokenMutationCoordinator(
	pool *pg.Pool,
	access identity.AccessStore,
	tokens *automationtoken.Manager,
	tokenStore *automationtoken.PostgresStore,
	idem *idempotency.PostgresStore,
	chain *audit.PostgresChain,
	now func() time.Time,
) *PostgresAutomationTokenMutationCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresAutomationTokenMutationCoordinator{
		Pool: pool, Access: access, Tokens: tokens, TokenStore: tokenStore,
		Idempotency: idem, Audit: chain, Now: now,
	}
}

func (c *PostgresAutomationTokenMutationCoordinator) MintAutomationToken(
	ctx context.Context,
	request AutomationTokenMintMutationRequest,
) (TenantMutationResult, error) {
	if err := c.validateMintRequest(request); err != nil {
		return TenantMutationResult{}, err
	}
	grant, _, err := identity.Authorize(
		ctx,
		c.Access,
		request.Principal.UserID,
		identity.AccessTarget{ProjectID: request.ProjectID},
		identity.CapabilityProjectManage,
		false,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	workspaceID := strings.TrimSpace(grant.Target.WorkspaceID)
	if workspaceID == "" {
		return TenantMutationResult{}, identity.ErrAccessNotFound
	}
	now := c.now().Truncate(time.Microsecond)
	if !request.ExpiresAt.After(now) ||
		request.ExpiresAt.Sub(now) > maxManagedAutomationTokenLifetime {
		return TenantMutationResult{}, automationtoken.ErrInvalidRequest
	}
	serviceAccountID := projectAutomationServiceAccountID(request.ProjectID)
	plan, err := c.Tokens.PrepareMint(automationtoken.MintRequest{
		ServiceAccountID: serviceAccountID,
		WorkspaceID:      workspaceID,
		ProjectID:        request.ProjectID,
		Name:             request.Name,
		Profile:          automationtoken.ProfileCIDeployV1,
		CreatedByUserID:  request.Principal.UserID,
		ExpiresAt:        request.ExpiresAt.UTC(),
	})
	if err != nil {
		return TenantMutationResult{}, err
	}
	defer plan.Destroy()

	tenantCtx := automationTokenMutationContext(ctx, request.Principal.UserID, workspaceID, request.ProjectID)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(tenantCtx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var txErr error
			result, txErr = c.mintAutomationTokenInTx(
				tenantCtx, tx, request, workspaceID, serviceAccountID, plan, now,
			)
			return txErr
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("automation token mutation serializable retries exhausted")
}

func (c *PostgresAutomationTokenMutationCoordinator) mintAutomationTokenInTx(
	ctx context.Context,
	tx *pg.Tx,
	request AutomationTokenMintMutationRequest,
	expectedWorkspaceID string,
	serviceAccountID string,
	plan *automationtoken.MintPlan,
	now time.Time,
) (TenantMutationResult, error) {
	workspaceID, workspaceRole, projectRole, err := lockAutomationTokenProjectAccess(
		ctx, tx, request.Principal.UserID, request.ProjectID,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if workspaceID != expectedWorkspaceID {
		return TenantMutationResult{}, identity.ErrAccessNotFound
	}
	if err := revalidateAutomationTokenSession(ctx, tx, request.Principal, request.Session, now); err != nil {
		return TenantMutationResult{}, err
	}
	decision := identity.EvaluateAccess(identity.AccessRequest{
		WorkspaceRole: workspaceRole,
		ProjectRole:   projectRole,
		Capability:    identity.CapabilityProjectManage,
	})
	if !decision.Allowed {
		return TenantMutationResult{}, identity.ErrAuthorizationDenied
	}

	reservation, err := c.Idempotency.ReserveInTx(ctx, tx, request.Key, request.Fingerprint, 24*time.Hour)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantMutationResult{}, idempotency.ErrSensitiveResponseUnavailable
		}
		return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}
	if err := ensureProjectAutomationServiceAccount(
		ctx, tx, serviceAccountID, workspaceID, request.ProjectID, now,
	); err != nil {
		return TenantMutationResult{}, err
	}
	minted, err := plan.Commit(ctx, automationTokenTxStore{store: c.TokenStore, tx: tx})
	if err != nil {
		return TenantMutationResult{}, err
	}
	response, err := automationTokenCreatedResponse(minted)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSensitiveSuccessInTx(
		ctx, tx, reservation.Reference(), response,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("automation token encrypted idempotency completion: %w", err)
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    workspaceID,
		ProjectID:      request.ProjectID,
		ResourceKind:   "automation_token",
		ResourceID:     minted.Token.ID,
		Action:         "automation_token.create",
		PolicyDecision: "allowed",
		After: map[string]any{
			"name":               minted.Token.Name,
			"token_prefix":       minted.Token.TokenPrefix,
			"profile":            string(minted.Token.Profile),
			"expires_at":         minted.Token.ExpiresAt,
			"service_account_id": minted.Token.ServiceAccountID,
		},
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityProjectManage),
			"workspace_role": string(workspaceRole),
			"project_role":   string(projectRole),
			"session_id":     request.Session.ID,
		},
	}); err != nil {
		return TenantMutationResult{}, fmt.Errorf("automation token create audit append: %w", err)
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresAutomationTokenMutationCoordinator) RevokeAutomationToken(
	ctx context.Context,
	request AutomationTokenRevokeMutationRequest,
) (TenantMutationResult, error) {
	if err := c.validateRevokeRequest(request); err != nil {
		return TenantMutationResult{}, err
	}
	grant, _, err := identity.Authorize(
		ctx,
		c.Access,
		request.Principal.UserID,
		identity.AccessTarget{ProjectID: request.ProjectID},
		identity.CapabilityProjectManage,
		false,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	workspaceID := strings.TrimSpace(grant.Target.WorkspaceID)
	if workspaceID == "" {
		return TenantMutationResult{}, identity.ErrAccessNotFound
	}
	now := c.now().Truncate(time.Microsecond)
	tenantCtx := automationTokenMutationContext(ctx, request.Principal.UserID, workspaceID, request.ProjectID)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(tenantCtx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var txErr error
			result, txErr = c.revokeAutomationTokenInTx(tenantCtx, tx, request, workspaceID, now)
			return txErr
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("automation token revocation serializable retries exhausted")
}

func (c *PostgresAutomationTokenMutationCoordinator) revokeAutomationTokenInTx(
	ctx context.Context,
	tx *pg.Tx,
	request AutomationTokenRevokeMutationRequest,
	expectedWorkspaceID string,
	now time.Time,
) (TenantMutationResult, error) {
	workspaceID, workspaceRole, projectRole, err := lockAutomationTokenProjectAccess(
		ctx, tx, request.Principal.UserID, request.ProjectID,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if workspaceID != expectedWorkspaceID {
		return TenantMutationResult{}, identity.ErrAccessNotFound
	}
	if err := revalidateAutomationTokenSession(ctx, tx, request.Principal, request.Session, now); err != nil {
		return TenantMutationResult{}, err
	}
	decision := identity.EvaluateAccess(identity.AccessRequest{
		WorkspaceRole: workspaceRole,
		ProjectRole:   projectRole,
		Capability:    identity.CapabilityProjectManage,
	})
	if !decision.Allowed {
		return TenantMutationResult{}, identity.ErrAuthorizationDenied
	}
	reservation, err := c.Idempotency.ReserveInTx(ctx, tx, request.Key, request.Fingerprint, 24*time.Hour)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantMutationResult{}, errors.New("automation token revoke replay has no stored response")
		}
		return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}
	revoked, err := c.Tokens.RevokeWith(ctx, automationtoken.RevokeRequest{
		TokenID:         request.TokenID,
		WorkspaceID:     workspaceID,
		ProjectID:       request.ProjectID,
		RevokedByUserID: request.Principal.UserID,
		Reason:          "user-requested",
		ExpectedVersion: &request.ExpectedVersion,
	}, automationTokenTxStore{store: c.TokenStore, tx: tx})
	if errors.Is(err, automationtoken.ErrTokenNotFound) {
		response, responseErr := automationTokenNotFoundResponse(request.RequestID)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if _, completeErr := c.Idempotency.CompleteFailureInTx(
			ctx, tx, reservation.Reference(), response,
		); completeErr != nil {
			return TenantMutationResult{}, completeErr
		}
		if _, auditErr := c.Audit.AppendInTx(ctx, tx, automationTokenAuditRow(
			request, workspaceID, workspaceRole, projectRole, now,
			"denied", string(failurecodes.ResourceNotFound), nil,
		)); auditErr != nil {
			return TenantMutationResult{}, auditErr
		}
		return TenantMutationResult{Response: response}, nil
	}
	if errors.Is(err, automationtoken.ErrVersionConflict) {
		response, responseErr := automationTokenPreconditionFailedResponse(request.RequestID)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if _, completeErr := c.Idempotency.CompleteFailureInTx(ctx, tx, reservation.Reference(), response); completeErr != nil {
			return TenantMutationResult{}, completeErr
		}
		if _, auditErr := c.Audit.AppendInTx(ctx, tx, automationTokenAuditRow(request, workspaceID, workspaceRole, projectRole, now, "denied", string(failurecodes.PreconditionFailed), nil)); auditErr != nil {
			return TenantMutationResult{}, auditErr
		}
		return TenantMutationResult{Response: response}, nil
	}
	if err != nil {
		return TenantMutationResult{}, err
	}
	response := idempotency.StoredResponse{
		StatusCode: http.StatusNoContent,
		Header:     http.Header{"Cache-Control": []string{"no-store"}},
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(ctx, tx, reservation.Reference(), response); err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, automationTokenAuditRow(
		request, workspaceID, workspaceRole, projectRole, now,
		"allowed", "", &revoked,
	)); err != nil {
		return TenantMutationResult{}, err
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresAutomationTokenMutationCoordinator) validateMintRequest(request AutomationTokenMintMutationRequest) error {
	if err := c.ready(); err != nil {
		return err
	}
	if err := validateAutomationTokenSessionTuple(request.Principal, request.Session); err != nil {
		return err
	}
	if strings.TrimSpace(request.ProjectID) == "" ||
		strings.TrimSpace(request.Name) == "" ||
		len(strings.TrimSpace(request.Name)) > 128 ||
		request.ExpiresAt.IsZero() {
		return automationtoken.ErrInvalidRequest
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	return idempotency.ValidateFingerprint(request.Fingerprint)
}

func (c *PostgresAutomationTokenMutationCoordinator) validateRevokeRequest(request AutomationTokenRevokeMutationRequest) error {
	if err := c.ready(); err != nil {
		return err
	}
	if err := validateAutomationTokenSessionTuple(request.Principal, request.Session); err != nil {
		return err
	}
	if strings.TrimSpace(request.ProjectID) == "" || strings.TrimSpace(request.TokenID) == "" {
		return automationtoken.ErrInvalidRequest
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	return idempotency.ValidateFingerprint(request.Fingerprint)
}

func (c *PostgresAutomationTokenMutationCoordinator) ready() error {
	if c == nil || c.Pool == nil || c.Access == nil || c.Tokens == nil ||
		c.TokenStore == nil || c.Idempotency == nil || c.Audit == nil {
		return errors.New("automation token mutation coordinator is not fully configured")
	}
	return nil
}

func validateAutomationTokenSessionTuple(principal identity.Principal, session identity.Session) error {
	if principal.Kind != "user" || principal.UserID == "" ||
		principal.SessionID == "" || session.ID != principal.SessionID ||
		session.UserID != principal.UserID || session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	return nil
}

func revalidateAutomationTokenSession(
	ctx context.Context,
	tx *pg.Tx,
	principal identity.Principal,
	session identity.Session,
	now time.Time,
) error {
	return revalidateTenantMutationSession(ctx, tx, TenantProjectCreateRequest{
		Principal: principal,
		Session:   session,
	}, now)
}

func lockAutomationTokenProjectAccess(
	ctx context.Context,
	tx *pg.Tx,
	userID string,
	projectID string,
) (string, identity.WorkspaceRole, identity.ProjectRole, error) {
	var workspaceID string
	if err := tx.QueryRow(ctx, `
		SELECT workspace_id
		FROM projects
		WHERE id=$1 AND deleted_at IS NULL
		FOR UPDATE`,
		projectID,
	).Scan(&workspaceID); errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", identity.ErrAccessNotFound
	} else if err != nil {
		return "", "", "", fmt.Errorf("automation token project lock: %w", err)
	}
	var workspaceRole identity.WorkspaceRole
	workspaceErr := tx.QueryRow(ctx, `
		SELECT role
		FROM workspace_members
		WHERE workspace_id=$1 AND user_id=$2 AND accepted_at IS NOT NULL
		FOR UPDATE`,
		workspaceID, userID,
	).Scan(&workspaceRole)
	if workspaceErr != nil && !errors.Is(workspaceErr, pgx.ErrNoRows) {
		return "", "", "", fmt.Errorf("automation token workspace membership lock: %w", workspaceErr)
	}
	var projectRole identity.ProjectRole
	projectErr := tx.QueryRow(ctx, `
		SELECT role
		FROM project_members
		WHERE project_id=$1 AND user_id=$2 AND accepted_at IS NOT NULL
		FOR UPDATE`,
		projectID, userID,
	).Scan(&projectRole)
	if projectErr != nil && !errors.Is(projectErr, pgx.ErrNoRows) {
		return "", "", "", fmt.Errorf("automation token project membership lock: %w", projectErr)
	}
	if errors.Is(workspaceErr, pgx.ErrNoRows) && errors.Is(projectErr, pgx.ErrNoRows) {
		return "", "", "", identity.ErrAccessNotFound
	}
	return workspaceID, workspaceRole, projectRole, nil
}

func ensureProjectAutomationServiceAccount(
	ctx context.Context,
	tx *pg.Tx,
	serviceAccountID string,
	workspaceID string,
	projectID string,
	now time.Time,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO service_accounts (
			id, workspace_id, slug, display_name, created_at, updated_at
		)
		VALUES ($1,$2,$1,$3,$4,$4)
		ON CONFLICT (id) DO NOTHING`,
		serviceAccountID,
		workspaceID,
		"Project automation "+projectID,
		now,
	)
	if err != nil {
		return fmt.Errorf("ensure project automation service account: %w", err)
	}
	var active bool
	err = tx.QueryRow(ctx, `
		SELECT TRUE
		FROM service_accounts
		WHERE id=$1 AND workspace_id=$2 AND slug=$1 AND deleted_at IS NULL
		FOR UPDATE`,
		serviceAccountID, workspaceID,
	).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || !active {
		return automationtoken.ErrScopeNotFound
	}
	if err != nil {
		return fmt.Errorf("validate project automation service account: %w", err)
	}
	return nil
}

type automationTokenTxStore struct {
	store *automationtoken.PostgresStore
	tx    *pg.Tx
}

func (s automationTokenTxStore) Create(ctx context.Context, credential automationtoken.Credential) (automationtoken.Token, error) {
	return s.store.CreateInTx(ctx, s.tx, credential)
}

func (s automationTokenTxStore) Revoke(ctx context.Context, request automationtoken.RevokeRequest, now time.Time) (automationtoken.Token, error) {
	return s.store.RevokeInTx(ctx, s.tx, request, now)
}

func automationTokenCreatedResponse(result automationtoken.MintResult) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("automation token response encode: %w", err)
	}
	body = append(body, '\n')
	return idempotency.StoredResponse{
		StatusCode: http.StatusCreated,
		Header: http.Header{
			"Content-Type":  []string{"application/json"},
			"Cache-Control": []string{"no-store"},
			"Location": []string{
				v1alphaPrefix + "/projects/" + result.Token.ProjectID +
					automationTokenRouteSuffix + "/" + result.Token.ID,
			},
		},
		Body: body,
	}, nil
}

func automationTokenNotFoundResponse(requestID string) (idempotency.StoredResponse, error) {
	body, err := marshalErrorResponse(http.StatusNotFound, failurecodes.ResourceNotFound, "project automation token scope not found", requestID, nil)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	return idempotency.StoredResponse{
		StatusCode: http.StatusNotFound,
		Header: http.Header{
			"Content-Type":  []string{"application/problem+json"},
			"Cache-Control": []string{"no-store"},
		},
		Body: append(body, '\n'),
	}, nil
}

func automationTokenPreconditionFailedResponse(requestID string) (idempotency.StoredResponse, error) {
	body, err := marshalErrorResponse(http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "If-Match does not identify the current automation token version", requestID, nil)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	return idempotency.StoredResponse{StatusCode: http.StatusPreconditionFailed, Header: http.Header{"Content-Type": []string{"application/problem+json"}, "Cache-Control": []string{"no-store"}}, Body: append(body, '\n')}, nil
}

func automationTokenAuditRow(
	request AutomationTokenRevokeMutationRequest,
	workspaceID string,
	workspaceRole identity.WorkspaceRole,
	projectRole identity.ProjectRole,
	now time.Time,
	policyDecision string,
	reasonCode string,
	token *automationtoken.Token,
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
		ProjectID:      request.ProjectID,
		ResourceKind:   "automation_token",
		ResourceID:     request.TokenID,
		Action:         "automation_token.revoke",
		ReasonCode:     reasonCode,
		PolicyDecision: policyDecision,
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityProjectManage),
			"workspace_role": string(workspaceRole),
			"project_role":   string(projectRole),
			"session_id":     request.Session.ID,
		},
	}
	if token != nil {
		row.Before = map[string]any{
			"name":         token.Name,
			"token_prefix": token.TokenPrefix,
			"profile":      string(token.Profile),
		}
		row.After = map[string]any{
			"revoked_at":     token.RevokedAt,
			"revoked_reason": token.RevokedReason,
		}
	}
	return row
}

func automationTokenMutationContext(
	ctx context.Context,
	userID string,
	workspaceID string,
	projectID string,
) context.Context {
	ctx = idempotency.WithActor(ctx, idempotency.UserActor(userID))
	return idempotency.WithTenant(ctx, projectScopedIdempotencyTenant(workspaceID, projectID))
}

func projectScopedIdempotencyTenant(workspaceID, projectID string) idempotency.Tenant {
	return idempotency.Tenant(fmt.Sprintf(
		"workspace:%d:%s|project:%d:%s",
		len(workspaceID), workspaceID, len(projectID), projectID,
	))
}

func projectAutomationServiceAccountID(projectID string) string {
	digest := sha256.Sum256([]byte("midnight/project-automation-service-account/v1\x00" + projectID))
	return "sa_" + base64.RawURLEncoding.EncodeToString(digest[:18])
}

func (c *PostgresAutomationTokenMutationCoordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

var _ AutomationTokenMutationCoordinator = (*PostgresAutomationTokenMutationCoordinator)(nil)
