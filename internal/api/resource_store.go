// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/pkg/orailtypes"
)

// DefaultWorkspaceID is the explicit migration/bootstrap tenant assigned to
// pre-RBAC resources. Callers must opt into it; stores never silently infer a
// tenant for an unscoped write.
const DefaultWorkspaceID = "wrk_default"

// ErrResourceNotFound is returned by the resource stores when a lookup
// misses.
var ErrResourceNotFound = errors.New("resource not found")

// ErrResourceConflict is returned when a unique constraint is violated.
var ErrResourceConflict = errors.New("resource conflict")

// ErrResourcePreconditionFailed is returned when an atomic resource-version
// compare-and-swap observes a stale validator.
var ErrResourcePreconditionFailed = errors.New("resource precondition failed")

// ErrResourceInvalid is returned when a resource fails validation at the
// store boundary (the handler layer is the canonical validator).
var ErrResourceInvalid = errors.New("resource invalid")

// Project is the public Phase 0 representation of an `prj_*` resource.
type Project struct {
	ID          orailtypes.ID `json:"id"`
	WorkspaceID string        `json:"workspace_id"`
	Slug        string        `json:"slug"`
	DisplayName string        `json:"display_name"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Service is the public Phase 0 representation of an `svc_*` resource.
type Service struct {
	ID          orailtypes.ID `json:"id"`
	ProjectID   orailtypes.ID `json:"project_id"`
	Slug        string        `json:"slug"`
	DisplayName string        `json:"display_name"`
	// ResourceVersion is the strong validator a conditional mutation must
	// present in If-Match. It starts at 1 and increases on every write the
	// store makes to the row, including the delete tombstone.
	ResourceVersion int64     `json:"resource_version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// Spec is the panel-locked per-service free-form JSONB blob mirrored
	// from the `services.spec` column. Subkeys are owned by the surface
	// that writes them (e.g. `github` for the GitHub-App auto-deploy
	// link in ADR 0022). The API layer is the canonical validator for
	// each subkey; the store treats Spec as opaque storage.
	Spec map[string]any `json:"spec,omitempty"`
}

// ResourceListFilter narrows a list query.
type ResourceListFilter struct {
	ParentID orailtypes.ID
}

// ProjectStore is the in-memory contract for prj_* resources.
type ProjectStore interface {
	CreateProject(ctx context.Context, project Project) (Project, error)
	GetProject(ctx context.Context, id orailtypes.ID) (Project, error)
	ListProjects(ctx context.Context) ([]Project, error)
}

// ServiceStore is the in-memory contract for svc_* resources.
type ServiceStore interface {
	CreateService(ctx context.Context, service Service) (Service, error)
	GetService(ctx context.Context, id orailtypes.ID) (Service, error)
	ListServices(ctx context.Context, filter ResourceListFilter) ([]Service, error)
}

// ConditionalServiceStore is the optional extension a ServiceStore implements
// when it can delete a service under an exact resource-version precondition.
// The handler discovers this capability at runtime and reports the delete as
// unavailable when the installed store does not provide it.
type ConditionalServiceStore interface {
	ServiceStore
	DeleteServiceCAS(ctx context.Context, id orailtypes.ID, expectedVersion int64) (ServiceDeletionReceipt, error)
}

// ServiceSpecStore is the optional extension a ServiceStore implements
// when it can persist per-service Spec mutations. The github-link
// handler discovers this capability at runtime and 501s when missing.
type ServiceSpecStore interface {
	ServiceStore
	UpdateServiceSpec(ctx context.Context, id orailtypes.ID, mutate func(map[string]any) (map[string]any, error)) (Service, error)
}

// ServiceLookupBySpec is the optional capability the GitHub-App webhook
// router consumes to resolve `(repo, branch)` to linked services. The
// in-memory store implements it with a linear scan; the Postgres store
// will implement it with an index on the JSONB key path (see
// migration 0011_service_github_index.sql).
type ServiceLookupBySpec interface {
	FindServicesByGitHubLink(ctx context.Context, repo, branch string) ([]Service, error)
}

// MemoryResourceStore is the deterministic in-memory implementation of both
// stores. IDs increment monotonically so list responses are stable across
// reads, satisfying the cursor-pagination ordering invariant.
type MemoryResourceStore struct {
	mu        sync.Mutex
	projects  []Project
	projectIx map[orailtypes.ID]int
	services  []Service
	serviceIx map[orailtypes.ID]int
	nextProj  uint64
	nextSvc   uint64
	now       func() time.Time
}

// NewMemoryResourceStore returns an empty MemoryResourceStore using
// time.Now for timestamping.
func NewMemoryResourceStore() *MemoryResourceStore {
	return NewMemoryResourceStoreWithClock(time.Now)
}

// NewMemoryResourceStoreWithClock supports deterministic tests.
func NewMemoryResourceStoreWithClock(now func() time.Time) *MemoryResourceStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryResourceStore{
		projectIx: make(map[orailtypes.ID]int),
		serviceIx: make(map[orailtypes.ID]int),
		now:       now,
	}
}

// CreateProject inserts a new project, allocating a monotonic prj_* id when
// the caller leaves project.ID empty.
func (s *MemoryResourceStore) CreateProject(ctx context.Context, project Project) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	if project.Slug == "" {
		return Project{}, fmt.Errorf("%w: project slug is required", ErrResourceInvalid)
	}
	if project.WorkspaceID == "" {
		return Project{}, fmt.Errorf("%w: project workspace_id is required", ErrResourceInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.projects {
		if existing.WorkspaceID == project.WorkspaceID && existing.Slug == project.Slug {
			return Project{}, fmt.Errorf("%w: project slug %q already exists in workspace %q", ErrResourceConflict, project.Slug, project.WorkspaceID)
		}
	}
	if project.ID == "" {
		s.nextProj++
		project.ID = orailtypes.ID(fmt.Sprintf("%s_%016x", orailtypes.IDPrefixProject, s.nextProj))
	}
	if _, exists := s.projectIx[project.ID]; exists {
		return Project{}, fmt.Errorf("%w: project id %q already exists", ErrResourceConflict, project.ID)
	}
	if !strings.HasPrefix(string(project.ID), string(orailtypes.IDPrefixProject)+"_") {
		return Project{}, fmt.Errorf("%w: project id %q must use the %s_ prefix", ErrResourceInvalid, project.ID, orailtypes.IDPrefixProject)
	}
	now := s.now().UTC()
	if project.CreatedAt.IsZero() {
		project.CreatedAt = now
	}
	project.UpdatedAt = now
	s.projectIx[project.ID] = len(s.projects)
	s.projects = append(s.projects, project)
	return project, nil
}

// GetProject returns a project by id.
func (s *MemoryResourceStore) GetProject(ctx context.Context, id orailtypes.ID) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.projectIx[id]
	if !ok {
		return Project{}, ErrResourceNotFound
	}
	return s.projects[idx], nil
}

// ListProjects returns the full project list in monotonic creation order.
func (s *MemoryResourceStore) ListProjects(ctx context.Context) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Project, len(s.projects))
	copy(out, s.projects)
	return out, nil
}

// CreateService inserts a new service. The caller may supply a parent project id;
// when blank, the service is created without a parent (Phase 0 placeholder).
func (s *MemoryResourceStore) CreateService(ctx context.Context, service Service) (Service, error) {
	if err := ctx.Err(); err != nil {
		return Service{}, err
	}
	if service.Slug == "" {
		return Service{}, fmt.Errorf("%w: service slug is required", ErrResourceInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if service.ProjectID != "" {
		if _, ok := s.projectIx[service.ProjectID]; !ok {
			return Service{}, fmt.Errorf("%w: project %q not found", ErrResourceInvalid, service.ProjectID)
		}
	}
	for _, existing := range s.services {
		if existing.ProjectID == service.ProjectID && existing.Slug == service.Slug {
			return Service{}, fmt.Errorf("%w: service slug %q already exists in project %q", ErrResourceConflict, service.Slug, service.ProjectID)
		}
	}
	if service.ID == "" {
		s.nextSvc++
		service.ID = orailtypes.ID(fmt.Sprintf("%s_%016x", orailtypes.IDPrefixService, s.nextSvc))
	}
	if _, exists := s.serviceIx[service.ID]; exists {
		return Service{}, fmt.Errorf("%w: service id %q already exists", ErrResourceConflict, service.ID)
	}
	if !strings.HasPrefix(string(service.ID), string(orailtypes.IDPrefixService)+"_") {
		return Service{}, fmt.Errorf("%w: service id %q must use the %s_ prefix", ErrResourceInvalid, service.ID, orailtypes.IDPrefixService)
	}
	now := s.now().UTC()
	if service.CreatedAt.IsZero() {
		service.CreatedAt = now
	}
	service.UpdatedAt = now
	service.ResourceVersion = 1
	s.serviceIx[service.ID] = len(s.services)
	s.services = append(s.services, service)
	return service, nil
}

// DeleteServiceCAS removes a service and reports what went with it. The
// in-memory store owns no variables, volumes, routes, or configuration, so the
// receipt honestly reports zero for each of those; the Postgres store is the
// only implementation that can report real counts.
func (s *MemoryResourceStore) DeleteServiceCAS(ctx context.Context, id orailtypes.ID, expectedVersion int64) (ServiceDeletionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return ServiceDeletionReceipt{}, err
	}
	if expectedVersion < 1 {
		return ServiceDeletionReceipt{}, ErrResourcePreconditionFailed
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.serviceIx[id]
	if !ok {
		return ServiceDeletionReceipt{}, ErrResourceNotFound
	}
	current := s.services[idx]
	if current.ResourceVersion != expectedVersion {
		return ServiceDeletionReceipt{}, ErrResourcePreconditionFailed
	}
	s.services = append(s.services[:idx], s.services[idx+1:]...)
	delete(s.serviceIx, id)
	for position := idx; position < len(s.services); position++ {
		s.serviceIx[s.services[position].ID] = position
	}
	return ServiceDeletionReceipt{
		ServiceID: string(current.ID),
		ProjectID: string(current.ProjectID),
		Slug:      current.Slug,
		DeletedAt: s.now().UTC(),
	}, nil
}

// GetService returns a service by id.
func (s *MemoryResourceStore) GetService(ctx context.Context, id orailtypes.ID) (Service, error) {
	if err := ctx.Err(); err != nil {
		return Service{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.serviceIx[id]
	if !ok {
		return Service{}, ErrResourceNotFound
	}
	return s.services[idx], nil
}

// UpdateServiceSpec applies mutate to a copy of the service's Spec
// and persists the result. The mutate function MAY return nil to leave
// the spec unchanged, or a new map to replace it; an error short-
// circuits the persist.
func (s *MemoryResourceStore) UpdateServiceSpec(ctx context.Context, id orailtypes.ID, mutate func(map[string]any) (map[string]any, error)) (Service, error) {
	if err := ctx.Err(); err != nil {
		return Service{}, err
	}
	if mutate == nil {
		return Service{}, fmt.Errorf("%w: mutate is nil", ErrResourceInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.serviceIx[id]
	if !ok {
		return Service{}, ErrResourceNotFound
	}
	current := s.services[idx]
	specCopy := map[string]any{}
	for k, v := range current.Spec {
		specCopy[k] = v
	}
	next, err := mutate(specCopy)
	if err != nil {
		return Service{}, err
	}
	if next != nil {
		current.Spec = next
	}
	current.UpdatedAt = s.now().UTC()
	current.ResourceVersion++
	s.services[idx] = current
	return current, nil
}

// FindServicesByGitHubLink returns every service whose
// spec.github.repo and spec.github.branch match. Implements
// ServiceLookupBySpec.
func (s *MemoryResourceStore) FindServicesByGitHubLink(ctx context.Context, repo, branch string) ([]Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Service, 0, 4)
	for _, svc := range s.services {
		if svc.Spec == nil {
			continue
		}
		gh, ok := svc.Spec["github"].(map[string]any)
		if !ok {
			continue
		}
		if r, _ := gh["repo"].(string); r != repo {
			continue
		}
		if b, _ := gh["branch"].(string); b != branch {
			continue
		}
		out = append(out, svc)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListServices returns the services in monotonic creation order, optionally
// filtered by parent project id.
func (s *MemoryResourceStore) ListServices(ctx context.Context, filter ResourceListFilter) ([]Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Service, 0, len(s.services))
	for _, svc := range s.services {
		if filter.ParentID != "" && svc.ProjectID != filter.ParentID {
			continue
		}
		out = append(out, svc)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
