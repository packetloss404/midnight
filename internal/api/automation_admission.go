// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/failurecodes"
)

// automationRouteCapability is a server-owned route policy key. The
// ci-deploy-v1 profile is intentionally expressed as exact method/path shapes
// rather than broad "read" or "deploy" scopes that could grow accidentally as
// handlers add subroutes.
type automationRouteCapability string

const (
	automationCapabilityProjectRead     automationRouteCapability = "project.read"
	automationCapabilityServiceList     automationRouteCapability = "service.list"
	automationCapabilityServiceRead     automationRouteCapability = "service.read"
	automationCapabilityEnvironmentList automationRouteCapability = "environment.list"
	automationCapabilityEnvironmentRead automationRouteCapability = "environment.read"
	automationCapabilityDeployCreate    automationRouteCapability = "deployment.create"
	automationCapabilitySourceDeploy    automationRouteCapability = "deployment.source.create"
	automationCapabilityDeploymentList  automationRouteCapability = "deployment.list"
	automationCapabilityDeploymentRead  automationRouteCapability = "deployment.read"
	automationCapabilityOperationRead   automationRouteCapability = "operation.read"
	automationCapabilityOperationEvents automationRouteCapability = "operation.events.read"
	automationCapabilityOperationLogs   automationRouteCapability = "operation.logs.read"
)

type automationRouteAdmission struct {
	Capability    automationRouteCapability
	ProjectID     string
	ServiceID     string
	EnvironmentID string
	DeploymentID  string
	OperationID   string
}

type automationRouteAdmissionContextKey struct{}

func automationRouteAdmissionFromContext(ctx context.Context) (automationRouteAdmission, bool) {
	admission, ok := ctx.Value(automationRouteAdmissionContextKey{}).(automationRouteAdmission)
	return admission, ok
}

// automationAdmissionMiddleware admits only the exact ci-deploy-v1 registry
// below and proves every referenced resource belongs to both the token project
// and its canonical workspace. Authentication without this middleware grants
// no product route capability.
func automationAdmissionMiddleware(scopeResolver MutationScopeResolver, deploymentStore DeploymentStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := productPrincipalFromContext(r.Context())
		if !ok || principal.Kind != productPrincipalAutomation {
			next.ServeHTTP(w, r)
			return
		}

		admission, allowed := ciDeployV1Admission(r)
		if !allowed || principal.Automation.Profile != automationtoken.ProfileCIDeployV1 {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "automation credential is not permitted for this method and route", nil)
			return
		}
		if scopeResolver == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "automation scope authorization is unavailable", nil)
			return
		}

		scopeRequest := MutationScopeRequest{
			ProjectID:          principal.Automation.ProjectID,
			ServiceID:          admission.ServiceID,
			EnvironmentID:      admission.EnvironmentID,
			RequireProject:     true,
			RequireService:     admission.ServiceID != "",
			RequireEnvironment: admission.EnvironmentID != "",
		}
		if admission.ProjectID != "" && admission.ProjectID != principal.Automation.ProjectID {
			writeAutomationResourceNotFound(w, r)
			return
		}

		if admission.OperationID != "" {
			if deploymentStore == nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "automation operation authorization is unavailable", nil)
				return
			}
			deployment, err := deploymentStore.GetDeploymentByOperation(r.Context(), admission.OperationID)
			if errors.Is(err, ErrDeploymentNotFound) {
				writeAutomationResourceNotFound(w, r)
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "automation operation authorization is unavailable", nil)
				return
			}
			if strings.TrimSpace(deployment.ID) == "" ||
				strings.TrimSpace(deployment.ProjectID) == "" ||
				strings.TrimSpace(deployment.ServiceID) == "" ||
				strings.TrimSpace(deployment.EnvironmentID) == "" ||
				deployment.OperationID != admission.OperationID ||
				deployment.ProjectID != principal.Automation.ProjectID {
				writeAutomationResourceNotFound(w, r)
				return
			}
			admission.ProjectID = deployment.ProjectID
			admission.ServiceID = deployment.ServiceID
			admission.EnvironmentID = deployment.EnvironmentID
			admission.DeploymentID = deployment.ID
			scopeRequest.ServiceID = deployment.ServiceID
			scopeRequest.EnvironmentID = deployment.EnvironmentID
			scopeRequest.RequireService = true
			scopeRequest.RequireEnvironment = true
		}

		resolved, err := scopeResolver.ResolveMutationScope(r.Context(), scopeRequest)
		if err != nil {
			var scopeErr *MutationScopeError
			if errors.As(err, &scopeErr) &&
				(scopeErr.Kind == MutationScopeNotFound ||
					scopeErr.Kind == MutationScopeMismatch ||
					scopeErr.Kind == MutationScopeInvalid) {
				writeAutomationResourceNotFound(w, r)
				return
			}
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "automation scope authorization is unavailable", nil)
			return
		}
		if resolved.Project == nil ||
			resolved.ProjectID != principal.Automation.ProjectID ||
			strings.TrimSpace(resolved.Project.WorkspaceID) == "" ||
			resolved.Project.WorkspaceID != principal.Automation.WorkspaceID ||
			(admission.ServiceID != "" && resolved.ServiceID != admission.ServiceID) ||
			(admission.EnvironmentID != "" && resolved.EnvironmentID != admission.EnvironmentID) {
			writeAutomationResourceNotFound(w, r)
			return
		}

		ctx := context.WithValue(r.Context(), automationRouteAdmissionContextKey{}, admission)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeAutomationResourceNotFound(w http.ResponseWriter, r *http.Request) {
	writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "automation-scoped resource not found", nil)
}

// ciDeployV1Admission is the complete route registry for project automation
// tokens. Adding a handler elsewhere does not expand this profile.
func ciDeployV1Admission(r *http.Request) (automationRouteAdmission, bool) {
	if r == nil || r.URL == nil {
		return automationRouteAdmission{}, false
	}
	path := r.URL.Path

	if strings.HasPrefix(path, v1alphaPrefix+"/projects/") && r.Method == http.MethodGet {
		projectID := strings.TrimPrefix(path, v1alphaPrefix+"/projects/")
		if projectID != "" && !strings.Contains(projectID, "/") {
			return automationRouteAdmission{
				Capability: automationCapabilityProjectRead,
				ProjectID:  projectID,
			}, true
		}
		return automationRouteAdmission{}, false
	}

	if path == v1alphaPrefix+"/services" && r.Method == http.MethodGet {
		projectID, ok := singleProjectFilter(r)
		if !ok {
			return automationRouteAdmission{}, false
		}
		return automationRouteAdmission{
			Capability: automationCapabilityServiceList,
			ProjectID:  projectID,
		}, true
	}
	if strings.HasPrefix(path, v1alphaPrefix+"/services/") {
		rest := strings.TrimPrefix(path, v1alphaPrefix+"/services/")
		parts := strings.Split(rest, "/")
		if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodGet {
			return automationRouteAdmission{
				Capability: automationCapabilityServiceRead,
				ServiceID:  parts[0],
			}, true
		}
		if len(parts) == 2 && parts[0] != "" {
			switch {
			case parts[1] == "up" && r.Method == http.MethodPost:
				return automationRouteAdmission{
					Capability: automationCapabilitySourceDeploy,
					ServiceID:  parts[0],
				}, true
			case parts[1] == "deployments" && r.Method == http.MethodPost:
				return automationRouteAdmission{
					Capability: automationCapabilityDeployCreate,
					ServiceID:  parts[0],
				}, true
			case parts[1] == "deployments" && r.Method == http.MethodGet:
				return automationRouteAdmission{
					Capability: automationCapabilityDeploymentList,
					ServiceID:  parts[0],
				}, true
			}
		}
		if len(parts) == 3 && parts[0] != "" && parts[1] == "deployments" &&
			parts[2] != "" && r.Method == http.MethodGet {
			return automationRouteAdmission{
				Capability:   automationCapabilityDeploymentRead,
				ServiceID:    parts[0],
				DeploymentID: parts[2],
			}, true
		}
		return automationRouteAdmission{}, false
	}

	if path == v1alphaPrefix+"/environments" && r.Method == http.MethodGet {
		projectID, ok := singleProjectFilter(r)
		if !ok {
			return automationRouteAdmission{}, false
		}
		return automationRouteAdmission{
			Capability: automationCapabilityEnvironmentList,
			ProjectID:  projectID,
		}, true
	}
	if strings.HasPrefix(path, v1alphaPrefix+"/environments/") && r.Method == http.MethodGet {
		environmentID := strings.TrimPrefix(path, v1alphaPrefix+"/environments/")
		if environmentID != "" && !strings.Contains(environmentID, "/") {
			return automationRouteAdmission{
				Capability:    automationCapabilityEnvironmentRead,
				EnvironmentID: environmentID,
			}, true
		}
		return automationRouteAdmission{}, false
	}

	if strings.HasPrefix(path, v1alphaPrefix+"/operations/") && r.Method == http.MethodGet {
		operationID, action, ok := parseOperationPath(path)
		if !ok {
			return automationRouteAdmission{}, false
		}
		switch action {
		case "":
			return automationRouteAdmission{
				Capability:  automationCapabilityOperationRead,
				OperationID: operationID,
			}, true
		case "events":
			return automationRouteAdmission{
				Capability:  automationCapabilityOperationEvents,
				OperationID: operationID,
			}, true
		case "logs":
			return automationRouteAdmission{
				Capability:  automationCapabilityOperationLogs,
				OperationID: operationID,
			}, true
		}
	}

	return automationRouteAdmission{}, false
}

func singleProjectFilter(r *http.Request) (string, bool) {
	values, ok := r.URL.Query()["project_id"]
	if !ok || len(values) != 1 {
		return "", false
	}
	projectID := strings.TrimSpace(values[0])
	return projectID, projectID != ""
}
