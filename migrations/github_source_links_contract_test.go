package migrations

import (
	"strings"
	"testing"
)

func TestGitHubSourceLinksMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0049_github_source_links.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"CREATE TABLE github_workspace_installation_grants",
		"UNIQUE (installation_id)",
		"CREATE TABLE github_workspace_repository_grants",
		"PRIMARY KEY (workspace_id, installation_id, repository_id)",
		"UNIQUE (repository_id)",
		"github_workspace_repository_grants_installation_fk",
		"repository_access_verified_at TIMESTAMPTZ NOT NULL",
		"CREATE TABLE service_github_links",
		"service_github_links_service_scope_fk",
		"service_github_links_project_scope_fk",
		"service_github_links_repository_grant_fk",
		"wait_for_ci BOOLEAN NOT NULL",
		"required_checks TEXT[] NOT NULL",
		"service_github_links_webhook_lookup_idx",
		"CREATE TABLE github_source_link_quarantine",
		"legacy_source_link_requires_reverification",
		"service.spec->'github'",
		"SET spec = spec - 'github'",
		"DROP INDEX IF EXISTS idx_services_github_jsonb",
		"DROP INDEX IF EXISTS idx_services_github_repo_branch",
		"VALUES (49, 'github_source_links'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0049 up migration missing %q", required)
		}
	}

	downBytes, err := FS.ReadFile("0049_github_source_links.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"DROP TABLE IF EXISTS service_github_links",
		"DROP TABLE IF EXISTS github_workspace_repository_grants",
		"DROP TABLE IF EXISTS github_workspace_installation_grants",
		"DROP TABLE IF EXISTS github_source_link_quarantine",
		"CREATE INDEX IF NOT EXISTS idx_services_github_repo_branch",
		"CREATE INDEX IF NOT EXISTS idx_services_github_jsonb",
		"version = 49",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("0049 down migration missing %q", required)
		}
	}
	legacyRestore := strings.Index(down, "FROM github_source_link_quarantine quarantine")
	canonicalRestore := strings.Index(down, "FROM service_github_links link")
	serviceDrop := strings.Index(down, "DROP TABLE IF EXISTS service_github_links")
	repositoryDrop := strings.Index(down, "DROP TABLE IF EXISTS github_workspace_repository_grants")
	installationDrop := strings.Index(down, "DROP TABLE IF EXISTS github_workspace_installation_grants")
	quarantineDrop := strings.Index(down, "DROP TABLE IF EXISTS github_source_link_quarantine")
	if legacyRestore < 0 || canonicalRestore < 0 || serviceDrop < 0 ||
		repositoryDrop < 0 || installationDrop < 0 || quarantineDrop < 0 ||
		legacyRestore >= canonicalRestore ||
		canonicalRestore >= serviceDrop ||
		serviceDrop >= repositoryDrop ||
		repositoryDrop >= installationDrop ||
		installationDrop >= quarantineDrop {
		t.Fatal("0049 down migration must restore legacy then canonical truth before dropping children, grants, installation, and quarantine")
	}
}

func TestGitHubSourceLinksMigrationIsLatest(t *testing.T) {
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 49\n    name: github_source_links") {
		t.Fatal("manifest does not register github_source_links before the current schema version")
	}
}
