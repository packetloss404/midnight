package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/failurecodes"
)

func backupRestoreStatusHandler(destinationURI string, executorAvailable ...bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}

		available := len(executorAvailable) > 0 && executorAvailable[0]
		WriteJSON(w, http.StatusOK, backup.BuildRestoreAPIStatus(backup.RestoreAPIStatusInput{
			DestinationURI:           destinationURI,
			RestoreExecutorAvailable: available,
		}))
	}
}

type createBackupRunRequest struct {
	ID                    string `json:"id,omitempty"`
	OperatorPassphrase    string `json:"operator_passphrase,omitempty"`
	DatabaseDumpBytes     int64  `json:"database_dump_bytes,omitempty"`
	AppDataBytes          int64  `json:"app_data_bytes,omitempty"`
	RegistryMetadataBytes int64  `json:"registry_metadata_bytes,omitempty"`
	ConfigSnapshotBytes   int64  `json:"config_snapshot_bytes,omitempty"`
	SupportReferenceBytes int64  `json:"support_reference_bytes,omitempty"`
	SupportReferenceCount int    `json:"support_reference_count,omitempty"`
}

type backupRunResource struct {
	ID       string                `json:"id"`
	Manifest backup.ExportManifest `json:"manifest"`
	Object   backup.Object         `json:"object"`
}

type backupRunListResponse struct {
	Data []backupRunResource `json:"data"`
}

func backupRunsHandler(destinationURI string, clock func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listBackupRuns(w, r, destinationURI)
		case http.MethodPost:
			createBackupRun(w, r, destinationURI, clock)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func createBackupRun(w http.ResponseWriter, r *http.Request, destinationURI string, clock func() time.Time) {
	if !experimentalEnabled() {
		writeRequestError(
			w,
			r,
			http.StatusNotImplemented,
			failurecodes.NotImplemented,
			"backup execution is unavailable; this build can only create evidence manifests in experimental mode",
			map[string]any{
				"capability": "backup-execution",
				"state":      "unavailable",
				"recovery":   "configure an external backup workflow; MIDNIGHT_EXPERIMENTAL=1 exposes manifest evidence only and does not copy application data",
			},
		)
		return
	}
	var request createBackupRunRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid backup run request") {
		return
	}
	createdAt := clock().UTC()
	id := strings.TrimSpace(request.ID)
	if id == "" {
		id = "backup-" + createdAt.Format("20060102T150405Z")
	}
	if err := validateBackupArtifactID(id); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	manifest, err := backup.BuildExportManifest(backup.ExportManifestInput{
		ID:                    id,
		CreatedAt:             createdAt,
		DatabaseDumpBytes:     request.DatabaseDumpBytes,
		AppDataBytes:          request.AppDataBytes,
		RegistryMetadataBytes: request.RegistryMetadataBytes,
		ConfigSnapshotBytes:   request.ConfigSnapshotBytes,
		SupportReferenceBytes: request.SupportReferenceBytes,
		SupportReferenceCount: request.SupportReferenceCount,
		OperatorPassphrase:    request.OperatorPassphrase,
	})
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	data, err := backup.MarshalExportManifestJSON(manifest)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "backup manifest render failed", nil)
		return
	}
	destination, err := backup.OpenDestination(r.Context(), destinationURI)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.BackupOffhostMissing, err.Error(), nil)
		return
	}
	object, err := destination.Put(r.Context(), backupRunManifestKey(id), bytes.NewReader(data))
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.BackupDestinationWriteFailed, "backup manifest write failed", backupDestinationFailureDetails(id, err))
		return
	}
	w.Header().Set("Location", v1alphaPrefix+"/backup/runs/"+id)
	WriteJSON(w, http.StatusCreated, backupRunResource{ID: id, Manifest: manifest, Object: object})
}

func listBackupRuns(w http.ResponseWriter, r *http.Request, destinationURI string) {
	destination, err := backup.OpenDestination(r.Context(), destinationURI)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.BackupOffhostMissing, err.Error(), nil)
		return
	}
	objects, err := destination.List(r.Context(), "runs/")
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "backup run list failed", nil)
		return
	}
	runs := make([]backupRunResource, 0, len(objects))
	for _, object := range objects {
		if !strings.HasSuffix(object.Key, "/manifest.json") {
			continue
		}
		body, gotObject, err := destination.Get(r.Context(), object.Key)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "backup manifest read failed", map[string]any{"object_key": object.Key})
			return
		}
		var manifest backup.ExportManifest
		decodeErr := json.NewDecoder(body).Decode(&manifest)
		closeErr := body.Close()
		if decodeErr != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "backup manifest decode failed", map[string]any{"object_key": object.Key})
			return
		}
		if closeErr != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "backup manifest close failed", map[string]any{"object_key": object.Key})
			return
		}
		if err := manifest.Validate(); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "backup manifest validation failed", map[string]any{"object_key": object.Key})
			return
		}
		runs = append(runs, backupRunResource{ID: manifest.ID, Manifest: manifest, Object: gotObject})
	}
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].Manifest.CreatedAt.After(runs[j].Manifest.CreatedAt)
	})
	WriteJSON(w, http.StatusOK, backupRunListResponse{Data: runs})
}

type createRestorePlanRequest struct {
	BackupID      string `json:"backup_id"`
	Operator      string `json:"operator"`
	RollbackOwner string `json:"rollback_owner"`
	Notes         string `json:"notes,omitempty"`
}

type restorePlanResource struct {
	BackupID string             `json:"backup_id"`
	Plan     backup.RestorePlan `json:"plan"`
	Object   backup.Object      `json:"object"`
}

type restorePlanListResponse struct {
	Data []restorePlanResource `json:"data"`
}

func backupRestorePlansHandler(destinationURI string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listRestorePlans(w, r, destinationURI)
		case http.MethodPost:
			createRestorePlan(w, r, destinationURI)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func createRestorePlan(w http.ResponseWriter, r *http.Request, destinationURI string) {
	var request createRestorePlanRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid restore plan request") {
		return
	}
	plan, err := backup.GenerateRestorePlan(backup.RestorePlanInput{
		BackupID:      request.BackupID,
		Operator:      request.Operator,
		RollbackOwner: request.RollbackOwner,
		Notes:         request.Notes,
	})
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err := validateBackupArtifactID(plan.BackupID); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	data, err := backup.MarshalRestorePlan(plan)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.RestorePlanSerializationFailed, "restore plan render failed", nil)
		return
	}
	destination, err := backup.OpenDestination(r.Context(), destinationURI)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.BackupOffhostMissing, err.Error(), nil)
		return
	}
	object, err := destination.Put(r.Context(), restorePlanKey(plan.BackupID), bytes.NewReader(data))
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.BackupDestinationWriteFailed, "restore plan write failed", backupDestinationFailureDetails(plan.BackupID, err))
		return
	}
	w.Header().Set("Location", v1alphaPrefix+"/backup/restore/plans/"+plan.BackupID)
	WriteJSON(w, http.StatusCreated, restorePlanResource{BackupID: plan.BackupID, Plan: plan, Object: object})
}

func listRestorePlans(w http.ResponseWriter, r *http.Request, destinationURI string) {
	destination, err := backup.OpenDestination(r.Context(), destinationURI)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.BackupOffhostMissing, err.Error(), nil)
		return
	}
	objects, err := destination.List(r.Context(), "restore-plans/")
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "restore plan list failed", nil)
		return
	}
	plans := make([]restorePlanResource, 0, len(objects))
	for _, object := range objects {
		if !strings.HasSuffix(object.Key, ".json") {
			continue
		}
		body, gotObject, err := destination.Get(r.Context(), object.Key)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "restore plan read failed", map[string]any{"object_key": object.Key})
			return
		}
		data, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "restore plan read failed", map[string]any{"object_key": object.Key})
			return
		}
		if closeErr != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "restore plan close failed", map[string]any{"object_key": object.Key})
			return
		}
		plan, err := backup.UnmarshalRestorePlan(data)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.RestorePlanSerializationFailed, "restore plan decode failed", map[string]any{"object_key": object.Key})
			return
		}
		plans = append(plans, restorePlanResource{BackupID: plan.BackupID, Plan: plan, Object: gotObject})
	}
	sort.SliceStable(plans, func(i, j int) bool { return plans[i].BackupID < plans[j].BackupID })
	WriteJSON(w, http.StatusOK, restorePlanListResponse{Data: plans})
}

func backupRunManifestKey(id string) string {
	return "runs/" + id + "/manifest.json"
}

func restorePlanKey(backupID string) string {
	return "restore-plans/" + backupID + ".json"
}

func validateBackupArtifactID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("backup id is required")
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("backup id %q must be a single safe path segment", id)
	}
	return nil
}

func backupDestinationFailureDetails(backupID string, err error) map[string]any {
	return map[string]any{
		"backup_id":      backupID,
		"reason_code":    failurecodes.BackupDestinationWriteFailed,
		"recovery_hint":  failurecodes.RecoveryHint(failurecodes.BackupDestinationWriteFailed),
		"operator_error": err.Error(),
	}
}
