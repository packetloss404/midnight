// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type projectAccessResponse struct {
	PrincipalKind         productPrincipalKind   `json:"principal_kind"`
	WorkspaceID           string                 `json:"workspace_id"`
	ProjectID             string                 `json:"project_id"`
	EnvironmentID         string                 `json:"environment_id,omitempty"`
	WorkspaceRole         identity.WorkspaceRole `json:"workspace_role,omitempty"`
	ProjectRole           identity.ProjectRole   `json:"project_role,omitempty"`
	EnvironmentRestricted bool                   `json:"environment_restricted"`
	// Permissions are RBAC decisions only. They deliberately do not claim
	// that every corresponding product/runtime route is installed.
	Permissions []identity.Capability `json:"permissions"`
}

func projectAccessHandler(projects ProjectStore, environments EnvironmentStore, access identity.AccessStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		principal, ok := productPrincipalFromContext(r.Context())
		if !ok {
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthRequiredMissing, "product principal is required", nil)
			return
		}
		projectID := strings.TrimSpace(r.PathValue("project_id"))
		environmentID := strings.TrimSpace(r.URL.Query().Get("environment_id"))
		project, err := projects.GetProject(r.Context(), orailtypes.ID(projectID))
		if errors.Is(err, ErrResourceNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project access lookup failed", nil)
			return
		}
		if environmentID != "" {
			environment, environmentErr := environments.GetEnvironment(r.Context(), orailtypes.ID(environmentID))
			if errors.Is(environmentErr, ErrResourceNotFound) || (environmentErr == nil && environment.ProjectID != project.ID) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
				return
			}
			if environmentErr != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project access lookup failed", nil)
				return
			}
		}

		response := projectAccessResponse{
			PrincipalKind: principal.Kind,
			WorkspaceID:   project.WorkspaceID,
			ProjectID:     string(project.ID),
			EnvironmentID: environmentID,
			Permissions:   identity.Capabilities(),
		}
		if principal.Kind == productPrincipalHuman {
			if access == nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant authorization is unavailable", nil)
				return
			}
			grant, resolveErr := access.ResolveAccess(r.Context(), principal.Human.UserID, identity.AccessTarget{
				WorkspaceID:   project.WorkspaceID,
				ProjectID:     string(project.ID),
				EnvironmentID: environmentID,
			})
			if errors.Is(resolveErr, identity.ErrAccessNotFound) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
				return
			}
			if resolveErr != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project access lookup failed", nil)
				return
			}
			if grant.EnvironmentRestricted && grant.WorkspaceRole != identity.WorkspaceRoleAdmin {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
				return
			}
			response.WorkspaceRole = grant.WorkspaceRole
			response.ProjectRole = grant.ProjectRole
			response.EnvironmentRestricted = grant.EnvironmentRestricted
			response.Permissions = response.Permissions[:0]
			for _, capability := range identity.Capabilities() {
				if identity.EvaluateAccess(identity.AccessRequest{
					WorkspaceRole:         grant.WorkspaceRole,
					ProjectRole:           grant.ProjectRole,
					Capability:            capability,
					EnvironmentRestricted: grant.EnvironmentRestricted,
				}).Allowed {
					response.Permissions = append(response.Permissions, capability)
				}
			}
		}
		WriteJSON(w, http.StatusOK, response)
	}
}
