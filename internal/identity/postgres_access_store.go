package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/midnight/midnight/internal/db/pg"
)

const (
	DefaultWorkspaceID     = "wrk_default"
	migrationDefaultUserID = "usr_migration_default"
)

// DefaultWorkspaceClaimed reports whether the migration default workspace
// already has a human administrator. The predicate is intentionally identical
// to the one ClaimDefaultWorkspaceInTx enforces under the advisory lock so the
// unauthenticated status probe cannot disagree with the claim itself.
func (s *PostgresStore) DefaultWorkspaceClaimed(ctx context.Context) (bool, error) {
	if s == nil || s.Pool == nil {
		return false, errors.New("identity postgres store: pool is required")
	}
	var claimed bool
	if err := s.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM workspace_members wm
				JOIN users u ON u.id = wm.user_id
				WHERE wm.workspace_id = $1
				  AND wm.role = 'admin'
				  AND wm.accepted_at IS NOT NULL
				  AND wm.user_id <> $2
				  AND u.deleted_at IS NULL
				  AND u.is_disabled = FALSE
			)`,
		DefaultWorkspaceID,
		migrationDefaultUserID,
	).Scan(&claimed); err != nil {
		return false, fmt.Errorf("identity default workspace claim status: %w", err)
	}
	return claimed, nil
}

func (s *PostgresStore) ClaimDefaultWorkspace(ctx context.Context, request ClaimDefaultWorkspaceRequest) (ClaimDefaultWorkspaceResult, error) {
	if s == nil || s.Pool == nil {
		return ClaimDefaultWorkspaceResult{}, errors.New("identity postgres store: pool is required")
	}
	var result ClaimDefaultWorkspaceResult
	err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		var err error
		result, err = s.ClaimDefaultWorkspaceInTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return ClaimDefaultWorkspaceResult{}, err
	}
	return result, nil
}

// ClaimDefaultWorkspaceInTx performs the one-time identity claim using a
// transaction owned by the caller. It lets production coordinators commit the
// identity state beside audit evidence and an exact idempotency response. The
// caller must use SERIALIZABLE isolation and is responsible for commit/rollback.
func (s *PostgresStore) ClaimDefaultWorkspaceInTx(
	ctx context.Context,
	tx *pg.Tx,
	request ClaimDefaultWorkspaceRequest,
) (ClaimDefaultWorkspaceResult, error) {
	if s == nil || s.Pool == nil || tx == nil {
		return ClaimDefaultWorkspaceResult{}, errors.New("identity postgres store: pool and transaction are required")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('midnight.identity.default-workspace-claim'))`); err != nil {
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim lock: %w", err)
	}
	var claimed bool
	if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM workspace_members wm
				JOIN users u ON u.id = wm.user_id
				WHERE wm.workspace_id = $1
				  AND wm.role = 'admin'
				  AND wm.accepted_at IS NOT NULL
				  AND wm.user_id <> $2
				  AND u.deleted_at IS NULL
				  AND u.is_disabled = FALSE
			)`,
		DefaultWorkspaceID,
		migrationDefaultUserID,
	).Scan(&claimed); err != nil {
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim check: %w", err)
	}
	if claimed {
		return ClaimDefaultWorkspaceResult{}, ErrWorkspaceClaimed
	}

	_, err := tx.Exec(ctx, `
			INSERT INTO users (
				id, email, display_name, password_hash, must_rotate_password,
				totp_secret_encrypted, totp_enrolled_at, is_admin,
				session_generation, is_disabled, created_at, updated_at
			)
			VALUES ($1,$2,$3,$4,FALSE,$5,$6,TRUE,1,FALSE,$7,$7)`,
		request.UserID,
		strings.ToLower(strings.TrimSpace(request.Email)),
		strings.TrimSpace(request.DisplayName),
		request.PasswordHash,
		request.TOTPSecretEnvelope,
		request.TOTPEnrolledAt,
		request.Now,
	)
	if err != nil {
		if isIdentityUniqueViolation(err) {
			return ClaimDefaultWorkspaceResult{}, ErrIdentityConflict
		}
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim user insert: %w", err)
	}

	tag, err := tx.Exec(ctx, `
			UPDATE workspaces
			SET slug = $2,
			    display_name = $3,
			    updated_at = $4,
			    resource_version = resource_version + 1
			WHERE id = $1`,
		DefaultWorkspaceID,
		request.WorkspaceSlug,
		request.WorkspaceDisplayName,
		request.Now,
	)
	if err != nil {
		if isIdentityUniqueViolation(err) {
			return ClaimDefaultWorkspaceResult{}, ErrIdentityConflict
		}
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim update: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ClaimDefaultWorkspaceResult{}, ErrAccessNotFound
	}

	if _, err := tx.Exec(ctx, `
			INSERT INTO workspace_members (
				workspace_id, user_id, role, invited_at, accepted_at,
				created_at, updated_at, resource_version
			)
			VALUES ($1,$2,'admin',$3,$3,$3,$3,1)
			ON CONFLICT (workspace_id, user_id) DO UPDATE
			SET role='admin', accepted_at=$3, updated_at=$3,
			    resource_version=workspace_members.resource_version+1`,
		DefaultWorkspaceID,
		request.UserID,
		request.Now,
	); err != nil {
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim membership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
			INSERT INTO project_members (
				project_id, user_id, role, invited_at, accepted_at,
				created_at, updated_at, resource_version
			)
			SELECT id, $1, 'owner', $2, $2, $2, $2, 1
			FROM projects
			WHERE workspace_id = $3
			ON CONFLICT (project_id, user_id) DO UPDATE
			SET role='owner', accepted_at=$2, updated_at=$2,
			    resource_version=project_members.resource_version+1`,
		request.UserID,
		request.Now,
		DefaultWorkspaceID,
	); err != nil {
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim project ownership: %w", err)
	}
	if _, err := tx.Exec(ctx, `
			DELETE FROM project_members
			WHERE user_id=$1
			  AND project_id IN (SELECT id FROM projects WHERE workspace_id=$2)`,
		migrationDefaultUserID,
		DefaultWorkspaceID,
	); err != nil {
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim migration project cleanup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
			DELETE FROM workspace_members
			WHERE workspace_id=$1 AND user_id=$2`,
		DefaultWorkspaceID,
		migrationDefaultUserID,
	); err != nil {
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim migration membership cleanup: %w", err)
	}
	var workspaceCreatedAt, workspaceUpdatedAt time.Time
	if err := tx.QueryRow(ctx, `
			SELECT created_at, updated_at
			FROM workspaces
			WHERE id=$1`,
		DefaultWorkspaceID,
	).Scan(&workspaceCreatedAt, &workspaceUpdatedAt); err != nil {
		return ClaimDefaultWorkspaceResult{}, fmt.Errorf("identity workspace claim result: %w", err)
	}
	return ClaimDefaultWorkspaceResult{
		User: User{
			ID:            request.UserID,
			Email:         strings.ToLower(strings.TrimSpace(request.Email)),
			DisplayName:   strings.TrimSpace(request.DisplayName),
			InstanceAdmin: true,
			MFAEnabled:    !request.TOTPEnrolledAt.IsZero(),
		},
		Workspace: Workspace{
			ID:          DefaultWorkspaceID,
			Slug:        request.WorkspaceSlug,
			DisplayName: request.WorkspaceDisplayName,
			Role:        WorkspaceRoleAdmin,
			CreatedAt:   workspaceCreatedAt.UTC(),
			UpdatedAt:   workspaceUpdatedAt.UTC(),
		},
	}, nil
}

func (s *PostgresStore) ListWorkspaces(ctx context.Context, userID string) ([]Workspace, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT w.id, w.slug, w.display_name, wm.role, w.created_at, w.updated_at
		FROM workspaces w
		JOIN workspace_members wm ON wm.workspace_id = w.id
		JOIN users u ON u.id = wm.user_id
		WHERE wm.user_id = $1
		  AND wm.accepted_at IS NOT NULL
		  AND w.deleted_at IS NULL
		  AND u.deleted_at IS NULL
		  AND u.is_disabled = FALSE
		ORDER BY w.display_name, w.id`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("identity workspace list: %w", err)
	}
	defer rows.Close()
	out := make([]Workspace, 0)
	for rows.Next() {
		var workspace Workspace
		if err := rows.Scan(&workspace.ID, &workspace.Slug, &workspace.DisplayName, &workspace.Role, &workspace.CreatedAt, &workspace.UpdatedAt); err != nil {
			return nil, fmt.Errorf("identity workspace scan: %w", err)
		}
		workspace.CreatedAt = workspace.CreatedAt.UTC()
		workspace.UpdatedAt = workspace.UpdatedAt.UTC()
		out = append(out, workspace)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListWorkspaceMembers(ctx context.Context, workspaceID string) ([]WorkspaceMember, error) {
	rows, err := s.Pool.Query(ctx, workspaceMembersSelect+`
		WHERE wm.workspace_id = $1
		ORDER BY LOWER(u.email), u.id`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("identity workspace member list: %w", err)
	}
	defer rows.Close()
	out := make([]WorkspaceMember, 0)
	for rows.Next() {
		member, err := scanWorkspaceMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PutWorkspaceMember(ctx context.Context, workspaceID, userID string, role WorkspaceRole, actorID string, now time.Time) (WorkspaceMember, error) {
	return s.putWorkspaceMember(ctx, workspaceID, userID, role, actorID, 0, false, now)
}

func (s *PostgresStore) PutWorkspaceMemberCAS(ctx context.Context, workspaceID, userID string, role WorkspaceRole, actorID string, expectedVersion int64, now time.Time) (WorkspaceMember, error) {
	return s.putWorkspaceMember(ctx, workspaceID, userID, role, actorID, expectedVersion, true, now)
}

func (s *PostgresStore) putWorkspaceMember(ctx context.Context, workspaceID, userID string, role WorkspaceRole, actorID string, expectedVersion int64, conditional bool, now time.Time) (WorkspaceMember, error) {
	if _, ok := ParseWorkspaceRole(string(role)); !ok {
		return WorkspaceMember{}, ErrAuthorizationDenied
	}
	if conditional && expectedVersion < 1 {
		return WorkspaceMember{}, ErrMembershipVersionConflict
	}
	var result WorkspaceMember
	err := s.withMembershipTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
			return err
		}
		if err := authorizeWorkspaceMemberMutation(ctx, tx, workspaceID, actorID); err != nil {
			return err
		}
		var currentRole string
		var currentVersion int64
		err := tx.QueryRow(ctx, `
			SELECT role, resource_version
			FROM workspace_members
			WHERE workspace_id=$1 AND user_id=$2
			FOR UPDATE`,
			workspaceID,
			userID,
		).Scan(&currentRole, &currentVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMembershipNotFound
		}
		if err != nil {
			return err
		}
		if conditional && currentVersion != expectedVersion {
			return ErrMembershipVersionConflict
		}
		if currentRole == string(WorkspaceRoleAdmin) && role != WorkspaceRoleAdmin {
			if err := requireAnotherWorkspaceAdmin(ctx, tx, workspaceID, userID); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE workspace_members wm
			SET role=$3, invited_by=$4, updated_at=$5,
			    resource_version=wm.resource_version+1
			FROM users u
			WHERE wm.workspace_id=$1
			  AND wm.user_id=$2
			  AND u.id=wm.user_id
			  AND u.deleted_at IS NULL
			  AND u.is_disabled=FALSE
			  AND ($6=FALSE OR wm.resource_version=$7)`,
			workspaceID,
			userID,
			role,
			nullableIdentityID(actorID),
			now,
			conditional,
			expectedVersion,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 && conditional {
			return ErrMembershipVersionConflict
		}
		if tag.RowsAffected() != 1 {
			return ErrIdentityNotFound
		}
		result, err = scanWorkspaceMember(tx.QueryRow(
			ctx,
			workspaceMembersSelect+` WHERE wm.workspace_id=$1 AND wm.user_id=$2`,
			workspaceID,
			userID,
		))
		if err != nil {
			return fmt.Errorf("identity workspace member result: %w", err)
		}
		return nil
	})
	if err != nil {
		return WorkspaceMember{}, err
	}
	return result, nil
}

func (s *PostgresStore) RemoveWorkspaceMember(ctx context.Context, workspaceID, userID, actorID string, now time.Time) error {
	return s.removeWorkspaceMember(ctx, workspaceID, userID, actorID, 0, false, now)
}

func (s *PostgresStore) RemoveWorkspaceMemberCAS(ctx context.Context, workspaceID, userID, actorID string, expectedVersion int64, now time.Time) error {
	return s.removeWorkspaceMember(ctx, workspaceID, userID, actorID, expectedVersion, true, now)
}

func (s *PostgresStore) removeWorkspaceMember(ctx context.Context, workspaceID, userID, actorID string, expectedVersion int64, conditional bool, now time.Time) error {
	if conditional && expectedVersion < 1 {
		return ErrMembershipVersionConflict
	}
	return s.withMembershipTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
			return err
		}
		if err := authorizeWorkspaceMemberMutation(ctx, tx, workspaceID, actorID); err != nil {
			return err
		}
		var role string
		var currentVersion int64
		err := tx.QueryRow(ctx, `
			SELECT role, resource_version FROM workspace_members
			WHERE workspace_id=$1 AND user_id=$2
			FOR UPDATE`,
			workspaceID,
			userID,
		).Scan(&role, &currentVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMembershipNotFound
		}
		if err != nil {
			return err
		}
		if conditional && currentVersion != expectedVersion {
			return ErrMembershipVersionConflict
		}
		if role == string(WorkspaceRoleAdmin) {
			if err := requireAnotherWorkspaceAdmin(ctx, tx, workspaceID, userID); err != nil {
				return err
			}
		}
		if err := requireWorkspaceRemovalPreservesProjectOwners(ctx, tx, workspaceID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM project_members pm USING projects p WHERE pm.project_id=p.id AND p.workspace_id=$1 AND pm.user_id=$2`, workspaceID, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=$2 AND ($3=FALSE OR resource_version=$4)`, workspaceID, userID, conditional, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 && conditional {
			return ErrMembershipVersionConflict
		}
		if tag.RowsAffected() != 1 {
			return ErrMembershipNotFound
		}
		_ = now
		return nil
	})
}

func (s *PostgresStore) ListProjectMembers(ctx context.Context, projectID string) ([]ProjectMember, error) {
	rows, err := s.Pool.Query(ctx, projectMembersSelect+`
		WHERE pm.project_id = $1
		ORDER BY LOWER(u.email), u.id`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("identity project member list: %w", err)
	}
	defer rows.Close()
	out := make([]ProjectMember, 0)
	for rows.Next() {
		member, err := scanProjectMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PutProjectMember(ctx context.Context, projectID, userID string, role ProjectRole, actorID string, now time.Time) (ProjectMember, error) {
	return s.putProjectMember(ctx, projectID, userID, role, actorID, 0, false, now)
}

func (s *PostgresStore) PutProjectMemberCAS(ctx context.Context, projectID, userID string, role ProjectRole, actorID string, expectedVersion int64, now time.Time) (ProjectMember, error) {
	return s.putProjectMember(ctx, projectID, userID, role, actorID, expectedVersion, true, now)
}

func (s *PostgresStore) putProjectMember(ctx context.Context, projectID, userID string, role ProjectRole, actorID string, expectedVersion int64, conditional bool, now time.Time) (ProjectMember, error) {
	if _, ok := ParseProjectRole(string(role)); !ok {
		return ProjectMember{}, ErrAuthorizationDenied
	}
	if conditional && expectedVersion < 1 {
		return ProjectMember{}, ErrMembershipVersionConflict
	}
	var result ProjectMember
	err := s.withMembershipTx(ctx, func(tx *pg.Tx) error {
		workspaceID, err := projectWorkspaceForMembershipMutation(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
			return err
		}
		editorOnly, err := authorizeProjectMemberMutation(ctx, tx, projectID, workspaceID, actorID)
		if err != nil {
			return err
		}
		var currentRole string
		var currentVersion int64
		err = tx.QueryRow(ctx, `SELECT role, resource_version FROM project_members WHERE project_id=$1 AND user_id=$2 FOR UPDATE`, projectID, userID).Scan(&currentRole, &currentVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMembershipNotFound
		}
		if err != nil {
			return err
		}
		if conditional && currentVersion != expectedVersion {
			return ErrMembershipVersionConflict
		}
		if editorOnly && (currentRole == string(ProjectRoleOwner) || role == ProjectRoleOwner) {
			return ErrAuthorizationDenied
		}
		if currentRole == string(ProjectRoleOwner) && role != ProjectRoleOwner {
			if err := requireAnotherProjectOwner(ctx, tx, projectID, userID); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE project_members pm
			SET role=$3, invited_by=$4, updated_at=$5,
			    resource_version=pm.resource_version+1
			FROM users u, projects p
			WHERE pm.project_id=$1
			  AND pm.user_id=$2
			  AND u.id=pm.user_id
			  AND u.deleted_at IS NULL
			  AND u.is_disabled=FALSE
			  AND p.id=pm.project_id
			  AND p.deleted_at IS NULL
			  AND ($6=FALSE OR pm.resource_version=$7)`,
			projectID,
			userID,
			role,
			nullableIdentityID(actorID),
			now,
			conditional,
			expectedVersion,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 && conditional {
			return ErrMembershipVersionConflict
		}
		if tag.RowsAffected() != 1 {
			return ErrIdentityNotFound
		}
		result, err = scanProjectMember(tx.QueryRow(
			ctx,
			projectMembersSelect+` WHERE pm.project_id=$1 AND pm.user_id=$2`,
			projectID,
			userID,
		))
		if err != nil {
			return fmt.Errorf("identity project member result: %w", err)
		}
		return nil
	})
	if err != nil {
		return ProjectMember{}, err
	}
	return result, nil
}

func (s *PostgresStore) RemoveProjectMember(ctx context.Context, projectID, userID, actorID string, _ time.Time) error {
	return s.removeProjectMember(ctx, projectID, userID, actorID, 0, false)
}

func (s *PostgresStore) RemoveProjectMemberCAS(ctx context.Context, projectID, userID, actorID string, expectedVersion int64, _ time.Time) error {
	return s.removeProjectMember(ctx, projectID, userID, actorID, expectedVersion, true)
}

func (s *PostgresStore) removeProjectMember(ctx context.Context, projectID, userID, actorID string, expectedVersion int64, conditional bool) error {
	if conditional && expectedVersion < 1 {
		return ErrMembershipVersionConflict
	}
	return s.withMembershipTx(ctx, func(tx *pg.Tx) error {
		workspaceID, err := projectWorkspaceForMembershipMutation(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
			return err
		}
		editorOnly, err := authorizeProjectMemberMutation(ctx, tx, projectID, workspaceID, actorID)
		if err != nil {
			return err
		}
		var role string
		var currentVersion int64
		err = tx.QueryRow(ctx, `SELECT role, resource_version FROM project_members WHERE project_id=$1 AND user_id=$2 FOR UPDATE`, projectID, userID).Scan(&role, &currentVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMembershipNotFound
		}
		if err != nil {
			return err
		}
		if conditional && currentVersion != expectedVersion {
			return ErrMembershipVersionConflict
		}
		if editorOnly && role == string(ProjectRoleOwner) {
			return ErrAuthorizationDenied
		}
		if role == string(ProjectRoleOwner) {
			if err := requireAnotherProjectOwner(ctx, tx, projectID, userID); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2 AND ($3=FALSE OR resource_version=$4)`, projectID, userID, conditional, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 && conditional {
			return ErrMembershipVersionConflict
		}
		if tag.RowsAffected() != 1 {
			return ErrMembershipNotFound
		}
		return nil
	})
}

// An advisory lock acquired inside SERIALIZABLE can wait after PostgreSQL has
// chosen its snapshot. Retry the whole transaction so the loser observes the
// committed membership version and returns the normal CAS conflict.
func (s *PostgresStore) withMembershipTx(ctx context.Context, fn func(*pg.Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, fn)
		var postgresError *pgconn.PgError
		if attempt >= 2 || ctx.Err() != nil || !errors.As(err, &postgresError) ||
			(postgresError.Code != "40001" && postgresError.Code != "40P01") {
			return err
		}
	}
}

func (s *PostgresStore) ResolveAccess(ctx context.Context, userID string, target AccessTarget) (AccessGrant, error) {
	if strings.TrimSpace(target.ProjectID) == "" {
		if strings.TrimSpace(target.WorkspaceID) == "" {
			return AccessGrant{}, ErrAccessNotFound
		}
		var role WorkspaceRole
		err := s.Pool.QueryRow(ctx, `
			SELECT wm.role
			FROM workspace_members wm
			JOIN users u ON u.id=wm.user_id
			JOIN workspaces w ON w.id=wm.workspace_id
			WHERE wm.workspace_id=$1 AND wm.user_id=$2
			  AND wm.accepted_at IS NOT NULL
			  AND u.deleted_at IS NULL AND u.is_disabled=FALSE
			  AND w.deleted_at IS NULL`,
			target.WorkspaceID,
			userID,
		).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return AccessGrant{}, ErrAccessNotFound
		}
		if err != nil {
			return AccessGrant{}, fmt.Errorf("identity workspace access resolve: %w", err)
		}
		return AccessGrant{Target: target, WorkspaceRole: role}, nil
	}

	var (
		workspaceID   string
		workspaceRole *string
		projectRole   *string
		restricted    bool
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT p.workspace_id, wm.role, pm.role,
		       COALESCE(e.access_restricted, FALSE)
		FROM projects p
		JOIN workspaces w
		  ON w.id=p.workspace_id
		 AND w.deleted_at IS NULL
		LEFT JOIN workspace_members wm
		  ON wm.workspace_id=p.workspace_id
		 AND wm.user_id=$2
		 AND wm.accepted_at IS NOT NULL
		LEFT JOIN project_members pm
		  ON pm.project_id=p.id
		 AND pm.user_id=$2
		 AND pm.accepted_at IS NOT NULL
		LEFT JOIN environments e
		  ON e.project_id=p.id
		 AND e.id=NULLIF($3, '')
		 AND e.deleted_at IS NULL
		JOIN users u ON u.id=$2 AND u.deleted_at IS NULL AND u.is_disabled=FALSE
		WHERE p.id=$1
		  AND p.deleted_at IS NULL
		  AND ($3='' OR e.id IS NOT NULL)
		  AND (wm.user_id IS NOT NULL OR pm.user_id IS NOT NULL)`,
		target.ProjectID,
		userID,
		target.EnvironmentID,
	).Scan(&workspaceID, &workspaceRole, &projectRole, &restricted)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessGrant{}, ErrAccessNotFound
	}
	if err != nil {
		return AccessGrant{}, fmt.Errorf("identity project access resolve: %w", err)
	}
	if target.WorkspaceID != "" && target.WorkspaceID != workspaceID {
		return AccessGrant{}, ErrAccessNotFound
	}
	target.WorkspaceID = workspaceID
	grant := AccessGrant{Target: target, EnvironmentRestricted: restricted}
	if workspaceRole != nil {
		role, ok := ParseWorkspaceRole(*workspaceRole)
		if !ok {
			return AccessGrant{}, fmt.Errorf("identity project access resolve: invalid workspace role %q", *workspaceRole)
		}
		grant.WorkspaceRole = role
	}
	if projectRole != nil {
		role, ok := ParseProjectRole(*projectRole)
		if !ok {
			return AccessGrant{}, fmt.Errorf("identity project access resolve: invalid project role %q", *projectRole)
		}
		grant.ProjectRole = role
	}
	return grant, nil
}

const workspaceMembersSelect = `
	SELECT wm.workspace_id, u.id, u.email, u.display_name,
	       u.is_admin, u.totp_enrolled_at IS NOT NULL,
	       COALESCE(u.last_login_at, 'epoch'),
	       wm.role, COALESCE(wm.invited_by, ''), wm.invited_at, wm.accepted_at,
	       wm.created_at, wm.updated_at, wm.resource_version
	FROM workspace_members wm
	JOIN users u ON u.id=wm.user_id
`

const projectMembersSelect = `
	SELECT pm.project_id, u.id, u.email, u.display_name,
	       u.is_admin, u.totp_enrolled_at IS NOT NULL,
	       COALESCE(u.last_login_at, 'epoch'),
	       pm.role, COALESCE(pm.invited_by, ''), pm.invited_at, pm.accepted_at,
	       pm.created_at, pm.updated_at, pm.resource_version
	FROM project_members pm
	JOIN users u ON u.id=pm.user_id
`

func (s *PostgresStore) GetWorkspaceMember(ctx context.Context, workspaceID, userID string) (WorkspaceMember, error) {
	row := s.Pool.QueryRow(ctx, workspaceMembersSelect+` WHERE wm.workspace_id=$1 AND wm.user_id=$2`, workspaceID, userID)
	member, err := scanWorkspaceMember(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkspaceMember{}, ErrMembershipNotFound
	}
	return member, err
}

func scanWorkspaceMember(row pgx.Row) (WorkspaceMember, error) {
	var member WorkspaceMember
	if err := row.Scan(
		&member.WorkspaceID,
		&member.User.ID,
		&member.User.Email,
		&member.User.DisplayName,
		&member.User.InstanceAdmin,
		&member.User.MFAEnabled,
		&member.User.LastLoginAt,
		&member.Role,
		&member.InvitedBy,
		&member.InvitedAt,
		&member.AcceptedAt,
		&member.CreatedAt,
		&member.UpdatedAt,
		&member.Version,
	); err != nil {
		return WorkspaceMember{}, err
	}
	normalizeWorkspaceMember(&member)
	return member, nil
}

func (s *PostgresStore) GetProjectMember(ctx context.Context, projectID, userID string) (ProjectMember, error) {
	row := s.Pool.QueryRow(ctx, projectMembersSelect+` WHERE pm.project_id=$1 AND pm.user_id=$2`, projectID, userID)
	member, err := scanProjectMember(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectMember{}, ErrMembershipNotFound
	}
	return member, err
}

func scanProjectMember(row pgx.Row) (ProjectMember, error) {
	var member ProjectMember
	if err := row.Scan(
		&member.ProjectID,
		&member.User.ID,
		&member.User.Email,
		&member.User.DisplayName,
		&member.User.InstanceAdmin,
		&member.User.MFAEnabled,
		&member.User.LastLoginAt,
		&member.Role,
		&member.InvitedBy,
		&member.InvitedAt,
		&member.AcceptedAt,
		&member.CreatedAt,
		&member.UpdatedAt,
		&member.Version,
	); err != nil {
		return ProjectMember{}, err
	}
	normalizeProjectMember(&member)
	return member, nil
}

func normalizeWorkspaceMember(member *WorkspaceMember) {
	member.User.LastLoginAt = member.User.LastLoginAt.UTC()
	member.InvitedAt = member.InvitedAt.UTC()
	member.CreatedAt = member.CreatedAt.UTC()
	member.UpdatedAt = member.UpdatedAt.UTC()
	if member.AcceptedAt != nil {
		value := member.AcceptedAt.UTC()
		member.AcceptedAt = &value
	}
}

func normalizeProjectMember(member *ProjectMember) {
	member.User.LastLoginAt = member.User.LastLoginAt.UTC()
	member.InvitedAt = member.InvitedAt.UTC()
	member.CreatedAt = member.CreatedAt.UTC()
	member.UpdatedAt = member.UpdatedAt.UTC()
	if member.AcceptedAt != nil {
		value := member.AcceptedAt.UTC()
		member.AcceptedAt = &value
	}
}

func authorizeWorkspaceMemberMutation(ctx context.Context, tx *pg.Tx, workspaceID, actorID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM workspaces
			WHERE id=$1 AND deleted_at IS NULL
			FOR SHARE
		)`,
		workspaceID,
	).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrAccessNotFound
	}
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM users
			WHERE id=$1 AND deleted_at IS NULL AND is_disabled=FALSE
			FOR SHARE
		)`,
		actorID,
	).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrAccessNotFound
	}
	var role string
	err := tx.QueryRow(ctx, `
		SELECT role
		FROM workspace_members
		WHERE workspace_id=$1
		  AND user_id=$2
		  AND accepted_at IS NOT NULL`,
		workspaceID,
		actorID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccessNotFound
	}
	if err != nil {
		return err
	}
	if role != string(WorkspaceRoleAdmin) {
		return ErrAuthorizationDenied
	}
	return nil
}

func projectWorkspaceForMembershipMutation(ctx context.Context, tx *pg.Tx, projectID string) (string, error) {
	var workspaceID string
	err := tx.QueryRow(ctx, `
		SELECT p.workspace_id
		FROM projects p
		JOIN workspaces w ON w.id=p.workspace_id
		WHERE p.id=$1
		  AND p.deleted_at IS NULL
		  AND w.deleted_at IS NULL
		FOR SHARE OF p, w`,
		projectID,
	).Scan(&workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAccessNotFound
	}
	if err != nil {
		return "", err
	}
	return workspaceID, nil
}

// authorizeProjectMemberMutation is called only after the workspace and
// project advisory locks are held. Every supported workspace/project
// membership mutation takes those same locks, so actor revocation, target
// inspection, invariant checks, and the write share one serialization point.
func authorizeProjectMemberMutation(ctx context.Context, tx *pg.Tx, projectID, workspaceID, actorID string) (bool, error) {
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM users
			WHERE id=$1 AND deleted_at IS NULL AND is_disabled=FALSE
			FOR SHARE
		)`,
		actorID,
	).Scan(&active); err != nil {
		return false, err
	}
	if !active {
		return false, ErrAccessNotFound
	}
	var workspaceRole, projectRole *string
	err := tx.QueryRow(ctx, `
		SELECT
			(SELECT role
			 FROM workspace_members
			 WHERE workspace_id=$1 AND user_id=$3
			   AND accepted_at IS NOT NULL),
			(SELECT role
			 FROM project_members
			 WHERE project_id=$2 AND user_id=$3
			   AND accepted_at IS NOT NULL)`,
		workspaceID,
		projectID,
		actorID,
	).Scan(&workspaceRole, &projectRole)
	if err != nil {
		return false, err
	}
	var parsedWorkspaceRole WorkspaceRole
	if workspaceRole != nil {
		role, ok := ParseWorkspaceRole(*workspaceRole)
		if !ok {
			return false, fmt.Errorf("identity membership authorization: invalid workspace role %q", *workspaceRole)
		}
		parsedWorkspaceRole = role
	}
	var parsedProjectRole ProjectRole
	if projectRole != nil {
		role, ok := ParseProjectRole(*projectRole)
		if !ok {
			return false, fmt.Errorf("identity membership authorization: invalid project role %q", *projectRole)
		}
		parsedProjectRole = role
	}
	if parsedWorkspaceRole == "" && parsedProjectRole == "" {
		return false, ErrAccessNotFound
	}
	decision := EvaluateAccess(AccessRequest{
		WorkspaceRole: parsedWorkspaceRole,
		ProjectRole:   parsedProjectRole,
		Capability:    CapabilityProjectMembersManage,
	})
	if !decision.Allowed {
		return false, ErrAuthorizationDenied
	}
	return parsedWorkspaceRole != WorkspaceRoleAdmin && parsedProjectRole == ProjectRoleEditor, nil
}

func requireWorkspaceRemovalPreservesProjectOwners(ctx context.Context, tx *pg.Tx, workspaceID, excludedUserID string) error {
	var orphaned bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM project_members target
			JOIN projects p ON p.id=target.project_id
			WHERE p.workspace_id=$1
			  AND target.user_id=$2
			  AND target.role='owner'
			  AND target.accepted_at IS NOT NULL
			  AND NOT EXISTS (
				  SELECT 1
				  FROM project_members alternate
				  JOIN users u ON u.id=alternate.user_id
				  WHERE alternate.project_id=target.project_id
				    AND alternate.user_id<>$2
				    AND alternate.role='owner'
				    AND alternate.accepted_at IS NOT NULL
				    AND u.deleted_at IS NULL
				    AND u.is_disabled=FALSE
			  )
		)`,
		workspaceID,
		excludedUserID,
	).Scan(&orphaned); err != nil {
		return err
	}
	if orphaned {
		return ErrLastProjectOwner
	}
	return nil
}

func requireAnotherWorkspaceAdmin(ctx context.Context, tx *pg.Tx, workspaceID, excludedUserID string) error {
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM workspace_members wm
		JOIN users u ON u.id=wm.user_id
		WHERE wm.workspace_id=$1
		  AND wm.user_id<>$2
		  AND wm.role='admin'
		  AND wm.accepted_at IS NOT NULL
		  AND u.deleted_at IS NULL
		  AND u.is_disabled=FALSE`,
		workspaceID,
		excludedUserID,
	).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrLastWorkspaceAdmin
	}
	return nil
}

func requireAnotherProjectOwner(ctx context.Context, tx *pg.Tx, projectID, excludedUserID string) error {
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM project_members pm
		JOIN users u ON u.id=pm.user_id
		WHERE pm.project_id=$1
		  AND pm.user_id<>$2
		  AND pm.role='owner'
		  AND pm.accepted_at IS NOT NULL
		  AND u.deleted_at IS NULL
		  AND u.is_disabled=FALSE`,
		projectID,
		excludedUserID,
	).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrLastProjectOwner
	}
	return nil
}

func nullableIdentityID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func isIdentityUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
