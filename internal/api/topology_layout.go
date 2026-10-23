// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/topology"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type putTopologyLayoutRequest struct {
	Nodes *[]topology.Node `json:"nodes"`
}

func topologyLayoutHandler(
	store topology.Store,
	projects ProjectStore,
	environments EnvironmentStore,
	services ServiceStore,
	access identity.AccessStore,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, classified := productPrincipalFromContext(r.Context())
		if !classified {
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthRequiredMissing, "an authenticated human session is required", nil)
			return
		}
		if principal.Kind == productPrincipalAutomation || principal.Kind == productPrincipalOperator {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "presentation layouts are available only to tenant members", nil)
			return
		}
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable topology layouts are not configured", nil)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPut {
			w.Header().Set("Allow", "GET, PUT")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		projectID := strings.TrimSpace(r.PathValue("project_id"))
		environmentID := strings.TrimSpace(r.PathValue("environment_id"))
		project, err := projects.GetProject(r.Context(), orailtypes.ID(projectID))
		if errors.Is(err, ErrResourceNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "topology scope lookup failed", nil)
			return
		}
		environment, err := environments.GetEnvironment(r.Context(), orailtypes.ID(environmentID))
		if errors.Is(err, ErrResourceNotFound) || (err == nil && environment.ProjectID != project.ID) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment not found", nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "topology scope lookup failed", nil)
			return
		}

		capability := identity.CapabilityProjectRead
		if r.Method == http.MethodPut {
			capability = identity.CapabilityProjectManage
		}
		if !authorizeTopologyAccess(w, r, access, project.WorkspaceID, projectID, environmentID, capability) {
			return
		}

		if r.Method == http.MethodGet {
			layout, getErr := store.Get(r.Context(), projectID, environmentID)
			if errors.Is(getErr, topology.ErrNotFound) {
				layout = topology.Layout{WorkspaceID: project.WorkspaceID, ProjectID: projectID, EnvironmentID: environmentID, Nodes: []topology.Node{}, Version: 0}
			} else if getErr != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "topology layout lookup failed", nil)
				return
			}
			w.Header().Set("ETag", strongVersionETag(layout.Version))
			WriteJSON(w, http.StatusOK, layout)
			return
		}

		expected, ok := requireIfMatchVersion(w, r)
		if !ok {
			return
		}
		var body putTopologyLayoutRequest
		if _, ok := decodeStrictJSONRequestBody(w, r, &body, "invalid topology layout request"); !ok {
			return
		}
		if body.Nodes == nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "topology nodes are required", nil)
			return
		}
		if err := topology.Validate(*body.Nodes); err != nil {
			writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, "invalid topology layout", map[string]any{"reason": err.Error()})
			return
		}
		for _, node := range *body.Nodes {
			service, lookupErr := services.GetService(r.Context(), orailtypes.ID(node.ServiceID))
			if errors.Is(lookupErr, ErrResourceNotFound) || (lookupErr == nil && service.ProjectID != project.ID) {
				writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, "topology node does not belong to this project", map[string]any{"service_id": node.ServiceID})
				return
			}
			if lookupErr != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "topology service lookup failed", nil)
				return
			}
		}
		layout, replaceErr := store.Replace(r.Context(), topology.Layout{
			WorkspaceID: project.WorkspaceID, ProjectID: projectID, EnvironmentID: environmentID, Nodes: *body.Nodes,
		}, expected)
		if errors.Is(replaceErr, topology.ErrVersionConflict) {
			writeIfMatchFailed(w, r, "If-Match does not identify the current topology layout version", nil)
			return
		}
		if errors.Is(replaceErr, topology.ErrInvalid) {
			writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, "invalid topology layout", nil)
			return
		}
		if replaceErr != nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "topology layout update failed", nil)
			return
		}
		w.Header().Set("ETag", strongVersionETag(layout.Version))
		WriteJSON(w, http.StatusOK, layout)
	}
}

func authorizeTopologyAccess(w http.ResponseWriter, r *http.Request, access identity.AccessStore, workspaceID, projectID, environmentID string, capability identity.Capability) bool {
	principal, ok := productPrincipalFromContext(r.Context())
	if ok && principal.Kind == productPrincipalDeveloper {
		return true
	}
	if !ok || principal.Kind != productPrincipalHuman || principal.Human.UserID == "" {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "presentation layouts are available only to tenant members", nil)
		return false
	}
	if access == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant authorization is unavailable", nil)
		return false
	}
	grant, err := access.ResolveAccess(r.Context(), principal.Human.UserID, identity.AccessTarget{WorkspaceID: workspaceID, ProjectID: projectID, EnvironmentID: environmentID})
	if errors.Is(err, identity.ErrAccessNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
		return false
	}
	if err != nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant authorization is unavailable", nil)
		return false
	}
	decision := identity.EvaluateAccess(identity.AccessRequest{WorkspaceRole: grant.WorkspaceRole, ProjectRole: grant.ProjectRole, Capability: capability, EnvironmentRestricted: grant.EnvironmentRestricted})
	if !decision.Allowed {
		if decision.Reason == "environment-restricted" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
			return false
		}
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "the required project permission is not available for this topology layout", map[string]any{"reason": decision.Reason, "required_capability": capability})
		return false
	}
	return true
}
