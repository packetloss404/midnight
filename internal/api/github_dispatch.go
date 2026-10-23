// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/webhook"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// GitHubWebhookDispatcher promotes an accepted GitHub webhook delivery
// into one or more source-build operations via the same queue path the
// /services/{id}/deployments REST handler uses. It is wired by the
// router when both ServiceStore implements ServiceLookupBySpec and a
// queue is configured.
//
// The dispatcher implements webhook.Dispatcher and is wired as
// DataStack.WebhookDispatcher.
type GitHubWebhookDispatcher struct {
	router           github.Router
	serviceStore     ServiceStore
	environmentStore EnvironmentStore
	operationStore   OperationStore
	deploymentStore  DeploymentStore
	queueStore       queue.Store
	stagedWork       GitHubBuildIntakeStore
	previewLifecycle GitHubPreviewLifecycle
	emitter          *auditEmitter
	clock            func() time.Time
	// waitForCI is retained for the legacy compatibility seam. Canonical
	// production links reject wait_for_ci until a durable check-event
	// reconciler exists; a nil gate must therefore fail closed if stale or
	// corrupt data nevertheless reaches dispatch.
	waitForCI WaitForCIGate
}

// WaitForCIGate is the optional boundary the dispatcher consults
// before enqueueing a deploy. Returns (allowed=true, _) to proceed,
// (false, reason) to defer/abort.
type WaitForCIGate interface {
	Evaluate(ctx context.Context, intent github.DeployIntent) (allowed bool, reason string, err error)
}

// NewGitHubWebhookDispatcher constructs the dispatcher. A nil gate keeps the
// unadmitted Wait-for-CI feature fail closed.
func NewGitHubWebhookDispatcher(serviceStore ServiceStore, operationStore OperationStore, queueStore queue.Store, emitter *auditEmitter, clock func() time.Time, gate WaitForCIGate, environmentStores ...EnvironmentStore) *GitHubWebhookDispatcher {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	d := &GitHubWebhookDispatcher{
		serviceStore:    serviceStore,
		operationStore:  operationStore,
		deploymentStore: NewMemoryDeploymentStore(clock),
		queueStore:      queueStore,
		emitter:         emitter,
		clock:           clock,
		waitForCI:       gate,
	}
	if len(environmentStores) > 0 {
		d.environmentStore = environmentStores[0]
	}
	d.router = github.Router{Lookup: &serviceStoreLookup{store: serviceStore}}
	return d
}

// WithDeploymentStore replaces the process-local constructor default with the
// canonical deployment store wired by the owning router. It preserves the
// original constructor contract for direct callers while ensuring production
// webhook dispatch uses the same durable store as REST and GraphQL intake.
func (d *GitHubWebhookDispatcher) WithDeploymentStore(store DeploymentStore) *GitHubWebhookDispatcher {
	if d != nil && store != nil {
		d.deploymentStore = store
	}
	return d
}

// WithStagedWorkOutbox enables the crash-safe production intake transaction.
// Memory-mode callers intentionally retain the direct queue compatibility path.
func (d *GitHubWebhookDispatcher) WithStagedWorkOutbox(store GitHubBuildIntakeStore) *GitHubWebhookDispatcher {
	if d != nil {
		d.stagedWork = store
	}
	return d
}

// WithPreviewLifecycle enables signed pull_request routing and durable
// service-focused environment lifecycle. Nil keeps the capability closed.
func (d *GitHubWebhookDispatcher) WithPreviewLifecycle(lifecycle GitHubPreviewLifecycle) *GitHubWebhookDispatcher {
	if d != nil {
		d.previewLifecycle = lifecycle
	}
	return d
}

// WithServiceLookup installs canonical exact-source lookup truth. Production
// uses the relational SourceLinkStore adapter; memory fixtures may keep the
// constructor's exact-ID scanning adapter.
func (d *GitHubWebhookDispatcher) WithServiceLookup(lookup github.ServiceLookup) *GitHubWebhookDispatcher {
	if d != nil && lookup != nil {
		d.router.Lookup = lookup
	}
	return d
}

// Dispatch implements webhook.Dispatcher.
func (d *GitHubWebhookDispatcher) Dispatch(ctx context.Context, del webhook.ReceivedDelivery) error {
	// The synchronous dispatcher is retained for memory-mode compatibility,
	// where no durable receipt timestamp or temporal lookup authority exists.
	// Production persist-before-ack workers call Plan, which requires both.
	plan, err := d.plan(ctx, del, false)
	if err != nil {
		return err
	}
	for _, target := range plan.Targets {
		if _, err := d.DispatchTarget(ctx, del, target); err != nil {
			var ignored *webhook.IgnoredTargetError
			if errors.As(err, &ignored) {
				continue
			}
			return err
		}
	}
	return nil
}

// Plan parses one retained delivery and snapshots every exact service target.
// Durable workers persist this plan before attempting any target, so retries
// never discover a service linked after the provider delivery was received.
func (d *GitHubWebhookDispatcher) Plan(ctx context.Context, del webhook.ReceivedDelivery) (webhook.DispatchPlan, error) {
	return d.plan(ctx, del, true)
}

func (d *GitHubWebhookDispatcher) plan(
	ctx context.Context,
	del webhook.ReceivedDelivery,
	requireTemporalAuthority bool,
) (webhook.DispatchPlan, error) {
	if del.Provider != webhook.ProviderGitHub {
		return webhook.DispatchPlan{IgnoredReason: "provider_not_routed"}, nil
	}
	eventType, body, err := readGitHubEvent(del)
	if err != nil {
		return webhook.DispatchPlan{}, &webhook.PermanentError{
			Code: "WEBHOOK_GITHUB_PAYLOAD_INVALID",
			Err:  err,
		}
	}
	var decision github.RouteDecision
	if eventType == github.EventPullRequest && d.previewLifecycle != nil {
		action, actionErr := github.PullRequestEventAction(body)
		if actionErr != nil {
			return webhook.DispatchPlan{}, &webhook.PermanentError{Code: "WEBHOOK_GITHUB_PAYLOAD_INVALID", Err: actionErr}
		}
		if !github.IsPullRequestLifecycleAction(action) {
			decision = github.RouteDecision{Skipped: true, SkipReason: "event_not_routed:pull_request:" + string(action)}
		} else {
			event, parseErr := github.ParsePullRequestEvent(body)
			if parseErr != nil {
				return webhook.DispatchPlan{}, &webhook.PermanentError{Code: "WEBHOOK_GITHUB_PAYLOAD_INVALID", Err: parseErr}
			}
			intents, planErr := d.previewLifecycle.PlanPullRequest(ctx, event, del.ReceivedAt, del.DeliveryID)
			if planErr != nil {
				return webhook.DispatchPlan{}, planErr
			}
			if len(intents) == 0 {
				decision = github.RouteDecision{Skipped: true, SkipReason: "no_linked_preview_service"}
			} else {
				decision = github.RouteDecision{Intents: intents}
			}
		}
	} else if requireTemporalAuthority {
		decision, err = d.router.RouteAt(ctx, eventType, body, del.ReceivedAt)
	} else {
		decision, err = d.router.Route(ctx, eventType, body)
	}
	if err != nil {
		if errors.Is(err, github.ErrPushEventInvalid) {
			return webhook.DispatchPlan{}, &webhook.PermanentError{
				Code: "WEBHOOK_GITHUB_PAYLOAD_INVALID",
				Err:  err,
			}
		}
		return webhook.DispatchPlan{}, err
	}
	if decision.Skipped {
		// Audit the no-op so operators can confirm webhook delivery is
		// being received but skipped for a known reason.
		if d.emitter != nil {
			d.emitter.emit(ctx, audit.Row{
				ResourceKind: "webhook_delivery",
				ResourceID:   del.EndpointID,
				Action:       "webhook.github.skipped",
				ReasonCode:   decision.SkipReason,
				Attrs: map[string]any{
					"delivery_id": del.DeliveryID,
					"event_type":  string(eventType),
					"reason":      decision.SkipReason,
				},
			})
		}
		return webhook.DispatchPlan{IgnoredReason: decision.SkipReason}, nil
	}
	plan := webhook.DispatchPlan{Targets: make([]webhook.DispatchTarget, 0, len(decision.Intents))}
	for _, intent := range decision.Intents {
		intent.Repo = strings.ToLower(strings.TrimSpace(intent.Repo))
		payload, err := json.Marshal(intent)
		if err != nil {
			return webhook.DispatchPlan{}, fmt.Errorf("encode GitHub target intent: %w", err)
		}
		plan.Targets = append(plan.Targets, webhook.DispatchTarget{
			TargetKey:                        webhookTargetKey(intent.IdempotencyKey),
			WorkspaceID:                      intent.WorkspaceID,
			ProjectID:                        intent.ProjectID,
			ServiceID:                        intent.ServiceID,
			InstallationID:                   intent.InstallationID,
			InstallationGrantResourceVersion: intent.InstallationGrantResourceVersion,
			RepositoryID:                     intent.RepositoryID,
			Repository:                       intent.Repo,
			RepositoryGrantResourceVersion:   intent.RepositoryGrantResourceVersion,
			LinkResourceVersion:              intent.LinkVersion,
			InstallationGrantCreatedAt:       intent.InstallationGrantCreatedAt,
			InstallationGrantUpdatedAt:       intent.InstallationGrantUpdatedAt,
			RepositoryGrantCreatedAt:         intent.RepositoryGrantCreatedAt,
			RepositoryGrantUpdatedAt:         intent.RepositoryGrantUpdatedAt,
			RepositoryVerifiedAt:             intent.RepositoryAccessVerifiedAt,
			LinkRepositoryVerifiedAt:         intent.LinkRepositoryAccessVerifiedAt,
			LinkCreatedAt:                    intent.LinkCreatedAt,
			LinkUpdatedAt:                    intent.LinkUpdatedAt,
			Branch:                           strings.TrimPrefix(intent.Ref, "refs/heads/"),
			Ref:                              intent.Ref,
			CommitSHA:                        intent.SHA,
			CloneURL:                         intent.CloneURL,
			RootDirectory:                    intent.RootDirectory,
			ConfigPath:                       intent.ConfigPath,
			TriggerReason:                    intent.TriggerReason,
			Payload:                          payload,
		})
	}
	return plan, nil
}

// DispatchTarget executes one immutable plan entry through canonical staged
// intake. Its payload is validated against the queryable target snapshot
// before any downstream write.
func (d *GitHubWebhookDispatcher) DispatchTarget(
	ctx context.Context,
	_ webhook.ReceivedDelivery,
	target webhook.DispatchTarget,
) (webhook.TargetResult, error) {
	var intent github.DeployIntent
	if err := json.Unmarshal(target.Payload, &intent); err != nil {
		return webhook.TargetResult{}, &webhook.PermanentError{
			Code: "WEBHOOK_TARGET_PAYLOAD_INVALID",
			Err:  fmt.Errorf("decode GitHub target intent: %w", err),
		}
	}
	if webhookTargetKey(intent.IdempotencyKey) != target.TargetKey ||
		intent.WorkspaceID != target.WorkspaceID ||
		intent.ProjectID != target.ProjectID ||
		intent.ServiceID != target.ServiceID ||
		intent.InstallationID != target.InstallationID ||
		intent.InstallationGrantResourceVersion != target.InstallationGrantResourceVersion ||
		intent.RepositoryID != target.RepositoryID ||
		!strings.EqualFold(intent.Repo, target.Repository) ||
		intent.RepositoryGrantResourceVersion != target.RepositoryGrantResourceVersion ||
		intent.LinkVersion != target.LinkResourceVersion ||
		intent.SHA != target.CommitSHA ||
		strings.TrimPrefix(intent.Ref, "refs/heads/") != target.Branch {
		return webhook.TargetResult{}, &webhook.PermanentError{
			Code: "WEBHOOK_TARGET_SNAPSHOT_MISMATCH",
			Err:  errors.New("GitHub target payload does not match durable identity snapshot"),
		}
	}
	if intent.Preview != nil {
		if d.previewLifecycle == nil ||
			(intent.Preview.Action != previewActionUpsert && intent.Preview.Action != previewActionDelete) ||
			intent.Preview.Number <= 0 || intent.Preview.HeadSHA != intent.SHA ||
			intent.Preview.BaseBranch != target.Branch || intent.Preview.FromFork != intent.Untrusted {
			return webhook.TargetResult{}, &webhook.PermanentError{
				Code: string(failurecodes.WebhookTargetInvalid),
				Err:  errors.New("GitHub preview target payload does not match durable source snapshot"),
			}
		}
	}
	if intent.WaitForCI {
		if d.waitForCI == nil {
			return webhook.TargetResult{}, &webhook.PermanentError{
				Code: "WEBHOOK_TARGET_INVALID",
				Err:  errors.New("wait-for-CI requires a durable check-event gate"),
			}
		}
		allowed, reason, err := d.waitForCI.Evaluate(ctx, intent)
		if err != nil {
			return webhook.TargetResult{}, fmt.Errorf("service %s: wait-for-ci eval failed: %w", intent.ServiceID, err)
		}
		if !allowed {
			if d.emitter != nil {
				d.emitter.emit(ctx, audit.Row{
					ResourceKind: "service",
					ResourceID:   intent.ServiceID,
					ProjectID:    intent.ProjectID,
					Action:       "service.github.deploy.deferred",
					ReasonCode:   "DEPLOY_GATE_PENDING_CHECKS",
					Attrs: map[string]any{
						"reason":          reason,
						"sha":             intent.SHA,
						"installation_id": intent.InstallationID,
					},
				})
			}
			return webhook.TargetResult{}, &webhook.IgnoredTargetError{
				Code: "DEPLOY_GATE_PENDING_CHECKS",
				Err:  errors.New(reason),
			}
		}
	}
	if intent.Preview != nil && intent.Preview.Action == previewActionDelete {
		closed, err := d.previewLifecycle.ClosePullRequest(ctx, intent)
		return webhook.TargetResult{DeploymentID: closed.DeploymentID}, err
	}
	var preparation *PreviewPreparation
	if intent.Preview != nil {
		prepared, err := d.previewLifecycle.PreparePullRequest(ctx, intent)
		if err != nil {
			return webhook.TargetResult{}, err
		}
		preparation = &prepared
	}
	result, err := d.enqueueDeploy(ctx, intent, preparation)
	if errors.Is(err, ErrGitHubBuildSourceAuthorityRevoked) ||
		errors.Is(err, github.ErrSourceLinkNotFound) ||
		errors.Is(err, github.ErrInstallationGrantNotFound) ||
		errors.Is(err, github.ErrRepositoryGrantNotFound) {
		if preparation != nil && intent.Preview != nil {
			closeIntent := intent
			closePreview := *intent.Preview
			closePreview.Action = previewActionDelete
			closePreview.ExistingPreviewID = preparation.PreviewID
			closePreview.ExistingEnvironmentID = preparation.EnvironmentID
			closeIntent.Preview = &closePreview
			if _, closeErr := d.previewLifecycle.ClosePullRequest(ctx, closeIntent); closeErr != nil {
				return webhook.TargetResult{}, errors.Join(err, closeErr)
			}
		}
		return webhook.TargetResult{}, &webhook.IgnoredTargetError{
			Code: "WEBHOOK_TARGET_AUTHORITY_CHANGED",
			Err:  err,
		}
	}
	return result, err
}

func webhookTargetKey(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(idempotencyKey)))
	return fmt.Sprintf("%x", sum[:])
}

// enqueueDeploy creates canonical deployment truth before exposing work to the
// queue. GitHub's deterministic intent key becomes the deployment trigger key;
// the database uniqueness constraint, not an assumed queue behavior, is the
// final concurrency-safe dedupe boundary.
func (d *GitHubWebhookDispatcher) enqueueDeploy(ctx context.Context, intent github.DeployIntent, preview *PreviewPreparation) (webhook.TargetResult, error) {
	if d.queueStore == nil || d.operationStore == nil || d.serviceStore == nil || d.deploymentStore == nil {
		return webhook.TargetResult{}, fmt.Errorf("github dispatcher: store(s) unwired")
	}
	if preview != nil && d.stagedWork == nil {
		return webhook.TargetResult{}, errors.New("github dispatcher: preview deployment requires durable staged intake")
	}
	svc, err := d.serviceStore.GetService(ctx, orailtypes.ID(intent.ServiceID))
	if err != nil {
		return webhook.TargetResult{}, fmt.Errorf("service lookup: %w", err)
	}
	sourceRepo := strings.TrimSpace(intent.CloneURL)
	if sourceRepo == "" {
		sourceRepo = strings.TrimSpace(intent.Repo)
	}
	sourceRef := strings.TrimSpace(intent.SHA)
	if sourceRef == "" {
		sourceRef = strings.TrimSpace(intent.Ref)
	}
	environmentID := ""
	if preview != nil {
		environmentID = preview.EnvironmentID
	} else {
		environmentID, err = d.resolveProductionEnvironment(ctx, svc)
		if err != nil {
			return webhook.TargetResult{}, err
		}
	}
	trustProfile := DeployTrustProfileGitHubTrusted
	if intent.Untrusted {
		trustProfile = DeployTrustProfileGitHubForkPreview
	}
	fetchRef := ""
	if intent.Preview != nil {
		fetchRef = fmt.Sprintf("refs/pull/%d/head", intent.Preview.Number)
	}
	request := DeployIntakeRequest{
		EnvironmentID: environmentID,
		Source: &DeploySourceIntent{
			Provider:            "github",
			Repository:          strings.TrimSpace(intent.Repo),
			WorkspaceID:         strings.TrimSpace(intent.WorkspaceID),
			InstallationID:      intent.InstallationID,
			RepositoryID:        intent.RepositoryID,
			LinkResourceVersion: intent.LinkVersion,
			AuthorityBranch:     strings.TrimSpace(strings.TrimPrefix(intent.Ref, "refs/heads/")),
			TrustProfile:        trustProfile,
			Repo:                sourceRepo,
			Ref:                 sourceRef,
			FetchRef:            fetchRef,
			RootDirectory:       intent.RootDirectory,
			ConfigPath:          intent.ConfigPath,
		},
	}
	request.PreviousActive = resolvePreviousActiveDeployment(ctx, d.deploymentStore, svc.ID, environmentID, "")
	intentKey := githubDeployIdempotencyKey(intent)
	triggerKey := canonicalDeploymentTriggerKey(svc, request, intentKey)
	if d.stagedWork == nil {
		if existing, found, err := findDeploymentByTrigger(ctx, d.deploymentStore, svc.ID, triggerKey); err != nil {
			return webhook.TargetResult{}, fmt.Errorf("deployment dedupe lookup: %w", err)
		} else if found {
			return webhook.TargetResult{
				OperationID:  existing.OperationID,
				DeploymentID: existing.ID,
			}, nil
		}
	}
	enqueuedAt := d.clock().UTC()
	if d.stagedWork != nil {
		op := orailtypes.Operation{ID: defaultOperationID(), Kind: "build", State: orailtypes.OperationQueued}
		canonical, err := canonicalDeploymentRecord(svc, request, op.ID, intentKey)
		if err != nil {
			return webhook.TargetResult{}, fmt.Errorf("prepare canonical deployment: %w", err)
		}
		canonical.ID = mintID(orailtypes.IDPrefixDeployment)
		canonical.SHA = strings.ToLower(strings.TrimSpace(intent.SHA))
		canonical.ExpectedActiveID = previousActiveDeploymentID(request.PreviousActive)
		request.DeploymentID = canonical.ID
		request.InstanceID = canonical.ID + "-r0"
		payloadJSON, err := buildDeployJobPayload(op.ID, svc, request, enqueuedAt)
		if err != nil {
			return webhook.TargetResult{}, fmt.Errorf("encode payload: %w", err)
		}
		previewID := ""
		previewURL := ""
		initialMessage := "GitHub push build intent durably accepted"
		if preview != nil {
			previewID = preview.PreviewID
			previewURL = preview.URL
			initialMessage = "GitHub pull request preview build intent durably accepted"
		}
		result, err := d.stagedWork.CommitGitHubBuildIntake(ctx, GitHubBuildIntakeCommit{
			Operation:  op,
			Deployment: canonical,
			SourceAuthority: GitHubBuildSourceAuthority{
				WorkspaceID:                      intent.WorkspaceID,
				ProjectID:                        intent.ProjectID,
				ServiceID:                        intent.ServiceID,
				InstallationID:                   intent.InstallationID,
				RepositoryID:                     intent.RepositoryID,
				Repository:                       intent.Repo,
				CloneURL:                         intent.CloneURL,
				Branch:                           strings.TrimPrefix(intent.Ref, "refs/heads/"),
				InstallationGrantResourceVersion: intent.InstallationGrantResourceVersion,
				RepositoryGrantResourceVersion:   intent.RepositoryGrantResourceVersion,
				LinkResourceVersion:              intent.LinkVersion,
			},
			Enqueue: queue.EnqueueRequest{
				Queue:             queue.Build,
				PartitionKey:      partitionKey(svc),
				ParentOperationID: string(op.ID),
				AvailableAt:       enqueuedAt,
				Payload:           payloadJSON,
			},
			PreviewID:      previewID,
			PreviewURL:     previewURL,
			InitialMessage: initialMessage,
			Now:            enqueuedAt,
		})
		if err != nil {
			return webhook.TargetResult{}, fmt.Errorf("commit durable GitHub intake: %w", err)
		}
		if !result.Created {
			return webhook.TargetResult{
				OperationID:    result.OperationID,
				DeploymentID:   result.DeploymentID,
				StagedOutboxID: result.OutboxID,
			}, nil
		}
		if d.emitter != nil {
			d.emitter.emit(ctx, audit.Row{
				ResourceKind: "operation",
				ResourceID:   result.OperationID,
				ProjectID:    string(svc.ProjectID),
				Action:       "deploy.intake.github_push",
				After: map[string]any{
					"service_id":      string(svc.ID),
					"project_id":      string(svc.ProjectID),
					"queue":           string(queue.Build),
					"outbox_id":       result.OutboxID,
					"delivery_state":  "pending",
					"operation_id":    result.OperationID,
					"deployment_id":   result.DeploymentID,
					"trigger_key":     canonical.TriggerKey,
					"repo":            sourceRepo,
					"ref":             sourceRef,
					"sha":             intent.SHA,
					"installation_id": intent.InstallationID,
					"root_directory":  intent.RootDirectory,
					"config_path":     intent.ConfigPath,
				},
			})
		}
		return webhook.TargetResult{
			OperationID:    result.OperationID,
			DeploymentID:   result.DeploymentID,
			StagedOutboxID: result.OutboxID,
		}, nil
	}

	op, err := CreateQueuedOperation(ctx, d.operationStore, "build")
	if err != nil {
		return webhook.TargetResult{}, fmt.Errorf("create operation: %w", err)
	}
	canonical, err := createCanonicalDeployment(ctx, d.deploymentStore, svc, request, op.ID, intentKey)
	if err != nil {
		// Close the check/create race by treating an observed canonical row
		// with the same trigger as the winner. No second job is enqueued.
		if winner, found, lookupErr := findDeploymentByTrigger(ctx, d.deploymentStore, svc.ID, triggerKey); lookupErr == nil && found {
			_ = d.cancelDuplicateOperation(ctx, op.ID)
			return webhook.TargetResult{
				OperationID:  winner.OperationID,
				DeploymentID: winner.ID,
			}, nil
		}
		_ = d.failOperation(ctx, op.ID, failurecodes.InternalError, "canonical deployment create failed")
		return webhook.TargetResult{}, fmt.Errorf("create canonical deployment: %w", err)
	}
	request.DeploymentID = canonical.ID
	request.InstanceID = canonical.ID + "-r0"
	payloadJSON, err := buildDeployJobPayload(op.ID, svc, request, enqueuedAt)
	if err != nil {
		transitionErr := d.failIntake(ctx, op.ID, canonical.ID, failurecodes.InternalError, "payload encode failed")
		return webhook.TargetResult{}, errors.Join(fmt.Errorf("encode payload: %w", err), transitionErr)
	}
	job, err := d.queueStore.Enqueue(ctx, queue.EnqueueRequest{
		Queue:             queue.Build,
		PartitionKey:      partitionKey(svc),
		ParentOperationID: string(op.ID),
		AvailableAt:       enqueuedAt,
		Payload:           payloadJSON,
	})
	if err != nil {
		transitionErr := d.failIntake(ctx, op.ID, canonical.ID, failurecodes.DeployJobEnqueueFailed, "build job enqueue failed")
		return webhook.TargetResult{}, errors.Join(fmt.Errorf("enqueue: %w", err), transitionErr)
	}
	if _, err := d.operationStore.AppendEvent(ctx, OperationEvent{OperationID: op.ID, State: op.State, Message: "build job enqueued via github push: job_id=" + job.ID + " operation_id=" + string(op.ID)}); err != nil {
		return webhook.TargetResult{}, fmt.Errorf("append event: %w", err)
	}
	if d.emitter != nil {
		d.emitter.emit(ctx, audit.Row{
			ResourceKind: "operation",
			ResourceID:   string(op.ID),
			ProjectID:    string(svc.ProjectID),
			Action:       "deploy.intake.github_push",
			After: map[string]any{
				"service_id":      string(svc.ID),
				"project_id":      string(svc.ProjectID),
				"queue":           string(queue.Build),
				"job_id":          job.ID,
				"operation_id":    string(op.ID),
				"deployment_id":   canonical.ID,
				"trigger_key":     canonical.TriggerKey,
				"repo":            sourceRepo,
				"ref":             sourceRef,
				"sha":             intent.SHA,
				"installation_id": intent.InstallationID,
				"root_directory":  intent.RootDirectory,
				"config_path":     intent.ConfigPath,
			},
		})
	}
	return webhook.TargetResult{
		OperationID:  string(op.ID),
		DeploymentID: canonical.ID,
	}, nil
}

func (d *GitHubWebhookDispatcher) resolveProductionEnvironment(ctx context.Context, svc Service) (string, error) {
	if d.environmentStore == nil {
		return "", errors.New("github dispatcher: environment store unwired")
	}
	environments, err := d.environmentStore.ListEnvironments(ctx, ResourceListFilter{ParentID: svc.ProjectID})
	if err != nil {
		return "", fmt.Errorf("environment lookup: %w", err)
	}
	for _, environment := range environments {
		if environment.Slug == "production" {
			return string(environment.ID), nil
		}
	}
	return "", fmt.Errorf("project %s has no production environment", svc.ProjectID)
}

func githubDeployIdempotencyKey(intent github.DeployIntent) idempotency.Key {
	if key := strings.TrimSpace(intent.IdempotencyKey); key != "" {
		return idempotency.Key(key)
	}
	raw := strings.Join([]string{
		strings.TrimSpace(intent.ProjectID),
		strings.TrimSpace(intent.ServiceID),
		strings.TrimSpace(intent.Repo),
		strings.TrimSpace(intent.SHA),
		strings.TrimSpace(intent.Ref),
		strings.TrimSpace(intent.RootDirectory),
		strings.TrimSpace(intent.ConfigPath),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return idempotency.Key(fmt.Sprintf("gh-intent-%x", sum[:]))
}

func findDeploymentByTrigger(ctx context.Context, store DeploymentStore, serviceID orailtypes.ID, triggerKey string) (DeploymentRecord, bool, error) {
	records, err := store.ListDeploymentsByService(ctx, string(serviceID))
	if err != nil {
		return DeploymentRecord{}, false, err
	}
	for _, record := range records {
		if record.TriggerKey == triggerKey {
			return record, true, nil
		}
	}
	return DeploymentRecord{}, false, nil
}

func (d *GitHubWebhookDispatcher) cancelDuplicateOperation(ctx context.Context, id orailtypes.ID) error {
	op, err := d.operationStore.Transition(ctx, id, orailtypes.OperationCanceled)
	if err != nil {
		return err
	}
	_, err = d.operationStore.AppendEvent(ctx, OperationEvent{
		OperationID: op.ID,
		State:       op.State,
		Message:     "duplicate GitHub deploy intent suppressed",
	})
	return err
}

func (d *GitHubWebhookDispatcher) failOperation(ctx context.Context, id orailtypes.ID, code failurecodes.Code, message string) error {
	op, err := d.operationStore.Fail(ctx, id, orailtypes.FailureCode(code), message)
	if err != nil {
		return err
	}
	_, err = d.operationStore.AppendEvent(ctx, OperationEvent{OperationID: op.ID, State: op.State, Message: message})
	return err
}

func (d *GitHubWebhookDispatcher) failIntake(ctx context.Context, operationID orailtypes.ID, deploymentID string, code failurecodes.Code, message string) error {
	var errs []error
	if err := d.failOperation(ctx, operationID, code, message); err != nil {
		errs = append(errs, fmt.Errorf("fail operation: %w", err))
	}
	if err := d.deploymentStore.FailDeployment(ctx, deploymentID, deploy.DeploymentQueued, string(code), message); err != nil {
		errs = append(errs, fmt.Errorf("fail canonical deployment: %w", err))
	}
	return errors.Join(errs...)
}

func partitionKey(svc Service) string {
	if svc.ProjectID != "" {
		return string(svc.ProjectID)
	}
	return string(svc.ID)
}

// readGitHubEvent prefers the receiver-supplied EventType (taken from
// the X-GitHub-Event header) and falls back to body sniffing for
// directly-invoked tests.
func readGitHubEvent(del webhook.ReceivedDelivery) (github.EventType, []byte, error) {
	if del.EventType != "" {
		return github.EventType(del.EventType), del.Body, nil
	}
	if eventTypeStr, ok := eventTypeFromBody(del.Body); ok {
		return github.EventType(eventTypeStr), del.Body, nil
	}
	// Unknown event type degrades to "" which Router.Route() handles
	// as Skipped("unknown_event:") — a safe no-op.
	return github.EventType(""), del.Body, nil
}

// eventTypeFromBody parses well-known top-level keys to infer the
// event type when the receiver did not surface the X-GitHub-Event
// header in the ReceivedDelivery payload.
//
// We accept three explicit hints in priority order:
//
//  1. `_midnight_event_type` (synthetic, used in tests)
//  2. `zen` (ping events carry this)
//  3. `commits` + `ref` (push events)
func eventTypeFromBody(body []byte) (string, bool) {
	var raw struct {
		MidnightEventType string `json:"_midnight_event_type"`
		Zen               string `json:"zen"`
		Ref               string `json:"ref"`
		Commits           []any  `json:"commits"`
		Action            string `json:"action"`
		Installation      *struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repositories []any `json:"repositories"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", false
	}
	if raw.MidnightEventType != "" {
		return raw.MidnightEventType, true
	}
	if raw.Zen != "" {
		return string(github.EventPing), true
	}
	if raw.Ref != "" && raw.Commits != nil {
		return string(github.EventPush), true
	}
	if raw.Installation != nil && raw.Action != "" && raw.Repositories != nil {
		return string(github.EventInstallationRepos), true
	}
	if raw.Installation != nil && raw.Action != "" {
		return string(github.EventInstallation), true
	}
	return "", false
}

// serviceStoreLookup adapts a ServiceStore implementing
// ServiceLookupBySpec to github.ServiceLookup.
type serviceStoreLookup struct {
	store ServiceStore
	// fallback search when the store doesn't implement
	// ServiceLookupBySpec — we list & scan. Production callers wire a
	// Postgres ServiceStore that DOES implement the lookup, so this is
	// the in-memory test path.
	mu sync.Mutex
}

func (l *serviceStoreLookup) FindByRepoBranch(
	ctx context.Context,
	installationID,
	repositoryID int64,
	repo,
	branch string,
) ([]github.LinkedService, error) {
	if l == nil || l.store == nil {
		return nil, nil
	}
	// Compatibility fixtures retain JSON service specs, but even this scan
	// requires exact immutable identities. There is no repository-name-only
	// fallback.
	l.mu.Lock()
	defer l.mu.Unlock()
	all, err := l.store.ListServices(ctx, ResourceListFilter{})
	if err != nil {
		return nil, err
	}
	out := make([]github.LinkedService, 0, 4)
	for _, svc := range all {
		link, ok := readGitHubLink(svc)
		if !ok {
			continue
		}
		if link.InstallationID != installationID ||
			link.RepositoryID != repositoryID ||
			!strings.EqualFold(link.Repo, repo) ||
			link.Branch != branch {
			continue
		}
		out = append(out, github.LinkedService{
			ServiceID:           string(svc.ID),
			ProjectID:           string(svc.ProjectID),
			LinkResourceVersion: 1,
			Link:                link,
		})
	}
	return out, nil
}

type canonicalSourceLinkLookup struct {
	store *github.SourceLinkStore
}

func (l canonicalSourceLinkLookup) FindByRepoBranch(
	ctx context.Context,
	installationID,
	repositoryID int64,
	repo,
	branch string,
) ([]github.LinkedService, error) {
	if l.store == nil {
		return nil, nil
	}
	links, err := l.store.FindServiceLinksForPush(
		ctx,
		installationID,
		repositoryID,
		repo,
		branch,
	)
	if err != nil {
		return nil, err
	}
	return durableLinksToLinkedServices(links), nil
}

func (l canonicalSourceLinkLookup) FindByRepoBranchAt(
	ctx context.Context,
	installationID,
	repositoryID int64,
	repo,
	branch string,
	receivedAt time.Time,
) ([]github.LinkedService, error) {
	if l.store == nil {
		return nil, nil
	}
	links, err := l.store.FindServiceLinksForPushAt(
		ctx,
		installationID,
		repositoryID,
		repo,
		branch,
		receivedAt,
	)
	if err != nil {
		return nil, err
	}
	return durableLinksToLinkedServices(links), nil
}

func durableLinksToLinkedServices(links []github.DurableServiceLink) []github.LinkedService {
	out := make([]github.LinkedService, 0, len(links))
	for _, link := range links {
		out = append(out, github.LinkedService{
			ServiceID:                        link.ServiceID,
			ProjectID:                        link.ProjectID,
			WorkspaceID:                      link.WorkspaceID,
			InstallationGrantResourceVersion: link.InstallationGrantResourceVersion,
			InstallationGrantCreatedAt:       link.InstallationGrantCreatedAt,
			InstallationGrantUpdatedAt:       link.InstallationGrantUpdatedAt,
			RepositoryGrantResourceVersion:   link.RepositoryGrantResourceVersion,
			RepositoryGrantCreatedAt:         link.RepositoryGrantCreatedAt,
			RepositoryGrantUpdatedAt:         link.RepositoryGrantUpdatedAt,
			RepositoryAccessVerifiedAt:       link.RepositoryAccessVerifiedAt,
			LinkResourceVersion:              link.ResourceVersion,
			LinkCreatedAt:                    link.CreatedAt,
			LinkUpdatedAt:                    link.UpdatedAt,
			LinkRepositoryAccessVerifiedAt:   link.LinkRepositoryAccessVerifiedAt,
			Link:                             link.Link,
		})
	}
	return out
}
