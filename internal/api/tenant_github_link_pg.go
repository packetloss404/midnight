// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/integrations/github"
)

// PostgresTenantGitHubSourceLinkCoordinator is the canonical product source
// authority. Live GitHub calls occur only after a local preflight and are
// followed by a fresh SERIALIZABLE authorization/grant transaction before a
// link is committed.
type PostgresTenantGitHubSourceLinkCoordinator struct {
	Pool        *pg.Pool
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain
	Links       *github.SourceLinkStore
	Verifier    GitHubSourceVerifier
	Now         func() time.Time
}

func NewPostgresTenantGitHubSourceLinkCoordinator(
	pool *pg.Pool,
	idempotencyStore *idempotency.PostgresStore,
	auditChain *audit.PostgresChain,
	links *github.SourceLinkStore,
	now func() time.Time,
) *PostgresTenantGitHubSourceLinkCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresTenantGitHubSourceLinkCoordinator{
		Pool:        pool,
		Idempotency: idempotencyStore,
		Audit:       auditChain,
		Links:       links,
		Now:         now,
	}
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) ListProjectRepositories(
	ctx context.Context,
	request TenantGitHubProjectRequest,
) ([]TenantGitHubRepositoryView, error) {
	if err := c.ready(false); err != nil {
		return nil, err
	}
	if err := validateTenantGitHubIdentity(request.TenantGitHubIdentityRequest); err != nil {
		return nil, err
	}
	var grants []github.RepositoryGrant
	var installationAccounts map[int64]string
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite}, func(tx *pg.Tx) error {
		_, authorization, err := authorizeTenantGitHubProjectInTx(
			ctx,
			tx,
			request.Principal,
			request.Session,
			request.ProjectID,
			c.now(),
			true,
		)
		if err != nil {
			return err
		}
		grants, err = c.Links.ListRepositoryGrantsInTx(ctx, tx, authorization.WorkspaceID)
		if err != nil {
			return err
		}
		installations, err := c.Links.ListInstallationGrantsInTx(ctx, tx, authorization.WorkspaceID)
		if err != nil {
			return err
		}
		installationAccounts = make(map[int64]string, len(installations))
		for _, installation := range installations {
			installationAccounts[installation.InstallationID] = installation.AccountLogin
		}
		return nil
	})
	if err != nil {
		return nil, classifyTenantGitHubCoordinatorError(err)
	}
	items := make([]TenantGitHubRepositoryView, 0, len(grants))
	for _, grant := range grants {
		items = append(items, TenantGitHubRepositoryView{
			InstallationID:  grant.InstallationID,
			RepositoryID:    grant.RepositoryID,
			Repository:      grant.Repository,
			AccountLogin:    installationAccounts[grant.InstallationID],
			Private:         grant.Private,
			DefaultBranch:   grant.DefaultBranch,
			ResourceVersion: grant.ResourceVersion,
		})
	}
	return items, nil
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) ListProjectRepositoryBranches(
	ctx context.Context,
	request TenantGitHubProjectRepositoryRequest,
) ([]github.Branch, error) {
	if err := c.ready(true); err != nil {
		return nil, err
	}
	if err := validateTenantGitHubIdentity(request.TenantGitHubIdentityRequest); err != nil ||
		strings.TrimSpace(request.ProjectID) == "" ||
		request.RepositoryID <= 0 {
		if err != nil {
			return nil, err
		}
		return nil, github.ErrLinkInvalid
	}
	grant, err := c.preflightProjectRepository(ctx, request)
	if err != nil {
		return nil, err
	}
	branches, err := c.Verifier.ListBranchesForRepository(
		ctx,
		grant.InstallationID,
		grant.RepositoryID,
	)
	if err != nil {
		return nil, err
	}
	live, err := c.Verifier.GetRepositoryForInstallation(
		ctx,
		grant.InstallationID,
		grant.RepositoryID,
	)
	if err != nil {
		return nil, err
	}
	err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		if err := github.LockInstallationInTx(
			ctx,
			tx,
			grant.InstallationID,
		); err != nil {
			return err
		}
		_, authorization, err := authorizeTenantGitHubProjectInTx(
			ctx,
			tx,
			request.Principal,
			request.Session,
			request.ProjectID,
			c.now(),
			true,
		)
		if err != nil {
			return err
		}
		current, err := c.Links.LockRepositoryGrantInTx(
			ctx,
			tx,
			authorization.WorkspaceID,
			grant.InstallationID,
			grant.RepositoryID,
		)
		if err != nil {
			return err
		}
		if current.ResourceVersion != grant.ResourceVersion ||
			!repositoryMatchesGrant(live, current) {
			return ErrTenantGitHubSourceConflict
		}
		return nil
	})
	if err != nil {
		return nil, classifyTenantGitHubCoordinatorError(err)
	}
	return branches, nil
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) GetServiceLink(
	ctx context.Context,
	request TenantGitHubServiceRequest,
) (TenantGitHubLinkView, error) {
	if err := c.ready(false); err != nil {
		return TenantGitHubLinkView{}, err
	}
	if err := validateTenantGitHubIdentity(request.TenantGitHubIdentityRequest); err != nil {
		return TenantGitHubLinkView{}, err
	}
	var link github.DurableServiceLink
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite}, func(tx *pg.Tx) error {
		_, authorization, err := authorizeTenantGitHubServiceInTx(
			ctx,
			tx,
			request.Principal,
			request.Session,
			string(request.ServiceID),
			c.now(),
			false,
		)
		if err != nil {
			return err
		}
		link, err = c.Links.GetServiceLinkInTx(
			ctx,
			tx,
			authorization.WorkspaceID,
			authorization.ProjectID,
			string(request.ServiceID),
		)
		return err
	})
	if err != nil {
		return TenantGitHubLinkView{}, classifyTenantGitHubCoordinatorError(err)
	}
	return tenantGitHubLinkView(link), nil
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) PutServiceLink(
	ctx context.Context,
	request TenantGitHubLinkMutationRequest,
) (TenantGitHubLinkMutationResult, error) {
	if err := c.ready(true); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if err := validateTenantGitHubPutRequest(request); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}

	grant, replay, err := c.preflightPut(ctx, request)
	if err != nil || replay != nil {
		if replay != nil {
			replay.Response = replay.Response.Clone()
			return *replay, nil
		}
		return TenantGitHubLinkMutationResult{}, err
	}
	live, err := c.Verifier.VerifyRepositoryBranch(
		ctx,
		request.InstallationID,
		request.RepositoryID,
		request.Branch,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if !repositoryMatchesGrant(live, grant) {
		return TenantGitHubLinkMutationResult{}, ErrTenantGitHubSourceConflict
	}

	var result TenantGitHubLinkMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var txErr error
			result, txErr = c.putServiceLinkInTx(ctx, tx, request, grant, live)
			return txErr
		})
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantGitHubLinkMutationResult{}, classifyTenantGitHubCoordinatorError(err)
		}
	}
	return TenantGitHubLinkMutationResult{}, ErrTenantGitHubSourceUnavailable
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) DeleteServiceLink(
	ctx context.Context,
	request TenantGitHubLinkDeleteRequest,
) (TenantGitHubLinkMutationResult, error) {
	if err := c.ready(false); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if err := validateTenantGitHubDeleteRequest(request); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	var result TenantGitHubLinkMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var txErr error
			result, txErr = c.deleteServiceLinkInTx(ctx, tx, request)
			return txErr
		})
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantGitHubLinkMutationResult{}, classifyTenantGitHubCoordinatorError(err)
		}
	}
	return TenantGitHubLinkMutationResult{}, ErrTenantGitHubSourceUnavailable
}

type tenantGitHubAuthorization struct {
	WorkspaceID   string
	ProjectID     string
	WorkspaceRole identity.WorkspaceRole
	ProjectRole   identity.ProjectRole
	Service       Service
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) preflightProjectRepository(
	ctx context.Context,
	request TenantGitHubProjectRepositoryRequest,
) (github.RepositoryGrant, error) {
	var grant github.RepositoryGrant
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite}, func(tx *pg.Tx) error {
		_, authorization, err := authorizeTenantGitHubProjectInTx(
			ctx,
			tx,
			request.Principal,
			request.Session,
			request.ProjectID,
			c.now(),
			true,
		)
		if err != nil {
			return err
		}
		grant, err = c.Links.GetRepositoryGrantByIDInTx(
			ctx,
			tx,
			authorization.WorkspaceID,
			request.RepositoryID,
		)
		return err
	})
	if err != nil {
		return github.RepositoryGrant{}, classifyTenantGitHubCoordinatorError(err)
	}
	return grant, nil
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) preflightPut(
	ctx context.Context,
	request TenantGitHubLinkMutationRequest,
) (github.RepositoryGrant, *TenantGitHubLinkMutationResult, error) {
	var grant github.RepositoryGrant
	var replay *TenantGitHubLinkMutationResult
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		if _, err := lockTenantGitHubInstallationsForService(
			ctx,
			tx,
			string(request.ServiceID),
			request.InstallationID,
		); err != nil {
			return err
		}
		mutationCtx, authorization, err := authorizeTenantGitHubServiceInTx(
			ctx,
			tx,
			request.Principal,
			request.Session,
			string(request.ServiceID),
			c.now(),
			true,
		)
		if err != nil {
			return err
		}
		grant, err = c.Links.LockRepositoryGrantInTx(
			mutationCtx,
			tx,
			authorization.WorkspaceID,
			request.InstallationID,
			request.RepositoryID,
		)
		if err != nil {
			return err
		}
		reservation, err := c.Idempotency.LoadInTx(
			mutationCtx,
			tx,
			request.Key,
			request.Fingerprint,
		)
		if errors.Is(err, idempotency.ErrReservationNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !reservation.IsTerminal() || reservation.Response == nil {
			return idempotency.ErrRequestInProgress
		}
		if err := validateGitHubReplayGrant(*reservation.Response, grant); err != nil {
			return err
		}
		replay = &TenantGitHubLinkMutationResult{
			Response: reservation.Response.Clone(),
			Replayed: true,
		}
		return nil
	})
	if err != nil {
		return github.RepositoryGrant{}, nil, classifyTenantGitHubCoordinatorError(err)
	}
	return grant, replay, nil
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) putServiceLinkInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantGitHubLinkMutationRequest,
	preflightGrant github.RepositoryGrant,
	live github.Repo,
) (TenantGitHubLinkMutationResult, error) {
	lockedInstallationIDs, err := lockTenantGitHubInstallationsForService(
		ctx,
		tx,
		string(request.ServiceID),
		request.InstallationID,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	mutationCtx, authorization, err := authorizeTenantGitHubServiceInTx(
		ctx,
		tx,
		request.Principal,
		request.Session,
		string(request.ServiceID),
		c.now(),
		true,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	grant, err := c.Links.LockRepositoryGrantInTx(
		mutationCtx,
		tx,
		authorization.WorkspaceID,
		request.InstallationID,
		request.RepositoryID,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if grant.ResourceVersion != preflightGrant.ResourceVersion ||
		!repositoryMatchesGrant(live, grant) {
		return TenantGitHubLinkMutationResult{}, ErrTenantGitHubSourceConflict
	}
	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantGitHubLinkMutationResult{}, errors.New("tenant github source replay has no stored response")
		}
		if err := validateGitHubReplayGrant(*reservation.Response, grant); err != nil {
			return TenantGitHubLinkMutationResult{}, err
		}
		return TenantGitHubLinkMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	now := c.now().Truncate(time.Microsecond)
	link, err := c.Links.PutServiceLinkUnderInstallationLocksInTx(
		mutationCtx,
		tx,
		github.DurableServiceLink{
			ServiceID:   string(request.ServiceID),
			ProjectID:   authorization.ProjectID,
			WorkspaceID: authorization.WorkspaceID,
			Link: github.ServiceLink{
				InstallationID: request.InstallationID,
				RepositoryID:   request.RepositoryID,
				Repo:           strings.ToLower(live.FullName),
				Branch:         request.Branch,
				CloneURL:       live.CloneURL,
			},
			AutoDeploy:     true,
			LinkedByUserID: request.Principal.UserID,
			VerifiedAt:     grant.VerifiedAt,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		lockedInstallationIDs,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	response, err := tenantGitHubLinkStoredResponse(http.StatusOK, tenantGitHubLinkView(link))
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if _, err := c.Audit.AppendInTx(mutationCtx, tx, tenantGitHubLinkAuditRow(
		request.RequestID,
		request.SourceIP,
		request.UserAgent,
		request.Principal,
		authorization,
		link,
		"service.github.link",
		now,
	)); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	return TenantGitHubLinkMutationResult{Response: response}, nil
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) deleteServiceLinkInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantGitHubLinkDeleteRequest,
) (TenantGitHubLinkMutationResult, error) {
	lockScope, lockScopeErr := c.prepareTenantGitHubDeleteLockScope(
		ctx,
		tx,
		request,
	)
	if lockScopeErr != nil && lockScope.context == nil {
		return TenantGitHubLinkMutationResult{}, lockScopeErr
	}
	if err := github.LockInstallationsInTx(
		ctx,
		tx,
		lockScope.installationIDs,
	); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	mutationCtx, authorization, err := authorizeTenantGitHubServiceInTx(
		ctx,
		tx,
		request.Principal,
		request.Session,
		string(request.ServiceID),
		c.now(),
		true,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if lockScopeErr != nil {
		return TenantGitHubLinkMutationResult{}, lockScopeErr
	}
	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantGitHubLinkMutationResult{}, errors.New("tenant github source replay has no stored response")
		}
		if err := revalidateGitHubDeleteReplayGrant(
			mutationCtx,
			tx,
			c.Links,
			authorization.WorkspaceID,
			*reservation.Response,
		); err != nil {
			return TenantGitHubLinkMutationResult{}, err
		}
		return TenantGitHubLinkMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}
	link, err := c.Links.DeleteServiceLinkUnderInstallationLocksInTx(
		mutationCtx,
		tx,
		authorization.WorkspaceID,
		authorization.ProjectID,
		string(request.ServiceID),
		lockScope.installationIDs,
	)
	if err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	response := idempotency.StoredResponse{
		StatusCode: http.StatusNoContent,
		Header: http.Header{
			"X-Midnight-Github-Installation-Id": []string{strconv.FormatInt(link.Link.InstallationID, 10)},
			"X-Midnight-Github-Repository-Id":   []string{strconv.FormatInt(link.Link.RepositoryID, 10)},
		},
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	now := c.now().Truncate(time.Microsecond)
	if _, err := c.Audit.AppendInTx(mutationCtx, tx, tenantGitHubLinkAuditRow(
		request.RequestID,
		request.SourceIP,
		request.UserAgent,
		request.Principal,
		authorization,
		link,
		"service.github.unlink",
		now,
	)); err != nil {
		return TenantGitHubLinkMutationResult{}, err
	}
	return TenantGitHubLinkMutationResult{Response: response}, nil
}

type tenantGitHubDeleteLockScope struct {
	context         context.Context
	installationIDs []int64
}

// prepareTenantGitHubDeleteLockScope discovers both the current link and an
// idempotent replay's historical installation without taking product row
// locks. The caller acquires the resulting installation set before final
// session, membership, project, and service reauthorization.
func (c *PostgresTenantGitHubSourceLinkCoordinator) prepareTenantGitHubDeleteLockScope(
	ctx context.Context,
	tx *pg.Tx,
	request TenantGitHubLinkDeleteRequest,
) (tenantGitHubDeleteLockScope, error) {
	var projectID, workspaceID string
	err := tx.QueryRow(ctx, `
		SELECT service.project_id, project.workspace_id
		  FROM services service
		  JOIN projects project ON project.id=service.project_id
		  JOIN workspaces workspace ON workspace.id=project.workspace_id
		 WHERE service.id=$1
		   AND service.deleted_at IS NULL
		   AND project.deleted_at IS NULL
		   AND workspace.deleted_at IS NULL`,
		strings.TrimSpace(string(request.ServiceID)),
	).Scan(&projectID, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tenantGitHubDeleteLockScope{}, identity.ErrAccessNotFound
	}
	if err != nil {
		return tenantGitHubDeleteLockScope{}, fmt.Errorf(
			"tenant github delete lock scope lineage: %w",
			err,
		)
	}
	scopeContext := idempotency.WithActor(
		ctx,
		idempotency.UserActor(request.Principal.UserID),
	)
	scopeContext = idempotency.WithTenant(
		scopeContext,
		projectScopedIdempotencyTenant(workspaceID, projectID),
	)
	installationIDs := make([]int64, 0, 2)
	var delayedErr error
	reservation, loadErr := c.Idempotency.LoadInTx(
		scopeContext,
		tx,
		request.Key,
		request.Fingerprint,
	)
	switch {
	case loadErr == nil && reservation.IsTerminal() && reservation.Response != nil:
		installationID, _, parseErr := tenantGitHubDeleteReplaySource(
			*reservation.Response,
		)
		if parseErr != nil {
			delayedErr = parseErr
		} else {
			installationIDs = append(installationIDs, installationID)
		}
	case errors.Is(loadErr, idempotency.ErrReservationNotFound):
	case loadErr != nil:
		delayedErr = loadErr
	}
	var currentInstallationID int64
	currentErr := tx.QueryRow(ctx, `
		SELECT installation_id
		  FROM service_github_links
		 WHERE service_id=$1`,
		strings.TrimSpace(string(request.ServiceID)),
	).Scan(&currentInstallationID)
	if currentErr == nil {
		installationIDs = append(installationIDs, currentInstallationID)
	} else if !errors.Is(currentErr, pgx.ErrNoRows) {
		return tenantGitHubDeleteLockScope{}, fmt.Errorf(
			"tenant github delete current installation discovery: %w",
			currentErr,
		)
	}
	return tenantGitHubDeleteLockScope{
		context:         scopeContext,
		installationIDs: installationIDs,
	}, delayedErr
}

func authorizeTenantGitHubProjectInTx(
	ctx context.Context,
	tx *pg.Tx,
	principal identity.Principal,
	session identity.Session,
	projectID string,
	now time.Time,
	mutation bool,
) (context.Context, tenantGitHubAuthorization, error) {
	projectID = strings.TrimSpace(projectID)
	workspaceID, err := resolveTenantServiceProjectWorkspace(ctx, tx, projectID)
	if err != nil {
		return ctx, tenantGitHubAuthorization{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
		return ctx, tenantGitHubAuthorization{}, fmt.Errorf("tenant github workspace membership lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
		return ctx, tenantGitHubAuthorization{}, fmt.Errorf("tenant github project membership lock: %w", err)
	}
	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(principal.UserID))
	mutationCtx = idempotency.WithTenant(mutationCtx, projectScopedIdempotencyTenant(workspaceID, projectID))
	if err := revalidateTenantMutationSessionTuple(mutationCtx, tx, principal, session, now); err != nil {
		return mutationCtx, tenantGitHubAuthorization{}, err
	}
	canonicalWorkspaceID, workspaceRole, projectRole, err := lockTenantServiceProjectAccess(
		mutationCtx,
		tx,
		projectID,
		workspaceID,
		principal.UserID,
	)
	if err != nil {
		return mutationCtx, tenantGitHubAuthorization{}, err
	}
	if canonicalWorkspaceID != workspaceID {
		return mutationCtx, tenantGitHubAuthorization{}, identity.ErrAccessNotFound
	}
	capabilities := []identity.Capability{identity.CapabilityProjectRead}
	if mutation {
		capabilities = []identity.Capability{
			identity.CapabilityServiceWrite,
			identity.CapabilityDeploymentTriggerGit,
		}
	}
	if err := requireTenantGitHubCapabilities(workspaceRole, projectRole, capabilities...); err != nil {
		return mutationCtx, tenantGitHubAuthorization{}, err
	}
	return mutationCtx, tenantGitHubAuthorization{
		WorkspaceID:   workspaceID,
		ProjectID:     projectID,
		WorkspaceRole: workspaceRole,
		ProjectRole:   projectRole,
	}, nil
}

func authorizeTenantGitHubServiceInTx(
	ctx context.Context,
	tx *pg.Tx,
	principal identity.Principal,
	session identity.Session,
	serviceID string,
	now time.Time,
	mutation bool,
) (context.Context, tenantGitHubAuthorization, error) {
	var projectID, workspaceID string
	err := tx.QueryRow(ctx, `
		SELECT service.project_id, project.workspace_id
		  FROM services service
		  JOIN projects project ON project.id=service.project_id
		  JOIN workspaces workspace ON workspace.id=project.workspace_id
		 WHERE service.id=$1
		   AND service.deleted_at IS NULL
		   AND project.deleted_at IS NULL
		   AND workspace.deleted_at IS NULL`,
		strings.TrimSpace(serviceID),
	).Scan(&projectID, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, tenantGitHubAuthorization{}, identity.ErrAccessNotFound
	}
	if err != nil {
		return ctx, tenantGitHubAuthorization{}, fmt.Errorf("tenant github service lineage resolve: %w", err)
	}
	mutationCtx, authorization, err := authorizeTenantGitHubProjectInTx(
		ctx,
		tx,
		principal,
		session,
		projectID,
		now,
		mutation,
	)
	if err != nil {
		return mutationCtx, tenantGitHubAuthorization{}, err
	}
	var service Service
	err = tx.QueryRow(mutationCtx, `
		SELECT id,project_id,slug,display_name,created_at,updated_at
		  FROM services
		 WHERE id=$1
		   AND project_id=$2
		   AND deleted_at IS NULL
		 FOR UPDATE`,
		strings.TrimSpace(serviceID),
		projectID,
	).Scan(
		&service.ID,
		&service.ProjectID,
		&service.Slug,
		&service.DisplayName,
		&service.CreatedAt,
		&service.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return mutationCtx, tenantGitHubAuthorization{}, identity.ErrAccessNotFound
	}
	if err != nil {
		return mutationCtx, tenantGitHubAuthorization{}, fmt.Errorf("tenant github service lock: %w", err)
	}
	if !mutation {
		if err := requireTenantGitHubCapabilities(
			authorization.WorkspaceRole,
			authorization.ProjectRole,
			identity.CapabilityServiceRead,
		); err != nil {
			return mutationCtx, tenantGitHubAuthorization{}, err
		}
	}
	authorization.Service = service
	return mutationCtx, authorization, nil
}

func requireTenantGitHubCapabilities(
	workspaceRole identity.WorkspaceRole,
	projectRole identity.ProjectRole,
	capabilities ...identity.Capability,
) error {
	for _, capability := range capabilities {
		if !identity.EvaluateAccess(identity.AccessRequest{
			WorkspaceRole: workspaceRole,
			ProjectRole:   projectRole,
			Capability:    capability,
		}).Allowed {
			return identity.ErrAuthorizationDenied
		}
	}
	return nil
}

func lockTenantGitHubInstallationsForService(
	ctx context.Context,
	tx *pg.Tx,
	serviceID string,
	requestedInstallationID int64,
) ([]int64, error) {
	ids := make([]int64, 0, 2)
	if requestedInstallationID > 0 {
		ids = append(ids, requestedInstallationID)
	}
	var existing int64
	err := tx.QueryRow(ctx, `
		SELECT installation_id
		  FROM service_github_links
		 WHERE service_id=$1`,
		serviceID,
	).Scan(&existing)
	if err == nil && existing != requestedInstallationID {
		ids = append(ids, existing)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tenant github existing source lock scope: %w", err)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for index, installationID := range ids {
		if index == 0 || installationID != ids[index-1] {
			unique = append(unique, installationID)
		}
	}
	if err := github.LockInstallationsInTx(ctx, tx, unique); err != nil {
		return nil, fmt.Errorf("tenant github installation lock: %w", err)
	}
	return unique, nil
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) ready(requireVerifier bool) error {
	if c == nil || c.Pool == nil || c.Idempotency == nil || c.Audit == nil || c.Links == nil {
		return ErrTenantGitHubSourceUnavailable
	}
	if requireVerifier && c.Verifier == nil {
		return ErrTenantGitHubSourceUnconfigured
	}
	return nil
}

func (c *PostgresTenantGitHubSourceLinkCoordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func validateTenantGitHubIdentity(request TenantGitHubIdentityRequest) error {
	if strings.TrimSpace(request.Principal.UserID) == "" ||
		strings.TrimSpace(request.Session.ID) == "" ||
		request.Session.UserID != request.Principal.UserID ||
		request.Session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	return nil
}

func validateTenantGitHubPutRequest(request TenantGitHubLinkMutationRequest) error {
	if err := validateTenantGitHubIdentity(request.TenantGitHubIdentityRequest); err != nil {
		return err
	}
	if strings.TrimSpace(string(request.ServiceID)) == "" ||
		request.InstallationID <= 0 ||
		request.RepositoryID <= 0 ||
		strings.TrimSpace(request.Branch) == "" {
		return github.ErrLinkInvalid
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	if request.Fingerprint.Method != http.MethodPut ||
		request.Fingerprint.Path != tenantGitHubLinkIdempotencyPath(request.ServiceID) {
		return idempotency.ErrInvalidFingerprint
	}
	return nil
}

func validateTenantGitHubDeleteRequest(request TenantGitHubLinkDeleteRequest) error {
	if err := validateTenantGitHubIdentity(request.TenantGitHubIdentityRequest); err != nil {
		return err
	}
	if strings.TrimSpace(string(request.ServiceID)) == "" {
		return github.ErrLinkInvalid
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	if request.Fingerprint.Method != http.MethodDelete ||
		request.Fingerprint.Path != tenantGitHubLinkIdempotencyPath(request.ServiceID) {
		return idempotency.ErrInvalidFingerprint
	}
	return nil
}

func repositoryMatchesGrant(live github.Repo, grant github.RepositoryGrant) bool {
	return live.ID == grant.RepositoryID &&
		strings.EqualFold(strings.TrimSpace(live.FullName), grant.Repository) &&
		strings.TrimSpace(live.CloneURL) == grant.CloneURL &&
		strings.TrimSpace(live.Default) == grant.DefaultBranch &&
		live.Private == grant.Private
}

func tenantGitHubLinkView(link github.DurableServiceLink) TenantGitHubLinkView {
	return TenantGitHubLinkView{
		InstallationID:             link.Link.InstallationID,
		RepositoryID:               link.Link.RepositoryID,
		Repository:                 link.Link.Repo,
		Branch:                     link.Link.Branch,
		BindingState:               "bound",
		RepositoryAccessVerifiedAt: link.VerifiedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:                  link.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func tenantGitHubLinkStoredResponse(
	status int,
	view TenantGitHubLinkView,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	body = append(body, '\n')
	return idempotency.StoredResponse{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}, nil
}

func validateGitHubReplayGrant(
	response idempotency.StoredResponse,
	grant github.RepositoryGrant,
) error {
	var view TenantGitHubLinkView
	if err := json.Unmarshal(response.Body, &view); err != nil {
		return fmt.Errorf("tenant github replay response decode: %w", err)
	}
	if view.InstallationID != grant.InstallationID ||
		view.RepositoryID != grant.RepositoryID ||
		!strings.EqualFold(view.Repository, grant.Repository) {
		return ErrTenantGitHubSourceConflict
	}
	return nil
}

func revalidateGitHubDeleteReplayGrant(
	ctx context.Context,
	tx *pg.Tx,
	store *github.SourceLinkStore,
	workspaceID string,
	response idempotency.StoredResponse,
) error {
	installationID, repositoryID, err := tenantGitHubDeleteReplaySource(response)
	if err != nil {
		return err
	}
	_, err = store.LockRepositoryGrantInTx(ctx, tx, workspaceID, installationID, repositoryID)
	return err
}

func tenantGitHubDeleteReplaySource(
	response idempotency.StoredResponse,
) (int64, int64, error) {
	installationID, err := strconv.ParseInt(
		response.Header.Get("X-Midnight-Github-Installation-Id"),
		10,
		64,
	)
	if err != nil || installationID <= 0 {
		return 0, 0, errors.New("tenant github unlink replay is missing installation identity")
	}
	repositoryID, err := strconv.ParseInt(
		response.Header.Get("X-Midnight-Github-Repository-Id"),
		10,
		64,
	)
	if err != nil || repositoryID <= 0 {
		return 0, 0, errors.New("tenant github unlink replay is missing repository identity")
	}
	return installationID, repositoryID, nil
}

func tenantGitHubLinkAuditRow(
	requestID string,
	sourceIP string,
	userAgent string,
	principal identity.Principal,
	authorization tenantGitHubAuthorization,
	link github.DurableServiceLink,
	action string,
	now time.Time,
) audit.Row {
	return audit.Row{
		RequestID:      requestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        principal.UserID,
		ActorType:      "user",
		AuthMethod:     principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(sourceIP),
		UserAgent:      userAgent,
		WorkspaceID:    authorization.WorkspaceID,
		ProjectID:      authorization.ProjectID,
		ResourceKind:   "service",
		ResourceID:     link.ServiceID,
		Action:         action,
		PolicyDecision: "allowed",
		After: map[string]any{
			"installation_id":  link.Link.InstallationID,
			"repository_id":    link.Link.RepositoryID,
			"repository":       link.Link.Repo,
			"branch":           link.Link.Branch,
			"auto_deploy":      link.AutoDeploy,
			"resource_version": link.ResourceVersion,
		},
		Attrs: map[string]any{
			"capabilities": []string{
				string(identity.CapabilityServiceWrite),
				string(identity.CapabilityDeploymentTriggerGit),
			},
			"workspace_role": string(authorization.WorkspaceRole),
			"project_role":   string(authorization.ProjectRole),
			"session_id":     principal.SessionID,
		},
	}
}

func classifyTenantGitHubCoordinatorError(err error) error {
	if err == nil ||
		errors.Is(err, identity.ErrSessionInvalid) ||
		errors.Is(err, identity.ErrAccessNotFound) ||
		errors.Is(err, identity.ErrAuthorizationDenied) ||
		errors.Is(err, idempotency.ErrInvalidKey) ||
		errors.Is(err, idempotency.ErrInvalidFingerprint) ||
		errors.Is(err, idempotency.ErrKeyReused) ||
		errors.Is(err, idempotency.ErrKeyReusedDifferentBody) ||
		errors.Is(err, idempotency.ErrRequestInProgress) ||
		errors.Is(err, idempotency.ErrReservationNotFound) ||
		errors.Is(err, github.ErrSourceLinkNotFound) ||
		errors.Is(err, github.ErrInstallationGrantNotFound) ||
		errors.Is(err, github.ErrRepositoryGrantNotFound) ||
		errors.Is(err, github.ErrSourceLinkConflict) ||
		errors.Is(err, github.ErrLinkInvalid) ||
		errors.Is(err, ErrTenantGitHubSourceConflict) ||
		errors.Is(err, ErrTenantGitHubSourceUnconfigured) ||
		errors.Is(err, github.ErrAppHTTP) ||
		errors.Is(err, context.Canceled) {
		return err
	}
	if tenantDeployDependencyError(err) {
		return errors.Join(ErrTenantGitHubSourceUnavailable, err)
	}
	return err
}
