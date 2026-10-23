// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LinkedService is the subset of a Midnight service the push-router
// consumes. The API layer wires this from its ServiceStore.
type LinkedService struct {
	ServiceID                        string
	ProjectID                        string
	WorkspaceID                      string
	InstallationGrantResourceVersion int64
	InstallationGrantCreatedAt       time.Time
	InstallationGrantUpdatedAt       time.Time
	RepositoryGrantResourceVersion   int64
	RepositoryGrantCreatedAt         time.Time
	RepositoryGrantUpdatedAt         time.Time
	RepositoryAccessVerifiedAt       time.Time
	LinkResourceVersion              int64
	LinkCreatedAt                    time.Time
	LinkUpdatedAt                    time.Time
	LinkRepositoryAccessVerifiedAt   time.Time
	Link                             ServiceLink
}

// ServiceLookup is the boundary the router calls to find every service linked
// to one exact GitHub source identity. InstallationID and RepositoryID are
// stable security boundaries; implementations must not fall back to a
// repository-name-only match.
type ServiceLookup interface {
	FindByRepoBranch(ctx context.Context, installationID, repositoryID int64, repo, branch string) ([]LinkedService, error)
}

// TemporalServiceLookup is required for retained durable deliveries. It only
// returns canonical authority whose grant, verification, and link timestamps
// are no later than the provider delivery's database receipt time.
type TemporalServiceLookup interface {
	FindByRepoBranchAt(
		ctx context.Context,
		installationID,
		repositoryID int64,
		repo,
		branch string,
		receivedAt time.Time,
	) ([]LinkedService, error)
}

// DeployIntent is the panel-locked shape Lane 4 hands the
// deploy-intake path. The api dispatcher converts this into a
// DeployIntakeRequest with `source: {repo, ref, subpath}` and an
// installation_id hint for the build worker.
type DeployIntent struct {
	ServiceID                        string
	ProjectID                        string
	WorkspaceID                      string
	Repo                             string
	RepositoryID                     int64
	CloneURL                         string
	Ref                              string
	SHA                              string
	RootDirectory                    string
	ConfigPath                       string
	InstallationID                   int64
	InstallationGrantResourceVersion int64
	InstallationGrantCreatedAt       time.Time
	InstallationGrantUpdatedAt       time.Time
	RepositoryGrantResourceVersion   int64
	RepositoryGrantCreatedAt         time.Time
	RepositoryGrantUpdatedAt         time.Time
	RepositoryAccessVerifiedAt       time.Time
	LinkVersion                      int64
	LinkCreatedAt                    time.Time
	LinkUpdatedAt                    time.Time
	LinkRepositoryAccessVerifiedAt   time.Time
	Untrusted                        bool
	IdempotencyKey                   string
	Commit                           struct {
		Author    string
		Email     string
		Message   string
		Timestamp string
	}
	WaitForCI      bool
	RequiredChecks []string
	// Preview is present only for a server-planned pull_request lifecycle
	// target. It is part of the immutable webhook target payload; callers can
	// neither manufacture it through generic deploy intake nor mutate it after
	// the retained delivery has been planned.
	Preview *PullRequestPreviewIntent `json:"preview,omitempty"`
	// TriggerReason records why the path filter triggered this deploy
	// (matched_watch_path, no_watch_paths, or truncated_push_fail_open). It is
	// audit metadata only — deploy execution does not branch on it.
	TriggerReason string
}

// PullRequestPreviewIntent is the provider-normalized lifecycle metadata
// carried by one exact service target.
type PullRequestPreviewIntent struct {
	Action                string `json:"action"`
	DeliveryID            string `json:"delivery_id"`
	Number                int    `json:"number"`
	BaseBranch            string `json:"base_branch"`
	HeadRepositoryID      int64  `json:"head_repository_id,omitempty"`
	HeadRepository        string `json:"head_repository,omitempty"`
	HeadBranch            string `json:"head_branch"`
	HeadSHA               string `json:"head_sha"`
	FromFork              bool   `json:"from_fork"`
	SenderLogin           string `json:"sender_login,omitempty"`
	ExistingPreviewID     string `json:"existing_preview_id,omitempty"`
	ExistingEnvironmentID string `json:"existing_environment_id,omitempty"`
}

// RouteDecision is the full structured outcome of routing one push.
type RouteDecision struct {
	// Skipped is true when the event was understood but produced no
	// deploys (e.g. push to a non-linked branch, no path-match).
	Skipped bool
	// SkipReason describes why we skipped (for audit + observability).
	SkipReason string
	// Intents is the list of services to deploy. Multiple linked
	// services to the same (repo, branch) all deploy.
	Intents []DeployIntent
}

// ErrEventUnhandled is returned by Route for event types that are
// recognised but produce no deploy intents (ping, installation,
// installation_repositories, pull_request, etc.). The receiver should
// 200 these.
var ErrEventUnhandled = errors.New("github: event has no deploy effect")

// ErrWebhookReceiptTimeRequired prevents durable webhook routing from falling
// back to current authority when the database receipt timestamp is absent.
var ErrWebhookReceiptTimeRequired = errors.New("github: webhook receipt time required")

// Router maps `(eventType, body)` to zero or more DeployIntents.
type Router struct {
	// Lookup is kept as any during the source-link storage transition so older
	// API adapters still compile. routePush requires the exact ServiceLookup
	// contract above at runtime and fails closed when an old name-only adapter
	// is wired.
	Lookup any
}

// Route is the panel-locked decision tree:
//
//  1. ping -> Skipped=true, reason="ping"
//  2. installation, installation_repositories, pull_request,
//     check_run, check_suite, workflow_run -> Skipped=true,
//     reason="event_not_routed"
//  3. push -> parse, reject untrusted sources, lookup services by exact
//     (installation, repository, repo, branch), filter by watch_paths, emit one
//     DeployIntent per matching service.
//  4. unknown event types -> Skipped=true, reason="unknown_event"
//
// The router returns nil error for every recognised event; callers MUST
// surface 200 OK even when Skipped=true so GitHub doesn't disable the
// webhook delivery channel.
func (r Router) Route(ctx context.Context, eventType EventType, body []byte) (RouteDecision, error) {
	return r.route(ctx, eventType, body, time.Time{})
}

// RouteAt routes a retained delivery against authority that existed no later
// than receivedAt. It fails closed when only a non-temporal lookup is wired.
func (r Router) RouteAt(
	ctx context.Context,
	eventType EventType,
	body []byte,
	receivedAt time.Time,
) (RouteDecision, error) {
	if receivedAt.IsZero() {
		return RouteDecision{}, ErrWebhookReceiptTimeRequired
	}
	return r.route(ctx, eventType, body, receivedAt.UTC())
}

func (r Router) route(
	ctx context.Context,
	eventType EventType,
	body []byte,
	receivedAt time.Time,
) (RouteDecision, error) {
	switch eventType {
	case EventPing:
		return RouteDecision{Skipped: true, SkipReason: "ping"}, nil
	case EventInstallation, EventInstallationRepos, EventPullRequest,
		EventCheckRun, EventCheckSuite, EventWorkflowRun,
		EventInstallationTargetEvent:
		return RouteDecision{Skipped: true, SkipReason: "event_not_routed:" + string(eventType)}, nil
	case EventPush:
		return r.routePush(ctx, body, receivedAt)
	default:
		return RouteDecision{Skipped: true, SkipReason: "unknown_event:" + string(eventType)}, nil
	}
}

func (r Router) routePush(ctx context.Context, body []byte, receivedAt time.Time) (RouteDecision, error) {
	ev, err := ParsePushEvent(body)
	if err != nil {
		return RouteDecision{}, err
	}
	if ev.Deleted {
		return RouteDecision{Skipped: true, SkipReason: "branch_deleted"}, nil
	}
	// Until untrusted-build isolation is implemented end to end, accepting a
	// fork push would expose the normal deploy path (including project secrets
	// and shared caches). Fail closed before repository lookup or deploy intake.
	if ev.Untrusted {
		return RouteDecision{Skipped: true, SkipReason: "untrusted_push_unsupported"}, nil
	}
	if r.Lookup == nil {
		return RouteDecision{Skipped: true, SkipReason: "no_service_lookup_wired"}, nil
	}
	var linked []LinkedService
	if receivedAt.IsZero() {
		lookup, ok := r.Lookup.(ServiceLookup)
		if !ok {
			return RouteDecision{Skipped: true, SkipReason: "exact_service_lookup_required"}, nil
		}
		linked, err = lookup.FindByRepoBranch(
			ctx,
			ev.InstallationID,
			ev.Repository.ID,
			ev.Repository.FullName,
			ev.Branch,
		)
	} else {
		lookup, ok := r.Lookup.(TemporalServiceLookup)
		if !ok {
			return RouteDecision{Skipped: true, SkipReason: "temporal_service_lookup_required"}, nil
		}
		linked, err = lookup.FindByRepoBranchAt(
			ctx,
			ev.InstallationID,
			ev.Repository.ID,
			ev.Repository.FullName,
			ev.Branch,
			receivedAt,
		)
	}
	if err != nil {
		return RouteDecision{}, err
	}
	if len(linked) == 0 {
		return RouteDecision{Skipped: true, SkipReason: "no_linked_service:" + ev.Repository.FullName + "@" + ev.Branch}, nil
	}
	commits, _ := ParsePushEventCommits(body)
	cloneURL := PushEventCloneURL(body)
	intents := make([]DeployIntent, 0, len(linked))
	identityMatched := false
	for _, svc := range linked {
		// Treat lookup results as untrusted storage output. An implementation
		// bug must not let a signed event for one installation/repository deploy
		// a service linked to another.
		if svc.Link.InstallationID != ev.InstallationID ||
			svc.Link.RepositoryID != ev.Repository.ID ||
			!strings.EqualFold(svc.Link.Repo, ev.Repository.FullName) ||
			svc.Link.Branch != ev.Branch {
			continue
		}
		identityMatched = true
		decision := EvaluateWatchPaths(svc.Link.WatchPaths, commits)
		if !decision.Matched {
			continue
		}
		intent := DeployIntent{
			ServiceID:                        svc.ServiceID,
			ProjectID:                        svc.ProjectID,
			WorkspaceID:                      svc.WorkspaceID,
			Repo:                             ev.Repository.FullName,
			RepositoryID:                     ev.Repository.ID,
			Ref:                              ev.Ref,
			SHA:                              ev.HeadSHA,
			RootDirectory:                    svc.Link.RootDirectory,
			ConfigPath:                       svc.Link.ConfigPath,
			TriggerReason:                    string(decision.Reason),
			InstallationID:                   ev.InstallationID,
			InstallationGrantResourceVersion: svc.InstallationGrantResourceVersion,
			InstallationGrantCreatedAt:       svc.InstallationGrantCreatedAt,
			InstallationGrantUpdatedAt:       svc.InstallationGrantUpdatedAt,
			RepositoryGrantResourceVersion:   svc.RepositoryGrantResourceVersion,
			RepositoryGrantCreatedAt:         svc.RepositoryGrantCreatedAt,
			RepositoryGrantUpdatedAt:         svc.RepositoryGrantUpdatedAt,
			RepositoryAccessVerifiedAt:       svc.RepositoryAccessVerifiedAt,
			LinkVersion:                      svc.LinkResourceVersion,
			LinkCreatedAt:                    svc.LinkCreatedAt,
			LinkUpdatedAt:                    svc.LinkUpdatedAt,
			LinkRepositoryAccessVerifiedAt:   svc.LinkRepositoryAccessVerifiedAt,
			Untrusted:                        ev.Untrusted,
			IdempotencyKey: fmt.Sprintf(
				"gh-push-%d-%d-v%d-%s-%s",
				ev.InstallationID,
				ev.Repository.ID,
				svc.LinkResourceVersion,
				ev.HeadSHA,
				svc.ServiceID,
			),
			CloneURL:       firstNonEmpty(svc.Link.CloneURL, cloneURL, CloneURLForRepo(ev.Repository.FullName)),
			WaitForCI:      svc.Link.WaitForCI,
			RequiredChecks: append([]string(nil), svc.Link.RequiredChecks...),
		}
		intent.Commit.Author = ev.Pusher.Login
		intent.Commit.Email = ev.Pusher.Email
		intent.Commit.Message = ev.HeadCommit.Message
		if !ev.HeadCommit.Timestamp.IsZero() {
			intent.Commit.Timestamp = ev.HeadCommit.Timestamp.UTC().Format("2006-01-02T15:04:05Z")
		}
		intents = append(intents, intent)
	}
	if len(intents) == 0 {
		if !identityMatched {
			return RouteDecision{Skipped: true, SkipReason: "source_identity_mismatch"}, nil
		}
		return RouteDecision{Skipped: true, SkipReason: "watch_paths_no_match"}, nil
	}
	return RouteDecision{Intents: intents}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
