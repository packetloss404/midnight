// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package github

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbpg "github.com/midnight/midnight/internal/db/pg"
)

var (
	ErrSourceLinkNotFound        = errors.New("github: source link not found")
	ErrInstallationGrantNotFound = errors.New("github: installation grant not found")
	ErrRepositoryGrantNotFound   = errors.New("github: repository grant not found")
	ErrSourceLinkConflict        = errors.New("github: source link conflict")
)

// Source-link transactions share one cross-path lock order with webhook
// intake:
//
//  1. discover the complete candidate installation set without row locks;
//  2. acquire installation advisory locks in ascending numeric order;
//  3. acquire workspace, project, service, link, and grant row locks.
//
// A caller that discovers a different installation after step 3 must abort and
// retry from step 1. It must never acquire the newly observed installation
// while holding a product row lock.

// InstallationGrant exclusively assigns one GitHub App installation to one
// Midnight workspace. The database also enforces the global exclusivity.
type InstallationGrant struct {
	WorkspaceID     string    `json:"workspace_id"`
	InstallationID  int64     `json:"installation_id"`
	AccountLogin    string    `json:"account"`
	TargetType      string    `json:"target_type"`
	GrantedByActor  string    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ResourceVersion int64     `json:"resource_version"`
}

func (g InstallationGrant) Normalize() InstallationGrant {
	g.WorkspaceID = strings.TrimSpace(g.WorkspaceID)
	g.AccountLogin = strings.TrimSpace(g.AccountLogin)
	g.TargetType = strings.TrimSpace(g.TargetType)
	g.GrantedByActor = strings.TrimSpace(g.GrantedByActor)
	return g
}

func (g InstallationGrant) Validate() error {
	g = g.Normalize()
	if g.WorkspaceID == "" || g.InstallationID <= 0 {
		return fmt.Errorf("%w: workspace and installation ids are required", ErrLinkInvalid)
	}
	if g.AccountLogin == "" || g.GrantedByActor == "" {
		return fmt.Errorf("%w: account and granting actor are required", ErrLinkInvalid)
	}
	if g.TargetType != "Organization" && g.TargetType != "User" {
		return fmt.Errorf("%w: target_type must be Organization or User", ErrLinkInvalid)
	}
	return nil
}

// RepositoryGrant is the recovery-owned proof that one exact immutable GitHub
// repository id was visible through an exclusively assigned installation.
type RepositoryGrant struct {
	WorkspaceID     string    `json:"workspace_id"`
	InstallationID  int64     `json:"installation_id"`
	RepositoryID    int64     `json:"repository_id"`
	Repository      string    `json:"repo"`
	CloneURL        string    `json:"-"`
	DefaultBranch   string    `json:"default_branch"`
	Private         bool      `json:"private"`
	VerifiedAt      time.Time `json:"repository_access_verified_at"`
	GrantedByActor  string    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ResourceVersion int64     `json:"resource_version"`
}

func (g RepositoryGrant) Normalize() RepositoryGrant {
	g.WorkspaceID = strings.TrimSpace(g.WorkspaceID)
	g.Repository = strings.ToLower(strings.TrimSpace(g.Repository))
	g.CloneURL = strings.TrimSpace(g.CloneURL)
	g.DefaultBranch = strings.TrimSpace(strings.TrimPrefix(g.DefaultBranch, "refs/heads/"))
	g.GrantedByActor = strings.TrimSpace(g.GrantedByActor)
	return g
}

func (g RepositoryGrant) Validate() error {
	g = g.Normalize()
	if g.WorkspaceID == "" || g.InstallationID <= 0 || g.RepositoryID <= 0 {
		return fmt.Errorf("%w: workspace, installation, and repository ids are required", ErrLinkInvalid)
	}
	if g.DefaultBranch == "" || g.GrantedByActor == "" {
		return fmt.Errorf("%w: default branch and granting actor are required", ErrLinkInvalid)
	}
	if g.CloneURL == "" {
		return fmt.Errorf("%w: verified clone_url is required", ErrLinkInvalid)
	}
	if g.VerifiedAt.IsZero() {
		return fmt.Errorf("%w: repository access verification is required", ErrLinkInvalid)
	}
	link := ServiceLink{
		InstallationID: g.InstallationID,
		RepositoryID:   g.RepositoryID,
		Repo:           g.Repository,
		Branch:         g.DefaultBranch,
		CloneURL:       g.CloneURL,
	}
	return link.Validate()
}

// DurableServiceLink is canonical source truth. RepositoryAccessVerifiedAt is
// the current repository grant observation; LinkRepositoryAccessVerifiedAt is
// the verification copied into the link when it was written. Neither proves
// permanent upstream access or a successful deployment.
type DurableServiceLink struct {
	ServiceID                        string `json:"service_id"`
	ProjectID                        string `json:"project_id"`
	WorkspaceID                      string `json:"workspace_id"`
	Link                             ServiceLink
	AutoDeploy                       bool      `json:"auto_deploy"`
	LinkedByUserID                   string    `json:"-"`
	InstallationGrantResourceVersion int64     `json:"installation_grant_resource_version"`
	InstallationGrantCreatedAt       time.Time `json:"installation_grant_created_at"`
	InstallationGrantUpdatedAt       time.Time `json:"installation_grant_updated_at"`
	RepositoryGrantResourceVersion   int64     `json:"repository_grant_resource_version"`
	RepositoryGrantCreatedAt         time.Time `json:"repository_grant_created_at"`
	RepositoryGrantUpdatedAt         time.Time `json:"repository_grant_updated_at"`
	RepositoryAccessVerifiedAt       time.Time `json:"repository_access_verified_at"`
	LinkRepositoryAccessVerifiedAt   time.Time `json:"link_repository_access_verified_at"`
	// VerifiedAt is retained as a compatibility alias for the link's copied
	// verification timestamp. New durable routing code must use the explicit
	// grant/link fields above so the two authority observations cannot be
	// conflated.
	VerifiedAt      time.Time `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	ResourceVersion int64     `json:"resource_version"`
}

func (l DurableServiceLink) Normalize() DurableServiceLink {
	l.ServiceID = strings.TrimSpace(l.ServiceID)
	l.ProjectID = strings.TrimSpace(l.ProjectID)
	l.WorkspaceID = strings.TrimSpace(l.WorkspaceID)
	l.LinkedByUserID = strings.TrimSpace(l.LinkedByUserID)
	l.Link = l.Link.Normalize()
	l.Link.Repo = strings.ToLower(l.Link.Repo)
	return l
}

func (l DurableServiceLink) Validate() error {
	l = l.Normalize()
	if l.ServiceID == "" || l.ProjectID == "" || l.WorkspaceID == "" {
		return fmt.Errorf("%w: service, project, and workspace ids are required", ErrLinkInvalid)
	}
	if l.Link.RepositoryID <= 0 {
		return fmt.Errorf("%w: repository_id required", ErrLinkInvalid)
	}
	if l.Link.WaitForCI || len(l.Link.RequiredChecks) != 0 {
		return fmt.Errorf("%w: wait-for-CI is not admitted by the canonical source-link gate", ErrLinkInvalid)
	}
	return l.Link.Validate()
}

// SourceLinkStore owns canonical GitHub installation grants, exact repository
// grants, and service links. Product coordinators retain authorization,
// idempotency, and audit ownership and call the InTx methods inside their
// serializable transaction.
type SourceLinkStore struct {
	Pool *dbpg.Pool
	Now  func() time.Time
}

func NewSourceLinkStore(pool *dbpg.Pool) *SourceLinkStore {
	return &SourceLinkStore{Pool: pool}
}

func (s *SourceLinkStore) transactionNow(
	ctx context.Context,
	tx *dbpg.Tx,
) (time.Time, error) {
	if s != nil && s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond), nil
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("read github source authority database time: %w", err)
	}
	return now.UTC().Truncate(time.Microsecond), nil
}

func (s *SourceLinkStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("github source link store pool is required")
	}
	return nil
}

// ListInstallationGrants returns only assignments belonging to one exact
// workspace. It never enumerates the GitHub App's global installation set.
func (s *SourceLinkStore) ListInstallationGrants(ctx context.Context, workspaceID string) ([]InstallationGrant, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return listInstallationGrants(ctx, s.Pool, workspaceID)
}

// ListInstallationGrantsInTx gives a coordinator the installation metadata
// associated with its transaction-bound repository discovery result.
func (s *SourceLinkStore) ListInstallationGrantsInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
) ([]InstallationGrant, error) {
	if tx == nil {
		return nil, errors.New("github source link transaction is required")
	}
	return listInstallationGrants(ctx, tx, workspaceID)
}

func listInstallationGrants(
	ctx context.Context,
	conn dbpg.Conn,
	workspaceID string,
) ([]InstallationGrant, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("%w: workspace id required", ErrLinkInvalid)
	}
	rows, err := conn.Query(ctx, installationGrantSelect+`
		 WHERE workspace_id=$1
		 ORDER BY account_login,installation_id`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list github installation grants: %w", err)
	}
	defer rows.Close()
	var out []InstallationGrant
	for rows.Next() {
		value, scanErr := scanInstallationGrant(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan github installation grant: %w", scanErr)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list github installation grants: %w", err)
	}
	return out, nil
}

func (s *SourceLinkStore) GetInstallationGrant(
	ctx context.Context,
	workspaceID string,
	installationID int64,
) (InstallationGrant, error) {
	if err := s.ready(); err != nil {
		return InstallationGrant{}, err
	}
	value, err := scanInstallationGrant(s.Pool.QueryRow(ctx, installationGrantSelect+`
		 WHERE workspace_id=$1 AND installation_id=$2`,
		strings.TrimSpace(workspaceID),
		installationID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return InstallationGrant{}, ErrInstallationGrantNotFound
	}
	if err != nil {
		return InstallationGrant{}, fmt.Errorf("get github installation grant: %w", err)
	}
	return value, nil
}

// LockInstallationGrantInTx acquires the installation-wide advisory lock used
// by every source-link mutation before locking the exact workspace assignment.
func (s *SourceLinkStore) LockInstallationGrantInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	installationID int64,
) (InstallationGrant, error) {
	if tx == nil {
		return InstallationGrant{}, errors.New("github source link transaction is required")
	}
	if err := lockInstallationInTx(ctx, tx, installationID); err != nil {
		return InstallationGrant{}, err
	}
	value, err := scanInstallationGrant(tx.QueryRow(ctx, installationGrantSelect+`
		 WHERE workspace_id=$1 AND installation_id=$2
		 FOR UPDATE`,
		strings.TrimSpace(workspaceID),
		installationID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return InstallationGrant{}, ErrInstallationGrantNotFound
	}
	if err != nil {
		return InstallationGrant{}, fmt.Errorf("lock github installation grant: %w", err)
	}
	return value, nil
}

func (s *SourceLinkStore) PutInstallationGrantInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	value InstallationGrant,
) (InstallationGrant, error) {
	if tx == nil {
		return InstallationGrant{}, errors.New("github source link transaction is required")
	}
	value = value.Normalize()
	if err := value.Validate(); err != nil {
		return InstallationGrant{}, err
	}
	if err := lockInstallationInTx(ctx, tx, value.InstallationID); err != nil {
		return InstallationGrant{}, err
	}
	now, err := s.transactionNow(ctx, tx)
	if err != nil {
		return InstallationGrant{}, err
	}
	value.CreatedAt = now
	value.UpdatedAt = now
	if value.ResourceVersion < 1 {
		value.ResourceVersion = 1
	}
	out, err := scanInstallationGrant(tx.QueryRow(ctx, `
		INSERT INTO github_workspace_installation_grants (
			workspace_id,installation_id,account_login,target_type,
			granted_by_actor,created_at,updated_at,resource_version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (installation_id)
		DO UPDATE SET
			account_login=EXCLUDED.account_login,
			target_type=EXCLUDED.target_type,
			granted_by_actor=EXCLUDED.granted_by_actor,
			updated_at=EXCLUDED.updated_at,
			resource_version=github_workspace_installation_grants.resource_version+1
		WHERE github_workspace_installation_grants.workspace_id=EXCLUDED.workspace_id
		RETURNING workspace_id,installation_id,account_login,target_type,
		          granted_by_actor,created_at,updated_at,resource_version`,
		value.WorkspaceID,
		value.InstallationID,
		value.AccountLogin,
		value.TargetType,
		value.GrantedByActor,
		value.CreatedAt.UTC(),
		value.UpdatedAt.UTC(),
		value.ResourceVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return InstallationGrant{}, ErrSourceLinkConflict
	}
	if err != nil {
		return InstallationGrant{}, classifySourceLinkWriteError("put github installation grant", err)
	}
	return out, nil
}

func (s *SourceLinkStore) ListRepositoryGrants(ctx context.Context, workspaceID string) ([]RepositoryGrant, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return listRepositoryGrants(ctx, s.Pool, workspaceID)
}

// ListRepositoryGrantsInTx gives a coordinator a current-authority view in
// the same transaction that revalidates session and workspace membership.
func (s *SourceLinkStore) ListRepositoryGrantsInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
) ([]RepositoryGrant, error) {
	if tx == nil {
		return nil, errors.New("github source link transaction is required")
	}
	return listRepositoryGrants(ctx, tx, workspaceID)
}

func listRepositoryGrants(
	ctx context.Context,
	conn dbpg.Conn,
	workspaceID string,
) ([]RepositoryGrant, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("%w: workspace id required", ErrLinkInvalid)
	}
	rows, err := conn.Query(ctx, repositoryGrantSelect+`
		 WHERE repository.workspace_id=$1
		 ORDER BY repository.repository_full_name,repository.repository_id`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list github repository grants: %w", err)
	}
	defer rows.Close()
	var out []RepositoryGrant
	for rows.Next() {
		value, scanErr := scanRepositoryGrant(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan github repository grant: %w", scanErr)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list github repository grants: %w", err)
	}
	return out, nil
}

func (s *SourceLinkStore) GetRepositoryGrant(
	ctx context.Context,
	workspaceID string,
	installationID int64,
	repositoryID int64,
) (RepositoryGrant, error) {
	if err := s.ready(); err != nil {
		return RepositoryGrant{}, err
	}
	value, err := scanRepositoryGrant(s.Pool.QueryRow(ctx, repositoryGrantSelect+`
		 WHERE repository.workspace_id=$1
		   AND repository.installation_id=$2
		   AND repository.repository_id=$3`,
		strings.TrimSpace(workspaceID),
		installationID,
		repositoryID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryGrant{}, ErrRepositoryGrantNotFound
	}
	if err != nil {
		return RepositoryGrant{}, fmt.Errorf("get github repository grant: %w", err)
	}
	return value, nil
}

// GetRepositoryGrantByIDInTx resolves GitHub's globally immutable repository
// id only inside one exact workspace. The schema's global repository-id
// uniqueness prevents an ambiguous result after repository transfer.
func (s *SourceLinkStore) GetRepositoryGrantByIDInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	repositoryID int64,
) (RepositoryGrant, error) {
	if tx == nil {
		return RepositoryGrant{}, errors.New("github source link transaction is required")
	}
	rows, err := tx.Query(ctx, repositoryGrantSelect+`
		 WHERE repository.workspace_id=$1
		   AND repository.repository_id=$2
		 ORDER BY repository.installation_id`,
		strings.TrimSpace(workspaceID),
		repositoryID,
	)
	if err != nil {
		return RepositoryGrant{}, fmt.Errorf("get github repository grant by id: %w", err)
	}
	defer rows.Close()
	var matches []RepositoryGrant
	for rows.Next() {
		value, scanErr := scanRepositoryGrant(rows)
		if scanErr != nil {
			return RepositoryGrant{}, fmt.Errorf("scan github repository grant by id: %w", scanErr)
		}
		matches = append(matches, value)
	}
	if err := rows.Err(); err != nil {
		return RepositoryGrant{}, fmt.Errorf("get github repository grant by id: %w", err)
	}
	if len(matches) == 0 {
		return RepositoryGrant{}, ErrRepositoryGrantNotFound
	}
	if len(matches) != 1 {
		return RepositoryGrant{}, ErrSourceLinkConflict
	}
	return matches[0], nil
}

func (s *SourceLinkStore) LockRepositoryGrantInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	installationID int64,
	repositoryID int64,
) (RepositoryGrant, error) {
	if tx == nil {
		return RepositoryGrant{}, errors.New("github source link transaction is required")
	}
	if _, err := s.LockInstallationGrantInTx(ctx, tx, workspaceID, installationID); err != nil {
		return RepositoryGrant{}, err
	}
	value, err := scanRepositoryGrant(tx.QueryRow(ctx, repositoryGrantSelect+`
		 WHERE repository.workspace_id=$1
		   AND repository.installation_id=$2
		   AND repository.repository_id=$3
		 FOR UPDATE OF repository`,
		strings.TrimSpace(workspaceID),
		installationID,
		repositoryID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryGrant{}, ErrRepositoryGrantNotFound
	}
	if err != nil {
		return RepositoryGrant{}, fmt.Errorf("lock github repository grant: %w", err)
	}
	return value, nil
}

func (s *SourceLinkStore) PutRepositoryGrantInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	value RepositoryGrant,
) (RepositoryGrant, error) {
	if tx == nil {
		return RepositoryGrant{}, errors.New("github source link transaction is required")
	}
	value = value.Normalize()
	if err := value.Validate(); err != nil {
		return RepositoryGrant{}, err
	}
	if _, err := s.LockInstallationGrantInTx(ctx, tx, value.WorkspaceID, value.InstallationID); err != nil {
		return RepositoryGrant{}, err
	}
	now, err := s.transactionNow(ctx, tx)
	if err != nil {
		return RepositoryGrant{}, err
	}
	value.VerifiedAt = value.VerifiedAt.UTC().Truncate(time.Microsecond)
	if value.VerifiedAt.After(now) {
		return RepositoryGrant{}, fmt.Errorf("%w: repository verification cannot be in the future", ErrLinkInvalid)
	}
	value.CreatedAt = now
	value.UpdatedAt = now
	if value.ResourceVersion < 1 {
		value.ResourceVersion = 1
	}
	out, err := scanRepositoryGrant(tx.QueryRow(ctx, `
		INSERT INTO github_workspace_repository_grants (
			workspace_id,installation_id,repository_id,
			repository_full_name,clone_url,default_branch,
			repository_private,repository_access_verified_at,
			granted_by_actor,created_at,updated_at,resource_version
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (workspace_id,installation_id,repository_id)
		DO UPDATE SET
			repository_full_name=EXCLUDED.repository_full_name,
			clone_url=EXCLUDED.clone_url,
			default_branch=EXCLUDED.default_branch,
			repository_private=EXCLUDED.repository_private,
			repository_access_verified_at=EXCLUDED.repository_access_verified_at,
			granted_by_actor=EXCLUDED.granted_by_actor,
			updated_at=EXCLUDED.updated_at,
			resource_version=github_workspace_repository_grants.resource_version+1
		RETURNING workspace_id,installation_id,repository_id,
		          repository_full_name,clone_url,default_branch,
		          repository_private,repository_access_verified_at,
		          granted_by_actor,created_at,updated_at,resource_version`,
		value.WorkspaceID,
		value.InstallationID,
		value.RepositoryID,
		value.Repository,
		value.CloneURL,
		value.DefaultBranch,
		value.Private,
		value.VerifiedAt,
		value.GrantedByActor,
		value.CreatedAt.UTC(),
		value.UpdatedAt.UTC(),
		value.ResourceVersion,
	))
	if err != nil {
		return RepositoryGrant{}, classifySourceLinkWriteError("put github repository grant", err)
	}
	return out, nil
}

// RevokeRepositoryGrantInTx deletes dependent links before the exact
// repository grant while holding the same installation advisory lock used by
// link writes. The installation assignment remains available for other repos.
func (s *SourceLinkStore) RevokeRepositoryGrantInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	installationID int64,
	repositoryID int64,
) ([]string, error) {
	grant, err := s.LockRepositoryGrantInTx(ctx, tx, workspaceID, installationID, repositoryID)
	if err != nil {
		return nil, err
	}
	serviceIDs, err := deleteLinksForGrantInTx(
		ctx,
		tx,
		grant.WorkspaceID,
		grant.InstallationID,
		grant.RepositoryID,
	)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM github_workspace_repository_grants
		 WHERE workspace_id=$1 AND installation_id=$2 AND repository_id=$3`,
		grant.WorkspaceID,
		grant.InstallationID,
		grant.RepositoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("delete github repository grant: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrRepositoryGrantNotFound
	}
	return serviceIDs, nil
}

// RevokeInstallationGrantInTx is the immediate workspace-source revocation
// primitive. It removes every dependent service link, then every repository
// grant, and finally the exclusive installation assignment under one lock.
func (s *SourceLinkStore) RevokeInstallationGrantInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	installationID int64,
) ([]string, error) {
	grant, err := s.LockInstallationGrantInTx(ctx, tx, workspaceID, installationID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM service_github_links
		 WHERE workspace_id=$1 AND installation_id=$2
		RETURNING service_id`,
		grant.WorkspaceID,
		grant.InstallationID,
	)
	if err != nil {
		return nil, fmt.Errorf("unlink services for github installation revocation: %w", err)
	}
	serviceIDs, err := collectDeletedServiceIDs(rows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM github_workspace_repository_grants
		 WHERE workspace_id=$1 AND installation_id=$2`,
		grant.WorkspaceID,
		grant.InstallationID,
	); err != nil {
		return nil, fmt.Errorf("delete github installation repository grants: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM github_workspace_installation_grants
		 WHERE workspace_id=$1 AND installation_id=$2`,
		grant.WorkspaceID,
		grant.InstallationID,
	)
	if err != nil {
		return nil, fmt.Errorf("delete github installation grant: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrInstallationGrantNotFound
	}
	return serviceIDs, nil
}

// GetServiceLink requires the full tenant lineage. A service id by itself is
// deliberately insufficient for a product-facing read.
func (s *SourceLinkStore) GetServiceLink(
	ctx context.Context,
	workspaceID string,
	projectID string,
	serviceID string,
) (DurableServiceLink, error) {
	if err := s.ready(); err != nil {
		return DurableServiceLink{}, err
	}
	return getServiceLink(ctx, s.Pool, workspaceID, projectID, serviceID)
}

// GetServiceLinkInTx lets product authorization and its exact-scope read share
// one transaction snapshot.
func (s *SourceLinkStore) GetServiceLinkInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	projectID string,
	serviceID string,
) (DurableServiceLink, error) {
	if tx == nil {
		return DurableServiceLink{}, errors.New("github source link transaction is required")
	}
	return getServiceLink(ctx, tx, workspaceID, projectID, serviceID)
}

func getServiceLink(
	ctx context.Context,
	conn dbpg.Conn,
	workspaceID string,
	projectID string,
	serviceID string,
) (DurableServiceLink, error) {
	value, err := scanDurableServiceLink(conn.QueryRow(ctx, serviceLinkSelect+`
		 WHERE link.workspace_id=$1
		   AND link.project_id=$2
		   AND link.service_id=$3`,
		strings.TrimSpace(workspaceID),
		strings.TrimSpace(projectID),
		strings.TrimSpace(serviceID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableServiceLink{}, ErrSourceLinkNotFound
	}
	if err != nil {
		return DurableServiceLink{}, fmt.Errorf("get service github link: %w", err)
	}
	return value, nil
}

func (s *SourceLinkStore) PutServiceLinkInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	value DurableServiceLink,
) (DurableServiceLink, error) {
	if tx == nil {
		return DurableServiceLink{}, errors.New("github source link transaction is required")
	}
	value = value.Normalize()
	if err := value.Validate(); err != nil {
		return DurableServiceLink{}, err
	}

	installationIDs := []int64{value.Link.InstallationID}
	var previousInstallationID int64
	err := tx.QueryRow(ctx, `
		SELECT installation_id
		  FROM service_github_links
		 WHERE service_id=$1`,
		value.ServiceID,
	).Scan(&previousInstallationID)
	if err == nil && previousInstallationID != value.Link.InstallationID {
		installationIDs = append(installationIDs, previousInstallationID)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DurableServiceLink{}, fmt.Errorf("read existing github link lock scope: %w", err)
	}
	if err := LockInstallationsInTx(ctx, tx, installationIDs); err != nil {
		return DurableServiceLink{}, err
	}
	return s.PutServiceLinkUnderInstallationLocksInTx(ctx, tx, value, installationIDs)
}

// PutServiceLinkUnderInstallationLocksInTx writes a link after the caller has
// acquired every installation in lockedInstallationIDs before taking any
// product row lock. It fails closed when the current link drifted to an
// installation outside that set; callers must roll back and retry discovery.
func (s *SourceLinkStore) PutServiceLinkUnderInstallationLocksInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	value DurableServiceLink,
	lockedInstallationIDs []int64,
) (DurableServiceLink, error) {
	if tx == nil {
		return DurableServiceLink{}, errors.New("github source link transaction is required")
	}
	value = value.Normalize()
	if err := value.Validate(); err != nil {
		return DurableServiceLink{}, err
	}
	if !containsInstallationID(lockedInstallationIDs, value.Link.InstallationID) {
		return DurableServiceLink{}, fmt.Errorf(
			"%w: requested installation is outside the acquired lock set",
			ErrSourceLinkConflict,
		)
	}
	if err := lockExactServiceScopeInTx(
		ctx,
		tx,
		value.WorkspaceID,
		value.ProjectID,
		value.ServiceID,
	); err != nil {
		return DurableServiceLink{}, err
	}
	var currentInstallationID int64
	err := tx.QueryRow(ctx, `
		SELECT installation_id
		  FROM service_github_links
		 WHERE service_id=$1
		 FOR UPDATE`,
		value.ServiceID,
	).Scan(&currentInstallationID)
	if err == nil && !containsInstallationID(lockedInstallationIDs, currentInstallationID) {
		return DurableServiceLink{}, ErrSourceLinkConflict
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DurableServiceLink{}, fmt.Errorf("lock existing github link: %w", err)
	}
	grant, err := s.LockRepositoryGrantInTx(
		ctx,
		tx,
		value.WorkspaceID,
		value.Link.InstallationID,
		value.Link.RepositoryID,
	)
	if err != nil {
		return DurableServiceLink{}, err
	}
	if grant.Repository != value.Link.Repo {
		return DurableServiceLink{}, ErrSourceLinkConflict
	}
	installation, err := s.LockInstallationGrantInTx(
		ctx,
		tx,
		value.WorkspaceID,
		value.Link.InstallationID,
	)
	if err != nil {
		return DurableServiceLink{}, err
	}

	now, err := s.transactionNow(ctx, tx)
	if err != nil {
		return DurableServiceLink{}, err
	}
	value.CreatedAt = now
	value.UpdatedAt = now
	value.InstallationGrantResourceVersion = installation.ResourceVersion
	value.InstallationGrantCreatedAt = installation.CreatedAt
	value.InstallationGrantUpdatedAt = installation.UpdatedAt
	value.RepositoryGrantResourceVersion = grant.ResourceVersion
	value.RepositoryGrantCreatedAt = grant.CreatedAt
	value.RepositoryGrantUpdatedAt = grant.UpdatedAt
	value.RepositoryAccessVerifiedAt = grant.VerifiedAt
	value.LinkRepositoryAccessVerifiedAt = grant.VerifiedAt
	value.VerifiedAt = value.LinkRepositoryAccessVerifiedAt
	if value.ResourceVersion < 1 {
		value.ResourceVersion = 1
	}
	out, err := scanDurableServiceLink(tx.QueryRow(ctx, `
		INSERT INTO service_github_links (
			service_id,project_id,workspace_id,installation_id,
			repository_id,repository_full_name,branch,watch_paths,
			root_directory,config_path,wait_for_ci,required_checks,
			auto_deploy,linked_by_user_id,
			repository_access_verified_at,created_at,updated_at,
			resource_version
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),
			$15,$16,$17,$18
		)
		ON CONFLICT (service_id)
		DO UPDATE SET
			project_id=EXCLUDED.project_id,
			workspace_id=EXCLUDED.workspace_id,
			installation_id=EXCLUDED.installation_id,
			repository_id=EXCLUDED.repository_id,
			repository_full_name=EXCLUDED.repository_full_name,
			branch=EXCLUDED.branch,
			watch_paths=EXCLUDED.watch_paths,
			root_directory=EXCLUDED.root_directory,
			config_path=EXCLUDED.config_path,
			wait_for_ci=EXCLUDED.wait_for_ci,
			required_checks=EXCLUDED.required_checks,
			auto_deploy=EXCLUDED.auto_deploy,
			linked_by_user_id=EXCLUDED.linked_by_user_id,
			repository_access_verified_at=EXCLUDED.repository_access_verified_at,
			updated_at=EXCLUDED.updated_at,
			resource_version=service_github_links.resource_version+1
		RETURNING service_id,project_id,workspace_id,installation_id,
		          repository_id,repository_full_name,branch,watch_paths,
		          root_directory,config_path,wait_for_ci,required_checks,
		          auto_deploy,COALESCE(linked_by_user_id,''),
		          repository_access_verified_at,created_at,updated_at,
		          resource_version,$19::TEXT,
		          $20::BIGINT,$21::TIMESTAMPTZ,$22::TIMESTAMPTZ,
		          $23::BIGINT,$24::TIMESTAMPTZ,$25::TIMESTAMPTZ,
		          $26::TIMESTAMPTZ`,
		value.ServiceID,
		value.ProjectID,
		value.WorkspaceID,
		value.Link.InstallationID,
		value.Link.RepositoryID,
		value.Link.Repo,
		value.Link.Branch,
		value.Link.WatchPaths,
		value.Link.RootDirectory,
		value.Link.ConfigPath,
		value.Link.WaitForCI,
		value.Link.RequiredChecks,
		value.AutoDeploy,
		value.LinkedByUserID,
		value.LinkRepositoryAccessVerifiedAt,
		value.CreatedAt.UTC(),
		value.UpdatedAt.UTC(),
		value.ResourceVersion,
		grant.CloneURL,
		value.InstallationGrantResourceVersion,
		value.InstallationGrantCreatedAt.UTC(),
		value.InstallationGrantUpdatedAt.UTC(),
		value.RepositoryGrantResourceVersion,
		value.RepositoryGrantCreatedAt.UTC(),
		value.RepositoryGrantUpdatedAt.UTC(),
		value.RepositoryAccessVerifiedAt.UTC(),
	))
	if err != nil {
		return DurableServiceLink{}, classifySourceLinkWriteError("put service github link", err)
	}
	out.Link.CloneURL = grant.CloneURL
	return out, nil
}

func (s *SourceLinkStore) DeleteServiceLinkInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	projectID string,
	serviceID string,
) (DurableServiceLink, error) {
	if tx == nil {
		return DurableServiceLink{}, errors.New("github source link transaction is required")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	projectID = strings.TrimSpace(projectID)
	serviceID = strings.TrimSpace(serviceID)
	var installationID int64
	if err := tx.QueryRow(ctx, `
		SELECT installation_id
		  FROM service_github_links
		 WHERE workspace_id=$1 AND project_id=$2 AND service_id=$3`,
		workspaceID,
		projectID,
		serviceID,
	).Scan(&installationID); errors.Is(err, pgx.ErrNoRows) {
		return DurableServiceLink{}, ErrSourceLinkNotFound
	} else if err != nil {
		return DurableServiceLink{}, fmt.Errorf("read service github link lock scope: %w", err)
	}
	if err := LockInstallationInTx(ctx, tx, installationID); err != nil {
		return DurableServiceLink{}, err
	}
	return s.DeleteServiceLinkUnderInstallationLocksInTx(
		ctx,
		tx,
		workspaceID,
		projectID,
		serviceID,
		[]int64{installationID},
	)
}

// DeleteServiceLinkUnderInstallationLocksInTx deletes a link only when its
// current installation is part of the advisory-lock set acquired before any
// product row. Lock-scope drift is a conflict, never a reason to acquire
// another installation after the service row has been locked.
func (s *SourceLinkStore) DeleteServiceLinkUnderInstallationLocksInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	projectID string,
	serviceID string,
	lockedInstallationIDs []int64,
) (DurableServiceLink, error) {
	if tx == nil {
		return DurableServiceLink{}, errors.New("github source link transaction is required")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	projectID = strings.TrimSpace(projectID)
	serviceID = strings.TrimSpace(serviceID)
	value, err := scanDurableServiceLink(tx.QueryRow(ctx, serviceLinkSelect+`
		 WHERE link.workspace_id=$1
		   AND link.project_id=$2
		   AND link.service_id=$3
		 FOR UPDATE OF link`,
		workspaceID,
		projectID,
		serviceID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableServiceLink{}, ErrSourceLinkNotFound
	}
	if err != nil {
		return DurableServiceLink{}, fmt.Errorf("lock service github link: %w", err)
	}
	if !containsInstallationID(lockedInstallationIDs, value.Link.InstallationID) {
		return DurableServiceLink{}, ErrSourceLinkConflict
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM service_github_links
		 WHERE workspace_id=$1 AND project_id=$2 AND service_id=$3`,
		workspaceID,
		projectID,
		serviceID,
	)
	if err != nil {
		return DurableServiceLink{}, fmt.Errorf("delete service github link: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return DurableServiceLink{}, ErrSourceLinkNotFound
	}
	return value, nil
}

// FindServiceLinksForPush binds a signed push to exact installation and
// repository identities. The repository name remains an additional equality
// check so a rename cannot silently deploy until recovery refreshes the grant.
func (s *SourceLinkStore) FindServiceLinksForPush(
	ctx context.Context,
	installationID int64,
	repositoryID int64,
	repository string,
	branch string,
) ([]DurableServiceLink, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	repository = strings.ToLower(strings.TrimSpace(repository))
	branch = strings.TrimSpace(strings.TrimPrefix(branch, "refs/heads/"))
	if installationID <= 0 || repositoryID <= 0 || repository == "" || branch == "" {
		return nil, fmt.Errorf("%w: exact push source identity required", ErrLinkInvalid)
	}
	rows, err := s.Pool.Query(ctx, serviceLinkSelect+`
		 WHERE link.installation_id=$1
		   AND link.repository_id=$2
		   AND link.repository_full_name=$3
		   AND link.branch=$4
		   AND link.auto_deploy=TRUE
		 ORDER BY link.service_id`,
		installationID,
		repositoryID,
		repository,
		branch,
	)
	if err != nil {
		return nil, fmt.Errorf("find service github links for push: %w", err)
	}
	defer rows.Close()
	var out []DurableServiceLink
	for rows.Next() {
		value, scanErr := scanDurableServiceLink(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan service github link: %w", scanErr)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find service github links for push: %w", err)
	}
	return out, nil
}

// FindServiceLinksForPushAt binds a retained signed push to authority that
// already existed when the provider delivery reached Midnight. A grant, its
// repository verification, or a service link created/refreshed after
// receivedAt must never retroactively authorize that historical delivery.
func (s *SourceLinkStore) FindServiceLinksForPushAt(
	ctx context.Context,
	installationID int64,
	repositoryID int64,
	repository string,
	branch string,
	receivedAt time.Time,
) ([]DurableServiceLink, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	repository = strings.ToLower(strings.TrimSpace(repository))
	branch = strings.TrimSpace(strings.TrimPrefix(branch, "refs/heads/"))
	receivedAt = receivedAt.UTC()
	if installationID <= 0 || repositoryID <= 0 || repository == "" || branch == "" || receivedAt.IsZero() {
		return nil, fmt.Errorf("%w: exact push source identity and receipt time required", ErrLinkInvalid)
	}
	rows, err := s.Pool.Query(ctx, serviceLinkSelect+`
		 WHERE link.installation_id=$1
		   AND link.repository_id=$2
		   AND link.repository_full_name=$3
		   AND link.branch=$4
		   AND link.auto_deploy=TRUE
		   AND installation.created_at <= $5
		   AND installation.updated_at <= $5
		   AND repository.created_at <= $5
		   AND repository.updated_at <= $5
		   AND repository.repository_access_verified_at <= $5
		   AND link.created_at <= $5
		   AND link.updated_at <= $5
		   AND link.repository_access_verified_at <= $5
		 ORDER BY link.service_id`,
		installationID,
		repositoryID,
		repository,
		branch,
		receivedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("find temporal service github links for push: %w", err)
	}
	defer rows.Close()
	var out []DurableServiceLink
	for rows.Next() {
		value, scanErr := scanDurableServiceLink(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan temporal service github link: %w", scanErr)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find temporal service github links for push: %w", err)
	}
	return out, nil
}

// LockInstallationInTx acquires the installation fence shared by source-link
// mutation/revocation and webhook build intake. Call it before any product row
// lock; reacquisition by the same transaction is safe.
func LockInstallationInTx(ctx context.Context, tx *dbpg.Tx, installationID int64) error {
	if tx == nil {
		return errors.New("github source link transaction is required")
	}
	if installationID <= 0 {
		return fmt.Errorf("%w: installation_id required", ErrLinkInvalid)
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		InstallationAdvisoryLockKey(installationID),
	); err != nil {
		return fmt.Errorf("lock github installation: %w", err)
	}
	return nil
}

func lockInstallationInTx(ctx context.Context, tx *dbpg.Tx, installationID int64) error {
	return LockInstallationInTx(ctx, tx, installationID)
}

// InstallationAdvisoryLockKey is shared with webhook intake so source-link
// mutation/revocation and durable build admission serialize on one key.
func InstallationAdvisoryLockKey(installationID int64) string {
	return fmt.Sprintf("github-installation:%d", installationID)
}

// LockInstallationsInTx deduplicates and orders an installation lock set so
// replacement mutations cannot deadlock each other by acquiring old/new
// installations in opposite orders.
func LockInstallationsInTx(ctx context.Context, tx *dbpg.Tx, installationIDs []int64) error {
	unique := make(map[int64]struct{}, len(installationIDs))
	ordered := make([]int64, 0, len(installationIDs))
	for _, installationID := range installationIDs {
		if installationID <= 0 {
			return fmt.Errorf("%w: installation_id required", ErrLinkInvalid)
		}
		if _, present := unique[installationID]; present {
			continue
		}
		unique[installationID] = struct{}{}
		ordered = append(ordered, installationID)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, installationID := range ordered {
		if err := lockInstallationInTx(ctx, tx, installationID); err != nil {
			return err
		}
	}
	return nil
}

func lockInstallationsInTx(ctx context.Context, tx *dbpg.Tx, installationIDs []int64) error {
	return LockInstallationsInTx(ctx, tx, installationIDs)
}

func containsInstallationID(installationIDs []int64, candidate int64) bool {
	for _, installationID := range installationIDs {
		if installationID == candidate {
			return true
		}
	}
	return false
}

func lockExactServiceScopeInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	projectID string,
	serviceID string,
) error {
	var lockedServiceID string
	err := tx.QueryRow(ctx, `
		SELECT service.id
		  FROM services service
		  JOIN projects project
		    ON project.id=service.project_id
		   AND project.workspace_id=$1
		 WHERE service.id=$2
		   AND service.project_id=$3
		   AND service.deleted_at IS NULL
		   AND project.deleted_at IS NULL
		 FOR UPDATE OF service`,
		workspaceID,
		serviceID,
		projectID,
	).Scan(&lockedServiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSourceLinkConflict
	}
	if err != nil {
		return fmt.Errorf("lock exact service github scope: %w", err)
	}
	return nil
}

func deleteLinksForGrantInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	workspaceID string,
	installationID int64,
	repositoryID int64,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		DELETE FROM service_github_links
		 WHERE workspace_id=$1 AND installation_id=$2 AND repository_id=$3
		RETURNING service_id`,
		workspaceID,
		installationID,
		repositoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("unlink services for github grant revocation: %w", err)
	}
	return collectDeletedServiceIDs(rows)
}

func collectDeletedServiceIDs(rows pgx.Rows) ([]string, error) {
	defer rows.Close()
	var serviceIDs []string
	for rows.Next() {
		var serviceID string
		if err := rows.Scan(&serviceID); err != nil {
			return nil, fmt.Errorf("scan revoked github service link: %w", err)
		}
		serviceIDs = append(serviceIDs, serviceID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read revoked github service links: %w", err)
	}
	sort.Strings(serviceIDs)
	return serviceIDs, nil
}

func classifySourceLinkWriteError(action string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23505" || pgErr.Code == "23514") {
		return fmt.Errorf("%w: %s", ErrSourceLinkConflict, action)
	}
	return fmt.Errorf("%s: %w", action, err)
}

const installationGrantSelect = `
	SELECT workspace_id,installation_id,account_login,target_type,
	       granted_by_actor,created_at,updated_at,resource_version
	  FROM github_workspace_installation_grants`

const repositoryGrantSelect = `
	SELECT repository.workspace_id,repository.installation_id,
	       repository.repository_id,repository.repository_full_name,
	       repository.clone_url,repository.default_branch,
	       repository.repository_private,
	       repository.repository_access_verified_at,
	       repository.granted_by_actor,repository.created_at,
	       repository.updated_at,repository.resource_version
	  FROM github_workspace_repository_grants repository
	  JOIN github_workspace_installation_grants installation
	    ON installation.workspace_id=repository.workspace_id
	   AND installation.installation_id=repository.installation_id`

const serviceLinkSelect = `
	SELECT link.service_id,link.project_id,link.workspace_id,
	       link.installation_id,link.repository_id,
	       link.repository_full_name,link.branch,link.watch_paths,
	       link.root_directory,link.config_path,link.wait_for_ci,
	       link.required_checks,link.auto_deploy,
	       COALESCE(link.linked_by_user_id,''),
	       link.repository_access_verified_at,link.created_at,
	       link.updated_at,link.resource_version,repository.clone_url,
	       installation.resource_version,installation.created_at,
	       installation.updated_at,repository.resource_version,
	       repository.created_at,repository.updated_at,
	       repository.repository_access_verified_at
	  FROM service_github_links link
	  JOIN github_workspace_repository_grants repository
	    ON repository.workspace_id=link.workspace_id
	   AND repository.installation_id=link.installation_id
	   AND repository.repository_id=link.repository_id
	   AND repository.repository_full_name=link.repository_full_name
	  JOIN github_workspace_installation_grants installation
	    ON installation.workspace_id=repository.workspace_id
	   AND installation.installation_id=repository.installation_id`

type rowScanner interface {
	Scan(...any) error
}

func scanInstallationGrant(row rowScanner) (InstallationGrant, error) {
	var value InstallationGrant
	err := row.Scan(
		&value.WorkspaceID,
		&value.InstallationID,
		&value.AccountLogin,
		&value.TargetType,
		&value.GrantedByActor,
		&value.CreatedAt,
		&value.UpdatedAt,
		&value.ResourceVersion,
	)
	if err != nil {
		return InstallationGrant{}, err
	}
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value, nil
}

func scanRepositoryGrant(row rowScanner) (RepositoryGrant, error) {
	var value RepositoryGrant
	err := row.Scan(
		&value.WorkspaceID,
		&value.InstallationID,
		&value.RepositoryID,
		&value.Repository,
		&value.CloneURL,
		&value.DefaultBranch,
		&value.Private,
		&value.VerifiedAt,
		&value.GrantedByActor,
		&value.CreatedAt,
		&value.UpdatedAt,
		&value.ResourceVersion,
	)
	if err != nil {
		return RepositoryGrant{}, err
	}
	value.VerifiedAt = value.VerifiedAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value, nil
}

func scanDurableServiceLink(row rowScanner) (DurableServiceLink, error) {
	var value DurableServiceLink
	var cloneURL string
	err := row.Scan(
		&value.ServiceID,
		&value.ProjectID,
		&value.WorkspaceID,
		&value.Link.InstallationID,
		&value.Link.RepositoryID,
		&value.Link.Repo,
		&value.Link.Branch,
		&value.Link.WatchPaths,
		&value.Link.RootDirectory,
		&value.Link.ConfigPath,
		&value.Link.WaitForCI,
		&value.Link.RequiredChecks,
		&value.AutoDeploy,
		&value.LinkedByUserID,
		&value.LinkRepositoryAccessVerifiedAt,
		&value.CreatedAt,
		&value.UpdatedAt,
		&value.ResourceVersion,
		&cloneURL,
		&value.InstallationGrantResourceVersion,
		&value.InstallationGrantCreatedAt,
		&value.InstallationGrantUpdatedAt,
		&value.RepositoryGrantResourceVersion,
		&value.RepositoryGrantCreatedAt,
		&value.RepositoryGrantUpdatedAt,
		&value.RepositoryAccessVerifiedAt,
	)
	if err != nil {
		return DurableServiceLink{}, err
	}
	value.Link.CloneURL = cloneURL
	value.InstallationGrantCreatedAt = value.InstallationGrantCreatedAt.UTC()
	value.InstallationGrantUpdatedAt = value.InstallationGrantUpdatedAt.UTC()
	value.RepositoryGrantCreatedAt = value.RepositoryGrantCreatedAt.UTC()
	value.RepositoryGrantUpdatedAt = value.RepositoryGrantUpdatedAt.UTC()
	value.RepositoryAccessVerifiedAt = value.RepositoryAccessVerifiedAt.UTC()
	value.LinkRepositoryAccessVerifiedAt = value.LinkRepositoryAccessVerifiedAt.UTC()
	value.VerifiedAt = value.LinkRepositoryAccessVerifiedAt
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value, nil
}
