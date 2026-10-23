// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/integrations/github"
)

const (
	githubSetupCallbackPath = v1alphaPrefix + "/github/setup/callback"
	githubSetupBodyLimit    = 1024
)

var (
	ErrGitHubSetupUnavailable = errors.New("github setup is unavailable")
	ErrGitHubSetupForbidden   = errors.New("github setup is forbidden")
	ErrGitHubSetupInvalid     = errors.New("github setup request is invalid")
	ErrGitHubSetupConsumed    = errors.New("github setup state was already consumed")
)

type GitHubSetupVerifier interface {
	ExchangeUserCode(context.Context, string) (github.Secret, error)
	UserCanAccessInstallation(context.Context, github.Secret, int64) error
	GetInstallation(context.Context, int64) (github.Installation, error)
	ListReposForInstallation(context.Context, int64) ([]github.Repo, error)
}

type GitHubSetupInitiateRequest struct {
	WorkspaceID string
	Principal   identity.Principal
	Session     identity.Session
	RequestID   string
	SourceIP    string
	UserAgent   string
}

type GitHubSetupInitiateResult struct {
	InstallURL string    `json:"install_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type GitHubSetupCallbackRequest struct {
	State          string
	OAuthCode      string
	InstallationID int64
	RequestID      string
	SourceIP       string
	UserAgent      string
}

type GitHubSetupCallbackResult struct {
	WorkspaceID        string              `json:"workspace_id"`
	Installation       github.Installation `json:"installation"`
	RepositoryCount    int                 `json:"repository_count"`
	UnlinkedServiceIDs []string            `json:"unlinked_service_ids,omitempty"`
}

type GitHubSetupCoordinator interface {
	Initiate(context.Context, GitHubSetupInitiateRequest) (GitHubSetupInitiateResult, error)
	Complete(context.Context, GitHubSetupCallbackRequest) (GitHubSetupCallbackResult, error)
}

type PostgresGitHubSetupCoordinator struct {
	Pool     *pg.Pool
	Links    *github.SourceLinkStore
	Audit    *audit.PostgresChain
	Verifier GitHubSetupVerifier
	Codec    *github.SetupStateCodec
	Config   github.AppConfig
	Now      func() time.Time
}

func NewPostgresGitHubSetupCoordinator(
	pool *pg.Pool,
	links *github.SourceLinkStore,
	auditChain *audit.PostgresChain,
	codec *github.SetupStateCodec,
	now func() time.Time,
) *PostgresGitHubSetupCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresGitHubSetupCoordinator{
		Pool: pool, Links: links, Audit: auditChain, Codec: codec, Now: now,
	}
}

func (c *PostgresGitHubSetupCoordinator) Initiate(
	ctx context.Context,
	request GitHubSetupInitiateRequest,
) (GitHubSetupInitiateResult, error) {
	if err := c.ready(); err != nil {
		return GitHubSetupInitiateResult{}, err
	}
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	if request.WorkspaceID == "" || request.Principal.UserID == "" || request.Session.ID == "" ||
		request.Principal.UserID != request.Session.UserID ||
		request.Principal.SessionID != request.Session.ID {
		return GitHubSetupInitiateResult{}, ErrGitHubSetupInvalid
	}
	stateToken, state, err := c.Codec.Issue(request.WorkspaceID, request.Principal.UserID, request.Session.ID)
	if err != nil {
		return GitHubSetupInitiateResult{}, err
	}
	installURL, err := c.Config.InstallationURL(stateToken)
	if err != nil {
		return GitHubSetupInitiateResult{}, ErrGitHubSetupUnavailable
	}
	now := c.now()
	err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		if err := requireActiveGitHubSetupAdminInTx(ctx, tx, state, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO github_app_setup_states (
				nonce_sha256,workspace_id,user_id,session_id,issued_at,expires_at,created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			state.NonceHash(), state.WorkspaceID, state.UserID, state.SessionID,
			state.IssuedTime(), state.ExpiresTime(), now,
		); err != nil {
			return fmt.Errorf("insert github setup state: %w", err)
		}
		_, err := c.Audit.AppendInTx(ctx, tx, githubSetupAuditRow(
			request.RequestID, request.SourceIP, request.UserAgent,
			request.Principal.UserID, request.Principal.AuthMethod,
			state, 0, nil, "github_app_setup_issued", now,
		))
		return err
	})
	if err != nil {
		return GitHubSetupInitiateResult{}, classifyGitHubSetupError(err)
	}
	return GitHubSetupInitiateResult{InstallURL: installURL, ExpiresAt: state.ExpiresTime()}, nil
}

func (c *PostgresGitHubSetupCoordinator) Complete(
	ctx context.Context,
	request GitHubSetupCallbackRequest,
) (GitHubSetupCallbackResult, error) {
	if err := c.ready(); err != nil {
		return GitHubSetupCallbackResult{}, err
	}
	if strings.TrimSpace(request.OAuthCode) == "" || len(request.OAuthCode) > 1024 || request.InstallationID <= 0 {
		return GitHubSetupCallbackResult{}, ErrGitHubSetupInvalid
	}
	state, err := c.Codec.Verify(request.State)
	if err != nil {
		return GitHubSetupCallbackResult{}, ErrGitHubSetupInvalid
	}
	state, err = c.preflight(ctx, state)
	if err != nil {
		return GitHubSetupCallbackResult{}, err
	}
	userToken, err := c.Verifier.ExchangeUserCode(ctx, request.OAuthCode)
	if err != nil {
		return GitHubSetupCallbackResult{}, err
	}
	if err := c.Verifier.UserCanAccessInstallation(ctx, userToken, request.InstallationID); err != nil {
		return GitHubSetupCallbackResult{}, err
	}
	installation, err := c.Verifier.GetInstallation(ctx, request.InstallationID)
	if err != nil {
		return GitHubSetupCallbackResult{}, err
	}
	if installation.ID != request.InstallationID || strings.TrimSpace(installation.Account) == "" {
		return GitHubSetupCallbackResult{}, ErrGitHubSetupInvalid
	}
	repositories, err := c.Verifier.ListReposForInstallation(ctx, request.InstallationID)
	if err != nil {
		return GitHubSetupCallbackResult{}, err
	}
	if err := validateGitHubSetupRepositories(repositories); err != nil {
		return GitHubSetupCallbackResult{}, err
	}

	now := c.now()
	var result GitHubSetupCallbackResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			if err := lockGitHubSetupStateInTx(ctx, tx, state, now); err != nil {
				return err
			}
			if err := requireActiveGitHubSetupAdminInTx(ctx, tx, state, now); err != nil {
				return err
			}
			grant, err := c.Links.PutInstallationGrantInTx(ctx, tx, github.InstallationGrant{
				WorkspaceID: state.WorkspaceID, InstallationID: installation.ID,
				AccountLogin: installation.Account, TargetType: installation.TargetType,
				GrantedByActor: state.UserID,
			})
			if err != nil {
				return err
			}
			current, err := c.Links.ListRepositoryGrantsInTx(ctx, tx, state.WorkspaceID)
			if err != nil {
				return err
			}
			target := make(map[int64]github.Repo, len(repositories))
			for _, repository := range repositories {
				target[repository.ID] = repository
			}
			var unlinked []string
			for _, repository := range current {
				if repository.InstallationID != installation.ID {
					continue
				}
				if _, keep := target[repository.RepositoryID]; keep {
					continue
				}
				serviceIDs, err := c.Links.RevokeRepositoryGrantInTx(
					ctx, tx, state.WorkspaceID, installation.ID, repository.RepositoryID,
				)
				if err != nil {
					return err
				}
				unlinked = append(unlinked, serviceIDs...)
			}
			for _, repository := range repositories {
				if _, err := c.Links.PutRepositoryGrantInTx(ctx, tx, github.RepositoryGrant{
					WorkspaceID: state.WorkspaceID, InstallationID: installation.ID,
					RepositoryID: repository.ID, Repository: repository.FullName,
					CloneURL: repository.CloneURL, DefaultBranch: repository.Default,
					Private: repository.Private, VerifiedAt: now, GrantedByActor: state.UserID,
				}); err != nil {
					return err
				}
			}
			sort.Strings(unlinked)
			if _, err := tx.Exec(ctx, `
				UPDATE github_app_setup_states
				   SET consumed_at=$2,installation_id=$3
				 WHERE nonce_sha256=$1`,
				state.NonceHash(), now, installation.ID,
			); err != nil {
				return fmt.Errorf("consume github setup state: %w", err)
			}
			_, err = c.Audit.AppendInTx(ctx, tx, githubSetupAuditRow(
				request.RequestID, request.SourceIP, request.UserAgent,
				state.UserID, "github-oauth+midnight-state", state,
				installation.ID, repositoryIDs(repositories),
				"github_app_installation_bound", now,
			))
			if err != nil {
				return err
			}
			result = GitHubSetupCallbackResult{
				WorkspaceID: state.WorkspaceID, Installation: github.Installation{
					ID: grant.InstallationID, Account: grant.AccountLogin,
					TargetType: grant.TargetType, HTMLURL: installation.HTMLURL,
				},
				RepositoryCount: len(repositories), UnlinkedServiceIDs: unlinked,
			}
			return nil
		})
		if err == nil {
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return GitHubSetupCallbackResult{}, classifyGitHubSetupError(err)
		}
	}
	return GitHubSetupCallbackResult{}, ErrGitHubSetupUnavailable
}

func (c *PostgresGitHubSetupCoordinator) preflight(ctx context.Context, state github.SetupState) (github.SetupState, error) {
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx *pg.Tx) error {
		var consumedAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT workspace_id,user_id,session_id,consumed_at
			  FROM github_app_setup_states
			 WHERE nonce_sha256=$1
			   AND issued_at=$2 AND expires_at=$3`,
			state.NonceHash(),
			state.IssuedTime(), state.ExpiresTime(),
		).Scan(&state.WorkspaceID, &state.UserID, &state.SessionID, &consumedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGitHubSetupInvalid
		}
		if err != nil {
			return err
		}
		if consumedAt != nil {
			return ErrGitHubSetupConsumed
		}
		return requireActiveGitHubSetupAdminInTx(ctx, tx, state, c.now())
	})
	if err != nil {
		return github.SetupState{}, err
	}
	return state, nil
}

func (c *PostgresGitHubSetupCoordinator) ready() error {
	if c == nil || c.Pool == nil || c.Links == nil || c.Audit == nil || c.Codec == nil || c.Verifier == nil {
		return ErrGitHubSetupUnavailable
	}
	if err := c.Config.ValidateSetup(); err != nil {
		return ErrGitHubSetupUnavailable
	}
	return nil
}

func (c *PostgresGitHubSetupCoordinator) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func requireActiveGitHubSetupAdminInTx(ctx context.Context, tx *pg.Tx, state github.SetupState, now time.Time) error {
	var role string
	err := tx.QueryRow(ctx, `
		SELECT member.role
		  FROM auth_sessions session
		  JOIN users identity_user ON identity_user.id=session.user_id
		  JOIN workspace_members member
		    ON member.user_id=session.user_id AND member.workspace_id=$3
		 WHERE session.id=$1 AND session.user_id=$2
		   AND session.revoked_at IS NULL
		   AND session.expires_at > $4 AND session.idle_expires_at > $4
		   AND session.session_generation=identity_user.session_generation
		   AND identity_user.is_disabled=FALSE AND identity_user.deleted_at IS NULL
		   AND member.accepted_at IS NOT NULL
		 FOR SHARE OF session,identity_user,member`,
		state.SessionID, state.UserID, state.WorkspaceID, now,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGitHubSetupForbidden
	}
	if err != nil {
		return err
	}
	if role != string(identity.WorkspaceRoleAdmin) {
		return ErrGitHubSetupForbidden
	}
	return nil
}

func lockGitHubSetupStateInTx(ctx context.Context, tx *pg.Tx, state github.SetupState, now time.Time) error {
	var consumedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT consumed_at
		  FROM github_app_setup_states
		 WHERE nonce_sha256=$1
		   AND workspace_id=$2 AND user_id=$3 AND session_id=$4
		   AND issued_at=$5 AND expires_at=$6 AND expires_at > $7
		 FOR UPDATE`,
		state.NonceHash(), state.WorkspaceID, state.UserID, state.SessionID,
		state.IssuedTime(), state.ExpiresTime(), now,
	).Scan(&consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGitHubSetupInvalid
	}
	if err != nil {
		return err
	}
	if consumedAt != nil {
		return ErrGitHubSetupConsumed
	}
	return nil
}

func validateGitHubSetupRepositories(repositories []github.Repo) error {
	seen := make(map[int64]struct{}, len(repositories))
	for _, repository := range repositories {
		if repository.ID <= 0 || strings.TrimSpace(repository.FullName) == "" ||
			strings.TrimSpace(repository.CloneURL) == "" || strings.TrimSpace(repository.Default) == "" {
			return ErrGitHubSetupInvalid
		}
		if _, duplicate := seen[repository.ID]; duplicate {
			return ErrGitHubSetupInvalid
		}
		seen[repository.ID] = struct{}{}
	}
	return nil
}

func repositoryIDs(repositories []github.Repo) []int64 {
	ids := make([]int64, 0, len(repositories))
	for _, repository := range repositories {
		ids = append(ids, repository.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func githubSetupAuditRow(
	requestID, sourceIP, userAgent, actorID, authMethod string,
	state github.SetupState,
	installationID int64,
	repositoryIDs []int64,
	action string,
	now time.Time,
) audit.Row {
	attrs := map[string]any{
		"session_id":    state.SessionID,
		"state_version": state.Version,
	}
	if installationID > 0 {
		attrs["installation_id"] = installationID
		attrs["repository_ids"] = repositoryIDs
	}
	return audit.Row{
		RequestID: requestID, OccurredAt: now, RecordedAt: now,
		ActorID: actorID, ActorType: "user", AuthMethod: authMethod,
		SourceIP: normalizedMutationSourceIP(sourceIP), UserAgent: userAgent,
		WorkspaceID: state.WorkspaceID, ResourceKind: "github_app_setup",
		ResourceID: hex.EncodeToString(state.NonceHash()[:8]), Action: action,
		PolicyDecision: "allowed", Attrs: attrs,
	}
}

func classifyGitHubSetupError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrGitHubSetupInvalid), errors.Is(err, ErrGitHubSetupConsumed),
		errors.Is(err, ErrGitHubSetupForbidden), errors.Is(err, ErrGitHubSetupUnavailable),
		errors.Is(err, github.ErrSourceLinkConflict), errors.Is(err, github.ErrAppHTTP),
		errors.Is(err, github.ErrSetupStateInvalid):
		return err
	default:
		return fmt.Errorf("%w: %v", ErrGitHubSetupUnavailable, err)
	}
}

func registerGitHubSetupRoutes(
	mux handlerRegistrar,
	manager *identity.Manager,
	coordinator GitHubSetupCoordinator,
) {
	if coordinator == nil {
		return
	}
	if manager != nil {
		mux.Handle(
			v1alphaPrefix+"/workspaces/{workspace_id}/github/setup",
			identitySessionMiddleware(manager)(githubSetupInitiateHandler(coordinator)),
		)
	}
	// GitHub's browser redirect cannot carry Midnight's Authorization header.
	// The signed, one-time state and GitHub OAuth proof are the callback's two
	// independent authorities.
	mux.Handle(githubSetupCallbackPath, githubSetupCallbackHandler(coordinator))
}

func githubSetupInitiateHandler(coordinator GitHubSetupCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		limited := http.MaxBytesReader(w, r.Body, githubSetupBodyLimit)
		body, err := io.ReadAll(limited)
		_ = limited.Close()
		if err != nil || len(bytes.TrimSpace(body)) != 0 {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub setup initiation requires an empty body", nil)
			return
		}
		principal, principalOK := identityPrincipalFromContext(r.Context())
		session, sessionOK := identitySessionFromContext(r.Context())
		if !principalOK || !sessionOK {
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
			return
		}
		result, err := coordinator.Initiate(r.Context(), GitHubSetupInitiateRequest{
			WorkspaceID: strings.TrimSpace(r.PathValue("workspace_id")),
			Principal:   principal, Session: session,
			RequestID: middleware.FromContext(r.Context()), SourceIP: r.RemoteAddr, UserAgent: r.UserAgent(),
		})
		if writeGitHubSetupError(w, r, err) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusCreated, result)
	}
}

func githubSetupCallbackHandler(coordinator GitHubSetupCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		query := r.URL.Query()
		for key, values := range query {
			if key != "state" && key != "code" && key != "installation_id" && key != "setup_action" {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub callback contains an unsupported query field", nil)
				return
			}
			if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub callback query fields must be singular and non-empty", nil)
				return
			}
		}
		if action := query.Get("setup_action"); action != "" && action != "install" && action != "update" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub callback setup_action is invalid", nil)
			return
		}
		installationID, err := strconv.ParseInt(query.Get("installation_id"), 10, 64)
		if err != nil || installationID <= 0 {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub callback installation_id is invalid", nil)
			return
		}
		_, err = coordinator.Complete(r.Context(), GitHubSetupCallbackRequest{
			State: query.Get("state"), OAuthCode: query.Get("code"), InstallationID: installationID,
			RequestID: middleware.FromContext(r.Context()), SourceIP: r.RemoteAddr, UserAgent: r.UserAgent(),
		})
		if writeGitHubSetupError(w, r, err) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, "/?github_setup=connected", http.StatusSeeOther)
	}
}

func writeGitHubSetupError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrGitHubSetupInvalid), errors.Is(err, github.ErrSetupStateInvalid):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub setup state or callback is invalid", nil)
	case errors.Is(err, ErrGitHubSetupConsumed):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "GitHub setup state was already used", nil)
	case errors.Is(err, ErrGitHubSetupForbidden):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "workspace administrator access is required", nil)
	case errors.Is(err, github.ErrSourceLinkConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "GitHub installation is already assigned to another workspace", nil)
	case errors.Is(err, github.ErrAppHTTP):
		writeRequestError(w, r, http.StatusBadGateway, failurecodes.InternalError, "GitHub did not verify this installation callback", nil)
	case errors.Is(err, ErrGitHubSetupUnavailable), errors.Is(err, context.DeadlineExceeded):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "GitHub setup is temporarily unavailable", nil)
	default:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "GitHub setup failed", nil)
	}
	return true
}
