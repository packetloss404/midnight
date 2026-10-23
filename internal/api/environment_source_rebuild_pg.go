// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

var errEnvironmentSourceAuthorityChanged = errors.New("environment source authority changed during apply")

// discoverEnvironmentChangesetGitHubInstallations performs the lock-set
// discovery required by the canonical GitHub source-link lock order. The
// accepting transaction acquires these installation fences before any product
// row and then proves that a rebuild still uses one of the discovered links.
func (c *PostgresTenantMutationCoordinator) discoverEnvironmentChangesetGitHubInstallations(
	ctx context.Context,
	environmentID string,
) ([]int64, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT DISTINCT link.installation_id
		  FROM environment_changesets changeset
		  JOIN environment_changeset_items item
		    ON item.changeset_id=changeset.id
		   AND item.kind='runtime'
		  JOIN service_github_links link
		    ON link.service_id=item.service_id
		   AND link.project_id=changeset.project_id
		 WHERE changeset.environment_id=$1
		   AND changeset.state='draft'
		 ORDER BY link.installation_id`, strings.TrimSpace(environmentID))
	if err != nil {
		return nil, fmt.Errorf("discover environment GitHub source locks: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var installationID int64
		if err := rows.Scan(&installationID); err != nil {
			return nil, err
		}
		out = append(out, installationID)
	}
	return out, rows.Err()
}

func lockEnvironmentChangesetGitHubInstallations(ctx context.Context, tx *pg.Tx, installationIDs []int64) error {
	installationIDs = append([]int64(nil), installationIDs...)
	sort.Slice(installationIDs, func(i, j int) bool { return installationIDs[i] < installationIDs[j] })
	for _, installationID := range installationIDs {
		if err := githubapp.LockInstallationInTx(ctx, tx, installationID); err != nil {
			return err
		}
	}
	return nil
}

func containsInstallationID(values []int64, candidate int64) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (c *PostgresTenantMutationCoordinator) canonicalEnvironmentRebuildSourceInTx(
	ctx context.Context,
	tx *pg.Tx,
	authorization tenantDeployAuthorization,
	source DeploymentRecord,
	configuredRoot string,
	lockedInstallations []int64,
) (*DeploySourceIntent, error) {
	sha := strings.ToLower(strings.TrimSpace(source.SHA))
	if !validPinnedSourceCommit(sha) {
		return nil, fmt.Errorf("%w: active source release has no immutable commit", ErrDeploymentActionConflict)
	}
	switch strings.ToLower(strings.TrimSpace(source.SourceType)) {
	case "git":
		repo := strings.TrimSpace(source.Repo)
		if err := ValidateGenericGitSourceRepo(repo); err != nil {
			return nil, fmt.Errorf("%w: active git source is no longer admissible", ErrDeploymentActionConflict)
		}
		return &DeploySourceIntent{Provider: "git", Repo: repo, Ref: sha, RootDirectory: configuredRoot}, nil
	case "github":
		var sourceIntent DeploySourceIntent
		var installationResourceVersion, repositoryResourceVersion int64
		err := tx.QueryRow(ctx, `
			SELECT link.workspace_id,link.installation_id,link.repository_id,
			       link.repository_full_name,repository.clone_url,link.branch,
			       link.config_path,link.resource_version,
			       installation.resource_version,repository.resource_version
			  FROM service_github_links link
			  JOIN github_workspace_repository_grants repository
			    ON repository.workspace_id=link.workspace_id
			   AND repository.installation_id=link.installation_id
			   AND repository.repository_id=link.repository_id
			   AND repository.repository_full_name=link.repository_full_name
			  JOIN github_workspace_installation_grants installation
			    ON installation.workspace_id=link.workspace_id
			   AND installation.installation_id=link.installation_id
			 WHERE link.workspace_id=$1
			   AND link.project_id=$2
			   AND link.service_id=$3
			 FOR SHARE OF link,repository,installation`,
			authorization.WorkspaceID, source.ProjectID, source.ServiceID,
		).Scan(
			&sourceIntent.WorkspaceID,
			&sourceIntent.InstallationID,
			&sourceIntent.RepositoryID,
			&sourceIntent.Repository,
			&sourceIntent.Repo,
			&sourceIntent.AuthorityBranch,
			&sourceIntent.ConfigPath,
			&sourceIntent.LinkResourceVersion,
			&installationResourceVersion,
			&repositoryResourceVersion,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: canonical GitHub source link is unavailable", ErrDeploymentActionConflict)
		}
		if err != nil {
			return nil, err
		}
		if !containsInstallationID(lockedInstallations, sourceIntent.InstallationID) {
			return nil, errEnvironmentSourceAuthorityChanged
		}
		if !strings.EqualFold(strings.TrimSpace(source.Repo), strings.TrimSpace(sourceIntent.Repo)) {
			return nil, fmt.Errorf("%w: active release does not match the canonical GitHub source", ErrDeploymentActionConflict)
		}
		if installationResourceVersion <= 0 || repositoryResourceVersion <= 0 {
			return nil, fmt.Errorf("%w: canonical GitHub grants are unavailable", ErrDeploymentActionConflict)
		}
		sourceIntent.Provider = "github"
		sourceIntent.TrustProfile = DeployTrustProfileGitHubManual
		sourceIntent.Ref = sha
		sourceIntent.RootDirectory = configuredRoot
		return &sourceIntent, nil
	default:
		return nil, fmt.Errorf("%w: active release is not backed by a rebuildable canonical source", ErrDeploymentActionConflict)
	}
}

func validPinnedSourceCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	nonzero := false
	for _, candidate := range value {
		switch {
		case candidate >= '0' && candidate <= '9':
		case candidate >= 'a' && candidate <= 'f':
		default:
			return false
		}
		if candidate != '0' {
			nonzero = true
		}
	}
	return nonzero
}

func (c *PostgresTenantMutationCoordinator) commitEnvironmentSourceRebuildInTx(
	ctx context.Context,
	tx *pg.Tx,
	request EnvironmentChangesetMutationRequest,
	reservation idempotency.Reservation,
	authorization tenantDeployAuthorization,
	changeset EnvironmentChangeset,
	affected EnvironmentAffectedResource,
	source DeploymentRecord,
	appliedRevision int64,
	lockedInstallations []int64,
	now time.Time,
	ids deploymentActionPreparedIDs,
) (DeploymentActionResult, error) {
	if c.DeployOutbox == nil || c.DeployOutbox.Pool != c.Pool {
		return DeploymentActionResult{}, fmt.Errorf("%w: source rebuild outbox is unavailable", ErrDeploymentActionConflict)
	}
	config, err := c.ServiceConfigs.GetInTx(ctx, tx, authorization.ProjectID, changeset.EnvironmentID, affected.ServiceID)
	if err != nil {
		return DeploymentActionResult{}, fmt.Errorf("load applied source rebuild config: %w", err)
	}
	sourceIntent, err := c.canonicalEnvironmentRebuildSourceInTx(ctx, tx, authorization, source, config.RootDirectory, lockedInstallations)
	if err != nil {
		return DeploymentActionResult{}, err
	}
	operation := orailtypes.Operation{ID: ids.OperationID, Kind: "build", State: orailtypes.OperationQueued}
	triggerKey := fmt.Sprintf("changeset:%s:%d:%s:rebuild", changeset.ID, appliedRevision, affected.ServiceID)
	deployment := DeploymentRecord{
		ID: ids.DeploymentID, ProjectID: authorization.ProjectID,
		ServiceID: affected.ServiceID, EnvironmentID: changeset.EnvironmentID,
		Action: deploy.ActionRedeployRebuild, SourceType: sourceIntent.Provider,
		Repo: sourceIntent.Repo, Ref: sourceIntent.Ref, SHA: sourceIntent.Ref,
		RootDirectory: config.RootDirectory, DockerfilePath: config.DockerfilePath,
		Builder: config.Builder, OperationID: string(operation.ID), TriggerKey: triggerKey,
		RollbackSourceID: source.ID, ExpectedActiveID: source.ID,
	}
	intake := DeployIntakeRequest{
		EnvironmentID:  changeset.EnvironmentID,
		Source:         sourceIntent,
		DeploymentID:   deployment.ID,
		InstanceID:     deployment.ID + "-r0",
		PreviousActive: &DeployActiveInstanceRef{DeploymentID: source.ID, InstanceID: source.ID + "-r0"},
	}
	payload, err := buildDeployJobPayload(operation.ID, Service{ID: orailtypes.ID(affected.ServiceID), ProjectID: orailtypes.ID(authorization.ProjectID)}, intake, now)
	if err != nil {
		return DeploymentActionResult{}, err
	}
	enqueue := buildDeployEnqueueRequest(operation.ID, Service{ID: orailtypes.ID(affected.ServiceID), ProjectID: orailtypes.ID(authorization.ProjectID)}, now, payload)
	enqueue.Queue = queue.Build
	response := idempotency.StoredResponse{StatusCode: http.StatusAccepted, Header: make(http.Header)}
	commit, err := c.DeployOutbox.CommitGroupedDeployIntakeInTx(ctx, tx, DeployIntakeCommit{
		Operation: operation, OperationActor: request.Principal.UserID,
		TargetKind: "service", TargetID: affected.ServiceID,
		Deployment: deployment, IdempotencyKey: request.Key,
		IdempotencyRef: reservation.Reference(), Response: response,
		Enqueue: enqueue, InitialMessage: "configuration rebuild accepted from immutable source " + source.ID,
		Now: now,
	}, ids.OutboxID, ids.EventID)
	if err != nil {
		return DeploymentActionResult{}, err
	}
	if err := cloneEnvironmentRebuildRoutesInTx(ctx, tx, source, commit.Deployment.ID, now); err != nil {
		return DeploymentActionResult{}, err
	}
	if _, err := bindServiceManagedGeneratedRoutesInTx(ctx, tx, commit.Deployment.ID, authorization.ProjectID, changeset.EnvironmentID, affected.ServiceID, now); err != nil {
		return DeploymentActionResult{}, err
	}
	return DeploymentActionResult{
		Operation: commit.Operation, Deployment: commit.Deployment,
		OutboxID: commit.OutboxID, Created: true,
	}, nil
}

func cloneEnvironmentRebuildRoutesInTx(ctx context.Context, tx *pg.Tx, source DeploymentRecord, deploymentID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id,route_id,domain_id,project_id,environment_id,service_id,created_at
		)
		SELECT $1,i.route_id,i.domain_id,i.project_id,i.environment_id,i.service_id,$2
		  FROM deployment_route_intents i
		  JOIN http_routes route
		    ON route.id=i.route_id AND route.project_id=i.project_id
		   AND route.environment_id=i.environment_id AND route.service_id=i.service_id
		  JOIN domains domain
		    ON domain.id=i.domain_id AND domain.project_id=i.project_id
		   AND domain.environment_id=i.environment_id AND domain.service_id=i.service_id
		 WHERE i.deployment_id=$3 AND i.project_id=$4
		   AND i.environment_id=$5 AND i.service_id=$6
		   AND domain.tls_desired=FALSE AND domain.tls_state='disabled'
		   AND (domain.kind='generated' OR (domain.kind='custom' AND EXISTS (
			SELECT 1 FROM domain_dns_challenges challenge
			 WHERE challenge.domain_id=domain.id AND challenge.state='verified'
		   )))
		   AND route.public_enabled=TRUE
		ON CONFLICT (deployment_id,route_id) DO NOTHING`,
		deploymentID, now, source.ID, source.ProjectID, source.EnvironmentID, source.ServiceID,
	)
	if err != nil {
		return fmt.Errorf("source rebuild route intent clone: %w", err)
	}
	return nil
}

func environmentDeploymentIntakeFailure(action string, err error) (string, string, bool) {
	if errors.Is(err, ErrDeploymentActionConflict) || errors.Is(err, ErrDeploymentSnapshotAbsent) || errors.Is(err, ErrDeploymentNotFound) {
		reason := "configuration applied, but deployment intake could not use the current release"
		if action == environmentDeploymentActionRebuild {
			reason = "configuration applied, but rebuild intake requires an active immutable canonical source"
		}
		return string(failurecodes.ResourceConflict), reason, true
	}
	return "", "", false
}
