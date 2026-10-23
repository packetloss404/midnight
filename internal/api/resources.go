// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	resourceCollectionProjects  = "/projects"
	resourceCollectionServices  = "/services"
	serviceCreateBodyLimitBytes = 16 << 10
)

var serviceSlugPattern = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)

// CreateProjectRequest is the JSON body accepted by POST /api/v1alpha/projects.
type CreateProjectRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// CreateServiceRequest is the JSON body accepted by POST /api/v1alpha/services.
type CreateServiceRequest struct {
	ProjectID   string `json:"project_id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type serviceCreateValidationError struct {
	field   string
	message string
}

func (e *serviceCreateValidationError) Error() string {
	return e.message
}

func (e *serviceCreateValidationError) Unwrap() error {
	return ErrResourceInvalid
}

// projectsHandler dispatches the projects collection routes.
func projectsHandler(store ProjectStore, environments EnvironmentStore, idem idempotency.Store, emitter *auditEmitter, coordinators ...TenantMutationCoordinator) http.HandlerFunc {
	var coordinator TenantMutationCoordinator
	if len(coordinators) > 0 {
		coordinator = coordinators[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		if path == resourceCollectionProjects {
			switch r.Method {
			case http.MethodPost:
				createProject(w, r, store, environments, idem, emitter, coordinator)
				return
			case http.MethodGet:
				listProjects(w, r, store)
				return
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
		}
		id := strings.TrimPrefix(path, resourceCollectionProjects+"/")
		if id == "" || strings.Contains(id, "/") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"project_id": id})
			return
		}
		project, err := store.GetProject(r.Context(), orailtypes.ID(id))
		if errors.Is(err, ErrResourceNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", map[string]any{"project_id": id})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "project lookup failed", map[string]any{"project_id": id})
			return
		}
		WriteJSON(w, http.StatusOK, project)
	}
}

// servicesHandler dispatches the services collection routes.
func servicesHandler(store ServiceStore, idem idempotency.Store, operationStore OperationStore, queueStore queue.Store, deploymentStore DeploymentStore, durableIntake DurableDeployIntake, emitter *auditEmitter, clock func() time.Time, coordinator TenantMutationCoordinator, tenantDeployCoordinator TenantDeployIntakeCoordinator, tenantUpCoordinator TenantUpIntakeCoordinator, sourceArtifacts TenantUpArtifactStore, routeStore router.DomainWorkflowStore, cursorCodec CursorCodec, previewLifecycle GitHubPreviewLifecycle, scopeResolvers ...MutationScopeResolver) http.HandlerFunc {
	scopeResolver := firstMutationScopeResolver(scopeResolvers)
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		if path == resourceCollectionServices {
			switch r.Method {
			case http.MethodPost:
				createService(w, r, store, idem, emitter, coordinator)
				return
			case http.MethodGet:
				listServices(w, r, store)
				return
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
		}
		rest := strings.TrimPrefix(path, resourceCollectionServices+"/")
		if serviceID, ok := parseServiceUpPath(rest); ok {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"service_id": serviceID})
				return
			}
			if principal, human := productPrincipalFromContext(r.Context()); human && principal.Kind == productPrincipalHuman {
				createTenantServiceUp(w, r, serviceID, tenantUpCoordinator, sourceArtifacts)
				return
			}
			createServiceUp(w, r, serviceID, store, operationStore, queueStore, deploymentStore, durableIntake, idem, emitter, clock, scopeResolver)
			return
		}
		if serviceID, ok := parseServiceDeploymentsCollectionPath(rest); ok {
			if r.Method != http.MethodPost {
				if r.Method == http.MethodGet {
					serviceDeploymentsHandler(store, operationStore, deploymentStore, routeStore, cursorCodec)(w, r, serviceID, "", "")
					return
				}
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"service_id": serviceID})
				return
			}
			if principal, human := productPrincipalFromContext(r.Context()); human && principal.Kind == productPrincipalHuman {
				createTenantImageDeploy(w, r, serviceID, tenantDeployCoordinator)
				return
			}
			createDeployIntake(w, r, serviceID, store, operationStore, queueStore, deploymentStore, durableIntake, idem, emitter, clock, routeStore, scopeResolver)
			return
		}
		if serviceID, ok := parseServicePreviewEnvironmentsPath(rest); ok {
			servicePreviewEnvironmentsHandler(store, previewLifecycle)(w, r, serviceID)
			return
		}
		if serviceID, action, deploymentID, ok := parseServiceDeploymentResourcePath(rest); ok {
			serviceDeploymentsHandler(store, operationStore, deploymentStore, routeStore, cursorCodec)(w, r, serviceID, action, deploymentID)
			return
		}
		id := rest
		if id == "" || strings.Contains(id, "/") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", nil)
			return
		}
		if r.Method != http.MethodGet {
			// DELETE on this path is served by serviceDeleteHandler, which the
			// router dispatches before this handler ever sees the request.
			w.Header().Set("Allow", "GET, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"service_id": id})
			return
		}
		service, err := store.GetService(r.Context(), orailtypes.ID(id))
		if errors.Is(err, ErrResourceNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", map[string]any{"service_id": id})
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service lookup failed", map[string]any{"service_id": id})
			return
		}
		w.Header().Set("ETag", strongVersionETag(service.ResourceVersion))
		WriteJSON(w, http.StatusOK, service)
	}
}

func parseServiceDeploymentsCollectionPath(rest string) (orailtypes.ID, bool) {
	serviceID, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "deployments" || serviceID == "" {
		return "", false
	}
	return orailtypes.ID(serviceID), true
}

func parseServicePreviewEnvironmentsPath(rest string) (orailtypes.ID, bool) {
	serviceID, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "preview-environments" || serviceID == "" {
		return "", false
	}
	return orailtypes.ID(serviceID), true
}

// parseServiceUpPath identifies `POST /services/{id}/up` requests where the
// body is a gzipped tarball of the operator's working directory.
func parseServiceUpPath(rest string) (orailtypes.ID, bool) {
	serviceID, suffix, ok := strings.Cut(rest, "/")
	if !ok || suffix != "up" || serviceID == "" {
		return "", false
	}
	return orailtypes.ID(serviceID), true
}

func parseServiceDeploymentResourcePath(rest string) (orailtypes.ID, string, string, bool) {
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "deployments" || parts[2] == "" {
		return "", "", "", false
	}
	if parts[2] == "latest-active" {
		return orailtypes.ID(parts[0]), "latest-active", "", true
	}
	return orailtypes.ID(parts[0]), "get", parts[2], true
}

func createProject(w http.ResponseWriter, r *http.Request, store ProjectStore, environments EnvironmentStore, idem idempotency.Store, emitter *auditEmitter, coordinators ...TenantMutationCoordinator) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid project request body")
	if !ok {
		return
	}

	var request CreateProjectRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid project request body") {
		return
	}
	if request.Slug == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project slug is required", map[string]any{"field": "slug"})
		return
	}
	if request.DisplayName == "" {
		request.DisplayName = request.Slug
	}

	principal, principalOK := productPrincipalFromContext(r.Context())
	if principalOK && principal.Kind == productPrincipalHuman {
		var coordinator TenantMutationCoordinator
		if len(coordinators) > 0 {
			coordinator = coordinators[0]
		}
		createTenantProject(w, r, coordinator, request, body)
		return
	}

	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	response := newIdempotencyResponseCapture(w)
	w = response
	defer response.Complete(r, idem, reservation.Reference())

	projectInput := Project{
		WorkspaceID: request.WorkspaceID,
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
	}
	project, err := createProjectWithDefaultEnvironment(r.Context(), store, environments, projectInput)
	if errors.Is(err, errDefaultProductionEnvironmentCreate) {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "default production environment create failed", nil)
		return
	}
	if errors.Is(err, ErrResourceConflict) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), map[string]any{"slug": request.Slug})
		return
	}
	if errors.Is(err, ErrResourceInvalid) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "project create failed", nil)
		return
	}
	emitter.emit(r.Context(), audit.Row{
		ResourceKind: "project",
		ResourceID:   string(project.ID),
		ProjectID:    string(project.ID),
		Action:       "project.create",
		After: map[string]any{
			"slug":         project.Slug,
			"display_name": project.DisplayName,
		},
	})
	w.Header().Set("Location", v1alphaPrefix+resourceCollectionProjects+"/"+string(project.ID))
	WriteJSON(w, http.StatusCreated, project)
}

func createTenantProject(w http.ResponseWriter, r *http.Request, coordinator TenantMutationCoordinator, request CreateProjectRequest, body []byte) {
	if coordinator == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant project creation is unavailable", nil)
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK {
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	if strings.TrimSpace(request.WorkspaceID) == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "workspace_id is required", map[string]any{"field": "workspace_id"})
		return
	}
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	if err := idempotency.ValidateKey(key); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return
	}
	result, err := coordinator.CreateProject(r.Context(), TenantProjectCreateRequest{
		Principal: principal,
		Session:   session,
		Project: Project{
			WorkspaceID: request.WorkspaceID,
			Slug:        request.Slug,
			DisplayName: request.DisplayName,
		},
		Key: key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     r.Method,
			Path:       idempotencyRouteTemplate(r),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
		return
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "workspace not found", nil)
		return
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to create projects in this workspace", nil)
		return
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
		return
	case errors.Is(err, ErrResourceConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), map[string]any{"slug": request.Slug})
		return
	case errors.Is(err, ErrResourceInvalid), errors.Is(err, idempotency.ErrInvalidKey):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant project creation is unavailable", nil)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func createService(w http.ResponseWriter, r *http.Request, store ServiceStore, idem idempotency.Store, emitter *auditEmitter, coordinators ...TenantMutationCoordinator) {
	limited := http.MaxBytesReader(w, r.Body, serviceCreateBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "service request body exceeds the configured limit", map[string]any{"limit": serviceCreateBodyLimitBytes})
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return
	}

	var request CreateServiceRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &request); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid service request body", map[string]any{"reason": "json"})
			return
		}
	}
	principal, principalOK := productPrincipalFromContext(r.Context())
	request = normalizeCreateServiceRequest(request)
	if validationErr := validateServiceCreateFields(Service{
		ProjectID:   orailtypes.ID(request.ProjectID),
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
	}, principalOK && principal.Kind == productPrincipalHuman); validationErr != nil {
		details := map[string]any{}
		var fieldErr *serviceCreateValidationError
		if errors.As(validationErr, &fieldErr) {
			details["field"] = fieldErr.field
		}
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, validationErr.Error(), details)
		return
	}

	if principalOK && principal.Kind == productPrincipalHuman {
		var coordinator TenantMutationCoordinator
		if len(coordinators) > 0 {
			coordinator = coordinators[0]
		}
		createTenantService(w, r, coordinator, request, body)
		return
	}

	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	response := newIdempotencyResponseCapture(w)
	w = response
	defer response.Complete(r, idem, reservation.Reference())

	service, err := store.CreateService(r.Context(), Service{
		ProjectID:   orailtypes.ID(request.ProjectID),
		Slug:        request.Slug,
		DisplayName: request.DisplayName,
	})
	if errors.Is(err, ErrResourceConflict) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), map[string]any{"slug": request.Slug})
		return
	}
	if errors.Is(err, ErrResourceInvalid) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service create failed", nil)
		return
	}

	emitter.emit(r.Context(), audit.Row{
		ResourceKind: "service",
		ResourceID:   string(service.ID),
		ProjectID:    string(service.ProjectID),
		Action:       "service.create",
		After: map[string]any{
			"slug":         service.Slug,
			"display_name": service.DisplayName,
			"project_id":   string(service.ProjectID),
		},
	})
	w.Header().Set("Location", v1alphaPrefix+resourceCollectionServices+"/"+string(service.ID))
	WriteJSON(w, http.StatusCreated, service)
}

func normalizeCreateServiceRequest(request CreateServiceRequest) CreateServiceRequest {
	if strings.TrimSpace(request.DisplayName) == "" {
		request.DisplayName = request.Slug
	}
	return request
}

func validateServiceCreateFields(service Service, requireProjectID bool) error {
	if requireProjectID && strings.TrimSpace(string(service.ProjectID)) == "" {
		return &serviceCreateValidationError{
			field:   "project_id",
			message: "project_id is required",
		}
	}
	if !serviceSlugPattern.MatchString(service.Slug) {
		return &serviceCreateValidationError{
			field:   "slug",
			message: "service slug must start with a lowercase letter, contain only lowercase letters, numbers, or hyphens, end with a letter or number, and be at most 63 characters",
		}
	}
	if utf8.RuneCountInString(service.DisplayName) > 200 {
		return &serviceCreateValidationError{
			field:   "display_name",
			message: "service display_name must be at most 200 characters",
		}
	}
	return nil
}

func createTenantService(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantMutationCoordinator,
	request CreateServiceRequest,
	body []byte,
) {
	if coordinator == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant service creation is unavailable", nil)
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK {
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	if err := idempotency.ValidateKey(key); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return
	}
	result, err := coordinator.CreateService(r.Context(), TenantServiceCreateRequest{
		Principal: principal,
		Session:   session,
		Service: Service{
			ProjectID:   orailtypes.ID(request.ProjectID),
			Slug:        request.Slug,
			DisplayName: request.DisplayName,
		},
		Key: key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     r.Method,
			Path:       idempotencyRouteTemplate(r),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
		return
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
		return
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to create services in this project", nil)
		return
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
		return
	case errors.Is(err, ErrResourceConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), map[string]any{"slug": request.Slug})
		return
	case errors.Is(err, ErrResourceInvalid), errors.Is(err, idempotency.ErrInvalidKey):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant service creation is unavailable", nil)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func listProjects(w http.ResponseWriter, r *http.Request, store ProjectStore) {
	projects, err := store.ListProjects(r.Context())
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "project list failed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, projects)
}

func listServices(w http.ResponseWriter, r *http.Request, store ServiceStore) {
	filter := ResourceListFilter{ParentID: orailtypes.ID(r.URL.Query().Get("project_id"))}
	services, err := store.ListServices(r.Context(), filter)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service list failed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, services)
}

// reserveIdempotencyOrAbort enforces the contract on every mutating path.
// It returns false when an error envelope has been written and the caller
// should stop. Successful callers retain the returned reservation so every
// state transition presents the generation-fenced Reference.
func reserveIdempotencyOrAbort(w http.ResponseWriter, r *http.Request, idem idempotency.Store, body []byte) (idempotency.Reservation, bool) {
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "Idempotency-Key header is required", nil)
		return idempotency.Reservation{}, false
	}
	if err := idempotency.ValidateKey(key); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return idempotency.Reservation{}, false
	}
	fingerprint := idempotencyRequestFingerprint(r, idempotency.BodyHashSHA256(body))
	reservation, err := idem.Reserve(r.Context(), key, fingerprint, 24*time.Hour)
	if errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
		return idempotency.Reservation{}, false
	}
	if errors.Is(err, idempotency.ErrKeyReused) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
		return idempotency.Reservation{}, false
	}
	if errors.Is(err, idempotency.ErrRequestInProgress) {
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
		return idempotency.Reservation{}, false
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency reservation failed", nil)
		return idempotency.Reservation{}, false
	}
	if reservation.IsTerminal() && reservation.Response != nil {
		stored := reservation.Response.Clone()
		w.Header().Set("Idempotent-Replayed", "true")
		for key, values := range stored.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		status := stored.StatusCode
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(stored.Body)
		return idempotency.Reservation{}, false
	}
	return reservation, true
}

func idempotencyRouteTemplate(r *http.Request) string {
	if r != nil {
		if pattern := strings.TrimSpace(r.Pattern); pattern != "" {
			return pattern
		}
		if r.URL != nil {
			return r.URL.Path
		}
	}
	return ""
}

func idempotencyRequestFingerprint(r *http.Request, bodyHash string) idempotency.RequestFingerprint {
	return idempotencyRequestFingerprintForRoute(r, idempotencyRouteTemplate(r), bodyHash)
}

func idempotencyRequestFingerprintForRoute(r *http.Request, pathTemplate, bodyHash string) idempotency.RequestFingerprint {
	if r != nil && r.URL != nil && r.URL.Path != "" && r.URL.Path != pathTemplate {
		// Route-template scoping must retain the old concrete-target distinction:
		// the same client key and body against two resource IDs is a conflict, not
		// a replay of the first target's response.
		bodyHash = idempotency.BodyHashSHA256([]byte(r.URL.Path + "\x00" + bodyHash))
	}
	method := ""
	if r != nil {
		method = r.Method
	}
	return idempotency.RequestFingerprint{
		Method:     method,
		Path:       pathTemplate,
		BodySHA256: bodyHash,
	}
}
