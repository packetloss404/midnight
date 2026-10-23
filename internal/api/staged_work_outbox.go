// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	StagedWorkGitHubBuildIntake  = "github_build_intake"
	StagedWorkBuildDeployHandoff = "build_deploy_handoff"
)

// GitHubBuildIntakeStore atomically creates source-build product truth and its
// first durable work intent.
type GitHubBuildIntakeStore interface {
	CommitGitHubBuildIntake(context.Context, GitHubBuildIntakeCommit) (StagedWorkCommitResult, error)
}

// BuildDeployHandoffStore atomically records an immutable build result and its
// deploy-stage work intent.
type BuildDeployHandoffStore interface {
	CommitBuildDeployHandoff(context.Context, BuildDeployHandoffCommit) (StagedWorkCommitResult, error)
}

// BuildDeployHandoffRecoveryStore lets a retried build job prove that its
// result+handoff transaction committed before the previous worker died.
type BuildDeployHandoffRecoveryStore interface {
	GetBuildDeployHandoff(context.Context, string) (CommittedBuildDeployHandoff, bool, error)
}

// StagedWorkDispatcher delivers generic stage intents to the jobs table.
type StagedWorkDispatcher interface {
	DispatchStagedWork(context.Context, int) (int, error)
}

// DurableStagedWorkOutbox is the complete production staged-work seam.
type DurableStagedWorkOutbox interface {
	GitHubBuildIntakeStore
	BuildDeployHandoffStore
	StagedWorkDispatcher
}

type GitHubBuildIntakeCommit struct {
	Operation       orailtypes.Operation
	Deployment      DeploymentRecord
	SourceAuthority GitHubBuildSourceAuthority
	// PreviewID optionally attaches this canonical deployment to one durable
	// PR lifecycle row in the same transaction that exposes build work.
	PreviewID      string
	PreviewURL     string
	Enqueue        queue.EnqueueRequest
	InitialMessage string
	Now            time.Time
}

// GitHubBuildSourceAuthority is the exact canonical binding observed by the
// signed-event router. CommitGitHubBuildIntake revalidates it while holding the
// installation revocation lock, closing the lookup-to-enqueue race.
type GitHubBuildSourceAuthority struct {
	WorkspaceID                      string
	ProjectID                        string
	ServiceID                        string
	InstallationID                   int64
	InstallationGrantResourceVersion int64
	RepositoryID                     int64
	Repository                       string
	CloneURL                         string
	RepositoryGrantResourceVersion   int64
	Branch                           string
	LinkResourceVersion              int64
}

type BuildDeployHandoffCommit struct {
	OperationID  string
	DeploymentID string
	SourceSHA    string
	Builder      string
	ImageRef     string
	ImageDigest  string
	Enqueue      queue.EnqueueRequest
	Message      string
	Now          time.Time
}

type StagedWorkCommitResult struct {
	OperationID  string
	DeploymentID string
	OutboxID     string
	Created      bool
}

type CommittedBuildDeployHandoff struct {
	OperationID  string
	DeploymentID string
	OutboxID     string
	ImageRef     string
	ImageDigest  string
}

// PostgresStagedWorkOutbox persists GitHub intake and build-stage handoffs.
type PostgresStagedWorkOutbox struct {
	Pool      *pg.Pool
	Now       func() time.Time
	LeaseTime time.Duration
	WorkerID  string
	IDFactory func(prefix string) string
}

func NewPostgresStagedWorkOutbox(pool *pg.Pool) *PostgresStagedWorkOutbox {
	return &PostgresStagedWorkOutbox{
		Pool:      pool,
		Now:       func() time.Time { return time.Now().UTC() },
		LeaseTime: defaultDeployOutboxLease,
		WorkerID:  "staged-" + randomOutboxID("dispatcher"),
		IDFactory: randomOutboxID,
	}
}

func (s *PostgresStagedWorkOutbox) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *PostgresStagedWorkOutbox) newID(prefix string) string {
	if s != nil && s.IDFactory != nil {
		return s.IDFactory(prefix)
	}
	return randomOutboxID(prefix)
}

func (s *PostgresStagedWorkOutbox) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("PostgresStagedWorkOutbox: pool not initialized")
	}
	return nil
}

func (s *PostgresStagedWorkOutbox) CommitGitHubBuildIntake(ctx context.Context, request GitHubBuildIntakeCommit) (StagedWorkCommitResult, error) {
	if err := s.ready(); err != nil {
		return StagedWorkCommitResult{}, err
	}
	if err := validateGitHubBuildIntakeCommit(request); err != nil {
		return StagedWorkCommitResult{}, err
	}
	commitStatus, err := githubBuildCommitStatus(request)
	if err != nil {
		return StagedWorkCommitResult{}, err
	}
	now := request.Now.UTC()
	if now.IsZero() {
		now = s.now()
	}
	outboxID := s.newID("wox")
	result := StagedWorkCommitResult{}
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if err := revalidateGitHubBuildSourceAuthorityInTx(ctx, tx, request.SourceAuthority); err != nil {
			return err
		}
		// Serialize identical webhook intents before checking deployments so
		// concurrent delivery IDs cannot create orphan operations.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, request.Deployment.TriggerKey); err != nil {
			return fmt.Errorf("github intake trigger lock: %w", err)
		}
		var existingDeploymentID, existingOperationID string
		err := tx.QueryRow(ctx,
			`SELECT id, COALESCE(operation_id, '') FROM deployments WHERE trigger_key = $1`,
			request.Deployment.TriggerKey,
		).Scan(&existingDeploymentID, &existingOperationID)
		if err == nil {
			result = StagedWorkCommitResult{
				OperationID:  existingOperationID,
				DeploymentID: existingDeploymentID,
				Created:      false,
			}
			_ = tx.QueryRow(ctx,
				`SELECT id FROM staged_work_outbox WHERE dedupe_key = $1`,
				githubStagedWorkDedupeKey(request.Deployment.TriggerKey),
			).Scan(&result.OutboxID)
			if strings.TrimSpace(request.PreviewID) != "" {
				if err := attachGitHubPreviewDeploymentInTx(ctx, tx, request.PreviewID, request.PreviewURL, request.Deployment, existingDeploymentID, now); err != nil {
					return err
				}
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("github intake dedupe lookup: %w", err)
		}

		expiresAt := now.Add(24 * time.Hour)
		if _, err := tx.Exec(ctx,
			`INSERT INTO operations (id, kind, state, expires_at, created_at, updated_at)
			 VALUES ($1,$2,'QUEUED',$3,$4,$4)`,
			string(request.Operation.ID), request.Operation.Kind, expiresAt, now,
		); err != nil {
			return fmt.Errorf("github intake operation insert: %w", err)
		}
		deployment := request.Deployment
		if _, err := tx.Exec(ctx,
			`INSERT INTO deployments (
			    id, project_id, service_id, environment_id, action, source_type, repo, ref, sha,
			    root_directory, dockerfile_path, builder, image_ref, image_digest, state, active,
			    operation_id, trigger_key, rollback_source_deployment_id, expected_active_deployment_id,
			    created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'queued',FALSE,
			    $15,$16,$17,$18,$19,$19)`,
			deployment.ID, deployment.ProjectID, deployment.ServiceID, deployment.EnvironmentID,
			string(deployment.Action), deployment.SourceType, deployment.Repo, deployment.Ref,
			deployment.SHA, deployment.RootDirectory, deployment.DockerfilePath, deployment.Builder,
			deployment.ImageRef, deployment.ImageDigest, string(request.Operation.ID), deployment.TriggerKey,
			nullableString(deployment.RollbackSourceID), nullableString(deployment.ExpectedActiveID), now,
		); err != nil {
			return fmt.Errorf("github intake deployment insert: %w", err)
		}
		if _, err := bindServiceManagedGeneratedRoutesInTx(
			ctx, tx, deployment.ID, deployment.ProjectID, deployment.EnvironmentID,
			deployment.ServiceID, now,
		); err != nil {
			return fmt.Errorf("github intake service-managed route bind: %w", err)
		}
		if strings.TrimSpace(request.PreviewID) != "" {
			if err := attachGitHubPreviewDeploymentInTx(ctx, tx, request.PreviewID, request.PreviewURL, deployment, deployment.ID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO github_commit_statuses (
			    id, deployment_id, provider_host, installation_id, repository_id,
			    repo_full_name, commit_sha, state, context, target_url, description,
			    delivery_state, attempt, max_attempts, available_at, created_at, updated_at)
			 VALUES ('ghs_' || md5($1 || ':pending:' || $2),$1,$3,$4,$5,$6,$7,$8,$2,$9,$10,
			    'pending',0,8,$11,$11,$11)`,
			deployment.ID, commitStatus.Context, commitStatus.ProviderHost,
			commitStatus.InstallationID, commitStatus.RepositoryID,
			commitStatus.RepoFullName, commitStatus.SHA, commitStatus.State,
			commitStatus.TargetURL, commitStatus.Description, now,
		); err != nil {
			return fmt.Errorf("github intake pending status insert: %w", err)
		}
		message := firstNonEmpty(strings.TrimSpace(request.InitialMessage), "GitHub build intent durably accepted")
		if _, err := tx.Exec(ctx,
			`INSERT INTO operation_events (id, operation_id, seq, state, message, created_at)
			 VALUES ($1,$2,1,'QUEUED',$3,$4)`,
			fmt.Sprintf("%s-1", request.Operation.ID), string(request.Operation.ID), message, now,
		); err != nil {
			return fmt.Errorf("github intake operation event: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO deployment_events
			    (id, deployment_id, seq, event_type, state, message, data, created_at)
			 VALUES ($1,$2,1,'state','queued',$3,'{}'::jsonb,$4)`,
			s.newID("dev"), deployment.ID, message, now,
		); err != nil {
			return fmt.Errorf("github intake deployment event: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO staged_work_outbox
			    (id, dedupe_key, stage, operation_id, deployment_id, queue, partition_key,
			     priority, available_at, payload, state, max_attempts, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,'pending',$11,$12,$12)`,
			outboxID, githubStagedWorkDedupeKey(deployment.TriggerKey), StagedWorkGitHubBuildIntake,
			string(request.Operation.ID), deployment.ID, string(request.Enqueue.Queue),
			request.Enqueue.PartitionKey, request.Enqueue.Priority, request.Enqueue.AvailableAt.UTC(),
			string(request.Enqueue.Payload), defaultDeployOutboxMaxAttempts, now,
		); err != nil {
			return fmt.Errorf("github intake outbox insert: %w", err)
		}
		result = StagedWorkCommitResult{
			OperationID:  string(request.Operation.ID),
			DeploymentID: deployment.ID,
			OutboxID:     outboxID,
			Created:      true,
		}
		return nil
	})
	return result, err
}

func attachGitHubPreviewDeploymentInTx(
	ctx context.Context,
	tx *pg.Tx,
	previewID string,
	previewURL string,
	deployment DeploymentRecord,
	deploymentID string,
	now time.Time,
) error {
	var attachedDeploymentID, attachedState string
	err := tx.QueryRow(ctx, `SELECT COALESCE(deployment_id,''),state
		FROM github_pull_request_previews
		WHERE id=$1 AND project_id=$2 AND service_id=$3 AND environment_id=$4
		  AND preview_url=$5 FOR UPDATE`,
		strings.TrimSpace(previewID), deployment.ProjectID, deployment.ServiceID,
		deployment.EnvironmentID, strings.TrimSpace(previewURL),
	).Scan(&attachedDeploymentID, &attachedState)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("github intake preview scope changed before deployment attach")
		}
		return fmt.Errorf("github intake lock preview deployment: %w", err)
	}
	if attachedDeploymentID == deploymentID &&
		(attachedState == "deploying" || attachedState == "ready" || attachedState == "failed") {
		return nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE github_pull_request_previews
		   SET deployment_id=$1,state='deploying',updated_at=$2,resource_version=resource_version+1
		 WHERE id=$3 AND project_id=$4 AND service_id=$5 AND environment_id=$6
		   AND preview_url=$7
		   AND state IN ('creating','refreshing','deploying')`,
		deploymentID, now, strings.TrimSpace(previewID), deployment.ProjectID,
		deployment.ServiceID, deployment.EnvironmentID, strings.TrimSpace(previewURL))
	if err != nil {
		return fmt.Errorf("github intake attach preview deployment: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("github intake preview scope changed before deployment attach")
	}
	return enqueuePreviewCommentInTx(ctx, tx, previewID, "deploying", deploymentID, now)
}

func validateGitHubBuildIntakeCommit(request GitHubBuildIntakeCommit) error {
	op := request.Operation
	op.State = orailtypes.OperationQueued
	if err := op.Validate(); err != nil {
		return fmt.Errorf("github intake: invalid operation: %w", err)
	}
	if request.Deployment.ID == "" || request.Deployment.OperationID != string(op.ID) ||
		request.Deployment.TriggerKey == "" || !deploy.ValidAction(request.Deployment.Action) {
		return errors.New("github intake: canonical deployment identity/action/trigger is invalid")
	}
	if (strings.TrimSpace(request.PreviewID) == "") != (strings.TrimSpace(request.PreviewURL) == "") {
		return errors.New("github intake: preview id and URL must be supplied together")
	}
	source := request.SourceAuthority
	if strings.TrimSpace(source.WorkspaceID) == "" ||
		strings.TrimSpace(source.ProjectID) == "" ||
		strings.TrimSpace(source.ServiceID) == "" ||
		source.InstallationID <= 0 ||
		source.RepositoryID <= 0 ||
		strings.TrimSpace(source.Repository) == "" ||
		strings.TrimSpace(source.CloneURL) == "" ||
		strings.TrimSpace(source.Branch) == "" ||
		source.InstallationGrantResourceVersion <= 0 ||
		source.RepositoryGrantResourceVersion <= 0 ||
		source.LinkResourceVersion <= 0 ||
		source.ProjectID != request.Deployment.ProjectID ||
		source.ServiceID != request.Deployment.ServiceID {
		return errors.New("github intake: exact canonical source authority is required")
	}
	if request.Enqueue.Queue != queue.Build || request.Enqueue.ParentOperationID != string(op.ID) ||
		len(request.Enqueue.Payload) == 0 {
		return errors.New("github intake: valid build enqueue request is required")
	}
	return nil
}

func githubBuildCommitStatus(request GitHubBuildIntakeCommit) (github.CommitStatusMetadata, error) {
	cloneURL, err := url.Parse(strings.TrimSpace(request.SourceAuthority.CloneURL))
	if err != nil || cloneURL.Scheme != "https" || cloneURL.Hostname() == "" || cloneURL.User != nil ||
		cloneURL.RawQuery != "" || cloneURL.Fragment != "" {
		return github.CommitStatusMetadata{}, errors.New("github intake: canonical https clone URL is required")
	}
	targetURL := strings.TrimSuffix(cloneURL.String(), ".git") + "/commit/" + strings.TrimSpace(request.Deployment.SHA)
	if strings.TrimSpace(request.PreviewID) != "" {
		previewURL, previewErr := url.Parse(strings.TrimSpace(request.PreviewURL))
		if previewErr != nil || previewURL.Scheme != "https" || previewURL.Hostname() == "" ||
			previewURL.User != nil || previewURL.RawQuery != "" || previewURL.Fragment != "" {
			return github.CommitStatusMetadata{}, errors.New("github intake: canonical HTTPS preview URL is required")
		}
		targetURL = previewURL.String()
	}
	metadata, err := github.NewCommitStatusMetadata(github.CommitStatusInput{
		ProviderHost:   cloneURL.Hostname(),
		InstallationID: request.SourceAuthority.InstallationID,
		RepositoryID:   request.SourceAuthority.RepositoryID,
		RepoFullName:   request.SourceAuthority.Repository,
		SHA:            request.Deployment.SHA,
		State:          string(github.StatusPending),
		TargetURL:      targetURL,
		Description:    "Deployment queued",
	})
	if err != nil {
		return github.CommitStatusMetadata{}, fmt.Errorf("github intake: pending commit status: %w", err)
	}
	return metadata, nil
}

var ErrGitHubBuildSourceAuthorityRevoked = errors.New("github intake: source authority was revoked or changed")

func revalidateGitHubBuildSourceAuthorityInTx(
	ctx context.Context,
	tx *pg.Tx,
	source GitHubBuildSourceAuthority,
) error {
	// Cross-path lock order is installation fence first, then canonical
	// source/product rows. Human source-link mutation uses the same helper and
	// must never hold a service row while waiting for this fence.
	if err := github.LockInstallationInTx(ctx, tx, source.InstallationID); err != nil {
		return fmt.Errorf("github intake installation lock: %w", err)
	}
	var installationResourceVersion, repositoryResourceVersion, linkResourceVersion int64
	err := tx.QueryRow(ctx, `
		SELECT installation.resource_version,
		       repository.resource_version,
		       link.resource_version
		  FROM service_github_links link
		  JOIN github_workspace_repository_grants repository
		    ON repository.workspace_id=link.workspace_id
		   AND repository.installation_id=link.installation_id
		   AND repository.repository_id=link.repository_id
		   AND repository.repository_full_name=link.repository_full_name
		  JOIN github_workspace_installation_grants installation
		    ON installation.workspace_id=repository.workspace_id
		   AND installation.installation_id=repository.installation_id
		 WHERE link.workspace_id=$1
		   AND link.project_id=$2
		   AND link.service_id=$3
		   AND link.installation_id=$4
		   AND link.repository_id=$5
		   AND link.repository_full_name=$6
		   AND link.branch=$7
		   AND repository.clone_url=$8
		   AND installation.resource_version=$9
		   AND repository.resource_version=$10
		   AND link.resource_version=$11
		   AND link.auto_deploy=TRUE
		 FOR SHARE OF link, repository, installation`,
		strings.TrimSpace(source.WorkspaceID),
		strings.TrimSpace(source.ProjectID),
		strings.TrimSpace(source.ServiceID),
		source.InstallationID,
		source.RepositoryID,
		strings.ToLower(strings.TrimSpace(source.Repository)),
		strings.TrimSpace(source.Branch),
		strings.TrimSpace(source.CloneURL),
		source.InstallationGrantResourceVersion,
		source.RepositoryGrantResourceVersion,
		source.LinkResourceVersion,
	).Scan(
		&installationResourceVersion,
		&repositoryResourceVersion,
		&linkResourceVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGitHubBuildSourceAuthorityRevoked
	}
	if err != nil {
		return fmt.Errorf("github intake source authority revalidation: %w", err)
	}
	if installationResourceVersion != source.InstallationGrantResourceVersion ||
		repositoryResourceVersion != source.RepositoryGrantResourceVersion ||
		linkResourceVersion != source.LinkResourceVersion {
		return ErrGitHubBuildSourceAuthorityRevoked
	}
	return nil
}

func githubStagedWorkDedupeKey(triggerKey string) string {
	return "github-build-intake:" + triggerKey
}

func buildHandoffDedupeKey(deploymentID string) string {
	return "build-deploy-handoff:" + strings.TrimSpace(deploymentID)
}

func (s *PostgresStagedWorkOutbox) GetBuildDeployHandoff(ctx context.Context, deploymentID string) (CommittedBuildDeployHandoff, bool, error) {
	if err := s.ready(); err != nil {
		return CommittedBuildDeployHandoff{}, false, err
	}
	var result CommittedBuildDeployHandoff
	err := s.Pool.QueryRow(ctx,
		`SELECT o.operation_id,o.deployment_id,o.id,d.image_ref,d.image_digest
		   FROM staged_work_outbox o
		   JOIN deployments d ON d.id=o.deployment_id
		  WHERE o.dedupe_key=$1
		    AND o.stage=$2
		    AND o.state IN ('pending','delivering','delivered')`,
		buildHandoffDedupeKey(deploymentID), StagedWorkBuildDeployHandoff,
	).Scan(&result.OperationID, &result.DeploymentID, &result.OutboxID, &result.ImageRef, &result.ImageDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return CommittedBuildDeployHandoff{}, false, nil
	}
	if err != nil {
		return CommittedBuildDeployHandoff{}, false, fmt.Errorf("build handoff recovery lookup: %w", err)
	}
	return result, true, nil
}

func (s *PostgresStagedWorkOutbox) CommitBuildDeployHandoff(ctx context.Context, request BuildDeployHandoffCommit) (StagedWorkCommitResult, error) {
	if err := s.ready(); err != nil {
		return StagedWorkCommitResult{}, err
	}
	if request.OperationID == "" || request.DeploymentID == "" ||
		request.ImageRef == "" || request.ImageDigest == "" ||
		request.Enqueue.Queue != queue.Deploy ||
		request.Enqueue.ParentOperationID != request.OperationID ||
		len(request.Enqueue.Payload) == 0 {
		return StagedWorkCommitResult{}, errors.New("build handoff: operation, deployment, build result, and deploy enqueue are required")
	}
	now := request.Now.UTC()
	if now.IsZero() {
		now = s.now()
	}
	dedupeKey := buildHandoffDedupeKey(request.DeploymentID)
	outboxID := s.newID("wox")
	created := false
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var state, operationID, sourceSHA, builder, imageRef, imageDigest string
		if err := tx.QueryRow(ctx,
			`SELECT state, COALESCE(operation_id, ''), sha, builder, image_ref, image_digest
			   FROM deployments WHERE id = $1 FOR UPDATE`,
			request.DeploymentID,
		).Scan(&state, &operationID, &sourceSHA, &builder, &imageRef, &imageDigest); err != nil {
			return fmt.Errorf("build handoff deployment lock: %w", err)
		}
		if operationID != request.OperationID {
			return fmt.Errorf("build handoff: deployment operation mismatch: operation_id=%s", operationID)
		}
		var existingOutbox bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM staged_work_outbox WHERE dedupe_key=$1)`,
			dedupeKey,
		).Scan(&existingOutbox); err != nil {
			return fmt.Errorf("build handoff existing intent lookup: %w", err)
		}
		if state != string(deploy.DeploymentBuilding) && !existingOutbox {
			return fmt.Errorf("build handoff: deployment is in state %s without a prior durable handoff", state)
		}
		if imageRef == "" && imageDigest == "" {
			if _, err := tx.Exec(ctx,
				`UPDATE deployments
				    SET sha = $2, builder = $3, image_ref = $4, image_digest = $5,
				        build_completed_at = COALESCE(build_completed_at,$6),
				        updated_at = $6, resource_version = resource_version + 1
				  WHERE id = $1 AND state = 'building' AND image_ref = '' AND image_digest = ''`,
				request.DeploymentID, request.SourceSHA, request.Builder, request.ImageRef, request.ImageDigest, now,
			); err != nil {
				return fmt.Errorf("build handoff result update: %w", err)
			}
		} else if sourceSHA != request.SourceSHA || builder != request.Builder ||
			imageRef != request.ImageRef || imageDigest != request.ImageDigest {
			return errors.New("build handoff: immutable build result conflicts with prior committed result")
		}

		tag, err := tx.Exec(ctx,
			`INSERT INTO staged_work_outbox
			    (id, dedupe_key, stage, operation_id, deployment_id, queue, partition_key,
			     priority, available_at, payload, state, max_attempts, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,'pending',$11,$12,$12)
			 ON CONFLICT (dedupe_key) DO NOTHING`,
			outboxID, dedupeKey, StagedWorkBuildDeployHandoff, request.OperationID,
			request.DeploymentID, string(request.Enqueue.Queue), request.Enqueue.PartitionKey,
			request.Enqueue.Priority, request.Enqueue.AvailableAt.UTC(), string(request.Enqueue.Payload),
			defaultDeployOutboxMaxAttempts, now,
		)
		if err != nil {
			return fmt.Errorf("build handoff outbox insert: %w", err)
		}
		created = tag.RowsAffected() == 1
		var storedID, storedOperationID, storedDeploymentID, storedQueue string
		var payloadEqual bool
		if err := tx.QueryRow(ctx,
			`SELECT id, operation_id, deployment_id, queue, payload = $2::jsonb
			   FROM staged_work_outbox WHERE dedupe_key = $1`,
			dedupeKey, string(request.Enqueue.Payload),
		).Scan(&storedID, &storedOperationID, &storedDeploymentID, &storedQueue, &payloadEqual); err != nil {
			return fmt.Errorf("build handoff outbox verify: %w", err)
		}
		outboxID = storedID
		if storedOperationID != request.OperationID || storedDeploymentID != request.DeploymentID ||
			storedQueue != string(queue.Deploy) || !payloadEqual {
			return errors.New("build handoff: existing outbox intent conflicts with retry")
		}
		if created {
			message := firstNonEmpty(strings.TrimSpace(request.Message), "build result and deploy handoff durably committed")
			if err := appendOperationEventTx(ctx, tx, orailtypes.ID(request.OperationID), orailtypes.OperationRunning, message, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return StagedWorkCommitResult{}, err
	}
	return StagedWorkCommitResult{
		OperationID:  request.OperationID,
		DeploymentID: request.DeploymentID,
		OutboxID:     outboxID,
		Created:      created,
	}, nil
}

type stagedWorkClaim struct {
	ID, LeaseOwner, Stage, OperationID, DeploymentID, PartitionKey string
	Queue                                                          queue.Name
	Priority, Attempt, MaxAttempts                                 int
	AvailableAt                                                    time.Time
	Payload                                                        []byte
}

func (s *PostgresStagedWorkOutbox) DispatchStagedWork(ctx context.Context, limit int) (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 1
	}
	delivered := 0
	for delivered < limit {
		claim, err := s.claim(ctx)
		if errors.Is(err, ErrDeployOutboxNoWork) {
			return delivered, nil
		}
		if err != nil {
			return delivered, err
		}
		if err := s.deliver(ctx, claim); err != nil {
			_ = s.recordFailure(ctx, claim, err)
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

func (s *PostgresStagedWorkOutbox) claim(ctx context.Context) (stagedWorkClaim, error) {
	now := s.now()
	lease := s.LeaseTime
	if lease <= 0 {
		lease = defaultDeployOutboxLease
	}
	owner := strings.TrimSpace(s.WorkerID) + ":" + randomOutboxID("lease")
	var claim stagedWorkClaim
	var queueName string
	err := s.Pool.QueryRow(ctx,
		`WITH candidate AS (
		    SELECT id FROM staged_work_outbox
		     WHERE attempt < max_attempts AND available_at <= $1
		       AND (state = 'pending' OR (state = 'delivering' AND lease_expires_at < $1))
		     ORDER BY available_at, created_at
		     FOR UPDATE SKIP LOCKED LIMIT 1
		 )
		 UPDATE staged_work_outbox o
		    SET state='delivering', attempt=o.attempt+1, lease_owner=$2,
		        lease_expires_at=$3, updated_at=$1
		   FROM candidate WHERE o.id=candidate.id
		 RETURNING o.id,o.lease_owner,o.stage,o.operation_id,o.deployment_id,o.queue,
		           o.partition_key,o.priority,o.available_at,o.payload,o.attempt,o.max_attempts`,
		now, owner, now.Add(lease),
	).Scan(&claim.ID, &claim.LeaseOwner, &claim.Stage, &claim.OperationID,
		&claim.DeploymentID, &queueName, &claim.PartitionKey, &claim.Priority,
		&claim.AvailableAt, &claim.Payload, &claim.Attempt, &claim.MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return stagedWorkClaim{}, ErrDeployOutboxNoWork
	}
	if err != nil {
		return stagedWorkClaim{}, fmt.Errorf("staged work claim: %w", err)
	}
	claim.Queue = queue.Name(queueName)
	return claim, nil
}

func (s *PostgresStagedWorkOutbox) deliver(ctx context.Context, claim stagedWorkClaim) error {
	now := s.now()
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		jobID := randomOutboxID("job")
		if _, err := tx.Exec(ctx,
			`INSERT INTO jobs
			    (id,queue,state,partition_key,parent_operation_id,priority,available_at,payload,outbox_id,created_at,updated_at)
			 VALUES ($1,$2,'pending',$3,$4,$5,$6,$7::jsonb,$8,$9,$9)
			 ON CONFLICT (outbox_id) WHERE outbox_id IS NOT NULL DO NOTHING`,
			jobID, string(claim.Queue), claim.PartitionKey, claim.OperationID, claim.Priority,
			claim.AvailableAt.UTC(), string(claim.Payload), claim.ID, now,
		); err != nil {
			return fmt.Errorf("staged work job insert: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM jobs WHERE outbox_id=$1`, claim.ID).Scan(&jobID); err != nil {
			return fmt.Errorf("staged work job lookup: %w", err)
		}
		tag, err := tx.Exec(ctx,
			`UPDATE staged_work_outbox
			    SET state='delivered',job_id=$1,delivered_at=$2,lease_owner=NULL,
			        lease_expires_at=NULL,last_error='',updated_at=$2
			  WHERE id=$3 AND state='delivering' AND lease_owner=$4`,
			jobID, now, claim.ID, claim.LeaseOwner,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDeployOutboxLeaseLost
		}
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM operations WHERE id=$1`, claim.OperationID).Scan(&state); err != nil {
			return err
		}
		return appendOperationEventTx(ctx, tx, orailtypes.ID(claim.OperationID), orailtypes.OperationState(state),
			fmt.Sprintf("%s work delivered: stage=%s job_id=%s outbox_id=%s", claim.Queue, claim.Stage, jobID, claim.ID), now)
	})
}

func (s *PostgresStagedWorkOutbox) recordFailure(ctx context.Context, claim stagedWorkClaim, deliveryErr error) error {
	now := s.now()
	message := boundedOutboxError(deliveryErr)
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		state := "pending"
		availableAt := now.Add(outboxRetryDelay(claim.Attempt))
		terminal := claim.Attempt >= claim.MaxAttempts
		if terminal {
			state = "failed"
			availableAt = now
		}
		tag, err := tx.Exec(ctx,
			`UPDATE staged_work_outbox
			    SET state=$1,available_at=$2,lease_owner=NULL,lease_expires_at=NULL,
			        last_error=$3,updated_at=$4
			  WHERE id=$5 AND state='delivering' AND lease_owner=$6`,
			state, availableAt, message, now, claim.ID, claim.LeaseOwner,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDeployOutboxLeaseLost
		}
		eventState := orailtypes.OperationRunning
		if terminal {
			eventState = orailtypes.OperationFailed
			if _, err := tx.Exec(ctx,
				`UPDATE operations SET state='FAILED',code=$1,reason=$2,finished_at=$3,
				        updated_at=$3,resource_version=resource_version+1
				  WHERE id=$4 AND state NOT IN ('SUCCEEDED','FAILED','CANCELED')`,
				string(failurecodes.DeployJobEnqueueFailed), message, now, claim.OperationID,
			); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE deployments SET state='failed',failure_code=$1,failure_message=$2,
				        completed_at=$3,updated_at=$3,resource_version=resource_version+1
				  WHERE id=$4 AND state IN ('queued','building')`,
				string(failurecodes.DeployJobEnqueueFailed), message, now, claim.DeploymentID,
			); err != nil {
				return err
			}
		} else {
			var current string
			if err := tx.QueryRow(ctx, `SELECT state FROM operations WHERE id=$1`, claim.OperationID).Scan(&current); err != nil {
				return err
			}
			eventState = orailtypes.OperationState(current)
		}
		return appendOperationEventTx(ctx, tx, orailtypes.ID(claim.OperationID), eventState,
			fmt.Sprintf("staged work delivery attempt %d/%d failed: stage=%s %s", claim.Attempt, claim.MaxAttempts, claim.Stage, message), now)
	})
}

func RunStagedWorkDispatcher(ctx context.Context, dispatcher StagedWorkDispatcher, poll time.Duration, report func(error)) {
	if dispatcher == nil {
		return
	}
	if poll <= 0 {
		poll = defaultDeployOutboxPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		delivered, err := dispatcher.DispatchStagedWork(ctx, 32)
		if err != nil && ctx.Err() == nil && report != nil {
			report(err)
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil && delivered == 32 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
