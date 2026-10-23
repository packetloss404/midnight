// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const GraphQLDeployFingerprintPath = "/graphql/v2#serviceInstanceDeployV2"

// GraphQLDeployAccepted is the immutable transport-neutral result available
// before commit. The GraphQL layer projects its selected service-result bytes
// from this value without querying outside the accepting transaction.
type GraphQLDeployAccepted struct {
	Operation    orailtypes.Operation
	Deployment   DeploymentResource
	InitialEvent OperationEvent
}

// GraphQLDeployResult carries the exact selected GraphQL service-result bytes
// that were committed with product truth and the worker outbox.
type GraphQLDeployResult struct {
	Body     []byte
	Replayed bool
}

// GraphQLDeployRequest is already parsed and validated by the GraphQL layer.
// EncodeResult must be pure: it runs inside the transaction and must use only
// the supplied accepted snapshot.
type GraphQLDeployRequest struct {
	ServiceID    orailtypes.ID
	Intent       DeployIntakeRequest
	Key          idempotency.Key
	Fingerprint  idempotency.RequestFingerprint
	RequestID    string
	SourceIP     string
	UserAgent    string
	EncodeResult func(GraphQLDeployAccepted) ([]byte, error)
}

// GraphQLDeployIntakeCoordinator is independent from human REST admission.
// Its only production implementation requires the authenticated recovery
// actor in context and proves all durable adapters share one Postgres pool.
type GraphQLDeployIntakeCoordinator interface {
	DeployGraphQL(context.Context, GraphQLDeployRequest) (GraphQLDeployResult, error)
}

// GraphQLBeforeCommit is a test-only fault seam. A non-nil error is returned
// after every transactional write but before commit, proving rollback leaves
// no operation, deployment, outbox, idempotency completion, or audit row.
func (c *PostgresTenantDeployIntakeCoordinator) setGraphQLBeforeCommitForTest(hook func() error) {
	c.graphQLBeforeCommit = hook
}

func (c *PostgresTenantDeployIntakeCoordinator) DeployGraphQL(ctx context.Context, request GraphQLDeployRequest) (GraphQLDeployResult, error) {
	if err := c.validateGraphQLDeployRequest(ctx, request); err != nil {
		return GraphQLDeployResult{}, err
	}
	operationID := c.OperationIDFactory()
	deploymentID := c.DeploymentIDFactory()
	outboxID := c.OutboxIDFactory()
	eventID := c.EventIDFactory()
	now := c.now().Truncate(time.Microsecond)
	var result GraphQLDeployResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.deployGraphQLInTx(ctx, tx, request, operationID, deploymentID, outboxID, eventID, now)
			return err
		})
		if err == nil {
			result.Body = append([]byte(nil), result.Body...)
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return GraphQLDeployResult{}, classifyTenantDeployCoordinatorError(err)
		}
	}
	return GraphQLDeployResult{}, errors.New("graphql deploy intake serializable retries exhausted")
}

func (c *PostgresTenantDeployIntakeCoordinator) validateGraphQLDeployRequest(ctx context.Context, request GraphQLDeployRequest) error {
	if err := c.validateCoordinatorAuthority(); err != nil {
		return err
	}
	if c.Audit == nil || c.Audit.Pool != c.Pool {
		return errors.New("graphql deploy intake requires audit on the coordinator pool")
	}
	actor := idempotency.ActorFromContext(ctx)
	if actor != idempotency.ActorRecoveryOperator && !IsInstanceAdminRequest(ctx) {
		return errors.New("graphql deploy intake requires authenticated recovery-operator or installation-administrator context")
	}
	if strings.TrimSpace(string(request.ServiceID)) == "" || request.EncodeResult == nil {
		return fmt.Errorf("%w: service id and response encoder are required", ErrResourceInvalid)
	}
	if request.Intent.Source != nil || request.Intent.Route != nil || strings.TrimSpace(request.Intent.SourceRef) != "" || strings.TrimSpace(request.Intent.Mode) != "" {
		return fmt.Errorf("%w: graphql deploy accepts immutable images only", ErrResourceInvalid)
	}
	if strings.TrimSpace(request.Intent.DeploymentID) != "" || strings.TrimSpace(request.Intent.InstanceID) != "" || request.Intent.PreviousActive != nil {
		return fmt.Errorf("%w: deployment identity is server-owned", ErrResourceInvalid)
	}
	if code, message, _, ok := validateDeployIntakeRequest(request.Intent); !ok {
		return fmt.Errorf("%w: %s (%s)", ErrResourceInvalid, message, code)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	if request.Fingerprint.Method != http.MethodPost || request.Fingerprint.Path != GraphQLDeployFingerprintPath {
		return fmt.Errorf("%w: graphql deploy fingerprint must use POST %s", idempotency.ErrInvalidFingerprint, GraphQLDeployFingerprintPath)
	}
	return nil
}

func (c *PostgresTenantDeployIntakeCoordinator) deployGraphQLInTx(
	ctx context.Context,
	tx *pg.Tx,
	request GraphQLDeployRequest,
	operationID orailtypes.ID,
	deploymentID, outboxID, eventID string,
	now time.Time,
) (GraphQLDeployResult, error) {
	projectID, workspaceID, err := resolveTenantDeployLineage(ctx, tx, string(request.ServiceID), request.Intent.EnvironmentID)
	if err != nil {
		return GraphQLDeployResult{}, err
	}
	service, _, err := lockTenantDeployServiceEnvironment(ctx, tx, string(request.ServiceID), projectID, request.Intent.EnvironmentID)
	if err != nil {
		return GraphQLDeployResult{}, err
	}
	mutationCtx := idempotency.WithTenant(ctx, idempotency.Tenant(workspaceID))
	reservation, err := c.Idempotency.ReserveInTx(mutationCtx, tx, request.Key, request.Fingerprint, 24*time.Hour)
	if err != nil {
		return GraphQLDeployResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return GraphQLDeployResult{}, errors.New("graphql deploy replay has no stored result")
		}
		return GraphQLDeployResult{Body: append([]byte(nil), reservation.Response.Body...), Replayed: true}, nil
	}
	previousActiveID, err := lockTenantDeployActiveState(mutationCtx, tx, string(service.ID), request.Intent.EnvironmentID, now)
	if err != nil {
		return GraphQLDeployResult{}, err
	}
	intent := request.Intent
	intent.DeploymentID = deploymentID
	intent.InstanceID = deploymentID + "-r0"
	if previousActiveID != "" {
		intent.PreviousActive = &DeployActiveInstanceRef{DeploymentID: previousActiveID, InstanceID: previousActiveID + "-r0"}
	}
	operation := orailtypes.Operation{ID: operationID, Kind: "deploy", State: orailtypes.OperationPending, CreatedAt: now, UpdatedAt: now}
	deployment, err := canonicalDeploymentRecord(service, intent, operation.ID, acceptedDeploymentIntentKey(operation.ID))
	if err != nil {
		return GraphQLDeployResult{}, err
	}
	deployment.ID = deploymentID
	deployment.ExpectedActiveID = previousActiveID
	payload, err := buildDeployJobPayload(operation.ID, service, intent, now)
	if err != nil {
		return GraphQLDeployResult{}, err
	}
	enqueue := buildDeployEnqueueRequest(operation.ID, service, now, payload)
	enqueue.Queue = queue.Deploy
	message := durableDeployIntakeMessage(service, intent, queue.Deploy)
	resource := deploymentResourceFromRecord(service, deployment)
	resource.LastEvent = message
	resource.LastEventAt = now
	initialEvent := OperationEvent{OperationID: operation.ID, State: operation.State, Message: message, CreatedAt: now}
	body, err := request.EncodeResult(GraphQLDeployAccepted{Operation: operation, Deployment: resource, InitialEvent: initialEvent})
	if err != nil {
		return GraphQLDeployResult{}, fmt.Errorf("graphql deploy result encode: %w", err)
	}
	if len(body) == 0 {
		return GraphQLDeployResult{}, errors.New("graphql deploy result encoder returned empty bytes")
	}
	response := idempotency.StoredResponse{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: append([]byte(nil), body...)}
	actor := idempotency.ActorFromContext(mutationCtx)
	actorType := "user"
	authMethod := "human-session"
	if actor == idempotency.ActorRecoveryOperator {
		actorType = "system"
		authMethod = "operator-bearer"
	}
	commit, err := c.Outbox.CommitDeployIntakeInTx(mutationCtx, tx, DeployIntakeCommit{
		Operation: operation, OperationActor: string(actor), TargetKind: "service", TargetID: string(service.ID),
		Deployment: deployment, IdempotencyKey: request.Key, IdempotencyRef: reservation.Reference(), Response: response,
		Enqueue: enqueue, InitialMessage: message, Now: now,
	}, outboxID, eventID)
	if err != nil {
		return GraphQLDeployResult{}, err
	}
	if _, err := c.Audit.AppendInTx(mutationCtx, tx, audit.Row{
		RequestID: request.RequestID, OccurredAt: now, RecordedAt: now,
		ActorID: string(actor), ActorType: actorType, AuthMethod: authMethod,
		SourceIP: normalizedMutationSourceIP(request.SourceIP), UserAgent: request.UserAgent,
		WorkspaceID: workspaceID, ProjectID: projectID, EnvironmentID: intent.EnvironmentID,
		ResourceKind: "operation", ResourceID: string(operation.ID), Action: "graphql.deploy.intake.accept", PolicyDecision: "allowed",
		After: map[string]any{"service_id": service.ID, "deployment_id": commit.Deployment.ID, "operation_id": operation.ID, "outbox_id": commit.OutboxID, "delivery_state": "pending"},
	}); err != nil {
		return GraphQLDeployResult{}, fmt.Errorf("graphql deploy audit append: %w", err)
	}
	if c.graphQLBeforeCommit != nil {
		if err := c.graphQLBeforeCommit(); err != nil {
			return GraphQLDeployResult{}, err
		}
	}
	return GraphQLDeployResult{Body: append([]byte(nil), body...)}, nil
}

// EnqueueGraphQLDeployService selects the durable production coordinator when
// present. Memory/dev stacks retain EnqueueDeployService compatibility.
func (s *ServicesAPI) EnqueueGraphQLDeployService(ctx context.Context, request GraphQLDeployRequest) (GraphQLDeployResult, *ServiceError) {
	if s.GraphQLDeployIntake == nil {
		return GraphQLDeployResult{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "durable GraphQL deploy intake is not configured"}
	}
	result, err := s.GraphQLDeployIntake.DeployGraphQL(ctx, request)
	if err == nil {
		return result, nil
	}
	switch {
	case errors.Is(err, idempotency.ErrInvalidKey), errors.Is(err, idempotency.ErrInvalidFingerprint), errors.Is(err, ErrResourceInvalid):
		return GraphQLDeployResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error()}
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody), errors.Is(err, idempotency.ErrKeyReused):
		return GraphQLDeployResult{}, &ServiceError{Status: http.StatusConflict, Code: "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY", Message: "idempotency key reused with different GraphQL request"}
	case errors.Is(err, idempotency.ErrRequestInProgress):
		return GraphQLDeployResult{}, &ServiceError{Status: http.StatusConflict, Code: "IDEMPOTENCY_REQUEST_IN_PROGRESS", Message: "idempotent GraphQL deploy is in progress"}
	case errors.Is(err, identity.ErrAccessNotFound):
		return GraphQLDeployResult{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "service or environment not found"}
	case errors.Is(err, ErrTenantDeployIntakeUnavailable):
		return GraphQLDeployResult{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "durable GraphQL deploy intake is unavailable"}
	default:
		return GraphQLDeployResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "durable GraphQL deploy intake failed"}
	}
}
