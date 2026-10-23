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
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// serviceDeleteLiveDeploymentSample bounds how many blocking deployments the
// refusal message names. The point is to tell the operator where to look, not
// to dump the whole history.
const serviceDeleteLiveDeploymentSample = 5

// DeleteServiceCAS is the operator recovery path. It applies the same
// preconditions and the same removal set as the tenant coordinator, in one
// serializable transaction, but evaluates no membership because the caller
// already presented the installation recovery credential.
func (s *PostgresResourceStore) DeleteServiceCAS(
	ctx context.Context,
	id orailtypes.ID,
	expectedVersion int64,
) (ServiceDeletionReceipt, error) {
	if s == nil || s.Pool == nil {
		return ServiceDeletionReceipt{}, errServiceDeleteUnsupportedStore
	}
	if err := ctx.Err(); err != nil {
		return ServiceDeletionReceipt{}, err
	}
	if expectedVersion < 1 {
		return ServiceDeletionReceipt{}, ErrResourcePreconditionFailed
	}
	now := s.now().Truncate(time.Microsecond)
	var receipt ServiceDeletionReceipt
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			receipt, err = deleteServiceInTx(ctx, tx, string(id), expectedVersion, now)
			return err
		})
		if err == nil {
			return receipt, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return ServiceDeletionReceipt{}, err
		}
	}
	return ServiceDeletionReceipt{}, errors.New("service delete serializable retries exhausted")
}

// deleteServiceInTx is the single implementation of "what deleting a service
// means". Both the recovery store and the tenant coordinator call it, so the
// two surfaces cannot drift on which rows survive.
//
// Everything that can refuse the delete is checked before the first write, so
// a refusal never leaves a half-torn-down service behind.
func deleteServiceInTx(
	ctx context.Context,
	tx *pg.Tx,
	serviceID string,
	expectedVersion int64,
	now time.Time,
) (ServiceDeletionReceipt, error) {
	var projectID, slug string
	var currentVersion int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(project_id, ''), slug, resource_version
		FROM services
		WHERE id=$1
		  AND deleted_at IS NULL
		FOR UPDATE`,
		serviceID,
	).Scan(&projectID, &slug, &currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceDeletionReceipt{}, ErrResourceNotFound
	}
	if err != nil {
		return ServiceDeletionReceipt{}, fmt.Errorf("service delete lookup: %w", err)
	}
	if currentVersion != expectedVersion {
		return ServiceDeletionReceipt{}, ErrResourcePreconditionFailed
	}

	if err := refuseServiceDeleteWhileRunning(ctx, tx, serviceID); err != nil {
		return ServiceDeletionReceipt{}, err
	}
	if err := refuseServiceDeleteWithBlockingDependents(ctx, tx, serviceID, now); err != nil {
		return ServiceDeletionReceipt{}, err
	}

	counts, err := removeServiceDependentsInTx(ctx, tx, serviceID, now)
	if err != nil {
		return ServiceDeletionReceipt{}, err
	}

	var retained int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM deployments WHERE service_id=$1`, serviceID,
	).Scan(&retained); err != nil {
		return ServiceDeletionReceipt{}, fmt.Errorf("service delete deployment count: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE services
		SET deleted_at=$1,
		    updated_at=$1,
		    resource_version=resource_version+1
		WHERE id=$2
		  AND deleted_at IS NULL
		  AND resource_version=$3`,
		now, serviceID, expectedVersion,
	)
	if err != nil {
		return ServiceDeletionReceipt{}, fmt.Errorf("service delete: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ServiceDeletionReceipt{}, ErrResourcePreconditionFailed
	}
	return ServiceDeletionReceipt{
		ServiceID:           serviceID,
		ProjectID:           projectID,
		Slug:                slug,
		DeletedAt:           now.UTC(),
		Removed:             counts,
		RetainedDeployments: retained,
	}, nil
}

// refuseServiceDeleteWhileRunning is the guard that keeps the delete from
// stranding infrastructure. A deployment is "live" when it is still moving
// (queued, building, deploying) or when it is the active deployment and its
// runtime is still meant to be running. A stopped active deployment is not
// live: the runtime action path already tore its containers down, and the row
// only survives as the record of what ran last.
func refuseServiceDeleteWhileRunning(ctx context.Context, tx *pg.Tx, serviceID string) error {
	rows, err := tx.Query(ctx, `
		SELECT id, state, runtime_desired_state
		FROM deployments
		WHERE service_id=$1
		  AND (
		      state IN ('queued', 'building', 'deploying')
		      OR (active AND runtime_desired_state = 'running')
		  )
		ORDER BY id
		LIMIT $2`,
		serviceID,
		serviceDeleteLiveDeploymentSample,
	)
	if err != nil {
		return fmt.Errorf("service delete live deployment lookup: %w", err)
	}
	defer rows.Close()
	var blocking []string
	for rows.Next() {
		var id, state, desired string
		if err := rows.Scan(&id, &state, &desired); err != nil {
			return fmt.Errorf("service delete live deployment scan: %w", err)
		}
		blocking = append(blocking, id+" ("+state+"/"+desired+")")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("service delete live deployment read: %w", err)
	}
	if len(blocking) > 0 {
		return fmt.Errorf(
			"%w: stop or wait for %v before deleting this service",
			ErrServiceDeleteRunning,
			blocking,
		)
	}

	var pendingAction bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM deployment_runtime_actions a
			JOIN deployments d ON d.id = a.deployment_id
			WHERE d.service_id=$1
			  AND a.state IN ('queued', 'running')
		)`,
		serviceID,
	).Scan(&pendingAction); err != nil {
		return fmt.Errorf("service delete runtime action lookup: %w", err)
	}
	if pendingAction {
		return fmt.Errorf(
			"%w: a runtime action is still queued or running for this service",
			ErrServiceDeleteRunning,
		)
	}
	return nil
}

// refuseServiceDeleteWithBlockingDependents covers the records this delete
// will not discard on the caller's behalf: storage that is still attached to
// something, an open pull-request preview, and an unfinished source upload.
// Each of them means another surface still believes it owns part of this
// service.
func refuseServiceDeleteWithBlockingDependents(
	ctx context.Context,
	tx *pg.Tx,
	serviceID string,
	now time.Time,
) error {
	var attached int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM volume_attachments a
		JOIN volumes v ON v.id = a.volume_id
		WHERE v.service_id=$1
		  AND a.status IN ('pending', 'active')`,
		serviceID,
	).Scan(&attached); err != nil {
		return fmt.Errorf("service delete volume attachment lookup: %w", err)
	}
	if attached > 0 {
		return fmt.Errorf(
			"%w: %d volume attachment(s) are still pending or active; redeploy without the volume or stop the deployment first",
			ErrServiceDeleteBlocked,
			attached,
		)
	}

	var previews int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM github_pull_request_previews
		WHERE service_id=$1
		  AND closed_at IS NULL`,
		serviceID,
	).Scan(&previews); err != nil {
		return fmt.Errorf("service delete preview lookup: %w", err)
	}
	if previews > 0 {
		return fmt.Errorf(
			"%w: %d pull-request preview(s) are still open for this service",
			ErrServiceDeleteBlocked,
			previews,
		)
	}

	var uploads int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM source_artifact_upload_reservations
		WHERE service_id=$1
		  AND published_at IS NULL
		  AND lease_expires_at > $2`,
		serviceID,
		now,
	).Scan(&uploads); err != nil {
		return fmt.Errorf("service delete source upload lookup: %w", err)
	}
	if uploads > 0 {
		return fmt.Errorf(
			"%w: %d source upload(s) are still in progress for this service",
			ErrServiceDeleteBlocked,
			uploads,
		)
	}
	return nil
}

// removeServiceDependentsInTx tears down every row that represents live intent
// for the service. It runs after the refusal checks, so nothing here can be
// pointing at a running container.
//
// Deployment records and their evidence are deliberately absent: they are
// history, they stay readable, and touching them would mean writing deployment
// state, which this path is not allowed to do.
func removeServiceDependentsInTx(
	ctx context.Context,
	tx *pg.Tx,
	serviceID string,
	now time.Time,
) (ServiceDeletionCounts, error) {
	var counts ServiceDeletionCounts

	// Variables keep the same tombstone shape the variable delete route uses:
	// envelope material is erased immediately, the metadata row survives, and
	// the immutable historical versions a past deployment pinned are left
	// alone so that history stays readable.
	variables, err := execServiceDeleteStep(ctx, tx, "variables", `
		UPDATE service_variables
		SET value_ciphertext=''::bytea,
		    kek_id='',
		    wrapped_dek=''::bytea,
		    "references"='[]'::jsonb,
		    length_bucket='deleted',
		    kek_fingerprint='',
		    version=version+1,
		    deleted_at=$2,
		    updated_at=$2
		WHERE service_id=$1
		  AND deleted_at IS NULL`,
		serviceID, now)
	if err != nil {
		return counts, err
	}
	counts.Variables = variables

	// Storage follows the volume store's own soft-delete: the row is marked
	// and given the panel-locked grace window, so the existing reaper removes
	// the backing directory instead of leaving it on disk with no owner.
	volumes, err := execServiceDeleteStep(ctx, tx, "volumes", `
		UPDATE volumes
		SET status='soft_deleted',
		    soft_deleted_at=$2,
		    hard_delete_at=$3,
		    updated_at=$2,
		    resource_version=resource_version+1
		WHERE service_id=$1
		  AND status <> 'soft_deleted'`,
		serviceID, now, now.Add(volume.SoftDeleteGrace))
	if err != nil {
		return counts, err
	}
	counts.Volumes = volumes
	if _, err := execServiceDeleteStep(ctx, tx, "volume attachments",
		`DELETE FROM volume_attachments WHERE service_id=$1`, serviceID); err != nil {
		return counts, err
	}

	// Published routing. http_routes cascades its backends, route intents, and
	// domain bindings; domains cascades its DNS challenges and certificate
	// orders. retired_product_domain_ids has no foreign key, so it is cleared
	// explicitly rather than left dangling.
	if _, err := execServiceDeleteStep(ctx, tx, "retired domain ids",
		`DELETE FROM retired_product_domain_ids WHERE service_id=$1`, serviceID); err != nil {
		return counts, err
	}
	routes, err := execServiceDeleteStep(ctx, tx, "http routes",
		`DELETE FROM http_routes WHERE service_id=$1`, serviceID)
	if err != nil {
		return counts, err
	}
	counts.Routes = routes
	if _, err := execServiceDeleteStep(ctx, tx, "route intents",
		`DELETE FROM deployment_route_intents WHERE service_id=$1`, serviceID); err != nil {
		return counts, err
	}
	domains, err := execServiceDeleteStep(ctx, tx, "domains",
		`DELETE FROM domains WHERE service_id=$1`, serviceID)
	if err != nil {
		return counts, err
	}
	counts.PublishedDomains = domains

	proxies, err := execServiceDeleteStep(ctx, tx, "tcp proxies",
		`DELETE FROM tcp_proxy_endpoints WHERE service_id=$1`, serviceID)
	if err != nil {
		return counts, err
	}
	counts.TCPProxies = proxies

	configs, err := execServiceDeleteStep(ctx, tx, "environment configs",
		`DELETE FROM service_environment_configs WHERE service_id=$1`, serviceID)
	if err != nil {
		return counts, err
	}
	counts.EnvironmentConfigs = configs
	if _, err := execServiceDeleteStep(ctx, tx, "configuration revisions",
		`DELETE FROM service_configuration_revisions WHERE service_id=$1`, serviceID); err != nil {
		return counts, err
	}
	if _, err := execServiceDeleteStep(ctx, tx, "environment state",
		`DELETE FROM service_environment_state WHERE service_id=$1`, serviceID); err != nil {
		return counts, err
	}

	links, err := execServiceDeleteStep(ctx, tx, "source links",
		`DELETE FROM service_github_links WHERE service_id=$1`, serviceID)
	if err != nil {
		return counts, err
	}
	counts.SourceLinks = links

	// Unapplied environment edits that still name this service would otherwise
	// keep appearing in the review panel with nothing behind them.
	pending, err := execServiceDeleteStep(ctx, tx, "pending changeset items",
		`DELETE FROM environment_changeset_items WHERE service_id=$1`, serviceID)
	if err != nil {
		return counts, err
	}
	counts.PendingChanges = pending

	monitors, err := execServiceDeleteStep(ctx, tx, "monitors",
		`DELETE FROM monitor_definitions WHERE service_id=$1`, serviceID)
	if err != nil {
		return counts, err
	}
	counts.Monitors = monitors

	// Scheduling intent that carries a service_id but no foreign key, and so
	// would survive on its own.
	if _, err := execServiceDeleteStep(ctx, tx, "cron cursors",
		`DELETE FROM cron_schedule_cursors WHERE service_id=$1`, serviceID); err != nil {
		return counts, err
	}
	if _, err := execServiceDeleteStep(ctx, tx, "environment locks",
		`DELETE FROM environment_locks WHERE service_id=$1`, serviceID); err != nil {
		return counts, err
	}
	return counts, nil
}

func execServiceDeleteStep(ctx context.Context, tx *pg.Tx, step, query string, args ...any) (int, error) {
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("service delete %s: %w", step, err)
	}
	return int(tag.RowsAffected()), nil
}

// DeleteService linearizes current session validity, canonical project access,
// idempotency, the removal itself, the exact response, and audit evidence.
// A refusal is a committed outcome, not a rollback: the stored problem
// response and a denied audit row are written in the same transaction so a
// retry with the same key replays the identical refusal.
func (c *PostgresTenantMutationCoordinator) DeleteService(
	ctx context.Context,
	request TenantServiceDeleteRequest,
) (TenantMutationResult, error) {
	if err := c.validateServiceDelete(request); err != nil {
		return TenantMutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TenantMutationResult{}, err
	}
	now := c.now().Truncate(time.Microsecond)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.deleteServiceInTx(ctx, tx, request, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("tenant service delete serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) validateServiceDelete(request TenantServiceDeleteRequest) error {
	if c == nil ||
		c.Pool == nil ||
		c.Idempotency == nil ||
		c.Idempotency.Pool == nil ||
		c.Idempotency.Pool != c.Pool ||
		c.Audit == nil ||
		c.Audit.Pool == nil ||
		c.Audit.Pool != c.Pool {
		return errors.New("tenant service delete does not share one postgres authority")
	}
	if request.Principal.UserID == "" || request.Session.ID == "" {
		return identity.ErrSessionInvalid
	}
	if request.ServiceID == "" {
		return fmt.Errorf("%w: service id is required", ErrResourceInvalid)
	}
	if request.ExpectedVersion < 1 {
		return ErrResourcePreconditionFailed
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	canonicalPath := serviceDeleteIdempotencyPath(request.ServiceID)
	if request.Fingerprint.Method != http.MethodDelete || request.Fingerprint.Path != canonicalPath {
		return fmt.Errorf(
			"%w: tenant service delete fingerprint must use concrete DELETE %s",
			idempotency.ErrInvalidFingerprint,
			canonicalPath,
		)
	}
	return nil
}

func (c *PostgresTenantMutationCoordinator) deleteServiceInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantServiceDeleteRequest,
	now time.Time,
) (TenantMutationResult, error) {
	projectID, workspaceID, err := resolveTenantServiceLineage(ctx, tx, request.ServiceID)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service delete workspace membership lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service delete project membership lock: %w", err)
	}

	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(request.Principal.UserID))
	mutationCtx = idempotency.WithTenant(mutationCtx, projectScopedIdempotencyTenant(workspaceID, projectID))
	if err := revalidateTenantMutationSessionTuple(
		mutationCtx, tx, request.Principal, request.Session, now,
	); err != nil {
		return TenantMutationResult{}, err
	}
	canonicalWorkspaceID, workspaceRole, projectRole, err := lockTenantServiceProjectAccess(
		mutationCtx, tx, projectID, workspaceID, request.Principal.UserID,
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
		Capability:    identity.CapabilityServiceDelete,
	})
	if !decision.Allowed {
		return TenantMutationResult{}, identity.ErrAuthorizationDenied
	}

	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx, tx, request.Key, request.Fingerprint, 24*time.Hour,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantMutationResult{}, errors.New("tenant service delete replay has no stored response")
		}
		return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	scope := tenantServiceDeleteScope{
		request:       request,
		workspaceID:   workspaceID,
		projectID:     projectID,
		workspaceRole: workspaceRole,
		projectRole:   projectRole,
		now:           now,
	}
	receipt, deleteErr := deleteServiceInTx(mutationCtx, tx, request.ServiceID, request.ExpectedVersion, now)
	if deleteErr != nil {
		status, code, ok := serviceDeleteRefusal(deleteErr)
		if !ok {
			return TenantMutationResult{}, deleteErr
		}
		return c.completeServiceDeleteRefusalInTx(mutationCtx, tx, reservation, scope, status, code, deleteErr.Error())
	}

	response, err := serviceDeletedStoredResponse(receipt)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(mutationCtx, tx, reservation.Reference(), response); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service delete idempotency complete: %w", err)
	}
	row := tenantServiceDeleteAuditRow(scope, "", "allowed")
	row.Before = map[string]any{"slug": receipt.Slug, "resource_version": request.ExpectedVersion}
	row.After = serviceDeletionAuditAttrs(receipt)
	if _, err := c.Audit.AppendInTx(mutationCtx, tx, row); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service delete audit append: %w", err)
	}
	return TenantMutationResult{Response: response}, nil
}

// completeServiceDeleteRefusalInTx commits a refusal as the exact stored
// response plus a denied audit row, so the caller sees the same answer on
// every retry with that key and the decision is on the audit chain.
func (c *PostgresTenantMutationCoordinator) completeServiceDeleteRefusalInTx(
	ctx context.Context,
	tx *pg.Tx,
	reservation idempotency.Reservation,
	scope tenantServiceDeleteScope,
	status int,
	code failurecodes.Code,
	message string,
) (TenantMutationResult, error) {
	body, err := marshalErrorResponse(status, code, message, scope.request.RequestID, map[string]any{
		"service_id": scope.request.ServiceID,
	})
	if err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service delete refusal encode: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	response := idempotency.StoredResponse{StatusCode: status, Header: headers, Body: body}
	if _, err := c.Idempotency.CompleteFailureInTx(ctx, tx, reservation.Reference(), response); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service delete refusal idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, tenantServiceDeleteAuditRow(scope, string(code), "denied")); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service delete refusal audit append: %w", err)
	}
	return TenantMutationResult{Response: response}, nil
}

// serviceDeleteRefusal maps a delete failure onto the stored refusal taxonomy.
// It reports false for anything that is a fault rather than a decision, so
// those still roll the transaction back.
func serviceDeleteRefusal(err error) (int, failurecodes.Code, bool) {
	switch {
	case errors.Is(err, ErrServiceDeleteRunning):
		return http.StatusConflict, failurecodes.ServiceDeleteRunning, true
	case errors.Is(err, ErrServiceDeleteBlocked):
		return http.StatusConflict, failurecodes.ServiceDeleteBlocked, true
	case errors.Is(err, ErrResourcePreconditionFailed):
		return http.StatusPreconditionFailed, failurecodes.PreconditionFailed, true
	default:
		return 0, "", false
	}
}

type tenantServiceDeleteScope struct {
	request       TenantServiceDeleteRequest
	workspaceID   string
	projectID     string
	workspaceRole identity.WorkspaceRole
	projectRole   identity.ProjectRole
	now           time.Time
}

func tenantServiceDeleteAuditRow(scope tenantServiceDeleteScope, reasonCode, decision string) audit.Row {
	return audit.Row{
		RequestID:      scope.request.RequestID,
		OccurredAt:     scope.now,
		RecordedAt:     scope.now,
		ActorID:        scope.request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     scope.request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(scope.request.SourceIP),
		UserAgent:      scope.request.UserAgent,
		WorkspaceID:    scope.workspaceID,
		ProjectID:      scope.projectID,
		ResourceKind:   "service",
		ResourceID:     scope.request.ServiceID,
		Action:         serviceDeleteAuditAction,
		ReasonCode:     reasonCode,
		PolicyDecision: decision,
		Attrs: map[string]any{
			"capability":       string(identity.CapabilityServiceDelete),
			"workspace_role":   string(scope.workspaceRole),
			"project_role":     string(scope.projectRole),
			"session_id":       scope.request.Principal.SessionID,
			"expected_version": scope.request.ExpectedVersion,
		},
	}
}

// resolveTenantServiceLineage resolves the project and workspace that own a
// live service. A miss is reported as an access miss rather than a not-found
// so the route never discloses the existence of another tenant's service.
func resolveTenantServiceLineage(ctx context.Context, tx *pg.Tx, serviceID string) (string, string, error) {
	var projectID, workspaceID string
	err := tx.QueryRow(ctx, `
		SELECT s.project_id, p.workspace_id
		FROM services s
		JOIN projects p ON p.id = s.project_id
		JOIN workspaces w ON w.id = p.workspace_id
		WHERE s.id=$1
		  AND s.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		  AND w.deleted_at IS NULL`,
		serviceID,
	).Scan(&projectID, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", identity.ErrAccessNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("tenant service lineage resolve: %w", err)
	}
	return projectID, workspaceID, nil
}

func serviceDeletedStoredResponse(receipt ServiceDeletionReceipt) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(receipt)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant service delete response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return idempotency.StoredResponse{
		StatusCode: http.StatusOK,
		Header:     headers,
		Body:       body,
	}, nil
}

var (
	_ ConditionalServiceStore        = (*PostgresResourceStore)(nil)
	_ ConditionalServiceStore        = (*MemoryResourceStore)(nil)
	_ TenantServiceDeleteCoordinator = (*PostgresTenantMutationCoordinator)(nil)
)
