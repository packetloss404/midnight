// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

// IdentityClaimCommitRequest contains the fully validated one-time claim and
// the redacted idempotency fingerprint. Password hashing, TOTP verification,
// and TOTP envelope encryption are deliberately completed before this value
// crosses the transaction boundary.
type IdentityClaimCommitRequest struct {
	Claim       identity.ClaimDefaultWorkspaceRequest
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

// IdentityClaimMutationResult is the exact response committed beside the
// identity claim and its audit evidence.
type IdentityClaimMutationResult struct {
	Response idempotency.StoredResponse
	Replayed bool
}

// IdentityClaimCoordinator is an opt-in durable mutation boundary. Memory and
// custom access stores continue to use the legacy AccessStore seam.
type IdentityClaimCoordinator interface {
	ClaimDefaultWorkspace(context.Context, IdentityClaimCommitRequest) (IdentityClaimMutationResult, error)
}

// PostgresIdentityClaimCoordinator commits the one-time identity claim, its
// audit row, and its encrypted exact terminal response in one SERIALIZABLE
// transaction. Every participant must share Pool; mismatches fail closed.
type PostgresIdentityClaimCoordinator struct {
	Pool        *pg.Pool
	Identity    *identity.PostgresStore
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain

	// beforeCommit is a deterministic test seam. Returning an error rolls back
	// identity, audit, and idempotency. Production cannot configure it.
	beforeCommit func() error
}

func NewPostgresIdentityClaimCoordinator(
	pool *pg.Pool,
	identityStore *identity.PostgresStore,
	idempotencyStore *idempotency.PostgresStore,
	auditChain *audit.PostgresChain,
) *PostgresIdentityClaimCoordinator {
	return &PostgresIdentityClaimCoordinator{
		Pool:        pool,
		Identity:    identityStore,
		Idempotency: idempotencyStore,
		Audit:       auditChain,
	}
}

func (c *PostgresIdentityClaimCoordinator) ClaimDefaultWorkspace(
	ctx context.Context,
	request IdentityClaimCommitRequest,
) (IdentityClaimMutationResult, error) {
	if err := c.validate(request); err != nil {
		return IdentityClaimMutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return IdentityClaimMutationResult{}, err
	}

	var result IdentityClaimMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.claimDefaultWorkspaceInTx(ctx, tx, request)
			if err != nil {
				return err
			}
			if c.beforeCommit != nil && !result.Replayed {
				return c.beforeCommit()
			}
			return nil
		})
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !identityClaimTransactionRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return IdentityClaimMutationResult{}, err
		}
	}
	return IdentityClaimMutationResult{}, errors.New("identity claim serializable retries exhausted")
}

func (c *PostgresIdentityClaimCoordinator) setBeforeCommitForTest(hook func() error) {
	c.beforeCommit = hook
}

func (c *PostgresIdentityClaimCoordinator) claimDefaultWorkspaceInTx(
	ctx context.Context,
	tx *pg.Tx,
	request IdentityClaimCommitRequest,
) (IdentityClaimMutationResult, error) {
	reservation, err := c.Idempotency.ReserveInTx(ctx, tx, request.Key, request.Fingerprint, 24*time.Hour)
	if err != nil {
		return IdentityClaimMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return IdentityClaimMutationResult{}, errors.New("identity claim replay has no stored response")
		}
		return IdentityClaimMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	// A uniqueness error aborts PostgreSQL's transaction. The savepoint lets us
	// convert an expected claim conflict into an exact committed problem while
	// still rolling back every partial identity write.
	if _, err := tx.Exec(ctx, "SAVEPOINT identity_claim_domain_write"); err != nil {
		return IdentityClaimMutationResult{}, fmt.Errorf("identity claim savepoint: %w", err)
	}
	claimResult, claimErr := c.Identity.ClaimDefaultWorkspaceInTx(ctx, tx, request.Claim)
	if claimErr != nil {
		if !errors.Is(claimErr, identity.ErrWorkspaceClaimed) &&
			!errors.Is(claimErr, identity.ErrIdentityConflict) {
			return IdentityClaimMutationResult{}, claimErr
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT identity_claim_domain_write"); err != nil {
			return IdentityClaimMutationResult{}, fmt.Errorf("identity claim conflict rollback: %w", err)
		}
		if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT identity_claim_domain_write"); err != nil {
			return IdentityClaimMutationResult{}, fmt.Errorf("identity claim conflict savepoint release: %w", err)
		}
		message := "identity or workspace already exists"
		if errors.Is(claimErr, identity.ErrWorkspaceClaimed) {
			message = "default workspace is already claimed"
		}
		response, err := identityClaimConflictStoredResponse(request.RequestID, message)
		if err != nil {
			return IdentityClaimMutationResult{}, err
		}
		if _, err := c.Idempotency.CompleteSensitiveFailureInTx(
			ctx,
			tx,
			reservation.Reference(),
			response,
		); err != nil {
			return IdentityClaimMutationResult{}, fmt.Errorf("identity claim conflict idempotency complete: %w", err)
		}
		if _, err := c.Audit.AppendInTx(ctx, tx, identityClaimDeniedAuditRow(request)); err != nil {
			return IdentityClaimMutationResult{}, fmt.Errorf("identity claim conflict audit append: %w", err)
		}
		return IdentityClaimMutationResult{Response: response}, nil
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT identity_claim_domain_write"); err != nil {
		return IdentityClaimMutationResult{}, fmt.Errorf("identity claim savepoint release: %w", err)
	}

	response, err := identityClaimCreatedStoredResponse(claimResult)
	if err != nil {
		return IdentityClaimMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSensitiveSuccessInTx(
		ctx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return IdentityClaimMutationResult{}, fmt.Errorf("identity claim idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, identityClaimAllowedAuditRow(request, claimResult)); err != nil {
		return IdentityClaimMutationResult{}, fmt.Errorf("identity claim audit append: %w", err)
	}
	return IdentityClaimMutationResult{Response: response}, nil
}

func (c *PostgresIdentityClaimCoordinator) validate(request IdentityClaimCommitRequest) error {
	if c == nil || c.Pool == nil || c.Identity == nil || c.Idempotency == nil || c.Audit == nil {
		return errors.New("identity claim coordinator is not configured")
	}
	if c.Identity.Pool != c.Pool || c.Idempotency.Pool != c.Pool || c.Audit.Pool != c.Pool {
		return errors.New("identity claim coordinator authorities do not share one PostgreSQL pool")
	}
	if c.Idempotency.ResponseKeys == nil {
		return errors.New("identity claim coordinator sensitive response keys are not configured")
	}
	if request.Claim.UserID == "" || request.Key == "" {
		return errors.New("identity claim coordinator request is incomplete")
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return fmt.Errorf("identity claim fingerprint: %w", err)
	}
	return nil
}

func identityClaimCreatedStoredResponse(result identity.ClaimDefaultWorkspaceResult) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("identity claim response encode: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Cache-Control", "no-store")
	return idempotency.StoredResponse{
		StatusCode: http.StatusCreated,
		Header:     headers,
		Body:       append(body, '\n'),
	}, nil
}

func identityClaimConflictStoredResponse(requestID, message string) (idempotency.StoredResponse, error) {
	body, err := marshalErrorResponse(
		http.StatusConflict,
		failurecodes.ResourceConflict,
		message,
		requestID,
		nil,
	)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("identity claim conflict response encode: %w", err)
	}
	return idempotency.StoredResponse{
		StatusCode: responseStatus(body, http.StatusConflict),
		Header:     http.Header{"Content-Type": []string{"application/problem+json"}},
		Body:       body,
	}, nil
}

func identityClaimAllowedAuditRow(
	request IdentityClaimCommitRequest,
	result identity.ClaimDefaultWorkspaceResult,
) audit.Row {
	return audit.Row{
		RequestID:  request.RequestID,
		OccurredAt: request.Claim.Now,
		ActorID:    result.User.ID, ActorType: "user", AuthMethod: "password+totp",
		SourceIP: normalizedMutationSourceIP(request.SourceIP), UserAgent: request.UserAgent,
		WorkspaceID: result.Workspace.ID, ResourceKind: "workspace",
		ResourceID: result.Workspace.ID, Action: "identity.default-workspace.claim",
		PolicyDecision: "allowed",
		After: map[string]any{
			"user_id":        result.User.ID,
			"workspace_slug": result.Workspace.Slug,
			"role":           string(identity.WorkspaceRoleAdmin),
			"mfa_enabled":    result.User.MFAEnabled,
		},
	}
}

func identityClaimDeniedAuditRow(request IdentityClaimCommitRequest) audit.Row {
	return audit.Row{
		RequestID:  request.RequestID,
		OccurredAt: request.Claim.Now,
		ActorType:  "system", AuthMethod: "operator-bearer",
		SourceIP: normalizedMutationSourceIP(request.SourceIP), UserAgent: request.UserAgent,
		WorkspaceID: identity.DefaultWorkspaceID, ResourceKind: "workspace",
		ResourceID: identity.DefaultWorkspaceID, Action: "identity.default-workspace.claim.denied",
		ReasonCode: string(failurecodes.ResourceConflict), PolicyDecision: "denied",
		Attrs: map[string]any{"actor_class": "operator-recovery"},
	}
}

func identityClaimTransactionRetryable(err error) bool {
	return errors.Is(err, pg.ErrTxCommitIndeterminate) || isTenantMutationRetryable(err)
}
