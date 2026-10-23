package migrations

import (
	"strings"
	"testing"
)

func TestChangesetApplyDeployGroupsMigrationContract(t *testing.T) {
	up, err := FS.ReadFile("0070_changeset_apply_deploy_groups.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(up)
	for _, required := range []string{
		"applied_configuration_revision",
		"configuration_revision BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE deployment_config_snapshots",
		"CREATE TABLE environment_configuration_revisions",
		"CREATE TABLE service_configuration_revisions",
		"CREATE TABLE environment_operation_groups",
		"CREATE TABLE environment_operation_group_items",
		"deployment_action TEXT NOT NULL CHECK (deployment_action IN ('none', 'redeploy', 'rebuild'))",
		"record_deployed_configuration_revision",
		"deployments_configuration_revision_active_trg",
		"deployed_revision = EXCLUDED.deployed_revision",
		"deployed_deployment_id = EXCLUDED.deployed_deployment_id",
		"SET deployed_revision = 0",
		"pending_deployment_id = NULL",
		"NEW.state IN ('failed', 'canceled')",
		"AFTER UPDATE OF state, active, configuration_revision ON deployments",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	if strings.Contains(sql, "deployed_revision = GREATEST") {
		t.Fatal("deployed revision must represent the exact active deployment, not a monotonic maximum")
	}
}
