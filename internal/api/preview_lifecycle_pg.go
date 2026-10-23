// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const previewLifetime = 7 * 24 * time.Hour

// PostgresGitHubPreviewLifecycle is the durable PR-to-service lifecycle
// coordinator. It deliberately clones only the focused service and scoped
// variables; volumes, databases, domains, and production data are never
// copied from the source environment.
type PostgresGitHubPreviewLifecycle struct {
	Pool        *pg.Pool
	Lookup      githubapp.TemporalServiceLookup
	KeyProvider secrets.KeyProvider
	Routes      *router.PostgresDurableRouteStore
	Actions     DeploymentActionIntake
	AppsDomain  string
	Now         func() time.Time
}

func NewPostgresGitHubPreviewLifecycle(
	pool *pg.Pool,
	lookup githubapp.TemporalServiceLookup,
	keyProvider secrets.KeyProvider,
	routes *router.PostgresDurableRouteStore,
	actions DeploymentActionIntake,
	appsDomain string,
) *PostgresGitHubPreviewLifecycle {
	return &PostgresGitHubPreviewLifecycle{
		Pool: pool, Lookup: lookup, KeyProvider: keyProvider, Routes: routes,
		Actions: actions, AppsDomain: strings.TrimSpace(appsDomain),
		Now: func() time.Time { return time.Now().UTC() },
	}
}

func (c *PostgresGitHubPreviewLifecycle) PlanPullRequest(
	ctx context.Context,
	event githubapp.PullRequestEvent,
	receivedAt time.Time,
	deliveryID string,
) ([]githubapp.DeployIntent, error) {
	if c == nil || c.Pool == nil {
		return nil, errors.New("preview lifecycle: postgres pool is required")
	}
	if event.IsClose() {
		return c.planPreviewClose(ctx, event, deliveryID)
	}
	if c.Lookup == nil || receivedAt.IsZero() {
		return nil, errors.New("preview lifecycle: temporal source authority is required")
	}
	links, err := c.Lookup.FindByRepoBranchAt(
		ctx,
		event.InstallationID,
		event.BaseRepositoryID,
		event.BaseRepository,
		event.BaseBranch,
		receivedAt.UTC(),
	)
	if err != nil {
		return nil, err
	}
	out := make([]githubapp.DeployIntent, 0, len(links))
	for _, linked := range links {
		if linked.Link.InstallationID != event.InstallationID ||
			linked.Link.RepositoryID != event.BaseRepositoryID ||
			!strings.EqualFold(linked.Link.Repo, event.BaseRepository) ||
			linked.Link.Branch != event.BaseBranch {
			continue
		}
		out = append(out, previewDeployIntent(event, linked, deliveryID))
	}
	return out, nil
}

func previewDeployIntent(
	event githubapp.PullRequestEvent,
	linked githubapp.LinkedService,
	deliveryID string,
) githubapp.DeployIntent {
	repo := strings.ToLower(strings.TrimSpace(event.BaseRepository))
	intent := githubapp.DeployIntent{
		ServiceID: linked.ServiceID, ProjectID: linked.ProjectID, WorkspaceID: linked.WorkspaceID,
		Repo: repo, RepositoryID: event.BaseRepositoryID,
		CloneURL: "https://github.com/" + repo + ".git",
		Ref:      "refs/heads/" + event.BaseBranch, SHA: event.HeadSHA,
		RootDirectory: linked.Link.RootDirectory, ConfigPath: linked.Link.ConfigPath,
		InstallationID:                   event.InstallationID,
		InstallationGrantResourceVersion: linked.InstallationGrantResourceVersion,
		InstallationGrantCreatedAt:       linked.InstallationGrantCreatedAt,
		InstallationGrantUpdatedAt:       linked.InstallationGrantUpdatedAt,
		RepositoryGrantResourceVersion:   linked.RepositoryGrantResourceVersion,
		RepositoryGrantCreatedAt:         linked.RepositoryGrantCreatedAt,
		RepositoryGrantUpdatedAt:         linked.RepositoryGrantUpdatedAt,
		RepositoryAccessVerifiedAt:       linked.RepositoryAccessVerifiedAt,
		LinkVersion:                      linked.LinkResourceVersion,
		LinkCreatedAt:                    linked.LinkCreatedAt, LinkUpdatedAt: linked.LinkUpdatedAt,
		LinkRepositoryAccessVerifiedAt: linked.LinkRepositoryAccessVerifiedAt,
		Untrusted:                      event.FromFork,
		IdempotencyKey: fmt.Sprintf("gh-pr-%d-%d-%d-v%d-%s-%s",
			event.InstallationID, event.BaseRepositoryID, event.Number,
			linked.LinkResourceVersion, event.HeadSHA, linked.ServiceID),
		TriggerReason: "pull_request:" + string(event.Action),
		Preview: &githubapp.PullRequestPreviewIntent{
			Action: previewActionUpsert, DeliveryID: strings.TrimSpace(deliveryID),
			Number: event.Number, BaseBranch: event.BaseBranch,
			HeadRepositoryID: event.HeadRepositoryID, HeadRepository: event.HeadRepository,
			HeadBranch: event.HeadBranch, HeadSHA: event.HeadSHA,
			FromFork: event.FromFork, SenderLogin: event.SenderLogin,
		},
	}
	return intent
}

func (c *PostgresGitHubPreviewLifecycle) planPreviewClose(
	ctx context.Context,
	event githubapp.PullRequestEvent,
	deliveryID string,
) ([]githubapp.DeployIntent, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT id,workspace_id,project_id,service_id,environment_id,
		       installation_grant_resource_version,installation_grant_created_at,
		       installation_grant_updated_at,repository_grant_resource_version,
		       repository_grant_created_at,repository_grant_updated_at,
		       repository_access_verified_at,link_resource_version,link_created_at,
		       link_updated_at,link_repository_access_verified_at,clone_url,
		       root_directory,config_path,base_branch,head_repository_id,
		       head_repository_full_name,head_branch,head_sha,from_fork
		  FROM github_pull_request_previews
		 WHERE installation_id=$1 AND repository_id=$2 AND pull_request_number=$3
		   AND state <> 'deleted'
		 ORDER BY service_id`,
		event.InstallationID, event.BaseRepositoryID, event.Number,
	)
	if err != nil {
		return nil, fmt.Errorf("plan preview close: %w", err)
	}
	defer rows.Close()
	var out []githubapp.DeployIntent
	for rows.Next() {
		var intent githubapp.DeployIntent
		var previewID, environmentID, baseBranch, headRepo, headBranch, headSHA string
		var headRepoID *int64
		var fromFork bool
		if err := rows.Scan(
			&previewID, &intent.WorkspaceID, &intent.ProjectID, &intent.ServiceID, &environmentID,
			&intent.InstallationGrantResourceVersion, &intent.InstallationGrantCreatedAt,
			&intent.InstallationGrantUpdatedAt, &intent.RepositoryGrantResourceVersion,
			&intent.RepositoryGrantCreatedAt, &intent.RepositoryGrantUpdatedAt,
			&intent.RepositoryAccessVerifiedAt, &intent.LinkVersion, &intent.LinkCreatedAt,
			&intent.LinkUpdatedAt, &intent.LinkRepositoryAccessVerifiedAt, &intent.CloneURL,
			&intent.RootDirectory, &intent.ConfigPath, &baseBranch, &headRepoID,
			&headRepo, &headBranch, &headSHA, &fromFork,
		); err != nil {
			return nil, fmt.Errorf("scan preview close target: %w", err)
		}
		intent.InstallationID = event.InstallationID
		intent.RepositoryID = event.BaseRepositoryID
		intent.Repo = strings.ToLower(strings.TrimSpace(event.BaseRepository))
		intent.Ref = "refs/heads/" + baseBranch
		intent.SHA = headSHA
		intent.Untrusted = fromFork
		intent.IdempotencyKey = fmt.Sprintf("gh-pr-close-%d-%d-%d-%s", event.InstallationID, event.BaseRepositoryID, event.Number, intent.ServiceID)
		intent.TriggerReason = "pull_request:closed"
		intent.Preview = &githubapp.PullRequestPreviewIntent{
			Action: previewActionDelete, DeliveryID: strings.TrimSpace(deliveryID), Number: event.Number,
			BaseBranch: baseBranch, HeadRepository: headRepo, HeadBranch: headBranch,
			HeadSHA: headSHA, FromFork: fromFork, ExistingPreviewID: previewID,
			ExistingEnvironmentID: environmentID,
		}
		if headRepoID != nil {
			intent.Preview.HeadRepositoryID = *headRepoID
		}
		out = append(out, intent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate preview close targets: %w", err)
	}
	return out, nil
}

func (c *PostgresGitHubPreviewLifecycle) PreparePullRequest(
	ctx context.Context,
	intent githubapp.DeployIntent,
) (PreviewPreparation, error) {
	if err := c.validatePrepare(intent); err != nil {
		return PreviewPreparation{}, err
	}
	now := c.now().Truncate(time.Microsecond)
	var out PreviewPreparation
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		var err error
		out, err = c.preparePullRequestInTx(ctx, tx, intent, now)
		return err
	})
	return out, err
}

func (c *PostgresGitHubPreviewLifecycle) validatePrepare(intent githubapp.DeployIntent) error {
	if c == nil || c.Pool == nil || c.KeyProvider == nil || c.Routes == nil ||
		strings.TrimSpace(c.AppsDomain) == "" {
		return errors.New("preview lifecycle: postgres, encryption, routing, and apps domain are required")
	}
	if intent.Preview == nil || intent.Preview.Action != previewActionUpsert ||
		intent.Preview.Number <= 0 || intent.Preview.HeadSHA != intent.SHA ||
		intent.Preview.BaseBranch != strings.TrimPrefix(intent.Ref, "refs/heads/") ||
		intent.Preview.FromFork != intent.Untrusted ||
		strings.TrimSpace(intent.WorkspaceID) == "" || strings.TrimSpace(intent.ProjectID) == "" ||
		strings.TrimSpace(intent.ServiceID) == "" {
		return errors.New("preview lifecycle: invalid immutable upsert intent")
	}
	return nil
}

func (c *PostgresGitHubPreviewLifecycle) preparePullRequestInTx(
	ctx context.Context,
	tx *pg.Tx,
	intent githubapp.DeployIntent,
	now time.Time,
) (PreviewPreparation, error) {
	preview := intent.Preview
	var previewID, environmentID, sourceEnvironmentID string
	var existingState, existingHeadSHA, existingDeploymentID, existingPreviewURL string
	var existingLinkVersion int64
	err := tx.QueryRow(ctx, `
		SELECT id,environment_id,source_environment_id,state,head_sha,
		       COALESCE(deployment_id,''),preview_url,link_resource_version
		  FROM github_pull_request_previews
		 WHERE installation_id=$1 AND repository_id=$2
		   AND pull_request_number=$3 AND service_id=$4
		 FOR UPDATE`,
		intent.InstallationID, intent.RepositoryID, preview.Number, intent.ServiceID,
	).Scan(&previewID, &environmentID, &sourceEnvironmentID, &existingState,
		&existingHeadSHA, &existingDeploymentID, &existingPreviewURL, &existingLinkVersion)
	newPreview := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !newPreview {
		return PreviewPreparation{}, fmt.Errorf("lock preview mapping: %w", err)
	}
	if !newPreview && existingDeploymentID != "" && existingHeadSHA == preview.HeadSHA &&
		existingLinkVersion == intent.LinkVersion &&
		(existingState == "deploying" || existingState == "ready" || existingState == "failed") {
		expiresAt := now.Add(previewLifetime)
		if _, err := tx.Exec(ctx, `UPDATE github_pull_request_previews SET
			last_delivery_id=$1,expires_at=$2,updated_at=$3,resource_version=resource_version+1
			WHERE id=$4`, preview.DeliveryID, expiresAt, now, previewID); err != nil {
			return PreviewPreparation{}, fmt.Errorf("refresh preview replay lease: %w", err)
		}
		return PreviewPreparation{PreviewID: previewID, EnvironmentID: environmentID, URL: existingPreviewURL}, nil
	}
	var serviceSlug string
	if err := tx.QueryRow(ctx, `
		SELECT slug FROM services
		 WHERE id=$1 AND project_id=$2 AND deleted_at IS NULL FOR SHARE`,
		intent.ServiceID, intent.ProjectID,
	).Scan(&serviceSlug); err != nil {
		return PreviewPreparation{}, fmt.Errorf("preview service scope: %w", err)
	}
	if newPreview {
		if err := tx.QueryRow(ctx, `
			SELECT id FROM environments
			 WHERE project_id=$1 AND slug='production' AND deleted_at IS NULL
			 FOR SHARE`, intent.ProjectID).Scan(&sourceEnvironmentID); err != nil {
			return PreviewPreparation{}, fmt.Errorf("preview production environment: %w", err)
		}
		previewID = mintID(orailtypes.IDPrefix("prv"))
		environmentID = string(defaultResourceID(orailtypes.IDPrefixEnvironment))
		slug := previewEnvironmentSlug(preview.Number, serviceSlug)
		_, err := tx.Exec(ctx, `
			INSERT INTO environments (
				id,project_id,slug,name,tier,is_protected,labels,created_at,updated_at
			) VALUES ($1,$2,$3,$4,'preview',FALSE,$5::jsonb,$6,$6)`,
			environmentID, intent.ProjectID, slug,
			fmt.Sprintf("PR #%d · %s", preview.Number, serviceSlug),
			fmt.Sprintf(`{"managed_by":"github-preview","pull_request":%d,"service_id":%q}`, preview.Number, intent.ServiceID),
			now,
		)
		if err != nil {
			return PreviewPreparation{}, fmt.Errorf("create preview environment: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE environments SET deleted_at=NULL,updated_at=$1,resource_version=resource_version+1
			 WHERE id=$2 AND project_id=$3`, now, environmentID, intent.ProjectID); err != nil {
			return PreviewPreparation{}, fmt.Errorf("restore preview environment: %w", err)
		}
		if err := tombstonePreviewVariablesInTx(ctx, tx, intent.ProjectID, environmentID, intent.ServiceID, now); err != nil {
			return PreviewPreparation{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM service_environment_configs
			 WHERE project_id=$1 AND environment_id=$2 AND service_id=$3`,
			intent.ProjectID, environmentID, intent.ServiceID); err != nil {
			return PreviewPreparation{}, fmt.Errorf("refresh preview config: %w", err)
		}
	}
	if err := copyPreviewServiceConfigInTx(ctx, tx, sourceEnvironmentID, environmentID, intent.ProjectID, intent.ServiceID, preview.FromFork, now); err != nil {
		return PreviewPreparation{}, err
	}
	duplicator := NewPostgresEnvironmentDuplicator(c.Pool, c.KeyProvider)
	duplicator.Now = c.Now
	if _, _, err := duplicator.copyPreviewVariables(ctx, tx, Environment{ID: orailtypes.ID(sourceEnvironmentID), ProjectID: orailtypes.ID(intent.ProjectID)}, Environment{ID: orailtypes.ID(environmentID), ProjectID: orailtypes.ID(intent.ProjectID)}, intent.ServiceID, preview.FromFork, now); err != nil {
		return PreviewPreparation{}, err
	}
	previewURL, err := c.ensurePreviewRouteInTx(ctx, tx, intent.ProjectID, environmentID, intent.ServiceID, now)
	if err != nil {
		return PreviewPreparation{}, err
	}
	secretPolicy, cachePolicy, quotaProfile := "non-sealed", "service-preview", "preview-default"
	if preview.FromFork {
		secretPolicy, cachePolicy, quotaProfile = "no-shared-secrets", "ephemeral", "preview-fork-restricted"
	}
	expiresAt := now.Add(previewLifetime)
	headRepoID := any(nil)
	if preview.HeadRepositoryID > 0 {
		headRepoID = preview.HeadRepositoryID
	}
	if newPreview {
		_, err = tx.Exec(ctx, `
			INSERT INTO github_pull_request_previews (
				id,workspace_id,project_id,service_id,environment_id,source_environment_id,
				installation_id,installation_grant_resource_version,installation_grant_created_at,
				installation_grant_updated_at,repository_id,repository_full_name,
				repository_grant_resource_version,repository_grant_created_at,
				repository_grant_updated_at,repository_access_verified_at,link_resource_version,
				link_created_at,link_updated_at,link_repository_access_verified_at,clone_url,
				root_directory,config_path,pull_request_number,base_branch,head_repository_id,
				head_repository_full_name,head_branch,head_sha,from_fork,secret_policy,
				cache_policy,quota_profile,state,preview_url,last_delivery_id,expires_at,
				created_at,updated_at
			) VALUES (
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
				$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,'creating',$34,$35,$36,$37,$37
			)`,
			previewID, intent.WorkspaceID, intent.ProjectID, intent.ServiceID, environmentID, sourceEnvironmentID,
			intent.InstallationID, intent.InstallationGrantResourceVersion, intent.InstallationGrantCreatedAt,
			intent.InstallationGrantUpdatedAt, intent.RepositoryID, strings.ToLower(intent.Repo),
			intent.RepositoryGrantResourceVersion, intent.RepositoryGrantCreatedAt, intent.RepositoryGrantUpdatedAt,
			intent.RepositoryAccessVerifiedAt, intent.LinkVersion, intent.LinkCreatedAt, intent.LinkUpdatedAt,
			intent.LinkRepositoryAccessVerifiedAt, strings.ToLower(intent.CloneURL), intent.RootDirectory,
			intent.ConfigPath, preview.Number, preview.BaseBranch, headRepoID,
			strings.ToLower(preview.HeadRepository), preview.HeadBranch, preview.HeadSHA, preview.FromFork,
			secretPolicy, cachePolicy, quotaProfile, previewURL, preview.DeliveryID, expiresAt, now,
		)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE github_pull_request_previews SET
				installation_grant_resource_version=$1,installation_grant_created_at=$2,
				installation_grant_updated_at=$3,repository_grant_resource_version=$4,
				repository_grant_created_at=$5,repository_grant_updated_at=$6,
				repository_access_verified_at=$7,link_resource_version=$8,link_created_at=$9,
				link_updated_at=$10,link_repository_access_verified_at=$11,clone_url=$12,
				root_directory=$13,config_path=$14,base_branch=$15,head_repository_id=$16,
				head_repository_full_name=$17,head_branch=$18,head_sha=$19,from_fork=$20,
				secret_policy=$21,cache_policy=$22,quota_profile=$23,state='refreshing',
				preview_url=$24,deployment_id=NULL,last_delivery_id=$25,expires_at=$26,
				delete_after=NULL,closed_at=NULL,updated_at=$27,resource_version=resource_version+1
			 WHERE id=$28`,
			intent.InstallationGrantResourceVersion, intent.InstallationGrantCreatedAt, intent.InstallationGrantUpdatedAt,
			intent.RepositoryGrantResourceVersion, intent.RepositoryGrantCreatedAt, intent.RepositoryGrantUpdatedAt,
			intent.RepositoryAccessVerifiedAt, intent.LinkVersion, intent.LinkCreatedAt, intent.LinkUpdatedAt,
			intent.LinkRepositoryAccessVerifiedAt, strings.ToLower(intent.CloneURL), intent.RootDirectory,
			intent.ConfigPath, preview.BaseBranch, headRepoID, strings.ToLower(preview.HeadRepository),
			preview.HeadBranch, preview.HeadSHA, preview.FromFork, secretPolicy, cachePolicy, quotaProfile,
			previewURL, preview.DeliveryID, expiresAt, now, previewID,
		)
	}
	if err != nil {
		return PreviewPreparation{}, fmt.Errorf("persist preview mapping: %w", err)
	}
	return PreviewPreparation{PreviewID: previewID, EnvironmentID: environmentID, URL: previewURL}, nil
}

func copyPreviewServiceConfigInTx(ctx context.Context, tx *pg.Tx, sourceEnvironmentID, targetEnvironmentID, projectID, serviceID string, restricted bool, now time.Time) error {
	replicas := "replicas"
	if restricted {
		replicas = "LEAST(replicas,1)"
	}
	query := `INSERT INTO service_environment_configs (
		project_id,service_id,environment_id,version,root_directory,builder,
		dockerfile_path,build_command,start_command,predeploy_command,target_port,
		health_kind,health_path,health_port,health_timeout_seconds,
		health_interval_seconds,restart_policy,restart_max_retries,replicas,
		overlap_enabled,drain_grace_seconds,created_at,updated_at
	) SELECT project_id,service_id,$1,version,root_directory,builder,dockerfile_path,
		build_command,start_command,predeploy_command,target_port,health_kind,health_path,
		health_port,health_timeout_seconds,health_interval_seconds,restart_policy,
		restart_max_retries,` + replicas + `,overlap_enabled,drain_grace_seconds,$2,$2
	  FROM service_environment_configs
	 WHERE project_id=$3 AND environment_id=$4 AND service_id=$5`
	if _, err := tx.Exec(ctx, query, targetEnvironmentID, now, projectID, sourceEnvironmentID, serviceID); err != nil {
		return fmt.Errorf("copy preview service config: %w", err)
	}
	return nil
}

func tombstonePreviewVariablesInTx(ctx context.Context, tx *pg.Tx, projectID, environmentID, serviceID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE service_variables SET value_ciphertext=''::bytea,kek_id='',wrapped_dek=''::bytea,
		       "references"='[]'::jsonb,length_bucket='deleted',kek_fingerprint='',
		       version=version+1,deleted_at=$1,updated_at=$1
		 WHERE project_id=$2 AND environment_id=$3 AND service_id IN ('',$4)
		   AND deleted_at IS NULL`, now, projectID, environmentID, serviceID)
	if err != nil {
		return fmt.Errorf("tombstone preview variables: %w", err)
	}
	return nil
}

func (s *PostgresEnvironmentDuplicator) copyPreviewVariables(ctx context.Context, tx *pg.Tx, source, target Environment, serviceID string, fork bool, now time.Time) (int, int, error) {
	scopePredicate := `scope <> 'sealed'`
	if fork {
		scopePredicate = `scope = 'plain'`
	}
	rows, err := tx.Query(ctx, `SELECT id,workspace_id,project_id,environment_id,service_id,name,scope,
		value_kind,value_ciphertext,kek_id,wrapped_dek,canonical_form,"references",length_bucket,
		kek_fingerprint,version,last_rotated_at FROM service_variables
		WHERE environment_id=$1 AND project_id=$2 AND service_id IN ('',$3)
		  AND deleted_at IS NULL AND `+scopePredicate+` ORDER BY id FOR SHARE`, source.ID, source.ProjectID, serviceID)
	if err != nil {
		return 0, 0, fmt.Errorf("preview variables list: %w", err)
	}
	var variables []duplicateVariableRecord
	for rows.Next() {
		var variable duplicateVariableRecord
		if err := rows.Scan(&variable.ID, &variable.Workspace, &variable.Project, &variable.Environment,
			&variable.Service, &variable.Name, &variable.Scope, &variable.ValueKind,
			&variable.Envelope.Ciphertext, &variable.Envelope.KEKID, &variable.Envelope.WrappedDEK,
			&variable.Envelope.CanonicalForm, &variable.References, &variable.LengthBucket,
			&variable.KEKFingerprint, &variable.Version, &variable.LastRotatedAt); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("preview variables scan: %w", err)
		}
		variables = append(variables, variable)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, fmt.Errorf("preview variables iterate: %w", err)
	}
	rows.Close()
	fingerprint, err := s.KeyProvider.Fingerprint(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("preview variables key fingerprint: %w", err)
	}
	versionCount := 0
	for _, variable := range variables {
		newID := secrets.VariableID(s.id(orailtypes.IDPrefixVariable))
		sealed, err := resealDuplicateVariable(ctx, s.KeyProvider, variable.ID, newID, variable.Envelope)
		if err != nil {
			return 0, 0, fmt.Errorf("preview variable reseal: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO service_variables (
			id,workspace_id,project_id,environment_id,service_id,name,scope,value_kind,
			value_ciphertext,kek_id,wrapped_dek,canonical_form,"references",length_bucket,
			kek_fingerprint,version,last_rotated_at,created_at,updated_at,deleted_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18,NULL)`,
			newID, variable.Workspace, target.ProjectID, target.ID, variable.Service, variable.Name,
			variable.Scope, variable.ValueKind, sealed.Ciphertext, sealed.KEKID, sealed.WrappedDEK,
			sealed.CanonicalForm, variable.References, variable.LengthBucket, fingerprint,
			variable.Version, variable.LastRotatedAt, now); err != nil {
			return 0, 0, fmt.Errorf("preview variable insert: %w", err)
		}
		copied, err := s.copyVariableVersions(ctx, tx, variable.ID, newID, target, fingerprint, now)
		if err != nil {
			return 0, 0, err
		}
		versionCount += copied
	}
	return len(variables), versionCount, nil
}

func (c *PostgresGitHubPreviewLifecycle) ensurePreviewRouteInTx(ctx context.Context, tx *pg.Tx, projectID, environmentID, serviceID string, now time.Time) (string, error) {
	targetPort, err := tenantDeployDesiredTargetPortInTx(ctx, tx, projectID, environmentID, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		targetPort = serviceconfig.DefaultTargetPort
	} else if err != nil {
		return "", err
	}
	scope := router.RouteScope{ProjectID: projectID, EnvironmentID: environmentID, ServiceID: serviceID}
	domainValue, routeValue, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{
		Scope: scope, Kind: router.DomainKindGenerated, AppsDomain: c.AppsDomain,
		PathPrefix: "/", TargetPort: targetPort, Public: true, TLSDesired: false,
	})
	if err != nil {
		return "", fmt.Errorf("preview route intent: %w", err)
	}
	domainValue, err = c.Routes.PutDomainInTx(ctx, tx, domainValue)
	if err != nil {
		return "", fmt.Errorf("preview domain persist: %w", err)
	}
	if err := c.Routes.EnsureDomainLifecycleInTx(ctx, tx, domainValue); err != nil {
		return "", fmt.Errorf("preview domain lifecycle: %w", err)
	}
	routeValue, err = c.Routes.PutHTTPRouteInTx(ctx, tx, routeValue)
	if err != nil {
		return "", fmt.Errorf("preview route persist: %w", err)
	}
	if err := c.Routes.BindServiceDomainInTx(ctx, tx, domainValue.ID, routeValue.ID, scope, now); err != nil {
		return "", fmt.Errorf("preview route bind: %w", err)
	}
	return "https://" + domainValue.NormalizedHostname, nil
}

func (c *PostgresGitHubPreviewLifecycle) ListServicePreviews(ctx context.Context, serviceID string) ([]PreviewEnvironmentView, error) {
	rows, err := c.Pool.Query(ctx, `SELECT id,pull_request_number,environment_id,
		COALESCE(deployment_id,''),state,preview_url,head_sha,from_fork,expires_at,updated_at
		FROM github_pull_request_previews WHERE service_id=$1 AND state <> 'deleted'
		ORDER BY pull_request_number DESC,id LIMIT 100`, strings.TrimSpace(serviceID))
	if err != nil {
		return nil, fmt.Errorf("list service previews: %w", err)
	}
	defer rows.Close()
	out := []PreviewEnvironmentView{}
	for rows.Next() {
		var value PreviewEnvironmentView
		if err := rows.Scan(&value.ID, &value.PullRequest, &value.EnvironmentID, &value.DeploymentID,
			&value.State, &value.URL, &value.HeadSHA, &value.FromFork, &value.ExpiresAt, &value.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan service preview: %w", err)
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (c *PostgresGitHubPreviewLifecycle) ClosePullRequest(
	ctx context.Context,
	intent githubapp.DeployIntent,
) (PreviewEnvironmentView, error) {
	if c == nil || c.Pool == nil || intent.Preview == nil ||
		intent.Preview.Action != previewActionDelete ||
		strings.TrimSpace(intent.Preview.ExistingPreviewID) == "" ||
		strings.TrimSpace(intent.Preview.ExistingEnvironmentID) == "" {
		return PreviewEnvironmentView{}, errors.New("preview lifecycle: invalid immutable close intent")
	}
	now := c.now().Truncate(time.Microsecond)
	var out PreviewEnvironmentView
	err := c.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE github_pull_request_previews SET state='deleting',closed_at=COALESCE(closed_at,$1),
			       last_delivery_id=$2,updated_at=$1,resource_version=resource_version+1
			 WHERE id=$3 AND installation_id=$4 AND repository_id=$5
			   AND pull_request_number=$6 AND service_id=$7 AND environment_id=$8
			   AND state <> 'deleted'
			 RETURNING id,pull_request_number,environment_id,COALESCE(deployment_id,''),state,
			           preview_url,head_sha,from_fork,expires_at,updated_at`,
			now, intent.Preview.DeliveryID, intent.Preview.ExistingPreviewID,
			intent.InstallationID, intent.RepositoryID, intent.Preview.Number,
			intent.ServiceID, intent.Preview.ExistingEnvironmentID,
		).Scan(&out.ID, &out.PullRequest, &out.EnvironmentID, &out.DeploymentID,
			&out.State, &out.URL, &out.HeadSHA, &out.FromFork, &out.ExpiresAt,
			&out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("close preview mapping: %w", err)
		}
		return enqueuePreviewCommentInTx(ctx, tx, out.ID, "deleting", out.DeploymentID, now)
	})
	if err != nil {
		return PreviewEnvironmentView{}, err
	}
	if out.ID != "" {
		_, _ = c.Reconcile(ctx, 1)
	}
	return out, nil
}

func enqueuePreviewCommentInTx(ctx context.Context, tx *pg.Tx, previewID, state, deploymentID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO github_pull_request_preview_comments (
			id,preview_id,dedupe_key,installation_id,repository_id,repository_full_name,
			pull_request_number,preview_state,head_sha,deployment_id,preview_url,
			available_at,created_at,updated_at
		)
		SELECT 'ghc_' || md5(id || ':' || $2 || ':' || COALESCE($3,'')),id,
		       id || ':' || $2 || ':' || COALESCE($3,''),installation_id,repository_id,
		       repository_full_name,pull_request_number,$2,head_sha,NULLIF($3,''),
		       preview_url,$4,$4,$4
		  FROM github_pull_request_previews WHERE id=$1
		ON CONFLICT (dedupe_key) DO NOTHING`,
		previewID, state, deploymentID, now)
	if err != nil {
		return fmt.Errorf("enqueue preview comment: %w", err)
	}
	return nil
}

func (c *PostgresGitHubPreviewLifecycle) Reconcile(ctx context.Context, limit int) (int, error) {
	if c == nil || c.Pool == nil {
		return 0, errors.New("preview lifecycle: postgres pool is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	now := c.now().Truncate(time.Microsecond)
	if _, err := c.Pool.Exec(ctx, `
		WITH expired AS (
			SELECT id FROM github_pull_request_previews
			 WHERE state NOT IN ('deleting','deleted') AND expires_at <= $1
			 ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		), updated AS (
			UPDATE github_pull_request_previews p SET state='deleting',closed_at=$1,
			       updated_at=$1,resource_version=resource_version+1
			  FROM expired WHERE p.id=expired.id RETURNING p.id
		)
		INSERT INTO github_pull_request_preview_comments (
			id,preview_id,dedupe_key,installation_id,repository_id,repository_full_name,
			pull_request_number,preview_state,head_sha,deployment_id,preview_url,
			available_at,created_at,updated_at
		)
		SELECT 'ghc_' || md5(p.id || ':deleting:' || COALESCE(p.deployment_id,'')),p.id,
		       p.id || ':deleting:' || COALESCE(p.deployment_id,''),p.installation_id,p.repository_id,
		       p.repository_full_name,p.pull_request_number,'deleting',p.head_sha,p.deployment_id,
		       p.preview_url,$1,$1,$1 FROM github_pull_request_previews p JOIN updated u ON u.id=p.id
		ON CONFLICT (dedupe_key) DO NOTHING`, now, limit); err != nil {
		return 0, fmt.Errorf("expire previews: %w", err)
	}
	type cleanupCandidate struct {
		previewID, serviceID, environmentID, deploymentID string
		active                                            bool
		desired                                           string
		pending                                           bool
	}
	rows, err := c.Pool.Query(ctx, `
		SELECT p.id,p.service_id,p.environment_id,COALESCE(p.deployment_id,''),
		       COALESCE(d.active,FALSE),COALESCE(d.runtime_desired_state,'removed'),
		       EXISTS (SELECT 1 FROM deployment_runtime_actions a
		                WHERE a.deployment_id=p.deployment_id AND a.state IN ('queued','running'))
		  FROM github_pull_request_previews p
		  LEFT JOIN deployments d ON d.id=p.deployment_id
		 WHERE p.state='deleting' ORDER BY p.updated_at,p.id LIMIT $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("list preview cleanup: %w", err)
	}
	var candidates []cleanupCandidate
	for rows.Next() {
		var candidate cleanupCandidate
		if err := rows.Scan(&candidate.previewID, &candidate.serviceID, &candidate.environmentID,
			&candidate.deploymentID, &candidate.active, &candidate.desired, &candidate.pending); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan preview cleanup: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate preview cleanup: %w", err)
	}
	rows.Close()
	processed := 0
	for _, candidate := range candidates {
		if candidate.pending {
			continue
		}
		if candidate.deploymentID != "" && candidate.active && candidate.desired == "running" {
			if c.Actions == nil {
				continue
			}
			_, err := c.Actions.CommitRuntimeAction(ctx, RuntimeActionRequest{
				ServiceID: candidate.serviceID, TargetID: candidate.deploymentID,
				Action:     deploy.RuntimeActionStop,
				TriggerKey: "preview-cleanup:stop:" + candidate.previewID + ":" + candidate.deploymentID,
				Now:        now,
			})
			if err != nil && !errors.Is(err, ErrDeploymentActionConflict) {
				return processed, fmt.Errorf("queue preview stop: %w", err)
			}
			processed++
			continue
		}
		if candidate.deploymentID != "" && candidate.desired != "removed" {
			if c.Actions == nil {
				continue
			}
			_, err := c.Actions.CommitRuntimeAction(ctx, RuntimeActionRequest{
				ServiceID: candidate.serviceID, TargetID: candidate.deploymentID,
				Action:     deploy.RuntimeActionRemove,
				TriggerKey: "preview-cleanup:remove:" + candidate.previewID + ":" + candidate.deploymentID,
				Now:        now,
			})
			if err != nil && !errors.Is(err, ErrDeploymentActionConflict) {
				return processed, fmt.Errorf("queue preview remove: %w", err)
			}
			processed++
			continue
		}
		if err := c.finalizePreviewCleanup(ctx, candidate.previewID, candidate.environmentID, now); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (c *PostgresGitHubPreviewLifecycle) finalizePreviewCleanup(ctx context.Context, previewID, environmentID string, now time.Time) error {
	return c.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE environments SET deleted_at=$1,updated_at=$1,
			resource_version=resource_version+1 WHERE id=$2 AND deleted_at IS NULL`, now, environmentID); err != nil {
			return fmt.Errorf("delete preview environment: %w", err)
		}
		tag, err := tx.Exec(ctx, `UPDATE github_pull_request_previews SET state='deleted',
			delete_after=$1,updated_at=$1,resource_version=resource_version+1
			WHERE id=$2 AND state='deleting'`, now, previewID)
		if err != nil {
			return fmt.Errorf("finalize preview cleanup: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return enqueuePreviewCommentInTx(ctx, tx, previewID, "deleted", "", now)
	})
}

var previewSlugUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

func previewEnvironmentSlug(number int, serviceSlug string) string {
	serviceSlug = previewSlugUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(serviceSlug)), "-")
	serviceSlug = strings.Trim(serviceSlug, "-")
	if serviceSlug == "" {
		serviceSlug = "service"
	}
	value := fmt.Sprintf("pr-%d-%s", number, serviceSlug)
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	return value
}

func (c *PostgresGitHubPreviewLifecycle) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
