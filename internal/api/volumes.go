// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/volume"
)

const resourceCollectionVolumes = "/volumes"

// VolumeView is the JSON shape returned by the REST API. It is a thin
// projection of volume.Volume that omits soft-delete bookkeeping the
// public surface does not need to expose.
type VolumeView struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	EnvironmentID   string    `json:"environment_id"`
	ServiceID       string    `json:"service_id"`
	Name            string    `json:"name"`
	SizeBytes       int64     `json:"size_bytes"`
	Status          string    `json:"status"`
	MountPath       string    `json:"mount_path,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ResourceVersion int64     `json:"resource_version"`
}

// CreateVolumeRequest is the JSON body accepted by POST /api/v1alpha/volumes.
// MountPath is authoritative durable intent on the volume row. Deploy workers
// load it by service/environment so queue messages never carry mutable paths.
type CreateVolumeRequest struct {
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ServiceID     string `json:"service_id"`
	Name          string `json:"name"`
	SizeBytes     int64  `json:"size_bytes"`
	MountPath     string `json:"mount_path"`
}

// ResizeVolumeRequest is the JSON body accepted by PATCH /api/v1alpha/volumes/{id}.
type ResizeVolumeRequest struct {
	SizeBytes int64 `json:"size_bytes"`
}

// VolumeMountStore tracks the per-volume mount path so the REST surface
// can echo back what the create request supplied. The Postgres-backed
// store records the mount on volume_attachments as soon as a deploy is
// scheduled; the in-memory variant keeps the mount on a sidecar map so
// API round-trips remain self-consistent.
type VolumeMountStore interface {
	SetMountPath(ctx context.Context, volumeID, mountPath string) error
	GetMountPath(ctx context.Context, volumeID string) (string, error)
}

// volumeHandler dispatches the /volumes collection routes.
func volumeHandler(store volume.Store, mounts VolumeMountStore, idem idempotency.Store, emitter *auditEmitter, scopeResolvers ...MutationScopeResolver) http.HandlerFunc {
	scopeResolver := firstMutationScopeResolver(scopeResolvers)
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "volumes are not configured on this server", nil)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		if path == resourceCollectionVolumes {
			switch r.Method {
			case http.MethodPost:
				createVolume(w, r, store, mounts, idem, emitter, scopeResolver)
				return
			case http.MethodGet:
				listVolumes(w, r, store, mounts)
				return
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
		}
		id := strings.TrimPrefix(path, resourceCollectionVolumes+"/")
		if id == "" || strings.Contains(id, "/") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume not found", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			getVolume(w, r, store, mounts, id)
		case http.MethodPatch:
			resizeVolume(w, r, store, mounts, idem, emitter, id, scopeResolver)
		case http.MethodDelete:
			deleteVolume(w, r, store, idem, emitter, id, scopeResolver)
		default:
			w.Header().Set("Allow", "GET, PATCH, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"volume_id": id})
		}
	}
}

func createVolume(w http.ResponseWriter, r *http.Request, store volume.Store, mounts VolumeMountStore, idem idempotency.Store, emitter *auditEmitter, scopeResolvers ...MutationScopeResolver) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid volume request body")
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
	var req CreateVolumeRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &req, "invalid volume request body") {
		return
	}
	if req.ServiceID == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "service_id is required", map[string]any{"field": "service_id"})
		return
	}
	if req.EnvironmentID == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "environment_id is required", map[string]any{"field": "environment_id"})
		return
	}
	if req.ProjectID == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", map[string]any{"field": "project_id"})
		return
	}
	if req.Name == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "name is required", map[string]any{"field": "name"})
		return
	}
	if req.SizeBytes < 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "size_bytes must be non-negative", map[string]any{"field": "size_bytes"})
		return
	}
	if req.MountPath == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "mount_path is required", map[string]any{"field": "mount_path"})
		return
	}
	if !strings.HasPrefix(req.MountPath, "/") {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "mount_path must be absolute", map[string]any{"field": "mount_path"})
		return
	}
	if scopeResolver := firstMutationScopeResolver(scopeResolvers); scopeResolver != nil {
		resolved, err := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:          req.ProjectID,
			ServiceID:          req.ServiceID,
			EnvironmentID:      req.EnvironmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			writeMutationScopeError(w, r, err)
			return
		}
		req.ProjectID = resolved.ProjectID
	}

	vol, err := store.Create(r.Context(), volume.VolumeCreateInput{
		ProjectID:     req.ProjectID,
		EnvironmentID: req.EnvironmentID,
		ServiceID:     req.ServiceID,
		Name:          req.Name,
		SizeBytes:     req.SizeBytes,
		MountPath:     req.MountPath,
	})
	if errors.Is(err, volume.ErrVolumeConflict) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), map[string]any{"service_id": req.ServiceID, "environment_id": req.EnvironmentID, "name": req.Name})
		return
	}
	if errors.Is(err, volume.ErrVolumeInvalid) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume create failed", nil)
		return
	}

	if mounts != nil {
		// Compatibility mirror for memory-mode callers. Production reads the
		// durable Volume.MountPath and does not depend on this sidecar.
		_ = mounts.SetMountPath(r.Context(), vol.ID, req.MountPath)
	}

	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "volume",
			ResourceID:   vol.ID,
			ProjectID:    vol.ProjectID,
			Action:       "volume.create",
			After: map[string]any{
				"name":           vol.Name,
				"service_id":     vol.ServiceID,
				"environment_id": vol.EnvironmentID,
				"size_bytes":     vol.SizeBytes,
				"mount_path":     req.MountPath,
			},
		})
	}
	w.Header().Set("Location", v1alphaPrefix+resourceCollectionVolumes+"/"+vol.ID)
	WriteJSON(w, http.StatusCreated, viewFromVolume(vol, req.MountPath))
}

func listVolumes(w http.ResponseWriter, r *http.Request, store volume.Store, mounts VolumeMountStore) {
	q := r.URL.Query()
	filter := volume.ListFilter{
		ProjectID:     q.Get("project_id"),
		ServiceID:     q.Get("service_id"),
		EnvironmentID: q.Get("environment_id"),
	}
	volumes, err := store.List(r.Context(), filter)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume list failed", nil)
		return
	}
	out := make([]VolumeView, 0, len(volumes))
	for _, v := range volumes {
		mountPath := v.MountPath
		if mountPath == "" && mounts != nil {
			mp, _ := mounts.GetMountPath(r.Context(), v.ID)
			mountPath = mp
		}
		out = append(out, viewFromVolume(v, mountPath))
	}
	WriteJSON(w, http.StatusOK, out)
}

func getVolume(w http.ResponseWriter, r *http.Request, store volume.Store, mounts VolumeMountStore, id string) {
	vol, err := store.Get(r.Context(), id)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume not found", map[string]any{"volume_id": id})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume lookup failed", map[string]any{"volume_id": id})
		return
	}
	mountPath := vol.MountPath
	if mountPath == "" && mounts != nil {
		mp, _ := mounts.GetMountPath(r.Context(), vol.ID)
		mountPath = mp
	}
	WriteJSON(w, http.StatusOK, viewFromVolume(vol, mountPath))
}

func resizeVolume(w http.ResponseWriter, r *http.Request, store volume.Store, mounts VolumeMountStore, idem idempotency.Store, emitter *auditEmitter, id string, scopeResolvers ...MutationScopeResolver) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid resize request body")
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
	var req ResizeVolumeRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &req, "invalid resize request body") {
		return
	}
	if req.SizeBytes <= 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "size_bytes must be positive", map[string]any{"field": "size_bytes"})
		return
	}
	before, err := store.Get(r.Context(), id)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume not found", map[string]any{"volume_id": id})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume lookup failed", map[string]any{"volume_id": id})
		return
	}
	if scopeResolver := firstMutationScopeResolver(scopeResolvers); scopeResolver != nil {
		_, err := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:          before.ProjectID,
			ServiceID:          before.ServiceID,
			EnvironmentID:      before.EnvironmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			writeMutationScopeError(w, r, err)
			return
		}
	}
	updated, err := store.Resize(r.Context(), id, req.SizeBytes)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume not found", map[string]any{"volume_id": id})
		return
	}
	if errors.Is(err, volume.ErrVolumeResizeDownUnsupported) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.VolumeResizeDownUnsupported, "volume resize down is not supported", map[string]any{
			"volume_id":      id,
			"current_size":   before.SizeBytes,
			"requested_size": req.SizeBytes,
		})
		return
	}
	if errors.Is(err, volume.ErrVolumeInvalid) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume resize failed", map[string]any{"volume_id": id})
		return
	}

	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "volume",
			ResourceID:   id,
			ProjectID:    updated.ProjectID,
			Action:       "volume.resize",
			Before:       map[string]any{"size_bytes": before.SizeBytes},
			After:        map[string]any{"size_bytes": updated.SizeBytes},
		})
	}
	mountPath := updated.MountPath
	if mountPath == "" && mounts != nil {
		mountPath, _ = mounts.GetMountPath(r.Context(), updated.ID)
	}
	WriteJSON(w, http.StatusOK, viewFromVolume(updated, mountPath))
}

func deleteVolume(w http.ResponseWriter, r *http.Request, store volume.Store, idem idempotency.Store, emitter *auditEmitter, id string, scopeResolvers ...MutationScopeResolver) {
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, nil)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	before, err := store.Get(r.Context(), id)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume not found", map[string]any{"volume_id": id})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume lookup failed", map[string]any{"volume_id": id})
		return
	}
	if scopeResolver := firstMutationScopeResolver(scopeResolvers); scopeResolver != nil {
		_, err := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:          before.ProjectID,
			ServiceID:          before.ServiceID,
			EnvironmentID:      before.EnvironmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			writeMutationScopeError(w, r, err)
			return
		}
	}
	if err := store.Delete(r.Context(), id); err != nil {
		if errors.Is(err, volume.ErrVolumeAttached) {
			writeRequestError(w, r, http.StatusConflict, failurecodes.VolumeAttached, "volume has active attachment", map[string]any{"volume_id": id})
			return
		}
		if errors.Is(err, volume.ErrVolumeNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "volume not found", map[string]any{"volume_id": id})
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "volume delete failed", map[string]any{"volume_id": id})
		return
	}
	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "volume",
			ResourceID:   id,
			ProjectID:    before.ProjectID,
			Action:       "volume.delete",
			Before:       map[string]any{"status": string(before.Status)},
			After:        map[string]any{"status": string(volume.StatusSoftDeleted)},
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func viewFromVolume(v volume.Volume, mountPath string) VolumeView {
	return VolumeView{
		ID:              v.ID,
		ProjectID:       v.ProjectID,
		EnvironmentID:   v.EnvironmentID,
		ServiceID:       v.ServiceID,
		Name:            v.Name,
		SizeBytes:       v.SizeBytes,
		Status:          string(v.Status),
		MountPath:       mountPath,
		CreatedAt:       v.CreatedAt,
		UpdatedAt:       v.UpdatedAt,
		ResourceVersion: v.ResourceVersion,
	}
}
