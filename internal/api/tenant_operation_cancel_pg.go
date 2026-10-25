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
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const tenantOperationCancelPathTemplate = v1alphaPrefix +
	"/operations/{operation_id}/cancel"

var (
	ErrTenantOperationCancelUnavailable = errors.New(
		"tenant operation cancellation dependency is unavailable",
	)
	ErrTenantOperationCancelUnsafe = errors.New(
		"operation has no cancelable durable work or worker safe point",
	)
)

// TenantOperationCancelRequest contains the verified human identity and exact
// operation route. Canonical deployment lineage and current authority are
// resolved again inside the accepting transaction.
type TenantOperationCancelRequest struct {
	Principal   identity.Principal
	Session     identity.Session
	OperationID orailtypes.ID
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

type TenantOperationCancelResult struct {
	Response idempotency.StoredResponse
	Replayed bool
}

// TenantOperationCancelCoordinator is deliberately separate from operation
// reads, deployment actions, and the internal fail endpoint.
type TenantOperationCancelCoordinator interface {
	CancelTenantOperation(
		context.Context,
		TenantOperationCancelRequest,
	) (TenantOperationCancelResult, error)
}

// PostgresTenantOperationCancelCoordinator linearizes current session/RBAC
// authority, actor/project/route-scoped replay, outbox/queue fencing,
// operation/deployment state, the exact response, and audit evidence.
type PostgresTenantOperationCancelCoordinator struct {
	Pool        *pg.Pool
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain
	Now         func() time.Time
	EventID     func(prefix string) string
}

func NewPostgresTenantOperationCancelCoordinator(
	pool *pg.Pool,
	idempotencyStore *idempotency.PostgresStore,
	chain *audit.PostgresChain,
	now func() time.Time,
) *PostgresTenantOperationCancelCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresTenantOperationCancelCoordinator{
		Pool:        pool,
		Idempotency: idempotencyStore,
		Audit:       chain,
		Now:         now,
		EventID:     randomOutboxID,
	}
}

func (c *PostgresTenantOperationCancelCoordinator) CancelTenantOperation(
	ctx context.Context,
	request TenantOperationCancelRequest,
) (TenantOperationCancelResult, error) {
	if err := c.validate(request); err != nil {
		return TenantOperationCancelResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TenantOperationCancelResult{}, err
	}

	now := c.now().Truncate(time.Microsecond)
	deploymentEventID := c.EventID("dev")
	var result TenantOperationCancelResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(
			ctx,
			pgx.TxOptions{IsoLevel: pgx.Serializable},
			func(tx *pg.Tx) error {
				var err error
				result, err = c.cancelInTx(
					ctx,
					tx,
					request,
					now,
					c.now().Truncate(time.Microsecond),
					deploymentEventID,
				)
				return err
			},
		)
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !isTenantMutationRetryable(err) ||
			attempt == tenantMutationSerializableAttempts-1 {
			return TenantOperationCancelResult{},
				classifyTenantOperationCancelError(err)
		}
	}
	return TenantOperationCancelResult{},
		errors.New("tenant operation cancellation serializable retries exhausted")
}

type tenantOperationCancelTarget struct {
	OperationID   orailtypes.ID
	DeploymentID  string
	ServiceID     string
	ProjectID     string
	EnvironmentID string
	Deployment    deploy.DeploymentState
}

func (c *PostgresTenantOperationCancelCoordinator) cancelInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantOperationCancelRequest,
	now time.Time,
	authorizationNow time.Time,
	deploymentEventID string,
) (TenantOperationCancelResult, error) {
	target, err := loadTenantOperationCancelTargetTx(
		ctx,
		tx,
		request.OperationID,
		false,
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	mutationCtx, authorization, err := authorizeTenantDeployCapabilityInTx(
		ctx,
		tx,
		request.Principal,
		request.Session,
		target.ServiceID,
		target.EnvironmentID,
		authorizationNow,
		identity.CapabilityDeploymentTriggerCLI,
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	if authorization.ProjectID != target.ProjectID ||
		string(authorization.Service.ID) != target.ServiceID {
		return TenantOperationCancelResult{}, identity.ErrAccessNotFound
	}

	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantOperationCancelResult{},
				errors.New("tenant operation cancellation replay has no stored response")
		}
		return TenantOperationCancelResult{
			Response: reservation.Response.Clone(),
			Replayed: true,
		}, nil
	}

	canceledDeployOutboxes, err := cancelOperationOutboxRowsTx(
		mutationCtx,
		tx,
		"deploy_intake_outbox",
		string(request.OperationID),
		now,
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	canceledStagedOutboxes, err := cancelOperationOutboxRowsTx(
		mutationCtx,
		tx,
		"staged_work_outbox",
		string(request.OperationID),
		now,
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	canceledJobs, err := cancelPendingOperationJobsTx(
		mutationCtx,
		tx,
		string(request.OperationID),
		now,
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	leasedJobs, err := lockLeasedOperationJobsTx(
		mutationCtx,
		tx,
		string(request.OperationID),
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}

	operation, err := lockOperationTx(
		mutationCtx,
		tx,
		request.OperationID,
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	lockedTarget, err := loadTenantOperationCancelTargetTx(
		mutationCtx,
		tx,
		request.OperationID,
		true,
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	if lockedTarget != target {
		return TenantOperationCancelResult{}, identity.ErrAccessNotFound
	}
	if operation.State != orailtypes.OperationQueued &&
		operation.State != orailtypes.OperationRunning {
		return TenantOperationCancelResult{}, ErrOperationInvalidTransition
	}

	canceledWork := len(canceledDeployOutboxes) +
		len(canceledStagedOutboxes) +
		len(canceledJobs)
	terminalized := len(leasedJobs) == 0 && canceledWork > 0
	eventMessage := "cancel requested: worker will stop at its safe point before traffic promotion"
	auditAction := "operation.cancel.request"
	if terminalized {
		if err := cancelCanonicalDeploymentAndOperationTx(
			mutationCtx,
			tx,
			lockedTarget,
			operation,
			now,
			deploymentEventID,
			"canceled before worker lease",
		); err != nil {
			return TenantOperationCancelResult{}, err
		}
		operation.State = orailtypes.OperationCanceled
		eventMessage = "canceled: durable work withdrawn before worker lease"
		auditAction = "operation.cancel"
	} else if len(leasedJobs) == 0 {
		return TenantOperationCancelResult{}, ErrTenantOperationCancelUnsafe
	}
	if err := appendOperationEventTx(
		mutationCtx,
		tx,
		request.OperationID,
		operation.State,
		eventMessage,
		now,
	); err != nil {
		return TenantOperationCancelResult{}, err
	}

	running, err := c.Idempotency.AttachOperationInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		string(operation.ID),
	)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	response, err := tenantOperationCancelStoredResponse(operation)
	if err != nil {
		return TenantOperationCancelResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		running.Reference(),
		response,
	); err != nil {
		return TenantOperationCancelResult{}, err
	}
	if _, err := c.Audit.AppendInTx(
		mutationCtx,
		tx,
		tenantOperationCancelAuditRow(
			request,
			now,
			authorization,
			lockedTarget,
			operation,
			auditAction,
			canceledDeployOutboxes,
			canceledStagedOutboxes,
			canceledJobs,
			leasedJobs,
		),
	); err != nil {
		return TenantOperationCancelResult{},
			fmt.Errorf("tenant operation cancellation audit append: %w", err)
	}
	return TenantOperationCancelResult{Response: response}, nil
}

func loadTenantOperationCancelTargetTx(
	ctx context.Context,
	tx *pg.Tx,
	operationID orailtypes.ID,
	lock bool,
) (tenantOperationCancelTarget, error) {
	query := `
		SELECT operation_id,id,service_id,project_id,environment_id,state
		  FROM deployments
		 WHERE operation_id=$1
		 ORDER BY id
		 LIMIT 2`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, query, string(operationID))
	if err != nil {
		return tenantOperationCancelTarget{},
			fmt.Errorf("tenant operation cancellation lineage: %w", err)
	}
	defer rows.Close()
	targets := make([]tenantOperationCancelTarget, 0, 2)
	for rows.Next() {
		var target tenantOperationCancelTarget
		if err := rows.Scan(
			&target.OperationID,
			&target.DeploymentID,
			&target.ServiceID,
			&target.ProjectID,
			&target.EnvironmentID,
			&target.Deployment,
		); err != nil {
			return tenantOperationCancelTarget{}, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return tenantOperationCancelTarget{}, err
	}
	if len(targets) != 1 ||
		targets[0].OperationID != operationID ||
		strings.TrimSpace(targets[0].DeploymentID) == "" ||
		strings.TrimSpace(targets[0].ServiceID) == "" ||
		strings.TrimSpace(targets[0].ProjectID) == "" ||
		strings.TrimSpace(targets[0].EnvironmentID) == "" {
		return tenantOperationCancelTarget{}, ErrOperationNotFound
	}
	return targets[0], nil
}

func cancelOperationOutboxRowsTx(
	ctx context.Context,
	tx *pg.Tx,
	table string,
	operationID string,
	now time.Time,
) ([]string, error) {
	if table != "deploy_intake_outbox" && table != "staged_work_outbox" {
		return nil, errors.New("unsupported cancellation outbox table")
	}
	rows, err := tx.Query(
		ctx,
		`UPDATE `+table+`
		    SET state='canceled',
		        lease_owner=NULL,
		        lease_expires_at=NULL,
		        last_error='canceled by authorized user before dispatch',
		        updated_at=$2
		  WHERE operation_id=$1
		    AND state IN ('pending','delivering')
		RETURNING id`,
		operationID,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("cancel %s: %w", table, err)
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func cancelPendingOperationJobsTx(
	ctx context.Context,
	tx *pg.Tx,
	operationID string,
	now time.Time,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		UPDATE jobs
		   SET state='canceled',
		       reason_code=$2,
		       lease_owner=NULL,
		       lease_expires_at=NULL,
		       heartbeat_at=NULL,
		       finished_at=$3,
		       updated_at=$3
		 WHERE queue IN ('build','deploy')
		   AND (parent_operation_id=$1 OR operation_id=$1)
		   AND state='pending'
		RETURNING id`,
		operationID,
		queue.ReasonDeployCanceledBeforeLease,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("cancel pending operation jobs: %w", err)
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func lockLeasedOperationJobsTx(
	ctx context.Context,
	tx *pg.Tx,
	operationID string,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id
		  FROM jobs
		 WHERE queue IN ('build','deploy')
		   AND (parent_operation_id=$1 OR operation_id=$1)
		   AND state='leased'
		 ORDER BY id
		   FOR UPDATE`,
		operationID,
	)
	if err != nil {
		return nil, fmt.Errorf("lock leased operation jobs: %w", err)
	}
	defer rows.Close()
	return scanStringRows(rows)
}

type stringRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanStringRows(rows stringRows) ([]string, error) {
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func lockOperationTx(
	ctx context.Context,
	tx *pg.Tx,
	operationID orailtypes.ID,
) (orailtypes.Operation, error) {
	var operation orailtypes.Operation
	var state, code, reason string
	err := tx.QueryRow(ctx, `
		SELECT id,kind,state,COALESCE(code,''),COALESCE(reason,'')
		  FROM operations
		 WHERE id=$1
		   FOR UPDATE`,
		string(operationID),
	).Scan(
		&operation.ID,
		&operation.Kind,
		&state,
		&code,
		&reason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return orailtypes.Operation{}, ErrOperationNotFound
	}
	if err != nil {
		return orailtypes.Operation{}, fmt.Errorf("lock operation: %w", err)
	}
	operation.State = orailtypes.OperationState(state)
	operation.Code = orailtypes.FailureCode(code)
	operation.Reason = reason
	return operation, nil
}

func cancelCanonicalDeploymentAndOperationTx(
	ctx context.Context,
	tx *pg.Tx,
	target tenantOperationCancelTarget,
	operation orailtypes.Operation,
	now time.Time,
	deploymentEventID string,
	deploymentEventMessage string,
) error {
	if err := deploy.ValidateTransition(
		target.Deployment,
		deploy.DeploymentCanceled,
	); err != nil {
		return ErrTenantOperationCancelUnsafe
	}
	tag, err := tx.Exec(ctx, `
		UPDATE deployments
		   SET state='canceled',
		       completed_at=COALESCE(completed_at,$2),
		       build_completed_at=CASE
		         WHEN state='building' AND build_started_at IS NOT NULL
		           THEN COALESCE(build_completed_at,$2)
		         ELSE build_completed_at
		       END,
		       updated_at=$2,
		       resource_version=resource_version+1
		 WHERE id=$1
		   AND operation_id=$3
		   AND state=$4`,
		target.DeploymentID,
		now,
		string(target.OperationID),
		string(target.Deployment),
	)
	if err != nil {
		return fmt.Errorf("cancel canonical deployment: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrTenantOperationCancelUnsafe
	}
	tag, err = tx.Exec(ctx, `
		UPDATE operations
		   SET state='CANCELED',
		       finished_at=COALESCE(finished_at,$2),
		       updated_at=$2,
		       resource_version=resource_version+1
		 WHERE id=$1
		   AND state=$3`,
		string(operation.ID),
		now,
		string(operation.State),
	)
	if err != nil {
		return fmt.Errorf("cancel canonical operation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrOperationInvalidTransition
	}
	if strings.TrimSpace(deploymentEventID) == "" {
		return errors.New("deployment cancellation event id is required")
	}
	deploymentEventMessage = strings.TrimSpace(deploymentEventMessage)
	if deploymentEventMessage == "" {
		return errors.New("deployment cancellation event message is required")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deployment_events (
			id,deployment_id,seq,event_type,state,message,data,created_at
		)
		SELECT $1,$2,COALESCE(MAX(seq),0)+1,'state','canceled',
		       $3,'{}'::jsonb,$4
		  FROM deployment_events
		 WHERE deployment_id=$2`,
		deploymentEventID,
		target.DeploymentID,
		deploymentEventMessage,
		now,
	); err != nil {
		return fmt.Errorf("deployment cancellation event: %w", err)
	}
	return nil
}

func tenantOperationCancelStoredResponse(
	operation orailtypes.Operation,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(operation)
	if err != nil {
		return idempotency.StoredResponse{},
			fmt.Errorf("tenant operation cancellation response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set(
		"Location",
		v1alphaPrefix+"/operations/"+string(operation.ID),
	)
	return idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantOperationCancelAuditRow(
	request TenantOperationCancelRequest,
	now time.Time,
	authorization tenantDeployAuthorization,
	target tenantOperationCancelTarget,
	operation orailtypes.Operation,
	action string,
	deployOutboxes []string,
	stagedOutboxes []string,
	canceledJobs []string,
	leasedJobs []string,
) audit.Row {
	return audit.Row{
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
		EnvironmentID:  target.EnvironmentID,
		ResourceKind:   "operation",
		ResourceID:     string(operation.ID),
		Action:         action,
		PolicyDecision: "allowed",
		After: map[string]any{
			"state":                     string(operation.State),
			"deployment_id":             target.DeploymentID,
			"service_id":                target.ServiceID,
			"deploy_outboxes_canceled":  len(deployOutboxes),
			"staged_outboxes_canceled":  len(stagedOutboxes),
			"pending_jobs_canceled":     len(canceledJobs),
			"leased_jobs_at_acceptance": len(leasedJobs),
			"worker_safe_point":         len(leasedJobs) > 0,
		},
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityDeploymentTriggerCLI),
			"workspace_role": string(authorization.WorkspaceRole),
			"project_role":   string(authorization.ProjectRole),
			"session_id":     request.Principal.SessionID,
		},
	}
}

func (c *PostgresTenantOperationCancelCoordinator) validate(
	request TenantOperationCancelRequest,
) error {
	if c == nil ||
		c.Pool == nil ||
		c.Idempotency == nil ||
		c.Idempotency.Pool != c.Pool ||
		c.Audit == nil ||
		c.Audit.Pool != c.Pool ||
		c.Now == nil ||
		c.EventID == nil {
		return errors.New(
			"tenant operation cancellation coordinator does not share one postgres authority",
		)
	}
	if err := validateTenantHumanTuple(request.Principal, request.Session); err != nil {
		return err
	}
	if strings.TrimSpace(string(request.OperationID)) == "" {
		return fmt.Errorf("%w: operation id is required", ErrResourceInvalid)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	canonicalPath := tenantOperationCancelIdempotencyPath(request.OperationID)
	if request.Fingerprint.Method != http.MethodPost ||
		request.Fingerprint.Path != canonicalPath ||
		request.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256(nil) {
		return fmt.Errorf(
			"%w: operation cancellation fingerprint must use empty-body POST %s (template %s)",
			idempotency.ErrInvalidFingerprint,
			canonicalPath,
			tenantOperationCancelPathTemplate,
		)
	}
	return nil
}

func tenantOperationCancelIdempotencyPath(operationID orailtypes.ID) string {
	return v1alphaPrefix + "/operations/" + string(operationID) + "/cancel"
}

func classifyTenantOperationCancelError(err error) error {
	if err == nil ||
		errors.Is(err, identity.ErrSessionInvalid) ||
		errors.Is(err, identity.ErrAccessNotFound) ||
		errors.Is(err, identity.ErrAuthorizationDenied) ||
		errors.Is(err, idempotency.ErrInvalidKey) ||
		errors.Is(err, idempotency.ErrInvalidFingerprint) ||
		errors.Is(err, idempotency.ErrKeyReused) ||
		errors.Is(err, idempotency.ErrKeyReusedDifferentBody) ||
		errors.Is(err, idempotency.ErrRequestInProgress) ||
		errors.Is(err, ErrResourceInvalid) ||
		errors.Is(err, ErrOperationNotFound) ||
		errors.Is(err, ErrOperationInvalidTransition) ||
		errors.Is(err, ErrTenantOperationCancelUnsafe) ||
		errors.Is(err, context.Canceled) {
		return err
	}
	classified := classifyTenantDeployCoordinatorError(err)
	if errors.Is(classified, ErrTenantDeployIntakeUnavailable) {
		return errors.Join(ErrTenantOperationCancelUnavailable, err)
	}
	return err
}

func (c *PostgresTenantOperationCancelCoordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

var _ TenantOperationCancelCoordinator = (*PostgresTenantOperationCancelCoordinator)(nil)
