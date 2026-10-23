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
)

const tenantDeploymentActionPathTemplate = v1alphaPrefix +
	"/services/{service_id}/deployments/{deployment_id}/actions/{action}"

var ErrTenantDeploymentActionUnavailable = errors.New(
	"tenant deployment action dependency is unavailable",
)

// TenantDeploymentActionRequest contains only the verified human identity and
// canonical route intent. The coordinator resolves the deployment's project,
// environment, service ownership, and current runtime state again inside its
// accepting transaction.
type TenantDeploymentActionRequest struct {
	Principal    identity.Principal
	Session      identity.Session
	ServiceID    string
	DeploymentID string
	Action       string
	Key          idempotency.Key
	Fingerprint  idempotency.RequestFingerprint
	RequestID    string
	SourceIP     string
	UserAgent    string
}

// TenantDeploymentActionCoordinator is a separate capability from first
// deployment intake. Installing one does not admit source linking, routing,
// operation cancellation, GraphQL, MCP, or any other human mutation.
type TenantDeploymentActionCoordinator interface {
	CommitTenantDeploymentAction(
		context.Context,
		TenantDeploymentActionRequest,
	) (TenantDeployIntakeResult, error)
}

// PostgresTenantDeploymentActionCoordinator linearizes current session/RBAC
// authority, actor/project/route-scoped replay, durable action/outbox state,
// the exact accepted response, and audit evidence in one SERIALIZABLE
// transaction.
type PostgresTenantDeploymentActionCoordinator struct {
	Pool        *pg.Pool
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain
	Actions     *PostgresDeploymentActionStore
	Now         func() time.Time
}

func NewPostgresTenantDeploymentActionCoordinator(
	pool *pg.Pool,
	idempotencyStore *idempotency.PostgresStore,
	chain *audit.PostgresChain,
	actions *PostgresDeploymentActionStore,
	now func() time.Time,
) *PostgresTenantDeploymentActionCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresTenantDeploymentActionCoordinator{
		Pool:        pool,
		Idempotency: idempotencyStore,
		Audit:       chain,
		Actions:     actions,
		Now:         now,
	}
}

func (c *PostgresTenantDeploymentActionCoordinator) CommitTenantDeploymentAction(
	ctx context.Context,
	request TenantDeploymentActionRequest,
) (TenantDeployIntakeResult, error) {
	if err := c.validate(request); err != nil {
		return TenantDeployIntakeResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TenantDeployIntakeResult{}, err
	}

	now := c.now().Truncate(time.Microsecond)
	var imageIDs, runtimeIDs deploymentActionPreparedIDs
	switch request.Action {
	case "redeploy", "rollback":
		imageIDs = c.Actions.prepareImageReplayIDs()
	case "restart", "stop", "remove":
		runtimeIDs = c.Actions.prepareRuntimeActionIDs()
	}
	var result TenantDeployIntakeResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(
			ctx,
			pgx.TxOptions{IsoLevel: pgx.Serializable},
			func(tx *pg.Tx) error {
				var err error
				result, err = c.commitInTx(
					ctx,
					tx,
					request,
					now,
					c.now().Truncate(time.Microsecond),
					imageIDs,
					runtimeIDs,
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
			return TenantDeployIntakeResult{},
				classifyTenantDeploymentActionError(err)
		}
	}
	return TenantDeployIntakeResult{},
		errors.New("tenant deployment action serializable retries exhausted")
}

func (c *PostgresTenantDeploymentActionCoordinator) commitInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantDeploymentActionRequest,
	now time.Time,
	authorizationNow time.Time,
	imageIDs deploymentActionPreparedIDs,
	runtimeIDs deploymentActionPreparedIDs,
) (TenantDeployIntakeResult, error) {
	target, err := loadDeploymentTx(ctx, tx, request.DeploymentID)
	if errors.Is(err, ErrDeploymentNotFound) ||
		(err == nil && target.ServiceID != request.ServiceID) {
		return TenantDeployIntakeResult{}, identity.ErrAccessNotFound
	}
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}

	capability := tenantDeploymentActionCapability(request.Action)
	mutationCtx, authorization, err := authorizeTenantDeployCapabilityInTx(
		ctx,
		tx,
		request.Principal,
		request.Session,
		request.ServiceID,
		target.EnvironmentID,
		authorizationNow,
		capability,
	)
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	if authorization.ProjectID != target.ProjectID ||
		string(authorization.Service.ID) != target.ServiceID {
		return TenantDeployIntakeResult{}, identity.ErrAccessNotFound
	}

	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantDeployIntakeResult{},
				errors.New("tenant deployment action replay has no stored response")
		}
		return TenantDeployIntakeResult{
			Response: reservation.Response.Clone(),
			Replayed: true,
		}, nil
	}

	triggerKey := string(acceptedDeploymentIntentKey(imageIDs.OperationID))
	var actionResult DeploymentActionResult
	switch request.Action {
	case "redeploy":
		err = c.Actions.commitImageReplayPreparedTx(
			mutationCtx,
			tx,
			ImageReplayRequest{
				ServiceID:  request.ServiceID,
				SourceID:   request.DeploymentID,
				Action:     deploy.ActionRedeployImage,
				TriggerKey: triggerKey,
				Now:        now,
			},
			now,
			imageIDs,
			&actionResult,
		)
	case "rollback":
		err = c.Actions.commitImageReplayPreparedTx(
			mutationCtx,
			tx,
			ImageReplayRequest{
				ServiceID:  request.ServiceID,
				SourceID:   request.DeploymentID,
				Action:     deploy.ActionRollbackDigest,
				TriggerKey: triggerKey,
				Now:        now,
			},
			now,
			imageIDs,
			&actionResult,
		)
	case "restart", "stop", "remove":
		triggerKey = string(acceptedDeploymentIntentKey(runtimeIDs.OperationID))
		err = c.Actions.commitRuntimeActionPreparedTx(
			mutationCtx,
			tx,
			RuntimeActionRequest{
				ServiceID:  request.ServiceID,
				TargetID:   request.DeploymentID,
				Action:     deploy.RuntimeAction(request.Action),
				TriggerKey: triggerKey,
				Now:        now,
			},
			now,
			runtimeIDs,
			&actionResult,
		)
	default:
		err = fmt.Errorf("%w: unsupported deployment action", ErrResourceInvalid)
	}
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	if !actionResult.Created {
		return TenantDeployIntakeResult{},
			errors.New("tenant deployment action unexpectedly replayed work identity")
	}
	tag, err := tx.Exec(
		mutationCtx,
		`UPDATE operations
		    SET actor=$2,target_resource_kind='deployment',
		        target_resource_id=$3,idempotency_key=$4,updated_at=$5
		  WHERE id=$1`,
		string(actionResult.Operation.ID),
		request.Principal.UserID,
		target.ID,
		string(request.Key),
		now,
	)
	if err != nil {
		return TenantDeployIntakeResult{},
			fmt.Errorf("tenant deployment action operation lineage: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return TenantDeployIntakeResult{},
			errors.New("tenant deployment action operation lineage was not persisted")
	}

	running, err := c.Idempotency.AttachOperationInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		string(actionResult.Operation.ID),
	)
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	response, err := tenantDeploymentActionStoredResponse(actionResult)
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		running.Reference(),
		response,
	); err != nil {
		return TenantDeployIntakeResult{}, err
	}
	if _, err := c.Audit.AppendInTx(
		mutationCtx,
		tx,
		tenantDeploymentActionAuditRow(
			request,
			now,
			authorization,
			target,
			actionResult,
			capability,
		),
	); err != nil {
		return TenantDeployIntakeResult{},
			fmt.Errorf("tenant deployment action audit append: %w", err)
	}
	return TenantDeployIntakeResult{Response: response}, nil
}

func (c *PostgresTenantDeploymentActionCoordinator) validate(
	request TenantDeploymentActionRequest,
) error {
	if c == nil ||
		c.Pool == nil ||
		c.Idempotency == nil ||
		c.Idempotency.Pool != c.Pool ||
		c.Audit == nil ||
		c.Audit.Pool != c.Pool ||
		c.Actions == nil ||
		c.Actions.Pool != c.Pool ||
		c.Now == nil {
		return errors.New(
			"tenant deployment action coordinator does not share one postgres authority",
		)
	}
	if err := c.Actions.ready(); err != nil {
		return err
	}
	if err := validateTenantHumanTuple(request.Principal, request.Session); err != nil {
		return err
	}
	if strings.TrimSpace(request.ServiceID) == "" ||
		strings.TrimSpace(request.DeploymentID) == "" ||
		!validTenantDeploymentAction(request.Action) {
		return fmt.Errorf(
			"%w: service, deployment, and supported action are required",
			ErrResourceInvalid,
		)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	canonicalPath := tenantDeploymentActionIdempotencyPath(
		request.ServiceID,
		request.DeploymentID,
		request.Action,
	)
	if request.Fingerprint.Method != http.MethodPost ||
		request.Fingerprint.Path != canonicalPath ||
		request.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256(nil) {
		return fmt.Errorf(
			"%w: deployment action fingerprint must use empty-body POST %s (template %s)",
			idempotency.ErrInvalidFingerprint,
			canonicalPath,
			tenantDeploymentActionPathTemplate,
		)
	}
	return nil
}

func classifyTenantDeploymentActionError(err error) error {
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
		errors.Is(err, ErrDeploymentNotFound) ||
		errors.Is(err, ErrDeploymentActionConflict) ||
		errors.Is(err, ErrDeploymentSnapshotAbsent) ||
		errors.Is(err, context.Canceled) {
		return err
	}
	classified := classifyTenantDeployCoordinatorError(err)
	if errors.Is(classified, ErrTenantDeployIntakeUnavailable) {
		return errors.Join(ErrTenantDeploymentActionUnavailable, err)
	}
	return err
}

func validTenantDeploymentAction(action string) bool {
	switch strings.TrimSpace(action) {
	case "redeploy", "rollback", "restart", "stop", "remove":
		return true
	default:
		return false
	}
}

func tenantDeploymentActionIdempotencyPath(
	serviceID string,
	deploymentID string,
	action string,
) string {
	return v1alphaPrefix + "/services/" + serviceID + "/deployments/" +
		deploymentID + "/actions/" + action
}

func tenantDeploymentActionStoredResponse(
	result DeploymentActionResult,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return idempotency.StoredResponse{},
			fmt.Errorf("tenant deployment action response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set(
		"Location",
		v1alphaPrefix+"/operations/"+string(result.Operation.ID),
	)
	headers.Set("X-Midnight-Delivery-State", "pending")
	return idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantDeploymentActionAuditRow(
	request TenantDeploymentActionRequest,
	now time.Time,
	authorization tenantDeployAuthorization,
	target DeploymentRecord,
	result DeploymentActionResult,
	capability identity.Capability,
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
		ResourceKind:   "deployment",
		ResourceID:     target.ID,
		Action:         "deployment.action.accept",
		PolicyDecision: "allowed",
		After: map[string]any{
			"service_id":        target.ServiceID,
			"source_deployment": target.ID,
			"action":            request.Action,
			"operation_id":      string(result.Operation.ID),
			"result_deployment": result.Deployment.ID,
			"runtime_action_id": result.ActionID,
			"outbox_id":         result.OutboxID,
			"delivery_state":    "pending",
		},
		Attrs: map[string]any{
			"capability":     string(capability),
			"workspace_role": string(authorization.WorkspaceRole),
			"project_role":   string(authorization.ProjectRole),
			"session_id":     request.Principal.SessionID,
		},
	}
}

func tenantDeploymentActionCapability(action string) identity.Capability {
	switch action {
	case "redeploy", "rollback":
		return identity.CapabilityDeploymentTriggerCLI
	case "restart", "stop", "remove":
		return identity.CapabilityServiceWrite
	default:
		return ""
	}
}

func (c *PostgresTenantDeploymentActionCoordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

var _ TenantDeploymentActionCoordinator = (*PostgresTenantDeploymentActionCoordinator)(nil)
