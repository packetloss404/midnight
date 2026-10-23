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

// IdentityRecoveryCommitRequest contains the fully validated administrator
// credential reset and its redacted idempotency fingerprint. Password hashing,
// TOTP verification, and envelope encryption are deliberately completed before
// this value crosses the transaction boundary.
type IdentityRecoveryCommitRequest struct {
	Reset       identity.ResetAdministratorCredentialsRequest
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

// IdentityRecoveryMutationResult is the exact response committed beside the
// credential reset and its audit evidence.
type IdentityRecoveryMutationResult struct {
	Response idempotency.StoredResponse
	Replayed bool
}

// IdentityRecoveryCoordinator is an opt-in durable mutation boundary. Memory
// and custom recovery stores continue to use the legacy store seam.
type IdentityRecoveryCoordinator interface {
	ResetAdministratorCredentials(context.Context, IdentityRecoveryCommitRequest) (IdentityRecoveryMutationResult, error)
}

// PostgresIdentityRecoveryCoordinator commits an operator-authorized
// administrator credential reset, its audit row, and its encrypted exact
// terminal response in one SERIALIZABLE transaction. Every participant must
// share Pool; mismatches fail closed.
type PostgresIdentityRecoveryCoordinator struct {
	Pool        *pg.Pool
	Identity    *identity.PostgresStore
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain
}

func NewPostgresIdentityRecoveryCoordinator(
	pool *pg.Pool,
	identityStore *identity.PostgresStore,
	idempotencyStore *idempotency.PostgresStore,
	auditChain *audit.PostgresChain,
) *PostgresIdentityRecoveryCoordinator {
	return &PostgresIdentityRecoveryCoordinator{
		Pool:        pool,
		Identity:    identityStore,
		Idempotency: idempotencyStore,
		Audit:       auditChain,
	}
}

func (c *PostgresIdentityRecoveryCoordinator) ResetAdministratorCredentials(
	ctx context.Context,
	request IdentityRecoveryCommitRequest,
) (IdentityRecoveryMutationResult, error) {
	if err := c.validate(request); err != nil {
		return IdentityRecoveryMutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return IdentityRecoveryMutationResult{}, err
	}

	var result IdentityRecoveryMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.resetAdministratorCredentialsInTx(ctx, tx, request)
			return err
		})
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !identityClaimTransactionRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return IdentityRecoveryMutationResult{}, err
		}
	}
	return IdentityRecoveryMutationResult{}, errors.New("identity recovery serializable retries exhausted")
}

func (c *PostgresIdentityRecoveryCoordinator) resetAdministratorCredentialsInTx(
	ctx context.Context,
	tx *pg.Tx,
	request IdentityRecoveryCommitRequest,
) (IdentityRecoveryMutationResult, error) {
	reservation, err := c.Idempotency.ReserveInTx(ctx, tx, request.Key, request.Fingerprint, 24*time.Hour)
	if err != nil {
		return IdentityRecoveryMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return IdentityRecoveryMutationResult{}, errors.New("identity recovery replay has no stored response")
		}
		return IdentityRecoveryMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	result, resetErr := c.Identity.ResetAdministratorCredentialsInTx(ctx, tx, request.Reset)
	if resetErr != nil {
		status, code, message := identityRecoveryFailureResponse(resetErr)
		if code == "" {
			return IdentityRecoveryMutationResult{}, resetErr
		}
		// A refused reset writes nothing to users or auth_sessions, so the
		// transaction is still clean: the exact problem response and its denial
		// evidence commit together with the idempotency record.
		response, err := identityRecoveryProblemStoredResponse(status, code, request.RequestID, message)
		if err != nil {
			return IdentityRecoveryMutationResult{}, err
		}
		if _, err := c.Idempotency.CompleteSensitiveFailureInTx(ctx, tx, reservation.Reference(), response); err != nil {
			return IdentityRecoveryMutationResult{}, fmt.Errorf("identity recovery denial idempotency complete: %w", err)
		}
		if _, err := c.Audit.AppendInTx(ctx, tx, identityRecoveryDeniedAuditRow(
			identityRecoveryAuditContext(request), request.Reset.UserID, string(code), request.Reset.Now,
		)); err != nil {
			return IdentityRecoveryMutationResult{}, fmt.Errorf("identity recovery denial audit append: %w", err)
		}
		return IdentityRecoveryMutationResult{Response: response}, nil
	}

	response, err := identityRecoveryCompletedStoredResponse(result)
	if err != nil {
		return IdentityRecoveryMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSensitiveSuccessInTx(ctx, tx, reservation.Reference(), response); err != nil {
		return IdentityRecoveryMutationResult{}, fmt.Errorf("identity recovery idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, identityRecoveryAllowedAuditRow(
		identityRecoveryAuditContext(request), result, request.Reset.Now,
	)); err != nil {
		return IdentityRecoveryMutationResult{}, fmt.Errorf("identity recovery audit append: %w", err)
	}
	return IdentityRecoveryMutationResult{Response: response}, nil
}

func (c *PostgresIdentityRecoveryCoordinator) validate(request IdentityRecoveryCommitRequest) error {
	if c == nil || c.Pool == nil || c.Identity == nil || c.Idempotency == nil || c.Audit == nil {
		return errors.New("identity recovery coordinator is not configured")
	}
	if c.Identity.Pool != c.Pool || c.Idempotency.Pool != c.Pool || c.Audit.Pool != c.Pool {
		return errors.New("identity recovery coordinator authorities do not share one PostgreSQL pool")
	}
	if c.Idempotency.ResponseKeys == nil {
		return errors.New("identity recovery coordinator sensitive response keys are not configured")
	}
	if request.Reset.UserID == "" || request.Key == "" {
		return errors.New("identity recovery coordinator request is incomplete")
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return fmt.Errorf("identity recovery fingerprint: %w", err)
	}
	return nil
}

// identityRecoveryFailureResponse maps the domain refusals onto the exact
// problem the handler emits on the legacy path. An unrecognized error returns
// an empty code so the caller rolls the transaction back instead of committing
// a denial it does not understand.
func identityRecoveryFailureResponse(err error) (int, failurecodes.Code, string) {
	switch {
	case errors.Is(err, identity.ErrAdministratorNotRecoverable):
		return http.StatusNotFound, failurecodes.ResourceNotFound,
			"no enabled administrator account matches that address"
	case errors.Is(err, identity.ErrTOTPEnrollmentMissing):
		return http.StatusBadRequest, failurecodes.ResourceValidationFailed,
			"this account has no TOTP enrollment to preserve; supply a replacement enrollment"
	default:
		return 0, "", ""
	}
}

func identityRecoveryAuditContext(request IdentityRecoveryCommitRequest) audit.Row {
	return audit.Row{
		RequestID: request.RequestID,
		SourceIP:  normalizedMutationSourceIP(request.SourceIP),
		UserAgent: request.UserAgent,
	}
}

func identityRecoveryCompletedStoredResponse(
	result identity.ResetAdministratorCredentialsResult,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("identity recovery response encode: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Cache-Control", "no-store")
	return idempotency.StoredResponse{
		StatusCode: http.StatusOK,
		Header:     headers,
		Body:       append(body, '\n'),
	}, nil
}

func identityRecoveryProblemStoredResponse(
	status int,
	code failurecodes.Code,
	requestID, message string,
) (idempotency.StoredResponse, error) {
	body, err := marshalErrorResponse(status, code, message, requestID, nil)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("identity recovery problem response encode: %w", err)
	}
	return idempotency.StoredResponse{
		StatusCode: responseStatus(body, status),
		Header:     http.Header{"Content-Type": []string{"application/problem+json"}},
		Body:       body,
	}, nil
}
