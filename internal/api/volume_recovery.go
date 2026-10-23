// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
)

// VolumeRecoveryStore is the durable, transactional backup/restore control
// plane. Archive keys and destination credentials are intentionally absent.
type VolumeRecoveryStore interface {
	CreateBackupWork(context.Context, backup.CreateVolumeBackupRun) (backup.VolumeBackupWork, error)
	ListBackups(context.Context, string, string, string, string) ([]backup.VolumeBackupRun, error)
	GetBackup(context.Context, string) (backup.VolumeBackupRun, error)
	CreateRestoreWork(context.Context, backup.CreateVolumeRestoreRun) (backup.VolumeRestoreWork, error)
	GetRestore(context.Context, string) (backup.VolumeRestoreRun, error)
	ListRestores(context.Context, string) ([]backup.VolumeRestoreRun, error)
	ApplyRestore(context.Context, string) (backup.VolumeRestoreRun, error)
	CreatePolicy(context.Context, backup.CreateVolumeBackupPolicy) (backup.VolumeBackupPolicy, error)
	ListPolicies(context.Context, string) ([]backup.VolumeBackupPolicy, error)
	DeletePolicy(context.Context, string, string) error
}

type createVolumeBackupRequest struct {
	RetentionDays int `json:"retention_days,omitempty"`
}

type volumeBackupList struct {
	Data []backup.VolumeBackupRun `json:"data"`
}

type volumeBackupAccepted struct {
	Run   backup.VolumeBackupRun `json:"run"`
	JobID string                 `json:"job_id"`
}

type createVolumeRestoreRequest struct{}

type volumeRestoreAccepted struct {
	Run   backup.VolumeRestoreRun `json:"run"`
	JobID string                  `json:"job_id"`
}

type volumeRestoreList struct {
	Data []backup.VolumeRestoreRun `json:"data"`
}

func volumeBackupsHandler(store VolumeRecoveryStore, executorAvailable bool, idem idempotency.Store, clock func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "volume backup execution is not configured", map[string]any{
				"capability": "volume-backup-execution",
				"state":      "unavailable",
			})
			return
		}
		volumeID := strings.TrimSpace(r.PathValue("volume_id"))
		if volumeID == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "volume_id is required", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			runs, err := store.ListBackups(r.Context(), "", "", "", volumeID)
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume backup list failed", nil)
				return
			}
			WriteJSON(w, http.StatusOK, volumeBackupList{Data: runs})
		case http.MethodPost:
			if !executorAvailable {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.BackupOffhostMissing, "volume backup worker destination and key are not configured", map[string]any{
					"capability": "volume-backup-execution", "state": "unavailable",
				})
				return
			}
			createVolumeBackup(w, r, store, idem, clock, volumeID)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func createVolumeBackup(
	w http.ResponseWriter,
	r *http.Request,
	store VolumeRecoveryStore,
	idem idempotency.Store,
	clock func() time.Time,
	volumeID string,
) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid volume backup request")
	if !ok {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	var request createVolumeBackupRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid volume backup request") {
		return
	}
	if request.RetentionDays < 0 || request.RetentionDays > 3650 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "retention_days must be between 0 and 3650", map[string]any{"field": "retention_days"})
		return
	}
	var retention time.Time
	if request.RetentionDays > 0 {
		retention = clock().UTC().Add(time.Duration(request.RetentionDays) * 24 * time.Hour)
	}
	work, err := store.CreateBackupWork(r.Context(), backup.CreateVolumeBackupRun{
		VolumeID:       volumeID,
		TriggerKind:    "manual",
		RetentionUntil: retention,
	})
	if errors.Is(err, backup.ErrVolumeRunNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "ready volume not found", map[string]any{"volume_id": volumeID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume backup enqueue failed", nil)
		return
	}
	w.Header().Set("Location", v1alphaPrefix+"/volume-backups/"+work.Run.ID)
	WriteJSON(w, http.StatusAccepted, volumeBackupAccepted{Run: work.Run, JobID: work.Job.ID})
}

func volumeBackupResourceHandler(store VolumeRecoveryStore, executorAvailable bool, idem idempotency.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "volume recovery execution is not configured", map[string]any{
				"capability": "volume-recovery-execution",
				"state":      "unavailable",
			})
			return
		}
		backupID := strings.TrimSpace(r.PathValue("backup_id"))
		switch {
		case r.Method == http.MethodGet:
			run, err := store.GetBackup(r.Context(), backupID)
			if errors.Is(err, backup.ErrVolumeRunNotFound) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume backup not found", map[string]any{"backup_id": backupID})
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume backup lookup failed", nil)
				return
			}
			WriteJSON(w, http.StatusOK, run)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/restores"):
			if !executorAvailable {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.BackupOffhostMissing, "volume recovery worker destination and key are not configured", map[string]any{
					"capability": "volume-recovery-execution", "state": "unavailable",
				})
				return
			}
			createVolumeRestore(w, r, store, idem, backupID)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

type createVolumeBackupPolicyRequest struct {
	ScheduleKind  string `json:"schedule_kind"`
	RetentionDays int    `json:"retention_days"`
}

func volumeBackupPoliciesHandler(store VolumeRecoveryStore, executorAvailable bool, idem idempotency.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "volume backup policies are not configured", nil)
			return
		}
		volumeID := strings.TrimSpace(r.PathValue("volume_id"))
		policyID := strings.TrimSpace(r.PathValue("policy_id"))
		switch {
		case policyID == "" && r.Method == http.MethodGet:
			policies, err := store.ListPolicies(r.Context(), volumeID)
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume backup policy list failed", nil)
				return
			}
			WriteJSON(w, http.StatusOK, policies)
		case policyID == "" && r.Method == http.MethodPost:
			if !executorAvailable {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.BackupOffhostMissing, "volume backup worker destination and key are not configured", map[string]any{
					"capability": "volume-backup-policy-execution", "state": "unavailable",
				})
				return
			}
			body, ok := readBoundedJSONRequestBody(w, r, "invalid backup policy request")
			if !ok {
				return
			}
			reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
			if !ok {
				return
			}
			idempotentResponse := newIdempotencyResponseCapture(w)
			w = idempotentResponse
			defer idempotentResponse.Complete(r, idem, reservation.Reference())
			var request createVolumeBackupPolicyRequest
			if !decodeStrictJSONRequestBytes(w, r, body, &request, "invalid backup policy request") {
				return
			}
			if request.RetentionDays < 1 || request.RetentionDays > 3650 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid backup policy request", nil)
				return
			}
			policy, err := store.CreatePolicy(r.Context(), backup.CreateVolumeBackupPolicy{
				VolumeID: volumeID, ScheduleKind: request.ScheduleKind,
				RetentionSeconds: int64(request.RetentionDays) * 24 * 60 * 60,
			})
			if errors.Is(err, backup.ErrVolumeRunNotFound) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "ready volume not found", nil)
				return
			}
			if errors.Is(err, backup.ErrVolumeRunInvalid) {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "backup policy conflicts with existing schedule", nil)
				return
			}
			WriteJSON(w, http.StatusCreated, policy)
		case policyID != "" && r.Method == http.MethodDelete:
			reservation, ok := reserveIdempotencyOrAbort(w, r, idem, nil)
			if !ok {
				return
			}
			idempotentResponse := newIdempotencyResponseCapture(w)
			w = idempotentResponse
			defer idempotentResponse.Complete(r, idem, reservation.Reference())
			if err := store.DeletePolicy(r.Context(), volumeID, policyID); errors.Is(err, backup.ErrVolumeRunNotFound) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "backup policy not found", nil)
				return
			} else if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "backup policy delete failed", nil)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func createVolumeRestore(w http.ResponseWriter, r *http.Request, store VolumeRecoveryStore, idem idempotency.Store, backupID string) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid volume restore request")
	if !ok {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	var request struct{}
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "volume restore request accepts no fields") {
		return
	}
	work, err := store.CreateRestoreWork(r.Context(), backup.CreateVolumeRestoreRun{BackupID: backupID})
	if errors.Is(err, backup.ErrVolumeRunNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "successful volume backup not found", map[string]any{"backup_id": backupID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume restore enqueue failed", nil)
		return
	}
	w.Header().Set("Location", v1alphaPrefix+"/volume-restores/"+work.Run.ID)
	WriteJSON(w, http.StatusAccepted, volumeRestoreAccepted{Run: work.Run, JobID: work.Job.ID})
}

func volumeRestoresHandler(store VolumeRecoveryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "volume recovery execution is not configured", nil)
			return
		}
		volumeID := strings.TrimSpace(r.PathValue("volume_id"))
		runs, err := store.ListRestores(r.Context(), volumeID)
		if errors.Is(err, backup.ErrVolumeRunInvalid) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume restore list failed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, volumeRestoreList{Data: runs})
	}
}

func volumeRestoreResourceHandler(store VolumeRecoveryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "volume recovery execution is not configured", map[string]any{
				"capability": "volume-recovery-execution",
				"state":      "unavailable",
			})
			return
		}
		id := strings.TrimSpace(r.PathValue("restore_id"))
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "restore apply is unavailable until an atomic redeploy handoff is wired", map[string]any{
				"capability": "volume-restore-apply-redeploy",
				"state":      "unavailable",
				"restore_id": id,
			})
			return
		}
		run, err := store.GetRestore(r.Context(), id)
		if errors.Is(err, backup.ErrVolumeRunNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume restore not found", map[string]any{"restore_id": id})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume restore lookup failed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, run)
	}
}
