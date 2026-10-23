// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/pkg/configcode"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type ConfigApplyResponse struct {
	Plan    configcode.Plan `json:"plan"`
	Applied int             `json:"applied"`
}

type configSyncDocument struct {
	APIVersion       string                      `json:"api_version"`
	EnvironmentID    string                      `json:"environment_id"`
	ExpectedRevision *int64                      `json:"expected_revision"`
	Note             string                      `json:"note,omitempty"`
	Changes          []EnvironmentChangesetInput `json:"changes"`
}

// configSyncHandler accepts one strict environment desired-state document and
// stages it through the same durable changeset authority as the dashboard. It
// never applies the draft and never starts a deployment.
func configSyncHandler(coordinator EnvironmentChangesetCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		principal, classified := productPrincipalFromContext(r.Context())
		if !classified || principal.Kind != productPrincipalHuman {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment config sync not found", nil)
			return
		}
		identityPrincipal, principalOK := identityPrincipalFromContext(r.Context())
		session, sessionOK := identitySessionFromContext(r.Context())
		if !principalOK || !sessionOK {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
			return
		}
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable environment config sync is unavailable", map[string]any{"capability": "environment-config-sync"})
			return
		}
		var document configSyncDocument
		raw, ok := readStrictEnvironmentChangesetBody(w, r, &document)
		if !ok {
			return
		}
		defer clear(raw)
		document.EnvironmentID = strings.TrimSpace(document.EnvironmentID)
		if document.APIVersion != configcode.APIVersion || document.EnvironmentID == "" || document.ExpectedRevision == nil || *document.ExpectedRevision < 0 || len(document.Changes) == 0 {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "config sync document is invalid", nil)
			return
		}
		if !requireMatchingExpectedVersion(w, r, *document.ExpectedRevision) {
			return
		}
		key, keyOK := requireTenantVariableIdempotencyKey(w, r)
		if !keyOK {
			return
		}
		result, err := coordinator.PutEnvironmentChangeset(r.Context(), EnvironmentChangesetMutationRequest{
			EnvironmentChangesetReadRequest: EnvironmentChangesetReadRequest{Principal: identityPrincipal, Session: session, EnvironmentID: document.EnvironmentID},
			ExpectedRevision:                *document.ExpectedRevision,
			Note:                            document.Note,
			Changes:                         document.Changes,
			Key:                             key,
			RawBody:                         raw,
			Method:                          r.Method,
			Path:                            r.URL.Path,
			RequestID:                       middleware.FromContext(r.Context()),
			SourceIP:                        r.RemoteAddr,
			UserAgent:                       r.UserAgent(),
		})
		if writeEnvironmentChangesetError(w, r, err) {
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

func configDiffHandler(projects ProjectStore, services ServiceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		doc, ok := decodeConfigDocument(w, r)
		if !ok {
			return
		}
		plan, err := diffConfigDocument(r, projects, services, doc)
		if errors.Is(err, configcode.ErrInvalidDocument) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "config diff failed", nil)
			return
		}
		WriteJSON(w, http.StatusOK, plan)
	}
}

func configApplyHandler(projects ProjectStore, services ServiceStore, idem idempotency.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		body, ok := readBoundedJSONRequestBody(w, r, "invalid config request body")
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
		var doc configcode.Document
		if !decodeStrictJSONRequestBytes(w, r, body, &doc, "invalid config request body") {
			return
		}
		plan, err := diffConfigDocument(r, projects, services, doc)
		if errors.Is(err, configcode.ErrInvalidDocument) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "config apply planning failed", nil)
			return
		}
		if plan.Summary.Update > 0 {
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "config apply cannot update existing resources yet", map[string]any{"updates": plan.Summary.Update})
			return
		}

		projectIDs, err := projectIDsBySlug(r, projects)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "project lookup failed", nil)
			return
		}
		applied := 0
		for _, project := range doc.Projects {
			if _, exists := projectIDs[project.Slug]; exists {
				continue
			}
			created, err := projects.CreateProject(r.Context(), Project{WorkspaceID: DefaultWorkspaceID, Slug: project.Slug, DisplayName: defaultName(project.DisplayName, project.Slug)})
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, fmt.Sprintf("project %q create failed", project.Slug), nil)
				return
			}
			projectIDs[project.Slug] = created.ID
			applied++
		}
		for _, service := range doc.Services {
			projectID := projectIDs[service.ProjectSlug]
			if projectID == "" {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "service project not found", map[string]any{"project_slug": service.ProjectSlug})
				return
			}
			if serviceExists(r, services, projectID, service.Slug) {
				continue
			}
			_, err := services.CreateService(r.Context(), Service{ProjectID: projectID, Slug: service.Slug, DisplayName: defaultName(service.DisplayName, service.Slug)})
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, fmt.Sprintf("service %q create failed", service.Slug), nil)
				return
			}
			applied++
		}

		WriteJSON(w, http.StatusOK, ConfigApplyResponse{Plan: plan, Applied: applied})
	}
}

func decodeConfigDocument(w http.ResponseWriter, r *http.Request) (configcode.Document, bool) {
	var doc configcode.Document
	if !decodeStrictJSONRequest(w, r, &doc, "invalid config request body") {
		return configcode.Document{}, false
	}
	return doc, true
}

func diffConfigDocument(r *http.Request, projects ProjectStore, services ServiceStore, doc configcode.Document) (configcode.Plan, error) {
	state, err := configState(r, projects, services)
	if err != nil {
		return configcode.Plan{}, err
	}
	return configcode.Diff(doc, state)
}

func configState(r *http.Request, projects ProjectStore, services ServiceStore) (configcode.State, error) {
	projectList, err := projects.ListProjects(r.Context())
	if err != nil {
		return configcode.State{}, err
	}
	projectSlugByID := make(map[orailtypes.ID]string, len(projectList))
	state := configcode.State{Projects: make([]configcode.Project, 0, len(projectList))}
	for _, project := range projectList {
		projectSlugByID[project.ID] = project.Slug
		state.Projects = append(state.Projects, configcode.Project{Slug: project.Slug, DisplayName: project.DisplayName})
	}
	serviceList, err := services.ListServices(r.Context(), ResourceListFilter{})
	if err != nil {
		return configcode.State{}, err
	}
	state.Services = make([]configcode.Service, 0, len(serviceList))
	for _, service := range serviceList {
		state.Services = append(state.Services, configcode.Service{ProjectSlug: projectSlugByID[service.ProjectID], Slug: service.Slug, DisplayName: service.DisplayName})
	}
	return state, nil
}

func projectIDsBySlug(r *http.Request, projects ProjectStore) (map[string]orailtypes.ID, error) {
	projectList, err := projects.ListProjects(r.Context())
	if err != nil {
		return nil, err
	}
	out := make(map[string]orailtypes.ID, len(projectList))
	for _, project := range projectList {
		out[project.Slug] = project.ID
	}
	return out, nil
}

func serviceExists(r *http.Request, services ServiceStore, projectID orailtypes.ID, slug string) bool {
	serviceList, err := services.ListServices(r.Context(), ResourceListFilter{ParentID: projectID})
	if err != nil {
		return false
	}
	for _, service := range serviceList {
		if service.Slug == slug {
			return true
		}
	}
	return false
}

func defaultName(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}
