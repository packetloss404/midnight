// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	StagedWorkDeploymentImageReplay   = "deployment_image_replay"
	StagedWorkDeploymentRuntimeAction = "deployment_runtime_action"
)

var (
	ErrDeploymentActionConflict = errors.New("deployment action conflicts with current state")
	ErrDeploymentSnapshotAbsent = errors.New("deployment has no immutable config snapshot")
)

type DeploymentActionIntake interface {
	CommitImageReplay(context.Context, ImageReplayRequest) (DeploymentActionResult, error)
	CommitRuntimeAction(context.Context, RuntimeActionRequest) (DeploymentActionResult, error)
}

type ImageReplayRequest struct {
	ServiceID  string
	SourceID   string
	Action     deploy.DeploymentAction
	TriggerKey string
	Route      *DeployRouteIntent
	Now        time.Time
}

type RuntimeActionRequest struct {
	ServiceID  string
	TargetID   string
	Action     deploy.RuntimeAction
	TriggerKey string
	Now        time.Time
}

type DeploymentActionResult struct {
	Operation  orailtypes.Operation `json:"operation"`
	Deployment DeploymentRecord     `json:"deployment"`
	ActionID   string               `json:"action_id,omitempty"`
	OutboxID   string               `json:"outbox_id"`
	Created    bool                 `json:"created"`
}

type PostgresDeploymentActionStore struct {
	Pool                *pg.Pool
	Now                 func() time.Time
	OperationIDFactory  func() orailtypes.ID
	DeploymentIDFactory func() string
	IDFactory           func(prefix string) string
}

func NewPostgresDeploymentActionStore(pool *pg.Pool) *PostgresDeploymentActionStore {
	return &PostgresDeploymentActionStore{
		Pool:                pool,
		Now:                 func() time.Time { return time.Now().UTC() },
		OperationIDFactory:  defaultOperationID,
		DeploymentIDFactory: func() string { return mintID(orailtypes.IDPrefixDeployment) },
		IDFactory:           randomOutboxID,
	}
}

func (s *PostgresDeploymentActionStore) now(value time.Time) time.Time {
	if !value.IsZero() {
		return value.UTC()
	}
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *PostgresDeploymentActionStore) newID(prefix string) string {
	if s != nil && s.IDFactory != nil {
		return s.IDFactory(prefix)
	}
	return randomOutboxID(prefix)
}

func (s *PostgresDeploymentActionStore) ready() error {
	if s == nil ||
		s.Pool == nil ||
		s.OperationIDFactory == nil ||
		s.DeploymentIDFactory == nil ||
		s.IDFactory == nil {
		return errors.New("deployment action store: pool is required")
	}
	return nil
}

type deploymentActionPreparedIDs struct {
	OperationID  orailtypes.ID
	DeploymentID string
	ActionID     string
	OutboxID     string
	EventID      string
}

func (s *PostgresDeploymentActionStore) prepareImageReplayIDs() deploymentActionPreparedIDs {
	return deploymentActionPreparedIDs{
		OperationID:  s.OperationIDFactory(),
		DeploymentID: s.DeploymentIDFactory(),
		OutboxID:     s.newID("wox"),
		EventID:      s.newID("dev"),
	}
}

func (s *PostgresDeploymentActionStore) prepareRuntimeActionIDs() deploymentActionPreparedIDs {
	return deploymentActionPreparedIDs{
		OperationID: s.OperationIDFactory(),
		ActionID:    s.newID("dact"),
		OutboxID:    s.newID("wox"),
	}
}

func (s *PostgresDeploymentActionStore) CommitImageReplay(ctx context.Context, request ImageReplayRequest) (DeploymentActionResult, error) {
	if err := s.ready(); err != nil {
		return DeploymentActionResult{}, err
	}
	if strings.TrimSpace(request.ServiceID) == "" || strings.TrimSpace(request.SourceID) == "" ||
		strings.TrimSpace(request.TriggerKey) == "" ||
		(request.Action != deploy.ActionRedeployImage && request.Action != deploy.ActionRollbackDigest) {
		return DeploymentActionResult{}, errors.New("image replay requires service, source, trigger, and a no-build action")
	}
	now := s.now(request.Now)
	ids := s.prepareImageReplayIDs()
	var result DeploymentActionResult
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		return s.commitImageReplayPreparedTx(ctx, tx, request, now, ids, &result)
	})
	return result, err
}

// commitImageReplayTx commits an immutable no-build deployment and its durable
// outbox intent in a caller-owned transaction. Recovery uses this seam to make
// a verified volume attachment swap and the redeploy handoff indivisible.
func (s *PostgresDeploymentActionStore) commitImageReplayTx(
	ctx context.Context,
	tx *pg.Tx,
	request ImageReplayRequest,
	now time.Time,
	result *DeploymentActionResult,
) error {
	return s.commitImageReplayPreparedTx(
		ctx,
		tx,
		request,
		now,
		s.prepareImageReplayIDs(),
		result,
	)
}

func (s *PostgresDeploymentActionStore) commitImageReplayPreparedTx(
	ctx context.Context,
	tx *pg.Tx,
	request ImageReplayRequest,
	now time.Time,
	ids deploymentActionPreparedIDs,
	result *DeploymentActionResult,
) error {
	if err := replayDeploymentActionResult(ctx, tx, request.TriggerKey, result); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	source, currentActive, err := loadReplaySource(ctx, tx, request.ServiceID, request.SourceID)
	if err != nil {
		return err
	}
	if source.ImageDigest == "" {
		return fmt.Errorf("%w: source image digest is empty", ErrDeploymentActionConflict)
	}
	if source.State == deploy.DeploymentQueued ||
		source.State == deploy.DeploymentBuilding ||
		source.State == deploy.DeploymentDeploying {
		return fmt.Errorf(
			"%w: image replay requires a deployment outside its build/deploy phase",
			ErrDeploymentActionConflict,
		)
	}
	if source.RuntimeDesiredState == "removed" {
		return fmt.Errorf(
			"%w: removed deployment cannot be replayed",
			ErrDeploymentActionConflict,
		)
	}
	if request.Action == deploy.ActionRollbackDigest && source.ID == currentActive {
		return fmt.Errorf("%w: rollback target is already active", ErrDeploymentActionConflict)
	}
	var sourceSnapshotRevision int64
	if err := tx.QueryRow(ctx,
		`SELECT configuration_revision FROM deployment_config_snapshots WHERE deployment_id=$1`,
		source.ID,
	).Scan(&sourceSnapshotRevision); errors.Is(err, pgx.ErrNoRows) {
		return ErrDeploymentSnapshotAbsent
	} else if err != nil {
		return err
	}
	routeIntent := request.Route

	operationID := ids.OperationID
	deploymentID := ids.DeploymentID
	outboxID := ids.OutboxID
	expiresAt := now.Add(24 * time.Hour)
	if _, err := tx.Exec(ctx,
		`INSERT INTO operations (id,kind,state,expires_at,created_at,updated_at)
			 VALUES ($1,'deploy','QUEUED',$2,$3,$3)`,
		string(operationID), expiresAt, now,
	); err != nil {
		return fmt.Errorf("image replay operation insert: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployments (
			    id,project_id,service_id,environment_id,action,source_type,repo,ref,sha,
			    root_directory,dockerfile_path,builder,image_ref,image_digest,state,active,
			    operation_id,trigger_key,rollback_source_deployment_id,expected_active_deployment_id,
			    runtime_desired_state,runtime_observed_state,configuration_revision,created_at,updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'queued',FALSE,
			    $15,$16,$17,$18,'running','unknown',$19,$20,$20)`,
		deploymentID, source.ProjectID, source.ServiceID, source.EnvironmentID,
		string(request.Action), source.SourceType, source.Repo, source.Ref, source.SHA,
		source.RootDirectory, source.DockerfilePath, source.Builder, source.ImageRef,
		source.ImageDigest, string(operationID), request.TriggerKey,
		nullableString(source.ID), nullableString(currentActive), sourceSnapshotRevision, now,
	); err != nil {
		return fmt.Errorf("image replay deployment insert: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id,route_id,domain_id,project_id,
			environment_id,service_id,created_at
		)
		SELECT $1,i.route_id,i.domain_id,i.project_id,
		       i.environment_id,i.service_id,$2
		  FROM deployment_route_intents i
		  JOIN http_routes r
		    ON r.id=i.route_id
		   AND r.project_id=i.project_id
		   AND r.environment_id=i.environment_id
		   AND r.service_id=i.service_id
		  JOIN domains d
		    ON d.id=i.domain_id
		   AND d.project_id=i.project_id
		   AND d.environment_id=i.environment_id
		   AND d.service_id=i.service_id
		 WHERE i.deployment_id=$3
		   AND i.project_id=$4
		   AND i.environment_id=$5
		   AND i.service_id=$6
		   AND d.tls_desired=FALSE
		   AND d.tls_state='disabled'
		   AND (d.kind='generated' OR (d.kind='custom' AND EXISTS (
			   SELECT 1 FROM domain_dns_challenges dc
			    WHERE dc.domain_id=d.id AND dc.state='verified'
			)))
		   AND r.public_enabled=TRUE
		ON CONFLICT (deployment_id,route_id) DO NOTHING`,
		deploymentID,
		now,
		source.ID,
		source.ProjectID,
		source.EnvironmentID,
		source.ServiceID,
	); err != nil {
		return fmt.Errorf("image replay route intent clone: %w", err)
	}
	if _, err := bindServiceManagedGeneratedRoutesInTx(
		ctx,
		tx,
		deploymentID,
		source.ProjectID,
		source.EnvironmentID,
		source.ServiceID,
		now,
	); err != nil {
		return fmt.Errorf("image replay service-managed route bind: %w", err)
	}
	if routeIntent == nil {
		routeIntent, err = loadReplayRouteIntent(ctx, tx, DeploymentRecord{
			ID:            deploymentID,
			ProjectID:     source.ProjectID,
			EnvironmentID: source.EnvironmentID,
			ServiceID:     source.ServiceID,
		})
		if err != nil {
			return fmt.Errorf("image replay route signal: %w", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployment_config_snapshots (
			    deployment_id,project_id,service_id,environment_id,configuration_revision,config_version,
			    effective_config,variable_versions,snapshot_sha256,created_at)
			 SELECT $1,project_id,service_id,environment_id,configuration_revision,config_version,
			        effective_config,variable_versions,snapshot_sha256,$2
			   FROM deployment_config_snapshots WHERE deployment_id=$3`,
		deploymentID, now, source.ID,
	); err != nil {
		return fmt.Errorf("image replay snapshot clone: %w", err)
	}

	intake := DeployIntakeRequest{
		EnvironmentID: source.EnvironmentID,
		Image:         imageWithDigest(source.ImageRef, source.ImageDigest),
		Mode:          actionMode(request.Action),
		DeploymentID:  deploymentID,
		InstanceID:    deploymentID + "-r0",
		Route:         routeIntent,
	}
	if currentActive != "" {
		intake.PreviousActive = &DeployActiveInstanceRef{
			DeploymentID: currentActive,
			InstanceID:   currentActive + "-r0",
		}
	}
	service := Service{ID: orailtypes.ID(source.ServiceID), ProjectID: orailtypes.ID(source.ProjectID)}
	payload, err := buildDeployJobPayload(operationID, service, intake, now)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO staged_work_outbox (
			    id,dedupe_key,stage,operation_id,deployment_id,queue,partition_key,
			    available_at,payload,state,created_at,updated_at)
			 VALUES ($1,$2,$3,$4,$5,'deploy',$6,$7,$8::jsonb,'pending',$7,$7)`,
		outboxID, request.TriggerKey, StagedWorkDeploymentImageReplay,
		string(operationID), deploymentID, source.ProjectID, now, string(payload),
	); err != nil {
		return fmt.Errorf("image replay outbox insert: %w", err)
	}
	message := actionMode(request.Action) + " accepted from immutable deployment " + source.ID
	if err := appendOperationEventTx(ctx, tx, operationID, orailtypes.OperationQueued, message, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployment_events
			    (id,deployment_id,seq,event_type,state,message,data,created_at)
			 VALUES ($1,$2,1,'state','queued',$3,'{}'::jsonb,$4)`,
		ids.EventID, deploymentID, message, now,
	); err != nil {
		return err
	}
	*result = DeploymentActionResult{
		Operation: orailtypes.Operation{ID: operationID, Kind: "deploy", State: orailtypes.OperationQueued},
		Deployment: DeploymentRecord{
			ID: deploymentID, ProjectID: source.ProjectID, ServiceID: source.ServiceID,
			EnvironmentID: source.EnvironmentID, Action: request.Action,
			ImageRef: source.ImageRef, ImageDigest: source.ImageDigest,
			ConfigurationRevision: sourceSnapshotRevision,
			State:                 deploy.DeploymentQueued, OperationID: string(operationID),
			TriggerKey: request.TriggerKey, RollbackSourceID: source.ID,
			ExpectedActiveID: currentActive, CreatedAt: now, UpdatedAt: now,
		},
		OutboxID: outboxID, Created: true,
	}
	return nil
}

// loadReplayRouteIntent returns one generated route as the legacy worker
// payload's public-routing signal. Exact route identity is the complete set in
// deployment_route_intents; custom recovery routes are never selected here.
func loadReplayRouteIntent(ctx context.Context, tx *pg.Tx, source DeploymentRecord) (*DeployRouteIntent, error) {
	var value DeployRouteIntent
	var kind string
	err := tx.QueryRow(ctx, `
		SELECT r.name,COALESCE(g.route_group,''),d.normalized_hostname,
		       r.path_prefix,r.public_enabled,d.tls_desired,d.kind
		  FROM deployment_route_intents i
		  JOIN http_routes r
		    ON r.id=i.route_id
		   AND r.project_id=i.project_id
		   AND r.environment_id=i.environment_id
		   AND r.service_id=i.service_id
		  JOIN domains d
		    ON d.id=i.domain_id
		   AND d.project_id=i.project_id
		   AND d.environment_id=i.environment_id
		   AND d.service_id=i.service_id
		  LEFT JOIN service_domain_bindings b
		    ON b.route_id=i.route_id
		   AND b.domain_id=i.domain_id
		   AND b.project_id=i.project_id
		   AND b.environment_id=i.environment_id
		   AND b.service_id=i.service_id
		  LEFT JOIN route_controller_groups g
		    ON g.project_id=i.project_id AND g.environment_id=i.environment_id
		 WHERE i.deployment_id=$1
		   AND i.project_id=$2
		   AND i.environment_id=$3
		   AND i.service_id=$4
		   AND d.tls_desired=FALSE
		   AND d.tls_state='disabled'
		   AND (d.kind='generated' OR (d.kind='custom' AND EXISTS (
			   SELECT 1 FROM domain_dns_challenges dc
			    WHERE dc.domain_id=d.id AND dc.state='verified'
			)))
		   AND r.public_enabled=TRUE
		 ORDER BY (b.route_id IS NOT NULL) DESC, i.route_id
		 LIMIT 1`,
		source.ID,
		source.ProjectID,
		source.EnvironmentID,
		source.ServiceID,
	).Scan(&value.Name, &value.Group, &value.Host, &value.Path, &value.Public, &value.TLS, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Generated routes remain generated in the worker payload. Passing their
	// derived hostname would reclassify them as custom.
	if kind == string(router.DomainKindGenerated) {
		value.Host = ""
	}
	return &value, nil
}

func (s *PostgresDeploymentActionStore) CommitRuntimeAction(ctx context.Context, request RuntimeActionRequest) (DeploymentActionResult, error) {
	if err := s.ready(); err != nil {
		return DeploymentActionResult{}, err
	}
	if strings.TrimSpace(request.ServiceID) == "" || strings.TrimSpace(request.TargetID) == "" ||
		strings.TrimSpace(request.TriggerKey) == "" || !deploy.ValidRuntimeAction(request.Action) {
		return DeploymentActionResult{}, errors.New("runtime action requires service, target, trigger, and valid action")
	}
	now := s.now(request.Now)
	ids := s.prepareRuntimeActionIDs()
	var result DeploymentActionResult
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		return s.commitRuntimeActionPreparedTx(ctx, tx, request, now, ids, &result)
	})
	return result, err
}

func (s *PostgresDeploymentActionStore) commitRuntimeActionPreparedTx(
	ctx context.Context,
	tx *pg.Tx,
	request RuntimeActionRequest,
	now time.Time,
	ids deploymentActionPreparedIDs,
	result *DeploymentActionResult,
) error {
	if err := replayDeploymentActionResult(ctx, tx, request.TriggerKey, result); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"deployment-runtime-action:"+request.TargetID,
	); err != nil {
		return fmt.Errorf("runtime action target lock: %w", err)
	}
	target, _, err := loadReplaySource(ctx, tx, request.ServiceID, request.TargetID)
	if err != nil {
		return err
	}
	var pending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM deployment_runtime_actions
			 WHERE deployment_id=$1
			   AND state IN ('queued','running')
		)`,
		target.ID,
	).Scan(&pending); err != nil {
		return fmt.Errorf("runtime action pending lookup: %w", err)
	}
	if pending {
		return fmt.Errorf(
			"%w: deployment already has a queued or running runtime action",
			ErrDeploymentActionConflict,
		)
	}
	if err := validateRuntimeActionTransition(request.Action, target); err != nil {
		return err
	}

	operationID := ids.OperationID
	actionID := ids.ActionID
	outboxID := ids.OutboxID
	if _, err := tx.Exec(ctx,
		`INSERT INTO operations (id,kind,state,expires_at,created_at,updated_at)
		 VALUES ($1,'deploy','QUEUED',$2,$3,$3)`,
		string(operationID), now.Add(24*time.Hour), now,
	); err != nil {
		return fmt.Errorf("runtime action operation insert: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployment_runtime_actions
		    (id,deployment_id,operation_id,action,state,trigger_key,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,'queued',$5,$6,$6)`,
		actionID, target.ID, string(operationID), string(request.Action), request.TriggerKey, now,
	); err != nil {
		return fmt.Errorf("runtime action insert: %w", err)
	}
	payload, err := json.Marshal(deploy.DeploymentActionJobPayload{
		SchemaVersion: deploy.DeploymentActionJobPayloadSchema,
		ActionID:      actionID, OperationID: string(operationID),
		DeploymentID: target.ID, Action: request.Action,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO staged_work_outbox (
		    id,dedupe_key,stage,operation_id,deployment_id,queue,partition_key,
		    available_at,payload,state,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,'deploy',$6,$7,$8::jsonb,'pending',$7,$7)`,
		outboxID, request.TriggerKey, StagedWorkDeploymentRuntimeAction,
		string(operationID), target.ID, target.ProjectID, now, string(payload),
	); err != nil {
		return fmt.Errorf("runtime action outbox insert: %w", err)
	}
	if err := appendOperationEventTx(ctx, tx, operationID, orailtypes.OperationQueued,
		string(request.Action)+" accepted for deployment "+target.ID, now); err != nil {
		return err
	}
	*result = DeploymentActionResult{
		Operation:  orailtypes.Operation{ID: operationID, Kind: "deploy", State: orailtypes.OperationQueued},
		Deployment: target, ActionID: actionID, OutboxID: outboxID, Created: true,
	}
	return nil
}

func (s *PostgresDeploymentActionStore) BeginRuntimeAction(ctx context.Context, actionID, operationID string) (deploy.RuntimeActionRecord, error) {
	if err := s.ready(); err != nil {
		return deploy.RuntimeActionRecord{}, err
	}
	now := s.now(time.Time{})
	var out deploy.RuntimeActionRecord
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var action, state string
		err := tx.QueryRow(ctx, `
			SELECT a.id,a.operation_id,a.deployment_id,d.project_id,d.service_id,d.environment_id,
			       a.action,a.state,a.step,d.active,d.runtime_desired_state,d.image_ref
			  FROM deployment_runtime_actions a
			  JOIN deployments d ON d.id=a.deployment_id
			 WHERE a.id=$1 AND a.operation_id=$2
			 FOR UPDATE OF a,d`,
			actionID, operationID,
		).Scan(
			&out.ID, &out.OperationID, &out.DeploymentID, &out.ProjectID, &out.ServiceID,
			&out.EnvironmentID, &action, &state, &out.Step, &out.DeploymentActive,
			&out.DesiredState, &out.Image,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeploymentNotFound
		}
		if err != nil {
			return err
		}
		out.Action = deploy.RuntimeAction(action)
		out.State = state
		out.InstanceID = out.DeploymentID + "-r0"
		if state == "queued" {
			_, err = tx.Exec(ctx, `
				UPDATE deployment_runtime_actions
				   SET state='running',started_at=COALESCE(started_at,$2),
				       updated_at=$2,resource_version=resource_version+1
				 WHERE id=$1`, actionID, now)
			out.State = "running"
			return err
		}
		if state != "running" {
			return fmt.Errorf("%w: action is %s", ErrDeploymentActionConflict, state)
		}
		return nil
	})
	return out, err
}

func (s *PostgresDeploymentActionStore) RecordRuntimeActionStep(ctx context.Context, actionID, step string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if step == "started" || step == "route_withdrawn" || step == "stopped" {
		return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
			var deploymentID, action string
			if err := tx.QueryRow(ctx, `
				UPDATE deployment_runtime_actions SET step=$2,updated_at=$3,resource_version=resource_version+1
				WHERE id=$1 AND state='running' AND ($2<>'started' OR action='restart') RETURNING deployment_id,action`,
				actionID, step, s.now(time.Time{})).Scan(&deploymentID, &action); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrDeploymentActionConflict
				}
				return err
			}
			if step != "started" {
				if action != "stop" && !(action == "restart" && step == "stopped") {
					return nil
				}
				// Startup route repair must not resurrect a withdrawn stop after
				// a crash, or publish a restart still waiting for app readiness.
				_, err := tx.Exec(ctx, `UPDATE deployments SET runtime_desired_state='stopped',updated_at=$2,
					resource_version=resource_version+1 WHERE id=$1`, deploymentID, s.now(time.Time{}))
				return err
			}
			// "started" is recorded only after application readiness succeeds.
			// Until then startup repair must see durable stopped intent. Route
			// publication and observed success still remain to be verified.
			tag, err := tx.Exec(ctx, `UPDATE deployments SET runtime_desired_state='running',runtime_observed_state='unknown',
				updated_at=$2,resource_version=resource_version+1 WHERE id=$1 AND active=TRUE AND state='active' AND runtime_desired_state<>'removed'`, deploymentID, s.now(time.Time{}))
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrDeploymentActionConflict
			}
			return nil
		})
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE deployment_runtime_actions
		   SET step=$2,updated_at=$3,resource_version=resource_version+1
		 WHERE id=$1 AND state='running'`,
		actionID, step, s.now(time.Time{}),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrDeploymentActionConflict
	}
	return nil
}

func (s *PostgresDeploymentActionStore) CompleteRuntimeAction(ctx context.Context, actionID, runtimeState string) error {
	if err := s.ready(); err != nil {
		return err
	}
	now := s.now(time.Time{})
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var scope router.RouteScope
		if runtimeState == "removed" {
			if err := tx.QueryRow(ctx, `SELECT d.project_id,d.environment_id,d.service_id FROM deployments d
				JOIN deployment_runtime_actions a ON a.deployment_id=d.id WHERE a.id=$1`, actionID).Scan(&scope.ProjectID, &scope.EnvironmentID, &scope.ServiceID); err != nil {
				return err
			}
			if err := router.AcquireRouteEnvironmentMutationLock(ctx, tx, scope.ProjectID, scope.EnvironmentID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "service-environment-deploy:"+scope.ServiceID+":"+scope.EnvironmentID); err != nil {
				return err
			}
		}
		var deploymentID, action string
		if err := tx.QueryRow(ctx, `
			UPDATE deployment_runtime_actions
			   SET state='succeeded',completed_at=$2,updated_at=$2,
			       resource_version=resource_version+1
			 WHERE id=$1 AND state='running'
			 RETURNING deployment_id,action`,
			actionID, now,
		).Scan(&deploymentID, &action); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDeploymentActionConflict
			}
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE deployments
			   SET runtime_desired_state=$2,runtime_observed_state=$2,
			       state=CASE WHEN $2='removed' AND active THEN 'superseded' ELSE state END,
			       active=CASE WHEN $2='removed' THEN FALSE ELSE active END,
			       runtime_generation=runtime_generation+1,updated_at=$3,
			       resource_version=resource_version+1
			 WHERE id=$1
			   AND ($2 <> 'removed' OR active=FALSE OR runtime_desired_state='stopped')`,
			deploymentID, runtimeState, now,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: %s cannot set runtime state %s", ErrDeploymentActionConflict, action, runtimeState)
		}
		if runtimeState == "removed" {
			// Preserve a newer deployment that won promotion while runtime removal
			// was in flight. Only this exact removed pointer can be cleared.
			_, err := tx.Exec(ctx, `UPDATE service_environment_state SET active_deployment_id=NULL,version=version+1,updated_at=$4
				WHERE service_id=$1 AND environment_id=$2 AND active_deployment_id=$3`, scope.ServiceID, scope.EnvironmentID, deploymentID, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *PostgresDeploymentActionStore) FailRuntimeAction(ctx context.Context, actionID string, code failurecodes.Code, message string) error {
	if err := s.ready(); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE deployment_runtime_actions
		   SET state='failed',failure_code=$2,failure_message=$3,
		       completed_at=$4,updated_at=$4,resource_version=resource_version+1
		 WHERE id=$1 AND state IN ('queued','running')`,
		actionID, string(code), boundedOutboxError(errors.New(message)), s.now(time.Time{}),
	)
	return err
}

func replayDeploymentActionResult(ctx context.Context, tx *pg.Tx, triggerKey string, out *DeploymentActionResult) error {
	var operationID, deploymentID, outboxID, actionID string
	var createdAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT o.operation_id,o.deployment_id,o.id,
		       COALESCE(a.id,''),op.created_at
		  FROM staged_work_outbox o
		  JOIN operations op ON op.id=o.operation_id
		  LEFT JOIN deployment_runtime_actions a ON a.operation_id=o.operation_id
		 WHERE o.dedupe_key=$1`,
		triggerKey,
	).Scan(&operationID, &deploymentID, &outboxID, &actionID, &createdAt)
	if err != nil {
		return err
	}
	var kind, state string
	if err := tx.QueryRow(ctx, `SELECT kind,state FROM operations WHERE id=$1`, operationID).Scan(&kind, &state); err != nil {
		return err
	}
	deployment, err := loadDeploymentTx(ctx, tx, deploymentID)
	if err != nil {
		return err
	}
	*out = DeploymentActionResult{
		Operation: orailtypes.Operation{
			ID: orailtypes.ID(operationID), Kind: kind,
			State: orailtypes.OperationState(state),
		},
		Deployment: deployment, ActionID: actionID, OutboxID: outboxID, Created: false,
	}
	return nil
}

func loadReplaySource(ctx context.Context, tx *pg.Tx, serviceID, deploymentID string) (DeploymentRecord, string, error) {
	source, err := loadDeploymentTx(ctx, tx, deploymentID)
	if err != nil {
		return DeploymentRecord{}, "", err
	}
	if source.ServiceID != serviceID {
		return DeploymentRecord{}, "", ErrDeploymentNotFound
	}
	var active *string
	err = tx.QueryRow(ctx, `
		SELECT active_deployment_id
		  FROM service_environment_state
		 WHERE service_id=$1 AND environment_id=$2`,
		source.ServiceID, source.EnvironmentID,
	).Scan(&active)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DeploymentRecord{}, "", err
	}
	if active == nil {
		return source, "", nil
	}
	return source, *active, nil
}

func loadDeploymentTx(ctx context.Context, tx *pg.Tx, id string) (DeploymentRecord, error) {
	var out DeploymentRecord
	var action, state string
	err := tx.QueryRow(ctx, `
		SELECT id,project_id,service_id,environment_id,action,source_type,repo,ref,sha,
		       root_directory,dockerfile_path,builder,image_ref,image_digest,state,active,
		       COALESCE(operation_id,''),COALESCE(trigger_key,''),
		       COALESCE(rollback_source_deployment_id,''),
		       COALESCE(expected_active_deployment_id,''),
		       configuration_revision,
		       failure_code,failure_message,runtime_desired_state,runtime_observed_state,
		       runtime_generation,created_at,updated_at,started_at,completed_at
		  FROM deployments WHERE id=$1`,
		id,
	).Scan(
		&out.ID, &out.ProjectID, &out.ServiceID, &out.EnvironmentID, &action,
		&out.SourceType, &out.Repo, &out.Ref, &out.SHA, &out.RootDirectory,
		&out.DockerfilePath, &out.Builder, &out.ImageRef, &out.ImageDigest,
		&state, &out.Active, &out.OperationID, &out.TriggerKey, &out.RollbackSourceID,
		&out.ExpectedActiveID, &out.ConfigurationRevision, &out.FailureCode, &out.FailureMessage,
		&out.RuntimeDesiredState, &out.RuntimeObservedState, &out.RuntimeGeneration,
		&out.CreatedAt, &out.UpdatedAt, &out.StartedAt, &out.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeploymentRecord{}, ErrDeploymentNotFound
	}
	if err != nil {
		return DeploymentRecord{}, err
	}
	out.Action = deploy.DeploymentAction(action)
	out.State = deploy.DeploymentState(state)
	return out, nil
}

func imageWithDigest(imageRef, imageDigest string) string {
	imageRef = strings.TrimSpace(imageRef)
	imageDigest = strings.TrimSpace(imageDigest)
	if strings.Contains(imageRef, "@sha256:") {
		return imageRef
	}
	if at := strings.IndexByte(imageRef, '@'); at >= 0 {
		imageRef = imageRef[:at]
	}
	if imageRef == "" {
		return imageDigest
	}
	return imageRef + "@" + imageDigest
}

func actionMode(action deploy.DeploymentAction) string {
	if action == deploy.ActionRollbackDigest {
		return "rollback"
	}
	return "redeploy"
}

var _ DeploymentActionIntake = (*PostgresDeploymentActionStore)(nil)
var _ deploy.RuntimeActionStore = (*PostgresDeploymentActionStore)(nil)

// validateRuntimeActionTransition reports whether action may be queued
// against target. It is pure so the rules can be tested without a database.
//
// `Active` is not a runtime fact: it marks the deployment currently owning
// the service and only clears when a newer deployment supersedes it. Stopping
// a deployment therefore leaves Active set, which is why removal must key off
// the runtime state instead. Gating removal on Active alone made a service's
// only deployment impossible to remove — stop refused to clear the flag and
// remove refused while it was set — so stop, remove and delete could never
// terminate for a single-deployment service.
func validateRuntimeActionTransition(action deploy.RuntimeAction, target DeploymentRecord) error {
	switch action {
	case deploy.RuntimeActionStop:
		if !target.Active {
			return fmt.Errorf("%w: %s requires the active deployment", ErrDeploymentActionConflict, action)
		}
		if target.RuntimeDesiredState != "running" {
			return fmt.Errorf("%w: stop requires a running deployment", ErrDeploymentActionConflict)
		}
	case deploy.RuntimeActionRestart:
		if !target.Active {
			return fmt.Errorf("%w: %s requires the active deployment", ErrDeploymentActionConflict, action)
		}
		if target.RuntimeDesiredState == "removed" {
			return fmt.Errorf("%w: removed deployment cannot be restarted", ErrDeploymentActionConflict)
		}
	case deploy.RuntimeActionRemove:
		if target.RuntimeDesiredState == "removed" {
			return fmt.Errorf("%w: deployment is already removed", ErrDeploymentActionConflict)
		}
		if target.Active && target.RuntimeDesiredState == "running" {
			return fmt.Errorf("%w: stop this deployment before removing it", ErrDeploymentActionConflict)
		}
	}
	return nil
}
