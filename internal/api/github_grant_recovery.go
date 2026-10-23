// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/integrations/github"
)

const githubGrantRecoveryBodyLimit int64 = 64 << 10

var ErrGitHubGrantWorkspaceNotFound = errors.New("github recovery workspace not found")

type GitHubGrantRecoveryVerifier interface {
	GetInstallation(context.Context, int64) (github.Installation, error)
	GetRepositoryForInstallation(context.Context, int64, int64) (github.Repo, error)
}

type GitHubGrantRecoveryRequest struct {
	WorkspaceID    string
	InstallationID int64
	RepositoryIDs  []int64
	Key            idempotency.Key
	Fingerprint    idempotency.RequestFingerprint
	RequestID      string
	SourceIP       string
	UserAgent      string
}

type GitHubGrantRecoveryResult struct {
	Response idempotency.StoredResponse
	Replayed bool
}

type GitHubGrantRecoveryView struct {
	WorkspaceID      string                       `json:"workspace_id"`
	Installation     github.Installation          `json:"installation"`
	Repositories     []TenantGitHubRepositoryView `json:"repositories"`
	UnlinkedServices []string                     `json:"unlinked_service_ids,omitempty"`
}

type GitHubGrantRecoveryCoordinator interface {
	Get(context.Context, string, int64) (GitHubGrantRecoveryView, error)
	Put(context.Context, GitHubGrantRecoveryRequest) (GitHubGrantRecoveryResult, error)
	Delete(context.Context, GitHubGrantRecoveryRequest) (GitHubGrantRecoveryResult, error)
}

type PostgresGitHubGrantRecoveryCoordinator struct {
	Pool        *pg.Pool
	Links       *github.SourceLinkStore
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain
	Verifier    GitHubGrantRecoveryVerifier
	Now         func() time.Time
}

func NewPostgresGitHubGrantRecoveryCoordinator(
	pool *pg.Pool,
	links *github.SourceLinkStore,
	idempotencyStore *idempotency.PostgresStore,
	auditChain *audit.PostgresChain,
	now func() time.Time,
) *PostgresGitHubGrantRecoveryCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresGitHubGrantRecoveryCoordinator{
		Pool:        pool,
		Links:       links,
		Idempotency: idempotencyStore,
		Audit:       auditChain,
		Now:         now,
	}
}

func (c *PostgresGitHubGrantRecoveryCoordinator) Get(
	ctx context.Context,
	workspaceID string,
	installationID int64,
) (GitHubGrantRecoveryView, error) {
	if err := c.ready(false); err != nil {
		return GitHubGrantRecoveryView{}, err
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" || installationID <= 0 {
		return GitHubGrantRecoveryView{}, github.ErrLinkInvalid
	}
	installation, err := c.Links.GetInstallationGrant(ctx, workspaceID, installationID)
	if err != nil {
		return GitHubGrantRecoveryView{}, err
	}
	repositories, err := c.Links.ListRepositoryGrants(ctx, workspaceID)
	if err != nil {
		return GitHubGrantRecoveryView{}, err
	}
	return githubGrantRecoveryView(installation, repositories, nil), nil
}

func (c *PostgresGitHubGrantRecoveryCoordinator) Put(
	ctx context.Context,
	request GitHubGrantRecoveryRequest,
) (GitHubGrantRecoveryResult, error) {
	if err := c.ready(true); err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	if err := validateGitHubGrantRecoveryRequest(request, true); err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	if replay, found, err := c.loadReplay(ctx, request); err != nil || found {
		return replay, err
	}
	installation, err := c.Verifier.GetInstallation(ctx, request.InstallationID)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	repositories := make([]github.Repo, 0, len(request.RepositoryIDs))
	for _, repositoryID := range request.RepositoryIDs {
		repository, err := c.Verifier.GetRepositoryForInstallation(
			ctx,
			request.InstallationID,
			repositoryID,
		)
		if err != nil {
			return GitHubGrantRecoveryResult{}, err
		}
		repositories = append(repositories, repository)
	}

	var result GitHubGrantRecoveryResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var txErr error
			result, txErr = c.putInTx(ctx, tx, request, installation, repositories)
			return txErr
		})
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return GitHubGrantRecoveryResult{}, classifyTenantGitHubCoordinatorError(err)
		}
	}
	return GitHubGrantRecoveryResult{}, ErrTenantGitHubSourceUnavailable
}

func (c *PostgresGitHubGrantRecoveryCoordinator) Delete(
	ctx context.Context,
	request GitHubGrantRecoveryRequest,
) (GitHubGrantRecoveryResult, error) {
	if err := c.ready(false); err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	if err := validateGitHubGrantRecoveryRequest(request, false); err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	var result GitHubGrantRecoveryResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var txErr error
			result, txErr = c.deleteInTx(ctx, tx, request)
			return txErr
		})
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return GitHubGrantRecoveryResult{}, classifyTenantGitHubCoordinatorError(err)
		}
	}
	return GitHubGrantRecoveryResult{}, ErrTenantGitHubSourceUnavailable
}

func (c *PostgresGitHubGrantRecoveryCoordinator) putInTx(
	ctx context.Context,
	tx *pg.Tx,
	request GitHubGrantRecoveryRequest,
	liveInstallation github.Installation,
	liveRepositories []github.Repo,
) (GitHubGrantRecoveryResult, error) {
	mutationCtx, err := lockGitHubGrantRecoveryWorkspace(ctx, tx, request.WorkspaceID)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return GitHubGrantRecoveryResult{}, errors.New("github grant recovery replay has no stored response")
		}
		return GitHubGrantRecoveryResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}
	now := c.now()
	installation, err := c.Links.PutInstallationGrantInTx(ctx, tx, github.InstallationGrant{
		WorkspaceID:    request.WorkspaceID,
		InstallationID: request.InstallationID,
		AccountLogin:   liveInstallation.Account,
		TargetType:     liveInstallation.TargetType,
		GrantedByActor: string(idempotency.ActorRecoveryOperator),
	})
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	current, err := c.Links.ListRepositoryGrantsInTx(ctx, tx, request.WorkspaceID)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	target := make(map[int64]github.Repo, len(liveRepositories))
	for _, repository := range liveRepositories {
		target[repository.ID] = repository
	}
	var unlinked []string
	for _, repository := range current {
		if repository.InstallationID != request.InstallationID {
			continue
		}
		if _, keep := target[repository.RepositoryID]; keep {
			continue
		}
		serviceIDs, err := c.Links.RevokeRepositoryGrantInTx(
			ctx,
			tx,
			request.WorkspaceID,
			request.InstallationID,
			repository.RepositoryID,
		)
		if err != nil {
			return GitHubGrantRecoveryResult{}, err
		}
		unlinked = append(unlinked, serviceIDs...)
	}
	grants := make([]github.RepositoryGrant, 0, len(liveRepositories))
	for _, repository := range liveRepositories {
		grant, err := c.Links.PutRepositoryGrantInTx(ctx, tx, github.RepositoryGrant{
			WorkspaceID:    request.WorkspaceID,
			InstallationID: request.InstallationID,
			RepositoryID:   repository.ID,
			Repository:     repository.FullName,
			CloneURL:       repository.CloneURL,
			DefaultBranch:  repository.Default,
			Private:        repository.Private,
			VerifiedAt:     now,
			GrantedByActor: string(idempotency.ActorRecoveryOperator),
		})
		if err != nil {
			return GitHubGrantRecoveryResult{}, err
		}
		grants = append(grants, grant)
	}
	sort.Strings(unlinked)
	view := githubGrantRecoveryView(installation, grants, unlinked)
	response, err := githubGrantRecoveryStoredResponse(view)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	if _, err := c.Audit.AppendInTx(mutationCtx, tx, githubGrantRecoveryAuditRow(
		request,
		view,
		"github.installation.workspace_grant.replace",
		now,
	)); err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	return GitHubGrantRecoveryResult{Response: response}, nil
}

func (c *PostgresGitHubGrantRecoveryCoordinator) deleteInTx(
	ctx context.Context,
	tx *pg.Tx,
	request GitHubGrantRecoveryRequest,
) (GitHubGrantRecoveryResult, error) {
	mutationCtx, err := lockGitHubGrantRecoveryWorkspace(ctx, tx, request.WorkspaceID)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return GitHubGrantRecoveryResult{}, errors.New("github grant recovery replay has no stored response")
		}
		return GitHubGrantRecoveryResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}
	installation, err := c.Links.LockInstallationGrantInTx(
		ctx,
		tx,
		request.WorkspaceID,
		request.InstallationID,
	)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	serviceIDs, err := c.Links.RevokeInstallationGrantInTx(
		ctx,
		tx,
		request.WorkspaceID,
		request.InstallationID,
	)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	view := githubGrantRecoveryView(installation, nil, serviceIDs)
	response, err := githubGrantRecoveryStoredResponse(view)
	if err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	now := c.now()
	if _, err := c.Audit.AppendInTx(mutationCtx, tx, githubGrantRecoveryAuditRow(
		request,
		view,
		"github.installation.workspace_grant.revoke",
		now,
	)); err != nil {
		return GitHubGrantRecoveryResult{}, err
	}
	return GitHubGrantRecoveryResult{Response: response}, nil
}

func (c *PostgresGitHubGrantRecoveryCoordinator) loadReplay(
	ctx context.Context,
	request GitHubGrantRecoveryRequest,
) (GitHubGrantRecoveryResult, bool, error) {
	var result GitHubGrantRecoveryResult
	found := false
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		mutationCtx, err := lockGitHubGrantRecoveryWorkspace(ctx, tx, request.WorkspaceID)
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
		result = GitHubGrantRecoveryResult{Response: reservation.Response.Clone(), Replayed: true}
		found = true
		return nil
	})
	return result, found, classifyTenantGitHubCoordinatorError(err)
}

func lockGitHubGrantRecoveryWorkspace(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
) (context.Context, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return ctx, github.ErrLinkInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "github-workspace-grants:"+workspaceID); err != nil {
		return ctx, err
	}
	var canonical string
	err := tx.QueryRow(ctx, `
		SELECT id
		  FROM workspaces
		 WHERE id=$1
		   AND deleted_at IS NULL
		 FOR UPDATE`,
		workspaceID,
	).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, ErrGitHubGrantWorkspaceNotFound
	}
	if err != nil {
		return ctx, err
	}
	mutationCtx := idempotency.WithActor(ctx, idempotency.ActorRecoveryOperator)
	mutationCtx = idempotency.WithTenant(mutationCtx, idempotency.Tenant(workspaceID))
	return mutationCtx, nil
}

func (c *PostgresGitHubGrantRecoveryCoordinator) ready(requireVerifier bool) error {
	if c == nil || c.Pool == nil || c.Links == nil || c.Idempotency == nil || c.Audit == nil {
		return ErrTenantGitHubSourceUnavailable
	}
	if requireVerifier && c.Verifier == nil {
		return ErrTenantGitHubSourceUnconfigured
	}
	return nil
}

func (c *PostgresGitHubGrantRecoveryCoordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func validateGitHubGrantRecoveryRequest(request GitHubGrantRecoveryRequest, requireRepositories bool) error {
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	if request.WorkspaceID == "" || request.InstallationID <= 0 {
		return github.ErrLinkInvalid
	}
	if requireRepositories && (len(request.RepositoryIDs) == 0 || len(request.RepositoryIDs) > 500) {
		return github.ErrLinkInvalid
	}
	seen := make(map[int64]struct{}, len(request.RepositoryIDs))
	for _, repositoryID := range request.RepositoryIDs {
		if repositoryID <= 0 {
			return github.ErrLinkInvalid
		}
		if _, duplicate := seen[repositoryID]; duplicate {
			return github.ErrLinkInvalid
		}
		seen[repositoryID] = struct{}{}
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	expectedMethod := http.MethodDelete
	if requireRepositories {
		expectedMethod = http.MethodPut
	}
	if request.Fingerprint.Method != expectedMethod ||
		request.Fingerprint.Path != githubGrantRecoveryIdempotencyPath(request.InstallationID) {
		return idempotency.ErrInvalidFingerprint
	}
	return nil
}

func githubGrantRecoveryView(
	installation github.InstallationGrant,
	repositories []github.RepositoryGrant,
	unlinked []string,
) GitHubGrantRecoveryView {
	items := make([]TenantGitHubRepositoryView, 0, len(repositories))
	for _, repository := range repositories {
		if repository.InstallationID != installation.InstallationID {
			continue
		}
		items = append(items, TenantGitHubRepositoryView{
			InstallationID:  repository.InstallationID,
			RepositoryID:    repository.RepositoryID,
			Repository:      repository.Repository,
			AccountLogin:    installation.AccountLogin,
			Private:         repository.Private,
			DefaultBranch:   repository.DefaultBranch,
			ResourceVersion: repository.ResourceVersion,
		})
	}
	return GitHubGrantRecoveryView{
		WorkspaceID: installation.WorkspaceID,
		Installation: github.Installation{
			ID:         installation.InstallationID,
			Account:    installation.AccountLogin,
			TargetType: installation.TargetType,
		},
		Repositories:     items,
		UnlinkedServices: append([]string(nil), unlinked...),
	}
}

func githubGrantRecoveryStoredResponse(view GitHubGrantRecoveryView) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	return idempotency.StoredResponse{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       append(body, '\n'),
	}, nil
}

func githubGrantRecoveryAuditRow(
	request GitHubGrantRecoveryRequest,
	view GitHubGrantRecoveryView,
	action string,
	now time.Time,
) audit.Row {
	repositoryIDs := make([]int64, 0, len(view.Repositories))
	for _, repository := range view.Repositories {
		repositoryIDs = append(repositoryIDs, repository.RepositoryID)
	}
	return audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        string(idempotency.ActorRecoveryOperator),
		ActorType:      "system",
		AuthMethod:     "operator-bearer",
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    request.WorkspaceID,
		ResourceKind:   "github_installation_grant",
		ResourceID:     strconv.FormatInt(request.InstallationID, 10),
		Action:         action,
		PolicyDecision: "allowed",
		After: map[string]any{
			"installation_id":      request.InstallationID,
			"repository_ids":       repositoryIDs,
			"unlinked_service_ids": view.UnlinkedServices,
		},
	}
}

type githubGrantRecoveryBody struct {
	WorkspaceID   string  `json:"workspace_id"`
	RepositoryIDs []int64 `json:"repository_ids,omitempty"`
}

func githubGrantRecoveryHandler(coordinator GitHubGrantRecoveryCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "GitHub grant recovery is unavailable", nil)
			return
		}
		installationID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("installation_id")), 10, 64)
		if err != nil || installationID <= 0 {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "installation id must be a positive integer", nil)
			return
		}
		if r.Method == http.MethodGet {
			workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
			view, err := coordinator.Get(r.Context(), workspaceID, installationID)
			if githubGrantRecoveryError(w, r, err) {
				return
			}
			WriteJSON(w, http.StatusOK, view)
			return
		}
		if r.Method != http.MethodPut && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "GET, PUT, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		key, ok := tenantGitHubIdempotencyKey(w, r)
		if !ok {
			return
		}
		body, ok := readGitHubGrantRecoveryBody(w, r)
		if !ok {
			return
		}
		var decoded githubGrantRecoveryBody
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid GitHub grant recovery request", nil)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "request must contain exactly one JSON document", nil)
			return
		}
		if r.Method == http.MethodDelete && len(decoded.RepositoryIDs) != 0 {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "repository_ids are not accepted when revoking an installation grant", nil)
			return
		}
		request := GitHubGrantRecoveryRequest{
			WorkspaceID:    strings.TrimSpace(decoded.WorkspaceID),
			InstallationID: installationID,
			RepositoryIDs:  append([]int64(nil), decoded.RepositoryIDs...),
			Key:            key,
			Fingerprint: idempotency.RequestFingerprint{
				Method:     r.Method,
				Path:       githubGrantRecoveryIdempotencyPath(installationID),
				BodySHA256: idempotency.BodyHashSHA256(body),
			},
			RequestID: middleware.FromContext(r.Context()),
			SourceIP:  r.RemoteAddr,
			UserAgent: r.UserAgent(),
		}
		var result GitHubGrantRecoveryResult
		if r.Method == http.MethodPut {
			result, err = coordinator.Put(r.Context(), request)
		} else {
			result, err = coordinator.Delete(r.Context(), request)
		}
		if githubGrantRecoveryError(w, r, err) {
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

func readGitHubGrantRecoveryBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, githubGrantRecoveryBodyLimit)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "GitHub grant recovery request is too large", nil)
			return nil, false
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub grant recovery request body is required", nil)
		return nil, false
	}
	return body, true
}

func githubGrantRecoveryError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, github.ErrInstallationGrantNotFound),
		errors.Is(err, github.ErrRepositoryGrantNotFound),
		errors.Is(err, ErrGitHubGrantWorkspaceNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "GitHub workspace grant was not found", nil)
	case errors.Is(err, github.ErrSourceLinkConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "GitHub installation is already assigned to another workspace", nil)
	case errors.Is(err, github.ErrLinkInvalid),
		errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid GitHub workspace grant request", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with a different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with a different request", nil)
	case errors.Is(err, ErrTenantGitHubSourceUnconfigured):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "GitHub App integration is not configured", nil)
	case errors.Is(err, github.ErrAppHTTP):
		var appErr *github.AppHTTPError
		if errors.As(err, &appErr) && appErr.StatusCode == http.StatusNotFound {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "GitHub installation or repository was not found", nil)
		} else {
			writeRequestError(w, r, http.StatusBadGateway, failurecodes.InternalError, "GitHub grant verification failed", nil)
		}
	case errors.Is(err, ErrTenantGitHubSourceUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "GitHub grant recovery is temporarily unavailable", nil)
	default:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "GitHub grant recovery failed", nil)
	}
	return true
}

func githubGrantRecoveryIdempotencyPath(installationID int64) string {
	return v1alphaPrefix + "/github/installations/" + strconv.FormatInt(installationID, 10) + "/workspace-grant"
}
