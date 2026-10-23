package migrations

import (
	"strings"
	"testing"
)

func TestGitHubAppSetupStatesMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0064_github_app_setup_states.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ToLower(string(upBytes))
	for _, required := range []string{
		"create table github_app_setup_states",
		"nonce_sha256 bytea primary key",
		"references workspaces",
		"references users",
		"references auth_sessions",
		"expires_at <= issued_at + interval '1 hour'",
		"consumed_at is null and installation_id is null",
		"consumed_at is not null",
		"installation_id > 0",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0064 up migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"access_token", "client_secret", "oauth_code", "private_key", "signed_state text"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("0064 must not persist %q", forbidden)
		}
	}
	downBytes, err := FS.ReadFile("0064_github_app_setup_states.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := strings.ToLower(string(downBytes))
	if !strings.Contains(down, "where consumed_at is null") || !strings.Contains(down, "expires_at > current_timestamp") {
		t.Fatal("0064 down migration must refuse while a live setup is pending")
	}
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 64\n    name: github_app_setup_states") {
		t.Fatal("manifest does not register github_app_setup_states as migration 64")
	}
}
