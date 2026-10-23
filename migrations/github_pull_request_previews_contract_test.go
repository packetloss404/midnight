package migrations

import (
	"strings"
	"testing"
)

func TestGitHubPullRequestPreviewsMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0065_github_pull_request_previews.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ToLower(string(upBytes))
	for _, required := range []string{
		"create table github_pull_request_previews",
		"unique (installation_id, repository_id, pull_request_number, service_id)",
		"foreign key (project_id, workspace_id)",
		"foreign key (deployment_id, project_id, service_id, environment_id)",
		"from_fork and secret_policy = 'no-shared-secrets'",
		"not from_fork and secret_policy = 'non-sealed'",
		"cache_policy = 'ephemeral'",
		"expires_at <= created_at + interval '30 days'",
		"create table github_pull_request_preview_comments",
		"create trigger trg_enqueue_preview_deployment_comment",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0065 up migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"access_token", "private_key", "webhook_secret", "value_ciphertext"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("0065 must not persist %q", forbidden)
		}
	}
	downBytes, err := FS.ReadFile("0065_github_pull_request_previews.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := strings.ToLower(string(downBytes))
	if !strings.Contains(down, "where state <> 'deleted'") {
		t.Fatal("0065 down migration must refuse while a preview lifecycle is active")
	}
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 65\n    name: github_pull_request_previews") {
		t.Fatal("manifest does not register github_pull_request_previews as migration 65")
	}
}
