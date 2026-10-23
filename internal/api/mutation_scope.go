// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// MutationScopeRequest describes the resource graph a mutation intends to
// change. IDs may be omitted when the mutation is not scoped to that resource;
// Require* distinguishes an omitted optional scope from an invalid request.
type MutationScopeRequest struct {
	ProjectID     string
	ServiceID     string
	EnvironmentID string

	RequireProject     bool
	RequireService     bool
	RequireEnvironment bool
}

// ResolvedMutationScope is the canonical, store-backed scope. ProjectID is
// inferred from a supplied service or environment when the caller omitted it.
type ResolvedMutationScope struct {
	ProjectID     string
	ServiceID     string
	EnvironmentID string
	Project       *Project
	Service       *Service
	Environment   *Environment
}

// MutationScopeErrorKind keeps resolver failures typed so HTTP, GraphQL, CLI,
// and worker callers can map the same integrity decision onto their transports.
type MutationScopeErrorKind string

const (
	MutationScopeInvalid               MutationScopeErrorKind = "invalid"
	MutationScopeNotFound              MutationScopeErrorKind = "not_found"
	MutationScopeMismatch              MutationScopeErrorKind = "project_mismatch"
	MutationScopeDependencyUnavailable MutationScopeErrorKind = "dependency_unavailable"
	MutationScopeLookupFailed          MutationScopeErrorKind = "lookup_failed"
)

// MutationScopeError is safe to return to API clients: it contains resource
// identifiers and relationship metadata, never variable values or credentials.
type MutationScopeError struct {
	Kind       MutationScopeErrorKind
	Resource   string
	ID         string
	ProjectID  string
	ExpectedID string
	ActualID   string
	Err        error
}

func (e *MutationScopeError) Error() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case MutationScopeInvalid:
		if e.ID != "" {
			return fmt.Sprintf("%s is not assigned to a project", e.Resource)
		}
		return fmt.Sprintf("%s_id is required", e.Resource)
	case MutationScopeNotFound:
		return fmt.Sprintf("%s not found", e.Resource)
	case MutationScopeMismatch:
		return fmt.Sprintf("%s belongs to project %q, not %q", e.Resource, e.ActualID, e.ExpectedID)
	case MutationScopeDependencyUnavailable:
		return fmt.Sprintf("%s scope lookup is not configured", e.Resource)
	default:
		return fmt.Sprintf("%s scope lookup failed", e.Resource)
	}
}

func (e *MutationScopeError) Unwrap() error { return e.Err }

// MutationScopeResolver verifies project/service/environment relationships
// before a mutation writes any state.
type MutationScopeResolver interface {
	ResolveMutationScope(context.Context, MutationScopeRequest) (ResolvedMutationScope, error)
}

// StoreMutationScopeResolver resolves resource relationships through the same
// stores used by the API. A missing dependency fails closed with a typed error.
type StoreMutationScopeResolver struct {
	projects     ProjectStore
	services     ServiceStore
	environments EnvironmentStore
}

func NewMutationScopeResolver(projects ProjectStore, services ServiceStore, environments EnvironmentStore) *StoreMutationScopeResolver {
	return &StoreMutationScopeResolver{
		projects:     projects,
		services:     services,
		environments: environments,
	}
}

// ResolveMutationScope verifies every supplied resource exists and belongs to
// one canonical project. It never accepts a parentless service for a
// project-scoped mutation.
func (r *StoreMutationScopeResolver) ResolveMutationScope(ctx context.Context, req MutationScopeRequest) (ResolvedMutationScope, error) {
	projectID := strings.TrimSpace(req.ProjectID)
	serviceID := strings.TrimSpace(req.ServiceID)
	environmentID := strings.TrimSpace(req.EnvironmentID)

	if req.RequireProject && projectID == "" && serviceID == "" && environmentID == "" {
		return ResolvedMutationScope{}, requiredScopeError("project")
	}
	if req.RequireService && serviceID == "" {
		return ResolvedMutationScope{}, requiredScopeError("service")
	}
	if req.RequireEnvironment && environmentID == "" {
		return ResolvedMutationScope{}, requiredScopeError("environment")
	}

	out := ResolvedMutationScope{
		ProjectID:     projectID,
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
	}

	if serviceID != "" {
		if r == nil || r.services == nil {
			return ResolvedMutationScope{}, unavailableScopeError("service", serviceID)
		}
		service, err := r.services.GetService(ctx, orailtypes.ID(serviceID))
		if err != nil {
			return ResolvedMutationScope{}, lookupScopeError("service", serviceID, err)
		}
		serviceProjectID := strings.TrimSpace(string(service.ProjectID))
		if serviceProjectID == "" {
			return ResolvedMutationScope{}, &MutationScopeError{
				Kind:      MutationScopeInvalid,
				Resource:  "service",
				ID:        serviceID,
				ProjectID: projectID,
				Err:       ErrResourceInvalid,
			}
		}
		if err := mergeMutationProject(&projectID, "service", serviceID, serviceProjectID); err != nil {
			return ResolvedMutationScope{}, err
		}
		out.Service = &service
	}

	if environmentID != "" {
		if r == nil || r.environments == nil {
			return ResolvedMutationScope{}, unavailableScopeError("environment", environmentID)
		}
		environment, err := r.environments.GetEnvironment(ctx, orailtypes.ID(environmentID))
		if err != nil {
			return ResolvedMutationScope{}, lookupScopeError("environment", environmentID, err)
		}
		environmentProjectID := strings.TrimSpace(string(environment.ProjectID))
		if environmentProjectID == "" {
			return ResolvedMutationScope{}, &MutationScopeError{
				Kind:      MutationScopeInvalid,
				Resource:  "environment",
				ID:        environmentID,
				ProjectID: projectID,
				Err:       ErrResourceInvalid,
			}
		}
		if err := mergeMutationProject(&projectID, "environment", environmentID, environmentProjectID); err != nil {
			return ResolvedMutationScope{}, err
		}
		out.Environment = &environment
	}

	if req.RequireProject && projectID == "" {
		return ResolvedMutationScope{}, requiredScopeError("project")
	}
	if projectID != "" {
		if r == nil || r.projects == nil {
			return ResolvedMutationScope{}, unavailableScopeError("project", projectID)
		}
		project, err := r.projects.GetProject(ctx, orailtypes.ID(projectID))
		if err != nil {
			return ResolvedMutationScope{}, lookupScopeError("project", projectID, err)
		}
		out.Project = &project
	}
	out.ProjectID = projectID
	return out, nil
}

func mergeMutationProject(projectID *string, resource, id, actualProjectID string) error {
	if *projectID == "" {
		*projectID = actualProjectID
		return nil
	}
	if *projectID != actualProjectID {
		return &MutationScopeError{
			Kind:       MutationScopeMismatch,
			Resource:   resource,
			ID:         id,
			ProjectID:  *projectID,
			ExpectedID: *projectID,
			ActualID:   actualProjectID,
			Err:        ErrResourceInvalid,
		}
	}
	return nil
}

func requiredScopeError(resource string) *MutationScopeError {
	return &MutationScopeError{Kind: MutationScopeInvalid, Resource: resource, Err: ErrResourceInvalid}
}

func unavailableScopeError(resource, id string) *MutationScopeError {
	return &MutationScopeError{Kind: MutationScopeDependencyUnavailable, Resource: resource, ID: id}
}

func lookupScopeError(resource, id string, err error) *MutationScopeError {
	if errors.Is(err, ErrResourceNotFound) {
		return &MutationScopeError{Kind: MutationScopeNotFound, Resource: resource, ID: id, Err: err}
	}
	return &MutationScopeError{Kind: MutationScopeLookupFailed, Resource: resource, ID: id, Err: err}
}

func mutationScopeErrorDetails(err error) map[string]any {
	var scopeErr *MutationScopeError
	if !errors.As(err, &scopeErr) {
		return nil
	}
	details := map[string]any{"resource": scopeErr.Resource}
	if scopeErr.ID != "" {
		details["resource_id"] = scopeErr.ID
	}
	if scopeErr.ProjectID != "" {
		details["project_id"] = scopeErr.ProjectID
	}
	if scopeErr.ExpectedID != "" {
		details["expected_project_id"] = scopeErr.ExpectedID
	}
	if scopeErr.ActualID != "" {
		details["actual_project_id"] = scopeErr.ActualID
	}
	return details
}

func mutationScopeHTTPError(err error) (int, failurecodes.Code, string, map[string]any) {
	var scopeErr *MutationScopeError
	if !errors.As(err, &scopeErr) {
		return http.StatusInternalServerError, failurecodes.InternalError, "resource scope validation failed", nil
	}
	switch scopeErr.Kind {
	case MutationScopeInvalid, MutationScopeMismatch:
		return http.StatusBadRequest, failurecodes.ResourceValidationFailed, scopeErr.Error(), mutationScopeErrorDetails(err)
	case MutationScopeNotFound:
		return http.StatusNotFound, failurecodes.ResourceNotFound, scopeErr.Error(), mutationScopeErrorDetails(err)
	case MutationScopeDependencyUnavailable:
		return http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, scopeErr.Error(), mutationScopeErrorDetails(err)
	default:
		return http.StatusInternalServerError, failurecodes.InternalError, scopeErr.Error(), mutationScopeErrorDetails(err)
	}
}

func writeMutationScopeError(w http.ResponseWriter, req *http.Request, err error) {
	status, code, message, details := mutationScopeHTTPError(err)
	writeRequestError(w, req, status, code, message, details)
}

func mutationScopeServiceError(err error) *ServiceError {
	status, code, message, details := mutationScopeHTTPError(err)
	return &ServiceError{
		Status:  status,
		Code:    string(code),
		Message: message,
		Details: details,
	}
}

func firstMutationScopeResolver(resolvers []MutationScopeResolver) MutationScopeResolver {
	if len(resolvers) == 0 {
		return nil
	}
	return resolvers[0]
}
