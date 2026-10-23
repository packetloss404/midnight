// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/registry"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/internal/varrefs"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func newDefaultAuditChain() audit.AuditChain {
	return audit.NewMemoryChain()
}

// This file extracts thin service-method wrappers from the v1alpha REST
// handlers so the GraphQL gateway (internal/graphql) can call into the
// same business logic without round-tripping through HTTP.
//
// Each function is the minimal pull-out of the body the REST handler
// already runs: idempotency reservation, validation, store call, audit
// emit. REST handlers keep their existing shape (they still own the
// HTTP marshalling); GraphQL resolvers invoke these from the resolver
// layer and translate errors into GraphQL response errors.

// ServiceError is the typed error the service-method layer returns so
// callers (REST or GraphQL) can map onto their own response shape.
type ServiceError struct {
	Status  int            // HTTP status the REST layer would have returned
	Code    string         // failurecodes.Code value
	Message string         // human-readable message
	Details map[string]any // optional structured details
}

type serviceIdempotencyError struct {
	Status  int            `json:"status"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *ServiceError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return e.Message
}

func storedServiceJSON(status int, value any) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	return idempotency.StoredResponse{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       append(body, '\n'),
	}, nil
}

func replayedServiceError(stored idempotency.StoredResponse) *ServiceError {
	var value serviceIdempotencyError
	if err := json.Unmarshal(stored.Body, &value); err != nil {
		return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "stored idempotency failure is invalid"}
	}
	return &ServiceError{Status: value.Status, Code: value.Code, Message: value.Message, Details: value.Details}
}

func completeServiceIdempotencyFailure(
	ctx context.Context,
	store idempotency.Store,
	ref idempotency.Reference,
	serviceErr *ServiceError,
) *ServiceError {
	if serviceErr == nil || store == nil || ref.Key == "" {
		return serviceErr
	}
	stored, err := storedServiceJSON(serviceErr.Status, serviceIdempotencyError{
		Status: serviceErr.Status, Code: serviceErr.Code, Message: serviceErr.Message, Details: serviceErr.Details,
	})
	if err != nil {
		return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency failure response encode failed"}
	}
	if _, err := store.CompleteFailure(ctx, ref, stored); err != nil {
		return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency failure completion failed"}
	}
	return serviceErr
}

// ServicesAPI bundles every dependency the gateway needs to call into the
// business logic. It is a thin facade over DataStack; the GraphQL
// resolver layer builds one ServicesAPI per request and dispatches
// directly into the same stores the REST handlers use.
type ServicesAPI struct {
	ProjectStore          ProjectStore
	ServiceStore          ServiceStore
	EnvironmentStore      EnvironmentStore
	MutationScopeResolver MutationScopeResolver
	OperationStore        OperationStore
	DeploymentStore       DeploymentStore
	VariableStore         *secrets.Vault
	QueueStore            queue.Store
	IdempotencyStore      idempotency.Store
	GraphQLDeployIntake   GraphQLDeployIntakeCoordinator
	AuditEmitter          *auditEmitter
	Clock                 func() time.Time
	// VolumeStore is the per-(service, environment) persistent volume
	// store the GraphQL volume CRUD resolvers consume. The REST surface
	// at /api/v1alpha/volumes consumes the same store via the DataStack.
	VolumeStore volume.Store
	// VolumeMountStore tracks the mount path supplied at create-time so
	// reads echo back the value the REST surface persisted.
	VolumeMountStore VolumeMountStore
	DomainStore      router.DomainWorkflowStore
	AppsDomain       string
	// GitHubApp is the optional GitHub App accessor consumed by the
	// `githubInstallations` and `githubInstallationRepos` queries. Nil
	// causes those queries to surface a CAPABILITY_UNAVAILABLE service error.
	GitHubApp GitHubAppAccessor
}

// NewServicesAPI builds a ServicesAPI from the DataStack used by the REST
// router. It MUST be called after newRouterWithDataStack has populated
// the in-memory defaults.
func NewServicesAPI(stack DataStack, emitter *auditEmitter) *ServicesAPI {
	clock := stack.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &ServicesAPI{
		ProjectStore:          stack.ProjectStore,
		ServiceStore:          stack.ServiceStore,
		EnvironmentStore:      stack.EnvironmentStore,
		MutationScopeResolver: stack.MutationScopeResolver,
		OperationStore:        stack.OperationStore,
		DeploymentStore:       stack.DeploymentStore,
		VariableStore:         stack.VariableStore,
		QueueStore:            stack.QueueStore,
		IdempotencyStore:      stack.IdempotencyStore,
		GraphQLDeployIntake:   stack.GraphQLDeployIntake,
		AuditEmitter:          emitter,
		Clock:                 clock,
		VolumeStore:           stack.VolumeStore,
		VolumeMountStore:      stack.VolumeMountStore,
		DomainStore:           stack.DomainStore,
		AppsDomain:            stack.AppsDomain,
		GitHubApp:             stack.GitHubApp,
	}
}

func (s *ServicesAPI) ListDomainsService(ctx context.Context, projectID, environmentID, serviceID string) ([]router.DomainRouteState, *ServiceError) {
	if s.DomainStore == nil {
		return nil, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "public domain workflow is not configured"}
	}
	scope := router.RouteScope{ProjectID: projectID, EnvironmentID: environmentID, ServiceID: serviceID}
	if s.MutationScopeResolver == nil {
		return nil, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "domain scope validation is not configured"}
	}
	resolved, err := s.MutationScopeResolver.ResolveMutationScope(ctx, MutationScopeRequest{
		ProjectID: projectID, EnvironmentID: environmentID, ServiceID: serviceID,
		RequireProject: true, RequireEnvironment: true, RequireService: true,
	})
	if err != nil {
		return nil, mutationScopeServiceError(err)
	}
	scope.ProjectID = resolved.ProjectID
	values, err := s.DomainStore.ListDomainRouteStates(ctx, scope)
	if err != nil {
		return nil, domainServiceError(err, "domain list failed")
	}
	return values, nil
}

func (s *ServicesAPI) CreateCustomDomainService(ctx context.Context, environmentID, serviceID, hostname string) (router.DomainRouteState, *ServiceError) {
	if s.DomainStore == nil {
		return router.DomainRouteState{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "public domain workflow is not configured"}
	}
	if s.MutationScopeResolver == nil {
		return router.DomainRouteState{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "domain scope validation is not configured"}
	}
	resolved, err := s.MutationScopeResolver.ResolveMutationScope(ctx, MutationScopeRequest{
		EnvironmentID: environmentID, ServiceID: serviceID,
		RequireProject: true, RequireEnvironment: true, RequireService: true,
	})
	if err != nil {
		return router.DomainRouteState{}, mutationScopeServiceError(err)
	}
	scope := router.RouteScope{ProjectID: resolved.ProjectID, EnvironmentID: resolved.EnvironmentID, ServiceID: resolved.ServiceID}
	domainValue, routeValue, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{
		Scope: scope, Kind: router.DomainKindCustom, Hostname: hostname,
		PathPrefix: "/", TargetPort: 8080, Public: true, TLSDesired: true,
	})
	if err != nil {
		return router.DomainRouteState{}, domainServiceError(err, "invalid domain intent")
	}
	domainValue, err = s.DomainStore.PutDomain(ctx, domainValue)
	if err != nil {
		return router.DomainRouteState{}, domainServiceError(err, "domain create failed")
	}
	if err := s.DomainStore.EnsureDomainLifecycle(ctx, domainValue); err != nil {
		return router.DomainRouteState{}, domainServiceError(err, "domain lifecycle initialization failed")
	}
	if _, err := s.DomainStore.PutHTTPRoute(ctx, routeValue); err != nil {
		return router.DomainRouteState{}, domainServiceError(err, "domain route create failed")
	}
	values, err := s.DomainStore.ListDomainRouteStates(ctx, scope)
	if err != nil {
		return router.DomainRouteState{}, domainServiceError(err, "domain lookup failed")
	}
	for _, value := range values {
		if value.Domain.ID == domainValue.ID {
			return value, nil
		}
	}
	return router.DomainRouteState{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "created domain was not observable"}
}

func domainServiceError(err error, message string) *ServiceError {
	switch {
	case errors.Is(err, router.ErrDurableRouteNotFound):
		return &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "domain not found"}
	case errors.Is(err, router.ErrDurableHostnameOwned):
		return &ServiceError{Status: http.StatusConflict, Code: "RESOURCE_CONFLICT", Message: "hostname is already owned"}
	case errors.Is(err, router.ErrDurableRouteInvalid), errors.Is(err, router.ErrDurableRouteScopeMismatch):
		return &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error()}
	default:
		return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: message}
	}
}

// BuildServicesAPI constructs a fully-populated ServicesAPI from a raw
// DataStack, applying the same defaults the REST router applies (memory
// stores, default audit chain, etc.). External packages (such as the
// GraphQL gateway) call this to obtain a service handle without
// reimplementing the wire-up.
//
// BuildServicesAPI mutates the supplied stack pointer so the GraphQL
// gateway and the REST router share the same in-memory stores when
// both are wired off the same DataStack (the normal case). Callers
// that want isolated stores should clone before passing in.
func BuildServicesAPI(stack *DataStack) *ServicesAPI {
	if stack.OperationStore == nil {
		stack.OperationStore = NewMemoryOperationStore()
	}
	if stack.IdempotencyStore == nil {
		stack.IdempotencyStore = idempotency.NewMemoryStore()
	}
	if stack.ProjectStore == nil {
		stack.ProjectStore = NewMemoryResourceStore()
	}
	if stack.ServiceStore == nil {
		if memory, ok := stack.ProjectStore.(*MemoryResourceStore); ok {
			stack.ServiceStore = memory
		} else {
			stack.ServiceStore = NewMemoryResourceStore()
		}
	}
	if stack.EnvironmentStore == nil {
		stack.EnvironmentStore = NewMemoryEnvironmentStore(stack.Clock)
	}
	if stack.DeploymentStore == nil {
		stack.DeploymentStore = NewMemoryDeploymentStore(stack.Clock)
	}
	if stack.VariableStore == nil {
		stack.VariableStore = newDefaultVariableStore(stack.ProductionAuthRequired)
	}
	if stack.AuditChain == nil {
		stack.AuditChain = newDefaultAuditChain()
	}
	if stack.VolumeStore == nil {
		stack.VolumeStore = volume.NewMemoryStore()
	}
	if stack.VolumeMountStore == nil {
		stack.VolumeMountStore = NewMemoryVolumeMountStore()
	}
	clock := stack.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	emitter := newAuditEmitter(stack.AuditChain, clock)
	stack.Clock = clock
	return NewServicesAPI(*stack, emitter)
}

// CreateProjectService inserts a project, runs the audit emit, and
// returns the persisted record. Idempotency is the caller's
// responsibility (the GraphQL layer reserves via reserveIdempotencyKey).
func (s *ServicesAPI) CreateProjectService(ctx context.Context, req CreateProjectRequest) (Project, *ServiceError) {
	if strings.TrimSpace(req.Slug) == "" {
		return Project{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "project slug is required", Details: map[string]any{"field": "slug"}}
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		req.DisplayName = req.Slug
	}
	project, err := s.ProjectStore.CreateProject(ctx, Project{WorkspaceID: req.WorkspaceID, Slug: req.Slug, DisplayName: req.DisplayName})
	if errors.Is(err, ErrResourceConflict) {
		return Project{}, &ServiceError{Status: http.StatusConflict, Code: "RESOURCE_CONFLICT", Message: err.Error()}
	}
	if errors.Is(err, ErrResourceInvalid) {
		return Project{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error()}
	}
	if err != nil {
		return Project{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "project create failed"}
	}
	if _, err := createDefaultProductionEnvironment(ctx, s.EnvironmentStore, project); err != nil {
		return Project{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "default production environment create failed"}
	}
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "project",
			ResourceID:   string(project.ID),
			ProjectID:    string(project.ID),
			Action:       "project.create",
			After:        map[string]any{"slug": project.Slug, "display_name": project.DisplayName},
		})
	}
	return project, nil
}

// CreateServiceService inserts a service.
func (s *ServicesAPI) CreateServiceService(ctx context.Context, req CreateServiceRequest) (Service, *ServiceError) {
	req = normalizeCreateServiceRequest(req)
	if err := validateServiceCreateFields(Service{
		ProjectID:   orailtypes.ID(req.ProjectID),
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
	}, false); err != nil {
		details := map[string]any{}
		var fieldErr *serviceCreateValidationError
		if errors.As(err, &fieldErr) {
			details["field"] = fieldErr.field
		}
		return Service{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error(), Details: details}
	}
	svc, err := s.ServiceStore.CreateService(ctx, Service{
		ProjectID:   orailtypes.ID(req.ProjectID),
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
	})
	if errors.Is(err, ErrResourceConflict) {
		return Service{}, &ServiceError{Status: http.StatusConflict, Code: "RESOURCE_CONFLICT", Message: err.Error()}
	}
	if errors.Is(err, ErrResourceInvalid) {
		return Service{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error()}
	}
	if err != nil {
		return Service{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service create failed"}
	}
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "service",
			ResourceID:   string(svc.ID),
			ProjectID:    string(svc.ProjectID),
			Action:       "service.create",
			After: map[string]any{
				"slug":         svc.Slug,
				"display_name": svc.DisplayName,
				"project_id":   string(svc.ProjectID),
			},
		})
	}
	return svc, nil
}

// ListServicesService returns all services optionally filtered by project.
func (s *ServicesAPI) ListServicesService(ctx context.Context, projectID string) ([]Service, *ServiceError) {
	out, err := s.ServiceStore.ListServices(ctx, ResourceListFilter{ParentID: orailtypes.ID(projectID)})
	if err != nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service list failed"}
	}
	return out, nil
}

// ListProjectsService returns the full project list.
func (s *ServicesAPI) ListProjectsService(ctx context.Context) ([]Project, *ServiceError) {
	out, err := s.ProjectStore.ListProjects(ctx)
	if err != nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "project list failed"}
	}
	return out, nil
}

// GetProjectService returns a project by id.
func (s *ServicesAPI) GetProjectService(ctx context.Context, id string) (Project, *ServiceError) {
	out, err := s.ProjectStore.GetProject(ctx, orailtypes.ID(id))
	if errors.Is(err, ErrResourceNotFound) {
		return Project{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "project not found"}
	}
	if err != nil {
		return Project{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "project lookup failed"}
	}
	return out, nil
}

// GetServiceService returns a service by id.
func (s *ServicesAPI) GetServiceService(ctx context.Context, id string) (Service, *ServiceError) {
	out, err := s.ServiceStore.GetService(ctx, orailtypes.ID(id))
	if errors.Is(err, ErrResourceNotFound) {
		return Service{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "service not found"}
	}
	if err != nil {
		return Service{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service lookup failed"}
	}
	return out, nil
}

// ListEnvironmentsService returns environments scoped to a project.
func (s *ServicesAPI) ListEnvironmentsService(ctx context.Context, projectID string) ([]Environment, *ServiceError) {
	if s.EnvironmentStore == nil {
		return nil, nil
	}
	out, err := s.EnvironmentStore.ListEnvironments(ctx, ResourceListFilter{ParentID: orailtypes.ID(projectID)})
	if err != nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "environment list failed"}
	}
	return out, nil
}

// GetEnvironmentService returns an environment by id.
func (s *ServicesAPI) GetEnvironmentService(ctx context.Context, id string) (Environment, *ServiceError) {
	if s.EnvironmentStore == nil {
		return Environment{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "environment store not configured"}
	}
	out, err := s.EnvironmentStore.GetEnvironment(ctx, orailtypes.ID(id))
	if errors.Is(err, ErrResourceNotFound) {
		return Environment{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "environment not found"}
	}
	if err != nil {
		return Environment{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "environment lookup failed"}
	}
	return out, nil
}

// CreateEnvironmentService inserts a new environment.
func (s *ServicesAPI) CreateEnvironmentService(ctx context.Context, env Environment) (Environment, *ServiceError) {
	if s.EnvironmentStore == nil {
		return Environment{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "environment store not configured"}
	}
	out, err := s.EnvironmentStore.CreateEnvironment(ctx, env)
	if errors.Is(err, ErrResourceConflict) {
		return Environment{}, &ServiceError{Status: http.StatusConflict, Code: "RESOURCE_CONFLICT", Message: err.Error()}
	}
	if errors.Is(err, ErrResourceInvalid) {
		return Environment{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error()}
	}
	if err != nil {
		return Environment{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "environment create failed"}
	}
	return out, nil
}

// ListVariablesService returns the variable metadata list, optionally
// scoped to project/environment/service. Plaintext is never returned for
// SECRET/SEALED variables.
func (s *ServicesAPI) ListVariablesService(ctx context.Context, project, environment, service string) ([]secrets.VariableMeta, *ServiceError) {
	all, err := s.VariableStore.List(ctx)
	if err != nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "variable list failed"}
	}
	out := make([]secrets.VariableMeta, 0, len(all))
	for _, v := range all {
		if project != "" && v.Project != project {
			continue
		}
		if environment != "" && v.Environment != environment {
			continue
		}
		if service != "" && v.Service != service {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// UpsertVariableService creates or rotates a variable. Sealed scope is
// write-only; the response never includes plaintext.
func (s *ServicesAPI) UpsertVariableService(ctx context.Context, req CreateVariableRequest) (secrets.VariableMeta, *ServiceError) {
	if !secrets.ValidVariableName(req.Name) {
		return secrets.VariableMeta{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "variable name is invalid"}
	}
	scope := req.Scope
	if scope == "" {
		scope = secrets.ScopeSecret
	}
	switch scope {
	case secrets.ScopePlain, secrets.ScopeSecret, secrets.ScopeSealed:
	default:
		return secrets.VariableMeta{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "variable scope must be one of plain, secret, sealed"}
	}
	if s.MutationScopeResolver != nil {
		resolved, err := s.MutationScopeResolver.ResolveMutationScope(ctx, MutationScopeRequest{
			ProjectID:     req.ProjectID,
			ServiceID:     req.ServiceID,
			EnvironmentID: req.EnvironmentID,
		})
		if err != nil {
			return secrets.VariableMeta{}, mutationScopeServiceError(err)
		}
		req.ProjectID = resolved.ProjectID
	}
	// Parse the value template so the redaction-safe metadata view
	// surfaces value_kind/references the same way the REST handler does.
	valueKind := "literal"
	var refs []secrets.VariableReference
	parsed, parseErr := varrefs.Parse(req.Value)
	if parseErr != nil {
		return secrets.VariableMeta{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "variable reference syntax is invalid"}
	}
	if len(parsed.Refs) > 0 {
		valueKind = "reference"
		refs = make([]secrets.VariableReference, 0, len(parsed.Refs))
		for _, ref := range parsed.Refs {
			refs = append(refs, secrets.VariableReference{
				Kind:    string(ref.Kind),
				Service: ref.Service,
				Name:    ref.Name,
			})
		}
	}
	input := secrets.VariableInput{
		Name:        strings.TrimSpace(req.Name),
		Workspace:   strings.TrimSpace(req.WorkspaceID),
		Project:     strings.TrimSpace(req.ProjectID),
		Environment: strings.TrimSpace(req.EnvironmentID),
		Service:     strings.TrimSpace(req.ServiceID),
		Scope:       scope,
		Plaintext:   []byte(req.Value),
		ValueKind:   valueKind,
		References:  refs,
	}
	var v *secrets.Variable
	var err error
	if req.ExpectedVersion == nil {
		v, err = s.VariableStore.Create(ctx, input)
	} else {
		existing, findErr := s.VariableStore.Find(ctx, input.Project, input.Environment, input.Service, input.Name)
		if errors.Is(findErr, secrets.ErrVariableNotFound) {
			return secrets.VariableMeta{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "variable not found"}
		}
		if findErr != nil {
			return secrets.VariableMeta{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "variable lookup failed"}
		}
		v, err = s.VariableStore.Update(ctx, existing.Meta().ID, *req.ExpectedVersion, input)
	}
	if errors.Is(err, secrets.ErrInvalidScope) {
		return secrets.VariableMeta{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "invalid variable scope"}
	}
	if errors.Is(err, secrets.ErrInvalidVariableName) {
		return secrets.VariableMeta{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "invalid variable name"}
	}
	if errors.Is(err, secrets.ErrVariableScopeIntegrity) {
		return secrets.VariableMeta{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "variable ownership scope is invalid"}
	}
	if errors.Is(err, secrets.ErrVariableVersionConflict) {
		details := map[string]any{}
		var conflict *secrets.VariableVersionConflict
		if errors.As(err, &conflict) {
			details["expected_version"] = conflict.Expected
			details["actual_version"] = conflict.Actual
		}
		return secrets.VariableMeta{}, &ServiceError{Status: http.StatusConflict, Code: "RESOURCE_CONFLICT", Message: "variable version conflict", Details: details}
	}
	if err != nil {
		return secrets.VariableMeta{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "variable create failed"}
	}
	meta := v.Meta()
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "variable",
			ResourceID:   string(meta.ID),
			ProjectID:    meta.Project,
			Action:       "variable.create",
			After: map[string]any{
				"name":           meta.Name,
				"scope":          meta.Scope,
				"workspace_id":   meta.Workspace,
				"project_id":     meta.Project,
				"environment_id": meta.Environment,
				"service_id":     meta.Service,
				"length_bucket":  meta.LengthBucket,
			},
		})
	}
	return meta, nil
}

// ListDeploymentsForServiceService returns the canonical deployment view for a
// service. The operation-event reconstruction remains only for callers that
// deliberately construct a legacy ServicesAPI without a deployment store.
func (s *ServicesAPI) ListDeploymentsForServiceService(ctx context.Context, serviceID string) ([]DeploymentResource, *ServiceError) {
	if s.OperationStore == nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation store not configured"}
	}
	svc, err := s.ServiceStore.GetService(ctx, orailtypes.ID(serviceID))
	if errors.Is(err, ErrResourceNotFound) {
		return nil, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "service not found"}
	}
	if err != nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service lookup failed"}
	}
	if s.DeploymentStore != nil {
		records, err := s.DeploymentStore.ListDeploymentsByService(ctx, serviceID)
		if err != nil {
			return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "deployment list failed"}
		}
		if len(records) > 0 {
			deployments := make([]DeploymentResource, 0, len(records))
			for _, record := range records {
				deployments = append(deployments, deploymentResourceFromRecord(svc, record))
			}
			return deployments, nil
		}
	}
	operations, err := s.OperationStore.List(ctx, OperationListFilter{Kind: "deploy"})
	if err != nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation list failed"}
	}
	deployments := make([]DeploymentResource, 0, len(operations))
	for _, op := range operations {
		events, err := s.OperationStore.ListEvents(ctx, op.ID)
		if err != nil {
			return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation events list failed"}
		}
		if d, ok := deploymentResourceFromOperation(svc, op, events); ok {
			deployments = append(deployments, d)
		}
	}
	return deployments, nil
}

// GetDeploymentService returns a single deployment by id by scanning the
// operation log. Returns RESOURCE_NOT_FOUND when no operation surface
// matches.
func (s *ServicesAPI) GetDeploymentService(ctx context.Context, deploymentID string) (DeploymentResource, *ServiceError) {
	if s.OperationStore == nil {
		return DeploymentResource{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation store not configured"}
	}
	services, err := s.ServiceStore.ListServices(ctx, ResourceListFilter{})
	if err != nil {
		return DeploymentResource{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service list failed"}
	}
	for _, svc := range services {
		deployments, srvErr := s.ListDeploymentsForServiceService(ctx, string(svc.ID))
		if srvErr != nil {
			return DeploymentResource{}, srvErr
		}
		for _, d := range deployments {
			if d.ID == deploymentID || d.DeploymentID == deploymentID || d.OperationID == deploymentID {
				return d, nil
			}
		}
	}
	return DeploymentResource{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "deployment not found"}
}

// LatestActiveDeploymentForService returns the latest active deployment
// for a service, or NOT_FOUND when no active deployment exists.
func (s *ServicesAPI) LatestActiveDeploymentForService(ctx context.Context, serviceID string) (DeploymentResource, *ServiceError) {
	deployments, srvErr := s.ListDeploymentsForServiceService(ctx, serviceID)
	if srvErr != nil {
		return DeploymentResource{}, srvErr
	}
	if d, ok := latestActiveDeployment(deployments); ok {
		return d, nil
	}
	// Fall back to most recently updated deployment overall when no
	// route has been promoted yet.
	var latest DeploymentResource
	for _, d := range deployments {
		if latest.ID == "" || d.LastEventAt.After(latest.LastEventAt) {
			latest = d
		}
	}
	if latest.ID == "" {
		return DeploymentResource{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "no deployment found for service"}
	}
	return latest, nil
}

// DeployRequest is the GraphQL-facing deploy intake shape. It mirrors
// DeployIntakeRequest minus the idempotency dance so the resolver layer
// can call EnqueueDeployService directly.
type DeployRequest struct {
	ServiceID     string
	EnvironmentID string
	Image         string
	SourceRef     string
	DeploymentID  string
	InstanceID    string
}

// EnqueueDeployService runs the same intake validation + queue enqueue +
// operation FSM seed the REST POST /services/{id}/deployments handler
// does. The supplied idempotency.Key (may be empty) reserves the same
// idempotency.Store the REST surface uses.
//
// The returned Operation has its Kind="deploy" and State=QUEUED; the
// resolver layer surfaces it as a Deployment to the client.
func (s *ServicesAPI) EnqueueDeployService(ctx context.Context, req DeployRequest, idemKey string) (orailtypes.Operation, *ServiceError) {
	if s.QueueStore == nil {
		return orailtypes.Operation{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "deploy queue is not wired"}
	}
	if s.OperationStore == nil {
		return orailtypes.Operation{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation store is not wired"}
	}
	if strings.TrimSpace(req.ServiceID) == "" {
		return orailtypes.Operation{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "service_id is required"}
	}
	intake := DeployIntakeRequest{
		EnvironmentID: strings.TrimSpace(req.EnvironmentID),
		SourceRef:     strings.TrimSpace(req.SourceRef),
		Image:         strings.TrimSpace(req.Image),
		DeploymentID:  strings.TrimSpace(req.DeploymentID),
		InstanceID:    strings.TrimSpace(req.InstanceID),
	}
	if code, message, details, ok := validateDeployIntakeRequest(intake); !ok {
		return orailtypes.Operation{}, &ServiceError{Status: http.StatusBadRequest, Code: string(code), Message: message, Details: details}
	}
	svc, err := s.ServiceStore.GetService(ctx, orailtypes.ID(req.ServiceID))
	if errors.Is(err, ErrResourceNotFound) {
		return orailtypes.Operation{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "service not found"}
	}
	if err != nil {
		return orailtypes.Operation{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service lookup failed"}
	}
	if s.MutationScopeResolver != nil {
		_, err := s.MutationScopeResolver.ResolveMutationScope(ctx, MutationScopeRequest{
			ProjectID:          string(svc.ProjectID),
			ServiceID:          string(svc.ID),
			EnvironmentID:      intake.EnvironmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			return orailtypes.Operation{}, mutationScopeServiceError(err)
		}
	}

	// Idempotency: if a key was provided, route into the same store the
	// REST surface uses so a retry across either API replays the same
	// operation.
	key := idempotency.Key(strings.TrimSpace(idemKey))
	var reservation idempotency.Reservation
	if key != "" && s.IdempotencyStore != nil {
		body, _ := json.Marshal(struct {
			ServiceID string              `json:"service_id"`
			Intake    DeployIntakeRequest `json:"intake"`
		}{ServiceID: req.ServiceID, Intake: intake})
		fingerprint := idempotency.RequestFingerprint{
			Method:     "POST",
			Path:       "/graphql/v2/deploy/{service_id}",
			BodySHA256: idempotency.BodyHashSHA256(body),
		}
		reservation, err = s.IdempotencyStore.Reserve(ctx, key, fingerprint, 24*time.Hour)
		if errors.Is(err, idempotency.ErrInvalidKey) {
			return orailtypes.Operation{}, &ServiceError{Status: http.StatusBadRequest, Code: "IDEMPOTENCY_KEY_INVALID", Message: "invalid idempotency key"}
		}
		if errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
			return orailtypes.Operation{}, &ServiceError{Status: http.StatusConflict, Code: "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY", Message: "idempotency key reused with different body"}
		}
		if errors.Is(err, idempotency.ErrKeyReused) {
			return orailtypes.Operation{}, &ServiceError{Status: http.StatusConflict, Code: "IDEMPOTENCY_KEY_REUSED", Message: "idempotency key reused with different request"}
		}
		if errors.Is(err, idempotency.ErrRequestInProgress) {
			return orailtypes.Operation{}, &ServiceError{Status: http.StatusConflict, Code: "IDEMPOTENCY_REQUEST_IN_PROGRESS", Message: "idempotency request in progress"}
		}
		if err != nil {
			return orailtypes.Operation{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency reservation failed"}
		}
		if reservationHasExactStoredResponse(reservation) {
			stored := reservation.Response.Clone()
			if reservation.State == idempotency.StateFailed {
				return orailtypes.Operation{}, replayedServiceError(stored)
			}
			var operation orailtypes.Operation
			if err := json.Unmarshal(stored.Body, &operation); err != nil {
				return orailtypes.Operation{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "stored idempotency response is invalid"}
			}
			return operation, nil
		}
		if reservation.OperationID != "" {
			op, err := s.OperationStore.Get(ctx, orailtypes.ID(reservation.OperationID))
			if err != nil {
				return orailtypes.Operation{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation replay failed"}
			}
			return op, nil
		}
	}

	op, err := CreateQueuedOperation(ctx, s.OperationStore, "deploy")
	if err != nil {
		serviceErr := &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation create failed"}
		return orailtypes.Operation{}, completeServiceIdempotencyFailure(ctx, s.IdempotencyStore, reservation.Reference(), serviceErr)
	}
	if key != "" && s.IdempotencyStore != nil {
		if _, err := s.IdempotencyStore.AttachOperation(ctx, reservation.Reference(), string(op.ID)); err != nil {
			serviceErr := &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency operation attach failed"}
			return orailtypes.Operation{}, completeServiceIdempotencyFailure(ctx, s.IdempotencyStore, reservation.Reference(), serviceErr)
		}
	}

	enqueuedAt := s.Clock().UTC()
	canonical, err := createCanonicalDeployment(
		ctx,
		s.DeploymentStore,
		svc,
		intake,
		op.ID,
		acceptedDeploymentIntentKey(op.ID),
	)
	if err != nil {
		failDeployIntakeOperationCtx(ctx, s.OperationStore, op.ID, "INTERNAL_ERROR", "canonical deployment create failed")
		serviceErr := &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "canonical deployment create failed"}
		return orailtypes.Operation{}, completeServiceIdempotencyFailure(ctx, s.IdempotencyStore, reservation.Reference(), serviceErr)
	}
	intake.DeploymentID = canonical.ID
	intake.InstanceID = canonical.ID + "-r0"
	intake.PreviousActive = resolvePreviousActiveDeployment(ctx, s.DeploymentStore, svc.ID, intake.EnvironmentID, canonical.ID)
	payload, err := buildDeployJobPayload(op.ID, svc, intake, enqueuedAt)
	if err != nil {
		serviceErr := &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "deploy job payload encode failed"}
		return orailtypes.Operation{}, completeServiceIdempotencyFailure(ctx, s.IdempotencyStore, reservation.Reference(), serviceErr)
	}
	job, err := s.QueueStore.Enqueue(ctx, buildDeployEnqueueRequest(op.ID, svc, enqueuedAt, payload))
	if err != nil {
		failDeployIntakeOperationCtx(ctx, s.OperationStore, op.ID, "DEPLOY_JOB_ENQUEUE_FAILED", "deploy job enqueue failed")
		_ = s.DeploymentStore.FailDeployment(ctx, canonical.ID, deploy.DeploymentQueued, "DEPLOY_JOB_ENQUEUE_FAILED", "deploy job enqueue failed")
		serviceErr := &ServiceError{Status: http.StatusInternalServerError, Code: "DEPLOY_JOB_ENQUEUE_FAILED", Message: "deploy job enqueue failed"}
		return orailtypes.Operation{}, completeServiceIdempotencyFailure(ctx, s.IdempotencyStore, reservation.Reference(), serviceErr)
	}
	_, _ = s.OperationStore.AppendEvent(ctx, OperationEvent{
		OperationID: op.ID,
		State:       op.State,
		Message:     "deploy job enqueued: job_id=" + job.ID + " operation_id=" + string(op.ID),
		CreatedAt:   enqueuedAt,
	})
	_, _ = s.OperationStore.AppendEvent(ctx, OperationEvent{
		OperationID: op.ID,
		State:       op.State,
		Message:     deployIntakeQueuedEventMessage(svc, intake, job),
		CreatedAt:   enqueuedAt,
	})
	if key != "" && s.IdempotencyStore != nil {
		stored, err := storedServiceJSON(http.StatusAccepted, op)
		if err != nil {
			return orailtypes.Operation{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency response encode failed"}
		}
		if _, err := s.IdempotencyStore.CompleteSuccess(ctx, reservation.Reference(), stored); err != nil {
			return orailtypes.Operation{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency completion failed"}
		}
	}
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "operation",
			ResourceID:   string(op.ID),
			ProjectID:    string(svc.ProjectID),
			Action:       "deploy.intake.accept",
			After: map[string]any{
				"service_id":          string(svc.ID),
				"project_id":          string(svc.ProjectID),
				"queue":               string(queue.Deploy),
				"job_id":              job.ID,
				"operation_id":        string(op.ID),
				"parent_operation_id": string(op.ID),
				"operation_kind":      op.Kind,
				"source":              "graphql",
			},
		})
	}
	return op, nil
}

// failDeployIntakeOperationCtx is the ctx-friendly companion to the
// REST handler's failDeployIntakeOperation. Mirrors that function's
// shape but takes a raw context.Context.
func failDeployIntakeOperationCtx(ctx context.Context, operationStore OperationStore, operationID orailtypes.ID, code, message string) {
	op, err := operationStore.Fail(ctx, operationID, orailtypes.FailureCode(code), message)
	if err != nil {
		return
	}
	_, _ = operationStore.AppendEvent(ctx, OperationEvent{OperationID: op.ID, State: op.State, Message: message})
}

// ListOperationEventsService returns the durable event log for an
// operation. Used by GraphQL deployment.logs to surface tail logs.
func (s *ServicesAPI) ListOperationEventsService(ctx context.Context, operationID string) ([]OperationEvent, *ServiceError) {
	if s.OperationStore == nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation store not configured"}
	}
	events, err := s.OperationStore.ListEvents(ctx, orailtypes.ID(operationID))
	if errors.Is(err, ErrOperationNotFound) {
		return nil, &ServiceError{Status: http.StatusNotFound, Code: "OPERATION_NOT_FOUND", Message: "operation not found"}
	}
	if err != nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation events list failed"}
	}
	return events, nil
}

// ParseDeployImageReference is a small wrapper around registry.ParseImageReference
// used by GraphQL to surface validation errors with our typed shape.
func ParseDeployImageReference(image string) error {
	_, err := registry.ParseImageReference(image)
	if err != nil {
		return fmt.Errorf("deploy image reference: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------
// Templates (GraphQL parity for the REST /templates surface).
// -----------------------------------------------------------------------

// ListTemplatesService returns every built-in template manifest. The
// REST surface returns Summary objects; the GraphQL resolver needs the
// full manifest so it can project services + env into the schema shape.
func (s *ServicesAPI) ListTemplatesService(_ context.Context) ([]templates.Template, *ServiceError) {
	return templates.Builtins(), nil
}

// GetTemplateService returns one built-in template by slug.
func (s *ServicesAPI) GetTemplateService(_ context.Context, slug string) (templates.Template, *ServiceError) {
	tmpl, err := templates.Lookup(slug)
	if errors.Is(err, templates.ErrTemplateNotFound) {
		return templates.Template{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "template not found", Details: map[string]any{"slug": slug}}
	}
	if err != nil {
		return templates.Template{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "template lookup failed", Details: map[string]any{"slug": slug}}
	}
	return tmpl, nil
}

// TemplateDeployServiceRequest is the GraphQL-facing template deploy
// intake. It mirrors the REST TemplateDeployRequest minus the
// idempotency dance so the resolver layer can call this directly.
type TemplateDeployServiceRequest struct {
	Slug          string
	ProjectID     string
	EnvironmentID string
	Name          string
}

// TemplateDeployServiceResult is the GraphQL-facing template deploy
// response. The schema's TemplateDeployPayload projects this shape.
type TemplateDeployServiceResult struct {
	OperationID     string
	CreatedServices []Service
	Note            string
	Raw             templates.Result
}

// MaterializeTemplateService runs the same materializer the REST handler
// runs (extracted so the GraphQL gateway reuses the same business
// logic, including the audit emit). The supplied idempotency key (may
// be empty) reserves through the same idempotency.Store the REST
// surface consumes.
func (s *ServicesAPI) MaterializeTemplateService(ctx context.Context, req TemplateDeployServiceRequest, idemKey string) (TemplateDeployServiceResult, *ServiceError) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "project_id is required", Details: map[string]any{"field": "projectId"}}
	}
	if strings.TrimSpace(req.Slug) == "" {
		return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "template slug is required", Details: map[string]any{"field": "slug"}}
	}
	if s.MutationScopeResolver != nil {
		resolved, err := s.MutationScopeResolver.ResolveMutationScope(ctx, MutationScopeRequest{
			ProjectID:          req.ProjectID,
			EnvironmentID:      req.EnvironmentID,
			RequireProject:     true,
			RequireEnvironment: req.EnvironmentID != "",
		})
		if err != nil {
			return TemplateDeployServiceResult{}, mutationScopeServiceError(err)
		}
		req.ProjectID = resolved.ProjectID
	}
	tmpl, srvErr := s.GetTemplateService(ctx, req.Slug)
	if srvErr != nil {
		return TemplateDeployServiceResult{}, srvErr
	}
	if s.OperationStore == nil {
		return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation store not configured"}
	}

	// Idempotency reservation matches the REST shape so a retry across
	// either surface replays the same materialize operation.
	key := idempotency.Key(strings.TrimSpace(idemKey))
	var reservation idempotency.Reservation
	var err error
	if key != "" && s.IdempotencyStore != nil {
		body, _ := json.Marshal(map[string]string{"slug": req.Slug, "project_id": req.ProjectID, "environment_id": req.EnvironmentID})
		fingerprint := idempotency.RequestFingerprint{
			Method:     "POST",
			Path:       "/graphql/v2/template/{slug}",
			BodySHA256: idempotency.BodyHashSHA256(body),
		}
		reservation, err = s.IdempotencyStore.Reserve(ctx, key, fingerprint, 24*time.Hour)
		if errors.Is(err, idempotency.ErrInvalidKey) {
			return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "IDEMPOTENCY_KEY_INVALID", Message: "invalid idempotency key"}
		}
		if errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
			return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusConflict, Code: "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY", Message: "idempotency key reused with different body"}
		}
		if errors.Is(err, idempotency.ErrKeyReused) {
			return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusConflict, Code: "IDEMPOTENCY_KEY_REUSED", Message: "idempotency key reused with different request"}
		}
		if errors.Is(err, idempotency.ErrRequestInProgress) {
			return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusConflict, Code: "IDEMPOTENCY_REQUEST_IN_PROGRESS", Message: "idempotency request in progress"}
		}
		if err != nil {
			return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency reservation failed"}
		}
		if reservationHasExactStoredResponse(reservation) {
			stored := reservation.Response.Clone()
			if reservation.State == idempotency.StateFailed {
				return TemplateDeployServiceResult{}, replayedServiceError(stored)
			}
			var result TemplateDeployServiceResult
			if err := json.Unmarshal(stored.Body, &result); err != nil {
				return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "stored idempotency response is invalid"}
			}
			return result, nil
		}
		if reservation.OperationID != "" {
			// Replay path: we can return the operation id but recreating
			// the full created-services list is not possible without
			// re-running the materializer. Surface what we have.
			return TemplateDeployServiceResult{OperationID: reservation.OperationID}, nil
		}
	}

	op, err := CreateQueuedOperation(ctx, s.OperationStore, "template.deploy")
	if err != nil {
		serviceErr := &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "operation create failed"}
		return TemplateDeployServiceResult{}, completeServiceIdempotencyFailure(ctx, s.IdempotencyStore, reservation.Reference(), serviceErr)
	}
	if key != "" && s.IdempotencyStore != nil {
		if _, err := s.IdempotencyStore.AttachOperation(ctx, reservation.Reference(), string(op.ID)); err != nil {
			serviceErr := &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency operation attach failed"}
			return TemplateDeployServiceResult{}, completeServiceIdempotencyFailure(ctx, s.IdempotencyStore, reservation.Reference(), serviceErr)
		}
	}
	mat := &templates.Materializer{
		Services:  newServiceCreatorAdapter(s.ServiceStore, req.ProjectID),
		Variables: newVariableUpserterAdapter(s.VariableStore, req.ProjectID, req.EnvironmentID),
		Volumes:   noopVolumeAttacher{},
	}
	result, err := mat.Materialize(ctx, tmpl, req.ProjectID, req.EnvironmentID)
	if err != nil {
		_, _ = s.OperationStore.Fail(ctx, op.ID, orailtypes.FailureCode("RESOURCE_VALIDATION_FAILED"), err.Error())
		serviceErr := &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error(), Details: map[string]any{"slug": req.Slug, "operation_id": string(op.ID)}}
		return TemplateDeployServiceResult{}, completeServiceIdempotencyFailure(ctx, s.IdempotencyStore, reservation.Reference(), serviceErr)
	}

	if _, err := s.OperationStore.Transition(ctx, op.ID, orailtypes.OperationRunning); err == nil {
		_, _ = s.OperationStore.Transition(ctx, op.ID, orailtypes.OperationSucceeded)
	}
	_, _ = s.OperationStore.AppendEvent(ctx, OperationEvent{
		OperationID: op.ID,
		State:       orailtypes.OperationSucceeded,
		Message:     "template materialized: " + req.Slug,
	})

	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "template",
			ResourceID:   req.Slug,
			ProjectID:    req.ProjectID,
			Action:       "template.deploy",
			After: map[string]any{
				"slug":           req.Slug,
				"operation_id":   string(op.ID),
				"created_count":  len(result.CreatedResources),
				"environment_id": req.EnvironmentID,
				"source":         "graphql",
			},
		})
	}
	// Surface the freshly created services in the canonical Service
	// shape so the GraphQL resolver can project them without a second
	// round-trip.
	created := make([]Service, 0)
	for _, r := range result.CreatedResources {
		if r.Kind != "service" {
			continue
		}
		svc, err := s.ServiceStore.GetService(ctx, orailtypes.ID(r.ID))
		if err == nil {
			created = append(created, svc)
		}
	}
	out := TemplateDeployServiceResult{
		OperationID:     string(op.ID),
		CreatedServices: created,
		Raw:             result,
	}
	// Match the REST handler's volume-pending note when the template
	// declared at least one volume.
	for _, r := range result.CreatedResources {
		if r.Kind == "volume" {
			out.Note = "Volume rows are durable and the Docker worker mounts them at deploy time. This template materializer still does not prove runtime readiness, backup, or restore."
			break
		}
	}
	if key != "" && s.IdempotencyStore != nil {
		stored, err := storedServiceJSON(http.StatusAccepted, out)
		if err != nil {
			return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency response encode failed"}
		}
		if _, err := s.IdempotencyStore.CompleteSuccess(ctx, reservation.Reference(), stored); err != nil {
			return TemplateDeployServiceResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "idempotency completion failed"}
		}
	}
	return out, nil
}

// -----------------------------------------------------------------------
// Volumes (GraphQL parity for the REST /volumes surface).
// -----------------------------------------------------------------------

// VolumeCreateServiceRequest is the GraphQL-facing volume create intake.
type VolumeCreateServiceRequest struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	Name          string
	SizeBytes     int64
	MountPath     string
}

// VolumeResult bundles the persisted Volume together with the mount
// path the REST surface persists in the sidecar mount store.
type VolumeResult struct {
	Volume    volume.Volume
	MountPath string
}

// ListVolumesService returns every volume optionally filtered by service
// + project. Mirrors the REST GET /volumes filter.
func (s *ServicesAPI) ListVolumesService(ctx context.Context, projectID, serviceID, environmentID string) ([]VolumeResult, *ServiceError) {
	if s.VolumeStore == nil {
		return nil, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "volumes are not configured on this server"}
	}
	volumes, err := s.VolumeStore.List(ctx, volume.ListFilter{
		ProjectID:     strings.TrimSpace(projectID),
		ServiceID:     strings.TrimSpace(serviceID),
		EnvironmentID: strings.TrimSpace(environmentID),
	})
	if err != nil {
		return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "volume list failed"}
	}
	out := make([]VolumeResult, 0, len(volumes))
	for _, v := range volumes {
		mp := ""
		if s.VolumeMountStore != nil {
			mp, _ = s.VolumeMountStore.GetMountPath(ctx, v.ID)
		}
		out = append(out, VolumeResult{Volume: v, MountPath: mp})
	}
	return out, nil
}

// GetVolumeService returns one volume by id.
func (s *ServicesAPI) GetVolumeService(ctx context.Context, id string) (VolumeResult, *ServiceError) {
	if s.VolumeStore == nil {
		return VolumeResult{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "volumes are not configured on this server"}
	}
	v, err := s.VolumeStore.Get(ctx, id)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		return VolumeResult{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "volume not found", Details: map[string]any{"volume_id": id}}
	}
	if err != nil {
		return VolumeResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "volume lookup failed"}
	}
	mp := ""
	if s.VolumeMountStore != nil {
		mp, _ = s.VolumeMountStore.GetMountPath(ctx, v.ID)
	}
	return VolumeResult{Volume: v, MountPath: mp}, nil
}

// CreateVolumeService runs the same validation + store insert + audit
// emit the REST POST /volumes handler runs.
func (s *ServicesAPI) CreateVolumeService(ctx context.Context, req VolumeCreateServiceRequest, _ string) (VolumeResult, *ServiceError) {
	if s.VolumeStore == nil {
		return VolumeResult{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "volumes are not configured on this server"}
	}
	if strings.TrimSpace(req.ServiceID) == "" {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "service_id is required", Details: map[string]any{"field": "serviceId"}}
	}
	if strings.TrimSpace(req.EnvironmentID) == "" {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "environment_id is required", Details: map[string]any{"field": "environmentId"}}
	}
	if strings.TrimSpace(req.Name) == "" {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "name is required", Details: map[string]any{"field": "name"}}
	}
	if req.SizeBytes < 0 {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "size_bytes must be non-negative", Details: map[string]any{"field": "sizeBytes"}}
	}
	if strings.TrimSpace(req.MountPath) == "" {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "mount_path is required", Details: map[string]any{"field": "mountPath"}}
	}
	if !strings.HasPrefix(req.MountPath, "/") {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "mount_path must be absolute", Details: map[string]any{"field": "mountPath"}}
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if s.MutationScopeResolver != nil {
		resolved, err := s.MutationScopeResolver.ResolveMutationScope(ctx, MutationScopeRequest{
			ProjectID:          projectID,
			ServiceID:          req.ServiceID,
			EnvironmentID:      req.EnvironmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			return VolumeResult{}, mutationScopeServiceError(err)
		}
		projectID = resolved.ProjectID
	} else if projectID == "" {
		// Inherit the service's project so the GraphQL caller does not
		// need to repeat the redundant lookup.
		svc, err := s.ServiceStore.GetService(ctx, orailtypes.ID(req.ServiceID))
		if err == nil {
			projectID = string(svc.ProjectID)
		}
	}
	v, err := s.VolumeStore.Create(ctx, volume.VolumeCreateInput{
		ProjectID:     projectID,
		EnvironmentID: req.EnvironmentID,
		ServiceID:     req.ServiceID,
		Name:          req.Name,
		SizeBytes:     req.SizeBytes,
		MountPath:     req.MountPath,
	})
	if errors.Is(err, volume.ErrVolumeConflict) {
		return VolumeResult{}, &ServiceError{Status: http.StatusConflict, Code: "RESOURCE_CONFLICT", Message: err.Error()}
	}
	if errors.Is(err, volume.ErrVolumeInvalid) {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error()}
	}
	if err != nil {
		return VolumeResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "volume create failed"}
	}
	if s.VolumeMountStore != nil {
		_ = s.VolumeMountStore.SetMountPath(ctx, v.ID, req.MountPath)
	}
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "volume",
			ResourceID:   v.ID,
			ProjectID:    v.ProjectID,
			Action:       "volume.create",
			After: map[string]any{
				"name":           v.Name,
				"service_id":     v.ServiceID,
				"environment_id": v.EnvironmentID,
				"size_bytes":     v.SizeBytes,
				"mount_path":     req.MountPath,
				"source":         "graphql",
			},
		})
	}
	return VolumeResult{Volume: v, MountPath: req.MountPath}, nil
}

// ResizeVolumeService grows an existing volume. Shrinking returns
// VOLUME_RESIZE_DOWN_UNSUPPORTED per Railway's parity contract.
func (s *ServicesAPI) ResizeVolumeService(ctx context.Context, id string, newSize int64) (VolumeResult, *ServiceError) {
	if s.VolumeStore == nil {
		return VolumeResult{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "volumes are not configured on this server"}
	}
	if newSize <= 0 {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "size_bytes must be positive", Details: map[string]any{"field": "sizeBytes"}}
	}
	before, err := s.VolumeStore.Get(ctx, id)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		return VolumeResult{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "volume not found", Details: map[string]any{"volume_id": id}}
	}
	if err != nil {
		return VolumeResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "volume lookup failed"}
	}
	if s.MutationScopeResolver != nil {
		_, err := s.MutationScopeResolver.ResolveMutationScope(ctx, MutationScopeRequest{
			ProjectID:          before.ProjectID,
			ServiceID:          before.ServiceID,
			EnvironmentID:      before.EnvironmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			return VolumeResult{}, mutationScopeServiceError(err)
		}
	}
	updated, err := s.VolumeStore.Resize(ctx, id, newSize)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		return VolumeResult{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "volume not found", Details: map[string]any{"volume_id": id}}
	}
	if errors.Is(err, volume.ErrVolumeResizeDownUnsupported) {
		return VolumeResult{}, &ServiceError{Status: http.StatusConflict, Code: "VOLUME_RESIZE_DOWN_UNSUPPORTED", Message: "volume resize down is not supported", Details: map[string]any{
			"volume_id":      id,
			"current_size":   before.SizeBytes,
			"requested_size": newSize,
		}}
	}
	if errors.Is(err, volume.ErrVolumeInvalid) {
		return VolumeResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error()}
	}
	if err != nil {
		return VolumeResult{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "volume resize failed"}
	}
	mp := ""
	if s.VolumeMountStore != nil {
		mp, _ = s.VolumeMountStore.GetMountPath(ctx, updated.ID)
	}
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "volume",
			ResourceID:   updated.ID,
			ProjectID:    updated.ProjectID,
			Action:       "volume.resize",
			Before:       map[string]any{"size_bytes": before.SizeBytes},
			After:        map[string]any{"size_bytes": updated.SizeBytes, "source": "graphql"},
		})
	}
	return VolumeResult{Volume: updated, MountPath: mp}, nil
}

// DeleteVolumeService soft-deletes a volume. Rejects with VOLUME_ATTACHED
// when an active attachment exists.
func (s *ServicesAPI) DeleteVolumeService(ctx context.Context, id string) *ServiceError {
	if s.VolumeStore == nil {
		return &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "volumes are not configured on this server"}
	}
	before, err := s.VolumeStore.Get(ctx, id)
	if errors.Is(err, volume.ErrVolumeNotFound) {
		return &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "volume not found", Details: map[string]any{"volume_id": id}}
	}
	if err != nil {
		return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "volume lookup failed"}
	}
	if s.MutationScopeResolver != nil {
		_, err := s.MutationScopeResolver.ResolveMutationScope(ctx, MutationScopeRequest{
			ProjectID:          before.ProjectID,
			ServiceID:          before.ServiceID,
			EnvironmentID:      before.EnvironmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			return mutationScopeServiceError(err)
		}
	}
	if err := s.VolumeStore.Delete(ctx, id); err != nil {
		if errors.Is(err, volume.ErrVolumeAttached) {
			return &ServiceError{Status: http.StatusConflict, Code: "VOLUME_ATTACHED", Message: "volume has active attachment", Details: map[string]any{"volume_id": id}}
		}
		if errors.Is(err, volume.ErrVolumeNotFound) {
			return &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "volume not found", Details: map[string]any{"volume_id": id}}
		}
		return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "volume delete failed"}
	}
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "volume",
			ResourceID:   id,
			ProjectID:    before.ProjectID,
			Action:       "volume.delete",
			Before:       map[string]any{"status": string(before.Status)},
			After:        map[string]any{"status": string(volume.StatusSoftDeleted), "source": "graphql"},
		})
	}
	return nil
}

// -----------------------------------------------------------------------
// GitHub link (GraphQL parity for the REST /services/{id}/github surface).
// -----------------------------------------------------------------------

// GithubLinkSpecForService returns the redaction-safe github link
// recorded on a service's spec, if any. The boolean is false when no
// link is present.
func (s *ServicesAPI) GithubLinkSpecForService(ctx context.Context, serviceID string) (github.ServiceLink, bool, *ServiceError) {
	svc, err := s.ServiceStore.GetService(ctx, orailtypes.ID(serviceID))
	if errors.Is(err, ErrResourceNotFound) {
		return github.ServiceLink{}, false, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "service not found", Details: map[string]any{"service_id": serviceID}}
	}
	if err != nil {
		return github.ServiceLink{}, false, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service lookup failed"}
	}
	link, ok := readGitHubLink(svc)
	return link, ok, nil
}

// GithubLinkServiceRequest mirrors githubLinkBody so the GraphQL
// resolver can pass through the validated payload without importing
// the REST handler's private types.
type GithubLinkServiceRequest struct {
	ServiceID      string
	InstallationID int64
	Repo           string
	Branch         string
	WatchPaths     []string
	RootDirectory  string
	ConfigPath     string
	CloneURL       string
	WaitForCI      bool
	RequiredChecks []string
}

// LinkGithubService writes the supplied github link into
// service.spec.github. Returns ServiceError(CAPABILITY_UNAVAILABLE) when the
// ServiceStore does not satisfy ServiceSpecStore (memory + Postgres
// implementations both do).
func (s *ServicesAPI) LinkGithubService(ctx context.Context, req GithubLinkServiceRequest) (Service, *ServiceError) {
	specStore, ok := s.ServiceStore.(ServiceSpecStore)
	if !ok {
		return Service{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "service store does not support spec mutations"}
	}
	link := github.ServiceLink{
		InstallationID: req.InstallationID,
		Repo:           req.Repo,
		Branch:         req.Branch,
		WatchPaths:     req.WatchPaths,
		RootDirectory:  req.RootDirectory,
		ConfigPath:     req.ConfigPath,
		CloneURL:       req.CloneURL,
		WaitForCI:      req.WaitForCI,
		RequiredChecks: req.RequiredChecks,
	}.Normalize()
	if err := link.Validate(); err != nil {
		return Service{}, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: err.Error(), Details: map[string]any{"service_id": req.ServiceID}}
	}
	svc, err := specStore.UpdateServiceSpec(ctx, orailtypes.ID(req.ServiceID), func(spec map[string]any) (map[string]any, error) {
		if spec == nil {
			spec = map[string]any{}
		}
		spec["github"] = encodeLink(link)
		return spec, nil
	})
	if errors.Is(err, ErrResourceNotFound) {
		return Service{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "service not found", Details: map[string]any{"service_id": req.ServiceID}}
	}
	if err != nil {
		return Service{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service spec update failed"}
	}
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "service",
			ResourceID:   string(svc.ID),
			ProjectID:    string(svc.ProjectID),
			Action:       "service.github.link",
			After: map[string]any{
				"repo":            link.Repo,
				"branch":          link.Branch,
				"installation_id": link.InstallationID,
				"watch_paths":     link.WatchPaths,
				"root_directory":  link.RootDirectory,
				"config_path":     link.ConfigPath,
				"wait_for_ci":     link.WaitForCI,
				"source":          "graphql",
			},
		})
	}
	return svc, nil
}

// UnlinkGithubService clears service.spec.github.
func (s *ServicesAPI) UnlinkGithubService(ctx context.Context, serviceID string) (Service, *ServiceError) {
	specStore, ok := s.ServiceStore.(ServiceSpecStore)
	if !ok {
		return Service{}, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "service store does not support spec mutations"}
	}
	had := false
	svc, err := specStore.UpdateServiceSpec(ctx, orailtypes.ID(serviceID), func(spec map[string]any) (map[string]any, error) {
		if _, present := spec["github"]; present {
			had = true
			delete(spec, "github")
		}
		return spec, nil
	})
	if errors.Is(err, ErrResourceNotFound) {
		return Service{}, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "service not found", Details: map[string]any{"service_id": serviceID}}
	}
	if err != nil {
		return Service{}, &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "service spec update failed"}
	}
	if !had {
		// Match REST: the absence of a prior link is a 404 so retries
		// surface a clear no-op. The GraphQL resolver maps this to a
		// typed error the caller can ignore.
		return svc, &ServiceError{Status: http.StatusNotFound, Code: "RESOURCE_NOT_FOUND", Message: "service has no github link", Details: map[string]any{"service_id": serviceID}}
	}
	if s.AuditEmitter != nil {
		s.AuditEmitter.emit(ctx, audit.Row{
			ResourceKind: "service",
			ResourceID:   string(svc.ID),
			ProjectID:    string(svc.ProjectID),
			Action:       "service.github.unlink",
			After:        map[string]any{"source": "graphql"},
		})
	}
	return svc, nil
}

// ListGithubInstallationsService surfaces the App's installation list.
func (s *ServicesAPI) ListGithubInstallationsService(ctx context.Context) ([]github.Installation, *ServiceError) {
	if s.GitHubApp == nil {
		return nil, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "github app accessor not configured"}
	}
	out, err := s.GitHubApp.ListInstallations(ctx)
	if err != nil {
		return nil, &ServiceError{Status: http.StatusBadGateway, Code: "INTERNAL_ERROR", Message: "github installations fetch failed", Details: map[string]any{"error": err.Error()}}
	}
	return out, nil
}

// ListGithubInstallationReposService surfaces the repos visible to one
// installation.
func (s *ServicesAPI) ListGithubInstallationReposService(ctx context.Context, installationID int64) ([]github.Repo, *ServiceError) {
	if s.GitHubApp == nil {
		return nil, &ServiceError{Status: http.StatusServiceUnavailable, Code: "CAPABILITY_UNAVAILABLE", Message: "github app accessor not configured"}
	}
	if installationID <= 0 {
		return nil, &ServiceError{Status: http.StatusBadRequest, Code: "RESOURCE_VALIDATION_FAILED", Message: "installation id invalid", Details: map[string]any{"installation_id": installationID}}
	}
	out, err := s.GitHubApp.ListReposForInstallation(ctx, installationID)
	if err != nil {
		return nil, &ServiceError{Status: http.StatusBadGateway, Code: "INTERNAL_ERROR", Message: "github installation repos fetch failed", Details: map[string]any{"installation_id": installationID, "error": err.Error()}}
	}
	return out, nil
}
