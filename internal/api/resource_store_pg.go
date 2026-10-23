// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// PostgresResourceStore is the Lane 1 persistence implementation of
// ProjectStore + ServiceStore + the (new) EnvironmentStore. It wraps
// the pg.Pool and the projects/services/environments tables with the
// SAME interface shapes the in-memory store presents, so the router
// and tests are unchanged.
type PostgresResourceStore struct {
	Pool      *pg.Pool
	Now       func() time.Time
	IDFactory func(prefix orailtypes.IDPrefix) orailtypes.ID
}

// NewPostgresResourceStore wires a PostgresResourceStore.
func NewPostgresResourceStore(pool *pg.Pool) *PostgresResourceStore {
	return &PostgresResourceStore{
		Pool:      pool,
		Now:       func() time.Time { return time.Now().UTC() },
		IDFactory: defaultResourceID,
	}
}

func defaultResourceID(prefix orailtypes.IDPrefix) orailtypes.ID {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return orailtypes.ID(fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()))
	}
	return orailtypes.ID(fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b[:])))
}

// CreateProject inserts a new project row, allocating a prj_* id when
// the caller did not supply one.
func (s *PostgresResourceStore) CreateProject(ctx context.Context, project Project) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	project, err := s.prepareProjectCreate(project)
	if err != nil {
		return Project{}, err
	}
	return s.createProject(ctx, s.Pool, project)
}

// CreateProjectWithDefaultEnvironment commits the operator compatibility
// project's row and its required production environment together. This is the
// durable equivalent of the tenant coordinator's bootstrap transaction.
func (s *PostgresResourceStore) CreateProjectWithDefaultEnvironment(
	ctx context.Context,
	project Project,
) (Project, Environment, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, Environment{}, err
	}
	project, err := s.prepareProjectCreate(project)
	if err != nil {
		return Project{}, Environment{}, err
	}
	environment, err := s.prepareEnvironmentCreate(Environment{
		ProjectID: project.ID,
		Slug:      "production",
		Name:      "Production",
		Tier:      "production",
	})
	if err != nil {
		return Project{}, Environment{}, fmt.Errorf("%w: %v", errDefaultProductionEnvironmentCreate, err)
	}
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := s.createProject(ctx, tx, project); err != nil {
			return err
		}
		if _, err := s.createEnvironment(ctx, tx, environment); err != nil {
			return fmt.Errorf("%w: %v", errDefaultProductionEnvironmentCreate, err)
		}
		return nil
	})
	if err != nil {
		return Project{}, Environment{}, err
	}
	return project, environment, nil
}

func (s *PostgresResourceStore) prepareProjectCreate(project Project) (Project, error) {
	if s == nil || s.Pool == nil || s.IDFactory == nil {
		return Project{}, errors.New("postgres resource store is not fully configured")
	}
	if project.Slug == "" {
		return Project{}, fmt.Errorf("%w: project slug is required", ErrResourceInvalid)
	}
	if project.WorkspaceID == "" {
		return Project{}, fmt.Errorf("%w: project workspace_id is required", ErrResourceInvalid)
	}
	if project.ID == "" {
		project.ID = s.IDFactory(orailtypes.IDPrefixProject)
	}
	if !strings.HasPrefix(string(project.ID), string(orailtypes.IDPrefixProject)+"_") {
		return Project{}, fmt.Errorf("%w: project id %q must use the %s_ prefix", ErrResourceInvalid, project.ID, orailtypes.IDPrefixProject)
	}
	now := s.now()
	if project.CreatedAt.IsZero() {
		project.CreatedAt = now
	}
	project.UpdatedAt = now
	if project.DisplayName == "" {
		project.DisplayName = project.Slug
	}
	return project, nil
}

func (s *PostgresResourceStore) createProject(ctx context.Context, conn pg.Conn, project Project) (Project, error) {
	_, err := conn.Exec(ctx,
		`INSERT INTO projects (id, workspace_id, slug, display_name, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		string(project.ID), project.WorkspaceID, project.Slug, project.DisplayName, project.CreatedAt, project.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Project{}, fmt.Errorf("%w: project slug %q already exists in workspace %q", ErrResourceConflict, project.Slug, project.WorkspaceID)
		}
		return Project{}, fmt.Errorf("project insert: %w", err)
	}
	return project, nil
}

// GetProject returns a project by id.
func (s *PostgresResourceStore) GetProject(ctx context.Context, id orailtypes.ID) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	var p Project
	err := s.Pool.QueryRow(ctx,
		`SELECT id, workspace_id, slug, display_name, created_at, updated_at
		 FROM projects WHERE id = $1 AND deleted_at IS NULL`,
		string(id),
	).Scan(&p.ID, &p.WorkspaceID, &p.Slug, &p.DisplayName, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrResourceNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("project get: %w", err)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, nil
}

// ListProjects returns all undeleted projects ordered by creation.
func (s *PostgresResourceStore) ListProjects(ctx context.Context) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, workspace_id, slug, display_name, created_at, updated_at
		 FROM projects WHERE deleted_at IS NULL ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("project list: %w", err)
	}
	defer rows.Close()
	out := make([]Project, 0)
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.Slug, &p.DisplayName, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("project scan: %w", err)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		p.UpdatedAt = p.UpdatedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateService inserts a service row.
func (s *PostgresResourceStore) CreateService(ctx context.Context, service Service) (Service, error) {
	if err := ctx.Err(); err != nil {
		return Service{}, err
	}
	service, err := s.prepareServiceCreate(service)
	if err != nil {
		return Service{}, err
	}
	return s.createService(ctx, s.Pool, service)
}

// CreateServiceInTx inserts a service through a caller-owned transaction.
// Coordinators use this seam so authorization, idempotency, the product row,
// the exact response, and audit evidence share one commit boundary.
func (s *PostgresResourceStore) CreateServiceInTx(ctx context.Context, tx *pg.Tx, service Service) (Service, error) {
	if err := ctx.Err(); err != nil {
		return Service{}, err
	}
	if tx == nil {
		return Service{}, errors.New("service create transaction is required")
	}
	service, err := s.prepareServiceCreate(service)
	if err != nil {
		return Service{}, err
	}
	return s.createService(ctx, tx, service)
}

func (s *PostgresResourceStore) prepareServiceCreate(service Service) (Service, error) {
	if s == nil || s.Pool == nil || s.IDFactory == nil {
		return Service{}, errors.New("postgres resource store is not fully configured")
	}
	if service.Slug == "" {
		return Service{}, fmt.Errorf("%w: service slug is required", ErrResourceInvalid)
	}
	if service.ID == "" {
		service.ID = s.IDFactory(orailtypes.IDPrefixService)
	}
	if !strings.HasPrefix(string(service.ID), string(orailtypes.IDPrefixService)+"_") {
		return Service{}, fmt.Errorf("%w: service id %q must use the %s_ prefix", ErrResourceInvalid, service.ID, orailtypes.IDPrefixService)
	}
	now := s.now()
	if service.CreatedAt.IsZero() {
		service.CreatedAt = now
	}
	if service.UpdatedAt.IsZero() {
		service.UpdatedAt = now
	}
	if service.DisplayName == "" {
		service.DisplayName = service.Slug
	}
	return service, nil
}

func (s *PostgresResourceStore) createService(ctx context.Context, conn pg.Conn, service Service) (Service, error) {
	if service.ProjectID != "" {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1 AND deleted_at IS NULL)`, string(service.ProjectID)).Scan(&exists); err != nil {
			return Service{}, fmt.Errorf("service parent check: %w", err)
		}
		if !exists {
			return Service{}, fmt.Errorf("%w: project %q not found", ErrResourceInvalid, service.ProjectID)
		}
	}

	err := conn.QueryRow(ctx,
		`INSERT INTO services (id, project_id, slug, display_name, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING resource_version`,
		string(service.ID), nullableID(service.ProjectID), service.Slug, service.DisplayName, service.CreatedAt, service.UpdatedAt,
	).Scan(&service.ResourceVersion)
	if err != nil {
		if isUniqueViolation(err) {
			return Service{}, fmt.Errorf("%w: service slug %q already exists in project %q", ErrResourceConflict, service.Slug, service.ProjectID)
		}
		return Service{}, fmt.Errorf("service insert: %w", err)
	}
	return service, nil
}

// GetService returns a service by id.
func (s *PostgresResourceStore) GetService(ctx context.Context, id orailtypes.ID) (Service, error) {
	if err := ctx.Err(); err != nil {
		return Service{}, err
	}
	var svc Service
	var projectID *string
	err := s.Pool.QueryRow(ctx,
		`SELECT id, project_id, slug, display_name, resource_version, created_at, updated_at
		 FROM services WHERE id = $1 AND deleted_at IS NULL`,
		string(id),
	).Scan(&svc.ID, &projectID, &svc.Slug, &svc.DisplayName, &svc.ResourceVersion, &svc.CreatedAt, &svc.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Service{}, ErrResourceNotFound
	}
	if err != nil {
		return Service{}, fmt.Errorf("service get: %w", err)
	}
	if projectID != nil {
		svc.ProjectID = orailtypes.ID(*projectID)
	}
	svc.CreatedAt = svc.CreatedAt.UTC()
	svc.UpdatedAt = svc.UpdatedAt.UTC()
	return svc, nil
}

// ListServices returns services optionally filtered by project parent.
func (s *PostgresResourceStore) ListServices(ctx context.Context, filter ResourceListFilter) ([]Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parent := string(filter.ParentID)
	rows, err := s.Pool.Query(ctx,
		`SELECT id, COALESCE(project_id, ''), slug, display_name, resource_version, created_at, updated_at
		 FROM services
		 WHERE deleted_at IS NULL
		   AND ($1 = '' OR project_id = $1)
		 ORDER BY id ASC`,
		parent,
	)
	if err != nil {
		return nil, fmt.Errorf("service list: %w", err)
	}
	defer rows.Close()
	out := make([]Service, 0)
	for rows.Next() {
		var svc Service
		var projectID string
		if err := rows.Scan(&svc.ID, &projectID, &svc.Slug, &svc.DisplayName, &svc.ResourceVersion, &svc.CreatedAt, &svc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("service scan: %w", err)
		}
		svc.ProjectID = orailtypes.ID(projectID)
		svc.CreatedAt = svc.CreatedAt.UTC()
		svc.UpdatedAt = svc.UpdatedAt.UTC()
		out = append(out, svc)
	}
	return out, rows.Err()
}

// Environment is the public Phase 0 representation of an env_*
// resource. The Lane 1 store is read-only via API; environments are
// created by tooling for now (full CRUD lands when the deployment FSM
// in Lane 2 wires environment lifecycle).
type Environment struct {
	ID              orailtypes.ID `json:"id"`
	ProjectID       orailtypes.ID `json:"project_id"`
	Slug            string        `json:"slug"`
	Name            string        `json:"name"`
	Tier            string        `json:"tier"`
	IsProtected     bool          `json:"is_protected"`
	ResourceVersion int64         `json:"resource_version"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// EnvironmentStore owns durable environment metadata. Lifecycle mutations are
// deliberately kept here rather than inferred from deployment state.
type EnvironmentStore interface {
	ListEnvironments(ctx context.Context, filter ResourceListFilter) ([]Environment, error)
	GetEnvironment(ctx context.Context, id orailtypes.ID) (Environment, error)
	CreateEnvironment(ctx context.Context, env Environment) (Environment, error)
	UpdateEnvironment(ctx context.Context, env Environment) (Environment, error)
	DeleteEnvironment(ctx context.Context, id orailtypes.ID) error
}

// ConditionalEnvironmentStore applies mutation preconditions inside the same
// storage critical section as the write.
type ConditionalEnvironmentStore interface {
	UpdateEnvironmentCAS(context.Context, Environment, int64) (Environment, error)
	DeleteEnvironmentCAS(context.Context, orailtypes.ID, int64) error
}

// CreateEnvironment inserts an environment through the store-owned
// transaction boundary.
func (s *PostgresResourceStore) CreateEnvironment(ctx context.Context, env Environment) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	env, err := s.prepareEnvironmentCreate(env)
	if err != nil {
		return Environment{}, err
	}
	return s.createEnvironment(ctx, s.Pool, env)
}

// CreateEnvironmentInTx inserts an environment through a caller-owned
// transaction. Tenant coordinators use this seam so current authorization,
// idempotency, the product row, exact response, and audit evidence commit
// together.
func (s *PostgresResourceStore) CreateEnvironmentInTx(ctx context.Context, tx *pg.Tx, env Environment) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	if tx == nil {
		return Environment{}, errors.New("environment create transaction is required")
	}
	env, err := s.prepareEnvironmentCreate(env)
	if err != nil {
		return Environment{}, err
	}
	return s.createEnvironment(ctx, tx, env)
}

func (s *PostgresResourceStore) prepareEnvironmentCreate(env Environment) (Environment, error) {
	if s == nil || s.Pool == nil || s.IDFactory == nil {
		return Environment{}, errors.New("postgres resource store is not fully configured")
	}
	if env.Slug == "" || env.ProjectID == "" {
		return Environment{}, fmt.Errorf("%w: environment requires project_id and slug", ErrResourceInvalid)
	}
	if env.ID == "" {
		env.ID = s.IDFactory(orailtypes.IDPrefixEnvironment)
	}
	if !strings.HasPrefix(string(env.ID), string(orailtypes.IDPrefixEnvironment)+"_") {
		return Environment{}, fmt.Errorf("%w: environment id %q must use the %s_ prefix", ErrResourceInvalid, env.ID, orailtypes.IDPrefixEnvironment)
	}
	if env.Tier == "" {
		env.Tier = "development"
	}
	if env.Name == "" {
		env.Name = env.Slug
	}
	if !validEnvironmentTier(env.Tier) {
		return Environment{}, fmt.Errorf("%w: environment tier %q is invalid", ErrResourceInvalid, env.Tier)
	}
	now := s.now()
	if env.CreatedAt.IsZero() {
		env.CreatedAt = now
	}
	if env.UpdatedAt.IsZero() {
		env.UpdatedAt = now
	}
	env.ResourceVersion = 1
	return env, nil
}

func (s *PostgresResourceStore) createEnvironment(ctx context.Context, conn pg.Conn, env Environment) (Environment, error) {
	_, err := conn.Exec(ctx,
		`INSERT INTO environments (id, project_id, slug, name, tier, is_protected, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(env.ID), string(env.ProjectID), env.Slug, env.Name, env.Tier, env.IsProtected, env.CreatedAt, env.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Environment{}, fmt.Errorf("%w: environment slug %q already exists in project %q", ErrResourceConflict, env.Slug, env.ProjectID)
		}
		return Environment{}, fmt.Errorf("environment insert: %w", err)
	}
	return env, nil
}

// UpdateEnvironment replaces mutable environment metadata. The project is
// immutable, which prevents a lifecycle request from moving dependent state
// across tenant boundaries.
func (s *PostgresResourceStore) UpdateEnvironment(ctx context.Context, env Environment) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	if env.ID == "" || strings.TrimSpace(env.Slug) == "" || strings.TrimSpace(env.Name) == "" || !validEnvironmentTier(env.Tier) {
		return Environment{}, fmt.Errorf("%w: environment id, slug, name, and valid tier are required", ErrResourceInvalid)
	}
	env.UpdatedAt = s.now()
	tag, err := s.Pool.Exec(ctx, `UPDATE environments SET slug=$1, name=$2, tier=$3, is_protected=$4, updated_at=$5, resource_version=resource_version+1 WHERE id=$6 AND deleted_at IS NULL`, env.Slug, env.Name, env.Tier, env.IsProtected, env.UpdatedAt, string(env.ID))
	if err != nil {
		if isUniqueViolation(err) {
			return Environment{}, fmt.Errorf("%w: environment slug already exists in project", ErrResourceConflict)
		}
		return Environment{}, fmt.Errorf("environment update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Environment{}, ErrResourceNotFound
	}
	return s.GetEnvironment(ctx, env.ID)
}

func (s *PostgresResourceStore) UpdateEnvironmentCAS(ctx context.Context, env Environment, expectedVersion int64) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	if expectedVersion < 1 || env.ID == "" || strings.TrimSpace(env.Slug) == "" || strings.TrimSpace(env.Name) == "" || !validEnvironmentTier(env.Tier) {
		return Environment{}, fmt.Errorf("%w: environment and positive expected version are required", ErrResourceInvalid)
	}
	env.UpdatedAt = s.now()
	var projectID string
	err := s.Pool.QueryRow(ctx, `
		UPDATE environments
		SET slug=$1,name=$2,tier=$3,is_protected=$4,updated_at=$5,resource_version=resource_version+1
		WHERE id=$6 AND deleted_at IS NULL AND resource_version=$7
		RETURNING project_id,slug,name,tier,is_protected,resource_version,created_at,updated_at`,
		env.Slug, env.Name, env.Tier, env.IsProtected, env.UpdatedAt, string(env.ID), expectedVersion,
	).Scan(
		&projectID, &env.Slug, &env.Name, &env.Tier, &env.IsProtected,
		&env.ResourceVersion, &env.CreatedAt, &env.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Environment{}, fmt.Errorf("%w: environment slug already exists in project", ErrResourceConflict)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if _, getErr := s.GetEnvironment(ctx, env.ID); errors.Is(getErr, ErrResourceNotFound) {
				return Environment{}, ErrResourceNotFound
			} else if getErr != nil {
				return Environment{}, getErr
			}
			return Environment{}, ErrResourcePreconditionFailed
		}
		return Environment{}, fmt.Errorf("environment conditional update: %w", err)
	}
	env.ProjectID = orailtypes.ID(projectID)
	env.CreatedAt = env.CreatedAt.UTC()
	env.UpdatedAt = env.UpdatedAt.UTC()
	return env, nil
}

// UpdateEnvironmentMetadataInTx applies an exact-version metadata replacement
// through a caller-owned transaction. Project ownership and protected state
// are deliberately immutable at this seam.
func (s *PostgresResourceStore) UpdateEnvironmentMetadataInTx(
	ctx context.Context,
	tx *pg.Tx,
	env Environment,
	expectedVersion int64,
) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	if s == nil || s.Pool == nil || tx == nil || expectedVersion < 1 {
		return Environment{}, fmt.Errorf("%w: environment update transaction and positive expected version are required", ErrResourceInvalid)
	}
	env.Slug = strings.TrimSpace(env.Slug)
	env.Name = strings.TrimSpace(env.Name)
	env.Tier = strings.TrimSpace(env.Tier)
	if env.ID == "" || env.ProjectID == "" || !serviceSlugPattern.MatchString(env.Slug) ||
		env.Name == "" || utf8.RuneCountInString(env.Name) > 200 || !validEnvironmentTier(env.Tier) {
		return Environment{}, fmt.Errorf("%w: environment id, project, valid slug, name, and tier are required", ErrResourceInvalid)
	}
	env.UpdatedAt = s.now().Truncate(time.Microsecond)
	err := tx.QueryRow(ctx, `
		UPDATE environments
		SET slug=$1,
		    name=$2,
		    tier=$3,
		    updated_at=$4,
		    resource_version=resource_version+1
		WHERE id=$5
		  AND project_id=$6
		  AND deleted_at IS NULL
		  AND resource_version=$7
		RETURNING is_protected, created_at, updated_at, resource_version`,
		env.Slug,
		env.Name,
		env.Tier,
		env.UpdatedAt,
		string(env.ID),
		string(env.ProjectID),
		expectedVersion,
	).Scan(
		&env.IsProtected,
		&env.CreatedAt,
		&env.UpdatedAt,
		&env.ResourceVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, ErrResourceConflict
	}
	if err != nil {
		if isUniqueViolation(err) {
			return Environment{}, fmt.Errorf("%w: environment slug already exists in project", ErrResourceConflict)
		}
		return Environment{}, fmt.Errorf("environment metadata update: %w", err)
	}
	env.CreatedAt = env.CreatedAt.UTC()
	env.UpdatedAt = env.UpdatedAt.UTC()
	return env, nil
}

// DeleteEnvironment soft-deletes an unprotected environment. Foreign keys keep
// all historical deployment evidence intact while future scope resolution no
// longer accepts the deleted environment.
func (s *PostgresResourceStore) DeleteEnvironment(ctx context.Context, id orailtypes.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE environments SET deleted_at=$1, updated_at=$1, resource_version=resource_version+1 WHERE id=$2 AND deleted_at IS NULL AND is_protected=FALSE`, s.now(), string(id))
	if err != nil {
		return fmt.Errorf("environment delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		env, getErr := s.GetEnvironment(ctx, id)
		if errors.Is(getErr, ErrResourceNotFound) {
			return ErrResourceNotFound
		}
		if getErr != nil {
			return getErr
		}
		if env.IsProtected {
			return fmt.Errorf("%w: protected environment cannot be deleted", ErrResourceConflict)
		}
		return ErrResourceNotFound
	}
	return nil
}

func (s *PostgresResourceStore) DeleteEnvironmentCAS(ctx context.Context, id orailtypes.ID, expectedVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if expectedVersion < 1 {
		return ErrResourcePreconditionFailed
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE environments SET deleted_at=$1,updated_at=$1,resource_version=resource_version+1 WHERE id=$2 AND deleted_at IS NULL AND is_protected=FALSE AND resource_version=$3`, s.now(), string(id), expectedVersion)
	if err != nil {
		return fmt.Errorf("environment conditional delete: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	env, getErr := s.GetEnvironment(ctx, id)
	if errors.Is(getErr, ErrResourceNotFound) {
		return ErrResourceNotFound
	}
	if getErr != nil {
		return getErr
	}
	if env.ResourceVersion != expectedVersion {
		return ErrResourcePreconditionFailed
	}
	if env.IsProtected {
		return fmt.Errorf("%w: protected environment cannot be deleted", ErrResourceConflict)
	}
	return ErrResourcePreconditionFailed
}

func validEnvironmentTier(tier string) bool {
	switch tier {
	case "production", "staging", "preview", "development":
		return true
	default:
		return false
	}
}

// GetEnvironment returns an environment by id.
func (s *PostgresResourceStore) GetEnvironment(ctx context.Context, id orailtypes.ID) (Environment, error) {
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	var env Environment
	var projectID string
	err := s.Pool.QueryRow(ctx,
		`SELECT id, project_id, slug, name, tier, is_protected, resource_version, created_at, updated_at
		 FROM environments WHERE id = $1 AND deleted_at IS NULL`,
		string(id),
	).Scan(&env.ID, &projectID, &env.Slug, &env.Name, &env.Tier, &env.IsProtected, &env.ResourceVersion, &env.CreatedAt, &env.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, ErrResourceNotFound
	}
	if err != nil {
		return Environment{}, fmt.Errorf("environment get: %w", err)
	}
	env.ProjectID = orailtypes.ID(projectID)
	env.CreatedAt = env.CreatedAt.UTC()
	env.UpdatedAt = env.UpdatedAt.UTC()
	return env, nil
}

// ListEnvironments returns environments scoped by parent project.
func (s *PostgresResourceStore) ListEnvironments(ctx context.Context, filter ResourceListFilter) ([]Environment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, project_id, slug, name, tier, is_protected, resource_version, created_at, updated_at
		 FROM environments
		 WHERE deleted_at IS NULL AND ($1 = '' OR project_id = $1)
		 ORDER BY id ASC`,
		string(filter.ParentID),
	)
	if err != nil {
		return nil, fmt.Errorf("environment list: %w", err)
	}
	defer rows.Close()
	out := make([]Environment, 0)
	for rows.Next() {
		var env Environment
		var projectID string
		if err := rows.Scan(&env.ID, &projectID, &env.Slug, &env.Name, &env.Tier, &env.IsProtected, &env.ResourceVersion, &env.CreatedAt, &env.UpdatedAt); err != nil {
			return nil, fmt.Errorf("environment scan: %w", err)
		}
		env.ProjectID = orailtypes.ID(projectID)
		env.CreatedAt = env.CreatedAt.UTC()
		env.UpdatedAt = env.UpdatedAt.UTC()
		out = append(out, env)
	}
	return out, rows.Err()
}

func (s *PostgresResourceStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// nullableID returns nil for empty ID values so the FK column is set to
// NULL rather than the empty string (which would violate the FK).
func nullableID(id orailtypes.ID) any {
	if id == "" {
		return nil
	}
	return string(id)
}

// isUniqueViolation reports whether err is a Postgres unique-violation
// (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
