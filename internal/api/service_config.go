// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
)

type ServiceConfigStore interface {
	Get(context.Context, string, string, string) (serviceconfig.Config, error)
	Put(context.Context, serviceconfig.Config, int64) (serviceconfig.Config, error)
}

type ServiceConfigView struct {
	ProjectID             string    `json:"project_id"`
	ServiceID             string    `json:"service_id"`
	EnvironmentID         string    `json:"environment_id"`
	Version               int64     `json:"version"`
	Explicit              bool      `json:"explicit"`
	RootDirectory         string    `json:"root_directory"`
	Builder               string    `json:"builder"`
	DockerfilePath        string    `json:"dockerfile_path"`
	BuildCommand          string    `json:"build_command"`
	StartCommand          string    `json:"start_command"`
	PredeployCommand      string    `json:"predeploy_command"`
	TargetPort            int       `json:"target_port"`
	HealthKind            string    `json:"health_kind"`
	HealthPath            string    `json:"health_path"`
	HealthPort            int       `json:"health_port"`
	HealthTimeoutSeconds  int64     `json:"health_timeout_seconds"`
	HealthIntervalSeconds int64     `json:"health_interval_seconds"`
	RestartPolicy         string    `json:"restart_policy"`
	RestartRetries        int       `json:"restart_retries"`
	Replicas              int       `json:"replicas"`
	OverlapEnabled        bool      `json:"overlap_enabled"`
	DrainGraceSeconds     int64     `json:"drain_grace_seconds"`
	RuntimeKind           string    `json:"runtime_kind"`
	CronSchedule          string    `json:"cron_schedule"`
	CronTimeoutSeconds    int64     `json:"cron_timeout_seconds"`
	CronMaxAttempts       int       `json:"cron_max_attempts"`
	CronRetryDelaySeconds int64     `json:"cron_retry_delay_seconds"`
	CreatedAt             time.Time `json:"created_at,omitempty"`
	UpdatedAt             time.Time `json:"updated_at,omitempty"`
}

type PutServiceConfigRequest struct {
	ExpectedVersion       *int64 `json:"expected_version"`
	RootDirectory         string `json:"root_directory"`
	Builder               string `json:"builder"`
	DockerfilePath        string `json:"dockerfile_path"`
	BuildCommand          string `json:"build_command"`
	StartCommand          string `json:"start_command"`
	PredeployCommand      string `json:"predeploy_command"`
	TargetPort            int    `json:"target_port"`
	HealthKind            string `json:"health_kind"`
	HealthPath            string `json:"health_path"`
	HealthPort            int    `json:"health_port"`
	HealthTimeoutSeconds  int64  `json:"health_timeout_seconds"`
	HealthIntervalSeconds int64  `json:"health_interval_seconds"`
	RestartPolicy         string `json:"restart_policy"`
	RestartRetries        int    `json:"restart_retries"`
	Replicas              int    `json:"replicas"`
	OverlapEnabled        bool   `json:"overlap_enabled"`
	DrainGraceSeconds     int64  `json:"drain_grace_seconds"`
	RuntimeKind           string `json:"runtime_kind"`
	CronSchedule          string `json:"cron_schedule"`
	CronTimeoutSeconds    int64  `json:"cron_timeout_seconds"`
	CronMaxAttempts       int    `json:"cron_max_attempts"`
	CronRetryDelaySeconds int64  `json:"cron_retry_delay_seconds"`
}

const serviceConfigBodyLimitBytes = 32 << 10

func serviceConfigHandler(
	store ServiceConfigStore,
	idem idempotency.Store,
	emitter *auditEmitter,
	scopeResolver MutationScopeResolver,
	coordinators ...TenantServiceConfigCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serviceID := strings.TrimSpace(r.PathValue("service_id"))
		environmentID := strings.TrimSpace(r.PathValue("environment_id"))
		if principal, ok := productPrincipalFromContext(r.Context()); ok &&
			principal.Kind == productPrincipalHuman {
			var coordinator TenantServiceConfigCoordinator
			if len(coordinators) > 0 {
				coordinator = coordinators[0]
			}
			switch r.Method {
			case http.MethodGet:
				getTenantServiceConfig(w, r, coordinator, serviceID, environmentID)
			case http.MethodPut:
				putTenantServiceConfig(w, r, coordinator, serviceID, environmentID)
			default:
				w.Header().Set("Allow", "GET, PUT")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			}
			return
		}
		if store == nil || scopeResolver == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable service configuration is not configured", map[string]any{
				"capability": "durable-service-configuration",
				"state":      "unavailable",
			})
			return
		}
		scope, err := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ServiceID: serviceID, EnvironmentID: environmentID,
			RequireProject: true, RequireService: true, RequireEnvironment: true,
		})
		if err != nil {
			writeMutationScopeError(w, r, err)
			return
		}
		switch r.Method {
		case http.MethodGet:
			cfg, err := store.Get(r.Context(), scope.ProjectID, environmentID, serviceID)
			explicit := true
			if errors.Is(err, serviceconfig.ErrNotFound) {
				cfg = serviceconfig.Default(scope.ProjectID, serviceID, environmentID)
				explicit = false
			} else if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service config lookup failed", nil)
				return
			}
			view := serviceConfigView(cfg, explicit)
			w.Header().Set("ETag", strongVersionETag(view.Version))
			WriteJSON(w, http.StatusOK, view)
		case http.MethodPut:
			putServiceConfig(w, r, store, idem, emitter, scope.ProjectID, serviceID, environmentID)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func putServiceConfig(
	w http.ResponseWriter,
	r *http.Request,
	store ServiceConfigStore,
	idem idempotency.Store,
	emitter *auditEmitter,
	projectID, serviceID, environmentID string,
) {
	body, request, ok := readPutServiceConfigRequest(w, r)
	if !ok {
		return
	}
	ifMatch, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	if !expectedVersionMatchesIfMatch(w, r, *request.ExpectedVersion, ifMatch) {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	cfg := serviceConfigFromRequest(request, projectID, serviceID, environmentID)
	expectedVersion := *request.ExpectedVersion
	saved, err := store.Put(r.Context(), cfg, expectedVersion)
	if errors.Is(err, serviceconfig.ErrConflict) {
		writeIfMatchFailed(w, r, "service config version conflict", map[string]any{"expected_version": expectedVersion})
		return
	}
	if errors.Is(err, serviceconfig.ErrInvalid) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service config save failed", nil)
		return
	}
	if emitter != nil {
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "service_environment_config",
			ResourceID:   serviceID + ":" + environmentID,
			ProjectID:    projectID,
			Action:       "service.config.put",
			After: map[string]any{
				"version": saved.Version, "builder": saved.Builder,
				"target_port": saved.TargetPort, "health_kind": saved.HealthKind,
				"restart_policy": saved.RestartPolicy, "replicas": saved.Replicas,
				"runtime_kind": saved.RuntimeKind, "cron_schedule": saved.CronSchedule,
				"has_build_command":     saved.BuildCommand != "",
				"has_start_command":     saved.StartCommand != "",
				"has_predeploy_command": saved.PredeployCommand != "",
			},
		})
	}
	view := serviceConfigView(saved, true)
	w.Header().Set("ETag", strongVersionETag(view.Version))
	WriteJSON(w, http.StatusOK, view)
}

func getTenantServiceConfig(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantServiceConfigCoordinator,
	serviceID string,
	environmentID string,
) {
	if coordinator == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant service configuration is unavailable", nil)
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	view, err := coordinator.GetServiceConfig(r.Context(), TenantServiceConfigReadRequest{
		Principal:     principal,
		Session:       session,
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
	})
	if writeTenantServiceConfigError(w, r, err) {
		return
	}
	w.Header().Set("ETag", strongVersionETag(view.Version))
	WriteJSON(w, http.StatusOK, view)
}

func putTenantServiceConfig(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantServiceConfigCoordinator,
	serviceID string,
	environmentID string,
) {
	if coordinator == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant service configuration is unavailable", nil)
		return
	}
	body, request, ok := readPutServiceConfigRequest(w, r)
	if !ok {
		return
	}
	ifMatch, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	if !expectedVersionMatchesIfMatch(w, r, *request.ExpectedVersion, ifMatch) {
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	key, keyFailure := singletonIdempotencyKey(r.Header)
	if keyFailure == failurecodes.IdempotencyKeyRequired {
		writeRequestError(w, r, http.StatusBadRequest, keyFailure, "exactly one Idempotency-Key header is required", nil)
		return
	}
	if keyFailure == failurecodes.IdempotencyKeyInvalid {
		writeRequestError(w, r, http.StatusBadRequest, keyFailure, "invalid idempotency key", nil)
		return
	}
	result, err := coordinator.PutServiceConfig(r.Context(), TenantServiceConfigPutRequest{
		Principal:       principal,
		Session:         session,
		Config:          serviceConfigFromRequest(request, "", serviceID, environmentID),
		ExpectedVersion: *request.ExpectedVersion,
		Key:             key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     r.Method,
			Path:       tenantServiceConfigIdempotencyPath(serviceID, environmentID),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	if writeTenantServiceConfigError(w, r, err) {
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func readPutServiceConfigRequest(
	w http.ResponseWriter,
	r *http.Request,
) ([]byte, PutServiceConfigRequest, bool) {
	limited := http.MaxBytesReader(w, r.Body, serviceConfigBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "service config request body exceeds the configured limit", map[string]any{"limit": serviceConfigBodyLimitBytes})
			return nil, PutServiceConfigRequest{}, false
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return nil, PutServiceConfigRequest{}, false
	}
	if len(bytes.TrimSpace(body)) == 0 || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "service config request body must be one JSON object", nil)
		return nil, PutServiceConfigRequest{}, false
	}
	var request PutServiceConfigRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid service config request", nil)
		return nil, PutServiceConfigRequest{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "service config request body must contain exactly one JSON object", nil)
		return nil, PutServiceConfigRequest{}, false
	}
	if request.ExpectedVersion == nil || *request.ExpectedVersion < 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version is required and must be non-negative", map[string]any{"field": "expected_version"})
		return nil, PutServiceConfigRequest{}, false
	}
	return body, request, true
}

func serviceConfigFromRequest(
	request PutServiceConfigRequest,
	projectID string,
	serviceID string,
	environmentID string,
) serviceconfig.Config {
	return serviceconfig.Config{
		ProjectID: projectID, ServiceID: serviceID, EnvironmentID: environmentID,
		RootDirectory: request.RootDirectory, Builder: request.Builder,
		DockerfilePath: request.DockerfilePath, BuildCommand: request.BuildCommand,
		StartCommand: request.StartCommand, PredeployCommand: request.PredeployCommand,
		TargetPort: request.TargetPort, HealthKind: request.HealthKind,
		HealthPath: request.HealthPath, HealthPort: request.HealthPort,
		HealthTimeout:  time.Duration(request.HealthTimeoutSeconds) * time.Second,
		HealthInterval: time.Duration(request.HealthIntervalSeconds) * time.Second,
		RestartPolicy:  request.RestartPolicy, RestartRetries: request.RestartRetries,
		Replicas: request.Replicas, OverlapEnabled: request.OverlapEnabled,
		DrainGrace:  time.Duration(request.DrainGraceSeconds) * time.Second,
		RuntimeKind: request.RuntimeKind, CronSchedule: request.CronSchedule,
		CronTimeout:     time.Duration(request.CronTimeoutSeconds) * time.Second,
		CronMaxAttempts: request.CronMaxAttempts,
		CronRetryDelay:  time.Duration(request.CronRetryDelaySeconds) * time.Second,
	}
}

func writeTenantServiceConfigError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service or environment not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to access this service configuration", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
	case errors.Is(err, serviceconfig.ErrInvalid),
		errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
	default:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant service configuration is unavailable", nil)
	}
	return true
}

func serviceConfigView(cfg serviceconfig.Config, explicit bool) ServiceConfigView {
	return ServiceConfigView{
		ProjectID: cfg.ProjectID, ServiceID: cfg.ServiceID, EnvironmentID: cfg.EnvironmentID,
		Version: cfg.Version, Explicit: explicit, RootDirectory: cfg.RootDirectory,
		Builder: cfg.Builder, DockerfilePath: cfg.DockerfilePath,
		BuildCommand: cfg.BuildCommand, StartCommand: cfg.StartCommand,
		PredeployCommand: cfg.PredeployCommand, TargetPort: cfg.TargetPort,
		HealthKind: cfg.HealthKind, HealthPath: cfg.HealthPath, HealthPort: cfg.HealthPort,
		HealthTimeoutSeconds:  int64(cfg.HealthTimeout / time.Second),
		HealthIntervalSeconds: int64(cfg.HealthInterval / time.Second),
		RestartPolicy:         cfg.RestartPolicy, RestartRetries: cfg.RestartRetries,
		Replicas: cfg.Replicas, OverlapEnabled: cfg.OverlapEnabled,
		DrainGraceSeconds: int64(cfg.DrainGrace / time.Second),
		RuntimeKind:       cfg.RuntimeKind, CronSchedule: cfg.CronSchedule,
		CronTimeoutSeconds:    int64(cfg.CronTimeout / time.Second),
		CronMaxAttempts:       cfg.CronMaxAttempts,
		CronRetryDelaySeconds: int64(cfg.CronRetryDelay / time.Second),
		CreatedAt:             cfg.CreatedAt, UpdatedAt: cfg.UpdatedAt,
	}
}
