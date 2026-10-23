package previews

import (
	"strings"
	"testing"
	"time"
)

func TestPlanPullRequestPreviewForSameRepoGitHub(t *testing.T) {
	plan, err := PlanPullRequestPreview(validPullRequestRef(ProviderGitHub), time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PlanPullRequestPreview: %v", err)
	}
	if plan.Action != PreviewCreate || plan.SecretPolicy != "non-sealed" || plan.CachePolicy != "service-preview" || plan.QuotaProfile != "preview-default" {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.EnvironmentName != "preview/pr-42" || plan.URL != "https://pr-42--checkout-api.previews.example.com" {
		t.Fatalf("environment/url = %q %q", plan.EnvironmentName, plan.URL)
	}
	if strings.Contains(plan.DeploymentKey, "github.com") {
		t.Fatalf("deployment key should use provider-neutral repository id, got %q", plan.DeploymentKey)
	}
}

func TestPlanPullRequestPreviewRestrictsForks(t *testing.T) {
	ref := validPullRequestRef(ProviderGitLab)
	ref.FromFork = true
	plan, err := PlanPullRequestPreview(ref, time.Time{})
	if err != nil {
		t.Fatalf("PlanPullRequestPreview: %v", err)
	}
	if plan.SecretPolicy != "no-shared-secrets" || plan.CachePolicy != "ephemeral" || plan.QuotaProfile != "preview-fork-restricted" {
		t.Fatalf("fork plan = %#v", plan)
	}
	if containsString(plan.RequiredChecks, "maintainer-approval") {
		t.Fatalf("runtime does not require a manual approval check: %#v", plan.RequiredChecks)
	}
}

func TestPlanPullRequestPreviewArchivesClosedPR(t *testing.T) {
	ref := validPullRequestRef(ProviderGitHub)
	ref.State = PullRequestClosed
	ref.TTL = 2 * time.Hour
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	plan, err := PlanPullRequestPreview(ref, now)
	if err != nil {
		t.Fatalf("PlanPullRequestPreview: %v", err)
	}
	if plan.Action != PreviewArchive || plan.SecretPolicy != "no-secrets" || !plan.ArchiveAfter.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("archive plan = %#v", plan)
	}
}

func TestPullRequestRefRejectsProviderSpecificCallbackURL(t *testing.T) {
	ref := validPullRequestRef(ProviderGitHub)
	ref.PreviewBaseHost = "https://previews.example.com/path"
	if _, err := PlanPullRequestPreview(ref, time.Time{}); err == nil {
		t.Fatal("expected preview_base_host with scheme/path to be rejected")
	}
}

func validPullRequestRef(provider Provider) PullRequestRef {
	return PullRequestRef{
		Provider:        provider,
		WorkspaceID:     "wrk_acme",
		Repository:      "acme/checkout",
		Number:          42,
		State:           PullRequestOpened,
		HeadSHA:         "abc123",
		BaseBranch:      "main",
		HeadBranch:      "feature/cart",
		PreviewBaseHost: "previews.example.com",
		ServiceName:     "Checkout API",
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
