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

	"github.com/midnight/midnight/internal/backup"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
)

// VolumeRestoreApplyResult is the complete durable cutover decision. The
// replacement attachment and immutable redeploy/outbox are committed in one
// Postgres transaction; runtime cutover is then performed by the normal deploy
// worker and remains observable through the returned operation.
type VolumeRestoreApplyResult struct {
	Restore          backup.VolumeRestoreRun `json:"restore"`
	DeploymentAction DeploymentActionResult  `json:"deployment_action"`
	Created          bool                    `json:"created"`
}

type VolumeRecoveryApply interface {
	ApplyRestoreAndRedeploy(context.Context, string, string) (VolumeRestoreApplyResult, error)
}

// PostgresVolumeRecoveryApplyCoordinator bridges the backup and deployment
// state machines without a split-brain window.
type PostgresVolumeRecoveryApplyCoordinator struct {
	Pool     *dbpg.Pool
	Restores *backup.PostgresVolumeRunStore
	Actions  *PostgresDeploymentActionStore
	Now      func() time.Time
}

func NewPostgresVolumeRecoveryApplyCoordinator(
	pool *dbpg.Pool,
	restores *backup.PostgresVolumeRunStore,
	actions *PostgresDeploymentActionStore,
) *PostgresVolumeRecoveryApplyCoordinator {
	return &PostgresVolumeRecoveryApplyCoordinator{
		Pool: pool, Restores: restores, Actions: actions,
		Now: func() time.Time { return time.Now().UTC() },
	}
}

func (c *PostgresVolumeRecoveryApplyCoordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *PostgresVolumeRecoveryApplyCoordinator) ApplyRestoreAndRedeploy(
	ctx context.Context,
	restoreID string,
	triggerKey string,
) (VolumeRestoreApplyResult, error) {
	restoreID = strings.TrimSpace(restoreID)
	triggerKey = strings.TrimSpace(triggerKey)
	if c == nil || c.Pool == nil || c.Restores == nil || c.Actions == nil {
		return VolumeRestoreApplyResult{}, errors.New("volume recovery apply coordinator is not configured")
	}
	if restoreID == "" || triggerKey == "" {
		return VolumeRestoreApplyResult{}, fmt.Errorf("%w: restore id and trigger key are required", backup.ErrVolumeRunInvalid)
	}

	var out VolumeRestoreApplyResult
	err := c.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		// The deployment outbox dedupe key is the transaction's replay anchor.
		// Check it before requiring staged restore state because a successful
		// first call has already moved the restore to applied.
		if err := replayDeploymentActionResult(ctx, tx, triggerKey, &out.DeploymentAction); err == nil {
			run, getErr := c.Restores.GetRestoreTx(ctx, tx, restoreID, false)
			if getErr != nil {
				return getErr
			}
			if run.State != backup.VolumeRestoreApplied {
				return fmt.Errorf("%w: replayed redeploy exists for a non-applied restore", backup.ErrVolumeRunConflict)
			}
			out.Restore = run
			out.Created = false
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		run, err := c.Restores.ApplyRestoreTx(ctx, tx, restoreID)
		if err != nil {
			return err
		}

		var activeDeploymentID *string
		err = tx.QueryRow(ctx, `
			SELECT active_deployment_id
			  FROM service_environment_state
			 WHERE service_id=$1 AND environment_id=$2
			 FOR UPDATE`,
			run.ServiceID, run.EnvironmentID,
		).Scan(&activeDeploymentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: restore apply requires an active deployment to redeploy", ErrDeploymentActionConflict)
		}
		if err != nil {
			return err
		}
		if activeDeploymentID == nil || strings.TrimSpace(*activeDeploymentID) == "" {
			return fmt.Errorf("%w: restore apply requires an active deployment to redeploy", ErrDeploymentActionConflict)
		}

		actionRequest := ImageReplayRequest{
			ServiceID:  run.ServiceID,
			SourceID:   strings.TrimSpace(*activeDeploymentID),
			Action:     deploy.ActionRedeployImage,
			TriggerKey: triggerKey,
			Now:        c.now(),
		}
		if err := c.Actions.commitImageReplayTx(ctx, tx, actionRequest, c.Actions.now(actionRequest.Now), &out.DeploymentAction); err != nil {
			return err
		}
		out.Restore = run
		out.Created = true
		return nil
	})
	return out, err
}

func volumeRestoreApplyHandler(coordinator VolumeRecoveryApply) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "atomic volume restore cutover is not configured", map[string]any{
				"capability": "volume-restore-apply-redeploy", "state": "unavailable",
			})
			return
		}
		body, ok := readBoundedJSONRequestBody(w, r, "volume restore apply accepts no fields")
		if !ok {
			return
		}
		var request struct{}
		if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "volume restore apply accepts no fields") {
			return
		}
		restoreID := strings.TrimSpace(r.PathValue("restore_id"))
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		triggerKey := "rest:volume-restore-apply:" + restoreID + ":" + key
		result, err := coordinator.ApplyRestoreAndRedeploy(r.Context(), restoreID, triggerKey)
		switch {
		case errors.Is(err, backup.ErrVolumeRunNotFound):
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume restore not found", map[string]any{"restore_id": restoreID})
			return
		case errors.Is(err, backup.ErrVolumeRunConflict),
			errors.Is(err, ErrDeploymentActionConflict),
			errors.Is(err, ErrDeploymentSnapshotAbsent):
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), nil)
			return
		case err != nil:
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "atomic volume restore cutover failed", nil)
			return
		}
		if !result.Created {
			w.Header().Set("Idempotent-Replay", "true")
		}
		WriteJSON(w, http.StatusAccepted, result)
	}
}

var _ VolumeRecoveryApply = (*PostgresVolumeRecoveryApplyCoordinator)(nil)
