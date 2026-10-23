// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestSourceArtifactsMigrationContract(t *testing.T) {
	upBytes, err := fs.ReadFile(FS, "0045_source_artifacts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"CREATE TABLE source_artifacts",
		"digest TEXT PRIMARY KEY",
		"^sha256:[0-9a-f]{64}$",
		"storage_key TEXT NOT NULL UNIQUE",
		"substring(digest FROM 8 FOR 2)",
		"substring(digest FROM 8)",
		"ADD CONSTRAINT deployments_source_artifact_lineage_uq",
		"UNIQUE (id, operation_id, project_id, service_id, environment_id)",
		"CREATE TABLE source_artifact_refs",
		"operation_id TEXT PRIMARY KEY",
		"deployment_id TEXT NOT NULL UNIQUE",
		"CONSTRAINT source_artifact_refs_operation_lifecycle_fk",
		"CONSTRAINT source_artifact_refs_deployment_lineage_fk",
		"deployment_id,\n            operation_id,\n            project_id,\n            service_id,\n            environment_id",
		"REFERENCES deployments",
		"DEFERRABLE INITIALLY DEFERRED",
		"REFERENCES source_artifacts(digest) ON DELETE RESTRICT",
		"message !~ '[[:cntrl:]]'",
		"octet_length(message) <= 256",
		"WHERE released_at IS NULL",
		"VALUES (45, 'source_artifacts'",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("up migration missing %q", required)
		}
	}

	downBytes, err := fs.ReadFile(FS, "0045_source_artifacts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	refs := strings.Index(down, "DROP TABLE IF EXISTS source_artifact_refs")
	artifacts := strings.Index(down, "DROP TABLE IF EXISTS source_artifacts")
	lineage := strings.Index(down, "DROP CONSTRAINT IF EXISTS deployments_source_artifact_lineage_uq")
	if refs < 0 || artifacts < 0 || refs >= artifacts ||
		lineage < 0 || lineage <= refs ||
		!strings.Contains(down, "WHERE version = 45") {
		t.Fatal("down migration must remove references before artifact metadata and deployment lineage constraint")
	}

	manifestBytes, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 45\n    name: source_artifacts") {
		t.Fatal("migration 0045 is not registered in the current schema")
	}
}
