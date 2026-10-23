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
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func advanceEnvironmentConfigurationRevisionInTx(
	ctx context.Context,
	tx *pg.Tx,
	authorization tenantDeployAuthorization,
	changeset EnvironmentChangeset,
	validation EnvironmentChangesetValidation,
	now time.Time,
) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, `
		INSERT INTO environment_configuration_revisions (
			environment_id,project_id,workspace_id,applied_revision,updated_at
		) VALUES ($1,$2,$3,1,$4)
		ON CONFLICT (environment_id) DO UPDATE SET
			applied_revision=environment_configuration_revisions.applied_revision+1,
			updated_at=EXCLUDED.updated_at
		RETURNING applied_revision`,
		changeset.EnvironmentID, authorization.ProjectID, authorization.WorkspaceID, now,
	).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("advance environment configuration revision: %w", err)
	}
	for _, affected := range validation.AffectedResources {
		deployedRevision := int64(0)
		if !affected.RequiresDeploy {
			deployedRevision = revision
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO service_configuration_revisions (
				project_id,environment_id,service_id,applied_revision,deployed_revision,updated_at
			) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (environment_id,service_id) DO UPDATE SET
				applied_revision=EXCLUDED.applied_revision,
				deployed_revision=CASE WHEN $7 THEN service_configuration_revisions.deployed_revision ELSE EXCLUDED.applied_revision END,
				updated_at=EXCLUDED.updated_at`,
			authorization.ProjectID, changeset.EnvironmentID, affected.ServiceID,
			revision, deployedRevision, now, affected.RequiresDeploy,
		)
		if err != nil {
			return 0, fmt.Errorf("record service applied configuration revision: %w", err)
		}
	}
	return revision, nil
}

func loadEnvironmentConfigurationStateInTx(ctx context.Context, tx *pg.Tx, projectID, environmentID string) (EnvironmentConfigurationState, error) {
	out := EnvironmentConfigurationState{Services: []EnvironmentServiceConfigurationRevision{}}
	err := tx.QueryRow(ctx, `SELECT applied_revision FROM environment_configuration_revisions WHERE environment_id=$1 AND project_id=$2`, environmentID, projectID).Scan(&out.AppliedRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `
		SELECT service_id,applied_revision,deployed_revision,
		       COALESCE(deployed_deployment_id,''),COALESCE(pending_deployment_id,'')
		  FROM service_configuration_revisions
		 WHERE project_id=$1 AND environment_id=$2
		 ORDER BY service_id`, projectID, environmentID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item EnvironmentServiceConfigurationRevision
		if err := rows.Scan(&item.ServiceID, &item.AppliedRevision, &item.DeployedRevision, &item.DeployedDeploymentID, &item.PendingDeploymentID); err != nil {
			return out, err
		}
		item.DeploymentRequired = item.DeployedRevision < item.AppliedRevision
		out.Services = append(out.Services, item)
	}
	return out, rows.Err()
}

func (c *PostgresTenantMutationCoordinator) GetEnvironmentOperationGroup(ctx context.Context, request EnvironmentChangesetReadRequest, groupID string) (EnvironmentOperationGroup, error) {
	if err := c.validateEnvironmentChangesetRead(request); err != nil || groupID == "" {
		if err != nil {
			return EnvironmentOperationGroup{}, err
		}
		return EnvironmentOperationGroup{}, errEnvironmentChangesetNotFound
	}
	var group EnvironmentOperationGroup
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx *pg.Tx) error {
		// Operation groups expose service-scoped deployment and operation
		// identifiers. Project visibility alone is intentionally insufficient:
		// deployer-only members may trigger Git automation, but cannot inspect
		// service state unless they also hold a direct project grant.
		mutationCtx, authorization, err := c.authorizeEnvironmentChangesetInTx(ctx, tx, request, identity.CapabilityServiceRead, c.now().Truncate(time.Microsecond))
		if err != nil {
			return err
		}
		var storedState string
		err = tx.QueryRow(mutationCtx, `SELECT id,environment_id,changeset_id,configuration_revision,state,created_at FROM environment_operation_groups WHERE id=$1 AND environment_id=$2 AND project_id=$3`, groupID, request.EnvironmentID, authorization.ProjectID).Scan(&group.ID, &group.EnvironmentID, &group.ChangesetID, &group.ConfigurationRevision, &storedState, &group.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errEnvironmentChangesetNotFound
		}
		if err != nil {
			return err
		}
		group.State, group.Items = storedState, []EnvironmentOperationGroupItem{}
		rows, err := tx.Query(mutationCtx, `
			SELECT i.service_id,i.resource_kinds,i.requires_deploy,i.deployment_action,i.intake_state,
			       COALESCE(i.deployment_id,''),COALESCE(i.operation_id,''),
			       COALESCE(o.state,''),COALESCE(d.state,''),
			       i.failure_code,i.failure_reason
			  FROM environment_operation_group_items i
			  LEFT JOIN operations o ON o.id=i.operation_id
			  LEFT JOIN deployments d ON d.id=i.deployment_id
			 WHERE i.group_id=$1 AND i.project_id=$2 AND i.environment_id=$3
			 ORDER BY i.service_id`, group.ID, authorization.ProjectID, request.EnvironmentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item EnvironmentOperationGroupItem
			if err := rows.Scan(&item.ServiceID, &item.ResourceKinds, &item.RequiresDeploy, &item.DeploymentAction, &item.IntakeState, &item.DeploymentID, &item.OperationID, &item.OperationState, &item.DeploymentState, &item.FailureCode, &item.FailureReason); err != nil {
				return err
			}
			group.Items = append(group.Items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		kindItems := make([]EnvironmentChangesetItem, 0)
		for _, item := range group.Items {
			for _, kind := range item.ResourceKinds {
				kindItems = append(kindItems, EnvironmentChangesetItem{ServiceID: item.ServiceID, Kind: kind})
			}
		}
		// Service visibility is the baseline, not a substitute for resource
		// visibility. Apply the same current-authority kind checks used by the
		// changeset review endpoint before returning grouped rollout evidence.
		if err := authorizeEnvironmentChangesetKinds(authorization, kindItems, false); err != nil {
			return err
		}
		group.State = aggregateEnvironmentOperationGroupState(storedState, group.Items)
		return nil
	})
	return group, err
}

// aggregateEnvironmentOperationGroupState keeps intake acceptance separate
// from rollout completion. In particular, one failed intake must not make the
// group look terminal while an admitted sibling is still running.
func aggregateEnvironmentOperationGroupState(storedState string, items []EnvironmentOperationGroupItem) string {
	var inFlight, intakeFailed, runtimeFailed bool
	for _, item := range items {
		if !item.RequiresDeploy {
			continue
		}
		if item.IntakeState == "failed" {
			intakeFailed = true
			continue
		}
		failed := item.DeploymentState == string(deploy.DeploymentFailed) ||
			item.DeploymentState == string(deploy.DeploymentCanceled) ||
			item.OperationState == string(orailtypes.OperationFailed) ||
			item.OperationState == string(orailtypes.OperationCanceled)
		if failed {
			runtimeFailed = true
			continue
		}
		succeeded := item.OperationState == string(orailtypes.OperationSucceeded) &&
			(item.DeploymentState == string(deploy.DeploymentActive) ||
				item.DeploymentState == string(deploy.DeploymentSuperseded))
		if !succeeded {
			inFlight = true
		}
	}
	switch {
	case inFlight:
		return "running"
	case storedState == "partial" || intakeFailed:
		return "partial"
	case runtimeFailed:
		return "failed"
	default:
		return "succeeded"
	}
}

func (c *PostgresTenantMutationCoordinator) applyAndDeployEnvironmentChangeset(ctx context.Context, request EnvironmentChangesetMutationRequest) (TenantMutationResult, error) {
	defer clear(request.RawBody)
	if err := c.validateEnvironmentChangesetMutation(request, false); err != nil {
		return TenantMutationResult{}, err
	}
	if c.DeploymentActions == nil || c.DeploymentActions.Pool != c.Pool {
		return TenantMutationResult{}, errors.New("environment apply and deploy authority is unavailable")
	}
	if err := c.DeploymentActions.ready(); err != nil {
		return TenantMutationResult{}, err
	}
	if err := c.bindEnvironmentChangesetFingerprint(&request); err != nil {
		return TenantMutationResult{}, err
	}
	groupID := string(c.IDFactory(orailtypes.IDPrefix("opg")))
	preparedIDs := make([]deploymentActionPreparedIDs, environmentChangesetMaxItems)
	for i := range preparedIDs {
		preparedIDs[i] = c.DeploymentActions.prepareImageReplayIDs()
	}
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		lockedInstallations, err := c.discoverEnvironmentChangesetGitHubInstallations(ctx, request.EnvironmentID)
		if err != nil {
			return TenantMutationResult{}, err
		}
		now := c.now().Truncate(time.Microsecond)
		err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			if err := lockEnvironmentChangesetGitHubInstallations(ctx, tx, lockedInstallations); err != nil {
				return err
			}
			mutationCtx, authorization, err := c.authorizeEnvironmentChangesetInTx(ctx, tx, request.EnvironmentChangesetReadRequest, identity.CapabilityProjectRead, now)
			if err != nil {
				return err
			}
			reservation, err := c.reserveEnvironmentChangesetInTx(mutationCtx, tx, request)
			if err != nil {
				return err
			}
			if replay, ok, err := environmentChangesetReplay(reservation); ok || err != nil {
				result = replay
				return err
			}
			changeset, err := loadActiveEnvironmentChangesetInTx(mutationCtx, tx, request.EnvironmentID, true)
			if err != nil {
				return err
			}
			if err := authorizeEnvironmentChangesetKinds(authorization, changeset.Items, true); err != nil {
				return err
			}
			if changeset.Revision != request.ExpectedRevision {
				response, responseErr := environmentChangesetProblemResponse(http.StatusPreconditionFailed, failurecodes.PreconditionFailed, "environment changeset revision changed", request.RequestID, map[string]any{"expected_revision": request.ExpectedRevision, "current_revision": changeset.Revision})
				if responseErr != nil {
					return responseErr
				}
				if err := c.completeEnvironmentChangesetFailureInTx(mutationCtx, tx, reservation, request, authorization, changeset, response, "changeset.apply-and-deploy", failurecodes.PreconditionFailed, now); err != nil {
					return err
				}
				result = TenantMutationResult{Response: response}
				return nil
			}
			if err := authorizeEnvironmentChangesetViewServicesInTx(mutationCtx, tx, authorization.ProjectID, request.EnvironmentID, changeset.Items); err != nil {
				return err
			}
			validation, err := c.validateLoadedChangesetInTx(mutationCtx, tx, changeset)
			if err != nil {
				return err
			}
			if !validation.Valid {
				response, responseErr := environmentChangesetProblemResponse(http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, "environment changeset validation failed", request.RequestID, map[string]any{"errors": validation.Errors})
				if responseErr != nil {
					return responseErr
				}
				if err := c.completeEnvironmentChangesetFailureInTx(mutationCtx, tx, reservation, request, authorization, changeset, response, "changeset.apply-and-deploy", failurecodes.ResourceValidationFailed, now); err != nil {
					return err
				}
				result = TenantMutationResult{Response: response}
				return nil
			}
			if environmentValidationRequiresDeploy(validation) {
				decision := identity.EvaluateAccess(identity.AccessRequest{WorkspaceRole: authorization.WorkspaceRole, ProjectRole: authorization.ProjectRole, Capability: identity.CapabilityDeploymentTriggerCLI, EnvironmentRestricted: authorization.EnvironmentRestricted})
				if !decision.Allowed {
					if decision.Reason == "environment-restricted" {
						return identity.ErrAccessNotFound
					}
					return identity.ErrAuthorizationDenied
				}
			}
			volumeDelta, storageChanged := environmentChangesetVolumeDelta(changeset.Items)
			var volumeReservation metering.Reservation
			var volumeLimitEnforced bool
			if storageChanged {
				if c.Metering == nil || c.Metering.Pool != c.Pool {
					return errors.New("environment changeset usage admission authority is unavailable")
				}
				if volumeDelta > 0 {
					volumeReservation, volumeLimitEnforced, err = c.Metering.ReserveIfEnforcedInTx(mutationCtx, tx, metering.ReservationRequest{WorkspaceID: authorization.WorkspaceID, ActorUserID: request.Principal.UserID, Resource: metering.ResourceVolumeCount, Quantity: volumeDelta, IdempotencyKey: "changeset-volume:" + string(request.Key), IntentHash: request.Fingerprint.BodySHA256, TTL: 10 * time.Minute})
					var limitErr *metering.LimitError
					if errors.As(err, &limitErr) {
						response, responseErr := environmentChangesetProblemResponse(http.StatusConflict, failurecodes.ResourceConflict, "workspace volume limit exceeded", request.RequestID, map[string]any{"dimension": limitErr.Resource, "used": limitErr.Used, "reserved": limitErr.Reserved, "requested": limitErr.Requested, "limit": limitErr.Limit})
						if responseErr != nil {
							return responseErr
						}
						if err := c.completeEnvironmentChangesetFailureInTx(mutationCtx, tx, reservation, request, authorization, changeset, response, "changeset.apply-and-deploy", failurecodes.ResourceConflict, now); err != nil {
							return err
						}
						result = TenantMutationResult{Response: response}
						return nil
					}
					if err != nil {
						return fmt.Errorf("reserve environment volume capacity: %w", err)
					}
				}
			}
			if err := c.applyEnvironmentChangesetInTx(mutationCtx, tx, authorization, changeset, now); err != nil {
				return err
			}
			if storageChanged {
				if volumeLimitEnforced {
					if err := c.Metering.CommitReservationInTx(mutationCtx, tx, volumeReservation, now); err != nil {
						return fmt.Errorf("commit environment volume reservation: %w", err)
					}
				} else if _, err := c.Metering.RecordResourceGaugeInTx(mutationCtx, tx, authorization.WorkspaceID, metering.ResourceVolumeCount, now); err != nil {
					return fmt.Errorf("record environment volume count: %w", err)
				}
			}
			appliedRevision, err := advanceEnvironmentConfigurationRevisionInTx(mutationCtx, tx, authorization, changeset, validation, now)
			if err != nil {
				return err
			}
			changeset.AppliedRevision = &appliedRevision
			group := EnvironmentOperationGroup{ID: groupID, EnvironmentID: changeset.EnvironmentID, ChangesetID: changeset.ID, ConfigurationRevision: appliedRevision, State: "accepted", Items: []EnvironmentOperationGroupItem{}, CreatedAt: now}
			if _, err := tx.Exec(mutationCtx, `INSERT INTO environment_operation_groups (id,workspace_id,project_id,environment_id,changeset_id,configuration_revision,state,created_by,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,'accepted',$7,$8,$8)`, group.ID, authorization.WorkspaceID, authorization.ProjectID, changeset.EnvironmentID, changeset.ID, appliedRevision, request.Principal.UserID, now); err != nil {
				return fmt.Errorf("insert environment operation group: %w", err)
			}
			for index, affected := range validation.AffectedResources {
				item := EnvironmentOperationGroupItem{ServiceID: affected.ServiceID, ResourceKinds: affected.ResourceKinds, RequiresDeploy: affected.RequiresDeploy, DeploymentAction: affected.DeploymentAction, IntakeState: "not_required"}
				if affected.RequiresDeploy {
					item.IntakeState = "failed"
					var sourceID string
					err := tx.QueryRow(mutationCtx, `SELECT id FROM deployments WHERE project_id=$1 AND environment_id=$2 AND service_id=$3 AND active=TRUE AND state='active' ORDER BY updated_at DESC,id DESC LIMIT 1 FOR UPDATE`, authorization.ProjectID, changeset.EnvironmentID, affected.ServiceID).Scan(&sourceID)
					if errors.Is(err, pgx.ErrNoRows) {
						item.FailureCode = string(failurecodes.ResourceConflict)
						item.FailureReason = environmentMissingActiveDeploymentReason(affected.DeploymentAction)
					} else if err != nil {
						return err
					} else {
						var actionResult DeploymentActionResult
						var actionErr error
						switch affected.DeploymentAction {
						case environmentDeploymentActionRedeploy:
							actionErr = c.DeploymentActions.commitImageReplayPreparedTx(mutationCtx, tx, ImageReplayRequest{ServiceID: affected.ServiceID, SourceID: sourceID, Action: deploy.ActionRedeployImage, TriggerKey: fmt.Sprintf("changeset:%s:%d:%s", changeset.ID, appliedRevision, affected.ServiceID), Now: now}, now, preparedIDs[index], &actionResult)
						case environmentDeploymentActionRebuild:
							var source DeploymentRecord
							source, actionErr = loadDeploymentTx(mutationCtx, tx, sourceID)
							if actionErr == nil {
								actionResult, actionErr = c.commitEnvironmentSourceRebuildInTx(mutationCtx, tx, request, reservation, authorization, changeset, affected, source, appliedRevision, lockedInstallations, now, preparedIDs[index])
							}
						default:
							actionErr = fmt.Errorf("invalid environment deployment action %q", affected.DeploymentAction)
						}
						if actionErr == nil {
							if _, err := c.ServiceConfigs.ReplaceDeploymentSnapshotInTx(mutationCtx, tx, actionResult.Deployment.ID, authorization.ProjectID, changeset.EnvironmentID, affected.ServiceID); err != nil {
								return err
							}
							if _, err := tx.Exec(mutationCtx, `UPDATE operations SET actor=$2,target_resource_kind='service',target_resource_id=$3,idempotency_key=$4,updated_at=$5 WHERE id=$1`, string(actionResult.Operation.ID), request.Principal.UserID, affected.ServiceID, string(request.Key), now); err != nil {
								return err
							}
							if _, err := tx.Exec(mutationCtx, `UPDATE service_configuration_revisions SET pending_deployment_id=$3,updated_at=$4 WHERE environment_id=$1 AND service_id=$2`, changeset.EnvironmentID, affected.ServiceID, actionResult.Deployment.ID, now); err != nil {
								return err
							}
							item.IntakeState, item.DeploymentID, item.OperationID = "queued", actionResult.Deployment.ID, string(actionResult.Operation.ID)
						} else if failureCode, failureReason, handled := environmentDeploymentIntakeFailure(affected.DeploymentAction, actionErr); handled {
							item.FailureCode, item.FailureReason = failureCode, failureReason
						} else {
							return actionErr
						}
					}
				}
				if item.IntakeState == "failed" {
					group.State = "partial"
				}
				if _, err := tx.Exec(mutationCtx, `INSERT INTO environment_operation_group_items (group_id,project_id,environment_id,service_id,resource_kinds,requires_deploy,deployment_action,intake_state,deployment_id,operation_id,failure_code,failure_reason,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)`, group.ID, authorization.ProjectID, changeset.EnvironmentID, item.ServiceID, item.ResourceKinds, item.RequiresDeploy, item.DeploymentAction, item.IntakeState, nullableString(item.DeploymentID), nullableString(item.OperationID), item.FailureCode, item.FailureReason, now); err != nil {
					return fmt.Errorf("insert environment operation group item: %w", err)
				}
				group.Items = append(group.Items, item)
			}
			if group.State == "partial" {
				if _, err := tx.Exec(mutationCtx, `UPDATE environment_operation_groups SET state='partial',updated_at=$2 WHERE id=$1`, group.ID, now); err != nil {
					return err
				}
				// The durable partial marker records intake truth, but the response
				// must not present that terminal aggregate while an admitted sibling
				// is still in flight.
				group.State = aggregateEnvironmentOperationGroupState("partial", group.Items)
			}
			changeset.State, changeset.AppliedAt, changeset.UpdatedAt = "applied", &now, now
			changeset.Revision++
			if _, err := tx.Exec(mutationCtx, `UPDATE environment_changesets SET state='applied',revision=$2,updated_at=$3,applied_at=$3,applied_configuration_revision=$4 WHERE id=$1 AND state='draft'`, changeset.ID, changeset.Revision, now, appliedRevision); err != nil {
				return err
			}
			if _, err := tx.Exec(mutationCtx, `UPDATE environment_changeset_items SET value_ciphertext=''::bytea,kek_id='',wrapped_dek=''::bytea,canonical_form='',"references"='[]'::jsonb WHERE changeset_id=$1`, changeset.ID); err != nil {
				return err
			}
			responseValue := EnvironmentApplyAndDeployResponse{AppliedRevision: appliedRevision, OperationGroupID: group.ID, AffectedServices: validation.AffectedServices, Changeset: &changeset, Validation: validation, OperationGroup: group}
			response, err := environmentApplyAndDeployStoredResponse(responseValue)
			if err != nil {
				return err
			}
			if _, err := c.Idempotency.CompleteSuccessInTx(mutationCtx, tx, reservation.Reference(), response); err != nil {
				return err
			}
			if _, err := c.Audit.AppendInTx(mutationCtx, tx, audit.Row{RequestID: request.RequestID, OccurredAt: now, RecordedAt: now, ActorID: request.Principal.UserID, ActorType: "user", AuthMethod: request.Principal.AuthMethod, SourceIP: normalizedMutationSourceIP(request.SourceIP), UserAgent: request.UserAgent, WorkspaceID: authorization.WorkspaceID, ProjectID: authorization.ProjectID, EnvironmentID: changeset.EnvironmentID, ResourceKind: "environment_operation_group", ResourceID: group.ID, Action: "changeset.apply-and-deploy", PolicyDecision: "allowed", After: map[string]any{"changeset_id": changeset.ID, "configuration_revision": appliedRevision, "state": group.State, "affected_services": validation.AffectedServices}, Attrs: map[string]any{"session_id": request.Principal.SessionID}}); err != nil {
				return err
			}
			result = TenantMutationResult{Response: response}
			return nil
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("environment apply-and-deploy serializable retries exhausted")
}

func environmentMissingActiveDeploymentReason(action string) string {
	if action == environmentDeploymentActionRebuild {
		return "no active immutable canonical source is available to rebuild"
	}
	return "no active immutable deployment is available to redeploy"
}

func environmentValidationRequiresDeploy(validation EnvironmentChangesetValidation) bool {
	for _, affected := range validation.AffectedResources {
		if affected.RequiresDeploy {
			return true
		}
	}
	return false
}

func environmentApplyAndDeployStoredResponse(value EnvironmentApplyAndDeployResponse) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Location", v1alphaPrefix+"/environments/"+value.OperationGroup.EnvironmentID+"/operation-groups/"+value.OperationGroupID)
	headers.Set("X-Midnight-Delivery-State", value.OperationGroup.State)
	return idempotency.StoredResponse{StatusCode: http.StatusAccepted, Header: headers, Body: body}, nil
}
