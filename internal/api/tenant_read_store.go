// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"

	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// tenantReadStores adapt the canonical stores to a verified product principal.
// Operator/developer callers retain the compatibility behavior. Human reads
// are filtered through server-owned project/environment lineage and the
// identity policy matrix; a denial is concealed as a normal resource miss.
type tenantProjectStore struct {
	base   ProjectStore
	access identity.AccessStore
}

type tenantServiceStore struct {
	base   ServiceStore
	access identity.AccessStore
}

type tenantEnvironmentStore struct {
	base   EnvironmentStore
	access identity.AccessStore
}

// indexedTenantReadStore is implemented by the durable resource store. It
// applies membership predicates in SQL so human collection reads do not first
// enumerate installation-wide resources. Memory fixtures retain the
// deterministic filter adapter below.
type indexedTenantReadStore interface {
	tenantGetProject(context.Context, string, orailtypes.ID) (Project, error)
	tenantListProjects(context.Context, string) ([]Project, error)
	tenantGetService(context.Context, string, orailtypes.ID) (Service, error)
	tenantListServices(context.Context, string, ResourceListFilter) ([]Service, error)
	tenantGetEnvironment(context.Context, string, orailtypes.ID) (Environment, error)
	tenantListEnvironments(context.Context, string, ResourceListFilter) ([]Environment, error)
}

func (s tenantProjectStore) CreateProject(ctx context.Context, project Project) (Project, error) {
	if humanProductPrincipal(ctx) {
		return Project{}, identity.ErrAuthorizationDenied
	}
	return s.base.CreateProject(ctx, project)
}

func (s tenantProjectStore) GetProject(ctx context.Context, id orailtypes.ID) (Project, error) {
	if userID, ok := humanProductUserID(ctx); ok {
		if indexed, indexedOK := tenantIndexedReadStore(s.base, s.access); indexedOK {
			return indexed.tenantGetProject(ctx, userID, id)
		}
	}
	project, err := s.base.GetProject(ctx, id)
	if err != nil {
		return Project{}, err
	}
	if err := authorizeTenantRead(ctx, s.access, identity.AccessTarget{ProjectID: string(project.ID)}, identity.CapabilityProjectRead); err != nil {
		return Project{}, concealTenantReadError(err)
	}
	return project, nil
}

func (s tenantProjectStore) ListProjects(ctx context.Context) ([]Project, error) {
	if userID, ok := humanProductUserID(ctx); ok {
		if indexed, indexedOK := tenantIndexedReadStore(s.base, s.access); indexedOK {
			return indexed.tenantListProjects(ctx, userID)
		}
	}
	projects, err := s.base.ListProjects(ctx)
	if err != nil || !humanProductPrincipal(ctx) {
		return projects, err
	}
	out := make([]Project, 0, len(projects))
	for _, project := range projects {
		err := authorizeTenantRead(ctx, s.access, identity.AccessTarget{ProjectID: string(project.ID)}, identity.CapabilityProjectRead)
		if err == nil {
			out = append(out, project)
			continue
		}
		if !tenantReadDenied(err) {
			return nil, err
		}
	}
	return out, nil
}

func (s tenantServiceStore) CreateService(ctx context.Context, service Service) (Service, error) {
	if humanProductPrincipal(ctx) {
		return Service{}, identity.ErrAuthorizationDenied
	}
	return s.base.CreateService(ctx, service)
}

func (s tenantServiceStore) GetService(ctx context.Context, id orailtypes.ID) (Service, error) {
	if userID, ok := humanProductUserID(ctx); ok {
		if indexed, indexedOK := tenantIndexedReadStore(s.base, s.access); indexedOK {
			return indexed.tenantGetService(ctx, userID, id)
		}
	}
	service, err := s.base.GetService(ctx, id)
	if err != nil {
		return Service{}, err
	}
	if err := authorizeTenantRead(ctx, s.access, identity.AccessTarget{ProjectID: string(service.ProjectID)}, identity.CapabilityServiceRead); err != nil {
		return Service{}, concealTenantReadError(err)
	}
	return service, nil
}

func (s tenantServiceStore) ListServices(ctx context.Context, filter ResourceListFilter) ([]Service, error) {
	if userID, ok := humanProductUserID(ctx); ok {
		if indexed, indexedOK := tenantIndexedReadStore(s.base, s.access); indexedOK {
			return indexed.tenantListServices(ctx, userID, filter)
		}
	}
	if humanProductPrincipal(ctx) && filter.ParentID != "" {
		if err := authorizeTenantRead(ctx, s.access, identity.AccessTarget{ProjectID: string(filter.ParentID)}, identity.CapabilityServiceRead); err != nil {
			if tenantReadDenied(err) {
				return []Service{}, nil
			}
			return nil, err
		}
	}
	services, err := s.base.ListServices(ctx, filter)
	if err != nil || !humanProductPrincipal(ctx) || filter.ParentID != "" {
		return services, err
	}
	out := make([]Service, 0, len(services))
	for _, service := range services {
		err := authorizeTenantRead(ctx, s.access, identity.AccessTarget{ProjectID: string(service.ProjectID)}, identity.CapabilityServiceRead)
		if err == nil {
			out = append(out, service)
			continue
		}
		if !tenantReadDenied(err) {
			return nil, err
		}
	}
	return out, nil
}

func (s tenantEnvironmentStore) ListEnvironments(ctx context.Context, filter ResourceListFilter) ([]Environment, error) {
	if userID, ok := humanProductUserID(ctx); ok {
		if indexed, indexedOK := tenantIndexedReadStore(s.base, s.access); indexedOK {
			return indexed.tenantListEnvironments(ctx, userID, filter)
		}
	}
	environments, err := s.base.ListEnvironments(ctx, filter)
	if err != nil || !humanProductPrincipal(ctx) {
		return environments, err
	}
	out := make([]Environment, 0, len(environments))
	for _, environment := range environments {
		err := authorizeTenantRead(ctx, s.access, identity.AccessTarget{
			ProjectID:     string(environment.ProjectID),
			EnvironmentID: string(environment.ID),
		}, identity.CapabilityProjectRead)
		if err == nil {
			out = append(out, environment)
			continue
		}
		if !tenantReadDenied(err) {
			return nil, err
		}
	}
	return out, nil
}

func (s tenantEnvironmentStore) GetEnvironment(ctx context.Context, id orailtypes.ID) (Environment, error) {
	if userID, ok := humanProductUserID(ctx); ok {
		if indexed, indexedOK := tenantIndexedReadStore(s.base, s.access); indexedOK {
			return indexed.tenantGetEnvironment(ctx, userID, id)
		}
	}
	environment, err := s.base.GetEnvironment(ctx, id)
	if err != nil {
		return Environment{}, err
	}
	if err := authorizeTenantRead(ctx, s.access, identity.AccessTarget{
		ProjectID:     string(environment.ProjectID),
		EnvironmentID: string(environment.ID),
	}, identity.CapabilityProjectRead); err != nil {
		return Environment{}, concealTenantReadError(err)
	}
	return environment, nil
}

func (s tenantEnvironmentStore) CreateEnvironment(ctx context.Context, environment Environment) (Environment, error) {
	if humanProductPrincipal(ctx) {
		return Environment{}, identity.ErrAuthorizationDenied
	}
	return s.base.CreateEnvironment(ctx, environment)
}

func (s tenantEnvironmentStore) UpdateEnvironment(ctx context.Context, environment Environment) (Environment, error) {
	if humanProductPrincipal(ctx) {
		return Environment{}, identity.ErrAuthorizationDenied
	}
	return s.base.UpdateEnvironment(ctx, environment)
}

func (s tenantEnvironmentStore) DeleteEnvironment(ctx context.Context, id orailtypes.ID) error {
	if humanProductPrincipal(ctx) {
		return identity.ErrAuthorizationDenied
	}
	return s.base.DeleteEnvironment(ctx, id)
}

func humanProductPrincipal(ctx context.Context) bool {
	principal, ok := productPrincipalFromContext(ctx)
	return ok && principal.Kind == productPrincipalHuman
}

func humanProductUserID(ctx context.Context) (string, bool) {
	principal, ok := productPrincipalFromContext(ctx)
	if !ok || principal.Kind != productPrincipalHuman || principal.Human.UserID == "" {
		return "", false
	}
	return principal.Human.UserID, true
}

// tenantIndexedReadStore returns the PostgreSQL fast path only when resource
// lineage and identity grants are read from the exact same pool. This prevents
// an arbitrarily assembled DataStack from authenticating a user in authority A
// and authorizing a colliding user ID against authority B.
func tenantIndexedReadStore(base any, access identity.AccessStore) (indexedTenantReadStore, bool) {
	resources, resourcesOK := base.(*PostgresResourceStore)
	authority, authorityOK := access.(*identity.PostgresStore)
	if !resourcesOK || !authorityOK || resources.Pool == nil || authority.Pool == nil || resources.Pool != authority.Pool {
		return nil, false
	}
	return resources, true
}

// productResourceStoresShareAuthority verifies that product lineage and access
// grants come from one storage authority before any human route is registered.
// Unknown/custom combinations fail closed instead of falling back to
// cross-database ID joins in application memory.
func productResourceStoresShareAuthority(projects ProjectStore, services ServiceStore, environments EnvironmentStore, access identity.AccessStore) bool {
	switch authority := access.(type) {
	case *identity.PostgresStore:
		if authority.Pool == nil {
			return false
		}
		projectStore, projectOK := projects.(*PostgresResourceStore)
		serviceStore, serviceOK := services.(*PostgresResourceStore)
		environmentStore, environmentOK := environments.(*PostgresResourceStore)
		return projectOK && serviceOK && environmentOK &&
			projectStore.Pool == authority.Pool &&
			serviceStore.Pool == authority.Pool &&
			environmentStore.Pool == authority.Pool
	case *identity.MemoryAccessStore:
		_, projectOK := projects.(*MemoryResourceStore)
		_, serviceOK := services.(*MemoryResourceStore)
		_, environmentOK := environments.(*MemoryEnvironmentStore)
		return projectOK && serviceOK && environmentOK
	default:
		return false
	}
}

func authorizeTenantRead(ctx context.Context, store identity.AccessStore, target identity.AccessTarget, capability identity.Capability) error {
	principal, ok := productPrincipalFromContext(ctx)
	if !ok || principal.Kind != productPrincipalHuman {
		return nil
	}
	if store == nil || principal.Human.UserID == "" {
		return identity.ErrAuthorizationDenied
	}
	_, _, err := identity.Authorize(ctx, store, principal.Human.UserID, target, capability, false)
	return err
}

func tenantReadDenied(err error) bool {
	return errors.Is(err, identity.ErrAccessNotFound) || errors.Is(err, identity.ErrAuthorizationDenied)
}

func concealTenantReadError(err error) error {
	if tenantReadDenied(err) {
		return ErrResourceNotFound
	}
	return err
}
