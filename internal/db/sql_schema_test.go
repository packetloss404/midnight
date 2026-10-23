// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package db

import (
	"testing"
)

func TestParseSQLSubsetExtractsTablesIndexesAndPartitions(t *testing.T) {
	sql := `
CREATE TABLE IF NOT EXISTS operations (
    id TEXT PRIMARY KEY,
    state TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    queue TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (id, queue)
) PARTITION BY RANGE (created_at);

CREATE TABLE jobs_2026_04 PARTITION OF jobs FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');

CREATE INDEX IF NOT EXISTS idx_jobs_queue_state ON jobs(queue, state);
CREATE UNIQUE INDEX idx_operations_id ON operations(id);
`

	tables, indexes, partitions, unsupported, invalid := ParseSQLSubset(sql)
	if unsupported != 0 || invalid != 0 {
		t.Fatalf("unsupported = %d, invalid = %d", unsupported, invalid)
	}
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want 2 (%v)", len(tables), tables)
	}
	if len(indexes) != 2 {
		t.Fatalf("indexes = %d, want 2 (%v)", len(indexes), indexes)
	}
	if len(partitions) != 1 || partitions[0].Parent != "jobs" || partitions[0].Name != "jobs_2026_04" {
		t.Fatalf("partitions = %#v, want one jobs partition", partitions)
	}

	var jobs *TableEvidence
	for i := range tables {
		if tables[i].Name == "jobs" {
			jobs = &tables[i]
		}
	}
	if jobs == nil {
		t.Fatal("missing jobs table evidence")
	}
	if !jobs.Partitioned || jobs.PartitionBy == "" {
		t.Fatalf("jobs partition info = %v, want partitioned", jobs)
	}
	if len(jobs.Columns) < 4 {
		t.Fatalf("jobs columns = %v, want at least 4", jobs.Columns)
	}

	var unique *IndexEvidence
	for i := range indexes {
		if indexes[i].Unique {
			unique = &indexes[i]
		}
	}
	if unique == nil || unique.Table != "operations" {
		t.Fatalf("expected unique index on operations, got %v", indexes)
	}
}

func TestSchemaEvidenceCountsRichObjects(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 1,
		Migrations:    []Migration{{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"}},
	}
	report, err := BuildSchemaEvidence(manifest, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	report.AddSQLSubsetEvidence(`
CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY, state TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_operations_state ON operations(state);
`)
	if report.DetectedTableCount != 1 || report.DetectedIndexCount != 1 || report.DetectedColumnCount != 2 {
		t.Fatalf("counts = %+v", report)
	}
	if report.UnsupportedStatementCount != 0 || report.InvalidStatementCount != 0 {
		t.Fatalf("unsupported/invalid = %d/%d", report.UnsupportedStatementCount, report.InvalidStatementCount)
	}
}

func TestSchemaEvidenceFromGoldenManifest(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 1,
		Migrations: []Migration{{
			Version: 1,
			Name:    "alpha_core",
			Up:      "0001_alpha_core.up.sql",
			Down:    "0001_alpha_core.down.sql",
		}},
	}
	report, err := BuildSchemaEvidence(manifest, []int{1})
	if err != nil {
		t.Fatal(err)
	}

	golden := `
CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY, state TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, queue TEXT NOT NULL, payload JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS hosts (id TEXT PRIMARY KEY, hostname TEXT NOT NULL, labels JSONB NOT NULL);
CREATE INDEX IF NOT EXISTS idx_jobs_queue_state ON jobs(queue, state);
CREATE INDEX IF NOT EXISTS idx_hosts_hostname ON hosts(hostname);
`
	report.AddSQLSubsetEvidence(golden)
	if report.DetectedTableCount != 3 {
		t.Fatalf("detected_table_count = %d, want 3", report.DetectedTableCount)
	}
	if report.DetectedIndexCount != 2 {
		t.Fatalf("detected_index_count = %d, want 2", report.DetectedIndexCount)
	}
	if report.DetectedColumnCount != 8 {
		t.Fatalf("detected_column_count = %d, want 8", report.DetectedColumnCount)
	}
}
