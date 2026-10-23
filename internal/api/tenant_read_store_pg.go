// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func (s *PostgresResourceStore) tenantGetProject(ctx context.Context, userID string, id orailtypes.ID) (Project, error) {
	workspaceRoles, projectRoles := tenantRoleNames(identity.CapabilityProjectRead)
	var project Project
	err := s.Pool.QueryRow(ctx, `
		SELECT p.id,p.workspace_id,p.slug,p.display_name,p.created_at,p.updated_at
		FROM projects p
		JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
		JOIN users u ON u.id=$1 AND u.deleted_at IS NULL AND u.is_disabled=FALSE
		WHERE p.id=$2 AND p.deleted_at IS NULL
		  AND (
		    EXISTS (
		      SELECT 1 FROM workspace_members wm
		      WHERE wm.workspace_id=p.workspace_id AND wm.user_id=u.id
		        AND wm.accepted_at IS NOT NULL
		        AND wm.role=ANY($3::text[])
		    )
		    OR EXISTS (
		      SELECT 1 FROM project_members pm
		      WHERE pm.project_id=p.id AND pm.user_id=u.id
		        AND pm.accepted_at IS NOT NULL
		        AND pm.role=ANY($4::text[])
		    )
		  )`,
		userID, string(id), workspaceRoles, projectRoles,
	).Scan(&project.ID, &project.WorkspaceID, &project.Slug, &project.DisplayName, &project.CreatedAt, &project.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrResourceNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("tenant project get: %w", err)
	}
	project.CreatedAt = project.CreatedAt.UTC()
	project.UpdatedAt = project.UpdatedAt.UTC()
	return project, nil
}

func (s *PostgresResourceStore) tenantListProjects(ctx context.Context, userID string) ([]Project, error) {
	workspaceRoles, projectRoles := tenantRoleNames(identity.CapabilityProjectRead)
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id,p.workspace_id,p.slug,p.display_name,p.created_at,p.updated_at
		FROM projects p
		JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
		JOIN users u ON u.id=$1 AND u.deleted_at IS NULL AND u.is_disabled=FALSE
		WHERE p.deleted_at IS NULL
		  AND (
		    EXISTS (
		      SELECT 1 FROM workspace_members wm
		      WHERE wm.workspace_id=p.workspace_id AND wm.user_id=u.id
		        AND wm.accepted_at IS NOT NULL
		        AND wm.role=ANY($2::text[])
		    )
		    OR EXISTS (
		      SELECT 1 FROM project_members pm
		      WHERE pm.project_id=p.id AND pm.user_id=u.id
		        AND pm.accepted_at IS NOT NULL
		        AND pm.role=ANY($3::text[])
		    )
		  )
		ORDER BY p.created_at ASC,p.id ASC`,
		userID, workspaceRoles, projectRoles,
	)
	if err != nil {
		return nil, fmt.Errorf("tenant project list: %w", err)
	}
	defer rows.Close()
	out := make([]Project, 0)
	for rows.Next() {
		var project Project
		if err := rows.Scan(&project.ID, &project.WorkspaceID, &project.Slug, &project.DisplayName, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return nil, fmt.Errorf("tenant project scan: %w", err)
		}
		project.CreatedAt = project.CreatedAt.UTC()
		project.UpdatedAt = project.UpdatedAt.UTC()
		out = append(out, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tenant project rows: %w", err)
	}
	return out, nil
}

func (s *PostgresResourceStore) tenantGetService(ctx context.Context, userID string, id orailtypes.ID) (Service, error) {
	workspaceRoles, projectRoles := tenantRoleNames(identity.CapabilityServiceRead)
	var service Service
	err := s.Pool.QueryRow(ctx, `
		SELECT svc.id,p.id,svc.slug,svc.display_name,svc.created_at,svc.updated_at
		FROM services svc
		JOIN projects p ON p.id=svc.project_id AND p.deleted_at IS NULL
		JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
		JOIN users u ON u.id=$1 AND u.deleted_at IS NULL AND u.is_disabled=FALSE
		WHERE svc.id=$2 AND svc.deleted_at IS NULL
		  AND (
		    EXISTS (
		      SELECT 1 FROM workspace_members wm
		      WHERE wm.workspace_id=p.workspace_id AND wm.user_id=u.id
		        AND wm.accepted_at IS NOT NULL
		        AND wm.role=ANY($3::text[])
		    )
		    OR EXISTS (
		      SELECT 1 FROM project_members pm
		      WHERE pm.project_id=p.id AND pm.user_id=u.id
		        AND pm.accepted_at IS NOT NULL
		        AND pm.role=ANY($4::text[])
		    )
		  )`,
		userID, string(id), workspaceRoles, projectRoles,
	).Scan(&service.ID, &service.ProjectID, &service.Slug, &service.DisplayName, &service.CreatedAt, &service.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Service{}, ErrResourceNotFound
	}
	if err != nil {
		return Service{}, fmt.Errorf("tenant service get: %w", err)
	}
	service.CreatedAt = service.CreatedAt.UTC()
	service.UpdatedAt = service.UpdatedAt.UTC()
	return service, nil
}

func (s *PostgresResourceStore) tenantListServices(ctx context.Context, userID string, filter ResourceListFilter) ([]Service, error) {
	workspaceRoles, projectRoles := tenantRoleNames(identity.CapabilityServiceRead)
	rows, err := s.Pool.Query(ctx, `
		SELECT svc.id,p.id,svc.slug,svc.display_name,svc.created_at,svc.updated_at
		FROM services svc
		JOIN projects p ON p.id=svc.project_id AND p.deleted_at IS NULL
		JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
		JOIN users u ON u.id=$1 AND u.deleted_at IS NULL AND u.is_disabled=FALSE
		WHERE svc.deleted_at IS NULL
		  AND ($2='' OR p.id=$2)
		  AND (
		    EXISTS (
		      SELECT 1 FROM workspace_members wm
		      WHERE wm.workspace_id=p.workspace_id AND wm.user_id=u.id
		        AND wm.accepted_at IS NOT NULL
		        AND wm.role=ANY($3::text[])
		    )
		    OR EXISTS (
		      SELECT 1 FROM project_members pm
		      WHERE pm.project_id=p.id AND pm.user_id=u.id
		        AND pm.accepted_at IS NOT NULL
		        AND pm.role=ANY($4::text[])
		    )
		  )
		ORDER BY svc.id ASC`,
		userID, string(filter.ParentID), workspaceRoles, projectRoles,
	)
	if err != nil {
		return nil, fmt.Errorf("tenant service list: %w", err)
	}
	defer rows.Close()
	out := make([]Service, 0)
	for rows.Next() {
		var service Service
		if err := rows.Scan(&service.ID, &service.ProjectID, &service.Slug, &service.DisplayName, &service.CreatedAt, &service.UpdatedAt); err != nil {
			return nil, fmt.Errorf("tenant service scan: %w", err)
		}
		service.CreatedAt = service.CreatedAt.UTC()
		service.UpdatedAt = service.UpdatedAt.UTC()
		out = append(out, service)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tenant service rows: %w", err)
	}
	return out, nil
}

func (s *PostgresResourceStore) tenantGetEnvironment(ctx context.Context, userID string, id orailtypes.ID) (Environment, error) {
	workspaceRoles, projectRoles := tenantRoleNames(identity.CapabilityProjectRead)
	var environment Environment
	err := s.Pool.QueryRow(ctx, `
		SELECT e.id,p.id,e.slug,e.name,e.tier,e.is_protected,e.resource_version,e.created_at,e.updated_at
		FROM environments e
		JOIN projects p ON p.id=e.project_id AND p.deleted_at IS NULL
		JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
		JOIN users u ON u.id=$1 AND u.deleted_at IS NULL AND u.is_disabled=FALSE
		WHERE e.id=$2 AND e.deleted_at IS NULL
		  AND (
		    EXISTS (
		      SELECT 1 FROM workspace_members wm
		      WHERE wm.workspace_id=p.workspace_id AND wm.user_id=u.id
		        AND wm.accepted_at IS NOT NULL
		        AND wm.role=ANY($3::text[])
		        AND (NOT e.access_restricted OR wm.role='admin')
		    )
		    OR (
		      NOT e.access_restricted
		      AND EXISTS (
		        SELECT 1 FROM project_members pm
		        WHERE pm.project_id=p.id AND pm.user_id=u.id
		          AND pm.accepted_at IS NOT NULL
		          AND pm.role=ANY($4::text[])
		      )
		    )
		  )`,
		userID, string(id), workspaceRoles, projectRoles,
	).Scan(
		&environment.ID, &environment.ProjectID, &environment.Slug, &environment.Name,
		&environment.Tier, &environment.IsProtected, &environment.ResourceVersion,
		&environment.CreatedAt, &environment.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, ErrResourceNotFound
	}
	if err != nil {
		return Environment{}, fmt.Errorf("tenant environment get: %w", err)
	}
	environment.CreatedAt = environment.CreatedAt.UTC()
	environment.UpdatedAt = environment.UpdatedAt.UTC()
	return environment, nil
}

func (s *PostgresResourceStore) tenantListEnvironments(ctx context.Context, userID string, filter ResourceListFilter) ([]Environment, error) {
	workspaceRoles, projectRoles := tenantRoleNames(identity.CapabilityProjectRead)
	rows, err := s.Pool.Query(ctx, `
		SELECT e.id,p.id,e.slug,e.name,e.tier,e.is_protected,e.resource_version,e.created_at,e.updated_at
		FROM environments e
		JOIN projects p ON p.id=e.project_id AND p.deleted_at IS NULL
		JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
		JOIN users u ON u.id=$1 AND u.deleted_at IS NULL AND u.is_disabled=FALSE
		WHERE e.deleted_at IS NULL
		  AND ($2='' OR p.id=$2)
		  AND (
		    EXISTS (
		      SELECT 1 FROM workspace_members wm
		      WHERE wm.workspace_id=p.workspace_id AND wm.user_id=u.id
		        AND wm.accepted_at IS NOT NULL
		        AND wm.role=ANY($3::text[])
		        AND (NOT e.access_restricted OR wm.role='admin')
		    )
		    OR (
		      NOT e.access_restricted
		      AND EXISTS (
		        SELECT 1 FROM project_members pm
		        WHERE pm.project_id=p.id AND pm.user_id=u.id
		          AND pm.accepted_at IS NOT NULL
		          AND pm.role=ANY($4::text[])
		      )
		    )
		  )
		ORDER BY e.id ASC`,
		userID, string(filter.ParentID), workspaceRoles, projectRoles,
	)
	if err != nil {
		return nil, fmt.Errorf("tenant environment list: %w", err)
	}
	defer rows.Close()
	out := make([]Environment, 0)
	for rows.Next() {
		var environment Environment
		if err := rows.Scan(
			&environment.ID, &environment.ProjectID, &environment.Slug, &environment.Name,
			&environment.Tier, &environment.IsProtected, &environment.ResourceVersion,
			&environment.CreatedAt, &environment.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("tenant environment scan: %w", err)
		}
		environment.CreatedAt = environment.CreatedAt.UTC()
		environment.UpdatedAt = environment.UpdatedAt.UTC()
		out = append(out, environment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tenant environment rows: %w", err)
	}
	return out, nil
}

func tenantRoleNames(capability identity.Capability) ([]string, []string) {
	workspaceRoles := identity.WorkspaceRolesForCapability(capability)
	projectRoles := identity.ProjectRolesForCapability(capability)
	workspaceNames := make([]string, 0, len(workspaceRoles))
	for _, role := range workspaceRoles {
		workspaceNames = append(workspaceNames, string(role))
	}
	projectNames := make([]string, 0, len(projectRoles))
	for _, role := range projectRoles {
		projectNames = append(projectNames, string(role))
	}
	return workspaceNames, projectNames
}
