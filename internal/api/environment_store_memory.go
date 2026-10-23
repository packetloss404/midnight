package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/midnight/midnight/pkg/orailtypes"
)

var errDefaultProductionEnvironmentCreate = errors.New("default production environment create failed")

// MemoryEnvironmentStore is the development/test EnvironmentStore. It keeps
// the same project-scoped uniqueness and deterministic ordering contract as
// the Postgres adapter.
type MemoryEnvironmentStore struct {
	mu     sync.Mutex
	items  map[orailtypes.ID]Environment
	nextID uint64
	now    func() time.Time
}

func NewMemoryEnvironmentStore(now func() time.Time) *MemoryEnvironmentStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryEnvironmentStore{items: make(map[orailtypes.ID]Environment), now: now}
}

func (s *MemoryEnvironmentStore) CreateEnvironment(ctx context.Context, env Environment) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	if env.ProjectID == "" || strings.TrimSpace(env.Slug) == "" {
		return Environment{}, fmt.Errorf("%w: environment requires project_id and slug", ErrResourceInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.items {
		if existing.ProjectID == env.ProjectID && existing.Slug == env.Slug {
			return Environment{}, fmt.Errorf("%w: environment slug %q already exists in project %q", ErrResourceConflict, env.Slug, env.ProjectID)
		}
	}
	if env.ID == "" {
		s.nextID++
		env.ID = orailtypes.ID(fmt.Sprintf("%s_%016x", orailtypes.IDPrefixEnvironment, s.nextID))
	}
	if _, exists := s.items[env.ID]; exists {
		return Environment{}, fmt.Errorf("%w: environment id %q already exists", ErrResourceConflict, env.ID)
	}
	if !strings.HasPrefix(string(env.ID), string(orailtypes.IDPrefixEnvironment)+"_") {
		return Environment{}, fmt.Errorf("%w: environment id %q must use the %s_ prefix", ErrResourceInvalid, env.ID, orailtypes.IDPrefixEnvironment)
	}
	if env.Name == "" {
		env.Name = env.Slug
	}
	if env.Tier == "" {
		env.Tier = "development"
	}
	now := s.now().UTC()
	if env.CreatedAt.IsZero() {
		env.CreatedAt = now
	}
	env.UpdatedAt = now
	env.ResourceVersion = 1
	s.items[env.ID] = env
	return env, nil
}

func (s *MemoryEnvironmentStore) UpdateEnvironment(ctx context.Context, env Environment) (Environment, error) {
	return s.updateEnvironment(ctx, env, nil)
}

func (s *MemoryEnvironmentStore) UpdateEnvironmentCAS(ctx context.Context, env Environment, expectedVersion int64) (Environment, error) {
	return s.updateEnvironment(ctx, env, &expectedVersion)
}

func (s *MemoryEnvironmentStore) updateEnvironment(ctx context.Context, env Environment, expectedVersion *int64) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	if env.ID == "" || strings.TrimSpace(env.Slug) == "" || strings.TrimSpace(env.Name) == "" ||
		utf8.RuneCountInString(strings.TrimSpace(env.Name)) > 200 || !validEnvironmentTier(env.Tier) {
		return Environment{}, fmt.Errorf("%w: environment id, slug, name, and valid tier are required", ErrResourceInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.items[env.ID]
	if !ok {
		return Environment{}, ErrResourceNotFound
	}
	if expectedVersion != nil && (*expectedVersion < 1 || existing.ResourceVersion != *expectedVersion) {
		return Environment{}, ErrResourcePreconditionFailed
	}
	for id, other := range s.items {
		if id != env.ID && other.ProjectID == existing.ProjectID && other.Slug == env.Slug {
			return Environment{}, fmt.Errorf("%w: environment slug %q already exists", ErrResourceConflict, env.Slug)
		}
	}
	existing.Slug, existing.Name, existing.Tier, existing.IsProtected = env.Slug, env.Name, env.Tier, env.IsProtected
	existing.UpdatedAt = s.now().UTC()
	existing.ResourceVersion++
	s.items[env.ID] = existing
	return existing, nil
}

func (s *MemoryEnvironmentStore) DeleteEnvironment(ctx context.Context, id orailtypes.ID) error {
	return s.deleteEnvironment(ctx, id, nil)
}

func (s *MemoryEnvironmentStore) DeleteEnvironmentCAS(ctx context.Context, id orailtypes.ID, expectedVersion int64) error {
	return s.deleteEnvironment(ctx, id, &expectedVersion)
}

func (s *MemoryEnvironmentStore) deleteEnvironment(ctx context.Context, id orailtypes.ID, expectedVersion *int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	env, ok := s.items[id]
	if !ok {
		return ErrResourceNotFound
	}
	if expectedVersion != nil && (*expectedVersion < 1 || env.ResourceVersion != *expectedVersion) {
		return ErrResourcePreconditionFailed
	}
	if env.IsProtected {
		return fmt.Errorf("%w: protected environment cannot be deleted", ErrResourceConflict)
	}
	delete(s.items, id)
	return nil
}

func (s *MemoryEnvironmentStore) GetEnvironment(ctx context.Context, id orailtypes.ID) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	env, ok := s.items[id]
	if !ok {
		return Environment{}, ErrResourceNotFound
	}
	return env, nil
}

func (s *MemoryEnvironmentStore) ListEnvironments(ctx context.Context, filter ResourceListFilter) ([]Environment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Environment, 0, len(s.items))
	for _, env := range s.items {
		if filter.ParentID != "" && env.ProjectID != filter.ParentID {
			continue
		}
		out = append(out, env)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func createDefaultProductionEnvironment(ctx context.Context, store EnvironmentStore, project Project) (Environment, error) {
	if store == nil {
		return Environment{}, fmt.Errorf("default production environment: environment store is not configured")
	}
	return store.CreateEnvironment(ctx, Environment{
		ProjectID: project.ID,
		Slug:      "production",
		Name:      "Production",
		Tier:      "production",
	})
}

type projectDefaultEnvironmentCreator interface {
	CreateProjectWithDefaultEnvironment(context.Context, Project) (Project, Environment, error)
}

// createProjectWithDefaultEnvironment selects the single-transaction durable
// seam only when both compatibility stores resolve to the same Postgres
// authority. Memory/custom stores retain their existing two-call behavior.
func createProjectWithDefaultEnvironment(
	ctx context.Context,
	projects ProjectStore,
	environments EnvironmentStore,
	project Project,
) (Project, error) {
	if creator, ok := sharedProjectDefaultEnvironmentCreator(projects, environments); ok {
		created, _, err := creator.CreateProjectWithDefaultEnvironment(ctx, project)
		return created, err
	}
	created, err := projects.CreateProject(ctx, project)
	if err != nil {
		return Project{}, err
	}
	if _, err := createDefaultProductionEnvironment(ctx, environments, created); err != nil {
		return Project{}, fmt.Errorf("%w: %v", errDefaultProductionEnvironmentCreate, err)
	}
	return created, nil
}

func sharedProjectDefaultEnvironmentCreator(
	projects ProjectStore,
	environments EnvironmentStore,
) (projectDefaultEnvironmentCreator, bool) {
	projectBase := projects
	if tenant, ok := projects.(tenantProjectStore); ok {
		projectBase = tenant.base
	}
	environmentBase := environments
	if tenant, ok := environments.(tenantEnvironmentStore); ok {
		environmentBase = tenant.base
	}
	projectResources, projectOK := projectBase.(*PostgresResourceStore)
	environmentResources, environmentOK := environmentBase.(*PostgresResourceStore)
	if !projectOK || !environmentOK || projectResources != environmentResources {
		return nil, false
	}
	return projectResources, true
}

func ensureProductionEnvironment(ctx context.Context, store EnvironmentStore, project Project) (Environment, error) {
	if store == nil {
		return Environment{}, fmt.Errorf("production environment: environment store is not configured")
	}
	environments, err := store.ListEnvironments(ctx, ResourceListFilter{ParentID: project.ID})
	if err != nil {
		return Environment{}, err
	}
	for _, environment := range environments {
		if environment.Slug == "production" {
			return environment, nil
		}
	}
	return createDefaultProductionEnvironment(ctx, store, project)
}
