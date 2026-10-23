package previews

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Provider string

const (
	ProviderGitHub Provider = "github"
	ProviderGitLab Provider = "gitlab"
)

type PullRequestState string

const (
	PullRequestOpened  PullRequestState = "opened"
	PullRequestUpdated PullRequestState = "updated"
	PullRequestClosed  PullRequestState = "closed"
	PullRequestMerged  PullRequestState = "merged"
)

type PreviewAction string

const (
	PreviewCreate  PreviewAction = "create"
	PreviewUpdate  PreviewAction = "update"
	PreviewArchive PreviewAction = "archive"
)

type PullRequestRef struct {
	Provider        Provider         `json:"provider"`
	WorkspaceID     string           `json:"workspace_id"`
	Repository      string           `json:"repository"`
	Number          int              `json:"number"`
	State           PullRequestState `json:"state"`
	HeadSHA         string           `json:"head_sha"`
	BaseBranch      string           `json:"base_branch"`
	HeadBranch      string           `json:"head_branch"`
	FromFork        bool             `json:"from_fork"`
	PreviewBaseHost string           `json:"preview_base_host,omitempty"`
	ServiceName     string           `json:"service_name"`
	TTL             time.Duration    `json:"ttl,omitempty"`
}

type PreviewPlan struct {
	Provider          Provider      `json:"provider"`
	WorkspaceID       string        `json:"workspace_id"`
	EnvironmentName   string        `json:"environment_name"`
	Action            PreviewAction `json:"action"`
	SourceRef         string        `json:"source_ref"`
	DeploymentKey     string        `json:"deployment_key"`
	URL               string        `json:"url,omitempty"`
	SecretPolicy      string        `json:"secret_policy"`
	CachePolicy       string        `json:"cache_policy"`
	QuotaProfile      string        `json:"quota_profile"`
	ArchiveAfter      time.Time     `json:"archive_after,omitempty"`
	RequiredChecks    []string      `json:"required_checks"`
	NotificationHints []string      `json:"notification_hints"`
}

func PlanPullRequestPreview(ref PullRequestRef, now time.Time) (PreviewPlan, error) {
	if err := ref.Validate(); err != nil {
		return PreviewPlan{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	action := actionForState(ref.State)
	plan := PreviewPlan{
		Provider:        ref.Provider,
		WorkspaceID:     strings.TrimSpace(ref.WorkspaceID),
		EnvironmentName: fmt.Sprintf("preview/pr-%d", ref.Number),
		Action:          action,
		SourceRef:       strings.TrimSpace(ref.HeadSHA),
		DeploymentKey:   fmt.Sprintf("%s:%s:%d:%s", ref.Provider, strings.TrimSpace(ref.Repository), ref.Number, strings.TrimSpace(ref.HeadSHA)),
		SecretPolicy:    "non-sealed",
		CachePolicy:     "service-preview",
		QuotaProfile:    "preview-default",
		RequiredChecks:  []string{"build", "preview-plan"},
	}
	if ref.FromFork {
		plan.SecretPolicy = "no-shared-secrets"
		plan.CachePolicy = "ephemeral"
		plan.QuotaProfile = "preview-fork-restricted"
	}
	if action == PreviewArchive {
		plan.SecretPolicy = "no-secrets"
		plan.CachePolicy = "none"
		plan.NotificationHints = []string{"preview.archived"}
		if ref.TTL > 0 {
			plan.ArchiveAfter = now.UTC().Add(ref.TTL).UTC()
		}
	} else {
		plan.NotificationHints = []string{"preview.created"}
	}
	if ref.PreviewBaseHost != "" {
		plan.URL = fmt.Sprintf("https://pr-%d--%s.%s", ref.Number, slug(ref.ServiceName), strings.TrimSpace(ref.PreviewBaseHost))
	}
	sort.Strings(plan.RequiredChecks)
	return plan, nil
}

func (ref PullRequestRef) Validate() error {
	if ref.Provider != ProviderGitHub && ref.Provider != ProviderGitLab {
		return fmt.Errorf("preview provider %q is not supported", ref.Provider)
	}
	if strings.TrimSpace(ref.WorkspaceID) == "" || strings.TrimSpace(ref.Repository) == "" {
		return errors.New("workspace_id and repository are required")
	}
	if ref.Number <= 0 {
		return errors.New("pull request number must be positive")
	}
	if actionForState(ref.State) == "" {
		return fmt.Errorf("pull request state %q is not supported", ref.State)
	}
	if strings.TrimSpace(ref.HeadSHA) == "" || strings.TrimSpace(ref.BaseBranch) == "" || strings.TrimSpace(ref.HeadBranch) == "" {
		return errors.New("head_sha, base_branch, and head_branch are required")
	}
	if strings.TrimSpace(ref.ServiceName) == "" {
		return errors.New("service_name is required")
	}
	if ref.PreviewBaseHost != "" {
		parsed, err := url.Parse("https://" + strings.TrimSpace(ref.PreviewBaseHost))
		if err != nil || parsed.Host != strings.TrimSpace(ref.PreviewBaseHost) || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("preview_base_host must be a host name without scheme, path, query, or fragment")
		}
	}
	return nil
}

func actionForState(state PullRequestState) PreviewAction {
	switch state {
	case PullRequestOpened:
		return PreviewCreate
	case PullRequestUpdated:
		return PreviewUpdate
	case PullRequestClosed, PullRequestMerged:
		return PreviewArchive
	default:
		return ""
	}
}

var nonSlug = regexp.MustCompile(`[^a-z0-9-]+`)

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonSlug.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "service"
	}
	return value
}
