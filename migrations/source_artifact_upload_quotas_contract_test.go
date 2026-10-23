// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"strings"
	"testing"
)

func TestSourceArtifactUploadQuotasMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0046_source_artifact_upload_quotas.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, fragment := range []string{
		"CREATE TABLE source_artifact_workspace_quota_state",
		"CREATE TABLE source_artifact_workspace_charges",
		"CREATE TABLE source_artifact_upload_reservations",
		"UNIQUE (id, user_id)",
		"FOREIGN KEY (project_id, workspace_id)",
		"FOREIGN KEY (service_id, project_id)",
		"FOREIGN KEY (environment_id, project_id)",
		"FOREIGN KEY (session_id, actor_user_id)",
		"REFERENCES projects(id, workspace_id) ON DELETE RESTRICT",
		"REFERENCES services(id, project_id) ON DELETE RESTRICT",
		"REFERENCES environments(id, project_id) ON DELETE RESTRICT",
		"REFERENCES auth_sessions(id, user_id) ON DELETE RESTRICT",
		"lease_expires_at TIMESTAMPTZ NOT NULL",
		"charge_expires_at TIMESTAMPTZ NOT NULL",
		"published_at TIMESTAMPTZ",
		"UNIQUE (actor_user_id, project_id, service_id, idempotency_key)",
		"AND (published_at IS NULL OR published_at >= created_at)",
		"source_artifact_upload_reservations_workspace_lease_idx",
		"source_artifact_workspace_charges_digest_idx",
		"source_artifact_workspace_charges_reconcile_idx",
		"source_artifact_refs_project_digest_idx",
		"source_artifact_refs_digest_idx",
		"INSERT INTO source_artifact_workspace_charges",
		"GROUP BY project.workspace_id,artifact_ref.artifact_digest",
		"VALUES (46, 'source_artifact_upload_quotas'",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("up migration missing %q", fragment)
		}
	}

	downBytes, err := FS.ReadFile("0046_source_artifact_upload_quotas.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "DROP TABLE IF EXISTS source_artifact_upload_reservations") ||
		!strings.Contains(down, "DROP TABLE IF EXISTS source_artifact_workspace_charges") ||
		!strings.Contains(down, "DROP TABLE IF EXISTS source_artifact_workspace_quota_state") ||
		!strings.Contains(down, "DROP INDEX IF EXISTS source_artifact_refs_project_digest_idx") ||
		!strings.Contains(down, "DROP INDEX IF EXISTS source_artifact_refs_digest_idx") ||
		!strings.Contains(down, "DROP CONSTRAINT IF EXISTS auth_sessions_id_user_uniq") {
		t.Fatal("down migration does not remove the quota contract")
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 46\n    name: source_artifact_upload_quotas") {
		t.Fatal("manifest does not register migration 46")
	}
}
