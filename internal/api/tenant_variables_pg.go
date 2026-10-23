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
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/variableintent"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func (c *PostgresTenantMutationCoordinator) ListVariables(
	ctx context.Context,
	request TenantVariableReadRequest,
) ([]secrets.VariableMeta, error) {
	if err := c.validateTenantVariableAuthority(request.Principal, request.Session, request.ServiceID, request.EnvironmentID, false); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store := secrets.NewPostgresEnvelopeStore(c.Pool)
	var result []secrets.VariableMeta
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
				identity.CapabilityVariablesRead,
			)
			if err != nil {
				return err
			}
			result, err = store.ListMetadataInTx(mutationCtx, tx, tenantVariableScope(authorization, request))
			return err
		})
		if err == nil {
			if result == nil {
				result = []secrets.VariableMeta{}
			}
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return nil, normalizeTenantVariableStoreError(err)
		}
	}
	return nil, errors.New("tenant variable list serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) GetVariable(
	ctx context.Context,
	request TenantVariableReadRequest,
) (secrets.VariableMeta, error) {
	if err := c.validateTenantVariableAuthority(request.Principal, request.Session, request.ServiceID, request.EnvironmentID, false); err != nil {
		return secrets.VariableMeta{}, err
	}
	if strings.TrimSpace(string(request.VariableID)) == "" {
		return secrets.VariableMeta{}, secrets.ErrVariableNotFound
	}
	store := secrets.NewPostgresEnvelopeStore(c.Pool)
	var result secrets.PersistedVariable
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
				identity.CapabilityVariablesRead,
			)
			if err != nil {
				return err
			}
			result, err = store.GetActiveInTx(mutationCtx, tx, tenantVariableScope(authorization, request), request.VariableID)
			return err
		})
		if err == nil {
			return result.Meta, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return secrets.VariableMeta{}, normalizeTenantVariableStoreError(err)
		}
	}
	return secrets.VariableMeta{}, errors.New("tenant variable get serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) CreateVariable(
	ctx context.Context,
	request TenantVariableCreateRequest,
) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if err := c.validateTenantVariableMutation(request.TenantVariableMutationMeta, http.MethodPost, false); err != nil {
		return TenantMutationResult{}, err
	}
	if !validTenantVariableName(request.Name) {
		return TenantMutationResult{}, secrets.ErrInvalidVariableName
	}
	if !validVariableScope(request.Scope) {
		return TenantMutationResult{}, secrets.ErrInvalidScope
	}
	variableID := secrets.VariableID(c.IDFactory(orailtypes.IDPrefixVariable))
	now := c.now().Truncate(time.Microsecond)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.createTenantVariableInTx(ctx, tx, request, variableID, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, normalizeTenantVariableStoreError(err)
		}
	}
	return TenantMutationResult{}, errors.New("tenant variable create serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) UpdateVariable(
	ctx context.Context,
	request TenantVariableUpdateRequest,
) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if err := c.validateTenantVariableMutation(request.TenantVariableMutationMeta, http.MethodPatch, true); err != nil {
		return TenantMutationResult{}, err
	}
	if request.ExpectedVersion < 1 {
		return TenantMutationResult{}, &secrets.VariableVersionConflict{Expected: request.ExpectedVersion}
	}
	if request.Scope != nil && !validVariableScope(*request.Scope) {
		return TenantMutationResult{}, secrets.ErrInvalidScope
	}
	now := c.now().Truncate(time.Microsecond)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.updateTenantVariableInTx(ctx, tx, request, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, normalizeTenantVariableStoreError(err)
		}
	}
	return TenantMutationResult{}, errors.New("tenant variable update serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) DeleteVariable(
	ctx context.Context,
	request TenantVariableDeleteRequest,
) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if err := c.validateTenantVariableMutation(request.TenantVariableMutationMeta, http.MethodDelete, true); err != nil {
		return TenantMutationResult{}, err
	}
	if request.ExpectedVersion < 1 {
		return TenantMutationResult{}, &secrets.VariableVersionConflict{Expected: request.ExpectedVersion}
	}
	now := c.now().Truncate(time.Microsecond)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.deleteTenantVariableInTx(ctx, tx, request, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, normalizeTenantVariableStoreError(err)
		}
	}
	return TenantMutationResult{}, errors.New("tenant variable delete serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) createTenantVariableInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantVariableCreateRequest,
	variableID secrets.VariableID,
	now time.Time,
) (TenantMutationResult, error) {
	mutationCtx, authorization, reservation, err := c.authorizeAndReserveTenantVariable(
		ctx,
		tx,
		request.TenantVariableMutationMeta,
		http.MethodPost,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if replay, ok, err := tenantVariableReplay(reservation); ok || err != nil {
		return replay, err
	}
	scope := tenantVariableScope(authorization, TenantVariableReadRequest{
		ServiceID: request.ServiceID, EnvironmentID: request.EnvironmentID,
	})
	prepared, err := c.prepareTenantVariable(
		mutationCtx,
		variableID,
		scope,
		request.Name,
		request.Scope,
		1,
		request.Plaintext,
		request.ValueKind,
		request.References,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	store := secrets.NewPostgresEnvelopeStore(c.Pool)
	if err := store.InsertPreparedInTx(mutationCtx, tx, scope, prepared); errors.Is(err, secrets.ErrVariableVersionConflict) {
		response, responseErr := tenantVariableConflictResponse(request.RequestID, "variable already exists", err)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if err := c.completeTenantVariableFailure(mutationCtx, tx, reservation, request.TenantVariableMutationMeta, authorization, nil, prepared.Meta, now, response); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	} else if err != nil {
		return TenantMutationResult{}, err
	}
	response, err := tenantVariableMetadataResponse(http.StatusCreated, prepared.Meta, tenantVariableItemPath(request.ServiceID, request.EnvironmentID, variableID))
	if err != nil {
		return TenantMutationResult{}, err
	}
	if err := c.completeTenantVariableSuccess(mutationCtx, tx, reservation, request.TenantVariableMutationMeta, authorization, nil, prepared.Meta, now, response); err != nil {
		return TenantMutationResult{}, err
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) updateTenantVariableInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantVariableUpdateRequest,
	now time.Time,
) (TenantMutationResult, error) {
	mutationCtx, authorization, reservation, err := c.authorizeAndReserveTenantVariable(
		ctx,
		tx,
		request.TenantVariableMutationMeta,
		http.MethodPatch,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if replay, ok, err := tenantVariableReplay(reservation); ok || err != nil {
		return replay, err
	}
	scope := tenantVariableScope(authorization, TenantVariableReadRequest{
		ServiceID: request.ServiceID, EnvironmentID: request.EnvironmentID,
	})
	store := secrets.NewPostgresEnvelopeStore(c.Pool)
	current, err := store.GetActiveInTx(mutationCtx, tx, scope, request.VariableID)
	if errors.Is(err, secrets.ErrVariableNotFound) {
		response, responseErr := tenantVariableNotFoundResponse(request.RequestID)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if err := c.completeTenantVariableFailure(mutationCtx, tx, reservation, request.TenantVariableMutationMeta, authorization, nil, secrets.VariableMeta{ID: request.VariableID}, now, response); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	}
	if err != nil {
		return TenantMutationResult{}, err
	}
	nextScope := current.Meta.Scope
	if request.Scope != nil {
		nextScope = *request.Scope
	}
	if current.Meta.Scope == secrets.ScopeSealed && nextScope != secrets.ScopeSealed {
		response, responseErr := tenantVariableErrorResponse(
			http.StatusBadRequest,
			failurecodes.ResourceValidationFailed,
			errTenantVariableSealedDowngrade.Error(),
			request.RequestID,
			map[string]any{"field": "scope"},
		)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if err := c.completeTenantVariableFailure(
			mutationCtx,
			tx,
			reservation,
			request.TenantVariableMutationMeta,
			authorization,
			&current.Meta,
			current.Meta,
			now,
			response,
		); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	}
	prepared, err := c.prepareTenantVariable(
		mutationCtx,
		current.Meta.ID,
		scope,
		current.Meta.Name,
		nextScope,
		request.ExpectedVersion+1,
		request.Plaintext,
		request.ValueKind,
		request.References,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if err := store.UpdatePreparedCASInTx(mutationCtx, tx, scope, prepared, request.ExpectedVersion); errors.Is(err, secrets.ErrVariableVersionConflict) {
		response, responseErr := tenantVariableConflictResponse(request.RequestID, "variable version conflict", err)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if err := c.completeTenantVariableFailure(mutationCtx, tx, reservation, request.TenantVariableMutationMeta, authorization, &current.Meta, prepared.Meta, now, response); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	} else if err != nil {
		return TenantMutationResult{}, err
	}
	response, err := tenantVariableMetadataResponse(http.StatusOK, prepared.Meta, "")
	if err != nil {
		return TenantMutationResult{}, err
	}
	if err := c.completeTenantVariableSuccess(mutationCtx, tx, reservation, request.TenantVariableMutationMeta, authorization, &current.Meta, prepared.Meta, now, response); err != nil {
		return TenantMutationResult{}, err
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) deleteTenantVariableInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantVariableDeleteRequest,
	now time.Time,
) (TenantMutationResult, error) {
	mutationCtx, authorization, reservation, err := c.authorizeAndReserveTenantVariable(
		ctx,
		tx,
		request.TenantVariableMutationMeta,
		http.MethodDelete,
		now,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if replay, ok, err := tenantVariableReplay(reservation); ok || err != nil {
		return replay, err
	}
	scope := tenantVariableScope(authorization, TenantVariableReadRequest{
		ServiceID: request.ServiceID, EnvironmentID: request.EnvironmentID,
	})
	store := secrets.NewPostgresEnvelopeStore(c.Pool)
	current, err := store.GetActiveInTx(mutationCtx, tx, scope, request.VariableID)
	if errors.Is(err, secrets.ErrVariableNotFound) {
		response, responseErr := tenantVariableNotFoundResponse(request.RequestID)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if err := c.completeTenantVariableFailure(mutationCtx, tx, reservation, request.TenantVariableMutationMeta, authorization, nil, secrets.VariableMeta{ID: request.VariableID}, now, response); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	}
	if err != nil {
		return TenantMutationResult{}, err
	}
	if err := store.TombstoneCASInTx(mutationCtx, tx, scope, request.VariableID, request.ExpectedVersion); errors.Is(err, secrets.ErrVariableVersionConflict) {
		response, responseErr := tenantVariableConflictResponse(request.RequestID, "variable version conflict", err)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		after := current.Meta
		after.Version = request.ExpectedVersion + 1
		if err := c.completeTenantVariableFailure(mutationCtx, tx, reservation, request.TenantVariableMutationMeta, authorization, &current.Meta, after, now, response); err != nil {
			return TenantMutationResult{}, err
		}
		return TenantMutationResult{Response: response}, nil
	} else if err != nil {
		return TenantMutationResult{}, err
	}
	response := idempotency.StoredResponse{StatusCode: http.StatusNoContent, Header: make(http.Header)}
	after := current.Meta
	after.Version = request.ExpectedVersion + 1
	after.LengthBucket = "deleted"
	if err := c.completeTenantVariableSuccess(mutationCtx, tx, reservation, request.TenantVariableMutationMeta, authorization, &current.Meta, after, now, response); err != nil {
		return TenantMutationResult{}, err
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) authorizeAndReserveTenantVariable(
	ctx context.Context,
	tx *pg.Tx,
	request TenantVariableMutationMeta,
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
		identity.CapabilityVariablesWrite,
	)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, idempotency.Reservation{}, err
	}
	bodyMAC, err := variableintent.BodyMAC(c.VariableIntentMACKey, request.RawBody)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, idempotency.Reservation{}, err
	}
	path := tenantVariableCollectionPath(request.ServiceID, request.EnvironmentID)
	if request.VariableID != "" {
		path = tenantVariableItemPath(request.ServiceID, request.EnvironmentID, request.VariableID)
	}
	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		idempotency.RequestFingerprint{Method: method, Path: path, BodySHA256: bodyMAC},
		24*time.Hour,
	)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, idempotency.Reservation{}, err
	}
	return mutationCtx, authorization, reservation, nil
}

func (c *PostgresTenantMutationCoordinator) prepareTenantVariable(
	ctx context.Context,
	id secrets.VariableID,
	scope secrets.VariableOwnershipScope,
	name string,
	privacy secrets.Scope,
	version int,
	plaintext []byte,
	valueKind string,
	references []secrets.VariableReference,
	now time.Time,
) (secrets.PersistedVariable, error) {
	envelope, err := secrets.SealForVariable(ctx, c.VariableKeyProvider, id, plaintext)
	if err != nil {
		return secrets.PersistedVariable{}, err
	}
	fingerprint, err := c.VariableKeyProvider.Fingerprint(ctx)
	if err != nil {
		return secrets.PersistedVariable{}, err
	}
	return secrets.PersistedVariable{
		Meta: secrets.VariableMeta{
			ID: id, Scope: privacy, Workspace: scope.Workspace, Project: scope.Project,
			Environment: scope.Environment, Service: scope.Service, Name: name,
			Version: version, LastRotatedAt: now, LengthBucket: secrets.LengthBucket(len(plaintext)),
			KEKFingerprint: fingerprint, ValueKind: valueKind,
			References: append([]secrets.VariableReference(nil), references...),
		},
		Envelope: envelope,
	}, nil
}

func (c *PostgresTenantMutationCoordinator) validateTenantVariableAuthority(
	principal identity.Principal,
	session identity.Session,
	serviceID string,
	environmentID string,
	write bool,
) error {
	if c == nil || c.Pool == nil {
		return errors.New("tenant variable coordinator requires postgres")
	}
	if write && (c.Idempotency == nil || c.Idempotency.Pool != c.Pool ||
		c.Audit == nil || c.Audit.Pool != c.Pool ||
		c.VariableKeyProvider == nil ||
		len(c.VariableIntentMACKey) < variableintent.MinMACKeyBytes ||
		c.IDFactory == nil) {
		return errors.New("tenant variable mutation does not share one state, idempotency, audit, encryption, and intent authority")
	}
	if principal.Kind != "user" || principal.UserID == "" || principal.SessionID == "" ||
		session.ID != principal.SessionID || session.UserID != principal.UserID ||
		session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	if strings.TrimSpace(serviceID) == "" || strings.TrimSpace(environmentID) == "" {
		return identity.ErrAccessNotFound
	}
	return nil
}

func (c *PostgresTenantMutationCoordinator) validateTenantVariableMutation(
	request TenantVariableMutationMeta,
	method string,
	requireID bool,
) error {
	if err := c.validateTenantVariableAuthority(request.Principal, request.Session, request.ServiceID, request.EnvironmentID, true); err != nil {
		return err
	}
	if requireID && strings.TrimSpace(string(request.VariableID)) == "" {
		return secrets.ErrVariableNotFound
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if len(request.RawBody) == 0 {
		return idempotency.ErrInvalidFingerprint
	}
	if method == http.MethodPost && request.VariableID != "" {
		return idempotency.ErrInvalidFingerprint
	}
	if method != http.MethodPost && request.VariableID == "" {
		return idempotency.ErrInvalidFingerprint
	}
	if request.Method != method {
		return idempotency.ErrInvalidFingerprint
	}
	return nil
}

func tenantVariableScope(
	authorization tenantDeployAuthorization,
	request TenantVariableReadRequest,
) secrets.VariableOwnershipScope {
	return secrets.VariableOwnershipScope{
		Workspace: authorization.WorkspaceID, Project: authorization.ProjectID,
		Environment: request.EnvironmentID, Service: request.ServiceID,
	}
}

func tenantVariableCollectionPath(serviceID, environmentID string) string {
	return v1alphaPrefix + "/services/" + serviceID + "/environments/" + environmentID + "/variables"
}

func tenantVariableItemPath(serviceID, environmentID string, variableID secrets.VariableID) string {
	return tenantVariableCollectionPath(serviceID, environmentID) + "/" + string(variableID)
}

func tenantVariableReplay(reservation idempotency.Reservation) (TenantMutationResult, bool, error) {
	if !reservation.IsTerminal() {
		return TenantMutationResult{}, false, nil
	}
	if reservation.Response == nil {
		return TenantMutationResult{}, true, errors.New("tenant variable replay has no stored response")
	}
	return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, true, nil
}

func tenantVariableMetadataResponse(status int, meta secrets.VariableMeta, location string) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(meta)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant variable response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("ETag", variableETag(meta.Version))
	if location != "" {
		headers.Set("Location", location)
	}
	return idempotency.StoredResponse{StatusCode: responseStatus(body, status), Header: headers, Body: body}, nil
}

func tenantVariableConflictResponse(requestID, message string, conflict error) (idempotency.StoredResponse, error) {
	details := map[string]any{}
	var versionConflict *secrets.VariableVersionConflict
	if errors.As(conflict, &versionConflict) {
		details["expected_version"] = versionConflict.Expected
		details["actual_version"] = versionConflict.Actual
	}
	status, code := http.StatusPreconditionFailed, failurecodes.PreconditionFailed
	if message == "variable already exists" {
		status, code = http.StatusConflict, failurecodes.ResourceConflict
	}
	return tenantVariableErrorResponse(status, code, message, requestID, details)
}

func tenantVariableNotFoundResponse(requestID string) (idempotency.StoredResponse, error) {
	return tenantVariableErrorResponse(http.StatusNotFound, failurecodes.ResourceNotFound, "variable not found", requestID, nil)
}

func tenantVariableErrorResponse(status int, code failurecodes.Code, message, requestID string, details map[string]any) (idempotency.StoredResponse, error) {
	body, err := marshalErrorResponse(status, code, message, requestID, details)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	return idempotency.StoredResponse{StatusCode: status, Header: headers, Body: body}, nil
}

func (c *PostgresTenantMutationCoordinator) completeTenantVariableSuccess(
	ctx context.Context,
	tx *pg.Tx,
	reservation idempotency.Reservation,
	request TenantVariableMutationMeta,
	authorization tenantDeployAuthorization,
	before *secrets.VariableMeta,
	after secrets.VariableMeta,
	now time.Time,
	response idempotency.StoredResponse,
) error {
	if _, err := c.Idempotency.CompleteSuccessInTx(ctx, tx, reservation.Reference(), response); err != nil {
		return fmt.Errorf("tenant variable idempotency success complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, tenantVariableAuditRow(request, authorization, before, after, now, "", "allowed")); err != nil {
		return fmt.Errorf("tenant variable audit append: %w", err)
	}
	return nil
}

func (c *PostgresTenantMutationCoordinator) completeTenantVariableFailure(
	ctx context.Context,
	tx *pg.Tx,
	reservation idempotency.Reservation,
	request TenantVariableMutationMeta,
	authorization tenantDeployAuthorization,
	before *secrets.VariableMeta,
	after secrets.VariableMeta,
	now time.Time,
	response idempotency.StoredResponse,
) error {
	if _, err := c.Idempotency.CompleteFailureInTx(ctx, tx, reservation.Reference(), response); err != nil {
		return fmt.Errorf("tenant variable idempotency failure complete: %w", err)
	}
	reasonCode := failurecodes.ResourceConflict
	switch response.StatusCode {
	case http.StatusBadRequest:
		reasonCode = failurecodes.ResourceValidationFailed
	case http.StatusNotFound:
		reasonCode = failurecodes.ResourceNotFound
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, tenantVariableAuditRow(request, authorization, before, after, now, string(reasonCode), "denied")); err != nil {
		return fmt.Errorf("tenant variable failure audit append: %w", err)
	}
	return nil
}

func tenantVariableAuditRow(
	request TenantVariableMutationMeta,
	authorization tenantDeployAuthorization,
	before *secrets.VariableMeta,
	after secrets.VariableMeta,
	now time.Time,
	reasonCode string,
	decision string,
) audit.Row {
	action := "variable.update"
	switch request.Method {
	case http.MethodPost:
		action = "variable.create"
	case http.MethodDelete:
		action = "variable.delete"
	}
	row := audit.Row{
		RequestID: request.RequestID, OccurredAt: now, RecordedAt: now,
		ActorID: request.Principal.UserID, ActorType: "user",
		AuthMethod: request.Principal.AuthMethod,
		SourceIP:   normalizedMutationSourceIP(request.SourceIP), UserAgent: request.UserAgent,
		WorkspaceID: authorization.WorkspaceID, ProjectID: authorization.ProjectID,
		EnvironmentID: request.EnvironmentID, ResourceKind: "variable",
		ResourceID: string(after.ID), Action: action, ReasonCode: reasonCode,
		PolicyDecision: decision,
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityVariablesWrite),
			"workspace_role": string(authorization.WorkspaceRole),
			"project_role":   string(authorization.ProjectRole),
			"session_id":     request.Principal.SessionID,
		},
	}
	if before != nil {
		row.Before = variableAuditMeta(*before)
	}
	if decision == "allowed" {
		row.After = variableAuditMeta(after)
	}
	return row
}

func normalizeTenantVariableStoreError(err error) error {
	if errors.Is(err, secrets.ErrVariableScopeIntegrity) {
		return identity.ErrAccessNotFound
	}
	return err
}

var _ TenantVariableCoordinator = (*PostgresTenantMutationCoordinator)(nil)
